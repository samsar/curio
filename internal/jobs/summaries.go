package jobs

import (
	"cmp"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/summarize"
)

// The summary pass: each fetched document without a summary of its current
// extraction, or with one owed, gets a summarize job, fed to the queue a
// few at a time (SummaryStore.EnqueueSummarize), newest indexed first, as
// the re-embed is fed: a pause or the schedule closing takes effect within
// one document, a page saved meanwhile is summarized next, and an outage
// catches one job. What is left is read from the summaries and the jobs
// table each time, so a restart resumes where the last left off.
//
// A failure every document meets (a model that answers with an error) is
// held to a few documents: at most summariesWaiting jobs wait out a retry
// besides the one in flight, a document whose summary failed for good
// isn't fed again for SummariesFailedWait, and after summariesBrakeAfter
// documents' first failures in a row the brake stops the feed for
// summariesBrakeStart, doubling to summariesBrakeMax while they keep
// failing. A model that doesn't answer, or whose fitness check waits to
// run again, stops the feed but for one job, whose deferrals are the probe
// that sees it answer again; one the provider doesn't have, or that Ollama
// keeps unloading, stops it whole (summarize.Writer.Feed): a pull is seen
// by the feed's checks (Writer.Probe), and a hold ends by its time.
//
// A job that meets the check's wait, the hold or the missing model is done
// at once, so the feed must never queue one due while the job would only
// meet it: the one job kept while the check waits is queued for the wait's
// end, on the wall clock as run_after is (Writer.CheckRetryAt), whatever
// ended the one before it (its day of deferrals spent, a restart, a search
// pre-empting it), and none at all while the writer feeds none. Each job's
// outcome reaches the feed while the job still runs (Failed, Settled), so
// a top-up that read the writer before the job changed it counts that job
// in flight and queues nothing.

const (
	// summariesInFlight is how many summarize jobs may run, or wait to and
	// be due, at once: the pool's one worker.
	summariesInFlight = 1
	// summariesWaiting is how many more may wait out a retry or a
	// deferral.
	summariesWaiting = 2
	// SummariesFailedWait is how long after a document's summary failed
	// for good, under its current extraction and the writer, it isn't fed
	// again: a page that defeats the model now likely does tomorrow, and a
	// week costs the pass nothing.
	SummariesFailedWait = 7 * 24 * time.Hour
	// The brake, as the re-embed's.
	summariesBrakeAfter = 3
	summariesBrakeStart = 15 * time.Minute
	summariesBrakeMax   = 4 * time.Hour
	// summariesCheckEvery is how often the feed checks when nothing kicks
	// it: what it found then (the backlog empty, or a page the kicks
	// missed), and the progress line.
	summariesCheckEvery = 5 * time.Minute
	// summariesProgressEvery spaces the progress lines, and
	// summariesRateWindow is what the rate is measured over.
	summariesProgressEvery = 5 * time.Minute
	summariesRateWindow    = 10 * time.Minute
	// summaryCountsFresh is how long the counts read for /v1/stats are
	// served again before they are read anew, unless the feed hears of a
	// change first: counting scans every fetched document (17 to 22 ms at
	// the median on a 5,329-page library, up to 170 ms beside the pass's
	// work), and the dashboard's Status page asks every 2 s.
	summaryCountsFresh = 10 * time.Second
)

// SummaryFeedStore is what the pass reads and queues through;
// store.SummaryStore is one.
type SummaryFeedStore interface {
	CountSummaries(ctx context.Context, tenantID string, w store.SummaryWriter) (store.SummaryCounts, error)
	EnqueueSummarize(ctx context.Context, tenantID string, w store.SummaryWriter, limits store.SummarizeLimits) (int,
		error)
}

// SummaryFeedWriter is what the feed asks of the writer; *summarize.Writer
// is one.
type SummaryFeedWriter interface {
	Identity() store.SummaryWriter
	// Refusal is why it writes nothing for good, "" while it may.
	Refusal() string
	// Feed says how many jobs the pass may keep queued now.
	Feed() summarize.Feed
	// CheckRetryAt is when its fitness check runs again while it waits,
	// zero when it doesn't: no job queued may run sooner.
	CheckRetryAt() time.Time
	// Probe asks, cheaply, whether a model that wasn't pulled is now.
	Probe(ctx context.Context)
}

// SummariesOptions are a Summaries' collaborators and its pace; the zero
// durations and counts are the constants above.
type SummariesOptions struct {
	Store    SummaryFeedStore
	Writer   SummaryFeedWriter
	TenantID string
	Log      *slog.Logger

	InFlight, Waiting                     int
	FailedWait                            time.Duration
	CheckEvery, ProgressEvery, RateWindow time.Duration
	CountsFresh                           time.Duration
	BrakeAfter                            int
	BrakeMin, BrakeMax                    time.Duration
}

// Summaries feeds the summary pass to the summarize pool and says how it
// goes: Start begins it at the daemon's start, Run tops it up at each Kick
// (a summarize or index job's outcome recorded, a rewrite asked for, an
// outage ending) and each check, and Progress reports its rate, for
// /v1/stats, and its brake, for /v1/healthz. The summarize handler tells it
// each document's outcome (Written, Failed), which the brake goes by.
type Summaries struct {
	opts SummariesOptions
	kick chan struct{}

	// feed is held by a top-up from its read of the brake to its commit,
	// and by a failure's report, so neither lands in the middle of the
	// other (Reembed.feed). It is taken before mu.
	feed sync.Mutex

	mu       sync.Mutex
	brake    brake
	written  []time.Time // when each summary was written, over the rate window
	lastLine time.Time
	drained  bool // the last check found nothing left to write
	// counts are the counts last read, at countedAt (zero for none);
	// changes counts the changes the feed heard of, each of which drops
	// them, so a read begun before one isn't kept.
	counts    store.SummaryCounts
	countedAt time.Time
	changes   uint64
}

// SummaryProgress is how the pass goes: summaries written a minute over
// the rate window (0 until one is), and the brake while it is on.
type SummaryProgress struct {
	PerMinute float64
	Brake     Brake
}

// ETA is how long left documents take at the rate measured; 0 when it is
// unknown.
func (p SummaryProgress) ETA(left int) time.Duration {
	if p.PerMinute <= 0 || left <= 0 {
		return 0
	}
	return (time.Duration(float64(left)/p.PerMinute*60) * time.Second).Round(time.Second)
}

// NewSummaries returns a pass with opts, not yet begun.
func NewSummaries(opts SummariesOptions) *Summaries {
	opts.InFlight = cmp.Or(opts.InFlight, summariesInFlight)
	opts.Waiting = cmp.Or(opts.Waiting, summariesWaiting)
	opts.FailedWait = cmp.Or(opts.FailedWait, SummariesFailedWait)
	opts.CheckEvery = cmp.Or(opts.CheckEvery, summariesCheckEvery)
	opts.ProgressEvery = cmp.Or(opts.ProgressEvery, summariesProgressEvery)
	opts.RateWindow = cmp.Or(opts.RateWindow, summariesRateWindow)
	opts.CountsFresh = cmp.Or(opts.CountsFresh, summaryCountsFresh)
	opts.BrakeAfter = cmp.Or(opts.BrakeAfter, summariesBrakeAfter)
	opts.BrakeMin = cmp.Or(opts.BrakeMin, summariesBrakeStart)
	opts.BrakeMax = cmp.Or(opts.BrakeMax, summariesBrakeMax)
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &Summaries{opts: opts, kick: make(chan struct{}, 1),
		brake: newBrake(opts.BrakeAfter, opts.BrakeMin, opts.BrakeMax)}
}

// Start counts what the pass has to write and queues the first jobs, with
// one INFO. A writer that writes nothing starts no pass: the summarize jobs
// left from before are done as they come, without a call.
func (s *Summaries) Start(ctx context.Context) error {
	if s.opts.Writer.Refusal() != "" {
		return nil
	}
	n, err := s.opts.Store.CountSummaries(ctx, s.opts.TenantID, s.opts.Writer.Identity())
	if err != nil {
		return err
	}
	queued, err := s.topUp(ctx)
	if err != nil {
		return err
	}
	s.opts.Log.Info("document summaries: the pass", "to_write", n.ToWrite, "written", n.Written,
		"outdated", n.Outdated, "failed", n.Failed, "not_the_page", n.NotThePage, "queued", queued,
		"model", s.opts.Writer.Identity().Model)
	return nil
}

// Kick asks Run to top the pass up now: something may owe a summary, or
// write one (an index or summarize job's end, a rewrite asked for), so the
// counts are read anew. It never blocks. A nil Summaries does nothing.
func (s *Summaries) Kick() {
	if s == nil {
		return
	}
	s.changed()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run feeds the pass until ctx ends: at each Kick it tops it up, and at
// each check too, reading how much is left: a progress line at most every
// ProgressEvery while summaries are written, and one INFO when nothing is
// left to write. A top-up or a read that fails is one WARN at most every
// ProgressEvery.
func (s *Summaries) Run(ctx context.Context) {
	if s.opts.Writer.Refusal() != "" {
		return
	}
	tick := time.NewTicker(s.opts.CheckEvery)
	defer tick.Stop()
	var lastWarn time.Time
	warn := func(msg string, err error) {
		if ctx.Err() == nil && time.Since(lastWarn) >= s.opts.ProgressEvery {
			s.opts.Log.Warn(msg, "err", err)
			lastWarn = time.Now()
		}
	}
	for {
		check := false
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-tick.C:
			check = true
		}
		if check {
			// A model that wasn't pulled keeps no job to see it pulled:
			// each check asks the provider's listing instead.
			s.opts.Writer.Probe(ctx)
		}
		if _, err := s.topUp(ctx); err != nil {
			warn("document summaries: couldn't queue the next pages; trying again at the next check", err)
		}
		if !check {
			continue
		}
		if err := s.check(ctx); err != nil {
			warn("document summaries: couldn't read how far the pass got; reading again at the next check", err)
		}
	}
}

// Counts are the fetched documents by their summary under the writer now
// (SummaryFeedStore.CountSummaries), and when they were read: served
// again for CountsFresh after a read, unless the feed hears of a change
// first (a kick, a summary written, a failure reported), so /v1/stats,
// polled every 2 s by the dashboard, scans the library once in that time
// at most. Changes it doesn't hear of, a document deleted or refetched
// before its index job ends, show within CountsFresh.
func (s *Summaries) Counts(ctx context.Context) (store.SummaryCounts, time.Time, error) {
	now := time.Now()
	s.mu.Lock()
	if !s.countedAt.IsZero() && now.Sub(s.countedAt) < s.opts.CountsFresh {
		n, at := s.counts, s.countedAt
		s.mu.Unlock()
		return n, at, nil
	}
	changes := s.changes
	s.mu.Unlock()
	n, err := s.opts.Store.CountSummaries(ctx, s.opts.TenantID, s.opts.Writer.Identity())
	if err != nil {
		return store.SummaryCounts{}, time.Time{}, err
	}
	s.mu.Lock()
	if s.changes == changes {
		s.counts, s.countedAt = n, now
	}
	s.mu.Unlock()
	return n, now, nil
}

// changed drops the counts read: the feed heard of a change.
func (s *Summaries) changed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changes++
	s.countedAt = time.Time{}
}

// check reads what is left to write, logging the pass's progress at most
// every ProgressEvery while it moves, and once when it is done.
func (s *Summaries) check(ctx context.Context) error {
	n, _, err := s.Counts(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	s.mu.Lock()
	p := s.progressLocked(now)
	wasDrained := s.drained
	s.drained = n.ToWrite == 0
	logLine := p.PerMinute > 0 && n.ToWrite > 0 && now.Sub(s.lastLine) >= s.opts.ProgressEvery
	if logLine {
		s.lastLine = now
	}
	s.mu.Unlock()
	switch {
	case n.ToWrite == 0 && !wasDrained:
		s.opts.Log.Info("document summaries: every page has its summary; new pages get theirs as they are indexed",
			"written", n.Written, "outdated", n.Outdated, "failed", n.Failed, "not_the_page", n.NotThePage)
	case logLine:
		s.opts.Log.Info("document summaries: writing", "to_write", n.ToWrite, "written", n.Written,
			"per_minute", round1(p.PerMinute), "eta", p.ETA(n.ToWrite).String())
	}
	return nil
}

// topUp queues the next documents (EnqueueSummarize), unless the brake is
// on or the writer says to feed none, holding feed from its read of the
// brake to its commit. While the model doesn't answer, or its check waits
// to run again, it keeps one job queued, no more: its deferrals are what
// sees the model answer again; while the check waits, that job runs no
// sooner than the wait's end, since one due sooner would meet the wait and
// be done at once, and the next top-up would queue another, one every few
// milliseconds until the wait ended.
func (s *Summaries) topUp(ctx context.Context) (int, error) {
	s.feed.Lock()
	defer s.feed.Unlock()
	if s.braked(time.Now()) {
		return 0, nil
	}
	limits := store.SummarizeLimits{InFlight: s.opts.InFlight, Waiting: s.opts.Waiting, FailedWait: s.opts.FailedWait}
	switch s.opts.Writer.Feed() {
	case summarize.FeedNone:
		return 0, nil
	case summarize.FeedOne:
		limits.InFlight, limits.Waiting, limits.NotBefore = 1, 0, s.opts.Writer.CheckRetryAt()
	case summarize.FeedAll:
	}
	return s.opts.Store.EnqueueSummarize(ctx, s.opts.TenantID, s.opts.Writer.Identity(), limits)
}

// braked reports whether the brake stops the feed at now, and turns it off,
// with one INFO, once its time has passed.
func (s *Summaries) braked(now time.Time) bool {
	s.mu.Lock()
	lifted := s.brake.lift(now)
	on := s.brake.holds()
	s.mu.Unlock()
	s.logLift(lifted)
	return on
}

func (s *Summaries) logLift(lifted bool) {
	if lifted {
		s.opts.Log.Info("document summaries: the pass tries again after its pause")
	}
}

// Written reports that a summary was written: the streak of documents that
// failed before it is over, and the brake off, with one INFO if it was on.
// A nil Summaries does nothing.
func (s *Summaries) Written() {
	if s == nil {
		return
	}
	now := time.Now()
	s.changed()
	s.mu.Lock()
	on := s.brake.succeed()
	s.written = append(s.written, now)
	s.drained = false
	s.mu.Unlock()
	if on {
		s.opts.Log.Info("document summaries: a summary was written; the pass resumes")
	}
}

// Failed reports that a summarize job's attempt at documentID failed with
// err, a verdict or an error to retry: nothing deferred, pre-empted or
// interrupted. Like Reembed.Failed it must be reported while the job still
// runs, and waits for a top-up under way, so the brake a top-up reads holds
// every failure of a job it counts out of flight. A nil Summaries does
// nothing.
func (s *Summaries) Failed(documentID string, err error) {
	if s == nil {
		return
	}
	s.feed.Lock()
	defer s.feed.Unlock()
	now := time.Now()
	s.changed()
	s.mu.Lock()
	lifted := s.brake.lift(now)
	on, pause := s.brake.fail(documentID, err, now)
	state := s.brake.state()
	s.mu.Unlock()
	s.logLift(lifted)
	if on {
		s.opts.Log.Warn("document summaries: pages' summaries keep failing; the pass stops queueing them for a while",
			"failures", state.Failures, "document_ids", state.Documents, "until", state.Until, "pause", pause.String(),
			"err", err)
	}
}

// Settled reports that a summarize job's attempt ended with no summary
// written and no failure for the brake: deferred, or nothing to do. Like
// Failed it must be reported while the job still runs, and waits for a
// top-up under way: that top-up counts the job in flight, so it queues
// nothing beside it, and every later one reads the writer as the attempt
// left it (an outage, the check's wait, an eviction hold, a model not
// pulled). Without it, a top-up that read the writer before the attempt
// changed it could count after the job was done, and queue a job the
// writer had just ruled out. A nil Summaries does nothing.
func (s *Summaries) Settled() {
	if s == nil {
		return
	}
	s.feed.Lock()
	defer s.feed.Unlock() // nothing to read or write: taking it waits out a top-up under way
}

// Progress is the pass's rate over the rate window and its brake while it
// is on: a brake whose time has passed is off, as the next top-up would
// find it.
func (s *Summaries) Progress() SummaryProgress {
	if s == nil {
		return SummaryProgress{}
	}
	now := time.Now()
	s.mu.Lock()
	lifted := s.brake.lift(now)
	p := s.progressLocked(now)
	s.mu.Unlock()
	s.logLift(lifted)
	return p
}

// progressLocked drops the writes past the rate window and measures the
// rate over the window, or over the time since the first write in it when
// the pass began within it. s.mu is held.
func (s *Summaries) progressLocked(now time.Time) SummaryProgress {
	cut := 0
	for cut < len(s.written) && now.Sub(s.written[cut]) > s.opts.RateWindow {
		cut++
	}
	s.written = s.written[cut:]
	p := SummaryProgress{Brake: s.brake.state()}
	if len(s.written) >= 2 {
		if span := now.Sub(s.written[0]); span > 0 {
			p.PerMinute = float64(len(s.written)) / span.Minutes()
		}
	}
	return p
}
