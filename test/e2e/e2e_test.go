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
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/migrations"
)

// daemonBin is the curio-daemon TestMain builds for this run.
var daemonBin string

func TestMain(m *testing.M) {
	os.Exit(buildAndRun(m))
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

// fakeOllama answers /api/tags with the default embedding model and
// /api/embed with deterministic vectors, counting document and query
// embeddings by their task prefix.
type fakeOllama struct {
	t                      *testing.T
	model                  string
	docEmbeds, queryEmbeds atomic.Int32
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/tags":
		fmt.Fprintf(w, `{"models":[{"name":%q,"model":%q}]}`, f.model+":latest", f.model+":latest")
	case "/api/embed":
		var req struct {
			Input []string `json:"input"`
		}
		if !assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&req)) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var resp struct {
			Embeddings [][]float32 `json:"embeddings"`
		}
		for _, text := range req.Input {
			if strings.HasPrefix(text, "search_query: ") {
				f.queryEmbeds.Add(1)
			} else {
				f.docEmbeds.Add(1)
			}
			resp.Embeddings = append(resp.Embeddings, embed(text))
		}
		assert.NoError(f.t, json.NewEncoder(w).Encode(resp))
	default:
		http.NotFound(w, r)
	}
}

// embed is a bag-of-words embedding: each word adds weight to one hashed
// dimension, so texts that share words land close together. Unit length.
func embed(text string) []float32 {
	v := make([]float32, store.EmbeddingDim)
	for word := range strings.FieldsSeq(strings.ToLower(text)) {
		v[crc32.ChecksumIEEE([]byte(word))%store.EmbeddingDim]++
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
	ollama := &fakeOllama{t: t, model: config.Default().Embedding.Model}
	ollamaSrv := httptest.NewServer(ollama)
	t.Cleanup(ollamaSrv.Close)
	pages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, articleHTML())
	}))
	t.Cleanup(pages.Close)

	listen := freeLoopbackAddr(t)
	home := newHome(t, listen, ollamaSrv.URL)
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
