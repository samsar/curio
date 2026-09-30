package ui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEstimateProgress(t *testing.T) {
	window := 10 * time.Minute
	soon := time.Date(2026, 9, 30, 9, 24, 0, 0, time.UTC)
	later := soon.Add(30 * time.Minute)
	cases := []struct {
		name string
		work []KindWork
		open bool
		want Progress
	}{
		{
			name: "idle",
			work: []KindWork{{Kind: "fetch", Finished: 30}, {Kind: "index"}},
			open: true,
			want: Progress{State: ProgressIdle, Finished: 30, Window: window, PerMinute: 3},
		},
		{
			name: "idle and closed",
			work: []KindWork{{Kind: "fetch"}},
			want: Progress{State: ProgressIdle, Window: window},
		},
		{
			name: "closed",
			work: []KindWork{{Kind: "fetch", Pending: 40, Finished: 20}},
			want: Progress{State: ProgressClosed, Outstanding: 40, Finished: 20, Window: window, PerMinute: 2},
		},
		{
			name: "stalled",
			work: []KindWork{{Kind: "fetch", Pending: 5, Running: 1}, {Kind: "index", Pending: 2}},
			open: true,
			want: Progress{State: ProgressStalled, Outstanding: 8, Window: window},
		},
		{
			name: "running",
			work: []KindWork{{Kind: "fetch", Pending: 100, Running: 16, Finished: 50}, {Kind: "index", Pending: 4, Finished: 10}},
			open: true,
			// 120 outstanding at 60 per 10 minutes.
			want: Progress{State: ProgressRunning, Outstanding: 120, Finished: 60, Window: window, PerMinute: 6,
				ETA: 20 * time.Minute},
		},
		{
			name: "closed, with jobs due later",
			work: []KindWork{{Kind: "fetch", Pending: 40, DueLater: 30, NextDue: soon}},
			want: Progress{State: ProgressClosed, Outstanding: 10, DueLater: 30, NextDue: soon, Window: window},
		},
		{
			name: "waiting",
			work: []KindWork{{Kind: "fetch", Pending: 171, DueLater: 171, NextDue: later, Finished: 20},
				{Kind: "index", Pending: 2, DueLater: 2, NextDue: soon}},
			open: true,
			want: Progress{State: ProgressWaiting, DueLater: 173, NextDue: soon, Finished: 20, Window: window,
				PerMinute: 2},
		},
		{
			name: "waiting while closed is closed",
			work: []KindWork{{Kind: "fetch", Pending: 171, DueLater: 171, NextDue: later}},
			want: Progress{State: ProgressClosed, DueLater: 171, NextDue: later, Window: window},
		},
		{
			name: "stalled, with jobs due later",
			work: []KindWork{{Kind: "fetch", Pending: 5, DueLater: 3, NextDue: later, Running: 1}},
			open: true,
			want: Progress{State: ProgressStalled, Outstanding: 3, DueLater: 3, NextDue: later, Window: window},
		},
		{
			name: "running, with jobs due later",
			work: []KindWork{{Kind: "fetch", Pending: 271, DueLater: 171, NextDue: later, Running: 16, Finished: 58}},
			open: true,
			// The 116 due now at 58 per 10 minutes; the 171 due later wait anyway.
			want: Progress{State: ProgressRunning, Outstanding: 116, DueLater: 171, NextDue: later, Finished: 58,
				Window: window, PerMinute: 5.8, ETA: 20 * time.Minute},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, EstimateProgress(tc.work, window, tc.open))
		})
	}
}
