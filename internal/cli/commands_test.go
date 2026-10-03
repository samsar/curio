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
	"github.com/samsar/curio/internal/insight"
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

	// A deferred job's last_error says what it waits for; it is no error.
	ctx := context.Background()
	deferred, err := store.NewDocumentJob(apitest.TenantID, store.JobKindFetch, doc.ID)
	require.NoError(t, err)
	require.NoError(t, srv.Deps.Queue.Enqueue(ctx, deferred))
	claimed, err := srv.Deps.Queue.ClaimNext(ctx, []store.JobKind{store.JobKindFetch})
	require.NoError(t, err)
	require.NoError(t, srv.Deps.Queue.Defer(ctx, claimed.ID, time.Now().Add(24*time.Minute),
		"waiting for GitHub's API rate limit to reset"))
	out = mustRun(t, srv, "jobs", "show", deferred.ID)
	assert.Contains(t, out, "pending  fetch      attempts=0")
	assert.Contains(t, out, "\n  last: waiting for GitHub's API rate limit to reset\n  next attempt: ")
	assert.NotContains(t, out, "err:")
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
	assert.Contains(t, out, "reindex enqueued for documents in state=fetched: 1 jobs\n"+
		"interests are regrouped from scratch once the re-embedding finishes; documents indexed meanwhile join them then\n")

	out = mustRun(t, srv, "reindex", "--all", "--state", "pending")
	assert.Contains(t, out, "documents in state=pending: 0 jobs")
	assert.NotContains(t, out, "interests", "no re-embedding of the library, no regrouping")

	require.NoError(t, os.WriteFile(srv.Home.ConfigPath(), []byte("insight:\n  enabled: false\n"), 0o600))
	out = mustRun(t, srv, "reindex", "--all")
	assert.Contains(t, out, "documents in state=fetched: 1 jobs")
	assert.NotContains(t, out, "interests", "no insight layer, no regrouping")

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

// TestInterests_Outline: in the areas shape, curio interests outlines
// the areas, each with its largest interests and the way to the rest, then
// Unsorted; IDs whole, ready to paste.
func TestInterests_Outline(t *testing.T) {
	srv := apitest.Start(t)
	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	loose := srv.AddDocument(t, "https://example.com/loose", store.DocStateFetched)
	unsorted := srv.AddDocument(t, "https://example.com/unsorted", store.DocStateFetched)
	children := make([]apitest.Interest, 0, 7)
	children = append(children, apitest.Interest{Label: "Agents", Size: 86, Members: []*store.Document{a},
		Loose: []*store.Document{loose}})
	for i := range 6 {
		children = append(children, apitest.Interest{Label: fmt.Sprintf("Topic %d", i), Size: 50 - i})
	}
	run := srv.AddRun(t, apitest.RunSpec{Areas: []apitest.Area{{Label: "Tech", Interests: children},
		{Interests: []apitest.Interest{{Size: 12}}}}, Unsorted: []*store.Document{unsorted}})
	srv.Place(t, run, run.Interests[0], srv.AddDocument(t, "https://example.com/new", store.DocStateFetched))
	srv.Scheduler.Set(insight.Snapshot{State: insight.StateCurrent, Changed: 1, RebuildAt: 20})

	out := mustRun(t, srv, "interests", "--children", "2")
	lines := strings.Split(out, "\n")
	assert.Equal(t, "2 areas, 8 interests across 385 documents (1 loose fit, 1 unsorted, 1 new)", lines[0])
	assert.Regexp(t, `^rebuilt \d{4}-\d\d-\d\d \d\d:\d\d \(fresh, manual\) · next after 20 changes, 1 so far$`, lines[1])
	assert.Contains(t, out, "\nTech — 371 docs, 7 interests  "+run.Areas[0]+"\n"+
		"    Agents  86  "+run.Interests[0]+"\n"+
		"    Topic 0  50  "+run.Interests[1]+"\n"+
		"    + 5 more: curio interests show "+run.Areas[0]+"\n")
	assert.Contains(t, out, "\n(unlabeled area) — 12 docs, 1 interest  "+run.Areas[1]+"\n"+
		"    (unlabeled)  12  "+run.Interests[7]+"\n")
	assert.True(t, strings.HasSuffix(out, "\nUnsorted — 1 doc: curio interests unsorted\n"), out)
	assert.NotContains(t, out, "min_similarity")

	out = mustRun(t, srv, "interests", "--limit", "1")
	assert.Contains(t, out, "+ 1 more: curio interests --offset 1\n")
	out = mustRun(t, srv, "interests", "--offset", "1")
	assert.NotContains(t, out, "Tech")
	assert.Contains(t, out, "(unlabeled area)")

	flat := mustRun(t, srv, "interests", "--flat", "--limit", "2", "--members", "1")
	assert.Contains(t, flat, "\nAgents — 86 docs  "+run.Interests[0]+"\n    in Tech  "+run.Areas[0]+"\n"+
		"    • https://example.com/a\n      doc_id: "+a.ID+"\n")
	assert.Contains(t, flat, "+ 6 more: curio interests --offset 2 --flat\n")
}

// TestInterests_Flat: in a library of one level, curio interests lists
// interests with their members, each with its doc_id and path.
func TestInterests_Flat(t *testing.T) {
	srv := apitest.Start(t)
	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	ext := srv.AddContent(t, a, "go")
	run := srv.AddInterest(t, "Go Programming", a, srv.AddDocument(t, "https://example.com/b", store.DocStateFetched))

	out := mustRun(t, srv, "interests", "--members", "2")
	assert.True(t, strings.HasPrefix(out, "1 interest across 2 documents (0 loose fits, 0 unsorted)\n"), out)
	assert.Contains(t, out, "\nGo Programming — 2 docs  "+run.Interests[0]+"\n"+
		"    • https://example.com/a\n      doc_id: "+a.ID+"\n"+
		"      path:   "+filepath.Join(srv.Home.ContentDir(), *ext.MarkdownPath)+"\n")
	assert.NotContains(t, out, "areas")
}

// TestInterests_Show: show lists an area's interests, an interest's
// members then its loose fits, and for a retired ID fails with what took
// its documents.
func TestInterests_Show(t *testing.T) {
	srv := apitest.Start(t)
	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	b := srv.AddDocument(t, "https://example.com/b", store.DocStateFetched)
	loose := srv.AddDocument(t, "https://example.com/loose", store.DocStateFetched)
	run := srv.AddAreas(t, apitest.Area{Label: "Programming", Interests: []apitest.Interest{
		{Label: "Go", Members: []*store.Document{a, b}, Loose: []*store.Document{loose}}, {Label: "Rust", Size: 1}}})
	area, goID := run.Areas[0], run.Interests[0]

	out := mustRun(t, srv, "interests", "show", area)
	assert.Equal(t, "Programming — area of 3 docs, 2 interests  "+area+"\n"+
		"About Programming.\n\n"+
		"    Go  2  "+goID+"\n"+
		"    Rust  1  "+run.Interests[1]+"\n", out)

	out = mustRun(t, srv, "interests", "show", goID)
	assert.Contains(t, out, "Go — 2 docs, 1 loose fit  "+goID+"\nin Programming  "+area+"\nAbout Go.\n")
	assert.Contains(t, out, "\nmembers:\n  • https://example.com/a\n    doc_id: "+a.ID+"\n  • https://example.com/b\n")
	assert.Contains(t, out, "\nloose fits:\n  • https://example.com/loose\n    doc_id: "+loose.ID+"\n")
	out = mustRun(t, srv, "interests", "show", goID, "--members", "1")
	assert.Contains(t, out, "+ 2 more: curio interests show "+goID+" --offset 1\n")
	out = mustRun(t, srv, "interests", "show", goID, "--offset", "2")
	assert.NotContains(t, out, "members:")
	assert.Contains(t, out, "loose fits:")
	newer := srv.AddDocument(t, "https://example.com/newer", store.DocStateFetched)
	srv.Place(t, run, goID, newer)
	out = mustRun(t, srv, "interests", "show", goID)
	assert.True(t, strings.HasSuffix(out, "\nnew since the last rebuild:\n  • https://example.com/newer\n"+
		"    doc_id: "+newer.ID+"\n"), out)

	split := srv.SplitInterest(t, run, goID, apitest.Interest{Label: "Go Web", Members: []*store.Document{a}},
		apitest.Interest{Label: "Go Tools", Members: []*store.Document{b}})
	_, err := runCLI(t, srv, "interests", "show", goID)
	require.Error(t, err)
	msg := err.Error()
	assert.Regexp(t, `^interest Go was retired by the rebuild of \d{4}-\d\d-\d\d \d\d:\d\d: its documents went to`, msg)
	for i, label := range []string{"Go Web", "Go Tools"} {
		assert.Contains(t, msg, "\n  "+label+" (split, 1 doc shared): curio interests show "+split.Interests[i])
	}

	_, err = runCLI(t, srv, "interests", "show", "no-such-interest")
	require.EqualError(t, err, `interest "no-such-interest" not found`)
}

// TestInterests_Unsorted lists the documents in no interest, each with
// the interest it is nearest.
func TestInterests_Unsorted(t *testing.T) {
	srv := apitest.Start(t)
	assert.Contains(t, mustRun(t, srv, "interests", "unsorted"), "no rebuild has finished yet")
	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	far := srv.AddDocument(t, "https://example.com/far", store.DocStateFetched)
	_, err := srv.DB.Exec(`UPDATE documents SET title = 'Far away' WHERE id = ?`, far.ID)
	require.NoError(t, err)
	run := srv.AddRun(t, apitest.RunSpec{Interests: []apitest.Interest{{Label: "Go", Members: []*store.Document{a}}},
		Unsorted: []*store.Document{far}})

	out := mustRun(t, srv, "interests", "unsorted")
	assert.Equal(t, "1 document in no interest, nearest first\n\n"+
		"• Far away\n  doc_id: "+far.ID+"\n  nearest: Go ("+run.Interests[0]+") 0.30\n", out)

	arrived := srv.AddDocument(t, "https://example.com/arrived", store.DocStateFetched)
	srv.Place(t, run, "", arrived)
	out = mustRun(t, srv, "interests", "unsorted")
	assert.True(t, strings.HasSuffix(out, "\n1 document new since the last rebuild, near no interest:\n\n"+
		"• https://example.com/arrived\n  doc_id: "+arrived.ID+"\n"), out)
}

// TestInterests_Changes: changes says what the latest rebuild did, a
// first grouping in a line.
func TestInterests_Changes(t *testing.T) {
	srv := apitest.Start(t)
	assert.Contains(t, mustRun(t, srv, "interests", "changes"), "no rebuild has finished yet")
	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	b := srv.AddDocument(t, "https://example.com/b", store.DocStateFetched)
	run := srv.AddInterest(t, "Go", a, b)
	out := mustRun(t, srv, "interests", "changes")
	assert.Regexp(t, `^rebuilt .* \(fresh, manual\)\nfirst grouping: every area and interest is new\n$`, out)

	split := srv.SplitInterest(t, run, run.Interests[0], apitest.Interest{Label: "Go Web", Members: []*store.Document{a}},
		apitest.Interest{Label: "Go Tools", Members: []*store.Document{b}})
	out = mustRun(t, srv, "interests", "changes")
	assert.Contains(t, out, "\nsplit:\n")
	for i := range 2 {
		assert.Contains(t, out, "  interest Go ("+run.Interests[0]+") → interest ")
		assert.Contains(t, out, "("+split.Interests[i]+"), 1 doc shared\n")
	}
	assert.NotContains(t, out, "first grouping")

	// A kept area has no event, but a rebuild that kept every area and
	// replaced every interest is no first grouping: each interest it
	// replaced was retired, which is an event.
	areas := srv.AddAreas(t, apitest.Area{Label: "Languages", Interests: []apitest.Interest{
		{Label: "Rust", Members: []*store.Document{a, b}}}})
	srv.SplitInterest(t, areas, areas.Interests[0], apitest.Interest{Label: "Rust Async", Members: []*store.Document{a}},
		apitest.Interest{Label: "Rust Macros", Members: []*store.Document{b}})
	out = mustRun(t, srv, "interests", "changes")
	assert.Contains(t, out, "\nsplit:\n")
	assert.NotContains(t, out, "first grouping")
}

// TestInterests_Rebuild: rebuild queues one, and asking again while it is
// queued answers the same job; --fresh makes it, or the next, fresh.
func TestInterests_Rebuild(t *testing.T) {
	srv := apitest.Start(t)
	out := mustRun(t, srv, "interests", "rebuild")
	id, ok := strings.CutPrefix(strings.Split(out, "\n")[0], "rebuild queued: job ")
	require.True(t, ok, out)
	assert.Contains(t, out, "follow it with `curio jobs show "+id+"`")
	assert.Equal(t, out, mustRun(t, srv, "interests", "rebuild", "--fresh"), "the job already queued")
	assert.Equal(t, 1, count(t, srv, `SELECT count(*) FROM jobs WHERE kind = 'cluster'`))
	st, err := srv.Deps.Insights.State(context.Background(), apitest.TenantID)
	require.NoError(t, err)
	assert.Equal(t, store.FreshManual, st.FreshOwed, "a fresh rebuild is owed")
}

// TestInterests_Empty: before a rebuild has grouped anything, curio
// interests says why from where rebuilds stand; a rebuild that found
// nothing says so.
func TestInterests_Empty(t *testing.T) {
	for name, tc := range map[string]struct {
		snap insight.Snapshot
		want string
	}{
		"none": {insight.Snapshot{State: insight.StateNone, Changed: 7, RebuildAt: 20},
			"no interests yet: the library is grouped on its own once 20 documents are indexed (7 so far)\n" +
				"`curio interests rebuild` groups it now\n"},
		"due": {insight.Snapshot{State: insight.StateDue, Changed: 25, RebuildAt: 20},
			"no interests yet: the first grouping is due (25 documents indexed), and starts once the library settles\n" +
				"`curio interests rebuild` groups it now\n"},
		"queued": {insight.Snapshot{State: insight.StateQueued},
			"your library is being grouped for the first time\n"},
		"rebuilding": {insight.Snapshot{State: insight.StateRebuilding},
			"your library is being grouped for the first time\n"},
		"held": {insight.Snapshot{State: insight.StateHeld, HeldReason: "the embeddings drifted"},
			"no interests yet: rebuilds are held: the embeddings drifted\n" +
				"run `curio reindex --all`; `curio interests rebuild` groups the library now anyway\n"},
		"failing": {insight.Snapshot{State: insight.StateFailing, LastError: "ollama unreachable",
			RetryAt: time.Now().Add(-time.Minute)},
			"the last rebuild failed: ollama unreachable; retrying once the library settles\n" +
				"once the cause is fixed, `curio interests rebuild` tries again without waiting\n"},
		"unknown": {insight.Snapshot{State: insight.StateUnknown},
			"no interests yet: `curio interests rebuild` groups the library now\n"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := apitest.Start(t)
			srv.Scheduler.Set(tc.snap)
			assert.True(t, strings.HasPrefix(mustRun(t, srv, "interests"), tc.want), name)
		})
	}
	t.Run("off", func(t *testing.T) {
		srv := apitest.Start(t, func(d *api.Deps) { d.InsightEnabled = false })
		assert.Equal(t, "interests are turned off: set insight.enabled: true in "+
			filepath.Join(srv.Home.Path, "config.yaml")+" and restart the daemon\n", mustRun(t, srv, "interests"))
	})
	t.Run("no documents", func(t *testing.T) {
		// A rebuild asked for before anything was fetched: the library is
		// regrouped on its own, unless a rebuild is already on its way.
		srv := apitest.Start(t)
		srv.AddRun(t, apitest.RunSpec{})
		hint := func(state insight.RebuildState) string {
			t.Helper()
			srv.Scheduler.Set(insight.Snapshot{State: state})
			lines := strings.Split(strings.TrimSuffix(mustRun(t, srv, "interests"), "\n"), "\n")
			require.Len(t, lines, 2)
			assert.Regexp(t, `^no interests: the rebuild of \d{4}-\d{2}-\d{2} \d{2}:\d{2} found no fetched, indexed documents$`, lines[0])
			return lines[1]
		}
		assert.Equal(t, "the library is regrouped on its own once enough documents are indexed; "+
			"`curio interests rebuild` regroups it now", hint(insight.StateCurrent))
		assert.Equal(t, "another rebuild is queued; follow it with `curio jobs --kind cluster --all`",
			hint(insight.StateQueued))
		assert.Equal(t, "another rebuild is running; follow it with `curio jobs --kind cluster --all`",
			hint(insight.StateRebuilding))
	})
	t.Run("nothing grouped", func(t *testing.T) {
		for n, want := range map[int]string{
			1: "grouped none of its 1 document; it is in Unsorted\n",
			2: "grouped none of its 2 documents; they are all in Unsorted\n",
		} {
			srv := apitest.Start(t)
			var unsorted []*store.Document
			for i := range n {
				unsorted = append(unsorted, srv.AddDocument(t, fmt.Sprintf("https://example.com/%d", i), store.DocStateFetched))
			}
			srv.AddRun(t, apitest.RunSpec{Unsorted: unsorted})
			out := mustRun(t, srv, "interests")
			assert.Contains(t, out, want)
			assert.Contains(t, out, "list them with `curio interests unsorted`\n")
			assert.NotContains(t, out, "min_similarity")
		}
	})
}

// TestInterests_HelpDescribesRebuilds: the help says rebuilds are
// automatic, and that rebuild means now.
func TestInterests_HelpDescribesRebuilds(t *testing.T) {
	out := runArgs(t, "interests", "--help")
	assert.Contains(t, out, "Rebuilds are automatic: the daemon groups the library once\n20 documents are indexed")
	assert.Contains(t, out, "`curio interests rebuild` rebuilds now")
	out = runArgs(t, "interests", "rebuild", "--help")
	assert.Contains(t, out, "Queue a rebuild of the interests now, rather than when the daemon would on its\nown")
	assert.Contains(t, out, "--fresh")
}

// TestStatus_Interests: status says in a line where automatic rebuilds of
// the interests stand, and nothing before the daemon's first check.
func TestStatus_Interests(t *testing.T) {
	hourAgo := time.Now().Add(-2*time.Hour - time.Minute)
	retry := time.Now().Add(time.Hour)
	for name, tc := range map[string]struct {
		snap insight.Snapshot
		want string
	}{
		"current": {insight.Snapshot{State: insight.StateCurrent, LastRebuildAt: hourAgo, LastKind: store.RunKindWarm,
			Changed: 37, RebuildAt: 276}, "interests: rebuilt 2 h ago (warm) · 37 documents changed, next at 276"},
		"none": {insight.Snapshot{State: insight.StateNone, Changed: 7, RebuildAt: 20},
			"interests: waiting for 20 indexed documents (7 so far)"},
		"due": {insight.Snapshot{State: insight.StateDue, LastRebuildAt: hourAgo, Changed: 271, RebuildAt: 263},
			"interests: a rebuild is due (271 changed, threshold 263): waiting for the library to settle"},
		"due, re-embedding": {insight.Snapshot{State: insight.StateDue, LastRebuildAt: hourAgo,
			FreshOwed: string(store.FreshReindex)},
			"interests: a fresh rebuild is due: waiting for the re-embedding to finish"},
		"queued":     {insight.Snapshot{State: insight.StateQueued}, "interests: a rebuild is queued"},
		"rebuilding": {insight.Snapshot{State: insight.StateRebuilding}, "interests: rebuilding"},
		"held": {insight.Snapshot{State: insight.StateHeld, HeldReason: "the embeddings drifted"},
			"interests: rebuilds held: the embeddings drifted; run `curio reindex --all`"},
		"failing": {insight.Snapshot{State: insight.StateFailing, LastError: "ollama unreachable", RetryAt: retry},
			"interests: the last rebuild failed (ollama unreachable); retrying at " + clockTime(retry, time.Now())},
	} {
		t.Run(name, func(t *testing.T) {
			srv := apitest.Start(t)
			srv.Scheduler.Set(tc.snap)
			out := mustRun(t, srv, "status")
			assert.Contains(t, out, "\n"+tc.want+"\n")
			assert.Equal(t, 1, strings.Count(out, "interests:"))
		})
	}
	off := apitest.Start(t, func(d *api.Deps) { d.InsightEnabled = false })
	assert.Contains(t, mustRun(t, off, "status"), "\ninterests: off (insight.enabled: false)\n")
	unknown := apitest.Start(t)
	unknown.Scheduler.Set(insight.Snapshot{State: insight.StateUnknown})
	assert.NotContains(t, mustRun(t, unknown, "status"), "interests:", "the daemon hasn't checked yet")
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
		"embeddings", "interests", "github", "fetcher", "content dir"} {
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
	assert.Contains(t, line, "no token: GitHub allows 60 API requests an hour, so github.com pages wait for its "+
		"hourly limit (about 30 repositories an hour)")
	assert.NotContains(t, line, "fail")
	assert.Contains(t, hint, "even one that can access nothing")
	assert.Contains(t, hint, "a classic token with no scopes ticked")
	assert.Contains(t, hint, "Set fetcher.github.token in "+filepath.Join(w.home, "config.yaml"))
	assert.Contains(t, out, "0 failure(s), 1 warning(s)")
}

// TestDoctorAndStatus_EmbeddingDrift: while the daemon reports a drift,
// doctor warns, listing each change, the daemon's evidence and the fix,
// and status warns once, saying how sure the drift is; `curio reindex
// --all` resets the baseline, and the warnings go.
func TestDoctorAndStatus_EmbeddingDrift(t *testing.T) {
	const changed = "model digest sha256:0a109f42 → sha256:ac6da0df, Ollama 0.30.0 → 0.34.4"
	for name, tc := range map[string]struct {
		unverified     string // the reason, for a drift no sample verified
		doctor, status string
	}{
		"verified": {
			doctor: "drifted: " + changed + "; 64 of 64 sampled chunks changed (worst cosine 0.9713)",
			status: "warning: embeddings drifted since the library was indexed (" + changed +
				"; 64 of 64 sampled chunks changed (worst cosine 0.9713)); run `curio reindex --all`",
		},
		"unverified": {
			unverified: "after 3 attempts: ollama unreachable",
			doctor:     "may have drifted: " + changed + "; not verified: after 3 attempts: ollama unreachable",
			status: "warning: embeddings may have drifted since the library was indexed (" + changed +
				"; not verified: after 3 attempts: ollama unreachable); run `curio reindex --all`",
		},
	} {
		t.Run(name, func(t *testing.T) {
			monitor := apitest.NewDrift(time.Now(),
				drift.Change{What: drift.ModelDigest, Recorded: "sha256:0a109f42", Current: "sha256:ac6da0df"},
				drift.Change{What: drift.OllamaVersion, Recorded: "0.30.0", Current: "0.34.4"})
			if tc.unverified != "" {
				monitor.Unverified(tc.unverified)
			}
			w := upWorld(t, func(d *api.Deps) { d.Drift = monitor })
			w.srv.AddContent(t, w.srv.AddDocument(t, "https://example.com/a", store.DocStateFetched), "content")

			out, err := w.run(t, "doctor")
			require.NoError(t, err, out)
			assert.Contains(t, out, fmt.Sprintf("! %-22s %s\n", "embeddings", tc.doctor))
			assert.Contains(t, out,
				"  → searches compare vectors from two builds; run `curio reindex --all` to re-embed the library\n")
			assert.Contains(t, out, "0 failure(s), 1 warning(s)")

			out, err = w.run(t, "status")
			require.NoError(t, err)
			assert.Contains(t, out, "embed:   qwen3-embedding:0.6b (dim 1024)\n"+tc.status+"\n")
			assert.Equal(t, 1, strings.Count(out, "warning: embeddings"))

			_, err = w.run(t, "reindex", "--all")
			require.NoError(t, err)
			out, err = w.run(t, "status")
			require.NoError(t, err)
			assert.NotContains(t, out, "drifted")
			out, err = w.run(t, "doctor")
			require.NoError(t, err, out)
			assert.Contains(t, out, fmt.Sprintf("✓ %-22s no drift reported since the library was indexed\n", "embeddings"))
			assert.Contains(t, out, "all checks passed")
		})
	}
}

// TestDoctor_Interests: doctor checks where automatic rebuilds stand:
// held and failing warn, each with its fix; any other state passes,
// saying it.
func TestDoctor_Interests(t *testing.T) {
	for name, tc := range map[string]struct {
		snap         insight.Snapshot
		marker, line string
		hint         string
	}{
		"current": {insight.Snapshot{State: insight.StateCurrent, LastRebuildAt: time.Now().Add(-5 * time.Minute),
			LastKind: store.RunKindFresh, Changed: 0, RebuildAt: 263}, "✓",
			"rebuilt 5 min ago (fresh) · 0 documents changed, next at 263", ""},
		"none": {insight.Snapshot{State: insight.StateNone, RebuildAt: 20}, "✓",
			"waiting for 20 indexed documents (0 so far)", ""},
		"held": {insight.Snapshot{State: insight.StateHeld, HeldReason: "the embeddings may have drifted"}, "!",
			"rebuilds held: the embeddings may have drifted",
			"run `curio reindex --all`: the interests are regrouped once the re-embedding finishes"},
		"failing": {insight.Snapshot{State: insight.StateFailing, LastError: "boom",
			RetryAt: time.Now().Add(-time.Minute)}, "!",
			"the last rebuild failed: boom; retrying once the library settles",
			"`curio daemon logs` has the details; once the cause is fixed, `curio interests rebuild` tries again without waiting"},
	} {
		t.Run(name, func(t *testing.T) {
			w := upWorld(t)
			w.srv.Scheduler.Set(tc.snap)
			out, _ := w.run(t, "doctor")
			line, hint := doctorLine(t, out, "interests")
			assert.Equal(t, tc.marker, markerOf(line), line)
			assert.Contains(t, line, tc.line)
			assert.Equal(t, tc.hint, hint)
		})
	}
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
