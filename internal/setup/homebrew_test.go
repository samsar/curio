package setup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fake tools this test binary becomes; TestMain (main_test.go, package
// setup_test) runs them.
const (
	fakeToolEnv    = "CURIO_SETUPTEST_TOOL"
	fakeBrewEnv    = "CURIO_SETUPTEST_BREW"
	fakeCommandEnv = "CURIO_SETUPTEST_COMMAND"
)

// fakeHomebrew is a Homebrew whose brew is this test binary, answering as
// mode says, with apps looked for in appDir.
func fakeHomebrew(t *testing.T, mode, appDir string) *Homebrew {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	t.Setenv(fakeToolEnv, "brew")
	t.Setenv(fakeBrewEnv, mode)
	return &Homebrew{brews: []string{filepath.Join(t.TempDir(), "no-brew"), exe}, appDirs: []string{appDir},
		timeout: detectTimeout}
}

func TestHomebrew_Detect(t *testing.T) {
	ctx := context.Background()
	apps := t.TempDir()

	h := fakeHomebrew(t, "installed", apps)
	d, err := h.Detect(ctx, "ollama", "Ollama")
	require.NoError(t, err)
	assert.Equal(t, h.brews[1], d.Brew, "the first brew that exists")
	assert.True(t, d.Formula)
	assert.Empty(t, d.App)

	require.NoError(t, os.Mkdir(filepath.Join(apps, "Ollama.app"), 0o700))
	h = fakeHomebrew(t, "missing", apps)
	d, err = h.Detect(ctx, "ollama", "Ollama")
	require.NoError(t, err)
	assert.False(t, d.Formula, "brew list exits 1: not installed")
	assert.Equal(t, filepath.Join(apps, "Ollama.app"), d.App)

	h = fakeHomebrew(t, "broken", apps)
	_, err = h.Detect(ctx, "ollama", "Ollama")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list --versions ollama")
	assert.Contains(t, err.Error(), "Error: Homebrew is broken")
}

// TestHomebrew_DetectTimeout: a brew that doesn't answer is given up on.
func TestHomebrew_DetectTimeout(t *testing.T) {
	h := fakeHomebrew(t, "hang", t.TempDir())
	h.timeout = 200 * time.Millisecond
	start := time.Now()
	_, err := h.Detect(context.Background(), "ollama", "Ollama")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "no answer within 200ms")
	assert.Less(t, time.Since(start), 5*time.Second)
}

// TestHomebrew_DetectWithoutBrew: no Homebrew is an answer, not an error,
// and nothing is run.
func TestHomebrew_DetectWithoutBrew(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	h := &Homebrew{brews: []string{filepath.Join(t.TempDir(), "brew")}, appDirs: []string{t.TempDir()}, timeout: detectTimeout}
	d, err := h.Detect(context.Background(), "ollama", "Ollama")
	require.NoError(t, err)
	assert.Equal(t, Detection{}, d)
}

// fakeCommand is argv for this test binary as a command doing mode.
func fakeCommand(t *testing.T, mode string) []string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	t.Setenv(fakeToolEnv, "command")
	t.Setenv(fakeCommandEnv, mode)
	return []string{exe}
}

// TestHomebrew_Run_Fails: a command's output streams through the UI, and
// its failure names the command line and how it exited.
func TestHomebrew_Run_Fails(t *testing.T) {
	var out bytes.Buffer
	argv := fakeCommand(t, "fail")
	err := (&Homebrew{}).Run(context.Background(), newPlainUI(&out), argv)
	require.Error(t, err)
	assert.Equal(t, "`"+argv[0]+"` failed: exit status 3", err.Error())
	assert.Contains(t, out.String(), "==> Downloading ollama\n")
	assert.Contains(t, out.String(), "Error: no space left on device\n")
}

// TestHomebrew_Run_NoTerminalNoStdin: without a terminal a command reads
// /dev/null, so one that prompts fails a script instead of hanging it.
func TestHomebrew_Run_NoTerminalNoStdin(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, (&Homebrew{}).Run(context.Background(), newPlainUI(&out), fakeCommand(t, "stdin")))
	assert.Equal(t, "stdin gave 0 bytes (<nil>)\n", out.String())
}

// TestHomebrew_Run_Interrupt: cancelling sends the command SIGINT, as
// ctrl-c would, and lets it finish up rather than killing it.
func TestHomebrew_Run_Interrupt(t *testing.T) {
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (&Homebrew{}).Run(ctx, newPlainUI(&out), fakeCommand(t, "interrupt")) }()
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "waiting") }, 10*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		assert.Contains(t, err.Error(), "was interrupted")
	case <-time.After(commandGrace):
		t.Fatal("the command wasn't interrupted")
	}
	assert.Contains(t, out.String(), "interrupted, finishing up", "SIGINT, not SIGKILL")
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
