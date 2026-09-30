package setup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/store"
)

// What an import's estimates are made of. Fetching is network-bound and
// indexing Ollama-bound, so each has its own range: fetching from a rate
// seen with a full pool, indexing from a rate measured on this Mac.
const (
	// fetchPerMinuteLow and fetchPerMinuteHigh are the pages a minute a
	// pool of fullFetchPool fetches, taking at most perHostInFlight at
	// once from any one site.
	fetchPerMinuteLow  = 25
	fetchPerMinuteHigh = 45
	// fullFetchPool is the pool size those rates were seen with; a smaller
	// fetch limit scales them down.
	fullFetchPool = 16
	// perHostInFlight is how many pages curio fetches from one site at a
	// time: the fetcher's originRequestsPerHost.
	perHostInFlight = 2
	// chunksPerPage is how many chunks a page makes on average.
	chunksPerPage = 23
	// indexLowFactor and indexHighFactor bound indexing around the
	// measured rate: pages vary in length.
	indexLowFactor  = 0.7
	indexHighFactor = 1.5
	// hostFloor is the fewest new pages a host needs before a warning
	// names it, and namedHosts how many it names at most.
	hostFloor  = 100
	namedHosts = 3
	// sampleChunkLen and sampleChunkSpread size the synthetic chunks the
	// index rate is measured with: 2,300 to 2,500 characters, about a
	// full chunk of prose.
	sampleChunkLen    = 2300
	sampleChunkSpread = 200
)

// githubHost is the host the GitHub fetcher takes, through GitHub's API.
const githubHost = "github.com"

// gitHubPagesPerHour is how many github.com pages GitHub's API serves an
// hour without a token: 60 requests, about 2 a page.
const gitHubPagesPerHour = 30

// gitHubWait bounds how long n github.com pages take without a token: the
// hourly limit serves gitHubPagesPerHour of them, and the rest wait for the
// next hour's.
func gitHubWait(n int) time.Duration {
	return time.Duration((n+gitHubPagesPerHour-1)/gitHubPagesPerHour) * time.Hour
}

// Estimate is how long an import's new pages take to fetch and to index.
type Estimate struct {
	// Pages is how many new pages there are.
	Pages int
	// FetchLo and FetchHi bound the fetching, rounded up to the minute.
	FetchLo, FetchHi time.Duration
	// IndexLo and IndexHi bound the indexing, rounded up to the minute;
	// zero when the index rate is unknown, and IndexUnknown says why.
	IndexLo, IndexHi time.Duration
	IndexUnknown     string
}

// estimateImport is the time pages new pages take with fetchLimit fetches
// at once, and rate chunks a second of indexing (0 when unknown, and
// unknown says why).
func estimateImport(pages, fetchLimit int, rate float64, unknown string) Estimate {
	scale := float64(min(fetchLimit, fullFetchPool)) / fullFetchPool
	e := Estimate{
		Pages:   pages,
		FetchLo: minutesUp(float64(pages) / fetchPerMinuteHigh / scale),
		FetchHi: minutesUp(float64(pages) / fetchPerMinuteLow / scale),
	}
	if rate <= 0 {
		e.IndexUnknown = cmp.Or(unknown, "the index rate wasn't measured")
		return e
	}
	seconds := float64(pages) * chunksPerPage / rate
	e.IndexLo = minutesUp(seconds * indexLowFactor / 60)
	e.IndexHi = minutesUp(seconds * indexHighFactor / 60)
	return e
}

// minutesUp is m minutes rounded up to a whole minute.
func minutesUp(m float64) time.Duration {
	return time.Duration(math.Ceil(m)) * time.Minute
}

// Work is how long the whole import takes at most: its slower half, or
// the fetching alone when the indexing is unknown.
func (e Estimate) Work() time.Duration { return max(e.FetchHi, e.IndexHi) }

// Fetching is the fetch range: "4h35m–8h14m".
func (e Estimate) Fetching() string { return durationRange(e.FetchLo, e.FetchHi) }

// Indexing is the index range, or why it is unknown.
func (e Estimate) Indexing() string {
	if e.IndexHi == 0 {
		return "unknown (" + e.IndexUnknown + ")"
	}
	return durationRange(e.IndexLo, e.IndexHi)
}

// durationRange is lo to hi, or about lo when they meet.
func durationRange(lo, hi time.Duration) string {
	if lo == hi {
		return "about " + formatDuration(lo)
	}
	return formatDuration(lo) + "–" + formatDuration(hi)
}

// formatDuration writes whole minutes as hours and minutes: "4h35m",
// "45m", "3h".
func formatDuration(d time.Duration) string {
	m := int(d / time.Minute)
	h, m := m/60, m%60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// finishAt is when work that starts at now is done, working only inside
// window when it has one: a queue kept to 22:00-07:00 works through the
// nights, and waits out the days.
func finishAt(now time.Time, work time.Duration, window store.DailyWindow) time.Time {
	if window.IsZero() || !window.Valid() {
		return now.Add(work)
	}
	t, left := now, work
	for left > 0 {
		if !window.Contains(t) {
			t = window.NextStart(t)
			continue
		}
		end := windowEnd(t, window)
		if span := end.Sub(t); left > span {
			t, left = end, left-span
			continue
		}
		return t.Add(left)
	}
	return t
}

// windowEnd is the first time after t, in t's location, at which window
// closes.
func windowEnd(t time.Time, window store.DailyWindow) time.Time {
	y, m, d := t.Date()
	end := time.Date(y, m, d, window.End/60, window.End%60, 0, 0, t.Location())
	if !end.After(t) {
		end = time.Date(y, m, d+1, window.End/60, window.End%60, 0, 0, t.Location())
	}
	return end
}

// CheckBackText is when at is, as seen from now: "15:04" today, "tomorrow
// 15:04", or "Mon 2 Jan 15:04".
func CheckBackText(now, at time.Time) string {
	at = at.In(now.Location())
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, now.Location()); {
	case day.Equal(today):
		return at.Format("15:04")
	case day.Equal(today.AddDate(0, 0, 1)):
		return "tomorrow " + at.Format("15:04")
	}
	return at.Format("Mon 2 Jan 15:04")
}

// HostCount is a host and how many new pages are on it.
type HostCount struct {
	Host  string
	Pages int
}

// dominantHosts are the hosts, github.com aside, that hold more than
// perHostInFlight/fullPool of urls and at least hostFloor of them, largest
// first, namedHosts at most: with a full pool of fullPool fetches, curio
// fetches no more than perHostInFlight at once from any of them, so they
// take longer than the estimate. Hosts are compared as the fetcher's
// per-host gate compares them, lowercased.
func dominantHosts(urls []string, fullPool int) []HostCount {
	by := map[string]int{}
	for _, raw := range urls {
		if host := hostOf(raw); host != "" && host != githubHost {
			by[host]++
		}
	}
	share := float64(perHostInFlight) / float64(max(fullPool, 1)) * float64(len(urls))
	var out []HostCount
	for host, n := range by {
		if n >= hostFloor && float64(n) > share {
			out = append(out, HostCount{Host: host, Pages: n})
		}
	}
	slices.SortFunc(out, func(a, b HostCount) int {
		return cmp.Or(cmp.Compare(b.Pages, a.Pages), strings.Compare(a.Host, b.Host))
	})
	return out[:min(len(out), namedHosts)]
}

// countHost is how many of urls are on host.
func countHost(urls []string, host string) int {
	n := 0
	for _, raw := range urls {
		if hostOf(raw) == host {
			n++
		}
	}
	return n
}

// hostOf is raw's hostname, lowercased; empty when raw doesn't parse.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// measureIndexRate is how many chunks a second emb embeds on this Mac:
// one batch of indexer.EmbedBatchSize synthetic chunks, prefixed as the
// daemon prefixes documents, to load the model, then one more, timed with
// now. An embed that fails, or a timing of no time, is an error: the
// estimate says the rate is unknown.
func measureIndexRate(ctx context.Context, emb embedder.Embedder, prefix string, now func() time.Time) (float64, error) {
	if _, err := emb.Embed(ctx, sampleChunks(prefix, 0)); err != nil {
		return 0, err
	}
	start := now()
	if _, err := emb.Embed(ctx, sampleChunks(prefix, indexer.EmbedBatchSize)); err != nil {
		return 0, err
	}
	elapsed := now().Sub(start)
	if elapsed <= 0 {
		return 0, errors.New("the embed took no measurable time")
	}
	return indexer.EmbedBatchSize / elapsed.Seconds(), nil
}

// sampleWords are what the synthetic chunks are made of: prose-like, with
// no two chunks alike, so nothing is served from a cache.
var sampleWords = strings.Fields(`the index turns each page into passages of prose and embeds them so a
search finds what a page means as well as the words it uses a library of
bookmarks holds articles essays papers talks and notes about programming
design science history and whatever else caught the reader's eye`)

// sampleChunks is one batch of synthetic chunks, each 2,300 to 2,500
// characters and prefixed with prefix, the first numbered from.
func sampleChunks(prefix string, from int) []string {
	out := make([]string, indexer.EmbedBatchSize)
	for i := range out {
		n := from + i
		want := sampleChunkLen + (n*37)%sampleChunkSpread
		var b strings.Builder
		b.WriteString(prefix)
		for w := n; b.Len() < want; w += 7 {
			fmt.Fprintf(&b, "%s%d ", sampleWords[w%len(sampleWords)], w%97)
		}
		out[i] = b.String()[:want]
	}
	return out
}
