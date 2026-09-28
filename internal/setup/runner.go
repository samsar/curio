package setup

import (
	"context"
	"errors"
	"fmt"

	"github.com/samsar/curio/internal/textutil"
)

// Runner runs `curio up`: it checks every step, and applies the plan.
type Runner struct {
	w     *world
	ui    UI
	steps []Step
}

// New returns the Runner for opts over deps. An --embedding-model without
// a tag is refused: a home's vectors are fixed for its life, and an
// untagged name means :latest, which moves.
func New(opts Options, deps Deps) (*Runner, error) {
	if m := opts.EmbeddingModel; m != "" && !hasTag(m) {
		return nil, fmt.Errorf("--embedding-model %s: name a tag (%s:<tag>): a home embeds with one model for good, "+
			"and an untagged name means :latest, which moves when the library does", m, m)
	}
	w, err := newWorld(opts, deps)
	if err != nil {
		return nil, err
	}
	return &Runner{w: w, ui: deps.UI, steps: steps(w)}, nil
}

// steps is `curio up`, in the order of its design: the machine first,
// before anything is installed; Ollama, which the models need; the
// models, which a new home measures its width with; the home and its
// config.yaml, which the daemon needs; the daemon. The import step goes
// after the daemon.
func steps(w *world) []Step {
	return []Step{&machineStep{w}, &ollamaStep{w: w}, &modelsStep{w}, &homeStep{w}, &daemonStep{w}}
}

// Steps are the steps, in the order they run.
func (r *Runner) Steps() []Step { return append([]Step(nil), r.steps...) }

// Checks are the checks `curio doctor` shares with the steps, in its
// order.
func (r *Runner) Checks() []Check {
	w := r.w
	return []Check{
		{"machine", w.checkMachine},
		{"curio home", w.checkHome},
		{"config", w.checkConfig},
		{"ollama", w.checkOllama},
		{"models", w.checkModels},
		{"daemon", w.checkDaemon},
		{"launchd", w.checkAgent},
		{"embeddings", w.checkDrift},
	}
}

// Plan checks every step, changing nothing.
func (r *Runner) Plan(ctx context.Context) Plan {
	plan := make(Plan, 0, len(r.steps))
	for _, s := range r.steps {
		plan = append(plan, Item{Step: s.Name(), Result: s.Check(ctx)})
	}
	return plan
}

// Outcome is how a run ended.
type Outcome struct {
	// Plan is what the checks found before anything was applied.
	Plan Plan
	// DryRun: the plan was shown and nothing applied.
	DryRun bool
	// Changed: a step was applied.
	Changed bool
	// Status is curio's state at the end, for a run that got there.
	Status Snapshot
}

// Run plans, and applies the plan. With nothing to do it changes nothing
// and returns the status. Otherwise it shows the plan, and stops before
// applying anything when the plan has a blocker (BlockedError), for a dry
// run, and without a terminal to ask on or --yes (ErrNeedsYes). Then it
// asks the machine's question, if any, and applies the steps in order,
// each checked again right before it and after: a step that still fails
// after its Apply fails the run (StepError), and nothing after a failed
// or declined step (DeclinedError) runs.
func (r *Runner) Run(ctx context.Context, show func(Plan)) (Outcome, error) {
	if m := r.w.opts.GenerationModel; m != "" && !hasTag(m) {
		r.ui.Warn(fmt.Sprintf("--generation-model %s has no tag, so it means %s:latest, which moves when the library does", m, m))
	}
	plan := r.Plan(ctx)
	out := Outcome{Plan: plan}
	if plan.Empty() {
		out.Status = r.w.snapshot(ctx, plan)
		return out, nil
	}
	show(plan)
	switch {
	case len(plan.Blockers()) > 0:
		return out, &BlockedError{Blockers: plan.Blockers()}
	case r.w.opts.DryRun:
		out.DryRun = true
		return out, nil
	case !r.ui.Interactive() && !r.w.opts.Yes:
		return out, ErrNeedsYes
	}
	if err := r.askQuestions(ctx, plan); err != nil {
		return out, err
	}
	final := make(Plan, len(plan))
	for i, s := range r.steps {
		res, applied, err := r.applyStep(ctx, s, plan[i:])
		if err != nil {
			return out, err
		}
		final[i] = Item{Step: s.Name(), Result: res}
		out.Changed = out.Changed || applied
	}
	out.Status = r.w.snapshot(ctx, final)
	return out, nil
}

// askQuestions asks the plan's questions, a degraded machine's whether to
// go on, once each before anything is applied. A no stops the run.
func (r *Runner) askQuestions(ctx context.Context, plan Plan) error {
	for _, it := range plan {
		if it.Result.Question == "" {
			continue
		}
		for _, reason := range append([]string{it.Result.Detail}, it.Result.Notes...) {
			r.ui.Info(it.Step + ": " + reason)
		}
		goOn, err := r.ui.Confirm(ctx, it.Result.Question, true)
		if err != nil {
			return err
		}
		if !goOn {
			return &DeclinedError{Step: it.Step, Left: plan.Fixes()}
		}
	}
	return nil
}

// applyStep checks s again, and when it still has something to do, asks
// for consent, applies it and checks it again. left is the plan from s on,
// for what a decline leaves to do. applied says whether it applied a fix;
// res is the check it ended on.
func (r *Runner) applyStep(ctx context.Context, s Step, left Plan) (res Result, applied bool, err error) {
	res = s.Check(ctx)
	switch {
	case res.Blocked():
		return res, false, &StepError{Step: s.Name(), Err: blockedError(res)}
	case !res.Pending():
		return res, false, nil
	}
	ok, err := r.confirm(ctx, s, *res.Fix)
	if err != nil {
		return res, false, err
	}
	if !ok {
		declined := append(Plan{{Step: s.Name(), Result: res}}, left[1:].Fixes()...)
		return res, false, &DeclinedError{Step: s.Name(), Left: declined}
	}
	if err := s.Apply(ctx, r.ui); err != nil {
		return res, true, &StepError{Step: s.Name(), Err: err}
	}
	after := s.Check(ctx)
	if after.Status == Fail || after.Pending() {
		return after, true, &StepError{Step: s.Name(), Err: fmt.Errorf("still not done after applying it: %s", after.Detail)}
	}
	return after, true, nil
}

// blockedError is a blocker found right before its step ran.
func blockedError(res Result) error {
	if res.Hint == "" {
		return errors.New(res.Detail)
	}
	return fmt.Errorf("%s (%s)", res.Detail, res.Hint)
}

// confirm asks for consent to apply fix: the step's own question, or the
// fix's summary and every command it runs, then one question, whose
// default the fix's Consent sets; an announced fix is only said.
func (r *Runner) confirm(ctx context.Context, s Step, fix Fix) (bool, error) {
	if c, ok := s.(Confirmer); ok {
		return c.Confirm(ctx, r.ui, fix)
	}
	if fix.Consent == Announce {
		r.ui.Info(s.Name() + ": " + fix.Summary)
		return true, nil
	}
	for _, argv := range fix.Commands {
		r.ui.Info("  $ " + textutil.ShellJoin(argv))
	}
	return r.ui.Confirm(ctx, capitalize(fix.Summary)+"?", fix.Consent != AskNo)
}

// machineStep judges the machine before anything is installed. It never
// has a fix: a verdict is a warning, a question, or fine.
type machineStep struct{ w *world }

func (*machineStep) Name() string { return "machine" }

func (s *machineStep) Check(ctx context.Context) Result { return s.w.checkMachine(ctx) }

func (*machineStep) Apply(context.Context, UI) error { return nil }

// checkMachine is the machine check: its facts and Assess's verdict.
func (w *world) checkMachine(ctx context.Context) Result {
	m, err := w.probeMachine(ctx)
	if err != nil {
		return Result{Status: Warn, Detail: "couldn't probe this Mac: " + err.Error()}
	}
	v := Assess(m)
	res := Result{Status: OK, Detail: m.String(), Notes: v.Reasons, Question: v.Question}
	if v.Level != Supported {
		res.Status = Warn
	}
	if len(v.Reasons) > 0 {
		res.Hint = v.Reasons[0]
	}
	return res
}
