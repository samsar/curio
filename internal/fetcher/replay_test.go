package fetcher

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// The replay drives one Native through an import's worth of fetches of one
// busy site with a queue simulator: the host cache, the site's Jina turns
// and Jina's block of the site have to work together over many fetches,
// retries and deferrals, which no test of one of them alone shows. The
// simulator mirrors only the job queue's rules the fetcher's errors meet
// (internal/jobs can't be imported here): claims by (run_after, creation),
// a deferral refunded until Until (at least a second off) while the job is
// younger than a day, a retryable failure retried after 60 s doubling, and
// a permanent failure or the fifth failed attempt final.

// replayJob is one fetch job of the replay's queue.
type replayJob struct {
	url      string
	runAt    time.Time
	seq      int // creation order
	attempts int
	fetches  int
	deferred bool // its last fetch was deferred
	err      error
}

// replayQueue orders jobs as JobQueue claims them: by run_after, then by
// creation.
type replayQueue []*replayJob

func (q replayQueue) Len() int { return len(q) }
func (q replayQueue) Less(i, j int) bool {
	if !q[i].runAt.Equal(q[j].runAt) {
		return q[i].runAt.Before(q[j].runAt)
	}
	return q[i].seq < q[j].seq
}
func (q replayQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *replayQueue) Push(x any)   { *q = append(*q, x.(*replayJob)) }
func (q *replayQueue) Pop() any {
	old := *q
	job := old[len(old)-1]
	*q = old[:len(old)-1]
	return job
}

const (
	replayMaxAttempts = 5
	replayRetryDelay  = 60 * time.Second
)

// runQueue runs jobs, all created at fc's now, through n until none is left,
// calling claimed before and settled after each fetch. The clock moves to
// each job's run_after as it is claimed, and on by whatever the fetch sits
// out.
func runQueue(n *Native, fc *fakeClock, jobs []*replayJob, claimed, settled func(*replayJob)) {
	created := fc.now()
	q := make(replayQueue, 0, len(jobs))
	for i, job := range jobs {
		job.runAt, job.seq = created, i
		q = append(q, job)
	}
	heap.Init(&q)
	for q.Len() > 0 {
		job := heap.Pop(&q).(*replayJob)
		if wait := job.runAt.Sub(fc.now()); wait > 0 {
			fc.advance(wait)
		}
		claimed(job)
		job.attempts++
		job.fetches++
		_, job.err = n.Fetch(context.Background(), job.url)
		de, deferred := errors.AsType[*DeferError](job.err)
		job.deferred = deferred && fc.now().Sub(created) < store.DeferralBudget
		settled(job)
		_, permanent := errors.AsType[*PermanentError](job.err)
		switch {
		case job.err == nil, permanent, !job.deferred && job.attempts >= replayMaxAttempts:
			continue
		case job.deferred:
			job.attempts--
			job.runAt = latest(de.Until, fc.now().Add(time.Second))
		default:
			job.runAt = fc.now().Add(replayRetryDelay << (job.attempts - 1))
		}
		heap.Push(&q, job)
	}
}

// TestReplay_BurstOfOneSite replays the owner's import shape: 80 pages of
// one site, on two of its hosts, whose origin answers 403, with 10 pages
// of another site mixed in. Jina answers the busy site's first request
// with a CAPTCHA, then a mix of articles, CAPTCHAs and the target's 403,
// then blocks the site for an hour, then serves it.
func TestReplay_BurstOfOneSite(t *testing.T) {
	const (
		jinaBase      = "https://jina.test/"
		busySite      = "news.example"
		blockAnswerNo = 10
	)
	fc := newFakeClock()
	start := fc.now()
	blockEnds := start.Add(time.Hour)
	captcha := jinaPage("An article", []string{warnCaptcha}, longArticleBody)
	article := &fakeAnswer{status: http.StatusOK, body: jinaArticleBody()}
	busyAnswers := map[int]*fakeAnswer{
		1: captcha, 2: article, 3: captcha, 4: jinaTarget(http.StatusForbidden),
		5: article, 6: captcha, 7: article, 8: jinaTarget(http.StatusForbidden), 9: article,
		blockAnswerNo: {status: http.StatusForbidden, contentType: "text/plain; charset=utf-8",
			body: "AbuseAlleviationError: Anonymous access to domain m.news.example blocked until " +
				jinaBlockDate(blockEnds) + " due to previous abuse found on https://m.news.example/story/10: " +
				"DDoS attack suspected: Too many requests"},
	}

	logs := &logRecorder{}
	n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jinaBase,
		DeadLinkDetection: true, Log: slog.New(logs)}), fc)
	n.jinaSites = newSitePacer(testSiteInterval)
	var busyJina []time.Time
	jinaRequests := map[string]int{}
	n.rt = fakeRT(func(target string) (*fetchResponse, error) {
		page, isJina := strings.CutPrefix(target, jinaBase)
		if !isJina {
			host := hostOf(target)
			if siteOf(host) != busySite {
				return htmlPage(thinPage).respond(target)
			}
			if hostCached(n, host) {
				t.Errorf("the origin was asked for %s while %s has a fresh entry", target, host)
			}
			return answerStatus(http.StatusForbidden).respond(target)
		}
		jinaRequests[page]++
		if siteOf(hostOf(page)) != busySite {
			return article.respond(target)
		}
		now := fc.now()
		if len(busyJina) >= blockAnswerNo && now.Before(blockEnds) {
			t.Errorf("a Jina request for %s at %s, inside the block", page, now)
		}
		busyJina = append(busyJina, now)
		if answer, ok := busyAnswers[len(busyJina)]; ok {
			return answer.respond(target)
		}
		return article.respond(target)
	})

	var jobs, busy []*replayJob
	for i := 1; i <= 80; i++ {
		host := "www." + busySite
		if i%2 == 0 {
			host = "m." + busySite
		}
		job := &replayJob{url: fmt.Sprintf("https://%s/story/%d", host, i)}
		jobs, busy = append(jobs, job), append(busy, job)
		if i%8 == 0 {
			jobs = append(jobs, &replayJob{url: fmt.Sprintf("https://blog.example/post/%d", i/8)})
		}
	}

	var (
		cacheBefore         map[string]hostCacheEntry
		busyBefore          int         // the busy site's Jina requests before the fetch
		returns             []time.Time // when pages the block held came back
		lastOther, lastBusy time.Time
	)
	claimed := func(job *replayJob) {
		if job.deferred && !job.runAt.Before(blockEnds) {
			returns = append(returns, fc.now())
		}
		cacheBefore, busyBefore = cacheEntries(n), len(busyJina)
	}
	settled := func(job *replayJob) {
		if siteOf(hostOf(job.url)) != busySite {
			lastOther = fc.now()
			return
		}
		lastBusy = fc.now()
		if job == busy[0] && job.fetches == 1 {
			assert.ErrorIs(t, job.err, errJinaRejected, "the first page fails on its own Jina verdict")
			assertNotDeferred(t, job.err)
			_, permanent := errors.AsType[*PermanentError](job.err)
			assert.False(t, permanent, "the first failure for a host stays retryable")
			assert.NotContains(t, job.err.Error(), "(cached:")
			assert.True(t, hostCached(n, "www."+busySite), "and its host is cached")
		}
		if busyBefore < blockAnswerNo && len(busyJina) >= blockAnswerNo {
			assert.ErrorIs(t, job.err, errJinaSiteBlocked, "the page that met the block")
			requireDeferred(t, job.err, blockEnds)
			assert.Equal(t, cacheBefore, cacheEntries(n), "the block writes no host-cache entry")
		}
		// Within the window's first 14 minutes, every call sent is counted.
		if fc.now().Before(start.Add(healthWindow - time.Minute)) {
			recent := n.JinaHealth().Recent
			assert.Equal(t, len(busyJina)+jinaRequestsElsewhere(jinaRequests, busySite), sumCalls(recent),
				"health counts the requests sent, and nothing for deferrals")
			wantLimited := 0
			if len(busyJina) >= blockAnswerNo {
				wantLimited = 1
			}
			assert.Equal(t, wantLimited, recent[CallRateLimited], "the block's answer, once")
		}
	}
	runQueue(n, fc, jobs, claimed, settled)

	require.GreaterOrEqual(t, len(busyJina), blockAnswerNo)
	for i := 1; i < len(busyJina); i++ {
		assert.GreaterOrEqual(t, busyJina[i].Sub(busyJina[i-1]), testSiteInterval, "the busy site's requests %d and %d", i-1, i)
	}
	require.NotEmpty(t, returns)
	for i := 1; i < len(returns); i++ {
		assert.GreaterOrEqual(t, returns[i].Sub(returns[i-1]), testSiteInterval, "held pages come back spread out")
	}
	assert.Equal(t, 1, logs.count(slog.LevelWarn, "Jina Reader blocks keyless reads of a site, holding its pages"))
	assert.True(t, lastOther.Before(lastBusy), "the other site's pages are done before the busy site's backlog clears")

	fetches := 0
	for _, job := range busy {
		fetches += job.fetches
		if job.err == nil {
			continue
		}
		assert.Positive(t, jinaRequests[job.url], "%s failed with a Jina request of its own", job.url)
		assert.ErrorIs(t, job.err, errJinaRejected, "%s failed on Jina's verdict for it", job.url)
		assert.Equal(t, store.FailureCauseAntiBot, FailureCause(job.err), job.url)
	}
	assert.LessOrEqual(t, fetches, 3*len(busy))
	for _, job := range jobs {
		if siteOf(hostOf(job.url)) != busySite {
			assert.NoError(t, job.err, job.url)
		}
	}
}

// cacheEntries is a copy of n's host-cache entries.
func cacheEntries(n *Native) map[string]hostCacheEntry {
	n.hostCache.mu.RLock()
	defer n.hostCache.mu.RUnlock()
	return maps.Clone(n.hostCache.entries)
}

// sumCalls is how many calls recent counts.
func sumCalls(recent map[CallClass]int) int {
	total := 0
	for _, n := range recent {
		total += n
	}
	return total
}

// jinaRequestsElsewhere is how many of requests, per page, were for pages
// of sites other than site.
func jinaRequestsElsewhere(requests map[string]int, site string) int {
	total := 0
	for page, n := range requests {
		if siteOf(hostOf(page)) != site {
			total += n
		}
	}
	return total
}

// TestReplay_OwnersBlockAnswers feeds the owner's AbuseAlleviationErrors,
// verbatim but for the pages they name, through the classifier: each is a
// site block of the site of the domain it names, until the time it gives,
// capped at a day.
func TestReplay_OwnersBlockAnswers(t *testing.T) {
	const suffix = " (Coordinated Universal Time) due to previous abuse found on https://%s/…: "
	cases := []struct {
		domain, until, why string
		answered           time.Time // when the answer came, where the log says
		site               string
		ends               time.Time
	}{
		{"mobile.twitter.com", "Mon Sep 28 2026 18:48:50 GMT+0000", "DDoS attack suspected: Too many requests",
			time.Date(2026, 9, 28, 17, 50, 10, 0, time.UTC), "twitter.com", time.Date(2026, 9, 28, 18, 48, 50, 0, time.UTC)},
		{"twitter.com", "Mon Sep 28 2026 07:47:34 GMT+0000", "DDoS attack suspected: Too many requests",
			time.Date(2026, 9, 28, 6, 47, 34, 0, time.UTC), "twitter.com", time.Date(2026, 9, 28, 7, 47, 34, 0, time.UTC)},
		{"www.forbes.com", "Mon Sep 28 2026 07:33:33 GMT+0000", "DDoS attack suspected: Too many requests",
			time.Date(2026, 9, 28, 6, 33, 33, 0, time.UTC), "forbes.com", time.Date(2026, 9, 28, 7, 33, 33, 0, time.UTC)},
		{"www.alibaba.com", "Mon Sep 28 2026 08:51:48 GMT+0000",
			"Suspicious action: Request to local network: http://127.0.0.1:4012/?callback=jsonp_1",
			time.Date(2026, 9, 28, 7, 51, 49, 0, time.UTC), "alibaba.com", time.Date(2026, 9, 28, 8, 51, 48, 0, time.UTC)},
		{"x.com", "Mon Sep 28 2026 18:51:10 GMT+0000", "DDoS attack suspected: Too many requests",
			time.Date(2026, 9, 28, 17, 51, 10, 0, time.UTC), "x.com", time.Date(2026, 9, 28, 18, 51, 10, 0, time.UTC)},
		{"www.investing.com", "Fri Dec 30 2039 17:09:06 GMT+0000", "DDoS attack suspected: Too many requests",
			time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC), "investing.com", time.Date(2026, 9, 29, 7, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.domain, func(t *testing.T) {
			reason := "AbuseAlleviationError: Anonymous access to domain " + tc.domain + " blocked until " + tc.until +
				fmt.Sprintf(suffix, tc.domain) + tc.why
			err := jinaStatusError("https://"+tc.domain+"/page", &HTTPStatusError{StatusCode: http.StatusForbidden}, nil, reason)
			require.ErrorIs(t, err, errJinaSiteBlocked)
			sb, ok := errors.AsType[*jinaSiteBlock](err)
			require.True(t, ok)
			assert.Equal(t, tc.site, siteOf(sb.domain))
			assert.Equal(t, tc.ends, siteBlockEnd(sb.until, tc.answered))
		})
	}
}
