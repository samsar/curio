//go:build e2e

package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/samsar/curio/internal/insight"
)

// e2eTimingEnv is the interest scheduler's timing in an e2e build: a
// comma-separated list of key=duration, the keys interval, settle,
// max_wait and max_wait_first, a key left out keeping its value. With the
// design's, an import would be grouped ten minutes after it ends; the
// end-to-end tests need seconds. Only a binary built with the e2e tag reads
// it: a release has no such knob.
const e2eTimingEnv = "CURIO_E2E_INTERESTS"

// schedulerConfig reads the scheduler's timing from e2eTimingEnv, warning
// that it is test timing; an unset one is the design's.
func schedulerConfig() (insight.SchedulerConfig, error) {
	raw := os.Getenv(e2eTimingEnv)
	if raw == "" {
		return insight.SchedulerConfig{}, nil
	}
	cfg, err := parseSchedulerTiming(raw)
	if err != nil {
		return insight.SchedulerConfig{}, fmt.Errorf("%s: %w", e2eTimingEnv, err)
	}
	slog.Warn("interests: the scheduler runs on test timing", "env", e2eTimingEnv, "interval", cfg.Interval.String(),
		"settle", cfg.Settle.String(), "max_wait", cfg.MaxWait.String(), "max_wait_first", cfg.MaxWaitFirst.String())
	return cfg, nil
}

// parseSchedulerTiming reads e2eTimingEnv's value into the timing it
// gives, every key it leaves out at its default. An unknown key, a pair
// that isn't key=duration, or a duration that isn't positive is an error.
func parseSchedulerTiming(raw string) (insight.SchedulerConfig, error) {
	var cfg insight.SchedulerConfig
	for pair := range strings.SplitSeq(raw, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			return insight.SchedulerConfig{}, fmt.Errorf("%q is not key=duration", pair)
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			return insight.SchedulerConfig{}, fmt.Errorf("%s: %w", key, err)
		}
		if d <= 0 {
			return insight.SchedulerConfig{}, fmt.Errorf("%s: %s is not a positive duration", key, value)
		}
		switch key {
		case "interval":
			cfg.Interval = d
		case "settle":
			cfg.Settle = d
		case "max_wait":
			cfg.MaxWait = d
		case "max_wait_first":
			cfg.MaxWaitFirst = d
		default:
			return insight.SchedulerConfig{}, fmt.Errorf("unknown key %q (interval, settle, max_wait, max_wait_first)", key)
		}
	}
	return cfg.WithDefaults(), nil
}
