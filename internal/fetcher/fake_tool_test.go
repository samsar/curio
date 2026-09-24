package fetcher

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The subprocess fetchers are tested against fake yt-dlp and web2md
// binaries: the test binary itself, re-executed with fakeToolEnv set.
// TestMain hands such a run to runFakeTool before the test flags are
// parsed, so the fake sees exactly the arguments the fetcher passed. No
// scripts are written at test time and no shell is needed.
const (
	fakeToolEnv    = "CURIO_FAKE_TOOL"
	fakePIDFileEnv = "CURIO_FAKE_PIDFILE" // hang-with-grandchild writes the grandchild's PID here
	fakeLogEnv     = "CURIO_FAKE_LOG"     // yt-dlp appends start/end timestamps here
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
	// Under -race a Go binary sleeps a second at a clean exit to let
	// late races report; the fakes have nothing to report.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	bin, err := os.Executable()
	require.NoError(t, err)
	return bin
}

func runFakeTool(mode string, args []string) int {
	switch mode {
	case "web2md":
		fmt.Printf("---\ntitle: \"Fake Title\"\nsource: %q\nvia: \"test\"\n---\n\n# Fake Title\n\nThis is the body.\n", args[0])
		return 0
	case "web2md-fail":
		fmt.Fprintln(os.Stderr, "[fake] login wall detected")
		return 1
	case "flood-stdout":
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for {
			if _, err := os.Stdout.Write(chunk); err != nil {
				return 1
			}
		}
	case "flood-stderr":
		chunk := bytes.Repeat([]byte("e"), 1<<20)
		for range 10 {
			if _, err := os.Stderr.Write(chunk); err != nil {
				return 1
			}
		}
		return 1
	case "hang-with-grandchild":
		// A helper that inherits the output pipes, as a Node or Python
		// child process would, then the tool itself hangs.
		helper := exec.Command("sleep", "60")
		helper.Stdout, helper.Stderr = os.Stdout, os.Stderr
		if err := helper.Start(); err != nil {
			return 2
		}
		if err := os.WriteFile(os.Getenv(fakePIDFileEnv), []byte(strconv.Itoa(helper.Process.Pid)), 0o600); err != nil {
			return 2
		}
		time.Sleep(time.Hour)
		return 0
	case "yt-dlp":
		return fakeYTDLP(args)
	case "yt-dlp-unavailable":
		fmt.Fprintln(os.Stderr, "WARNING: ffmpeg not found")
		fmt.Fprintln(os.Stderr, "ERROR: Video unavailable")
		return 1
	}
	fmt.Fprintln(os.Stderr, "unknown fake tool mode", mode)
	return 2
}

// fakeYTDLP writes what `yt-dlp --write-info-json --write-subs` would into
// the directory of its -o template: an info.json and an English VTT.
func fakeYTDLP(args []string) int {
	logPath := os.Getenv(fakeLogEnv)
	if logPath != "" {
		if err := appendLine(logPath, "start"); err != nil {
			return 2
		}
		time.Sleep(100 * time.Millisecond)
	}

	var dir string
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			dir = filepath.Dir(args[i+1])
		}
	}
	info := `{"title":"Test Video","channel":"Test Channel","channel_id":"UC123","upload_date":"20240315",` +
		`"duration":120.0,"description":"A test video description.","tags":["test","video"],` +
		`"categories":["Education"],"view_count":1000,"like_count":50,"language":"en",` +
		`"subtitles":{"en":[]},"automatic_captions":{}}`
	vtt := "WEBVTT\nKind: captions\nLanguage: en\n\n00:00:01.000 --> 00:00:04.000\n" +
		"Hello world this is a test transcript.\n\n00:00:04.500 --> 00:00:08.000\nIt has multiple lines of content.\n"
	if os.WriteFile(filepath.Join(dir, "test_id.info.json"), []byte(info), 0o600) != nil ||
		os.WriteFile(filepath.Join(dir, "test_id.en.vtt"), []byte(vtt), 0o600) != nil {
		return 2
	}

	if logPath != "" {
		if err := appendLine(logPath, "end"); err != nil {
			return 2
		}
	}
	return 0
}

// appendLine appends "<event> <unix nanos>" to path. Short O_APPEND writes
// don't interleave, so concurrent fakes can share the file.
func appendLine(path, event string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s %d\n", event, time.Now().UnixNano())
	return errors.Join(err, f.Close())
}
