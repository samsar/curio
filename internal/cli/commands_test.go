package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/store"
)

// These tests run the real command tree against the real API (apitest). The
// daemon they find answers healthz with this process's PID and the server's
// home, so EnsureRunning sees it running and never spawns one. They check
// stdout and returned errors; TestRun covers how Run prints an error.

// runCLI runs curio with args against srv and returns its stdout.
func runCLI(t *testing.T, srv *apitest.Server, args ...string) (string, error) {
	t.Helper()
	return runCLIAt(t, srv.Home.Path, srv.URL, args...)
}

// runCLIAt runs curio with args for home and daemonURL.
func runCLIAt(t *testing.T, home, daemonURL string, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRootCmd() // flags bind to closures made per construction
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"--curio-home", home, "--daemon-url", daemonURL}, args...))
	err := root.Execute()
	return stdout.String(), err
}

func mustRun(t *testing.T, srv *apitest.Server, args ...string) string {
	t.Helper()
	out, err := runCLI(t, srv, args...)
	require.NoError(t, err, "curio %s\n%s", strings.Join(args, " "), out)
	return out
}

func count(t *testing.T, srv *apitest.Server, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, srv.DB.QueryRow(query, args...).Scan(&n))
	return n
}

// TestRun: an error reaches stderr once, and a path an *os.PathError
// already names isn't repeated.
func TestRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(),
		[]string{"--curio-home", filepath.Join(t.TempDir(), "home"), "import", "html", "/nonexistent.html", "--dry-run"},
		&stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Equal(t, "Error: open /nonexistent.html: no such file or directory\n", stderr.String())
	assert.Empty(t, stdout.String())

	stderr.Reset()
	assert.Zero(t, Run(context.Background(),
		[]string{"--curio-home", filepath.Join(t.TempDir(), "home"), "version"}, &stdout, &stderr))
	assert.Contains(t, stdout.String(), "curio ")
	assert.Empty(t, stderr.String())
}

func TestVersion(t *testing.T) {
	srv := apitest.Start(t)
	out := mustRun(t, srv, "version")
	assert.Contains(t, out, "curio ")
	assert.Contains(t, out, "embedder: nomic-embed-text/768")
}

func TestAdd(t *testing.T) {
	srv := apitest.Start(t)
	out := mustRun(t, srv, "add", "https://example.com/new", "--title", "New", "--folder", "/Reading", "--tag", "go")
	assert.Contains(t, out, "added bookmark ")
	assert.Contains(t, out, "fetch job: ")
	assert.Equal(t, 1, count(t, srv, `SELECT count(*) FROM jobs WHERE kind = 'fetch'`))

	// A document that is already fetched: no fetch, and --wait returns at once.
	srv.AddDocument(t, "https://example.com/known", store.DocStateFetched)
	out = mustRun(t, srv, "add", "https://example.com/known", "--wait")
	assert.Contains(t, out, "added bookmark ")
	assert.NotContains(t, out, "fetch job")
	assert.Contains(t, out, "fetched and indexed")

	_, err := runCLI(t, srv, "add", "https://example.com/known")
	require.Error(t, err, "a second manual bookmark for the same URL conflicts")
}

func TestAdd_WaitReportsFailure(t *testing.T) {
	srv := apitest.Start(t)
	srv.AddDocument(t, "https://example.com/dead", store.DocStateDead)
	_, err := runCLI(t, srv, "add", "https://example.com/dead", "--wait")
	require.ErrorContains(t, err, "dead")
}

func TestDocs(t *testing.T) {
	srv := apitest.Start(t)
	fetched := srv.AddDocument(t, "https://example.com/fetched", store.DocStateFetched)
	srv.AddContent(t, fetched, "# Fetched\n\nThe fetched body.")
	failed := srv.AddDocument(t, "https://example.com/failed", store.DocStateFailed)
	job, err := store.NewDocumentJob(apitest.TenantID, store.JobKindFetch, failed.ID)
	require.NoError(t, err)
	job.Status = store.JobStatusFailed
	require.NoError(t, srv.Deps.Queue.Enqueue(context.Background(), job))
	_, err = srv.DB.Exec(`UPDATE jobs SET last_error = 'HTTP 500 from origin' WHERE id = ?`, job.ID)
	require.NoError(t, err)
	srv.AddDocument(t, "https://example.com/pending", store.DocStatePending)

	out := mustRun(t, srv, "docs")
	assert.Contains(t, out, "https://example.com/fetched")
	assert.Contains(t, out, "path:   "+srv.Home.ContentDir())
	assert.NotContains(t, out, "https://example.com/failed", "fetched only by default")
	assert.Contains(t, out, "1 document(s)")

	out = mustRun(t, srv, "docs", "--failed")
	assert.Contains(t, out, "https://example.com/failed")
	assert.Contains(t, out, "err: HTTP 500 from origin")

	out = mustRun(t, srv, "docs", "--all")
	assert.Contains(t, out, "3 document(s)")

	out = mustRun(t, srv, "docs", "--state", "pending")
	assert.Contains(t, out, "https://example.com/pending")
	assert.Contains(t, out, "1 document(s)")

	out = mustRun(t, srv, "docs", "--state", "dead")
	assert.Contains(t, out, "no documents match")
	_, err = runCLI(t, srv, "docs", "--state", "archived")
	require.ErrorContains(t, err, "pending, fetched, failed, dead", "a mistyped state names the valid ones")

	out = mustRun(t, srv, "docs", "show", fetched.ID)
	assert.Contains(t, out, "url:          https://example.com/fetched")
	assert.Contains(t, out, "markdown:     "+filepath.Join(srv.Home.ContentDir(), fetched.ID)+"/",
		"the daemon's absolute path, printed as given")
	assert.Contains(t, out, "state:        fetched")
	assert.Contains(t, out, "fetcher:      apitest")
	assert.NotContains(t, out, "The fetched body.")

	out = mustRun(t, srv, "docs", "show", fetched.ID, "--content")
	assert.Contains(t, out, "--- content ---")
	assert.Contains(t, out, "The fetched body.")

	_, err = runCLI(t, srv, "docs", "show", "no-such-document")
	require.Error(t, err)
}

func TestJobs(t *testing.T) {
	srv := apitest.Start(t)
	doc := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	for _, status := range []store.JobStatus{store.JobStatusDone, store.JobStatusFailed} {
		job, err := store.NewDocumentJob(apitest.TenantID, store.JobKindFetch, doc.ID)
		require.NoError(t, err)
		job.Status = status
		require.NoError(t, srv.Deps.Queue.Enqueue(context.Background(), job))
	}
	_, err := srv.DB.Exec(`UPDATE jobs SET last_error = 'connection reset' WHERE status = 'failed'`)
	require.NoError(t, err)
	require.NoError(t, srv.Deps.Queue.Enqueue(context.Background(),
		&store.Job{TenantID: apitest.TenantID, Kind: store.JobKindCluster}))

	out := mustRun(t, srv, "jobs")
	assert.Contains(t, out, "done ")
	assert.Contains(t, out, "url: https://example.com/a")
	assert.Contains(t, out, "doc_id: "+doc.ID)
	assert.Contains(t, out, "1 job(s)")

	out = mustRun(t, srv, "jobs", "--failed")
	assert.Contains(t, out, "err: connection reset")

	out = mustRun(t, srv, "jobs", "--all", "--kind", "cluster")
	assert.Contains(t, out, "payload: {}")
	assert.Contains(t, out, "next attempt:")

	out = mustRun(t, srv, "jobs", "delete", "--status", "failed")
	assert.Contains(t, out, "deleted 1 job(s) in status=failed")
	_, err = runCLI(t, srv, "jobs", "delete")
	require.ErrorContains(t, err, "--status is required")

	out = mustRun(t, srv, "jobs", "prune", "--older-than", "30d")
	assert.Contains(t, out, "pruned 0 job(s) older than 30d")
	_, err = runCLI(t, srv, "jobs", "prune")
	require.ErrorContains(t, err, "--older-than is required")

	out = mustRun(t, srv, "jobs", "--status", "failed")
	assert.Contains(t, out, "no jobs match")
}

func TestRefetch(t *testing.T) {
	srv := apitest.Start(t)
	dead := srv.AddDocument(t, "https://example.com/gone", store.DocStateDead)
	failed := srv.AddDocument(t, "https://example.com/failed", store.DocStateFailed)

	_, err := runCLI(t, srv, "refetch", dead.ID)
	require.Error(t, err, "a dead document needs --force")
	assert.Zero(t, count(t, srv, `SELECT count(*) FROM jobs`))

	out := mustRun(t, srv, "refetch", dead.ID, "--force")
	assert.Contains(t, out, "refetch enqueued for document "+dead.ID)

	out = mustRun(t, srv, "refetch", "--all", "--state", "failed")
	assert.Contains(t, out, "refetch enqueued for documents in state=failed: 1 jobs")
	assert.Equal(t, 1, count(t, srv, `SELECT count(*) FROM jobs WHERE json_extract(payload, '$.document_id') = ?`, failed.ID))

	out = mustRun(t, srv, "refetch", "--all")
	assert.Contains(t, out, "refetch enqueued for all documents: 2 jobs")

	_, err = runCLI(t, srv, "refetch")
	require.ErrorContains(t, err, "provide a document ID or pass --all")
}

func TestReindex(t *testing.T) {
	srv := apitest.Start(t)
	fetched := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	srv.AddContent(t, fetched, "content")
	srv.AddDocument(t, "https://example.com/never-fetched", store.DocStateFetched)

	out := mustRun(t, srv, "reindex", fetched.ID)
	assert.Contains(t, out, "reindex enqueued for document "+fetched.ID)

	out = mustRun(t, srv, "reindex", "--all")
	assert.Contains(t, out, "reindex enqueued for documents in state=fetched: 1 jobs")

	out = mustRun(t, srv, "reindex", "--all", "--state", "pending")
	assert.Contains(t, out, "documents in state=pending: 0 jobs")

	_, err := runCLI(t, srv, "reindex")
	require.ErrorContains(t, err, "provide a document ID or pass --all")
}

// indexKafka seeds three fetched documents, two about kafka.
func indexKafka(t *testing.T, srv *apitest.Server) (kafka, other *store.Document) {
	t.Helper()
	kafka = srv.AddDocument(t, "https://example.com/kafka", store.DocStateFetched)
	srv.AddContent(t, kafka, "kafka partitions and consumer groups")
	srv.AddContent(t, srv.AddDocument(t, "https://example.com/kafka-2", store.DocStateFetched),
		"kafka brokers and partitions")
	other = srv.AddDocument(t, "https://example.com/bread", store.DocStateFetched)
	srv.AddContent(t, other, "sourdough bread")
	return kafka, other
}

func TestSearchAndRelated(t *testing.T) {
	srv := apitest.Start(t)
	kafka, _ := indexKafka(t, srv)

	out := mustRun(t, srv, "search", "kafka", "partitions", "-k", "2")
	assert.Contains(t, out, `2 results for "kafka partitions"`)
	assert.Contains(t, out, "https://example.com/kafka")
	assert.Contains(t, out, "<em>kafka</em>")

	out = mustRun(t, srv, "search", "kafka", "--host", "elsewhere.example")
	assert.Contains(t, out, `no results for "kafka"`)

	out = mustRun(t, srv, "related", kafka.ID, "-k", "1")
	assert.Contains(t, out, "1 documents related to "+kafka.ID)
	assert.Contains(t, out, "https://example.com/kafka-2")

	unindexed := srv.AddDocument(t, "https://example.com/new", store.DocStatePending)
	out = mustRun(t, srv, "related", unindexed.ID)
	assert.Contains(t, out, "no related documents")
}

func TestEval(t *testing.T) {
	srv := apitest.Start(t)
	indexKafka(t, srv)

	out := mustRun(t, srv, "eval", "--queries", filepath.Join("testdata", "queries.yaml"), "-k", "3")
	assert.Contains(t, out, "kafka partitions")
	assert.Contains(t, out, "1 queries · NDCG@3")

	_, err := runCLI(t, srv, "eval")
	require.ErrorContains(t, err, "--queries is required")
}

func TestInterests(t *testing.T) {
	srv := apitest.Start(t)
	out := mustRun(t, srv, "interests")
	assert.Contains(t, out, "no interests yet")

	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	srv.AddContent(t, a, "go")
	srv.AddInterest(t, "Go Programming", a, srv.AddDocument(t, "https://example.com/b", store.DocStateFetched))
	out = mustRun(t, srv, "interests", "--members", "2")
	assert.Contains(t, out, "1 interests across 2 documents")
	assert.Contains(t, out, "Go Programming")
	assert.Contains(t, out, "doc_id: "+a.ID)

	out = mustRun(t, srv, "interests", "rebuild")
	assert.Contains(t, out, "clustering job enqueued: ")
	assert.Equal(t, 1, count(t, srv, `SELECT count(*) FROM jobs WHERE kind = 'cluster'`))
}

func TestStatus(t *testing.T) {
	srv := apitest.Start(t)
	mustRun(t, srv, "add", "https://example.com/a")
	srv.AddDocument(t, "https://example.com/b", store.DocStateFetched)

	out := mustRun(t, srv, "status")
	assert.Contains(t, out, "daemon:  running")
	assert.Contains(t, out, "home:    "+srv.Home.Path)
	assert.Contains(t, out, "bookmarks: 1")
	assert.Contains(t, out, "documents: 2")
	assert.Contains(t, out, "fetched=1  pending=1")
	assert.Contains(t, out, "jobs:      pending=1")
	assert.Contains(t, out, "disk:")
}

func TestStatus_DaemonNotRunning(t *testing.T) {
	srv := apitest.Start(t)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	out, err := runCLIAt(t, srv.Home.Path, down.URL, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "daemon:  not running")
	assert.Contains(t, out, "home:    "+srv.Home.Path)
}

func TestDoctor(t *testing.T) {
	srv := apitest.Start(t)
	out := mustRun(t, srv, "doctor")
	assert.Contains(t, out, "daemon")
	assert.Contains(t, out, "content dir")
	assert.Contains(t, out, "all checks passed")
}

func TestImport(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		source  string
		created int
	}{
		{"html", []string{"import", "html", filepath.Join("testdata", "bookmarks.html")}, "html", 2},
		{"chrome", []string{"import", "chrome", "--file", filepath.Join("testdata", "chrome_bookmarks.json")}, "chrome", 2},
		{"safari", []string{"import", "safari", "--file", filepath.Join("testdata", "safari_bookmarks.plist")}, "safari", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := apitest.Start(t)
			out := mustRun(t, srv, tc.args...)
			assert.Contains(t, out, "parsed ")
			assert.Contains(t, out, "done in ")
			assert.Equal(t, tc.created, count(t, srv, `SELECT count(*) FROM bookmarks WHERE source = ?`, tc.source))
			assert.Equal(t, tc.created, count(t, srv, `SELECT count(*) FROM jobs WHERE kind = 'fetch'`))

			out = mustRun(t, srv, append(tc.args, "--limit", "1")...)
			assert.Contains(t, out, "limited to first 1")
			assert.Contains(t, out, "skipped (dup): 1")
		})
	}
}

// TestImport_DryRun: a dry run parses and filters locally and never
// contacts the daemon.
func TestImport_DryRun(t *testing.T) {
	var requests atomic.Int32
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(daemon.Close)
	srv := apitest.Start(t) // its home and database, behind a daemon URL that counts

	out, err := runCLIAt(t, srv.Home.Path, daemon.URL, "import", "html", "--dry-run",
		filepath.Join("testdata", "bookmarks.html"))
	require.NoError(t, err)
	assert.Contains(t, out, "dry-run — nothing sent to the daemon")
	assert.Contains(t, out, "would import:  2")
	assert.Contains(t, out, "would filter:  1")
	assert.Contains(t, out, "javascript: 1")
	assert.Zero(t, requests.Load(), "the daemon is never contacted")
	assert.Zero(t, count(t, srv, `SELECT count(*) FROM bookmarks`))
	assert.Zero(t, count(t, srv, `SELECT count(*) FROM documents`))
}
