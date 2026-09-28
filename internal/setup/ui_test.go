package setup

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChooseMode(t *testing.T) {
	cases := []struct {
		name                   string
		stdin, stderr          bool
		termEnv, accessibleEnv string
		want                   Mode
	}{
		{"both terminals", true, true, "xterm-256color", "", ModeTerminal},
		{"stdin piped", false, true, "xterm-256color", "", ModePlain},
		{"stderr redirected", true, false, "xterm-256color", "", ModePlain},
		{"neither", false, false, "", "", ModePlain},
		{"a dumb terminal", true, true, "dumb", "", ModeAccessible},
		{"ACCESSIBLE set", true, true, "xterm-256color", "1", ModeAccessible},
		{"ACCESSIBLE without a terminal", false, true, "", "1", ModePlain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ChooseMode(tc.stdin, tc.stderr, tc.termEnv, tc.accessibleEnv))
		})
	}
}

// TestNewUI_NoTerminal: pipes are no terminal, so the UI never asks, and
// --yes answers for it.
func TestNewUI_NoTerminal(t *testing.T) {
	var stderr bytes.Buffer
	ui := NewUI(strings.NewReader("y\n"), &stderr, false)
	assert.False(t, ui.Interactive())
	_, err := ui.Confirm(context.Background(), "Install Ollama?", true)
	require.ErrorIs(t, err, errNoTerminal)

	ui = NewUI(strings.NewReader(""), &stderr, true)
	ok, err := ui.Confirm(context.Background(), "Move ~/.curio aside?", false)
	require.NoError(t, err)
	assert.True(t, ok, "--yes answers every confirmation yes, a default-no one included")
	assert.Contains(t, stderr.String(), "Move ~/.curio aside? yes (--yes)")
}

func TestYesUI(t *testing.T) {
	var out bytes.Buffer
	ui := yesUI{newPlainUI(&out)}
	ctx := context.Background()
	i, err := ui.Ask(ctx, "Use these?", "yes", "no", "choose")
	require.NoError(t, err)
	assert.Zero(t, i)
	i, err = ui.Select(ctx, "Which?", []string{"a", "b", "c"}, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, i)
	s, err := ui.Input(ctx, "Path?", "/tmp/x")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/x", s)
	assert.Equal(t, "Use these? yes (--yes)\nWhich? b (--yes)\nPath? /tmp/x (--yes)\n", out.String())
}

// TestYesUI_Cancelled: --yes answers nothing once the run is cancelled, so
// ctrl-c stops a run between prompts it would have answered itself.
func TestYesUI_Cancelled(t *testing.T) {
	var out bytes.Buffer
	ui := yesUI{newPlainUI(&out)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prompts := map[string]func() error{
		"confirm": func() error { _, err := ui.Confirm(ctx, "Install Ollama?", true); return err },
		"ask":     func() error { _, err := ui.Ask(ctx, "Use these?", "yes", "no"); return err },
		"select":  func() error { _, err := ui.Select(ctx, "Which?", []string{"a", "b"}, 0); return err },
		"input":   func() error { _, err := ui.Input(ctx, "Path?", "/tmp"); return err },
	}
	for name, prompt := range prompts {
		require.ErrorIs(t, prompt(), context.Canceled, name)
	}
	assert.Empty(t, out.String(), "nothing answered")
}

func TestLineUI_Confirm(t *testing.T) {
	cases := []struct {
		input string
		def   bool
		want  bool
	}{
		{"y\n", false, true},
		{"YES\n", false, true},
		{"n\n", true, false},
		{"\n", true, true},
		{"\n", false, false},
		{"maybe\nno\n", true, false},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		ui := newLineUI(strings.NewReader(tc.input), &out)
		got, err := ui.Confirm(context.Background(), "Install Ollama?", tc.def)
		require.NoError(t, err, "%q", tc.input)
		assert.Equal(t, tc.want, got, "%q", tc.input)
	}

	var out bytes.Buffer
	ui := newLineUI(strings.NewReader("maybe\ny\n"), &out)
	_, err := ui.Confirm(context.Background(), "Install Ollama?", true)
	require.NoError(t, err)
	assert.Equal(t, "Install Ollama? [Y/n] Answer y or n.\nInstall Ollama? [Y/n] ", out.String())
}

// TestLineUI_EndOfInputAborts: input that ends before an answer aborts
// the prompt; it is never taken for the default, a yes to `brew install`
// included, and a select without an answer doesn't panic.
func TestLineUI_EndOfInputAborts(t *testing.T) {
	ctx := context.Background()
	prompts := map[string]func(ui *lineUI) error{
		"confirm": func(ui *lineUI) error { _, err := ui.Confirm(ctx, "Install Ollama?", true); return err },
		"ask":     func(ui *lineUI) error { _, err := ui.Ask(ctx, "Use these?", "yes", "no", "choose"); return err },
		"select":  func(ui *lineUI) error { _, err := ui.Select(ctx, "Which?", []string{"a", "b"}, 0); return err },
		"input":   func(ui *lineUI) error { _, err := ui.Input(ctx, "Path?", "/tmp"); return err },
	}
	for name, prompt := range prompts {
		t.Run(name, func(t *testing.T) {
			ui := newLineUI(strings.NewReader(""), io.Discard)
			require.ErrorIs(t, prompt(ui), ErrAborted)
		})
	}
}

// TestLineUI_CancelWhileReading: a context cancelled while the prompt
// waits on input that never comes returns at once.
func TestLineUI_CancelWhileReading(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	ui := newLineUI(r, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ui.Confirm(ctx, "Install Ollama?", true)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt didn't return once its context was cancelled")
	}
}

// TestLineUI_ReadsOnlyWhileAsking: between prompts the input is left to
// the commands curio runs on the same terminal: a line typed for one
// reaches it, and the next prompt reads the line after.
func TestLineUI_ReadsOnlyWhileAsking(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	ui := newLineUI(r, io.Discard)
	write := func(s string) {
		go func() { _, _ = w.Write([]byte(s)) }() // a pipe write waits for its reader
	}

	write("y\n")
	ok, err := ui.Confirm(context.Background(), "Install yt-dlp?", false)
	require.NoError(t, err)
	require.True(t, ok)

	write("for brew\n")
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := r.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case line := <-got:
		assert.Equal(t, "for brew\n", line, "the command's reader gets the line")
	case <-time.After(2 * time.Second):
		t.Fatal("the line typed for the command never reached it")
	}

	write("n\n")
	ok, err = ui.Confirm(context.Background(), "Check again?", true)
	require.NoError(t, err)
	assert.False(t, ok, "the next prompt reads the line after")
}

// TestLineUI_CancelledReadGoesToTheNextPrompt: the read a cancelled prompt
// left waiting delivers its line to the next prompt, not to nobody.
func TestLineUI_CancelledReadGoesToTheNextPrompt(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	ui := newLineUI(r, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ui.read(ctx)
	require.ErrorIs(t, err, context.Canceled)

	go func() { _, _ = w.Write([]byte("n\n")) }()
	ok, err := ui.Confirm(context.Background(), "Install Ollama?", true)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestLineUI_CancelledBefore(t *testing.T) {
	var out bytes.Buffer
	ui := newLineUI(strings.NewReader("y\n"), &out)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ui.Confirm(ctx, "Install Ollama?", true)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, out.String(), "nothing asked")
}

func TestLineUI_Ask(t *testing.T) {
	cases := map[string]int{"\n": 0, "y\n": 0, "Yes\n": 0, "n\n": 1, "c\n": 2, "choose\n": 2, "what\nch\n": 2}
	for input, want := range cases {
		var out bytes.Buffer
		ui := newLineUI(strings.NewReader(input), &out)
		got, err := ui.Ask(context.Background(), "Use these?", "yes", "no", "choose")
		require.NoError(t, err, "%q", input)
		assert.Equal(t, want, got, "%q", input)
		assert.True(t, strings.HasPrefix(out.String(), "Use these? [Y/n/choose] "), out.String())
	}
}

func TestLineUI_Select(t *testing.T) {
	var out bytes.Buffer
	ui := newLineUI(strings.NewReader("9\n3\n"), &out)
	got, err := ui.Select(context.Background(), "Which writing model?", []string{"a", "b", "c"}, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, got)
	assert.Equal(t, "Which writing model?\n  1. a\n  2. b\n  3. c\nEnter a number [2]: "+
		"Enter a number from 1 to 3.\nEnter a number [2]: ", out.String())

	ui = newLineUI(strings.NewReader("\n"), io.Discard)
	got, err = ui.Select(context.Background(), "Which?", []string{"a", "b", "c"}, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, got, "the default")
}

func TestLineUI_Input(t *testing.T) {
	ui := newLineUI(strings.NewReader("\n~/Downloads/b.html\n"), io.Discard)
	got, err := ui.Input(context.Background(), "Path?", "/tmp/a.html")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/a.html", got)
	got, err = ui.Input(context.Background(), "Path?", "/tmp/a.html")
	require.NoError(t, err)
	assert.Equal(t, "~/Downloads/b.html", got)
}

// TestLineUI_WritesNothingToStdout: an accessible prompt goes to the
// UI's writer, stderr, never to stdout, which carries the plan and the
// status.
func TestLineUI_WritesNothingToStdout(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = w
	var stderr bytes.Buffer
	ui := newLineUI(strings.NewReader("y\n"), &stderr)
	_, confirmErr := ui.Confirm(context.Background(), "Install Ollama?", true)
	ui.Info("installing")
	os.Stdout = stdout
	require.NoError(t, w.Close())
	written, err := io.ReadAll(r)
	require.NoError(t, err)

	require.NoError(t, confirmErr)
	assert.Empty(t, written)
	assert.Equal(t, "Install Ollama? [Y/n] installing\n", stderr.String())
}

func TestMatchAnswer(t *testing.T) {
	answers := []string{"yes", "no", "choose"}
	cases := map[string]int{"y": 0, "YES": 0, "n": 1, "no": 1, "c": 2, "cho": 2, "x": -1, "yes please": -1}
	for in, want := range cases {
		assert.Equal(t, want, matchAnswer(in, answers), in)
	}
	assert.Equal(t, -1, matchAnswer("c", []string{"choose", "cancel"}), "ambiguous")
	assert.Equal(t, "[Y/n/choose]", askChoices(answers))
	assert.Equal(t, "[N/y]", askChoices([]string{"no", "yes"}))
}

// TestHuhUI_CancelledContext: a context already done runs no form.
func TestHuhUI_CancelledContext(t *testing.T) {
	var out bytes.Buffer
	ui := newHuhUI(&out)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ui.Confirm(ctx, "Install Ollama?", true)
	require.ErrorIs(t, err, context.Canceled)
	_, err = ui.Select(ctx, "Which?", []string{"a"}, 0)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, out.String())
}
