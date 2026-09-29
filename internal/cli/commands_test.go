package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/drift"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/service/servicetest"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
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
	err := runCLIStreams(newRootCmdWith(testDeps(t)), home, daemonURL, &stdout, &stderr, args...)
	return stdout.String(), err
}

// runCLIStreams runs root with args for home and daemonURL, writing to
// stdout and stderr, with nothing on stdin: never a terminal.
func runCLIStreams(root *cobra.Command, home, daemonURL string, stdout, stderr io.Writer, args ...string) error {
	root.SetIn(strings.NewReader(""))
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(append([]string{"--curio-home", home, "--daemon-url", daemonURL}, args...))
	return root.Execute()
}

// testDeps are the root's dependencies in a test: a 64 GiB Apple silicon
// Mac, an installer that finds Homebrew and nothing else, an Ollama (a
// fake, with curio's models) that config.yaml's defaults point at, and a
// launchd agent (a fake) that isn't installed. Nothing reaches the machine
// the test runs on, so the results are the same on every CI runner.
func testDeps(t *testing.T) deps {
	t.Helper()
	ollama := setuptest.NewOllama(t, "0.34.4", embedModel, genModel)
	defaults := config.Default()
	defaults.Embedding.BaseURL, defaults.Generation.BaseURL = ollama.URL, ollama.URL
	return deps{
		connect:   withService(servicetest.New(t, agentLabel)),
		probe:     setuptest.NewProbe(),
		installer: setuptest.NewInstaller(),
		defaults:  &defaults,
		newUI:     setup.NewUI,
		sources:   setuptest.NoSources,
		geteuid:   func() int { return 501 },
		openURL: func(_ context.Context, url string) error {
			t.Errorf("a test opened %s in a browser", url)
			return errors.New("tests open nothing")
		},
	}
}

// The models curio picks for testDeps' Mac.
const (
	embedModel = "qwen3-embedding:0.6b"
	genModel   = "gemma4:26b"
)

// nextPage returns the arguments of the "next page:" line out ends with,
// without the leading "curio".
func nextPage(t *testing.T, out string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	hint, ok := strings.CutPrefix(last, "next page: curio ")
	require.True(t, ok, "the page ends with the next page's command:\n%s", out)
	return strings.Fields(hint)
}

// runArgs runs curio with exactly args, as a pasted command line would.
func runArgs(t *testing.T, args ...string) string {
	t.Helper()
	var stdout bytes.Buffer
	root := newRootCmdWith(testDeps(t))
	root.SetOut(&stdout)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	require.NoError(t, root.Execute(), "curio %s", strings.Join(args, " "))
	return stdout.String()
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

// TestRun_UsageErrors: a mistake in the command line itself says where the
// failing command's usage is; errors from running a command don't.
func TestRun_UsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"nosuchcmd"},
			"Error: unknown command \"nosuchcmd\" for \"curio\"\nRun 'curio --help' for usage.\n"},
		{"unknown flag", []string{"search", "--nosuchflag"},
			"Error: unknown flag: --nosuchflag\nRun 'curio search --help' for usage.\n"},
		{"missing argument", []string{"docs", "show"},
			"Error: accepts 1 arg(s), received 0\nRun 'curio docs show --help' for usage.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"--curio-home", filepath.Join(t.TempDir(), "home")}, tc.args...)
			assert.Equal(t, 1, Run(context.Background(), args, &stdout, &stderr))
			assert.Equal(t, tc.want, stderr.String())
		})
	}
}

// TestRun_Interrupted: a run the signal context cancelled exits 130 and
// prints nothing; the "error" is the interruption the user just caused.
func TestRun_Interrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := Run(ctx, []string{"--curio-home", filepath.Join(t.TempDir(), "home"), "daemon", "logs", "-f"},
		&stdout, &stderr)
	assert.Equal(t, 130, code)
	assert.Empty(t, stderr.String())
}

func TestVersion(t *testing.T) {
	srv := apitest.Start(t)
	out := mustRun(t, srv, "version")
	assert.Contains(t, out, "curio ")
	assert.Contains(t, out, "embedder: qwen3-embedding:0.6b/1024")
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

// TestAdd_WaitOnAClosedQueue: waiting on a fetch the queue won't start
// says why before it waits, and then waits as ever.
func TestAdd_WaitOnAClosedQueue(t *testing.T) {
	srv := apitest.Start(t)
	mustRun(t, srv, "pause")
	var stdout, stderr bytes.Buffer
	err := runCLIStreams(newRootCmdWith(testDeps(t)), srv.Home.Path, srv.URL, &stdout, &stderr, "add",
		"https://example.com/new", "--wait",
		"--wait-timeout", "1")
	require.EqualError(t, err, "timed out after 1s waiting for the fetch")
	assert.Equal(t, "note: nothing starts while the queue is paused (curio resume)\n", stderr.String())
	assert.Contains(t, stdout.String(), "added bookmark ")

	mustRun(t, srv, "resume")
	stderr.Reset()
	err = runCLIStreams(newRootCmdWith(testDeps(t)), srv.Home.Path, srv.URL, &stdout, &stderr, "add",
		"https://example.com/other", "--wait",
		"--wait-timeout", "1")
	require.EqualError(t, err, "timed out after 1s waiting for the fetch", "no daemon workers run here")
	assert.Empty(t, stderr.String(), "an open queue needs no note")
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
	assert.Contains(t, out, "\n         cause:  other\n         doc_id: "+failed.ID+"\n",
		"the cause, aligned with the lines below it")

	out = mustRun(t, srv, "docs", "--all")
	assert.Contains(t, out, "3 document(s)")

	out = mustRun(t, srv, "docs", "--state", "pending")
	assert.Contains(t, out, "https://example.com/pending")
	assert.Contains(t, out, "1 document(s)")

	out = mustRun(t, srv, "docs", "--state", "dead")
	assert.Contains(t, out, "no documents match")
	assert.NotContains(t, out, "next page:", "a single page has no next")
	out = mustRun(t, srv, "docs", "--state", "pending")
	assert.NotContains(t, out, "cause:", "only a failed or dead document has one")

	// Paging: the first page ends with the command for the second, which
	// keeps the filters and the home the first was run with.
	out = mustRun(t, srv, "docs", "--all", "--limit", "2")
	assert.Contains(t, out, "2 document(s)")
	args := nextPage(t, out)
	assert.Contains(t, args, "--all")
	assert.Contains(t, args, "--limit=2")
	assert.Contains(t, args, "--curio-home="+srv.Home.Path)
	out = runArgs(t, args...)
	assert.Contains(t, out, "1 document(s)")
	assert.NotContains(t, out, "next page:")
	for _, limit := range []string{"0", "501"} {
		_, err = runCLI(t, srv, "docs", "--limit", limit)
		require.ErrorContains(t, err, "--limit must be between 1 and 500")
	}
	_, err = runCLI(t, srv, "docs", "--state", "archived")
	require.ErrorContains(t, err, "pending, fetched, failed, dead", "a mistyped state names the valid ones")

	out = mustRun(t, srv, "docs", "show", fetched.ID)
	assert.Contains(t, out, "url:          https://example.com/fetched")
	assert.Contains(t, out, "markdown:     "+filepath.Join(srv.Home.ContentDir(), fetched.ID)+"/",
		"the daemon's absolute path, printed as given")
	assert.Contains(t, out, "state:        fetched")
	assert.NotContains(t, out, "cause:")
	assert.Contains(t, out, "fetcher:      apitest")
	assert.NotContains(t, out, "The fetched body.")

	out = mustRun(t, srv, "docs", "show", fetched.ID, "--content")
	assert.Contains(t, out, "--- content ---")
	assert.Contains(t, out, "The fetched body.")

	out = mustRun(t, srv, "docs", "show", failed.ID, "--content")
	assert.Contains(t, out, "state:        failed\ncause:        other\n")
	assert.Contains(t, out, "(no extracted content yet)", "the content 404 of a known document is an answer")

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

	out = mustRun(t, srv, "jobs", "--all", "--limit", "2")
	assert.Contains(t, out, "2 job(s)")
	out = runArgs(t, nextPage(t, out)...)
	assert.Contains(t, out, "1 job(s)")
	_, err = runCLI(t, srv, "jobs", "--limit", "501")
	require.ErrorContains(t, err, "--limit must be between 1 and 500")

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

	var cluster string
	require.NoError(t, srv.DB.QueryRow(`SELECT id FROM jobs WHERE kind = 'cluster'`).Scan(&cluster))
	out = mustRun(t, srv, "jobs", "show", cluster)
	assert.Contains(t, out, "pending  cluster")
	assert.Contains(t, out, cluster)
	assert.Contains(t, out, "payload: {}")
	assert.NotContains(t, out, "job(s)", "one job, not a list")
	_, err = runCLI(t, srv, "jobs", "show", "no-such-job")
	require.EqualError(t, err, `job "no-such-job" not found`)
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
	assert.Contains(t, out, "follow it: curio jobs show ")

	out = mustRun(t, srv, "refetch", "--all", "--state", "failed")
	assert.Contains(t, out, "refetch enqueued for documents in state=failed: 1 jobs")
	assert.Equal(t, 1, count(t, srv, `SELECT count(*) FROM jobs WHERE json_extract(payload, '$.document_id') = ?`, failed.ID))

	out = mustRun(t, srv, "refetch", "--all")
	assert.Contains(t, out, "refetch enqueued for all documents: 2 jobs")

	_, err = runCLI(t, srv, "refetch")
	require.ErrorContains(t, err, "provide a document ID or URL, or pass --all")
}

// TestRefetch_Cause: --cause refetches the documents that failed for it,
// with --state or without, and prints what it requeued; the daemon's 400s
// come through, and --state or --cause without --all is refused before
// the daemon is contacted.
func TestRefetch_Cause(t *testing.T) {
	srv := apitest.Start(t)
	blocked := []*store.Document{srv.AddFailedDocument(t, "https://a.example/1", store.FailureCauseAntiBot),
		srv.AddFailedDocument(t, "https://b.example/1", store.FailureCauseAntiBot)}
	slow := srv.AddFailedDocument(t, "https://c.example/1", store.FailureCauseTimeout)
	srv.AddFailedDocument(t, "https://d.example/1", store.FailureCauseDeadLink)
	jobsFor := func(doc *store.Document) int {
		t.Helper()
		return count(t, srv, `SELECT count(*) FROM jobs WHERE document_id = ?`, doc.ID)
	}

	_, err := runCLI(t, srv, "refetch", "--all", "--cause", "bogus")
	require.ErrorContains(t, err, `cause "bogus" must be one of: `+failureCauses)
	_, err = runCLI(t, srv, "refetch", "--all", "--cause", "dead_link")
	require.ErrorContains(t, err, "dead links are refetched only with state=dead")
	assert.Zero(t, count(t, srv, `SELECT count(*) FROM jobs`))

	out := mustRun(t, srv, "refetch", "--all", "--cause", "anti_bot")
	assert.Contains(t, out, "refetch enqueued for documents with cause=anti_bot: 2 jobs")
	for _, doc := range blocked {
		assert.Equal(t, 1, jobsFor(doc), doc.URL)
	}
	assert.Zero(t, jobsFor(slow))

	out = mustRun(t, srv, "refetch", "--all", "--state", "failed", "--cause", "timeout")
	assert.Contains(t, out, "refetch enqueued for documents in state=failed with cause=timeout: 1 jobs")
	out = mustRun(t, srv, "refetch", "--all", "--state", "dead", "--cause", "dead_link")
	assert.Contains(t, out, "refetch enqueued for documents in state=dead with cause=dead_link: 1 jobs")

	var requests atomic.Int32
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(daemon.Close)
	for _, args := range [][]string{
		{"refetch", blocked[0].ID, "--cause", "anti_bot"},
		{"refetch", blocked[0].ID, "--state", "failed"},
		{"refetch", "--cause", "anti_bot"},
	} {
		_, err := runCLIAt(t, srv.Home.Path, daemon.URL, args...)
		require.EqualError(t, err, "--state and --cause filter --all", strings.Join(args, " "))
	}
	assert.Zero(t, requests.Load(), "the daemon is never contacted")
	assert.Equal(t, 4, count(t, srv, `SELECT count(*) FROM jobs`), "nothing more is enqueued")
}

// TestRefetch_CauseHelp: --cause's help names every cause the store has, in
// its order.
func TestRefetch_CauseHelp(t *testing.T) {
	causes := store.FailureCauses()
	names := make([]string, len(causes))
	for i, c := range causes {
		names[i] = string(c)
	}
	assert.Equal(t, strings.Join(names, ", "), failureCauses)
	flag := newRefetchCmd(nil).Flags().Lookup("cause")
	require.NotNil(t, flag)
	assert.Contains(t, flag.Usage, failureCauses)
}

// TestDocumentByURL: refetch, reindex, docs show and related take the URL
// a document was bookmarked under, as typed or pasted, in place of its
// ID; a URL with no document says so and how to add it.
func TestDocumentByURL(t *testing.T) {
	srv := apitest.Start(t)
	failed := srv.AddDocument(t, "https://example.com/failed", store.DocStateFailed)
	fetched := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	srv.AddContent(t, fetched, "content")

	out := mustRun(t, srv, "refetch", "https://EXAMPLE.com/failed#top")
	assert.Contains(t, out, "refetch enqueued for document "+failed.ID)
	assert.Equal(t, 1, count(t, srv, `SELECT count(*) FROM jobs WHERE json_extract(payload, '$.document_id') = ?`, failed.ID))

	out = mustRun(t, srv, "reindex", "https://example.com/a?utm_source=x")
	assert.Contains(t, out, "reindex enqueued for document "+fetched.ID)

	out = mustRun(t, srv, "docs", "show", "https://example.com/a")
	assert.Contains(t, out, "id:           "+fetched.ID)

	_, err := runCLI(t, srv, "refetch", "https://example.com/unknown")
	require.EqualError(t, err, "no document for https://example.com/unknown in the library "+
		"(curio add https://example.com/unknown saves it)")
	_, err = runCLI(t, srv, "related", "https://example.com/unknown")
	require.ErrorContains(t, err, "no document for https://example.com/unknown")
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
	require.ErrorContains(t, err, "provide a document ID or URL, or pass --all")
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

// A finished run that found no interests is reported as such, never as a run
// still to do: rebuilding again would only repeat it.
func TestInterests_EmptyRun(t *testing.T) {
	t.Run("no documents", func(t *testing.T) {
		srv := apitest.Start(t)
		srv.AddEmptyClusterRun(t, 0)
		out := mustRun(t, srv, "interests")
		assert.Contains(t, out, "found no fetched, indexed documents")
		assert.Contains(t, out, "`curio status`")
		assert.NotContains(t, out, "no interests yet")
	})
	t.Run("nothing grouped", func(t *testing.T) {
		srv := apitest.Start(t)
		srv.AddEmptyClusterRun(t, 35)
		out := mustRun(t, srv, "interests")
		assert.Contains(t, out, "grouped none of its 35 documents")
		assert.Contains(t, out, "insight.min_similarity or insight.min_cluster_size in "+
			filepath.Join(srv.Home.Path, "config.yaml"))
		assert.NotContains(t, out, "no interests yet")
	})
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

// TestStatus_DaemonErrors: a daemon that answers healthz with an error is
// not reported as "not running".
func TestStatus_DaemonErrors(t *testing.T) {
	srv := apitest.Start(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"title":"internal error","status":500,"detail":"read marker: permission denied"}`)
	}))
	t.Cleanup(broken.Close)
	daemonBin(t)

	out, err := runCLIAt(t, srv.Home.Path, broken.URL, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "daemon:  not answering healthz: read marker: permission denied")
	assert.NotContains(t, out, "not running")

	out, err = runCLIAt(t, srv.Home.Path, broken.URL, "doctor")
	require.Error(t, err)
	line, _ := doctorLine(t, out, "daemon")
	assert.Contains(t, line, broken.URL+" answers, but healthz failed: read marker: permission denied")
}

// TestStatus_DaemonStarting: a daemon that is still starting is reported
// as such, with what it is doing, and status still shows what it can
// without the full API.
func TestStatus_DaemonStarting(t *testing.T) {
	srv := apitest.StartNotReady(t)
	srv.Startup.SetMigrating(6)
	srv.Startup.MigrationApplied()

	out, err := runCLI(t, srv, "status")
	require.NoError(t, err)
	assert.Contains(t, out, fmt.Sprintf("daemon:  starting  (pid %d, version ", os.Getpid()))
	assert.Contains(t, out, "): migrating the database, 1 of 6 migrations applied\n")
	assert.Contains(t, out, "home:    "+srv.Home.Path)
	assert.Contains(t, out, "disk:")
	assert.NotContains(t, out, "not answering healthz")
	assert.NotContains(t, out, "not running")
	assert.NotContains(t, out, "bookmarks:")
}

// TestDoctor_DaemonStarting: a daemon that is starting is a warning, not a
// failed check, and the embeddings, which it reports, are left for later.
func TestDoctor_DaemonStarting(t *testing.T) {
	w := upWorldFrom(t, apitest.StartNotReady)
	w.srv.Startup.SetMigrating(6)
	out, err := w.run(t, "doctor")
	require.NoError(t, err, out)
	line, hint := doctorLine(t, out, "daemon")
	assert.Equal(t, fmt.Sprintf("! %-22s starting (pid %d): migrating the database, 0 of 6 migrations applied, "+
		"managed by launchd agent %s", "daemon", os.Getpid(), agentLabel), line)
	assert.Equal(t, "wait for it; `curio daemon logs -f` follows it", hint)
	line, _ = doctorLine(t, out, "embeddings")
	assert.Equal(t, fmt.Sprintf("! %-22s not checked while the daemon starts", "embeddings"), line)
	// A starting daemon doesn't say yet whether it sends a GitHub token, so
	// config.yaml, which names none here, decides.
	line, _ = doctorLine(t, out, "github")
	assert.Equal(t, "!", markerOf(line), line)
	assert.Contains(t, out, "0 failure(s), 3 warning(s)")
}

// TestDoctor: with everything up, every check passes: the ones doctor
// shares with curio up, then its own.
func TestDoctor(t *testing.T) {
	w := upWorld(t)
	out, err := w.run(t, "doctor")
	require.NoError(t, err, out)
	for _, name := range []string{"machine", "curio home", "config", "ollama", "models", "daemon", "launchd",
		"embeddings", "github", "fetcher", "content dir"} {
		line, _ := doctorLine(t, out, name)
		assert.Equal(t, "✓", markerOf(line), line)
	}
	assert.Contains(t, out, "all checks passed")
	assert.NotContains(t, out, "jina", "a daemon that reports no upstreams gets no upstream check")
}

// TestDoctor_NoGitHubToken: a daemon whose GitHub fetcher sends no token
// is a warning that says what it costs and that any token will do, even
// one with no access, and where to put it.
func TestDoctor_NoGitHubToken(t *testing.T) {
	w := upWorld(t, func(d *api.Deps) { d.GitHubToken = false })
	out, err := w.run(t, "doctor")
	require.NoError(t, err, out)
	line, hint := doctorLine(t, out, "github")
	assert.Equal(t, "!", markerOf(line), line)
	assert.Contains(t, line, "no token: GitHub allows 60 API requests an hour")
	assert.Contains(t, hint, "even one that can access nothing")
	assert.Contains(t, hint, "a classic token with no scopes ticked")
	assert.Contains(t, hint, "Set fetcher.github.token in "+filepath.Join(w.home, "config.yaml"))
	assert.Contains(t, out, "0 failure(s), 1 warning(s)")
}

// TestDoctorAndStatus_EmbeddingDrift: while the daemon reports the build
// that makes the embeddings changed, doctor warns, listing each change and
// the fix, and status warns once; `curio reindex --all` resets the
// baseline, and the warnings go.
func TestDoctorAndStatus_EmbeddingDrift(t *testing.T) {
	monitor := apitest.NewDrift(time.Now(),
		drift.Change{What: drift.ModelDigest, Recorded: "sha256:0a109f42", Current: "sha256:ac6da0df"},
		drift.Change{What: drift.OllamaVersion, Recorded: "0.30.0", Current: "0.34.4"})
	w := upWorld(t, func(d *api.Deps) { d.Drift = monitor })
	w.srv.AddContent(t, w.srv.AddDocument(t, "https://example.com/a", store.DocStateFetched), "content")

	out, err := w.run(t, "doctor")
	require.NoError(t, err, out)
	assert.Contains(t, out, fmt.Sprintf("! %-22s drifted: model digest sha256:0a109f42 → sha256:ac6da0df, "+
		"Ollama 0.30.0 → 0.34.4\n", "embeddings"))
	assert.Contains(t, out, "  → searches compare vectors from two builds; run `curio reindex --all` to re-embed the library\n")
	assert.Contains(t, out, "0 failure(s), 1 warning(s)")

	out, err = w.run(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "embed:   qwen3-embedding:0.6b (dim 1024)\n"+
		"warning: embeddings drifted since the library was indexed (model digest sha256:0a109f42 → sha256:ac6da0df, "+
		"Ollama 0.30.0 → 0.34.4); run `curio reindex --all`\n")

	_, err = w.run(t, "reindex", "--all")
	require.NoError(t, err)
	out, err = w.run(t, "status")
	require.NoError(t, err)
	assert.NotContains(t, out, "drifted")
	out, err = w.run(t, "doctor")
	require.NoError(t, err, out)
	assert.Contains(t, out, fmt.Sprintf("✓ %-22s no drift reported since the library was indexed\n", "embeddings"))
	assert.Contains(t, out, "all checks passed")
}

func TestReindex_HelpNamesDrift(t *testing.T) {
	out := runArgs(t, "reindex", "--help")
	assert.Contains(t, out, "embeddings drifted")
	assert.Contains(t, out, "--all")
}

// TestDoctor_HomeTheDaemonRefuses: a home the daemon won't serve, a legacy
// one, one from a newer curio, one whose marker can't be read or one whose
// config.yaml asks for another embedding model, fails doctor's home check
// offline, with the daemon's own reason and fix, and the daemon check
// points there, with the remedy that fits the reason, not at starting a
// daemon that would only refuse it.
func TestDoctor_HomeTheDaemonRefuses(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	const fresh = "`curio up --fresh` sets the home aside and starts a new one"
	defaults := config.Default().Embedding
	cases := []struct {
		name   string
		setup  func(t *testing.T, home string)
		want   []string
		hint   func(home string) string
		models string // the models line's detail; empty when the models check looks at the home
	}{
		{"legacy", func(t *testing.T, home string) {
			require.NoError(t, os.WriteFile(filepath.Join(home, curiohome.MarkerFile), []byte(`{"schema_version":11,`+
				`"embedding_model":"nomic-embed-text","embedding_dim":768}`), 0o600))
		}, []string{"curio home from an older curio", `"nomic-embed-text" (dim 768)`, "`curio up --fresh`"},
			func(string) string { return fresh },
			"not checked: curio home from an older curio (see the curio home check)"},
		{"newer", func(t *testing.T, home string) {
			h, err := curiohome.Init(home, defaults.Model, defaults.Dim)
			require.NoError(t, err)
			meta, err := h.Meta()
			require.NoError(t, err)
			meta.Format = curiohome.CurrentFormat + 1
			require.NoError(t, h.WriteMeta(meta))
		}, []string{"curio home from a newer curio"},
			func(string) string { return "upgrade curio (`brew upgrade curio`)" },
			"not checked: curio home from a newer curio (see the curio home check)"},
		{"unreadable marker", func(t *testing.T, home string) {
			_, err := curiohome.Init(home, defaults.Model, defaults.Dim)
			require.NoError(t, err)
			marker := filepath.Join(home, curiohome.MarkerFile)
			require.NoError(t, os.Chmod(marker, 0))
			t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })
		}, []string{"its marker is unreadable", "permission denied"},
			func(home string) string {
				return "check the permissions of " + filepath.Join(home, curiohome.MarkerFile) + ", or " + fresh
			}, ""},
		{"mismatch", func(t *testing.T, home string) {
			_, err := curiohome.Init(home, defaults.Model, defaults.Dim)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(home, curiohome.ConfigFile),
				[]byte("embedding:\n  model: mxbai-embed-large\n"), 0o600))
		}, []string{"embedding model mismatch", `configured "mxbai-embed-large" (dim 1024)`,
			"Set embedding.model and embedding.dim back", "`curio up --fresh`"},
			func(string) string {
				return "set config.yaml back (embedding.model and embedding.dim as the marker records them), or " + fresh
			}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			tc.setup(t, home)
			daemonBin(t)

			out, err := runCLIAt(t, home, down.URL, "doctor")
			require.Error(t, err)
			line, _ := doctorLine(t, out, "curio home")
			assert.Equal(t, "✗", markerOf(line), out)
			for _, want := range tc.want {
				assert.Contains(t, line, want)
			}
			line, hint := doctorLine(t, out, "daemon")
			assert.Equal(t, "✗", markerOf(line))
			assert.Contains(t, line, "see the curio home and config checks")
			assert.Equal(t, tc.hint(home), hint)
			assert.NotContains(t, out, "curio daemon start")
			if tc.models != "" {
				line, hint = doctorLine(t, out, "models")
				assert.Equal(t, fmt.Sprintf("! %-22s %s", "models", tc.models), line)
				assert.Empty(t, hint, "nothing to pull for a home no daemon serves")
			}
		})
	}
}

// TestUp_HomePathNotAHome: a file, or a directory that isn't a curio
// home, where the home should be blocks curio up at the home check alone:
// config.yaml and the daemon aren't checked, planned, or offered to do.
func TestUp_HomePathNotAHome(t *testing.T) {
	worlds := map[string]func(t *testing.T, path string){
		"a file": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte("notes"), 0o600))
		},
		"a non-curio directory": func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(path, "notes.txt"), []byte("mine"), 0o600))
		},
	}
	for name, make := range worlds {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, embedModel, genModel)
			down := httptest.NewServer(http.NotFoundHandler())
			down.Close()
			w.home, w.daemonURL = filepath.Join(t.TempDir(), "curio"), down.URL
			make(t, w.home)
			daemonBin(t)

			code, stdout, _ := w.exit(t, "up", "--dry-run")
			assert.Equal(t, 1, code)
			assert.NotRegexp(t, `\d\. (home|daemon): `, stdout, "no config.yaml or daemon to make before the home")
			assert.Contains(t, stdout, "To fix by hand first:\n  ✗ home: ")
			assert.Contains(t, stdout, "  ! daemon: not checked: ")

			out, err := w.run(t, "doctor")
			require.Error(t, err)
			for _, check := range []string{"config", "daemon"} {
				line, hint := doctorLine(t, out, check)
				assert.Equal(t, "!", markerOf(line), line)
				assert.Contains(t, line, "not checked: ")
				assert.Contains(t, line, "(see the curio home check)")
				assert.Empty(t, hint, "curio up can't do it")
			}
		})
	}
}

// TestDoctor_MissingHome: doctor never creates a home to look at it: a
// missing one fails its check, pointing at curio up, and the rest still
// print.
func TestDoctor_MissingHome(t *testing.T) {
	w := newWorld(t)
	w.home = filepath.Join(t.TempDir(), "curio")
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	w.daemonURL = down.URL

	out, err := w.run(t, "doctor")
	require.Error(t, err)
	line, hint := doctorLine(t, out, "curio home")
	assert.Equal(t, fmt.Sprintf("✗ %-22s no curio home at %s", "curio home", w.home), line)
	assert.Contains(t, hint, "`curio up` does it: create a curio home at "+w.home)
	for _, name := range []string{"machine", "config", "ollama", "models", "daemon", "launchd", "embeddings", "fetcher"} {
		doctorLine(t, out, name)
	}
	assert.NotContains(t, out, "content dir", "no home, no content directory to check")
	assert.NoDirExists(t, w.home)
}

// TestDoctor_InvalidConfig: a config.yaml that doesn't load fails its
// check, with the load error and the file to edit, and every other check
// still prints.
func TestDoctor_InvalidConfig(t *testing.T) {
	w := upWorld(t)
	path := filepath.Join(w.home, curiohome.ConfigFile)
	require.NoError(t, os.WriteFile(path, []byte("daemon:\n  fetch_workers: 0\n"), 0o600))

	out, err := w.run(t, "doctor")
	require.Error(t, err)
	line, hint := doctorLine(t, out, "config")
	assert.Equal(t, "✗", markerOf(line))
	assert.Contains(t, line, "daemon.fetch_workers must be positive")
	assert.Contains(t, hint, "edit "+path)
	for _, name := range []string{"machine", "curio home", "ollama", "models", "daemon", "launchd", "embeddings",
		"fetcher", "content dir"} {
		doctorLine(t, out, name)
	}
}

// TestDoctorAndStatus_FailingJina: a daemon that reports its Jina fallback
// failing fails doctor, whose jina check says since when and what to do,
// and status warns once, after the embedding line.
func TestDoctorAndStatus_FailingJina(t *testing.T) {
	answered := time.Date(2026, 9, 27, 14, 2, 42, 0, time.UTC)
	failed := answered.Add(time.Hour)
	w := upWorld(t, func(d *api.Deps) {
		d.Upstreams = func() []fetcher.UpstreamHealth {
			return []fetcher.UpstreamHealth{{Name: "jina", Enabled: true, State: fetcher.UpstreamFailing,
				LastSuccess: answered, LastFailure: failed, LastFailureClass: fetcher.CallChallenged,
				Window: 15 * time.Minute, Recent: map[fetcher.CallClass]int{fetcher.CallChallenged: 1}}}
		}
	})

	out, err := w.run(t, "doctor")
	require.Error(t, err)
	assert.Contains(t, out, fmt.Sprintf("✗ %-22s failing: no answer since %s; last failure challenged at %s\n",
		"jina", localTime(answered), localTime(failed)))
	assert.Contains(t, out, "  → challenged: r.jina.ai is challenging curio; see Troubleshooting in docs/setup.md\n")
	assert.Contains(t, out, "1 failure(s), 0 warning(s)")

	out, err = w.run(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, fmt.Sprintf("embed:   qwen3-embedding:0.6b (dim %d)\n"+
		"warning: jina is failing: no answer since %s; last failure challenged; run `curio doctor`\n",
		1024, localTime(answered)))
	assert.Equal(t, 1, strings.Count(out, "jina"))
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

// TestImport_SafariNeedsFullDiskAccess: macOS refusing Safari's bookmarks,
// already at stat, is a permission to grant, never "not found".
func TestImport_SafariNeedsFullDiskAccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Safari")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Bookmarks.plist"), []byte("<plist/>"), 0o600))
	require.NoError(t, os.Chmod(dir, 0))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Setenv("CURIO_SAFARI_DIR", dir)

	_, err := runCLI(t, apitest.Start(t), "import", "safari", "--dry-run")
	require.ErrorContains(t, err, "Grant Full Disk Access to your terminal")
	assert.NotContains(t, err.Error(), "not found")
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
