package fetcher

import (
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// DefaultJinaSiteRequestsPerMinute is how many Jina Reader requests curio
// sends for pages of one site a minute, unless configured otherwise
// (fetcher.native.jina_site_requests_per_minute). Jina blocks keyless reads
// of a domain after a run of them (an AbuseAlleviationError, for about an
// hour). Six a minute is under the 10 in one minute bloomberg.com took
// without a block, and leaves the keyless 20 a minute to three sites at
// once. It is not under every run that tripped one: mobile.twitter.com was
// blocked after about 80 reads in 19 minutes, some 4 a minute, so a long
// run of one site at this pace may still be blocked. curio then waits the
// block out (sitePacer), and the value can be lowered.
const DefaultJinaSiteRequestsPerMinute = 6

// sitePaceSweepEvery is the least time between two sweeps of the sites
// that have gone idle: often enough to bound the pacer's map by the sites
// of the last few minutes, rarely enough that a sweep costs nothing next
// to a request.
const sitePaceSweepEvery = time.Minute

// siteOf is the site a Jina request for a page on host counts against: its
// registrable domain, so mobile.twitter.com and twitter.com are one site,
// as www.forbes.com and forbes.com are. A host without one is its own site:
// an IP address, a name under no public suffix (localhost, intranet), a
// public suffix itself (github.io), or a name publicsuffix can't read.
func siteOf(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if net.ParseIP(host) != nil {
		return host
	}
	site, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		// No registrable domain: the host is the most specific site there is.
		return host
	}
	return site
}

// sitePacer spaces the Jina Reader requests for each site (siteOf) at
// least interval apart, and holds a site's pages while Jina Reader blocks
// the site. Every fetch worker shares one, under one lock that no wait is
// made under. It answers three questions:
//
//   - take: when may a page's request go? A turn that starts within the
//     inline cap is waited for; a later one defers the fetch. Deferred pages
//     get Untils one interval apart, after those already handed out (tail),
//     so they come back spread out, and one that comes back on time finds
//     its turn free.
//   - spaceSend: may it be sent now? The shared limiter's queue can hand a
//     site's requests their tokens in a bunch; this spaces their sends
//     again, so no two go less than interval apart.
//   - holdForBlock, after block recorded Jina Reader's block of the site:
//     is the site blocked? Then every page of it waits for the block's end,
//     the one that received it included, spread out the same way.
//
// It is not a rate.Limiter per site. Cancelling a deferred page's
// reservation sends every deferred page back at the same next slot, and
// keeping it leaves the page unable to claim the slot when it comes back,
// so the site starves: in a simulation of 169 pages of one site on 16
// workers, the first way took about 22 queue claims a page, and this way
// about 2. Its state per site is fixed, with nothing per page, and a site's
// state goes once the site is idle.
type sitePacer struct {
	interval time.Duration

	mu        sync.Mutex
	sites     map[string]*siteState
	lastSweep time.Time
}

// siteState is one site's pacing. The zero value is a site with no turn,
// deferral, send or block.
type siteState struct {
	turn    time.Time // the start of the latest turn handed out inline
	tail    time.Time // the latest Until handed to a deferred page
	sent    time.Time // when the latest request was cleared to be sent
	blocked time.Time // when Jina Reader's block of the site ends
	reason  string    // what Jina Reader said when it set blocked
}

// idle reports whether a site's state can go: its turns, deferrals and
// sends are an interval old or more, so a new state paces the site the
// same, and no block is in effect.
func (s *siteState) idle(now time.Time, interval time.Duration) bool {
	return !latest(s.turn, s.tail, s.sent).Add(interval).After(now) && !s.blocked.After(now)
}

// siteTurn is the pacer's answer for one request: go on after wait, or,
// when until is set, defer the fetch until then instead. block is set for a
// deferral that waits for Jina Reader's block of the site.
type siteTurn struct {
	wait  time.Duration
	until time.Time
	block *siteBlock
}

// siteBlock is Jina Reader's block of a site: when it ends, and the reason
// Jina gave for it.
type siteBlock struct {
	until  time.Time
	reason string
}

func newSitePacer(interval time.Duration) *sitePacer {
	return &sitePacer{interval: interval, sites: make(map[string]*siteState)}
}

// take hands a request for site, asked for at now, its turn: at least an
// interval after the site's previous turn, and not before a block of the
// site ends. A turn that starts within maxInline is the caller's to wait
// for; a later one is a deferral (see deferral).
func (p *sitePacer) take(site string, now time.Time, maxInline time.Duration) siteTurn {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state(site, now)
	earliest := latest(now, s.turn.Add(p.interval), s.blocked)
	if earliest.Sub(now) > maxInline {
		return p.deferral(s, earliest, now.Add(maxInline))
	}
	s.turn = earliest
	return siteTurn{wait: earliest.Sub(now)}
}

// spaceSend clears a request for site, whose turn and token it holds, to
// be sent at now or after the wait it returns: an interval after the
// site's previous request, whatever the shared limiter's queue did.
func (p *sitePacer) spaceSend(site string, now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state(site, now)
	s.sent = latest(now, s.sent.Add(p.interval))
	return s.sent.Sub(now)
}

// holdForBlock is the deferral of a request for site about to be sent at
// now, while Jina Reader blocks the site; ok is false when it doesn't.
func (p *sitePacer) holdForBlock(site string, now time.Time) (t siteTurn, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state(site, now)
	if !s.blocked.After(now) {
		return siteTurn{}, false
	}
	return p.deferral(s, s.blocked, now), true
}

// block records Jina Reader's block of site until until, for reason, from
// an answer received at now. It only ever extends a block in effect. It
// returns the deferral of the page that received the answer, and whether
// the block is a new one: whether none was in effect.
func (p *sitePacer) block(site string, until time.Time, reason string, now time.Time) (t siteTurn, started bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state(site, now)
	started = !s.blocked.After(now)
	if until.After(s.blocked) {
		s.blocked, s.reason = until, reason
	}
	return p.deferral(s, s.blocked, now), started
}

// pauses lists the blocks in effect at now, by site.
func (p *sitePacer) pauses(now time.Time) []SitePause {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep(now)
	var out []SitePause
	for site, s := range p.sites {
		if s.blocked.After(now) {
			out = append(out, SitePause{Site: site, Until: s.blocked})
		}
	}
	slices.SortFunc(out, func(a, b SitePause) int { return strings.Compare(a.Site, b.Site) })
	return out
}

// deferral defers a request of s that could go at earliest to the next
// free Until: earliest, or an interval after the latest Until handed out.
// It waits for the site's block when the block ends after heldPast, the
// latest the request could have gone without it. Called with p.mu held.
func (p *sitePacer) deferral(s *siteState, earliest, heldPast time.Time) siteTurn {
	s.tail = latest(earliest, s.tail.Add(p.interval))
	t := siteTurn{until: s.tail}
	if s.blocked.After(heldPast) {
		t.block = &siteBlock{until: s.blocked, reason: s.reason}
	}
	return t
}

// state returns site's state, after dropping the idle sites if a sweep is
// due. Called with p.mu held.
func (p *sitePacer) state(site string, now time.Time) *siteState {
	p.sweep(now)
	s, ok := p.sites[site]
	if !ok {
		s = &siteState{}
		p.sites[site] = s
	}
	return s
}

// sweep drops the sites idle at now, at most once per sitePaceSweepEvery.
// Called with p.mu held.
func (p *sitePacer) sweep(now time.Time) {
	if now.Sub(p.lastSweep) < sitePaceSweepEvery {
		return
	}
	p.lastSweep = now
	for site, s := range p.sites {
		if s.idle(now, p.interval) {
			delete(p.sites, site)
		}
	}
}

// latest is the latest of ts.
func latest(ts ...time.Time) time.Time {
	var t time.Time
	for _, u := range ts {
		if u.After(t) {
			t = u
		}
	}
	return t
}
