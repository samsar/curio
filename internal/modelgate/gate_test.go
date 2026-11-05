package modelgate

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/samsar/curio/internal/generator"
)

// fakeModel is a Generator and Warmer: a model that is loaded or not,
// loads as told, and whose provider's listing says what pingErr says.
type fakeModel struct {
	mu        sync.Mutex
	loaded    bool
	loadedErr error
	asked     func(ctx context.Context) (bool, error) // when set, answers Loaded
	warmErr   error
	warmGate  chan struct{} // when set, a warm-up waits for it to close, or for its context
	pingErr   error
	unloadErr error

	warms, unloads, pings int
	keepAlives            []time.Duration
}

func (*fakeModel) Generate(context.Context, string, generator.Options) (string, error) {
	return "", nil
}
func (*fakeModel) Model() string { return "m:1" }

func (m *fakeModel) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pings++
	return m.pingErr
}

func (m *fakeModel) Loaded(ctx context.Context) (bool, error) {
	m.mu.Lock()
	asked, loaded, err := m.asked, m.loaded, m.loadedErr
	m.mu.Unlock()
	if asked != nil {
		return asked(ctx)
	}
	return loaded, err
}

func (m *fakeModel) Warm(ctx context.Context, keepAlive time.Duration) error {
	m.mu.Lock()
	m.warms++
	m.keepAlives = append(m.keepAlives, keepAlive)
	gate, err := m.warmGate, m.warmErr
	m.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	m.setLoaded(true)
	return nil
}

func (m *fakeModel) Unload(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unloads++
	if m.unloadErr != nil {
		return m.unloadErr
	}
	m.loaded = false
	return nil
}

func (m *fakeModel) setLoaded(loaded bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loaded = loaded
}

func (m *fakeModel) count(n *int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *n
}

// fakeClock is a clock tests move by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var testWords = Words{Feature: "search widgets", Role: "widgeter", Calls: "widgets", Resumes: "widgeting",
	Fallback: "searches go on without one", UnfitFix: "set the widgeter's model"}

// rig is a gate over a fake model, on a clock at 15:00 UTC, logging every
// record into logs, whose check answers as check says.
type rig struct {
	g      *Gate
	model  *fakeModel
	clock  *fakeClock
	logs   *bytes.Buffer
	cancel context.CancelFunc
	checks atomic.Int32
	check  atomic.Pointer[func(context.Context) Verdict]
}

func newRig(t *testing.T, m *fakeModel, opt func(*Options)) *rig {
	t.Helper()
	r := &rig{model: m, clock: &fakeClock{t: time.Date(2025, 4, 17, 15, 0, 0, 0, time.UTC)}, logs: &bytes.Buffer{}}
	pass := func(context.Context) Verdict { return Verdict{} }
	r.check.Store(&pass)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	var mu sync.Mutex
	opts := Options{Words: testWords, Model: "m:1", Now: r.clock.Now, Location: time.UTC,
		Log: slog.New(slog.NewJSONHandler(lockedWriter{&mu, r.logs}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Check: func(ctx context.Context) Verdict {
			r.checks.Add(1)
			return (*r.check.Load())(ctx)
		}}
	if opt != nil {
		opt(&opts)
	}
	r.g = New(ctx, m, opts)
	t.Cleanup(func() {
		cancel()
		r.g.Wait()
	})
	return r
}

// lockedWriter serializes the log's writes, which the background work
// makes too.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (r *rig) setCheck(f func(context.Context) Verdict) { r.check.Store(&f) }

// ready asks the gate, then waits for the background work it started.
func (r *rig) ready(t *testing.T) (Refusal, bool) {
	t.Helper()
	ref, ok := r.g.Ready(context.Background())
	r.settle(t)
	return ref, ok
}

// settle waits until no background work runs.
func (r *rig) settle(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		r.g.mu.Lock()
		defer r.g.mu.Unlock()
		return !r.g.running
	}, 5*time.Second, time.Millisecond)
}

// fit makes the model loaded and checked fit.
func (r *rig) fit(t *testing.T) {
	t.Helper()
	r.model.setLoaded(true)
	_, _ = r.ready(t)
	ref, ok := r.ready(t)
	require.True(t, ok, "fit: %+v", ref)
}

// records are the log's records, decoded.
func (r *rig) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(r.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		out = append(out, rec)
	}
	return out
}

// levels are the levels of the records of cause, in order.
func (r *rig) levels(t *testing.T, cause string) []string {
	t.Helper()
	var out []string
	for _, rec := range r.records(t) {
		if c, _ := rec["cause"].(string); c == cause {
			out = append(out, rec["level"].(string))
		}
	}
	return out
}

// TestGate_ColdCheckingFit: a model that isn't loaded is loaded in the
// background, the refusal saying so, then checked, then fit, each warm-up
// sending the keep-alive.
func TestGate_ColdCheckingFit(t *testing.T) {
	r := newRig(t, &fakeModel{}, func(o *Options) { o.KeepAlive = 7 * time.Minute })
	ref, ok := r.g.Ready(context.Background())
	assert.False(t, ok)
	assert.Equal(t, Refusal{Kind: Cold, Reason: "m:1 is loading"}, ref)
	r.settle(t)
	assert.Equal(t, 1, r.model.count(&r.model.warms))
	assert.Equal(t, []time.Duration{7 * time.Minute}, r.model.keepAlives)
	assert.Equal(t, int32(1), r.checks.Load(), "checked once loaded")
	ref, ok = r.ready(t)
	assert.True(t, ok, "%+v", ref)
	st := r.g.State()
	assert.Equal(t, CheckFit, st.Check)
	assert.Equal(t, r.clock.Now(), st.CheckedAt)

	loaded := newRig(t, &fakeModel{loaded: true}, nil)
	loaded.setCheck(func(context.Context) Verdict {
		time.Sleep(time.Millisecond)
		return Verdict{}
	})
	ref, ok = loaded.g.Ready(context.Background())
	assert.False(t, ok)
	assert.Equal(t, Refusal{Kind: Checking, Reason: "curio is checking m:1"}, ref)
	loaded.settle(t)
	_, ok = loaded.ready(t)
	assert.True(t, ok)
	assert.Zero(t, loaded.model.count(&loaded.model.warms), "a loaded model isn't warmed")
}

// TestGate_Unfit: a model that fails a probe is unfit for the life of the
// process, the reason naming the probe, its error and the fix; it is
// unloaded unless another role shares it, and never checked again.
func TestGate_Unfit(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared %v", shared), func(t *testing.T) {
			r := newRig(t, &fakeModel{loaded: true}, func(o *Options) { o.Shared = shared })
			r.setCheck(func(context.Context) Verdict {
				return Verdict{Probe: "a probe", Unfit: true, Err: errors.New("the answer isn't JSON")}
			})
			_, _ = r.ready(t)
			ref, ok := r.ready(t)
			assert.False(t, ok)
			want := `m:1 didn't pass the check "a probe": the answer isn't JSON; set the widgeter's model`
			assert.Equal(t, Refusal{Kind: Unfit, Reason: want}, ref)
			st := r.g.State()
			assert.Equal(t, CheckUnfit, st.Check)
			assert.Equal(t, want, st.Unfit)
			assert.Equal(t, "unfit", st.Failure.Cause)
			wantUnloads := 1
			if shared {
				wantUnloads = 0
			}
			assert.Equal(t, wantUnloads, r.model.count(&r.model.unloads), "unloaded only when no role shares it")
			_, _ = r.ready(t)
			assert.Equal(t, int32(1), r.checks.Load(), "never checked again")
		})
	}
	wrong := newRig(t, &fakeModel{loaded: true}, nil)
	wrong.setCheck(func(context.Context) Verdict { return Verdict{Probe: "p", Unfit: true} })
	_, _ = wrong.ready(t)
	ref, _ := wrong.ready(t)
	assert.Equal(t, `m:1 didn't pass the check "p"; set the widgeter's model`, ref.Reason, "a wrong answer, no error")

	provider := newRig(t, &fakeModel{loaded: true}, nil)
	provider.setCheck(func(context.Context) Verdict {
		return Verdict{Probe: "p", Unfit: true, Err: errors.New("no logprobs"), Fix: "upgrade the provider"}
	})
	_, _ = provider.ready(t)
	ref, _ = provider.ready(t)
	assert.Equal(t, `m:1 didn't pass the check "p": no logprobs; upgrade the provider`, ref.Reason,
		"the verdict's own fix in place of the model's")
}

// TestGate_CheckUnreached: a check whose probe couldn't reach the model
// reaches no verdict: the failure answers for a minute, warned of once,
// and the check runs again after it.
func TestGate_CheckUnreached(t *testing.T) {
	r := newRig(t, &fakeModel{loaded: true}, nil)
	r.setCheck(func(context.Context) Verdict { return Verdict{Probe: "p", Err: errors.New("HTTP 500")} })
	_, _ = r.ready(t)
	ref, ok := r.ready(t)
	assert.False(t, ok)
	assert.Equal(t, Refusal{Kind: Unavailable, Reason: "checking m:1 failed: HTTP 500"}, ref)
	assert.Equal(t, int32(1), r.checks.Load(), "not again within the minute")
	assert.Equal(t, CheckUnchecked, r.g.State().Check)
	r.clock.advance(RetryAfter)
	ref, _ = r.ready(t)
	assert.Equal(t, Checking, ref.Kind)
	assert.Equal(t, int32(2), r.checks.Load())
	assert.Equal(t, []string{"WARN", "DEBUG"}, r.levels(t, "unavailable:check"))

	missing := newRig(t, &fakeModel{loaded: true}, nil)
	missing.setCheck(func(context.Context) Verdict {
		return Verdict{Probe: "p", Err: fmt.Errorf("gone: %w", generator.ErrModelMissing)}
	})
	_, _ = missing.ready(t)
	ref, _ = missing.ready(t)
	assert.Equal(t, Refusal{Kind: Unavailable, Reason: "m:1 isn't pulled — run curio up"}, ref)

	failed, err := r.g.CheckNow(context.Background())
	assert.Empty(t, failed)
	require.ErrorContains(t, err, `probe "p": HTTP 500`)
}

// TestGate_SlowCheck: a check whose probe ran out of its time reaches no
// verdict and holds the feature at once, as calls that keep timing out
// do: 2 minutes, then 4 while checks keep running out, the check running
// again only when the hold ends, never a minute after; a check that
// passes ends the run, so calls start a run of their own from 0.
func TestGate_SlowCheck(t *testing.T) {
	r := newRig(t, &fakeModel{loaded: true}, nil)
	slow := func(context.Context) Verdict {
		return Verdict{Probe: "p", Slow: true, Err: errors.New("took longer than 14s")}
	}
	r.setCheck(slow)
	_, _ = r.ready(t)
	ref, ok := r.ready(t)
	assert.False(t, ok)
	assert.Equal(t, Refusal{Kind: TimeoutHeld, Reason: `checking m:1, the probe "p" took longer than 14s; ` +
		"widgeting resumes at 15:02"}, ref)
	st := r.g.State()
	assert.Equal(t, CheckUnchecked, st.Check)
	assert.Equal(t, r.clock.Now().Add(2*time.Minute), st.TimeoutHoldUntil)
	assert.Equal(t, "timeout:check", st.Failure.Cause)
	r.clock.advance(RetryAfter)
	_, _ = r.ready(t)
	assert.Equal(t, int32(1), r.checks.Load(), "not again a minute after")

	r.clock.advance(RetryAfter)
	ref, _ = r.ready(t)
	assert.Equal(t, Checking, ref.Kind, "the hold ended: checked again")
	assert.Equal(t, int32(2), r.checks.Load())
	assert.Equal(t, r.clock.Now().Add(4*time.Minute), r.g.State().TimeoutHoldUntil, "held again, longer")
	assert.Equal(t, []string{"WARN", "DEBUG"}, r.levels(t, "timeout:check"))

	r.setCheck(func(context.Context) Verdict { return Verdict{} })
	r.clock.advance(4 * time.Minute)
	_, _ = r.ready(t)
	ref, ok = r.ready(t)
	require.True(t, ok, "%+v", ref)
	assert.Contains(t, r.logs.String(), "widgets answer in time again")
	r.g.TimedOut(time.Second)
	r.g.TimedOut(time.Second)
	assert.Zero(t, r.g.State().TimeoutHoldUntil, "the check's run ended with it")
	r.g.TimedOut(time.Second)
	assert.Equal(t, r.clock.Now().Add(2*time.Minute), r.g.State().TimeoutHoldUntil, "the calls' own run")
	ref, _ = r.g.Ready(context.Background())
	assert.Equal(t, "3 widgets in a row took longer than 1s; widgeting resumes at 15:08", ref.Reason)
}

// TestGate_CheckNowCancelled: a check whose context ends reaches no
// verdict and records nothing.
func TestGate_CheckNowCancelled(t *testing.T) {
	r := newRig(t, &fakeModel{loaded: true}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	r.setCheck(func(context.Context) Verdict {
		cancel()
		return Verdict{Probe: "p", Unfit: true}
	})
	_, err := r.g.CheckNow(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, CheckUnchecked, r.g.State().Check)
	assert.Empty(t, r.g.State().Failure.Cause)
}

// TestGate_TimeoutHold: after 3 calls in a row time out, Ready refuses
// at once for 2 minutes, then 4, 8 and at most 15 while calls keep timing
// out; a call that answers in time ends the run.
func TestGate_TimeoutHold(t *testing.T) {
	r := newRig(t, &fakeModel{loaded: true}, nil)
	r.fit(t)
	for range 3 {
		r.g.TimedOut(20 * time.Millisecond)
	}
	ref, ok := r.g.Ready(context.Background())
	assert.False(t, ok)
	assert.Equal(t, Refusal{Kind: TimeoutHeld, Reason: "3 widgets in a row took longer than 20ms; widgeting resumes " +
		"at 15:02"}, ref)
	assert.Equal(t, r.clock.Now().Add(2*time.Minute), r.g.State().TimeoutHoldUntil)
	assert.Equal(t, "3 widgets in a row took longer than 20ms", r.g.State().Failure.Err)
	for _, hold := range []time.Duration{4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
		r.clock.advance(time.Hour)
		_, ok := r.g.Ready(context.Background())
		require.True(t, ok, "the hold ended")
		r.g.TimedOut(20 * time.Millisecond)
		assert.Equal(t, r.clock.Now().Add(hold), r.g.State().TimeoutHoldUntil, "held again, longer")
	}
	r.clock.advance(time.Hour)
	r.g.Answered()
	r.g.Succeeded()
	assert.Zero(t, r.g.State().TimeoutHoldUntil)
	for range 3 {
		r.g.TimedOut(20 * time.Millisecond)
	}
	assert.Equal(t, r.clock.Now().Add(2*time.Minute), r.g.State().TimeoutHoldUntil, "a new run starts at 2 minutes")
	assert.Equal(t, []string{"WARN", "DEBUG", "DEBUG", "DEBUG", "DEBUG", "WARN"}, r.levels(t, "timeout:held"),
		"once a run, and again after a success")

	r.clock.advance(time.Hour)
	r.g.Answered()
	r.g.TimedOut(time.Second)
	r.g.TimedOut(time.Second)
	r.g.Answered()
	r.g.TimedOut(time.Second)
	r.g.TimedOut(time.Second)
	assert.Zero(t, r.g.State().TimeoutHoldUntil, "an answer in time, usable or not, ends the run")
}

// TestGate_ConcurrentTimeouts: calls that time out together start one
// hold of 2 minutes, not one escalated per call.
func TestGate_ConcurrentTimeouts(t *testing.T) {
	r := newRig(t, &fakeModel{loaded: true}, nil)
	r.fit(t)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { r.g.TimedOut(time.Second) })
	}
	wg.Wait()
	assert.Equal(t, r.clock.Now().Add(2*time.Minute), r.g.State().TimeoutHoldUntil)
}

// TestGate_LoadedCheck: a provider that can't say whether the model is
// loaded is unavailable, warned of once; a caller whose context ends while
// it is asked spends no WARN and gets no refusal.
func TestGate_LoadedCheck(t *testing.T) {
	r := newRig(t, &fakeModel{loadedErr: errors.New("/api/ps: HTTP 500")}, nil)
	for range 2 {
		ref, ok := r.ready(t)
		assert.False(t, ok)
		assert.Equal(t, Refusal{Kind: Unavailable, Reason: "can't tell whether m:1 is loaded: /api/ps: HTTP 500"}, ref)
	}
	assert.Equal(t, []string{"WARN", "DEBUG"}, r.levels(t, "unavailable:loaded"))

	left := newRig(t, &fakeModel{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	left.model.asked = func(context.Context) (bool, error) {
		cancel()
		return false, context.Canceled
	}
	ref, ok := left.g.Ready(ctx)
	assert.False(t, ok)
	assert.Zero(t, ref, "no refusal: the caller left")
	assert.Empty(t, left.records(t), "nothing logged")
	assert.Empty(t, left.g.State().Failure.Cause)
}

// TestGate_NotPulled: a model the provider's listing lacks is unavailable
// with the fix, no warm-up started; one whose load says so answers so
// until a retry a minute later.
func TestGate_NotPulled(t *testing.T) {
	r := newRig(t, &fakeModel{pingErr: fmt.Errorf("m:1: %w", generator.ErrModelMissing)}, nil)
	ref, ok := r.ready(t)
	assert.False(t, ok)
	assert.Equal(t, Refusal{Kind: Unavailable, Reason: "m:1 isn't pulled — run curio up"}, ref)
	assert.Zero(t, r.model.count(&r.model.warms))
	assert.Equal(t, "unavailable:missing", r.g.State().Failure.Cause)

	load := newRig(t, &fakeModel{warmErr: fmt.Errorf("pull it: %w", generator.ErrModelMissing)}, nil)
	ref, _ = load.ready(t)
	assert.Equal(t, Cold, ref.Kind)
	ref, _ = load.ready(t)
	assert.Equal(t, Refusal{Kind: Unavailable, Reason: "m:1 isn't pulled — run curio up"}, ref)
	assert.Equal(t, 1, load.model.count(&load.model.warms), "not again within the minute")
	load.clock.advance(RetryAfter)
	ref, _ = load.ready(t)
	assert.Equal(t, Refusal{Kind: Unavailable, Reason: "m:1 isn't pulled — run curio up"}, ref)
	assert.Equal(t, 2, load.model.count(&load.model.warms), "tried again")
}

// TestGate_WarmFailure: a load that fails answers its failure for a
// minute, then is tried again.
func TestGate_WarmFailure(t *testing.T) {
	r := newRig(t, &fakeModel{warmErr: errors.New("HTTP 500: out of memory")}, nil)
	_, _ = r.ready(t)
	ref, _ := r.ready(t)
	assert.Equal(t, Refusal{Kind: Unavailable, Reason: "loading m:1 failed: HTTP 500: out of memory"}, ref)
	assert.Equal(t, 1, r.model.count(&r.model.warms))
	r.clock.advance(RetryAfter)
	ref, _ = r.ready(t)
	assert.Equal(t, Cold, ref.Kind)
	assert.Equal(t, 2, r.model.count(&r.model.warms))
}

// TestGate_Evictions: a model found unloaded within its keep-alive twice
// within an hour holds warming for 15 minutes; one found unloaded after
// its keep-alive ran out counts nothing.
func TestGate_Evictions(t *testing.T) {
	r := newRig(t, &fakeModel{loaded: true}, func(o *Options) { o.KeepAlive = 30 * time.Minute })
	r.fit(t)
	r.g.Answered()
	r.model.setLoaded(false)
	ref, _ := r.ready(t)
	assert.Equal(t, Cold, ref.Kind, "the first eviction loads it again")
	r.g.Answered()
	r.model.setLoaded(false)
	ref, ok := r.ready(t)
	assert.False(t, ok)
	assert.Equal(t, Refusal{Kind: Unavailable, Reason: "Ollama unloaded m:1 within its keep-alive 2 times, for " +
		"other models; curio loads it again from 15:15"}, ref)
	assert.Equal(t, r.clock.Now().Add(15*time.Minute), r.g.State().EvictionHoldUntil)
	assert.Equal(t, 1, r.model.count(&r.model.warms), "the first eviction's warm-up alone: none while held")
	r.clock.advance(16 * time.Minute)
	ref, _ = r.ready(t)
	assert.Equal(t, Cold, ref.Kind, "the hold expired")

	expired := newRig(t, &fakeModel{loaded: true}, nil)
	expired.fit(t)
	for range 3 {
		expired.g.Answered()
		expired.clock.advance(31 * time.Minute)
		expired.model.setLoaded(false)
		ref, _ := expired.ready(t)
		assert.Equal(t, Cold, ref.Kind, "unloaded on time: no eviction")
	}
	assert.Zero(t, expired.g.State().EvictionHoldUntil)

	shared := newRig(t, &fakeModel{loaded: true}, func(o *Options) { o.Shared, o.ShortSharer = true, true })
	shared.fit(t)
	shared.g.Answered()
	shared.clock.advance(6 * time.Minute)
	shared.model.setLoaded(false)
	_, _ = shared.ready(t)
	shared.g.Answered()
	shared.clock.advance(6 * time.Minute)
	shared.model.setLoaded(false)
	ref, _ = shared.ready(t)
	assert.Equal(t, Cold, ref.Kind, "a model shared with a role sending no keep-alive is sure to stay only "+
		"Ollama's default 5 minutes")

	// A sharer sending the same keep-alive (the planner beside the
	// reranker) leaves it whole: unloaded 6 minutes after, it was evicted.
	same := newRig(t, &fakeModel{loaded: true}, func(o *Options) { o.Shared = true })
	same.fit(t)
	for range 2 {
		same.g.Answered()
		same.clock.advance(6 * time.Minute)
		same.model.setLoaded(false)
		ref, _ = same.ready(t)
	}
	assert.Equal(t, Unavailable, ref.Kind, "held: evicted twice within the keep-alive")
	assert.False(t, same.g.State().EvictionHoldUntil.IsZero())
	assert.Zero(t, same.model.count(&same.model.unloads))
}

// TestGate_HoldsEndOnTheWallClock: the timeout hold's end and the
// eviction hold's, which healthz reports, are wall times, with no
// monotonic reading: judged on Go's monotonic clock, which stops while a
// Mac sleeps, a hold would outlast the time it says.
func TestGate_HoldsEndOnTheWallClock(t *testing.T) {
	r := newRig(t, &fakeModel{loaded: true}, func(o *Options) { o.Now = time.Now })
	r.g.mu.Lock()
	r.g.timeouts = timeoutsToHold
	r.g.holdTimeoutsLocked()
	now := r.g.now()
	r.g.keptUntil, r.g.evictions, r.g.firstEviction = now.Add(time.Minute), evictionsToHold-1, now
	_, held := r.g.unloadedLocked(now)
	r.g.mu.Unlock()
	require.True(t, held)
	st := r.g.State()
	for _, end := range []time.Time{st.TimeoutHoldUntil, st.EvictionHoldUntil} {
		require.False(t, end.IsZero())
		assert.NotContains(t, end.String(), "m=")
	}
}

// panicModel is a fakeModel whose loads and unloads panic.
type panicModel struct{ *fakeModel }

func (panicModel) Warm(context.Context, time.Duration) error { panic("warm") }
func (panicModel) Unload(context.Context) error              { panic("unload") }

// TestGate_Using: a warm-up, the check's probes (through Use), an unload
// and a call each open a use and end it, even when the call panics.
func TestGate_Using(t *testing.T) {
	var open, begun atomic.Int32
	using := func() func() {
		open.Add(1)
		begun.Add(1)
		var once sync.Once
		return func() { once.Do(func() { open.Add(-1) }) }
	}
	var inUse atomic.Bool
	r := newRig(t, &fakeModel{}, func(o *Options) { o.Using = using })
	r.setCheck(func(context.Context) Verdict {
		end := r.g.Use()
		defer end()
		inUse.Store(open.Load() > 0)
		return Verdict{Probe: "p", Unfit: true}
	})
	_, _ = r.ready(t)
	assert.True(t, inUse.Load(), "a probe inside a use")
	assert.Equal(t, 1, r.model.count(&r.model.unloads))
	assert.Equal(t, int32(3), begun.Load(), "the warm-up, the probe and the unload")
	assert.Zero(t, open.Load(), "every use ended")

	p := New(context.Background(), panicModel{&fakeModel{}}, Options{Words: testWords, Model: "m:1", Using: using,
		Log: slog.New(slog.DiscardHandler), Check: func(context.Context) Verdict { return Verdict{} }})
	for _, c := range []struct {
		name, value string
		call        func()
	}{
		{"a load", "warm", func() { p.warm() }},
		{"an unload", "unload", p.unload},
		{"a call", "call", func() {
			end := p.Use()
			defer end()
			panic("call")
		}},
	} {
		assert.PanicsWithValue(t, c.value, c.call, c.name)
		assert.Zero(t, open.Load(), "%s's use ended", c.name)
	}
}

// TestGate_Lifetime: once the lifetime ends no background work starts,
// and Wait returns, a load in flight abandoned.
func TestGate_Lifetime(t *testing.T) {
	m := &fakeModel{warmGate: make(chan struct{})}
	r := newRig(t, m, nil)
	ref, _ := r.g.Ready(context.Background())
	assert.Equal(t, Cold, ref.Kind)
	r.cancel()
	done := make(chan struct{})
	go func() {
		r.g.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait didn't return")
	}
	r.clock.advance(RetryAfter)
	_, _ = r.g.Ready(context.Background())
	r.g.Wait()
	assert.Equal(t, 1, m.count(&m.warms), "nothing started after the lifetime")
	assert.Zero(t, r.checks.Load())
}
