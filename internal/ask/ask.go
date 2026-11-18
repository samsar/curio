// Package ask answers a question from a person's saved pages: the question
// is retrieved as a search is (internal/retrieve: understood, searched,
// reranked), the best pages are read as its sources, each shown the parts
// of its text nearest the question, and the answers role's model streams
// an answer citing them by number, each citation checked once it is
// written. A question its sources don't answer is answered so, in one
// fixed sentence. A question about the whole library takes one of the
// fixed plans instead (RouteFor): evidence for a claim, from passages the
// model judged and quoted; how the person's interests changed, from a
// table of their saves over time; and everything on a topic, from the
// pages found grouped by interest. Every way an ask ends without an
// answer is a status with its reason, never an error. See
// docs/decisions.md "Ask: a cited, streamed answer over the search path".
package ask

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/modelgate"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/retrieve"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// Status is how an ask ended: answered, or why not. It is a closed set.
type Status string

// The statuses.
const (
	StatusAnswered    Status = "answered"    // the answer, written whole
	StatusTruncated   Status = "truncated"   // the answer, cut at ask.max_tokens
	StatusNoSources   Status = "no_sources"  // no page to answer from, so no model was asked
	StatusOff         Status = "off"         // Ask is off, or its provider can't stream
	StatusWithheld    Status = "withheld"    // the question and pages may not go where the model runs
	StatusUnfit       Status = "unfit"       // the writer's model failed its check
	StatusTimeout     Status = "timeout"     // the ask ran past ask.timeout_seconds, or answers keep timing out
	StatusUnavailable Status = "unavailable" // the writer's model couldn't be reached or run
)

// Statuses lists every status.
func Statuses() []Status {
	return []Status{StatusAnswered, StatusTruncated, StatusNoSources, StatusOff, StatusWithheld, StatusUnfit,
		StatusTimeout, StatusUnavailable}
}

// Stage is what an ask is busy with, as a progress event names it.
type Stage string

// The stages.
const (
	StageRetrieving Stage = "retrieving" // understanding, searching and reranking the question
	StageLoading    Stage = "loading"    // waiting for the writer's model to load
	StageChecking   Stage = "checking"   // waiting for the writer's model to pass its check
	StageWaiting    Stage = "waiting"    // waiting for another ask's answer to finish
	StageJudging    Stage = "judging"    // the model is judging a claim's passages (the evidence route)
	StageWriting    Stage = "writing"    // the model is reading the sources and writing
)

// Stages lists every stage.
func Stages() []Stage {
	return []Stage{StageRetrieving, StageLoading, StageChecking, StageWaiting, StageJudging, StageWriting}
}

// Event is one thing an ask reports as it goes, in this order: Progress
// events, the Retrieved event, the route's own (Evidence, Table or
// Groups), Delta events, then one Done.
type Event interface{ event() }

// Progress says what the ask is busy with now; Model is the model it waits
// on, when it waits on one, and Pages, while judging, how many pages'
// passages it judges.
type Progress struct {
	Stage Stage
	Model string
	Pages int
}

// Retrieved is what the question retrieved: the route it takes, how it was
// understood and reranked (nil when it asked neither), whether the search
// went without its semantic lists and why, the sources the answer is
// written from (none on the interests and list routes, which write from a
// table and from groups), and the saved pages found that curio couldn't
// read.
type Retrieved struct {
	Route         Route
	Understanding *retrieve.Understanding
	Reranking     *retrieve.Reranking
	Degraded      bool
	Warnings      []string
	Sources       []Source
	Unreadable    []Unreadable
}

// Delta is a piece of the answer, as the model wrote it.
type Delta struct {
	Text string
}

// Timings are how long an ask's steps took.
type Timings struct {
	// Wait is the time spent waiting for the model to load and pass its
	// check, and for another answer to finish.
	Wait   time.Duration
	Plan   time.Duration
	Search time.Duration
	Rerank time.Duration
	// Sources is reading what the answer is written from: the sources'
	// fields, summaries, bookmarks and chunks, or the table, or the
	// groups.
	Sources time.Duration
	// Judge is the evidence route's judgment of a claim's passages.
	Judge time.Duration
	// PromptEval is the writer's reading of the answer's prompt, as its
	// provider reports it: 0 for an answer that never finished.
	PromptEval time.Duration
	// FirstToken and Total are from the ask's start.
	FirstToken time.Duration
	Total      time.Duration
}

// Done is how the ask ended: its status, and its reason for any status but
// answered; the model and prompt version; the route; the sources cited,
// in the order of their first citation, or on the interests and list
// routes the markers cited, likewise; the citations of what it wasn't
// given, as written; whether the answer is the abstention, or cites
// nothing without abstaining; and, for a timeout or an outage, whether
// pieces of the answer were sent before it stopped. The answer's text is
// never changed after it is sent: any marking is here.
type Done struct {
	Status           Status
	Reason           string
	Model            string
	PromptVersion    string
	Route            Route
	Citations        []int
	Markers          []string
	InvalidCitations []string
	Abstained        bool
	Uncited          bool
	Partial          bool
	Timings          Timings
}

func (Progress) event()  {}
func (Retrieved) event() {}
func (Delta) event()     {}
func (Done) event()      {}

// Request is a question to answer: its own filters, which win over a
// plan's dimension by dimension; Understand to ask the planner, or Plan to
// search by a plan supplied, not both; Rerank to ask the reranker (nil is
// ask.rerank), or RerankOrder to order the head as given (the developer's
// evaluation, replaying an ask); Sources, how many pages to answer from
// (0 is ask.sources); and Mode, the route to take ("" decides it:
// RouteFor).
type Request struct {
	Question    string
	Filters     search.FilterRequest
	Plan        *understand.Plan
	Understand  bool
	Rerank      *bool
	RerankOrder []string
	Sources     int
	Mode        Route
}

// RequestError is a request Validate refuses; its message says what to
// fix.
type RequestError struct{ Err error }

func (e *RequestError) Error() string { return e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

// Validate refuses a request before anything is retrieved or asked: an
// empty question, or one over MaxQuestionBytes; sources outside [1,
// config.MaxAskSources] (0 is ask.sources); filters that won't resolve
// alone (a filter's error names it, filters.<field>); understand with a
// plan, a plan understand.Validate refuses, or one whose filters won't
// resolve under the request's; an order retrieve.CheckOrder refuses; a
// mode no route is, and mode interests with a filter but the saved
// window, or an order (checkInterestsMode). A refusal is a
// *RequestError. Rerank nil is checked as no rerank asked: an ask
// resolves it (ask.rerank) before it checks it again.
func (r Request) Validate() error {
	refuse := func(err error) error { return &RequestError{Err: err} }
	switch q := strings.TrimSpace(r.Question); {
	case q == "":
		return refuse(errors.New("question is required"))
	case len(r.Question) > MaxQuestionBytes:
		return refuse(fmt.Errorf("question is %d bytes, at most %d", len(r.Question), MaxQuestionBytes))
	case r.Sources != 0 && (r.Sources < 1 || r.Sources > config.MaxAskSources):
		return refuse(fmt.Errorf("sources must be between 1 and %d (or omitted for ask.sources), got %d",
			config.MaxAskSources, r.Sources))
	}
	if _, err := r.Filters.Resolve(); err != nil {
		return refuse(err)
	}
	if err := retrieve.CheckPlan(r.query(0, false)); err != nil {
		return refuse(err)
	}
	if err := retrieve.CheckOrder(r.Rerank != nil && *r.Rerank, r.Understand, r.RerankOrder); err != nil {
		return refuse(err)
	}
	if err := checkMode(r.Mode); err != nil {
		return refuse(fmt.Errorf("mode: %w", err))
	}
	if r.Mode == RouteInterests {
		if err := checkInterestsMode(r.Filters, r.RerankOrder); err != nil {
			return refuse(err)
		}
	}
	return nil
}

// query is r as retrieve takes it, its window the ranking's first k,
// reranked when reranked says.
func (r Request) query(k int, reranked bool) retrieve.Query {
	return retrieve.Query{Question: r.Question, K: k, Filters: r.Filters, Understand: r.Understand, Plan: r.Plan,
		Rerank: reranked, RerankOrder: r.RerankOrder}
}

// resolved is r with Rerank set: ask.rerank when r leaves it out.
func (r Request) resolved(o Options) Request {
	if r.Rerank == nil {
		r.Rerank = new(o.Rerank)
	}
	return r
}

// Retriever retrieves a question as a search does: retrieve.Retriever.
// Understand is its understanding step alone, for the interests route,
// which searches nothing.
type Retriever interface {
	Retrieve(ctx context.Context, q retrieve.Query) (retrieve.Retrieval, error)
	Understand(ctx context.Context, q retrieve.Query) (retrieve.Understood, error)
}

// Stores are what an ask reads beside its search: the library's counts;
// the fields, summaries (nil reads none) and bookmarks of its sources;
// their chunks, ranked by the question (nil passes the search's chunks
// alone, without their sections); and the interests (nil reads none: the
// library reads as not grouped).
type Stores struct {
	Library interface {
		CountByState(ctx context.Context, tenantID string) (map[store.DocState]int, error)
	}
	Fields    rerank.FieldsReader
	Summaries rerank.SummaryReader
	Bookmarks interface {
		ListByDocuments(ctx context.Context, tenantID string, ids []string) (map[string][]*store.Bookmark, error)
	}
	Chunks    ChunkReader
	Interests InterestReader
	TenantID  string
}

// ChunkReader ranks a few documents' chunks by a question's vector and
// reads chunks back: store.ChunkStore's RankChunks and GetByIDs.
type ChunkReader interface {
	RankChunks(ctx context.Context, tenantID string, vector []float32, documentIDs []string) ([]store.RankedChunk,
		error)
	GetByIDs(ctx context.Context, ids []string) ([]*store.Chunk, error)
}

// InterestReader reads the latest rebuild of the interests: its saves for
// the interests route's table, where it put a ranking's documents for the
// list route's groups, and its identities' labels.
type InterestReader interface {
	insight.TimelineStore
	RunInterestsOf(ctx context.Context, runID string, ids []string) ([]store.DocumentInterest, error)
}

// summaries is the summary reader; without one, none is read.
func (s Stores) summaries() rerank.SummaryReader {
	if s.Summaries == nil {
		return noSummaries{}
	}
	return s.Summaries
}

// noSummaries reads no summary.
type noSummaries struct{}

func (noSummaries) CurrentSummaries(context.Context, string, []string) (map[string]*store.Summary, error) {
	return map[string]*store.Summary{}, nil
}

// Causes an ask's own context ends with: the ask's deadline, and the
// daemon stopping.
var (
	errAskDeadline = errors.New("the ask's deadline")
	errStopping    = errors.New("the daemon is stopping")
)

// stoppingReason is the reason of an ask the daemon's stop ended.
const stoppingReason = "the daemon is stopping; ask again once it has restarted"

// Asker answers questions with the answers role's model. Its model must
// pass a fitness check before any answer is used, run once a process, as
// is loading it; a model that keeps timing out, or that Ollama keeps
// evicting, is given a rest (internal/modelgate). Unlike a search's, an
// ask waits for a model that is loading or being checked. One answer is
// written at a time. It is safe for concurrent use.
type Asker struct {
	gate      *modelgate.Gate
	gen       generator.StreamGenerator // nil when the provider can't stream
	retriever Retriever
	stores    Stores
	lifetime  context.Context
	opts      Options
	offReason string
	log       *slog.Logger
	now       func() time.Time
	loc       *time.Location
	// slot is held by the answer being written, and by an evidence ask
	// from its judgment through its answer: Ollama runs a model's requests
	// one at a time, and two asks interleaved would both crawl.
	slot chan struct{}
	// rankWarned is set once a failure to rank the sources' chunks was
	// logged at WARN.
	rankWarned atomic.Bool

	mu     sync.Mutex
	last   Status
	lastAt time.Time
}

// New builds an asker over gen, retrieving through retriever and reading
// stores, whose background work runs on lifetime, the daemon's context:
// an ask in flight when it ends stops with StatusUnavailable, and Wait
// returns once the background work has stopped. Off (opts.On false), or
// with a gen that can't stream (generator.StreamGenerator), every ask is
// StatusOff; withheld (opts.Withheld), StatusWithheld, logged once at
// WARN.
func New(lifetime context.Context, gen generator.Generator, retriever Retriever, stores Stores,
	opts Options) *Asker {
	a := &Asker{retriever: retriever, stores: stores, lifetime: lifetime, opts: opts, log: opts.Log, now: opts.Now,
		loc: opts.Location, slot: make(chan struct{}, 1)}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.loc == nil {
		a.loc = time.Local
	}
	a.gate = modelgate.New(lifetime, gen, modelgate.Options{Words: gateWords, Model: opts.Model.Model,
		KeepAlive: opts.KeepAlive, Shared: opts.Shared, ShortSharer: opts.ShortSharer, Using: opts.Using,
		Traffic: opts.Traffic, Server: opts.Model.BaseURL, Now: a.now, Location: a.loc, Log: a.log,
		Check: a.runCheck})
	switch sg, ok := gen.(generator.StreamGenerator); {
	case !opts.On:
		a.offReason = cmp.Or(opts.OffReason, OffReason)
	case !ok:
		a.offReason = opts.Model.Provider + " can't stream answers"
		a.log.Error("ask is off: the answers role's model can't stream", "provider", opts.Model.Provider,
			"model", opts.Model.Model)
	default:
		a.gen = sg
		if opts.Withheld != "" {
			a.log.Warn("ask: no question is answered, nor any question or page sent to the answers role's model",
				"model", opts.Model.Model, "reason", opts.Withheld)
		}
	}
	return a
}

// Wait returns once the asker's background work has stopped, after the
// lifetime context New was given has ended.
func (a *Asker) Wait() { a.gate.Wait() }

// Timeout is how long an ask may take, its waits included:
// ask.timeout_seconds.
func (a *Asker) Timeout() time.Duration { return a.opts.Timeout }

// Ask answers req, emitting its events as it goes from the caller's
// goroutine: progress, what it retrieved, the answer's pieces and how it
// ended. Only a request Validate refuses, the caller's context ending, a
// store failing and an emit failing are errors; every other way an ask
// ends is the Done event's status. The whole ask counts as the person's
// use of the models (Options.Using), which the summary pass yields to.
// Each ask is logged at DEBUG, its status, timing and counts, never the
// question, a source or the answer.
func (a *Asker) Ask(ctx context.Context, req Request, emit func(Event) error) error {
	req = req.resolved(a.opts)
	if err := req.Validate(); err != nil {
		return err
	}
	if a.opts.Using != nil {
		end := a.opts.Using()
		defer end()
	}
	r := &run{a: a, req: req, emit: emit, began: time.Now(), route: req.route()}
	lctx, stopping := context.WithCancelCause(ctx)
	defer stopping(nil)
	stopWatching := context.AfterFunc(a.lifetime, func() { stopping(errStopping) })
	defer stopWatching()
	actx, cancel := context.WithTimeoutCause(lctx, a.opts.Timeout, errAskDeadline)
	defer cancel()

	done, err := r.answer(ctx, actx)
	if err != nil {
		return err
	}
	done.Model, done.PromptVersion, done.Route = a.opts.Model.Model, PromptVersion, r.route
	done.Timings = r.timings
	done.Timings.Total = time.Since(r.began)
	a.mu.Lock()
	a.last, a.lastAt = done.Status, a.now()
	a.mu.Unlock()
	t := done.Timings
	a.log.Debug("ask: an answer", "status", done.Status, "route", done.Route,
		"took_ms", t.Total.Milliseconds(), "first_token_ms", t.FirstToken.Milliseconds(),
		"sources_ms", t.Sources.Milliseconds(), "judge_ms", t.Judge.Milliseconds(),
		"prompt_eval_ms", t.PromptEval.Milliseconds(), "sources", r.sources,
		"cited", len(done.Citations)+len(done.Markers), "invalid", len(done.InvalidCitations),
		"model", done.Model, "prompt_version", PromptVersion)
	return emit(done)
}

// run is one ask in flight: what it was asked, and what it has found.
type run struct {
	a       *Asker
	req     Request
	emit    func(Event) error
	began   time.Time
	route   Route
	timings Timings
	sources int
	// waited says the ask spent time waiting on the model or the slot: a
	// deadline it then meets says nothing of the model's speed.
	waited bool
	// streamed is when the answer's call of the model began.
	streamed time.Time
}

// answer runs the ask on actx, its deadline and the daemon's lifetime
// laid over ctx, the caller's, and returns how it ended.
func (r *run) answer(ctx, actx context.Context) (Done, error) {
	a := r.a
	if done, refused := a.refusal(); refused {
		return done, nil
	}
	counts, err := a.stores.Library.CountByState(actx, a.stores.TenantID)
	if err != nil {
		return r.ended(ctx, actx, fmt.Errorf("count the library's pages: %w", err))
	}
	if counts[store.DocStateFetched] == 0 {
		return Done{Status: StatusNoSources, Reason: "the library has no page curio has read yet"}, nil
	}
	// A library not grouped yet is said so before the writer is awaited:
	// a cold load to say that would cost seconds for nothing.
	if r.route == RouteInterests {
		switch grouped, err := a.grouped(actx); {
		case err != nil:
			return r.ended(ctx, actx, err)
		case !grouped:
			return Done{Status: StatusNoSources, Reason: notGrouped}, nil
		}
	}

	waitBegan := time.Now()
	var emitErr error
	ref, ready := a.gate.Await(actx, func(w modelgate.Refusal) {
		r.waited = true
		stage := StageLoading
		if w.Kind == modelgate.Checking {
			stage = StageChecking
		}
		if err := r.emit(Progress{Stage: stage, Model: a.opts.Model.Model}); err != nil && emitErr == nil {
			emitErr = err
		}
	})
	r.timings.Wait += time.Since(waitBegan)
	if emitErr != nil {
		return Done{}, emitErr
	}
	switch {
	case ready:
	case ref.Kind == 0 || a.stopped():
		return r.ended(ctx, actx, actx.Err())
	default:
		return refusedDone(ref), nil
	}

	if r.route == RouteInterests {
		return r.interests(ctx, actx)
	}
	if err := r.emit(Progress{Stage: StageRetrieving}); err != nil {
		return Done{}, err
	}
	depth := readDepth
	if r.route == RouteList {
		depth = listMax
	}
	got, err := a.retriever.Retrieve(actx, r.req.query(depth, *r.req.Rerank))
	if err != nil {
		return r.ended(ctx, actx, err)
	}
	r.timings.Plan, r.timings.Search, r.timings.Rerank = got.Timings.Plan, got.Timings.Search, got.Timings.Rerank
	switch r.route {
	case RouteEvidence:
		return r.evidence(ctx, actx, got)
	case RouteList:
		return r.list(ctx, actx, got)
	case RouteAnswer, RouteInterests:
	}
	return r.answerRoute(ctx, actx, got)
}

// answerRoute answers from the first readable pages the question
// retrieved, citing them.
func (r *run) answerRoute(ctx, actx context.Context, got retrieve.Retrieval) (Done, error) {
	a := r.a
	began := time.Now()
	read, err := a.read(actx, got.Result.Vector, got.Result.Items)
	if err != nil {
		return r.ended(ctx, actx, err)
	}
	r.timings.Sources = time.Since(began)
	sources, unread := a.sources(read, r.k())
	r.sources = len(sources)
	// Rendered before it is reported, so each source's passages are those
	// the model is shown. The retrieval event lists every source, the
	// last among them when a refused prompt is asked again without it.
	prompt := renderAnswer(r.req.Question, planWords(got), sources, a.opts.MaxTokens, a.now(), a.loc)
	if err := r.emit(Retrieved{Route: r.route, Understanding: got.Understanding, Reranking: got.Reranking,
		Degraded: got.Result.Degraded, Warnings: got.Result.Warnings, Sources: shownSources(sources),
		Unreadable: unread}); err != nil {
		return Done{}, err
	}
	if len(sources) == 0 {
		return Done{Status: StatusNoSources, Reason: noSourcesReason(unread)}, nil
	}
	return r.withSlot(ctx, actx, func() (Done, error) { return r.stream(ctx, actx, prompt) })
}

// k is how many sources the answer is written from: the request's, else
// ask.sources.
func (r *run) k() int {
	if r.req.Sources != 0 {
		return r.req.Sources
	}
	return r.a.opts.Sources
}

// planWords are the plan's rewrites and names, which a source's passages
// are fitted by beside the question's own words.
func planWords(got retrieve.Retrieval) []string {
	if got.Plan == nil {
		return nil
	}
	return slices.Concat(got.Plan.Rewrites, got.Plan.Names)
}

// shownSources are sources as the retrieval event reports them.
func shownSources(sources []Source) []Source {
	out := make([]Source, len(sources))
	for i, s := range sources {
		out[i] = s.shown()
	}
	return out
}

// grouped reports whether the interests have a done rebuild to answer
// from; without a store to read one from, they haven't.
func (a *Asker) grouped(ctx context.Context) (bool, error) {
	if a.stores.Interests == nil {
		return false, nil
	}
	_, err := a.stores.Interests.LatestRun(ctx, a.stores.TenantID, store.InterestRunDone)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read the latest rebuild of the interests: %w", err)
	}
	return true, nil
}

// noSourcesReason says why an ask found nothing to answer from.
func noSourcesReason(unreadable []Unreadable) string {
	if len(unreadable) > 0 {
		return "the pages the search found are ones curio couldn't read"
	}
	return "the search found no saved page to answer from"
}

// refusal is the Done of an ask refused before anything is read or
// asked: Ask off or its provider unable to stream, withheld, or its model
// found unfit.
func (a *Asker) refusal() (Done, bool) {
	switch {
	case a.offReason != "":
		return Done{Status: StatusOff, Reason: a.offReason}, true
	case a.opts.Withheld != "":
		return Done{Status: StatusWithheld, Reason: a.opts.Withheld}, true
	}
	if s := a.gate.State(); s.Check == modelgate.CheckUnfit {
		return Done{Status: StatusUnfit, Reason: s.Unfit}, true
	}
	return Done{}, false
}

// refusedDone is the Done of an ask the gate held back from its model.
func refusedDone(ref modelgate.Refusal) Done {
	status := StatusUnavailable
	switch ref.Kind {
	case modelgate.Unfit:
		status = StatusUnfit
	case modelgate.TimeoutHeld:
		status = StatusTimeout
	case modelgate.Unavailable, modelgate.Cold, modelgate.Checking:
	}
	return Done{Status: status, Reason: ref.Reason}
}

// ended is how an ask stopped by err ends: the caller's context ending is
// its error; the daemon's stop is StatusUnavailable (its lifetime is read
// too, since the watcher that cancels the ask runs apart and may not have
// yet), the ask's deadline StatusTimeout; any other error is the error.
func (r *run) ended(ctx, actx context.Context, err error) (Done, error) {
	if cerr := ctx.Err(); cerr != nil {
		return Done{}, cerr
	}
	switch cause := context.Cause(actx); {
	case errors.Is(cause, errStopping) || r.a.stopped():
		return Done{Status: StatusUnavailable, Reason: stoppingReason}, nil
	case errors.Is(cause, errAskDeadline):
		return Done{Status: StatusTimeout, Reason: r.a.deadlineReason()}, nil
	case err == nil:
		return Done{}, fmt.Errorf("ask: stopped without a cause: %w", actx.Err())
	}
	return Done{}, err
}

// stopped reports whether the daemon's lifetime, which the asker was
// built on, has ended.
func (a *Asker) stopped() bool { return a.lifetime.Err() != nil }

// deadlineReason says an ask ran past its deadline.
func (a *Asker) deadlineReason() string {
	return fmt.Sprintf("the ask took longer than %s (ask.timeout_seconds)", a.opts.Timeout)
}

// State is what the asker knows of its model, read from memory alone:
// whether Ask is on, and on by default (for a model measured as the
// answer writer, with ask.enabled left out), why no question is answered
// at all (Reason: off, off by default, a provider that can't stream, or
// withheld), the fitness check's verdict and when it was reached, the last
// ask's status, the holds in effect and the last failure.
type State struct {
	On                bool
	ByDefault         bool
	Model             string
	Reason            string
	Check             string
	CheckedAt         time.Time
	Unfit             string
	Last              Status
	LastAt            time.Time
	TimeoutHoldUntil  time.Time
	EvictionHoldUntil time.Time
	Failure           modelgate.Failure
}

// State reads the asker's state, asking the model nothing.
func (a *Asker) State() State {
	g := a.gate.State()
	s := State{On: a.opts.On, ByDefault: a.opts.ByDefault, Model: a.opts.Model.Model, Reason: a.offReason,
		Check:     g.Check,
		CheckedAt: g.CheckedAt, Unfit: g.Unfit, TimeoutHoldUntil: g.TimeoutHoldUntil,
		EvictionHoldUntil: g.EvictionHoldUntil, Failure: g.Failure}
	if s.Reason == "" {
		s.Reason = a.opts.Withheld
	}
	a.mu.Lock()
	s.Last, s.LastAt = a.last, a.lastAt
	a.mu.Unlock()
	return s
}
