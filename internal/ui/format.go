package ui

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/samsar/curio/internal/store"
)

// The display transforms the templates show stored values through. Each
// returns a plain string, or a struct of them, which html/template escapes
// like any other value: none returns a trusted type. A stored value is
// hostile and can be any length: a list shows it on one line, cut by CSS,
// with the whole value in a title attribute.

// ago is when t is, relative to now, as a list shows a time: "13 min ago",
// "in 3 h", a date from a week on. See relTime.
func ago(t time.Time) string { return relTime(t, time.Now()) }

// relTime is t relative to now in whole units, floored: "just now" within
// a minute either way, then minutes, hours and days, "ago" or "in", and
// the daemon-local date from a week on; "never" for the zero time.
func relTime(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	// Sub saturates past about 292 years, and negating the saturated
	// minimum stays negative, so the future is measured from now.
	future := t.After(now)
	d := now.Sub(t)
	if future {
		d = t.Sub(now)
	}
	var n int
	var unit string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n, unit = int(d/time.Minute), "min"
	case d < 24*time.Hour:
		n, unit = int(d/time.Hour), "h"
	case d < 7*24*time.Hour:
		n, unit = int(d/(24*time.Hour)), "d"
	default:
		return t.Local().Format("Jan 2, 2006")
	}
	if future {
		return fmt.Sprintf("in %d %s", n, unit)
	}
	return fmt.Sprintf("%d %s ago", n, unit)
}

// datetime is t for a <time> element's datetime attribute: RFC 3339, in
// UTC.
func datetime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// day is t's date in UTC. A published date is a day, stored as its
// midnight UTC, which the daemon's clock would show as the evening before
// anywhere west of UTC.
func day(t time.Time) string { return t.UTC().Format("Jan 2, 2006") }

// Host is u's host, with its port and without userinfo, or "" when u has
// none or doesn't parse: how the dashboard, and the interest map's
// documents, name a document's site.
func Host(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// shortURL is u as a list names an untitled document: its host, its path
// without the trailing slash, and its query, which often is what tells
// two pages apart (a search's, a video's). It drops the scheme, userinfo
// and fragment, and shows percent-escapes as stored, never decoded. A URL
// without a host, or one that doesn't parse, is shown whole.
func shortURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return u
	}
	out := parsed.Host + strings.TrimSuffix(parsed.EscapedPath(), "/")
	if parsed.RawQuery != "" {
		out += "?" + parsed.RawQuery
	}
	return out
}

// urlTrail is where under its host u leads: its path's segments, escaped
// as stored, joined by " › ", without the query. "" for a URL without a
// path or one that doesn't parse.
func urlTrail(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	var segments []string
	for s := range strings.SplitSeq(parsed.EscapedPath(), "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}
	return strings.Join(segments, " › ")
}

// interestName is an interest's label as a line of them names it: the
// words before its first " and " when there are at least two of them, or
// the whole label. "Mobile Ecosystems and Strategy" is "Mobile
// Ecosystems", but "Identity and Access Management" stays whole, which one
// word, "Identity", wouldn't name.
func interestName(label string) string {
	head, _, found := strings.Cut(label, " and ")
	if found && len(strings.Fields(head)) >= 2 {
		return head
	}
	return label
}

// maxShortErrorRunes is how much of an error a list shows: stored errors
// run to thousands of characters, a subprocess's stderr to 64 KiB.
const maxShortErrorRunes = 300

// The prefixes a stored error starts with that say nothing about the
// document: the worker's wrappers, then a fetcher's name (its Name()
// value), then native's "fetch: ". "jina: " stays, since it says Jina
// Reader gave the verdict, and so does "index: ", which says the fetch
// worked.
var (
	errorWrappers = []string{"permanent failure: ", "fetch failed: "}
	fetcherNames  = []string{"native", "github", "youtube", "web2md"}
)

// shortError is a stored error as a list shows it: its prefixes that say
// nothing about the document dropped (at most one of each kind), its
// whitespace collapsed, and cut to maxShortErrorRunes. An error that is
// nothing but prefixes is shown whole.
func shortError(s string) string {
	msg := trimErrorPrefixes(s)
	if strings.TrimSpace(msg) == "" {
		msg = s
	}
	return Excerpt(msg, maxShortErrorRunes)
}

func trimErrorPrefixes(s string) string {
	for _, p := range errorWrappers {
		if rest, ok := strings.CutPrefix(s, p); ok {
			s = rest
			break
		}
	}
	for _, name := range fetcherNames {
		if rest, ok := strings.CutPrefix(s, name+": "); ok {
			s = rest
			break
		}
	}
	s, _ = strings.CutPrefix(s, "fetch: ")
	return s
}

// stateClass is the badge class of a document state, or "" for a value
// that isn't one: a class never comes from a stored string.
func stateClass(state string) string {
	switch store.DocState(state) {
	case store.DocStatePending, store.DocStateFetched, store.DocStateFailed, store.DocStateDead:
		return "state-" + state
	}
	return ""
}

// stateTone is the tone a document state is drawn in, for bar fills and
// legend swatches: ok, warn, danger or neutral.
func stateTone(state string) string {
	switch store.DocState(state) {
	case store.DocStateFetched:
		return "ok"
	case store.DocStatePending:
		return "warn"
	case store.DocStateFailed:
		return "danger"
	case store.DocStateDead:
	}
	return "neutral"
}

// typeIcon is the icon of a content type: a globe for unknown, or for a
// value that isn't one.
func typeIcon(contentType string) string {
	switch store.ContentType(contentType) {
	case store.ContentTypeArticle:
		return "article"
	case store.ContentTypeRepo:
		return "repo"
	case store.ContentTypeVideo:
		return "video"
	case store.ContentTypePDF:
		return "pdf"
	case store.ContentTypeThread:
		return "thread"
	case store.ContentTypeUnknown:
	}
	return "globe"
}

// num is n with its thousands grouped: 7,467.
func num(n int) string {
	digits := strconv.Itoa(n)
	sign := ""
	if n < 0 {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	b.WriteString(sign)
	for i := range len(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(digits[i])
	}
	return b.String()
}

// count is n, grouped as num groups it, and the noun for it: one for 1,
// many for any other n ("1 topic", "2,041 topics", "0 jobs").
func count(n int, one, many string) string {
	if n == 1 {
		return num(n) + " " + one
	}
	return num(n) + " " + many
}

// pct is n's share of total as a whole percent, rounded, but never a
// share that isn't there: "<1%" for a sliver, ">99%" for all but one.
func pct(n, total int) string {
	switch {
	case n <= 0 || total <= 0:
		return "0%"
	case n >= total:
		return "100%"
	}
	p := (200*n + total) / (2 * total) // n/total of 100, rounded half up
	switch p {
	case 0:
		return "<1%"
	case 100:
		return ">99%"
	}
	return strconv.Itoa(p) + "%"
}

// BarSegment is one part of a stacked bar in its 0-100 viewBox: where it
// starts and how wide it is, as SVG attributes, and its fill's class.
type BarSegment struct {
	Class, X, Width string
}

// barPart is a part of a stacked bar before it is drawn.
type barPart struct {
	value int
	class string
}

// barSegments draws parts, in order, as shares of total, each starting
// where the one before ended. It works in thousandths of the viewBox, so
// that a segment ends exactly where the next starts and the bar at most
// at 100.000. A part of 0 or less draws nothing, and a total of 0 or less
// no bar.
func barSegments(parts []barPart, total int) []BarSegment {
	if total <= 0 {
		return nil
	}
	var segments []BarSegment
	sum := 0
	for _, p := range parts {
		if p.value <= 0 {
			continue
		}
		start := thousandths(sum, total)
		sum = min(sum+p.value, total)
		segments = append(segments, BarSegment{Class: p.class, X: decimal3(start),
			Width: decimal3(thousandths(sum, total) - start)})
	}
	return segments
}

// thousandths is n/total of 100, in thousandths, rounded.
func thousandths(n, total int) int { return (100_000*n + total/2) / total }

// decimal3 writes thousandths m as a decimal: 12345 is "12.345".
func decimal3(m int) string { return fmt.Sprintf("%d.%03d", m/1000, m%1000) }

// stateBar draws the library's documents by state, in lifecycle order:
// counts of states that aren't one are left out, and a negative count
// is none.
func stateBar(counts []Count) []BarSegment {
	byState := make(map[string]int, len(counts))
	for _, c := range counts {
		byState[c.Name] += max(c.Count, 0)
	}
	parts := make([]barPart, 0, len(docStates))
	total := 0
	for _, state := range docStates {
		parts = append(parts, barPart{value: byState[state], class: "fill-" + stateTone(state)})
		total += byState[state]
	}
	return barSegments(parts, total)
}

// causeBar draws the n documents that failed for cause as a bar scaled to
// most, the count of the cause with the most: neutral for dead links, as
// the dead state is drawn, and danger for every other cause.
func causeBar(cause string, n, most int) []BarSegment {
	class := "fill-danger"
	if store.FailureCause(cause) == store.FailureCauseDeadLink {
		class = "fill-neutral"
	}
	return barSegments([]barPart{{value: n, class: class}}, most)
}

// coverageBar draws a run's coverage parts, in order, as shares of total,
// each filled in its tone, but for the track's own: the part the bar
// leaves over.
func coverageBar(parts []CoveragePart, total int) []BarSegment {
	bar := make([]barPart, 0, len(parts))
	for _, p := range parts {
		if p.Tone != toneTrack {
			bar = append(bar, barPart{value: p.Count, class: "fill-" + p.Tone})
		}
	}
	return barSegments(bar, total)
}

// childBar draws an area's interest of size documents as a bar scaled to
// largest, its largest interest's size: none when either is 0 or less.
func childBar(size, largest int) []BarSegment {
	return barSegments([]barPart{{value: size, class: "fill-accent"}}, largest)
}

// fitClass is the class of a document of fit in the interests (a member,
// a loose fit, unsorted, or placed since the rebuild), or "" for a value
// that isn't one: a class never comes from a stored string.
func fitClass(fit string) string {
	switch fit {
	case fitMember, fitLoose, fitUnsorted, fitNew:
		return "fit-" + fit
	}
	return ""
}

// eventLabel names a kind of rebuild event, as the changes page heads it;
// one this build doesn't know is the other changes.
func eventLabel(event string) string {
	switch event {
	case eventSplit:
		return "Split"
	case eventMerged:
		return "Merged"
	case eventMoved:
		return "Moved"
	case eventDissolved:
		return "Dissolved"
	case eventNew:
		return "New"
	}
	return "Other changes"
}

// triggerLabel says what started a rebuild (store.RunTrigger), for people;
// a value it doesn't know is shown as stored.
func triggerLabel(trigger string) string {
	switch store.RunTrigger(trigger) {
	case store.RunTriggerFirst:
		return "the library's first grouping"
	case store.RunTriggerAuto:
		return "automatic"
	case store.RunTriggerManual:
		return "asked for"
	case store.RunTriggerReindex:
		return "after a re-embedding"
	case store.RunTriggerParams:
		return "after the grouping's parameters changed"
	case store.RunTriggerShape:
		return "after the library's shape changed"
	}
	return trigger
}

// freshReason says why a fresh rebuild is owed, as curio status does; a
// value it doesn't know is shown as stored.
func freshReason(owed string) string {
	switch owed {
	case string(store.FreshManual):
		return "asked for"
	case freshParams:
		return "the grouping's parameters changed"
	}
	return owed
}

// freshParams is the fresh rebuild owed when the grouping's parameters
// changed (insight.FreshParams), which nothing stores.
const freshParams = "params"

// localDay is t's date on the daemon's clock: when a rebuild ran.
func localDay(t time.Time) string { return t.Local().Format("Jan 2, 2006") }

// throttleLabel is a throttle's name on its button.
func throttleLabel(t store.Throttle) string {
	switch t {
	case store.ThrottleNormal:
		return "Normal"
	case store.ThrottleGentle:
		return "Gentle"
	}
	return string(t)
}

// gentleHint says what the gentle throttle holds each pool to, from its
// caps.
func gentleHint() string {
	fetches, _ := store.ThrottleGentle.Cap(store.JobKindFetch)
	indexes, _ := store.ThrottleGentle.Cap(store.JobKindIndex)
	return "Gentle: at most " + count(fetches, "fetch", "fetches") + " and " + count(indexes, "index", "indexes") +
		" at a time."
}

// queueReason is why a closed queue holds its jobs, for people: from the
// gate's reasons, as GET /v1/queue reports them.
func queueReason(reason string) string {
	switch reason {
	case "paused":
		return "Paused"
	case "outside_schedule":
		return "Outside its schedule"
	}
	return "Closed"
}

// jobKindLabel is a job's kind for people: what its work is.
func jobKindLabel(kind string) string {
	switch store.JobKind(kind) {
	case store.JobKindFetch:
		return "Fetch"
	case store.JobKindIndex:
		return "Index"
	case store.JobKindImport:
		return "Import"
	case store.JobKindCluster:
		return "Rebuild"
	case store.JobKindSummarize:
		return "Summary"
	}
	return "Job"
}

// viaLabel is what served an extraction's text (its extraction_meta via)
// for people; a value it doesn't know is shown as stored.
func viaLabel(via string) string {
	switch via {
	case "readability":
		return "Readability"
	case "jina":
		return "Jina Reader"
	case "pdf-local":
		return "local PDF extraction"
	case "github-api":
		return "GitHub's API"
	}
	return via
}
