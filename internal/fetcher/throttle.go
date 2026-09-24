package fetcher

import (
	"context"
	"sync"
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

// cooldown is a "blocked until" deadline shared by every caller of one
// upstream. A rate-limit answer extends it, and every later call waits it
// out first: the upstream's limit is per account or IP, so one worker's
// 429 means the others would walk into the same wall. The zero value is
// ready to use.
type cooldown struct {
	mu    sync.Mutex
	until time.Time
}

// extend pushes the deadline out to at least now+d.
func (c *cooldown) extend(now time.Time, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := now.Add(d); t.After(c.until) {
		c.until = t
	}
}

// wait blocks until the cooldown is over, as long as that takes at most
// maxInline. A longer cooldown returns at once with the time left, for the
// caller to fail fast rather than hold a worker; the job queue's backoff
// covers the rest.
func (c *cooldown) wait(ctx context.Context, clk clock, maxInline time.Duration) (time.Duration, error) {
	for {
		left := c.remaining(clk.now())
		if left <= 0 {
			return 0, nil
		}
		if left > maxInline {
			return left, nil
		}
		// Re-check afterwards: another caller may have extended it.
		if err := clk.sleep(ctx, left); err != nil {
			return 0, err
		}
	}
}

func (c *cooldown) remaining(now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return max(c.until.Sub(now), 0)
}
