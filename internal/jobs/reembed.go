package jobs

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/store"
)

// The re-embed of a library for a new chunk context (indexer.
// ContextVersion): each fetched document holding chunks embedded under an
// older version gets an index job, marked Reembed, fed to the queue a few
// at a time (DocumentStore.EnqueueReembed) rather than all at once. Which
// documents are left is read from their chunks and the jobs table each
// time, so a restart resumes where the last left off, and no document is
// queued twice.
//
// Fed, not queued whole, because claims go in the order jobs became
// runnable: the whole library queued would hold a page saved meanwhile
// behind it, up to the whole re-embed, and keep every index worker
// re-embedding at once, so that a search's query embedding waited behind
// four batches. With reembedInFlight in flight, a page saved meanwhile is
// indexed next, the query embedding waits behind at most one batch, and an
// Ollama outage catches one job. Ollama embeds one request at a time, so
// one stream re-embeds as fast as four (decisions.md "Chunks embedded with
// their context").
//
// A re-embed job never fails its document (markDocFailed): the document
// keeps its earlier vectors, searchable, and is fed again, reembedFailedWait
// after its failure, until reembedMaxFailures of its own.
//
// A failure every document meets (an embedding model that answers with an
// error, a model missing, a width that doesn't fit) is held to a few
// documents three ways: at most reembedWaiting re-embed jobs wait out a
// retry or a deferral besides the one in flight; a document whose re-embed
// failed for good isn't fed again for reembedFailedWait; and after
// reembedBrakeAfter documents' first failures in a row, the brake stops the
// feed for reembedBrakeMin, doubling to reembedBrakeMax while they keep
// failing, until a re-embed is done. A few documents failing for their own
// reasons hold up none of the others: the feed queues the documents that
// never failed first, and a document failing again doesn't count.

// reembedInFlight is how many re-embed jobs may run, or wait to and be
// due, at once.
const reembedInFlight = 1

// reembedWaiting is how many more re-embed jobs may wait out a retry or a
// deferral: enough that a few documents failing for their own reasons
// don't stall the feed, few enough that a failure of every document parks
// a handful of them, not the library, and puts few retries ahead of a page
// saved meanwhile (claims go by run_after).
const reembedWaiting = 4

// reembedFailedWait is how long after a document's re-embed failed for good
// it isn't fed again: one a verdict fails at once would otherwise be fed
// again at once, and again.
const reembedFailedWait = 15 * time.Minute

// The brake: after reembedBrakeAfter documents' re-embeds failed for the
// first time in a row, the feed stops for reembedBrakeMin, then twice as
// long each time it stops again without a re-embed done in between, up to
// reembedBrakeMax.
const (
	reembedBrakeAfter = 3
	reembedBrakeMin   = 15 * time.Minute
	reembedBrakeMax   = 4 * time.Hour
)

// Watching the re-embed: a check every 30 s, which tops it up and reads
// how far it got, its rate over the last 10 minutes, and a progress line
// at most every 5 minutes, about 16 through the owner's library's 80
// minutes.
const (
	reembedCheckEvery    = 30 * time.Second
	reembedProgressEvery = 5 * time.Minute
	reembedRateWindow    = 10 * time.Minute
)

// ReembedStore is what the re-embed reads and queues through;
// store.DocumentStore is one.
type ReembedStore interface {
	CountToReembed(ctx context.Context, tenantID string, version int) (store.ReembedCount, error)
	EnqueueReembed(ctx context.Context, tenantID string, version int, limits store.ReembedLimits) (int, error)
}

// FreshOwer owes the interests a fresh rebuild; store.InsightStore is one.
type FreshOwer interface {
	OweFresh(ctx context.Context, tenantID string, reason store.FreshReason) error
}

// ReembedOptions are a Reembed's collaborators and its pace.
type ReembedOptions struct {
	Documents ReembedStore
	Insights  FreshOwer
	TenantID  string
	Log       *slog.Logger
	// InFlight, Waiting, FailedWait, CheckEvery, ProgressEvery and
	// RateWindow are reembedInFlight, reembedWaiting, reembedFailedWait,
	// reembedCheckEvery, reembedProgressEvery and reembedRateWindow when
	// zero.
	InFlight, Waiting                     int
	FailedWait                            time.Duration
	CheckEvery, ProgressEvery, RateWindow time.Duration
	// BrakeAfter, BrakeMin and BrakeMax are reembedBrakeAfter,
	// reembedBrakeMin and reembedBrakeMax when zero.
	BrakeAfter         int
	BrakeMin, BrakeMax time.Duration
}

// Reembed feeds the re-embed to the index pool and says how it goes: Start
// begins it at the daemon's start, Run tops it up at each Kick (each index
// job's outcome recorded) and each check until no document is left, and
// Progress reports how far it got, for /v1/stats, and its brake, for
// /v1/healthz. The index handler tells it each re-embed's outcome
// (Reembedded, Failed), which the brake goes by.
type Reembed struct {
	opts ReembedOptions
	kick chan struct{}

	// feed is held by a top-up (topUp), from its read of the brake to its
	// commit, and by a failure's report (Failed), so that neither lands in
	// the middle of the other. It is taken before mu.
	feed sync.Mutex

	mu       sync.Mutex
	progress ReembedProgress
	samples  []reembedSample // (when, chunks left) over the rate window, oldest first
	brake    brake
}

// brake is the re-embed's brake: the documents whose re-embeds failed
// since the daemon started, those of them that failed for the first time
// since the last document re-embedded, in turn, and while it is on, until
// when.
type brake struct {
	failed  map[string]struct{}
	streak  []string
	lastErr string        // the last of the streak's errors
	until   time.Time     // zero while it is off
	next    time.Duration // how long it stops the feed the next time it goes on
}

// reembedBrakeNamed is how many of the documents whose failures put the
// brake on it names.
const reembedBrakeNamed = 5

// ReembedBrake is the re-embed's brake while it is on: until when it stops
// the feed, how many documents' re-embeds failed in a row, the first
// reembedBrakeNamed of them, and the last one's error. The zero value is a
// brake that is off.
type ReembedBrake struct {
	Until     time.Time
	Failures  int
	Documents []string
	Err       string
}

// On reports whether the brake is on.
func (b ReembedBrake) On() bool { return !b.Until.IsZero() }

// reembedSample is how many chunks were left to re-embed at a check.
type reembedSample struct {
	at     time.Time
	chunks int
}

// ReembedProgress is how far the re-embed got, as its last check read it.
type ReembedProgress struct {
	// Active: the re-embed began at this daemon's start and isn't done.
	Active bool
	Began  time.Time
	// Of is what was left to re-embed when it began, and Left what was at
	// the last check.
	Of, Left store.ReembedCount
	// ChunksPerSecond is the rate chunks left the stale range over the
	// last RateWindow; 0 until two checks apart, and while nothing moves.
	ChunksPerSecond float64
	// Brake is the brake while it stops the feed; zero while it doesn't.
	Brake ReembedBrake
}

// ETA is how long chunks take at the rate measured; 0 when it is unknown.
func (p ReembedProgress) ETA(chunks int) time.Duration {
	return ReembedETA(chunks, p.ChunksPerSecond)
}

// ReembedETA is how long chunks take at rate chunks a second, to the
// second; 0 when the rate is unknown (0).
func ReembedETA(chunks int, rate float64) time.Duration {
	if rate <= 0 || chunks <= 0 {
		return 0
	}
	return (time.Duration(float64(chunks)/rate) * time.Second).Round(time.Second)
}

// NewReembed returns a re-embed with opts, not yet begun.
func NewReembed(opts ReembedOptions) *Reembed {
	opts.InFlight = cmp.Or(opts.InFlight, reembedInFlight)
	opts.Waiting = cmp.Or(opts.Waiting, reembedWaiting)
	opts.FailedWait = cmp.Or(opts.FailedWait, reembedFailedWait)
	opts.CheckEvery = cmp.Or(opts.CheckEvery, reembedCheckEvery)
	opts.ProgressEvery = cmp.Or(opts.ProgressEvery, reembedProgressEvery)
	opts.RateWindow = cmp.Or(opts.RateWindow, reembedRateWindow)
	opts.BrakeAfter = cmp.Or(opts.BrakeAfter, reembedBrakeAfter)
	opts.BrakeMin = cmp.Or(opts.BrakeMin, reembedBrakeMin)
	opts.BrakeMax = cmp.Or(opts.BrakeMax, reembedBrakeMax)
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &Reembed{opts: opts, kick: make(chan struct{}, 1),
		brake: brake{failed: map[string]struct{}{}, next: opts.BrakeMin}}
}

// limits are what the feed queues within.
func (r *Reembed) limits() store.ReembedLimits {
	return store.ReembedLimits{InFlight: r.opts.InFlight, Waiting: r.opts.Waiting, FailedWait: r.opts.FailedWait}
}

// Start begins the re-embed when documents are left to re-embed: it owes
// the interests a fresh rebuild (store.FreshReindex), as `curio reindex
// --all` does, since every vector moves and a warm start from the old ones
// would carry their grouping over; queues the first jobs; and logs one
// INFO with the counts. It reports whether Run has a re-embed to feed. The
// cards are left alone, their text and model unchanged, and so is the
// drift monitor's baseline: the cards keep the build they were embedded
// by, so a drift reported stays reported.
func (r *Reembed) Start(ctx context.Context) (bool, error) {
	left, err := r.opts.Documents.CountToReembed(ctx, r.opts.TenantID, indexer.ContextVersion)
	if err != nil {
		return false, err
	}
	if left.Documents == 0 {
		return false, nil
	}
	if err := r.opts.Insights.OweFresh(ctx, r.opts.TenantID, store.FreshReindex); err != nil {
		return false, fmt.Errorf("owe the interests a fresh rebuild: %w", err)
	}
	queued, err := r.opts.Documents.EnqueueReembed(ctx, r.opts.TenantID, indexer.ContextVersion, r.limits())
	if err != nil {
		return false, err
	}
	now := time.Now()
	r.mu.Lock()
	r.progress = ReembedProgress{Active: true, Began: now, Of: left, Left: left}
	r.samples = []reembedSample{{at: now, chunks: left.Chunks}}
	r.mu.Unlock()
	r.opts.Log.Info("chunk context: re-embedding the library's chunks", "documents", left.Documents,
		"chunks", left.Chunks, "queued", queued, "in_flight", r.opts.InFlight,
		"context_version", indexer.ContextVersion)
	return true, nil
}

// Kick asks Run to top the re-embed up now. It never blocks, and does
// nothing once Run has returned. A nil Reembed does nothing.
func (r *Reembed) Kick() {
	if r == nil {
		return
	}
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Progress is how far the re-embed got at its last check, and its brake
// while it is on: a brake whose time has passed is off, as the next check
// would find it.
func (r *Reembed) Progress() ReembedProgress {
	if r == nil {
		return ReembedProgress{}
	}
	r.mu.Lock()
	lifted := r.lift(time.Now())
	p := r.progress
	if b := r.brake; !b.until.IsZero() {
		p.Brake = ReembedBrake{Until: b.until, Failures: len(b.streak),
			Documents: slices.Clone(b.streak[:min(len(b.streak), reembedBrakeNamed)]), Err: b.lastErr}
	}
	r.mu.Unlock()
	r.logLift(lifted)
	return p
}

// Reembedded reports that a re-embed job re-embedded its document: the
// embedding model embeds, so the streak of documents that failed before it
// is over, and the brake is off, its next stop back to BrakeMin. A brake
// that was on logs one INFO. A nil Reembed does nothing.
func (r *Reembed) Reembedded() {
	if r == nil {
		return
	}
	r.mu.Lock()
	on := !r.brake.until.IsZero()
	r.brake.streak, r.brake.lastErr, r.brake.until, r.brake.next = nil, "", time.Time{}, r.opts.BrakeMin
	r.mu.Unlock()
	if on {
		r.opts.Log.Info("chunk context: a document was re-embedded; the re-embed resumes")
	}
}

// Failed reports that a re-embed job's attempt at documentID failed with
// err, a verdict or an error to retry: nothing the job deferred, and
// nothing a shutdown interrupted. Only a document's first failure since the
// daemon started counts: one that failed before may be failing for its own
// reasons, and its failing again says nothing of the library. Once
// BrakeAfter documents failed for the first time with none re-embedded
// since, the brake goes on, unless it is on already: the feed stops until
// the brake's time passes, which doubles, up to BrakeMax, each time it goes
// on again, with one WARN. The brake is off again once that time has passed
// (at the next check, or read), and a first failure after it puts it
// straight back on, so the one document the feed queues then, one that
// never failed if any is left (DocumentStore.EnqueueReembed), is a probe
// of whether the failure lasts. A nil Reembed does nothing.
//
// The failure must be reported while its job still runs, before the worker
// records its outcome, as the index handler does; the report waits for a
// top-up under way (feed). So a top-up that counts the job out of flight,
// and may queue a document in its place, reads the brake with this failure
// in it. Otherwise one that read the brake off just before the failure that
// puts it on would queue one more document once the job left flight, and
// the brake would stop the feed a failure late.
func (r *Reembed) Failed(documentID string, err error) {
	if r == nil {
		return
	}
	r.feed.Lock()
	defer r.feed.Unlock()
	r.mu.Lock()
	b := &r.brake
	lifted := r.lift(time.Now())
	if _, before := b.failed[documentID]; before {
		r.mu.Unlock()
		r.logLift(lifted)
		return
	}
	b.failed[documentID] = struct{}{}
	b.streak = append(b.streak, documentID)
	b.lastErr = err.Error()
	if len(b.streak) < r.opts.BrakeAfter || !b.until.IsZero() {
		r.mu.Unlock()
		r.logLift(lifted)
		return
	}
	pause := b.next
	b.until, b.next = time.Now().Add(pause), min(2*pause, r.opts.BrakeMax)
	until, failures := b.until, len(b.streak)
	documents := slices.Clone(b.streak[:min(len(b.streak), reembedBrakeNamed)])
	r.mu.Unlock()
	r.logLift(lifted)
	r.opts.Log.Warn("chunk context: documents' re-embeds keep failing; the re-embed stops queueing them for a "+
		"while, and they keep their earlier vectors", "failures", failures, "document_ids", documents, "until", until,
		"pause", pause.String(), "err", err)
}

// braked reports whether the brake stops the feed at now, and turns it off,
// with one INFO, once its time has passed.
func (r *Reembed) braked(now time.Time) bool {
	r.mu.Lock()
	lifted := r.lift(now)
	on := !r.brake.until.IsZero()
	r.mu.Unlock()
	r.logLift(lifted)
	return on
}

// lift turns the brake off when its time has passed at now, and reports
// whether it did. r.mu must be held.
func (r *Reembed) lift(now time.Time) bool {
	if r.brake.until.IsZero() || now.Before(r.brake.until) {
		return false
	}
	r.brake.until = time.Time{}
	return true
}

// logLift logs the INFO of a brake lift turned off, when it did.
func (r *Reembed) logLift(lifted bool) {
	if lifted {
		r.opts.Log.Info("chunk context: the re-embed tries again after its pause")
	}
}

// Run feeds the re-embed Start began until no document is left to
// re-embed, or ctx ends. At each Kick it tops it up (EnqueueReembed),
// unless the brake is on; at each check, every CheckEvery, it does too and
// reads how much is left:
//
//   - it logs INFO "chunk context: re-embedding" (documents and chunks
//     left, of, the rate and the time left at it) when the chunks left
//     changed and ProgressEvery passed since its last line, so a paused
//     queue logs nothing;
//   - once none is left, it logs INFO "chunk context: re-embedded the
//     library's chunks" and returns;
//   - a top-up or a read that fails is one WARN at most every
//     ProgressEvery, and the next tries again.
func (r *Reembed) Run(ctx context.Context) {
	tick := time.NewTicker(r.opts.CheckEvery)
	defer tick.Stop()
	began := r.Progress().Began
	lastLine := began
	var lastWarn time.Time
	warn := func(msg string, err error) {
		if ctx.Err() == nil && time.Since(lastWarn) >= r.opts.ProgressEvery {
			r.opts.Log.Warn(msg, "err", err)
			lastWarn = time.Now()
		}
	}
	for {
		check := false
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
		case <-tick.C:
			check = true
		}
		if err := r.topUp(ctx); err != nil {
			warn("chunk context: couldn't queue the next documents to re-embed; trying again at the next check", err)
		}
		if !check {
			continue
		}
		left, err := r.opts.Documents.CountToReembed(ctx, r.opts.TenantID, indexer.ContextVersion)
		if err != nil {
			warn("chunk context: couldn't read how far the re-embed got; reading again at the next check", err)
			continue
		}
		p, moved := r.record(time.Now(), left)
		took := time.Since(began).Milliseconds()
		if left.Documents == 0 {
			r.opts.Log.Info("chunk context: re-embedded the library's chunks", "documents", p.Of.Documents,
				"chunks", p.Of.Chunks, "took_ms", took)
			return
		}
		if moved && time.Since(lastLine) >= r.opts.ProgressEvery {
			r.opts.Log.Info("chunk context: re-embedding", "documents_left", left.Documents,
				"chunks_left", left.Chunks, "of_documents", p.Of.Documents, "of_chunks", p.Of.Chunks,
				"chunks_per_s", round1(p.ChunksPerSecond), "eta", p.ETA(left.Chunks).String(), "took_ms", took)
			lastLine = time.Now()
		}
	}
}

// topUp queues the next documents to re-embed (EnqueueReembed), unless the
// brake is on, holding feed from its read of the brake to its commit: a
// failure reported meanwhile (Failed) waits for it, so the brake it read
// holds every failure of a job its count finds out of flight.
func (r *Reembed) topUp(ctx context.Context) error {
	r.feed.Lock()
	defer r.feed.Unlock()
	if r.braked(time.Now()) {
		return nil
	}
	_, err := r.opts.Documents.EnqueueReembed(ctx, r.opts.TenantID, indexer.ContextVersion, r.limits())
	return err
}

// record notes that left was left at now: the progress, and the rate over
// the samples within the rate window (and the last one before it, so the
// window is spanned). moved reports whether the chunks left changed since
// the check before.
func (r *Reembed) record(now time.Time, left store.ReembedCount) (p ReembedProgress, moved bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	moved = left != r.progress.Left
	r.samples = append(r.samples, reembedSample{at: now, chunks: left.Chunks})
	for len(r.samples) > 2 && now.Sub(r.samples[1].at) >= r.opts.RateWindow {
		r.samples = r.samples[1:]
	}
	first := r.samples[0]
	r.progress.Left, r.progress.ChunksPerSecond = left, 0
	if span := now.Sub(first.at).Seconds(); span > 0 && first.chunks > left.Chunks {
		r.progress.ChunksPerSecond = float64(first.chunks-left.Chunks) / span
	}
	r.progress.Active = left.Documents > 0
	return r.progress, moved
}

// round1 is x to one decimal place, for a log line.
func round1(x float64) float64 { return math.Round(x*10) / 10 }
