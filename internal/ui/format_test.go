package ui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/store"
)

func TestRelTime(t *testing.T) {
	now := time.Date(2026, 9, 28, 20, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		d          time.Duration
		ago, until string
	}{
		{0, "just now", "just now"},
		{59 * time.Second, "just now", "just now"},
		{60 * time.Second, "1 min ago", "in 1 min"},
		{59*time.Minute + 59*time.Second, "59 min ago", "in 59 min"},
		{time.Hour, "1 h ago", "in 1 h"},
		{23*time.Hour + 59*time.Minute, "23 h ago", "in 23 h"},
		{24 * time.Hour, "1 d ago", "in 1 d"},
		{6*24*time.Hour + 23*time.Hour, "6 d ago", "in 6 d"},
	} {
		assert.Equal(t, tc.ago, relTime(now.Add(-tc.d), now), tc.d)
		assert.Equal(t, tc.until, relTime(now.Add(tc.d), now), tc.d)
	}
	week := 7 * 24 * time.Hour
	assert.Equal(t, now.Add(-week).Local().Format("Jan 2, 2006"), relTime(now.Add(-week), now), "a week ago: its date")
	assert.Equal(t, now.Add(week).Local().Format("Jan 2, 2006"), relTime(now.Add(week), now), "a week on: its date")
	assert.Equal(t, "never", relTime(time.Time{}, now))

	// Past about 292 years Sub saturates; a corrupt imported date that far
	// out is still a date, never "just now".
	for _, far := range []time.Time{time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), time.Unix(99999999999, 0),
		time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC)} {
		assert.Equal(t, far.Local().Format("Jan 2, 2006"), relTime(far, now), far)
	}
}

func TestTimeAttributes(t *testing.T) {
	at := time.Date(2026, 9, 28, 20, 30, 5, 0, time.FixedZone("EDT", -4*3600))
	assert.Equal(t, "2026-09-29T00:30:05Z", datetime(at), "RFC 3339, in UTC")

	// A date-only published date is its midnight UTC: the day is UTC's,
	// wherever the daemon runs.
	for _, published := range []time.Time{time.Date(2014, 3, 14, 0, 0, 0, 0, time.UTC),
		time.Date(2014, 3, 14, 7, 0, 0, 0, time.UTC), time.Date(2014, 3, 13, 20, 0, 0, 0, time.FixedZone("EDT", -4*3600))} {
		assert.Equal(t, "Mar 14, 2014", day(published), published)
	}
}

// googleSearch is a stored URL of 700 characters, all but its host and
// path in the query.
var googleSearch = "https://www.google.com/search?ei=43pAX8bxKNK0ggex446ADQ&gs_lcp=" + strings.Repeat("CgZwc3ktYWIQAzIFCCEQoAE6BAgAEEc", 20) +
	"&oq=mobile+view+is+zoomed+in+on+angular+app&q=mobile+view+is+zoomed+in+on+angular+app"

func TestHost(t *testing.T) {
	for u, want := range map[string]string{
		"https://www.example.com/a?b#c":       "www.example.com",
		"http://localhost:8080/x":             "localhost:8080",
		"https://user:pw@h.example/p":         "h.example",
		"javascript:alert(1)":                 "",
		"%zz":                                 "",
		"":                                    "",
		"https://":                            "",
		"mailto:someone@example.com":          "",
		"https://[::1]:8765/ui/":              "[::1]:8765",
		"HTTPS://Example.COM/Path":            "Example.COM",
		"https://xn--bcher-kva.example/books": "xn--bcher-kva.example",
	} {
		assert.Equal(t, want, host(u), u)
	}
}

func TestShortURL(t *testing.T) {
	for u, want := range map[string]string{
		"https://www.alexdebrie.com/posts/dynamodb-single-table/": "www.alexdebrie.com/posts/dynamodb-single-table",
		"https://example.com/":                              "example.com",
		"https://example.com":                               "example.com",
		"https://user:pw@h.example/p":                       "h.example/p",
		"https://h.example/p#section":                       "h.example/p",
		"https://www.youtube.com/watch?v=KhjhRRz3VJw":       "www.youtube.com/watch?v=KhjhRRz3VJw",
		"https://www.youtube.com/watch%5C?v%5C=pqlWNihgdjI": "www.youtube.com/watch%5C?v%5C=pqlWNihgdjI",
		"https://h.example/a%E2%80%AEb?x=%E2%80%AE":         "h.example/a%E2%80%AEb?x=%E2%80%AE",
		"https://h.example/a%7Eb":                           "h.example/a%7Eb",
		"https://h.example/?":                               "h.example",
		googleSearch:                                        strings.TrimPrefix(googleSearch, "https://"),
	} {
		assert.Equal(t, want, shortURL(u), u)
	}
	for _, raw := range []string{"javascript:alert(1)", "%zz", "", "https://", "/ui/documents/x", "mailto:a@example.com"} {
		assert.Equal(t, raw, shortURL(raw), "shown whole")
	}
}

func TestURLTrail(t *testing.T) {
	for u, want := range map[string]string{
		"https://www.alexdebrie.com/posts/dynamodb-single-table/": "posts › dynamodb-single-table",
		"https://example.com/":           "",
		"https://example.com//a///b":     "a › b",
		googleSearch:                     "search",
		"https://h.example/a%E2%80%AE/b": "a%E2%80%AE › b",
		"%zz":                            "",
	} {
		assert.Equal(t, want, urlTrail(u), u)
	}
}

func TestShortError(t *testing.T) {
	for _, tc := range []struct{ stored, want string }{
		{"permanent failure: native: origin blocked the request (likely anti-bot) (cached: jina: refused the target: " +
			"HTTP 403 Forbidden)", "origin blocked the request (likely anti-bot) (cached: jina: refused the target: " +
			"HTTP 403 Forbidden)"},
		{"permanent failure: native: dead link (redirected to another site's landing page: www.theschooloflife.com/articles/): " +
			"dead link (content is gone)", "dead link (redirected to another site's landing page: " +
			"www.theschooloflife.com/articles/): dead link (content is gone)"},
		{"permanent failure: jina: refused the target: HTTP 451 (after native: HTTP 403 Forbidden)",
			"jina: refused the target: HTTP 451 (after native: HTTP 403 Forbidden)"},
		{"permanent failure: youtube: cannot extract video ID from https://www.youtube.com/watch%5C?v%5C=x",
			"cannot extract video ID from https://www.youtube.com/watch%5C?v%5C=x"},
		{"fetch failed: github: HTTP 429 Too Many Requests", "HTTP 429 Too Many Requests"},
		{"permanent failure: native: fetch: dial tcp: lookup x.example: no such host", "dial tcp: lookup x.example: no such host"},
		{"fetch failed: native: fetch: context deadline exceeded", "context deadline exceeded"},
		{"permanent failure: index: embed: input too long", "index: embed: input too long"},
		{"index: ollama unreachable", "index: ollama unreachable"},
		{"permanent failure: no fetcher for ftp://x.example: unsupported", "no fetcher for ftp://x.example: unsupported"},
		{"native: native: twice", "native: twice"},
		{"permanent failure: permanent failure: twice", "permanent failure: twice"},
		{"<script>alert(1)</script>\n\n\tfailed   here ", "<script>alert(1)</script> failed here"},
		{"permanent failure: ", "permanent failure:"},
		{"permanent failure: native: fetch: ", "permanent failure: native: fetch:"},
		{"", ""},
	} {
		assert.Equal(t, tc.want, shortError(tc.stored), tc.stored)
	}

	// A subprocess's stderr, up to 64 KiB of it, multibyte: cut to the
	// cap on a rune boundary.
	long := shortError("permanent failure: web2md: " + strings.Repeat("é", 32<<10))
	assert.True(t, utf8.ValidString(long))
	assert.Equal(t, strings.Repeat("é", maxShortErrorRunes)+"…", long)
	words := shortError(strings.Repeat("stderr line ", 10_000))
	assert.LessOrEqual(t, utf8.RuneCountInString(words), maxShortErrorRunes+1)
	assert.True(t, strings.HasSuffix(words, "line…"), "cut at a word")

	// A long URL early on: the cut falls inside it rather than back at the
	// word before it, which would leave only "Get…".
	url := "https://example.com/" + strings.Repeat("a", 400)
	got := shortError(`fetch failed: native: fetch: Get "` + url + `": context deadline exceeded`)
	assert.Equal(t, `Get "`+url[:maxShortErrorRunes-len(`Get "`)]+"…", got)
}

// TestFetcherNames: shortError drops the prefix each fetcher's errors
// carry, its name.
func TestFetcherNames(t *testing.T) {
	assert.ElementsMatch(t, []string{(*fetcher.Native)(nil).Name(), (*fetcher.GitHub)(nil).Name(),
		(*fetcher.YouTube)(nil).Name(), (*fetcher.Web2MD)(nil).Name()}, fetcherNames)
}

func TestStateNames(t *testing.T) {
	for state, want := range map[string][2]string{
		"pending": {"state-pending", "warn"},
		"fetched": {"state-fetched", "ok"},
		"failed":  {"state-failed", "danger"},
		"dead":    {"state-dead", "neutral"},
		evilAttr:  {"", "neutral"},
		"":        {"", "neutral"},
		"Fetched": {"", "neutral"},
	} {
		assert.Equal(t, want[0], stateClass(state), state)
		assert.Equal(t, want[1], stateTone(state), state)
	}
	for contentType, want := range map[string]string{
		"article": "article", "repo": "repo", "video": "video", "pdf": "pdf", "thread": "thread",
		"unknown": "globe", "": "globe", evilScript: "globe",
	} {
		assert.Equal(t, want, typeIcon(contentType), contentType)
	}
}

func TestNum(t *testing.T) {
	for n, want := range map[int]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000", 7467: "7,467", 100_000: "100,000", 1_234_567: "1,234,567",
		-1: "-1", -1000: "-1,000", -123_456: "-123,456",
	} {
		assert.Equal(t, want, num(n), n)
	}
}

func TestCount(t *testing.T) {
	assert.Equal(t, "0 topics", count(0, "topic", "topics"))
	assert.Equal(t, "1 topic", count(1, "topic", "topics"))
	assert.Equal(t, "2 topics", count(2, "topic", "topics"))
	assert.Equal(t, "2,041 more passages", count(2041, "more passage", "more passages"))
}

func TestPct(t *testing.T) {
	for _, tc := range []struct {
		n, total int
		want     string
	}{
		{0, 10, "0%"}, {-1, 10, "0%"}, {5, 0, "0%"}, {5, -1, "0%"},
		{10, 10, "100%"}, {11, 10, "100%"},
		{4498, 7467, "60%"}, {1, 2, "50%"}, {1, 3, "33%"}, {2, 3, "67%"}, {1, 200, "1%"},
		{1, 201, "<1%"}, {1, 10_000, "<1%"},
		{199, 200, ">99%"}, {9999, 10_000, ">99%"}, {198, 200, "99%"},
	} {
		assert.Equal(t, tc.want, pct(tc.n, tc.total), "%d of %d", tc.n, tc.total)
	}
}

func TestBars(t *testing.T) {
	assert.Empty(t, stateBar(nil), "no documents, no bar")
	assert.Empty(t, stateBar([]Count{{Name: "fetched", Count: 0}, {Name: "failed", Count: -3}}))
	assert.Equal(t, []BarSegment{{Class: "fill-ok", X: "0.000", Width: "100.000"}},
		stateBar([]Count{{Name: "failed", Count: -2}, {Name: "fetched", Count: 5}, {Name: evilAttr, Count: 9}}),
		"one part is the whole bar; a negative part and a state that isn't one draw nothing")

	// Lifecycle order whatever the counts' order, each part starting where
	// the last ended, the last ending at 100.
	segments := stateBar([]Count{{Name: "dead", Count: 1}, {Name: "failed", Count: 1}, {Name: "pending", Count: 1},
		{Name: "fetched", Count: 1}})
	assert.Equal(t, []BarSegment{
		{Class: "fill-warn", X: "0.000", Width: "25.000"},
		{Class: "fill-ok", X: "25.000", Width: "25.000"},
		{Class: "fill-danger", X: "50.000", Width: "25.000"},
		{Class: "fill-neutral", X: "75.000", Width: "25.000"},
	}, segments)

	thirds := stateBar([]Count{{Name: "fetched", Count: 1}, {Name: "failed", Count: 1}, {Name: "dead", Count: 1}})
	require.Len(t, thirds, 3)
	end := 0
	for _, s := range thirds {
		assert.Equal(t, end, milli(t, s.X), "starts where the last ended")
		end += milli(t, s.Width)
	}
	assert.Equal(t, 100_000, end, "ends at 100.000")

}

// TestCoverageBar: a run's documents and those placed since, in four
// parts: members, loose fits and new, each where the last ended, and the
// unsorted left to the track; the legend in the bar's order, the loose
// fits and new only when there are any.
func TestCoverageBar(t *testing.T) {
	run := InterestRun{Documents: 3142, Loose: 191, Unsorted: 1000, New: 58}
	assert.Equal(t, 1951, run.Members())
	assert.Equal(t, 3200, run.CoverageTotal())
	assert.Equal(t, []BarSegment{
		{Class: "fill-accent", X: "0.000", Width: "60.969"},
		{Class: "fill-accent-soft", X: "60.969", Width: "5.969"},
		{Class: "fill-neutral", X: "66.938", Width: "1.812"},
	}, run.Bar(), "the unsorted are the track's 31.25")
	assert.Equal(t, []string{"accent", "accent-soft", "neutral", "track"}, tones(run.Legend()))
	assert.Equal(t, []string{"loose fits", "loose fit"},
		[]string{run.Legend()[1].Label(), CoveragePart{Count: 1, One: "loose fit", Many: "loose fits"}.Label()})

	plain := InterestRun{Documents: 10, Unsorted: 4}
	assert.Equal(t, []BarSegment{{Class: "fill-accent", X: "0.000", Width: "60.000"}}, plain.Bar())
	assert.Equal(t, []string{"accent", "track"}, tones(plain.Legend()), "no loose fit or new document, no legend for them")

	assert.Empty(t, InterestRun{}.Bar(), "nothing grouped, no bar")
	assert.Empty(t, InterestRun{Documents: 10, Unsorted: 10}.Bar(), "all unsorted: the track")
	assert.Zero(t, InterestRun{Documents: 3, Loose: 2, Unsorted: 4}.Members(), "never below none")
}

// tones are the parts' tones, in order.
func tones(parts []CoveragePart) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, p.Tone)
	}
	return out
}

// TestChildBar: an area's interest is drawn against its largest, and a
// largest of none draws nothing.
func TestChildBar(t *testing.T) {
	assert.Equal(t, []BarSegment{{Class: "fill-accent", X: "0.000", Width: "100.000"}}, childBar(86, 86))
	assert.Equal(t, []BarSegment{{Class: "fill-accent", X: "0.000", Width: "50.000"}}, childBar(43, 86))
	for _, tc := range []struct{ size, largest int }{{0, 86}, {5, 0}, {5, -1}} {
		assert.Empty(t, childBar(tc.size, tc.largest), "%d of %d", tc.size, tc.largest)
	}
}

// TestFixedSets: the classes and labels a page picks from a stored value
// come from fixed sets; a value that isn't one gets no class, or reads as
// stored where a label says so.
func TestFixedSets(t *testing.T) {
	for fit, want := range map[string]string{"member": "fit-member", "loose": "fit-loose", "unsorted": "fit-unsorted",
		"new": "fit-new", evilAttr: "", "": ""} {
		assert.Equal(t, want, fitClass(fit), fit)
	}
	for event, want := range map[string]string{"split": "Split", "merged": "Merged", "moved": "Moved",
		"dissolved": "Dissolved", "new": "New", "kept": "Other changes", evilScript: "Other changes"} {
		assert.Equal(t, want, eventLabel(event), event)
	}
	for _, trigger := range []store.RunTrigger{store.RunTriggerFirst, store.RunTriggerAuto, store.RunTriggerManual,
		store.RunTriggerReindex, store.RunTriggerParams, store.RunTriggerShape} {
		assert.NotEqual(t, string(trigger), triggerLabel(string(trigger)), "%s has words", trigger)
	}
	assert.Equal(t, evilScript, triggerLabel(evilScript), "one it doesn't know, as stored")
	assert.Equal(t, "asked for", freshReason(string(store.FreshManual)))
	assert.Equal(t, "the grouping's parameters changed", freshReason("params"))
	assert.Equal(t, evilQuotes, freshReason(evilQuotes))
	at := time.Date(2026, 10, 9, 23, 30, 0, 0, time.UTC)
	assert.Equal(t, at.Local().Format("Jan 2, 2006"), localDay(at), "the daemon's day")
}

// TestCauseBar: a cause's bar is scaled to the cause with the most, dead
// links drawn neutral like the dead state, every other cause danger.
func TestCauseBar(t *testing.T) {
	assert.Equal(t, []BarSegment{{Class: "fill-danger", X: "0.000", Width: "100.000"}}, causeBar("anti_bot", 926, 926))
	assert.Equal(t, []BarSegment{{Class: "fill-neutral", X: "0.000", Width: "88.445"}}, causeBar("dead_link", 819, 926))
	assert.Equal(t, []BarSegment{{Class: "fill-danger", X: "0.000", Width: "26.350"}}, causeBar(evilAttr, 244, 926),
		"a cause this build has no words for")
	assert.Equal(t, []BarSegment{{Class: "fill-danger", X: "0.000", Width: "100.000"}}, causeBar("tls", 5, 3),
		"never past the bar's end")
	for _, tc := range []struct{ n, most int }{{0, 926}, {-1, 926}, {5, 0}, {5, -1}} {
		assert.Empty(t, causeBar("anti_bot", tc.n, tc.most), "%d of %d", tc.n, tc.most)
	}
}

// TestInterestName: a label is cut at its first " and " only when the
// words before it are at least two.
func TestInterestName(t *testing.T) {
	for label, want := range map[string]string{
		"Mobile Ecosystems and Strategy":  "Mobile Ecosystems",
		"Job Search and Interviewing":     "Job Search",
		"Java GUI Focus and Events":       "Java GUI Focus",
		"Identity and Access Management":  "Identity and Access Management",
		"Economic and Social Instability": "Economic and Social Instability",
		"Rock and Roll":                   "Rock and Roll",
		"A and B and C":                   "A and B and C",
		"AWS Serverless Architecture":     "AWS Serverless Architecture",
		"":                                "",
		evilScript + " " + evilAttr:       evilScript + " " + evilAttr,
		"Research andDevelopment":         "Research andDevelopment",
	} {
		assert.Equal(t, want, interestName(label), label)
	}
}

// milli reads a BarSegment's attribute back in thousandths.
func milli(t *testing.T, decimal string) int {
	t.Helper()
	whole, frac, ok := strings.Cut(decimal, ".")
	require.True(t, ok, decimal)
	require.Len(t, frac, 3, decimal)
	n := 0
	for _, c := range whole + frac {
		require.True(t, c >= '0' && c <= '9', decimal)
		n = n*10 + int(c-'0')
	}
	return n
}
