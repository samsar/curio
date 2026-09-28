package jobs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// memSettings is an in-memory store.QueueSettingsStore that counts its
// calls. A Put fails with putErr when it is set, and waits for release,
// after sending its context on putCalled, when those are set.
type memSettings struct {
	mu         sync.Mutex
	settings   store.QueueSettings
	gets, puts int
	getErr     error
	putErr     error
	putCalled  chan context.Context
	release    chan struct{}
}

func newMemSettings(s store.QueueSettings) *memSettings { return &memSettings{settings: s} }

func (m *memSettings) Get(context.Context) (store.QueueSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	return m.settings, m.getErr
}

func (m *memSettings) Put(ctx context.Context, s store.QueueSettings) error {
	if m.putCalled != nil {
		m.putCalled <- ctx
		<-m.release
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	if m.putErr != nil {
		return m.putErr
	}
	m.settings = s
	return nil
}

func (m *memSettings) stored() (store.QueueSettings, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings, m.gets, m.puts
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

// messages returns the attributes of each record with message msg, and
// fails the test if one isn't at info.
func (r *logRecorder) messages(t *testing.T, msg string) []map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, rec := range r.records {
		if rec.Message != msg {
			continue
		}
		assert.Equal(t, slog.LevelInfo, rec.Level, msg)
		attrs := map[string]any{}
		rec.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Resolve().Any()
			return true
		})
		out = append(out, attrs)
	}
	return out
}

var daemonPools = PoolSizes{Fetch: 16, Index: 4}

func newGate(t *testing.T, s store.QueueSettings) (*QueueGate, *memSettings) {
	t.Helper()
	m := newMemSettings(s)
	g, err := NewQueueGate(context.Background(), m, daemonPools, quietLog)
	require.NoError(t, err)
	return g, m
}

// zone is a fixed zone for the schedule tests, so they don't depend on the
// machine's.
var zone = time.FixedZone("UTC-5", -5*60*60)

func at(day, hour, minute int) time.Time { return time.Date(2026, 6, day, hour, minute, 0, 0, zone) }

func window(t *testing.T, s string) store.DailyWindow {
	t.Helper()
	w, err := store.ParseDailyWindow(s)
	require.NoError(t, err)
	return w
}

// closedGate closes every kind with its own reason, as a later policy
// (running on battery, say) would.
type closedGate struct{ reason GateReason }

func (g closedGate) Admit(store.JobKind, int, time.Time) Verdict {
	return Verdict{Closed: g.reason, Until: time.Now().Add(time.Minute)}
}

// TestAll: a new policy joins the queue gate without changes to either;
// the first closed verdict wins.
func TestAll(t *testing.T) {
	now := at(10, 12, 0)
	open, _ := newGate(t, store.DefaultQueueSettings())
	paused, _ := newGate(t, store.QueueSettings{Paused: true, Throttle: store.ThrottleNormal})
	battery := closedGate{reason: "on_battery"}

	assert.True(t, All().Admit(store.JobKindFetch, 0, now).Open(), "no gates admit everything")
	assert.True(t, All(open).Admit(store.JobKindFetch, 0, now).Open())
	assert.Equal(t, GateReason("on_battery"), All(open, battery).Admit(store.JobKindFetch, 0, now).Closed)
	assert.Equal(t, ReasonPaused, All(paused, battery).Admit(store.JobKindFetch, 0, now).Closed)
	assert.Equal(t, GateReason("on_battery"), All(battery, paused).Admit(store.JobKindFetch, 0, now).Closed)
}

// TestNewQueueGate_ReadsTheStoreOnce: the settings are read when the gate
// is made and never again; deciding a claim costs no I/O.
func TestNewQueueGate_ReadsTheStoreOnce(t *testing.T) {
	logs := &logRecorder{}
	m := newMemSettings(store.QueueSettings{Paused: true, Throttle: store.ThrottleGentle, Schedule: window(t, "22:00-07:00")})
	g, err := NewQueueGate(context.Background(), m, daemonPools, slog.New(logs))
	require.NoError(t, err)
	for i := range 1000 {
		g.Admit(store.JobKindFetch, i%8, time.Now())
		g.State(time.Now())
	}
	_, gets, puts := m.stored()
	assert.Equal(t, 1, gets)
	assert.Zero(t, puts)
	assert.Equal(t, []map[string]any{{"paused": true, "throttle": "gentle", "schedule": "22:00-07:00", "keep_awake": false}},
		logs.messages(t, "queue settings loaded"))
}

func TestNewQueueGate_StoreFailure(t *testing.T) {
	m := newMemSettings(store.DefaultQueueSettings())
	m.getErr = errors.New("database is locked")
	_, err := NewQueueGate(context.Background(), m, daemonPools, quietLog)
	require.ErrorIs(t, err, m.getErr)
	assert.ErrorContains(t, err, "load queue settings")
}

// TestQueueGate_Paused: a pause holds every kind back, in the schedule's
// window or out of it, until the settings change.
func TestQueueGate_Paused(t *testing.T) {
	g, _ := newGate(t, store.QueueSettings{Paused: true, Throttle: store.ThrottleNormal, Schedule: window(t, "22:00-07:00")})
	for _, now := range []time.Time{at(10, 23, 0), at(10, 12, 0)} {
		for _, kind := range []store.JobKind{store.JobKindFetch, store.JobKindIndex, store.JobKindCluster} {
			v := g.Admit(kind, 0, now)
			assert.Equal(t, ReasonPaused, v.Closed, "%s at %s", kind, now)
			assert.NotNil(t, v.Changed)
			assert.True(t, v.Until.IsZero(), "a pause doesn't end by itself")
		}
	}
}

// TestQueueGate_Schedule: outside its window the queue is closed until the
// window's next start; inside it, open.
func TestQueueGate_Schedule(t *testing.T) {
	cases := []struct {
		window string
		now    time.Time
		until  time.Time // zero: open
	}{
		{"22:00-07:00", at(10, 21, 59), at(10, 22, 0)},
		{"22:00-07:00", at(10, 22, 0), time.Time{}},
		{"22:00-07:00", at(10, 23, 59), time.Time{}},
		{"22:00-07:00", at(11, 0, 0), time.Time{}},
		{"22:00-07:00", at(11, 6, 59), time.Time{}},
		{"22:00-07:00", at(11, 7, 0), at(11, 22, 0)},
		{"09:00-17:00", at(10, 8, 59), at(10, 9, 0)},
		{"09:00-17:00", at(10, 17, 0), at(11, 9, 0)},
		{"09:00-17:00", at(10, 23, 30), at(11, 9, 0)},
	}
	for _, tc := range cases {
		g, _ := newGate(t, store.QueueSettings{Throttle: store.ThrottleNormal, Schedule: window(t, tc.window)})
		v := g.Admit(store.JobKindFetch, 0, tc.now)
		what := tc.window + " at " + tc.now.Format("15:04")
		if tc.until.IsZero() {
			assert.True(t, v.Open(), what)
			continue
		}
		assert.Equal(t, ReasonOutsideSchedule, v.Closed, what)
		assert.Equal(t, tc.until, v.Until, what)
		assert.NotNil(t, v.Changed, what)
	}
}

// TestQueueGate_ResumeKeepsTheSchedule: resuming lifts the pause and
// nothing else.
func TestQueueGate_ResumeKeepsTheSchedule(t *testing.T) {
	g, _ := newGate(t, store.QueueSettings{Paused: true, Throttle: store.ThrottleNormal, Schedule: window(t, "22:00-07:00")})
	_, err := g.Update(context.Background(), QueueUpdate{Paused: new(false)})
	require.NoError(t, err)
	assert.Equal(t, ReasonOutsideSchedule, g.Admit(store.JobKindFetch, 0, at(10, 12, 0)).Closed)
	assert.True(t, g.Admit(store.JobKindFetch, 0, at(10, 23, 0)).Open())
}

func TestQueueGate_Throttle(t *testing.T) {
	gentle, _ := newGate(t, store.QueueSettings{Throttle: store.ThrottleGentle})
	now := at(10, 12, 0)

	assert.True(t, gentle.Admit(store.JobKindFetch, 3, now).Open())
	v := gentle.Admit(store.JobKindFetch, 4, now)
	assert.Equal(t, ReasonThrottled, v.Closed)
	assert.NotNil(t, v.Changed)
	assert.True(t, v.Until.IsZero(), "a slot frees when a job ends, not at a time")

	assert.True(t, gentle.Admit(store.JobKindIndex, 0, now).Open())
	assert.Equal(t, ReasonThrottled, gentle.Admit(store.JobKindIndex, 1, now).Closed)

	for _, kind := range []store.JobKind{store.JobKindCluster, store.JobKindSummarize, store.JobKindImport} {
		assert.True(t, gentle.Admit(kind, 100, now).Open(), kind)
	}
	normal, _ := newGate(t, store.DefaultQueueSettings())
	for _, kind := range []store.JobKind{store.JobKindFetch, store.JobKindIndex, store.JobKindCluster} {
		assert.True(t, normal.Admit(kind, 100, now).Open(), kind)
	}
}

func TestQueueGate_State(t *testing.T) {
	limits := func(fetch, index, cluster int) []KindLimit {
		return []KindLimit{{store.JobKindFetch, fetch}, {store.JobKindIndex, index}, {store.JobKindCluster, cluster}}
	}
	schedule := window(t, "22:00-07:00")
	cases := []struct {
		name     string
		settings store.QueueSettings
		pools    PoolSizes
		now      time.Time
		want     QueueState
	}{
		{"open", store.DefaultQueueSettings(), daemonPools, at(10, 12, 0),
			QueueState{Limits: limits(16, 4, 1)}},
		{"gentle", store.QueueSettings{Throttle: store.ThrottleGentle}, daemonPools, at(10, 12, 0),
			QueueState{Limits: limits(4, 1, 1)}},
		{"gentle, smaller pools", store.QueueSettings{Throttle: store.ThrottleGentle}, PoolSizes{Fetch: 2, Index: 4},
			at(10, 12, 0), QueueState{Limits: limits(2, 1, 1)}},
		{"paused", store.QueueSettings{Paused: true, Throttle: store.ThrottleNormal, Schedule: schedule}, daemonPools,
			at(10, 12, 0), QueueState{Closed: ReasonPaused, Limits: limits(16, 4, 1)}},
		{"outside the schedule", store.QueueSettings{Throttle: store.ThrottleNormal, Schedule: schedule}, daemonPools,
			at(10, 12, 0), QueueState{Closed: ReasonOutsideSchedule, OpensAt: at(10, 22, 0), Limits: limits(16, 4, 1)}},
		{"inside the schedule", store.QueueSettings{Throttle: store.ThrottleNormal, Schedule: schedule}, daemonPools,
			at(10, 23, 0), QueueState{Limits: limits(16, 4, 1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := NewQueueGate(context.Background(), newMemSettings(tc.settings), tc.pools, quietLog)
			require.NoError(t, err)
			tc.want.Settings = tc.settings
			assert.Equal(t, tc.want, g.State(tc.now))
		})
	}
}

// TestQueueGate_Update: only the fields given change, the result is stored
// before it takes effect, and every earlier verdict's waiter is woken.
func TestQueueGate_Update(t *testing.T) {
	ctx := context.Background()
	logs := &logRecorder{}
	m := newMemSettings(store.QueueSettings{Throttle: store.ThrottleNormal, Schedule: window(t, "22:00-07:00")})
	g, err := NewQueueGate(ctx, m, daemonPools, slog.New(logs))
	require.NoError(t, err)
	now := at(10, 12, 0)
	earlier := []Verdict{g.Admit(store.JobKindFetch, 0, now), g.Admit(store.JobKindIndex, 0, now)}

	got, err := g.Update(ctx, QueueUpdate{Paused: new(true), Throttle: new(store.ThrottleGentle)})
	require.NoError(t, err)
	want := store.QueueSettings{Paused: true, Throttle: store.ThrottleGentle, Schedule: window(t, "22:00-07:00")}
	assert.Equal(t, want, got)
	assert.Equal(t, want, g.State(now).Settings)
	stored, _, puts := m.stored()
	assert.Equal(t, want, stored)
	assert.Equal(t, 1, puts)
	for _, v := range earlier {
		assert.True(t, isClosed(v.Changed), "an earlier verdict's waiter is woken")
	}
	later := g.Admit(store.JobKindFetch, 0, now)
	require.NotNil(t, later.Changed)
	assert.False(t, isClosed(later.Changed), "a later verdict waits for the next change")
	assert.Equal(t, []map[string]any{{"paused": true, "throttle": "gentle", "schedule": "22:00-07:00", "keep_awake": false}},
		logs.messages(t, "queue settings changed"))

	got, err = g.Update(ctx, QueueUpdate{Schedule: &store.DailyWindow{}})
	require.NoError(t, err)
	assert.Equal(t, store.QueueSettings{Paused: true, Throttle: store.ThrottleGentle}, got, "a zero window clears the schedule")
	assert.Equal(t, "off", logs.messages(t, "queue settings changed")[1]["schedule"])
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// TestQueueGate_UpdateThatChangesNothing writes and wakes nothing.
func TestQueueGate_UpdateThatChangesNothing(t *testing.T) {
	g, m := newGate(t, store.QueueSettings{Paused: true, Throttle: store.ThrottleNormal})
	v := g.Admit(store.JobKindFetch, 0, time.Now())

	got, err := g.Update(context.Background(), QueueUpdate{Paused: new(true), Throttle: new(store.ThrottleNormal)})
	require.NoError(t, err)
	assert.Equal(t, store.QueueSettings{Paused: true, Throttle: store.ThrottleNormal}, got)
	_, _, puts := m.stored()
	assert.Zero(t, puts)
	assert.False(t, isClosed(v.Changed))
}

// TestQueueGate_UpdateStoreFailure: settings that couldn't be stored don't
// take effect; a restart would lose them.
func TestQueueGate_UpdateStoreFailure(t *testing.T) {
	g, m := newGate(t, store.DefaultQueueSettings())
	m.putErr = errors.New("disk I/O error")
	before := g.State(time.Now())
	v := g.Admit(store.JobKindFetch, 0, time.Now())

	_, err := g.Update(context.Background(), QueueUpdate{Paused: new(true)})
	require.ErrorIs(t, err, m.putErr)
	assert.Equal(t, before, g.State(time.Now()))
	assert.False(t, isClosed(v.Changed), "nobody is woken for a change that didn't happen")
}

// TestQueueGate_ConcurrentUpdates: updates to different fields, made at
// once, all take effect, in memory and in the store.
func TestQueueGate_ConcurrentUpdates(t *testing.T) {
	g, m := newGate(t, store.DefaultQueueSettings())
	schedule := window(t, "22:00-07:00")
	updates := []QueueUpdate{{Paused: new(true)}, {Throttle: new(store.ThrottleGentle)}, {Schedule: &schedule}}
	var wg sync.WaitGroup
	for _, u := range updates {
		wg.Go(func() {
			_, err := g.Update(context.Background(), u)
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	want := store.QueueSettings{Paused: true, Throttle: store.ThrottleGentle, Schedule: schedule}
	assert.Equal(t, want, g.State(time.Now()).Settings)
	stored, _, _ := m.stored()
	assert.Equal(t, want, stored)
}

func TestQueueGate_UpdateWithDoneContext(t *testing.T) {
	g, m := newGate(t, store.DefaultQueueSettings())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := g.Update(ctx, QueueUpdate{Paused: new(true)})
	require.ErrorIs(t, err, context.Canceled)
	_, _, puts := m.stored()
	assert.Zero(t, puts)
	assert.False(t, g.State(time.Now()).Settings.Paused)
}

// TestQueueGate_UpdateOutlivesItsCaller: once the write has begun, the
// caller going away doesn't cut it short, and it takes effect. Meanwhile
// Admit answers from the settings in effect, without waiting.
func TestQueueGate_UpdateOutlivesItsCaller(t *testing.T) {
	g, m := newGate(t, store.DefaultQueueSettings())
	m.putCalled = make(chan context.Context, 1)
	m.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := g.Update(ctx, QueueUpdate{Paused: new(true)})
		done <- err
	}()

	putCtx := <-m.putCalled
	cancel()
	assert.NoError(t, putCtx.Err(), "the write isn't cancelled with the request")
	assert.True(t, g.Admit(store.JobKindFetch, 0, time.Now()).Open(), "Admit answers from the settings in effect")

	close(m.release)
	require.NoError(t, <-done)
	assert.Equal(t, ReasonPaused, g.Admit(store.JobKindFetch, 0, time.Now()).Closed)
}

// TestQueueGate_KeepAwake: keep-awake is stored and published like the
// other settings, written only when it changes, and never gates a claim.
func TestQueueGate_KeepAwake(t *testing.T) {
	ctx := context.Background()
	logs := &logRecorder{}
	m := newMemSettings(store.DefaultQueueSettings())
	g, err := NewQueueGate(ctx, m, daemonPools, slog.New(logs))
	require.NoError(t, err)
	st, changed := g.Watch(time.Now())
	assert.False(t, st.Settings.KeepAwake)

	got, err := g.Update(ctx, QueueUpdate{KeepAwake: new(true)})
	require.NoError(t, err)
	assert.True(t, got.KeepAwake)
	assert.True(t, isClosed(changed), "a watcher is woken")
	stored, _, puts := m.stored()
	assert.True(t, stored.KeepAwake)
	assert.Equal(t, 1, puts)
	assert.Equal(t, true, logs.messages(t, "queue settings changed")[0]["keep_awake"])

	_, err = g.Update(ctx, QueueUpdate{KeepAwake: new(true)})
	require.NoError(t, err)
	_, _, puts = m.stored()
	assert.Equal(t, 1, puts, "the same value again writes nothing")

	settings := []store.QueueSettings{
		{Throttle: store.ThrottleGentle, Schedule: window(t, "22:00-07:00")},
		{Paused: true, Throttle: store.ThrottleNormal},
		store.DefaultQueueSettings(),
	}
	for _, s := range settings {
		off, _ := newGate(t, s)
		s.KeepAwake = true
		on, _ := newGate(t, s)
		for _, now := range []time.Time{at(10, 12, 0), at(10, 23, 0)} {
			for _, kind := range []store.JobKind{store.JobKindFetch, store.JobKindIndex, store.JobKindCluster} {
				for active := range 5 {
					a, b := off.Admit(kind, active, now), on.Admit(kind, active, now)
					assert.Equal(t, a.Closed, b.Closed)
					assert.Equal(t, a.Until, b.Until)
				}
			}
		}
	}
}

// TestQueueGate_Watch: the state and channel Watch returns belong
// together: the channel closes at the first change after that state.
func TestQueueGate_Watch(t *testing.T) {
	g, _ := newGate(t, store.DefaultQueueSettings())
	st, changed := g.Watch(at(10, 12, 0))
	assert.Equal(t, g.State(at(10, 12, 0)), st)
	assert.False(t, isClosed(changed))

	_, err := g.Update(context.Background(), QueueUpdate{Paused: new(true)})
	require.NoError(t, err)
	assert.True(t, isClosed(changed))
	st, changed = g.Watch(at(10, 12, 0))
	assert.Equal(t, ReasonPaused, st.Closed)
	assert.False(t, isClosed(changed))
}
