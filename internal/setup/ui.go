package setup

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/term"
)

// UI is how `curio up` talks to the user while it works. Everything it
// writes, prompts, progress and the output of the commands it runs, goes
// to stderr, whatever the mode; results (the plan, the status, what to do
// next) are the CLI's, on stdout. A prompt left without an answer (end of
// input, ctrl-c, a cancelled context) returns ErrAborted or the context's
// error, never the default.
type UI interface {
	// Interactive reports whether the UI can ask: a terminal on stdin and
	// stderr.
	Interactive() bool
	// Confirm asks a yes-or-no question, def by default.
	Confirm(ctx context.Context, prompt string, def bool) (bool, error)
	// Ask asks a question whose answers are short words, the first the
	// default, and returns the index of the one given: `Use these?
	// [Y/n/choose]`.
	Ask(ctx context.Context, prompt string, answers ...string) (int, error)
	// Select asks for one of options, def by default, and returns its
	// index.
	Select(ctx context.Context, prompt string, options []string, def int) (int, error)
	// Input asks for a line of text, def by default.
	Input(ctx context.Context, prompt, def string) (string, error)
	// Progress starts reporting the progress of one piece of work.
	Progress(title string) Progress
	// Info and Warn say one line.
	Info(msg string)
	Warn(msg string)
	// Output is where the commands curio runs write, live.
	Output() io.Writer
}

// Progress reports how far one piece of work is.
type Progress interface {
	// Update reports completed out of total. completed never goes
	// backwards; total may grow as the work learns its size.
	Update(completed, total int64)
	// Done reports the work finished: the report ends at 100%.
	Done()
	// Stop ends the report where it stands, the work unfinished.
	Stop()
}

// Mode is how a UI talks.
type Mode int

const (
	// ModePlain has no terminal: it never prompts, and says one line per
	// 10% of progress.
	ModePlain Mode = iota
	// ModeTerminal is the full-screen prompt (huh) and a progress line
	// redrawn in place.
	ModeTerminal
	// ModeAccessible is a terminal read a line at a time, with no
	// redrawing, for screen readers and dumb terminals.
	ModeAccessible
)

// ChooseMode picks the UI's mode: a UI is interactive only when stdin and
// stderr are both terminals, and accessible when TERM is dumb or
// ACCESSIBLE is set. It is decided before any prompt library runs, since
// the full-screen one fails without a controlling terminal.
func ChooseMode(stdinTerminal, stderrTerminal bool, termEnv, accessibleEnv string) Mode {
	switch {
	case !stdinTerminal || !stderrTerminal:
		return ModePlain
	case termEnv == "dumb" || accessibleEnv != "":
		return ModeAccessible
	default:
		return ModeTerminal
	}
}

// NewUI returns the UI for stdin and stderr in the mode ChooseMode picks
// from them and the environment. With yes, every prompt takes its default
// and every confirmation is a yes, unasked, in any mode.
func NewUI(stdin io.Reader, stderr io.Writer, yes bool) UI {
	mode := ChooseMode(isTerminal(stdin), isTerminal(stderr), os.Getenv("TERM"), os.Getenv("ACCESSIBLE"))
	var ui UI
	switch mode {
	case ModeTerminal:
		ui = newHuhUI(stderr)
	case ModeAccessible:
		ui = newLineUI(stdin, stderr)
	case ModePlain:
		ui = newPlainUI(stderr)
	}
	if yes {
		ui = yesUI{ui}
	}
	return ui
}

// isTerminal reports whether f is a terminal.
func isTerminal(f any) bool {
	file, ok := f.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

// yesUI answers every prompt of the UI it wraps as --yes does: a yes to
// every confirmation, the default to everything else. It says what it
// answered, so the output shows what was agreed to.
type yesUI struct{ UI }

func (u yesUI) Confirm(_ context.Context, prompt string, _ bool) (bool, error) {
	u.Info(prompt + " yes (--yes)")
	return true, nil
}

func (u yesUI) Ask(_ context.Context, prompt string, answers ...string) (int, error) {
	if len(answers) == 0 {
		return 0, errNoAnswers
	}
	u.Info(prompt + " " + answers[0] + " (--yes)")
	return 0, nil
}

func (u yesUI) Select(_ context.Context, prompt string, options []string, def int) (int, error) {
	if def < 0 || def >= len(options) {
		return 0, errNoAnswers
	}
	u.Info(prompt + " " + options[def] + " (--yes)")
	return def, nil
}

func (u yesUI) Input(_ context.Context, prompt, def string) (string, error) {
	u.Info(prompt + " " + def + " (--yes)")
	return def, nil
}

// errNoAnswers is a prompt without a default to take.
var errNoAnswers = errors.New("a prompt with no default answer")
