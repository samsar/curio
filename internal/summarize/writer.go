// Package summarize writes a document's summary: one structured call to
// the summaries role's model per page, which says what the page is (its
// kind), who made it, what it is about, the names it holds, its main
// claims and how someone might describe it later. Deterministic code
// decides what of it to trust: what the model is sent (BuildInput: the
// page's own fields and its reduced text, cut to fit the context), a
// strict decoding, entities grounded in that input, and a fitness check
// the model must pass before any of its summaries is written. See
// docs/decisions.md "Summaries: one structured call per document, stored
// for the reranker".
//
// The writer has no goroutines: the check runs inside the first call that
// needs it, on its caller's context, and so does a load of the model.
package summarize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/store"
)

// OffReason is why no summary is written while summaries.enabled is off:
// the writer's, and the API's when the daemon built none.
const OffReason = "document summaries are off (summaries.enabled: false)"

// OnFix is how to turn summaries on: config.yaml read again by a restart,
// whose start begins the pass.
const OnFix = "summaries.enabled: true in config.yaml, then a restart (curio daemon stop; any curio command " +
	"starts it), turns them on"

// OffByDefaultReason is why no summary is written while config.yaml leaves
// summaries.enabled out: they are off by default, for every model.
const OffByDefaultReason = "document summaries are off by default; " + OnFix

// UnfitFix is what to do about a summaries model that failed its check.
const UnfitFix = "set generation.roles.summaries.model to a model that passes, or summaries.enabled: false"

// A wait's or a hold's end is wall time, its monotonic reading dropped
// (Round(0)), so every comparison with it is made on the wall clock: the
// clock the jobs table keeps a job's run_after on, and the one healthz
// reports. On darwin Go's monotonic clock is mach_absolute_time, which
// stops while the Mac sleeps: an end judged on it outlasts a sleep, so the
// kept job, due by the wall clock, met a wait the writer still saw ahead,
// and the feed's next job, queued for an end already past on the wall
// clock, met it too, two jobs a second until the rest of the wait passed
// awake. The keep-alive the writer counts evictions by (keptUntil) stays
// on the monotonic clock: it stands for Ollama's own timer, which stops
// while the Mac sleeps too, and nothing stores or reports it.

// The writer's bounds.
const (
	// loadedBound bounds asking the provider whether the model is loaded,
	// and whether it answers at all, each a local listing.
	loadedBound = time.Second
	// unloadBound bounds asking the provider to free an unfit model.
	unloadBound = 10 * time.Second
	// checkRetryFirst is how long after a fitness check that reached no
	// verdict for a reason of the model's (it answered a probe with an
	// error, or too slowly) the check runs again, twice as long after each
	// further one, at most checkRetryLast: a model that answers every call
	// with an error would otherwise be asked a probe each time a job came
	// due, as the planner's check would be without its retry.
	checkRetryFirst = time.Minute
	checkRetryLast  = 15 * time.Minute
	// providerKeepAlive is how long Ollama keeps a model after a request
	// that names no keep-alive: the labels' never name one, nor the
	// summaries' off the planner's model.
	providerKeepAlive = 5 * time.Minute
	// evictionsToHold times the model is found unloaded within the
	// keep-alive of the writer's last call or load, within evictionWindow
	// of the first, hold the pass: Ollama is unloading it for another
	// model (the embedder, or another app's, on a Mac where they don't fit
	// together), and every page would load its weights again, gigabytes
	// each time, stalling that model's work. The hold lasts
	// firstEvictionHold, twice as long each time it holds again within
	// evictionWindow of the last hold's end, at most lastEvictionHold, as
	// the planner holds its warm-ups.
	evictionsToHold   = 2
	evictionWindow    = time.Hour
	firstEvictionHold = 15 * time.Minute
	lastEvictionHold  = 4 * time.Hour
)

// Errors a write returns besides the generator's, which it keeps in its
// chain (generator.ErrUnreachable and the others), and *InvalidAnswer.
var (
	// ErrNotWriting: the writer writes nothing, and never will in this
	// process: summaries are off, the provider can't answer in JSON, pages
	// may not be sent where the model runs, or the model failed its check.
	// The error says which.
	ErrNotWriting = errors.New("no summary is written")
	// ErrNotChecked: the fitness check couldn't reach a verdict (the model
	// didn't answer, or its provider couldn't serve the call), so nothing
	// is known of the model; a later call checks again.
	ErrNotChecked = errors.New("the summaries' model couldn't be checked")
	// ErrSlow: the call timed out while the model answers: the page took
	// longer than the role's timeout, the page's failure, not the model's.
	ErrSlow = errors.New("the summaries' model answers, but didn't write this page's summary within its timeout")
	// ErrHeld: the writer holds the pass for a while, asking the model
	// nothing: Ollama keeps unloading it for other models. The pass is fed
	// no job meanwhile (Writer.Feed).
	ErrHeld = errors.New("the summary pass is held")
)

// WaitError is a write, or a load, the writer turns down until Until: the
// fitness check couldn't reach a verdict, for a reason of the model's, and
// runs again then. Probed says whether this call asked the model and met
// the failure (it ran the check, or the load an unchecked model's check
// needs); one that didn't asked the model nothing. Err wraps ErrNotChecked
// and the check's failure.
type WaitError struct {
	Until  time.Time
	Probed bool
	Err    error
}

func (e *WaitError) Error() string { return e.Err.Error() }

func (e *WaitError) Unwrap() error { return e.Err }

// Feed is how many summarize jobs the pass may keep queued, as the writer
// sees its model (Writer.Feed).
type Feed int

const (
	// FeedAll: the writer may write and its model answers: the feed's own
	// limits.
	FeedAll Feed = iota
	// FeedOne: the model doesn't answer, or its fitness check couldn't
	// reach a verdict: one job, whose deferrals see it answer again, and no
	// more; while the check waits to run again, one that runs no sooner
	// (Writer.CheckRetryAt).
	FeedOne
	// FeedNone: the writer writes nothing, or holds the pass: no job.
	FeedNone
)

// Writer writes summaries with the summaries role's model. It is safe for
// concurrent use.
type Writer struct {
	gen       generator.JSONGenerator // nil when it writes nothing
	warmer    generator.Warmer        // nil when the provider can't say; the model is then taken as loaded
	offReason string
	// offByDefault says the writer is off because config.yaml leaves
	// summaries.enabled out.
	offByDefault bool
	withheld     string
	on           bool
	model        string
	shared       bool
	keepAlive    time.Duration
	measure      bool
	now          func() time.Time
	log          *slog.Logger

	mu          sync.Mutex
	verdict     verdict
	unfitReason string
	checkedAt   time.Time
	// checkErr is why the last fitness check reached no verdict, for a
	// reason of the model's, until one is reached; meanwhile no check runs
	// before checkRetryAt, wall time, and checkWait is how long the last
	// wait was.
	checkErr     error
	checkRetryAt time.Time
	checkWait    time.Duration
	// keptUntil is until when the model should stay loaded, by the
	// keep-alive of the writer's last call or load that got an answer;
	// zero when nothing of the writer's holds it.
	keptUntil time.Time
	// evictions counts the times the model was found unloaded before
	// keptUntil since firstEviction; while evictionHoldUntil is ahead the
	// writer asks nothing and the pass is fed nothing (it is wall time), and
	// evictionHold is how long the last hold lasted.
	evictions         int
	firstEviction     time.Time
	evictionHoldUntil time.Time
	evictionHold      time.Duration
	outage            Outage
	failure           Failure
	logged            map[string]bool // the causes logged at WARN since the last summary written
}

// verdict is what the fitness check found of the model.
type verdict int

const (
	verdictUnknown verdict = iota
	verdictFit
	verdictUnfit
)

// Outage is the model not answering: since when, the last error, and its
// cause, as the log names it (OutageMissing for a model the provider
// doesn't have). The zero value is none.
type Outage struct {
	Since time.Time
	Err   string
	Cause string
}

// The outages' causes: no answer at all, the model not pulled, the caller
// refused.
const (
	OutageUnreachable  = "unreachable"
	OutageMissing      = "missing"
	OutageUnauthorized = "unauthorized"
)

// Failure is the writer's last failure: what kind (Cause, as its log
// records name it), when, its error, and the document it was writing.
type Failure struct {
	Cause      string
	At         time.Time
	Err        string
	DocumentID string
}

// New builds a writer over gen. With opts.On false, or a gen that can't
// answer under a JSON schema (it isn't a generator.JSONGenerator), it
// writes nothing: it never falls back to parsing prose. The latter is
// logged once, at ERROR; summaries off, by default or by config.yaml, log
// nothing (healthz, status and doctor say so). With opts.Withheld set it
// writes nothing and sends nothing, logged once, at WARN.
func New(gen generator.Generator, opts Options) *Writer {
	w := &Writer{on: opts.On, model: opts.Model.Model, shared: opts.Shared, keepAlive: opts.KeepAlive,
		withheld: opts.Withheld, measure: opts.Measure, now: opts.Now, log: opts.Log, logged: map[string]bool{}}
	if w.now == nil {
		w.now = time.Now
	}
	if w.log == nil {
		w.log = slog.Default()
	}
	switch {
	case !opts.On && opts.OffByDefault:
		w.offReason, w.offByDefault = OffByDefaultReason, true
		return w
	case !opts.On:
		w.offReason = OffReason
		return w
	}
	jg, ok := gen.(generator.JSONGenerator)
	if !ok {
		w.offReason = opts.Model.Provider + " has no structured output"
		w.log.Error("document summaries are off: the summaries' model can't answer in JSON",
			"provider", opts.Model.Provider, "model", w.model)
		return w
	}
	if w.withheld != "" {
		w.log.Warn("document summaries: no page is sent to the summaries' model; none is written", "model", w.model,
			"reason", w.withheld)
	}
	w.gen = jg
	w.warmer, _ = gen.(generator.Warmer)
	return w
}

// Identity is who writes summaries now: the model and the prompt's
// version, which a summary written by another is outdated against.
func (w *Writer) Identity() store.SummaryWriter {
	return store.SummaryWriter{Model: w.model, PromptVersion: PromptVersion}
}

// Refusal is why the writer writes nothing, for good in this process: off,
// no structured output, withheld, or unfit with its fix; "" while it may
// write.
func (w *Writer) Refusal() string {
	switch {
	case w.gen == nil:
		return w.offReason
	case w.withheld != "":
		return w.withheld
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.verdict == verdictUnfit && !w.measure {
		return w.unfitReason
	}
	return ""
}

// Feed says how the pass may be fed now, read from memory alone: none for
// a writer that writes nothing, while an eviction hold is on, and while
// the provider doesn't have the model, which only a pull ends (Probe sees
// it); one job while the model doesn't answer otherwise or its fitness
// check waits to run again after reaching no verdict, since each further
// job would only wait beside it; all the feed's otherwise.
func (w *Writer) Feed() Feed {
	if w.Refusal() != "" {
		return FeedNone
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.now().Before(w.evictionHoldUntil), w.outage.Cause == OutageMissing:
		return FeedNone
	case !w.outage.Since.IsZero() || (w.verdict == verdictUnknown && w.checkErr != nil):
		return FeedOne
	}
	return FeedAll
}

// CheckRetryAt is when the fitness check runs again while it waits after
// one that reached no verdict, for a reason of the model's: until then
// every write and load asks nothing, and the feed queues no job due
// sooner, since one would only meet the wait and be done, and the next
// top-up would queue another. It is zero while the check doesn't wait, and
// once the time has passed. It reads memory alone; the time is wall time,
// as a job's run_after is.
func (w *Writer) CheckRetryAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.checkRetryAtLocked()
}

// checkRetryAtLocked is CheckRetryAt. w.mu is held.
func (w *Writer) checkRetryAtLocked() time.Time {
	if w.measure || w.verdict != verdictUnknown || w.checkErr == nil || !w.now().Before(w.checkRetryAt) {
		return time.Time{}
	}
	return w.checkRetryAt
}

// waiting is a *WaitError, asking nothing, while the fitness check waits
// to run again (CheckRetryAt); nil otherwise.
func (w *Writer) waiting() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	until := w.checkRetryAtLocked()
	if until.IsZero() {
		return nil
	}
	return &WaitError{Until: until, Err: fmt.Errorf("%w: %w", ErrNotChecked, w.checkErr)}
}

// refused is Refusal as an error wrapping ErrNotWriting, nil while it may
// write.
func (w *Writer) refused() error {
	if reason := w.Refusal(); reason != "" {
		return fmt.Errorf("%w: %s", ErrNotWriting, reason)
	}
	return nil
}

// Ready loads the model, when the provider says it isn't loaded, with the
// generator's context size and the writer's keep-alive, on ctx, whose
// end alone cancels the load: the caller passes a context no search
// pre-empts, so a load is never abandoned half way. A writer that writes
// nothing is an error wrapping ErrNotWriting, and a provider that doesn't
// answer one keeping its sentinel, which starts an outage. While the
// fitness check waits to run again it is that *WaitError, asking nothing;
// and a load the provider couldn't serve while the model is unchecked (a
// 5xx: not enough memory, say) is the check reaching no verdict, which
// can't run without it: a *WaitError, so no page is charged for a model
// that can't be loaded. A model found unloaded within the keep-alive of
// the writer's last call counts as evicted, and the evictionsToHold-th
// within evictionWindow starts a hold: then, and until the hold ends, it
// is an error wrapping ErrHeld, asking nothing.
func (w *Writer) Ready(ctx context.Context) error {
	if err := w.refused(); err != nil {
		return err
	}
	if err := w.waiting(); err != nil {
		return err
	}
	if w.warmer == nil {
		return nil
	}
	if err := w.held(); err != nil {
		return err
	}
	lctx, cancel := context.WithTimeout(ctx, loadedBound)
	loaded, err := w.warmer.Loaded(lctx)
	cancel()
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("summarize: %w", ctx.Err())
	case err != nil:
		return w.readyFailed("unavailable:loaded", fmt.Errorf("ask whether %s is loaded: %w", w.model, err))
	case loaded:
		w.answered()
		return nil
	}
	if err := w.unloaded(); err != nil {
		return err
	}
	w.log.Info("document summaries: loading the summaries' model", "model", w.model)
	err = w.warmer.Warm(ctx, w.keepAlive)
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("summarize: %w", ctx.Err())
	case err != nil && !noAnswer(err) && !unfitBy(err) && w.unchecked():
		err = fmt.Errorf("load %s: %w", w.model, err)
		return &WaitError{Until: w.checkFailed(err), Probed: true, Err: fmt.Errorf("%w: %w", ErrNotChecked, err)}
	case err != nil:
		return w.readyFailed("unavailable:load", fmt.Errorf("load %s: %w", w.model, err))
	}
	w.answered()
	w.kept()
	return nil
}

// unchecked reports whether the fitness check is still to reach a verdict
// before the writer's first summary: never for a writer that measures.
func (w *Writer) unchecked() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.measure && w.verdict == verdictUnknown
}

// held is an error wrapping ErrHeld while an eviction hold is on, nil
// otherwise.
func (w *Writer) held() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.now().Before(w.evictionHoldUntil) {
		return nil
	}
	return w.heldErrLocked()
}

// heldErrLocked is the eviction hold as an error. w.mu is held.
func (w *Writer) heldErrLocked() error {
	return fmt.Errorf("%w: Ollama unloaded %s within its keep-alive %d times within an hour, for other models; "+
		"the pass waits until %s", ErrHeld, w.model, evictionsToHold, w.evictionHoldUntil.Format(time.RFC3339))
}

// kept records that a call or a load of the writer's got an answer now:
// the model stays loaded for keptFor.
func (w *Writer) kept() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keptUntil = w.now().Add(w.keptFor())
}

// keptFor is how long after a call or a load of the writer's the model is
// sure to stay loaded: the shortest keep-alive a request for it may leave,
// the provider's default (another role's request may name none) or the
// writer's own when shorter.
func (w *Writer) keptFor() time.Duration {
	if w.keepAlive > 0 {
		return min(w.keepAlive, providerKeepAlive)
	}
	return providerKeepAlive
}

// unloaded counts the model, found unloaded, as evicted when its
// keep-alive by the writer's last call or load hadn't run out, and starts
// an eviction hold at the evictionsToHold-th within evictionWindow of the
// first: an error wrapping ErrHeld then, nil otherwise. A model found
// unloaded after its keep-alive ran out ends the count.
func (w *Writer) unloaded() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	evicted := !w.keptUntil.IsZero() && now.Before(w.keptUntil)
	expired := !w.keptUntil.IsZero() && !evicted
	w.keptUntil = time.Time{} // one unload counts once
	switch {
	case expired:
		w.evictions = 0
		return nil
	case !evicted:
		return nil
	case w.evictions == 0 || now.Sub(w.firstEviction) > evictionWindow:
		w.evictions, w.firstEviction = 1, now
	default:
		w.evictions++
	}
	w.log.Debug("document summaries: the summaries' model was unloaded within its keep-alive", "model", w.model,
		"evictions", w.evictions)
	if w.evictions < evictionsToHold {
		return nil
	}
	if w.evictionHold == 0 || now.Sub(w.evictionHoldUntil) > evictionWindow {
		w.evictionHold = firstEvictionHold
	} else {
		w.evictionHold = min(2*w.evictionHold, lastEvictionHold)
	}
	w.evictions, w.evictionHoldUntil = 0, now.Add(w.evictionHold).Round(0)
	err := w.heldErrLocked()
	w.recordLocked("", "evicted", err)
	w.log.Warn("document summaries: Ollama keeps unloading the summaries' model for other models; the pass waits",
		"model", w.model, "hold", w.evictionHold.String(), "until", w.evictionHoldUntil)
	return err
}

// readyFailed records and returns a failure of Ready, of cause unless it
// got no answer, which starts an outage.
func (w *Writer) readyFailed(cause string, err error) error {
	if noAnswer(err) {
		return w.unanswered("", err)
	}
	w.note("", cause, "document summaries: couldn't load the summaries' model", err)
	return err
}

// Written is a summary the model wrote, as normalized: its kind and texts,
// the writer and the hash of its input, but not the document or the
// extraction, which the caller sets. Dropped counts the entities left out
// as not in the input; Usage and Took are what the call cost.
type Written struct {
	Summary store.Summary
	Dropped int
	Usage   generator.Usage
	Took    time.Duration
}

// Write writes in's summary: the fitness check first while the model's
// verdict is unknown, then one call. Its errors:
//
//   - its context ending: that error, nothing recorded;
//   - ErrNotWriting: the writer writes nothing (off, withheld, unfit,
//     found unfit by this call's check);
//   - a *WaitError: the check couldn't reach a verdict for a reason of the
//     model's (by this call's check, or an earlier one whose wait isn't
//     over), and runs again at its Until;
//   - ErrNotChecked with a generator sentinel: the check got no answer,
//     which starts an outage;
//   - a failure no answer came back from, keeping the generator's
//     sentinel (generator.ErrUnreachable, ErrModelMissing,
//     ErrUnauthorized), which starts an outage, or ErrSlow for a timeout
//     while the model answers a listing;
//   - the page's own failures: an *InvalidAnswer, generator.ErrTruncated,
//     ErrNotJSON or ErrBadRequest, and ErrUnavailable or any other for a
//     provider that couldn't serve the call.
//
// Each failure but the context's is recorded for State, and logged at
// WARN the first time since the last summary written, at DEBUG after.
// Each page is logged at DEBUG: never its text, the prompt or the
// summary.
func (w *Writer) Write(ctx context.Context, in Input) (Written, error) {
	if err := w.refused(); err != nil {
		return Written{}, err
	}
	if err := w.ensureChecked(ctx); err != nil {
		return Written{}, err
	}
	prompt := renderPrompt(in)
	var usage generator.Usage
	began := time.Now()
	raw, err := w.gen.GenerateJSON(ctx, prompt, schema(), w.callOptions(&usage))
	took := time.Since(began)
	page := []any{"document_id", in.DocumentID, "took_ms", took.Milliseconds(), "input_bytes", len(prompt),
		"prompt_tokens", usage.PromptTokens, "output_tokens", usage.OutputTokens}
	if cerr := ctx.Err(); cerr != nil {
		return Written{}, fmt.Errorf("summarize: %w", cerr)
	}
	if err != nil {
		if errors.Is(err, generator.ErrTruncated) || errors.Is(err, generator.ErrNotJSON) {
			w.kept()
		}
		err = w.callFailed(ctx, in.DocumentID, err)
		w.log.Debug("document summaries: a page", append(page, "status", "failed", "err", err)...)
		return Written{}, err
	}
	w.answered()
	w.kept()
	a, err := decodeAnswer(raw)
	if err != nil {
		ia, _ := errors.AsType[*InvalidAnswer](err)
		w.note(in.DocumentID, "invalid:"+ia.Rule, "document summaries: the model's summary couldn't be used", err)
		w.log.Debug("document summaries: a page", append(page, "status", "invalid", "rule", ia.Rule)...)
		return Written{}, err
	}
	a, dropped := normalize(a, in)
	w.mu.Lock()
	clear(w.logged)
	w.mu.Unlock()
	w.log.Debug("document summaries: a page", append(page, "status", "written", "kind", a.Kind,
		"entities_dropped", dropped)...)
	return Written{Summary: store.Summary{Kind: a.Kind, Site: a.Site, Author: a.Author, About: a.About,
		Entities: a.Entities, Claims: a.Claims, Descriptions: a.Descriptions, SummaryWriter: w.Identity(),
		InputHash: in.Hash()}, Dropped: dropped, Usage: usage, Took: took}, nil
}

// callOptions are a summary call's generation options: greedy, capped at
// maxAnswerTokens, with the generator's own context size, which the
// planner and the labels share, and the writer's keep-alive (Options).
func (w *Writer) callOptions(usage *generator.Usage) generator.Options {
	return generator.Options{Temperature: new(0.0), MaxTokens: maxAnswerTokens, KeepAlive: w.keepAlive,
		Usage: usage}
}

// callFailed records and returns a call's failure that got no usable
// answer (see Write). A reply that isn't JSON is returned as the sentinel
// alone: the provider's error quotes the reply, which holds the page's
// words, and the job's last_error, the summary's failure and the worker's
// log would carry it.
func (w *Writer) callFailed(ctx context.Context, documentID string, err error) error {
	switch {
	case errors.Is(err, generator.ErrTruncated):
		w.note(documentID, "invalid:truncated", "document summaries: the model's summary couldn't be used", err)
	case errors.Is(err, generator.ErrNotJSON):
		err = fmt.Errorf("summarize: %w", generator.ErrNotJSON)
		w.note(documentID, "invalid:json", "document summaries: the model's summary couldn't be used", err)
	case errors.Is(err, generator.ErrBadRequest):
		w.note(documentID, "bad_request", "document summaries: the model's provider refuses the request", err)
	case timedOut(err) && w.answers(ctx):
		err = fmt.Errorf("%w: %s", ErrSlow, err.Error())
		w.note(documentID, "timeout", "document summaries: a page took longer than the role's timeout", err)
	case noAnswer(err):
		return w.unanswered(documentID, err)
	default:
		w.note(documentID, "unavailable", "document summaries: the model's provider couldn't write a summary", err)
	}
	return err
}

// noAnswer reports whether err is a call that got no answer from the
// model's provider: down, without the model, or refusing the caller.
func noAnswer(err error) bool {
	return errors.Is(err, generator.ErrUnreachable) || errors.Is(err, generator.ErrModelMissing) ||
		errors.Is(err, generator.ErrUnauthorized)
}

// timedOut reports whether err is a timeout: a deadline passed, or a
// network operation that timed out.
func timedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// answers reports whether the model's provider answers a listing of its
// models within loadedBound: up, if slow to write.
func (w *Writer) answers(ctx context.Context) bool {
	pctx, cancel := context.WithTimeout(ctx, loadedBound)
	defer cancel()
	return w.gen.Ping(pctx) == nil
}

// unanswered records err, a failure that got no answer (noAnswer), as the
// writer's outage, starting one unless one is on: one WARN for the
// outage, however many calls it fails. A missing model says how to get
// it.
func (w *Writer) unanswered(documentID string, err error) error {
	cause := OutageUnreachable
	switch {
	case errors.Is(err, generator.ErrModelMissing):
		cause = OutageMissing
		err = fmt.Errorf("%s isn't pulled — run curio up: %w", w.model, err)
	case errors.Is(err, generator.ErrUnauthorized):
		cause = OutageUnauthorized
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recordLocked(documentID, cause, err)
	if !w.outage.Since.IsZero() {
		w.outage.Err, w.outage.Cause = err.Error(), cause
		w.log.Debug("document summaries: the model still doesn't answer", "model", w.model, "cause", cause,
			"err", err)
		return err
	}
	w.outage = Outage{Since: w.now(), Err: err.Error(), Cause: cause}
	w.log.Warn("document summaries: the summaries' model doesn't answer; the pass waits for it", "model", w.model,
		"cause", cause, "err", err)
	return err
}

// Probe asks the provider's model listing, local and bounded by
// loadedBound, whether a model it didn't have is pulled now, while the
// writer records that outage; a listing that holds it ends the outage, so
// the pass is fed again. It asks nothing otherwise: an outage of another
// cause is seen ending by the job the pass keeps.
func (w *Writer) Probe(ctx context.Context) {
	if w.Refusal() != "" {
		return
	}
	w.mu.Lock()
	missing := w.outage.Cause == OutageMissing
	w.mu.Unlock()
	if !missing {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, loadedBound)
	err := w.gen.Ping(pctx)
	cancel()
	if err != nil {
		w.log.Debug("document summaries: the summaries' model isn't pulled yet", "model", w.model, "err", err)
		return
	}
	w.answered()
}

// answered records that the model's provider answered: an outage is over,
// with one INFO.
func (w *Writer) answered() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.outage.Since.IsZero() {
		return
	}
	w.log.Info("document summaries: the summaries' model answers again", "model", w.model,
		"since", w.outage.Since)
	w.outage = Outage{}
}

// note records a failure of cause and logs it: at WARN the first time
// since the last summary written, at DEBUG after. It carries the model,
// the cause and the error, never the page's text or the answer.
func (w *Writer) note(documentID, cause, msg string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recordLocked(documentID, cause, err)
	level := slog.LevelDebug
	if !w.logged[cause] {
		level = slog.LevelWarn
	}
	w.logged[cause] = true
	w.log.Log(context.Background(), level, msg, "model", w.model, "cause", cause, "document_id", documentID,
		"err", err)
}

// recordLocked keeps err as the writer's last failure, of cause, for
// State. w.mu is held.
func (w *Writer) recordLocked(documentID, cause string, err error) {
	w.failure = Failure{Cause: cause, At: w.now(), Err: err.Error(), DocumentID: documentID}
}

// State is what the writer knows, read from memory alone: whether
// summaries are on, its model and prompt, why it writes nothing (Reason:
// off, off by default, no structured output, or withheld),
// the fitness check's verdict and when it was reached or runs again, the
// eviction hold and the outage while one is on, and the last failure.
type State struct {
	On bool
	// OffByDefault is set while summaries are off because config.yaml
	// leaves summaries.enabled out; Reason then says how to turn them on.
	OffByDefault  bool
	Model         string
	PromptVersion string
	Reason        string
	// Check is "unchecked", "fit" or "unfit"; Unfit says why, with the
	// fix. CheckRetryAt is set only while an unchecked model's check
	// waits to run again, after one that reached no verdict, and that time
	// is ahead (Writer.CheckRetryAt): a job kept behind a pause runs it
	// later.
	Check        string
	CheckedAt    time.Time
	Unfit        string
	CheckRetryAt time.Time
	// EvictionHoldUntil is set only while the pass is held for a model
	// Ollama keeps unloading.
	EvictionHoldUntil time.Time
	Outage            Outage
	Failure           Failure
}

// The fitness check's verdicts, as State says them.
const (
	CheckUnchecked = "unchecked"
	CheckFit       = "fit"
	CheckUnfit     = "unfit"
)

// State reads the writer's state, asking the model nothing.
func (w *Writer) State() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := State{On: w.on, OffByDefault: w.offByDefault, Model: w.model, PromptVersion: PromptVersion,
		Reason: w.offReason, Check: CheckUnchecked, Outage: w.outage, Failure: w.failure}
	if w.now().Before(w.evictionHoldUntil) {
		s.EvictionHoldUntil = w.evictionHoldUntil
	}
	if w.withheld != "" {
		s.Reason = w.withheld
	}
	switch w.verdict {
	case verdictUnknown:
		s.CheckRetryAt = w.checkRetryAtLocked()
	case verdictFit:
		s.Check, s.CheckedAt = CheckFit, w.checkedAt
	case verdictUnfit:
		s.Check, s.CheckedAt, s.Unfit = CheckUnfit, w.checkedAt, w.unfitReason
	}
	return s
}
