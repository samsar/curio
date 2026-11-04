//go:build e2e

package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// e2eSummariesEnv is the summary pass's timing in an e2e build: a
// comma-separated list of key=duration, the keys quiet (the window after
// the person's last use of a model), check (how often the feed checks),
// brake_min and brake_max, a key left out keeping its value. With the
// design's, a search would hold the pass a minute and a brake a quarter of
// an hour; the end-to-end tests need seconds. Only a binary built with the
// e2e tag reads it: a release has no such knob.
const e2eSummariesEnv = "CURIO_E2E_SUMMARIES"

// summaryTiming reads the pass's timing from e2eSummariesEnv, warning that
// it is test timing; an unset one is the design's.
func summaryTiming() (passTiming, error) {
	raw := os.Getenv(e2eSummariesEnv)
	if raw == "" {
		return passTiming{}, nil
	}
	t, err := parseSummaryTiming(raw)
	if err != nil {
		return passTiming{}, fmt.Errorf("%s: %w", e2eSummariesEnv, err)
	}
	slog.Warn("summaries: the pass runs on test timing", "env", e2eSummariesEnv, "quiet", t.quiet.String(),
		"check", t.check.String(), "brake_min", t.brakeMin.String(), "brake_max", t.brakeMax.String())
	return t, nil
}

// parseSummaryTiming reads e2eSummariesEnv's value; an unknown key, a
// pair that isn't key=duration, or a duration that isn't positive is an
// error.
func parseSummaryTiming(raw string) (passTiming, error) {
	var t passTiming
	for pair := range strings.SplitSeq(raw, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			return passTiming{}, fmt.Errorf("%q is not key=duration", pair)
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			return passTiming{}, fmt.Errorf("%s: %w", key, err)
		}
		if d <= 0 {
			return passTiming{}, fmt.Errorf("%s: %s is not a positive duration", key, value)
		}
		switch key {
		case "quiet":
			t.quiet = d
		case "check":
			t.check = d
		case "brake_min":
			t.brakeMin = d
		case "brake_max":
			t.brakeMax = d
		default:
			return passTiming{}, fmt.Errorf("unknown key %q (quiet, check, brake_min, brake_max)", key)
		}
	}
	return t, nil
}
