package service

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/curiohome"
)

// testLaunchd is a Launchd for a new non-default home, driving a fake
// launchctl as uid 501 in agents, a directory not yet created.
type testLaunchd struct {
	*Launchd
	fake    *fakeLaunchctl
	home    *curiohome.Home
	program string // an executable the agent can run
	agents  string
}

func newTestLaunchd(t *testing.T, mode string, st fakeState) *testLaunchd {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // the default home, ~/.curio, is elsewhere
	home, err := curiohome.Init(t.TempDir(), "qwen3-embedding:0.6b", 1024)
	require.NoError(t, err)
	program := filepath.Join(t.TempDir(), "curio-daemon")
	require.NoError(t, os.WriteFile(program, []byte("#!/bin/false\n"), 0o700))
	fake := newFakeLaunchctl(t, mode, st)
	agents := filepath.Join(t.TempDir(), "LaunchAgents")
	l, err := NewLaunchd(home, LaunchdOptions{
		Launchctl: fake.bin, AgentsDir: agents, UID: 501, Geteuid: func() int { return 501 },
		PollInterval: 5 * time.Millisecond,
	})
	require.NoError(t, err)
	return &testLaunchd{Launchd: l, fake: fake, home: home, program: program, agents: agents}
}

// golden returns testdata/other-home.plist as Install writes it for tl:
// its label, home and program in place of the golden's.
func (tl *testLaunchd) golden(t *testing.T, program string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "other-home.plist"))
	require.NoError(t, err)
	s := strings.NewReplacer(
		BaseLabel+".1a2b3c4d", tl.Label(),
		"/Users/x/curio-work", tl.home.Path,
		"/opt/homebrew/bin/curio-daemon", program,
	).Replace(string(data))
	return []byte(s)
}

func argv(words ...string) []string { return words }

func TestAgentLabel(t *testing.T) {
	userHome := t.TempDir()
	defaultHome := filepath.Join(userHome, curiohome.DefaultDirName)
	require.NoError(t, os.Mkdir(defaultHome, 0o700))
	linkToDefault := filepath.Join(t.TempDir(), "curio")
	require.NoError(t, os.Symlink(defaultHome, linkToDefault))

	for name, home := range map[string]string{"~/.curio": defaultHome, "a symlink to it": linkToDefault} {
		label, isDefault := agentLabel(home, defaultHome)
		assert.Equal(t, BaseLabel, label, name)
		assert.True(t, isDefault, name)
	}
	t.Setenv("CURIO_HOME", linkToDefault)
	resolved, err := curiohome.Resolve()
	require.NoError(t, err)
	label, _ := agentLabel(resolved, defaultHome)
	assert.Equal(t, BaseLabel, label, "CURIO_HOME naming the default home")

	other := t.TempDir()
	linkToOther := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.Symlink(other, linkToOther))
	label, isDefault := agentLabel(other, defaultHome)
	assert.False(t, isDefault)
	assert.Regexp(t, `^`+strings.ReplaceAll(BaseLabel, ".", `\.`)+`\.[0-9a-f]{8}$`, label)
	viaLink, _ := agentLabel(linkToOther, defaultHome)
	assert.Equal(t, label, viaLink, "two spellings of one home")
	another, _ := agentLabel(t.TempDir(), defaultHome)
	assert.NotEqual(t, label, another, "two homes")
}

// TestNewLaunchd_Defaults: with no options, the agent of ~/.curio is the
// bare label in ~/Library/LaunchAgents, driven by /bin/launchctl.
func TestNewLaunchd_Defaults(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	home, err := curiohome.Init(filepath.Join(userHome, curiohome.DefaultDirName), "qwen3-embedding:0.6b", 1024)
	require.NoError(t, err)
	l, err := NewLaunchd(home, LaunchdOptions{})
	require.NoError(t, err)
	assert.Equal(t, BaseLabel, l.Label())
	assert.Equal(t, filepath.Join(userHome, "Library", "LaunchAgents", BaseLabel+".plist"), l.PlistPath())
	assert.Equal(t, "/bin/launchctl", l.opts.Launchctl)
	assert.Equal(t, os.Getuid(), l.opts.UID)
	assert.Equal(t, ExitTimeout+15*time.Second, l.opts.LongCallTimeout)
	assert.False(t, l.nonDefaultHome)
}

// TestNewLaunchd_NoHOME: an environment without $HOME (a supervisor's,
// say) still finds the user's agents and default home in the user
// database, so a command given its home needs neither.
func TestNewLaunchd_NoHOME(t *testing.T) {
	u, err := user.Current()
	require.NoError(t, err)
	t.Setenv("HOME", "")
	home, err := curiohome.Init(t.TempDir(), "qwen3-embedding:0.6b", 1024)
	require.NoError(t, err)

	l, err := NewLaunchd(home, LaunchdOptions{})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(u.HomeDir, "Library", "LaunchAgents"), l.opts.AgentsDir)
	label, _ := agentLabel(home.Path, filepath.Join(u.HomeDir, curiohome.DefaultDirName))
	assert.Equal(t, label, l.Label())
	assert.True(t, l.nonDefaultHome)
}

func TestRenderPlist(t *testing.T) {
	cases := []struct {
		golden string
		plist  agentPlist
	}{
		{"default-home.plist", agentPlist{Label: BaseLabel, Program: "/opt/homebrew/bin/curio-daemon",
			Home: "/Users/x/.curio", StdoutPath: "/Users/x/.curio/logs/daemon.log",
			StderrPath: "/Users/x/.curio/logs/launchd.err"}},
		{"other-home.plist", agentPlist{Label: BaseLabel + ".1a2b3c4d", Program: "/opt/homebrew/bin/curio-daemon",
			Home: "/Users/x/curio-work", NonDefaultHome: true, StdoutPath: "/Users/x/curio-work/logs/daemon.log",
			StderrPath: "/Users/x/curio-work/logs/launchd.err"}},
		{"escaped-home.plist", agentPlist{Label: BaseLabel + ".1a2b3c4d", Program: "/Users/x/tools & bits/curio-daemon",
			Home: "/Users/x/a & b <c>/curio", NonDefaultHome: true, StdoutPath: "/Users/x/a & b <c>/curio/logs/daemon.log",
			StderrPath: "/Users/x/a & b <c>/curio/logs/launchd.err"}},
	}
	for _, tc := range cases {
		t.Run(tc.golden, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			require.NoError(t, err)
			got, err := renderPlist(tc.plist)
			require.NoError(t, err)
			assert.Equal(t, string(want), string(got))

			program, err := plistProgram(got)
			require.NoError(t, err)
			assert.Equal(t, tc.plist.Program, program, "read back")
		})
	}
}

// TestGoldenPlists_Lint: launchd's own parser accepts every golden.
func TestGoldenPlists_Lint(t *testing.T) {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil is macOS's")
	}
	goldens, err := filepath.Glob(filepath.Join("testdata", "*.plist"))
	require.NoError(t, err)
	require.Len(t, goldens, 3)
	out, err := exec.Command(plutil, append([]string{"-lint"}, goldens...)...).CombinedOutput()
	require.NoError(t, err, "%s", out)
}

func TestPlistProgram_Malformed(t *testing.T) {
	for name, plist := range map[string]string{
		"not XML":                "{not xml",
		"no ProgramArguments":    `<plist version="1.0"><dict><key>Label</key><string>x</string></dict></plist>`,
		"empty ProgramArguments": `<plist version="1.0"><dict><key>ProgramArguments</key><array></array></dict></plist>`,
		"not a string":           `<plist version="1.0"><dict><key>ProgramArguments</key><array><integer>1</integer></array></dict></plist>`,
	} {
		_, err := plistProgram([]byte(plist))
		assert.Error(t, err, name)
	}
}

func TestInstall_Fresh(t *testing.T) {
	tl := newTestLaunchd(t, modeLaunchctl, fakeState{})
	target := "gui/501/" + tl.Label()

	changed, err := tl.Install(context.Background(), Spec{Program: tl.program})
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, [][]string{
		argv("print", target), argv("enable", target), argv("bootstrap", "gui/501", tl.PlistPath()),
	}, tl.fake.calls())

	data, err := os.ReadFile(tl.PlistPath())
	require.NoError(t, err)
	assert.Equal(t, string(tl.golden(t, tl.program)), string(data))
	info, err := os.Stat(tl.PlistPath())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	agents, err := os.Stat(tl.agents)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), agents.Mode().Perm())
	assert.DirExists(t, tl.home.LogsDir())
	entries, err := os.ReadDir(tl.agents)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file left behind")
}

// TestInstall_CreatesTheLogsDir: launchd opens the daemon's output files
// itself, in a logs directory that must exist.
func TestInstall_CreatesTheLogsDir(t *testing.T) {
	tl := newTestLaunchd(t, modeLaunchctl, fakeState{})
	require.NoError(t, os.RemoveAll(tl.home.LogsDir()))
	_, err := tl.Install(context.Background(), Spec{Program: tl.program})
	require.NoError(t, err)
	info, err := os.Stat(tl.home.LogsDir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// TestInstall_Idempotent: an agent loaded from the same plist is left
// alone; one loaded from another is booted out, waited out, and loaded
// again.
func TestInstall_Idempotent(t *testing.T) {
	ctx := context.Background()
	tl := newTestLaunchd(t, modeLaunchctl, fakeState{})
	target := "gui/501/" + tl.Label()
	_, err := tl.Install(ctx, Spec{Program: tl.program})
	require.NoError(t, err)
	before := len(tl.fake.calls())

	changed, err := tl.Install(ctx, Spec{Program: tl.program})
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, [][]string{argv("print", target)}, tl.fake.calls()[before:])

	st := tl.fake.state()
	st.StayLoaded = 2
	tl.fake.set(st)
	before = len(tl.fake.calls())
	moved := filepath.Join(t.TempDir(), "curio-daemon")
	require.NoError(t, os.WriteFile(moved, []byte("#!/bin/false\n"), 0o700))
	changed, err = tl.Install(ctx, Spec{Program: moved})
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, [][]string{
		argv("print", target), argv("bootout", target),
		argv("print", target), argv("print", target), argv("print", target), // loaded, loaded, gone
		argv("enable", target), argv("bootstrap", "gui/501", tl.PlistPath()),
	}, tl.fake.calls()[before:])
	data, err := os.ReadFile(tl.PlistPath())
	require.NoError(t, err)
	assert.Equal(t, string(tl.golden(t, moved)), string(data))
}

// TestInstall_SymlinkedProgram: the plist runs the daemon by the path it
// was installed at, so an upgrade that repoints the symlink needs no new
// plist.
func TestInstall_SymlinkedProgram(t *testing.T) {
	tl := newTestLaunchd(t, modeLaunchctl, fakeState{})
	link := filepath.Join(t.TempDir(), "curio-daemon")
	require.NoError(t, os.Symlink(tl.program, link))
	_, err := tl.Install(context.Background(), Spec{Program: link})
	require.NoError(t, err)
	data, err := os.ReadFile(tl.PlistPath())
	require.NoError(t, err)
	assert.Equal(t, string(tl.golden(t, link)), string(data))
	st, err := tl.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, link, st.Program)
}

// TestInstall_Refusals: a daemon launchd couldn't run, or an install as
// root, writes nothing and runs no launchctl; Preflight refuses the same,
// with the same error.
func TestInstall_Refusals(t *testing.T) {
	cases := []struct {
		name    string
		program func(tl *testLaunchd) string
		root    bool
		want    string
	}{
		{"root", func(tl *testLaunchd) string { return tl.program }, true, "curio never uses sudo"},
		{"relative", func(*testLaunchd) string { return "bin/curio-daemon" }, false, "absolute path"},
		{"missing", func(tl *testLaunchd) string { return tl.program + ".gone" }, false, "no such file"},
		{"a directory", func(tl *testLaunchd) string { return filepath.Dir(tl.program) }, false, "is a directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tl := newTestLaunchd(t, modeLaunchctl, fakeState{})
			if tc.root {
				tl.opts.Geteuid = func() int { return 0 }
			}
			spec := Spec{Program: tc.program(tl)}
			preflight := tl.Preflight(context.Background(), spec)
			_, err := tl.Install(context.Background(), spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Equal(t, err, preflight)
			assert.Empty(t, tl.fake.calls())
			assert.NoDirExists(t, tl.agents)
		})
	}
}

// TestPreflight: an install that would go ahead passes, having asked
// launchd only what it has loaded.
func TestPreflight(t *testing.T) {
	tl := newTestLaunchd(t, modeLaunchctl, fakeState{Loaded: true, PID: 4242})
	require.NoError(t, tl.Preflight(context.Background(), Spec{Program: tl.program}))
	assert.Equal(t, []string{"print"}, tl.fake.verbs())
	assert.NoDirExists(t, tl.agents)
}

// TestInstall_FailedRename: a plist that can't be put in place leaves no
// temp file, and nothing is loaded.
func TestInstall_FailedRename(t *testing.T) {
	tl := newTestLaunchd(t, modeLaunchctl, fakeState{})
	require.NoError(t, os.MkdirAll(filepath.Join(tl.PlistPath(), "keep"), 0o700))
	_, err := tl.Install(context.Background(), Spec{Program: tl.program})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install the launchd agent's plist")
	assert.Equal(t, []string{"print"}, tl.fake.verbs())
	entries, err := os.ReadDir(tl.agents)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, filepath.Base(tl.PlistPath()), entries[0].Name(), "only what was there")
}

// TestInstall_BootstrapFails: the error names the command, its exit
// status and what it said, and where to look next.
func TestInstall_BootstrapFails(t *testing.T) {
	tl := newTestLaunchd(t, modeLaunchctl, fakeState{Fail: map[string]fakeFailure{
		"bootstrap": {Exit: 5, Stderr: "Bootstrap failed: 5: Input/output error"},
	}})
	_, err := tl.Install(context.Background(), Spec{Program: tl.program})
	require.Error(t, err)
	var lerr *LaunchctlError
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, 5, lerr.ExitCode)
	for _, want := range []string{
		"launchctl bootstrap gui/501 " + tl.PlistPath() + ": exit 5: Bootstrap failed: 5: Input/output error",
		"`launchctl print gui/501/" + tl.Label() + "`",
		"System Settings > General > Login Items & Extensions",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

// TestInstall_NoGUISession: over ssh with nobody logged in at the Mac
// there is no GUI domain to load an agent in; the error says so and what
// to do.
func TestInstall_NoGUISession(t *testing.T) {
	tl := newTestLaunchd(t, modeLaunchctl, fakeState{NoDomain: true})
	_, err := tl.Install(context.Background(), Spec{Program: tl.program})
	require.ErrorIs(t, err, ErrNoGUISession)
	assert.Contains(t, err.Error(), "log in to the Mac's desktop session")
	assert.Contains(t, err.Error(), "exit 112")
	assert.Equal(t, []string{"print"}, tl.fake.verbs())
	require.ErrorIs(t, tl.Preflight(context.Background(), Spec{Program: tl.program}), ErrNoGUISession)
}

func TestStartRestartStop(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		out   string
		call  func(*Launchd, context.Context) (int, error)
		want  []string
		state int // the PID the fake runs before the call
		pid   int
	}{
		{"start, pid alone", "123\n", (*Launchd).Start, argv("kickstart", "-p"), 0, 123},
		{"start, pid in words", "service spawned with pid: 123\n", (*Launchd).Start, argv("kickstart", "-p"), 0, 123},
		{"start, nothing printed", "", (*Launchd).Start, argv("kickstart", "-p"), 0, 1},
		{"start, already running", "", (*Launchd).Start, argv("kickstart", "-p"), 77, 77},
		{"restart", "service spawned with pid: 124\n", (*Launchd).Restart, argv("kickstart", "-k", "-p"), 77, 124},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tl := newTestLaunchd(t, modeLaunchctl, fakeState{Loaded: true, PID: tc.state, KickstartOut: tc.out})
			pid, err := tc.call(tl.Launchd, ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.pid, pid)
			calls := tl.fake.calls()
			require.NotEmpty(t, calls)
			assert.Equal(t, append(tc.want, "gui/501/"+tl.Label()), calls[0])
		})
	}

	t.Run("start, nothing runs", func(t *testing.T) {
		// A kickstart that prints nothing and starts nothing: print then
		// reports no pid either.
		tl := newTestLaunchd(t, modeLaunchctl, fakeState{Loaded: true, Fail: map[string]fakeFailure{"kickstart": {}}})
		_, err := tl.Start(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "launchd runs no daemon for "+tl.Label())
	})

	t.Run("stop", func(t *testing.T) {
		tl := newTestLaunchd(t, modeLaunchctl, fakeState{Loaded: true, PID: 77})
		require.NoError(t, tl.Stop(ctx))
		assert.Equal(t, [][]string{argv("kill", "SIGTERM", "gui/501/"+tl.Label())}, tl.fake.calls())
	})
}

func TestUninstall(t *testing.T) {
	ctx := context.Background()

	t.Run("loaded", func(t *testing.T) {
		tl := newTestLaunchd(t, modeLaunchctl, fakeState{})
		_, err := tl.Install(ctx, Spec{Program: tl.program})
		require.NoError(t, err)
		st := tl.fake.state()
		st.StayLoaded = 2
		tl.fake.set(st)
		before := len(tl.fake.calls())

		removed, err := tl.Uninstall(ctx)
		require.NoError(t, err)
		assert.True(t, removed)
		target := "gui/501/" + tl.Label()
		assert.Equal(t, [][]string{
			argv("print", target), argv("bootout", target),
			argv("print", target), argv("print", target), argv("print", target),
		}, tl.fake.calls()[before:])
		assert.NoFileExists(t, tl.PlistPath())
	})

	t.Run("nothing installed", func(t *testing.T) {
		tl := newTestLaunchd(t, modeLaunchctl, fakeState{})
		removed, err := tl.Uninstall(ctx)
		require.NoError(t, err)
		assert.False(t, removed)
		assert.Equal(t, []string{"print"}, tl.fake.verbs())
	})

	t.Run("loaded without its plist", func(t *testing.T) {
		tl := newTestLaunchd(t, modeLaunchctl, fakeState{Loaded: true, PID: 77})
		removed, err := tl.Uninstall(ctx)
		require.NoError(t, err)
		assert.True(t, removed)
		assert.Equal(t, []string{"print", "bootout", "print"}, tl.fake.verbs())
	})
}

func TestStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("no plist runs no launchctl", func(t *testing.T) {
		tl := newTestLaunchd(t, modeLaunchctl, fakeState{Loaded: true, PID: 77})
		st, err := tl.Status(ctx)
		require.NoError(t, err)
		assert.Equal(t, Status{Supported: true, Label: tl.Label(), Path: tl.PlistPath()}, st)
		assert.Empty(t, tl.fake.calls())
	})

	cases := []struct {
		name  string
		state fakeState
		want  Status // Supported, Label, Path, Installed and Program are filled in
	}{
		{"running", fakeState{Loaded: true, PID: 4242},
			Status{Loaded: true, State: "running", PID: 4242}},
		{"loaded, exited non-zero", fakeState{Loaded: true, LastExit: "3"},
			Status{Loaded: true, State: "not running", LastExit: "exit code 3"}},
		{"loaded, exited with a named code", fakeState{Loaded: true, LastExit: "78: EX_CONFIG"},
			Status{Loaded: true, State: "not running", LastExit: "exit code 78: EX_CONFIG"}},
		{"loaded, killed", fakeState{Loaded: true, Signal: "Killed: 9"},
			Status{Loaded: true, State: "not running", LastExit: "signal Killed: 9"}},
		{"loaded, exited cleanly", fakeState{Loaded: true, LastExit: "0"},
			Status{Loaded: true, State: "not running"}},
		{"not loaded", fakeState{}, Status{}},
		{"no GUI domain", fakeState{NoDomain: true}, Status{NoGUISession: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tl := newTestLaunchd(t, modeLaunchctl, tc.state)
			writeGoldenPlist(t, tl)
			st, err := tl.Status(ctx)
			require.NoError(t, err)
			tc.want.Supported, tc.want.Label, tc.want.Path = true, tl.Label(), tl.PlistPath()
			tc.want.Installed, tc.want.Program = true, tl.program
			assert.Equal(t, tc.want, st)
		})
	}

	t.Run("an unexpected exit is an error", func(t *testing.T) {
		tl := newTestLaunchd(t, modeLaunchctl, fakeState{Fail: map[string]fakeFailure{
			"print": {Exit: 5, Stderr: "Input/output error"},
		}})
		writeGoldenPlist(t, tl)
		_, err := tl.Status(ctx)
		var lerr *LaunchctlError
		require.ErrorAs(t, err, &lerr)
		assert.Equal(t, 5, lerr.ExitCode)
		assert.Equal(t, "launchctl print gui/501/"+tl.Label()+": exit 5: Input/output error", err.Error())
	})

	t.Run("an unreadable plist has no program", func(t *testing.T) {
		tl := newTestLaunchd(t, modeLaunchctl, fakeState{Loaded: true, PID: 4242})
		require.NoError(t, os.MkdirAll(tl.agents, 0o755))
		require.NoError(t, os.WriteFile(tl.PlistPath(), []byte("{not xml"), 0o644))
		st, err := tl.Status(ctx)
		require.NoError(t, err)
		assert.True(t, st.Installed)
		assert.Empty(t, st.Program)
		assert.True(t, st.Loaded)
	})
}

// writeGoldenPlist installs tl's plist as Install would, without
// launchctl.
func writeGoldenPlist(t *testing.T, tl *testLaunchd) {
	t.Helper()
	require.NoError(t, os.MkdirAll(tl.agents, 0o755))
	require.NoError(t, os.WriteFile(tl.PlistPath(), tl.golden(t, tl.program), 0o644))
}

// TestRun_StderrIsCapped: a launchctl that floods its stderr is quoted at
// most 4 KiB deep.
func TestRun_StderrIsCapped(t *testing.T) {
	tl := newTestLaunchd(t, modeFloodStderr, fakeState{})
	writeGoldenPlist(t, tl)
	_, err := tl.Status(context.Background())
	var lerr *LaunchctlError
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, 1, lerr.ExitCode)
	assert.LessOrEqual(t, len(lerr.Stderr), maxLaunchctlStderr)
	assert.NotEmpty(t, lerr.Stderr)
}

// TestRun_Timeout: a launchctl that never answers is given up on within
// its timeout, and killed.
func TestRun_Timeout(t *testing.T) {
	tl := newTestLaunchd(t, modeHang, fakeState{})
	writeGoldenPlist(t, tl)
	tl.opts.CallTimeout = 2 * time.Second

	start := time.Now()
	_, err := tl.Status(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), tl.opts.CallTimeout+2*time.Second)
	assert.Contains(t, err.Error(), "launchctl print gui/501/"+tl.Label()+": no answer within 2s")

	data, err := os.ReadFile(filepath.Join(tl.fake.dir, fakeHangPID))
	require.NoError(t, err, "the fake started")
	pid, err := strconv.Atoi(string(data))
	require.NoError(t, err)
	assert.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "the hung launchctl is gone")
}

// TestLastExit: how print's two lines about the last exit read.
func TestLastExit(t *testing.T) {
	cases := []struct{ code, signal, want string }{
		{"(never exited)", "", ""},
		{"0", "", ""},
		{"1", "", "exit code 1"},
		{"78: EX_CONFIG", "", "exit code 78: EX_CONFIG"},
		{"", "Terminated: 15", "signal Terminated: 15"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, lastExit(tc.code, tc.signal), "%q %q", tc.code, tc.signal)
	}
}
