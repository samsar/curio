package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
)

// TestFollowProgress_Interrupted: ctrl-c during --follow is a quiet stop;
// the import itself has finished.
func TestFollowProgress_Interrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	// Nothing listens there: an interrupted follow never asks.
	require.NoError(t, followProgress(ctx, &out, client.New("http://127.0.0.1:1")))
	assert.Contains(t, out.String(), "interrupted")
	assert.NotContains(t, out.String(), "stats unavailable")
}

// TestProgressLine: --follow's line says why a closed queue starts nothing,
// and is as before when the queue is open or couldn't be read.
func TestProgressLine(t *testing.T) {
	stats := &client.Stats{
		JobsByStatus:     map[string]int{"done": 7, "pending": 5, "running": 2, "failed": 1},
		DocumentsByState: map[string]int{"fetched": 6},
	}
	const counts = "  done=7  pending=5  running=2  failed=1  fetched=6   rate≈0.5/s   eta≈14s"
	opensAt := time.Date(2026, 9, 27, 22, 0, 0, 0, time.Local)
	cases := []struct {
		name  string
		queue *client.Queue
		want  string
	}{
		{"unknown", nil, counts},
		{"open", &client.Queue{State: client.QueueOpen, Throttle: client.ThrottleGentle}, counts},
		{"paused", &client.Queue{State: client.QueueClosed, Reason: client.ReasonPaused, Paused: true},
			counts + "   queue paused (curio resume)"},
		{"outside the schedule", &client.Queue{State: client.QueueClosed, Reason: client.ReasonOutsideSchedule,
			Schedule: "22:00-07:00", OpensAt: opensAt.UTC()},
			counts + "   queue closed outside schedule 22:00-07:00 until 22:00 (curio schedule off)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, progressLine(stats, tc.queue, 0.5, time.Now()))
		})
	}
}

// TestProgressLine_DueLater: jobs due later are said beside the ETA, which
// leaves them out (102 jobs due now at 0.5 a second), or, when they are all
// that is queued, instead of a rate and an ETA that have nothing to measure.
func TestProgressLine_DueLater(t *testing.T) {
	next := time.Date(2026, 9, 30, 14, 32, 0, 0, time.Local)
	queue := func(pending, later int) *client.Queue {
		return &client.Queue{State: client.QueueOpen, Kinds: []client.QueueKind{
			{Kind: "fetch", Pending: pending, DueLater: later, NextDue: next.Add(time.Hour).UTC()},
			{Kind: "index", Pending: 1, DueLater: 1, NextDue: next.UTC()}}}
	}
	waiting := &client.Stats{JobsByStatus: map[string]int{"done": 7, "pending": 172}}
	assert.Equal(t, "  done=7  pending=172  running=0  failed=0  fetched=0   172 due later, the first at 14:32",
		progressLine(waiting, queue(171, 171), 0, next.Add(-time.Hour)))

	working := &client.Stats{JobsByStatus: map[string]int{"done": 7, "pending": 172, "running": 2}}
	assert.Equal(t, "  done=7  pending=172  running=2  failed=0  fetched=0   rate≈0.5/s   eta≈3m24s (72 more due later)",
		progressLine(working, queue(171, 71), 0.5, next.Add(-time.Hour)))
}

// TestDueAt: when the first job due later is due, on the local clock: a
// time today, tomorrow's, or a date after that; a deferral can be up to a
// day ahead.
func TestDueAt(t *testing.T) {
	now := time.Date(2026, 9, 30, 22, 0, 0, 0, time.Local)
	for next, want := range map[time.Time]string{
		time.Date(2026, 9, 30, 23, 15, 0, 0, time.Local): "at 23:15",
		time.Date(2026, 9, 30, 21, 59, 0, 0, time.Local): "at 21:59",
		time.Date(2026, 10, 1, 9, 5, 0, 0, time.Local):   "tomorrow at 09:05",
		time.Date(2026, 10, 2, 0, 0, 0, 0, time.Local):   "on Oct 2 at 00:00",
	} {
		assert.Equal(t, want, dueAt(next, now), next)
	}
}

// TestFollowETA: --follow's ETA counts the jobs due now, running or
// pending, and leaves out the ones due later, which no rate brings closer.
func TestFollowETA(t *testing.T) {
	cases := []struct {
		name                    string
		pending, running, later int
		rate                    float64
		want                    time.Duration
	}{
		{"nothing due later", 5, 2, 0, 0.5, 14 * time.Second},
		{"some due later", 172, 2, 71, 0.5, 206 * time.Second},
		{"only jobs due later", 171, 0, 171, 0.5, 0},
		{"running while the rest wait", 171, 2, 171, 0.5, 4 * time.Second},
		{"a count read between polls", 3, 1, 5, 0.5, 2 * time.Second},
		{"nothing finished", 5, 2, 0, 0, 0},
		{"drained", 0, 0, 0, 0.5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, followETA(tc.pending, tc.running, tc.later, tc.rate))
		})
	}
}
