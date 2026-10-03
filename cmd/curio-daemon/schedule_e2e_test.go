//go:build e2e

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
)

// TestParseSchedulerTiming: the e2e build's knob sets the durations it
// names, keeps the others' defaults, and refuses what it can't read.
func TestParseSchedulerTiming(t *testing.T) {
	got, err := parseSchedulerTiming("interval=200ms, settle=1s,max_wait_first=3s")
	require.NoError(t, err)
	assert.Equal(t, insight.SchedulerConfig{FirstRebuildAt: insight.FirstRebuildAt, Interval: 200 * time.Millisecond,
		Settle: time.Second, MaxWait: insight.MaxWait, MaxWaitFirst: 3 * time.Second}, got)

	for raw, want := range map[string]string{
		"interval=200ms,backoff=1s": `unknown key "backoff"`,
		"settle":                    `"settle" is not key=duration`,
		"settle=soon":               "settle: time: invalid duration",
		"max_wait=0s":               "max_wait: 0s is not a positive duration",
	} {
		_, err := parseSchedulerTiming(raw)
		assert.ErrorContains(t, err, want, raw)
	}
}

// TestSchedulerConfig_E2E: an e2e build without the knob runs on the
// design's timing, and one with a knob it can't read refuses to start.
func TestSchedulerConfig_E2E(t *testing.T) {
	t.Setenv(e2eTimingEnv, "")
	cfg, err := schedulerConfig()
	require.NoError(t, err)
	assert.Equal(t, insight.SchedulerConfig{}, cfg)

	t.Setenv(e2eTimingEnv, "settle=fast")
	_, err = schedulerConfig()
	require.ErrorContains(t, err, e2eTimingEnv)
}
