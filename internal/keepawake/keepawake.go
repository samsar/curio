// Package keepawake keeps the Mac from idle sleep while the daemon has work
// to do, so an import carries on unattended. It runs macOS's own tools, no
// cgo: `pmset -g ps` for the power source, and `caffeinate -i -w <daemon
// pid>` as the hold, which ends with the daemon however it ends.
//
// The rule: hold while keep-awake is on, the queue isn't paused, the Mac
// runs on AC power, and the workers have jobs of their kinds pending or
// running. A queue closed only by its schedule keeps the hold, so an idle
// Mac doesn't sleep through the window an overnight import waits for; a
// pause means "not now", and lets it sleep.
package keepawake

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
)

// DefaultInterval paces the keeper while it has work to watch: how often
// it counts the queue, which is how it learns the queue drained, and reads
// the power source, which is how it learns the Mac was unplugged.
const DefaultInterval = time.Minute

// Settings is where the keeper reads keep-awake and the pause: the
// daemon's queue gate, whose change channel wakes it the moment either
// changes.
type Settings interface {
	Watch(now time.Time) (jobs.QueueState, <-chan struct{})
}

// Jobs is what the keeper reads of the job queue.
type Jobs interface {
	QueueCounts(ctx context.Context) (map[store.JobKind]store.QueueCount, error)
	Enqueued(kinds []store.JobKind) <-chan struct{}
}

// Options configure a Keeper.
type Options struct {
	Settings Settings
	Jobs     Jobs
	// Kinds are the kinds the daemon's workers claim. Jobs of any other
	// kind wait for nobody, and would hold the Mac awake for ever.
	Kinds    []store.JobKind
	Probe    Probe
	Asserter Asserter
	Interval time.Duration // default DefaultInterval
	Log      *slog.Logger  // default slog.Default()
}

// State is what the keeper reports, for GET /v1/queue.
type State struct {
	Enabled bool  // keep-awake is on
	Active  bool  // the Mac is being held awake now
	Power   Power // the last reading; PowerUnknown until the first
}

// Keeper holds the Mac awake by the package's rule. Run does the work;
// State reports it.
type Keeper struct {
	opts Options

	mu    sync.Mutex
	state State
}

// New returns a Keeper over opts.
func New(opts Options) *Keeper {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Keeper{opts: opts, state: State{Power: PowerUnknown}}
}

// State reports whether keep-awake is on, whether the Mac is held awake,
// and the power source last read. It reads memory only.
func (k *Keeper) State() State {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.state
}

func (k *Keeper) update(change func(*State)) {
	k.mu.Lock()
	defer k.mu.Unlock()
	change(&k.state)
}

// Run keeps to the rule until ctx ends, then releases the hold. With
// keep-awake off it runs nothing and only waits for the setting to change.
func (k *Keeper) Run(ctx context.Context) {
	r := &run{k: k, power: PowerUnknown}
	defer r.release("daemon stopping")
	settingsChanged := true
	for {
		st, changed := k.opts.Settings.Watch(time.Now())
		wake := r.step(ctx, st.Settings, settingsChanged)
		settingsChanged = false
		select {
		case <-ctx.Done():
			return
		case <-changed:
			settingsChanged = true
		case <-wake.interval:
		case <-wake.enqueued:
		case <-wake.holdEnded:
			r.lost()
		}
	}
}

// run is the state of one Run.
type run struct {
	k     *Keeper
	hold  Hold  // nil while not holding
	power Power // the last reading
	// readAt is the start of the step that last read the power source;
	// zero for never.
	readAt time.Time
	// retryAt is when a hold may be tried again after one failed to start
	// or ended by itself: once an interval, never in a loop.
	retryAt time.Time
	// Failure streaks, each logged at its start.
	powerFailing, countFailing, holdFailing bool
}

// wakeups are what a step waits on besides a settings change; nil never
// fires.
type wakeups struct {
	interval  <-chan time.Time
	enqueued  <-chan struct{}
	holdEnded <-chan struct{}
}

// step applies the rule to settings s and the queue as it is now.
// settingsChanged means s differs from the last step's, which is worth a
// fresh power reading.
func (r *run) step(ctx context.Context, s store.QueueSettings, settingsChanged bool) wakeups {
	k := r.k
	// Readings and retries due an interval after this step are stamped
	// with its start, taken before its interval timer is armed: that
	// timer wakes the next step no earlier than they fall due, however
	// long this step's count and probe take.
	now := time.Now()
	k.update(func(st *State) { st.Enabled = s.KeepAwake })
	switch {
	case !s.KeepAwake:
		r.release("turned off")
		return wakeups{}
	case s.Paused:
		r.release("paused")
		return wakeups{}
	}
	// Taken before counting, so a job enqueued meanwhile still wakes an
	// idle keeper.
	enqueued := k.opts.Jobs.Enqueued(k.opts.Kinds)
	w := wakeups{interval: time.After(k.opts.Interval)}
	pending, running, err := r.count(ctx)
	switch {
	case err != nil:
		// The hold stays as it is until a count says otherwise.
	case pending+running == 0:
		r.release("queue drained")
		// Work arrives by enqueue; while there is work, enqueues say
		// nothing new, and an import makes hundreds a minute.
		w.enqueued = enqueued
	default:
		if settingsChanged || r.readAt.IsZero() || now.Sub(r.readAt) >= k.opts.Interval {
			r.readPower(ctx, now)
		}
		switch r.power {
		case PowerAC:
			r.start(ctx, now, pending, running)
		case PowerBattery:
			r.release("on battery")
		case PowerUnknown:
			r.release("power source unknown")
		}
	}
	if r.hold != nil {
		w.holdEnded = r.hold.Done()
	}
	return w
}

// count sums the pending and running jobs of the workers' kinds.
func (r *run) count(ctx context.Context) (pending, running int, err error) {
	counts, err := r.k.opts.Jobs.QueueCounts(ctx)
	if err != nil {
		if !r.countFailing {
			r.k.opts.Log.Warn("keep-awake: can't count the queue; the hold stays as it is", "err", err)
		}
		r.countFailing = true
		return 0, 0, err
	}
	r.countFailing = false
	for _, kind := range r.k.opts.Kinds {
		pending += counts[kind].Pending
		running += counts[kind].Running
	}
	return pending, running, nil
}

// readPower reads the power source for the step that began at now. A
// reading that fails is unknown, which holds nothing: the Mac may be on
// battery.
func (r *run) readPower(ctx context.Context, now time.Time) {
	power, err := r.k.opts.Probe.Power(ctx)
	r.readAt = now
	if err != nil && !r.powerFailing {
		r.k.opts.Log.Warn("keep-awake: can't read the power source, so the Mac isn't held awake", "err", err)
	}
	r.powerFailing = err != nil
	r.power = power
	r.k.update(func(st *State) { st.Power = power })
}

// start holds the Mac awake for the step that began at now, unless it is
// held already or a hold failed within the last interval. A hold that
// can't start (no caffeinate, say) is retried once an interval and warned
// about once, until one starts.
func (r *run) start(ctx context.Context, now time.Time, pending, running int) {
	if r.hold != nil || now.Before(r.retryAt) {
		return
	}
	h, err := r.k.opts.Asserter.Hold(ctx)
	if err != nil {
		if !r.holdFailing {
			r.k.opts.Log.Warn("keep-awake: can't hold the Mac awake; trying again once an interval",
				"err", err, "interval", r.k.opts.Interval)
		}
		r.holdFailing = true
		r.retryAt = now.Add(r.k.opts.Interval)
		return
	}
	r.holdFailing = false
	r.hold = h
	r.k.update(func(st *State) { st.Active = true })
	r.k.opts.Log.Info("keep-awake: holding the Mac awake", "caffeinate_pid", h.PID(), "pending", pending, "running", running)
}

// release ends the hold, if any, for reason.
func (r *run) release(reason string) {
	if r.hold == nil {
		return
	}
	r.hold.Release()
	r.hold = nil
	r.k.update(func(st *State) { st.Active = false })
	r.k.opts.Log.Info("keep-awake: released", "reason", reason)
}

// lost records that the hold's process ended by itself. The next hold
// starts an interval from now at the earliest, so a caffeinate that can't
// stay up isn't restarted in a loop.
func (r *run) lost() {
	r.k.opts.Log.Warn("keep-awake: caffeinate exited unexpectedly; trying again in an interval",
		"caffeinate_pid", r.hold.PID(), "err", r.hold.Err(), "interval", r.k.opts.Interval)
	r.hold = nil
	r.k.update(func(st *State) { st.Active = false })
	r.retryAt = time.Now().Add(r.k.opts.Interval)
}
