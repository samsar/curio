package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/huh/v2"
)

// huhUI is the full-screen terminal UI: each prompt a huh form on stderr,
// progress a line redrawn in place. huh reports ctrl-c as ErrUserAborted
// while the context stays live, and a killed program as ErrTimeout; both
// abort the prompt, as a context cancelled around it does.
type huhUI struct{ lineWriter }

func newHuhUI(out io.Writer) *huhUI { return &huhUI{lineWriter{out}} }

func (*huhUI) Interactive() bool { return true }

func (u *huhUI) Progress(title string) Progress { return newProgress(u.out, title, true, time.Now) }

func (u *huhUI) Confirm(ctx context.Context, prompt string, def bool) (bool, error) {
	v := def
	if err := u.run(ctx, prompt, huh.NewConfirm().Title(prompt).Value(&v)); err != nil {
		return false, err
	}
	answer := "no"
	if v {
		answer = "yes"
	}
	u.said(prompt, answer)
	return v, nil
}

func (u *huhUI) Ask(ctx context.Context, prompt string, answers ...string) (int, error) {
	if len(answers) == 0 {
		return 0, errNoAnswers
	}
	opts := make([]huh.Option[int], len(answers))
	for i, a := range answers {
		opts[i] = huh.NewOption(capitalize(a), i)
	}
	v := 0
	if err := u.run(ctx, prompt, huh.NewSelect[int]().Title(prompt).Options(opts...).Value(&v)); err != nil {
		return 0, err
	}
	u.said(prompt, answers[v])
	return v, nil
}

func (u *huhUI) Select(ctx context.Context, prompt string, options []string, def int) (int, error) {
	if def < 0 || def >= len(options) {
		return 0, errNoAnswers
	}
	opts := make([]huh.Option[int], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, i)
	}
	// A Select always has a value: huh panics on one without, at EOF.
	v := def
	if err := u.run(ctx, prompt, huh.NewSelect[int]().Title(prompt).Options(opts...).Value(&v)); err != nil {
		return 0, err
	}
	u.said(prompt, options[v])
	return v, nil
}

func (u *huhUI) Input(ctx context.Context, prompt, def string) (string, error) {
	v := def
	if err := u.run(ctx, prompt, huh.NewInput().Title(prompt).Value(&v)); err != nil {
		return "", err
	}
	u.said(prompt, v)
	return v, nil
}

// run runs one field, titled title, as a form on stderr. A context
// already done runs nothing; one done by the time the form returns wins
// over what it returned.
func (u *huhUI) run(ctx context.Context, title string, field huh.Field) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := huh.NewForm(huh.NewGroup(field)).WithOutput(u.out).WithShowHelp(false).RunWithContext(ctx)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, huh.ErrUserAborted), errors.Is(err, huh.ErrTimeout):
		return ErrAborted
	case err != nil:
		return fmt.Errorf("prompt %q: %w", title, err)
	}
	return nil
}

// said leaves the question and its answer on the terminal, which huh
// clears when the form ends.
func (u *huhUI) said(prompt, answer string) { u.Info(prompt + " " + answer) }

// capitalize is s with its first letter upper-cased: "choose" as "Choose".
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
