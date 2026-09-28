package keepawake

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
)

// poolKinds are the kinds the daemon's workers claim.
var poolKinds = []store.JobKind{store.JobKindFetch, store.JobKindIndex, store.JobKindCluster}

// memSettings is an in-memory store.QueueSettingsStore, for a real queue
// gate.
type memSettings struct {
	mu sync.Mutex
	s  store.QueueSettings
}

func (m *memSettings) Get(context.Context) (store.QueueSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.s, nil
}

func (m *memSettings) Put(_ context.Context, s store.QueueSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s = s
	return nil
}

// fakeJobs is a job queue whose counts a test sets; enqueue wakes
// Enqueued's waiters, as the store's does.
type fakeJobs struct {
	mu       sync.Mutex
	counts   map[store.JobKind]store.QueueCount
	enqueued chan struct{}
	counted  int
}

func newFakeJobs() *fakeJobs {
	return &fakeJobs{counts: map[store.JobKind]store.QueueCount{}, enqueued: make(chan struct{})}
}

func (j *fakeJobs) QueueCounts(context.Context) (map[store.JobKind]store.QueueCount, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.counted++
	return maps.Clone(j.counts), nil
}

func (j *fakeJobs) Enqueued([]store.JobKind) <-chan struct{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.enqueued
}

// set sets kind's counts without waking anyone, as jobs finishing do.
func (j *fakeJobs) set(kind store.JobKind, pending, running int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.counts[kind] = store.QueueCount{Pending: pending, Running: running}
}

// enqueue adds a pending job of kind and wakes Enqueued's waiters.
func (j *fakeJobs) enqueue(kind store.JobKind) {
	j.mu.Lock()
	defer j.mu.Unlock()
	c := j.counts[kind]
	c.Pending++
	j.counts[kind] = c
	close(j.enqueued)
	j.enqueued = make(chan struct{})
}

func (j *fakeJobs) countCalls() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.counted
}

// fakeProbe reports the power a test sets, and records when each read
// began.
type fakeProbe struct {
	mu    sync.Mutex
	power Power
	// script, when set, is answered in turn before power.
	script []Power
	err    error
	// latency is how long each read takes, as pmset takes a few
	// milliseconds.
	latency time.Duration
	reads   []time.Time
}

func (p *fakeProbe) Power(ctx context.Context) (Power, error) {
	p.mu.Lock()
	p.reads = append(p.reads, time.Now())
	power, err, latency := p.power, p.err, p.latency
	if len(p.script) > 0 {
		power, p.script = p.script[0], p.script[1:]
	}
	p.mu.Unlock()
	select {
	case <-time.After(latency):
	case <-ctx.Done():
		return PowerUnknown, ctx.Err()
	}
	if err != nil {
		return PowerUnknown, err
	}
	return power, nil
}

func (p *fakeProbe) set(power Power) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.power = power
}

func (p *fakeProbe) readCount() int {
	return len(p.readTimes())
}

func (p *fakeProbe) readTimes() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.reads...)
}

// fakeAsserter records its holds. With exitAtOnce each hold's process
// ends as soon as it starts; with err set, no hold starts.
type fakeAsserter struct {
	mu         sync.Mutex
	holds      []*fakeHold
	exitAtOnce bool
	err        error
	tries      int
}

type fakeHold struct {
	pid      int
	done     chan struct{}
	once     sync.Once
	released bool
}

func (a *fakeAsserter) Hold(context.Context) (Hold, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tries++
	if a.err != nil {
		return nil, a.err
	}
	h := &fakeHold{pid: 1000 + len(a.holds), done: make(chan struct{})}
	if a.exitAtOnce {
		h.end()
	}
	a.holds = append(a.holds, h)
	return h, nil
}

func (h *fakeHold) end()                  { h.once.Do(func() { close(h.done) }) }
func (h *fakeHold) PID() int              { return h.pid }
func (h *fakeHold) Done() <-chan struct{} { return h.done }
func (*fakeHold) Err() error              { return errors.New("exit status 1") }

func (h *fakeHold) Release() {
	h.released = true
	h.end()
}

// started is how many holds have been started, tried how many Hold
// calls were made, and held how many holds are still held.
func (a *fakeAsserter) started() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.holds)
}

func (a *fakeAsserter) tried() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tries
}

func (a *fakeAsserter) fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

func (a *fakeAsserter) held() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, h := range a.holds {
		select {
		case <-h.done:
		default:
			n++
		}
	}
	return n
}

// logRecorder is a slog handler that keeps every record.
type logRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler     { return r }
func (r *logRecorder) WithGroup(string) slog.Handler          { return r }

func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Clone())
	return nil
}

// with returns the attributes of each record with message msg.
func (r *logRecorder) with(msg string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, rec := range r.records {
		if rec.Message != msg {
			continue
		}
		attrs := map[string]any{}
		rec.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Resolve().Any()
			return true
		})
		out = append(out, attrs)
	}
	return out
}

// at returns the messages logged at level.
func (r *logRecorder) at(level slog.Level) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, rec := range r.records {
		if rec.Level == level {
			out = append(out, rec.Message)
		}
	}
	return out
}

// releases returns the reasons of the releases logged so far.
func (r *logRecorder) releases() []any {
	released := r.with("keep-awake: released")
	out := make([]any, 0, len(released))
	for _, attrs := range released {
		out = append(out, attrs["reason"])
	}
	return out
}

// harness is a Keeper over fakes, running until the test ends.
type harness struct {
	keeper   *Keeper
	gate     *jobs.QueueGate
	jobs     *fakeJobs
	probe    *fakeProbe
	asserter *fakeAsserter
	logs     *logRecorder
	stop     func() // cancels Run and waits for it
}

// start runs a Keeper with keep-awake set to on, work of jobs, power, and
// interval.
func start(t *testing.T, on bool, power Power, interval time.Duration, setup func(*harness)) *harness {
	t.Helper()
	settings := &memSettings{s: store.DefaultQueueSettings()}
	settings.s.KeepAwake = on
	gate, err := jobs.NewQueueGate(context.Background(), settings, jobs.PoolSizes{Fetch: 1, Index: 1},
		slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	h := &harness{gate: gate, jobs: newFakeJobs(), probe: &fakeProbe{power: power}, asserter: &fakeAsserter{},
		logs: &logRecorder{}}
	if setup != nil {
		setup(h)
	}
	h.keeper = New(Options{Settings: gate, Jobs: h.jobs, Kinds: poolKinds, Probe: h.probe, Asserter: h.asserter,
		Interval: interval, Log: slog.New(h.logs)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.keeper.Run(ctx)
		close(done)
	}()
	var once sync.Once
	h.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("Run did not return within 3s of its context ending")
			}
		})
	}
	t.Cleanup(h.stop)
	return h
}

func (h *harness) update(t *testing.T, u jobs.QueueUpdate) {
	t.Helper()
	_, err := h.gate.Update(context.Background(), u)
	require.NoError(t, err)
}

func (h *harness) eventuallyHeld(t *testing.T, want int, msg string) {
	t.Helper()
	require.Eventually(t, func() bool { return h.asserter.held() == want }, 2*time.Second, 5*time.Millisecond, msg)
}

// eventuallyReleased waits for the keeper to have logged releases for
// reasons, which it does after ending the hold and updating its State.
func (h *harness) eventuallyReleased(t *testing.T, reasons ...any) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, reasons, h.logs.releases())
	}, 2*time.Second, 5*time.Millisecond)
	assert.Zero(t, h.asserter.held())
}

const (
	tick  = 50 * time.Millisecond // a short interval
	never = time.Hour             // an interval no test waits out
)

func withWork(h *harness) { h.jobs.set(store.JobKindFetch, 3, 1) }

func TestKeeper_HoldsOnACWithWork(t *testing.T) {
	h := start(t, true, PowerAC, never, withWork)
	h.eventuallyHeld(t, 1, "held")
	assert.Equal(t, 1, h.asserter.started())
	assert.Equal(t, State{Enabled: true, Active: true, Power: PowerAC}, h.keeper.State())
	holding := h.logs.with("keep-awake: holding the Mac awake")
	require.Len(t, holding, 1)
	assert.EqualValues(t, 1000, holding[0]["caffeinate_pid"])
	assert.EqualValues(t, 3, holding[0]["pending"])
	assert.EqualValues(t, 1, holding[0]["running"])
}

// TestKeeper_FollowsThePower: on battery nothing is held; plugging in holds
// within an interval, and unplugging releases within one.
func TestKeeper_FollowsThePower(t *testing.T) {
	h := start(t, true, PowerBattery, tick, withWork)
	require.Never(t, func() bool { return h.asserter.started() > 0 }, 4*tick, tick/5, "nothing held on battery")
	assert.Equal(t, State{Enabled: true, Power: PowerBattery}, h.keeper.State())

	h.probe.set(PowerAC)
	h.eventuallyHeld(t, 1, "held once on AC")
	h.probe.set(PowerBattery)
	h.eventuallyReleased(t, "on battery")
	h.probe.set(PowerAC)
	h.eventuallyHeld(t, 1, "held again")
}

// TestKeeper_SlowProbe: the power source is read again at the first
// interval however long a reading takes (pmset takes a few milliseconds),
// so unplugging releases within an interval, not two.
func TestKeeper_SlowProbe(t *testing.T) {
	const interval = 300 * time.Millisecond
	h := start(t, true, PowerBattery, interval, func(h *harness) {
		withWork(h)
		h.probe.script = []Power{PowerAC}
		h.probe.latency = 30 * time.Millisecond
	})
	h.eventuallyHeld(t, 1, "held on AC")
	h.eventuallyReleased(t, "on battery")
	reads := h.probe.readTimes()
	require.GreaterOrEqual(t, len(reads), 2)
	assert.Less(t, reads[1].Sub(reads[0]), interval*3/2, "read again at the first interval")
}

// TestKeeper_NoCaffeinate: a hold that can't start (no caffeinate, say) is
// tried once an interval and warned about once, until one starts; a later
// failure starts a new streak, warned about again.
func TestKeeper_NoCaffeinate(t *testing.T) {
	missing := errors.New("start /usr/bin/caffeinate: fork/exec /usr/bin/caffeinate: no such file or directory")
	h := start(t, true, PowerAC, tick, func(h *harness) {
		withWork(h)
		h.asserter.err = missing
	})
	begin := time.Now()
	require.Never(t, func() bool { return h.asserter.tried() > int(time.Since(begin)/tick)+2 },
		8*tick, tick/5, "at most once an interval")
	assert.GreaterOrEqual(t, h.asserter.tried(), 3, "tried again")
	assert.Zero(t, h.asserter.started())
	assert.Equal(t, State{Enabled: true, Power: PowerAC}, h.keeper.State())
	warning := "keep-awake: can't hold the Mac awake; trying again once an interval"
	assert.Equal(t, []string{warning}, h.logs.at(slog.LevelWarn))

	h.asserter.fail(nil)
	h.eventuallyHeld(t, 1, "held once caffeinate starts")
	h.asserter.fail(missing)
	h.update(t, jobs.QueueUpdate{KeepAwake: new(false)})
	h.eventuallyReleased(t, "turned off")
	h.update(t, jobs.QueueUpdate{KeepAwake: new(true)})
	require.Eventually(t, func() bool { return len(h.logs.at(slog.LevelWarn)) == 2 }, 2*time.Second, 5*time.Millisecond,
		"a new streak is warned about")
	require.Never(t, func() bool { return len(h.logs.at(slog.LevelWarn)) > 2 }, 4*tick, tick/5)
	assert.Equal(t, []string{warning, warning}, h.logs.at(slog.LevelWarn))
}

// TestKeeper_Drains: a queue that drains is released at the next
// interval.
func TestKeeper_Drains(t *testing.T) {
	h := start(t, true, PowerAC, tick, withWork)
	h.eventuallyHeld(t, 1, "held")
	h.jobs.set(store.JobKindFetch, 0, 0)
	h.eventuallyReleased(t, "queue drained")
}

// TestKeeper_EnqueueWhileIdle: work arriving at an idle keeper is held at
// once, not at the next interval.
func TestKeeper_EnqueueWhileIdle(t *testing.T) {
	h := start(t, true, PowerAC, never, nil)
	require.Never(t, func() bool { return h.asserter.started() > 0 }, 4*tick, tick/5)
	assert.Zero(t, h.probe.readCount(), "no work, no power reading")
	h.jobs.enqueue(store.JobKindIndex)
	h.eventuallyHeld(t, 1, "held without waiting out the interval")
}

// TestKeeper_Pause: a pause releases at once and resuming holds again; a
// schedule that excludes now keeps the hold, since it waits for its
// window.
func TestKeeper_Pause(t *testing.T) {
	h := start(t, true, PowerAC, never, withWork)
	h.eventuallyHeld(t, 1, "held")

	h.update(t, jobs.QueueUpdate{Paused: new(true)})
	h.eventuallyReleased(t, "paused")
	h.update(t, jobs.QueueUpdate{Paused: new(false)})
	h.eventuallyHeld(t, 1, "held again")

	now := time.Now()
	m := now.Hour()*60 + now.Minute()
	window := store.DailyWindow{Start: (m + 120) % (24 * 60), End: (m + 180) % (24 * 60)}
	h.update(t, jobs.QueueUpdate{Schedule: &window})
	require.Never(t, func() bool { return h.asserter.held() == 0 }, 4*tick, tick/5, "the schedule keeps the hold")
	assert.Equal(t, 2, h.asserter.started())
}

// TestKeeper_TurnedOff: turning keep-awake off releases at once.
func TestKeeper_TurnedOff(t *testing.T) {
	h := start(t, true, PowerAC, never, withWork)
	h.eventuallyHeld(t, 1, "held")
	h.update(t, jobs.QueueUpdate{KeepAwake: new(false)})
	h.eventuallyReleased(t, "turned off")
	assert.Equal(t, State{Power: PowerAC}, h.keeper.State())
}

// TestKeeper_OffRunsNothing: with keep-awake off, the keeper neither reads
// the power, counts the queue, nor holds anything, however much is
// enqueued.
func TestKeeper_OffRunsNothing(t *testing.T) {
	h := start(t, false, PowerAC, tick, withWork)
	for range 5 {
		h.jobs.enqueue(store.JobKindFetch)
		require.Never(t, func() bool { return h.probe.readCount()+h.asserter.started()+h.jobs.countCalls() > 0 },
			tick, tick/5)
	}
	assert.Zero(t, h.probe.readCount())
	assert.Zero(t, h.asserter.started())
	assert.Zero(t, h.jobs.countCalls())
	assert.Equal(t, State{Power: PowerUnknown}, h.keeper.State())
}

// TestKeeper_EnqueuesDontReadThePower: an import enqueues hundreds of jobs
// a minute; while there is work, none of them wakes the keeper.
func TestKeeper_EnqueuesDontReadThePower(t *testing.T) {
	h := start(t, true, PowerBattery, never, withWork)
	require.Eventually(t, func() bool { return h.probe.readCount() == 1 }, 2*time.Second, 5*time.Millisecond)
	for range 100 {
		h.jobs.enqueue(store.JobKindFetch)
	}
	require.Never(t, func() bool { return h.probe.readCount() > 1 }, 4*tick, tick/5)
}

// TestKeeper_OnlyThePoolsKinds: jobs of a kind no worker claims hold
// nothing: they would wait for ever.
func TestKeeper_OnlyThePoolsKinds(t *testing.T) {
	h := start(t, true, PowerAC, tick, func(h *harness) { h.jobs.set(store.JobKindImport, 5, 0) })
	require.Never(t, func() bool { return h.asserter.started() > 0 }, 4*tick, tick/5)
}

// TestKeeper_StopReleases: the daemon stopping releases the hold before
// Run returns.
func TestKeeper_StopReleases(t *testing.T) {
	h := start(t, true, PowerAC, never, withWork)
	h.eventuallyHeld(t, 1, "held")
	h.stop()
	assert.Zero(t, h.asserter.held())
	assert.True(t, h.asserter.holds[0].released)
	assert.Equal(t, []any{"daemon stopping"}, h.logs.releases())
}

// TestKeeper_HoldThatWontStay: a caffeinate that exits as it starts is
// restarted once an interval, not in a loop, with a warning each time.
func TestKeeper_HoldThatWontStay(t *testing.T) {
	h := start(t, true, PowerAC, tick, func(h *harness) {
		withWork(h)
		h.asserter.exitAtOnce = true
	})
	begin := time.Now()
	require.Never(t, func() bool { return h.asserter.started() > int(time.Since(begin)/tick)+2 },
		10*tick, tick/5, "at most once an interval")
	h.stop()
	started := h.asserter.started()
	assert.GreaterOrEqual(t, started, 2, "tried again")
	assert.Len(t, h.logs.with("keep-awake: caffeinate exited unexpectedly; trying again in an interval"), started)
}

// TestKeeper_NoPowerReading: without an answer about the power source
// (no pmset, say), nothing is held, and it is said once.
func TestKeeper_NoPowerReading(t *testing.T) {
	h := start(t, true, PowerAC, tick, func(h *harness) {
		withWork(h)
		h.probe.err = errors.New("exec: \"/usr/bin/pmset\": no such file or directory")
	})
	require.Eventually(t, func() bool { return h.probe.readCount() >= 3 }, 2*time.Second, 5*time.Millisecond)
	assert.Zero(t, h.asserter.started())
	assert.Equal(t, []string{"keep-awake: can't read the power source, so the Mac isn't held awake"},
		h.logs.at(slog.LevelWarn))
	assert.Equal(t, State{Enabled: true, Power: PowerUnknown}, h.keeper.State())
}

func TestParsePower(t *testing.T) {
	cases := []struct {
		name, out string
		want      Power
	}{
		{"AC", "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged; 0:00 remaining present: true\n", PowerAC},
		{"battery", "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t87%; discharging; 5:12 remaining present: true\n", PowerBattery},
		{"UPS", "Now drawing from 'UPS Power'\n", PowerBattery},
		{"a desktop, no battery line", "Now drawing from 'AC Power'\n", PowerAC},
		{"empty", "", PowerUnknown},
		{"garbage", "Now drawing from 'Solar Power'\n", PowerUnknown},
		{"cut short", "Now drawing from 'AC Pow", PowerUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parsePower(tc.out))
		})
	}
}

func TestPmset(t *testing.T) {
	ctx := context.Background()
	t.Run("AC", func(t *testing.T) {
		t.Setenv(fakePowerEnv, "AC Power")
		power, err := Pmset{Bin: fakeTool(t, modePmset)}.Power(ctx)
		require.NoError(t, err)
		assert.Equal(t, PowerAC, power)
	})
	t.Run("battery", func(t *testing.T) {
		t.Setenv(fakePowerEnv, "Battery Power")
		power, err := Pmset{Bin: fakeTool(t, modePmset)}.Power(ctx)
		require.NoError(t, err)
		assert.Equal(t, PowerBattery, power)
	})
	t.Run("an answer without the line", func(t *testing.T) {
		t.Setenv(fakePowerEnv, "")
		power, err := Pmset{Bin: fakeTool(t, modePmset)}.Power(ctx)
		require.NoError(t, err)
		assert.Equal(t, PowerUnknown, power)
	})
	t.Run("a failure", func(t *testing.T) {
		power, err := Pmset{Bin: fakeTool(t, modePmsetFail)}.Power(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pmset -g ps: exit status 1")
		assert.Equal(t, PowerUnknown, power)
	})
	t.Run("no answer", func(t *testing.T) {
		p := Pmset{Bin: fakeTool(t, modePmsetHang), Timeout: 300 * time.Millisecond}
		start := time.Now()
		power, err := p.Power(ctx)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, PowerUnknown, power)
		assert.Less(t, time.Since(start), p.Timeout+toolWaitDelay+time.Second)
	})
	t.Run("no pmset", func(t *testing.T) {
		power, err := Pmset{Bin: filepath.Join(t.TempDir(), "pmset")}.Power(ctx)
		require.Error(t, err)
		assert.Equal(t, PowerUnknown, power)
	})
}

// TestCaffeinate: the hold runs caffeinate -i -w <daemon pid>, and a
// release sends it SIGTERM and reaps it.
func TestCaffeinate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(fakeDirEnv, dir)
	c := Caffeinate{Bin: fakeTool(t, modeCaffeinate), PID: 4242}

	h, err := c.Hold(context.Background())
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(caffeinateRuns(t, dir)) == 1 }, 5*time.Second, 5*time.Millisecond,
		"the fake recorded its run")
	run := caffeinateRuns(t, dir)[0]
	assert.Equal(t, []string{"-i", "-w", "4242"}, run.Args)
	assert.Equal(t, h.PID(), run.PID)

	h.Release()
	runs := caffeinateRuns(t, dir)
	require.Len(t, runs, 2)
	assert.Equal(t, caffeinateRun{PID: h.PID(), Signal: "SIGTERM"}, runs[1])
	assert.NoError(t, h.Err(), "a released caffeinate exits cleanly")
	assert.False(t, alive(h.PID()), "reaped")
}

// TestCaffeinate_ExitsByItself: a caffeinate that exits on its own closes
// Done and reports how it ended, and a release after that signals
// nothing.
func TestCaffeinate_ExitsByItself(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(fakeDirEnv, dir)
	h, err := Caffeinate{Bin: fakeTool(t, modeCaffeinateExit), PID: os.Getpid()}.Hold(context.Background())
	require.NoError(t, err)
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after caffeinate exited")
	}
	require.Error(t, h.Err())
	assert.Contains(t, h.Err().Error(), "exit status 1")

	h.Release()
	assert.Len(t, caffeinateRuns(t, dir), 1, "no SIGTERM recorded: there was no process to signal")
}

func TestCaffeinate_Missing(t *testing.T) {
	_, err := Caffeinate{Bin: filepath.Join(t.TempDir(), "caffeinate"), PID: os.Getpid()}.Hold(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "caffeinate")
}

// TestKeeper_RealRunners: the keeper over the fake pmset and caffeinate
// holds with exactly one caffeinate following the daemon's pid, and its
// stop leaves no caffeinate behind.
func TestKeeper_RealRunners(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(fakeDirEnv, dir)
	t.Setenv(fakePowerEnv, "AC Power")
	// One test binary plays both tools, each by the name it runs as.
	exe := fakeTool(t, modeByName)
	pmset := filepath.Join(t.TempDir(), "pmset")
	caffeinate := filepath.Join(t.TempDir(), "caffeinate")
	require.NoError(t, os.Symlink(exe, pmset))
	require.NoError(t, os.Symlink(exe, caffeinate))
	gate, err := jobs.NewQueueGate(context.Background(),
		&memSettings{s: store.QueueSettings{Throttle: store.ThrottleNormal, KeepAwake: true}},
		jobs.PoolSizes{Fetch: 1, Index: 1}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	queue := newFakeJobs()
	queue.set(store.JobKindIndex, 2, 0)
	keeper := New(Options{Settings: gate, Jobs: queue, Kinds: poolKinds, Probe: Pmset{Bin: pmset},
		Asserter: Caffeinate{Bin: caffeinate, PID: os.Getpid()}, Interval: never, Log: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		keeper.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	require.Eventually(t, func() bool { return keeper.State().Active && len(caffeinateRuns(t, dir)) == 1 },
		5*time.Second, 5*time.Millisecond)
	run := caffeinateRuns(t, dir)[0]
	assert.Equal(t, []string{"-i", "-w", pidString(os.Getpid())}, run.Args)
	assert.Equal(t, PowerAC, keeper.State().Power)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s")
	}
	runs := caffeinateRuns(t, dir)
	require.Len(t, runs, 2, "one caffeinate, and its SIGTERM")
	assert.Equal(t, "SIGTERM", runs[1].Signal)
	assert.False(t, alive(run.PID), "no caffeinate left")
}
