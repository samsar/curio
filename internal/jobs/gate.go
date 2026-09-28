package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samsar/curio/internal/store"
)

// Gate decides whether a worker may claim a job. A worker asks before every
// claim, once for each kind it claims, and claims only the kinds admitted.
//
// Admit runs under the asking worker's lock, so it must be fast and must
// never block or do I/O. active is how many jobs the asking worker already
// holds, claims in progress included, the asker not counted; the daemon
// runs one Worker per kind, so that is how many of the kind are running.
type Gate interface {
	Admit(kind store.JobKind, active int, now time.Time) Verdict
}

// GateReason says why a gate is closed.
type GateReason string

// Why the queue gate is closed.
const (
	ReasonPaused          GateReason = "paused"
	ReasonOutsideSchedule GateReason = "outside_schedule"
	ReasonThrottled       GateReason = "throttled"
)

// Verdict is a gate's answer. The zero value admits.
//
// A closed verdict says when to ask again: Changed is closed once what the
// verdict was decided from has changed, and Until is when the gate may
// reopen by itself. A closed verdict has a non-nil Changed, a non-zero
// Until after now, or both. Each verdict carries the channel of the state
// it was decided from, so a waiter never misses a change made after the
// decision, and gates compose (All) without merging their signals.
type Verdict struct {
	Closed  GateReason // empty when the gate admits
	Until   time.Time
	Changed <-chan struct{}
}

// Open reports whether v admits.
func (v Verdict) Open() bool { return v.Closed == "" }

// All returns a gate that admits when every one of gates does, and
// otherwise gives the first closed verdict, in argument order. With no
// gates it admits everything.
func All(gates ...Gate) Gate { return allGates(gates) }

type allGates []Gate

func (gates allGates) Admit(kind store.JobKind, active int, now time.Time) Verdict {
	for _, g := range gates {
		if v := g.Admit(kind, active, now); !v.Open() {
			return v
		}
	}
	return Verdict{}
}

// QueueGate is the daemon's Gate: it holds claims back while the queue is
// paused, outside its daily schedule, or at the throttle's cap for a kind,
// checked in that order. Its settings live in a store.QueueSettingsStore,
// read once when the gate is made and written only when Update changes
// them; Admit reads them from memory.
type QueueGate struct {
	settings store.QueueSettingsStore
	pools    PoolSizes
	log      *slog.Logger

	update sync.Mutex // serializes Update
	cur    atomic.Pointer[gateState]
}

// gateState is one version of the settings and the channel closed when
// they are replaced.
type gateState struct {
	settings store.QueueSettings
	changed  chan struct{}
}

// NewQueueGate loads the stored settings and returns a gate over them for
// pools of the given sizes. A failure to read them is an error: running
// with the queue open would break the pause a user set.
func NewQueueGate(ctx context.Context, settings store.QueueSettingsStore, pools PoolSizes,
	log *slog.Logger) (*QueueGate, error) {
	s, err := settings.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("load queue settings: %w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	g := &QueueGate{settings: settings, pools: pools, log: log}
	g.cur.Store(&gateState{settings: s, changed: make(chan struct{})})
	log.Info("queue settings loaded", settingsAttrs(s)...)
	return g, nil
}

// Admit implements Gate.
func (g *QueueGate) Admit(kind store.JobKind, active int, now time.Time) Verdict {
	st := g.cur.Load()
	if v := st.verdict(now); !v.Open() {
		return v
	}
	if limit, capped := st.settings.Throttle.Cap(kind); capped && active >= limit {
		return Verdict{Closed: ReasonThrottled, Changed: st.changed}
	}
	return Verdict{}
}

// verdict decides what holds every kind back: a pause, then the schedule.
func (st *gateState) verdict(now time.Time) Verdict {
	s := st.settings
	switch {
	case s.Paused:
		return Verdict{Closed: ReasonPaused, Changed: st.changed}
	case !s.Schedule.IsZero() && !s.Schedule.Contains(now):
		return Verdict{Closed: ReasonOutsideSchedule, Until: s.Schedule.NextStart(now), Changed: st.changed}
	}
	return Verdict{}
}

// QueueState is the queue gate's settings and what they mean at a moment.
type QueueState struct {
	Settings store.QueueSettings
	// Closed is why the whole queue is closed: paused or outside_schedule,
	// or empty while it is open. A throttle never closes it.
	Closed GateReason
	// OpensAt is when the schedule next opens it; set only when Closed is
	// outside_schedule.
	OpensAt time.Time
	// Limits is how many jobs of each pool's kind may run at once while the
	// queue is open: fetch, index, then cluster.
	Limits []KindLimit
}

// KindLimit is how many jobs of Kind may run at once.
type KindLimit struct {
	Kind  store.JobKind
	Limit int
}

// State reports the settings in effect and what they mean at now.
func (g *QueueGate) State(now time.Time) QueueState {
	return g.state(g.cur.Load(), now)
}

// Watch is State with a channel closed once the settings it reports are
// replaced. Both come from one version of the settings, so a watcher that
// waits on the channel after acting on the state never misses a change.
func (g *QueueGate) Watch(now time.Time) (QueueState, <-chan struct{}) {
	st := g.cur.Load()
	return g.state(st, now), st.changed
}

// state is what st means at now.
func (g *QueueGate) state(st *gateState, now time.Time) QueueState {
	v := st.verdict(now)
	return QueueState{
		Settings: st.settings,
		Closed:   v.Closed,
		OpensAt:  v.Until,
		Limits: []KindLimit{
			{store.JobKindFetch, st.settings.Throttle.Limit(store.JobKindFetch, g.pools.Fetch)},
			{store.JobKindIndex, st.settings.Throttle.Limit(store.JobKindIndex, g.pools.Index)},
			{store.JobKindCluster, st.settings.Throttle.Limit(store.JobKindCluster, clusterPoolSize)},
		},
	}
}

// QueueUpdate changes some of the queue settings; nil fields are left as
// they are. A zero Schedule clears the schedule.
type QueueUpdate struct {
	Paused    *bool
	Throttle  *store.Throttle
	Schedule  *store.DailyWindow
	KeepAwake *bool
}

// Update applies u, stores the result, and only then publishes it, waking
// every worker waiting on an earlier verdict, and every watcher (Watch).
// It returns the settings in effect afterwards. An update that changes
// nothing writes and wakes nothing. When the store fails, the settings in
// effect stay as they were.
//
// ctx being done before the update starts stops it. After that the write
// runs detached from ctx, bounded by bookkeepingTimeout: a statement
// cancelled mid-way can be reported failed after it committed, which would
// leave the store and the gate disagreeing.
func (g *QueueGate) Update(ctx context.Context, u QueueUpdate) (store.QueueSettings, error) {
	if err := ctx.Err(); err != nil {
		return store.QueueSettings{}, err
	}
	g.update.Lock()
	defer g.update.Unlock()

	old := g.cur.Load()
	next := old.settings
	if u.Paused != nil {
		next.Paused = *u.Paused
	}
	if u.Throttle != nil {
		next.Throttle = *u.Throttle
	}
	if u.Schedule != nil {
		next.Schedule = *u.Schedule
	}
	if u.KeepAwake != nil {
		next.KeepAwake = *u.KeepAwake
	}
	if next == old.settings {
		return next, nil
	}

	wctx, cancel := bookkeepingContext(ctx)
	defer cancel()
	if err := g.settings.Put(wctx, next); err != nil {
		return store.QueueSettings{}, fmt.Errorf("store queue settings: %w", err)
	}
	// Published before the old channel closes, so a woken worker reads the
	// new settings.
	g.cur.Store(&gateState{settings: next, changed: make(chan struct{})})
	close(old.changed)
	g.log.Info("queue settings changed", settingsAttrs(next)...)
	return next, nil
}

// settingsAttrs are s as log attributes.
func settingsAttrs(s store.QueueSettings) []any {
	schedule := "off"
	if !s.Schedule.IsZero() {
		schedule = s.Schedule.String()
	}
	return []any{"paused", s.Paused, "throttle", string(s.Throttle), "schedule", schedule, "keep_awake", s.KeepAwake}
}
