package understand

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/modelgate"
)

// OffReason is why a search wasn't understood while search.understand is
// off: the planner's, and the API's when the daemon built none.
const OffReason = "search understanding is off (search.understand: false)"

// The planner's constants, measured in review on Ollama 0.40.4 with
// gemma4:26b and on the owner's library (decisions.md "Understanding the
// question"). Readiness, holds and the fitness check's bookkeeping are the
// gate's (internal/modelgate).
const (
	// maxPlanTokens caps a plan's answer: the median plan is 46 tokens, and
	// a rambling model is cut off well before it costs seconds.
	maxPlanTokens = 256
	// probeTimeout bounds each question of the fitness check, run in the
	// background on a loaded model: a warm plan takes 1 to 4 s.
	probeTimeout = 30 * time.Second
	// maxNameBytes and maxRewriteBytes cap a plan's texts, once trimmed
	// (the counts are the schema's): one longer is a model rambling, not
	// a search.
	maxNameBytes    = 100
	maxRewriteBytes = 160
	// maxQuestionBytes is the longest question planned; a longer one is a
	// paste, not a question, and is searched by its words without asking
	// the model (the graded questions are at most 130 bytes, 23 words).
	maxQuestionBytes = 512
	// earliestSaveYear starts the year no saved window may end by:
	// nothing was bookmarked before it.
	earliestSaveYear = 1990
)

// gateWords are how the gate's records and reasons name the planner.
var gateWords = modelgate.Words{Feature: "search understanding", Role: "planner", Calls: "plans",
	Resumes: "understanding", Fallback: "searches use their words", UnfitFix: UnfitFix}

// Planner understands questions with the planner role's model. Before any
// plan is used the model must pass a fitness check, run once a process in
// the background, as is loading the model when a question finds it cold:
// the question is then searched by its own words at once, and a later one
// gets a plan. A model that keeps timing out, or that Ollama keeps
// evicting, is given a rest instead of every question's cost. It is safe
// for concurrent use.
type Planner struct {
	gen       generator.JSONGenerator // nil when the planner is off
	gate      *modelgate.Gate
	offReason string
	withheld  string
	on        bool
	model     string
	timeout   time.Duration
	now       func() time.Time
	loc       *time.Location
	log       *slog.Logger

	mu     sync.Mutex
	last   Status // the last question's status
	lastAt time.Time
}

// New builds a planner over gen, whose background work runs on lifetime,
// the daemon's context; Wait returns once it has stopped after lifetime
// ends. With opts.On false, or a gen that can't answer under a JSON schema
// (it isn't a generator.JSONGenerator), every plan is StatusOff: the
// planner never falls back to parsing prose. The latter is logged once, at
// ERROR. With opts.Withheld set, every plan is StatusUnavailable and no
// question reaches the model, logged once, at WARN.
func New(lifetime context.Context, gen generator.Generator, opts Options) *Planner {
	p := &Planner{
		on:       opts.On,
		model:    opts.Model.Model,
		timeout:  opts.Timeout,
		withheld: opts.Withheld,
		now:      opts.Now,
		loc:      opts.Location,
		log:      opts.Log,
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.loc == nil {
		p.loc = time.Local
	}
	if p.log == nil {
		p.log = slog.Default()
	}
	p.gate = modelgate.New(lifetime, gen, modelgate.Options{Words: gateWords, Model: p.model, KeepAlive: opts.KeepAlive,
		Shared: opts.Shared, ShortSharer: opts.ShortSharer, Using: opts.Using, Now: p.now, Location: p.loc, Log: p.log, Check: p.runCheck})
	if !opts.On {
		p.offReason = OffReason
		return p
	}
	jg, ok := gen.(generator.JSONGenerator)
	if !ok {
		p.offReason = opts.Model.Provider + " has no structured output"
		p.log.Error("search understanding is off: the planner's model can't answer in JSON",
			"provider", opts.Model.Provider, "model", p.model)
		return p
	}
	if p.withheld != "" {
		p.log.Warn("search understanding: no question is sent to the planner's model; searches use their words",
			"model", p.model, "reason", p.withheld)
	}
	if opts.Shortened != "" {
		p.log.Warn("search understanding: plans get less time than configured", "model", p.model,
			"timeout", p.timeout, "reason", opts.Shortened)
	}
	p.gen = jg
	return p
}

// Wait returns once the planner's background work has stopped, after the
// lifetime context New was given has ended.
func (p *Planner) Wait() { p.gate.Wait() }

// Timeout is how long a plan may take before the question is searched by
// its words: search.understand_timeout_seconds.
func (p *Planner) Timeout() time.Duration { return p.timeout }

// Plan understands question. Only the caller's context ending is an error,
// and it is returned as is, nothing logged; every other way a question
// goes without a plan is an Outcome whose Status says why, and the caller
// searches the question's own words. Each question is logged at DEBUG,
// its status and timing, never its text.
func (p *Planner) Plan(ctx context.Context, question string) (Outcome, error) {
	out, err := p.timed(ctx, func() (Outcome, error) {
		if out, refused := p.refuse(question); refused {
			return out, nil
		}
		if r, ready := p.gate.Ready(ctx); !ready {
			if err := ctx.Err(); err != nil {
				return Outcome{}, err
			}
			return refused(r), nil
		}
		out, err := p.ask(ctx, question)
		if err == nil {
			p.settle(out)
		}
		return out, err
	})
	if err != nil {
		return Outcome{}, err
	}
	p.mu.Lock()
	p.last, p.lastAt = out.Status, p.now()
	p.mu.Unlock()
	p.log.Debug("search understanding: a question", "status", out.Status, "took_ms", out.Took.Milliseconds(),
		"model", out.Model, "prompt_version", PromptVersion)
	return out, nil
}

// Ask plans question now, whatever the fitness check found, whether the
// model is loaded and whatever holds planning: for measuring a model, as
// cmd/searcheval does, never for a search, which goes through Plan. Its
// outcome is Plan's once the model is loaded and fit.
func (p *Planner) Ask(ctx context.Context, question string) (Outcome, error) {
	return p.timed(ctx, func() (Outcome, error) {
		if out, refused := p.refuse(question); refused {
			return out, nil
		}
		return p.ask(ctx, question)
	})
}

// refuse is the Outcome of a question no model is asked about, whatever
// state the model is in: the planner is off, its questions are withheld,
// or the question is longer than it takes.
func (p *Planner) refuse(question string) (Outcome, bool) {
	switch {
	case p.gen == nil:
		return p.off(), true
	case p.withheld != "":
		return Outcome{Status: StatusUnavailable, Reason: p.withheld}, true
	case len(question) > maxQuestionBytes:
		return Outcome{Status: StatusTooLong, Reason: fmt.Sprintf("the question is %d bytes; curio understands "+
			"questions of at most %d", len(question), maxQuestionBytes)}, true
	}
	return Outcome{}, false
}

// timed runs plan unless ctx has ended, and gives its outcome the model
// and how long it took.
func (p *Planner) timed(ctx context.Context, plan func() (Outcome, error)) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	began := time.Now()
	out, err := plan()
	if err != nil {
		return Outcome{}, err
	}
	out.Model, out.Took = p.model, time.Since(began)
	return out, nil
}

func (p *Planner) off() Outcome { return Outcome{Status: StatusOff, Reason: p.offReason} }

// refused is the Outcome of a question the gate held back from the
// model: its refusal's kind as a status, with its reason.
func refused(r modelgate.Refusal) Outcome {
	status := StatusUnavailable
	switch r.Kind {
	case modelgate.Unfit:
		status = StatusUnfit
	case modelgate.TimeoutHeld:
		status = StatusTimeout
	case modelgate.Cold:
		status = StatusCold
	case modelgate.Checking:
		status = StatusChecking
	case modelgate.Unavailable:
	}
	return Outcome{Status: status, Reason: r.Reason}
}

// KeepAlive is how long the model stays loaded after a plan or a warm-up:
// search.understand_keep_alive_minutes.
func (p *Planner) KeepAlive() time.Duration { return p.gate.KeepAlive() }

// ask asks the model for a plan of question under the planner's timeout,
// and turns its answer into one, or into the status that says why there
// is none.
func (p *Planner) ask(ctx context.Context, question string) (Outcome, error) {
	pctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	end := p.gate.Use()
	defer end()
	raw, err := p.gen.GenerateJSON(pctx, renderPrompt(question, p.now().In(p.loc)), schema(), p.planOptions())
	if cerr := ctx.Err(); cerr != nil {
		return Outcome{}, cerr
	}
	if err != nil {
		return p.failed(err), nil
	}
	a, err := decodeAnswer(raw)
	if err != nil {
		return p.failed(err), nil
	}
	a = normalize(a, question)
	filters, dropped := groundFilters(a.Filters, question, p.now(), p.loc)
	return Outcome{Status: StatusPlanned, Dropped: dropped,
		Plan: &Plan{Kind: a.Kind, Filters: filters, Rewrites: a.Rewrites, Names: a.Names}}, nil
}

// planOptions are a plan's generation options: greedy, capped, and keeping
// the model loaded. The context size is the generator's own, which the
// labels share: a request with another would load the model again
// (measured: 5.1 s).
func (p *Planner) planOptions() generator.Options {
	return generator.Options{Temperature: new(0.0), MaxTokens: maxPlanTokens, KeepAlive: p.gate.KeepAlive()}
}

// settle records what a search's plan said of the model: one that
// answered in time ends a run of timeouts, and holds the model loaded for
// its keep-alive; a plan made clears the causes logged, so a later outage
// warns again. A timeout counts toward holding planning.
func (p *Planner) settle(out Outcome) {
	switch out.Status {
	case StatusPlanned, StatusInvalid:
		p.gate.Answered()
		if out.Status == StatusPlanned {
			p.gate.Succeeded()
		}
	case StatusTimeout:
		p.gate.TimedOut(p.timeout)
	case StatusUnavailable:
		// No answer says nothing of the model's speed: the run stands.
	case StatusSupplied, StatusOff, StatusCold, StatusChecking, StatusUnfit, StatusTooLong:
		// Not a plan the model was asked for.
	}
}

// failed is the Outcome of a plan the model didn't give: a reply it can't
// use is invalid (the error never quotes the reply, which can hold the
// question's words), the planner's own deadline a timeout, and any other
// failure, the model or its provider missing, refusing or erring,
// unavailable. Each cause is logged at WARN the first time since the last
// plan made, at DEBUG after.
func (p *Planner) failed(err error) Outcome {
	if ia, ok := errors.AsType[*invalidAnswer](err); ok {
		p.gate.Note("invalid:"+ia.rule, "search understanding: the planner's answer couldn't be used", errors.New(ia.reason))
		return Outcome{Status: StatusInvalid, Reason: ia.reason}
	}
	switch {
	case errors.Is(err, generator.ErrTruncated):
		p.gate.Note("invalid:truncated", "search understanding: the planner's answer couldn't be used", err)
		return Outcome{Status: StatusInvalid, Reason: fmt.Sprintf("the answer ran past %d tokens", maxPlanTokens)}
	case errors.Is(err, generator.ErrNotJSON):
		p.gate.Note("invalid:json", "search understanding: the planner's answer couldn't be used", generator.ErrNotJSON)
		return Outcome{Status: StatusInvalid, Reason: "the answer isn't JSON"}
	case errors.Is(err, context.DeadlineExceeded):
		p.gate.Note("timeout", "search understanding: a plan took longer than its timeout", err)
		return Outcome{Status: StatusTimeout, Reason: fmt.Sprintf("understanding took longer than %s", p.timeout)}
	case errors.Is(err, generator.ErrModelMissing):
		p.gate.Note("unavailable:generate", "search understanding: the planner's model is unavailable", err)
		return Outcome{Status: StatusUnavailable, Reason: p.gate.NotPulled()}
	default:
		p.gate.Note("unavailable:generate", "search understanding: the planner's model is unavailable", err)
		return Outcome{Status: StatusUnavailable, Reason: err.Error()}
	}
}

// Check runs the fitness check now, on ctx, and keeps its verdict as the
// background check does: it returns the probe the model failed, "" when it
// passed, or an error when a probe couldn't reach the model, which says
// nothing of it and leaves the verdict unknown. A search's planner checks
// in the background (Plan); Check is for measuring a model, and frees no
// memory, since the measuring goes on to ask it.
func (p *Planner) Check(ctx context.Context) (string, error) {
	switch {
	case p.gen == nil:
		return "", errors.New(p.offReason)
	case p.withheld != "":
		return "", errors.New(p.withheld)
	}
	return p.gate.CheckNow(ctx)
}

// runCheck runs the fitness check's probes, the gate's Options.Check:
// each through the prompt, the schema and the strict decoding, its raw
// answer compared before any guard, stopping at the first the model fails
// or can't be asked. A model that answers a probe wrong, or whose
// provider refuses the request itself, is unfit; one that couldn't be
// reached, or that the provider couldn't serve, isn't judged.
func (p *Planner) runCheck(ctx context.Context) modelgate.Verdict {
	for _, pr := range probes {
		a, err := p.probe(ctx, pr.question)
		switch {
		case ctx.Err() != nil:
			return modelgate.Verdict{Probe: pr.name, Err: ctx.Err()}
		case err != nil && !unfitBy(err):
			return modelgate.Verdict{Probe: pr.name, Err: err}
		case err != nil || !pr.fits(a):
			return modelgate.Verdict{Probe: pr.name, Unfit: true, Err: err}
		}
	}
	return modelgate.Verdict{}
}

// UnfitFix is what to do about a planner's model that failed its check.
const UnfitFix = "set generation.roles.planner.model to a model that passes, or search.understand: false"

// probe asks the model to plan a probe's question for the check, decoding
// its answer strictly.
func (p *Planner) probe(ctx context.Context, question string) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	end := p.gate.Use()
	defer end()
	raw, err := p.gen.GenerateJSON(ctx, renderPrompt(question, p.now().In(p.loc)), schema(), p.planOptions())
	if err != nil {
		return answer{}, err
	}
	return decodeAnswer(raw)
}

// unfitBy reports whether a probe's failure says the model is unfit: an
// answer it gave that can't be used, or a request its provider refuses as
// made, which no retry changes (a format or option the model doesn't
// support). Anything else, no answer, a missing model, a refused caller,
// a provider that can't serve now or a failure of no known kind, says
// nothing of the model.
func unfitBy(err error) bool {
	if _, ok := errors.AsType[*invalidAnswer](err); ok {
		return true
	}
	return errors.Is(err, generator.ErrNotJSON) || errors.Is(err, generator.ErrTruncated) ||
		errors.Is(err, generator.ErrBadRequest)
}

// Failure is the planner's last failure: what kind (Cause, as its log
// records name it), when, and its error.
type Failure = modelgate.Failure

// State is what the planner knows of its model, read from memory alone:
// whether understanding is on, why no question is planned at all (Reason:
// off, or withheld), the fitness check's verdict and when it was reached,
// the last question's status, the holds in effect and the last failure.
type State struct {
	On     bool
	Model  string
	Reason string
	// Check is "unchecked", "fit" or "unfit"; Unfit says why, with the
	// fix.
	Check     string
	CheckedAt time.Time
	Unfit     string
	Last      Status
	LastAt    time.Time
	// TimeoutHoldUntil and EvictionHoldUntil are set only while each
	// holds.
	TimeoutHoldUntil  time.Time
	EvictionHoldUntil time.Time
	Failure           Failure
}

// The fitness check's verdicts, as State says them.
const (
	CheckUnchecked = modelgate.CheckUnchecked
	CheckFit       = modelgate.CheckFit
	CheckUnfit     = modelgate.CheckUnfit
)

// State reads the planner's state, asking the model nothing.
func (p *Planner) State() State {
	g := p.gate.State()
	s := State{On: p.on, Model: p.model, Reason: p.offReason, Check: g.Check, CheckedAt: g.CheckedAt, Unfit: g.Unfit,
		TimeoutHoldUntil: g.TimeoutHoldUntil, EvictionHoldUntil: g.EvictionHoldUntil, Failure: g.Failure}
	if p.withheld != "" {
		s.Reason = p.withheld
	}
	p.mu.Lock()
	s.Last, s.LastAt = p.last, p.lastAt
	p.mu.Unlock()
	return s
}
