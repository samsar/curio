// Package setuptest has test doubles for the setup wizard (internal/setup)
// and the commands built on it: a scripted UI, a fake Installer, a fake
// Probe, and a fake Ollama. With them no test runs brew, open or ollama,
// or reads the machine it runs on, so a test gives the same result on
// every CI runner. It is test support only: depguard keeps production
// code from importing it.
package setuptest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/samsar/curio/internal/setup"
)

// Answer is one scripted answer to a prompt.
type Answer struct {
	// Want, when set, is a substring the prompt must contain.
	Want    string
	confirm bool
	index   int
	text    string
	err     error
}

// Yes and No answer a Confirm.
func Yes() Answer { return Answer{confirm: true} }
func No() Answer  { return Answer{} }

// Pick answers an Ask or a Select with the answer or option at i.
func Pick(i int) Answer { return Answer{index: i} }

// Type answers an Input.
func Type(s string) Answer { return Answer{text: s} }

// Abort leaves the prompt without an answer, as the end of input or
// ctrl-c does.
func Abort() Answer { return Answer{err: setup.ErrAborted} }

// About requires the prompt answered to contain want.
func (a Answer) About(want string) Answer {
	a.Want = want
	return a
}

// Event is something the UI was told or asked, in order.
type Event struct {
	Kind string // "info", "warn", "prompt", "progress"
	Text string
}

// UI is a setup.UI that answers prompts from a script and records
// everything it is told. A prompt the script has no answer for fails the
// test and aborts; command output goes to Output, which the test reads.
type UI struct {
	t           testing.TB
	interactive bool

	mu       sync.Mutex
	answers  []Answer
	events   []Event
	output   bytes.Buffer
	progress []*Progress
}

// NewUI returns an interactive UI answering prompts with answers, in
// order.
func NewUI(t testing.TB, answers ...Answer) *UI {
	return &UI{t: t, interactive: true, answers: answers}
}

// NonInteractive makes the UI one with no terminal to ask on.
func (u *UI) NonInteractive() *UI {
	u.interactive = false
	return u
}

var _ setup.UI = (*UI)(nil)

func (u *UI) Interactive() bool { return u.interactive }

// next pops the answer for a prompt, failing the test when there is none
// or it doesn't fit.
func (u *UI) next(prompt string) Answer {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.events = append(u.events, Event{Kind: "prompt", Text: prompt})
	if len(u.answers) == 0 {
		u.t.Errorf("setuptest: unexpected prompt %q", prompt)
		return Abort()
	}
	a := u.answers[0]
	u.answers = u.answers[1:]
	if a.Want != "" && !strings.Contains(prompt, a.Want) {
		u.t.Errorf("setuptest: prompt %q, want one about %q", prompt, a.Want)
	}
	return a
}

func (u *UI) Confirm(ctx context.Context, prompt string, _ bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	a := u.next(prompt)
	return a.confirm, a.err
}

func (u *UI) Ask(ctx context.Context, prompt string, answers ...string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	a := u.next(fmt.Sprintf("%s %v", prompt, answers))
	return a.index, a.err
}

func (u *UI) Select(ctx context.Context, prompt string, options []string, _ int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	a := u.next(prompt + "\n" + strings.Join(options, "\n"))
	return a.index, a.err
}

func (u *UI) Input(ctx context.Context, prompt, _ string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a := u.next(prompt)
	return a.text, a.err
}

func (u *UI) Info(msg string) { u.record("info", msg) }
func (u *UI) Warn(msg string) { u.record("warn", msg) }

func (u *UI) record(kind, text string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.events = append(u.events, Event{Kind: kind, Text: text})
}

// Output is where commands write; Output's text is Written.
func (u *UI) Output() io.Writer { return syncWriter{u} }

// Written is what commands wrote.
func (u *UI) Written() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.output.String()
}

type syncWriter struct{ u *UI }

func (w syncWriter) Write(p []byte) (int, error) {
	w.u.mu.Lock()
	defer w.u.mu.Unlock()
	return w.u.output.Write(p)
}

// Events is everything the UI was told or asked, in order.
func (u *UI) Events() []Event {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.events)
}

// Lines is the text of the events of kind, in order.
func (u *UI) Lines(kind string) []string {
	var out []string
	for _, e := range u.Events() {
		if e.Kind == kind {
			out = append(out, e.Text)
		}
	}
	return out
}

// Said reports whether an info or warn line contains s.
func (u *UI) Said(s string) bool {
	for _, e := range u.Events() {
		if (e.Kind == "info" || e.Kind == "warn") && strings.Contains(e.Text, s) {
			return true
		}
	}
	return false
}

// Unanswered is how many scripted answers are left.
func (u *UI) Unanswered() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.answers)
}

// Progress starts recording a progress report.
func (u *UI) Progress(title string) setup.Progress {
	u.mu.Lock()
	defer u.mu.Unlock()
	p := &Progress{Title: title}
	u.progress = append(u.progress, p)
	u.events = append(u.events, Event{Kind: "progress", Text: title})
	return p
}

// Reports are the progress reports started, in order.
func (u *UI) Reports() []*Progress {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.progress)
}

// Progress records the updates of one report.
type Progress struct {
	Title string

	mu      sync.Mutex
	updates [][2]int64
	done    bool
	stopped bool
}

func (p *Progress) Update(completed, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updates = append(p.updates, [2]int64{completed, total})
}

func (p *Progress) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = true
}

func (p *Progress) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
}

// Updates are the (completed, total) pairs reported, in order.
func (p *Progress) Updates() [][2]int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.updates)
}

// Finished reports whether the work was reported done, or stopped.
func (p *Progress) Finished() (done, stopped bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done, p.stopped
}
