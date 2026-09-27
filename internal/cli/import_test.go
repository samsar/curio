package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/importer"
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

func TestPickChromeProfile(t *testing.T) {
	profiles := []importer.ChromeProfile{
		{Dir: "Default", Name: "Person 1"},
		{Dir: "Profile 1", Name: "Work"},
	}
	cases := map[string]string{
		"Default":   "Default",
		"Profile 1": "Profile 1",
		"Work":      "Profile 1",
		"work":      "Profile 1", // display names ignore case
		"person 1":  "Default",
	}
	for want, dir := range cases {
		got := pickChromeProfile(profiles, want)
		require.NotNil(t, got, want)
		assert.Equal(t, dir, got.Dir, want)
	}
	assert.Nil(t, pickChromeProfile(profiles, "default"), "directories match exactly")
	assert.Nil(t, pickChromeProfile(profiles, "Personal"))
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
			assert.Equal(t, tc.want, progressLine(stats, tc.queue, 0.5, 14*time.Second))
		})
	}
}
