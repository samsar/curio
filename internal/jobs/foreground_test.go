package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/summarize"
)

// TestForeground: uses nest, an end is idempotent, Begun closes at each
// use's beginning, and the gate holds the summary pass alone, while a use
// is in flight (until it ends) and for the quiet window after.
func TestForeground(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	f := NewForeground(time.Minute)
	f.now = func() time.Time { return now }
	assert.True(t, f.Admit(store.JobKindSummarize, 0, now).Open(), "quiet from the start")

	begun := f.Begun()
	end := f.Begin()
	select {
	case <-begun:
	default:
		t.Fatal("Begun didn't close at the use's beginning")
	}
	v := f.Admit(store.JobKindSummarize, 0, now)
	assert.Equal(t, ReasonForeground, v.Closed)
	require.NotNil(t, v.Changed)
	assert.True(t, v.Until.IsZero(), "a use in flight: its end decides")
	assert.True(t, f.Admit(store.JobKindIndex, 0, now).Open(), "only the summary pass yields")
	assert.True(t, f.Admit(store.JobKindCards, 0, now).Open())

	inner := f.Begin()
	inner()
	inner()
	select {
	case <-v.Changed:
		t.Fatal("the outer use is still in flight")
	default:
	}
	end()
	<-v.Changed
	v = f.Admit(store.JobKindSummarize, 0, now.Add(30*time.Second))
	assert.Equal(t, ReasonForeground, v.Closed)
	assert.Equal(t, now.Add(time.Minute), v.Until, "the quiet window after the last end")
	assert.Equal(t, now.Add(time.Minute), f.YieldingUntil(now.Add(30*time.Second)))
	assert.True(t, f.Admit(store.JobKindSummarize, 0, now.Add(time.Minute)).Open())
	assert.True(t, f.YieldingUntil(now.Add(time.Minute)).IsZero())

	var none *Foreground
	none.Begin()()
	assert.Nil(t, none.Begun())
	_, quiet := none.Quiet(now)
	assert.True(t, quiet)
	assert.True(t, none.Admit(store.JobKindSummarize, 0, now).Open())
}

// TestEnds_OnTheWallClock: the brake's end and the quiet window's, which
// healthz reports, are wall times, with no monotonic reading: judged on
// Go's monotonic clock, which stops while a Mac sleeps, either would hold
// the pass past the time it says.
func TestEnds_OnTheWallClock(t *testing.T) {
	b := newBrake(1, time.Minute, time.Hour)
	on, _ := b.fail("a", assert.AnError, time.Now())
	require.True(t, on)
	assert.NotContains(t, b.state().Until.String(), "m=")

	f := NewForeground(time.Minute)
	f.Begin()()
	until := f.YieldingUntil(time.Now())
	assert.NotContains(t, until.String(), "m=")
	assert.InDelta(t, time.Minute.Seconds(), time.Until(until).Seconds(), 5)
}

// feedWriter is a writer the feed asks: whether it writes, how the pass
// may be fed, and when its check runs again.
type feedWriter struct {
	refusal string
	feed    summarize.Feed
	retryAt time.Time
}

func (*feedWriter) Identity() store.SummaryWriter {
	return store.SummaryWriter{Model: "m", PromptVersion: "p"}
}
func (w *feedWriter) Refusal() string         { return w.refusal }
func (w *feedWriter) Feed() summarize.Feed    { return w.feed }
func (w *feedWriter) CheckRetryAt() time.Time { return w.retryAt }
func (*feedWriter) Probe(context.Context)     {}

// feedStore records the limits each top-up asked for, and counts its
// counts; during, when set, runs inside each count.
type feedStore struct {
	limits  []store.SummarizeLimits
	counts  store.SummaryCounts
	counted int
	during  func()
}

func (s *feedStore) CountSummaries(context.Context, string, store.SummaryWriter) (store.SummaryCounts, error) {
	s.counted++
	if s.during != nil {
		s.during()
	}
	return s.counts, nil
}

func (s *feedStore) EnqueueSummarize(_ context.Context, _ string, _ store.SummaryWriter,
	limits store.SummarizeLimits) (int, error) {
	s.limits = append(s.limits, limits)
	return 1, nil
}

// TestSummaries_Feed: a writer that writes nothing starts no pass; an
// outage keeps one job queued, its probe, and no more; the brake stops the
// feed; the rate is the summaries written over the window.
func TestSummaries_Feed(t *testing.T) {
	ctx := context.Background()
	st := &feedStore{}
	off := NewSummaries(SummariesOptions{Store: st, Writer: &feedWriter{refusal: "off", feed: summarize.FeedNone},
		TenantID: "local"})
	require.NoError(t, off.Start(ctx))
	off.Run(ctx) // returns at once
	assert.Empty(t, st.limits, "nothing queued for a writer that writes nothing")

	w := &feedWriter{feed: summarize.FeedAll}
	s := NewSummaries(SummariesOptions{Store: st, Writer: w, TenantID: "local", BrakeAfter: 2})
	require.NoError(t, s.Start(ctx))
	assert.Equal(t, store.SummarizeLimits{InFlight: summariesInFlight, Waiting: summariesWaiting,
		FailedWait: SummariesFailedWait}, st.limits[0])
	w.feed = summarize.FeedOne
	_, err := s.topUp(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.limits[1].InFlight)
	assert.Zero(t, st.limits[1].Waiting, "an outage: one job, the probe")
	assert.True(t, st.limits[1].NotBefore.IsZero(), "due now")
	w.retryAt = time.Now().Add(time.Minute)
	_, err = s.topUp(ctx)
	require.NoError(t, err)
	assert.Equal(t, store.SummarizeLimits{InFlight: 1, FailedWait: SummariesFailedWait, NotBefore: w.retryAt},
		st.limits[2], "the check waits: the one job, for the wait's end")
	w.retryAt = time.Time{}
	w.feed = summarize.FeedNone
	_, err = s.topUp(ctx)
	require.NoError(t, err)
	assert.Len(t, st.limits, 3, "none: nothing queued")
	w.feed = summarize.FeedOne

	s.Failed("a", assert.AnError)
	s.Failed("a", assert.AnError)
	assert.False(t, s.Progress().Brake.On(), "a document failing again counts once")
	s.Failed("b", assert.AnError)
	b := s.Progress().Brake
	assert.True(t, b.On())
	assert.Equal(t, []string{"a", "b"}, b.Documents)
	queued := len(st.limits)
	_, err = s.topUp(ctx)
	require.NoError(t, err)
	assert.Len(t, st.limits, queued, "the brake stops the feed")
	s.Written()
	assert.False(t, s.Progress().Brake.On(), "a summary written ends it")

	assert.Zero(t, s.Progress().PerMinute, "one write: no rate yet")
	s.mu.Lock()
	s.written = []time.Time{time.Now().Add(-2 * time.Minute), time.Now().Add(-time.Minute), time.Now()}
	s.mu.Unlock()
	p := s.Progress()
	assert.InDelta(t, 1.5, p.PerMinute, 0.05)
	assert.Equal(t, 2*time.Minute, p.ETA(3).Round(time.Minute))
	assert.Zero(t, SummaryProgress{}.ETA(10))

	var none *Summaries
	none.Kick()
	none.Written()
	none.Failed("x", assert.AnError)
	none.Settled()
	assert.Equal(t, SummaryProgress{}, none.Progress())
}

// TestSummaries_Counts: the counts are read once for their freshness,
// and again at once after any change the feed hears of: a kick, a summary
// written, a failure; a read a change overtook is served once, not kept.
func TestSummaries_Counts(t *testing.T) {
	ctx := context.Background()
	st := &feedStore{counts: store.SummaryCounts{Written: 3, ToWrite: 2}}
	s := NewSummaries(SummariesOptions{Store: st, Writer: &feedWriter{feed: summarize.FeedAll}, TenantID: "local",
		CountsFresh: time.Hour})
	n, at, err := s.Counts(ctx)
	require.NoError(t, err)
	assert.Equal(t, st.counts, n)
	assert.WithinDuration(t, time.Now(), at, time.Minute)
	_, again, err := s.Counts(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.counted, "fresh: served again")
	assert.Equal(t, at, again)

	for name, change := range map[string]func(){"a kick": s.Kick, "a summary written": s.Written,
		"a failure": func() { s.Failed("d", assert.AnError) }} {
		before := st.counted
		change()
		_, _, err := s.Counts(ctx)
		require.NoError(t, err)
		assert.Equal(t, before+1, st.counted, name)
	}

	s.Kick()
	st.during = s.Kick
	before := st.counted
	_, _, err = s.Counts(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, st.counted, "read, a change landing during it")
	st.during = nil
	before = st.counted
	_, _, err = s.Counts(ctx)
	require.NoError(t, err)
	assert.Equal(t, before+1, st.counted, "a read a change overtook isn't kept")

	stale := NewSummaries(SummariesOptions{Store: st, Writer: &feedWriter{feed: summarize.FeedAll},
		TenantID: "local", CountsFresh: time.Nanosecond})
	before = st.counted
	for range 2 {
		_, _, err = stale.Counts(ctx)
		require.NoError(t, err)
	}
	assert.Equal(t, before+2, st.counted, "past their freshness: read again")
}
