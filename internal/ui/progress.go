package ui

import "time"

// ProgressWindow is how far back Status's progress estimate looks
// for the pace the queue is working at.
const ProgressWindow = 10 * time.Minute

// KindWork is one job kind's work: how many jobs wait and run now, how
// many of the waiting ones can't run yet (DueLater) and when the first of
// them can (NextDue, zero when none), and how many finished, done or
// failed, in the estimate's window.
type KindWork struct {
	Kind             string
	Pending, Running int
	DueLater         int
	NextDue          time.Time
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
	// ProgressWaiting: the queue is open and nothing is due or running,
	// but jobs wait for a time still ahead (a retry's backoff, a hold on
	// an upstream): it says how many, and when the first is due.
	ProgressWaiting ProgressState = "waiting"
	// ProgressStalled: work is due and the queue is open, but none
	// finished in the window, so there is no pace to go by.
	ProgressStalled ProgressState = "stalled"
	// ProgressRunning: work is due and finishing, with an ETA.
	ProgressRunning ProgressState = "running"
)

// Progress estimates when the queued work will be done, at the pace of
// the last Window.
type Progress struct {
	State ProgressState
	// Outstanding is the work due now: pending jobs that can run, and
	// running ones. DueLater is the pending jobs that can't run yet, and
	// NextDue when the first of them can (zero when none).
	Outstanding int
	DueLater    int
	NextDue     time.Time
	Finished    int // done and failed in the window
	Window      time.Duration
	PerMinute   float64       // jobs finished per minute over the window
	ETA         time.Duration // set in ProgressRunning
}

// Queued is every job queued: the ones due now and the ones due later.
func (p Progress) Queued() int { return p.Outstanding + p.DueLater }

// EstimateProgress estimates when work will be done: its outstanding jobs
// divided by the rate its jobs finished at over window. The rate is the
// window's, not the moment's, so the estimate is steady, and it assumes
// the pace holds: jobs that fetches will enqueue (each fetched page's
// index job) aren't counted until they are. Jobs due later are left out of
// it: they can't run before their time however fast the queue works, and
// counting them would promise an hour's wait for GitHub in minutes. A
// closed queue, or one whose window saw nothing finish, has no ETA.
func EstimateProgress(work []KindWork, window time.Duration, open bool) Progress {
	p := Progress{Window: window}
	for _, k := range work {
		p.Outstanding += k.Pending - k.DueLater + k.Running
		p.DueLater += k.DueLater
		p.Finished += k.Finished
		if !k.NextDue.IsZero() && (p.NextDue.IsZero() || k.NextDue.Before(p.NextDue)) {
			p.NextDue = k.NextDue
		}
	}
	if window > 0 {
		p.PerMinute = float64(p.Finished) / window.Minutes()
	}
	switch {
	case p.Queued() == 0:
		p.State = ProgressIdle
	case !open:
		p.State = ProgressClosed
	case p.Outstanding == 0:
		p.State = ProgressWaiting
	case p.Finished == 0 || window <= 0:
		p.State = ProgressStalled
	default:
		p.State = ProgressRunning
		p.ETA = time.Duration(float64(p.Outstanding) / float64(p.Finished) * float64(window))
	}
	return p
}
