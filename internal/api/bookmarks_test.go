package api

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/importer"
	"github.com/samsar/curio/internal/store"
)

// unfetchableURLs are URLs curio can't fetch: not http(s), or no host.
var unfetchableURLs = []string{
	"javascript:alert(1)",
	"file:///etc/passwd",
	"mailto:someone@example.com",
	"ftp://example.com/file",
	"https:example.com/x",
	"https:///x",
}

// TestCreateBookmark_RejectsUnfetchableURLs: a URL curio could never fetch
// is a 400, not a document plus a fetch job that fails five times.
func TestCreateBookmark_RejectsUnfetchableURLs(t *testing.T) {
	s := newTestServer(t)
	for _, raw := range unfetchableURLs {
		t.Run(raw, func(t *testing.T) {
			body, err := json.Marshal(CreateBookmarkRequest{URL: raw})
			require.NoError(t, err)
			resp := s.do(t, request{method: http.MethodPost, path: "/v1/bookmarks", contentType: "application/json", body: string(body)})
			assertProblem(t, resp, http.StatusBadRequest)
		})
	}
	assert.Zero(t, s.count(t, "documents"))
	assert.Zero(t, s.count(t, "jobs"))
}

// TestImportBookmarks_FiltersUnfetchableURLs: the import endpoint keeps
// counting unfetchable URLs under their filter reasons.
func TestImportBookmarks_FiltersUnfetchableURLs(t *testing.T) {
	s := newTestServer(t)
	req := ImportRequest{Source: "manual"}
	for _, raw := range unfetchableURLs {
		req.Bookmarks = append(req.Bookmarks, ImportBookmark{URL: raw})
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/bookmarks/import", contentType: "application/json", body: string(body)})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got ImportResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, len(unfetchableURLs), got.Filtered)
	assert.Equal(t, map[importer.FilterReason]int{
		importer.ReasonJavaScript:        1,
		importer.ReasonLocalFile:         1,
		importer.ReasonUnsupportedScheme: 3, // mailto:, ftp://, https:example.com
		importer.ReasonInvalidURL:        1, // https:///x
	}, got.FilteredBy)
	assert.Zero(t, s.count(t, "documents"))
}

func (s *testServer) createBookmark(t *testing.T, url string) (response, BookmarkCreatedResponse) {
	t.Helper()
	body, err := json.Marshal(CreateBookmarkRequest{URL: url})
	require.NoError(t, err)
	resp := s.do(t, request{method: http.MethodPost, path: "/v1/bookmarks", contentType: "application/json", body: string(body)})
	var got BookmarkCreatedResponse
	if resp.status == http.StatusCreated {
		require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	}
	return resp, got
}

func (s *testServer) importBookmarks(t *testing.T, source string, urls ...string) ImportResponse {
	t.Helper()
	return s.postImport(t, importRequest(source, false, urls...))
}

// dryRun asks what importing urls from source would do.
func (s *testServer) dryRun(t *testing.T, source string, urls ...string) ImportResponse {
	t.Helper()
	return s.postImport(t, importRequest(source, true, urls...))
}

func importRequest(source string, dryRun bool, urls ...string) ImportRequest {
	req := ImportRequest{Source: source, DryRun: dryRun}
	for _, u := range urls {
		req.Bookmarks = append(req.Bookmarks, ImportBookmark{URL: u})
	}
	return req
}

func (s *testServer) postImport(t *testing.T, req ImportRequest) ImportResponse {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	resp := s.do(t, request{method: http.MethodPost, path: "/v1/bookmarks/import", contentType: "application/json", body: string(body)})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got ImportResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	return got
}

// TestCreateBookmark_EnqueueFailure: a bookmark whose fetch job can't be
// written isn't saved either, so the retry succeeds instead of answering 409
// for a bookmark whose document would sit pending with no job.
func TestCreateBookmark_EnqueueFailure(t *testing.T) {
	s := newTestServer(t)
	restore := s.failJobInserts(t)

	resp, _ := s.createBookmark(t, "https://example.com/a")
	assertProblem(t, resp, http.StatusInternalServerError)
	assert.Zero(t, s.count(t, "documents"))
	assert.Zero(t, s.count(t, "bookmarks"))
	assert.Zero(t, s.count(t, "jobs"))

	restore()
	resp, got := s.createBookmark(t, "https://example.com/a")
	require.Equal(t, http.StatusCreated, resp.status, resp.body)
	assert.NotEmpty(t, got.JobID)
	assert.Equal(t, string(store.DocStatePending), got.Bookmark.DocumentState)
}

// TestCreateBookmark_KnownDocument: a URL the corpus already has gets a
// bookmark but no second fetch.
func TestCreateBookmark_KnownDocument(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)

	resp, got := s.createBookmark(t, "https://example.com/a")
	require.Equal(t, http.StatusCreated, resp.status, resp.body)
	assert.Empty(t, got.JobID)
	assert.Equal(t, string(store.DocStateFetched), got.Bookmark.DocumentState)
	require.NotNil(t, got.Bookmark.DocumentID)
	assert.Equal(t, doc.ID, *got.Bookmark.DocumentID)
	assert.Zero(t, s.count(t, "jobs"))

	resp, _ = s.createBookmark(t, "https://example.com/a")
	p := assertProblem(t, resp, http.StatusConflict)
	assert.Equal(t, "a manual bookmark for https://example.com/a already exists", p.Detail)
}

func TestImportBookmarks_EnqueueFailure(t *testing.T) {
	s := newTestServer(t)
	restore := s.failJobInserts(t)

	got := s.importBookmarks(t, store.SourceChrome, "https://example.com/a")
	assert.Zero(t, got.Created)
	assert.NotEmpty(t, got.Errors)
	assert.Zero(t, s.count(t, "documents"))
	assert.Zero(t, s.count(t, "bookmarks"))
	assert.Zero(t, s.count(t, "jobs"))

	restore()
	got = s.importBookmarks(t, store.SourceChrome, "https://example.com/a")
	assert.Equal(t, 1, got.Created, "the failed row wasn't left behind to be skipped")
	assert.Equal(t, 1, got.JobsEnqueued)
	assert.Empty(t, got.Errors)
}

// TestImportBookmarks_OneFetchPerDocument: the same URL from synced browsers
// and a manual add is one document fetched once, not once per bookmark.
func TestImportBookmarks_OneFetchPerDocument(t *testing.T) {
	s := newTestServer(t)

	got := s.importBookmarks(t, store.SourceChrome, "https://example.com/a")
	assert.Equal(t, 1, got.Created)
	assert.Equal(t, 1, got.JobsEnqueued)
	for _, source := range []string{store.SourceSafari, store.SourceFirefox} {
		got = s.importBookmarks(t, source, "https://example.com/a")
		assert.Equal(t, 1, got.Created, source)
		assert.Zero(t, got.JobsEnqueued, source)
	}
	got = s.importBookmarks(t, store.SourceFirefox, "https://example.com/a")
	assert.Equal(t, 1, got.Skipped)

	resp, created := s.createBookmark(t, "https://example.com/a")
	require.Equal(t, http.StatusCreated, resp.status, resp.body)
	assert.Empty(t, created.JobID)

	assert.Equal(t, 1, s.count(t, "documents"))
	assert.Equal(t, 4, s.count(t, "bookmarks"))
	assert.Equal(t, 1, s.count(t, "jobs"))
}

// countingIngest counts the Ingest calls that reach the store.
type countingIngest struct {
	store.BookmarkStore
	calls atomic.Int32
}

func (c *countingIngest) Ingest(context.Context, *store.Bookmark) (store.IngestResult, error) {
	c.calls.Add(1)
	return store.IngestResult{}, nil
}

// TestImportBookmarks_StopsWhenClientGone: once the client has gone, the
// rest of the batch isn't written.
func TestImportBookmarks_StopsWhenClientGone(t *testing.T) {
	bms := &countingIngest{}
	d := Deps{Bookmarks: bms, TenantID: "local", Log: slog.New(slog.DiscardHandler)}
	body := `{"source":"chrome","bookmarks":[{"url":"https://example.com/a"},{"url":"https://example.com/b"}]}`

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/bookmarks/import", strings.NewReader(body))
	d.handleImportBookmarks(httptest.NewRecorder(), req)
	assert.Zero(t, bms.calls.Load())
}

func (s *testServer) listBookmarks(t *testing.T, query string) BookmarkListResponse {
	t.Helper()
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/bookmarks" + query})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got BookmarkListResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	return got
}

// TestListBookmarks_Paging: next_cursor is set exactly when another page
// follows, whether or not the client passed a limit.
func TestListBookmarks_Paging(t *testing.T) {
	s := newTestServer(t)
	for i := range 60 {
		require.NoError(t, s.deps.Bookmarks.Create(context.Background(), &store.Bookmark{TenantID: "local",
			URL: fmt.Sprintf("https://example.com/%02d", i), Source: store.SourceManual, SavedAt: time.Now().UTC()}))
	}

	first := s.listBookmarks(t, "")
	assert.Len(t, first.Items, defaultListLimit)
	require.NotEmpty(t, first.NextCursor, "more follow")
	rest := s.listBookmarks(t, "?cursor="+first.NextCursor)
	assert.Len(t, rest.Items, 60-defaultListLimit)
	assert.Empty(t, rest.NextCursor, "the last page")
	seen := map[string]bool{}
	all := slices.Concat(first.Items, rest.Items)
	for i, b := range all {
		seen[b.ID] = true
		if i > 0 {
			assert.False(t, b.CreatedAt.After(all[i-1].CreatedAt), "newest first")
		}
	}
	assert.Len(t, seen, 60, "the pages don't overlap")

	exact := s.listBookmarks(t, "?limit=60")
	assert.Len(t, exact.Items, 60)
	assert.Empty(t, exact.NextCursor, "no empty page to follow")

	for _, limit := range []string{"0", "-3", "501", "100000", "ten"} {
		got := s.listBookmarks(t, "?limit="+limit)
		assert.Len(t, got.Items, defaultListLimit, "limit=%s", limit)
	}
}

// TestLists_BogusCursor: a cursor this daemon didn't issue is a 400 on every
// list, never ignored and never a 500, and so is a cursor another order
// issued: a saved-order cursor on the created order, the documents or the
// jobs, and a default one on the saved order.
func TestLists_BogusCursor(t *testing.T) {
	s := newTestServer(t)
	for _, path := range []string{"/v1/bookmarks", "/v1/documents", "/v1/jobs"} {
		for _, cursor := range []string{"not-base64!", "bm90LWpzb24", "e30"} { // "not-json", "{}"
			p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: path + "?cursor=" + cursor}),
				http.StatusBadRequest)
			assert.Contains(t, p.Detail, "invalid cursor", "%s?cursor=%s", path, cursor)
		}
	}

	key := store.PageKey{At: time.Now(), ID: "b1"}
	saved, err := encodeCursor(key, "saved")
	require.NoError(t, err)
	plain, err := encodeCursor(key, "")
	require.NoError(t, err)
	for _, path := range []string{"/v1/bookmarks?cursor=" + saved, "/v1/bookmarks?order=created&cursor=" + saved,
		"/v1/documents?cursor=" + saved, "/v1/jobs?cursor=" + saved, "/v1/bookmarks?order=saved&cursor=" + plain} {
		p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: path}), http.StatusBadRequest)
		assert.Equal(t, "invalid cursor: another list or order issued it; list again from the first page", p.Detail,
			path)
	}
	for _, path := range []string{"/v1/bookmarks?order=saved&cursor=" + saved, "/v1/bookmarks?cursor=" + plain,
		"/v1/documents?cursor=" + plain, "/v1/jobs?cursor=" + plain} {
		assert.Equal(t, http.StatusOK, s.do(t, request{method: http.MethodGet, path: path}).status,
			"%s: a cursor of the list's own order", path)
	}
}

// TestListBookmarks_SavedOrder: order=saved lists every save newest saved
// first, ties broken by ID, a page at a time with cursors of its own; the
// Library's filters narrow it; each item carries what the list shows of
// its document, none for a bookmark linked to no document; and an order
// or filter outside its set is a 400 that names the set, the filters'
// the same as the document list's.
func TestListBookmarks_SavedOrder(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	fetched := s.seedDocument(t, "https://a.example/fetched", store.DocStateFetched)
	_, err := s.db.Exec(`UPDATE documents SET title = 'A page', content_type = 'article' WHERE id = ?`, fetched.ID)
	require.NoError(t, err)
	blocked := s.seedFailedDocument(t, "https://b.example/blocked", store.FailureCauseAntiBot)
	for i, msg := range []string{"an older failure", "the newest failure"} {
		job, err := store.NewDocumentJob("local", store.JobKindFetch, blocked.ID)
		require.NoError(t, err)
		job.Status = store.JobStatusFailed
		require.NoError(t, s.deps.Queue.Enqueue(ctx, job))
		_, err = s.db.Exec(`UPDATE jobs SET last_error = ?, updated_at = ? WHERE id = ?`, msg,
			fmt.Sprintf("2026-09-0%dT00:00:00.000Z", i+1), job.ID)
		require.NoError(t, err)
	}

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	type seed struct {
		doc    *store.Document
		url    string
		source string
		folder string
		saved  time.Time
	}
	seeds := []seed{
		{fetched, fetched.URL, store.SourceChrome, "/Tech/AI", base.Add(-48 * time.Hour)},
		{fetched, fetched.URL, store.SourceSafari, "/Reading", base}, // the same page, saved twice
		{blocked, blocked.URL, store.SourceChrome, "/Tech", base},    // a tie on saved_at
		{nil, "https://a.example/orphan", store.SourceFirefox, "/Tech/AI", base.Add(-time.Hour)},
		{nil, "https://c.example/newest", store.SourceManual, "", base.Add(time.Hour)},
	}
	bookmarks := make([]*store.Bookmark, 0, len(seeds))
	for _, b := range seeds {
		bm := &store.Bookmark{TenantID: "local", URL: b.url, Source: b.source, SavedAt: b.saved,
			FolderPath: store.NullableString(b.folder)}
		if b.doc != nil {
			bm.DocumentID = &b.doc.ID
		}
		require.NoError(t, s.deps.Bookmarks.Create(ctx, bm))
		bookmarks = append(bookmarks, bm)
	}
	want := slices.Clone(bookmarks)
	slices.SortFunc(want, func(a, b *store.Bookmark) int {
		return cmp.Or(b.SavedAt.Compare(a.SavedAt), strings.Compare(b.ID, a.ID))
	})
	wantIDs := make([]string, 0, len(want))
	for _, b := range want {
		wantIDs = append(wantIDs, b.ID)
	}
	ids := func(list BookmarkListResponse) []string {
		out := make([]string, 0, len(list.Items))
		for _, item := range list.Items {
			out = append(out, item.ID)
		}
		return out
	}

	assert.Equal(t, wantIDs, ids(s.listBookmarks(t, "?order=saved")), "newest saved first, ties by ID")
	var walked []string
	query := "?order=saved&limit=1"
	for range len(want) + 1 {
		page := s.listBookmarks(t, query)
		walked = append(walked, ids(page)...)
		if page.NextCursor == "" {
			break
		}
		query = "?order=saved&limit=1&cursor=" + page.NextCursor
	}
	assert.Equal(t, wantIDs, walked, "a page at a time, every save once")

	orphan, pagePair := bookmarks[3].ID, []string{bookmarks[0].ID, bookmarks[1].ID}
	for query, want := range map[string][]string{
		"&state=fetched":                  pagePair,
		"&state=failed":                   {bookmarks[2].ID},
		"&content_type=article":           pagePair,
		"&host=A.EXAMPLE":                 append([]string{orphan}, pagePair...),
		"&cause=anti_bot":                 {bookmarks[2].ID},
		"&folder=/Tech/AI":                {bookmarks[0].ID, orphan},
		"&source=safari":                  {bookmarks[1].ID},
		"&state=fetched&folder=/Reading":  {bookmarks[1].ID},
		"&state=dead":                     {},
		"&host=b.example&cause=dead_link": {},
	} {
		assert.ElementsMatch(t, want, ids(s.listBookmarks(t, "?order=saved"+query)), query)
	}

	items := map[string]BookmarkListItem{}
	for _, item := range s.listBookmarks(t, "?order=saved").Items {
		items[item.ID] = item
	}
	page := items[bookmarks[1].ID]
	require.NotNil(t, page.DocumentTitle)
	assert.Equal(t, "A page", *page.DocumentTitle)
	assert.Equal(t, "article", page.DocumentContentType)
	assert.Equal(t, "fetched", page.DocumentState)
	assert.Empty(t, page.DocumentFailureCause)
	failed := items[bookmarks[2].ID]
	assert.Nil(t, failed.DocumentTitle)
	assert.Equal(t, "failed", failed.DocumentState)
	assert.Equal(t, "anti_bot", failed.DocumentFailureCause)
	assert.Equal(t, "the newest failure", failed.DocumentLastError)
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/bookmarks?order=saved&source=firefox"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	for _, field := range []string{"document_id", "document_state", "document_title", "document_content_type",
		"document_failure_cause", "document_last_error"} {
		assert.NotContains(t, resp.body, `"`+field+`"`, "a bookmark linked to no document")
	}

	for path, detail := range map[string]string{
		"/v1/bookmarks?order=bogus":        `order "bogus" must be one of: created, saved`,
		"/v1/bookmarks?order=updated":      `order "updated" must be one of: created, saved`,
		"/v1/bookmarks?state=bogus":        `state "bogus" must be one of: pending, fetched, failed, dead`,
		"/v1/bookmarks?content_type=bogus": `content_type "bogus" must be one of: ` + contentTypeList,
		"/v1/bookmarks?cause=bogus":        `cause "bogus" must be one of: ` + failureCauseList,
	} {
		p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: path}), http.StatusBadRequest)
		assert.Equal(t, detail, p.Detail, path)
		documents := strings.Replace(path, "/v1/bookmarks", "/v1/documents", 1)
		if !strings.Contains(path, "order=") {
			p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: documents}), http.StatusBadRequest)
			assert.Equal(t, detail, p.Detail, "%s: the same as the document list's", documents)
		}
	}
}

// failingDocLookup fails every document lookup, as a store with a dangling
// or unreadable document row would.
type failingDocLookup struct{ store.DocumentStore }

func (failingDocLookup) GetByID(context.Context, string) (*store.Document, error) {
	return nil, errInjected
}

// TestBookmarks_DocumentLookupFailure: the list reads document states in
// its own query and needs no lookup; a single bookmark whose document can't
// be read is a server error, not a bookmark with a blank document_state.
func TestBookmarks_DocumentLookupFailure(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.Documents = failingDocLookup{d.Documents} })
	resp, created := s.createBookmark(t, "https://example.com/a")
	require.Equal(t, http.StatusCreated, resp.status, resp.body)

	list := s.listBookmarks(t, "")
	require.Len(t, list.Items, 1)
	assert.Equal(t, string(store.DocStatePending), list.Items[0].DocumentState)
	assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/bookmarks/" + created.Bookmark.ID}),
		http.StatusInternalServerError)
}

func TestBookmarks_GetListDelete(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	folder, title := "/Reading", "A"
	b := &store.Bookmark{TenantID: "local", URL: doc.URL, Title: &title, FolderPath: &folder,
		Tags: []string{"go"}, Source: store.SourceSafari, SavedAt: time.Now().UTC(), DocumentID: &doc.ID}
	require.NoError(t, s.deps.Bookmarks.Create(context.Background(), b))
	unlinked := &store.Bookmark{TenantID: "local", URL: "https://example.com/b", Source: store.SourceChrome,
		SavedAt: time.Now().UTC()}
	require.NoError(t, s.deps.Bookmarks.Create(context.Background(), unlinked))

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/bookmarks/" + b.ID})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got BookmarkResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, b.ID, got.ID)
	assert.Equal(t, "safari", got.Source)
	assert.Equal(t, "/Reading", *got.FolderPath)
	assert.Equal(t, []string{"go"}, got.Tags)
	assert.Equal(t, "fetched", got.DocumentState)
	assert.NotContains(t, resp.body, "tenant", "tenant_id is never echoed")

	list := s.listBookmarks(t, "?source=chrome")
	require.Len(t, list.Items, 1)
	assert.Equal(t, unlinked.ID, list.Items[0].ID)
	assert.Empty(t, list.Items[0].DocumentState, "no document linked")
	list = s.listBookmarks(t, "?folder=/Reading")
	require.Len(t, list.Items, 1)
	assert.Equal(t, b.ID, list.Items[0].ID)
	assert.Empty(t, s.listBookmarks(t, "?source=html").Items, "html is a source, just not one used here")
	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/bookmarks?source=bogus"}),
		http.StatusBadRequest)
	assert.Equal(t, `source "bogus" must be one of: chrome, safari, firefox, html, manual`, p.Detail)

	resp = s.do(t, request{method: http.MethodDelete, path: "/v1/bookmarks/" + b.ID})
	assert.Equal(t, http.StatusNoContent, resp.status)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		p := assertProblem(t, s.do(t, request{method: method, path: "/v1/bookmarks/" + b.ID}), http.StatusNotFound)
		assert.Equal(t, `bookmark "`+b.ID+`" not found`, p.Detail, method)
	}
	assert.Equal(t, store.DocStateFetched, s.docState(t, doc.ID), "the document outlives its bookmark")
}

func TestImportBookmarks_Validation(t *testing.T) {
	s := newTestServer(t)
	for _, body := range []string{
		`{"source":"netscape","bookmarks":[{"url":"https://example.com/a"}]}`,
		`{"source":"chrome","bookmarks":[]}`,
	} {
		resp := s.do(t, request{method: http.MethodPost, path: "/v1/bookmarks/import",
			contentType: "application/json", body: body})
		assertProblem(t, resp, http.StatusBadRequest)
	}

	got := s.importBookmarks(t, store.SourceHTML, "https://example.com/a", "https://example.com/a")
	assert.Equal(t, 1, got.Created)
	assert.Equal(t, 1, got.Skipped, "a duplicate within one batch is skipped")
}

// TestImportBookmarks_ErrorsAreCapped: the response quotes the first few
// errors, not one per bookmark.
func TestImportBookmarks_ErrorsAreCapped(t *testing.T) {
	s := newTestServer(t)
	s.failJobInserts(t)
	urls := make([]string, importErrorsCap+5)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://example.com/%d", i)
	}
	got := s.importBookmarks(t, store.SourceChrome, urls...)
	assert.Len(t, got.Errors, importErrorsCap)
	assert.Contains(t, got.Errors[0], "https://example.com/0: ")
}

// documentURLs are the URLs of the library's documents.
func (s *testServer) documentURLs(t *testing.T) map[string]bool {
	t.Helper()
	rows, err := s.db.Query(`SELECT url FROM documents`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u string
		require.NoError(t, rows.Scan(&u))
		out[u] = true
	}
	require.NoError(t, rows.Err())
	return out
}

// TestImportBookmarks_DryRunIsTheImport: a dry run answers what the import
// of the same body then does, count for count, and new_urls are the
// documents it then creates, however the batch mixes new URLs, ones the
// source has, ones the library has from elsewhere, duplicates and URLs
// curio can't fetch. The dry run writes nothing.
func TestImportBookmarks_DryRunIsTheImport(t *testing.T) {
	s := newTestServer(t)
	s.importBookmarks(t, store.SourceChrome, "https://example.com/mine")
	s.importBookmarks(t, store.SourceSafari, "https://example.com/theirs")
	s.seedDocument(t, "https://example.com/doc-only", store.DocStateFetched)
	batch := []string{
		"https://example.com/new-1",
		"https://example.com/mine",
		"https://example.com/theirs",
		"https://example.com/doc-only",
		"javascript:alert(1)",
		"HTTPS://Example.COM/new-1",                 // the same URL, normalized
		"https://example.com/new-2?utm_source=feed", // new-2 once its tracking is dropped
		"https://example.com/new-2",
		"file:///etc/hosts",
		"https://example.com/theirs",
	}
	before := [3]int{s.count(t, "bookmarks"), s.count(t, "documents"), s.count(t, "jobs")}
	docsBefore := s.documentURLs(t)

	preview := s.dryRun(t, store.SourceChrome, batch...)
	assert.Equal(t, before, [3]int{s.count(t, "bookmarks"), s.count(t, "documents"), s.count(t, "jobs")},
		"a dry run writes nothing")
	assert.True(t, preview.DryRun)
	assert.Equal(t, []string{"https://example.com/new-1", "https://example.com/new-2"}, preview.NewURLs)
	assert.Equal(t, len(preview.NewURLs), preview.JobsEnqueued)

	got := s.importBookmarks(t, store.SourceChrome, batch...)
	assert.False(t, got.DryRun)
	assert.Empty(t, got.NewURLs, "only a dry run lists them")
	want := preview
	want.DryRun, want.NewURLs = false, nil
	assert.Equal(t, want, got)
	assert.Equal(t, ImportResponse{Source: store.SourceChrome, Total: 10, Created: 4, Skipped: 4, Filtered: 2,
		JobsEnqueued: 2, FilteredBy: map[importer.FilterReason]int{importer.ReasonJavaScript: 1,
			importer.ReasonLocalFile: 1}}, got)

	created := []string{}
	for u := range s.documentURLs(t) {
		if !docsBefore[u] {
			created = append(created, u)
		}
	}
	assert.ElementsMatch(t, preview.NewURLs, created)
	again := s.dryRun(t, store.SourceChrome, batch...)
	assert.Zero(t, again.Created, "the import done, nothing is new")
	assert.Empty(t, again.NewURLs)
}

// TestImportBookmarks_DryRunAtScale: ten thousand bookmarks, two thousand
// the source has and a thousand the library has from another source, are
// counted in one request, exactly.
func TestImportBookmarks_DryRunAtScale(t *testing.T) {
	s := newTestServer(t)
	urls := make([]string, 10_000)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://example.com/page/%d", i)
	}
	s.importBookmarks(t, store.SourceChrome, urls[:2000]...)
	s.importBookmarks(t, store.SourceSafari, urls[2000:3000]...)
	before := [3]int{s.count(t, "bookmarks"), s.count(t, "documents"), s.count(t, "jobs")}

	got := s.dryRun(t, store.SourceChrome, urls...)
	assert.Equal(t, 10_000, got.Total)
	assert.Equal(t, 2000, got.Skipped)
	assert.Equal(t, 8000, got.Created)
	assert.Equal(t, 7000, got.JobsEnqueued)
	assert.Equal(t, urls[3000:], got.NewURLs)
	assert.Equal(t, before, [3]int{s.count(t, "bookmarks"), s.count(t, "documents"), s.count(t, "jobs")})
}

// TestImportBookmarks_DryRunValidates: a dry run is held to the import's
// rules: a known source and a non-empty list.
func TestImportBookmarks_DryRunValidates(t *testing.T) {
	s := newTestServer(t)
	for _, body := range []string{
		`{"source":"opera","dry_run":true,"bookmarks":[{"url":"https://example.com/a"}]}`,
		`{"source":"chrome","dry_run":true,"bookmarks":[]}`,
	} {
		resp := s.do(t, request{method: http.MethodPost, path: "/v1/bookmarks/import", contentType: "application/json",
			body: body})
		assertProblem(t, resp, http.StatusBadRequest)
	}
}
