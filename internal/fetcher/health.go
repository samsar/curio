package fetcher

import (
	"log/slog"
	"sync"
	"time"
)

// CallClass is how one request to an upstream service went, as the
// service's health sees it. ok, judged and refused are healthy answers: the
// service did its job, whatever it made of the target. The others are
// service-level failures (Failed).
type CallClass string

const (
	// CallOK: the upstream served what was asked for.
	CallOK CallClass = "ok"
	// CallJudged: it answered, and the answer is not the page (a challenge
	// or login page, the target's error status, a body over the cap).
	CallJudged CallClass = "judged"
	// CallRefused: it declined the target on purpose (a domain it excludes,
	// a 4xx about the request).
	CallRefused CallClass = "refused"
	// CallChallenged: its CDN took curio for a bot.
	CallChallenged CallClass = "challenged"
	// CallForbidden: a 403 that names no target.
	CallForbidden CallClass = "forbidden"
	// CallRateLimited: a 429, or its block of a site's keyless reads for
	// now (an AbuseAlleviationError).
	CallRateLimited CallClass = "rate_limited"
	// CallAuth: a 401 or 402, about curio's key or account.
	CallAuth CallClass = "auth"
	// CallServerError: a 5xx, a timeout status (408, 421, 425), or an answer
	// that is none (1xx, 3xx).
	CallServerError CallClass = "server_error"
	// CallNetwork: no whole answer: a transport error, a timeout, a TLS
	// failure, a body cut short.
	CallNetwork CallClass = "network"
)

// Failed reports whether c is a service-level failure: the upstream didn't
// do its job, whatever the target.
func (c CallClass) Failed() bool {
	switch c {
	case CallChallenged, CallForbidden, CallRateLimited, CallAuth, CallServerError, CallNetwork:
		return true
	case CallOK, CallJudged, CallRefused:
	}
	return false
}

// UpstreamState sums up an upstream's health (see healthTracker.snapshot
// for how it is derived).
type UpstreamState string

const (
	// UpstreamDisabled: the upstream is configured off.
	UpstreamDisabled UpstreamState = "disabled"
	// UpstreamIdle: no calls in the window.
	UpstreamIdle UpstreamState = "idle"
	// UpstreamOK: calls in the window, few of them failures.
	UpstreamOK UpstreamState = "ok"
	// UpstreamDegraded: at least degradedShare of the calls in the window
	// failed.
	UpstreamDegraded UpstreamState = "degraded"
	// UpstreamPaused: calls are held back by a cooldown the upstream asked
	// for (a rate limit, a challenge).
	UpstreamPaused UpstreamState = "paused"
	// UpstreamFailing: nothing but failures, across targets, since its last
	// healthy answer (failureStreak.failing).
	UpstreamFailing UpstreamState = "failing"
)

// UpstreamHealth is a snapshot of an upstream's health.
type UpstreamHealth struct {
	Name    string
	Enabled bool
	State   UpstreamState
	// LastSuccess is the last healthy answer (ok, judged or refused); zero
	// when there has been none since the daemon started.
	LastSuccess time.Time
	// LastFailure and LastFailureClass are the last service-level failure;
	// zero and empty when there has been none.
	LastFailure      time.Time
	LastFailureClass CallClass
	// Window is how far back Recent looks, to the minute.
	Window time.Duration
	// Recent counts the calls in the window by class, holding only classes
	// with calls.
	Recent map[CallClass]int
	// CooldownUntil is when the pause in effect ends; zero when none is.
	CooldownUntil time.Time
	// SitePauses are the upstream's blocks of one site each in effect, by
	// site; empty when there are none. They leave State alone: a block is
	// about one site's reads, not the service.
	SitePauses []SitePause
}

// SitePause is an upstream's block of one site's reads: until when it
// holds the site's pages back.
type SitePause struct {
	Site  string
	Until time.Time
}

const (
	// healthWindow is how far back Recent and the degraded share look.
	healthWindow = 15 * time.Minute
	// healthBuckets splits the window into one-minute buckets, which keeps
	// a tracker's memory fixed however many calls it counts.
	healthBuckets = int(healthWindow / time.Minute)
	// degradedShare is the share of service-level failures in the window
	// that makes an upstream degraded. Jina times out on about 4% of its
	// requests while it works.
	degradedShare = 0.25
	// failingStreak and failingAfter decide when a streak of service-level
	// failures that spans two or more target hosts makes an upstream
	// failing: at failingStreak failures, or once failingAfter lies between
	// its first failure and its latest. The duration rule is for a pause
	// that lets only one call through per pause (a challenge's, 10
	// minutes): four such calls fail in 30 minutes.
	failingStreak = 5
	failingAfter  = 30 * time.Minute
)

// healthTracker keeps an upstream's health from the outcome of each of its
// requests (record), in fixed memory and without a goroutine of its own:
// the state is derived when it is read (snapshot), except failing, which
// only a recorded call starts or ends. That makes failing the one state
// whose changes are logged, each exactly once and in order: a WARN when it
// starts and an INFO when the next healthy answer ends it. Degraded, paused
// and idle come and go with the clock; logging them would need a ticker,
// and would chatter on every routine 429. The zero value is not ready to
// use: see newHealthTracker.
type healthTracker struct {
	name string
	log  *slog.Logger // carries the upstream's name

	mu          sync.Mutex
	buckets     [healthBuckets]callBucket
	lastSuccess time.Time
	lastFailure time.Time
	lastClass   CallClass
	streak      failureStreak
	failing     bool
}

// callBucket counts the calls of one minute by class.
type callBucket struct {
	minute int64 // Unix time in minutes
	counts map[CallClass]int
}

// failureStreak is the run of service-level failures since the last healthy
// answer.
type failureStreak struct {
	n         int
	since     time.Time // the first failure's time
	firstHost string
	manyHosts bool // a host other than firstHost failed too
}

func newHealthTracker(name string, log *slog.Logger) *healthTracker {
	return &healthTracker{name: name, log: log.With("upstream", name)}
}

// record counts the outcome of one request made at now for a target on
// host: its class and, for a failure, its error. The transition into or out
// of failing is decided and logged under the lock, so concurrent recorders
// log each one once, in order.
func (t *healthTracker) record(now time.Time, host string, class CallClass, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.bucket(now).counts[class]++
	if !class.Failed() {
		t.lastSuccess = now
		if t.failing {
			t.log.Info("upstream recovered",
				"failed_for", now.Sub(t.streak.since).Round(time.Second).String(), "failures", t.streak.n)
		}
		t.streak, t.failing = failureStreak{}, false
		return
	}

	t.lastFailure, t.lastClass = now, class
	t.streak.add(now, host)
	if t.failing || !t.streak.failing(now) {
		return
	}
	t.failing = true
	var text string
	if err != nil {
		text = snippet([]byte(err.Error()))
	}
	t.log.Warn("upstream failing",
		"class", string(class), "failures", t.streak.n, "since", t.streak.since, "err", text)
}

// bucket returns the bucket for now's minute, emptied if it last counted an
// older one.
func (t *healthTracker) bucket(now time.Time) *callBucket {
	minute := now.Unix() / 60
	b := &t.buckets[minute%int64(healthBuckets)]
	if b.counts == nil || b.minute != minute {
		*b = callBucket{minute: minute, counts: make(map[CallClass]int)}
	}
	return b
}

// add counts a failure at now for a target on host.
func (s *failureStreak) add(now time.Time, host string) {
	if s.n == 0 {
		s.since, s.firstHost = now, host
	} else if host != s.firstHost {
		s.manyHosts = true
	}
	s.n++
}

// failing reports whether the streak, as of its latest failure at now, says
// the upstream is failing. It needs two hosts, so one slow target can't pass
// for an outage however long its timeouts run.
func (s failureStreak) failing(now time.Time) bool {
	return s.manyHosts && (s.n >= failingStreak || now.Sub(s.since) >= failingAfter)
}

// snapshot is the upstream's health at now. enabled is whether it is
// configured on, and cooldownUntil when its cooldown ends. The state is the
// first of these that holds:
//
//  1. disabled: not enabled.
//  2. failing: set by record, until the next healthy answer.
//  3. paused: the cooldown ends after now.
//  4. idle: no calls in the window.
//  5. degraded: at least degradedShare of the calls in the window failed.
//  6. ok.
func (t *healthTracker) snapshot(now time.Time, enabled bool, cooldownUntil time.Time) UpstreamHealth {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := UpstreamHealth{
		Name:             t.name,
		Enabled:          enabled,
		LastSuccess:      t.lastSuccess,
		LastFailure:      t.lastFailure,
		LastFailureClass: t.lastClass,
		Window:           healthWindow,
		Recent:           t.recent(now),
	}
	if cooldownUntil.After(now) {
		h.CooldownUntil = cooldownUntil
	}

	var calls, failed int
	for class, n := range h.Recent {
		calls += n
		if class.Failed() {
			failed += n
		}
	}
	switch {
	case !enabled:
		h.State = UpstreamDisabled
	case t.failing:
		h.State = UpstreamFailing
	case !h.CooldownUntil.IsZero():
		h.State = UpstreamPaused
	case calls == 0:
		h.State = UpstreamIdle
	case float64(failed) >= degradedShare*float64(calls):
		h.State = UpstreamDegraded
	default:
		h.State = UpstreamOK
	}
	return h
}

// recent sums the buckets of the window that ends at now's minute.
func (t *healthTracker) recent(now time.Time) map[CallClass]int {
	latest := now.Unix() / 60
	oldest := latest - int64(healthBuckets) + 1
	recent := make(map[CallClass]int)
	for _, b := range t.buckets {
		if b.minute < oldest || b.minute > latest {
			continue
		}
		for class, n := range b.counts {
			recent[class] += n
		}
	}
	return recent
}
