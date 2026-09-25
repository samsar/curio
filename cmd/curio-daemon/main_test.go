package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// newHome initializes a CURIO_HOME for run() with a config that keeps the
// daemon offline: Ollama unreachable, nothing auto-pulled, term labels.
func newHome(t *testing.T, listen string) *curiohome.Home {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CURIO_HOME", dir)
	home, err := curiohome.Init(dir, "nomic-embed-text", store.EmbeddingDim)
	require.NoError(t, err)

	cfg := fmt.Sprintf(`daemon:
  listen: %q
  fetch_workers: 1
  index_workers: 1
embedding:
  base_url: http://127.0.0.1:1
  auto_pull: false
generation:
  base_url: http://127.0.0.1:1
  auto_pull: false
insight:
  labeling: terms
`, listen)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	return home
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// seededJobs is a running job (as a previous daemon would leave it, mid
// work) and a pending one, in a migrated database.
type seededJobs struct {
	running, pending string
}

func seedJobs(t *testing.T, home *curiohome.Home) seededJobs {
	t.Helper()
	db, err := sqlitestore.Open(home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, sqlitestore.Migrate(db))

	q := sqlitestore.NewJobs(db)
	ctx := context.Background()
	running := &store.Job{TenantID: "local", Kind: store.JobKindFetch, Status: store.JobStatusRunning, Attempts: 1}
	pending := &store.Job{TenantID: "local", Kind: store.JobKindFetch}
	require.NoError(t, q.Enqueue(ctx, running))
	require.NoError(t, q.Enqueue(ctx, pending))
	return seededJobs{running: running.ID, pending: pending.ID}
}

// assertJobsUntouched checks run() left the seeded jobs exactly as seeded:
// the running one not requeued, the pending one not claimed.
func assertJobsUntouched(t *testing.T, home *curiohome.Home, seeded seededJobs) {
	t.Helper()
	db, err := sqlitestore.Open(home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	q := sqlitestore.NewJobs(db)

	running, err := q.GetByID(context.Background(), seeded.running)
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusRunning, running.Status)
	assert.Equal(t, 1, running.Attempts)

	pending, err := q.GetByID(context.Background(), seeded.pending)
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusPending, pending.Status)
	assert.Zero(t, pending.Attempts)
}

// TestRun_SecondDaemonLeavesJobsAlone: a daemon that finds the home locked
// exits before touching the database, so the running daemon's in-flight jobs
// are neither requeued (and run twice) nor claimed.
func TestRun_SecondDaemonLeavesJobsAlone(t *testing.T) {
	home := newHome(t, freeLoopbackAddr(t))
	seeded := seedJobs(t, home)

	lock, err := daemonctl.AcquireLock(home)
	require.NoError(t, err)
	defer lock.Release()

	err = run(context.Background(), new(slog.LevelVar))
	require.ErrorIs(t, err, daemonctl.ErrAlreadyRunning)
	assert.Contains(t, err.Error(), home.Path)
	assert.Contains(t, err.Error(), fmt.Sprintf("pid %d", os.Getpid()))
	assertJobsUntouched(t, home, seeded)
}

// TestRun_BindFailureLeavesJobsAlone: a daemon that can't bind its port
// exits before touching the database.
func TestRun_BindFailureLeavesJobsAlone(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupied.Close()

	home := newHome(t, occupied.Addr().String())
	seeded := seedJobs(t, home)

	err = run(context.Background(), new(slog.LevelVar))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen on "+occupied.Addr().String())
	assert.Contains(t, err.Error(), "curio daemon status")
	assertJobsUntouched(t, home, seeded)

	lock, err := daemonctl.AcquireLock(home)
	require.NoError(t, err, "the failed daemon released its lock")
	require.NoError(t, lock.Release())
}

func TestRun_ServesIdentityAndReleasesOnShutdown(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, new(slog.LevelVar)) }()

	c := client.New("http://" + listen)
	var health *client.Health
	require.Eventually(t, func() bool {
		hctx, hcancel := context.WithTimeout(ctx, time.Second)
		defer hcancel()
		h, err := c.Healthz(hctx)
		health = h
		return err == nil
	}, 15*time.Second, 50*time.Millisecond)
	assert.Equal(t, os.Getpid(), health.PID)
	assert.Equal(t, home.Path, health.Home)

	pidFile, err := os.ReadFile(home.PIDFile())
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprint(os.Getpid()), strings.TrimSpace(string(pidFile)))

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after cancellation")
	}

	lock, err := daemonctl.AcquireLock(home)
	require.NoError(t, err, "the lock is free after shutdown")
	require.NoError(t, lock.Release())
	pidFile, err = os.ReadFile(home.PIDFile())
	require.NoError(t, err)
	assert.Empty(t, pidFile, "a clean exit leaves the PID file empty")
}

// TestDrain: shutdown waits for the workers up to the grace period, then
// gives up on them and names the jobs still running.
func TestDrain(t *testing.T) {
	q := sqlitestore.NewJobs(sqlitetest.NewDB(t))
	job := &store.Job{TenantID: "local", Kind: store.JobKindFetch}
	require.NoError(t, q.Enqueue(context.Background(), job))

	claimed := make(chan struct{})
	release := make(chan struct{})
	w := jobs.NewWorker(q, jobs.WorkerOptions{PollInterval: 10 * time.Millisecond})
	w.Register(store.JobKindFetch, func(context.Context, *store.Job) error {
		close(claimed)
		<-release // deaf to cancellation, like a handler stuck in a syscall
		return nil
	})
	d := &daemon{pools: []pool{{name: "fetch", worker: w, size: 1}}}

	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Go(func() { assert.ErrorIs(t, w.Run(ctx), context.Canceled) })
	<-claimed
	cancel()

	stuck, drained := d.drain(&workers, 50*time.Millisecond)
	assert.False(t, drained)
	assert.Equal(t, []string{job.ID}, stuck)

	close(release)
	stuck, drained = d.drain(&workers, 5*time.Second)
	assert.True(t, drained)
	assert.Empty(t, stuck)
}

// docVectors serves canned document vectors to the insight engine.
type docVectors struct {
	store.ChunkStore
	dvs []store.DocVector
}

func (d *docVectors) DocumentVectors(context.Context, string) ([]store.DocVector, error) {
	return d.dvs, nil
}

// TestNewInsightEngine_LLMComesUpAfterStart: Ollama being down when the daemon
// starts (common: the CLI auto-starts the daemon before the Ollama app) must
// not switch LLM labels off for the life of the process.
func TestNewInsightEngine_LLMComesUpAfterStart(t *testing.T) {
	addr := freeLoopbackAddr(t) // nothing listens here yet
	cfg := config.Default()
	cfg.Generation.BaseURL = "http://" + addr
	cfg.Generation.AutoPull = false
	cfg.Insight.CenterVectors = false

	db := sqlitetest.NewDB(t)
	docs := sqlitestore.NewDocuments(db)
	insights := sqlitestore.NewInsights(db)
	chunks := &docVectors{}
	for i := range 3 {
		title := fmt.Sprintf("Article %d", i)
		d := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/%d", i),
			Title: &title, State: store.DocStateFetched}
		require.NoError(t, docs.Upsert(context.Background(), d))
		chunks.dvs = append(chunks.dvs, store.DocVector{DocumentID: d.ID, Vector: []float32{1, 0, 0}})
	}

	eng, err := newInsightEngine(context.Background(), cfg, docs, chunks, insights)
	require.NoError(t, err)

	// Ollama starts only now.
	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/generate", r.URL.Path)
		fmt.Fprint(w, `{"response":"NAME: Reading List\nSUMMARY: Things to read.","done":true}`)
	}))
	require.NoError(t, srv.Listener.Close())
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	runID, err := eng.Rebuild(context.Background(), "local")
	require.NoError(t, err)
	clusters, err := insights.ListClusters(context.Background(), runID, 0)
	require.NoError(t, err)
	require.Len(t, clusters, 1)
	require.NotNil(t, clusters[0].Label)
	assert.Equal(t, "Reading List", *clusters[0].Label)
}
