package daemonctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/service"
	"github.com/samsar/curio/internal/service/servicetest"
)

// agent is a launchd agent for a test controller's home: a
// servicetest.Fake that runs this test binary as the fake daemon, the way
// launchd runs the agent's program, with CURIO_HOME set and its output in
// the home's logs. The mode and version of each daemon it starts are read
// as it starts, so a test can change them for the next one.
type agent struct {
	*servicetest.Fake

	mu      sync.Mutex
	mode    string
	version string
}

// newAgent gives c a loaded agent running daemons in mode, and a DaemonBin
// that can't run, so any daemon c spawned itself would fail to start.
// With splitStderr the daemon's stderr goes to launchd.err, as the
// agent's plist sends it; otherwise both streams go to daemon.log.
func newAgent(t *testing.T, c *Controller, mode string, splitStderr bool) *agent {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(c.Home.LogsDir(), 0o700))
	stdout := openLog(t, c.Home.DaemonLogPath())
	stderr := stdout
	if splitStderr {
		stderr = openLog(t, c.Home.LaunchdErrPath())
	}

	a := &agent{Fake: servicetest.New(t, service.BaseLabel+".test"), mode: mode, version: "test"}
	a.Launch = func() *exec.Cmd {
		a.mu.Lock()
		defer a.mu.Unlock()
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "CURIO_HOME="+c.Home.Path, fakeModeEnv+"="+a.mode,
			fakeVersionEnv+"="+a.version, "GORACE=atexit_sleep_ms=0")
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd
	}
	a.Set(func(st *service.Status) {
		st.Installed, st.Loaded, st.Program = true, true, "/opt/homebrew/bin/curio-daemon"
	})
	c.Service = a.Fake
	c.DaemonBin = filepath.Join(t.TempDir(), "no-curio-daemon")
	return a
}

// next sets the mode and version of the next daemon the agent starts.
func (a *agent) next(mode, version string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode, a.version = mode, version
}

// openLog opens path for appending until the test ends, after the agent's
// daemons are gone.
func openLog(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}

// TestEnsureRunning_ThroughTheAgent: with the agent loaded, the daemon is
// started by the service manager, once, and waited on like a spawned
// child; a daemon that already serves costs no call to the manager.
func TestEnsureRunning_ThroughTheAgent(t *testing.T) {
	c := newTestController(t, modeNormal)
	a := newAgent(t, c, modeNormal, false)
	ctx := context.Background()

	require.NoError(t, c.EnsureRunning(ctx))
	assert.Equal(t, 1, a.Count("Start"))
	assert.Equal(t, 1, spawnCount(t, c))
	st, err := c.Status(ctx)
	require.NoError(t, err)
	assert.True(t, st.Managed(), "the lock holder is the agent's daemon")
	assert.Equal(t, a.PID(), st.PID)

	before := len(a.Calls())
	require.NoError(t, c.EnsureRunning(ctx))
	assert.Len(t, a.Calls(), before, "a daemon that serves costs no manager call")
}

func TestEnsureRunning_ConcurrentStartersLaunchOnce(t *testing.T) {
	c := newTestController(t, modeNormal)
	a := newAgent(t, c, modeNormal, false)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() { errs[i] = c.EnsureRunning(context.Background()) })
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, a.Count("Start"))
	assert.Equal(t, 1, spawnCount(t, c))
}

// TestEnsureStarted_ThroughTheAgent: the MCP sidecar's start returns at
// the launched daemon's first starting answer.
func TestEnsureStarted_ThroughTheAgent(t *testing.T) {
	c := newTestController(t, modeNormal)
	newAgent(t, c, modeStartingThenReady, false)
	t.Setenv(fakeStartingEnv, "30s")

	start := time.Now()
	st, err := c.EnsureStarted(context.Background())
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	require.NotNil(t, st)
	assert.Equal(t, client.PhaseMigrating, st.Phase)
}

// TestEnsureRunning_LaunchedStillStarting: a launched daemon migrating
// past ReadyTimeout is told about once and left running, as a spawned one
// is.
func TestEnsureRunning_LaunchedStillStarting(t *testing.T) {
	c := newTestController(t, modeNormal)
	a := newAgent(t, c, modeStartingThenReady, false)
	t.Setenv(fakeStartingEnv, "30s")
	c.ReadyTimeout = time.Second
	told := onMigrating(c)

	err := c.EnsureRunning(context.Background())
	require.ErrorIs(t, err, ErrStillStarting)
	assert.Equal(t, 1, told.count())
	assert.NotZero(t, a.PID(), "left running")
}

// TestEnsureRunning_LaunchedDaemonFails: a launched daemon that dies before
// it serves, before or after taking the lock, fails the start as soon as
// the manager reports it gone, with how it ended and its log's tail.
func TestEnsureRunning_LaunchedDaemonFails(t *testing.T) {
	cases := []struct {
		name, mode string
		want       []string
	}{
		{"before the lock", modeCrashOnStart, []string{"exited before it began serving (exit code 3)", "fake daemon: boom"}},
		{"after the lock", modeStartingThenExit, []string{"exited before it began serving (exit code 4)", "fake daemon: gave up migrating"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestController(t, modeNormal)
			newAgent(t, c, tc.mode, false)
			t.Setenv(fakeStartingEnv, "300ms")
			c.StartTimeout = 10 * time.Second

			start := time.Now()
			err := c.EnsureRunning(context.Background())
			require.Error(t, err)
			assert.Less(t, time.Since(start), 3*time.Second, "reported when it exits, not after StartTimeout")
			assert.Contains(t, err.Error(), "curio-daemon failed to start")
			assert.Contains(t, err.Error(), c.Home.DaemonLogPath())
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want)
			}
			assert.NotContains(t, err.Error(), "holding the lock")
		})
	}
}

// TestEnsureRunning_LaunchdErr: a launched daemon's stderr is launchd.err,
// which a failed start quotes when the launch wrote it, and not when it
// is left over from before.
func TestEnsureRunning_LaunchdErr(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		c := newTestController(t, modeNormal)
		newAgent(t, c, modeCrashOnStart, true)
		err := c.EnsureRunning(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "last lines of "+c.Home.LaunchdErrPath()+":\nfake daemon: boom")
	})

	t.Run("left over", func(t *testing.T) {
		c := newTestController(t, modeNormal)
		require.NoError(t, os.MkdirAll(c.Home.LogsDir(), 0o700))
		require.NoError(t, os.WriteFile(c.Home.LaunchdErrPath(), []byte("panic: an older crash\n"), 0o600))
		hourAgo := time.Now().Add(-time.Hour)
		require.NoError(t, os.Chtimes(c.Home.LaunchdErrPath(), hourAgo, hourAgo))
		newAgent(t, c, modeCrashOnStart, false)
		err := c.EnsureRunning(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fake daemon: boom", "daemon.log is quoted")
		assert.NotContains(t, err.Error(), "an older crash")
		assert.NotContains(t, err.Error(), c.Home.LaunchdErrPath())
	})
}

// TestEnsureRunning_AgentNotLoaded: an agent that is installed but not
// loaded, like no manager at all, leaves starting to a spawned child.
func TestEnsureRunning_AgentNotLoaded(t *testing.T) {
	c := newTestController(t, modeNormal)
	exe := c.DaemonBin
	a := newAgent(t, c, modeNormal, false)
	a.Set(func(st *service.Status) { st.Loaded = false })
	c.DaemonBin = exe

	require.NoError(t, c.EnsureRunning(context.Background()))
	assert.Equal(t, 1, spawnCount(t, c))
	assert.Zero(t, a.Count("Start"))
	assert.Zero(t, a.PID())
}

// TestEnsureRunning_ManagerFails: a manager that can't say whether the
// agent is loaded fails the start: a child spawned next to a loaded agent
// would race its daemon.
func TestEnsureRunning_ManagerFails(t *testing.T) {
	c := newTestController(t, modeNormal)
	exe := c.DaemonBin
	a := newAgent(t, c, modeNormal, false)
	c.DaemonBin = exe
	a.Fail("Status", errors.New("launchctl print: no answer within 10s"))

	err := c.EnsureRunning(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "launchctl print: no answer within 10s")
	assert.Zero(t, spawnCount(t, c))
}

// TestStatus_Service: Status carries the manager's view, and a manager
// that fails doesn't fail it: the lock still says whether a daemon runs.
func TestStatus_Service(t *testing.T) {
	c := newTestController(t, modeNormal)
	a := newAgent(t, c, modeNormal, false)
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx))

	st, err := c.Status(ctx)
	require.NoError(t, err)
	require.NotNil(t, st.Service)
	assert.Equal(t, a.PID(), st.Service.PID)
	assert.NoError(t, st.ServiceErr)

	a.Fail("Status", errors.New("launchctl print: exit 5"))
	st, err = c.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, Running, st.State)
	assert.Nil(t, st.Service)
	require.Error(t, st.ServiceErr)
	assert.False(t, st.Managed())

	c.Service = nil
	st, err = c.Status(ctx)
	require.NoError(t, err)
	assert.Nil(t, st.Service)
	assert.NoError(t, st.ServiceErr)
}

// TestStop_ThroughTheAgent: the agent's daemon is stopped through the
// manager, so launchd knows it was asked to; daemonctl then waits for the
// lock as it does for a daemon it signals.
func TestStop_ThroughTheAgent(t *testing.T) {
	c := newTestController(t, modeNormal)
	a := newAgent(t, c, modeNormal, false)
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx))

	stopped, err := c.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
	assert.Equal(t, 1, a.Count("Stop"))
	held, _, err := probeLock(c.Home.PIDFile())
	require.NoError(t, err)
	assert.False(t, held)
}

// TestStop_ManagerIgnoresTheStop: daemonctl asks the manager and doesn't
// signal the daemon itself as well, so a stop the manager never carries
// out leaves the daemon running, and says so.
func TestStop_ManagerIgnoresTheStop(t *testing.T) {
	c := newTestController(t, modeNormal)
	a := newAgent(t, c, modeNormal, false)
	a.IgnoreStop = true
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx))
	pid := a.PID()

	c.StopTimeout = 300 * time.Millisecond
	stopped, err := c.Stop(ctx)
	require.Error(t, err)
	assert.False(t, stopped)
	assert.Contains(t, err.Error(), fmt.Sprintf("daemon (pid %d) is still running", pid))
	assert.Equal(t, pid, a.PID(), "not signalled behind the manager's back")
}

// TestStop_ManagerFails: a stop the manager refused, with its daemon still
// running, is the stop's error.
func TestStop_ManagerFails(t *testing.T) {
	c := newTestController(t, modeNormal)
	a := newAgent(t, c, modeNormal, false)
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx))
	a.Fail("Stop", errors.New("launchctl kill SIGTERM: exit 3: No such process"))

	stopped, err := c.Stop(ctx)
	require.Error(t, err)
	assert.False(t, stopped)
	assert.Contains(t, err.Error(), "launchctl kill SIGTERM")
	assert.NotZero(t, a.PID(), "still running")
}

// TestStop_DaemonOutsideTheAgent: a daemon started outside launchd next to
// a loaded agent is the lock holder, and is signalled directly.
func TestStop_DaemonOutsideTheAgent(t *testing.T) {
	c := newTestController(t, modeNormal)
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx)) // spawned: no manager yet
	a := newAgent(t, c, modeNormal, false)

	stopped, err := c.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
	assert.Zero(t, a.Count("Stop"))
}

// TestStop_ManagedIdentityMismatch: the lock holder is signalled, through
// the manager or not, only when healthz vouches for it.
func TestStop_ManagedIdentityMismatch(t *testing.T) {
	c := newTestController(t, modeNormal)
	holder, exited := startBystander(t)
	holdLock(t, c)
	writePIDFile(t, c, holder)
	serveHealth(t, c, map[string]any{"status": "ok", "pid": holder + 1, "home": c.Home.Path})
	a := newAgent(t, c, modeNormal, false)
	a.Set(func(st *service.Status) { st.PID = holder })

	_, err := c.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("held by pid %d; not signalling", holder))
	assert.Zero(t, a.Count("Stop"))
	assertNotSignalled(t, exited, "the lock holder was signalled despite the mismatch")
}

// TestInstall_TakesOverFromASpawnedDaemon: installing the agent while a
// daemon a client spawned runs stops that daemon first, so the agent's can
// take the lock, and returns once the agent's daemon serves.
func TestInstall_TakesOverFromASpawnedDaemon(t *testing.T) {
	c := newTestController(t, modeNormal)
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx))
	a := newAgent(t, c, modeNormal, false)
	a.Set(func(st *service.Status) { st.Installed, st.Loaded, st.Program = false, false, "" })
	a.OnInstall = func(spec service.Spec) error {
		held, _, err := probeLock(c.Home.PIDFile())
		assert.NoError(t, err)
		assert.False(t, held, "the spawned daemon is gone before the agent loads")
		assert.Equal(t, c.DaemonBin, spec.Program)
		return nil
	}

	changed, err := c.Install(ctx)
	require.NoError(t, err)
	assert.True(t, changed)
	st, err := c.Status(ctx)
	require.NoError(t, err)
	assert.True(t, st.Managed())
	require.NotNil(t, st.Health, "the agent's daemon serves")
	assert.Equal(t, 2, spawnCount(t, c), "the spawned daemon, then the agent's")

	a.OnInstall = nil
	changed, err = c.Install(ctx)
	require.NoError(t, err)
	assert.False(t, changed, "the manager's answer passes through")
	assert.Equal(t, 2, spawnCount(t, c))
}

// TestInstall_ExcludesAutoStarters: a command starting the daemon while
// the agent is installed neither spawns nor launches a second daemon.
func TestInstall_ExcludesAutoStarters(t *testing.T) {
	c := newTestController(t, modeNormal)
	a := newAgent(t, c, modeNormal, false)
	a.Set(func(st *service.Status) { st.Installed, st.Loaded = false, false })
	ctx := context.Background()
	concurrent := make(chan error, 1)
	a.OnInstall = func(service.Spec) error {
		go func() { concurrent <- c.EnsureRunning(ctx) }()
		return nil
	}

	_, err := c.Install(ctx)
	require.NoError(t, err)
	require.NoError(t, <-concurrent)
	assert.Equal(t, 1, spawnCount(t, c))
}

// TestUninstall: removing the agent returns once its daemon has let go of
// the home, so a caller may move it. A command starting the daemon
// meanwhile starts nothing until then.
func TestUninstall(t *testing.T) {
	c := newTestController(t, modeNormal)
	exe := c.DaemonBin
	a := newAgent(t, c, modeSlowStop, false)
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx))
	agentPID := a.PID()
	c.DaemonBin = exe // what a command spawns once the agent is gone
	concurrent := make(chan error, 1)
	a.OnUninstall = func() error {
		go func() { concurrent <- c.EnsureRunning(ctx) }()
		return nil
	}

	start := time.Now()
	removed, err := c.Uninstall(ctx)
	require.NoError(t, err)
	assert.True(t, removed)
	assert.GreaterOrEqual(t, time.Since(start), slowStop, "waited out the agent's daemon")
	assert.Equal(t, 1, spawnCount(t, c), "nothing started while the agent went")
	held, holder, err := probeLock(c.Home.PIDFile())
	require.NoError(t, err)
	assert.False(t, held && holder == agentPID, "the agent's daemon let go of the home")
	require.NoError(t, <-concurrent, "served by the agent's daemon while it drained, or by one started after")

	removed, err = c.Uninstall(ctx)
	require.NoError(t, err)
	assert.False(t, removed, "nothing installed")
}

// TestUninstall_LeavesAnOutsideDaemon: a daemon launchd doesn't run keeps
// running when the agent goes.
func TestUninstall_LeavesAnOutsideDaemon(t *testing.T) {
	c := newTestController(t, modeNormal)
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx)) // spawned
	a := newAgent(t, c, modeNormal, false)
	before, err := c.Status(ctx)
	require.NoError(t, err)

	removed, err := c.Uninstall(ctx)
	require.NoError(t, err)
	assert.True(t, removed)
	after, err := c.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, Running, after.State)
	assert.Equal(t, before.PID, after.PID)
	require.NotNil(t, after.Health)
	assert.Zero(t, a.Count("Stop"))
}

func TestEnsureVersion(t *testing.T) {
	ctx := context.Background()

	t.Run("the same version", func(t *testing.T) {
		c := newTestController(t, modeNormal)
		a := newAgent(t, c, modeNormal, false)
		a.next(modeNormal, "v1")
		restarted, err := c.EnsureVersion(ctx, "v1")
		require.NoError(t, err)
		assert.False(t, restarted)
		assert.Zero(t, a.Count("Restart"))
		assert.Zero(t, a.Count("Stop"))
	})

	t.Run("managed, another version", func(t *testing.T) {
		c := newTestController(t, modeNormal)
		a := newAgent(t, c, modeSlowStop, false)
		a.next(modeSlowStop, "v1")
		require.NoError(t, c.EnsureRunning(ctx))
		old := a.PID()
		// The old daemon answers, as v1, until it has drained; none of
		// that is taken for the new daemon's answer.
		a.next(modeNormal, "v2")

		restarted, err := c.EnsureVersion(ctx, "v2")
		require.NoError(t, err)
		assert.True(t, restarted)
		assert.Equal(t, 1, a.Count("Restart"))
		h, err := client.New(c.BaseURL).Healthz(ctx)
		require.NoError(t, err)
		assert.Equal(t, "v2", h.Version)
		assert.NotEqual(t, old, h.PID)
		assert.Equal(t, a.PID(), h.PID)
	})

	t.Run("spawned, another version", func(t *testing.T) {
		c := newTestController(t, modeNormal)
		t.Setenv(fakeVersionEnv, "v1")
		require.NoError(t, c.EnsureRunning(ctx))
		t.Setenv(fakeVersionEnv, "v2")

		restarted, err := c.EnsureVersion(ctx, "v2")
		require.NoError(t, err)
		assert.True(t, restarted)
		assert.Equal(t, 2, spawnCount(t, c))
		h, err := client.New(c.BaseURL).Healthz(ctx)
		require.NoError(t, err)
		assert.Equal(t, "v2", h.Version)
	})

	t.Run("still another version after the restart", func(t *testing.T) {
		c := newTestController(t, modeNormal)
		a := newAgent(t, c, modeNormal, false)
		a.next(modeNormal, "v1")

		restarted, err := c.EnsureVersion(ctx, "v2")
		require.ErrorIs(t, err, ErrVersionMismatch)
		assert.True(t, restarted)
		assert.Equal(t, 1, a.Count("Restart"), "never twice")
		for _, want := range []string{"v1", "v2", "/opt/homebrew/bin/curio-daemon"} {
			assert.Contains(t, err.Error(), want)
		}
	})

	t.Run("a legacy daemon is never signalled", func(t *testing.T) {
		c := newTestController(t, modeNormal)
		serveHealth(t, c, map[string]any{"status": "ok", "version": "v0.2.0"})
		pid, exited := startBystander(t)
		writePIDFile(t, c, pid)

		_, err := c.EnsureVersion(ctx, "v2")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "v0.2.0")
		assert.Contains(t, err.Error(), "older version")
		assertNotSignalled(t, exited, "the legacy daemon's pid was signalled")
	})
}

// TestRestart_Unmanaged: a daemon the manager doesn't run is stopped,
// lock-verified, and a new one started in its place.
func TestRestart_Unmanaged(t *testing.T) {
	c := newTestController(t, modeNormal)
	ctx := context.Background()
	require.NoError(t, c.EnsureRunning(ctx))
	before, err := c.Status(ctx)
	require.NoError(t, err)

	require.NoError(t, c.Restart(ctx))
	after, err := c.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, Running, after.State)
	require.NotNil(t, after.Health)
	assert.Equal(t, after.PID, after.Health.PID)
	assert.NotEqual(t, before.PID, after.PID, "another daemon holds the lock")
	assert.Equal(t, 2, spawnCount(t, c))
}

// TestInstallWithoutManager: a controller given no manager has no agent
// to install.
func TestInstallWithoutManager(t *testing.T) {
	c := newTestController(t, modeNormal)
	_, err := c.Install(context.Background())
	require.ErrorIs(t, err, service.ErrUnsupported)
	_, err = c.Uninstall(context.Background())
	require.ErrorIs(t, err, service.ErrUnsupported)
}

// TestTimingChain: the daemon's shutdown (5s for HTTP, 15s to drain its
// workers: 20s) fits in the time launchd gives it after SIGTERM, which
// fits in the time Stop waits for the lock.
func TestTimingChain(t *testing.T) {
	const daemonShutdown = 20 * time.Second
	assert.Greater(t, service.ExitTimeout, daemonShutdown)
	assert.Greater(t, defaultStopTimeout, service.ExitTimeout)
}

func TestLaunchFailed_NoLaunchdErr(t *testing.T) {
	c := newTestController(t, modeNormal)
	err := c.launchFailed(errors.New("exited before it began serving"), time.Now())
	assert.True(t, strings.HasPrefix(err.Error(), "curio-daemon failed to start: exited before it began serving"))
	assert.NotContains(t, err.Error(), "launchd.err")
}
