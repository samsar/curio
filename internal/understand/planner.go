package understand

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/samsar/curio/internal/generator"
)

// OffReason is why a search wasn't understood while search.understand is
// off: the planner's, and the API's when the daemon built none.
const OffReason = "search understanding is off (search.understand: false)"

// The planner's constants, measured in review on Ollama 0.40.4 with
// gemma4:26b and on the owner's library (decisions.md "Understanding the
// question").
const (
	// loadedBound bounds asking Ollama whether the model is loaded, a local
	// listing.
	loadedBound = time.Second
	// defaultKeepAlive holds the model loaded after a plan or a warm-up
	// when Options leave it 0: a cold load takes 7 to 11 s, so a search a
	// few minutes after the last finds it warm.
	defaultKeepAlive = 30 * time.Minute
	// maxPlanTokens caps a plan's answer: the median plan is 46 tokens, and
	// a rambling model is cut off well before it costs seconds.
	maxPlanTokens = 256
	// probeTimeout bounds each question of the fitness check, run in the
	// background on a loaded model: a warm plan takes 1 to 4 s.
	probeTimeout = 30 * time.Second
	// unloadBound bounds asking the provider to free an unfit model.
	unloadBound = 10 * time.Second
	// retryAfter is how long after a failed warm-up, or a check that
	// couldn't reach the model, the planner tries again; meanwhile the
	// failure answers.
	retryAfter = time.Minute
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
	// timeoutsToHold plans in a row that time out hold planning: the
	// model is too slow here (too large, or sharing the machine with an
	// index), and each further question would cost its full timeout
	// before its words are searched. The hold lasts firstTimeoutHold,
	// twice as long each time it holds again, up to lastTimeoutHold; a
	// plan that answers in time ends the episode.
	timeoutsToHold   = 3
	firstTimeoutHold = 2 * time.Minute
	lastTimeoutHold  = 15 * time.Minute
	// evictionsToHold times the model found unloaded within its
	// keep-alive, within evictionWindow of the first, hold warming it for
	// evictionHold: Ollama is evicting it for another model, most often
	// the embedder, and every warm-up would reload gigabytes and stall
	// that model's work.
	evictionsToHold = 2
	evictionWindow  = time.Hour
	evictionHold    = 15 * time.Minute
	// sharedKeepAlive is Ollama's default keep-alive, which a request of
	// another role sharing the model sets when it sends none (the labels
	// don't): only that long after a plan is a shared model sure to stay.
	sharedKeepAlive = 5 * time.Minute
)

// Planner understands questions with the planner role's model. Before any
// plan is used the model must pass a fitness check, run once a process in
// the background, as is loading the model when a question finds it cold:
// the question is then searched by its own words at once, and a later one
// gets a plan. A model that keeps timing out, or that Ollama keeps
// evicting, is given a rest instead of every question's cost. It is safe
// for concurrent use.
type Planner struct {
	gen       generator.JSONGenerator // nil when the planner is off
	warmer    generator.Warmer        // nil when the provider can't say; the model is then taken as loaded
	offReason string
	withheld  string
	on        bool
	model     string
	timeout   time.Duration
	keepAlive time.Duration
	shared    bool
	now       func() time.Time
	loc       *time.Location
	log       *slog.Logger
	// lifetime is the daemon's context, which background work runs on: a
	// request cancelled during a cold load would abort the load itself.
	lifetime context.Context
	work     sync.WaitGroup

	mu          sync.Mutex
	verdict     verdict
	unfitReason string
	checkedAt   time.Time // when the verdict was reached
	running     bool      // a warm-up or check is in flight
	warmErr     error     // the last warm-up's failure
	warmErrAt   time.Time // when it failed
	checkErr    error     // the last check that couldn't reach the model
	checkErrAt  time.Time
	logged      map[string]bool // the causes logged at WARN since the last plan
	// timeouts counts the plans in a row that timed out; while
	// timeoutHoldUntil is ahead, questions answer timeout at once, and
	// timeoutHold is how long the last hold of the run lasted.
	timeouts         int
	timeoutHoldUntil time.Time
	timeoutHold      time.Duration
	// keptUntil is until when the model should stay loaded, by the last
	// answer or warm-up's keep-alive; zero when nothing holds it.
	keptUntil time.Time
	// evictions counts the times the model was found unloaded before
	// keptUntil since firstEviction; while evictionHoldUntil is ahead, no
	// warm-up starts.
	evictions         int
	firstEviction     time.Time
	evictionHoldUntil time.Time
	last              Status // the last question's status
	lastAt            time.Time
	failure           Failure // the last failure
}

// verdict is what the fitness check found of the model.
type verdict int

const (
	verdictUnknown verdict = iota
	verdictFit
	verdictUnfit
)

// New builds a planner over gen, whose background work runs on lifetime,
// the daemon's context; Wait returns once it has stopped after lifetime
// ends. With opts.On false, or a gen that can't answer under a JSON schema
// (it isn't a generator.JSONGenerator), every plan is StatusOff: the
// planner never falls back to parsing prose. The latter is logged once, at
// ERROR. With opts.Withheld set, every plan is StatusUnavailable and no
// question reaches the model, logged once, at WARN.
func New(lifetime context.Context, gen generator.Generator, opts Options) *Planner {
	p := &Planner{
		on:        opts.On,
		model:     opts.Model.Model,
		timeout:   opts.Timeout,
		keepAlive: opts.KeepAlive,
		shared:    opts.Shared,
		withheld:  opts.Withheld,
		now:       opts.Now,
		loc:       opts.Location,
		log:       opts.Log,
		lifetime:  lifetime,
		logged:    map[string]bool{},
	}
	if p.keepAlive <= 0 {
		p.keepAlive = defaultKeepAlive
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
	p.warmer, _ = gen.(generator.Warmer)
	return p
}

// Wait returns once the planner's background work has stopped, after the
// lifetime context New was given has ended.
func (p *Planner) Wait() { p.work.Wait() }

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
		if out, ready := p.ready(ctx); !ready {
			if err := ctx.Err(); err != nil {
				return Outcome{}, err
			}
			return out, nil
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

// KeepAlive is how long the model stays loaded after a plan or a warm-up:
// search.understand_keep_alive_minutes.
func (p *Planner) KeepAlive() time.Duration { return p.keepAlive }

// ask asks the model for a plan of question under the planner's timeout,
// and turns its answer into one, or into the status that says why there
// is none.
func (p *Planner) ask(ctx context.Context, question string) (Outcome, error) {
	pctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
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
	return generator.Options{Temperature: new(0.0), MaxTokens: maxPlanTokens, KeepAlive: p.keepAlive}
}

// settle records what a search's plan said of the model: one that
// answered in time ends a run of timeouts, and holds the model loaded for
// its keep-alive; a plan made clears the causes logged, so a later outage
// warns again. A timeout counts toward holding planning (holdTimeoutsLocked).
func (p *Planner) settle(out Outcome) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch out.Status {
	case StatusPlanned, StatusInvalid:
		if p.timeouts >= timeoutsToHold {
			p.log.Info("search understanding: plans answer in time again", "model", p.model)
		}
		p.timeouts, p.timeoutHold = 0, 0
		p.keptUntil = p.now().Add(p.keptFor())
		if out.Status == StatusPlanned {
			clear(p.logged)
		}
	case StatusTimeout:
		// Plans asked together time out together: one hold in effect
		// already counts for them all, so they don't escalate it.
		p.timeouts++
		if p.timeouts >= timeoutsToHold && !p.now().Before(p.timeoutHoldUntil) {
			p.holdTimeoutsLocked()
		}
	case StatusUnavailable:
		// No answer says nothing of the model's speed: the run stands.
	case StatusSupplied, StatusOff, StatusCold, StatusChecking, StatusUnfit, StatusTooLong:
		// Not a plan the model was asked for.
	}
}

// holdTimeoutsLocked holds planning after timeoutsToHold plans in a row
// took longer than the timeout: firstTimeoutHold, twice the last hold
// while the run goes on, at most lastTimeoutHold. p.mu is held.
func (p *Planner) holdTimeoutsLocked() {
	if p.timeoutHold == 0 {
		p.timeoutHold = firstTimeoutHold
	} else {
		p.timeoutHold = min(2*p.timeoutHold, lastTimeoutHold)
	}
	p.timeoutHoldUntil = p.now().Add(p.timeoutHold)
	p.recordLocked("timeout:held", fmt.Errorf("%d plans in a row took longer than %s", p.timeouts, p.timeout))
	p.noteLocked("timeout:held", "search understanding: plans keep timing out; searches use their words for a while",
		nil, "timeouts", p.timeouts, "hold", p.timeoutHold, "until", p.timeoutHoldUntil)
}

// keptFor is how long after an answer or a warm-up the model is sure to
// stay loaded: its keep-alive, or Ollama's default when another role,
// whose requests send none, shares the model.
func (p *Planner) keptFor() time.Duration {
	if p.shared {
		return min(p.keepAlive, sharedKeepAlive)
	}
	return p.keepAlive
}

// ready reports whether a plan may be asked for now; when it may not, the
// Outcome says why, and background work is started that will make it
// possible: loading the model, or checking it.
func (p *Planner) ready(ctx context.Context) (Outcome, bool) {
	p.mu.Lock()
	switch now := p.now(); {
	case p.verdict == verdictUnfit:
		out := Outcome{Status: StatusUnfit, Reason: p.unfitReason}
		p.mu.Unlock()
		return out, false
	case now.Before(p.timeoutHoldUntil):
		out := Outcome{Status: StatusTimeout, Reason: fmt.Sprintf("%d plans in a row took longer than %s; "+
			"understanding resumes at %s", p.timeouts, p.timeout, p.clock(p.timeoutHoldUntil))}
		p.mu.Unlock()
		return out, false
	}
	p.mu.Unlock()

	loaded := true
	if p.warmer != nil {
		lctx, cancel := context.WithTimeout(ctx, loadedBound)
		var err error
		loaded, err = p.warmer.Loaded(lctx)
		cancel()
		if err != nil && ctx.Err() != nil {
			// The caller left, which says nothing of Ollama: Plan returns
			// its error, and the first WARN is kept for a real outage.
			return Outcome{}, false
		}
		if err != nil {
			p.note("unavailable:loaded", "search understanding: can't ask whether the planner's model is loaded", err)
			return Outcome{Status: StatusUnavailable, Reason: fmt.Sprintf("can't tell whether %s is loaded: %v",
				p.model, err)}, false
		}
	}
	// A model that isn't loaded may not be pulled at all: the provider's
	// model listing, local and quick, says so before a warm-up is started
	// that would only fail, and says so again on each question until it
	// is pulled, so a pull is seen at the next one. Any other failure of
	// it says nothing, and the warm-up finds out.
	var missing error
	if !loaded {
		pctx, cancel := context.WithTimeout(ctx, loadedBound)
		err := p.gen.Ping(pctx)
		cancel()
		switch {
		case err != nil && ctx.Err() != nil:
			return Outcome{}, false
		case errors.Is(err, generator.ErrModelMissing):
			missing = err
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if !loaded {
		if out, held := p.unloadedLocked(now); held {
			return out, false
		}
	}
	switch {
	case missing != nil:
		p.recordLocked("unavailable:missing", missing)
		p.noteLocked("unavailable:missing", "search understanding: the planner's model isn't pulled: run curio up; "+
			"searches use their words", missing)
		return Outcome{Status: StatusUnavailable, Reason: p.notPulled()}, false
	case !loaded && p.warmErr != nil && errors.Is(p.warmErr, generator.ErrModelMissing):
		// A model not pulled stays so until curio up pulls it: each retry
		// tries again, and says what is known meanwhile.
		if now.Sub(p.warmErrAt) >= retryAfter {
			p.startLocked(true)
		}
		return Outcome{Status: StatusUnavailable, Reason: p.notPulled()}, false
	case !loaded && p.warmErr != nil && now.Sub(p.warmErrAt) < retryAfter:
		return Outcome{Status: StatusUnavailable, Reason: fmt.Sprintf("loading %s failed: %v", p.model, p.warmErr)},
			false
	case !loaded:
		p.startLocked(true)
		return Outcome{Status: StatusCold, Reason: p.model + " is loading"}, false
	case p.verdict == verdictFit:
		return Outcome{}, true
	case p.checkErr != nil && now.Sub(p.checkErrAt) < retryAfter:
		if errors.Is(p.checkErr, generator.ErrModelMissing) {
			return Outcome{Status: StatusUnavailable, Reason: p.notPulled()}, false
		}
		return Outcome{Status: StatusUnavailable, Reason: fmt.Sprintf("checking %s failed: %v", p.model, p.checkErr)},
			false
	default:
		p.startLocked(false)
		return Outcome{Status: StatusChecking, Reason: "curio is checking " + p.model}, false
	}
}

// unloadedLocked counts the model found unloaded before its keep-alive
// ran out as evicted, and reports whether warming it is held: by a hold
// in effect, or one that this eviction, the evictionsToHold-th within
// evictionWindow, starts. A model found unloaded after its keep-alive
// expired ends the count. p.mu is held.
func (p *Planner) unloadedLocked(now time.Time) (Outcome, bool) {
	held := func() Outcome {
		return Outcome{Status: StatusUnavailable, Reason: fmt.Sprintf("Ollama unloaded %s within its keep-alive "+
			"%d times, for other models; curio loads it again from %s", p.model, evictionsToHold,
			p.clock(p.evictionHoldUntil))}
	}
	if now.Before(p.evictionHoldUntil) {
		return held(), true
	}
	evicted := !p.keptUntil.IsZero() && now.Before(p.keptUntil)
	expired := !p.keptUntil.IsZero() && !evicted
	p.keptUntil = time.Time{} // one unload counts once
	switch {
	case expired:
		p.evictions = 0
		return Outcome{}, false
	case !evicted:
		return Outcome{}, false
	case p.evictions == 0 || now.Sub(p.firstEviction) > evictionWindow:
		p.evictions, p.firstEviction = 1, now
	default:
		p.evictions++
	}
	p.log.Debug("search understanding: the planner's model was unloaded within its keep-alive",
		"model", p.model, "evictions", p.evictions)
	if p.evictions < evictionsToHold {
		return Outcome{}, false
	}
	p.evictions = 0
	p.evictionHoldUntil = now.Add(evictionHold)
	p.recordLocked("evicted", fmt.Errorf("unloaded within its keep-alive %d times", evictionsToHold))
	p.noteLocked("evicted", "search understanding: Ollama keeps unloading the planner's model for other models; "+
		"searches use their words while it isn't loaded again", nil, "hold", evictionHold,
		"until", p.evictionHoldUntil)
	return held(), true
}

// notPulled says the planner's model is missing, and how to get it.
func (p *Planner) notPulled() string { return p.model + " isn't pulled — run curio up" }

// clock is t as a reason names it: the time of day in the planner's
// location.
func (p *Planner) clock(t time.Time) string { return t.In(p.loc).Format("15:04") }

// failed is the Outcome of a plan the model didn't give: a reply it can't
// use is invalid (the error never quotes the reply, which can hold the
// question's words), the planner's own deadline a timeout, and any other
// failure, the model or its provider missing, refusing or erring,
// unavailable. Each cause is logged at WARN the first time since the last
// plan made, at DEBUG after.
func (p *Planner) failed(err error) Outcome {
	if ia, ok := errors.AsType[*invalidAnswer](err); ok {
		p.note("invalid:"+ia.rule, "search understanding: the planner's answer couldn't be used", errors.New(ia.reason))
		return Outcome{Status: StatusInvalid, Reason: ia.reason}
	}
	switch {
	case errors.Is(err, generator.ErrTruncated):
		p.note("invalid:truncated", "search understanding: the planner's answer couldn't be used", err)
		return Outcome{Status: StatusInvalid, Reason: fmt.Sprintf("the answer ran past %d tokens", maxPlanTokens)}
	case errors.Is(err, generator.ErrNotJSON):
		p.note("invalid:json", "search understanding: the planner's answer couldn't be used", generator.ErrNotJSON)
		return Outcome{Status: StatusInvalid, Reason: "the answer isn't JSON"}
	case errors.Is(err, context.DeadlineExceeded):
		p.note("timeout", "search understanding: a plan took longer than its timeout", err)
		return Outcome{Status: StatusTimeout, Reason: fmt.Sprintf("understanding took longer than %s", p.timeout)}
	case errors.Is(err, generator.ErrModelMissing):
		p.note("unavailable:generate", "search understanding: the planner's model is unavailable", err)
		return Outcome{Status: StatusUnavailable, Reason: p.notPulled()}
	default:
		p.note("unavailable:generate", "search understanding: the planner's model is unavailable", err)
		return Outcome{Status: StatusUnavailable, Reason: err.Error()}
	}
}

// note records a planner failure of cause and logs it: at WARN the first
// time since the last plan made, at DEBUG after. It carries the model and
// why, never the question, a rewrite or a name.
func (p *Planner) note(cause, msg string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recordLocked(cause, err)
	p.noteLocked(cause, msg, err)
}

// noteLocked logs a failure of cause as note does, with args after the
// model and cause. p.mu is held.
func (p *Planner) noteLocked(cause, msg string, err error, args ...any) {
	level := slog.LevelDebug
	if !p.logged[cause] {
		level = slog.LevelWarn
	}
	p.logged[cause] = true
	attrs := append([]any{"model", p.model, "cause", cause}, args...)
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	p.log.Log(context.Background(), level, msg, attrs...)
}

// recordLocked keeps err as the planner's last failure, of cause, for
// State. p.mu is held.
func (p *Planner) recordLocked(cause string, err error) {
	p.failure = Failure{Cause: cause, At: p.now()}
	if err != nil {
		p.failure.Err = err.Error()
	}
}

// startLocked starts the background work unless it runs already or the
// lifetime has ended (the daemon is then waiting for that work to stop):
// loading the model when warm, then checking it while the verdict is
// unknown, and freeing an unfit model's memory. p.mu is held.
func (p *Planner) startLocked(warm bool) {
	if p.running || p.lifetime.Err() != nil {
		return
	}
	p.running = true
	p.work.Go(func() {
		defer func() {
			p.mu.Lock()
			p.running = false
			p.mu.Unlock()
		}()
		if warm && !p.warm() {
			return
		}
		p.mu.Lock()
		unknown := p.verdict == verdictUnknown
		p.mu.Unlock()
		if !unknown {
			return
		}
		// The check keeps its verdict and logs it; searches read it.
		if r := p.check(p.lifetime); r.failed != "" {
			p.unload()
		}
	})
}

// warm loads the model on the daemon's context, reporting whether it did.
func (p *Planner) warm() bool {
	p.log.Info("search understanding: loading the planner's model", "model", p.model)
	err := p.warmer.Warm(p.lifetime, p.keepAlive)
	if p.lifetime.Err() != nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.warmErr, p.warmErrAt = err, p.now()
		p.recordLocked("unavailable:load", err)
		reason := "searches use their words"
		if errors.Is(err, generator.ErrModelMissing) {
			reason = "it isn't pulled: run curio up; searches use their words"
		}
		p.noteLocked("unavailable:load", "search understanding: loading the planner's model failed; "+reason, err,
			"retry_in", retryAfter)
		return false
	}
	p.warmErr = nil
	p.keptUntil = p.now().Add(p.keptFor())
	return true
}

// unload frees an unfit model's memory, unless another role writes with
// it: an unfit model would otherwise sit loaded for its keep-alive,
// beside the embedder, for nothing.
func (p *Planner) unload() {
	if p.shared || p.warmer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(p.lifetime, unloadBound)
	defer cancel()
	err := p.warmer.Unload(ctx)
	if p.lifetime.Err() != nil {
		return
	}
	if err != nil {
		p.log.Warn("search understanding: couldn't unload the unfit planner's model", "model", p.model, "err", err)
		return
	}
	p.mu.Lock()
	p.keptUntil = time.Time{}
	p.mu.Unlock()
	p.log.Info("search understanding: unloaded the unfit planner's model", "model", p.model)
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
	r := p.check(ctx)
	return r.failed, r.unreached
}

// checkResult is what a fitness check found: the probe the model failed,
// or the failure that kept a probe from reaching it.
type checkResult struct {
	failed    string
	unreached error
}

// check runs the fitness check: each probe through the prompt, the schema
// and the strict decoding, its raw answer compared before any guard. A
// model that answers a probe wrong, or whose provider refuses the request
// itself, is unfit for the life of the process; one that couldn't be
// reached, or that the provider couldn't serve, leaves the verdict
// unknown, to be tried again after retryAfter. It records and logs the
// verdict.
func (p *Planner) check(ctx context.Context) checkResult {
	for _, pr := range probes {
		a, err := p.probe(ctx, pr.question)
		if cerr := ctx.Err(); cerr != nil {
			return checkResult{unreached: cerr}
		}
		switch {
		case err != nil && !unfitBy(err):
			// Retried a minute apart while it lasts: warned of once.
			p.mu.Lock()
			p.checkErr, p.checkErrAt = err, p.now()
			p.recordLocked("unavailable:check", err)
			p.noteLocked("unavailable:check", "search understanding: couldn't check the planner's model; searches "+
				"use their words", err, "probe", pr.name, "retry_in", retryAfter)
			p.mu.Unlock()
			return checkResult{unreached: fmt.Errorf("probe %q: %w", pr.name, err)}
		case err != nil || !pr.fits(a):
			reason := fmt.Sprintf("%s didn't pass the check %q", p.model, pr.name)
			if err != nil {
				reason = fmt.Sprintf("%s didn't pass the check %q: %v", p.model, pr.name, err)
			}
			reason += "; " + UnfitFix
			p.mu.Lock()
			p.verdict, p.unfitReason, p.checkedAt = verdictUnfit, reason, p.now()
			p.recordLocked("unfit", errors.New(reason))
			p.mu.Unlock()
			p.log.Warn("search understanding: the planner's model failed its check; searches use their words "+
				"("+UnfitFix+")", "model", p.model, "probe", pr.name)
			return checkResult{failed: pr.name}
		}
	}
	p.mu.Lock()
	p.verdict, p.checkErr, p.checkedAt = verdictFit, nil, p.now()
	p.keptUntil = p.now().Add(p.keptFor())
	p.mu.Unlock()
	p.log.Info("search understanding: the planner's model passed its check", "model", p.model)
	return checkResult{}
}

// UnfitFix is what to do about a planner's model that failed its check.
const UnfitFix = "set generation.roles.planner.model to a model that passes, or search.understand: false"

// probe asks the model to plan a probe's question for the check, decoding
// its answer strictly.
func (p *Planner) probe(ctx context.Context, question string) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
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
type Failure struct {
	Cause string
	At    time.Time
	Err   string
}

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
	CheckUnchecked = "unchecked"
	CheckFit       = "fit"
	CheckUnfit     = "unfit"
)

// State reads the planner's state, asking the model nothing.
func (p *Planner) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	s := State{On: p.on, Model: p.model, Reason: p.offReason, Check: CheckUnchecked, Last: p.last,
		LastAt: p.lastAt, Failure: p.failure}
	if p.withheld != "" {
		s.Reason = p.withheld
	}
	switch p.verdict {
	case verdictUnknown:
	case verdictFit:
		s.Check, s.CheckedAt = CheckFit, p.checkedAt
	case verdictUnfit:
		s.Check, s.CheckedAt, s.Unfit = CheckUnfit, p.checkedAt, p.unfitReason
	}
	if now.Before(p.timeoutHoldUntil) {
		s.TimeoutHoldUntil = p.timeoutHoldUntil
	}
	if now.Before(p.evictionHoldUntil) {
		s.EvictionHoldUntil = p.evictionHoldUntil
	}
	return s
}
