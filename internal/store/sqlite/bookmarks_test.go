package sqlite

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func TestBookmarks_TagsForDocument(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	bms := NewBookmarks(db)

	d1 := &store.Document{TenantID: "local", URL: "https://x/a", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Create(ctx, d1))
	d2 := &store.Document{TenantID: "local", URL: "https://x/b", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Create(ctx, d2))

	// Two bookmarks (distinct sources) for d1 with overlapping tags.
	require.NoError(t, bms.Create(ctx, &store.Bookmark{
		TenantID: "local", DocumentID: &d1.ID, URL: d1.URL, Source: store.SourceChrome,
		SavedAt: time.Now().UTC(), Tags: []string{"go", "db"},
	}))
	require.NoError(t, bms.Create(ctx, &store.Bookmark{
		TenantID: "local", DocumentID: &d1.ID, URL: d1.URL, Source: store.SourceManual,
		SavedAt: time.Now().UTC(), Tags: []string{"db", "sql"},
	}))
	// A different doc's tags must not leak in.
	require.NoError(t, bms.Create(ctx, &store.Bookmark{
		TenantID: "local", DocumentID: &d2.ID, URL: d2.URL, Source: store.SourceChrome,
		SavedAt: time.Now().UTC(), Tags: []string{"other"},
	}))

	tags, err := bms.TagsForDocument(ctx, "local", d1.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"go", "db", "sql"}, tags, "union, deduplicated, scoped to the doc")

	// Different tenant sees nothing.
	none, err := bms.TagsForDocument(ctx, "other-tenant", d1.ID)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestBookmarks_ListByDocument: a document's bookmarks, the tenant's only,
// newest saved first; one whose document was deleted links to none.
func TestBookmarks_ListByDocument(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms := NewBookmarks(db)
	saved := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	ingest := func(url, source string, savedAt time.Time) *store.Bookmark {
		t.Helper()
		b := newIngestBookmark(url, source)
		b.SavedAt = savedAt
		_, err := bms.Ingest(ctx, b)
		require.NoError(t, err)
		return b
	}
	oldest := ingest("https://example.com/a", store.SourceChrome, saved)
	newest := ingest("https://example.com/a", store.SourceSafari, saved.Add(2*time.Hour))
	middle := ingest("https://example.com/a", store.SourceFirefox, saved.Add(time.Hour))
	ingest("https://example.com/other", store.SourceChrome, saved)
	docID := *oldest.DocumentID
	require.NoError(t, bms.Create(ctx, &store.Bookmark{TenantID: "other-tenant", DocumentID: &docID,
		URL: oldest.URL, Source: store.SourceChrome, SavedAt: saved.Add(3 * time.Hour)}))

	got, err := bms.ListByDocument(ctx, "local", docID)
	require.NoError(t, err)
	ids := make([]string, 0, len(got))
	for _, b := range got {
		ids = append(ids, b.ID)
		assert.Equal(t, "local", b.TenantID)
	}
	assert.Equal(t, []string{newest.ID, middle.ID, oldest.ID}, ids)

	_, err = db.Exec(`DELETE FROM documents WHERE id = ?`, docID)
	require.NoError(t, err)
	got, err = bms.ListByDocument(ctx, "local", docID)
	require.NoError(t, err)
	assert.Empty(t, got, "the bookmarks outlive the document, linked to nothing")
	b, err := bms.GetByID(ctx, newest.ID)
	require.NoError(t, err)
	assert.Nil(t, b.DocumentID)
}

func newIngestBookmark(url, source string) *store.Bookmark {
	return &store.Bookmark{TenantID: "local", URL: url, Source: source, SavedAt: time.Now().UTC()}
}

type rowCounts struct{ documents, bookmarks, jobs int }

func countIngestRows(t *testing.T, db *DB) rowCounts {
	t.Helper()
	return rowCounts{countRows(t, db, "documents"), countRows(t, db, "bookmarks"), countRows(t, db, "jobs")}
}

func TestBookmarks_Ingest_NewURL(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms, q := NewBookmarks(db), NewJobs(db)

	title := "A post"
	b := newIngestBookmark("https://example.com/post", store.SourceChrome)
	b.Title = &title
	b.Tags = []string{"go"}
	res, err := bms.Ingest(ctx, b)
	require.NoError(t, err)

	assert.True(t, res.DocumentCreated)
	assert.Equal(t, store.DocStatePending, res.DocumentState)
	require.NotNil(t, res.FetchJob)
	assert.NotEmpty(t, b.ID)
	require.NotNil(t, b.DocumentID)
	assert.False(t, b.CreatedAt.IsZero())
	assert.False(t, b.UpdatedAt.IsZero())
	assert.Equal(t, rowCounts{1, 1, 1}, countIngestRows(t, db))

	doc, err := NewDocuments(db).GetByID(ctx, *b.DocumentID)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/post", doc.URL)
	assert.Equal(t, store.DocStatePending, doc.State)

	job, err := q.GetByID(ctx, res.FetchJob.ID)
	require.NoError(t, err)
	assert.Equal(t, store.JobKindFetch, job.Kind)
	assert.Equal(t, store.JobStatusPending, job.Status)
	assert.JSONEq(t, `{"document_id":"`+doc.ID+`"}`, string(job.Payload))

	saved, err := bms.GetByID(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, doc.ID, *saved.DocumentID, "linked at ingest")
	assert.Equal(t, "A post", *saved.Title)
	assert.Equal(t, []string{"go"}, saved.Tags)
}

// TestBookmarks_Ingest_KnownURL: a URL the corpus already has gets the new
// bookmark and nothing else; the document keeps whatever state it's in.
func TestBookmarks_Ingest_KnownURL(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms, docs := NewBookmarks(db), NewDocuments(db)

	first := newIngestBookmark("https://example.com/post", store.SourceChrome)
	_, err := bms.Ingest(ctx, first)
	require.NoError(t, err)
	require.NoError(t, docs.MarkFetched(ctx, *first.DocumentID))

	second := newIngestBookmark("https://example.com/post", store.SourceSafari)
	stale := "ignored"
	second.DocumentID = &stale
	res, err := bms.Ingest(ctx, second)
	require.NoError(t, err)
	assert.False(t, res.DocumentCreated)
	assert.Nil(t, res.FetchJob)
	assert.Equal(t, store.DocStateFetched, res.DocumentState)
	assert.Equal(t, *first.DocumentID, *second.DocumentID, "input DocumentID is ignored")
	assert.Equal(t, rowCounts{1, 2, 1}, countIngestRows(t, db))
}

func TestBookmarks_Ingest_Duplicate(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms := NewBookmarks(db)

	_, err := bms.Ingest(ctx, newIngestBookmark("https://example.com/post", store.SourceChrome))
	require.NoError(t, err)
	before := countIngestRows(t, db)

	dup := newIngestBookmark("https://example.com/post", store.SourceChrome)
	_, err = bms.Ingest(ctx, dup)
	require.ErrorIs(t, err, store.ErrConflict)
	assert.Empty(t, dup.ID, "a failed ingest leaves the input as it was")
	assert.Nil(t, dup.DocumentID)
	assert.Equal(t, before, countIngestRows(t, db))
}

// TestBookmarks_Ingest_Atomic: when the fetch job can't be written, neither
// is the document or the bookmark, so a retry starts from scratch instead of
// finding a pending document with no job.
func TestBookmarks_Ingest_Atomic(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms := NewBookmarks(db)
	failJobInserts(t, db)

	b := newIngestBookmark("https://example.com/post", store.SourceChrome)
	_, err := bms.Ingest(ctx, b)
	require.ErrorContains(t, err, "injected")
	assert.Equal(t, rowCounts{0, 0, 0}, countIngestRows(t, db))

	_, err = db.Exec(`DROP TRIGGER t_fail`)
	require.NoError(t, err)
	res, err := bms.Ingest(ctx, b)
	require.NoError(t, err)
	assert.True(t, res.DocumentCreated)
	require.NotNil(t, res.FetchJob)
	assert.Equal(t, rowCounts{1, 1, 1}, countIngestRows(t, db))
}

// TestBookmarks_Ingest_Concurrent: synced browsers import the same URL at
// once. Exactly one ingest creates the document and its fetch job; the rest
// link to it. The transactions write first, so they queue on the write lock
// instead of failing a read-to-write upgrade.
func TestBookmarks_Ingest_Concurrent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms := NewBookmarks(db)
	sources := []string{store.SourceChrome, store.SourceSafari, store.SourceFirefox, store.SourceHTML, store.SourceManual}
	const nURLs = 10

	var wg sync.WaitGroup
	errs := make(chan error, nURLs*len(sources))
	for i := range nURLs {
		url := fmt.Sprintf("https://example.com/%d", i)
		for _, source := range sources {
			wg.Go(func() {
				if _, err := bms.Ingest(ctx, newIngestBookmark(url, source)); err != nil {
					errs <- fmt.Errorf("%s from %s: %w", url, source, err)
				}
			})
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}

	assert.Equal(t, rowCounts{nURLs, nURLs * len(sources), nURLs}, countIngestRows(t, db))
	rows, err := db.Query(`SELECT count(*) FROM jobs GROUP BY json_extract(payload, '$.document_id')`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var n int
		require.NoError(t, rows.Scan(&n))
		assert.Equal(t, 1, n, "one fetch job per document")
	}
	require.NoError(t, rows.Err())
}

func TestBookmarks_Ingest_Validates(t *testing.T) {
	bms := NewBookmarks(newTestDB(t))
	for name, b := range map[string]*store.Bookmark{
		"tenant":   {URL: "https://example.com/", Source: store.SourceChrome, SavedAt: time.Now()},
		"url":      {TenantID: "local", Source: store.SourceChrome, SavedAt: time.Now()},
		"source":   {TenantID: "local", URL: "https://example.com/", SavedAt: time.Now()},
		"saved_at": {TenantID: "local", URL: "https://example.com/", Source: store.SourceChrome},
	} {
		_, err := bms.Ingest(context.Background(), b)
		assert.Error(t, err, name)
	}
}

// TestBookmarks_ListFolder: a folder filter matches the folder and its
// descendants on path segments, case-sensitively, with every character
// literal.
func TestBookmarks_ListFolder(t *testing.T) {
	ctx := context.Background()
	bms := NewBookmarks(newTestDB(t))
	folders := []string{
		"/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents",
		"/Tech/AIRPLANES", "/Tech/AI_x", "/Tech/AI0", "/tech/ai/lower",
		"/100% Reading", "/100X Reading",
	}
	for i, f := range folders {
		folder := f
		require.NoError(t, bms.Create(ctx, &store.Bookmark{TenantID: "local",
			URL: fmt.Sprintf("https://example.com/%d", i), Source: store.SourceChrome,
			SavedAt: time.Now().UTC(), FolderPath: &folder}))
	}

	cases := []struct {
		filter string
		want   []string
	}{
		{"/Tech/AI", []string{"/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents"}},
		{"/Tech/AI/", []string{"/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents"}},
		{"/Tech/AI/Agents", []string{"/Tech/AI/Agents"}},
		{"/Tech", []string{"/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents", "/Tech/AIRPLANES", "/Tech/AI_x", "/Tech/AI0"}},
		{"/tech/ai", []string{"/tech/ai/lower"}},
		{"/100% Reading", []string{"/100% Reading"}},
		{"/Tech/AI_", nil},
		{"/", folders},
	}
	for _, tc := range cases {
		t.Run(tc.filter, func(t *testing.T) {
			got, err := bms.List(ctx, "local", store.ListBookmarksOpts{FolderPath: tc.filter, Limit: 100})
			require.NoError(t, err)
			var paths []string
			for _, b := range got {
				paths = append(paths, *b.FolderPath)
			}
			assert.ElementsMatch(t, tc.want, paths)
		})
	}
}

// TestBookmarks_PreviewIngest: each URL is reported as Ingest would find
// it, for the tenant and the source asked about, and nothing is written.
func TestBookmarks_PreviewIngest(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms := NewBookmarks(db)
	_, err := bms.Ingest(ctx, newIngestBookmark("https://x/chrome", store.SourceChrome))
	require.NoError(t, err)
	_, err = bms.Ingest(ctx, newIngestBookmark("https://x/safari", store.SourceSafari))
	require.NoError(t, err)
	require.NoError(t, NewDocuments(db).Create(ctx, &store.Document{TenantID: "local", URL: "https://x/doc-only"}))
	other := newIngestBookmark("https://x/other-tenant", store.SourceChrome)
	other.TenantID = "other"
	_, err = bms.Ingest(ctx, other)
	require.NoError(t, err)
	before := countIngestRows(t, db)

	got, err := bms.PreviewIngest(ctx, "local", store.SourceChrome, []string{
		"https://x/chrome", "https://x/safari", "https://x/doc-only", "https://x/other-tenant", "https://x/new",
		"https://x/new",
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]store.IngestPreview{
		"https://x/chrome":       {DocumentExists: true, BookmarkExists: true},
		"https://x/safari":       {DocumentExists: true},
		"https://x/doc-only":     {DocumentExists: true},
		"https://x/other-tenant": {},
		"https://x/new":          {},
	}, got)
	assert.Equal(t, before, countIngestRows(t, db), "nothing written")

	got, err = bms.PreviewIngest(ctx, "other", store.SourceChrome, []string{"https://x/other-tenant", "https://x/chrome"})
	require.NoError(t, err)
	assert.Equal(t, map[string]store.IngestPreview{
		"https://x/other-tenant": {DocumentExists: true, BookmarkExists: true},
		"https://x/chrome":       {},
	}, got, "scoped to the tenant")
}

// TestBookmarks_PreviewIngestEmpty: no URLs, no query: a closed database
// isn't asked.
func TestBookmarks_PreviewIngestEmpty(t *testing.T) {
	db := newTestDB(t)
	bms := NewBookmarks(db)
	require.NoError(t, db.Close())
	got, err := bms.PreviewIngest(context.Background(), "local", store.SourceChrome, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	_, err = bms.PreviewIngest(context.Background(), "local", store.SourceChrome, []string{"https://x/a"})
	require.Error(t, err, "a URL is asked about")
}
