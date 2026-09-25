package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	req := ImportRequest{Source: source}
	for _, u := range urls {
		req.Bookmarks = append(req.Bookmarks, ImportBookmark{URL: u})
	}
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
	assertProblem(t, resp, http.StatusConflict)
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
	require.NotNil(t, first.NextCursor, "more follow")
	rest := s.listBookmarks(t, "?cursor="+*first.NextCursor)
	assert.Len(t, rest.Items, 60-defaultListLimit)
	assert.Nil(t, rest.NextCursor, "the last page")
	seen := map[string]bool{}
	for _, b := range append(first.Items, rest.Items...) {
		seen[b.ID] = true
	}
	assert.Len(t, seen, 60, "the pages don't overlap")

	exact := s.listBookmarks(t, "?limit=60")
	assert.Len(t, exact.Items, 60)
	assert.Nil(t, exact.NextCursor, "no empty page to follow")

	for _, limit := range []string{"0", "-3", "501", "100000", "ten"} {
		got := s.listBookmarks(t, "?limit="+limit)
		assert.Len(t, got.Items, defaultListLimit, "limit=%s", limit)
	}
}

// failingDocLookup fails every document lookup, as a store with a dangling
// or unreadable document row would.
type failingDocLookup struct{ store.DocumentStore }

func (failingDocLookup) GetByID(context.Context, string) (*store.Document, error) {
	return nil, errInjected
}

// TestBookmarks_DocumentLookupFailure: a bookmark whose document can't be
// read is a server error, not a bookmark with a blank document_state.
func TestBookmarks_DocumentLookupFailure(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.Documents = failingDocLookup{d.Documents} })
	resp, created := s.createBookmark(t, "https://example.com/a")
	require.Equal(t, http.StatusCreated, resp.status, resp.body)

	assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/bookmarks"}), http.StatusInternalServerError)
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

	resp = s.do(t, request{method: http.MethodDelete, path: "/v1/bookmarks/" + b.ID})
	assert.Equal(t, http.StatusNoContent, resp.status)
	assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/bookmarks/" + b.ID}), http.StatusNotFound)
	assertProblem(t, s.do(t, request{method: http.MethodDelete, path: "/v1/bookmarks/" + b.ID}), http.StatusNotFound)
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
