package client_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/store"
)

// Every exported method round-trips through the daemon's real router over a
// real database. Error cases assert only that the status code is reported:
// the error's format is not part of the client's contract.

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

func TestDaemonUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	c := client.New("http://" + addr)

	_, err = c.Healthz(context.Background())
	require.ErrorIs(t, err, client.ErrDaemonUnreachable)
	_, err = c.GetDocumentContent(context.Background(), "any")
	require.ErrorIs(t, err, client.ErrDaemonUnreachable)
}

func TestHTTPErrorsReportTheStatus(t *testing.T) {
	_, c := start(t)
	ctx := context.Background()

	_, err := c.GetDocument(ctx, "no-such-document")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")

	_, err = c.GetDocumentContent(ctx, "no-such-document")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")

	_, err = c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: "javascript:alert(1)"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
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
	require.NotNil(t, first.NextCursor)
	rest, err := c.ListBookmarks(ctx, client.BookmarkListOpts{Limit: 3, Cursor: *first.NextCursor})
	require.NoError(t, err)
	assert.Len(t, rest.Items, 1)
	assert.Nil(t, rest.NextCursor)

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
}

func TestRefetchAndReindex(t *testing.T) {
	s, c := start(t)
	ctx := context.Background()
	dead := s.AddDocument(t, "https://example.com/gone", store.DocStateDead)
	fetched := s.AddDocument(t, "https://example.com/fetched", store.DocStateFetched)
	s.AddContent(t, fetched, "content")

	_, err := c.RefetchDocument(ctx, dead.ID, false)
	require.Error(t, err, "a dead document needs force")
	assert.Contains(t, err.Error(), "409")
	forced, err := c.RefetchDocument(ctx, dead.ID, true)
	require.NoError(t, err)
	assert.NotEmpty(t, forced.JobID)

	all, err := c.RefetchAll(ctx, "fetched")
	require.NoError(t, err)
	assert.Equal(t, 1, all.JobsEnqueued)
	_, err = c.RefetchAll(ctx, "bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")

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

	deleted, err := c.DeleteJobsByStatus(ctx, "failed")
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted.Deleted)
	assert.Equal(t, "status=failed", deleted.Mode)
	_, err = c.DeleteJobsByStatus(ctx, "pending")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")

	// The done job is the only finished one left; a 0s cutoff takes it
	// unless it was written in the cutoff's millisecond. The pending
	// cluster job is live work and never pruned.
	pruned, err := c.PruneJobsOlderThan(ctx, "0s")
	require.NoError(t, err)
	assert.LessOrEqual(t, pruned.Deleted, int64(1))
	assert.Equal(t, "older_than=0s", pruned.Mode)
	_, err = c.PruneJobsOlderThan(ctx, "soon")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
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
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
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
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")

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
