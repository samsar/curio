//go:build e2e

// Package e2e drives the real curio-daemon binary the way the CLI does:
// started and stopped through daemonctl, talked to through the client, with
// a bookmark going through fetch, index and search. Ollama and the web are
// httptest fakes, so the test needs neither.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/setup/setuptest"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/migrations"
)

// daemonBin is the curio-daemon TestMain builds for this run.
var daemonBin string

func TestMain(m *testing.M) {
	os.Exit(setuptest.WithoutBrowsers(func() int { return buildAndRun(m) }))
}

func buildAndRun(m *testing.M) int {
	dir, err := os.MkdirTemp("", "curio-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer os.RemoveAll(dir)

	gomod, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: find the module root:", err)
		return 1
	}
	daemonBin = filepath.Join(dir, "curio-daemon")
	build := exec.Command("go", "build", "-tags=sqlite_fts5,sqlite_json", "-o", daemonBin, "./cmd/curio-daemon")
	build.Dir = filepath.Dir(strings.TrimSpace(string(gomod)))
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: build curio-daemon:", err)
		return 1
	}
	return m.Run()
}

// fakeOllama is Ollama at version with the default embedding model pulled
// as digest: /api/version, /api/tags, and /api/embed with deterministic
// vectors dim wide, counting document and query embeddings (a query
// carries the default query instruction).
type fakeOllama struct {
	t                      *testing.T
	model                  string
	dim                    int
	digest, version        string
	docEmbeds, queryEmbeds atomic.Int32
}

// Builds of Ollama and of the embedding model the fake can serve.
const (
	digestA = "sha256:0a109f422b47e3a30ba2b10eca18548e944e8a23073ee3f3e947efcf3c45e59f"
	digestB = "sha256:ac6da0dfba84d0b5b1f23d8e5b8b0e0e4f1b1e3c2f4a5b6c7d8e9f0a1b2c3d4e"
)

// serveOllama starts a fakeOllama at version, with the default embedding
// model pulled as digest, until the test ends, and returns it and its URL.
func serveOllama(t *testing.T, digest, version string) (*fakeOllama, string) {
	t.Helper()
	defaults := config.Default().Embedding
	f := &fakeOllama{t: t, model: defaults.Model, dim: defaults.Dim, digest: digest, version: version}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/version":
		fmt.Fprintf(w, `{"version":%q}`, f.version)
	case "/api/tags":
		fmt.Fprintf(w, `{"models":[{"name":%q,"model":%q,"digest":%q}]}`, f.model, f.model, f.digest)
	case "/api/embed":
		req, ok := f.embedRequest(w, r)
		if !ok {
			return
		}
		var resp struct {
			Embeddings [][]float32 `json:"embeddings"`
		}
		for _, text := range req.Input {
			if strings.HasPrefix(text, config.Default().Embedding.QueryPrefix) {
				f.queryEmbeds.Add(1)
			} else {
				f.docEmbeds.Add(1)
			}
			resp.Embeddings = append(resp.Embeddings, embed(text, f.dim))
		}
		assert.NoError(f.t, json.NewEncoder(w).Encode(resp))
	default:
		http.NotFound(w, r)
	}
}

// embedRequest reads an /api/embed request as the daemon must send it:
// truncate false, which Ollama defaults to true, and a keep_alive. Without
// truncate false it answers 400, as a real Ollama would for an input past
// the context, so a daemon that let Ollama truncate fails the test.
func (f *fakeOllama) embedRequest(w http.ResponseWriter, r *http.Request) (embedRequest, bool) {
	var req embedRequest
	body, err := io.ReadAll(r.Body)
	if !assert.NoError(f.t, err) {
		http.Error(w, `{"error":"unreadable request"}`, http.StatusBadRequest)
		return req, false
	}
	var raw map[string]json.RawMessage
	if !assert.NoError(f.t, json.Unmarshal(body, &raw)) || !assert.NoError(f.t, json.Unmarshal(body, &req)) {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return req, false
	}
	if string(raw["truncate"]) != "false" {
		http.Error(w, `{"error":"the input length exceeds the context length"}`, http.StatusBadRequest)
		return req, false
	}
	assert.NotEmpty(f.t, raw["keep_alive"], "every embed request keeps the model loaded")
	return req, true
}

// embedRequest is the part of an /api/embed request the fake embeds.
type embedRequest struct {
	Input []string `json:"input"`
}

// embed is a dim-wide bag-of-words embedding: each word adds weight to one
// hashed dimension, so texts that share words land close together. Unit
// length.
func embed(text string, dim int) []float32 {
	v := make([]float32, dim)
	for word := range strings.FieldsSeq(strings.ToLower(text)) {
		v[crc32.ChecksumIEEE([]byte(word))%uint32(dim)]++
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= norm
	}
	return v
}

// distinctive is a word only the article uses.
const distinctive = "zymurgy"

// articleHTML is a page the native fetcher extracts as an article: a real
// title and several paragraphs of prose.
func articleHTML() string {
	var body strings.Builder
	for i := range 6 {
		fmt.Fprintf(&body, "<p>Paragraph %d on %s, the chemistry of fermentation. Brewers who study %s "+
			"learn how yeast turns sugar into alcohol and carbon dioxide, why temperature changes the "+
			"flavour of the result, and how patience at each stage decides whether a batch is worth "+
			"keeping. This section covers the next step of the process in some detail.</p>\n", i+1, distinctive, distinctive)
	}
	return "<!doctype html><html lang=\"en\"><head><title>An Introduction to Zymurgy</title></head>" +
		"<body><article><h1>An Introduction to Zymurgy</h1>\n" + body.String() + "</article></body></html>"
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// newHome initializes a CURIO_HOME for a daemon on listen that embeds with
// the fake Ollama at ollamaURL and pulls nothing.
func newHome(t *testing.T, listen, ollamaURL string) *curiohome.Home {
	t.Helper()
	defaults := config.Default().Embedding
	home, err := curiohome.Init(t.TempDir(), defaults.Model, defaults.Dim)
	require.NoError(t, err)
	cfg := fmt.Sprintf(`daemon:
  listen: %q
  fetch_workers: 1
  index_workers: 1
embedding:
  base_url: %q
  auto_pull: false
generation:
  auto_pull: false
insight:
  labeling: terms
fetcher:
  native:
    backend: stock
    jina_fallback: false
`, listen, ollamaURL)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	return home
}

// logTail returns the end of the daemon's log, for a failure message.
func logTail(home *curiohome.Home) string {
	b, err := os.ReadFile(home.DaemonLogPath())
	if err != nil {
		return "(no daemon log: " + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-30):], "\n")
}

func TestDaemon_BookmarkIsFetchedIndexedAndFound(t *testing.T) {
	ctx := context.Background()
	ollama, ollamaURL := serveOllama(t, digestA, "0.34.4")
	pages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, articleHTML())
	}))
	t.Cleanup(pages.Close)

	listen := freeLoopbackAddr(t)
	home := newHome(t, listen, ollamaURL)
	baseURL := "http://" + listen
	ctl := daemonctl.New(home, daemonBin, baseURL)
	t.Cleanup(func() {
		// Whatever happened above, don't leave a daemon behind.
		if st, err := ctl.Status(context.Background()); err == nil && st.State == daemonctl.Running && st.PID > 0 {
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	})

	require.NoError(t, ctl.EnsureRunning(ctx), logTail(home))
	st, err := ctl.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, daemonctl.Running, st.State)
	c := client.New(baseURL)
	health, err := c.Healthz(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.PID, health.PID, "healthz names the daemon holding the lock")
	assert.True(t, daemonctl.SameHome(home.Path, health.Home), "served %s", health.Home)
	assert.Nil(t, health.EmbeddingDrift)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		meta, err := home.Meta()
		require.NoError(collect, err)
		assert.Equal(collect, digestA, meta.EmbeddingModelDigest)
		assert.Equal(collect, "0.34.4", meta.OllamaVersion)
	}, 10*time.Second, 50*time.Millisecond, "the daemon records the build that makes the embeddings")

	created, err := c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: pages.URL + "/zymurgy"})
	require.NoError(t, err)
	require.NotNil(t, created.Bookmark.DocumentID)
	docID := *created.Bookmark.DocumentID

	var doc *client.Document
	finished := assert.Eventually(t, func() bool {
		d, err := c.GetDocument(ctx, docID)
		if err != nil || d.State == string(store.DocStatePending) {
			return false
		}
		doc = d
		return true
	}, 30*time.Second, 50*time.Millisecond)
	if !finished || doc.State != string(store.DocStateFetched) {
		t.Logf("document %s: %+v\ndaemon log:\n%s", docID, doc, logTail(home))
		t.FailNow()
	}

	res, err := c.Search(ctx, client.SearchRequest{Query: distinctive})
	require.NoError(t, err)
	require.NotEmpty(t, res.Items)
	assert.Equal(t, docID, res.Items[0].Document.ID)
	assert.Equal(t, doc.URL, res.Items[0].Document.URL)
	assert.False(t, res.Degraded, "semantic search ran: %v", res.Warnings)
	assert.Positive(t, ollama.docEmbeds.Load(), "the index job embedded the article")
	assert.Positive(t, ollama.queryEmbeds.Load(), "the search embedded the query")

	stopped, err := ctl.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
	st, err = ctl.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, daemonctl.NotRunning, st.State)
	pidFile, err := os.ReadFile(home.PIDFile())
	require.NoError(t, err)
	assert.Empty(t, pidFile, "a clean exit empties the PID file")
}

// TestDaemon_PauseHoldsTheQueueAcrossARestart: a pause set on one daemon
// holds on the next one the home starts, so a bookmark added to it isn't
// fetched until the queue is resumed.
func TestDaemon_PauseHoldsTheQueueAcrossARestart(t *testing.T) {
	ctx := context.Background()
	_, ollamaURL := serveOllama(t, digestA, "0.34.4")
	pages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, articleHTML())
	}))
	t.Cleanup(pages.Close)

	listen := freeLoopbackAddr(t)
	home := newHome(t, listen, ollamaURL)
	baseURL := "http://" + listen
	ctl := daemonctl.New(home, daemonBin, baseURL)
	t.Cleanup(func() {
		if st, err := ctl.Status(context.Background()); err == nil && st.State == daemonctl.Running && st.PID > 0 {
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	})
	c := client.New(baseURL)

	require.NoError(t, ctl.EnsureRunning(ctx), logTail(home))
	_, err := c.UpdateQueue(ctx, client.QueueUpdate{Paused: new(true)})
	require.NoError(t, err)
	stopped, err := ctl.Stop(ctx)
	require.NoError(t, err)
	require.True(t, stopped)

	require.NoError(t, ctl.EnsureRunning(ctx), logTail(home))
	q, err := c.Queue(ctx)
	require.NoError(t, err)
	assert.True(t, q.Paused)
	assert.Equal(t, client.QueueClosed, q.State)
	assert.Equal(t, client.ReasonPaused, q.Reason)

	created, err := c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: pages.URL + "/zymurgy"})
	require.NoError(t, err)
	require.NotNil(t, created.Bookmark.DocumentID)
	require.NotEmpty(t, created.JobID)
	docID := *created.Bookmark.DocumentID
	assert.Never(t, func() bool {
		doc, err := c.GetDocument(ctx, docID)
		if !assert.NoError(t, err) {
			return true
		}
		job, err := c.GetJob(ctx, created.JobID)
		if !assert.NoError(t, err) {
			return true
		}
		return doc.State != string(store.DocStatePending) || job.Status != string(store.JobStatusPending)
	}, time.Second, 50*time.Millisecond, "the fetch waits while the queue is paused")

	_, err = c.UpdateQueue(ctx, client.QueueUpdate{Paused: new(false)})
	require.NoError(t, err)
	fetched := assert.Eventually(t, func() bool {
		doc, err := c.GetDocument(ctx, docID)
		return err == nil && doc.State == string(store.DocStateFetched)
	}, 30*time.Second, 50*time.Millisecond)
	if !fetched {
		t.Logf("daemon log:\n%s", logTail(home))
		t.FailNow()
	}

	stopped, err = ctl.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
}

// TestDaemon_ReportsEmbeddingDriftUntilReindexed: a library indexed under
// one build of the embedding model and Ollama, served by another, is
// reported drifted, and the daemon reindexes nothing by itself. `reindex
// --all` re-embeds it and makes the build serving now the baseline.
func TestDaemon_ReportsEmbeddingDriftUntilReindexed(t *testing.T) {
	ctx := context.Background()
	_, ollamaURL := serveOllama(t, digestB, "0.34.4")
	pages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, articleHTML())
	}))
	t.Cleanup(pages.Close)
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen, ollamaURL)
	meta, err := home.Meta()
	require.NoError(t, err)
	meta.EmbeddingModelDigest, meta.OllamaVersion = digestA, "0.30.0"
	require.NoError(t, home.WriteMeta(meta))
	baseURL := "http://" + listen
	ctl := daemonctl.New(home, daemonBin, baseURL)
	t.Cleanup(func() {
		if st, err := ctl.Status(context.Background()); err == nil && st.State == daemonctl.Running && st.PID > 0 {
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	})
	c := client.New(baseURL)

	require.NoError(t, ctl.EnsureRunning(ctx), logTail(home))
	var health *client.Health
	require.Eventually(t, func() bool {
		health, err = c.Healthz(ctx)
		return err == nil && health.EmbeddingDrift != nil
	}, 10*time.Second, 50*time.Millisecond, logTail(home))
	assert.Equal(t, "ok", health.Status)
	assert.Equal(t, []client.DriftChange{
		{What: client.DriftModelDigest, Recorded: digestA, Current: digestB},
		{What: client.DriftOllamaVersion, Recorded: "0.30.0", Current: "0.34.4"},
	}, health.EmbeddingDrift.Changes)
	assert.Equal(t, "curio reindex --all", health.EmbeddingDrift.Fix)

	// A document indexed under the drift: the only index job is its own.
	created, err := c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: pages.URL + "/zymurgy"})
	require.NoError(t, err)
	require.NotNil(t, created.Bookmark.DocumentID)
	require.Eventually(t, func() bool {
		d, err := c.GetDocument(ctx, *created.Bookmark.DocumentID)
		return err == nil && d.State == string(store.DocStateFetched)
	}, 30*time.Second, 50*time.Millisecond, logTail(home))
	assert.Equal(t, 1, indexJobs(t, home), "the daemon never reindexes by itself")

	reindexed, err := c.ReindexAll(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 1, reindexed.JobsEnqueued)
	// The reindex clears the baseline before it answers; the check it
	// triggers records the build now serving in its own time.
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		meta, err := home.Meta()
		require.NoError(collect, err)
		assert.Equal(collect, digestB, meta.EmbeddingModelDigest)
		assert.Equal(collect, "0.34.4", meta.OllamaVersion)
	}, 10*time.Second, 50*time.Millisecond, "reindex-all makes the build serving now the baseline")
	health, err = c.Healthz(ctx)
	require.NoError(t, err)
	assert.Nil(t, health.EmbeddingDrift)

	stopped, err := ctl.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
}

// indexJobs counts the index jobs in home's database, which a running
// daemon's WAL lets another connection read.
func indexJobs(t *testing.T, home *curiohome.Home) int {
	t.Helper()
	db, err := sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = ?`, store.JobKindIndex).Scan(&n))
	return n
}

// migratedTo brings home's database to version, as an older curio would
// have left it, and returns the newest version.
func migratedTo(t *testing.T, home *curiohome.Home, version int64) (latest int) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitestore.Open(ctx, home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, version)
	require.NoError(t, err)
	sources := p.ListSources()
	return int(sources[len(sources)-1].Version) // ListSources sorts by version
}

// TestDaemon_WaitsOutAMigration: starting a daemon whose migration takes
// longer than StartTimeout succeeds, because the daemon answers as starting
// throughout, and the caller hears once that it is migrating. The test
// keeps the daemon in its first migration by holding the database's write
// lock for 3s, within the 5s busy_timeout the migration waits for it.
func TestDaemon_WaitsOutAMigration(t *testing.T) {
	ctx := context.Background()
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen, "http://127.0.0.1:1")
	latest := migratedTo(t, home, 4)

	holder, err := sqlitestore.Open(ctx, home.DBPath())
	require.NoError(t, err)
	tx, err := holder.BeginTx(ctx, nil) // BEGIN IMMEDIATE: the write lock
	require.NoError(t, err)
	unlocked := make(chan struct{})
	time.AfterFunc(3*time.Second, func() {
		defer close(unlocked)
		assert.NoError(t, tx.Rollback())
		assert.NoError(t, holder.Close())
	})
	t.Cleanup(func() { <-unlocked })

	baseURL := "http://" + listen
	ctl := daemonctl.New(home, daemonBin, baseURL)
	ctl.StartTimeout = time.Second
	var told atomic.Int32
	ctl.OnMigrating = func(client.Startup) { told.Add(1) }
	t.Cleanup(func() {
		if st, err := ctl.Status(context.Background()); err == nil && st.State == daemonctl.Running && st.PID > 0 {
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	})

	start := time.Now()
	require.NoError(t, ctl.EnsureRunning(ctx), logTail(home))
	assert.Greater(t, time.Since(start), ctl.StartTimeout, "the migration outlasted StartTimeout")
	assert.EqualValues(t, 1, told.Load(), "told once that the daemon is migrating")
	health, err := client.New(baseURL).Healthz(ctx)
	require.NoError(t, err)
	assert.Equal(t, latest, health.SchemaVersion)

	stopped, err := ctl.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
}
