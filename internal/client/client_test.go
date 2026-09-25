package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/store"
)

// Every exported method round-trips through the daemon's real router over a
// real database. Error cases assert the *APIError's Status and Problem
// fields: the message's wording is not part of the client's contract.

func start(t *testing.T) (*apitest.Server, *client.Client) {
	t.Helper()
	s := apitest.Start(t)
	return s, client.New(s.URL)
}

func TestHealthz(t *testing.T) {
	s, c := start(t)
	h, err := c.Healthz(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ok", h.Status)
	assert.Equal(t, os.Getpid(), h.PID)
	assert.Equal(t, s.Home.Path, h.Home)
	assert.Equal(t, store.EmbeddingDim, h.EmbeddingDim)
	assert.Positive(t, h.SchemaVersion)
}

// requireStatus asserts that err is the daemon answering status, and
// returns the problem it sent.
func requireStatus(t *testing.T, err error, status int) client.Problem {
	t.Helper()
	var apiErr *client.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, status, apiErr.Status, err.Error())
	return apiErr.Problem
}

func TestDaemonUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	c := client.New("http://" + addr)

	_, err = c.Healthz(context.Background())
	require.ErrorIs(t, err, client.ErrDaemonUnreachable)
	var opErr *net.OpError
	require.ErrorAs(t, err, &opErr, "the dial error stays in the chain")
	assert.Equal(t, "dial", opErr.Op)
	_, err = c.GetDocumentContent(context.Background(), "any")
	require.ErrorIs(t, err, client.ErrDaemonUnreachable)
}

// TestTimeoutIsNotUnreachable: a daemon too slow for the caller's deadline
// was reached; the error says the deadline passed.
func TestTimeoutIsNotUnreachable(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := client.New(slow.URL).Stats(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotErrorIs(t, err, client.ErrDaemonUnreachable)
}

func TestHTTPErrorsAreAPIErrors(t *testing.T) {
	_, c := start(t)
	ctx := context.Background()

	_, err := c.GetDocument(ctx, "no-such-document")
	p := requireStatus(t, err, http.StatusNotFound)
	assert.Equal(t, `document "no-such-document" not found`, p.Detail)
	assert.Equal(t, `document "no-such-document" not found`, err.Error(), "the detail, not the raw problem")
	assert.NotEmpty(t, p.RequestID)
	assert.True(t, client.IsNotFound(err))

	_, err = c.GetDocumentContent(ctx, "no-such-document")
	requireStatus(t, err, http.StatusNotFound)

	_, err = c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: "javascript:alert(1)"})
	requireStatus(t, err, http.StatusBadRequest)
	assert.False(t, client.IsNotFound(err))
}

// fakeDaemon answers every request with handler.
func fakeDaemon(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return client.New(srv.URL)
}

func TestAPIError_ServerErrors(t *testing.T) {
	t.Run("a problem names its request ID and the log", func(t *testing.T) {
		c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"title":"internal error","status":500,"detail":"disk full","request_id":"req-7"}`)
		})
		_, err := c.GetDocumentContent(context.Background(), "doc")
		p := requireStatus(t, err, http.StatusInternalServerError)
		assert.Equal(t, "disk full", p.Detail)
		assert.Equal(t, "disk full (request req-7; see `curio daemon logs`)", err.Error())
	})

	t.Run("a body that isn't a problem is the detail", func(t *testing.T) {
		c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Request-Id", "req-8")
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, "upstream went away\n")
		})
		_, err := c.Stats(context.Background())
		p := requireStatus(t, err, http.StatusBadGateway)
		assert.Equal(t, "Bad Gateway", p.Title)
		assert.Equal(t, "upstream went away", p.Detail)
		assert.Equal(t, "req-8", p.RequestID, "from the header when the body has none")
	})

	t.Run("an error body is read to a bound", func(t *testing.T) {
		c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, strings.Repeat("x", 1<<20))
		})
		_, err := c.Stats(context.Background())
		p := requireStatus(t, err, http.StatusInternalServerError)
		assert.Len(t, p.Detail, 64<<10)
	})
}

// TestResponsesAreReadTolerantly: a newer daemon's extra fields are ignored.
func TestResponsesAreReadTolerantly(t *testing.T) {
	c := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"v9","bookmarks_total":3,"documents_total":2,"added_in_v9":{"x":1}}`)
	})
	stats, err := c.Stats(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, stats.BookmarksTotal)
}

// TestImport_OmitsUnknownDates: a bookmark without a date is sent without
// saved_at, not as the year 1.
func TestImport_OmitsUnknownDates(t *testing.T) {
	var body map[string]any
	c := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"source":"html","total":1}`)
	})
	_, err := c.ImportBookmarks(context.Background(), client.ImportRequest{Source: "html",
		Bookmarks: []client.ImportBookmark{{URL: "https://example.com/a"}}})
	require.NoError(t, err)
	bookmarks, ok := body["bookmarks"].([]any)
	require.True(t, ok, "%v", body)
	require.Len(t, bookmarks, 1)
	assert.NotContains(t, bookmarks[0], "saved_at")
}

func TestBookmarks(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()

	created, err := c.CreateBookmark(ctx, client.CreateBookmarkRequest{
		URL: "https://example.com/a", Title: "A", FolderPath: "/Reading", Tags: []string{"go"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, created.JobID)
	assert.Equal(t, "https://example.com/a", created.Bookmark.URL)
	assert.Equal(t, "manual", created.Bookmark.Source)
	assert.Equal(t, "pending", created.Bookmark.DocumentState)
	require.NotNil(t, created.Bookmark.Title)
	assert.Equal(t, "A", *created.Bookmark.Title)

	res, err := c.ImportBookmarks(ctx, client.ImportRequest{Source: "chrome", Bookmarks: []client.ImportBookmark{
		{URL: "https://example.com/a", SavedAt: time.Now().UTC()},
		{URL: "https://example.com/b", FolderPath: "/Reading/Go"},
		{URL: "https://example.com/c"},
		{URL: "file:///etc/passwd"},
	}})
	require.NoError(t, err)
	assert.Equal(t, "chrome", res.Source)
	assert.Equal(t, 4, res.Total)
	assert.Equal(t, 3, res.Created)
	assert.Equal(t, 1, res.Filtered)
	assert.Equal(t, 1, res.FilteredBy["local_file"])
	assert.Equal(t, 2, res.JobsEnqueued, "example.com/a was already a document")

	first, err := c.ListBookmarks(ctx, client.BookmarkListOpts{Limit: 3})
	require.NoError(t, err)
	assert.Len(t, first.Items, 3)
	require.NotEmpty(t, first.NextCursor)
	rest, err := c.ListBookmarks(ctx, client.BookmarkListOpts{Limit: 3, Cursor: first.NextCursor})
	require.NoError(t, err)
	assert.Len(t, rest.Items, 1)
	assert.Empty(t, rest.NextCursor)

	chrome, err := c.ListBookmarks(ctx, client.BookmarkListOpts{Source: "chrome", Folder: "/Reading"})
	require.NoError(t, err)
	require.Len(t, chrome.Items, 1)
	assert.Equal(t, "https://example.com/b", chrome.Items[0].URL)

	assert.Equal(t, 4, countRows(t, s, "bookmarks"))
}

func TestDocuments(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	fetched := s.AddDocument(t, "https://example.com/fetched", store.DocStateFetched)
	s.AddContent(t, fetched, "# Fetched\n\nThe body.")
	s.AddDocument(t, "https://example.com/pending", store.DocStatePending)

	doc, err := c.GetDocument(ctx, fetched.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/fetched", doc.URL)
	assert.Equal(t, "fetched", doc.State)
	require.NotNil(t, doc.CurrentExtraction)
	assert.Equal(t, "apitest", doc.CurrentExtraction.Fetcher)
	assert.NotEmpty(t, doc.CurrentExtraction.MarkdownPath)
	assert.Equal(t, "apitest", doc.CurrentExtraction.ExtractionMeta["source"])

	body, err := c.GetDocumentContent(ctx, fetched.ID)
	require.NoError(t, err)
	assert.Equal(t, "# Fetched\n\nThe body.", body)

	all, err := c.ListDocuments(ctx, client.ListDocumentsOpts{})
	require.NoError(t, err)
	assert.Len(t, all.Items, 2)
	pending, err := c.ListDocuments(ctx, client.ListDocumentsOpts{State: "pending", Limit: 10})
	require.NoError(t, err)
	require.Len(t, pending.Items, 1)
	assert.Equal(t, "https://example.com/pending", pending.Items[0].URL)
	withPath, err := c.ListDocuments(ctx, client.ListDocumentsOpts{State: "fetched"})
	require.NoError(t, err)
	require.Len(t, withPath.Items, 1)
	assert.Contains(t, withPath.Items[0].MarkdownPath, s.Home.ContentDir())

	first, err := c.ListDocuments(ctx, client.ListDocumentsOpts{Limit: 1})
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.NextCursor)
	rest, err := c.ListDocuments(ctx, client.ListDocumentsOpts{Limit: 1, Cursor: first.NextCursor})
	require.NoError(t, err)
	require.Len(t, rest.Items, 1)
	assert.Empty(t, rest.NextCursor)
	assert.ElementsMatch(t, []string{fetched.ID, pending.Items[0].ID}, []string{first.Items[0].ID, rest.Items[0].ID})
}

func TestRefetchAndReindex(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	dead := s.AddDocument(t, "https://example.com/gone", store.DocStateDead)
	fetched := s.AddDocument(t, "https://example.com/fetched", store.DocStateFetched)
	s.AddContent(t, fetched, "content")

	_, err := c.RefetchDocument(ctx, dead.ID, false)
	requireStatus(t, err, http.StatusConflict) // a dead document needs force
	forced, err := c.RefetchDocument(ctx, dead.ID, true)
	require.NoError(t, err)
	assert.NotEmpty(t, forced.JobID)

	all, err := c.RefetchAll(ctx, "fetched")
	require.NoError(t, err)
	assert.Equal(t, 1, all.JobsEnqueued)
	_, err = c.RefetchAll(ctx, "bogus")
	requireStatus(t, err, http.StatusBadRequest)

	one, err := c.ReindexDocument(ctx, fetched.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, one.JobID)
	reindexed, err := c.ReindexAll(ctx, "pending")
	require.NoError(t, err)
	assert.Equal(t, 1, reindexed.JobsEnqueued, "the refetch left the document with content pending")
	reindexed, err = c.ReindexAll(ctx, "")
	require.NoError(t, err)
	assert.Zero(t, reindexed.JobsEnqueued, "nothing is fetched any more")
}

func TestJobs(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	doc := s.AddDocument(t, "https://example.com/a", store.DocStateFailed)
	for _, status := range []store.JobStatus{store.JobStatusDone, store.JobStatusFailed, store.JobStatusFailed} {
		job, err := store.NewDocumentJob(apitest.TenantID, store.JobKindFetch, doc.ID)
		require.NoError(t, err)
		job.Status = status
		require.NoError(t, s.Deps.Queue.Enqueue(ctx, job))
	}
	require.NoError(t, s.Deps.Queue.Enqueue(ctx, &store.Job{TenantID: apitest.TenantID, Kind: store.JobKindCluster}))

	failed, err := c.ListJobs(ctx, client.JobListOpts{Status: "failed", Kind: "fetch", Limit: 10})
	require.NoError(t, err)
	require.Len(t, failed.Items, 2)
	assert.Equal(t, "https://example.com/a", failed.Items[0].DocURL)
	clusters, err := c.ListJobs(ctx, client.JobListOpts{Kind: "cluster"})
	require.NoError(t, err)
	require.Len(t, clusters.Items, 1)
	assert.Empty(t, clusters.Items[0].DocURL)

	seen := map[string]bool{}
	for cursor := ""; ; {
		page, err := c.ListJobs(ctx, client.JobListOpts{Limit: 3, Cursor: cursor})
		require.NoError(t, err)
		for _, j := range page.Items {
			assert.False(t, seen[j.ID], "job %s on two pages", j.ID)
			seen[j.ID] = true
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	assert.Len(t, seen, 4, "every job, over two pages")

	job, err := c.GetJob(ctx, failed.Items[0].ID)
	require.NoError(t, err)
	assert.Equal(t, failed.Items[0], *job, "one job reads as the list shows it")
	_, err = c.GetJob(ctx, "no-such-job")
	requireStatus(t, err, http.StatusNotFound)

	deleted, err := c.DeleteJobsByStatus(ctx, "failed")
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted.Deleted)
	assert.Equal(t, "status=failed", deleted.Mode)
	_, err = c.DeleteJobsByStatus(ctx, "pending")
	requireStatus(t, err, http.StatusBadRequest)

	// The done job is the only finished one left; a 0s cutoff takes it
	// unless it was written in the cutoff's millisecond. The pending
	// cluster job is live work and never pruned.
	pruned, err := c.PruneJobsOlderThan(ctx, "0s")
	require.NoError(t, err)
	assert.LessOrEqual(t, pruned.Deleted, int64(1))
	assert.Equal(t, "older_than=0s", pruned.Mode)
	_, err = c.PruneJobsOlderThan(ctx, "soon")
	requireStatus(t, err, http.StatusBadRequest)
}

func TestStatsAndMetrics(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	_, err := c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: "https://example.com/a"})
	require.NoError(t, err)
	s.AddDocument(t, "https://example.com/b", store.DocStateFetched)

	stats, err := c.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.BookmarksTotal)
	assert.Equal(t, 2, stats.DocumentsTotal)
	assert.Equal(t, map[string]int{"pending": 1, "fetched": 1}, stats.DocumentsByState)
	assert.Equal(t, map[string]int{"pending": 1}, stats.JobsByStatus)

	_, err = s.DB.Exec(`INSERT INTO jobs (id, tenant_id, kind, payload, status, started_at, updated_at)
		VALUES ('j1', 'local', 'index', '{}', 'done',
		        strftime('%Y-%m-%dT%H:%M:%fZ','now','-2 seconds'), strftime('%Y-%m-%dT%H:%M:%fZ','now'))`)
	require.NoError(t, err)
	m, err := c.Metrics(ctx, 600)
	require.NoError(t, err)
	assert.Equal(t, 600, m.WindowSeconds)
	require.Len(t, m.ByKind, 1)
	assert.Equal(t, "index", m.ByKind[0].Kind)
	assert.Equal(t, 1, m.ByKind[0].Count)
	assert.InDelta(t, 2000, m.ByKind[0].MeanMS, 50)

	m, err = c.Metrics(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, 3600, m.WindowSeconds, "the server's default window")
}

func TestSearchAndRelated(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	for i, body := range []string{"kafka partitions and consumer groups", "kafka brokers and partitions", "sourdough bread"} {
		doc := s.AddDocument(t, fmt.Sprintf("https://example.com/%d", i), store.DocStateFetched)
		s.AddContent(t, doc, body)
	}
	docs, err := c.ListDocuments(ctx, client.ListDocumentsOpts{})
	require.NoError(t, err)
	require.Len(t, docs.Items, 3)

	res, err := c.Search(ctx, client.SearchRequest{Query: "kafka partitions", K: 5})
	require.NoError(t, err)
	assert.False(t, res.Degraded)
	assert.Equal(t, 2, res.BM25Hits)
	require.Len(t, res.Items, 3, "vector search ranks every document")
	for _, hit := range res.Items[:2] {
		assert.Contains(t, []string{"https://example.com/0", "https://example.com/1"}, hit.Document.URL)
		assert.NotEmpty(t, hit.MarkdownPath)
		require.NotEmpty(t, hit.Matches)
	}
	assert.Equal(t, "https://example.com/2", res.Items[2].Document.URL)

	filtered, err := c.Search(ctx, client.SearchRequest{Query: "kafka",
		Filters: &client.SearchFilters{Host: []string{"other.example"}}})
	require.NoError(t, err)
	assert.Empty(t, filtered.Items)

	kafka := res.Items[0].Document.ID
	related, err := c.RelatedDocuments(ctx, kafka, 1)
	require.NoError(t, err)
	assert.Equal(t, kafka, related.DocID)
	require.Len(t, related.Items, 1)
	assert.NotEqual(t, kafka, related.Items[0].Document.ID)
	assert.Contains(t, []string{"https://example.com/0", "https://example.com/1"}, related.Items[0].Document.URL,
		"the other kafka document is the nearest")

	_, err = c.RelatedDocuments(ctx, "no-such-document", 0)
	requireStatus(t, err, http.StatusNotFound)
}

func TestInterests(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()

	none, err := c.ListInterests(ctx, client.ListInterestsOpts{})
	require.NoError(t, err)
	assert.Empty(t, none.Items)

	a := s.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	b := s.AddDocument(t, "https://example.com/b", store.DocStateFetched)
	s.AddContent(t, a, "go")
	cluster := s.AddInterest(t, "Go", a, b)

	list, err := c.ListInterests(ctx, client.ListInterestsOpts{Limit: 5, Members: 1})
	require.NoError(t, err)
	assert.Equal(t, 2, list.NumDocuments)
	assert.Equal(t, "apitest", list.Algo)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "Go", list.Items[0].Label)
	require.Len(t, list.Items[0].Members, 1, "members=1")
	assert.Equal(t, a.ID, list.Items[0].Members[0].DocID)
	assert.NotEmpty(t, list.Items[0].Members[0].MarkdownPath)

	got, err := c.GetInterest(ctx, cluster.ID, 10)
	require.NoError(t, err)
	assert.Equal(t, "Go", got.Label)
	assert.Len(t, got.Members, 2)
	_, err = c.GetInterest(ctx, "no-such-interest", 0)
	requireStatus(t, err, http.StatusNotFound)

	rebuild, err := c.RebuildInterests(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, rebuild.JobID)
}

func countRows(t *testing.T, s *apitest.Server, table string) int {
	t.Helper()
	var n int
	require.NoError(t, s.DB.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
	return n
}
