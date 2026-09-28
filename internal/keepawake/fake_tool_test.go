package keepawake

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The runners are tested against fake pmset and caffeinate binaries: the
// test binary itself, re-executed with fakeToolEnv set. TestMain hands such
// a run to runFakeTool before the test flags are parsed, so the fake sees
// exactly the arguments the runner passed. No test runs the real tools.
const (
	fakeToolEnv = "CURIO_FAKE_TOOL"
	// fakeDirEnv is where the fake caffeinate records its runs, one JSON
	// object per line, and each SIGTERM it gets.
	fakeDirEnv = "CURIO_FAKE_DIR"
	// fakePowerEnv is what the fake pmset says it draws from: "AC Power",
	// "Battery Power", ... or empty for an answer without the line.
	fakePowerEnv = "CURIO_FAKE_POWER"

	modePmset          = "pmset"
	modePmsetFail      = "pmset-fail" // exits 1
	modePmsetHang      = "pmset-hang"
	modeCaffeinate     = "caffeinate"      // runs until SIGTERM
	modeCaffeinateExit = "caffeinate-exit" // exits 1 at once
	// modeByName is pmset or caffeinate, by the name it runs as.
	modeByName = "by-name"

	caffeinateLog = "caffeinate.jsonl"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeToolEnv); mode != "" {
		os.Exit(runFakeTool(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeTool makes the test binary act as the named fake for the rest of the
// test and returns the path to run it by.
func fakeTool(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv(fakeToolEnv, mode)
	// Under -race a Go binary sleeps a second at a clean exit to let late
	// races report; the fakes have nothing to report.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	bin, err := os.Executable()
	require.NoError(t, err)
	return bin
}

// caffeinateRun is one run of the fake caffeinate, as it recorded it.
type caffeinateRun struct {
	Args []string `json:"args,omitempty"`
	PID  int      `json:"pid"`
	// Signal is set on the line recorded when it got SIGTERM.
	Signal string `json:"signal,omitempty"`
}

// caffeinateRuns returns what the fake caffeinate recorded in dir.
func caffeinateRuns(t *testing.T, dir string) []caffeinateRun {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, caffeinateLog))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var runs []caffeinateRun
	for line := range strings.Lines(string(data)) {
		var r caffeinateRun
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		runs = append(runs, r)
	}
	return runs
}

func runFakeTool(mode string, args []string) int {
	if mode == modeByName {
		mode = filepath.Base(os.Args[0])
	}
	switch mode {
	case modePmset:
		if power := os.Getenv(fakePowerEnv); power != "" {
			fmt.Printf("Now drawing from '%s'\n", power)
		}
		fmt.Println(" -InternalBattery-0 (id=1234)\t87%; charging; 0:42 remaining present: true")
		return 0
	case modePmsetFail:
		fmt.Fprintln(os.Stderr, "pmset: something went wrong")
		return 1
	case modePmsetHang:
		time.Sleep(time.Hour)
		return 0
	case modeCaffeinate, modeCaffeinateExit:
		return fakeCaffeinate(mode, args)
	}
	fmt.Fprintln(os.Stderr, "unknown fake tool mode", mode)
	return 2
}

// fakeCaffeinate records its run, then exits at once or waits for SIGTERM,
// which it records too.
func fakeCaffeinate(mode string, args []string) int {
	path := filepath.Join(os.Getenv(fakeDirEnv), caffeinateLog)
	record := func(r caffeinateRun) error {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(f, string(line))
		return errors.Join(err, f.Close())
	}
	terms := make(chan os.Signal, 1)
	signal.Notify(terms, syscall.SIGTERM)
	if err := record(caffeinateRun{Args: args, PID: os.Getpid()}); err != nil {
		fmt.Fprintln(os.Stderr, "fake caffeinate:", err)
		return 2
	}
	if mode == modeCaffeinateExit {
		return 1
	}
	select {
	case <-terms:
		if err := record(caffeinateRun{PID: os.Getpid(), Signal: "SIGTERM"}); err != nil {
			return 2
		}
		return 0
	case <-time.After(time.Minute): // never outlive the test run
		return 0
	}
}

// alive reports whether pid names a process.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// pidString is pid as caffeinate's -w takes it.
func pidString(pid int) string { return strconv.Itoa(pid) }
