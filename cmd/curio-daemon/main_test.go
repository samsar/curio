package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/drift"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
	"github.com/samsar/curio/internal/ui"
	"github.com/samsar/curio/migrations"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// newHome initializes a CURIO_HOME for run(), with the default embedding
// model and width, and a config that keeps the daemon offline: Ollama
// unreachable, nothing auto-pulled, term labels.
func newHome(t *testing.T, listen string) *curiohome.Home {
	t.Helper()
	defaults := config.Default().Embedding
	return newHomeFor(t, listen, defaults.Model, defaults.Dim, "")
}

// newHomeFor is newHome for a home created with model at dim, whose
// config's embedding section adds embeddingKeys.
func newHomeFor(t *testing.T, listen, model string, dim int, embeddingKeys string) *curiohome.Home {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CURIO_HOME", dir)
	home, err := curiohome.Init(dir, model, dim)
	require.NoError(t, err)

	cfg := fmt.Sprintf(`daemon:
  listen: %q
  fetch_workers: 1
  index_workers: 1
embedding:
%s  base_url: http://127.0.0.1:1
  auto_pull: false
generation:
  base_url: http://127.0.0.1:1
  auto_pull: false
insight:
  labeling: terms
`, listen, embeddingKeys)
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
	db, err := sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	_, err = sqlitestore.Migrate(context.Background(), db)
	require.NoError(t, err)
	return enqueueSeededJobs(t, db)
}

// enqueueSeededJobs adds seededJobs' two jobs to db, whatever its version.
func enqueueSeededJobs(t *testing.T, db *sqlitestore.DB) seededJobs {
	t.Helper()
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
	db, err := sqlitestore.Open(context.Background(), home.DBPath())
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

// recorder is a slog handler that keeps every record.
type recorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler     { return r }
func (r *recorder) WithGroup(string) slog.Handler          { return r }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Clone())
	return nil
}

// recordLogs sends the default logger to a recorder until the test ends.
func recordLogs(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec
}

// TestRun_EmbeddingMismatchRefusesToStart: a config whose embedding model
// differs from the one the home's vectors were made with stops the daemon
// before it touches the database, with one error that names both sides and
// the fixes, and nothing logged on the way.
func TestRun_EmbeddingMismatchRefusesToStart(t *testing.T) {
	home := newHome(t, freeLoopbackAddr(t))
	cfg, err := os.ReadFile(home.ConfigPath())
	require.NoError(t, err)
	cfg = []byte(strings.Replace(string(cfg), "embedding:\n", "embedding:\n  model: mxbai-embed-large\n", 1))
	require.NoError(t, os.WriteFile(home.ConfigPath(), cfg, 0o600))
	seeded := seedJobs(t, home)
	logs := recordLogs(t)

	err = run(context.Background(), new(slog.LevelVar))
	var mismatch *curiohome.EmbeddingMismatchError
	require.ErrorAs(t, err, &mismatch)
	for _, want := range []string{
		home.ConfigPath(), `"mxbai-embed-large" (dim 1024)`,
		home.MarkerPath(), `"qwen3-embedding:0.6b" (dim 1024)`,
		"Set embedding.model and embedding.dim back", "`curio up --fresh`",
	} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "Embedding model swap")
	assert.NotContains(t, err.Error(), "--reason")
	assertJobsUntouched(t, home, seeded)
	assertLoggedNothingAboveInfo(t, logs)
}

// assertLoggedNothingAboveInfo: main logs the error run returns, once, so
// run logs nothing of its own about a refusal.
func assertLoggedNothingAboveInfo(t *testing.T, logs *recorder) {
	t.Helper()
	logs.mu.Lock()
	defer logs.mu.Unlock()
	for _, r := range logs.records {
		assert.Less(t, r.Level, slog.LevelWarn, "logged %q; main logs the returned error once", r.Message)
	}
}

// legacyMarker is a marker as curio wrote it before home formats: no
// format key, and nomic-embed-text's width.
const legacyMarker = `{
  "schema_version": 11,
  "embedding_model": "nomic-embed-text",
  "embedding_dim": 768,
  "created_at": "2026-05-23T10:00:00Z",
  "updated_at": "2026-09-27T10:00:00Z"
}
`

// TestRun_LegacyHomeRefusedBeforeTheDatabase: a home from before home
// formats, whose config even pins its old model, is refused before the
// daemon opens its database: the schema, the vectors and the jobs are as
// they were, and the error says what the home holds and how to start over.
func TestRun_LegacyHomeRefusedBeforeTheDatabase(t *testing.T) {
	ctx := context.Background()
	listen := freeLoopbackAddr(t)
	home := newHomeFor(t, listen, "nomic-embed-text", 768, "  model: nomic-embed-text\n  dim: 768\n")
	require.NoError(t, os.WriteFile(home.MarkerPath(), []byte(legacyMarker), 0o600))
	const legacyVersion = 11
	migrateTo(t, home, legacyVersion)
	require.NoError(t, os.WriteFile(home.MarkerPath(), []byte(legacyMarker), 0o600), "migrateTo rewrote it")
	seeded, vectorID := seedLegacyHome(t, home)
	logs := recordLogs(t)

	err := run(ctx, new(slog.LevelVar))
	require.ErrorIs(t, err, curiohome.ErrLegacyHome)
	for _, want := range []string{home.Path, home.MarkerPath(), `"nomic-embed-text" (dim 768)`,
		"`curio up --fresh`", home.Path + ".bak-<YYYYMMDD-HHMMSS>", "import your bookmarks again"} {
		assert.Contains(t, err.Error(), want)
	}
	assertLoggedNothingAboveInfo(t, logs)
	assertJobsUntouched(t, home, seeded)

	db, err := sqlitestore.Open(ctx, home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	var version int64
	require.NoError(t, db.QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&version))
	assert.EqualValues(t, legacyVersion, version, "no migration ran")
	width, err := sqlitestore.VectorIndexWidth(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, 768, width)
	var kept string
	require.NoError(t, db.QueryRow(`SELECT chunk_id FROM chunks_vec`).Scan(&kept))
	assert.Equal(t, vectorID, kept)
	raw, err := os.ReadFile(home.MarkerPath())
	require.NoError(t, err)
	assert.Equal(t, legacyMarker, string(raw), "the marker is left as it was")
}

// seedLegacyHome gives home's database, left at its version, seededJobs'
// jobs and one 768-d chunk vector, as a nomic-embed-text home holds them,
// and returns the jobs and the chunk's ID.
func seedLegacyHome(t *testing.T, home *curiohome.Home) (seededJobs, string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitestore.Open(ctx, home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	seeded := enqueueSeededJobs(t, db)
	// The document and chunk stores write the latest schema; the document
	// and its chunk go in as the home's older one holds them.
	const docID, chunkID = "legacy-document", "legacy-chunk"
	_, err = db.ExecContext(ctx, `INSERT INTO documents (id, tenant_id, url, state)
		VALUES (?, 'local', 'https://example.com/a', 'fetched')`, docID)
	require.NoError(t, err)
	ext := &store.DocumentExtraction{DocumentID: docID, Fetcher: "test", Status: store.ExtractionStatusOK}
	require.NoError(t, sqlitestore.NewExtractions(db).Create(ctx, ext))
	_, err = db.ExecContext(ctx, `INSERT INTO chunks (id, document_id, extraction_id, ord, text)
		VALUES (?, ?, ?, 0, 'kept')`, chunkID, docID, ext.ID)
	require.NoError(t, err)
	vector, err := sqlitevec.SerializeFloat32(make([]float32, 768))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO chunks_vec (chunk_id, embedding) VALUES (?, ?)`, chunkID, vector)
	require.NoError(t, err)
	return seeded, chunkID
}

// TestRun_SizesTheVectorIndexFromTheHome: the vector index of a new home's
// database takes the width the marker records, not migration 001's.
func TestRun_SizesTheVectorIndexFromTheHome(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHomeFor(t, listen, "all-minilm:l6-v2", 384, "  model: all-minilm:l6-v2\n  dim: 384\n")

	_, stop := runDaemon(t, listen)
	stop()

	db, err := sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	var decl string
	require.NoError(t, db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'chunks_vec'`).Scan(&decl))
	assert.Contains(t, decl, "FLOAT[384]")
}

// TestOpenHome_NewHomeGetsTheDefaults: a home the daemon creates, with no
// config.yaml, records the default embedding model and width, in the
// current format.
func TestOpenHome_NewHomeGetsTheDefaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	t.Setenv("CURIO_HOME", dir)

	home, err := openHome()
	require.NoError(t, err)
	meta, err := home.Meta()
	require.NoError(t, err)
	assert.Equal(t, curiohome.CurrentFormat, meta.Format)
	assert.Equal(t, "qwen3-embedding:0.6b", meta.EmbeddingModel)
	assert.Equal(t, 1024, meta.EmbeddingDim)
}

// daemonRun is run going in the background.
type daemonRun struct {
	cancel context.CancelFunc
	done   chan error
	ended  bool
	err    error
}

// startRun starts run in the background. When the test ends it is
// cancelled and waited for.
func startRun(t *testing.T) *daemonRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &daemonRun{cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- run(ctx, new(slog.LevelVar)) }()
	t.Cleanup(func() {
		r.cancel()
		r.wait(t, 30*time.Second)
	})
	return r
}

// wait returns what run returned, failing the test if that takes longer
// than timeout.
func (r *daemonRun) wait(t *testing.T, timeout time.Duration) error {
	t.Helper()
	if !r.ended {
		select {
		case r.err = <-r.done:
			r.ended = true
		case <-time.After(timeout):
			t.Fatalf("run did not return within %s", timeout)
		}
	}
	return r.err
}

// runDaemon starts run in the background and waits until it answers
// /v1/healthz as ready. stop cancels it and waits for run to return
// cleanly.
func runDaemon(t *testing.T, listen string) (health *client.Health, stop func()) {
	t.Helper()
	r := startRun(t)
	c := client.New("http://" + listen)
	require.Eventually(t, func() bool {
		hctx, hcancel := context.WithTimeout(context.Background(), time.Second)
		defer hcancel()
		h, err := c.Healthz(hctx)
		health = h
		return err == nil
	}, 15*time.Second, 50*time.Millisecond)

	return health, func() {
		t.Helper()
		r.cancel()
		require.NoError(t, r.wait(t, 30*time.Second))
	}
}

func TestRun_ServesIdentityAndReleasesOnShutdown(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)

	health, stop := runDaemon(t, listen)
	assert.Equal(t, os.Getpid(), health.PID)
	assert.Equal(t, home.Path, health.Home)
	require.Len(t, health.Upstreams, 1, "the Jina fallback, on by default")
	jina := health.Upstreams[0]
	assert.Equal(t, "jina", jina.Name)
	assert.True(t, jina.Enabled)
	assert.Equal(t, client.UpstreamIdle, jina.State)

	pidFile, err := os.ReadFile(home.PIDFile())
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid()), strings.TrimSpace(string(pidFile)))

	stop()

	lock, err := daemonctl.AcquireLock(home)
	require.NoError(t, err, "the lock is free after shutdown")
	require.NoError(t, lock.Release())
	pidFile, err = os.ReadFile(home.PIDFile())
	require.NoError(t, err)
	assert.Empty(t, pidFile, "a clean exit leaves the PID file empty")
}

// TestRun_DashboardFollowsConfig: the daemon serves the dashboard by
// default, and with daemon.ui: false answers /ui/ as the API answers any
// path it doesn't have.
func TestRun_DashboardFollowsConfig(t *testing.T) {
	listen := freeLoopbackAddr(t)
	newHome(t, listen)
	_, stop := runDaemon(t, listen)
	status, header, _ := rawGet(t, listen, "/ui/", nil)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "text/html; charset=utf-8", header.Get("Content-Type"))
	stop()

	home := newHome(t, listen)
	cfg, err := os.ReadFile(home.ConfigPath())
	require.NoError(t, err)
	off := strings.Replace(string(cfg), "daemon:\n", "daemon:\n  ui: false\n", 1)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(off), 0o600))
	_, stop = runDaemon(t, listen)
	defer stop()
	status, header, body := rawGet(t, listen, "/ui/", nil)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "application/problem+json", header.Get("Content-Type"))
	assert.Contains(t, body, "no route for /ui/")
}

// TestRun_QueuePauseSurvivesRestart: a pause holds from the first claim of
// the next daemon, over the jobs the last one left: its orphan, requeued
// with the attempt it used, and a pending job. Resuming releases both.
func TestRun_QueuePauseSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)
	c := client.New("http://" + listen)

	_, stop := runDaemon(t, listen)
	_, err := c.UpdateQueue(ctx, client.QueueUpdate{Paused: new(true)})
	require.NoError(t, err)
	stop()

	seeded := seedJobs(t, home)
	_, stop = runDaemon(t, listen)
	defer stop()
	q, err := c.Queue(ctx)
	require.NoError(t, err)
	assert.True(t, q.Paused)
	assert.Equal(t, client.QueueClosed, q.State)
	assert.Equal(t, client.ReasonPaused, q.Reason)

	db, err := sqlitestore.Open(ctx, home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	jobsQ := sqlitestore.NewJobs(db)
	// job is called from the polling goroutines below, where only assert
	// may report a failure.
	job := func(id string) store.Job {
		j, err := jobsQ.GetByID(ctx, id)
		if !assert.NoError(t, err) {
			return store.Job{}
		}
		return *j
	}
	assert.Never(t, func() bool {
		orphan, pending := job(seeded.running), job(seeded.pending)
		return orphan.Status != store.JobStatusPending || orphan.Attempts != 1 ||
			pending.Status != store.JobStatusPending || pending.Attempts != 0
	}, 300*time.Millisecond, 20*time.Millisecond, "the jobs wait, unclaimed")

	_, err = c.UpdateQueue(ctx, client.QueueUpdate{Paused: new(false)})
	require.NoError(t, err)
	// Their {} payloads name no document, so each fails at its first claim.
	require.Eventually(t, func() bool {
		return job(seeded.running).Status == store.JobStatusFailed && job(seeded.pending).Status == store.JobStatusFailed
	}, 10*time.Second, 20*time.Millisecond)
}

// TestRun_UnreadableQueueSettingsStopTheStart: a daemon that can't read the
// queue settings doesn't start with the queue open, which would break a
// pause the user set.
func TestRun_UnreadableQueueSettingsStopTheStart(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)
	seeded := seedJobs(t, home)
	db, err := sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE queue_settings`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	err = run(context.Background(), new(slog.LevelVar))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load queue settings")
	assert.Contains(t, err.Error(), "no such table: queue_settings")
	ln, err := net.Listen("tcp", listen)
	require.NoError(t, err, "the port is free again")
	require.NoError(t, ln.Close())

	db, err = sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	pending, err := sqlitestore.NewJobs(db).GetByID(context.Background(), seeded.pending)
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusPending, pending.Status)
	assert.Zero(t, pending.Attempts, "nothing was claimed")
}

// migrateTo brings home's database to version, as an older curio would
// have left it, with the marker saying so, and returns the newest version.
func migrateTo(t *testing.T, home *curiohome.Home, version int64) (latest int) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitestore.Open(ctx, home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, version)
	require.NoError(t, err)
	meta, err := home.Meta()
	require.NoError(t, err)
	meta.SchemaVersion = int(version)
	require.NoError(t, home.WriteMeta(meta))
	sources := p.ListSources()
	return int(sources[len(sources)-1].Version) // ListSources sorts by version
}

// TestRun_SyncsMarkerSchemaVersion: the marker caches the version goose
// leaves the database at. Upgrading a database at version 4, whose marker
// says 4, leaves both at the newest migration.
func TestRun_SyncsMarkerSchemaVersion(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)
	latest := migrateTo(t, home, 4)
	meta, err := home.Meta()
	require.NoError(t, err)
	written := meta.UpdatedAt

	health, stop := runDaemon(t, listen)
	stop()

	require.Greater(t, latest, 4)
	assert.Equal(t, latest, health.SchemaVersion)
	meta, err = home.Meta()
	require.NoError(t, err)
	assert.Equal(t, latest, meta.SchemaVersion)
	assert.True(t, meta.UpdatedAt.After(written), "the sync stamps the marker")
}

// TestRun_GenerationModelIsNotPinned: the writing model is the user's to
// change. A daemon restarted with another generation.model starts, and the
// marker is byte for byte what it was, updated_at aside: the home records
// nothing about the writing model, so nothing needs reindexing.
func TestRun_GenerationModelIsNotPinned(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)
	original, err := os.ReadFile(home.ConfigPath())
	require.NoError(t, err)
	useWritingModel := func(model string) {
		t.Helper()
		cfg := strings.Replace(string(original), "generation:\n", "generation:\n  model: "+model+"\n", 1)
		require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	}
	marker := func() string {
		t.Helper()
		raw, err := os.ReadFile(home.MarkerPath())
		require.NoError(t, err)
		return regexp.MustCompile(`(?m)^\s*"updated_at": ".*"\n`).ReplaceAllString(string(raw), "")
	}

	useWritingModel("qwen3:4b-instruct")
	_, stop := runDaemon(t, listen)
	stop()
	before := marker()
	require.NotContains(t, before, "qwen3:4b-instruct")

	useWritingModel("gemma4:12b")
	_, stop = runDaemon(t, listen)
	stop()
	assert.Equal(t, before, marker())
}

// holdWriteLock takes the database's write lock from another connection,
// which keeps a daemon migrating it in its first migration: that waits up
// to busy_timeout (5s) for the lock, and cancelling the daemon's context
// doesn't cut the wait short. release gives the lock back; call it well
// within 5s of the daemon reaching the migration.
func holdWriteLock(t *testing.T, home *curiohome.Home) (release func()) {
	t.Helper()
	db, err := sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	tx, err := db.BeginTx(context.Background(), nil) // BEGIN IMMEDIATE: the write lock
	require.NoError(t, err)
	var once sync.Once
	release = func() {
		once.Do(func() {
			assert.NoError(t, tx.Rollback())
			assert.NoError(t, db.Close())
		})
	}
	t.Cleanup(release)
	return release
}

// waitMigrating waits until the daemon at c reports it is migrating, and
// returns what it reported.
func waitMigrating(t *testing.T, c *client.Client) *client.Startup {
	t.Helper()
	var st *client.Startup
	require.Eventually(t, func() bool {
		_, err := c.Healthz(context.Background())
		st = client.StartupOf(err)
		return st != nil && st.Phase == client.PhaseMigrating
	}, 10*time.Second, 20*time.Millisecond)
	return st
}

// rawGet sends a GET to the daemon at listen with extra headers, and
// returns the status, headers and body.
func rawGet(t *testing.T, listen, path string, header http.Header) (status int, respHeader http.Header, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+listen+path, nil)
	require.NoError(t, err)
	maps.Copy(req.Header, header)
	if host := header.Get("Host"); host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, string(b)
}

// TestRun_AnswersWhileMigrating: from the bind on, a daemon migrating its
// database answers as a starting daemon: healthz names it and its progress,
// everything else is refused with Retry-After, and the access policy
// holds. Once the migration can run, the full API takes over.
func TestRun_AnswersWhileMigrating(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)
	latest := migrateTo(t, home, 4)
	release := holdWriteLock(t, home)
	r := startRun(t)
	c := client.New("http://" + listen)

	st := waitMigrating(t, c)
	assert.Equal(t, os.Getpid(), st.PID)
	assert.Equal(t, home.Path, st.Home)
	assert.Equal(t, &client.MigrationProgress{Applied: 0, Total: latest - 4}, st.Migrations)

	_, err := c.Stats(context.Background())
	require.ErrorIs(t, err, client.ErrStarting)
	status, header, body := rawGet(t, listen, "/v1/stats", nil)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, "1", header.Get("Retry-After"))
	assert.NotContains(t, body, `"pid"`)
	status, _, body = rawGet(t, listen, "/v1/healthz", http.Header{"Host": {"attacker.example:" + strings.Split(listen, ":")[1]}})
	assert.Equal(t, http.StatusForbidden, status)
	assert.NotContains(t, body, `"pid"`)
	status, _, body = rawGet(t, listen, "/v1/healthz", http.Header{"Origin": {"https://attacker.example"}})
	assert.Equal(t, http.StatusForbidden, status)
	assert.NotContains(t, body, `"pid"`)
	status, header, body = rawGet(t, listen, "/ui/", nil)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, "text/html; charset=utf-8", header.Get("Content-Type"))
	assert.Equal(t, ui.CSP, header.Get("Content-Security-Policy"))
	assert.Contains(t, body, fmt.Sprintf("0 of %d migrations applied", latest-4))
	release()

	var health *client.Health
	require.Eventually(t, func() bool {
		health, err = c.Healthz(context.Background())
		return err == nil
	}, 10*time.Second, 20*time.Millisecond)
	assert.Equal(t, latest, health.SchemaVersion, "the first ready answer has the new schema version")
	_, err = c.Stats(context.Background())
	require.NoError(t, err)

	r.cancel()
	require.NoError(t, r.wait(t, 30*time.Second))
}

// TestRun_CreatingTheSchemaIsNotMigrating: a new database's schema is
// created in the initializing phase. It takes milliseconds, and a daemon
// reporting a migration has every waiting client tell its user to expect a
// wait. The database starts as goose leaves a new one just before its first
// migration, with only goose's version table, at version 0, so the held
// write lock stops the daemon in that migration.
func TestRun_CreatingTheSchemaIsNotMigrating(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)
	db, err := sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	p, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	require.NoError(t, err)
	_, err = p.GetDBVersion(context.Background()) // creates the version table
	require.NoError(t, err)
	require.NoError(t, db.Close())
	logs := recordLogs(t)
	release := holdWriteLock(t, home)
	r := startRun(t)
	c := client.New("http://" + listen)

	require.Eventually(t, func() bool { return len(logs.messages("applying migration")) > 0 },
		10*time.Second, 10*time.Millisecond, "the daemon reaches the first migration")
	_, err = c.Healthz(context.Background())
	st := client.StartupOf(err)
	require.NotNil(t, st, "a starting daemon's answer, got %v", err)
	assert.Equal(t, client.PhaseInitializing, st.Phase)
	assert.Nil(t, st.Migrations)
	release()

	require.Eventually(t, func() bool {
		_, err := c.Healthz(context.Background())
		return err == nil
	}, 10*time.Second, 20*time.Millisecond)
	r.cancel()
	require.NoError(t, r.wait(t, 30*time.Second))
}

// TestStart_InitializingOnceMigrated: once its migrations are applied, a
// starting daemon reports it is initializing for the rest of its startup,
// not "migrating, 6 of 6 applied", and clients go back to polling it at
// the pace for a start that is nearly done.
func TestStart_InitializingOnceMigrated(t *testing.T) {
	home := newHome(t, freeLoopbackAddr(t))
	migrateTo(t, home, 4)
	cfg, err := config.Load(home.ConfigPath())
	require.NoError(t, err)
	meta, err := home.Meta()
	require.NoError(t, err)
	db, err := sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	logs := recordLogs(t)
	startup := api.NewStartup()

	_, err = start(context.Background(), cfg, home, meta, db, startup)
	require.NoError(t, err)
	require.NotEmpty(t, logs.messages("migration applied"), "the daemon migrated")
	phase, progress := startup.Progress()
	assert.Equal(t, api.PhaseInitializing, phase)
	assert.Nil(t, progress)
}

// TestRun_FailureAfterBindStopsServing: a daemon that fails after binding
// (here, a curio.db that isn't a database) stops serving before it
// returns, and closes its port before it gives up the lock.
func TestRun_FailureAfterBindStopsServing(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)
	require.NoError(t, os.WriteFile(home.DBPath(), []byte(strings.Repeat("not a database\n", 512)), 0o600))

	err := run(context.Background(), new(slog.LevelVar))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a database")

	ln, err := net.Listen("tcp", listen)
	require.NoError(t, err, "the port is free again")
	require.NoError(t, ln.Close())
	lock, err := daemonctl.AcquireLock(home)
	require.NoError(t, err, "the lock is free again")
	require.NoError(t, lock.Release())
}

// TestRun_CancelWhileMigrating: a daemon told to stop while it migrates
// returns once the migration step it is in ends, with its port closed and
// its lock released.
func TestRun_CancelWhileMigrating(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)
	migrateTo(t, home, 4)
	release := holdWriteLock(t, home)
	r := startRun(t)
	waitMigrating(t, client.New("http://"+listen))

	r.cancel()
	select {
	case err := <-r.done:
		t.Fatalf("run returned mid-step: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	require.ErrorIs(t, r.wait(t, 10*time.Second), context.Canceled, "which main takes for a clean shutdown")

	ln, err := net.Listen("tcp", listen)
	require.NoError(t, err, "the port is free again")
	require.NoError(t, ln.Close())
	lock, err := daemonctl.AcquireLock(home)
	require.NoError(t, err, "the lock is free again")
	require.NoError(t, lock.Release())
}

// all returns every record.
func (r *recorder) all() []slog.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.records)
}

// messages returns the records at info with message msg, as attribute maps.
func (r *recorder) messages(msg string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, rec := range r.records {
		if rec.Message != msg {
			continue
		}
		attrs := map[string]any{}
		rec.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Resolve().Any()
			return true
		})
		out = append(out, attrs)
	}
	return out
}

// TestRun_LogsTheStartup: the log tells the startup's story: the daemon
// starting at the bind, what an upgrade migrates, each migration as it
// starts and finishes, and the daemon ready. An up-to-date home migrates
// nothing and says nothing about it.
func TestRun_LogsTheStartup(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)
	latest := migrateTo(t, home, 4)
	logs := recordLogs(t)

	_, stop := runDaemon(t, listen)
	stop()

	starting := logs.messages("curio-daemon starting")
	require.Len(t, starting, 1)
	assert.Equal(t, home.Path, starting[0]["home"])
	assert.EqualValues(t, os.Getpid(), starting[0]["pid"])
	assert.Contains(t, starting[0], "version")

	migrating := logs.messages("migrating database")
	require.Len(t, migrating, 1)
	assert.EqualValues(t, latest-4, migrating[0]["pending"])
	assert.EqualValues(t, 4, migrating[0]["from_version"])
	assert.EqualValues(t, latest, migrating[0]["to_version"])

	applying := logs.messages("applying migration")
	applied := logs.messages("migration applied")
	require.Len(t, applying, latest-4)
	require.Len(t, applied, latest-4)
	for i, rec := range applied {
		assert.EqualValues(t, 5+i, rec["version"])
		assert.Equal(t, applying[i]["source"], rec["source"])
		assert.Contains(t, rec, "duration_ms")
	}
	ready := logs.messages("database ready")
	require.Len(t, ready, 1)
	assert.EqualValues(t, latest, ready[0]["schema_version"])
	assert.EqualValues(t, 1024, ready[0]["embedding_dim"])
	assert.Regexp(t, `^v\d+\.\d+\.\d+`, ready[0]["sqlite_vec"], "the sqlite-vec go.mod pins")
	assert.Contains(t, ready[0], "sqlite_vec_build", "which SIMD kernels a released binary has")
	daemonReady := logs.messages("curio-daemon ready")
	require.Len(t, daemonReady, 1)
	assert.Contains(t, daemonReady[0], "startup_ms")

	again := recordLogs(t)
	_, stop = runDaemon(t, listen)
	stop()
	assert.Empty(t, again.messages("migrating database"), "nothing pending, nothing said")
	assert.Empty(t, again.messages("applying migration"))
	assert.Len(t, again.messages("curio-daemon ready"), 1)
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
	d := &daemon{pools: []jobs.Pool{{Name: "fetch", Worker: w, Size: 1}}}

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
		require.NoError(t, docs.Create(context.Background(), d))
		chunks.dvs = append(chunks.dvs, store.DocVector{DocumentID: d.ID, Vector: []float32{1, 0, 0}})
	}

	eng, err := newInsightEngine(cfg, docs, chunks, insights, sqlitestore.NewJobs(db), nil, nil)
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

	runID, err := eng.Rebuild(context.Background(), "local", store.RunTriggerManual)
	require.NoError(t, err)
	run, err := insights.GetRun(context.Background(), runID)
	require.NoError(t, err)
	assert.Equal(t, "louvain", run.Grouper, "the Louvain grouper")
	groups, err := insights.TopGroups(context.Background(), runID, 0, 0)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "Reading List", groups[0].Label)
	assert.Equal(t, store.LabelSourceLLM, groups[0].LabelSource)
}

// threeArticles is a library of three fetched documents with one vector
// between them, which a rebuild groups into one interest.
func threeArticles(t *testing.T) (*sqlitestore.Documents, *docVectors, *sqlitestore.Insights, *sqlitestore.Jobs) {
	t.Helper()
	db := sqlitetest.NewDB(t)
	docs := sqlitestore.NewDocuments(db)
	chunks := &docVectors{}
	for i := range 3 {
		title := fmt.Sprintf("Article %d", i)
		d := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/%d", i),
			Title: &title, State: store.DocStateFetched}
		require.NoError(t, docs.Create(context.Background(), d))
		chunks.dvs = append(chunks.dvs, store.DocVector{DocumentID: d.ID, Vector: []float32{1, 0, 0}})
	}
	return docs, chunks, sqlitestore.NewInsights(db), sqlitestore.NewJobs(db)
}

// rebuildLabel rebuilds eng's interests and returns the one interest's
// label and what named it.
func rebuildLabel(t *testing.T, eng *insight.Engine, insights store.InsightStore) (string, store.LabelSource) {
	t.Helper()
	runID, err := eng.Rebuild(context.Background(), "local", store.RunTriggerManual)
	require.NoError(t, err)
	groups, err := insights.TopGroups(context.Background(), runID, 0, 0)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	return groups[0].Label, groups[0].LabelSource
}

// TestNewInsightEngine_LabelsRole: interest labels are written by the
// labels role's model, at its own server, not generation.model.
func TestNewInsightEngine_LabelsRole(t *testing.T) {
	var asked []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		asked = append(asked, req.Model)
		mu.Unlock()
		fmt.Fprint(w, `{"response":"NAME: Reading List\nSUMMARY: Things to read.","done":true}`)
	}))
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Generation.Model, cfg.Generation.BaseURL = "gemma4:26b", "http://127.0.0.1:1"
	cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleLabels: {Model: "role-model", BaseURL: srv.URL}}
	cfg.Insight.CenterVectors = false
	docs, chunks, insights, queue := threeArticles(t)

	eng, err := newInsightEngine(cfg, docs, chunks, insights, queue, nil, nil)
	require.NoError(t, err)
	label, source := rebuildLabel(t, eng, insights)
	assert.Equal(t, "Reading List", label)
	assert.Equal(t, store.LabelSourceLLM, source)
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, asked)
	for _, m := range asked {
		assert.Equal(t, "role-model", m)
	}
}

// TestNewInsightEngine_InsightOffWritesNothing: with insight off, the
// labels role isn't in use, so even with insight.labeling llm the engine
// gets no LLM labeler, and the daemon pulls no writing model.
func TestNewInsightEngine_InsightOffWritesNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Insight.Enabled, cfg.Insight.Labeling, cfg.Insight.CenterVectors = false, "llm", false
	cfg.Generation.BaseURL = srv.URL
	docs, chunks, insights, queue := threeArticles(t)

	eng, err := newInsightEngine(cfg, docs, chunks, insights, queue, nil, nil)
	require.NoError(t, err)
	_, source := rebuildLabel(t, eng, insights)
	assert.Equal(t, store.LabelSourceTerms, source)
	for _, target := range pullTargets(cfg) {
		assert.Equal(t, "embeddings", target.usedFor, "only the embedding model is pulled")
	}
	assert.Empty(t, generationRoles(cfg))
}

func TestPullTargets(t *testing.T) {
	const local = "http://localhost:11434"
	embeddings := pullTarget{baseURL: local, model: "qwen3-embedding:0.6b", usedFor: "embeddings", timeout: time.Minute}
	labels := pullTarget{baseURL: local, model: "qwen3:4b-instruct", usedFor: "interest labels", timeout: 2 * time.Minute}
	for _, tc := range []struct {
		name   string
		mutate func(*config.Config)
		want   []pullTarget
	}{
		{"the defaults", func(*config.Config) {}, []pullTarget{embeddings, labels}},
		{"no embedding auto-pull", func(c *config.Config) { c.Embedding.AutoPull = false }, []pullTarget{labels}},
		{"no generation auto-pull", func(c *config.Config) { c.Generation.AutoPull = false }, []pullTarget{embeddings}},
		{"insight off", func(c *config.Config) { c.Insight.Enabled = false }, []pullTarget{embeddings}},
		{"term labels", func(c *config.Config) { c.Insight.Labeling = "terms" }, []pullTarget{embeddings}},
		{"labels on the embedding model", func(c *config.Config) {
			c.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleLabels: {Model: "qwen3-embedding:0.6b"}}
		}, []pullTarget{{baseURL: local, model: "qwen3-embedding:0.6b", usedFor: "embeddings, interest labels",
			timeout: time.Minute}}},
		{"one server with and without a trailing slash", func(c *config.Config) {
			c.Generation.BaseURL = local + "/"
			c.Generation.Model = "qwen3-embedding:0.6b"
		}, []pullTarget{{baseURL: local, model: "qwen3-embedding:0.6b", usedFor: "embeddings, interest labels",
			timeout: time.Minute}}},
		{"the labels role on its own server", func(c *config.Config) {
			c.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleLabels: {BaseURL: "http://127.0.0.1:11500/"}}
		}, []pullTarget{embeddings, {baseURL: "http://127.0.0.1:11500", model: "qwen3:4b-instruct",
			usedFor: "interest labels", timeout: 2 * time.Minute}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			tc.mutate(&cfg)
			require.NoError(t, cfg.Validate())
			assert.Equal(t, tc.want, pullTargets(cfg))
		})
	}
}

// TestGenerationRoles: healthz's roles are those in use, each with the
// model it resolves to.
func TestGenerationRoles(t *testing.T) {
	cfg := config.Default()
	assert.Equal(t, map[string]string{"labels": "qwen3:4b-instruct"}, generationRoles(cfg))
	cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleLabels: {Model: "gemma4:12b"}}
	assert.Equal(t, map[string]string{"labels": "gemma4:12b"}, generationRoles(cfg))
	cfg.Insight.Labeling = "terms"
	assert.Equal(t, map[string]string{}, generationRoles(cfg))
}

// syncBuffer is a bytes.Buffer safe for a logger's concurrent writes and
// the test's reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestKeepPulled: each target is pulled once, its log lines naming what it
// is used for.
func TestKeepPulled(t *testing.T) {
	var mu sync.Mutex
	pulls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[]}`)
		case "/api/pull":
			var req struct {
				Model string `json:"model"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			pulls[req.Model]++
			mu.Unlock()
			fmt.Fprintln(w, `{"status":"success"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	var logs syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	require.NoError(t, keepPulled(ctx, []pullTarget{
		{baseURL: srv.URL, model: "a:1", usedFor: "embeddings, interest labels", timeout: 5 * time.Second},
		{baseURL: srv.URL, model: "b:1", usedFor: "interest labels", timeout: 5 * time.Second},
	}, slog.New(slog.NewJSONHandler(&logs, nil))))
	require.Eventually(t, func() bool { return strings.Count(logs.String(), `"msg":"ollama model ready"`) == 2 },
		10*time.Second, 10*time.Millisecond)
	assert.Contains(t, logs.String(), `"msg":"ollama model ready","used_for":"embeddings, interest labels","model":"a:1"`)
	assert.Contains(t, logs.String(), `"msg":"ollama model ready","used_for":"interest labels","model":"b:1"`)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, map[string]int{"a:1": 1, "b:1": 1}, pulls)
}

// clusterJobs lists the pending cluster jobs' payloads.
func clusterJobs(t *testing.T, db *sqlitestore.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT payload FROM jobs WHERE kind = 'cluster' AND status = 'pending'`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var payload string
		require.NoError(t, rows.Scan(&payload))
		out = append(out, payload)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestStart_ChecksTheInterests: a daemon checks the interests once before
// its API is up and any worker claims: a library no rebuild has grouped,
// with 20 documents indexed and nothing since, is due, its first rebuild
// waiting for the drift monitor's first verdict; a new, empty one is
// reported waiting for documents; with insight off there is no scheduler.
func TestStart_ChecksTheInterests(t *testing.T) {
	startOn := func(t *testing.T, home *curiohome.Home, enabled bool) (*daemon, *sqlitestore.DB, *recorder) {
		t.Helper()
		cfg, err := config.Load(home.ConfigPath())
		require.NoError(t, err)
		cfg.Insight.Enabled = enabled
		meta, err := home.Meta()
		require.NoError(t, err)
		db, err := sqlitestore.Open(context.Background(), home.DBPath())
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, db.Close()) })
		logs := recordLogs(t)
		d, err := start(context.Background(), cfg, home, meta, db, api.NewStartup())
		require.NoError(t, err)
		return d, db, logs
	}

	t.Run("a new home", func(t *testing.T) {
		d, db, logs := startOn(t, newHome(t, freeLoopbackAddr(t)), true)
		assert.Empty(t, clusterJobs(t, db))
		assert.Empty(t, logs.messages("interests: rebuild enqueued"))
		require.NotNil(t, d.scheduler)
		snap := d.scheduler.Snapshot()
		assert.Equal(t, insight.StateNone, snap.State, "checked before the API is up")
		assert.Equal(t, insight.FirstRebuildAt, snap.RebuildAt)
		assert.Equal(t, d.scheduler, d.apiDeps.Interests, "the API serves its snapshot")
	})
	t.Run("a library to group", func(t *testing.T) {
		home := newHome(t, freeLoopbackAddr(t))
		db, err := sqlitestore.Open(context.Background(), home.DBPath())
		require.NoError(t, err)
		_, err = sqlitestore.Migrate(context.Background(), db)
		require.NoError(t, err)
		for i := range insight.FirstRebuildAt {
			_, err := db.Exec(`INSERT INTO documents (id, tenant_id, url, state, indexed_at)
				VALUES (?, 'local', ?, 'fetched', '2026-01-01T00:00:00.000Z')`,
				fmt.Sprintf("doc-%d", i), fmt.Sprintf("https://example.com/%d", i))
			require.NoError(t, err)
		}
		require.NoError(t, db.Close())

		// The first rebuild is due at start, but waits for the drift
		// monitor's first verdict, which comes once the daemon serves
		// (Scheduler.Run, TestScheduler_RunWaitsForTheFirstDriftCheck).
		d, db, logs := startOn(t, home, true)
		assert.Empty(t, clusterJobs(t, db))
		assert.Empty(t, logs.messages("interests: rebuild enqueued"))
		due := logs.messages("interests: rebuild due")
		require.Len(t, due, 1)
		assert.Equal(t, "the first embedding check since the daemon started", due[0]["waiting_for"])
		assert.Equal(t, insight.StateDue, d.scheduler.Snapshot().State)
	})
	t.Run("insight off", func(t *testing.T) {
		d, db, _ := startOn(t, newHome(t, freeLoopbackAddr(t)), false)
		assert.Empty(t, clusterJobs(t, db))
		assert.Nil(t, d.scheduler)
		assert.Nil(t, d.apiDeps.Interests, "no scheduler, so the API reports off")
	})
}

// seedHome migrates home's database and runs seed on it, as a previous
// daemon would have left it.
func seedHome(t *testing.T, home *curiohome.Home, seed func(db *sqlitestore.DB)) {
	t.Helper()
	db, err := sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	_, err = sqlitestore.Migrate(context.Background(), db)
	require.NoError(t, err)
	seed(db)
	require.NoError(t, db.Close())
}

// startHome runs start on home's database, recording the logs, and
// returns the daemon and the database it started on.
func startHome(t *testing.T, home *curiohome.Home) (*daemon, *sqlitestore.DB, *recorder) {
	t.Helper()
	cfg, err := config.Load(home.ConfigPath())
	require.NoError(t, err)
	meta, err := home.Meta()
	require.NoError(t, err)
	db, err := sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	logs := recordLogs(t)
	d, err := start(context.Background(), cfg, home, meta, db, api.NewStartup())
	require.NoError(t, err, "the documents' fields never fail the start")
	return d, db, logs
}

// at returns the records at level whose message starts with prefix.
func (r *recorder) at(level slog.Level, prefix string) []slog.Record {
	var out []slog.Record
	for _, rec := range r.all() {
		if rec.Level == level && strings.HasPrefix(rec.Message, prefix) {
			out = append(out, rec)
		}
	}
	return out
}

// TestStart_DocumentFields: a start that can't derive the documents'
// search fields still starts, logs one ERROR and reports the error on
// healthz; one that finds their index inconsistent rebuilds it, logs one
// WARN and reports the rebuild; a sound one reports its check alone.
func TestStart_DocumentFields(t *testing.T) {
	t.Run("a refresh that fails", func(t *testing.T) {
		home := newHome(t, freeLoopbackAddr(t))
		seedHome(t, home, func(db *sqlitestore.DB) {
			_, err := db.Exec(`
				INSERT INTO documents (id, tenant_id, url) VALUES ('doc', 'local', 'https://example.com/a');
				CREATE TRIGGER t_fail BEFORE INSERT ON document_fields BEGIN SELECT RAISE(ABORT, 'injected'); END;`)
			require.NoError(t, err)
		})
		d, _, logs := startHome(t, home)
		failed := logs.at(slog.LevelError, "document fields")
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Message, "search may miss documents")
		assert.Contains(t, failed[0].Message, "the next start tries again")
		got := d.apiDeps.DocumentFields
		assert.Contains(t, got.Error, "injected")
		assert.False(t, got.CheckedAt.IsZero())
		assert.Empty(t, logs.messages("document fields refreshed"))
	})
	t.Run("an inconsistent index", func(t *testing.T) {
		home := newHome(t, freeLoopbackAddr(t))
		seedHome(t, home, func(db *sqlitestore.DB) {
			_, err := sqlitestore.NewBookmarks(db).Ingest(context.Background(), &store.Bookmark{TenantID: "local",
				URL: "https://example.com/a", Source: store.SourceChrome, SavedAt: time.Now(), Title: new("Kafka")})
			require.NoError(t, err)
			var trigger string
			require.NoError(t, db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'trg_document_fields_update'`).
				Scan(&trigger))
			for _, q := range []string{`DROP TRIGGER trg_document_fields_update`,
				`UPDATE document_fields SET title = 'Zookeeper'`, trigger} {
				_, err := db.Exec(q)
				require.NoError(t, err)
			}
		})
		d, db, logs := startHome(t, home)
		assert.Len(t, logs.at(slog.LevelWarn, "document fields"), 1)
		assert.Empty(t, logs.at(slog.LevelError, "document fields"))
		assert.Equal(t, api.DocumentFields{CheckedAt: d.apiDeps.DocumentFields.CheckedAt, Rebuilt: true},
			d.apiDeps.DocumentFields)
		_, err := db.Exec(`INSERT INTO document_fields_fts (document_fields_fts, rank) VALUES ('integrity-check', 1)`)
		require.NoError(t, err, "the index matches the fields again")
		refreshed := logs.messages("document fields refreshed")
		require.Len(t, refreshed, 1)
		assert.Equal(t, true, refreshed[0]["rebuilt"])
	})
	t.Run("a sound home", func(t *testing.T) {
		d, _, logs := startHome(t, newHome(t, freeLoopbackAddr(t)))
		assert.Empty(t, logs.at(slog.LevelWarn, "document fields"))
		assert.False(t, d.apiDeps.DocumentFields.Rebuilt)
		assert.Empty(t, d.apiDeps.DocumentFields.Error)
		assert.False(t, d.apiDeps.DocumentFields.CheckedAt.IsZero())
	})
}

// libraryBeforeContext is a home's library as 2.6 left it, each document
// named by what it is: fetched with chunks embedded before chunk context
// (two of them, one of whose index job is pending), failed, dead and
// pending with such chunks, fetched without chunks, and fetched with
// chunks of the current context. Every card is embedded, so the start's
// own steps leave the cards as they are.
type libraryBeforeContext struct {
	stale, queued, failed, dead, pending, noChunks, current string
}

func seedLibraryBeforeContext(t *testing.T, home *curiohome.Home) libraryBeforeContext {
	t.Helper()
	var lib libraryBeforeContext
	seedHome(t, home, func(db *sqlitestore.DB) {
		ctx := context.Background()
		docs := sqlitestore.NewDocuments(db)
		exts := sqlitestore.NewExtractions(db)
		add := func(name string, state store.DocState, version int, chunks bool) string {
			doc := &store.Document{TenantID: store.LocalTenantID, URL: "https://example.com/" + name}
			require.NoError(t, docs.Create(ctx, doc))
			ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK}
			require.NoError(t, exts.Create(ctx, ext))
			require.NoError(t, docs.SetCurrentExtraction(ctx, doc.ID, ext.ID))
			if chunks {
				// The rows alone: no start step reads a chunk's vector.
				_, err := db.Exec(`INSERT INTO chunks (id, document_id, extraction_id, ord, text, context_version)
					VALUES (?, ?, ?, 0, 'text', ?)`, doc.ID+"-chunk", doc.ID, ext.ID, version)
				require.NoError(t, err)
			}
			switch state {
			case store.DocStateFetched:
				require.NoError(t, docs.MarkFetched(ctx, doc.ID, 1))
			case store.DocStateFailed:
				require.NoError(t, docs.MarkFailed(ctx, doc.ID, store.FailureCauseAntiBot))
			case store.DocStateDead:
				require.NoError(t, docs.MarkFailed(ctx, doc.ID, store.FailureCauseDeadLink))
			case store.DocStatePending:
			}
			return doc.ID
		}
		lib = libraryBeforeContext{
			stale:    add("stale", store.DocStateFetched, 0, true),
			queued:   add("queued", store.DocStateFetched, 0, true),
			failed:   add("failed", store.DocStateFailed, 0, true),
			dead:     add("dead", store.DocStateDead, 0, true),
			pending:  add("pending", store.DocStatePending, 0, true),
			noChunks: add("no-chunks", store.DocStateFetched, 0, false),
			current:  add("current", store.DocStateFetched, indexer.ContextVersion, true),
		}
		job, err := store.NewDocumentJob(store.LocalTenantID, store.JobKindIndex, lib.queued)
		require.NoError(t, err)
		require.NoError(t, sqlitestore.NewJobs(db).Enqueue(ctx, job))
		// The card index as an earlier start made it, so this start's own
		// steps leave the cards alone.
		meta, err := home.Meta()
		require.NoError(t, err)
		require.NoError(t, sqlitestore.EnsureVectorIndex(ctx, db, meta.EmbeddingDim))
		_, err = db.Exec(`UPDATE document_cards SET embedded_text = text, embedded_at = '2026-10-01T00:00:00.000Z';
			DELETE FROM jobs WHERE kind = 'cards'`)
		require.NoError(t, err)
	})
	return lib
}

// reembedJobs is, for each document with index jobs, how many and how
// many of them re-embed it.
func reembedJobs(t *testing.T, db *sqlitestore.DB) map[string][2]int {
	t.Helper()
	rows, err := db.Query(`SELECT document_id, count(*), sum(coalesce(json_extract(payload, '$.reembed'), 0))
		FROM jobs WHERE kind = 'index' GROUP BY document_id`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string][2]int{}
	for rows.Next() {
		var (
			id         string
			n, reembed int
		)
		require.NoError(t, rows.Scan(&id, &n, &reembed))
		out[id] = [2]int{n, reembed}
	}
	require.NoError(t, rows.Err())
	return out
}

// TestStart_BeginsTheChunkContextReembed: a start whose library holds
// fetched documents whose chunks predate the chunk context owes the
// interests a fresh rebuild, queues a re-embed job for the first of them
// an index job doesn't already re-embed, and no more, logs it once, and
// leaves every other document, the cards and the embedding baseline alone;
// a second start queues nothing more, the job in flight holding its place;
// a step that fails logs an ERROR, and a start whose search fields couldn't
// be checked a WARN, and the daemon starts anyway, with no re-embed.
func TestStart_BeginsTheChunkContextReembed(t *testing.T) {
	home := newHome(t, freeLoopbackAddr(t))
	lib := seedLibraryBeforeContext(t, home)
	meta, err := home.Meta()
	require.NoError(t, err)
	meta.EmbeddingModelDigest, meta.OllamaVersion = "sha256:recorded", "0.34.4"
	require.NoError(t, home.WriteMeta(meta))

	d, db, logs := startHome(t, home)
	assert.Equal(t, map[string][2]int{lib.stale: {1, 1}, lib.queued: {1, 0}}, reembedJobs(t, db))
	assert.True(t, d.reembedding, "serve feeds the rest")
	began := logs.messages("chunk context: re-embedding the library's chunks")
	require.Len(t, began, 1)
	assert.Equal(t, map[string]any{"documents": int64(2), "chunks": int64(2), "queued": int64(1),
		"in_flight": int64(1), "context_version": int64(indexer.ContextVersion)}, began[0])
	assert.Equal(t, jobs.ReembedProgress{Active: true, Began: d.reembed.Progress().Began,
		Of: store.ReembedCount{Documents: 2, Chunks: 2}, Left: store.ReembedCount{Documents: 2, Chunks: 2}},
		d.reembed.Progress())
	var owed string
	require.NoError(t, db.QueryRow(`SELECT fresh_owed FROM insight_state WHERE tenant_id = 'local'`).Scan(&owed))
	assert.Equal(t, string(store.FreshReindex), owed)
	var staleCards, cardsJobs int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM document_cards WHERE embedded_text IS NOT text`).Scan(&staleCards))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'cards'`).Scan(&cardsJobs))
	assert.Zero(t, staleCards, "no card made stale")
	assert.Zero(t, cardsJobs, "no cards job queued")
	after, err := home.Meta()
	require.NoError(t, err)
	assert.Equal(t, "sha256:recorded", after.EmbeddingModelDigest, "the drift baseline is left alone")
	assert.Equal(t, "0.34.4", after.OllamaVersion)

	d, db, logs = startHome(t, home)
	assert.Equal(t, map[string][2]int{lib.stale: {1, 1}, lib.queued: {1, 0}}, reembedJobs(t, db),
		"a second start queues nothing more")
	assert.True(t, d.reembedding, "both still to re-embed")
	assert.Equal(t, int64(0), logs.messages("chunk context: re-embedding the library's chunks")[0]["queued"])

	t.Run("a step that fails", func(t *testing.T) {
		home := newHome(t, freeLoopbackAddr(t))
		seedLibraryBeforeContext(t, home)
		seedHome(t, home, func(db *sqlitestore.DB) {
			_, err := db.Exec(`CREATE TRIGGER t_fail BEFORE INSERT ON jobs WHEN json_extract(new.payload, '$.reembed')
				BEGIN SELECT RAISE(ABORT, 'injected'); END`)
			require.NoError(t, err)
		})
		d, _, logs := startHome(t, home)
		require.Len(t, logs.at(slog.LevelError, "chunk context"), 1)
		assert.False(t, d.reembedding, "nothing to feed")
	})

	t.Run("search fields that couldn't be checked", func(t *testing.T) {
		home := newHome(t, freeLoopbackAddr(t))
		lib := seedLibraryBeforeContext(t, home)
		seedHome(t, home, func(db *sqlitestore.DB) {
			_, err := db.Exec(`
				INSERT INTO documents (id, tenant_id, url) VALUES ('no-fields', 'local', 'https://example.com/nf');
				CREATE TRIGGER t_fail BEFORE INSERT ON document_fields BEGIN SELECT RAISE(ABORT, 'injected'); END;`)
			require.NoError(t, err)
		})
		d, db, logs := startHome(t, home)
		require.NotEmpty(t, d.apiDeps.DocumentFields.Error)
		waits := logs.at(slog.LevelWarn, "chunk context")
		require.Len(t, waits, 1)
		assert.Contains(t, waits[0].Message, "the next start begins it")
		assert.False(t, d.reembedding)
		assert.Equal(t, map[string][2]int{lib.queued: {1, 0}}, reembedJobs(t, db), "no re-embed queued")
		var owed int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM insight_state WHERE fresh_owed IS NOT NULL`).Scan(&owed))
		assert.Zero(t, owed, "no fresh rebuild owed")
	})
}

// TestStart_SavedDates: the start marks dateless the Safari bookmarks an
// older daemon wrote dated, classifies every bookmark's saved date and
// logs one INFO with the counts, reporting when on healthz; a bookmark
// whose time doesn't parse is one WARN naming its ID; one that can't
// classify still starts, logging one ERROR and reporting it; and a
// shutdown during it fails the start, logging nothing.
func TestStart_SavedDates(t *testing.T) {
	// Five bookmarks of one bulk stamp, a lone save and a Safari bookmark,
	// inserted as an older daemon would, with no class and dated.
	const bookmarks = `INSERT INTO bookmarks (id, tenant_id, url, saved_at, source, folder_path) VALUES
		('b1', 'local', 'https://example.com/1', '2024-01-01T00:00:00.000Z', 'chrome', '/A'),
		('b2', 'local', 'https://example.com/2', '2024-01-01T00:00:00.100Z', 'chrome', '/A'),
		('b3', 'local', 'https://example.com/3', '2024-01-01T00:00:00.200Z', 'chrome', '/B'),
		('b4', 'local', 'https://example.com/4', '2024-01-01T00:00:00.300Z', 'chrome', '/B'),
		('b5', 'local', 'https://example.com/5', '2024-01-01T00:00:00.400Z', 'chrome', '/C'),
		('b6', 'local', 'https://example.com/6', '2025-01-01T00:00:00.000Z', 'chrome', '/A'),
		('s1', 'local', 'https://example.com/7', '2025-02-01T00:00:00.000Z', 'safari', '/A')`
	t.Run("a sound home", func(t *testing.T) {
		home := newHome(t, freeLoopbackAddr(t))
		seedHome(t, home, func(db *sqlitestore.DB) {
			_, err := db.Exec(bookmarks)
			require.NoError(t, err)
		})
		d, db, logs := startHome(t, home)
		classified := logs.messages("saved dates classified")
		require.Len(t, classified, 1)
		assert.Equal(t, int64(7), classified[0]["bookmarks"])
		assert.Equal(t, int64(1), classified[0]["changed"], "the lone save; the stamp was unknown already")
		assert.Equal(t, []any{int64(1), int64(0), int64(6)},
			[]any{classified[0]["exact"], classified[0]["approx"], classified[0]["unknown"]})
		assert.Equal(t, int64(1), classified[0]["marked_dateless"])
		assert.Contains(t, classified[0], "took_ms")
		var dateless bool
		require.NoError(t, db.QueryRow(`SELECT dateless FROM bookmarks WHERE id = 's1'`).Scan(&dateless))
		assert.True(t, dateless)
		assert.Empty(t, logs.at(slog.LevelWarn, "saved dates"))
		state := d.savedDates.State()
		assert.Equal(t, api.SavedDates{CheckedAt: state.CheckedAt, Changed: 1, Owed: []api.OwedClassification{}}, state)
		assert.False(t, state.CheckedAt.IsZero())
		assert.Same(t, d.savedDates, d.apiDeps.SavedDates)
	})
	t.Run("a time that doesn't parse", func(t *testing.T) {
		home := newHome(t, freeLoopbackAddr(t))
		seedHome(t, home, func(db *sqlitestore.DB) {
			_, err := db.Exec(bookmarks + `;
				UPDATE bookmarks SET saved_at = 'sometime in 2024' WHERE id = 'b6'`)
			require.NoError(t, err)
		})
		_, _, logs := startHome(t, home)
		skipped := logs.at(slog.LevelWarn, "saved dates")
		require.Len(t, skipped, 1)
		attrs := map[string]any{}
		skipped[0].Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		assert.Equal(t, int64(1), attrs["skipped"])
		assert.Equal(t, []string{"b6"}, attrs["ids"])
		assert.NotContains(t, fmt.Sprint(attrs), "sometime", "a row's ID alone")
		assert.Len(t, logs.messages("saved dates classified"), 1)
	})
	t.Run("a classification that fails", func(t *testing.T) {
		home := newHome(t, freeLoopbackAddr(t))
		seedHome(t, home, func(db *sqlitestore.DB) {
			_, err := db.Exec(bookmarks + `;
				CREATE TRIGGER t_fail BEFORE UPDATE OF saved_date ON bookmarks BEGIN SELECT RAISE(ABORT, 'injected'); END;`)
			require.NoError(t, err)
		})
		d, _, logs := startHome(t, home)
		failed := logs.at(slog.LevelError, "saved dates")
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Message, "the next start tries again")
		assert.Empty(t, logs.messages("saved dates classified"))
		state := d.savedDates.State()
		assert.Contains(t, state.Error, "injected")
		assert.False(t, state.CheckedAt.IsZero())
	})
	t.Run("a shutdown during it", func(t *testing.T) {
		db, err := sqlitestore.Open(context.Background(), filepath.Join(t.TempDir(), "curio.db"))
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, db.Close()) })
		_, err = sqlitestore.Migrate(context.Background(), db)
		require.NoError(t, err)
		logs := recordLogs(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = ensureSavedDates(ctx, db)
		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, logs.at(slog.LevelError, "saved dates"))
	})
}

// TestHoldReason: a drift the sample verified holds the interests'
// rebuilds as drifted, one it couldn't verify as perhaps drifted, and no
// drift holds nothing.
func TestHoldReason(t *testing.T) {
	changed := []drift.Change{{What: "ModelDigest", Recorded: "sha256:a", Current: "sha256:b"}}
	for want, r := range map[string]drift.Report{
		"":                                {},
		"the embeddings drifted":          {Changes: changed, Evidence: drift.Evidence{Verified: true}},
		"the embeddings may have drifted": {Changes: changed, Evidence: drift.Evidence{Reason: "Ollama is unreachable"}},
	} {
		assert.Equal(t, want, holdReason(r))
	}
}

// TestDriftChecked: after a start, the interests wait for the drift
// monitor's first verdict, a clean one or a drift alike, or for the grace
// to pass without one; placement is held meanwhile too, as by a drift.
func TestDriftChecked(t *testing.T) {
	started := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	now := started
	var report drift.Report
	checked := driftChecked(func() drift.Report { return report }, started, func() time.Time { return now })
	hold := placementHold(func() string { return holdReason(report) }, checked)

	assert.False(t, checked(), "no verdict yet")
	assert.Equal(t, "the embedding check hasn't concluded since the daemon started", hold())
	report = drift.Report{CheckedAt: started.Add(time.Second)}
	assert.True(t, checked(), "a clean verdict")
	assert.Empty(t, hold())
	report = drift.Report{Changes: []drift.Change{{What: "OllamaVersion", Recorded: "0.34.4", Current: "0.35.0"}},
		Evidence: drift.Evidence{Verified: true}, CheckedAt: started.Add(time.Second)}
	assert.True(t, checked(), "a drift is a verdict too")
	assert.Equal(t, "the embeddings drifted", hold(), "and holds as a drift")

	report = drift.Report{} // the monitor can't check: Ollama is down
	now = started.Add(driftCheckGrace - time.Second)
	assert.False(t, checked())
	now = started.Add(driftCheckGrace)
	assert.True(t, checked(), "past the grace, the interests go ahead")
	assert.Empty(t, hold())
}

// TestIndexing: the engine's re-embedding check reads the index jobs
// pending or running, no other kind.
func TestIndexing(t *testing.T) {
	ctx := context.Background()
	queue := sqlitestore.NewJobs(sqlitetest.NewDB(t))
	busy := indexing(queue)
	got, err := busy(ctx)
	require.NoError(t, err)
	assert.False(t, got, "an empty queue")
	require.NoError(t, queue.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch}))
	got, err = busy(ctx)
	require.NoError(t, err)
	assert.False(t, got, "a fetch job")
	require.NoError(t, queue.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindIndex}))
	got, err = busy(ctx)
	require.NoError(t, err)
	assert.True(t, got, "an index job")
}

// TestRun_WarnsOfDeprecatedKeys: a config.yaml that sets an insight key
// nothing reads any more starts, with one warning that names the key.
func TestRun_WarnsOfDeprecatedKeys(t *testing.T) {
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen)
	cfg, err := os.ReadFile(home.ConfigPath())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(home.ConfigPath(),
		[]byte(strings.Replace(string(cfg), "insight:\n", "insight:\n  min_similarity: 0.3\n", 1)), 0o600))
	logs := recordLogs(t)

	_, stop := runDaemon(t, listen)
	stop()

	var warns []slog.Record
	for _, rec := range logs.all() {
		if rec.Level == slog.LevelWarn && strings.Contains(rec.Message, "no longer do anything") {
			warns = append(warns, rec)
		}
	}
	require.Len(t, warns, 1)
	attrs := logs.messages(warns[0].Message)[0]
	assert.Equal(t, []string{"insight.min_similarity"}, attrs["keys"])
	assert.Equal(t, home.ConfigPath(), attrs["config"])
}

// TestRun_RefusalsExitZero: what run returns for a home or config.yaml it
// refuses is a refusal, which exits 0 so launchd doesn't relaunch the
// daemon every 10 seconds for nothing; a port it can't bind may free up,
// and still exits 1.
func TestRun_RefusalsExitZero(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T)
		want  int
	}{
		{"a legacy home", func(t *testing.T) {
			home := newHome(t, freeLoopbackAddr(t))
			require.NoError(t, os.WriteFile(home.MarkerPath(), []byte(legacyMarker), 0o600))
		}, 0},
		{"an invalid config.yaml", func(t *testing.T) {
			home := newHome(t, freeLoopbackAddr(t))
			require.NoError(t, os.WriteFile(home.ConfigPath(), []byte("embedding:\n  base_url: localhost:11434\n"), 0o600))
		}, 0},
		{"a directory that isn't a curio home", func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o600))
			t.Setenv("CURIO_HOME", dir)
		}, 0},
		{"a taken port", func(t *testing.T) {
			occupied, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { occupied.Close() })
			newHome(t, occupied.Addr().String())
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			err := run(context.Background(), new(slog.LevelVar))
			require.Error(t, err)
			assert.Equal(t, tc.want, exitCode(err, false), "%v", err)
		})
	}
}

// TestExitCode: the exit status the launchd agent's KeepAlive
// {SuccessfulExit: false} acts on. A daemon told to stop, one that finds
// another serving its home, and one refusing to start for a cause that
// stays until it is fixed exit 0, which launchd leaves alone; anything
// else exits 1, which launchd retries.
func TestExitCode(t *testing.T) {
	dir := t.TempDir()
	badConfig := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(badConfig, []byte("daemon:\n  speed: fast\n"), 0o600))
	_, configErr := config.Load(badConfig)
	require.Error(t, configErr)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupied.Close()
	_, bindErr := (&net.ListenConfig{}).Listen(context.Background(), "tcp", occupied.Addr().String())
	require.Error(t, bindErr)

	cases := []struct {
		name      string
		err       error
		signalled bool
		want      int
	}{
		{"a clean exit", nil, false, 0},
		{"stopped by a signal", context.Canceled, true, 0},
		{"a shutdown that ran over", fmt.Errorf("shut down api: %w", context.DeadlineExceeded), true, 0},
		{"another daemon serves the home", fmt.Errorf("%w for %s (pid 42)", daemonctl.ErrAlreadyRunning, dir), false, 0},
		{"cancelled", context.Canceled, false, 0},
		{"a refusal: a config it can't load", &refusal{configErr}, false, 0},
		{"a refusal: a legacy home", &refusal{curiohome.ErrLegacyHome}, false, 0},
		{"a refusal, joined with the API's shutdown", errors.Join(&refusal{
			&sqlitestore.VectorWidthError{Path: "curio.db", Have: 768, Want: 1024}}, nil), false, 0},
		{"a config error run didn't mark", configErr, false, 1},
		{"a port it can't bind", fmt.Errorf("listen on %s: %w", occupied.Addr(), bindErr), false, 1},
		{"a database it can't migrate", errors.New("migrate: no such table: jobs"), false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, exitCode(tc.err, tc.signalled))
		})
	}
}

// TestFinish_Logs: a shutdown that failed is still logged as an error
// though the daemon exits 0, and a daemon that finds its home served says
// by whom, once, as a warning.
func TestFinish_Logs(t *testing.T) {
	levels := func(logs *recorder) map[string]slog.Level {
		logs.mu.Lock()
		defer logs.mu.Unlock()
		out := map[string]slog.Level{}
		for _, r := range logs.records {
			out[r.Message] = r.Level
		}
		return out
	}

	logs := recordLogs(t)
	code := finish(fmt.Errorf("shut down api: %w", context.DeadlineExceeded), true, errors.New("terminated signal received"))
	assert.Equal(t, 0, code)
	assert.Equal(t, map[string]slog.Level{"daemon exited with error": slog.LevelError, "curio-daemon stopped": slog.LevelInfo},
		levels(logs))
	assert.Equal(t, "terminated signal received", logs.messages("curio-daemon stopped")[0]["cause"])

	logs = recordLogs(t)
	code = finish(fmt.Errorf("%w for /Users/x/.curio (pid 42)", daemonctl.ErrAlreadyRunning), false, nil)
	assert.Equal(t, 0, code)
	const served = "another curio-daemon already serves this home; exiting " +
		"(to replace the running build with this one, run `curio daemon stop` first)"
	assert.Equal(t, map[string]slog.Level{served: slog.LevelWarn}, levels(logs))
	assert.Contains(t, fmt.Sprint(logs.messages(served)[0]["err"]), "pid 42")

	logs = recordLogs(t)
	code = finish(&refusal{fmt.Errorf("%w: /Users/x/.curio was made before home format 2", curiohome.ErrLegacyHome)}, false, nil)
	assert.Equal(t, 0, code)
	const refused = "curio-daemon refuses to start, and stays down until the cause is fixed; " +
		"then a curio command (or `curio up`) starts it"
	assert.Equal(t, map[string]slog.Level{refused: slog.LevelError}, levels(logs), "one error, nothing else")
	assert.Contains(t, fmt.Sprint(logs.messages(refused)[0]["err"]), "home format 2")

	logs = recordLogs(t)
	assert.Equal(t, 0, finish(context.Canceled, true, errors.New("interrupt signal received")))
	assert.Equal(t, map[string]slog.Level{"curio-daemon stopped": slog.LevelInfo}, levels(logs), "nothing to report")
}

// TestNewDispatcher_YouTube: whether yt-dlp was found is logged either
// way; a daemon whose PATH lacks it (a launchd agent's, say) says so.
// TestNewDispatcher_GitHubToken: healthz's github_token says whether the
// GitHub fetcher sends a token, from config.yaml or the daemon's own
// environment.
func TestNewDispatcher_GitHubToken(t *testing.T) {
	home, err := curiohome.Init(t.TempDir(), "qwen3-embedding:0.6b", 1024)
	require.NoError(t, err)
	cases := []struct {
		name, config, env string
		want              bool
	}{
		{"none", "", "", false},
		{"config.yaml", "github_pat_x", "", true},
		{"environment", "", "github_pat_y", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CURIO_GITHUB_TOKEN", tc.env)
			cfg := config.Default()
			cfg.Fetcher.GitHub.Token = tc.config
			_, routed, err := newDispatcher(cfg, home, newNativeFetcher(cfg))
			require.NoError(t, err)
			assert.Equal(t, tc.want, routed.githubToken)
		})
	}
}

func TestNewDispatcher_YouTube(t *testing.T) {
	home, err := curiohome.Init(t.TempDir(), "qwen3-embedding:0.6b", 1024)
	require.NoError(t, err)
	found, err := os.Executable()
	require.NoError(t, err)
	cases := []struct {
		bin, msg, ytdlp string
	}{
		{"curio-no-such-yt-dlp", "youtube fetcher disabled: yt-dlp not found", ""},
		{found, "youtube fetcher enabled", found},
	}
	for _, tc := range cases {
		logs := recordLogs(t)
		cfg := config.Default()
		cfg.Fetcher.YouTube.Bin = tc.bin
		_, routed, err := newDispatcher(cfg, home, newNativeFetcher(cfg))
		require.NoError(t, err)
		assert.Equal(t, tc.ytdlp, routed.ytdlp, "what healthz reports as youtube_fetcher")
		got := logs.messages(tc.msg)
		require.Len(t, got, 1, tc.bin)
		assert.Equal(t, tc.bin, got[0]["bin"])
		if tc.bin != found {
			assert.Equal(t, os.Getenv("PATH"), got[0]["path"], "the PATH searched")
		}
	}
}
