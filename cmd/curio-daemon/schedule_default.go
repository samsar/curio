//go:build !e2e

package main

import "github.com/samsar/curio/internal/insight"

// schedulerConfig is the interest scheduler's timing: the design's values.
// Only an e2e build takes another (schedule_e2e.go).
func schedulerConfig() (insight.SchedulerConfig, error) {
	return insight.SchedulerConfig{}, nil
}
