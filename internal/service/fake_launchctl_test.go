package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The launchd tests run against a fake launchctl: the test binary itself,
// re-executed with fakeToolEnv set. TestMain hands such a run to
// runFakeLaunchctl before the test flags are parsed, so the fake sees
// exactly the arguments launchctl would. It keeps launchd's side of things
// in a JSON state file and appends each run's arguments to a log, both in
// the directory fakeDirEnv names. No test runs /bin/launchctl.
const (
	fakeToolEnv = "CURIO_FAKE_TOOL"
	fakeDirEnv  = "CURIO_FAKE_LAUNCHCTL_DIR"

	modeLaunchctl   = "launchctl"
	modeHang        = "launchctl-hang"         // writes its pid to hang.pid, then never exits
	modeFloodStderr = "launchctl-flood-stderr" // writes 1 MiB to stderr and exits 1

	fakeStateFile = "state.json"
	fakeCallsFile = "calls.jsonl"
	fakeHangPID   = "hang.pid"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeToolEnv); mode != "" {
		os.Exit(runFakeLaunchctl(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeState is launchd, as the fake launchctl keeps it.
type fakeState struct {
	NoDomain bool   `json:"no_domain,omitempty"` // the user has no GUI domain
	Loaded   bool   `json:"loaded,omitempty"`
	PID      int    `json:"pid,omitempty"`       // the agent's running daemon; 0 for none
	LastExit string `json:"last_exit,omitempty"` // print's "last exit code"; empty prints "(never exited)"
	Signal   string `json:"signal,omitempty"`    // print's "last terminating signal", when set
	// StayLoaded is how many prints after a bootout still find the agent
	// loaded, as launchd does while the job exits; Unloading counts them
	// down.
	StayLoaded int `json:"stay_loaded,omitempty"`
	Unloading  int `json:"unloading,omitempty"`
	// KickstartOut is what kickstart prints on stdout.
	KickstartOut string `json:"kickstart_out,omitempty"`
	// Fail makes a verb fail.
	Fail map[string]fakeFailure `json:"fail,omitempty"`
}

type fakeFailure struct {
	Exit   int    `json:"exit"`
	Stderr string `json:"stderr"`
}

// fakeLaunchctl is a test's handle on its fake launchctl.
type fakeLaunchctl struct {
	t   *testing.T
	dir string
	bin string
}

// newFakeLaunchctl makes the test binary act as launchctl in mode for the
// rest of the test, starting from st.
func newFakeLaunchctl(t *testing.T, mode string, st fakeState) *fakeLaunchctl {
	t.Helper()
	t.Setenv(fakeToolEnv, mode)
	// Under -race a Go binary sleeps a second at a clean exit to let late
	// races report; the fake has nothing to report.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	f := &fakeLaunchctl{t: t, dir: t.TempDir()}
	t.Setenv(fakeDirEnv, f.dir)
	bin, err := os.Executable()
	require.NoError(t, err)
	f.bin = bin
	f.set(st)
	return f
}

func (f *fakeLaunchctl) set(st fakeState) {
	f.t.Helper()
	require.NoError(f.t, writeFakeState(f.dir, st))
}

func (f *fakeLaunchctl) state() fakeState {
	f.t.Helper()
	st, err := readFakeState(f.dir)
	require.NoError(f.t, err)
	return st
}

// calls returns the arguments of each run so far, in order.
func (f *fakeLaunchctl) calls() [][]string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, fakeCallsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(f.t, err)
	var out [][]string
	for line := range strings.Lines(string(data)) {
		var args []string
		require.NoError(f.t, json.Unmarshal([]byte(line), &args))
		out = append(out, args)
	}
	return out
}

// verbs returns the verb of each run so far, in order.
func (f *fakeLaunchctl) verbs() []string {
	f.t.Helper()
	calls := f.calls()
	out := make([]string, 0, len(calls))
	for _, args := range calls {
		out = append(out, args[0])
	}
	return out
}

func readFakeState(dir string) (fakeState, error) {
	var st fakeState
	data, err := os.ReadFile(filepath.Join(dir, fakeStateFile))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(data, &st)
}

func writeFakeState(dir string, st fakeState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fakeStateFile), data, 0o600)
}

func runFakeLaunchctl(mode string, args []string) int {
	dir := os.Getenv(fakeDirEnv)
	line, err := json.Marshal(args)
	if err != nil {
		return fakeFail(err)
	}
	if err := appendLine(filepath.Join(dir, fakeCallsFile), string(line)); err != nil {
		return fakeFail(err)
	}
	switch mode {
	case modeHang:
		if err := os.WriteFile(filepath.Join(dir, fakeHangPID), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			return fakeFail(err)
		}
		time.Sleep(time.Hour)
		return 0
	case modeFloodStderr:
		fmt.Fprint(os.Stderr, strings.Repeat("e", 1<<20))
		return 1
	}

	st, err := readFakeState(dir)
	if err != nil {
		return fakeFail(err)
	}
	if len(args) == 0 {
		return fakeFail(errors.New("no verb"))
	}
	verb := args[0]
	if f, ok := st.Fail[verb]; ok {
		fmt.Fprintln(os.Stderr, f.Stderr)
		return f.Exit
	}
	code := fakeVerb(&st, verb, args[1:])
	if err := writeFakeState(dir, st); err != nil {
		return fakeFail(err)
	}
	return code
}

// fakeVerb runs verb against st, as launchd would, and returns the exit
// status.
func fakeVerb(st *fakeState, verb string, args []string) int {
	target := args[len(args)-1]
	switch verb {
	case "print":
		switch {
		case st.NoDomain:
			fmt.Fprintln(os.Stderr, "Could not find domain for user gui: 501")
			return exitNoDomain
		case !st.Loaded:
			fmt.Fprintf(os.Stderr, "Bad request.\nCould not find service %q in domain for user gui: 501\n",
				target[strings.LastIndex(target, "/")+1:])
			return exitNoService
		}
		fmt.Print(fakePrint(target, *st))
		if st.Unloading > 0 {
			st.Unloading--
			st.Loaded = st.Unloading > 0
		}
	case "enable":
	case "bootstrap":
		st.Loaded, st.PID = true, 4242 // RunAtLoad
	case "bootout":
		st.PID, st.Unloading = 0, st.StayLoaded
		st.Loaded = st.Unloading > 0
	case "kickstart":
		if !st.Loaded {
			fmt.Fprintln(os.Stderr, "Could not find service")
			return exitNoService
		}
		if st.PID == 0 || args[0] == "-k" {
			st.PID++
		}
		fmt.Print(st.KickstartOut)
	case "kill":
	default:
		fmt.Fprintln(os.Stderr, "Unrecognized subcommand:", verb)
		return 1
	}
	return 0
}

// fakePrint is print's answer for a loaded agent: its own properties one
// tab deep, and blocks whose lines, deeper, include a state and a pid of
// their own.
func fakePrint(target string, st fakeState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s = {\n\tactive count = 1\n\tpath = /fake/%s.plist\n\ttype = LaunchAgent\n", target, target)
	state := "not running"
	if st.PID > 0 {
		state = "running"
	}
	fmt.Fprintf(&b, "\tstate = %s\n\n\tprogram = /opt/homebrew/bin/curio-daemon\n", state)
	b.WriteString("\targuments = {\n\t\t/opt/homebrew/bin/curio-daemon\n\t}\n\n")
	b.WriteString("\tendpoints = {\n\t\t\"com.example.port\" = {\n\t\t\tport = 0x1234\n\t\t\tactive = 1\n\t\t}\n\t}\n")
	b.WriteString("\tsockets = {\n\t\tstate = active\n\t\tpid = 1\n\t}\n")
	b.WriteString("\tdefault environment = {\n\t\tPATH => /usr/bin:/bin:/usr/sbin:/sbin\n\t}\n\n")
	b.WriteString("\tdomain = gui/501 [100005]\n\tminimum runtime = 10\n\texit timeout = 25\n\truns = 1\n")
	if st.PID > 0 {
		fmt.Fprintf(&b, "\tpid = %d\n", st.PID)
	}
	lastExit := st.LastExit
	if lastExit == "" {
		lastExit = "(never exited)"
	}
	if st.Signal != "" {
		fmt.Fprintf(&b, "\tlast terminating signal = %s\n", st.Signal)
	} else {
		fmt.Fprintf(&b, "\tlast exit code = %s\n", lastExit)
	}
	b.WriteString("\n\tspawn type = daemon (3)\n\tjetsam priority = 40\n}\n")
	return b.String()
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, line)
	return errors.Join(err, f.Close())
}

func fakeFail(err error) int {
	fmt.Fprintln(os.Stderr, "fake launchctl:", err)
	return 2
}
