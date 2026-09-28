package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/service"
	"github.com/samsar/curio/internal/service/servicetest"
	"github.com/samsar/curio/internal/version"
)

func TestDescribeDaemonStatus(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir()
	cases := []struct {
		name string
		st   daemonctl.Status
		want string
	}{
		{
			name: "starting",
			st:   daemonctl.Status{State: daemonctl.Running},
			want: "starting (lock held, pid not recorded yet)",
		},
		{
			name: "starting, migrating",
			st: daemonctl.Status{State: daemonctl.Running, PID: 42,
				Startup: &client.Startup{PID: 42, Home: home, Version: "v1.2.3", Phase: client.PhaseMigrating,
					Migrations: &client.MigrationProgress{Applied: 2, Total: 6}}},
			want: "starting (pid 42, home " + home + ", version v1.2.3): migrating the database, 2 of 6 migrations applied",
		},
		{
			name: "starting, initializing",
			st: daemonctl.Status{State: daemonctl.Running, PID: 42,
				Startup: &client.Startup{PID: 42, Home: home, Version: "v1.2.3", Phase: client.PhaseInitializing}},
			want: "starting (pid 42, home " + home + ", version v1.2.3): initializing",
		},
		{
			name: "running, not serving yet",
			st:   daemonctl.Status{State: daemonctl.Running, PID: 42},
			want: "running (pid 42), not answering HTTP yet",
		},
		{
			name: "running",
			st: daemonctl.Status{State: daemonctl.Running, PID: 42,
				Health: &client.Health{PID: 42, Home: home, Version: "v1.2.3"}},
			want: "running (pid 42, home " + home + ", version v1.2.3)",
		},
		{
			name: "stale PID file",
			st:   daemonctl.Status{State: daemonctl.Stale, PID: 42},
			want: "not running (stale PID file: pid 42 is left over from an earlier run and is ignored)",
		},
		{
			name: "legacy daemon",
			st:   daemonctl.Status{State: daemonctl.Legacy, Health: &client.Health{Version: "v0.2.0"}},
			want: "legacy daemon from an older curio is answering (version v0.2.0); " +
				"run `curio daemon stop` for how to retire it",
		},
		{
			name: "not running",
			st:   daemonctl.Status{State: daemonctl.NotRunning},
			want: "not running",
		},
		{
			name: "not running, port served for another home",
			st: daemonctl.Status{State: daemonctl.NotRunning,
				Health: &client.Health{PID: 7, Home: other}},
			want: "not running (the port is served by the daemon for " + other + ")",
		},
		{
			name: "not running, port held by another home's starting daemon",
			st: daemonctl.Status{State: daemonctl.NotRunning,
				Startup: &client.Startup{PID: 7, Home: other, Phase: client.PhaseMigrating}},
			want: "not running (the port is served by the daemon for " + other + ")",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, describeDaemonStatus(tc.st, home))
		})
	}
}

// TestDaemonStop_NotRunning: stopping a home with no daemon says so rather
// than claiming to have stopped one.
func TestDaemonStop_NotRunning(t *testing.T) {
	defaults := config.Default().Embedding
	home, err := curiohome.Init(t.TempDir(), defaults.Model, defaults.Dim)
	require.NoError(t, err)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	out, err := runCLIAt(t, home.Path, down.URL, "daemon", "stop")
	require.NoError(t, err)
	assert.Equal(t, "daemon not running\n", out)
}

// syncBuffer is a bytes.Buffer safe to read while a command writes to it.
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

// TestDaemonStart_WaitsOutAMigration: `curio daemon start` against a
// daemon that is migrating says once, on stderr, why it waits, and reports
// the daemon running once it is ready.
func TestDaemonStart_WaitsOutAMigration(t *testing.T) {
	srv := apitest.StartNotReady(t)
	srv.Startup.SetMigrating(6)
	// The test process is the daemon holding the lock: healthz names it.
	lock, err := daemonctl.AcquireLock(srv.Home)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, lock.Release()) })

	var stdout, stderr syncBuffer
	done := make(chan error, 1)
	root := newRootCmdWith(testDeps(t))
	go func() { done <- runCLIStreams(root, srv.Home.Path, srv.URL, &stdout, &stderr, "daemon", "start") }()

	notice := "curio-daemon is migrating the database (6 migrations); this can take a minute on a large library; " +
		"`curio daemon logs -f` shows progress\n"
	require.Eventually(t, func() bool { return stderr.String() == notice }, 5*time.Second, 10*time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("daemon start returned while the daemon was migrating: %v", err)
	case <-time.After(1500 * time.Millisecond): // past a poll or two
	}
	require.NoError(t, srv.Ready())

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon start did not return once the daemon was ready")
	}
	assert.Equal(t, "daemon running\n", stdout.String())
	assert.Equal(t, notice, stderr.String(), "said once")
}

// withService is daemonctl.Connect, with svc as the controller's service
// manager.
func withService(svc service.Manager) daemonctl.ConnectFunc {
	return func(home *curiohome.Home, cfg config.Config, daemonURL string) (daemonctl.Env, error) {
		env, err := daemonctl.Connect(home, cfg, daemonURL)
		if err == nil {
			env.Controller.Service = svc
		}
		return env, err
	}
}

// runCLIWith runs curio with args for home and daemonURL, connecting to
// its daemon with connect.
func runCLIWith(t *testing.T, connect daemonctl.ConnectFunc, home, daemonURL string,
	args ...string) (stdout, stderr string, err error) {
	t.Helper()
	d := testDeps(t)
	d.connect = connect
	var out, errOut bytes.Buffer
	err = runCLIStreams(newRootCmdWith(d), home, daemonURL, &out, &errOut, args...)
	return out.String(), errOut.String(), err
}

// daemonBin makes CURIO_DAEMON_BIN an executable file for the rest of the
// test, and returns its path.
func daemonBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "curio-daemon")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/false\n"), 0o700))
	t.Setenv("CURIO_DAEMON_BIN", bin)
	return bin
}

// agentLabel is the fake agents' label.
const agentLabel = service.BaseLabel + ".test"

// holdLockAsDaemon makes the test process the daemon holding home's lock,
// as apitest's server reports it is, until release or the test's end.
func holdLockAsDaemon(t *testing.T, home *curiohome.Home) (release func()) {
	t.Helper()
	lock, err := daemonctl.AcquireLock(home)
	require.NoError(t, err)
	var once sync.Once
	release = func() { once.Do(func() { assert.NoError(t, lock.Release()) }) }
	t.Cleanup(release)
	return release
}

// managedByFake gives fake a loaded agent whose daemon is the test
// process.
func managedByFake(fake *servicetest.Fake, program string) {
	fake.Set(func(st *service.Status) {
		st.Installed, st.Loaded, st.Program, st.PID, st.State = true, true, program, os.Getpid(), "running"
	})
}

func TestDescribeAgent(t *testing.T) {
	agent := func(change func(*service.Status)) *service.Status {
		st := &service.Status{Supported: true, Label: agentLabel, Path: "/p.plist", Installed: true, Loaded: true,
			Program: "/opt/homebrew/bin/curio-daemon"}
		change(st)
		return st
	}
	running := daemonctl.Status{State: daemonctl.Running, PID: 42}
	cases := []struct {
		name string
		st   daemonctl.Status
		want string
	}{
		{"no launchd", daemonctl.Status{Service: &service.Status{}}, ""},
		{"no manager", daemonctl.Status{}, ""},
		{"no agent", daemonctl.Status{Service: agent(func(s *service.Status) { s.Installed, s.Loaded = false, false })},
			"launchd: no agent (`curio daemon install` keeps the daemon running across logins and crashes)"},
		{"manages the daemon", daemonctl.Status{State: daemonctl.Running, PID: 42,
			Service: agent(func(s *service.Status) { s.PID, s.State = 42, "running" })},
			"launchd: manages this daemon (agent " + agentLabel + ", runs /opt/homebrew/bin/curio-daemon)"},
		{"a daemon started outside it", daemonctl.Status{State: running.State, PID: running.PID,
			Service: agent(func(s *service.Status) { s.State = "not running" })},
			"launchd: agent " + agentLabel + " is loaded, but the running daemon (pid 42) was started outside launchd; " +
				"`curio daemon stop` hands it over at the next command"},
		{"its daemon without the lock", daemonctl.Status{
			Service: agent(func(s *service.Status) { s.PID, s.State = 42, "running" })},
			"launchd: agent " + agentLabel + " runs pid 42, which doesn't hold the home's lock (yet): " +
				"it is starting, or exiting"},
		{"exited non-zero", daemonctl.Status{Service: agent(func(s *service.Status) { s.LastExit = "exit code 1" })},
			"launchd: agent " + agentLabel + " is loaded, daemon not running: it last exited with exit code 1, " +
				"and launchd restarts it (`curio daemon logs` says why it exited)"},
		{"stopped cleanly", daemonctl.Status{Service: agent(func(*service.Status) {})},
			"launchd: agent " + agentLabel + " is loaded, daemon not running; launchd starts it at login " +
				"or when a curio command needs it"},
		{"installed, not loaded", daemonctl.Status{Service: agent(func(s *service.Status) { s.Loaded = false })},
			"launchd: agent " + agentLabel + " is installed but not loaded (`curio daemon install` loads it)"},
		{"no GUI session", daemonctl.Status{Service: agent(func(s *service.Status) { s.Loaded, s.NoGUISession = false, true })},
			"launchd: agent " + agentLabel + " is installed, but there is no GUI login session for it to run in (ssh); " +
				"curio commands start the daemon themselves meanwhile"},
		{"a manager that fails", daemonctl.Status{ServiceErr: errors.New("launchctl print: exit 5")},
			"launchd: can't read the agent's status: launchctl print: exit 5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, describeAgent(tc.st))
		})
	}
}

func TestDaemonInstall(t *testing.T) {
	srv := apitest.Start(t)
	bin := daemonBin(t)
	fake := servicetest.New(t, agentLabel)
	t.Setenv("CURIO_GITHUB_TOKEN", "ghp_secret")
	t.Setenv("CURIO_JINA_API_KEY", "")

	stdout, stderr, err := runCLIWith(t, withService(fake), srv.Home.Path, srv.URL, "daemon", "install")
	require.NoError(t, err)
	assert.Equal(t, "launchd agent "+agentLabel+" installed; it runs "+bin+"\n"+
		"launchd starts the daemon at login and restarts it after a crash\n"+
		fmt.Sprintf("daemon running (pid %d)\n", os.Getpid())+
		"macOS may announce a background item from curio-daemon: keep it allowed in "+
		"System Settings > General > Login Items & Extensions\n", stdout)
	assert.Equal(t, "warning: CURIO_GITHUB_TOKEN is set here, but the launchd agent's daemon won't see it; "+
		"set fetcher.github.token in "+srv.Home.ConfigPath()+" instead\n", stderr)
	assert.Equal(t, []service.Spec{{Program: bin}}, fake.Specs(), "the program, and nothing of the environment")

	stdout, _, err = runCLIWith(t, withService(fake), srv.Home.Path, srv.URL, "daemon", "install")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stdout, "launchd agent "+agentLabel+" already installed; it runs "+bin+"\n"), stdout)
	assert.NotContains(t, stdout, "background item")
}

// TestDaemonInstall_RefusedHome: an agent whose daemon would refuse the
// home is never installed: launchd would retry it every 10 seconds.
func TestDaemonInstall_RefusedHome(t *testing.T) {
	defaults := config.Default().Embedding
	home, err := curiohome.Init(t.TempDir(), defaults.Model, defaults.Dim)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte("embedding:\n  model: mxbai-embed-large\n"), 0o600))
	fake := servicetest.New(t, agentLabel)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	_, _, err = runCLIWith(t, withService(fake), home.Path, down.URL, "daemon", "install")
	var mismatch *curiohome.EmbeddingMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Empty(t, fake.Calls())
}

// TestDaemonInstall_Unsupported: where there is no launchd, install says
// so, and nothing about an agent's environment.
func TestDaemonInstall_Unsupported(t *testing.T) {
	srv := apitest.Start(t)
	t.Setenv("CURIO_GITHUB_TOKEN", "ghp_secret")
	_, stderr, err := runCLIWith(t, withService(service.Unsupported{}), srv.Home.Path, srv.URL, "daemon", "install")
	require.ErrorIs(t, err, service.ErrUnsupported)
	assert.Contains(t, err.Error(), "launchd agents are macOS-only; the CLI starts the daemon on demand")
	assert.NotContains(t, stderr, "CURIO_GITHUB_TOKEN")
}

func TestDaemonUninstall(t *testing.T) {
	t.Run("nothing installed", func(t *testing.T) {
		srv := apitest.Start(t)
		fake := servicetest.New(t, agentLabel)
		stdout, _, err := runCLIWith(t, withService(fake), srv.Home.Path, srv.URL, "daemon", "uninstall")
		require.NoError(t, err)
		assert.Equal(t, "no launchd agent installed\n", stdout)
	})

	t.Run("the agent runs the daemon", func(t *testing.T) {
		srv := apitest.Start(t)
		fake := servicetest.New(t, agentLabel)
		managedByFake(fake, daemonBin(t))
		release := holdLockAsDaemon(t, srv.Home)
		fake.OnUninstall = func() error {
			release() // the agent's daemon stops as launchd boots it out
			return nil
		}
		stdout, _, err := runCLIWith(t, withService(fake), srv.Home.Path, srv.URL, "daemon", "uninstall")
		require.NoError(t, err)
		assert.Equal(t, "launchd agent "+agentLabel+" removed; daemon stopped "+
			"(the next curio command starts it on demand)\n", stdout)
	})

	t.Run("a daemon outside it keeps running", func(t *testing.T) {
		srv := apitest.Start(t)
		fake := servicetest.New(t, agentLabel)
		fake.Set(func(st *service.Status) { st.Installed, st.Loaded = true, true })
		holdLockAsDaemon(t, srv.Home)
		stdout, _, err := runCLIWith(t, withService(fake), srv.Home.Path, srv.URL, "daemon", "uninstall")
		require.NoError(t, err)
		assert.Equal(t, "launchd agent "+agentLabel+" removed\n", stdout)
	})
}

// TestDaemonStartStopStatus_Launchd: with the agent running the daemon,
// start says so, status names the agent, and stop goes through launchd
// and says it starts the daemon again.
func TestDaemonStartStopStatus_Launchd(t *testing.T) {
	srv := apitest.Start(t)
	bin := daemonBin(t)
	fake := servicetest.New(t, agentLabel)
	managedByFake(fake, bin)
	release := holdLockAsDaemon(t, srv.Home)
	discover := withService(stopReleases{Fake: fake, release: release})

	stdout, stderr, err := runCLIWith(t, discover, srv.Home.Path, srv.URL, "daemon", "start")
	require.NoError(t, err)
	assert.Equal(t, "daemon running (launchd)\n", stdout)
	assert.Empty(t, stderr)

	stdout, _, err = runCLIWith(t, discover, srv.Home.Path, srv.URL, "daemon", "status")
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("running (pid %d, home %s, version %s)\n", os.Getpid(), srv.Home.Path, version.String())+
		"launchd: manages this daemon (agent "+agentLabel+", runs "+bin+")\n", stdout)

	stdout, _, err = runCLIWith(t, discover, srv.Home.Path, srv.URL, "daemon", "stop")
	require.NoError(t, err)
	assert.Equal(t, "daemon stopped; its launchd agent starts it again at login, or when a curio command needs it\n", stdout)
	assert.Equal(t, 1, fake.Count("Stop"))
}

// stopReleases is a fake whose Stop also releases the lock the test
// process holds as the agent's daemon, as that daemon would on SIGTERM.
type stopReleases struct {
	*servicetest.Fake
	release func()
}

func (s stopReleases) Stop(ctx context.Context) error {
	err := s.Fake.Stop(ctx)
	s.release()
	return err
}

// TestDaemonStart_AnotherVersion: after a rebuild or an upgrade the daemon
// still running is the old build; start says so, and how to switch: a
// stop, after which the next command starts this curio's daemon, or, when
// the launchd agent runs another curio-daemon, which a stop would only
// start again, an install that repoints it.
func TestDaemonStart_AnotherVersion(t *testing.T) {
	const warning = "warning: the daemon runs curio v0.9.0 (abc, 2026-09-01), and this is curio "
	cases := []struct {
		name   string
		agent  func(t *testing.T, fake *servicetest.Fake, bin string)
		stdout string
		advice func(bin, agentProgram string) string
	}{
		{"no agent", func(*testing.T, *servicetest.Fake, string) {}, "daemon running\n",
			func(bin, _ string) string {
				return "to switch, run `curio daemon stop`, and the next command starts " + bin
			}},
		{"an agent running this curio's daemon", func(_ *testing.T, fake *servicetest.Fake, bin string) {
			managedByFake(fake, bin)
		}, "daemon running (launchd)\n", func(bin, _ string) string {
			return "to switch, run `curio daemon stop`, and the next command starts " + bin
		}},
		{"an agent running another curio-daemon", func(t *testing.T, fake *servicetest.Fake, _ string) {
			other := filepath.Join(t.TempDir(), "curio-daemon")
			require.NoError(t, os.WriteFile(other, []byte("#!/bin/false\n"), 0o700))
			managedByFake(fake, other)
		}, "daemon running (launchd)\n", func(bin, agentProgram string) string {
			return "to switch, run `curio daemon install`, which repoints the launchd agent from " + agentProgram +
				" to " + bin + " and restarts the daemon"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := apitest.Start(t).Home
			bin := daemonBin(t)
			holdLockAsDaemon(t, home)
			old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"ok","pid":%d,"home":%q,"version":"v0.9.0 (abc, 2026-09-01)"}`,
					os.Getpid(), home.Path)
			}))
			t.Cleanup(old.Close)
			fake := servicetest.New(t, agentLabel)
			tc.agent(t, fake, bin)
			svc, err := fake.Status(context.Background())
			require.NoError(t, err)

			stdout, stderr, err := runCLIWith(t, withService(fake), home.Path, old.URL, "daemon", "start")
			require.NoError(t, err)
			assert.Equal(t, tc.stdout, stdout)
			assert.Equal(t, warning+version.String()+"; "+tc.advice(bin, svc.Program)+"\n", stderr)
		})
	}
}

// TestDoctor_Launchd: doctor's check of the launchd agent, which curio up
// installs: missing is a warning curio up fixes, as is an agent not
// loaded, one running another curio's daemon, or a program that is gone;
// over ssh, with no GUI session, it is a warning with nothing to fix.
func TestDoctor_Launchd(t *testing.T) {
	cases := []struct {
		name   string
		set    func(t *testing.T, w *world)
		marker string
		detail string
		hint   string
	}{
		{"loaded, runs this curio's daemon", func(*testing.T, *world) {}, "✓",
			"agent " + agentLabel + " loaded, runs {bin}", ""},
		{"no agent", func(_ *testing.T, w *world) {
			w.agent.Set(func(st *service.Status) { *st = service.Status{Supported: true, Label: agentLabel} })
		}, "!", "no agent: nothing keeps the daemon running across logins and crashes",
			"`curio up` does it: install the launchd agent (it runs {bin})"},
		{"the program is gone", func(t *testing.T, w *world) {
			managedByFake(w.agent, filepath.Join(t.TempDir(), "gone"))
		}, "!", "which is missing", "`curio up` does it"},
		{"installed, not loaded", func(_ *testing.T, w *world) {
			w.agent.Set(func(st *service.Status) { st.Loaded, st.PID = false, 0 })
		}, "!", "agent " + agentLabel + " is installed but not loaded", "`curio up` does it"},
		{"another curio's daemon", func(t *testing.T, w *world) {
			other := filepath.Join(t.TempDir(), "curio-daemon")
			require.NoError(t, os.WriteFile(other, []byte("#!/bin/false\n"), 0o700))
			managedByFake(w.agent, other)
		}, "!", "not this curio's {bin}", "`curio up` does it"},
		{"no GUI session", func(_ *testing.T, w *world) {
			w.agent.Set(func(st *service.Status) { st.Loaded, st.PID, st.NoGUISession = false, 0, true })
		}, "!", "installed, but there is no GUI login session for it to run in (ssh); " +
			"curio commands start the daemon themselves meanwhile", ""},
		{"a manager that fails", func(_ *testing.T, w *world) {
			w.agent.Fail("Status", errors.New("launchctl print: exit 5"))
		}, "!", "can't read the agent's status: launchctl print: exit 5", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := upWorld(t)
			tc.set(t, w)
			out, _ := w.run(t, "doctor")
			line, hint := doctorLine(t, out, "launchd")
			assert.Equal(t, tc.marker, markerOf(line), out)
			assert.Contains(t, line, strings.ReplaceAll(tc.detail, "{bin}", w.bin))
			assert.Contains(t, hint, strings.ReplaceAll(tc.hint, "{bin}", w.bin))
			if tc.marker == "✓" {
				assert.Contains(t, out, "all checks passed")
			}
		})
	}
}
