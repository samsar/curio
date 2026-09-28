package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// errNoTerminal is a prompt in a UI that has no terminal to ask on; the
// runner stops before it would ask (ErrNeedsYes).
var errNoTerminal = errors.New("no terminal to ask on")

// lineWriter is what every UI says with: whole lines on out, stderr.
type lineWriter struct{ out io.Writer }

func (w lineWriter) Info(msg string)   { fmt.Fprintln(w.out, msg) }
func (w lineWriter) Warn(msg string)   { fmt.Fprintln(w.out, "warning: "+msg) }
func (w lineWriter) Output() io.Writer { return w.out }

// plainUI is the UI without a terminal: it says what happens and never
// asks.
type plainUI struct{ lineWriter }

func newPlainUI(out io.Writer) plainUI { return plainUI{lineWriter{out}} }

func (plainUI) Interactive() bool { return false }

func (plainUI) Confirm(context.Context, string, bool) (bool, error) { return false, errNoTerminal }
func (plainUI) Ask(context.Context, string, ...string) (int, error) { return 0, errNoTerminal }
func (plainUI) Select(context.Context, string, []string, int) (int, error) {
	return 0, errNoTerminal
}
func (plainUI) Input(context.Context, string, string) (string, error) { return "", errNoTerminal }

func (u plainUI) Progress(title string) Progress { return newProgress(u.out, title, false, time.Now) }

// lineUI is the accessible UI: a prompt is a line, the answer a line of
// input, and nothing is redrawn. The end of input, or a context cancelled
// while it waits, aborts the prompt; it never answers for the user.
type lineUI struct {
	lineWriter
	in    io.Reader
	once  sync.Once
	lines chan string // closed when the input ends
}

func newLineUI(in io.Reader, out io.Writer) *lineUI {
	return &lineUI{lineWriter: lineWriter{out}, in: in}
}

func (*lineUI) Interactive() bool { return true }

func (u *lineUI) Progress(title string) Progress { return newProgress(u.out, title, false, time.Now) }

// read waits for the next line of input. One goroutine reads the input
// for the UI's life, so a prompt can give up on a cancelled context while
// a read it can't interrupt goes on. The input's end, whatever caused it,
// is ErrAborted.
func (u *lineUI) read(ctx context.Context) (string, error) {
	u.once.Do(func() {
		u.lines = make(chan string)
		go func() {
			defer close(u.lines)
			for sc := bufio.NewScanner(u.in); sc.Scan(); {
				u.lines <- sc.Text()
			}
		}()
	})
	select {
	case <-ctx.Done():
		fmt.Fprintln(u.out)
		return "", ctx.Err()
	case line, ok := <-u.lines:
		if !ok {
			fmt.Fprintln(u.out)
			return "", ErrAborted
		}
		return strings.TrimSpace(line), nil
	}
}

// prompt writes text and reads the answer; a cancelled context is
// reported before anything is written.
func (u *lineUI) prompt(ctx context.Context, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fmt.Fprint(u.out, text)
	return u.read(ctx)
}

func (u *lineUI) Confirm(ctx context.Context, prompt string, def bool) (bool, error) {
	choices := "[y/N]"
	if def {
		choices = "[Y/n]"
	}
	for {
		answer, err := u.prompt(ctx, prompt+" "+choices+" ")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		u.Info("Answer y or n.")
	}
}

func (u *lineUI) Ask(ctx context.Context, prompt string, answers ...string) (int, error) {
	if len(answers) == 0 {
		return 0, errNoAnswers
	}
	for {
		answer, err := u.prompt(ctx, prompt+" "+askChoices(answers)+" ")
		if err != nil {
			return 0, err
		}
		if answer == "" {
			return 0, nil
		}
		if i := matchAnswer(answer, answers); i >= 0 {
			return i, nil
		}
		u.Info("Answer " + askChoices(answers) + ".")
	}
}

func (u *lineUI) Select(ctx context.Context, prompt string, options []string, def int) (int, error) {
	if def < 0 || def >= len(options) {
		return 0, errNoAnswers
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	u.Info(prompt)
	for i, o := range options {
		u.Info(fmt.Sprintf("  %d. %s", i+1, o))
	}
	for {
		answer, err := u.prompt(ctx, fmt.Sprintf("Enter a number [%d]: ", def+1))
		if err != nil {
			return 0, err
		}
		if answer == "" {
			return def, nil
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		u.Info(fmt.Sprintf("Enter a number from 1 to %d.", len(options)))
	}
}

func (u *lineUI) Input(ctx context.Context, prompt, def string) (string, error) {
	text := prompt + " "
	if def != "" {
		text = fmt.Sprintf("%s [%s] ", prompt, def)
	}
	answer, err := u.prompt(ctx, text)
	if err != nil {
		return "", err
	}
	if answer == "" {
		return def, nil
	}
	return answer, nil
}

// askChoices shows answers the way a terminal prompt does, the default
// first and capitalized, yes and no by their initials: "[Y/n/choose]".
func askChoices(answers []string) string {
	shown := make([]string, len(answers))
	for i, a := range answers {
		shown[i] = shortAnswer(a)
	}
	if first, size := utf8.DecodeRuneInString(shown[0]); size > 0 {
		shown[0] = string(unicode.ToUpper(first)) + shown[0][size:]
	}
	return "[" + strings.Join(shown, "/") + "]"
}

// shortAnswer is how an answer is shown: yes and no by their initials.
func shortAnswer(a string) string {
	switch a {
	case "yes", "no":
		return a[:1]
	default:
		return a
	}
}

// matchAnswer is the index of the one answer in answers that in names, as
// a word or the start of one ("y", "yes", "c" for "choose"), or -1 when
// none or more than one does.
func matchAnswer(in string, answers []string) int {
	in = strings.ToLower(in)
	found := -1
	for i, a := range answers {
		if in == shortAnswer(a) || strings.HasPrefix(a, in) {
			if found >= 0 {
				return -1
			}
			found = i
		}
	}
	return found
}
