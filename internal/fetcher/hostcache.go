package fetcher

import (
	"errors"
	"net/url"
	"sync"
	"time"
)

// HostFailureKind classifies why a host previously failed. We only cache
// kinds that are host-wide ("this whole site rejects us") rather than
// path-specific ("this URL was 404"). 404s and timeouts aren't cached
// because a 404 on /foo doesn't tell us anything about /bar.
type HostFailureKind int

const (
	HostFailUnreachable HostFailureKind = iota // DNS lookup failed, connection refused
	HostFailAntiBot                            // 403 / 503 — Cloudflare / WAF style
	HostFailLoginWall                          // thin content / paywall — readability + Jina both failed
)

func (k HostFailureKind) String() string {
	switch k {
	case HostFailUnreachable:
		return "unreachable"
	case HostFailAntiBot:
		return "anti-bot"
	case HostFailLoginWall:
		return "login-wall"
	default:
		return "unknown"
	}
}

// ErrHostUnreachable is wrapped by tryReadability for DNS / dial errors.
// Distinct from ErrAntiBot because we shouldn't bother with Jina either
// — Jina can't reach a host that doesn't exist any more than we can.
var ErrHostUnreachable = errors.New("host unreachable")

// hostCacheEntry stores one prior failure for a host. originalErr is
// preserved so future short-circuits can return the same diagnostic
// instead of a generic "we cached this" message.
type hostCacheEntry struct {
	kind        HostFailureKind
	originalErr string
	seenAt      time.Time
}

// hostFailureCache memoizes host-wide failures so repeat fetches of bad
// domains short-circuit without burning the full 5×retry × N×backoff
// budget. Concurrent-safe; bounded by sweeping expired entries on every
// Put (cheap because the population of hosts in a corpus is small —
// hundreds, not millions).
type hostFailureCache struct {
	mu      sync.RWMutex
	entries map[string]hostCacheEntry
	ttl     time.Duration
}

func newHostFailureCache(ttl time.Duration) *hostFailureCache {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &hostFailureCache{
		entries: make(map[string]hostCacheEntry),
		ttl:     ttl,
	}
}

// Get returns the cached failure for host if one exists and is fresh.
// Returns zero entry + false on miss or expired.
func (c *hostFailureCache) Get(host string) (hostCacheEntry, bool) {
	if host == "" {
		return hostCacheEntry{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[host]
	if !ok || time.Since(e.seenAt) > c.ttl {
		return hostCacheEntry{}, false
	}
	return e, true
}

// Put records a host-wide failure. Sweeps expired entries opportunistically
// so the map doesn't grow without bound.
func (c *hostFailureCache) Put(host string, kind HostFailureKind, errMsg string) {
	if host == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[host] = hostCacheEntry{kind: kind, originalErr: errMsg, seenAt: time.Now()}

	// Opportunistic sweep: on every Put, drop any entry older than 2×TTL.
	// Cheap because typical map size is small.
	cutoff := time.Now().Add(-2 * c.ttl)
	for h, e := range c.entries {
		if e.seenAt.Before(cutoff) {
			delete(c.entries, h)
		}
	}
}

// hostOf extracts the lowercased host from a URL string. Returns "" on
// any parse failure; callers should treat that as "no caching."
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
