package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// listDocumentURLs lists the tenant's documents under opts and returns
// their URLs, in list order.
func listDocumentURLs(t *testing.T, docs *Documents, tenantID string, opts store.ListDocumentsOpts) []string {
	t.Helper()
	opts.Limit = 1000
	got, err := docs.ListWithLastError(context.Background(), tenantID, opts)
	require.NoError(t, err)
	urls := make([]string, 0, len(got))
	for _, d := range got {
		urls = append(urls, d.URL)
	}
	return urls
}

// TestDocuments_ListHostFilter: the documents list matches a host as the
// search host filter does, since it is the same predicate.
func TestDocuments_ListHostFilter(t *testing.T) {
	db := newTestDB(t)
	docs := NewDocuments(db)
	for _, u := range []string{
		"https://myxsite.com/a", "https://my_site.com/b",
		"https://example.com/c", "http://example.com/d", "https://example.com.evil/e",
	} {
		seedDoc(t, docs, u, store.DocStateFetched)
	}

	cases := []struct {
		host string
		want []string
	}{
		{"my_site.com", []string{"https://my_site.com/b"}},
		{"%", nil},
		{"_yxsite.com", nil},
		{"EXAMPLE.com", []string{"https://example.com/c", "http://example.com/d"}},
		{"example.com.evil", []string{"https://example.com.evil/e"}},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			assert.ElementsMatch(t, tc.want, listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{Host: tc.host}))
		})
	}
}

// TestDocuments_ListFolderFilter: the documents list matches a folder as
// the bookmark folder filter does, through the document's bookmarks: a
// document is listed once however many of its bookmarks match, and one
// without bookmarks never matches.
func TestDocuments_ListFolderFilter(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	bms := NewBookmarks(db)
	folders := []string{
		"/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents",
		"/Tech/AIRPLANES", "/Tech/AI_x", "/Tech/AI0", "/tech/ai/lower",
		"/100% Reading", "/100X Reading",
	}
	byFolder := map[string]string{}
	for i, f := range folders {
		folder := f
		url := fmt.Sprintf("https://example.com/%d", i)
		_, err := bms.Ingest(ctx, &store.Bookmark{TenantID: "local", URL: url, Source: store.SourceChrome,
			SavedAt: time.Now().UTC(), FolderPath: &folder})
		require.NoError(t, err)
		byFolder[f] = url
	}
	// A second bookmark of the /Tech/AI/Agents page, from another source
	// and in another matching folder.
	agents := "/Tech/AI"
	_, err := bms.Ingest(ctx, &store.Bookmark{TenantID: "local", URL: byFolder["/Tech/AI/Agents"],
		Source: store.SourceSafari, SavedAt: time.Now().UTC(), FolderPath: &agents})
	require.NoError(t, err)
	seedDoc(t, docs, "https://example.com/no-bookmark", store.DocStateFetched)

	urls := func(folders ...string) []string {
		out := make([]string, 0, len(folders))
		for _, f := range folders {
			out = append(out, byFolder[f])
		}
		return out
	}
	cases := []struct {
		filter string
		want   []string
	}{
		{"/Tech/AI", urls("/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents")},
		{"/Tech/AI/", urls("/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents")},
		{"/Tech/AI/Agents", urls("/Tech/AI/Agents")},
		{"/Tech", urls("/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents", "/Tech/AIRPLANES", "/Tech/AI_x", "/Tech/AI0")},
		{"/tech/ai", urls("/tech/ai/lower")},
		{"/100% Reading", urls("/100% Reading")},
		{"/Tech/AI_", nil},
		{"/", append(urls(folders...), "https://example.com/no-bookmark")},
	}
	for _, tc := range cases {
		t.Run(tc.filter, func(t *testing.T) {
			assert.ElementsMatch(t, tc.want, listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{Folder: tc.filter}))
		})
	}
}

// TestDocuments_ListFiltersCombine: every filter given must match, and a
// bookmark of another tenant's never matches.
func TestDocuments_ListFiltersCombine(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	bms := NewBookmarks(db)
	add := func(tenant, url string, ct store.ContentType, state store.DocState, folder string) {
		t.Helper()
		_, err := bms.Ingest(ctx, &store.Bookmark{TenantID: tenant, URL: url, Source: store.SourceChrome,
			SavedAt: time.Now().UTC(), FolderPath: store.NullableString(folder)})
		require.NoError(t, err)
		_, err = db.Exec(`UPDATE documents SET content_type = ?, state = ? WHERE tenant_id = ? AND url = ?`,
			ct, state, tenant, url)
		require.NoError(t, err)
	}
	add("local", "https://a.example/pdf", store.ContentTypePDF, store.DocStateFetched, "/Papers")
	add("local", "https://a.example/article", store.ContentTypeArticle, store.DocStateFetched, "/Papers")
	add("local", "https://b.example/pdf", store.ContentTypePDF, store.DocStateFetched, "/Papers")
	add("local", "https://a.example/failed", store.ContentTypePDF, store.DocStateFailed, "/Papers")
	add("local", "https://a.example/elsewhere", store.ContentTypePDF, store.DocStateFetched, "/Other")
	add("other", "https://a.example/theirs", store.ContentTypePDF, store.DocStateFetched, "/Papers")
	// Another tenant's bookmark of a local document, in the folder asked
	// for.
	var elsewhere string
	require.NoError(t, db.QueryRow(`SELECT id FROM documents WHERE tenant_id = 'local' AND url = ?`,
		"https://a.example/elsewhere").Scan(&elsewhere))
	papers := "/Papers"
	require.NoError(t, bms.Create(ctx, &store.Bookmark{TenantID: "other", DocumentID: &elsewhere,
		URL: "https://a.example/elsewhere", Source: store.SourceSafari, SavedAt: time.Now().UTC(), FolderPath: &papers}))

	assert.ElementsMatch(t,
		[]string{"https://a.example/pdf", "https://a.example/article", "https://b.example/pdf", "https://a.example/failed"},
		listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{Folder: "/Papers"}))
	assert.ElementsMatch(t,
		[]string{"https://a.example/pdf", "https://b.example/pdf", "https://a.example/failed", "https://a.example/elsewhere"},
		listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{ContentType: store.ContentTypePDF}))
	assert.Equal(t, []string{"https://a.example/pdf"}, listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{
		State: store.DocStateFetched, ContentType: store.ContentTypePDF, Host: "a.example", Folder: "/Papers"}))
	assert.Empty(t, listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{ContentType: store.ContentTypeVideo}))
}

// TestDocuments_ListFilteredPages: a walk one row a page over a filtered
// list visits every matching document once, most recently updated first.
func TestDocuments_ListFilteredPages(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var want []string
	for i := range 7 {
		host := "a.example"
		if i%3 == 0 {
			host = "b.example"
		}
		d := seedDoc(t, docs, fmt.Sprintf("https://%s/%d", host, i), store.DocStateFetched)
		// Two documents share each timestamp, so the ID breaks the tie.
		_, err := db.Exec(`UPDATE documents SET updated_at = ? WHERE id = ?`,
			formatTime(base.Add(time.Duration(i/2)*time.Hour)), d.ID)
		require.NoError(t, err)
		if host == "a.example" {
			want = append(want, d.URL)
		}
	}

	var got []string
	var after store.PageKey
	for range 10 {
		page, err := docs.ListWithLastError(ctx, "local", store.ListDocumentsOpts{Host: "a.example", Limit: 1, After: after})
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		require.Len(t, page, 1)
		got = append(got, page[0].URL)
		after = store.PageKey{At: page[0].UpdatedAt, ID: page[0].ID}
	}
	assert.ElementsMatch(t, want, got, "every matching row once")
	all, err := docs.ListWithLastError(ctx, "local", store.ListDocumentsOpts{Host: "a.example", Limit: 100})
	require.NoError(t, err)
	inOrder := make([]string, 0, len(all))
	for _, d := range all {
		inOrder = append(inOrder, d.URL)
	}
	assert.Equal(t, inOrder, got, "in the list's order: updated_at, then ID, descending")
}

// TestDocuments_GetWithLastError: one document as the list returns it: the
// error of its most recent failed job, and its markdown path.
func TestDocuments_GetWithLastError(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	exts := NewExtractions(db)
	q := NewJobs(db)

	doc := seedDoc(t, docs, "https://example.com/a", store.DocStateFailed)
	rel := doc.ID + "/e.md"
	ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
		MarkdownPath: &rel}
	require.NoError(t, exts.Create(ctx, ext))
	require.NoError(t, docs.SetCurrentExtraction(ctx, doc.ID, ext.ID))
	for i, msg := range []string{"older failure", "newest failure", "oldest failure"} {
		job, err := store.NewDocumentJob("local", store.JobKindFetch, doc.ID)
		require.NoError(t, err)
		job.Status = store.JobStatusFailed
		require.NoError(t, q.Enqueue(ctx, job))
		at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add([]time.Duration{time.Hour, 2 * time.Hour, 0}[i])
		_, err = db.Exec(`UPDATE jobs SET last_error = ?, updated_at = ? WHERE id = ?`, msg, formatTime(at), job.ID)
		require.NoError(t, err)
	}
	// A later job that didn't fail doesn't count.
	done, err := store.NewDocumentJob("local", store.JobKindFetch, doc.ID)
	require.NoError(t, err)
	done.Status = store.JobStatusDone
	require.NoError(t, q.Enqueue(ctx, done))

	got, err := docs.GetWithLastError(ctx, "local", doc.ID)
	require.NoError(t, err)
	assert.Equal(t, doc.URL, got.URL)
	assert.Equal(t, store.DocStateFailed, got.State)
	assert.Equal(t, "newest failure", got.LastError)
	assert.Equal(t, rel, got.MarkdownPath)

	listed, err := docs.ListWithLastError(ctx, "local", store.ListDocumentsOpts{})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, listed[0], *got, "as the list has it")

	clean := seedDoc(t, docs, "https://example.com/clean", store.DocStateFetched)
	got, err = docs.GetWithLastError(ctx, "local", clean.ID)
	require.NoError(t, err)
	assert.Empty(t, got.LastError)
	assert.Empty(t, got.MarkdownPath)

	theirs := &store.Document{TenantID: "other", URL: "https://example.com/theirs"}
	require.NoError(t, docs.Create(ctx, theirs))
	for _, id := range []string{theirs.ID, "no-such-document"} {
		_, err = docs.GetWithLastError(ctx, "local", id)
		assert.ErrorIs(t, err, store.ErrNotFound, id)
	}
}
