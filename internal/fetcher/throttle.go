package fetcher

import (
	"context"
	"fmt"
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
// upstream. A rate-limit answer extends it, and pace holds every later call
// until it ends: the upstream's limit is per account or IP, so one worker's
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

func (c *cooldown) remaining(now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return max(c.until.Sub(now), 0)
}

// rateLimiter paces calls to one upstream at a steady rate: a
// *rate.Limiter in production, and in tests one that holds callers until
// the test lets them through.
type rateLimiter interface {
	Wait(ctx context.Context) error
}

// pace clears one call to an upstream that lim paces and c cools down. A
// cooldown longer than maxInline returns the time left without waiting, for
// the caller to fail fast rather than hold a worker; the job queue's backoff
// covers the rest.
//
// The cooldown is checked twice. Before queueing in the limiter, so a
// cooldown already too long to sit out fails at once instead of after the
// caller's turn for a token (at 20 a minute, the 16th fetch worker would
// wait 45s only to fail), without spending a token a later call needs. And
// after lim grants a token, so a call that was already queued when a
// rate-limit answer arrived still sees it. A caller that sits a cooldown out
// queues for a fresh token afterwards, so the callers it held up resume at
// the limiter's pace rather than all at once when it ends.
func pace(ctx context.Context, lim rateLimiter, c *cooldown, clk clock, maxInline time.Duration) (time.Duration, error) {
	if left := c.remaining(clk.now()); left > maxInline {
		return left, nil
	}
	for {
		if err := lim.Wait(ctx); err != nil {
			return 0, fmt.Errorf("rate limiter: %w", err)
		}
		left := c.remaining(clk.now())
		if left == 0 || left > maxInline {
			return left, nil
		}
		if err := clk.sleep(ctx, left); err != nil {
			return 0, err
		}
	}
}

// hostGate bounds in-flight requests per host. A bulk import queues many
// URLs from one site, and letting every fetch worker hit it at once is what
// provokes the 403/503s that get the host cached as anti-bot. Waiting for a
// slot is deliberate: failing instead would spend job attempts on our own
// throttling. Entries live only while a request holds or waits for a slot,
// so the map is bounded by the hosts in flight.
type hostGate struct {
	perHost int
	mu      sync.Mutex
	hosts   map[string]*gateEntry
}

type gateEntry struct {
	slots chan struct{}
	refs  int // holders plus waiters
}

func newHostGate(perHost int) *hostGate {
	return &hostGate{perHost: perHost, hosts: make(map[string]*gateEntry)}
}

// acquire waits for a free slot on host or for ctx to end. The returned
// release frees the slot; calling it more than once is harmless.
func (g *hostGate) acquire(ctx context.Context, host string) (release func(), err error) {
	g.mu.Lock()
	e, ok := g.hosts[host]
	if !ok {
		e = &gateEntry{slots: make(chan struct{}, g.perHost)}
		g.hosts[host] = e
	}
	e.refs++
	g.mu.Unlock()

	select {
	case e.slots <- struct{}{}:
		return sync.OnceFunc(func() {
			<-e.slots
			g.drop(host, e)
		}), nil
	case <-ctx.Done():
		g.drop(host, e)
		return nil, ctx.Err()
	}
}

func (g *hostGate) drop(host string, e *gateEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e.refs--
	if e.refs == 0 {
		delete(g.hosts, host)
	}
}
