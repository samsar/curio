package fetcher

import (
	"context"
	"slices"
	"sync"
	"time"
)

// fakeClock is a clock whose sleeps return at once: each one is recorded
// and advances now by the requested duration, so deadline arithmetic
// (Retry-After dates, cooldowns) behaves as if the time really passed.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}
}

func (c *fakeClock) clock() clock { return clock{now: c.now, sleep: c.sleep} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return nil
}

// slept returns the durations requested so far, in order.
func (c *fakeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.sleeps)
}
