// Package setup is `curio up`: the steps that take a Mac from nothing to a
// running curio (the machine, Ollama, the models, the home and its
// config.yaml, the daemon under its launchd agent), and the checks `curio
// doctor` shares with them, so the two agree on what healthy means.
//
// A step checks the world and may apply a fix. Checks are read-only and
// bounded, so `curio up --dry-run` and `curio doctor` change nothing and
// never hang. The runner checks every step first; the fixes it found are
// the plan. Nothing records progress: an interrupted run resumes by
// checking the world again, and a run whose checks all pass changes
// nothing. setup returns data (the plan, a status snapshot); internal/cli
// renders it.
package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samsar/curio/internal/textutil"
)

// Status is how a check found its part of the world.
type Status int

const (
	// OK: nothing to do.
	OK Status = iota
	// Warn: worth knowing, and fixed by the step when it has a Fix.
	Warn
	// Fail: broken; the step's Fix repairs it, and without one it blocks
	// the run until someone does by hand.
	Fail
)

func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case Warn:
		return "warn"
	case Fail:
		return "fail"
	default:
		return fmt.Sprintf("status(%d)", int(s))
	}
}

// Consent is how the runner asks before it applies a fix.
type Consent int

const (
	// Ask asks one yes-or-no question, yes by default.
	Ask Consent = iota
	// AskNo asks one yes-or-no question, no by default: moving a home
	// aside.
	AskNo
	// Announce says what it does and doesn't ask: creating a new home and
	// its config.yaml, which are curio's own files.
	Announce
)

// Fix is what applying a step would do.
type Fix struct {
	// Summary says it in one line: "install Ollama with Homebrew".
	Summary string
	// Commands is the exact argv of every command it runs, in order;
	// empty for a fix that is curio's own work (a pull, a new home).
	Commands [][]string
	Consent  Consent
}

// Result is what a check found.
type Result struct {
	Status Status
	// Detail says what was found, in one line.
	Detail string
	// Fix is what the step would do about it; nil when there is nothing
	// the step can do.
	Fix *Fix
	// Hint is the remedy by hand, for a result the step can't fix, or
	// what to know about one it can.
	Hint string
	// Notes are lines the plan shows under the step: what was decided
	// and why ("keeping generation.model: gemma4:26b from config.yaml").
	Notes []string
	// Question, when set, is asked once before anything is applied, and
	// the run stops on a no: a machine curio runs on only slowly.
	Question string
}

// Blocked reports whether the result stops the run: a failure the step
// can't fix.
func (r Result) Blocked() bool { return r.Status == Fail && r.Fix == nil }

// Pending reports whether the step has something to apply.
func (r Result) Pending() bool { return r.Fix != nil }

// Step is one part of `curio up`.
type Step interface {
	Name() string
	// Check looks at the world, bounded and without changing it.
	Check(ctx context.Context) Result
	// Apply makes the fix Check found, through ui.
	Apply(ctx context.Context, ui UI) error
}

// Confirmer is a step that asks for its own consent in place of the
// runner's question: the models step asks `Use these? [Y/n/choose]`
// when curio picked the models.
type Confirmer interface {
	Confirm(ctx context.Context, ui UI, fix Fix) (bool, error)
}

// Check is one of the checks `curio doctor` runs: a step's, or a part of
// one's.
type Check struct {
	Name string
	Run  func(ctx context.Context) Result
}

// Item is one step's result in a plan.
type Item struct {
	Step   string
	Result Result
}

// Plan is every step's result, in order.
type Plan []Item

// Fixes are the items with something to apply.
func (p Plan) Fixes() []Item { return p.filter(Result.Pending) }

// Blockers are the items that stop the run.
func (p Plan) Blockers() []Item { return p.filter(Result.Blocked) }

// Warnings are the warnings without a fix: worth knowing, nothing to do.
func (p Plan) Warnings() []Item {
	return p.filter(func(r Result) bool { return r.Status == Warn && r.Fix == nil })
}

// Empty reports whether there is nothing to do and nothing in the way.
func (p Plan) Empty() bool { return len(p.Fixes()) == 0 && len(p.Blockers()) == 0 }

func (p Plan) filter(keep func(Result) bool) []Item {
	var out []Item
	for _, it := range p {
		if keep(it.Result) {
			out = append(out, it)
		}
	}
	return out
}

var (
	// ErrAborted: a prompt was left without an answer (end of input,
	// ctrl-c in the full-screen prompt). It is never taken for the
	// default; the CLI exits 130, as for an interrupt.
	ErrAborted = errors.New("aborted")

	// ErrNeedsYes: the plan isn't empty, and there is no terminal to ask
	// on and no --yes to answer for it.
	ErrNeedsYes = errors.New("nothing was changed: there is no terminal to ask on; " +
		"run `curio up --yes` to apply the plan above")
)

// BlockedError is a run the plan's blockers stopped before anything was
// applied; the plan printed says why and what to do by hand.
type BlockedError struct {
	Blockers []Item
}

func (e *BlockedError) Error() string {
	names := make([]string, 0, len(e.Blockers))
	for _, b := range e.Blockers {
		names = append(names, b.Step)
	}
	return fmt.Sprintf("nothing was changed: %s must be fixed by hand first (see above)", strings.Join(names, ", "))
}

// DeclinedError is a run that stopped because the user said no to a
// step: nothing after it ran.
type DeclinedError struct {
	Step string
	// Left is what was left to do, the declined step first.
	Left []Item
}

func (e *DeclinedError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "stopped at %s: declined. Left to do", e.Step)
	for _, it := range e.Left {
		fmt.Fprintf(&b, "\n  %s: %s", it.Step, it.Result.Fix.Summary)
		for _, argv := range it.Result.Fix.Commands {
			fmt.Fprintf(&b, "\n    %s", textutil.ShellJoin(argv))
		}
	}
	if len(e.Left) > 0 && e.Left[0].Step == e.Step && e.Left[0].Result.Hint != "" {
		fmt.Fprintf(&b, "\n%s", e.Left[0].Result.Hint)
	}
	return b.String()
}

// StepError is a step that failed: its Apply returned an error, its check
// found a blocker right before it, or it still failed its check after.
type StepError struct {
	Step string
	Err  error
}

func (e *StepError) Error() string { return e.Step + ": " + e.Err.Error() }
func (e *StepError) Unwrap() error { return e.Err }
