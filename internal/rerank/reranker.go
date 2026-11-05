package rerank

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/modelgate"
	"github.com/samsar/curio/internal/understand"
)

// Status is how a search's ranking came to be ordered: reranked, or why
// it kept its first ranking. It is a closed set.
type Status string

// The statuses.
const (
	StatusReranked    Status = "reranked"    // the reranker's order
	StatusSupplied    Status = "supplied"    // the client's order, a page of a reranked search
	StatusSkipped     Status = "skipped"     // the search's kind isn't reranked, or it has fewer than two candidates
	StatusOff         Status = "off"         // no reranker: search.rerank is off, or its model can't be asked
	StatusCold        Status = "cold"        // the reranker's model is loading
	StatusChecking    Status = "checking"    // the reranker's model is being checked
	StatusUnfit       Status = "unfit"       // the reranker's model failed the check
	StatusTimeout     Status = "timeout"     // the rerank took longer than search.rerank_timeout_seconds
	StatusUnavailable Status = "unavailable" // the reranker's model couldn't be reached or run
	StatusInvalid     Status = "invalid"     // the model's answer couldn't be used
	StatusTooLong     Status = "too_long"    // the question is longer than the reranker takes, and wasn't sent
)

// Statuses lists every status.
func Statuses() []Status {
	return []Status{StatusReranked, StatusSupplied, StatusSkipped, StatusOff, StatusCold, StatusChecking,
		StatusUnfit, StatusTimeout, StatusUnavailable, StatusInvalid, StatusTooLong}
}

// Outcome is what reranking a search's head came to: its status, why it
// kept its first ranking when it did, the model and method asked, how many
// candidates and how long it took, and the head's document IDs in the
// order applied.
type Outcome struct {
	Status Status
	Reason string
	Model  string
	Method Method
	// Depth is how many of the ranking's first documents were read, and
	// Candidates how many of them were sent.
	Depth      int
	Candidates int
	Took       time.Duration
	// Order is the head's IDs as reranked, for StatusReranked: what a
	// client sends back for another page.
	Order []string
	// Unscored and Bounded count the pointwise candidates left unscored,
	// and scored with a side bounded.
	Unscored int
	Bounded  int
}

// Request is a search's head to rerank: its question, its plan's kind
// (planned false for a search without a plan) and the candidates, the
// ranking's first in its order.
type Request struct {
	Question   string
	Kind       understand.Kind
	Planned    bool
	Candidates []Candidate
}

// method is one way of asking the model about the candidates shown:
// listwise, pointwise, or later a provider's own rerank endpoint.
type method interface {
	judge(ctx context.Context, question string, shown []Candidate, s Settings) (judgement, error)
}

// gateWords are how the gate's records and reasons name the reranker.
var gateWords = modelgate.Words{Feature: "search reranking", Role: "reranker", Calls: "reranks",
	Resumes: "reranking", Fallback: "searches keep their first ranking", UnfitFix: UnfitFix}

// Reranker reorders a search's head with the reranker role's model. As
// the planner's, its model must pass a fitness check before any answer is
// used, run once a process in the background, as is loading the model; a
// model that keeps timing out, or that Ollama keeps evicting, is given a
// rest (internal/modelgate). One rerank asks the model at a time. It is
// safe for concurrent use.
type Reranker struct {
	gate      *modelgate.Gate
	methods   map[Method]method
	missing   map[Method]string // why a method can't be asked: the provider lacks its interface
	settings  Settings
	family    Family
	on        bool
	byDefault bool
	offReason string
	withheld  string // why no search is reranked while on (Options.Held)
	model     string
	timeout   time.Duration
	log       *slog.Logger
	now       func() time.Time
	// slot is held by the rerank asking the model: Ollama runs a model's
	// requests one at a time, and two reranks interleaved would both run
	// out of time.
	slot chan struct{}

	mu     sync.Mutex
	last   Status
	lastAt time.Time
}

// New builds a reranker over gen, whose background work runs on lifetime,
// the daemon's context; Wait returns once it has stopped after lifetime
// ends. A method a route uses needs gen to implement its interface
// (generator.JSONGenerator for listwise, generator.LogprobGenerator for
// pointwise): without it, the searches of that route are StatusOff, never
// parsed from prose; with no route left, the reranker is off, logged once
// at ERROR. With opts.Held set (opts.Withheld, or opts.Unplanned while
// every route needs a plan), no search is reranked, logged once at WARN.
func New(lifetime context.Context, gen generator.Generator, opts Options) *Reranker {
	r := &Reranker{
		methods:   map[Method]method{},
		missing:   map[Method]string{},
		settings:  opts.Settings,
		family:    FamilyOf(opts.Model.Model),
		on:        opts.On,
		byDefault: opts.ByDefault,
		withheld:  opts.Held(),
		model:     opts.Model.Model,
		timeout:   opts.Timeout,
		log:       opts.Log,
		now:       opts.Now,
		slot:      make(chan struct{}, 1),
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	if r.now == nil {
		r.now = time.Now
	}
	r.gate = modelgate.New(lifetime, gen, modelgate.Options{Words: gateWords, Model: r.model,
		KeepAlive: opts.KeepAlive, Shared: opts.Shared, ShortSharer: opts.ShortSharer, Using: opts.Using, Now: r.now, Log: r.log,
		Check: r.runCheck})
	if !opts.On {
		r.offReason = opts.OffReason
		return r
	}
	if jg, ok := gen.(generator.JSONGenerator); ok {
		r.methods[MethodListwise] = listwise{gen: jg, keepAlive: r.gate.KeepAlive()}
	} else {
		r.missing[MethodListwise] = opts.Model.Provider + " has no structured output"
	}
	if lg, ok := gen.(generator.LogprobGenerator); ok {
		r.methods[MethodPointwise] = pointwise{gen: lg, family: r.family, keepAlive: r.gate.KeepAlive()}
	} else {
		r.missing[MethodPointwise] = opts.Model.Provider + " has no token probabilities"
	}
	if reason := r.noRoute(); reason != "" {
		r.offReason = reason
		r.log.Error("search reranking is off: the reranker's model can't be asked as its routes need",
			"provider", opts.Model.Provider, "model", r.model, "reason", reason)
		return r
	}
	if r.withheld != "" {
		r.log.Warn("search reranking: no search is reranked, nor any question or page sent to the reranker's "+
			"model; searches keep their first ranking", "model", r.model, "reason", r.withheld)
	}
	if opts.Shortened != "" {
		r.log.Warn("search reranking: reranks get less time than configured", "model", r.model,
			"timeout", r.timeout, "reason", opts.Shortened)
	}
	return r
}

// routedMethods are the methods the routes use for the model's family.
func (r *Reranker) routedMethods() []Method {
	var out []Method
	for _, g := range []Group{GroupOne, GroupTopical, GroupNone, GroupKeywords} {
		if m := r.settings.route(g, r.family); m != MethodSkip && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// uses reports whether a route uses m, the model able to be asked by it.
func (r *Reranker) uses(m Method) bool {
	_, ok := r.methods[m]
	return ok && slices.Contains(r.routedMethods(), m)
}

// noRoute is why no route can be asked, "" when one can.
func (r *Reranker) noRoute() string {
	reason := ""
	for _, m := range r.routedMethods() {
		if r.missing[m] == "" {
			return ""
		}
		reason = r.missing[m]
	}
	return reason
}

// Wait returns once the reranker's background work has stopped, after the
// lifetime context New was given has ended.
func (r *Reranker) Wait() { r.gate.Wait() }

// Timeout is how long a rerank may take before the search keeps its first
// ranking: search.rerank_timeout_seconds.
func (r *Reranker) Timeout() time.Duration { return r.timeout }

// Decide says, before a search runs, whether its head is reranked: the
// depth to read when it is, else 0 with the Outcome that says why not:
// the reranker is off, withheld, the question too long, the plan's kind
// skipped, or the route's method one the provider can't be asked by. It
// asks no model.
func (r *Reranker) Decide(question string, kind understand.Kind, planned bool) (int, Outcome) {
	out := Outcome{Model: r.model}
	switch {
	case !r.on || r.offReason != "":
		out.Status, out.Reason = StatusOff, r.offReason
		return 0, out
	case r.withheld != "":
		out.Status, out.Reason = StatusUnavailable, r.withheld
		return 0, out
	case len(question) > maxQuestionBytes:
		out.Status, out.Reason = StatusTooLong, fmt.Sprintf("the question is %d bytes; curio reranks for questions "+
			"of at most %d", len(question), maxQuestionBytes)
		return 0, out
	}
	g := GroupOf(kind, planned)
	switch m := r.settings.route(g, r.family); {
	case m == MethodSkip:
		out.Status, out.Reason = StatusSkipped, skippedReason(g)
		return 0, out
	case r.missing[m] != "":
		out.Status, out.Reason, out.Method = StatusOff, r.missing[m], m
		return 0, out
	default:
		return r.settings.Depth, out
	}
}

// skippedReason says why a search of group g keeps its first ranking.
func skippedReason(g Group) string {
	if g == GroupNone {
		return "searches without a plan keep their first ranking"
	}
	return fmt.Sprintf("searches for %s keep their first ranking", g)
}

// Rerank reorders req's head. Only the caller's context ending is an
// error, returned as is, nothing logged; every other way a head keeps its
// first ranking is an Outcome whose Status says why. Each rerank is logged
// at DEBUG, its status, timing and counts, never the question or a
// candidate's text.
func (r *Reranker) Rerank(ctx context.Context, req Request) (Outcome, error) {
	out, err := r.timed(ctx, req, func(m Method, cands []Candidate, held []bool) (Outcome, error) {
		if ref, ready := r.gate.Ready(ctx); !ready {
			if err := ctx.Err(); err != nil {
				return Outcome{}, err
			}
			return refused(ref), nil
		}
		rctx, cancel := context.WithTimeout(ctx, r.timeout)
		defer cancel()
		waited, turn := r.takeTurn(rctx)
		if !turn {
			if err := ctx.Err(); err != nil {
				return Outcome{}, err
			}
			// Waiting says nothing of the model's speed: no timeout counts.
			return Outcome{Status: StatusTimeout, Reason: "another search's rerank held the model for " +
				r.timeout.String()}, nil
		}
		defer func() { <-r.slot }()
		out, err := r.ask(rctx, m, req.Question, cands, held)
		if cerr := ctx.Err(); cerr != nil {
			return Outcome{}, cerr
		}
		if err != nil {
			out = r.failed(err)
		}
		if waited && out.Status == StatusTimeout {
			// Part of its time went to another search's rerank: its timeout
			// says no more of the model's speed than a wait that ran out.
			return out, nil
		}
		r.settle(out)
		return out, nil
	})
	if err != nil {
		return Outcome{}, err
	}
	r.mu.Lock()
	r.last, r.lastAt = out.Status, r.now()
	r.mu.Unlock()
	r.log.Debug("search reranking: a rerank", "status", out.Status, "took_ms", out.Took.Milliseconds(),
		"model", out.Model, "method", out.Method, "depth", out.Depth, "candidates", out.Candidates,
		"unscored", out.Unscored, "bounded", out.Bounded, "prompt_version", PromptVersion)
	return out, nil
}

// Ask reranks req's head now, whatever the fitness check found, whether
// the model is loaded, whatever holds reranking and with no timeout but
// ctx's: for measuring a model, as cmd/searcheval does, never for a
// search, which goes through Rerank. Its outcome is Rerank's once the
// model is loaded and fit.
func (r *Reranker) Ask(ctx context.Context, req Request) (Outcome, error) {
	return r.timed(ctx, req, func(m Method, cands []Candidate, held []bool) (Outcome, error) {
		out, err := r.ask(ctx, m, req.Question, cands, held)
		if cerr := ctx.Err(); cerr != nil {
			return Outcome{}, cerr
		}
		if err != nil {
			return r.failed(err), nil
		}
		return out, nil
	})
}

// timed decides req's route and runs rerank over its head unless the
// route says no or ctx has ended, and gives the outcome the model, the
// counts and how long it took.
func (r *Reranker) timed(ctx context.Context, req Request,
	rerank func(m Method, cands []Candidate, held []bool) (Outcome, error)) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	began := time.Now()
	depth, early := r.Decide(req.Question, req.Kind, req.Planned)
	if depth == 0 {
		early.Took = time.Since(began)
		return early, nil
	}
	m := r.settings.route(GroupOf(req.Kind, req.Planned), r.family)
	cands := req.Candidates[:min(len(req.Candidates), depth)]
	held := make([]bool, len(cands))
	shown := 0
	for i, c := range cands {
		held[i] = r.settings.Textless == TextlessHeld && c.Textless()
		if !held[i] {
			shown++
		}
	}
	var out Outcome
	if shown < 2 {
		out = Outcome{Status: StatusSkipped, Reason: fewCandidates(shown)}
	} else {
		var err error
		if out, err = rerank(m, cands, held); err != nil {
			return Outcome{}, err
		}
	}
	out.Model, out.Method, out.Depth, out.Candidates, out.Took = r.model, m, len(cands), shown, time.Since(began)
	return out, nil
}

// fewCandidates says why a head of shown candidates, fewer than two,
// keeps its first ranking.
func fewCandidates(shown int) string {
	if shown == 0 {
		return "no candidate to rerank; reranking takes at least two"
	}
	return "1 candidate to rerank; reranking takes at least two"
}

// ask asks the model about cands' shown candidates by m and arranges the
// head by its judgement, within ctx; its error is the call's.
func (r *Reranker) ask(ctx context.Context, m Method, question string, cands []Candidate, held []bool) (Outcome,
	error) {
	shown := make([]Candidate, 0, len(cands))
	for i, c := range cands {
		if !held[i] {
			shown = append(shown, c)
		}
	}
	end := r.gate.Use()
	defer end()
	j, err := r.methods[m].judge(ctx, question, shown, r.settings)
	if err != nil {
		return Outcome{}, err
	}
	head := reorder(cands, held, j, r.settings.Blend)
	order := make([]string, len(head))
	for i, c := range head {
		order[i] = c.ID
	}
	return Outcome{Status: StatusReranked, Order: order, Unscored: j.unscored(), Bounded: j.bounded}, nil
}

// takeTurn takes the model's slot, waiting for it until ctx ends: whether
// the rerank had to wait, and whether it got its turn.
func (r *Reranker) takeTurn(ctx context.Context) (waited, turn bool) {
	select {
	case r.slot <- struct{}{}:
		return false, true
	default:
	}
	select {
	case r.slot <- struct{}{}:
		return true, true
	case <-ctx.Done():
		return true, false
	}
}

// refused is the Outcome of a rerank the gate held back from the model:
// its refusal's kind as a status, with its reason.
func refused(ref modelgate.Refusal) Outcome {
	status := StatusUnavailable
	switch ref.Kind {
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
	return Outcome{Status: status, Reason: ref.Reason}
}

// settle records what a rerank said of the model: an answer in time, used
// or not, ends a run of timeouts; a rerank made clears the causes logged;
// a timeout counts toward holding reranking.
func (r *Reranker) settle(out Outcome) {
	switch out.Status {
	case StatusReranked, StatusInvalid:
		r.gate.Answered()
		if out.Status == StatusReranked {
			r.gate.Succeeded()
		}
	case StatusTimeout:
		r.gate.TimedOut(r.timeout)
	case StatusUnavailable:
		// No answer says nothing of the model's speed: the run stands.
	case StatusSupplied, StatusSkipped, StatusOff, StatusCold, StatusChecking, StatusUnfit, StatusTooLong:
		// Not a rerank the model was asked for.
	}
}

// failed is the Outcome of a rerank the model didn't give: an answer it
// can't use is invalid (never quoted: it can hold the candidates' words),
// the rerank's own deadline a timeout, and any other failure unavailable.
// Each cause is logged at WARN the first time since the last rerank made,
// at DEBUG after.
func (r *Reranker) failed(err error) Outcome {
	const unusable = "search reranking: the reranker's answer couldn't be used"
	if ia, ok := errors.AsType[*invalidAnswer](err); ok {
		r.gate.Note("invalid:"+ia.rule, unusable, errors.New(ia.reason))
		return Outcome{Status: StatusInvalid, Reason: ia.reason}
	}
	switch {
	case errors.Is(err, generator.ErrTruncated):
		r.gate.Note("invalid:truncated", unusable, generator.ErrTruncated)
		return Outcome{Status: StatusInvalid, Reason: fmt.Sprintf("the answer ran past %d tokens", maxOrderTokens)}
	case errors.Is(err, generator.ErrNotJSON):
		r.gate.Note("invalid:json", unusable, generator.ErrNotJSON)
		return Outcome{Status: StatusInvalid, Reason: "the answer isn't JSON"}
	case errors.Is(err, generator.ErrNoLogprobs):
		r.gate.Note("invalid:logprobs", unusable, generator.ErrNoLogprobs)
		return Outcome{Status: StatusInvalid, Reason: "the answer came without its token probabilities"}
	case errors.Is(err, context.DeadlineExceeded):
		r.gate.Note("timeout", "search reranking: a rerank took longer than its timeout", err)
		return Outcome{Status: StatusTimeout, Reason: fmt.Sprintf("reranking took longer than %s", r.timeout)}
	case errors.Is(err, generator.ErrModelMissing):
		r.gate.Note("unavailable:generate", "search reranking: the reranker's model is unavailable", err)
		return Outcome{Status: StatusUnavailable, Reason: r.gate.NotPulled()}
	default:
		r.gate.Note("unavailable:generate", "search reranking: the reranker's model is unavailable", err)
		return Outcome{Status: StatusUnavailable, Reason: err.Error()}
	}
}

// Check runs the fitness check now, on ctx, and keeps its verdict as the
// background check does: the probe the model failed, "" when it passed,
// or an error when a probe couldn't reach the model. For measuring a
// model; it frees no memory.
func (r *Reranker) Check(ctx context.Context) (string, error) {
	switch {
	case !r.on || r.offReason != "":
		return "", errors.New(r.offReason)
	case r.withheld != "":
		return "", errors.New(r.withheld)
	}
	return r.gate.CheckNow(ctx)
}

// Settings are the settings the reranker reranks by.
func (r *Reranker) Settings() Settings { return r.settings }

// State is what the reranker knows of its model, read from memory alone:
// whether reranking is on, why no search is reranked at all (Reason: off,
// a provider its routes can't ask, or withheld), the fitness check's
// verdict and when it was reached, the last rerank's status, the holds in
// effect and the last failure.
type State struct {
	On bool
	// ByDefault says it is on because config.yaml leaves search.rerank
	// out and the model is measured as the reranker.
	ByDefault bool
	Model     string
	Reason    string
	// Check is "unchecked", "fit" or "unfit"; Unfit says why, with the
	// fix.
	Check             string
	CheckedAt         time.Time
	Unfit             string
	Last              Status
	LastAt            time.Time
	TimeoutHoldUntil  time.Time
	EvictionHoldUntil time.Time
	Failure           Failure
}

// Failure is the reranker's last failure: what kind (Cause, as its log
// records name it), when, and its error.
type Failure = modelgate.Failure

// The fitness check's verdicts, as State says them.
const (
	CheckUnchecked = modelgate.CheckUnchecked
	CheckFit       = modelgate.CheckFit
	CheckUnfit     = modelgate.CheckUnfit
)

// State reads the reranker's state, asking the model nothing.
func (r *Reranker) State() State {
	g := r.gate.State()
	s := State{On: r.on, ByDefault: r.byDefault, Model: r.model, Reason: r.offReason, Check: g.Check, CheckedAt: g.CheckedAt,
		Unfit: g.Unfit, TimeoutHoldUntil: g.TimeoutHoldUntil, EvictionHoldUntil: g.EvictionHoldUntil,
		Failure: g.Failure}
	if r.withheld != "" && s.Reason == "" {
		s.Reason = r.withheld
	}
	r.mu.Lock()
	s.Last, s.LastAt = r.last, r.lastAt
	r.mu.Unlock()
	return s
}
