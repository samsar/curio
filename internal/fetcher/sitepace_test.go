package fetcher

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// testSiteInterval is the default spacing of one site's Jina requests.
const testSiteInterval = time.Minute / DefaultJinaSiteRequestsPerMinute

func TestSiteOf(t *testing.T) {
	cases := map[string]string{
		"mobile.twitter.com": "twitter.com",
		"www.forbes.com":     "forbes.com",
		"forbes.com":         "forbes.com",
		"Forbes.COM.":        "forbes.com",
		"akveo.github.io":    "akveo.github.io", // github.io is a public suffix
		"www.bbc.co.uk":      "bbc.co.uk",
		"github.io":          "github.io",
		"co.uk":              "co.uk",
		"localhost":          "localhost",
		"intranet":           "intranet",
		"127.0.0.1":          "127.0.0.1",
		"::1":                "::1",
		"":                   "",
	}
	for host, want := range cases {
		assert.Equal(t, want, siteOf(host), host)
	}
}

// TestSitePacer_Take: 20 pages of one site at one instant get the turns
// within the inline cap, an interval apart, and the rest deferrals an
// interval apart after them. A page that comes back on time gets its turn
// at once.
func TestSitePacer_Take(t *testing.T) {
	p := newSitePacer(testSiteInterval)
	t0 := newFakeClock().now()
	var waits []time.Duration
	var untils []time.Duration
	for range 20 {
		turn := p.take("news.example", t0, maxInlineJinaWait)
		if turn.until.IsZero() {
			waits = append(waits, turn.wait)
			continue
		}
		assert.Nil(t, turn.block)
		untils = append(untils, turn.until.Sub(t0))
	}
	assert.Equal(t, []time.Duration{0, 10 * time.Second, 20 * time.Second, 30 * time.Second}, waits)
	want := make([]time.Duration, 0, 16)
	for i := 4; i < 20; i++ {
		want = append(want, time.Duration(i)*testSiteInterval)
	}
	assert.Equal(t, want, untils)

	for i, until := range untils[:3] {
		turn := p.take("news.example", t0.Add(until), maxInlineJinaWait)
		assert.Zero(t, turn.wait, "deferred page %d comes back to a free turn", i)
		assert.True(t, turn.until.IsZero())
	}
}

// TestSitePacer_SitesDontShareTurns: pages of two sites arriving together
// each get an immediate first turn, and each site's second page waits one
// interval.
func TestSitePacer_SitesDontShareTurns(t *testing.T) {
	p := newSitePacer(testSiteInterval)
	t0 := newFakeClock().now()
	for _, want := range []time.Duration{0, testSiteInterval} {
		for _, site := range []string{"news.example", "blog.example"} {
			turn := p.take(site, t0, maxInlineJinaWait)
			assert.Equal(t, want, turn.wait, site)
		}
	}
}

// TestSitePacer_SpaceSend: requests for one site cleared together are sent
// an interval apart, whatever turns they took; another site's aren't held.
func TestSitePacer_SpaceSend(t *testing.T) {
	p := newSitePacer(testSiteInterval)
	t0 := newFakeClock().now()
	waits := make([]time.Duration, 0, 3)
	for range 3 {
		waits = append(waits, p.spaceSend("news.example", t0))
	}
	assert.Equal(t, []time.Duration{0, testSiteInterval, 2 * testSiteInterval}, waits)
	assert.Zero(t, p.spaceSend("blog.example", t0))
	assert.Zero(t, p.spaceSend("news.example", t0.Add(3*testSiteInterval)), "sent on time")
}

// TestSitePacer_Block: a block holds the site's turns and sends until it
// ends, spreads the pages it holds from its end an interval apart, the
// page that met it included, and is only ever extended. A block that ends
// within the inline cap is waited out.
func TestSitePacer_Block(t *testing.T) {
	p := newSitePacer(testSiteInterval)
	t0 := newFakeClock().now()
	ends := t0.Add(time.Hour)

	turn, started := p.block("news.example", ends, "AbuseAlleviationError: …", t0)
	assert.True(t, started)
	assert.Equal(t, ends, turn.until)
	require.NotNil(t, turn.block)
	assert.Equal(t, siteBlock{until: ends, reason: "AbuseAlleviationError: …"}, *turn.block)

	_, started = p.block("news.example", ends.Add(-time.Minute), "shorter", t0.Add(time.Second))
	assert.False(t, started, "a block in effect is no new one")
	assert.Equal(t, []SitePause{{Site: "news.example", Until: ends}}, p.pauses(t0), "never shortened")

	for i := range 3 {
		turn := p.take("news.example", t0.Add(time.Minute), maxInlineJinaWait)
		require.NotNil(t, turn.block, "page %d waits for the block", i)
		assert.Equal(t, ends.Add(time.Duration(i+2)*testSiteInterval), turn.until,
			"spread from the block's end, after the page that met it and the one the shorter answer held")
	}
	held, ok := p.holdForBlock("news.example", t0.Add(2*time.Minute))
	require.True(t, ok)
	assert.Equal(t, ends.Add(5*testSiteInterval), held.until)
	_, ok = p.holdForBlock("blog.example", t0)
	assert.False(t, ok, "another site isn't blocked")

	soon := newSitePacer(testSiteInterval)
	soon.block("news.example", t0.Add(20*time.Second), "", t0)
	turn = soon.take("news.example", t0, maxInlineJinaWait)
	assert.Equal(t, 20*time.Second, turn.wait, "a block ending within the cap is waited out")
	assert.True(t, turn.until.IsZero())

	turn = p.take("news.example", ends.Add(10*time.Minute), maxInlineJinaWait)
	assert.True(t, turn.until.IsZero(), "the block is over")
	assert.Empty(t, p.pauses(ends.Add(10*time.Minute)))
	_, started = p.block("news.example", ends.Add(time.Hour), "again", ends.Add(10*time.Minute))
	assert.True(t, started, "a block after the last one ended is a new one")
}

// TestSitePacer_TurnAfterABlock: a turn deferred for the site's pace, with
// a block that has ended or ends before the turn would go, waits for its
// turn, not for the block: it carries no block, so the page isn't told
// Jina's block holds it.
func TestSitePacer_TurnAfterABlock(t *testing.T) {
	t0 := newFakeClock().now()
	for _, tc := range []struct {
		name string
		ends time.Time // the block's
		now  time.Time // when the burst asks
	}{
		{"ended", t0.Add(time.Minute), t0.Add(2 * time.Minute)},
		{"ending within the inline wait", t0.Add(10 * time.Second), t0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newSitePacer(testSiteInterval)
			p.block("news.example", tc.ends, "AbuseAlleviationError: …", t0)
			var turn siteTurn
			for range 100 {
				if turn = p.take("news.example", tc.now, maxInlineJinaWait); !turn.until.IsZero() {
					break
				}
			}
			require.False(t, turn.until.IsZero(), "a burst past the inline cap defers")
			assert.Nil(t, turn.block, "the block ends before the turn")
		})
	}
}

// TestSitePacer_Pauses: the blocks in effect, by site.
func TestSitePacer_Pauses(t *testing.T) {
	p := newSitePacer(testSiteInterval)
	t0 := newFakeClock().now()
	p.block("twitter.com", t0.Add(time.Hour), "", t0)
	p.block("forbes.com", t0.Add(2*time.Hour), "", t0)
	p.block("x.com", t0.Add(time.Minute), "", t0)
	p.take("news.example", t0, maxInlineJinaWait)
	assert.Equal(t, []SitePause{
		{Site: "forbes.com", Until: t0.Add(2 * time.Hour)},
		{Site: "twitter.com", Until: t0.Add(time.Hour)},
		{Site: "x.com", Until: t0.Add(time.Minute)},
	}, p.pauses(t0))
	assert.Equal(t, []SitePause{{Site: "forbes.com", Until: t0.Add(2 * time.Hour)}}, p.pauses(t0.Add(time.Hour)))
}

// TestSitePacer_SweepsIdleSites: once a burst's turns, deferrals and sends
// are an interval old and no block is in effect, the sites' state is gone,
// at the next sweep.
func TestSitePacer_SweepsIdleSites(t *testing.T) {
	p := newSitePacer(testSiteInterval)
	t0 := newFakeClock().now()
	var last time.Time
	for i := range 30 {
		site := fmt.Sprintf("site%d.example", i%3)
		turn := p.take(site, t0, maxInlineJinaWait)
		if turn.until.IsZero() {
			p.spaceSend(site, t0.Add(turn.wait))
		}
		last = latest(last, turn.until)
	}
	p.block("blocked.example", t0.Add(time.Hour), "", t0)
	sites := func() []string {
		p.mu.Lock()
		defer p.mu.Unlock()
		return slices.Sorted(maps.Keys(p.sites))
	}

	p.pauses(last)
	assert.Len(t, sites(), 4, "a deferral an interval old or less keeps a site")

	p.pauses(last.Add(sitePaceSweepEvery))
	assert.Equal(t, []string{"blocked.example"}, sites(), "a block in effect keeps a site")

	p.pauses(t0.Add(time.Hour + 2*sitePaceSweepEvery))
	assert.Empty(t, sites())
}

// TestSitePacer_Concurrent: 50 callers taking turns for one site at one
// instant get exactly the inline turns, and the others distinct Untils an
// interval apart.
func TestSitePacer_Concurrent(t *testing.T) {
	p := newSitePacer(testSiteInterval)
	t0 := newFakeClock().now()
	turns := make([]siteTurn, 50)
	var wg sync.WaitGroup
	for i := range turns {
		wg.Go(func() { turns[i] = p.take("news.example", t0, maxInlineJinaWait) })
	}
	wg.Wait()

	var waits []time.Duration
	var untils []time.Time
	for _, turn := range turns {
		if turn.until.IsZero() {
			waits = append(waits, turn.wait)
		} else {
			untils = append(untils, turn.until)
		}
	}
	slices.Sort(waits)
	assert.Equal(t, []time.Duration{0, 10 * time.Second, 20 * time.Second, 30 * time.Second}, waits)
	require.Len(t, untils, 46)
	slices.SortFunc(untils, func(a, b time.Time) int { return a.Compare(b) })
	for i, until := range untils {
		assert.Equal(t, t0.Add(time.Duration(i+4)*testSiteInterval), until)
	}
}

// pacedNative is a Native on fc with Jina's site spacing at the default
// and the shared limiter unlimited, whose origin answers origin and whose
// Jina answers jina(n) to its nth request (from 1), for pages given by URL.
// It records when each Jina request was sent, by site.
func pacedNative(t *testing.T, fc *fakeClock, origin fakeAnswer, jina func(n int) fakeAnswer) (*Native, func() map[string][]time.Time) {
	t.Helper()
	const jinaBase = "https://jina.test/"
	n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jinaBase,
		DeadLinkDetection: true}), fc)
	n.jinaSites = newSitePacer(testSiteInterval)
	var mu sync.Mutex
	sent := map[string][]time.Time{}
	calls := 0
	n.rt = fakeRT(func(target string) (*fetchResponse, error) {
		page, isJina := strings.CutPrefix(target, jinaBase)
		if !isJina {
			return origin.respond(target)
		}
		mu.Lock()
		site := siteOf(hostOf(page))
		sent[site] = append(sent[site], fc.now())
		calls++
		answer := jina(calls)
		mu.Unlock()
		return answer.respond(target)
	})
	return n, func() map[string][]time.Time {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string][]time.Time, len(sent))
		for site, times := range sent {
			out[site] = slices.Clone(times)
		}
		return out
	}
}

// assertSpaced checks that each site's requests went an interval apart at
// least.
func assertSpaced(t *testing.T, sent map[string][]time.Time) {
	t.Helper()
	for site, times := range sent {
		for i := 1; i < len(times); i++ {
			assert.GreaterOrEqual(t, times[i].Sub(times[i-1]), testSiteInterval,
				"%s's requests %d and %d", site, i-1, i)
		}
	}
}

// jinaServes is a Jina that renders every page.
func jinaServes(int) fakeAnswer { return fakeAnswer{status: http.StatusOK, body: jinaArticleBody()} }

// TestNative_JinaSitePacing: pages of two sites fetched in turn send their
// Jina requests an interval apart per site, and one site's turns never
// hold the other's.
func TestNative_JinaSitePacing(t *testing.T) {
	fc := newFakeClock()
	n, sent := pacedNative(t, fc, htmlPage(thinPage), jinaServes)
	start := fc.now()
	for i := range 6 {
		for _, site := range []string{"news.example", "www.blog.example"} {
			res, err := n.Fetch(t.Context(), fmt.Sprintf("https://%s/%d", site, i))
			require.NoError(t, err)
			assert.Equal(t, "jina", res.Meta["via"])
		}
	}
	got := sent()
	assert.Len(t, got["news.example"], 6)
	assert.Len(t, got["blog.example"], 6)
	assertSpaced(t, got)
	assert.Equal(t, start, got["news.example"][0], "each site's first request goes at once")
	assert.Equal(t, start, got["blog.example"][0])
	assert.Equal(t, 5*testSiteInterval, fc.now().Sub(start), "the two sites' waits overlap")
}

// TestNative_JinaRetryTakesATurn: a Jina retry within a fetch is a request
// like any: after a 500, the next one goes an interval after the first, the
// 2 s backoff and the rest of the turn.
func TestNative_JinaRetryTakesATurn(t *testing.T) {
	fc := newFakeClock()
	n, sent := pacedNative(t, fc, htmlPage(thinPage), func(call int) fakeAnswer {
		if call == 1 {
			return answerStatus(http.StatusInternalServerError)
		}
		return jinaServes(call)
	})
	_, err := n.Fetch(t.Context(), "https://news.example/a")
	require.NoError(t, err)
	assert.Equal(t, []time.Duration{2 * time.Second, testSiteInterval - 2*time.Second}, fc.slept())
	assertSpaced(t, sent())
}

// TestNative_JinaSiteTurnDefers: a page whose site's turn is further off
// than the inline cap is deferred until it, without a Jina request or a
// token, retryably: its cause is the origin's.
func TestNative_JinaSiteTurnDefers(t *testing.T) {
	fc := newFakeClock()
	n, sent := pacedNative(t, fc, answerStatus(http.StatusForbidden), jinaServes)
	lim := newGatedLimiter(8)
	lim.open()
	n.jinaLimiter = lim
	var until time.Time
	for {
		turn := n.jinaSites.take("news.example", fc.now(), maxInlineJinaWait)
		if !turn.until.IsZero() {
			until = turn.until
			break
		}
	}

	_, err := n.Fetch(t.Context(), "https://news.example/a")
	de := requireDeferred(t, err, until.Add(testSiteInterval))
	assert.Equal(t, "a turn at Jina Reader for news.example", de.Reason)
	assert.True(t, strings.HasPrefix(err.Error(), "jina: not sent, news.example's turn comes at "), err.Error())
	assert.ErrorIs(t, err, ErrAntiBot)
	assert.Equal(t, store.FailureCauseAntiBot, FailureCause(fmt.Errorf("fetch failed: %w", err)))
	assert.Empty(t, sent())
	assert.Empty(t, lim.queued, "no token taken")
	assert.Empty(t, n.JinaHealth().Recent, "a held call is no call")
	assert.Empty(t, fc.slept())
}

// queueLimiter is a rateLimiter whose nth Wait lets delays[n] pass on fc
// first (none past the list), as a caller queued behind others would wait.
type queueLimiter struct {
	fc     *fakeClock
	delays []time.Duration
	calls  int
}

func (l *queueLimiter) Wait(context.Context) error {
	if l.calls < len(l.delays) {
		l.fc.advance(l.delays[l.calls])
	}
	l.calls++
	return nil
}

// TestNative_JinaSendGuard: a site's request that the shared limiter's
// queue held until its site's next turn was due is still sent an interval
// after it, not together with the next page's.
func TestNative_JinaSendGuard(t *testing.T) {
	fc := newFakeClock()
	n, sent := pacedNative(t, fc, htmlPage(thinPage), jinaServes)
	n.jinaLimiter = &queueLimiter{fc: fc, delays: []time.Duration{25 * time.Second}}

	for _, page := range []string{"https://news.example/a", "https://news.example/b"} {
		_, err := n.Fetch(t.Context(), page)
		require.NoError(t, err)
	}
	got := sent()["news.example"]
	require.Len(t, got, 2)
	assert.Equal(t, testSiteInterval, got[1].Sub(got[0]), "the guard spaces the second")
	assert.Equal(t, []time.Duration{testSiteInterval}, fc.slept(), "its turn was due; the guard held it")
}

// TestNative_SiteBlockSeenAtSend: a page that took its turn and its token
// before Jina blocked its site is held right before sending.
func TestNative_SiteBlockSeenAtSend(t *testing.T) {
	fc := newFakeClock()
	n, sent := pacedNative(t, fc, htmlPage(thinPage), jinaServes)
	lim := newGatedLimiter(1)
	n.jinaLimiter = lim
	ends := fc.now().Add(time.Hour)

	done := make(chan error, 1)
	go func() {
		_, err := n.Fetch(context.Background(), "https://news.example/a")
		done <- err
	}()
	lim.awaitQueued(t, 1)
	n.jinaSites.block("news.example", ends, "AbuseAlleviationError: blocked", fc.now())
	lim.grant()

	err := <-done
	// After the slot of the page whose answer set the block.
	de := requireDeferred(t, err, ends.Add(testSiteInterval))
	assert.Equal(t, "Jina Reader's block of news.example to lift", de.Reason)
	assert.ErrorIs(t, err, errJinaSiteBlocked)
	assert.Empty(t, sent())
}
