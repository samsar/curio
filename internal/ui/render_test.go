package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"iter"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui/uitest"
)

// Hostile values for the samples' strings: markup, an attribute breakout,
// a script URL, and quotes and angle brackets.
const (
	evilScript = `<script>alert(1)</script>`
	evilAttr   = `" onerror="alert(1)`
	evilURL    = `javascript:alert(1)`
	evilQuotes = `'"<>&`
)

func newRenderer(t testing.TB) *Renderer {
	t.Helper()
	r, err := New()
	require.NoError(t, err)
	return r
}

// samples are a typed view model for every page, each with hostile values
// in its strings and every optional part set, so every branch that shows
// stored content runs.
func samples(t testing.TB, r *Renderer) map[string]any {
	t.Helper()
	at := time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)
	layout := func(nav Nav) Layout {
		return Layout{Title: evilScript, Nav: nav, Version: evilQuotes, Listen: sampleListen}
	}
	panelErr := &PanelError{Message: evilScript, RequestID: evilAttr}
	text, err := r.RenderMarkdown([]byte("# "+evilScript+"\n\n<img src=x onerror=alert(1)> [x]("+evilURL+
		") ![alt "+evilAttr+"](https://img.example/a.png)\n"), "https://example.com/post", false)
	require.NoError(t, err)
	member := Member{DocumentID: evilAttr, Title: evilScript, URL: evilURL, Similarity: 0.8}
	interest := Interest{ID: evilAttr, Label: evilScript, Summary: evilQuotes, Size: 7, Cohesion: 0.7,
		Members: []Member{member, {DocumentID: "doc", URL: "https://example.com/" + evilQuotes},
			{DocumentID: evilScript, BookmarkTitle: evilAttr + evilScript, URL: evilURL}}}

	upstream := func(name, state string, enabled bool) Upstream {
		return Upstream{Name: name, Enabled: enabled, State: state, LastSuccess: at, LastFailure: at,
			LastFailureClass: evilQuotes, CooldownUntil: at, Window: 15 * time.Minute, Calls: 20, Failed: 6}
	}

	return map[string]any{
		PageStatus: Status{
			Layout: layout(NavStatus),
			Counts: &CountsPanel{Documents: 3, Bookmarks: 4,
				ByState: []Count{{Name: evilAttr, Count: 1}, {Name: "fetched", Count: 2}},
				Jobs:    []Count{{Name: evilScript, Count: 5}}},
			Queue: &QueuePanel{Open: false, Paused: true, Reason: evilScript, OpensAt: at, Throttle: evilQuotes,
				Schedule: evilAttr, Kinds: []KindLoad{{Kind: evilScript, Running: 1, Limit: 4, Pending: 9, Finished: 3}},
				FinishedIn: ProgressWindow, KeepAwake: true, KeepAwakeActive: true, PowerSource: evilQuotes},
			Progress: &ProgressPanel{Progress: EstimateProgress([]KindWork{{Kind: "fetch", Pending: 30, Finished: 12}},
				ProgressWindow, true), Reason: evilScript},
			Health: &HealthPanel{Version: evilQuotes, OllamaDetail: evilScript, EmbeddingModel: evilAttr,
				EmbeddingDim: 1024, GenerationModel: evilScript, YouTubeFetcher: evilAttr,
				Drift: &Drift{Changes: []DriftChange{{What: evilScript, Recorded: evilAttr, Current: evilURL}},
					Verified: true, Detail: evilScript, Fix: evilQuotes},
				Upstreams: []Upstream{upstream(evilScript, "failing", true), upstream("jina", "degraded", true),
					upstream(evilAttr, "paused", true), upstream("jina", "failing", false),
					upstream(evilQuotes, evilAttr, true), {Name: "jina", Enabled: true, State: "failing"}}},
			Failures: &FailuresPanel{Total: 17, Causes: []Count{{Name: evilScript, Count: 9},
				{Name: string(store.FailureCauseAntiBot), Count: 5}, {Name: string(store.FailureCauseDeadLink), Count: 3}}},
		},
		PageSearch: Search{
			Layout: layout(NavSearch),
			Query:  evilAttr,
			Type:   evilAttr,
			Page:   2,
			Err:    panelErr,
			Results: &SearchResults{Degraded: true, Warnings: []string{evilScript}, TookMS: 12, Total: 37,
				Hits: sampleHits()},
		},
		PageLibrary: Library{
			Layout: layout(NavLibrary),
			Filters: LibraryFilters{State: evilAttr, ContentType: evilScript, Host: evilQuotes, Folder: evilURL,
				Cause: evilScript, Limit: 7},
			Counts: &LibraryCounts{Documents: 3, Bookmarks: 4, ByState: map[string]int{evilAttr: 1}},
			Shown:  50,
			Rows: []LibraryRow{
				{DocumentID: evilAttr, Title: evilScript, URL: evilURL, State: evilQuotes, ContentType: evilAttr,
					When: at, LastError: evilScript, FailureCause: evilAttr},
				{DocumentID: "doc", URL: "https://example.com/" + evilQuotes, State: "failed", ContentType: "pdf",
					When: at, LastError: "permanent failure: native: " + evilScript, FailureCause: "anti_bot"},
				{DocumentID: evilAttr, BookmarkTitle: evilScript, URL: evilURL, State: "failed", ContentType: "unknown",
					When: at, LastError: evilAttr, FailureCause: "tls"},
			},
			NextCursor: evilAttr,
			PageSize:   7,
		},
		PageFailures: Failures{
			Layout: layout(NavLibrary),
			Counts: &LibraryCounts{Documents: 7467, Bookmarks: 7497, ByState: map[string]int{"failed": 930, "dead": 820}},
			Total:  1750,
			Groups: []FailureGroup{
				{Cause: "anti_bot", Count: 926, Hosts: []Count{{Name: evilScript, Count: 169}, {Name: evilAttr + evilQuotes,
					Count: 48}, {Name: strings.Repeat("h", 300) + ".example", Count: 5}}},
				{Cause: "dead_link", Count: 820, Hosts: []Count{{Name: evilAttr, Count: 51}}},
				{Cause: evilScript, Count: 3, Hosts: []Count{{Name: evilQuotes, Count: 3}}},
				{Cause: "rate_limited", Count: 1},
			},
		},
		PageDocument: Document{
			Layout: layout(NavLibrary),
			Meta: DocumentMeta{ID: evilAttr, Title: evilScript, BookmarkTitle: evilAttr, URL: evilURL, CanonicalURL: evilURL,
				ContentType: evilQuotes, State: evilAttr, Author: evilScript, Language: evilQuotes, PublishedAt: at,
				WordCount: 42, CreatedAt: at, UpdatedAt: at, MarkdownPath: evilScript},
			Extraction: &Extraction{Fetcher: evilScript, Via: "jina", Status: evilAttr, ErrorMessage: evilQuotes,
				FetchedAt: at},
			LastError:    evilScript,
			FailureCause: evilAttr,
			Text: TextPanel{State: TextShown, Text: text, Truncated: true, MarkdownPath: evilAttr,
				OfferImages: true},
			Related: RelatedPanel{Docs: []RelatedDoc{{DocumentID: evilAttr, Title: evilScript, URL: evilURL, Score: 0.9},
				{DocumentID: evilQuotes, BookmarkTitle: evilScript, URL: evilURL, Score: 0.8},
				{DocumentID: "doc", URL: "https://example.com/" + strings.Repeat("y", 700) + evilAttr, Score: 0.7}}},
			Bookmarks: BookmarksPanel{Bookmarks: []DocumentBookmark{{Source: evilScript, Folder: evilAttr,
				Title: evilQuotes, Tags: []string{evilScript, evilURL}, SavedAt: at}}},
			Jobs: DocumentJobs{DocumentID: evilAttr, State: evilAttr, Baseline: DocumentBaseline{Updated: at,
				Extraction: evilScript}, Current: DocumentBaseline{Updated: at, Extraction: evilAttr},
				Jobs: []JobLine{{Kind: evilScript, Running: true, Attempts: 2},
					{Kind: evilAttr, Waiting: true, Attempts: 1, RunAfter: at, LastError: evilScript + evilQuotes},
					{Kind: "fetch", Waiting: true, RunAfter: at, LastError: evilAttr}},
				AttemptLimit: 5, Hold: evilScript},
		},
		PageInterests: Interests{
			Layout: layout(NavInterests),
			Page:   2, RunChanged: true,
			Run: &InterestRun{ID: evilAttr + evilScript, ComputedAt: at, Algo: evilScript, Documents: 400, Noise: 3,
				Interests: 60},
			Interests: []Interest{interest, {ID: "unlabeled", Size: 1}},
			Rebuild: Rebuild{Enabled: true, Running: true, StartedAt: at, Shown: evilAttr, NewRun: evilScript,
				RunError: evilScript},
		},
		PageInterest: InterestPage{Layout: layout(NavInterests), Interest: interest, RunAt: at},
		PageError: ErrorPage{Layout: layout(NavNone), Status: http.StatusBadRequest, Title: evilScript,
			Message: evilAttr, RequestID: evilQuotes, Retry: NavLibrary},
		PageStarting: Starting{Layout: layout(NavNone), Phase: evilScript, Migrating: true, Applied: 1, Total: 6},
	}
}

// sampleHits are search results with hostile values everywhere: titled,
// named by a bookmark, named by an address alone, and a long title and an
// unbroken address; with passages BM25 found, the vector search found, both
// found, and hostile markup in snippets and text.
func sampleHits() []SearchHit {
	return []SearchHit{{
		DocumentID: evilAttr, Title: evilScript, URL: evilURL, ContentType: evilAttr, Score: 0.03,
		Matches: []Match{
			{Segments: Passage("before <em>"+evilScript+"</em> after "+evilAttr, ""), BM25: new(1.5)},
			{Segments: Passage("", "[x]("+evilURL+") "+evilQuotes+evilScript), Vector: new(0.7)},
			{Segments: Passage("<em>"+evilAttr+"</em>-<em>third</em> <a href=x>"+evilQuotes+"</a>", ""), BM25: new(0.5),
				Vector: new(0.2)},
		},
	}, {
		DocumentID: "doc", URL: "https://example.com/" + evilQuotes + "/a/b?q=" + evilScript, ContentType: "video",
		BookmarkTitle: evilScript + evilAttr,
	}, {
		DocumentID: evilQuotes, URL: "https://example.com/" + strings.Repeat("x", 700) + evilScript,
		Matches: []Match{{Segments: Passage("", "# "+evilScript), Vector: new(0.4)}},
	}, {
		DocumentID: "long", Title: strings.Repeat("A long title ", 25) + evilQuotes, URL: evilURL,
	}}
}

// sampleVariants are more samples of the pages with branches their sample
// can't take: the search home, with interests and without, and results: a
// middle page with gaps on both sides of its pager, a capped search's last
// page, a page past the end, and one page alone; Status with every panel
// failed, the Library with counts for its tabs and without counts, and the
// Failures tab in each of its states.
func sampleVariants(t testing.TB) map[string][]any {
	t.Helper()
	at := time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)
	layout := func(nav Nav) Layout {
		return Layout{Title: evilScript, Nav: nav, Version: evilQuotes, Listen: sampleListen}
	}
	panelErr := &PanelError{Message: evilScript, RequestID: evilAttr}
	row := LibraryRow{DocumentID: evilAttr, Title: evilScript, URL: evilURL, State: evilQuotes, ContentType: evilAttr,
		When: at}
	saves := []LibraryRow{
		{DocumentID: evilAttr, BookmarkTitle: evilQuotes + evilScript, URL: evilURL, State: "failed",
			ContentType: evilAttr, When: at, Source: evilScript, Folder: evilAttr, LastError: evilScript,
			FailureCause: evilQuotes},
		{BookmarkTitle: evilAttr, URL: evilURL, When: at, Source: evilAttr, Folder: evilScript},
		{URL: "https://example.com/" + evilQuotes, When: at, Source: "safari"},
	}
	counts := &LibraryCounts{Documents: 7467, Bookmarks: 7497, ByState: map[string]int{"fetched": 4498, evilAttr: 2}}
	return map[string][]any{
		PageSearch: {
			Search{Layout: layout(NavSearch), Type: "pdf", Home: &SearchHome{Searchable: 4498, AllInterests: 222,
				Interests: []Interest{
					{ID: evilAttr, Label: evilScript, Size: 90},
					{ID: "long", Label: strings.Repeat("W", 200), Size: 80},
					{ID: "unlabeled", Size: 70},
					{ID: "cut", Label: "Mobile Ecosystems and " + evilScript, Size: 60},
					{ID: "whole", Label: evilQuotes + " and " + evilAttr, Size: 50},
				}}},
			Search{Layout: layout(NavSearch), Query: " ", Home: &SearchHome{}},
			Search{Layout: layout(NavSearch), Query: evilScript, Type: evilQuotes, Page: 5,
				Results: &SearchResults{TookMS: 80, Total: 100, Hits: sampleHits()}},
			Search{Layout: layout(NavSearch), Query: evilQuotes, Page: 10,
				Results: &SearchResults{TookMS: 80, Total: 100, Capped: true, Hits: sampleHits()}},
			Search{Layout: layout(NavSearch), Query: evilAttr, Page: 5, Results: &SearchResults{Total: 37}},
			Search{Layout: layout(NavSearch), Query: evilURL, Page: 1, Results: &SearchResults{Total: 4, Hits: sampleHits()}},
			Search{Layout: layout(NavSearch), Query: evilScript, Page: 1, Results: &SearchResults{}},
		},
		PageStatus:    statusVariants(layout(NavStatus), panelErr, at),
		PageFailures:  failuresVariants(layout(NavLibrary), panelErr, counts),
		PageDocument:  documentVariants(layout(NavLibrary), panelErr, at),
		PageInterests: interestsVariants(layout(NavInterests), panelErr, at),
		PageInterest:  interestVariants(layout(NavInterests)),
		PageLibrary: {
			Library{Layout: layout(NavLibrary), Filters: LibraryFilters{State: evilAttr, Limit: 7}, Counts: counts,
				Rows: []LibraryRow{row}, NextCursor: evilScript, PageSize: 7, Shown: 3},
			Library{Layout: layout(NavLibrary), Filters: LibraryFilters{State: "fetched"}, Counts: counts,
				Rows: []LibraryRow{row}, PageSize: 50},
			Library{Layout: layout(NavLibrary), Rows: []LibraryRow{row}, NextCursor: evilAttr, PageSize: 50},
			Library{Layout: layout(NavLibrary), Filters: LibraryFilters{Order: OrderSaved, Host: evilScript, Limit: 7},
				Counts: counts, Rows: saves, NextCursor: evilScript, PageSize: 7, Shown: 3},
			Library{Layout: layout(NavLibrary), Filters: LibraryFilters{Order: OrderSaved}, Counts: counts,
				Rows: saves, PageSize: 50},
			Library{Layout: layout(NavLibrary), Filters: LibraryFilters{Order: OrderSaved, Folder: evilAttr},
				Counts: counts},
			Library{Layout: layout(NavLibrary), Filters: LibraryFilters{Order: OrderSaved, Cause: evilAttr,
				Host: evilQuotes}, Counts: counts, Rows: saves, PageSize: 50},
		},
	}
}

// statusVariants are Status with each state of its panels and controls:
// every read failed; the queue open, gentle, keep-awake on but not holding
// on battery, a schedule, the metrics unread; the queue closed by its
// schedule with keep-awake off; its read failed alone; and the two polls'
// answers.
func statusVariants(layout Layout, panelErr *PanelError, at time.Time) []any {
	kinds := []KindLoad{{Kind: "fetch", Running: 2, Limit: 16, Pending: 1632}, {Kind: "index", Limit: 4}}
	health := &HealthPanel{OllamaReachable: true, Version: evilQuotes}
	return []any{
		Status{Layout: layout, Counts: &CountsPanel{Err: panelErr}, Queue: &QueuePanel{Err: panelErr},
			Progress: &ProgressPanel{Err: panelErr}, Health: &HealthPanel{Err: panelErr},
			Failures: &FailuresPanel{Err: panelErr}},
		Status{Layout: layout, Counts: &CountsPanel{}, Queue: &QueuePanel{Open: true, Throttle: "gentle",
			Schedule: "22:00-07:00", Kinds: kinds, KeepAwake: true, PowerSource: "battery"},
			Progress: &ProgressPanel{Err: panelErr}, Health: health, Failures: &FailuresPanel{}},
		Status{Layout: layout, Counts: &CountsPanel{}, Queue: &QueuePanel{Reason: "outside_schedule", OpensAt: at,
			Throttle: "normal", Schedule: "22:00-07:00", Kinds: kinds, FinishedIn: ProgressWindow},
			Progress: &ProgressPanel{Progress: EstimateProgress([]KindWork{{Kind: "fetch", Pending: 3}}, ProgressWindow,
				false), Reason: "outside_schedule"}, Health: health, Failures: &FailuresPanel{}},
		Status{Layout: layout, Counts: &CountsPanel{}, Queue: &QueuePanel{Err: panelErr},
			Progress: &ProgressPanel{Err: panelErr}, Health: health, Failures: &FailuresPanel{}},
		Status{Layout: layout, Poll: PollLive, Counts: &CountsPanel{Documents: 2},
			Queue: &QueuePanel{Open: true, Throttle: "normal", Kinds: kinds}, Progress: &ProgressPanel{}},
		// Every job waits for a later time, and some wait beside work that runs.
		Status{Layout: layout, Poll: PollLive, Counts: &CountsPanel{Documents: 2},
			Queue: &QueuePanel{Open: true, Throttle: "normal", Kinds: []KindLoad{{Kind: "fetch", Limit: 16,
				Pending: 171, DueLater: 171, NextDue: at}}},
			Progress: &ProgressPanel{Progress: EstimateProgress([]KindWork{{Kind: "fetch", Pending: 171, DueLater: 171,
				NextDue: at}}, ProgressWindow, true)}},
		Status{Layout: layout, Poll: PollLive, Counts: &CountsPanel{Documents: 2},
			Queue: &QueuePanel{Open: true, Throttle: "normal", Kinds: kinds},
			Progress: &ProgressPanel{Progress: EstimateProgress([]KindWork{{Kind: "fetch", Pending: 40, DueLater: 30,
				NextDue: at, Running: 2, Finished: 9}}, ProgressWindow, true)}},
		Status{Layout: layout, Poll: PollHealth, Health: &HealthPanel{OllamaDetail: evilScript}},
	}
}

// documentVariants are a document's page in each state, untitled, with a
// job queued behind a paused queue, running, and retrying (named by its
// bookmark's title), each outcome of its jobs, a dead link's confirm, its
// jobs' read failed, and a poll's answer.
func documentVariants(layout Layout, panelErr *PanelError, at time.Time) []any {
	doc := func(state string, ext *Extraction, jobs DocumentJobs) Document {
		jobs.DocumentID, jobs.State, jobs.AttemptLimit = evilAttr, state, 5
		return Document{Layout: layout, Meta: DocumentMeta{ID: evilAttr, URL: evilURL, State: state},
			Extraction: ext, Text: TextPanel{State: TextNotFetched}, Jobs: jobs}
	}
	ext := &Extraction{Fetcher: evilScript, Via: "readability", FetchedAt: at}
	then := DocumentBaseline{Updated: at, Extraction: evilAttr}
	later := DocumentBaseline{Updated: at.Add(time.Second), Extraction: evilAttr}
	queued := []JobLine{{Kind: "fetch"}}
	// Named by its bookmark's title, as an untitled page is.
	failed := doc("failed", nil, DocumentJobs{Baseline: then, Current: then, Jobs: []JobLine{{Kind: "fetch",
		Waiting: true, Attempts: 2, RunAfter: at, LastError: evilScript}}})
	failed.Meta.BookmarkTitle = evilScript
	return []any{
		doc("pending", nil, DocumentJobs{Baseline: then, Current: then, Jobs: queued, Hold: "paused"}),
		doc("fetched", ext, DocumentJobs{Baseline: then, Current: then, Jobs: []JobLine{{Kind: "index", Running: true,
			Attempts: 1}}}),
		failed,
		doc("dead", nil, DocumentJobs{Baseline: then, Current: later}),
		doc("fetched", ext, DocumentJobs{Baseline: then, Current: later}),
		doc("fetched", ext, DocumentJobs{Baseline: then, Current: DocumentBaseline{Updated: at, Extraction: evilScript}}),
		doc("fetched", ext, DocumentJobs{Err: panelErr}),
		Document{Layout: layout, Poll: PollJobs, Meta: DocumentMeta{ID: evilAttr}, Jobs: DocumentJobs{DocumentID: evilAttr,
			State: "pending", Baseline: then, Current: later, Jobs: queued, AttemptLimit: 5}},
	}
}

// failuresVariants are the Failures tab with nothing failed, the summary
// unread, the library's counts unread, only failed documents, only dead
// links, and a poll's answer, the summary read and not.
func failuresVariants(layout Layout, panelErr *PanelError, counts *LibraryCounts) []any {
	failed := []FailureGroup{{Cause: "timeout", Count: 2, Hosts: []Count{{Name: evilAttr, Count: 2}}},
		{Cause: "tls", Count: 1}}
	dead := []FailureGroup{{Cause: "dead_link", Count: 1, Hosts: []Count{{Name: evilScript, Count: 1}}}}
	return []any{
		Failures{Layout: layout, Counts: counts},
		Failures{Layout: layout, Counts: counts, Err: panelErr},
		Failures{Layout: layout, Total: 3, Groups: failed},
		Failures{Layout: layout, Counts: counts, Total: 3, Groups: failed},
		Failures{Layout: layout, Counts: counts, Total: 1, Groups: dead},
		Failures{Layout: layout, Poll: PollCauses, Total: 4, Groups: append(slices.Clone(failed), dead...)},
		Failures{Layout: layout, Poll: PollCauses, Err: panelErr},
	}
}

// interestsVariants are the Interests with a rebuild queued behind a
// paused queue, one done and one failed since the run shown, insight off
// without a run, the rebuild's reads failed, a poll's answer, the first,
// a middle and the last of many pages, the first page of a newer run than
// asked for, and a page past the last of a run, of an empty run and
// without one.
func interestsVariants(layout Layout, panelErr *PanelError, at time.Time) []any {
	run := &InterestRun{ComputedAt: at, Documents: 3}
	many := &InterestRun{ID: evilScript, ComputedAt: at, Algo: evilAttr, Documents: 4498, Noise: 2547, Interests: 1951}
	cards := func(n int) []Interest {
		out := make([]Interest, 0, n)
		for i := range n {
			out = append(out, Interest{ID: evilAttr + strconv.Itoa(i), Label: evilScript, Size: 10, Cohesion: 0.6,
				Members: []Member{{DocumentID: evilAttr, BookmarkTitle: evilScript, URL: evilURL}}})
		}
		return out
	}
	rebuild := Rebuild{Enabled: true, Shown: evilScript}
	return []any{
		Interests{Layout: layout, Run: run, Rebuild: Rebuild{Enabled: true, Queued: true, Hold: "paused", Shown: "run"}},
		Interests{Layout: layout, Run: run, Rebuild: Rebuild{Enabled: true, Shown: "run", NewRun: "done"}},
		Interests{Layout: layout, Run: run, Rebuild: Rebuild{Enabled: true, Shown: "run", NewRun: "failed",
			RunError: evilScript}},
		Interests{Layout: layout, Rebuild: Rebuild{}},
		Interests{Layout: layout, Rebuild: Rebuild{Enabled: true, Err: panelErr}},
		Interests{Layout: layout, Poll: PollRebuild, Rebuild: Rebuild{Enabled: true, Running: true, Shown: evilAttr}},
		Interests{Layout: layout, Page: 1, Run: many, Interests: cards(InterestsPageSize), Rebuild: rebuild},
		Interests{Layout: layout, Page: 51, Run: many, Interests: cards(InterestsPageSize), Rebuild: rebuild},
		Interests{Layout: layout, Page: 82, Run: many, Interests: cards(7), Rebuild: rebuild},
		Interests{Layout: layout, Page: 1, RunChanged: true, Run: many, Interests: cards(InterestsPageSize),
			Rebuild: rebuild},
		Interests{Layout: layout, Page: 83, RunChanged: true, Run: many, Rebuild: rebuild},
		Interests{Layout: layout, Page: math.MaxInt, Run: &InterestRun{ID: evilAttr, ComputedAt: at, Documents: 5,
			Noise: 5}, Rebuild: rebuild},
		Interests{Layout: layout, Page: 2, Rebuild: Rebuild{Enabled: true}},
	}
}

// interestVariants are an interest's page of members named every way, on
// its first page, past its thousandth member, and past its last page, and
// without a run time.
func interestVariants(layout Layout) []any {
	members := func(n int) []Member {
		out := make([]Member, 0, n)
		for i := range n {
			out = append(out, Member{DocumentID: evilAttr + strconv.Itoa(i), BookmarkTitle: evilScript,
				URL: "https://example.com/" + evilQuotes + strconv.Itoa(i), Similarity: 0.5})
		}
		return out
	}
	big := Interest{ID: evilScript, Label: evilAttr, Summary: evilScript, Size: 1234, Cohesion: 0.6}
	first, deep := big, big
	first.Members, deep.Members = members(InterestMembersPageSize), members(InterestMembersPageSize)
	return []any{
		InterestPage{Layout: layout, Interest: first, Page: 1},
		InterestPage{Layout: layout, Interest: deep, Page: 21},
		InterestPage{Layout: layout, Interest: big, Page: 26},
		InterestPage{Layout: layout, Interest: Interest{ID: evilAttr, Size: 1}, Page: 2},
	}
}

// allSamples are every page's samples: its sample, then its variants.
func allSamples(t testing.TB, r *Renderer) map[string][]any {
	t.Helper()
	all := map[string][]any{}
	for page, sample := range samples(t, r) {
		all[page] = []any{sample}
	}
	for page, variants := range sampleVariants(t) {
		all[page] = append(all[page], variants...)
	}
	return all
}

// sampleListen is the samples' daemon address.
const sampleListen = "127.0.0.1:8765"

// partialSamples are the view models of the partials the page sets hold,
// each with every shape of data it takes: every icon, a known one or not,
// and hostile values in every string.
func partialSamples(t testing.TB) map[string][]any {
	t.Helper()
	at := time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)
	names := append(iconNames(t), "no-such-icon", evilScript)
	icons := make([]any, 0, len(names))
	for _, name := range names {
		icons = append(icons, name)
	}
	return map[string][]any{
		"panel-error": {&PanelError{Message: evilScript, RequestID: evilAttr}},
		"icon":        icons,
		"logo":        {nil},
		"icons.html":  {nil},
		"reltime":     {time.Time{}, at},
		"doc-title": {DocRef{ID: evilAttr, Title: evilScript, URL: evilURL}, DocRef{ID: evilAttr, URL: evilURL},
			DocRef{Title: evilScript, URL: evilURL}, DocRef{ID: evilAttr, Fallback: evilScript, URL: evilURL},
			DocRef{Fallback: evilAttr + evilQuotes, URL: evilURL}},
		"doc-cell": {DocCell{Ref: DocRef{ID: evilAttr, Title: evilScript, URL: evilURL}, Where: evilQuotes,
			Source: evilScript, Folder: evilAttr, ContentType: evilAttr, When: at, FailureCause: evilScript,
			LastError: evilAttr},
			DocCell{Ref: DocRef{Fallback: evilScript, URL: evilURL}, Source: evilAttr},
			DocCell{Ref: DocRef{URL: evilURL}}},
		"library-head": {LibraryHead{Counts: &LibraryCounts{Documents: 3, Bookmarks: 4},
			Views: LibraryViews{Failed: 2, Counted: true}}, LibraryHead{Views: LibraryViews{OnFailures: true}}},
		"library-subnav": {LibraryViews{},
			LibraryViews{OnFailures: true, Failed: 2971, Counted: true}},
		"state-badge": {evilScript, "dead"},
		"stackbar": {stateBar([]Count{{Name: "fetched", Count: 2}, {Name: "failed", Count: 1}}),
			causeBar("dead_link", 819, 926), []BarSegment(nil),
			[]BarSegment{{Class: evilAttr, X: evilScript, Width: evilQuotes}}},
		"upstream-failure": {Upstream{Name: evilScript, LastFailure: at, LastFailureClass: evilScript}, Upstream{}},
		"attention": {&HealthPanel{OllamaDetail: evilScript, Drift: &Drift{Fix: evilScript, Detail: evilAttr},
			Upstreams: []Upstream{{Name: evilScript, Enabled: true, State: "failing"}}},
			&HealthPanel{OllamaReachable: true, Drift: &Drift{Verified: true, Detail: evilScript, Fix: evilQuotes}},
			&HealthPanel{OllamaReachable: true}},
		"queue-card": {&QueuePanel{Paused: true, Reason: evilScript, OpensAt: at, Throttle: evilAttr, Schedule: evilScript,
			Kinds: []KindLoad{{Kind: evilScript}}, FinishedIn: ProgressWindow, KeepAwake: true, PowerSource: evilQuotes},
			&QueuePanel{Err: &PanelError{Message: evilScript, RequestID: evilAttr}}},
		"queue-why": {&QueuePanel{Open: true}, &QueuePanel{Reason: evilScript, OpensAt: at},
			&QueuePanel{Open: true, Kinds: []KindLoad{{Kind: evilScript, Pending: 2, DueLater: 2, NextDue: at}}}},
		"due-later": {Progress{DueLater: 3, NextDue: at}, Progress{}},
		"progress": {
			&ProgressPanel{Progress: EstimateProgress([]KindWork{{Kind: "fetch", Pending: 2, DueLater: 2, NextDue: at}},
				ProgressWindow, true)},
			&ProgressPanel{Progress: EstimateProgress([]KindWork{{Kind: "fetch", Pending: 1}}, ProgressWindow, false),
				Reason: evilScript}},
		"doc-jobs": {DocumentJobs{DocumentID: evilAttr, State: evilScript, Jobs: []JobLine{{Kind: evilScript},
			{Kind: evilAttr, Waiting: true, RunAfter: at, LastError: evilAttr + evilScript}},
			Hold: evilScript, Current: DocumentBaseline{Extraction: evilScript}},
			DocumentJobs{Err: &PanelError{Message: evilScript, RequestID: evilAttr}}},
		"rebuild-state": {Rebuild{Queued: true, Hold: evilScript, NewRun: "done"},
			Rebuild{NewRun: "failed", RunError: evilScript}, Rebuild{Err: &PanelError{Message: evilScript}}},
		"rebuild-control": {Rebuild{Enabled: true, Queued: true}, Rebuild{}},
		"action": {Action{Kind: evilAttr, Method: evilScript, Path: evilURL, body: map[string]any{evilAttr: evilScript},
			Field: evilQuotes, Join: evilAttr, Status: evilScript, Done: evilQuotes, DoneOff: evilScript}, Action{}},
		"poller": {DocumentJobs{DocumentID: evilAttr, Baseline: DocumentBaseline{Updated: at, Extraction: evilScript},
			Jobs: []JobLine{{Kind: "fetch"}}}.Poller(), Rebuild{Shown: evilScript}.Poller(), Status{}.Pollers()[1]},
		"pager": {
			newPager(pageSpan{Page: 5, Size: 24, Shown: 24, Total: 1951, Noun: evilScript, Suffix: evilAttr,
				Href: func(page int) string { return interestsPageHref(page, evilAttr+evilScript) }}),
			newPager(pageSpan{Page: 1, Size: 50, Shown: 50, Total: 84, Noun: evilQuotes, Suffix: evilScript,
				Href: func(page int) string { return interestPageHref(evilScript+evilURL, page) }}),
			newPager(pageSpan{Page: 2, Size: 50, Shown: 34, Total: 84, Noun: evilAttr,
				Href: func(page int) string { return interestPageHref(evilAttr, page) }}),
			newPager(pageSpan{Page: 10, Size: SearchPageSize, Shown: 10, Total: 100, Noun: evilQuotes,
				Href: evilSearchHref}),
		},
		"out-of-range": {
			outOfRange(11, 10, func(page int) string { return interestsPageHref(page, evilScript) }),
			outOfRange(math.MaxInt, 0, func(page int) string { return interestPageHref(evilAttr, page) }),
			outOfRange(7, 4, evilSearchHref),
		},
		"full-error": {evilScript, ""},
		"match": {Match{Segments: Highlight(evilScript + " <em>" + evilAttr + "</em>"), BM25: new(1.5)},
			Match{Segments: Passage("", evilQuotes), Vector: new(0.2)}, Match{}},
		"cohesion": {0.5},
	}
}

// evilSearchHref is a page of the search for a hostile query and type.
func evilSearchHref(page int) string { return searchHref(evilScript+evilURL, evilAttr, page) }

// TestEveryTemplateRenders executes every template each page's set
// defines, the page and the partials alike, with a typed sample whose
// strings are hostile, and checks that each output is inert. A template
// without a sample fails: a new one needs one here.
func TestEveryTemplateRenders(t *testing.T) {
	r := newRenderer(t)
	pageSamples := allSamples(t, r)
	partials := partialSamples(t)
	require.ElementsMatch(t, pageNames, keys(samples(t, r)), "a sample for every page")
	for _, page := range pageNames {
		set := r.pages[page]
		for _, tmpl := range set.Templates() {
			name := tmpl.Name()
			t.Run(page+"/"+name, func(t *testing.T) {
				data, ok := partials[name]
				if !ok && pageTemplate(page, name) {
					data, ok = pageSamples[page], true
				}
				require.True(t, ok, "template %q of the %s page has no sample", name, page)
				for _, d := range data {
					var buf bytes.Buffer
					require.NoError(t, set.ExecuteTemplate(&buf, name, d))
					out := buf.String()
					uitest.AssertInert(t, out)
					assert.NotContains(t, out, evilScript, "escaped")
					assert.NotContains(t, out, `onerror="alert`, "escaped")
				}
			})
		}
	}
}

// pageTemplate reports whether name is one of page's own templates: the
// layout and its blocks, which take the page's view model, and the
// template of the page's file, which holds its defines.
func pageTemplate(page, name string) bool {
	switch name {
	case "layout", "head", "header-search", "content", "document-page", "document-actions", "interests-page",
		"failures-page", "failures-live", "layout.html", page + ".html":
		return true
	}
	return false
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPage_WritesNothingOnFailure: a template that fails part way writes
// nothing, not a status and half a page.
func TestPage_WritesNothingOnFailure(t *testing.T) {
	r := newRenderer(t)
	w := httptest.NewRecorder()
	// Status's template reads fields a Search doesn't have.
	err := r.Page(w, http.StatusOK, PageStatus, Search{Layout: Layout{Title: "t"}}, CSP)
	require.Error(t, err)
	assert.False(t, w.Flushed)
	assert.Zero(t, w.Body.Len(), "nothing written")
	assert.Empty(t, w.Header(), "no headers set")

	require.Error(t, r.Page(w, http.StatusOK, "no-such-page", nil, CSP))
}

// TestPage_Headers: a page is HTML with the CSP it was given and its
// length.
func TestPage_Headers(t *testing.T) {
	r := newRenderer(t)
	w := httptest.NewRecorder()
	page := samples(t, r)[PageError]
	require.NoError(t, r.Page(w, http.StatusNotFound, PageError, page, CSPWithImages))
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, CSPWithImages, w.Header().Get("Content-Security-Policy"))
	assert.Equal(t, strconv.Itoa(w.Body.Len()), w.Header().Get("Content-Length"))
}

// TestPages_Layout: every page has the viewport meta, a skip link to its
// content, the navigation, the header's search box unless the page can't
// use one, and a footer naming the daemon and where it listens; it loads
// the stylesheet, which themes for dark mode.
func TestPages_Layout(t *testing.T) {
	r := newRenderer(t)
	for page, data := range eachSample(t, r) {
		out := render(t, r, page, data)
		assert.Contains(t, out, `<meta name="viewport" content="width=device-width, initial-scale=1">`, page)
		assert.Contains(t, out, `<a class="skip-link" href="#main">Skip to content</a>`, page)
		assert.Contains(t, out, `<main id="main" class="page">`, page)
		assert.Contains(t, out, `<nav aria-label="Main">`, page)
		assert.Contains(t, out, `<a class="brand" href="/ui/" aria-label="curio: search">`, page)
		const headerSearch = `<form class="header-search" action="/ui/" method="get" role="search">`
		if page != PageSearch && page != PageStarting {
			assert.Equal(t, 1, strings.Count(out, headerSearch), "%s: the header's search box", page)
			assert.Equal(t, 1, strings.Count(out, ` name="q"`), "%s: the header's search box sends only q", page)
		} else {
			assert.NotContains(t, out, `class="header-search"`, page)
		}
		assert.Contains(t, out, `<footer class="site"><div class="container"><span>curio-daemon &#39;&#34;&lt;&gt;&amp;</span>`+
			`<span>`+sampleListen+` · this Mac only</span></div></footer>`, page)
	}

	unknown := samples(t, r)[PageError].(ErrorPage)
	unknown.Layout.Listen = ""
	assert.Contains(t, render(t, r, PageError, unknown),
		`<footer class="site"><div class="container"><span>curio-daemon &#39;&#34;&lt;&gt;&amp;</span></div></footer>`,
		"the footer without an address")

	css, ok := r.assets.files[r.assets.hashed["app.css"]]
	require.True(t, ok)
	assert.Contains(t, string(css.body), "@media (prefers-color-scheme: dark)")
	assert.Contains(t, string(css.body), "color-scheme: light dark")
}

// TestPages_Navigation: the navigation links to every page, in its order,
// the current one marked, and to no page when none is current.
func TestPages_Navigation(t *testing.T) {
	r := newRenderer(t)
	navRE := regexp.MustCompile(`<a href="([^"]+)"( aria-current="page")?><svg class="icon"`)
	for page, data := range eachSample(t, r) {
		var hrefs, current []string
		for _, m := range navRE.FindAllStringSubmatch(render(t, r, page, data), -1) {
			hrefs = append(hrefs, m[1])
			if m[2] != "" {
				current = append(current, m[1])
			}
		}
		assert.Equal(t, []string{"/ui/", "/ui/library", "/ui/interests", "/ui/status"}, hrefs, page)
		switch page {
		case PageSearch:
			assert.Equal(t, []string{"/ui/"}, current, page)
		case PageStatus:
			assert.Equal(t, []string{"/ui/status"}, current, page)
		case PageLibrary, PageFailures, PageDocument:
			assert.Equal(t, []string{"/ui/library"}, current, page)
		case PageInterests, PageInterest:
			assert.Equal(t, []string{"/ui/interests"}, current, page)
		default:
			assert.Empty(t, current, page)
		}
	}
}

// eachSample yields every sample of every page, variants included.
func eachSample(t *testing.T, r *Renderer) iter.Seq2[string, any] {
	t.Helper()
	all := allSamples(t, r)
	return func(yield func(string, any) bool) {
		for page, samples := range all {
			for _, data := range samples {
				if !yield(page, data) {
					return
				}
			}
		}
	}
}

func render(t *testing.T, r *Renderer, page string, data any) string {
	t.Helper()
	w := httptest.NewRecorder()
	require.NoError(t, r.Page(w, http.StatusOK, page, data, CSP))
	return w.Body.String()
}

// assetRefRE finds the asset URLs a page references.
var assetRefRE = regexp.MustCompile(`(?:href|src)="(/ui/static/[^"]+)"`)

// TestAssets: every asset a page references is served, with its type and
// cached for good; a name that isn't an asset's, a stale hash included,
// is not.
func TestAssets(t *testing.T) {
	r := newRenderer(t)
	var refs []string
	for page, data := range eachSample(t, r) {
		for _, m := range assetRefRE.FindAllStringSubmatch(render(t, r, page, data), -1) {
			if !slices.Contains(refs, m[1]) {
				refs = append(refs, m[1])
			}
		}
	}
	require.Len(t, refs, 3, "the stylesheet, htmx and actions.js")
	types := map[string]string{".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8"}
	for _, ref := range refs {
		file := strings.TrimPrefix(ref, AssetPrefix)
		assert.Regexp(t, `^[a-z0-9.-]+\.[0-9a-f]{16}\.(css|js)$`, file)
		w := httptest.NewRecorder()
		require.True(t, r.ServeAsset(w, file), ref)
		resp := w.Result()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		sum := sha256.Sum256(body)
		assert.Contains(t, file, hex.EncodeToString(sum[:])[:16], "named after its content")
		assert.Equal(t, types[file[strings.LastIndexByte(file, '.'):]], resp.Header.Get("Content-Type"))
		assert.Equal(t, "public, max-age=31536000, immutable", resp.Header.Get("Cache-Control"))
	}
	for _, name := range []string{"app.css", "app.0000000000000000.css", "nope.js", ""} {
		assert.False(t, r.ServeAsset(httptest.NewRecorder(), name), name)
	}
}

// TestPages_Scripts: every page loads htmx, then actions.js, each deferred
// and under its hashed name.
func TestPages_Scripts(t *testing.T) {
	r := newRenderer(t)
	actionsJS := regexp.MustCompile(`<script src="/ui/static/actions\.[0-9a-f]{16}\.js" defer></script>`)
	for page, data := range eachSample(t, r) {
		out := render(t, r, page, data)
		loc := actionsJS.FindStringIndex(out)
		require.NotNil(t, loc, page)
		assert.Less(t, strings.Index(out, `<script src="/ui/static/htmx-`), loc[0], "%s: after htmx", page)
		assert.Equal(t, 2, strings.Count(out, "<script"), page)
	}
}

// polledPages are the pages with live regions.
var polledPages = []string{PageStatus, PageFailures, PageDocument, PageInterests}

// TestLiveRegions: in every sample of a page with live regions, each
// region a poller names is on the page exactly once, and nothing in it
// announces itself (no live role: it would speak on every poll that
// changes it) or can take focus without an id, which is how htmx finds
// what had focus after a swap. Every control's status is on the page, and
// announces; a poll's answer carries a control's region, not its status.
func TestLiveRegions(t *testing.T) {
	r := newRenderer(t)
	all := allSamples(t, r)
	for _, page := range polledPages {
		for i, data := range all[page] {
			doc := parse(t, render(t, r, page, data))
			ids := elementIDs(doc)
			for _, poller := range withAttr(doc, "data-poll") {
				for id := range strings.SplitSeq(strings.ReplaceAll(attrValue(poller, "hx-select-oob"), "#", ""), ",") {
					require.Equal(t, 1, ids[id], "%s sample %d: region %q", page, i, id)
					for n := range byID(doc, id).Descendants() {
						if n.Type != html.ElementNode {
							continue
						}
						role := attrValue(n, "role")
						assert.False(t, hasAttr(n, "aria-live") || role == "alert" || role == "status",
							"%s sample %d: a live role in region %q: <%s role=%q>", page, i, id, n.Data, role)
						if focusable(n) {
							assert.NotEmpty(t, attrValue(n, "id"), "%s sample %d: <%s> in region %q", page, i, n.Data, id)
						}
					}
				}
			}
			if isPoll(data) {
				continue
			}
			for _, control := range withAttr(doc, "data-status") {
				status := byID(doc, attrValue(control, "data-status"))
				require.NotNil(t, status, "%s sample %d: %s's status", page, i, attrValue(control, "data-kind"))
				assert.Equal(t, "action-status", attrValue(status, "class"))
				assert.Equal(t, "polite", attrValue(status, "aria-live"))
			}
		}
	}
}

// isPoll reports whether a sample is a poll's answer.
func isPoll(data any) bool {
	switch d := data.(type) {
	case Status:
		return d.Poll != ""
	case Document:
		return d.Poll != ""
	case Interests:
		return d.Poll != ""
	case Failures:
		return d.Poll != ""
	}
	return false
}

// elementIDs counts the ids of doc's elements.
func elementIDs(doc *html.Node) map[string]int {
	ids := map[string]int{}
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && hasAttr(n, "id") {
			ids[attrValue(n, "id")]++
		}
	}
	return ids
}

// withAttr are doc's elements that carry attr.
func withAttr(doc *html.Node, attr string) []*html.Node {
	var out []*html.Node
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && hasAttr(n, attr) {
			out = append(out, n)
		}
	}
	return out
}

func hasAttr(n *html.Node, key string) bool {
	return slices.ContainsFunc(n.Attr, func(a html.Attribute) bool { return a.Key == key })
}

// focusable reports whether n can take focus: a link, a control, or
// anything given a tabindex.
func focusable(n *html.Node) bool {
	switch n.Data {
	case "a":
		return hasAttr(n, "href")
	case "button", "input", "select", "textarea", "summary":
		return true
	}
	return hasAttr(n, "tabindex")
}
