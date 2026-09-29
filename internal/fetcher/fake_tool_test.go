package fetcher

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	fakeArgsEnv    = "CURIO_FAKE_ARGS"    // yt-dlp appends its arguments here, one JSON array per run
	// fakeSubsEnv lists the caption tracks the fake yt-dlp's video has, as
	// comma-separated kind:lang pairs ("manual:en,auto:en-orig"). A pair
	// ending in !<status> ("auto:en!429") is a track whose download fails
	// with that HTTP status. Unset means one uploaded English track; "none"
	// means no captions at all.
	fakeSubsEnv = "CURIO_FAKE_SUBS"
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
	case "yt-dlp", "yt-dlp-unavailable", "yt-dlp-private", "yt-dlp-gone", "yt-dlp-error", "yt-dlp-429",
		"yt-dlp-no-info", "yt-dlp-hang":
		return fakeYTDLP(mode, args)
	}
	fmt.Fprintln(os.Stderr, "unknown fake tool mode", mode)
	return 2
}

// fakeYTDLP is the fake yt-dlp. Every mode records its arguments and
// start/end times when asked to (fakeArgsEnv, fakeLogEnv), then:
//
//   - yt-dlp: a video with captions (fakeYTDLPRun)
//   - yt-dlp-unavailable: a video that is gone
//   - yt-dlp-private: a private video
//   - yt-dlp-gone: a video that is gone, in words no permanent pattern
//     matches
//   - yt-dlp-error: an extraction that failed for another reason
//   - yt-dlp-429: an extraction YouTube rate-limited
//   - yt-dlp-no-info: a run that exits 0 having written nothing
//   - yt-dlp-hang: a run that never ends
func fakeYTDLP(mode string, args []string) int {
	if path := os.Getenv(fakeArgsEnv); path != "" {
		line, err := json.Marshal(args)
		if err != nil || appendRecord(path, string(line)) != nil {
			return 2
		}
	}
	logPath := os.Getenv(fakeLogEnv)
	if logPath != "" {
		if err := appendLine(logPath, "start"); err != nil {
			return 2
		}
		time.Sleep(100 * time.Millisecond)
	}

	var code int
	switch mode {
	case "yt-dlp":
		code = fakeYTDLPRun(args)
	case "yt-dlp-unavailable":
		fmt.Fprintln(os.Stderr, "WARNING: ffmpeg not found")
		fmt.Fprintln(os.Stderr, "ERROR: Video unavailable")
		code = 1
	case "yt-dlp-private":
		fmt.Fprintln(os.Stderr, "ERROR: [youtube] test_id: Private video. Sign in if you've been granted access to this video")
		code = 1
	case "yt-dlp-gone":
		fmt.Fprintln(os.Stderr, "ERROR: [youtube] test_id: This video is unavailable")
		code = 1
	case "yt-dlp-error":
		fmt.Fprintln(os.Stderr, "ERROR: [youtube] test_id: Unable to extract initial player response")
		code = 1
	case "yt-dlp-429":
		fmt.Fprintln(os.Stderr, "ERROR: [youtube] test_id: Unable to download API page: HTTP Error 429: Too Many Requests")
		code = 1
	case "yt-dlp-no-info":
		code = 0
	case "yt-dlp-hang":
		time.Sleep(time.Hour)
	}

	if logPath != "" {
		if err := appendLine(logPath, "end"); err != nil {
			return 2
		}
	}
	return code
}

// fakeYTDLPRun writes what `yt-dlp --write-info-json --write-subs
// --write-auto-subs` would into the directory of its -o template: one VTT
// per caption language, then an info.json listing the tracks. Like yt-dlp,
// it downloads the uploaded track when a language has both kinds, and a
// track whose download fails (see fakeSubsEnv) aborts the video with an
// ERROR unless it runs with --ignore-errors, which makes that a WARNING.
func fakeYTDLPRun(args []string) int {
	var dir string
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			dir = filepath.Dir(args[i+1])
		}
	}
	tracks, captions, err := parseFakeSubs(cmp.Or(os.Getenv(fakeSubsEnv), "manual:en"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ignoreErrors := slices.Contains(args, "--ignore-errors") || slices.Contains(args, "-i")
	for _, lang := range slices.Sorted(maps.Keys(captions)) {
		c := captions[lang]
		if c.failStatus != 0 {
			msg := fmt.Sprintf("Unable to download video subtitles for '%s': HTTP Error %d: %s",
				lang, c.failStatus, http.StatusText(c.failStatus))
			if !ignoreErrors {
				fmt.Fprintln(os.Stderr, "ERROR: "+msg)
				return 1
			}
			fmt.Fprintln(os.Stderr, "WARNING: "+msg)
			continue
		}
		vtt := "WEBVTT\nKind: captions\nLanguage: " + lang + "\n\n00:00:01.000 --> 00:00:04.000\n" +
			"Hello world this is a test transcript.\n\n00:00:04.500 --> 00:00:08.000\n" +
			"This track is " + c.kind + " " + lang + ".\n"
		if os.WriteFile(filepath.Join(dir, "test_id."+lang+".vtt"), []byte(vtt), 0o600) != nil {
			return 2
		}
	}

	info, err := json.Marshal(map[string]any{
		"title": "Test Video", "channel": "Test Channel", "channel_id": "UC123",
		"upload_date": "20240315", "duration": 120.0, "description": "A test video description.",
		"tags": []string{"test", "video"}, "categories": []string{"Education"},
		"view_count": 1000, "like_count": 50, "language": "en",
		"subtitles": tracks["manual"], "automatic_captions": tracks["auto"],
	})
	if err != nil || os.WriteFile(filepath.Join(dir, "test_id.info.json"), info, 0o600) != nil {
		return 2
	}
	return 0
}

// fakeCaption is the track the fake yt-dlp downloads for one language.
type fakeCaption struct {
	kind       string // "manual" or "auto"
	failStatus int    // the HTTP status its download fails with; 0 when it succeeds
}

// parseFakeSubs reads a fakeSubsEnv value into info.json's two caption
// maps, by kind, and the track downloaded for each language.
func parseFakeSubs(spec string) (tracks map[string]map[string][]any, captions map[string]fakeCaption, err error) {
	tracks = map[string]map[string][]any{"manual": {}, "auto": {}}
	captions = map[string]fakeCaption{}
	for pair := range strings.SplitSeq(spec, ",") {
		kind, track, ok := strings.Cut(pair, ":")
		if !ok {
			continue // "none"
		}
		lang, failure, failing := strings.Cut(track, "!")
		var status int
		if failing {
			if status, err = strconv.Atoi(failure); err != nil {
				return nil, nil, fmt.Errorf("bad %s entry %q: %w", fakeSubsEnv, pair, err)
			}
		}
		tracks[kind][lang] = []any{}
		if captions[lang].kind != "manual" {
			captions[lang] = fakeCaption{kind: kind, failStatus: status}
		}
	}
	return tracks, captions, nil
}

// appendLine appends "<event> <unix nanos>" to path.
func appendLine(path, event string) error {
	return appendRecord(path, fmt.Sprintf("%s %d", event, time.Now().UnixNano()))
}

// appendRecord appends line and a newline to path. Short O_APPEND writes
// don't interleave, so concurrent fakes can share the file.
func appendRecord(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, line+"\n")
	return errors.Join(err, f.Close())
}

// ytdlpRuns returns the argument list of every run of the fake yt-dlp
// that recorded to path (see fakeArgsEnv).
func ytdlpRuns(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var runs [][]string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var args []string
		require.NoError(t, json.Unmarshal([]byte(line), &args))
		runs = append(runs, args)
	}
	return runs
}
