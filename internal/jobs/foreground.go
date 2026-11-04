package jobs

import (
	"sync"
	"time"

	"github.com/samsar/curio/internal/store"
)

// ReasonForeground is why a Foreground holds the summary pass back.
const ReasonForeground GateReason = "foreground"

// foregroundQuiet is how long after the person's last use of a model the
// summary pass waits to make a call: a search session's next question
// usually comes within a minute, and each summary call it would meet
// costs that plan up to a second to pre-empt (decisions.md "Summaries: one
// structured call per document, stored for the reranker").
const foregroundQuiet = time.Minute

// foregroundWaitReason is what a summarize job pre-empted, or held back by
// the person's use of a model, waits for, worded to follow "waiting for".
const foregroundWaitReason = "searches and plans to pause"

// Foreground tracks the person's use of the writing and embedding models:
// a search, from its plan to its last read, a plan or the planner's warm-up
// and check, and an interests rebuild's labels. The summary pass yields to
// it: as a Gate it admits a summarize job only while nothing is in use and
// Quiet has passed since the last use ended, and Begun's channel lets a
// summary call in flight be cancelled the moment a use begins, so a plan
// never waits behind a card. Every method of a nil Foreground does
// nothing, and admits.
type Foreground struct {
	quiet time.Duration
	now   func() time.Time

	mu      sync.Mutex
	active  int
	lastEnd time.Time
	begun   chan struct{} // closed when the next use begins
	ended   chan struct{} // closed when no use is in flight
}

// NewForeground returns a tracker whose quiet window is quiet, or
// foregroundQuiet when 0.
func NewForeground(quiet time.Duration) *Foreground {
	if quiet <= 0 {
		quiet = foregroundQuiet
	}
	ended := make(chan struct{})
	close(ended)
	return &Foreground{quiet: quiet, now: time.Now, begun: make(chan struct{}), ended: ended}
}

// Begin marks a use of a model beginning, and returns the func that marks
// it ended, which may be called more than once. Uses nest.
func (f *Foreground) Begin() (end func()) {
	if f == nil {
		return func() {}
	}
	f.mu.Lock()
	f.active++
	if f.active == 1 {
		f.ended = make(chan struct{})
	}
	close(f.begun)
	f.begun = make(chan struct{})
	f.mu.Unlock()
	var once sync.Once
	return func() { once.Do(f.end) }
}

// end marks one use ended.
func (f *Foreground) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active--
	f.lastEnd = f.now().Round(0) // wall time, as healthz reports the quiet window's end
	if f.active == 0 {
		close(f.ended)
	}
}

// Begun is a channel closed when the next use begins. Taken before a
// check that the models are quiet (Quiet), it closes for any use that
// begins after the check.
func (f *Foreground) Begun() <-chan struct{} {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.begun
}

// Quiet reports whether no use is in flight and the quiet window has passed
// since the last ended, at now; until is when it may be, zero while a use
// is in flight (its end decides).
func (f *Foreground) Quiet(now time.Time) (until time.Time, quiet bool) {
	if f == nil {
		return time.Time{}, true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.quietLocked(now)
}

func (f *Foreground) quietLocked(now time.Time) (time.Time, bool) {
	if f.active > 0 {
		return time.Time{}, false
	}
	until := f.lastEnd.Add(f.quiet)
	return until, !now.Before(until)
}

// Admit implements Gate: a summarize job only while the models are quiet,
// every other kind always. A closed verdict waits for the use in flight to
// end, or for the window to pass.
func (f *Foreground) Admit(kind store.JobKind, _ int, now time.Time) Verdict {
	if f == nil || kind != store.JobKindSummarize {
		return Verdict{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	until, quiet := f.quietLocked(now)
	switch {
	case quiet:
		return Verdict{}
	case f.active > 0:
		return Verdict{Closed: ReasonForeground, Changed: f.ended}
	}
	return Verdict{Closed: ReasonForeground, Until: until}
}

// YieldingUntil is when the summary pass may next make a call, while it
// yields: the zero time while the models are quiet, now while a use is in
// flight.
func (f *Foreground) YieldingUntil(now time.Time) time.Time {
	until, quiet := f.Quiet(now)
	switch {
	case quiet:
		return time.Time{}
	case until.IsZero():
		return now
	}
	return until
}
