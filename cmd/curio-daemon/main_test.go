package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
	"github.com/samsar/curio/migrations"
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
	db, err := sqlitestore.Open(context.Background(), home.DBPath())
	require.NoError(t, err)
	defer db.Close()
	_, err = sqlitestore.Migrate(context.Background(), db)
	require.NoError(t, err)

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
// the fix, and nothing logged on the way.
func TestRun_EmbeddingMismatchRefusesToStart(t *testing.T) {
	home := newHome(t, freeLoopbackAddr(t))
	cfg, err := os.ReadFile(home.ConfigPath())
	require.NoError(t, err)
	cfg = []byte(strings.Replace(string(cfg), "embedding:\n", "embedding:\n  model: mxbai-embed-large\n", 1))
	require.NoError(t, os.WriteFile(home.ConfigPath(), cfg, 0o600))
	seeded := seedJobs(t, home)
	logs := recordLogs(t)

	err = run(context.Background(), new(slog.LevelVar))
	require.Error(t, err)
	for _, want := range []string{
		home.ConfigPath(), `"mxbai-embed-large" (dim 768)`,
		home.MarkerPath(), `"nomic-embed-text" (dim 768)`,
		"set embedding.model and embedding.dim back", "different CURIO_HOME",
		`"Embedding model swap"`,
	} {
		assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(want))
	}
	assert.NotContains(t, err.Error(), "--reason")
	assertJobsUntouched(t, home, seeded)
	for _, r := range logs.records {
		assert.Less(t, r.Level, slog.LevelWarn, "logged %q; main logs the returned error once", r.Message)
	}
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
