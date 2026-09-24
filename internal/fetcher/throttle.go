package fetcher

import (
	"context"
	"time"
)

// clock is the time source for the waits fetchers make on their own:
// backoffs, rate-limit sleeps, Retry-After arithmetic. Production uses the
// real clock; tests swap in one that records the requested durations and
// returns at once.
type clock struct {
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

var realClock = clock{now: time.Now, sleep: sleepCtx}

// sleepCtx waits for d or until ctx is done, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
