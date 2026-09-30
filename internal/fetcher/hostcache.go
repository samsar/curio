package fetcher

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// HostFailureKind classifies why a host previously failed. We only cache
// kinds that are host-wide ("this whole site rejects us") rather than
// path-specific ("this URL was 404"). 404s, thin pages and timeouts aren't
// cached because a failure on /foo doesn't tell us anything about /bar.
// See hostVerdict for how a failure is classified, and Native's
// pastCachedHost for what an entry of each kind does to the host's other
// pages.
type HostFailureKind int

const (
	HostFailUnreachable HostFailureKind = iota // name doesn't exist, connection refused, no route
	HostFailAntiBot                            // 403 / 503 — Cloudflare / WAF style
	HostFailLoginWall                          // redirected onto the site's own login page
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

// sentinel is the error a cache hit of this kind wraps.
func (k HostFailureKind) sentinel() error {
	switch k {
	case HostFailUnreachable:
		return ErrHostUnreachable
	case HostFailAntiBot:
		return ErrAntiBot
	case HostFailLoginWall:
		return ErrLoginWall
	}
	return fmt.Errorf("unknown host failure kind %d", int(k))
}

// waitReason is what a page on host waits for while an anti-bot or
// login-wall entry holds it back, worded to follow "waiting for". An
// unreachable entry holds no page back: it fails the page (pastCachedHost).
func (k HostFailureKind) waitReason(host string) string {
	if k == HostFailLoginWall {
		return host + " to be tried again: it sent curio's last request to its login page"
	}
	return host + " to be tried again: it blocked curio's last request"
}

// hostCacheEntry is one host's cached failure: its kind, what the origin
// answered (originErr, which every page the entry holds back quotes), and
// when the entry expires.
type hostCacheEntry struct {
	kind      HostFailureKind
	originErr string
	until     time.Time
}

// err is the error of a page the entry holds back: the kind's sentinel,
// for errors.Is, and the origin's answer in a "(cached: …)" suffix, which
// survives into last_error for diagnosis.
func (e hostCacheEntry) err() error {
	return fmt.Errorf("native: %w (cached: %s)", e.kind.sentinel(), e.originErr)
}

// hostFailureCache remembers host-wide failures (hostVerdict) for ttl, so
// the rest of a bad host's pages don't repeat the origin request whose
// answer is known: an unreachable host's pages fail at once, and an
// anti-bot or login-wall host's go to Jina without it (Native's
// pastCachedHost). Time is the caller's, from the Native's clock.
// Concurrent-safe; bounded by sweeping long-expired entries on every Put
// (cheap because the population of hosts in a corpus is small — hundreds,
// not millions).
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

// Get returns host's entry while it is fresh at now, and false on a miss or
// once it has expired.
func (c *hostFailureCache) Get(host string, now time.Time) (hostCacheEntry, bool) {
	if host == "" {
		return hostCacheEntry{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[host]
	if !ok || !now.Before(e.until) {
		return hostCacheEntry{}, false
	}
	return e, true
}

// Put records a host-wide failure seen at now, quoting originErr, the
// origin's own answer. Sweeps expired entries opportunistically so the map
// doesn't grow without bound.
func (c *hostFailureCache) Put(host string, kind HostFailureKind, originErr string, now time.Time) {
	if host == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[host] = hostCacheEntry{kind: kind, originErr: originErr, until: now.Add(c.ttl)}

	// Opportunistic sweep: on every Put, drop any entry written 2×TTL ago
	// or more. Cheap because typical map size is small.
	for h, e := range c.entries {
		if !e.until.Add(c.ttl).After(now) {
			delete(c.entries, h)
		}
	}
}

// hostOf extracts the lowercased hostname (no port) from a URL string, the
// host cache's key. Returns "" on any parse failure; callers should treat
// that as "no caching."
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
