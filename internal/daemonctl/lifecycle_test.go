package daemonctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/curiohome"
)

// The tests spawn this test binary as the "daemon": with fakeModeEnv set,
// TestMain runs runFakeDaemon instead of the tests. The fake speaks the real
// lock protocol and serves /v1/healthz with its identity on fakeAddrEnv.
const (
	fakeModeEnv = "CURIO_FAKE_DAEMON_MODE"
	fakeAddrEnv = "CURIO_FAKE_DAEMON_ADDR"
	// fakeStartingEnv is how long the starting modes report they are
	// migrating, as a time.Duration.
	fakeStartingEnv = "CURIO_FAKE_DAEMON_STARTING"

	modeNormal        = "normal"
	modeCrashOnStart  = "crash-on-start"
	modeExitOnStart   = "exit-on-start"
	modeIgnoreSIGTERM = "ignore-sigterm"
	// modeStartingThenReady migrates for fakeStartingEnv, then serves.
	modeStartingThenReady = "starting-then-ready"
	// modeStartingThenExit migrates for fakeStartingEnv, then exits 4.
	modeStartingThenExit = "starting-then-exit"
	// modeSilent binds its port and never answers.
	modeSilent = "silent"

	// spawnLog, in the home, gets one line per fake daemon started.
	spawnLog = "spawned.log"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeModeEnv); mode != "" {
		os.Exit(runFakeDaemon(mode))
	}
	os.Exit(m.Run())
}

func runFakeDaemon(mode string) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "fake daemon:", err)
		return 1
	}
	home, err := curiohome.Open(os.Getenv("CURIO_HOME"))
	if err != nil {
		return fail(err)
	}
	if err := appendLine(filepath.Join(home.Path, spawnLog), strconv.Itoa(os.Getpid())); err != nil {
		return fail(err)
	}
	switch mode {
	case modeCrashOnStart:
		fmt.Fprintln(os.Stderr, "fake daemon: boom")
		return 3
	case modeExitOnStart:
		fmt.Fprintln(os.Stderr, "fake daemon: told to stop while starting")
		return 0
	}

	lock, err := AcquireLock(home)
	if err != nil {
		return fail(err)
	}
	defer lock.Release()

	stop := make(chan os.Signal, 1)
	if mode == modeIgnoreSIGTERM {
		signal.Ignore(syscall.SIGTERM)
	} else {
		signal.Notify(stop, syscall.SIGTERM)
	}

	ln, err := net.Listen("tcp", os.Getenv(fakeAddrEnv))
	if err != nil {
		return fail(err)
	}
	if mode == modeSilent {
		fmt.Fprintln(os.Stderr, "fake daemon: bound, not answering")
	} else {
		go func() { _ = http.Serve(ln, fakeHealthz(mode, home.Path)) }()
	}

	var gaveUp <-chan time.Time
	if mode == modeStartingThenExit {
		gaveUp = time.After(startingFor())
	}
	select {
	case <-stop:
	case <-gaveUp:
		fmt.Fprintln(os.Stderr, "fake daemon: gave up migrating")
		return 4
	case <-time.After(time.Minute): // never outlive the test run
	}
	return 0
}

// startingFor is how long the fake reports it is starting.
func startingFor() time.Duration {
	d, err := time.ParseDuration(os.Getenv(fakeStartingEnv))
	if err != nil {
		return 0
	}
	return d
}

// fakeHealthz answers healthz as the fake daemon, starting first in the
// starting modes.
func fakeHealthz(mode, home string) http.Handler {
	readyAt := time.Now()
	if mode == modeStartingThenReady || mode == modeStartingThenExit {
		readyAt = readyAt.Add(startingFor())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if time.Now().Before(readyAt) {
			if err := writeStarting(w, os.Getpid(), home); err != nil {
				fmt.Fprintln(os.Stderr, "fake daemon:", err)
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "pid": os.Getpid(), "home": home})
	})
}

// writeStarting answers as the daemon with pid for home does while it
// migrates, 2 of 6 migrations in.
func writeStarting(w http.ResponseWriter, pid int, home string) error {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusServiceUnavailable)
	return json.NewEncoder(w).Encode(map[string]any{
		"type": "urn:curio:problem:daemon-starting", "title": "daemon starting", "status": http.StatusServiceUnavailable,
		"pid": pid, "home": home, "version": "test", "phase": "migrating",
		"migrations": map[string]any{"applied": 2, "total": 6},
	})
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, line)
	return errors.Join(err, f.Close())
}

// newTestController returns a Controller for a fresh home whose DaemonBin is
// this test binary. Any daemon still holding the home's lock when the test
// ends is killed.
func newTestController(t *testing.T, mode string) *Controller {
	t.Helper()
	dir := t.TempDir()
	home, err := curiohome.Init(dir, "nomic-embed-text", 768)
	require.NoError(t, err)

	addr := freeAddr(t)
	t.Setenv(fakeAddrEnv, addr)
	t.Setenv(fakeModeEnv, mode)

	exe, err := os.Executable()
	require.NoError(t, err)
	c := New(home, exe, "http://"+addr)
	c.StartTimeout = 10 * time.Second
	c.StopTimeout = 5 * time.Second

	t.Cleanup(func() {
		if held, pid, err := probeLock(home.PIDFile()); err == nil && held && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return c
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func spawnCount(t *testing.T, c *Controller) int {
	t.Helper()
	f, err := os.Open(filepath.Join(c.Home.Path, spawnLog))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	require.NoError(t, err)
	defer f.Close()
	n := 0
	for sc := bufio.NewScanner(f); sc.Scan(); {
		n++
	}
	return n
}

// serveHealth answers healthz at the controller's address with body, in
// process: a daemon for another home, one from before the lock protocol, or
// one whose identity disagrees with the lock.
func serveHealth(t *testing.T, c *Controller, body map[string]any) {
	t.Helper()
	serveHealthAfter(t, c, 0, body)
}

// serveHealthAfter is serveHealth for a daemon that takes delay to answer.
func serveHealthAfter(t *testing.T, c *Controller, delay time.Duration, body map[string]any) {
	t.Helper()
	serveHandler(t, c, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(body))
	})
}

// serveHandler serves handler at the controller's address until the test
// ends.
func serveHandler(t *testing.T, c *Controller, handler http.HandlerFunc) {
	t.Helper()
	ln, err := net.Listen("tcp", strings.TrimPrefix(c.BaseURL, "http://"))
	require.NoError(t, err)
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// startingDaemon is a daemon served in process that reports it is
// migrating until ready is called, then serves.
type startingDaemon struct {
	requests atomic.Int32 // healthz requests answered while starting
	isReady  atomic.Bool
}

// serveStarting answers healthz at the controller's address as the daemon
// with pid for home.
func serveStarting(t *testing.T, c *Controller, pid int, home string) *startingDaemon {
	t.Helper()
	d := &startingDaemon{}
	serveHandler(t, c, func(w http.ResponseWriter, _ *http.Request) {
		if d.isReady.Load() {
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"status": "ok", "pid": pid, "home": home}))
			return
		}
		d.requests.Add(1)
		assert.NoError(t, writeStarting(w, pid, home))
	})
	return d
}

func (d *startingDaemon) ready() { d.isReady.Store(true) }

// migrations records the OnMigrating calls a controller makes.
type migrations struct {
	mu    sync.Mutex
	calls []client.Startup
}

func (m *migrations) record(s client.Startup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, s)
}

func (m *migrations) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// onMigrating makes c record its OnMigrating calls.
func onMigrating(c *Controller) *migrations {
	m := &migrations{}
	c.OnMigrating = m.record
	return m
}

// holdLock makes the test process the daemon holding c's home lock until the
// test ends. flock locks belong to the open file, so the controller's own
// probes see it as held.
func holdLock(t *testing.T, c *Controller) {
	t.Helper()
	lock, err := AcquireLock(c.Home)
	require.NoError(t, err)
	// Registered after newTestController's cleanup, so it runs first: that
	// one kills whatever still holds the lock.
	t.Cleanup(func() { assert.NoError(t, lock.Release()) })
}

// startBystander runs a process that has nothing to do with curio and
// returns its PID and a channel closed when it exits.
func startBystander(t *testing.T) (int, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	return cmd.Process.Pid, exited
}

// assertNotSignalled fails the test if the bystander exits within a quarter
// second. A signalled sleep is reaped within milliseconds, but not before the
// call that signalled it returns, so a check that doesn't wait passes either
// way.
func assertNotSignalled(t *testing.T, exited <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-exited:
		t.Error(msg)
	case <-time.After(250 * time.Millisecond):
	}
}

func writePIDFile(t *testing.T, c *Controller, pid int) {
	t.Helper()
	require.NoError(t, os.WriteFile(c.Home.PIDFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600))
}

func TestEnsureRunning_ConcurrentStartersSpawnOnce(t *testing.T) {
	c := newTestController(t, modeNormal)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() { errs[i] = c.EnsureRunning(ctx) })
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, spawnCount(t, c))

	st, err := c.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, Running, st.State)
	require.NotNil(t, st.Health)
	assert.Equal(t, st.PID, st.Health.PID)
	assert.True(t, SameHome(c.Home.Path, st.Health.Home))

	stopped, err := c.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
	st, err = c.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, NotRunning, st.State, "a clean stop leaves an empty PID file")
	pidFile, err := os.ReadFile(c.Home.PIDFile())
	require.NoError(t, err)
	assert.Empty(t, pidFile)
}

// TestEnsureRunning_ServesTheControllersHome: the daemon a controller
// starts serves the controller's home, not whatever $CURIO_HOME the calling
// process has.
func TestEnsureRunning_ServesTheControllersHome(t *testing.T) {
	c := newTestController(t, modeNormal)
	elsewhere, err := curiohome.Init(t.TempDir(), "nomic-embed-text", 768)
	require.NoError(t, err)
	t.Setenv("CURIO_HOME", elsewhere.Path)
	ctx := context.Background()

	require.NoError(t, c.EnsureRunning(ctx))
	st, err := c.Status(ctx)
	require.NoError(t, err)
	require.NotNil(t, st.Health)
	assert.True(t, SameHome(c.Home.Path, st.Health.Home), "served %s", st.Health.Home)
	assert.Zero(t, spawnCount(t, &Controller{Home: elsewhere}), "nothing started for the other home")
}

// TestEnsureRunning_RefusesDaemonForAnotherHome: a daemon for another home
// on the port, serving or starting, is an error at once: a daemon started
// here could only fail to bind.
func TestEnsureRunning_RefusesDaemonForAnotherHome(t *testing.T) {
	for _, starting := range []bool{false, true} {
		t.Run(fmt.Sprintf("starting=%t", starting), func(t *testing.T) {
			c := newTestController(t, modeNormal)
			other := t.TempDir()
			if starting {
				serveStarting(t, c, 4242, other)
			} else {
				serveHealth(t, c, map[string]any{"status": "ok", "pid": 4242, "home": other})
			}

			start := time.Now()
			err := c.EnsureRunning(context.Background())
			require.Error(t, err)
			assert.Less(t, time.Since(start), c.StartTimeout/2)
			assert.Contains(t, err.Error(), other)
			assert.Contains(t, err.Error(), c.Home.Path)
			assert.Contains(t, err.Error(), "daemon.listen")
			assert.Zero(t, spawnCount(t, c))
		})
	}
}

// TestEnsureRunning_WaitsOutAMigratingChild: a spawned daemon that reports
// it is migrating for longer than StartTimeout is waited for, and is told
// about once, rather than failed for not being ready.
func TestEnsureRunning_WaitsOutAMigratingChild(t *testing.T) {
	c := newTestController(t, modeStartingThenReady)
	const migrating = 1500 * time.Millisecond
	t.Setenv(fakeStartingEnv, migrating.String())
	c.StartTimeout = 500 * time.Millisecond
	told := onMigrating(c)

	start := time.Now()
	require.NoError(t, c.EnsureRunning(context.Background()))
	assert.GreaterOrEqual(t, time.Since(start), migrating)
	assert.Equal(t, 1, spawnCount(t, c))
	require.Equal(t, 1, told.count())
	st, err := c.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, st.PID, told.calls[0].PID, "told about the daemon it spawned")
	assert.Equal(t, "migrating the database, 2 of 6 migrations applied", told.calls[0].Progress())
}

// TestEnsureRunning_SilentChild: a spawned daemon that binds but never
// answers fails the start after StartTimeout, not StartTimeout plus a
// probe's own timeout, with the tail of its log.
func TestEnsureRunning_SilentChild(t *testing.T) {
	c := newTestController(t, modeSilent)
	c.StartTimeout = time.Second

	start := time.Now()
	err := c.EnsureRunning(context.Background())
	require.Error(t, err)
	assert.Less(t, time.Since(start), c.StartTimeout+500*time.Millisecond)
	assert.Contains(t, err.Error(), "curio-daemon failed to start: no answer at "+c.BaseURL+" within 1s")
	assert.Contains(t, err.Error(), c.Home.DaemonLogPath())
	assert.Contains(t, err.Error(), "fake daemon: bound, not answering")
}

// TestEnsureRunning_ChildExitsWhileMigrating: a spawned daemon that dies
// part way through its migrations is reported as soon as it exits, not
// when the ready ceiling runs out.
func TestEnsureRunning_ChildExitsWhileMigrating(t *testing.T) {
	c := newTestController(t, modeStartingThenExit)
	const migrating = time.Second
	t.Setenv(fakeStartingEnv, migrating.String())
	c.ReadyTimeout = time.Minute
	told := onMigrating(c)

	start := time.Now()
	err := c.EnsureRunning(context.Background())
	require.Error(t, err)
	assert.Less(t, time.Since(start), migrating+1500*time.Millisecond, "reported when it exits")
	assert.Contains(t, err.Error(), "curio-daemon failed to start: exit status 4")
	assert.Contains(t, err.Error(), "fake daemon: gave up migrating")
	assert.NotErrorIs(t, err, ErrStillStarting)
	assert.Equal(t, 1, told.count())
}

// TestEnsureRunning_WaitsOnAMigratingHolder: a daemon this call didn't
// spawn, holding the lock and reporting it is migrating, is waited for past
// both StartTimeout and StopTimeout, and nothing is spawned.
func TestEnsureRunning_WaitsOnAMigratingHolder(t *testing.T) {
	c := newTestController(t, modeNormal)
	c.StartTimeout = 200 * time.Millisecond
	c.StopTimeout = 400 * time.Millisecond
	holdLock(t, c)
	d := serveStarting(t, c, os.Getpid(), c.Home.Path)
	const migrating = time.Second
	time.AfterFunc(migrating, d.ready)

	start := time.Now()
	require.NoError(t, c.EnsureRunning(context.Background()))
	assert.GreaterOrEqual(t, time.Since(start), migrating)
	assert.Zero(t, spawnCount(t, c))
}

// TestEnsureRunning_UnverifiedStartingAnswer: a starting answer for this
// home from a process that is neither our child nor the lock holder is no
// evidence of progress, so it doesn't keep the wait going.
func TestEnsureRunning_UnverifiedStartingAnswer(t *testing.T) {
	c := newTestController(t, modeNormal)
	c.StartTimeout = 200 * time.Millisecond
	c.StopTimeout = 400 * time.Millisecond
	c.ReadyTimeout = time.Minute
	holdLock(t, c)
	serveStarting(t, c, os.Getpid()+1, c.Home.Path)

	start := time.Now()
	err := c.EnsureRunning(context.Background())
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*c.StopTimeout, "the silence budget, not the ready ceiling")
	assert.NotErrorIs(t, err, ErrStillStarting)
	assert.Contains(t, err.Error(), "(starting up or shutting down)")
	assert.Zero(t, spawnCount(t, c))
}

// TestEnsureRunning_StillStarting: a daemon still migrating when
// ReadyTimeout runs out is not a failed start. The error says what it is
// doing and where to look, and the daemon is left to carry on.
func TestEnsureRunning_StillStarting(t *testing.T) {
	c := newTestController(t, modeNormal)
	c.ReadyTimeout = 300 * time.Millisecond
	holder, exited := startBystander(t)
	holdLock(t, c)
	writePIDFile(t, c, holder) // the lock vouches for the bystander
	serveStarting(t, c, holder, c.Home.Path)

	err := c.EnsureRunning(context.Background())
	require.ErrorIs(t, err, ErrStillStarting)
	for _, want := range []string{
		fmt.Sprintf("pid %d", holder), "migrating the database, 2 of 6 migrations applied",
		"keeps running", "`curio daemon status`", "`curio daemon logs -f`",
	} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "failed to start")
	assertNotSignalled(t, exited, "a daemon still starting was signalled")
}

// TestEnsureRunning_PollsAMigratingDaemonSlowly: a daemon that reports it
// is migrating is asked at most once a second, its Retry-After, not every
// 100ms: each request is a line in its log.
func TestEnsureRunning_PollsAMigratingDaemonSlowly(t *testing.T) {
	c := newTestController(t, modeNormal)
	holdLock(t, c)
	d := serveStarting(t, c, os.Getpid(), c.Home.Path)
	time.AfterFunc(2*time.Second, d.ready)

	require.NoError(t, c.EnsureRunning(context.Background()))
	assert.LessOrEqual(t, d.requests.Load(), int32(4))
}

// TestEnsureStarted: EnsureStarted returns at the first answer from our
// daemon, starting or serving, spawning one if need be.
func TestEnsureStarted(t *testing.T) {
	t.Run("a migrating holder", func(t *testing.T) {
		c := newTestController(t, modeNormal)
		holdLock(t, c)
		serveStarting(t, c, os.Getpid(), c.Home.Path)
		told := onMigrating(c)

		start := time.Now()
		st, err := c.EnsureStarted(context.Background())
		require.NoError(t, err)
		assert.Less(t, time.Since(start), time.Second)
		require.NotNil(t, st)
		assert.Equal(t, client.PhaseMigrating, st.Phase)
		assert.Equal(t, 1, told.count())

		_, err = c.EnsureStarted(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 2, told.count(), "once per call")
	})

	t.Run("a serving daemon", func(t *testing.T) {
		c := newTestController(t, modeNormal)
		holdLock(t, c)
		serveHealth(t, c, map[string]any{"status": "ok", "pid": os.Getpid(), "home": c.Home.Path})
		st, err := c.EnsureStarted(context.Background())
		require.NoError(t, err)
		assert.Nil(t, st)
	})

	t.Run("a spawned daemon", func(t *testing.T) {
		c := newTestController(t, modeStartingThenReady)
		t.Setenv(fakeStartingEnv, "30s")
		start := time.Now()
		st, err := c.EnsureStarted(context.Background())
		require.NoError(t, err)
		assert.Less(t, time.Since(start), 5*time.Second)
		require.NotNil(t, st)
		assert.Equal(t, 1, spawnCount(t, c))
	})

	t.Run("a daemon that can't start", func(t *testing.T) {
		c := newTestController(t, modeCrashOnStart)
		_, err := c.EnsureStarted(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "curio-daemon failed to start: exit status 3")
	})
}

// TestEnsureRunning_SecondCallerIsNotParked: a caller arriving while
// another waits on a daemon that hasn't answered yet keeps to its own
// context, rather than blocking on the start lock until the first caller's
// wait ends.
func TestEnsureRunning_SecondCallerIsNotParked(t *testing.T) {
	c := newTestController(t, modeSilent)
	c.StartTimeout = 3 * time.Second
	first := make(chan error, 1)
	go func() { first <- c.EnsureRunning(context.Background()) }()
	t.Cleanup(func() { <-first })
	require.Eventually(t, func() bool { return spawnCount(t, c) == 1 }, 5*time.Second, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.EnsureRunning(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second)
}

// TestEnsureRunning_ConcurrentStartersWhileMigrating: two callers starting
// a daemon that migrates spawn one, and both hear it is migrating before
// it is ready: the second isn't parked on the start lock meanwhile.
func TestEnsureRunning_ConcurrentStartersWhileMigrating(t *testing.T) {
	c := newTestController(t, modeStartingThenReady)
	t.Setenv(fakeStartingEnv, "2s")
	told := onMigrating(c)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() { errs[i] = c.EnsureRunning(context.Background()) })
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, spawnCount(t, c))
	assert.Equal(t, 2, told.count(), "each caller heard the daemon was migrating")
}

// TestStatus_StartingDaemon: a daemon that holds the lock and reports it is
// starting is running, with what it reported; one that can be verified can
// be stopped.
func TestStatus_StartingDaemon(t *testing.T) {
	c := newTestController(t, modeStartingThenReady)
	t.Setenv(fakeStartingEnv, "30s")
	ctx := context.Background()
	_, err := c.EnsureStarted(ctx)
	require.NoError(t, err)

	st, err := c.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, Running, st.State)
	assert.Nil(t, st.Health)
	require.NotNil(t, st.Startup)
	assert.Equal(t, st.PID, st.Startup.PID)
	assert.True(t, SameHome(c.Home.Path, st.Startup.Home))

	stopped, err := c.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
}

func TestEnsureRunning_ReportsEarlyExit(t *testing.T) {
	cases := []struct {
		mode      string
		wantCause string
		wantLog   string
	}{
		{modeCrashOnStart, "exit status 3", "fake daemon: boom"},
		{modeExitOnStart, "exited with status 0 before it began serving", "fake daemon: told to stop"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			c := newTestController(t, tc.mode)

			start := time.Now()
			err := c.EnsureRunning(context.Background())
			require.Error(t, err)
			assert.Less(t, time.Since(start), c.StartTimeout/2, "an early exit is reported without waiting out the timeout")
			assert.Contains(t, err.Error(), tc.wantCause)
			assert.Contains(t, err.Error(), c.Home.DaemonLogPath())
			assert.Contains(t, err.Error(), tc.wantLog)
			assert.NotContains(t, err.Error(), "%!", "no formatting of a nil cause")
		})
	}
}

// TestEnsureRunning_LockHolderExitsWithoutServing: a daemon holding the lock
// but not serving yet is waited for. If it goes away instead (a stop still
// draining when the next command runs), a new daemon is started rather than
// waiting out StartTimeout for one that will never answer.
func TestEnsureRunning_LockHolderExitsWithoutServing(t *testing.T) {
	c := newTestController(t, modeNormal)

	// The test process plays the holder: flock locks belong to the open
	// file, so the controller's own probes see this one as held.
	lock, err := AcquireLock(c.Home)
	require.NoError(t, err)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { assert.NoError(t, lock.Release()) }) }
	t.Cleanup(release)

	// Healthz answers "not ready" until the holder exits. EnsureRunning
	// probes it once before it looks at the daemon lock, so the second
	// probe comes from the wait.
	var probes atomic.Int32
	waiting := make(chan struct{})
	ln, err := net.Listen("tcp", os.Getenv(fakeAddrEnv))
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if probes.Add(1) == 2 {
			close(waiting)
		}
		http.Error(w, "starting", http.StatusServiceUnavailable)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	result := make(chan error, 1)
	start := time.Now()
	go func() { result <- c.EnsureRunning(context.Background()) }()

	select {
	case <-waiting:
	case err := <-result:
		t.Fatalf("EnsureRunning returned before waiting on the lock holder: %v", err)
	}
	require.NoError(t, srv.Close(), "free the port for the daemon that replaces the holder")
	release()

	require.NoError(t, <-result)
	assert.Less(t, time.Since(start), c.StartTimeout/2, "the holder's exit ends the wait")
	assert.Equal(t, 1, spawnCount(t, c))
}

// TestEnsureRunning_SlowHealthz: when Ollama stalls, healthz takes as long as
// the daemon's own Ollama check (up to 500ms) to answer. A daemon that slow
// is still the running daemon: reused at once, not waited out or replaced.
func TestEnsureRunning_SlowHealthz(t *testing.T) {
	c := newTestController(t, modeNormal)
	ctx := context.Background()
	holdLock(t, c)
	serveHealthAfter(t, c, 700*time.Millisecond,
		map[string]any{"status": "ok", "pid": os.Getpid(), "home": c.Home.Path})

	start := time.Now()
	require.NoError(t, c.EnsureRunning(ctx))
	assert.Less(t, time.Since(start), c.StartTimeout/2, "the first probe's answer is enough")
	assert.Zero(t, spawnCount(t, c))

	st, err := c.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, Running, st.State)
	assert.Equal(t, os.Getpid(), st.PID)
	require.NotNil(t, st.Health, "a slow answer is still an answer")
	assert.Equal(t, os.Getpid(), st.Health.PID)
}

// TestEnsureRunning_TimesOutWaitingForAnotherDaemon: when the lock holder is
// a daemon this caller didn't spawn and it never answers, the error says so;
// there is no failed start of ours to report. The wait allows for a holder
// that is shutting down as well as one starting up.
func TestEnsureRunning_TimesOutWaitingForAnotherDaemon(t *testing.T) {
	c := newTestController(t, modeNormal)
	holdLock(t, c)
	c.StartTimeout = 100 * time.Millisecond
	c.StopTimeout = 400 * time.Millisecond

	start := time.Now()
	err := c.EnsureRunning(context.Background())
	require.Error(t, err)
	assert.GreaterOrEqual(t, time.Since(start), c.StopTimeout, "waited the longer of the two budgets")
	assert.Contains(t, err.Error(), "waiting for the curio-daemon holding the lock for "+c.Home.Path+
		" (starting up or shutting down)")
	assert.Contains(t, err.Error(), "within "+c.StopTimeout.String())
	assert.NotContains(t, err.Error(), "failed to start")
	assert.Zero(t, spawnCount(t, c))
}

// TestEnsureRunning_WaitsOutADrainingHolder: a daemon draining after a stop
// holds the lock for up to its shutdown budget, longer than StartTimeout.
// EnsureRunning waits it out and then starts exactly one daemon, rather
// than giving up on a daemon it thinks is starting.
func TestEnsureRunning_WaitsOutADrainingHolder(t *testing.T) {
	c := newTestController(t, modeNormal)
	c.StartTimeout = 300 * time.Millisecond
	c.StopTimeout = 5 * time.Second
	lock, err := AcquireLock(c.Home)
	require.NoError(t, err)
	released := time.AfterFunc(time.Second, func() { assert.NoError(t, lock.Release()) })
	t.Cleanup(func() {
		if released.Stop() {
			assert.NoError(t, lock.Release())
		}
	})

	require.NoError(t, c.EnsureRunning(context.Background()))
	assert.Equal(t, 1, spawnCount(t, c))
}

// TestMalformedPIDFileIsIgnored: with the lock free, whatever is left in
// daemon.pid is only informational. Garbage there mustn't make status or
// auto-start fail.
func TestMalformedPIDFileIsIgnored(t *testing.T) {
	c := newTestController(t, modeNormal)
	ctx := context.Background()
	require.NoError(t, os.WriteFile(c.Home.PIDFile(), []byte("not a pid\n"), 0o600))

	st, err := c.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, NotRunning, st.State)

	require.NoError(t, c.EnsureRunning(ctx))
	assert.Equal(t, 1, spawnCount(t, c))
}

// TestStalePIDFileIsNeverTrusted: after a crash or reboot daemon.pid can name
// an unrelated live process. It must not count as a running daemon, must
// never be signalled, and must not stop a real daemon from starting.
func TestStalePIDFileIsNeverTrusted(t *testing.T) {
	c := newTestController(t, modeNormal)
	ctx := context.Background()
	pid, exited := startBystander(t)
	writePIDFile(t, c, pid)

	st, err := c.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, Stale, st.State)
	assert.Equal(t, pid, st.PID)

	stopped, err := c.Stop(ctx)
	require.NoError(t, err)
	assert.False(t, stopped, "a stale PID file is nothing to stop")
	require.NoError(t, c.EnsureRunning(ctx))
	assert.Equal(t, 1, spawnCount(t, c))

	assertNotSignalled(t, exited, "the unrelated process named by the stale PID file was signalled")
}

func TestStop_DaemonIgnoringSIGTERM(t *testing.T) {
	c := newTestController(t, modeIgnoreSIGTERM)
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx))
	st, err := c.Status(ctx)
	require.NoError(t, err)

	c.StopTimeout = 300 * time.Millisecond
	stopped, err := c.Stop(ctx)
	require.Error(t, err)
	assert.False(t, stopped)
	assert.Contains(t, err.Error(), fmt.Sprintf("pid %d", st.PID))
	assert.Contains(t, err.Error(), "still running")
}

// TestStop_RefusesMismatchedIdentity: the lock holder's PID is signalled only
// if healthz, when something answers it, serving or starting, vouches for
// that same daemon.
func TestStop_RefusesMismatchedIdentity(t *testing.T) {
	cases := []struct {
		name  string
		serve func(t *testing.T, c *Controller, holder int)
	}{
		{"healthz names another pid", func(t *testing.T, c *Controller, holder int) {
			serveHealth(t, c, map[string]any{"status": "ok", "pid": holder + 1, "home": c.Home.Path})
		}},
		{"healthz names another home", func(t *testing.T, c *Controller, holder int) {
			serveHealth(t, c, map[string]any{"status": "ok", "pid": holder, "home": t.TempDir()})
		}},
		{"a starting answer names another pid", func(t *testing.T, c *Controller, holder int) {
			serveStarting(t, c, holder+1, c.Home.Path)
		}},
		{"a starting answer names another home", func(t *testing.T, c *Controller, holder int) {
			serveStarting(t, c, holder, t.TempDir())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestController(t, modeNormal)
			holder, exited := startBystander(t)
			holdLock(t, c)
			writePIDFile(t, c, holder) // the lock now vouches for the bystander
			tc.serve(t, c, holder)

			_, err := c.Stop(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("held by pid %d; not signalling", holder))
			assertNotSignalled(t, exited, "the lock holder was signalled despite the mismatch")
		})
	}
}

func TestStop_NotRunning(t *testing.T) {
	c := newTestController(t, modeNormal)
	stopped, err := c.Stop(context.Background())
	require.NoError(t, err)
	assert.False(t, stopped)
}

// TestStop_DaemonGoneBeforeSignal: the daemon Status saw exits before the
// signal lands. That is a stopped daemon, not a "no such process" error.
// The test process holds the lock with daemon.pid naming a process that
// has already exited, the state a signal racing the daemon's exit sees.
func TestStop_DaemonGoneBeforeSignal(t *testing.T) {
	c := newTestController(t, modeNormal)
	gone, exited := startBystander(t)
	require.NoError(t, syscall.Kill(gone, syscall.SIGKILL))
	<-exited // reaped: the PID now names no process
	holdLock(t, c)
	writePIDFile(t, c, gone)

	start := time.Now()
	stopped, err := c.Stop(context.Background())
	require.NoError(t, err)
	assert.True(t, stopped)
	assert.Less(t, time.Since(start), c.StopTimeout/2)
}

// TestSignalHolder_ReprobesBeforeSignalling: when the lock is free by the
// time of the signal, the PID Status read is gone and may already name an
// unrelated process, so it is not signalled.
func TestSignalHolder_ReprobesBeforeSignalling(t *testing.T) {
	c := newTestController(t, modeNormal)
	pid, exited := startBystander(t)
	writePIDFile(t, c, pid) // lock free: whoever wrote this has exited

	require.NoError(t, c.signalHolder(context.Background(), pid))
	assertNotSignalled(t, exited, "a PID the lock no longer vouches for was signalled")
}

// TestStop_WaitEndsWhenAnotherDaemonTakesOver: the lock held under a PID
// other than the signalled one means that daemon is gone and another client
// already started the next. Stop is done; it isn't the old daemon "still
// running".
func TestStop_WaitEndsWhenAnotherDaemonTakesOver(t *testing.T) {
	c := newTestController(t, modeNormal)
	holdLock(t, c) // the next daemon: this process
	signalled := os.Getpid() + 1

	start := time.Now()
	require.NoError(t, c.waitReleased(context.Background(), signalled))
	assert.Less(t, time.Since(start), c.StopTimeout/2)
}

// TestLegacyDaemon: a daemon from before the lock protocol holds no lock and
// reports no identity. Starting must reuse it (not spawn a second daemon
// that can't bind), and stopping must not signal a PID nothing vouches for.
func TestLegacyDaemon(t *testing.T) {
	c := newTestController(t, modeNormal)
	ctx := context.Background()
	serveHealth(t, c, map[string]any{"status": "ok", "version": "v0.2.0"})
	pid, exited := startBystander(t)
	writePIDFile(t, c, pid)

	require.NoError(t, c.EnsureRunning(ctx))
	assert.Zero(t, spawnCount(t, c))

	st, err := c.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, Legacy, st.State)

	stopped, err := c.Stop(ctx)
	require.Error(t, err)
	assert.False(t, stopped)
	assert.Contains(t, err.Error(), "older version")
	assert.Contains(t, err.Error(), "kill "+strconv.Itoa(pid))

	require.NoError(t, c.EnsureRunning(ctx), "still usable after the refused stop")
	assertNotSignalled(t, exited, "the legacy PID was signalled")
}

func TestSameHome_ResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))
	assert.True(t, SameHome(dir, link))
	assert.False(t, SameHome(dir, t.TempDir()))
}
