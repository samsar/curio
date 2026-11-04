//go:build !e2e

package main

// summaryTiming is the summary pass's timing: the design's values. Only an
// e2e build takes another (summaries_e2e.go).
func summaryTiming() (passTiming, error) {
	return passTiming{}, nil
}
