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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/curiohome"
)

// The tests spawn this test binary as the "daemon": with fakeModeEnv set,
// TestMain runs runFakeDaemon instead of the tests. The fake speaks the real
// lock protocol and serves /v1/healthz with its identity on fakeAddrEnv.
const (
	fakeModeEnv = "CURIO_FAKE_DAEMON_MODE"
	fakeAddrEnv = "CURIO_FAKE_DAEMON_ADDR"

	modeNormal        = "normal"
	modeCrashOnStart  = "crash-on-start"
	modeIgnoreSIGTERM = "ignore-sigterm"

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
	if mode == modeCrashOnStart {
		fmt.Fprintln(os.Stderr, "fake daemon: boom")
		return 3
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
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "pid": os.Getpid(), "home": home.Path})
	})
	go func() { _ = http.Serve(ln, mux) }()

	select {
	case <-stop:
	case <-time.After(time.Minute): // never outlive the test run
	}
	return 0
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
	t.Setenv("CURIO_HOME", dir)
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
// process: a daemon for another home, or one from before the lock protocol.
func serveHealth(t *testing.T, c *Controller, body map[string]any) {
	t.Helper()
	ln, err := net.Listen("tcp", os.Getenv(fakeAddrEnv))
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
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

	require.NoError(t, c.Stop(ctx))
	st, err = c.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, NotRunning, st.State, "a clean stop leaves an empty PID file")
}

func TestEnsureRunning_RefusesDaemonForAnotherHome(t *testing.T) {
	c := newTestController(t, modeNormal)
	other := t.TempDir()
	serveHealth(t, c, map[string]any{"status": "ok", "pid": 4242, "home": other})

	err := c.EnsureRunning(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), other)
	assert.Contains(t, err.Error(), c.Home.Path)
	assert.Contains(t, err.Error(), "daemon.listen")
	assert.Zero(t, spawnCount(t, c))
}

func TestEnsureRunning_ReportsCrashOnStart(t *testing.T) {
	c := newTestController(t, modeCrashOnStart)

	start := time.Now()
	err := c.EnsureRunning(context.Background())
	require.Error(t, err)
	assert.Less(t, time.Since(start), c.StartTimeout/2, "an early exit is reported without waiting out the timeout")
	assert.Contains(t, err.Error(), "exit status 3")
	assert.Contains(t, err.Error(), filepath.Join(c.Home.LogsDir(), "daemon.log"))
	assert.Contains(t, err.Error(), "fake daemon: boom")
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

	require.NoError(t, c.Stop(ctx))
	require.NoError(t, c.EnsureRunning(ctx))
	assert.Equal(t, 1, spawnCount(t, c))

	select {
	case <-exited:
		t.Fatal("the unrelated process named by the stale PID file was signalled")
	default:
	}
}

func TestStop_DaemonIgnoringSIGTERM(t *testing.T) {
	c := newTestController(t, modeIgnoreSIGTERM)
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx))
	st, err := c.Status(ctx)
	require.NoError(t, err)

	c.StopTimeout = 300 * time.Millisecond
	err = c.Stop(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("pid %d", st.PID))
	assert.Contains(t, err.Error(), "still running")
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

	err = c.Stop(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "older version")
	assert.Contains(t, err.Error(), "kill "+strconv.Itoa(pid))

	require.NoError(t, c.EnsureRunning(ctx), "still usable after the refused stop")
	select {
	case <-exited:
		t.Fatal("the legacy PID was signalled")
	default:
	}
}

func TestSameHome_ResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))
	assert.True(t, SameHome(dir, link))
	assert.False(t, SameHome(dir, t.TempDir()))
}
