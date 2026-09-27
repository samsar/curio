package fetcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// healthT0 is the time the health tests start from, on a minute boundary
// so that bucket expiry falls on whole minutes.
var healthT0 = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

// trackedCall is one request's outcome, at healthT0+at.
type trackedCall struct {
	at    time.Duration
	host  string
	class CallClass
}

// calls returns one call of class per host, a minute apart from start.
func calls(start time.Duration, class CallClass, hosts ...string) []trackedCall {
	out := make([]trackedCall, len(hosts))
	for i, host := range hosts {
		out[i] = trackedCall{at: start + time.Duration(i)*time.Minute, host: host, class: class}
	}
	return out
}

// replay records calls, in order, on a new tracker.
func replay(t *testing.T, log *slog.Logger, cs []trackedCall) *healthTracker {
	t.Helper()
	tr := newHealthTracker("jina", log)
	for _, c := range cs {
		var err error
		if c.class.Failed() {
			err = fmt.Errorf("jina: %s", c.class)
		}
		tr.record(healthT0.Add(c.at), c.host, c.class, err)
	}
	return tr
}

func TestCallClass_Failed(t *testing.T) {
	for _, c := range []CallClass{CallOK, CallJudged, CallRefused} {
		assert.False(t, c.Failed(), c)
	}
	for _, c := range []CallClass{CallChallenged, CallForbidden, CallRateLimited, CallAuth, CallServerError, CallNetwork} {
		assert.True(t, c.Failed(), c)
	}
}

// TestHealthTracker_State pins how a state is derived from the calls
// recorded and the cooldown, in its order of precedence.
func TestHealthTracker_State(t *testing.T) {
	failingCalls := calls(0, CallNetwork, "a", "b", "a", "b", "a") // 5 failures on 2 hosts, 0 to 4m
	cases := []struct {
		name     string
		calls    []trackedCall
		at       time.Duration // when the state is read; the last call's time when zero
		disabled bool
		cooldown time.Duration // ends at healthT0+cooldown; none when zero
		want     UpstreamState
	}{
		{name: "disabled", disabled: true, want: UpstreamDisabled},
		{name: "idle without calls", want: UpstreamIdle},
		{name: "counted to the window's last second", calls: calls(0, CallOK, "a"),
			at: healthWindow - time.Second, want: UpstreamOK},
		{name: "idle once the calls leave the window", calls: calls(0, CallOK, "a"),
			at: healthWindow, want: UpstreamIdle},
		{name: "ok", calls: calls(0, CallOK, "a", "b", "c"), want: UpstreamOK},
		{name: "ok with 1 failure in 5", calls: slices.Concat(
			calls(0, CallOK, "a", "b", "c", "d"), calls(4*time.Minute, CallNetwork, "e")), want: UpstreamOK},
		{name: "degraded at 1 failure in 4", calls: slices.Concat(
			calls(0, CallOK, "a", "b", "c"), calls(3*time.Minute, CallNetwork, "d")), want: UpstreamDegraded},
		{name: "paused while the cooldown lasts", calls: slices.Concat(
			calls(0, CallOK, "a"), calls(time.Minute, CallRateLimited, "b")),
			cooldown: 2 * time.Minute, want: UpstreamPaused},
		{name: "not paused once the cooldown has ended", calls: calls(0, CallOK, "a"),
			at: time.Minute, cooldown: time.Minute, want: UpstreamOK},
		{name: "failing at 5 failures on 2 hosts", calls: failingCalls, want: UpstreamFailing},
		{name: "not failing at 4 failures on 2 hosts", calls: calls(0, CallNetwork, "a", "b", "a", "b"),
			want: UpstreamDegraded},
		{name: "a single host is never failing", calls: calls(0, CallNetwork, "a", "a", "a", "a", "a", "a", "a", "a"),
			want: UpstreamDegraded},
		{name: "a single slow host is never failing, however long", calls: []trackedCall{
			{0, "a", CallNetwork}, {20 * time.Minute, "a", CallNetwork}, {40 * time.Minute, "a", CallNetwork},
		}, want: UpstreamDegraded},
		{name: "not failing before 30 minutes", calls: []trackedCall{
			{0, "a", CallChallenged}, {10 * time.Minute, "b", CallChallenged}, {20 * time.Minute, "c", CallChallenged},
			{30*time.Minute - time.Second, "d", CallChallenged},
		}, want: UpstreamDegraded},
		{name: "failing at 30 minutes", calls: []trackedCall{
			{0, "a", CallChallenged}, {10 * time.Minute, "b", CallChallenged}, {20 * time.Minute, "c", CallChallenged},
			{30 * time.Minute, "d", CallChallenged},
		}, want: UpstreamFailing},
		{name: "failing outranks paused", calls: failingCalls, cooldown: 10 * time.Minute, want: UpstreamFailing},
		{name: "failing lasts through an empty window", calls: failingCalls, at: 2 * time.Hour, want: UpstreamFailing},
		{name: "the first healthy answer ends failing", calls: slices.Concat(
			failingCalls, calls(20*time.Minute, CallOK, "c")), want: UpstreamOK},
		{name: "a refusal ends the streak", calls: slices.Concat(
			calls(0, CallNetwork, "a", "b", "a", "b"), calls(4*time.Minute, CallRefused, "c"),
			calls(5*time.Minute, CallNetwork, "a")), want: UpstreamDegraded},
		{name: "refusals and judged answers are no failures", calls: slices.Concat(
			calls(0, CallRefused, "a", "b", "c", "d", "e"),
			calls(5*time.Minute, CallJudged, "f", "g", "h", "i", "j")), want: UpstreamOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := replay(t, slog.New(slog.DiscardHandler), tc.calls)
			at := tc.at
			if at == 0 && len(tc.calls) > 0 {
				at = tc.calls[len(tc.calls)-1].at
			}
			var cooldownUntil time.Time
			if tc.cooldown > 0 {
				cooldownUntil = healthT0.Add(tc.cooldown)
			}
			h := tr.snapshot(healthT0.Add(at), !tc.disabled, cooldownUntil)
			assert.Equal(t, tc.want, h.State)
		})
	}
}

// TestHealthTracker_Snapshot: a snapshot reports the last healthy answer
// and failure, the window's calls by class and only a cooldown still to
// come.
func TestHealthTracker_Snapshot(t *testing.T) {
	tr := replay(t, slog.New(slog.DiscardHandler), slices.Concat(
		calls(0, CallOK, "a"), calls(time.Minute, CallJudged, "b"), calls(2*time.Minute, CallRateLimited, "c"),
		calls(3*time.Minute, CallRefused, "d"), calls(4*time.Minute, CallRateLimited, "e"),
	))
	now := healthT0.Add(5 * time.Minute)
	cooldown := now.Add(time.Minute)

	h := tr.snapshot(now, true, cooldown)
	assert.Equal(t, UpstreamHealth{
		Name:             "jina",
		Enabled:          true,
		State:            UpstreamPaused,
		LastSuccess:      healthT0.Add(3 * time.Minute),
		LastFailure:      healthT0.Add(4 * time.Minute),
		LastFailureClass: CallRateLimited,
		Window:           healthWindow,
		Recent:           map[CallClass]int{CallOK: 1, CallJudged: 1, CallRefused: 1, CallRateLimited: 2},
		CooldownUntil:    cooldown,
	}, h)

	h = tr.snapshot(cooldown, true, cooldown)
	assert.True(t, h.CooldownUntil.IsZero(), "a cooldown that has ended is not reported")

	h = replay(t, slog.New(slog.DiscardHandler), nil).snapshot(now, true, time.Time{})
	assert.Equal(t, UpstreamIdle, h.State)
	assert.Empty(t, h.Recent)
	assert.NotNil(t, h.Recent)
	assert.True(t, h.LastSuccess.IsZero())
	assert.True(t, h.LastFailure.IsZero())
	assert.Empty(t, h.LastFailureClass)
}

// TestHealthTracker_BoundedBuckets: the tracker counts in a fixed ring of
// one-minute buckets, and a bucket reused for a later minute forgets the
// one it counted before.
func TestHealthTracker_BoundedBuckets(t *testing.T) {
	tr := newHealthTracker("jina", slog.New(slog.DiscardHandler))
	for minute := range 3 * healthBuckets {
		for range 100 {
			tr.record(healthT0.Add(time.Duration(minute)*time.Minute), "a", CallOK, nil)
		}
	}
	h := tr.snapshot(healthT0.Add(time.Duration(3*healthBuckets-1)*time.Minute), true, time.Time{})
	assert.Equal(t, map[CallClass]int{CallOK: 100 * healthBuckets}, h.Recent)
}

// logRecorder is a slog handler that keeps every record; safe for
// concurrent use.
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

// messages lists the level and message of each record, in order.
func (r *logRecorder) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.records))
	for i, rec := range r.records {
		out[i] = rec.Level.String() + " " + rec.Message
	}
	return out
}

// count returns how many records have level and msg.
func (r *logRecorder) count(level slog.Level, msg string) int {
	return strings.Count(strings.Join(r.messages(), "\n")+"\n", level.String()+" "+msg+"\n")
}

// TestHealthTracker_Transitions: concurrent recorders log each transition
// into and out of failing exactly once and in order, and nothing else;
// the state is failing exactly while a warning is unanswered.
func TestHealthTracker_Transitions(t *testing.T) {
	const recorders = 64
	logs := &logRecorder{}
	tr := newHealthTracker("jina", slog.New(logs))
	now := healthT0
	errReset := errors.New("jina: connection reset by peer")
	warns := func() int { return logs.count(slog.LevelWarn, "upstream failing") }
	infos := func() int { return logs.count(slog.LevelInfo, "upstream recovered") }
	record := func(class CallClass, err error) {
		t.Helper()
		var wg sync.WaitGroup
		for i := range recorders {
			wg.Go(func() { tr.record(now, fmt.Sprintf("host%d.example", i), class, err) })
		}
		wg.Wait()
		failing := tr.snapshot(now, true, time.Time{}).State == UpstreamFailing
		assert.Equal(t, failing, warns()-infos() == 1, "failing=%v after %s: %v", failing, class, logs.messages())
		now = now.Add(time.Minute)
	}

	record(CallNetwork, errReset)
	assert.Equal(t, 1, warns())
	record(CallServerError, errors.New("jina: HTTP 502 Bad Gateway"))
	assert.Equal(t, 1, warns(), "no warning while failing")
	record(CallOK, nil)
	assert.Equal(t, 1, infos())
	record(CallJudged, nil)
	record(CallNetwork, errReset)
	assert.Equal(t, 2, warns())
	assert.Equal(t, 1, infos())
	assert.Equal(t, []string{"WARN upstream failing", "INFO upstream recovered", "WARN upstream failing"},
		logs.messages(), "nothing else is logged")
}

// TestHealthTracker_LogLines: the warning names the upstream, the class,
// the streak and its start, with the error capped; the recovery says how
// long it failed.
func TestHealthTracker_LogLines(t *testing.T) {
	var logs bytes.Buffer
	tr := newHealthTracker("jina", slog.New(slog.NewTextHandler(&logs, nil)))
	long := errors.New("jina: HTTP 401 Unauthorized: " + strings.Repeat("x", 2*maxErrorBody))
	for i := range failingStreak {
		tr.record(healthT0.Add(time.Duration(i)*time.Minute), fmt.Sprintf("host%d.example", i), CallAuth, long)
	}
	tr.record(healthT0.Add(10*time.Minute), "host9.example", CallOK, nil)

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	require.Len(t, lines, 2, logs.String())
	for _, want := range []string{"level=WARN", `msg="upstream failing"`, "upstream=jina", "class=auth",
		"failures=5", "since=" + healthT0.Format("2006-01-02T15:04:05"), `err="jina: HTTP 401 Unauthorized: xxx`} {
		assert.Contains(t, lines[0], want)
	}
	assert.Contains(t, lines[0], "x…", "the error is capped")
	assert.Less(t, len(lines[0]), 2*maxErrorBody)
	for _, want := range []string{"level=INFO", `msg="upstream recovered"`, "upstream=jina",
		"failed_for=10m0s", "failures=5"} {
		assert.Contains(t, lines[1], want)
	}
}
