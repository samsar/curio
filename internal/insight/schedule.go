package insight

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samsar/curio/internal/store"
)

// The values that decide when interests are rebuilt on their own (see
// docs/decisions.md, "Interests: two levels, stable identities, automatic
// rebuilds"). ChangePercent is the owner's; the others are the design's,
// unmeasured.
const (
	// FirstRebuildAt is how many fetched documents a library with no done
	// rebuild waits for before its first: 35 is the smallest library the
	// grouping was measured on.
	FirstRebuildAt = 20
	// ChangePercent is the share of a run's documents that, changed since
	// it, make the next rebuild due; MinChanges is the fewest, which keeps
	// a small library from rebuilding at every document (Threshold).
	ChangePercent = 5
	MinChanges    = 5
	// SettleWindow is how long nothing must be indexed before a due
	// rebuild is queued, so an import is grouped once, when it ends, not
	// at every threshold it crosses on the way.
	SettleWindow = 10 * time.Minute
	// MaxWait caps how long a due rebuild waits for the library to settle,
	// so an import of many hours is still regrouped every MaxWait; a
	// library with no done rebuild waits MaxWaitFirst at most.
	MaxWait      = 2 * time.Hour
	MaxWaitFirst = 30 * time.Minute
	// CheckInterval is how often the scheduler checks when nothing asks it
	// to.
	CheckInterval = time.Minute
	// RetryAfter is how long a failed rebuild holds the next automatic one,
	// doubling with each failure in a row up to MaxRetryAfter, as the
	// embedding drift check retries.
	RetryAfter    = 15 * time.Minute
	MaxRetryAfter = 4 * time.Hour
)

// HoldFix is what lifts a hold on automatic rebuilds and placement:
// re-embedding the library under the build that serves now.
const HoldFix = "curio reindex --all"

// Threshold is how many documents changed since a run of n documents make
// the next rebuild due: ChangePercent of n, rounded up, and at least
// MinChanges. The split check's cadence counts in it too (Engine.plan).
func Threshold(n int) int { return max(MinChanges, (n*ChangePercent+99)/100) }

// RetryDelay is how long the failures-th failed rebuild in a row holds the
// next automatic one: RetryAfter, doubling with each failure, up to
// MaxRetryAfter.
func RetryDelay(failures int) time.Duration {
	d := RetryAfter
	for range failures - 1 {
		if d *= 2; d >= MaxRetryAfter {
			return MaxRetryAfter
		}
	}
	return d
}

// RetryAt is when the failures st counts stop holding automatic rebuilds:
// RetryDelay after the last. Zero with no failure.
func RetryAt(st store.InsightState) time.Time {
	if st.Failures == 0 {
		return time.Time{}
	}
	return st.LastFailureAt.Add(RetryDelay(st.Failures))
}

// SchedulerConfig times a Scheduler. A zero field takes the constant of its
// name; tests, and the timing knob of e2e builds, shorten them.
type SchedulerConfig struct {
	FirstRebuildAt int
	Settle         time.Duration // SettleWindow
	MaxWait        time.Duration
	MaxWaitFirst   time.Duration
	Interval       time.Duration // CheckInterval
}

// WithDefaults is c with each zero field set to its constant: the timing a
// Scheduler built with c runs on.
func (c SchedulerConfig) WithDefaults() SchedulerConfig {
	if c.FirstRebuildAt <= 0 {
		c.FirstRebuildAt = FirstRebuildAt
	}
	if c.Settle <= 0 {
		c.Settle = SettleWindow
	}
	if c.MaxWait <= 0 {
		c.MaxWait = MaxWait
	}
	if c.MaxWaitFirst <= 0 {
		c.MaxWaitFirst = MaxWaitFirst
	}
	if c.Interval <= 0 {
		c.Interval = CheckInterval
	}
	return c
}

// RebuildState is where a tenant's automatic rebuilds stand.
type RebuildState string

// Rebuild states, by precedence: a check reports the first that holds.
const (
	// StateRebuilding: a rebuild runs.
	StateRebuilding RebuildState = "rebuilding"
	// StateQueued: a rebuild waits in the queue, whose gate may hold it.
	StateQueued RebuildState = "queued"
	// StateHeld: the embeddings drifted, so nothing is rebuilt or placed
	// on its own until the library is re-embedded.
	StateHeld RebuildState = "held"
	// StateFailing: the last rebuild failed; the next is held until the
	// backoff passes.
	StateFailing RebuildState = "failing"
	// StateDue: enough changed (or a fresh rebuild is owed), and the
	// library hasn't settled yet.
	StateDue RebuildState = "due"
	// StateCurrent: the done rebuild is current.
	StateCurrent RebuildState = "current"
	// StateNone: no done rebuild, and too few documents for the first.
	StateNone RebuildState = "none"
	// StateUnknown: no check has succeeded yet.
	StateUnknown RebuildState = "unknown"
)

// FreshParams is Snapshot.FreshOwed when the next rebuild must be fresh
// because the grouper or its params changed since the done one, which the
// done run's own record says (Engine.ParamsChanged), so nothing stores it.
const FreshParams = "params"

// Snapshot is where a tenant's automatic rebuilds stood at a check. The
// fields that don't apply are zero.
type Snapshot struct {
	State RebuildState
	// The done rebuild's finish, kind and trigger.
	LastRebuildAt time.Time
	LastKind      store.RunKind
	LastTrigger   store.RunTrigger
	// Changed is what changed since the done rebuild, and RebuildAt the
	// threshold; with none done, the fetched documents and FirstRebuildAt.
	Changed   int
	RebuildAt int
	// DueSince is when the rebuild became due, while it is: the check
	// that saw it so, since this scheduler started.
	DueSince time.Time
	// FreshOwed is why the next rebuild must be fresh: a store.FreshReason
	// or FreshParams.
	FreshOwed string
	// HeldReason is the drift that holds automatic rebuilds, while held.
	HeldReason string
	// RetryAt and LastError are the failed rebuilds' backoff and error,
	// while failing.
	RetryAt   time.Time
	LastError string
	CheckedAt time.Time
}

// Library is what a check reads of a tenant's library and of the queue;
// NewLibrary reads the stores.
type Library interface {
	Read(ctx context.Context, tenantID string) (Reading, error)
}

// Reading is one check's read.
type Reading struct {
	// Done is the tenant's latest done run, nil before the first, and
	// Changes what changed since it read its vectors.
	Done    *store.InterestRun
	Changes store.RunChanges
	// Fetched counts the tenant's fetched documents, and LastIndexed is
	// when a document was last indexed: zero when none ever was.
	Fetched     int
	LastIndexed time.Time
	// The rebuilds queued and running, and the index jobs pending or
	// running, daemon-wide: the workers claim across tenants.
	ClusterPending, ClusterRunning, IndexBusy int
	State                                     store.InsightState
}

// NewLibrary is the Library of the daemon's stores.
func NewLibrary(insights store.InsightStore, docs store.DocumentStore, queue store.JobStore) Library {
	return storeLibrary{insights: insights, docs: docs, queue: queue}
}

type storeLibrary struct {
	insights store.InsightStore
	docs     store.DocumentStore
	queue    store.JobStore
}

func (l storeLibrary) Read(ctx context.Context, tenantID string) (Reading, error) {
	var r Reading
	done, err := l.insights.LatestRun(ctx, tenantID, store.InterestRunDone)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return Reading{}, fmt.Errorf("read the latest done run: %w", err)
	default:
		r.Done = done
		if r.Changes, err = l.insights.Changes(ctx, done); err != nil {
			return Reading{}, err
		}
	}
	states, err := l.docs.CountByState(ctx, tenantID)
	if err != nil {
		return Reading{}, err
	}
	r.Fetched = states[store.DocStateFetched]
	if r.LastIndexed, err = l.docs.LastIndexedAt(ctx, tenantID); err != nil {
		return Reading{}, err
	}
	queued, err := l.queue.QueueCounts(ctx)
	if err != nil {
		return Reading{}, err
	}
	cluster, index := queued[store.JobKindCluster], queued[store.JobKindIndex]
	r.ClusterPending, r.ClusterRunning, r.IndexBusy = cluster.Pending, cluster.Running, index.Pending+index.Running
	if r.State, err = l.insights.State(ctx, tenantID); err != nil {
		return Reading{}, err
	}
	return r, nil
}

// SchedulerOptions are what a Scheduler works with.
type SchedulerOptions struct {
	TenantID string
	Library  Library
	// Enqueue queues a rebuild for trigger, unless one is pending, and
	// returns the job (jobs.EnqueueRebuild).
	Enqueue func(ctx context.Context, tenantID string, trigger store.RunTrigger) (job *store.Job, queued bool, err error)
	// Drift says how the embeddings drifted, "" while they haven't: it
	// holds automatic rebuilds. nil reports no drift.
	Drift func() string
	// ParamsChanged reports whether a run was grouped by another grouper,
	// or with other params, than a rebuild now would be
	// (Engine.ParamsChanged). nil reports none.
	ParamsChanged func(*store.InterestRun) bool
	// Placer sweeps once when Run starts; nil sweeps nothing.
	Placer *Placer
	Config SchedulerConfig
	Now    func() time.Time // default time.Now
	Log    *slog.Logger     // default slog.Default()
}

// checkTimeout bounds one check: its reads and the enqueue.
const checkTimeout = 30 * time.Second

// Scheduler queues a tenant's rebuilds as the library changes. A check
// reads the library and the queue, decides where rebuilds stand
// (Snapshot), and queues one when all of these hold:
//
//   - no rebuild is queued or running;
//   - due: no done rebuild and FirstRebuildAt documents fetched, a fresh
//     rebuild owed against a done one, a failed rebuild to retry, or
//     Threshold of the done rebuild's documents changed since it; and at
//     least one document fetched, without which a rebuild would have
//     nothing to group;
//   - not held: no embedding drift, and the failed rebuilds' backoff past;
//   - settled: nothing indexed for Settle, or due MaxWait already
//     (MaxWaitFirst with no done rebuild). A fresh rebuild a re-embedding
//     owes settles only once no index job is pending or running and
//     nothing was indexed for Settle, however long that takes, so it
//     groups the new vectors alone.
//
// A request through the API queues a rebuild whatever the scheduler says;
// the scheduler queues none while it waits. When a rebuild became due is
// kept in memory, so a restart starts its MaxWait again.
type Scheduler struct {
	tenant        string
	lib           Library
	enqueue       func(context.Context, string, store.RunTrigger) (*store.Job, bool, error)
	drift         func() string
	paramsChanged func(*store.InterestRun) bool
	placer        *Placer
	cfg           SchedulerConfig
	now           func() time.Time
	log           *slog.Logger
	kick          chan struct{} // capacity 1: a pending check absorbs further kicks
	snap          atomic.Pointer[Snapshot]

	mu         sync.Mutex // serializes checks, and guards what they remember
	dueSince   time.Time
	loggedDue  bool // the rebuild due now was logged
	loggedHeld bool // the hold in effect was logged
	failing    bool // the checks since the last that succeeded failed, and that was logged
}

// NewScheduler returns a Scheduler whose snapshot is StateUnknown until
// its first check.
func NewScheduler(opts SchedulerOptions) *Scheduler {
	s := &Scheduler{
		tenant:        opts.TenantID,
		lib:           opts.Library,
		enqueue:       opts.Enqueue,
		drift:         opts.Drift,
		paramsChanged: opts.ParamsChanged,
		placer:        opts.Placer,
		cfg:           opts.Config.WithDefaults(),
		now:           opts.Now,
		log:           opts.Log,
		kick:          make(chan struct{}, 1),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.snap.Store(&Snapshot{State: StateUnknown})
	return s
}

// Snapshot is the last check's finding. Safe for concurrent use.
func (s *Scheduler) Snapshot() Snapshot { return *s.snap.Load() }

// Kick asks Run for a check now, without waiting: for a rebuild queued,
// started or finished, or a library re-embedded.
func (s *Scheduler) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run sweeps unplaced documents into the current grouping, then checks at
// once, every Interval and at every kick, until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	if s.placer != nil {
		s.sweep(ctx)
	}
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		s.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.kick:
		}
	}
}

// sweep places what a daemon that stopped left unplaced: documents indexed
// while it was down, or a rebuild it committed without sweeping.
func (s *Scheduler) sweep(ctx context.Context) {
	placed, err := s.placer.Sweep(ctx, s.tenant)
	switch {
	case err != nil && ctx.Err() != nil:
		s.log.Debug("interests: the start's placement sweep was cancelled", "err", err)
	case err != nil:
		s.log.Warn("interests: the start's placement sweep failed", "tenant", s.tenant, "err", err)
	case placed > 0:
		s.log.Info("interests: placed the documents indexed since the last rebuild", "tenant", s.tenant,
			"placed", placed)
	}
}

// Check reads the library, records where rebuilds stand, and queues one
// when it is due, settled and not held. A check that can't read keeps the
// last snapshot; one that can't read or can't enqueue warns once until a
// check succeeds.
func (s *Scheduler) Check(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	now := s.now()
	in, err := s.read(cctx)
	if err != nil {
		s.checkFailed(ctx, "interests: can't check whether a rebuild is due; the last state stands", err)
		return
	}

	v := decide(in, now, s.dueSince, s.cfg)
	s.noteHold(v.snap, in.drift)
	if !v.due {
		s.dueSince, s.loggedDue = time.Time{}, false
	} else if s.dueSince.IsZero() {
		s.dueSince = now
	}
	snap := v.snap
	switch {
	case v.trigger != "":
		if err = s.queue(cctx, v, now); err == nil {
			snap.State, snap.DueSince = StateQueued, time.Time{}
			s.dueSince, s.loggedDue = time.Time{}, false
		}
	case v.due && !s.loggedDue:
		s.log.Info("interests: rebuild due", "tenant", s.tenant, "changed", snap.Changed, "rebuild_at", snap.RebuildAt,
			"fresh_owed", snap.FreshOwed, "waiting_for", waitingFor(in, now))
		s.loggedDue = true
	}
	s.snap.Store(&snap)
	if err != nil {
		s.checkFailed(ctx, "interests: can't enqueue the rebuild that is due", err)
		return
	}
	s.failing = false
}

// read is one check's inputs: the library's reading, and what the daemon
// reports.
func (s *Scheduler) read(ctx context.Context) (inputs, error) {
	r, err := s.lib.Read(ctx, s.tenant)
	if err != nil {
		return inputs{}, err
	}
	in := inputs{Reading: r}
	if s.drift != nil {
		in.drift = s.drift()
	}
	if r.Done != nil && s.paramsChanged != nil {
		in.paramsChanged = s.paramsChanged(r.Done)
	}
	return in, nil
}

// checkFailed logs a check that failed with msg: a warning once a streak,
// and at debug when the daemon is stopping.
func (s *Scheduler) checkFailed(ctx context.Context, msg string, err error) {
	switch {
	case ctx.Err() != nil:
		s.log.Debug(msg, "tenant", s.tenant, "err", err)
	case !s.failing:
		s.log.Warn(msg, "tenant", s.tenant, "err", err)
		s.failing = true
	}
}

// noteHold warns once a drift episode, at the first check that finds it
// holding automatic rebuilds: a rebuild queued or running before it was
// reported still runs.
func (s *Scheduler) noteHold(snap Snapshot, drift string) {
	switch {
	case drift == "":
		s.loggedHeld = false
	case snap.State == StateHeld && !s.loggedHeld:
		s.log.Warn("interests: rebuilds held", "tenant", s.tenant, "reason", drift, "fix", HoldFix)
		s.loggedHeld = true
	}
}

// queue queues the rebuild v decided on.
func (s *Scheduler) queue(ctx context.Context, v verdict, now time.Time) error {
	job, queued, err := s.enqueue(ctx, s.tenant, v.trigger)
	if err != nil {
		return fmt.Errorf("enqueue a rebuild (%s): %w", v.trigger, err)
	}
	s.log.Info("interests: rebuild enqueued", "tenant", s.tenant, "trigger", v.trigger, "changed", v.snap.Changed,
		"waited", now.Sub(s.dueSince).Round(time.Second).String(), "job", job.ID, "queued", queued)
	return nil
}

// inputs are what a check decides on.
type inputs struct {
	Reading
	drift         string // how the embeddings drifted; "" when they didn't
	paramsChanged bool   // the done run's grouper or params aren't today's
}

// verdict is what a check decided: the snapshot, whether a rebuild is due,
// and the trigger of the one to queue now, "" for none.
type verdict struct {
	snap    Snapshot
	due     bool
	trigger store.RunTrigger
}

// decide is a check's decision, for in read at now: dueSince is when the
// rebuild became due, zero when it wasn't at the last check.
func decide(in inputs, now, dueSince time.Time, cfg SchedulerConfig) verdict {
	r := in.Reading
	s := Snapshot{CheckedAt: now, FreshOwed: string(r.State.FreshOwed), Changed: r.Fetched, RebuildAt: cfg.FirstRebuildAt}
	if r.Done != nil {
		s.LastKind, s.LastTrigger = r.Done.Kind, r.Done.Trigger
		if r.Done.FinishedAt != nil {
			s.LastRebuildAt = *r.Done.FinishedAt
		}
		s.Changed, s.RebuildAt = r.Changes.Total(), Threshold(r.Done.NumDocuments)
		if s.FreshOwed == "" && in.paramsChanged {
			s.FreshOwed = FreshParams
		}
	}
	retrying := r.State.Failures > 0
	backingOff := now.Before(RetryAt(r.State))

	// A fresh rebuild owed makes one due only against a done one: the
	// first is fresh anyway, and waits for its documents.
	busy := r.ClusterPending > 0 || r.ClusterRunning > 0
	owed := r.Done != nil && s.FreshOwed != ""
	due := !busy && r.Fetched > 0 && (s.Changed >= s.RebuildAt || owed || (retrying && !backingOff))
	if due {
		if dueSince.IsZero() {
			dueSince = now
		}
		s.DueSince = dueSince
	}
	switch {
	case r.ClusterRunning > 0:
		s.State = StateRebuilding
	case r.ClusterPending > 0:
		s.State = StateQueued
	case in.drift != "":
		s.State, s.HeldReason = StateHeld, in.drift
	case retrying:
		s.State, s.RetryAt, s.LastError = StateFailing, RetryAt(r.State), r.State.LastError
	case due:
		s.State = StateDue
	case r.Done != nil:
		s.State = StateCurrent
	default:
		s.State = StateNone
	}

	v := verdict{snap: s, due: due}
	if due && in.drift == "" && !backingOff && settled(r, now, dueSince, cfg) {
		v.trigger = triggerOf(r, s.FreshOwed)
	}
	return v
}

// settled reports whether a rebuild due since dueSince may be queued at
// now. A re-embedding's fresh rebuild waits for the index queue to drain,
// with no cap: before that the vectors are of both builds.
func settled(r Reading, now, dueSince time.Time, cfg SchedulerConfig) bool {
	quiet := now.Sub(r.LastIndexed) >= cfg.Settle
	if r.State.FreshOwed == store.FreshReindex {
		return quiet && r.IndexBusy == 0
	}
	maxWait := cfg.MaxWait
	if r.Done == nil {
		maxWait = cfg.MaxWaitFirst
	}
	return quiet || now.Sub(dueSince) >= maxWait
}

// triggerOf is what a rebuild queued now is for: the first, a
// re-embedding's fresh rebuild, new params, or the library's changes (a
// retry, or a fresh rebuild asked for whose request failed, included).
func triggerOf(r Reading, freshOwed string) store.RunTrigger {
	switch {
	case r.Done == nil:
		return store.RunTriggerFirst
	case r.State.FreshOwed == store.FreshReindex:
		return store.RunTriggerReindex
	case freshOwed == FreshParams:
		return store.RunTriggerParams
	default:
		return store.RunTriggerAuto
	}
}

// waitingFor says what a due rebuild waits for, for its log line.
func waitingFor(in inputs, now time.Time) string {
	switch {
	case in.drift != "":
		return "the embeddings to be re-indexed"
	case now.Before(RetryAt(in.State)):
		return "the retry after a failed rebuild"
	case in.State.FreshOwed == store.FreshReindex:
		return "the re-embedding to finish"
	default:
		return "the library to settle"
	}
}
