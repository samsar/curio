package insight

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
)

// world simulates what the scheduler reads, on a clock it owns: documents
// indexed at given times, the done run and the documents it assigned, the
// rebuild and index queues, and the tenant's state. A rebuild queued runs
// at once, when the simulation says so: it reads the vectors a second
// after the check that queued it, and commits or fails. Safe for
// concurrent use.
type world struct {
	mu                             sync.Mutex
	now                            time.Time
	indexedAt                      map[string]time.Time // fetched documents, by when they were indexed
	pending                        map[string]bool      // documents assigned once, pending again
	done                           *store.InterestRun
	assigned                       map[string]bool
	clusterPending, clusterRunning int
	indexBusy                      int
	state                          store.InsightState
	readErr, enqueueErr            error
	enqueues                       []enqueued
	added                          int
	nextMap                        *store.RunMap // the map the next commit draws
}

// simMap is the map the simulations' rebuilds draw unless a test says
// otherwise.
var simMap = &store.RunMap{Status: store.MapBuilt, Kind: store.RunKindFresh, Took: 3 * time.Second, Params: []byte(`{}`),
	DotRadius: 1, Unsorted: store.Circle{X: 900, Y: 500, R: 40}}

// enqueued is a rebuild the scheduler queued, and when.
type enqueued struct {
	trigger store.RunTrigger
	at      time.Time
}

// simStart is the simulations' first instant.
var simStart = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// newWorld is a library of n documents grouped by a done run that read
// them an hour before simStart, and drew its map; with n 0 there is no run
// and no document.
func newWorld(n int) *world {
	w := &world{now: simStart, indexedAt: map[string]time.Time{}, pending: map[string]bool{}, assigned: map[string]bool{},
		nextMap: simMap}
	if n > 0 {
		w.index(n, simStart.Add(-2*time.Hour))
		w.commit(simStart.Add(-time.Hour))
	}
	return w
}

// index indexes n new documents at at.
func (w *world) index(n int, at time.Time) []string {
	ids := make([]string, 0, n)
	for range n {
		id := fmt.Sprintf("doc-%05d", w.added)
		w.added++
		w.indexedAt[id] = at
		ids = append(ids, id)
	}
	return ids
}

// commit commits a run that read the vectors at readAt, every fetched
// document indexed before, and drew nextMap.
func (w *world) commit(readAt time.Time) {
	w.assigned = map[string]bool{}
	for id, at := range w.indexedAt {
		if at.Before(readAt) {
			w.assigned[id] = true
		}
	}
	finished := readAt.Add(time.Second)
	w.done = &store.InterestRun{ID: fmt.Sprintf("run-%d", readAt.Unix()), Status: store.InterestRunDone,
		Trigger: store.RunTriggerAuto, VectorsReadAt: &readAt, FinishedAt: &finished,
		RunOutcome: store.RunOutcome{Kind: store.RunKindWarm, NumDocuments: len(w.assigned), Map: w.nextMap}}
	w.state.Failures, w.state.LastFailureAt, w.state.LastError = 0, time.Time{}, ""
	if w.state.FreshOwed != "" && !w.state.FreshOwedAt.After(readAt) {
		w.state.FreshOwed, w.state.FreshOwedAt = "", time.Time{}
	}
}

// fail records a rebuild that failed at at.
func (w *world) fail(at time.Time) {
	w.state.Failures++
	w.state.LastFailureAt, w.state.LastError = at, "boom"
}

func (w *world) Read(context.Context, string) (Reading, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.readErr != nil {
		return Reading{}, w.readErr
	}
	r := Reading{Done: w.done, Fetched: len(w.indexedAt), State: w.state, ClusterPending: w.clusterPending,
		ClusterRunning: w.clusterRunning, IndexBusy: w.indexBusy}
	for _, at := range w.indexedAt {
		if at.After(r.LastIndexed) {
			r.LastIndexed = at
		}
	}
	if w.done == nil {
		return r, nil
	}
	since := *w.done.VectorsReadAt
	for id, at := range w.indexedAt {
		switch {
		case at.Before(since):
		case w.assigned[id]:
			r.Changes.Reindexed++
		default:
			r.Changes.Added++
		}
	}
	for id := range w.pending {
		if w.assigned[id] {
			r.Changes.Left++
		}
	}
	return r, nil
}

func (w *world) enqueue(_ context.Context, _ string, trigger store.RunTrigger) (*store.Job, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.enqueueErr != nil {
		return nil, false, w.enqueueErr
	}
	w.enqueues = append(w.enqueues, enqueued{trigger: trigger, at: w.now})
	queued := w.clusterPending == 0
	w.clusterPending = 1
	return &store.Job{ID: fmt.Sprintf("job-%d", len(w.enqueues))}, queued, nil
}

// clock is the world's time, for the scheduler.
func (w *world) clock() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now
}

// sim drives a scheduler over a world, a check every interval.
type sim struct {
	t     *testing.T
	w     *world
	s     *Scheduler
	logs  *bytes.Buffer
	drift string
	// unchecked says the embedding check hasn't concluded since the
	// daemon started (DriftChecked).
	unchecked bool
	// mapOff is insight.map: false, for a scheduler built after it is set.
	mapOff bool
	// rebuild is what a queued rebuild does once the check that queued it
	// is done: commit, by default.
	rebuild func(w *world, readAt time.Time)
	// dueAt is when each enqueue's rebuild had become due, as the
	// snapshot before it said.
	dueAt []time.Time
}

func newSim(t *testing.T, w *world, params bool) *sim {
	t.Helper()
	m := &sim{t: t, w: w, logs: &bytes.Buffer{}, rebuild: func(w *world, readAt time.Time) { w.commit(readAt) }}
	m.s = m.scheduler(params)
	return m
}

// scheduler is a scheduler over the sim's world, as a daemon (re)starting
// now builds it.
func (m *sim) scheduler(params bool) *Scheduler {
	return NewScheduler(SchedulerOptions{
		TenantID: "local", Library: m.w, Enqueue: m.w.enqueue,
		Drift:         func() string { return m.drift },
		DriftChecked:  func() bool { return !m.unchecked },
		ParamsChanged: func(*store.InterestRun) bool { return params },
		MapOff:        m.mapOff,
		Now:           m.w.clock,
		Log:           slog.New(slog.NewTextHandler(m.logs, nil)),
	})
}

// step moves the clock one check interval on, indexing n documents half an
// interval before the check, then checks, and runs a rebuild it queued.
func (m *sim) step(n int) {
	m.w.mu.Lock()
	m.w.now = m.w.now.Add(CheckInterval)
	m.w.index(n, m.w.now.Add(-CheckInterval/2))
	before := len(m.w.enqueues)
	m.w.mu.Unlock()
	due := m.s.Snapshot().DueSince
	m.s.Check(context.Background())
	m.w.mu.Lock()
	defer m.w.mu.Unlock()
	if len(m.w.enqueues) > before {
		if due.IsZero() {
			due = m.w.now
		}
		m.dueAt = append(m.dueAt, due)
		m.w.clusterPending = 0
		m.rebuild(m.w, m.w.now.Add(time.Second))
	}
}

// runFor steps for d, indexing what perStep says before each check, from
// the step's time since the sim's simStart.
func (m *sim) runFor(d time.Duration, perStep func(elapsed time.Duration) int) {
	for elapsed := time.Duration(0); elapsed < d; elapsed += CheckInterval {
		m.step(perStep(elapsed))
	}
}

// at is each enqueue's time since simStart.
func (m *sim) at() []time.Duration {
	out := make([]time.Duration, 0, len(m.w.enqueues))
	for _, e := range m.w.enqueues {
		out = append(out, e.at.Sub(simStart))
	}
	return out
}

// lines are the scheduler's log lines containing s.
func (m *sim) lines(s string) []string {
	var out []string
	for line := range strings.Lines(m.logs.String()) {
		if strings.Contains(line, s) {
			out = append(out, line)
		}
	}
	return out
}

// spread indexes total documents evenly over d, nothing after.
func spread(total int, d time.Duration) func(time.Duration) int {
	return func(elapsed time.Duration) int {
		if elapsed >= d {
			return 0
		}
		next := elapsed + CheckInterval
		return int(int64(total)*int64(min(next, d))/int64(d)) - int(int64(total)*int64(elapsed)/int64(d))
	}
}

func quiet(time.Duration) int { return 0 }

func TestThreshold(t *testing.T) {
	for n, want := range map[int]int{0: 5, 30: 5, 100: 5, 101: 6, 120: 6, 5254: 263, 5517: 276} {
		assert.Equal(t, want, Threshold(n), "%d documents", n)
	}
}

func TestRetryDelay(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		1: 15 * time.Minute, 2: 30 * time.Minute, 3: time.Hour, 4: 2 * time.Hour, 5: 4 * time.Hour, 6: 4 * time.Hour,
		60: 4 * time.Hour,
	} {
		assert.Equal(t, want, RetryDelay(failures), "%d failures", failures)
	}
	assert.True(t, RetryAt(store.InsightState{}).IsZero())
}

// TestScheduler_AnImportSettlesOnce: 2,000 documents indexed over 30
// minutes into the owner's library pass the threshold within minutes, but
// are rebuilt once, ten minutes after the last is indexed.
func TestScheduler_AnImportSettlesOnce(t *testing.T) {
	t.Parallel()
	m := newSim(t, newWorld(5254), false)
	m.runFor(4*time.Hour, spread(2000, 30*time.Minute))
	require.Len(t, m.w.enqueues, 1)
	assert.Equal(t, store.RunTriggerAuto, m.w.enqueues[0].trigger)
	assert.Equal(t, 40*time.Minute, m.at()[0], "the last document is indexed at 29.5 minutes")
	assert.Equal(t, 4*time.Minute, m.dueAt[0].Sub(simStart), "due once 263 changed")
	due := m.lines("interests: rebuild due")
	require.Len(t, due, 1, "once an episode")
	assert.Contains(t, due[0], "rebuild_at=263")
	assert.Contains(t, due[0], `waiting_for="the library to settle"`)
	queued := m.lines("interests: rebuild enqueued")
	require.Len(t, queued, 1)
	assert.Contains(t, queued[0], "trigger=auto")
	assert.Contains(t, queued[0], "waited=36m0s")
	assert.True(t, strings.HasSuffix(queued[0], " queued=true\n"), "nothing said of a map: %s", queued[0])
	assert.NotContains(t, due[0], "map_owed")
	assert.Equal(t, StateCurrent, m.s.Snapshot().State)
}

// TestScheduler_ALongImportIsRebuiltEveryMaxWait: an import of five hours
// at the owner's index rate is rebuilt MaxWait after each rebuild became
// due, to the check, never sooner, and once more after it settles.
func TestScheduler_ALongImportIsRebuiltEveryMaxWait(t *testing.T) {
	t.Parallel()
	m := newSim(t, newWorld(5254), false)
	m.runFor(7*time.Hour, spread(15000, 5*time.Hour))
	at := m.at()
	require.Len(t, at, 3, "%v", at)
	for i := range 2 {
		waited := m.w.enqueues[i].at.Sub(m.dueAt[i])
		assert.GreaterOrEqual(t, waited, MaxWait, "enqueue %d", i)
		assert.LessOrEqual(t, waited, MaxWait+CheckInterval, "enqueue %d", i)
		if i > 0 {
			assert.GreaterOrEqual(t, at[i]-at[i-1], MaxWait, "never two within MaxWait")
		}
	}
	assert.Equal(t, 5*time.Hour+10*time.Minute, at[2], "and after the last document, at 4h59.5m, settles")
}

// TestScheduler_DriftHolds: while the embeddings drifted, nothing is
// queued however much changes, the state says held, and one warning says
// why for the whole episode.
func TestScheduler_DriftHolds(t *testing.T) {
	t.Parallel()
	m := newSim(t, newWorld(5254), false)
	m.drift = "the embeddings drifted"
	m.runFor(5*time.Hour, spread(15000, 5*time.Hour))
	assert.Empty(t, m.w.enqueues)
	snap := m.s.Snapshot()
	assert.Equal(t, StateHeld, snap.State)
	assert.Equal(t, "the embeddings drifted", snap.HeldReason)
	held := m.lines("interests: rebuilds held")
	require.Len(t, held, 1)
	assert.Contains(t, held[0], "level=WARN")
	assert.Contains(t, held[0], `fix="curio reindex --all"`)

	m.drift = ""
	m.step(0)
	require.Len(t, m.w.enqueues, 1, "the hold lifted, the long-due rebuild goes")
}

// TestScheduler_WaitsForTheFirstDriftCheck: right after a start, before
// the embedding check's first verdict, a rebuild that is due waits,
// silently: due, not held, nothing asked of the user. A drift that predates
// the start would otherwise only show after a rebuild of vectors from two
// builds was queued.
func TestScheduler_WaitsForTheFirstDriftCheck(t *testing.T) {
	t.Parallel()
	m := newSim(t, newWorld(5254), false)
	m.unchecked = true
	m.runFor(time.Hour, spread(400, 10*time.Minute))
	assert.Empty(t, m.w.enqueues)
	assert.Equal(t, StateDue, m.s.Snapshot().State)
	assert.Empty(t, m.lines("interests: rebuilds held"), "no hold, so no fix to run")
	due := m.lines("interests: rebuild due")
	require.Len(t, due, 1)
	assert.Contains(t, due[0], "the first embedding check since the daemon started")

	m.unchecked = false
	m.step(0)
	require.Len(t, m.w.enqueues, 1, "the verdict came clean: the due rebuild goes")
}

// TestScheduler_RunWaitsForTheFirstDriftCheck: Run neither sweeps nor
// checks before the embedding check's first verdict, so a drift that
// predates the start holds the start's placements and rebuilds too.
func TestScheduler_RunWaitsForTheFirstDriftCheck(t *testing.T) {
	w := newWorld(100)
	var checked atomic.Bool
	s := NewScheduler(SchedulerOptions{TenantID: "local", Library: w, Enqueue: w.enqueue, Now: w.clock,
		DriftChecked: checked.Load, Config: SchedulerConfig{Interval: time.Hour}, Log: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithCancel(context.Background())
	var running sync.WaitGroup
	running.Go(func() { s.Run(ctx) })
	t.Cleanup(func() {
		cancel()
		running.Wait()
	})

	s.Kick()
	assert.Never(t, func() bool { return s.Snapshot().State != StateUnknown }, 1500*time.Millisecond, 50*time.Millisecond,
		"no check before the verdict")
	checked.Store(true)
	require.Eventually(t, func() bool { return s.Snapshot().State == StateCurrent }, 5*time.Second, 10*time.Millisecond)
}

// TestScheduler_ARebuildQueuedBeforeAHold: a rebuild queued before the
// embeddings drifted, or past the failures' backoff, still runs, so the
// state says queued, then rebuilding, with neither the hold nor the
// backoff; the hold is warned about once it holds, and the backoff shown
// once nothing holds rebuilds but it.
func TestScheduler_ARebuildQueuedBeforeAHold(t *testing.T) {
	w := newWorld(100)
	w.fail(simStart.Add(-time.Minute))
	w.clusterPending = 1
	m := newSim(t, w, false)
	m.drift = "the embeddings drifted"
	m.step(0)
	snap := m.s.Snapshot()
	assert.Equal(t, StateQueued, snap.State)
	assert.Empty(t, snap.HeldReason)
	assert.Zero(t, snap.RetryAt)
	assert.Empty(t, snap.LastError)
	w.clusterPending, w.clusterRunning = 0, 1
	m.step(0)
	assert.Equal(t, Snapshot{State: StateRebuilding, LastKind: store.RunKindWarm, LastTrigger: store.RunTriggerAuto,
		LastRebuildAt: *w.done.FinishedAt, RebuildAt: 5, Map: simMapState, CheckedAt: w.now}, m.s.Snapshot())
	assert.Empty(t, m.lines("interests: rebuilds held"), "nothing is held yet")

	w.clusterRunning = 0
	m.step(0)
	snap = m.s.Snapshot()
	assert.Equal(t, StateHeld, snap.State)
	assert.Equal(t, "the embeddings drifted", snap.HeldReason)
	assert.Zero(t, snap.RetryAt, "the hold comes first")
	assert.Len(t, m.lines("interests: rebuilds held"), 1)

	m.drift = ""
	m.step(0)
	snap = m.s.Snapshot()
	assert.Equal(t, StateFailing, snap.State)
	assert.Empty(t, snap.HeldReason)
	assert.Equal(t, w.state.LastFailureAt.Add(RetryAfter), snap.RetryAt)
	assert.Equal(t, "boom", snap.LastError)
}

// TestScheduler_TheFirstRebuild: a library with no done rebuild waits for
// 20 documents, then for the library to settle, or MaxWaitFirst.
func TestScheduler_TheFirstRebuild(t *testing.T) {
	t.Run("19 documents", func(t *testing.T) {
		m := newSim(t, newWorld(0), false)
		m.step(19)
		m.runFor(3*time.Hour, quiet)
		assert.Empty(t, m.w.enqueues)
		snap := m.s.Snapshot()
		assert.Equal(t, StateNone, snap.State)
		assert.Equal(t, 19, snap.Changed)
		assert.Equal(t, FirstRebuildAt, snap.RebuildAt)
	})
	t.Run("20 documents, then quiet", func(t *testing.T) {
		m := newSim(t, newWorld(0), false)
		m.step(20)
		assert.Equal(t, StateDue, m.s.Snapshot().State)
		m.runFor(time.Hour, quiet)
		require.Len(t, m.w.enqueues, 1)
		assert.Equal(t, store.RunTriggerFirst, m.w.enqueues[0].trigger)
		assert.Equal(t, 11*time.Minute, m.at()[0], "10 minutes after the 20th, indexed at 0.5 minutes")
	})
	t.Run("20 documents, still indexing", func(t *testing.T) {
		m := newSim(t, newWorld(0), false)
		m.step(20)
		m.runFor(time.Hour, func(time.Duration) int { return 1 })
		require.NotEmpty(t, m.w.enqueues)
		assert.Equal(t, MaxWaitFirst, m.w.enqueues[0].at.Sub(m.dueAt[0]), "the first waits 30 minutes at most")
	})
	t.Run("a fresh rebuild owed changes nothing", func(t *testing.T) {
		w := newWorld(0)
		w.state.FreshOwed, w.state.FreshOwedAt = store.FreshReindex, simStart
		m := newSim(t, w, false)
		m.step(5)
		m.runFor(time.Hour, quiet)
		assert.Empty(t, m.w.enqueues, "the first rebuild is fresh anyway, and waits for its documents")
		assert.Equal(t, StateNone, m.s.Snapshot().State)
	})
}

// TestScheduler_TheFloor: a library of 30 documents is rebuilt after 5
// changes, not after 2 (5% of it).
func TestScheduler_TheFloor(t *testing.T) {
	m := newSim(t, newWorld(30), false)
	m.step(2)
	m.runFor(time.Hour, quiet)
	assert.Empty(t, m.w.enqueues)
	assert.Equal(t, StateCurrent, m.s.Snapshot().State)
	assert.Equal(t, 2, m.s.Snapshot().Changed)
	assert.Equal(t, 5, m.s.Snapshot().RebuildAt)
	m.step(3)
	m.runFor(time.Hour, quiet)
	require.Len(t, m.w.enqueues, 1)
}

// TestScheduler_AQueuedRebuildBlocks: with a rebuild queued, by a request
// or anything else, the scheduler queues none, and says queued.
func TestScheduler_AQueuedRebuildBlocks(t *testing.T) {
	w := newWorld(100)
	w.clusterPending = 1
	m := newSim(t, w, false)
	m.runFor(3*time.Hour, spread(50, time.Minute))
	assert.Empty(t, m.w.enqueues)
	assert.Equal(t, StateQueued, m.s.Snapshot().State)
	assert.Zero(t, m.s.Snapshot().DueSince, "a rebuild queued isn't waiting")

	w.clusterPending, w.clusterRunning = 0, 1
	m.step(0)
	assert.Equal(t, StateRebuilding, m.s.Snapshot().State)
	assert.Empty(t, m.w.enqueues)
}

// TestScheduler_NothingToGroup: a done run with no fetched document left
// (a refetch of the whole library in flight) is current, however many
// left it: a rebuild would have nothing to group, and fail.
func TestScheduler_NothingToGroup(t *testing.T) {
	w := newWorld(40)
	for id := range w.indexedAt {
		w.pending[id] = true
		delete(w.indexedAt, id)
	}
	m := newSim(t, w, false)
	m.runFor(3*time.Hour, quiet)
	assert.Empty(t, m.w.enqueues)
	snap := m.s.Snapshot()
	assert.Equal(t, StateCurrent, snap.State)
	assert.Equal(t, 40, snap.Changed, "every document left")
}

// TestScheduler_FailedRebuildsBackOff: a rebuild that keeps failing is
// retried 15 minutes after the first failure, doubling to 4 hours; a
// restart keeps the backoff, which is stored, and starts the wait for the
// library to settle again, which isn't; a success ends it.
func TestScheduler_FailedRebuildsBackOff(t *testing.T) {
	t.Parallel()
	w := newWorld(100)
	w.index(10, simStart.Add(-30*time.Minute))
	m := newSim(t, w, false)
	m.rebuild = func(w *world, readAt time.Time) { w.fail(readAt) }
	m.runFor(14*time.Hour, quiet)
	at := m.at()
	require.GreaterOrEqual(t, len(at), 7)
	var gaps []time.Duration
	for i := 1; i < 7; i++ {
		gaps = append(gaps, at[i]-at[i-1])
	}
	assert.Equal(t, []time.Duration{
		16 * time.Minute, 31 * time.Minute, 61 * time.Minute, 121 * time.Minute, 241 * time.Minute, 241 * time.Minute,
	}, gaps, "each failure a second after its check, so the retry is the check after RetryDelay")
	snap := m.s.Snapshot()
	assert.Equal(t, StateFailing, snap.State)
	assert.Equal(t, "boom", snap.LastError)
	assert.Equal(t, w.state.LastFailureAt.Add(MaxRetryAfter), snap.RetryAt)

	// A restart: the backoff is in the store, so it holds.
	retryAt := snap.RetryAt
	m.s = m.scheduler(false)
	before := len(m.w.enqueues)
	for m.w.clock().Add(CheckInterval).Before(retryAt) {
		m.step(0)
	}
	assert.Len(t, m.w.enqueues, before, "nothing before the stored retry time")
	m.rebuild = func(w *world, readAt time.Time) { w.commit(readAt) }
	m.step(0)
	m.step(0)
	require.Len(t, m.w.enqueues, before+1)
	assert.Equal(t, StateCurrent, m.s.Snapshot().State, "a success ends it")
	assert.Zero(t, m.w.state.Failures)
}

// TestScheduler_AReindexWaitsForTheDrain: a re-embedding's fresh rebuild
// waits until no index job is left and nothing was indexed for the settle
// window, however long the drain, then goes with trigger reindex; a queue
// that never drains (paused) never gets it.
func TestScheduler_AReindexWaitsForTheDrain(t *testing.T) {
	t.Parallel()
	reindexing := func(w *world, drain time.Duration) func(time.Duration) int {
		ids := make([]string, 0, len(w.indexedAt))
		for id := range w.indexedAt {
			ids = append(ids, id)
		}
		total := len(ids)
		w.indexBusy = total
		each := spread(total, drain)
		return func(elapsed time.Duration) int {
			n := each(elapsed)
			at := simStart.Add(elapsed + CheckInterval/2)
			for range n {
				w.indexedAt[ids[0]] = at
				ids = ids[1:]
			}
			w.indexBusy -= n
			return 0
		}
	}
	t.Run("a 3-hour drain", func(t *testing.T) {
		w := newWorld(5254)
		w.state.FreshOwed, w.state.FreshOwedAt = store.FreshReindex, simStart
		m := newSim(t, w, false)
		m.runFor(5*time.Hour, reindexing(w, 3*time.Hour))
		require.Len(t, m.w.enqueues, 1)
		assert.Equal(t, store.RunTriggerReindex, m.w.enqueues[0].trigger)
		assert.Equal(t, 3*time.Hour+10*time.Minute, m.at()[0], "the last job done at 2h59.5m, then the settle window")
		due := m.lines("interests: rebuild due")
		require.Len(t, due, 1)
		assert.Contains(t, due[0], `waiting_for="the re-embedding to finish"`)
		assert.Equal(t, StateCurrent, m.s.Snapshot().State)
	})
	t.Run("a paused queue", func(t *testing.T) {
		w := newWorld(5254)
		w.state.FreshOwed, w.state.FreshOwedAt = store.FreshReindex, simStart
		w.indexBusy = 5254
		m := newSim(t, w, false)
		m.runFor(6*time.Hour, quiet)
		assert.Empty(t, m.w.enqueues)
		snap := m.s.Snapshot()
		assert.Equal(t, StateDue, snap.State)
		assert.Equal(t, string(store.FreshReindex), snap.FreshOwed)
	})
}

// TestScheduler_ParamsChanged: a done run grouped with other params makes
// a fresh rebuild due at once, queued with trigger params when the library
// is quiet.
func TestScheduler_ParamsChanged(t *testing.T) {
	m := newSim(t, newWorld(500), true)
	m.step(0)
	require.Len(t, m.w.enqueues, 1)
	assert.Equal(t, store.RunTriggerParams, m.w.enqueues[0].trigger)
}

// TestScheduler_ACheckThatFails: one that can't read keeps the last
// snapshot, one that can't enqueue says due, and either warns once a
// streak.
func TestScheduler_ACheckThatFails(t *testing.T) {
	m := newSim(t, newWorld(100), false)
	m.step(1)
	before := m.s.Snapshot()
	m.w.readErr = errors.New("database is locked")
	m.step(0)
	m.step(0)
	assert.Equal(t, before, m.s.Snapshot())
	assert.Len(t, m.lines("level=WARN"), 1)
	m.w.readErr = nil
	m.step(0)
	m.w.readErr = errors.New("database is locked")
	m.step(0)
	assert.Len(t, m.lines("level=WARN"), 2, "a new streak warns again")

	m.w.readErr, m.w.enqueueErr = nil, errors.New("database is locked")
	m.step(10)
	m.runFor(time.Hour, quiet)
	assert.Empty(t, m.w.enqueues)
	assert.Equal(t, StateDue, m.s.Snapshot().State)
	failed := m.lines("interests: can't enqueue the rebuild that is due")
	require.Len(t, failed, 1, "once, however many checks fail")
	assert.Contains(t, failed[0], "level=WARN")
	m.w.enqueueErr = nil
	m.step(0)
	assert.Len(t, m.w.enqueues, 1)
}

// TestScheduler_Snapshot: before any check the state is unknown; then each
// check records the done run, the counts and, while due, since when.
func TestScheduler_Snapshot(t *testing.T) {
	w := newWorld(5254)
	m := newSim(t, w, false)
	assert.Equal(t, Snapshot{State: StateUnknown}, m.s.Snapshot())
	m.step(300)
	snap := m.s.Snapshot()
	assert.Equal(t, Snapshot{
		State: StateDue, LastRebuildAt: *w.done.FinishedAt, LastKind: store.RunKindWarm,
		LastTrigger: store.RunTriggerAuto, Changed: 300, RebuildAt: 263, DueSince: simStart.Add(CheckInterval),
		Map: simMapState, CheckedAt: simStart.Add(CheckInterval),
	}, snap)
}

// simMapState is simMap as a snapshot gives it.
var simMapState = MapState{Status: store.MapBuilt, Kind: store.RunKindFresh, Took: 3 * time.Second}

// TestDecide_Map: a check's snapshot gives the done rebuild's map as the
// run records it, none for a run that drew none, off with the map off
// whatever the run drew, and nothing with no done rebuild and the map on.
func TestDecide_Map(t *testing.T) {
	ran := func(m *store.RunMap) *store.InterestRun {
		return &store.InterestRun{RunOutcome: store.RunOutcome{Map: m}}
	}
	failed := &store.RunMap{Status: store.MapFailed, Error: "the map took longer than 2m0s", Took: 2 * time.Minute,
		Params: []byte(`{}`)}
	reused := &store.RunMap{Status: store.MapBuilt, Kind: store.RunKindWarm, Params: []byte(`{}`), DotRadius: 1,
		Unsorted: store.Circle{X: 900, Y: 500, R: 40}}
	for name, tc := range map[string]struct {
		done *store.InterestRun
		off  bool
		want MapState
	}{
		"no done rebuild":              {},
		"no done rebuild, the map off": {off: true, want: MapState{Status: MapOff}},
		"a run that drew no map":       {done: ran(nil), want: MapState{Status: MapNone}},
		"a built map":                  {done: ran(simMap), want: simMapState},
		"a reused map, in no time":     {done: ran(reused), want: MapState{Status: store.MapBuilt, Kind: store.RunKindWarm}},
		"a failed map": {done: ran(failed),
			want: MapState{Status: store.MapFailed, Took: 2 * time.Minute, Error: failed.Error}},
		"a run that drew none, map off":  {done: ran(nil), off: true, want: MapState{Status: MapOff}},
		"a built map, the map off since": {done: ran(simMap), off: true, want: MapState{Status: MapOff}},
	} {
		t.Run(name, func(t *testing.T) {
			in := inputs{Reading: Reading{Done: tc.done, Fetched: 100}, mapOff: tc.off}
			v := decide(in, simStart, time.Time{}, SchedulerConfig{}.WithDefaults())
			assert.Equal(t, tc.want, v.snap.Map)
			assert.Equal(t, tc.want.Status == MapNone, v.mapOwed, "a map owed only by a run that drew none, the map on")
		})
	}
}

// mapless is a world whose done rebuild drew no map, as one from before
// maps, committed by curio 2.5.x, or with the map off.
func mapless(n int) *world {
	w := newWorld(n)
	w.done.Map = nil
	return w
}

// TestScheduler_AMapOwed: a done rebuild that drew no map makes a rebuild
// due, queued once with trigger auto and saying the map is owed; the check
// after its commit finds it current with its map built, and nothing more
// is queued.
func TestScheduler_AMapOwed(t *testing.T) {
	t.Parallel()
	m := newSim(t, mapless(5254), false)
	m.step(0)
	require.Len(t, m.w.enqueues, 1, "the library is quiet: at once")
	assert.Equal(t, store.RunTriggerAuto, m.w.enqueues[0].trigger)
	queued := m.lines("interests: rebuild enqueued")
	require.Len(t, queued, 1)
	assert.Contains(t, queued[0], "trigger=auto changed=0")
	assert.Contains(t, queued[0], "map_owed=true")
	m.step(0)
	snap := m.s.Snapshot()
	assert.Equal(t, StateCurrent, snap.State)
	assert.Equal(t, simMapState, snap.Map)
	m.runFor(24*time.Hour, quiet)
	assert.Len(t, m.w.enqueues, 1)
}

// TestScheduler_AMapOwedKeepsTheRules: a rebuild a map owes waits as any
// due rebuild does: for the library to settle, saying so with the map
// owed; for a drift to clear, held; for the embedding check's first
// verdict; for a failed rebuild's backoff; and for a rebuild queued or
// running. With nothing fetched, or the map off, none is due.
func TestScheduler_AMapOwedKeepsTheRules(t *testing.T) {
	t.Parallel()
	t.Run("the library settling", func(t *testing.T) {
		m := newSim(t, mapless(5254), false)
		m.step(3)
		snap := m.s.Snapshot()
		assert.Equal(t, StateDue, snap.State)
		assert.Equal(t, MapState{Status: MapNone}, snap.Map)
		assert.Equal(t, 3, snap.Changed)
		due := m.lines("interests: rebuild due")
		require.Len(t, due, 1)
		assert.Contains(t, due[0], `waiting_for="the library to settle"`)
		assert.Contains(t, due[0], "map_owed=true")
		m.runFor(time.Hour, quiet)
		require.Len(t, m.w.enqueues, 1)
		assert.Equal(t, 11*time.Minute, m.at()[0], "10 minutes after the last, indexed at 0.5 minutes")
	})
	t.Run("a drift", func(t *testing.T) {
		m := newSim(t, mapless(5254), false)
		m.drift = "the embeddings drifted"
		m.runFor(time.Hour, quiet)
		assert.Empty(t, m.w.enqueues)
		snap := m.s.Snapshot()
		assert.Equal(t, StateHeld, snap.State)
		assert.Equal(t, MapState{Status: MapNone}, snap.Map)
		m.drift = ""
		m.step(0)
		assert.Len(t, m.w.enqueues, 1, "the drift cleared")
	})
	t.Run("the first drift check", func(t *testing.T) {
		m := newSim(t, mapless(5254), false)
		m.unchecked = true
		m.runFor(time.Hour, quiet)
		assert.Empty(t, m.w.enqueues)
		assert.Equal(t, StateDue, m.s.Snapshot().State)
		m.unchecked = false
		m.step(0)
		assert.Len(t, m.w.enqueues, 1)
	})
	t.Run("a failed rebuild's backoff", func(t *testing.T) {
		w := mapless(5254)
		w.fail(simStart)
		m := newSim(t, w, false)
		m.runFor(14*time.Minute, quiet)
		assert.Empty(t, m.w.enqueues)
		assert.Equal(t, StateFailing, m.s.Snapshot().State)
		m.runFor(2*time.Minute, quiet)
		require.Len(t, m.w.enqueues, 1)
		assert.Equal(t, 15*time.Minute, m.at()[0], "once RetryAfter passed")
	})
	t.Run("a rebuild queued", func(t *testing.T) {
		w := mapless(5254)
		w.clusterPending = 1
		m := newSim(t, w, false)
		m.runFor(time.Hour, quiet)
		assert.Empty(t, m.w.enqueues)
		assert.Equal(t, StateQueued, m.s.Snapshot().State)
	})
	t.Run("nothing fetched", func(t *testing.T) {
		w := mapless(40)
		for id := range w.indexedAt {
			w.pending[id] = true
			delete(w.indexedAt, id)
		}
		m := newSim(t, w, false)
		m.runFor(time.Hour, quiet)
		assert.Empty(t, m.w.enqueues)
		assert.Equal(t, StateCurrent, m.s.Snapshot().State)
	})
	t.Run("the map off", func(t *testing.T) {
		m := newSim(t, mapless(5254), false)
		m.mapOff = true
		m.s = m.scheduler(false)
		m.runFor(24*time.Hour, quiet)
		assert.Empty(t, m.w.enqueues)
		snap := m.s.Snapshot()
		assert.Equal(t, StateCurrent, snap.State)
		assert.Equal(t, MapState{Status: MapOff}, snap.Map)
	})
}

// TestScheduler_AFailedMapOwesNothing: a map that fails, here the one a
// map owed rebuild draws, makes nothing due: the check after says current
// with the map failed, and no rebuild is queued for a day.
func TestScheduler_AFailedMapOwesNothing(t *testing.T) {
	t.Parallel()
	w := mapless(5254)
	w.nextMap = &store.RunMap{Status: store.MapFailed, Error: "the map took longer than 2m0s", Took: 2 * time.Minute,
		Params: []byte(`{}`)}
	m := newSim(t, w, false)
	m.step(0)
	require.Len(t, m.w.enqueues, 1)
	m.runFor(24*time.Hour, quiet)
	assert.Len(t, m.w.enqueues, 1, "no second rebuild")
	snap := m.s.Snapshot()
	assert.Equal(t, StateCurrent, snap.State)
	assert.Equal(t, MapState{Status: store.MapFailed, Took: 2 * time.Minute, Error: "the map took longer than 2m0s"}, snap.Map)
	assert.Empty(t, m.lines("interests: rebuild due"))
}

// TestScheduler_KicksKeepTheSnapshotCurrent: Run checks at every kick, so
// a rebuild queued, started and done shows without the interval passing;
// readers meanwhile see whole snapshots.
func TestScheduler_KicksKeepTheSnapshotCurrent(t *testing.T) {
	w := newWorld(100)
	s := NewScheduler(SchedulerOptions{TenantID: "local", Library: w, Enqueue: w.enqueue, Now: w.clock,
		Config: SchedulerConfig{Interval: time.Hour}, Log: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithCancel(context.Background())
	var running sync.WaitGroup
	running.Go(func() { s.Run(ctx) })
	running.Go(func() {
		for ctx.Err() == nil {
			_ = s.Snapshot().State // a reader meanwhile, for -race
		}
	})
	t.Cleanup(func() {
		cancel()
		running.Wait()
	})

	reach := func(state RebuildState, change func()) {
		t.Helper()
		w.mu.Lock()
		change()
		w.mu.Unlock()
		s.Kick()
		require.Eventually(t, func() bool { return s.Snapshot().State == state }, 5*time.Second, time.Millisecond,
			"%s", state)
	}
	reach(StateCurrent, func() {})
	reach(StateQueued, func() { w.clusterPending = 1 })
	reach(StateRebuilding, func() { w.clusterPending, w.clusterRunning = 0, 1 })
	reach(StateCurrent, func() { w.clusterRunning = 0 })
	assert.Empty(t, w.enqueues)
}

// TestLibrary_ReadsTheStores: the stores' Library reads a hand-built
// library as it is: the done run and what changed since, the fetched
// documents and the last indexing, the queue and the state; and a second
// handle on the database reads the same, the count being derived.
func TestLibrary_ReadsTheStores(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 4)
	run := f.rebuild(t, f.engine(nil, nil, Config{}))
	f.add(t, 0, 2)
	f.failed(t, f.vectorIDs()[0])
	queue := sqlitestore.NewJobs(f.db)
	require.NoError(t, queue.Enqueue(ctx, &store.Job{TenantID: tenant, Kind: store.JobKindCluster}))
	index, err := store.NewDocumentJob(tenant, store.JobKindIndex, f.vectorIDs()[1])
	require.NoError(t, err)
	require.NoError(t, queue.Enqueue(ctx, index))
	require.NoError(t, f.store.OweFresh(ctx, tenant, store.FreshManual))
	_, err = f.store.RecordFailure(ctx, tenant, "", 0, "boom")
	require.NoError(t, err)

	read := func(db *sqlitestore.DB) Reading {
		t.Helper()
		r, err := NewLibrary(sqlitestore.NewInsights(db), sqlitestore.NewDocuments(db), sqlitestore.NewJobs(db)).
			Read(ctx, tenant)
		require.NoError(t, err)
		return r
	}
	got := read(f.db)
	require.NotNil(t, got.Done)
	assert.Equal(t, run.ID, got.Done.ID)
	assert.Equal(t, store.RunChanges{Added: 2, Left: 1}, got.Changes)
	assert.Equal(t, 8, got.Fetched)
	assert.Equal(t, f.clock, got.LastIndexed, "when the last document added was indexed")
	assert.Equal(t, 1, got.ClusterPending)
	assert.Zero(t, got.ClusterRunning)
	assert.Equal(t, 1, got.IndexBusy)
	assert.Equal(t, store.FreshManual, got.State.FreshOwed)
	assert.Equal(t, 1, got.State.Failures)

	var path string
	require.NoError(t, f.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path))
	other, err := sqlitestore.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, other.Close()) })
	assert.Equal(t, got, read(other), "another handle, as after a restart, reads the same")
}

// finishingInsights is the insight store of a check whose read straddles
// a rebuild's end: once its first LatestRun has read, finish runs, then
// that read is returned.
type finishingInsights struct {
	store.InsightStore
	finish func()
}

func (f *finishingInsights) LatestRun(ctx context.Context, tenantID string, status store.InterestRunStatus) (*store.InterestRun, error) {
	run, err := f.InsightStore.LatestRun(ctx, tenantID, status)
	if finish := f.finish; finish != nil {
		f.finish = nil
		finish()
	}
	return run, err
}

// TestLibrary_ARebuildEndingDuringARead: a check whose read straddles a
// running rebuild's commit and its job's end finds the rebuild running,
// and queues none from the done run it read from before the commit; the
// next check finds the new run current.
func TestLibrary_ARebuildEndingDuringARead(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, FirstRebuildAt)
	queue := sqlitestore.NewJobs(f.db)
	require.NoError(t, queue.Enqueue(ctx, &store.Job{TenantID: tenant, Kind: store.JobKindCluster}))
	job, err := queue.ClaimNext(ctx, []store.JobKind{store.JobKindCluster})
	require.NoError(t, err)
	e := f.engine(nil, nil, Config{})
	insights := &finishingInsights{InsightStore: f.store, finish: func() {
		f.tick()
		_, err := e.Rebuild(ctx, tenant, store.RunTriggerFirst)
		require.NoError(t, err)
		require.NoError(t, queue.MarkDone(ctx, job.ID))
	}}
	var enqueued []store.RunTrigger
	s := NewScheduler(SchedulerOptions{TenantID: tenant, Library: NewLibrary(insights, f.docs, queue),
		Enqueue: func(_ context.Context, _ string, trigger store.RunTrigger) (*store.Job, bool, error) {
			enqueued = append(enqueued, trigger)
			return &store.Job{ID: "job"}, true, nil
		},
		Now: func() time.Time { return f.clock.Add(time.Hour) }, Log: slog.New(slog.DiscardHandler)})

	s.Check(ctx)
	assert.Equal(t, StateRebuilding, s.Snapshot().State)
	s.Check(ctx)
	assert.Equal(t, StateCurrent, s.Snapshot().State)
	assert.Empty(t, enqueued, "the rebuild that ended is not queued again")
}
