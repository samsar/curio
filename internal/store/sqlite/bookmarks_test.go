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

// seedBookmarkAt inserts bookmark id of url from source, linked to the
// document docID ("" for none), saved at saved and added to curio at
// created, both in the store's time format.
func seedBookmarkAt(t *testing.T, db *DB, id, docID, url, source, saved, created string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO bookmarks (id, tenant_id, document_id, url, saved_at, source, created_at)
		VALUES (?, 'local', ?, ?, ?, ?, ?)`, id, store.NullableString(docID), url, saved, source, created)
	require.NoError(t, err)
}

// listBookmarkIDs lists the IDs of the tenant's bookmarks under opts, in
// the order List returns them.
func listBookmarkIDs(t *testing.T, bms *Bookmarks, opts store.ListBookmarksOpts) []string {
	t.Helper()
	got, err := bms.List(context.Background(), "local", opts)
	require.NoError(t, err)
	ids := make([]string, 0, len(got))
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	return ids
}

// TestBookmarks_ListOrders: the default order is newest added first, the
// saved order newest saved first, each broken by ID, and a cursor page of
// either starts after its own key: here the two disagree on every row.
func TestBookmarks_ListOrders(t *testing.T) {
	db := newTestDB(t)
	bms := NewBookmarks(db)
	seedBookmarkAt(t, db, "b1", "", "https://example.com/1", store.SourceSafari,
		"2020-01-01T00:00:00.000Z", "2026-01-04T00:00:00.000Z")
	seedBookmarkAt(t, db, "b2", "", "https://example.com/2", store.SourceChrome,
		"2024-01-01T00:00:00.000Z", "2026-01-03T00:00:00.000Z")
	seedBookmarkAt(t, db, "b3", "", "https://example.com/3", store.SourceChrome,
		"2022-01-01T00:00:00.000Z", "2026-01-02T00:00:00.000Z")
	seedBookmarkAt(t, db, "b4", "", "https://example.com/4", store.SourceFirefox,
		"2024-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z")

	created := []string{"b1", "b2", "b3", "b4"}
	assert.Equal(t, created, listBookmarkIDs(t, bms, store.ListBookmarksOpts{}), "the default order")
	assert.Equal(t, created, listBookmarkIDs(t, bms, store.ListBookmarksOpts{Order: store.BookmarkOrderCreated}))
	assert.Equal(t, []string{"b4", "b2", "b3", "b1"},
		listBookmarkIDs(t, bms, store.ListBookmarksOpts{Order: store.BookmarkOrderSaved}),
		"newest saved first, a tie broken by ID")

	b2, err := bms.GetByID(context.Background(), "b2")
	require.NoError(t, err)
	assert.Equal(t, []string{"b3", "b1"}, listBookmarkIDs(t, bms,
		store.ListBookmarksOpts{Order: store.BookmarkOrderSaved, After: store.BookmarkOrderSaved.Key(b2)}))
	assert.Equal(t, []string{"b3", "b4"}, listBookmarkIDs(t, bms,
		store.ListBookmarksOpts{Order: store.BookmarkOrderCreated, After: store.BookmarkOrderCreated.Key(b2)}))

	for _, order := range []store.BookmarkOrder{"updated", "SAVED", "saved_at"} {
		got, err := bms.List(context.Background(), "local", store.ListBookmarksOpts{Order: order})
		assert.ErrorContains(t, err, "unknown order", order)
		assert.Nil(t, got, order)
	}
}

// bookmarkListFixture is a library for the bookmark list's filters: each
// document's bookmarks, two of them the same page saved in two browsers,
// and bookmarks linked to no document.
type bookmarkListFixture struct {
	pagePair             [2]string // a titled fetched article's two bookmarks, chrome and safari
	blocked, gone, weird string    // the bookmarks of a failed, a dead and an odd-hosted document
	orphan, filed        string    // two bookmarks linked to no document, one filed in /Tech/AI
}

func seedBookmarkList(t *testing.T, db *DB) bookmarkListFixture {
	t.Helper()
	ctx := context.Background()
	docs, bms := NewDocuments(db), NewBookmarks(db)
	title := "A page"
	page := &store.Document{TenantID: "local", URL: "https://a.example/page", State: store.DocStateFetched,
		ContentType: store.ContentTypeArticle, Title: &title}
	blocked := &store.Document{TenantID: "local", URL: "https://b.example/blocked",
		FailureCause: store.FailureCauseAntiBot}
	gone := &store.Document{TenantID: "local", URL: "https://a.example/gone.pdf", ContentType: store.ContentTypePDF,
		FailureCause: store.FailureCauseDeadLink}
	weird := &store.Document{TenantID: "local", URL: "https://a_b.example/x", State: store.DocStateFetched}
	for _, d := range []*store.Document{page, blocked, gone, weird} {
		require.NoError(t, docs.Create(ctx, d))
	}
	save := func(doc *store.Document, url, source, folder string) string {
		t.Helper()
		b := &store.Bookmark{TenantID: "local", URL: url, Source: source, SavedAt: time.Now().UTC(),
			FolderPath: store.NullableString(folder)}
		if doc != nil {
			b.DocumentID = &doc.ID
		}
		require.NoError(t, bms.Create(ctx, b))
		return b.ID
	}
	return bookmarkListFixture{
		pagePair: [2]string{save(page, page.URL, store.SourceChrome, "/Tech/AI"),
			save(page, page.URL, store.SourceSafari, "/Tech/AIRPLANES")},
		blocked: save(blocked, blocked.URL, store.SourceChrome, "/Tech"),
		gone:    save(gone, gone.URL, store.SourceFirefox, "/Reading"),
		weird:   save(weird, weird.URL, store.SourceChrome, ""),
		orphan:  save(nil, "https://axb.example/x", store.SourceChrome, ""),
		filed:   save(nil, "https://a.example/filed", store.SourceManual, "/Tech/AI"),
	}
}

// TestBookmarks_ListFilters: each filter alone and all of them together,
// in both orders. A page saved in two browsers is listed once per
// bookmark; a bookmark linked to no document is listed unfiltered, and by
// the bookmark's own filters, but matches no filter on a document. The
// host matches case-insensitively with every character literal, and the
// folder on path segments.
func TestBookmarks_ListFilters(t *testing.T) {
	db := newTestDB(t)
	bms := NewBookmarks(db)
	f := seedBookmarkList(t, db)
	page := f.pagePair[:]
	cases := map[string]struct {
		opts store.ListBookmarksOpts
		want []string
	}{
		"unfiltered": {store.ListBookmarksOpts{},
			append([]string{f.blocked, f.gone, f.weird, f.orphan, f.filed}, page...)},
		"source":             {store.ListBookmarksOpts{Source: store.SourceSafari}, []string{f.pagePair[1]}},
		"folder":             {store.ListBookmarksOpts{FolderPath: "/Tech/AI"}, []string{f.pagePair[0], f.filed}},
		"host":               {store.ListBookmarksOpts{Host: "A.Example"}, append([]string{f.gone, f.filed}, page...)},
		"host, _ literal":    {store.ListBookmarksOpts{Host: "a_b.example"}, []string{f.weird}},
		"host, % literal":    {store.ListBookmarksOpts{Host: "a%"}, nil},
		"state":              {store.ListBookmarksOpts{State: store.DocStateFetched}, append([]string{f.weird}, page...)},
		"state, failed":      {store.ListBookmarksOpts{State: store.DocStateFailed}, []string{f.blocked}},
		"content type":       {store.ListBookmarksOpts{ContentType: store.ContentTypePDF}, []string{f.gone}},
		"content type, any":  {store.ListBookmarksOpts{ContentType: store.ContentTypeUnknown}, []string{f.blocked, f.weird}},
		"cause":              {store.ListBookmarksOpts{Cause: store.FailureCauseDeadLink}, []string{f.gone}},
		"a cause no one has": {store.ListBookmarksOpts{Cause: store.FailureCauseTLS}, nil},
		"every filter": {store.ListBookmarksOpts{Source: store.SourceChrome, FolderPath: "/Tech", Host: "b.example",
			State: store.DocStateFailed, ContentType: store.ContentTypeUnknown, Cause: store.FailureCauseAntiBot},
			[]string{f.blocked}},
		"every filter, one off": {store.ListBookmarksOpts{Source: store.SourceChrome, FolderPath: "/Tech",
			Host: "b.example", State: store.DocStateFailed, ContentType: store.ContentTypeUnknown,
			Cause: store.FailureCauseLoginWall}, nil},
	}
	for name, tc := range cases {
		for _, order := range []store.BookmarkOrder{store.BookmarkOrderCreated, store.BookmarkOrderSaved} {
			t.Run(name+" in "+string(order)+" order", func(t *testing.T) {
				tc.opts.Order = order
				assert.ElementsMatch(t, tc.want, listBookmarkIDs(t, bms, tc.opts))
			})
		}
	}
}

// TestBookmarks_ListDocumentFields: a row carries its document's state,
// title, type and cause, and the error of its most recent failed job, not
// an older one's; a bookmark linked to no document carries none of them.
func TestBookmarks_ListDocumentFields(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	bms := NewBookmarks(db)
	f := seedBookmarkList(t, db)
	blocked, err := bms.GetByID(ctx, f.blocked)
	require.NoError(t, err)
	for i, job := range []struct{ status, lastError, at string }{
		{"failed", "the older failure", "2026-09-01T00:00:00.000Z"},
		{"failed", "the newest failure", "2026-09-03T00:00:00.000Z"},
		{"done", "", "2026-09-04T00:00:00.000Z"},
		{"failed", "the oldest failure", "2026-08-01T00:00:00.000Z"},
	} {
		_, err := db.Exec(`INSERT INTO jobs (id, tenant_id, kind, payload, status, last_error, updated_at, document_id)
			VALUES (?, 'local', 'fetch', json_object('document_id', ?), ?, ?, ?, ?)`,
			fmt.Sprintf("j%d", i), *blocked.DocumentID, job.status, store.NullableString(job.lastError), job.at,
			*blocked.DocumentID)
		require.NoError(t, err)
	}

	for _, order := range []store.BookmarkOrder{store.BookmarkOrderCreated, store.BookmarkOrderSaved} {
		got, err := bms.List(ctx, "local", store.ListBookmarksOpts{Order: order})
		require.NoError(t, err)
		rows := map[string]store.BookmarkWithDocument{}
		for _, b := range got {
			rows[b.ID] = b
		}
		require.Len(t, rows, 7, order)

		assert.Equal(t, store.BookmarkWithDocument{Bookmark: rows[f.blocked].Bookmark,
			DocumentState: store.DocStateFailed, DocumentContentType: store.ContentTypeUnknown,
			DocumentFailureCause: store.FailureCauseAntiBot, DocumentLastError: "the newest failure"},
			rows[f.blocked], order)
		for _, id := range f.pagePair {
			page := rows[id]
			require.NotNil(t, page.DocumentTitle, order)
			assert.Equal(t, "A page", *page.DocumentTitle, order)
			assert.Equal(t, store.DocStateFetched, page.DocumentState, order)
			assert.Equal(t, store.ContentTypeArticle, page.DocumentContentType, order)
			assert.Empty(t, page.DocumentFailureCause, order)
			assert.Empty(t, page.DocumentLastError, order)
		}
		assert.Equal(t, store.FailureCauseDeadLink, rows[f.gone].DocumentFailureCause, order)
		assert.Equal(t, store.BookmarkWithDocument{Bookmark: rows[f.orphan].Bookmark}, rows[f.orphan],
			"%s: no document, no document fields", order)
	}
}
