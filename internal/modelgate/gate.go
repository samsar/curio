// Package modelgate decides when a search's use of a writing model, a
// plan or a rerank, may ask its model, and says why not when it may not.
// A search can't wait for a model: one that isn't loaded is loaded in the
// background while the search goes on without it; one not checked yet is
// checked once a process, in the background, before any of its answers is
// used; one that failed its check, keeps timing out, or that Ollama keeps
// unloading for other models is given a rest instead of every search's
// cost. The planner (internal/understand) and the reranker
// (internal/rerank) each keep a Gate, worded for their feature, and ask it
// before every call; what they ask and how they read the answer stays
// theirs.
package modelgate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/samsar/curio/internal/generator"
)

// The gate's constants, measured with the planner on Ollama 0.40.4 and
// gemma4:26b (decisions.md "Understanding the question").
const (
	// loadedBound bounds asking the provider whether the model is loaded,
	// and asking its model listing whether it is pulled: local listings.
	loadedBound = time.Second
	// DefaultKeepAlive holds the model loaded after a call or a warm-up
	// when Options leave it 0: a cold load takes 7 to 11 s, so a search a
	// few minutes after the last finds it warm.
	DefaultKeepAlive = 30 * time.Minute
	// unloadBound bounds asking the provider to free an unfit model.
	unloadBound = 10 * time.Second
	// RetryAfter is how long after a failed warm-up, or a check that
	// couldn't reach the model, the gate tries again; meanwhile the
	// failure answers.
	RetryAfter = time.Minute
	// timeoutsToHold calls in a row that time out hold the feature: the
	// model is too slow here (too large, or sharing the machine with an
	// index), and each further search would cost the call's full timeout
	// before it goes on without it. The hold lasts firstTimeoutHold, twice
	// as long each time it holds again, up to lastTimeoutHold; a call that
	// answers in time ends the episode.
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
	// don't): only that long after a call is such a shared model sure to
	// stay (Options.ShortSharer).
	sharedKeepAlive = 5 * time.Minute
)

// Words are how a feature's records and reasons name it.
type Words struct {
	// Feature begins every log record: "search understanding".
	Feature string
	// Role is the role whose model it is, as "the <Role>'s model":
	// "planner".
	Role string
	// Calls are the calls whose timeouts are counted: "plans".
	Calls string
	// Resumes is what resumes when a timeout hold ends: "understanding".
	Resumes string
	// Fallback is what a search does without the call: "searches use
	// their words".
	Fallback string
	// UnfitFix is what to do about a model that failed its check.
	UnfitFix string
}

// Verdict is what a fitness check found: the probe the model failed or
// couldn't be asked, whether it failed it, and why.
type Verdict struct {
	// Probe names the probe; "" when the model passed every one.
	Probe string
	// Unfit says the model failed Probe: an answer that can't be used, or
	// a request its provider refuses as made, which no retry changes.
	// Otherwise the probe never reached it, which says nothing of the
	// model.
	Unfit bool
	// Slow says Probe ran past its own deadline: the model answers too
	// slowly here for now, which holds the feature as calls that keep
	// timing out do, the check running again when the hold ends, the hold
	// doubling while checks keep running out. Err says by how much.
	Slow bool
	// Fix, when set, is what to do about an unfit model in place of
	// Words.UnfitFix: the provider's, when no other model would pass.
	Fix string
	// Err is why: for a probe that didn't reach the model, what kept it
	// from it; for an unfit model, the error its answer met, nil for a
	// wrong answer.
	Err error
}

// Options configure a Gate.
type Options struct {
	Words Words
	// Model is the role's model, as reasons name it.
	Model string
	// KeepAlive is how long the model stays loaded after a call or a
	// warm-up; 0 is DefaultKeepAlive.
	KeepAlive time.Duration
	// Shared says another role in use writes with the same model on the
	// same server (config.SharesModel): the gate then never unloads it.
	Shared bool
	// ShortSharer says such a role sends no keep-alive, or a shorter one
	// (config.SharesModelShorter): the gate then allows its requests to
	// shorten the model's keep-alive, to Ollama's default. A sharer that
	// sends the same keep-alive leaves it whole.
	ShortSharer bool
	// Using, when set, is told of each call of the model (a warm-up, an
	// unload, and every call the feature wraps in Use): it returns the
	// func that marks the call ended. The daemon's summary pass yields to
	// these calls; nil tells nobody.
	Using func() (end func())
	// Now is the clock holds and retries are judged by; nil is time.Now.
	Now func() time.Time
	// Location is where a reason's time of day is; nil is time.Local.
	Location *time.Location
	// Log receives the gate's records; nil is slog.Default().
	Log *slog.Logger
	// Check runs the fitness check on ctx: its probes in order, stopping
	// at the first the model fails or can't be asked.
	Check func(ctx context.Context) Verdict
}

// Kind is why a call may not be asked for now.
type Kind int

// The kinds of Refusal.
const (
	// Unfit: the model failed its fitness check.
	Unfit Kind = iota + 1
	// TimeoutHeld: calls kept timing out, and the hold is in effect.
	TimeoutHeld
	// Unavailable: the provider can't say, the model isn't pulled, its
	// load or check failed a moment ago, or Ollama keeps evicting it.
	Unavailable
	// Cold: the model is loading.
	Cold
	// Checking: the model is being checked.
	Checking
)

// Refusal is why a call may not be asked for now, and what the reason
// says.
type Refusal struct {
	Kind   Kind
	Reason string
}

// The fitness check's verdicts, as State says them.
const (
	CheckUnchecked = "unchecked"
	CheckFit       = "fit"
	CheckUnfit     = "unfit"
)

// verdict is what the fitness check found of the model.
type verdict int

const (
	verdictUnknown verdict = iota
	verdictFit
	verdictUnfit
)

// Gate holds a feature's model to the conditions under which a search may
// ask it. It is safe for concurrent use.
type Gate struct {
	words     Words
	model     string
	ping      func(ctx context.Context) error // the provider's listing; nil when there is no model to ask
	warmer    generator.Warmer                // nil when the provider can't say; the model is then taken as loaded
	keepAlive time.Duration
	shared    bool
	short     bool // a sharer sends no keep-alive, or a shorter one
	using     func() (end func())
	now       func() time.Time
	loc       *time.Location
	log       *slog.Logger
	runCheck  func(ctx context.Context) Verdict
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
	logged      map[string]bool // the causes logged at WARN since the last call that succeeded
	// timeouts counts the calls in a row that timed out, each past limit;
	// while timeoutHoldUntil is ahead, Ready refuses at once, and
	// timeoutHold is how long the last hold of the run lasted. slowCheck,
	// when set, is why a check that ran out of time holds it instead. Each hold's
	// end is wall time, its monotonic reading dropped: healthz reports it,
	// and judged on Go's monotonic clock, which stops while a Mac sleeps,
	// a hold would outlast the time it says. The retries (warmErrAt,
	// checkErrAt) and keptUntil, which stands for Ollama's own keep-alive
	// timer, stay monotonic: nothing reports or stores them.
	timeouts         int
	limit            time.Duration
	timeoutHoldUntil time.Time
	timeoutHold      time.Duration
	slowCheck        string
	// keptUntil is until when the model should stay loaded, by the last
	// answer or warm-up's keep-alive; zero when nothing holds it.
	keptUntil time.Time
	// evictions counts the times the model was found unloaded before
	// keptUntil since firstEviction; while evictionHoldUntil is ahead, no
	// warm-up starts.
	evictions         int
	firstEviction     time.Time
	evictionHoldUntil time.Time
	failure           Failure // the last failure
}

// New builds a gate over gen, whose background work runs on lifetime, the
// daemon's context; Wait returns once it has stopped after lifetime ends.
// A gen that is also a generator.Warmer is asked whether its model is
// loaded, and loaded; any other is taken as loaded.
func New(lifetime context.Context, gen generator.Generator, opts Options) *Gate {
	g := &Gate{
		words:     opts.Words,
		model:     opts.Model,
		keepAlive: opts.KeepAlive,
		shared:    opts.Shared,
		short:     opts.ShortSharer,
		using:     opts.Using,
		now:       opts.Now,
		loc:       opts.Location,
		log:       opts.Log,
		runCheck:  opts.Check,
		lifetime:  lifetime,
		logged:    map[string]bool{},
	}
	if g.keepAlive <= 0 {
		g.keepAlive = DefaultKeepAlive
	}
	if g.now == nil {
		g.now = time.Now
	}
	if g.loc == nil {
		g.loc = time.Local
	}
	if g.log == nil {
		g.log = slog.Default()
	}
	if gen != nil {
		g.ping = gen.Ping
		g.warmer, _ = gen.(generator.Warmer)
	}
	return g
}

// Use marks a call of the model beginning (Options.Using), and returns the
// func that marks it ended, which its callers defer: a call that panics
// (net/http recovers a search's) would otherwise leave the use open,
// holding the summary pass back until a restart.
func (g *Gate) Use() (end func()) {
	if g.using == nil {
		return func() {}
	}
	return g.using()
}

// Wait returns once the gate's background work has stopped, after the
// lifetime context New was given has ended.
func (g *Gate) Wait() { g.work.Wait() }

// KeepAlive is how long the model stays loaded after a call or a warm-up.
func (g *Gate) KeepAlive() time.Duration { return g.keepAlive }

// role is the role's model as a record names it: "the planner's model".
func (g *Gate) role() string { return "the " + g.words.Role + "'s model" }

// Ready reports whether a call may be asked for now; when it may not, the
// Refusal says why, and background work is started that will make it
// possible: loading the model, or checking it. A false with no Refusal
// (Kind 0) is the caller's context ending while the gate asked the
// provider, which says nothing of it and records nothing.
func (g *Gate) Ready(ctx context.Context) (Refusal, bool) {
	g.mu.Lock()
	switch now := g.now(); {
	case g.verdict == verdictUnfit:
		r := Refusal{Kind: Unfit, Reason: g.unfitReason}
		g.mu.Unlock()
		return r, false
	case now.Before(g.timeoutHoldUntil):
		why := g.slowCheck
		if why == "" {
			why = fmt.Sprintf("%d %s in a row took longer than %s", g.timeouts, g.words.Calls, g.limit)
		}
		r := Refusal{Kind: TimeoutHeld, Reason: fmt.Sprintf("%s; %s resumes at %s", why, g.words.Resumes,
			g.clock(g.timeoutHoldUntil))}
		g.mu.Unlock()
		return r, false
	}
	g.mu.Unlock()

	loaded := true
	if g.warmer != nil {
		lctx, cancel := context.WithTimeout(ctx, loadedBound)
		var err error
		loaded, err = g.warmer.Loaded(lctx)
		cancel()
		if err != nil && ctx.Err() != nil {
			// The caller left, which says nothing of the provider: the
			// first WARN is kept for a real outage.
			return Refusal{}, false
		}
		if err != nil {
			g.Note("unavailable:loaded", g.words.Feature+": can't ask whether "+g.role()+" is loaded", err)
			return Refusal{Kind: Unavailable, Reason: fmt.Sprintf("can't tell whether %s is loaded: %v", g.model,
				err)}, false
		}
	}
	// A model that isn't loaded may not be pulled at all: the provider's
	// model listing, local and quick, says so before a warm-up is started
	// that would only fail, and says so again on each call until it is
	// pulled, so a pull is seen at the next one. Any other failure of it
	// says nothing, and the warm-up finds out.
	var missing error
	if !loaded && g.ping != nil {
		pctx, cancel := context.WithTimeout(ctx, loadedBound)
		err := g.ping(pctx)
		cancel()
		switch {
		case err != nil && ctx.Err() != nil:
			return Refusal{}, false
		case errors.Is(err, generator.ErrModelMissing):
			missing = err
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if !loaded {
		if r, held := g.unloadedLocked(now); held {
			return r, false
		}
	}
	switch {
	case missing != nil:
		g.recordLocked("unavailable:missing", missing)
		g.noteLocked("unavailable:missing", g.words.Feature+": "+g.role()+" isn't pulled: run curio up; "+
			g.words.Fallback, missing)
		return Refusal{Kind: Unavailable, Reason: g.NotPulled()}, false
	case !loaded && g.warmErr != nil && errors.Is(g.warmErr, generator.ErrModelMissing):
		// A model not pulled stays so until curio up pulls it: each retry
		// tries again, and says what is known meanwhile.
		if now.Sub(g.warmErrAt) >= RetryAfter {
			g.startLocked(true)
		}
		return Refusal{Kind: Unavailable, Reason: g.NotPulled()}, false
	case !loaded && g.warmErr != nil && now.Sub(g.warmErrAt) < RetryAfter:
		return Refusal{Kind: Unavailable, Reason: fmt.Sprintf("loading %s failed: %v", g.model, g.warmErr)}, false
	case !loaded:
		g.startLocked(true)
		return Refusal{Kind: Cold, Reason: g.model + " is loading"}, false
	case g.verdict == verdictFit:
		return Refusal{}, true
	case g.checkErr != nil && now.Sub(g.checkErrAt) < RetryAfter:
		if errors.Is(g.checkErr, generator.ErrModelMissing) {
			return Refusal{Kind: Unavailable, Reason: g.NotPulled()}, false
		}
		return Refusal{Kind: Unavailable, Reason: fmt.Sprintf("checking %s failed: %v", g.model, g.checkErr)}, false
	default:
		g.startLocked(false)
		return Refusal{Kind: Checking, Reason: "curio is checking " + g.model}, false
	}
}

// Answered records a call that answered in time, a usable answer or not:
// it ends a run of timeouts, and holds the model loaded for its
// keep-alive.
func (g *Gate) Answered() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.answeredLocked()
	g.keptUntil = g.now().Add(g.keptFor())
}

// answeredLocked ends a run of timeouts, or of checks that ran out of
// time: a call or a check answered in time. g.mu is held.
func (g *Gate) answeredLocked() {
	if g.timeouts >= timeoutsToHold || g.slowCheck != "" {
		g.log.Info(g.words.Feature+": "+g.words.Calls+" answer in time again", "model", g.model)
	}
	g.timeouts, g.timeoutHold, g.slowCheck = 0, 0, ""
}

// Succeeded records a call whose answer was used: the causes logged are
// cleared, so a later outage warns again.
func (g *Gate) Succeeded() {
	g.mu.Lock()
	defer g.mu.Unlock()
	clear(g.logged)
}

// TimedOut records a call that ran past limit, its timeout. It counts
// toward holding the feature (holdTimeoutsLocked); calls asked together
// time out together, and a hold in effect already counts for them all, so
// they don't escalate it.
func (g *Gate) TimedOut(limit time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.timeouts++
	g.limit = limit
	if g.timeouts >= timeoutsToHold && !g.now().Before(g.timeoutHoldUntil) {
		g.slowCheck = ""
		g.holdTimeoutsLocked()
	}
}

// holdTimeoutsLocked holds the feature after timeoutsToHold calls in a row
// took longer than their timeout: firstTimeoutHold, twice the last hold
// while the run goes on, at most lastTimeoutHold. g.mu is held.
func (g *Gate) holdTimeoutsLocked() {
	if g.timeoutHold == 0 {
		g.timeoutHold = firstTimeoutHold
	} else {
		g.timeoutHold = min(2*g.timeoutHold, lastTimeoutHold)
	}
	g.timeoutHoldUntil = g.now().Add(g.timeoutHold).Round(0)
	if g.slowCheck != "" {
		g.recordLocked("timeout:check", errors.New(g.slowCheck))
		g.noteLocked("timeout:check", g.words.Feature+": checking "+g.role()+" ran out of time; "+g.words.Fallback+
			" for a while", nil, "hold", g.timeoutHold, "until", g.timeoutHoldUntil)
		return
	}
	g.recordLocked("timeout:held", fmt.Errorf("%d %s in a row took longer than %s", g.timeouts, g.words.Calls,
		g.limit))
	g.noteLocked("timeout:held", g.words.Feature+": "+g.words.Calls+" keep timing out; "+g.words.Fallback+
		" for a while", nil, "timeouts", g.timeouts, "hold", g.timeoutHold, "until", g.timeoutHoldUntil)
}

// keptFor is how long after an answer or a warm-up the model is sure to
// stay loaded: its keep-alive, or Ollama's default when another role,
// whose requests send none or a shorter one, shares the model.
func (g *Gate) keptFor() time.Duration {
	if g.short {
		return min(g.keepAlive, sharedKeepAlive)
	}
	return g.keepAlive
}

// unloadedLocked counts the model found unloaded before its keep-alive
// ran out as evicted, and reports whether warming it is held: by a hold
// in effect, or one that this eviction, the evictionsToHold-th within
// evictionWindow, starts. A model found unloaded after its keep-alive
// expired ends the count. g.mu is held.
func (g *Gate) unloadedLocked(now time.Time) (Refusal, bool) {
	held := func() Refusal {
		return Refusal{Kind: Unavailable, Reason: fmt.Sprintf("Ollama unloaded %s within its keep-alive "+
			"%d times, for other models; curio loads it again from %s", g.model, evictionsToHold,
			g.clock(g.evictionHoldUntil))}
	}
	if now.Before(g.evictionHoldUntil) {
		return held(), true
	}
	evicted := !g.keptUntil.IsZero() && now.Before(g.keptUntil)
	expired := !g.keptUntil.IsZero() && !evicted
	g.keptUntil = time.Time{} // one unload counts once
	switch {
	case expired:
		g.evictions = 0
		return Refusal{}, false
	case !evicted:
		return Refusal{}, false
	case g.evictions == 0 || now.Sub(g.firstEviction) > evictionWindow:
		g.evictions, g.firstEviction = 1, now
	default:
		g.evictions++
	}
	g.log.Debug(g.words.Feature+": "+g.role()+" was unloaded within its keep-alive", "model", g.model,
		"evictions", g.evictions)
	if g.evictions < evictionsToHold {
		return Refusal{}, false
	}
	g.evictions = 0
	g.evictionHoldUntil = now.Add(evictionHold).Round(0)
	g.recordLocked("evicted", fmt.Errorf("unloaded within its keep-alive %d times", evictionsToHold))
	g.noteLocked("evicted", g.words.Feature+": Ollama keeps unloading "+g.role()+" for other models; "+
		g.words.Fallback+" while it isn't loaded again", nil, "hold", evictionHold, "until", g.evictionHoldUntil)
	return held(), true
}

// NotPulled says the model is missing, and how to get it: the reason a
// call names for a model its provider doesn't have.
func (g *Gate) NotPulled() string { return g.model + " isn't pulled — run curio up" }

// clock is t as a reason names it: the time of day in the gate's
// location.
func (g *Gate) clock(t time.Time) string { return t.In(g.loc).Format("15:04") }

// Note records a failure of cause and logs it: at WARN the first time
// since the last call that succeeded, at DEBUG after. It carries the model
// and why, never what the call was about.
func (g *Gate) Note(cause, msg string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recordLocked(cause, err)
	g.noteLocked(cause, msg, err)
}

// noteLocked logs a failure of cause as Note does, with args after the
// model and cause. g.mu is held.
func (g *Gate) noteLocked(cause, msg string, err error, args ...any) {
	level := slog.LevelDebug
	if !g.logged[cause] {
		level = slog.LevelWarn
	}
	g.logged[cause] = true
	attrs := append([]any{"model", g.model, "cause", cause}, args...)
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	g.log.Log(context.Background(), level, msg, attrs...)
}

// recordLocked keeps err as the last failure, of cause, for State. g.mu is
// held.
func (g *Gate) recordLocked(cause string, err error) {
	g.failure = Failure{Cause: cause, At: g.now()}
	if err != nil {
		g.failure.Err = err.Error()
	}
}

// startLocked starts the background work unless it runs already or the
// lifetime has ended (the daemon is then waiting for that work to stop):
// loading the model when warm, then checking it while the verdict is
// unknown, and freeing an unfit model's memory. g.mu is held.
func (g *Gate) startLocked(warm bool) {
	if g.running || g.lifetime.Err() != nil {
		return
	}
	g.running = true
	g.work.Go(func() {
		defer func() {
			g.mu.Lock()
			g.running = false
			g.mu.Unlock()
		}()
		if warm && !g.warm() {
			return
		}
		g.mu.Lock()
		unknown := g.verdict == verdictUnknown
		g.mu.Unlock()
		if !unknown {
			return
		}
		// The check keeps its verdict and logs it; calls read it.
		if r := g.check(g.lifetime); r.failed != "" {
			g.unload()
		}
	})
}

// warm loads the model on the daemon's context, reporting whether it did.
func (g *Gate) warm() bool {
	g.log.Info(g.words.Feature+": loading "+g.role(), "model", g.model)
	end := g.Use()
	defer end()
	err := g.warmer.Warm(g.lifetime, g.keepAlive)
	if g.lifetime.Err() != nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.warmErr, g.warmErrAt = err, g.now()
		g.recordLocked("unavailable:load", err)
		reason := g.words.Fallback
		if errors.Is(err, generator.ErrModelMissing) {
			reason = "it isn't pulled: run curio up; " + g.words.Fallback
		}
		g.noteLocked("unavailable:load", g.words.Feature+": loading "+g.role()+" failed; "+reason, err,
			"retry_in", RetryAfter)
		return false
	}
	g.warmErr = nil
	g.keptUntil = g.now().Add(g.keptFor())
	return true
}

// unload frees an unfit model's memory, unless another role writes with
// it: an unfit model would otherwise sit loaded for its keep-alive,
// beside the embedder, for nothing.
func (g *Gate) unload() {
	if g.shared || g.warmer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(g.lifetime, unloadBound)
	defer cancel()
	end := g.Use()
	defer end()
	err := g.warmer.Unload(ctx)
	if g.lifetime.Err() != nil {
		return
	}
	if err != nil {
		g.log.Warn(g.words.Feature+": couldn't unload the unfit "+g.words.Role+"'s model", "model", g.model,
			"err", err)
		return
	}
	g.mu.Lock()
	g.keptUntil = time.Time{}
	g.mu.Unlock()
	g.log.Info(g.words.Feature+": unloaded the unfit "+g.words.Role+"'s model", "model", g.model)
}

// CheckNow runs the fitness check now, on ctx, and keeps its verdict as
// the background check does: it returns the probe the model failed, ""
// when it passed, or an error when a probe couldn't reach the model, which
// says nothing of it and leaves the verdict unknown. A search's gate
// checks in the background (Ready); CheckNow is for measuring a model, and
// frees no memory, since the measuring goes on to ask it.
func (g *Gate) CheckNow(ctx context.Context) (string, error) {
	r := g.check(ctx)
	return r.failed, r.unreached
}

// checkResult is what a fitness check found: the probe the model failed,
// or the failure that kept a probe from reaching it.
type checkResult struct {
	failed    string
	unreached error
}

// check runs the fitness check (Options.Check). A model that answers a
// probe wrong, or whose provider refuses the request itself, is unfit for
// the life of the process; one that couldn't be reached, or that the
// provider couldn't serve, leaves the verdict unknown, to be tried again
// after RetryAfter; one whose probe ran out of time leaves it unknown and
// holds the feature as calls that keep timing out do, so a model too slow
// here isn't asked again every minute. It records and logs the verdict.
func (g *Gate) check(ctx context.Context) checkResult {
	v := g.runCheck(ctx)
	if cerr := ctx.Err(); cerr != nil {
		return checkResult{unreached: cerr}
	}
	switch {
	case v.Probe == "":
		g.mu.Lock()
		g.verdict, g.checkErr, g.checkedAt = verdictFit, nil, g.now()
		g.keptUntil = g.now().Add(g.keptFor())
		if g.slowCheck != "" {
			g.answeredLocked()
		}
		g.mu.Unlock()
		g.log.Info(g.words.Feature+": "+g.role()+" passed its check", "model", g.model)
		return checkResult{}
	case v.Slow:
		g.mu.Lock()
		g.checkErr, g.checkErrAt = v.Err, g.now()
		g.slowCheck = fmt.Sprintf("checking %s, the probe %q %v", g.model, v.Probe, v.Err)
		if !g.now().Before(g.timeoutHoldUntil) {
			g.holdTimeoutsLocked()
		}
		g.mu.Unlock()
		return checkResult{unreached: fmt.Errorf("probe %q: %w", v.Probe, v.Err)}
	case !v.Unfit:
		// Retried a minute apart while it lasts: warned of once.
		g.mu.Lock()
		g.checkErr, g.checkErrAt = v.Err, g.now()
		g.recordLocked("unavailable:check", v.Err)
		g.noteLocked("unavailable:check", g.words.Feature+": couldn't check "+g.role()+"; "+g.words.Fallback, v.Err,
			"probe", v.Probe, "retry_in", RetryAfter)
		g.mu.Unlock()
		return checkResult{unreached: fmt.Errorf("probe %q: %w", v.Probe, v.Err)}
	}
	fix := g.words.UnfitFix
	if v.Fix != "" {
		fix = v.Fix
	}
	reason := fmt.Sprintf("%s didn't pass the check %q", g.model, v.Probe)
	if v.Err != nil {
		reason = fmt.Sprintf("%s didn't pass the check %q: %v", g.model, v.Probe, v.Err)
	}
	reason += "; " + fix
	g.mu.Lock()
	g.verdict, g.unfitReason, g.checkedAt = verdictUnfit, reason, g.now()
	g.recordLocked("unfit", errors.New(reason))
	g.mu.Unlock()
	g.log.Warn(g.words.Feature+": "+g.role()+" failed its check; "+g.words.Fallback+" ("+fix+")",
		"model", g.model, "probe", v.Probe)
	return checkResult{failed: v.Probe}
}

// Failure is the last failure: what kind (Cause, as its log records name
// it), when, and its error.
type Failure struct {
	Cause string
	At    time.Time
	Err   string
}

// State is what the gate knows of its model, read from memory alone: the
// fitness check's verdict and when it was reached, the holds in effect and
// the last failure.
type State struct {
	// Check is CheckUnchecked, CheckFit or CheckUnfit; Unfit says why,
	// with the fix.
	Check     string
	CheckedAt time.Time
	Unfit     string
	// TimeoutHoldUntil and EvictionHoldUntil are set only while each
	// holds.
	TimeoutHoldUntil  time.Time
	EvictionHoldUntil time.Time
	Failure           Failure
}

// State reads the gate's state, asking the model nothing.
func (g *Gate) State() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	s := State{Check: CheckUnchecked, Failure: g.failure}
	switch g.verdict {
	case verdictUnknown:
	case verdictFit:
		s.Check, s.CheckedAt = CheckFit, g.checkedAt
	case verdictUnfit:
		s.Check, s.CheckedAt, s.Unfit = CheckUnfit, g.checkedAt, g.unfitReason
	}
	if now.Before(g.timeoutHoldUntil) {
		s.TimeoutHoldUntil = g.timeoutHoldUntil
	}
	if now.Before(g.evictionHoldUntil) {
		s.EvictionHoldUntil = g.evictionHoldUntil
	}
	return s
}
