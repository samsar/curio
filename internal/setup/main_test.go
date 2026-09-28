package setup_test

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"testing"
	"time"

	"github.com/samsar/curio/internal/setup/setuptest"
)

// The tests run this test binary as the tools setup drives: with
// fakeToolEnv set, TestMain runs a fake instead of the tests, and with
// setuptest.DaemonVar set, the fake daemon. The internal tests (package
// setup) use the same names.
const (
	fakeToolEnv = "CURIO_SETUPTEST_TOOL"
	// fakeBrewEnv is the fake brew's answer to `brew list --versions`:
	// installed, missing, broken, or none (hang).
	fakeBrewEnv = "CURIO_SETUPTEST_BREW"
	// fakeCommandEnv is what the fake command does: fail, stdin, or wait
	// for an interrupt.
	fakeCommandEnv = "CURIO_SETUPTEST_COMMAND"
)

func TestMain(m *testing.M) {
	if code, asked := setuptest.RunDaemonIfAsked(); asked {
		os.Exit(code)
	}
	switch os.Getenv(fakeToolEnv) {
	case "brew":
		os.Exit(fakeBrew(os.Args[1:]))
	case "command":
		os.Exit(fakeCommand())
	}
	os.Exit(m.Run())
}

// fakeBrew answers `brew list --versions <formula>` as its mode says.
func fakeBrew(args []string) int {
	if len(args) != 3 || args[0] != "list" || args[1] != "--versions" {
		fmt.Fprintln(os.Stderr, "fake brew: unexpected", args)
		return 64
	}
	switch os.Getenv(fakeBrewEnv) {
	case "installed":
		fmt.Println(args[2] + " 0.34.4")
		return 0
	case "missing":
		return 1
	case "hang":
		time.Sleep(time.Minute) // until the detection gives up and kills it
		return 0
	default:
		fmt.Fprintln(os.Stderr, "Error: Homebrew is broken")
		return 2
	}
}

// fakeCommand is a command curio runs: one that fails saying so on both
// streams, one that reports what its stdin gave, or one that runs until
// an interrupt, which it reports.
func fakeCommand() int {
	switch os.Getenv(fakeCommandEnv) {
	case "fail":
		fmt.Println("==> Downloading ollama")
		fmt.Fprintln(os.Stderr, "Error: no space left on device")
		return 3
	case "stdin":
		b, err := io.ReadAll(os.Stdin)
		fmt.Printf("stdin gave %d bytes (%v)\n", len(b), err)
		return 0
	case "interrupt":
		interrupted := make(chan os.Signal, 1)
		signal.Notify(interrupted, os.Interrupt)
		fmt.Println("waiting")
		select {
		case <-interrupted:
			fmt.Println("interrupted, finishing up")
			return 0
		case <-time.After(time.Minute): // never outlive the test run
			return 1
		}
	}
	return 64
}
