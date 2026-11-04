package jobs

import (
	"slices"
	"time"
)

// brake is a feed's brake, the re-embed's and the summary pass's: after
// `after` documents failed for the first time in a row, the feed stops for
// `min`, then twice as long each time it stops again without a document
// done in between, up to `max`. Only a document's first failure since the
// daemon started counts: one that failed before may be failing for its own
// reasons, and its failing again says nothing of the model. It holds no
// lock: its feed serializes its calls.
type brake struct {
	after    int
	min, max time.Duration

	failed  map[string]struct{} // the documents that failed since the daemon started
	streak  []string            // those that failed for the first time since the last one done, in turn
	lastErr string              // the last of the streak's errors
	until   time.Time           // zero while it is off; wall time (fail)
	next    time.Duration       // how long it stops the feed the next time it goes on
}

// brakeNamed is how many of the documents whose failures put a brake on it
// names.
const brakeNamed = 5

// Brake is a feed's brake while it is on: until when it stops the feed,
// how many documents failed in a row, the first brakeNamed of them, and
// the last one's error. The zero value is a brake that is off.
type Brake struct {
	Until     time.Time
	Failures  int
	Documents []string
	Err       string
}

// On reports whether the brake is on.
func (b Brake) On() bool { return !b.Until.IsZero() }

func newBrake(after int, minPause, maxPause time.Duration) brake {
	return brake{after: after, min: minPause, max: maxPause, failed: map[string]struct{}{}, next: minPause}
}

// fail records documentID's failure, with err, at now. It reports whether
// the failure put the brake on, and then for how long: a document's
// failure after its first counts for nothing, and neither does one while
// the brake is on. The brake's end is wall time, its monotonic reading
// dropped, so it is judged on the clock healthz reports it on: Go's
// monotonic clock stops while a Mac sleeps, and an end judged on it would
// hold the feed past the time healthz says.
func (b *brake) fail(documentID string, err error, now time.Time) (on bool, pause time.Duration) {
	if _, before := b.failed[documentID]; before {
		return false, 0
	}
	b.failed[documentID] = struct{}{}
	b.streak = append(b.streak, documentID)
	b.lastErr = err.Error()
	if len(b.streak) < b.after || !b.until.IsZero() {
		return false, 0
	}
	pause = b.next
	b.until, b.next = now.Add(pause).Round(0), min(2*pause, b.max)
	return true, pause
}

// succeed records a document done: the streak is over, and the brake is
// off, its next stop back to min. It reports whether the brake was on.
func (b *brake) succeed() (wasOn bool) {
	wasOn = !b.until.IsZero()
	b.streak, b.lastErr, b.until, b.next = nil, "", time.Time{}, b.min
	return wasOn
}

// lift turns the brake off when its time has passed at now, and reports
// whether it did.
func (b *brake) lift(now time.Time) bool {
	if b.until.IsZero() || now.Before(b.until) {
		return false
	}
	b.until = time.Time{}
	return true
}

// holds reports whether the brake is on.
func (b *brake) holds() bool { return !b.until.IsZero() }

// state is the brake as Brake reports it: the zero Brake while it is off.
func (b *brake) state() Brake {
	if b.until.IsZero() {
		return Brake{}
	}
	return Brake{Until: b.until, Failures: len(b.streak),
		Documents: slices.Clone(b.streak[:min(len(b.streak), brakeNamed)]), Err: b.lastErr}
}
