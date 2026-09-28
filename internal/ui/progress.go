package ui

import "time"

// ProgressWindow is how far back the Overview's progress estimate looks
// for the pace the queue is working at.
const ProgressWindow = 10 * time.Minute

// KindWork is one job kind's work: how many jobs wait and run now, and how
// many finished, done or failed, in the estimate's window.
type KindWork struct {
	Kind             string
	Pending, Running int
	Finished         int
}

// ProgressState is what a progress estimate can say.
type ProgressState string

const (
	// ProgressIdle: no work is queued.
	ProgressIdle ProgressState = "idle"
	// ProgressClosed: work is queued, and the queue is closed (paused,
	// outside its schedule): it says why instead of when.
	ProgressClosed ProgressState = "closed"
	// ProgressStalled: work is queued and the queue is open, but none
	// finished in the window, so there is no pace to go by.
	ProgressStalled ProgressState = "stalled"
	// ProgressRunning: work is queued and finishing, with an ETA.
	ProgressRunning ProgressState = "running"
)

// Progress estimates when the queued work will be done, at the pace of
// the last Window.
type Progress struct {
	State       ProgressState
	Outstanding int // pending and running
	Finished    int // done and failed in the window
	Window      time.Duration
	PerMinute   float64       // jobs finished per minute over the window
	ETA         time.Duration // set in ProgressRunning
}

// EstimateProgress estimates when work will be done: its outstanding jobs
// divided by the rate its jobs finished at over window. The rate is the
// window's, not the moment's, so the estimate is steady, and it assumes
// the pace holds: jobs that fetches will enqueue (each fetched page's
// index job) aren't counted until they are. A closed queue, or one whose
// window saw nothing finish, has no ETA.
func EstimateProgress(work []KindWork, window time.Duration, open bool) Progress {
	p := Progress{Window: window}
	for _, k := range work {
		p.Outstanding += k.Pending + k.Running
		p.Finished += k.Finished
	}
	if window > 0 {
		p.PerMinute = float64(p.Finished) / window.Minutes()
	}
	switch {
	case p.Outstanding == 0:
		p.State = ProgressIdle
	case !open:
		p.State = ProgressClosed
	case p.Finished == 0 || window <= 0:
		p.State = ProgressStalled
	default:
		p.State = ProgressRunning
		p.ETA = time.Duration(float64(p.Outstanding) / float64(p.Finished) * float64(window))
	}
	return p
}
