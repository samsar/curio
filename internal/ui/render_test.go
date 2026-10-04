package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
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

// Hostile labels, which a model writes as it likes: 300 characters
// without a break, and a right-to-left override that turns the text after
// it around.
var (
	evilLong = strings.Repeat("W", 300)
	evilRTL  = "Kafka \u202estreams" + evilScript
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
	member := Member{DocumentID: evilAttr, Title: evilScript, URL: evilURL, Similarity: 0.8, Fit: "member"}
	interest := Interest{ID: evilAttr, ParentID: evilScript, ParentLabel: evilLong, Label: evilScript,
		Summary: evilQuotes, Size: 7, Loose: 1, New: 2, Cohesion: 0.7,
		Members: []Member{member, {DocumentID: "doc", URL: "https://example.com/" + evilQuotes, Fit: "member"},
			{DocumentID: evilScript, BookmarkTitle: evilAttr + evilScript, URL: evilURL, Fit: "loose"}},
		NewMembers: []Member{{DocumentID: evilQuotes, BookmarkTitle: evilRTL, URL: evilURL, Similarity: 0.5, Fit: "new"}},
		Events:     sampleEvents(evilAttr)}
	area := sampleArea(interest)

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
					upstream(evilQuotes, evilAttr, true), {Name: "jina", Enabled: true, State: "failing"}},
				Interests: InterestsState{State: "failing", LastError: evilLong + evilScript, RetryAt: at}},
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
			Place: &DocumentPlace{DocumentID: evilAttr, Fit: "member", Interest: InterestRef{ID: evilAttr, Label: evilLong},
				Area: InterestRef{ID: evilScript, Label: evilRTL, Area: true}},
		},
		PageInterests: Interests{
			Layout: layout(NavInterests),
			Page:   2, RunChanged: true,
			Run: &InterestRun{ID: evilAttr + evilScript, ComputedAt: at, Kind: evilScript, Shape: "areas",
				Documents: 400, Areas: 30, Interests: 60, Loose: 1, Unsorted: 2, New: 3, NewUnsorted: 1, Total: 30,
				Changes: RunChanges{Kept: 50, Split: 2, Merged: 1, Moved: 1, Dissolved: 1, Created: 4}, Recent: true},
			Interests: []Interest{area, {ID: "unlabeled", Area: true, Size: 1, NumChildren: 1}},
			Rebuild: Rebuild{Enabled: true, Running: true, StartedAt: at, Shown: evilAttr, Ready: true,
				State: InterestsState{State: "held", HeldReason: evilScript}},
		},
		PageInterest: InterestPage{Layout: layout(NavInterests), Interest: interest, RunAt: at},
		PageUnsorted: Unsorted{Layout: layout(NavInterests), Page: 1, RunChanged: true, Run: evilAttr, Total: 3,
			Documents: sampleUnsorted(), NumNew: 30,
			New: []Member{{DocumentID: evilAttr, BookmarkTitle: evilLong, URL: evilURL, Similarity: 0.2, Fit: "new"}}},
		PageMap: MapPage{Layout: layout(NavInterests), State: MapReady, Run: &MapRun{Shape: "areas", Documents: 5237,
			Areas: 29, Interests: 182, FinishedAt: at}, Rebuilds: InterestsState{State: "current", LastRebuildAt: at},
			Query: MapQuery{View: MapViewGroups, Select: MapSelection{Kind: MapSelectInterest, ID: evilAttr + evilScript}}},
		PageChanges: Changes{Layout: layout(NavInterests), Run: &ChangesRun{ComputedAt: at, Trigger: evilScript,
			Kind: evilAttr, Changes: RunChanges{Kept: 9, Split: 1, Merged: 2, Moved: 1, Dissolved: 1, Created: 2},
			Events: sampleChanges()}},
		PageRetired: Gone{Layout: layout(NavInterests), ID: evilAttr, RetentionDays: 180,
			Retired: &Retired{Label: evilRTL, RetiredAt: at, Successors: []Successor{
				{InterestRef: InterestRef{ID: evilScript, Label: evilLong}, Event: "split", Shared: 12},
				{InterestRef: InterestRef{ID: evilAttr, Retired: true}, Event: "merged", Shared: 1},
				{InterestRef: InterestRef{ID: evilQuotes, Label: evilQuotes}, Event: evilScript, Shared: 3}}}},
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
		PageUnsorted:  unsortedVariants(layout(NavInterests)),
		PageChanges:   changesVariants(layout(NavInterests), at),
		PageMap:       mapVariants(layout(NavInterests), at),
		PageRetired:   goneVariants(layout(NavInterests), at),
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
		Status{Layout: layout, Poll: PollHealth, Health: &HealthPanel{OllamaReachable: true,
			Interests: InterestsState{State: "held", HeldReason: evilLong + evilScript}}},
		Status{Layout: layout, Poll: PollHealth, Health: &HealthPanel{OllamaReachable: true,
			Interests: InterestsState{State: "failing", LastError: evilLong + evilAttr, RetryAt: at.Add(time.Hour)}}},
		Status{Layout: layout, Counts: &CountsPanel{}, Queue: &QueuePanel{Open: true}, Progress: &ProgressPanel{},
			Health: &HealthPanel{OllamaReachable: true, Interests: InterestsState{State: "current", LastRebuildAt: at,
				LastKind: evilQuotes, Changed: 37, RebuildAt: 263}}, Failures: &FailuresPanel{}},
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
	placed := func(p DocumentPlace) Document { return placedDocument(layout, p) }
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
		placed(DocumentPlace{Fit: "member", Interest: InterestRef{ID: evilQuotes, Label: evilRTL}}),
		placed(DocumentPlace{Fit: "loose", Interest: InterestRef{ID: evilQuotes}, Area: InterestRef{ID: evilAttr, Area: true}}),
		placed(DocumentPlace{Fit: "unsorted", Nearest: InterestRef{ID: evilScript, Label: evilLong}}),
		placed(DocumentPlace{Fit: "unsorted"}),
		placed(DocumentPlace{Fit: "new", Interest: InterestRef{ID: evilAttr, Label: evilScript},
			Area: InterestRef{ID: evilQuotes, Label: evilAttr, Area: true}}),
		placed(DocumentPlace{Fit: "new"}),
		placed(DocumentPlace{Fit: evilAttr, Interest: InterestRef{ID: evilAttr, Label: evilQuotes}}),
		placed(DocumentPlace{DocumentID: evilAttr + evilScript, Fit: "member", Interest: InterestRef{ID: evilQuotes}}),
		placed(DocumentPlace{DocumentID: evilAttr, Fit: "unsorted", MapOff: true}),
	}
}

// placedDocument is a fetched document's page whose line in the interests
// is place.
func placedDocument(layout Layout, place DocumentPlace) Document {
	return Document{Layout: layout, Meta: DocumentMeta{ID: evilAttr, URL: evilURL, State: "fetched"},
		Text: TextPanel{State: TextNotFetched}, Place: &place,
		Jobs: DocumentJobs{DocumentID: evilAttr, State: "fetched", AttemptLimit: 5}}
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

// sampleArea is an area holding in, hostile in every string.
func sampleArea(in Interest) Interest {
	return Interest{ID: evilScript + evilAttr, Area: true, Label: evilAttr, Summary: evilScript, Size: 628,
		Loose: 4, New: 3, Cohesion: 0.35, NumChildren: 10,
		Children: []Interest{in, {ID: evilQuotes, Label: evilLong, Size: 86, New: 1}, {ID: "unlabeled", Size: 3},
			{ID: "rtl", Label: evilRTL, Size: 2}},
		Events: sampleEvents(evilScript + evilAttr)}
}

// sampleEvents are a rebuild's events about the group id, one of each line
// its note says, from and to hostile identities, a retired one and an
// unlabeled one among them; and a kept one, which says nothing.
func sampleEvents(id string) []InterestEvent {
	self := &InterestRef{ID: id, Label: evilScript}
	return []InterestEvent{
		{Event: "kept", From: self, To: self, Shared: 9},
		{Event: "split", From: &InterestRef{ID: evilAttr, Label: evilLong, Retired: true}, To: self, Shared: 4},
		{Event: "split", From: self, To: &InterestRef{ID: evilQuotes, Label: evilRTL}, Shared: 3},
		{Event: "merged", From: &InterestRef{ID: evilScript, Retired: true}, To: self, Shared: 2},
		{Event: "merged", From: self, To: &InterestRef{ID: "x", Label: evilQuotes}, Shared: 1},
		{Event: "moved", From: self, To: self, In: &InterestRef{ID: evilAttr, Label: evilAttr, Area: true}},
		{Event: "new", To: self},
		{Event: evilScript, From: self, To: self},
	}
}

// sampleUnsorted are unsorted documents: nearest a hostile interest,
// nearest an unlabeled one, nearest a 300-character label, and nearest
// none.
func sampleUnsorted() []UnsortedDoc {
	doc := func(i int, nearest InterestRef) UnsortedDoc {
		return UnsortedDoc{Member: Member{DocumentID: evilAttr + strconv.Itoa(i), Title: evilScript,
			BookmarkTitle: evilQuotes, URL: "https://example.com/" + evilQuotes + strconv.Itoa(i), Similarity: 0.3,
			Fit: "unsorted"}, Nearest: nearest}
	}
	return []UnsortedDoc{doc(0, InterestRef{ID: evilAttr, Label: evilScript}), doc(1, InterestRef{ID: evilQuotes}),
		doc(2, InterestRef{ID: evilScript, Label: evilLong}), doc(3, InterestRef{})}
}

// sampleChanges are a rebuild's events, as the API lists them: of every
// kind, areas before interests, hostile identities, retired and live, and
// an event this build doesn't know.
func sampleChanges() []InterestEvent {
	ref := func(id, label string, retired bool) *InterestRef {
		return &InterestRef{ID: id, Label: label, Retired: retired}
	}
	return []InterestEvent{
		{Event: "split", Area: true, From: ref(evilAttr, evilScript, false), To: &InterestRef{ID: "a2", Area: true},
			Shared: 40},
		{Event: "split", From: ref(evilScript, evilLong, true), To: ref(evilQuotes, evilRTL, false), Shared: 12},
		{Event: "merged", From: ref("m1", evilAttr, true), To: ref("m2", evilQuotes, false), Shared: 1},
		{Event: "merged", From: ref("m3", "", true), To: ref("m2", evilQuotes, false), Shared: 3},
		{Event: "moved", From: ref("v1", evilScript, false), To: ref("v1", evilScript, false),
			In: &InterestRef{ID: evilAttr, Label: evilLong, Area: true}},
		{Event: "moved", From: ref("v2", "", false), To: ref("v2", "", false)},
		{Event: "dissolved", From: ref("d1", evilRTL, true)},
		{Event: "new", Area: true, To: &InterestRef{ID: "n1", Label: evilQuotes, Area: true}},
		{Event: "new", To: ref("n2", "", false)},
		{Event: evilScript, From: ref("o1", evilAttr, false), To: ref("o2", evilScript, false)},
	}
}

// interestsVariants are the Interests with a rebuild queued behind a
// paused queue, newer interests ready, insight off without a run, the
// library grouped for the first time (by the queue's word and by the
// scheduler's), the rebuild's reads failed, the scheduler's every state,
// a poll's answer in several, the first, a middle and the last of many
// pages of interests and of areas (Unsorted's card on the last), the
// first page of a newer run than asked for, a page past the last of a
// run, of an empty run and without one, and a run without groups.
func interestsVariants(layout Layout, panelErr *PanelError, at time.Time) []any {
	run := &InterestRun{ComputedAt: at, Documents: 3, Shape: "flat"}
	many := &InterestRun{ID: evilScript, ComputedAt: at, Kind: evilAttr, Shape: "flat", Documents: 4498, Loose: 47,
		Unsorted: 2500, New: 12, NewUnsorted: 5, Interests: 1951, Total: 1951}
	areas := &InterestRun{ID: evilAttr, ComputedAt: at, Kind: evilScript, Shape: "areas", Documents: 5254,
		Unsorted: 402, Areas: 30, Interests: 187, Total: 30,
		Changes: RunChanges{Kept: 180, Merged: 1, Created: 6}, Recent: true}
	cards := func(n int) []Interest {
		out := make([]Interest, 0, n)
		for i := range n {
			out = append(out, Interest{ID: evilAttr + strconv.Itoa(i), Label: evilScript, Size: 10, Loose: i % 2,
				New: i % 3, Cohesion: 0.6, Members: []Member{{DocumentID: evilAttr, BookmarkTitle: evilScript, URL: evilURL,
					Fit: "member"}}})
		}
		return out
	}
	rebuild := Rebuild{Enabled: true, Shown: evilScript, State: InterestsState{State: "current", LastRebuildAt: at,
		LastKind: evilAttr, Changed: 3, RebuildAt: 263}}
	state := func(s InterestsState) Rebuild { return Rebuild{Enabled: true, Shown: "run", State: s} }
	states := sampleStates(at)
	out := make([]any, 0, 18+3*len(states))
	out = append(out,
		Interests{Layout: layout, Run: run, Rebuild: Rebuild{Enabled: true, Queued: true, Hold: "paused", Shown: "run"}},
		Interests{Layout: layout, Run: run, Rebuild: Rebuild{Enabled: true, Shown: "run", Ready: true}},
		Interests{Layout: layout, Rebuild: Rebuild{State: InterestsState{State: "off"}}},
		Interests{Layout: layout, Rebuild: Rebuild{Enabled: true, Running: true, StartedAt: at}},
		Interests{Layout: layout, Rebuild: Rebuild{Enabled: true, State: InterestsState{State: "rebuilding"}}},
		Interests{Layout: layout, Rebuild: Rebuild{Enabled: true, Err: panelErr}},
		Interests{Layout: layout, Poll: PollRebuild, Rebuild: Rebuild{Enabled: true, Running: true, Shown: evilAttr}},
		Interests{Layout: layout, Poll: PollRebuild, Rebuild: Rebuild{Enabled: true, Queued: true, Hold: evilScript,
			Ready: true, Shown: evilAttr}},
		Interests{Layout: layout, Poll: PollRebuild, Rebuild: Rebuild{Enabled: true, Err: panelErr}},
		Interests{Layout: layout, Page: 1, Run: many, Interests: cards(InterestsPageSize), Rebuild: rebuild},
		Interests{Layout: layout, Page: 51, Run: many, Interests: cards(InterestsPageSize), Rebuild: rebuild},
		Interests{Layout: layout, Page: 82, Run: many, Interests: cards(7), Rebuild: rebuild},
		Interests{Layout: layout, Page: 1, RunChanged: true, Run: many, Interests: cards(InterestsPageSize),
			Rebuild: rebuild},
		Interests{Layout: layout, Page: 2, Run: areas, Interests: []Interest{sampleArea(cards(1)[0])}, Rebuild: rebuild},
		Interests{Layout: layout, Page: 83, RunChanged: true, Run: many, Rebuild: rebuild},
		Interests{Layout: layout, Page: math.MaxInt, Run: &InterestRun{ID: evilAttr, ComputedAt: at, Documents: 5,
			Unsorted: 5}, Rebuild: rebuild},
		Interests{Layout: layout, Page: 2, Rebuild: Rebuild{Enabled: true}},
		Interests{Layout: layout, Page: 1, Run: &InterestRun{ID: evilAttr, ComputedAt: at, Documents: 5, Unsorted: 5,
			NewUnsorted: 2}, Rebuild: rebuild},
	)
	for _, s := range states {
		out = append(out, Interests{Layout: layout, Run: run, Rebuild: state(s)}, Interests{Layout: layout,
			Rebuild: state(s)}, Interests{Layout: layout, Poll: PollRebuild, Rebuild: state(s)})
	}
	return out
}

// sampleStates are automatic rebuilds in every state the scheduler
// reports, and one this build doesn't know, hostile in every string.
func sampleStates(at time.Time) []InterestsState {
	return []InterestsState{
		{State: "unknown"}, {State: "off"}, {State: "none", Changed: 7, RebuildAt: 20},
		{State: "due", Changed: 25, RebuildAt: 20},
		{State: "due", LastRebuildAt: at, FreshOwed: "reindex"},
		{State: "due", LastRebuildAt: at, FreshOwed: evilScript},
		{State: "due", LastRebuildAt: at, Changed: 300, RebuildAt: 263},
		{State: "queued"}, {State: "rebuilding"},
		{State: "held", HeldReason: evilLong + evilScript},
		{State: "failing", LastError: evilLong + evilAttr, RetryAt: at.Add(time.Hour)},
		{State: "failing", LastError: evilScript},
		{State: "current", LastRebuildAt: at, LastKind: evilQuotes, Changed: 37, RebuildAt: 263},
		{State: evilScript, LastKind: evilAttr},
	}
}

// interestVariants are an interest's page of members named every way, on
// its first page (the new band, with more than it lists), past its
// thousandth member, and past its last page, and without a run time; an
// interest's last page, of loose fits; an area's pages: one of many,
// past the last, and with no interest; and groups whose rebuild did
// nothing but keep them.
func interestVariants(layout Layout) []any {
	members := func(n int) []Member {
		out := make([]Member, 0, n)
		for i := range n {
			out = append(out, Member{DocumentID: evilAttr + strconv.Itoa(i), BookmarkTitle: evilScript,
				URL: "https://example.com/" + evilQuotes + strconv.Itoa(i), Similarity: 0.5, Fit: "member"})
		}
		return out
	}
	big := Interest{ID: evilScript, Label: evilAttr, Summary: evilScript, Size: 1234, New: 30, Cohesion: 0.6,
		NewMembers: []Member{{DocumentID: evilAttr, Title: evilLong, URL: evilURL, Similarity: 0.6, Fit: "new"}}}
	first, deep := big, big
	first.Members, deep.Members = members(InterestMembersPageSize), members(InterestMembersPageSize)
	loose := Interest{ID: evilAttr, ParentID: evilScript, ParentLabel: evilRTL, Label: evilScript, Size: 50, Loose: 3,
		Members: []Member{{DocumentID: evilAttr, Title: evilScript, URL: evilURL, Fit: "loose", Similarity: 0.46}}}
	area := sampleArea(loose)
	area.NumChildren = 30
	kept := Interest{ID: evilAttr, Label: evilLong, Size: 1, Events: []InterestEvent{{Event: "kept",
		From: &InterestRef{ID: evilAttr}, To: &InterestRef{ID: evilAttr}, Shared: 1}}}
	return []any{
		InterestPage{Layout: layout, Interest: first, Page: 1},
		InterestPage{Layout: layout, Interest: deep, Page: 21},
		InterestPage{Layout: layout, Interest: big, Page: 26},
		InterestPage{Layout: layout, Interest: Interest{ID: evilAttr, Size: 1}, Page: 2},
		InterestPage{Layout: layout, Interest: loose, Page: 2},
		InterestPage{Layout: layout, Interest: area, Page: 2},
		InterestPage{Layout: layout, Interest: area, Page: 3},
		InterestPage{Layout: layout, Interest: Interest{ID: evilAttr, Area: true}},
		InterestPage{Layout: layout, Interest: kept, Page: 1},
		InterestPage{Layout: layout, Interest: area, Page: 1, MapOff: true},
		InterestPage{Layout: layout, Interest: loose, Page: 1, MapOff: true},
	}
}

// unsortedVariants are Unsorted before the first rebuild, on its first
// page without new documents, a middle page of many, past the last, a
// newer run than asked for past its first page, and a run with nothing
// unsorted but some placed there since, and with neither.
func unsortedVariants(layout Layout) []any {
	return []any{
		Unsorted{Layout: layout},
		Unsorted{Layout: layout, Run: evilScript, Total: 4, Documents: sampleUnsorted()},
		Unsorted{Layout: layout, Page: 5, Run: evilScript, Total: 600, Documents: sampleUnsorted(), NumNew: 2,
			New: []Member{{DocumentID: evilAttr, URL: evilURL, Fit: "new"}}},
		Unsorted{Layout: layout, Page: 13, Run: evilScript, Total: 600},
		Unsorted{Layout: layout, Page: 2, RunChanged: true, Run: evilAttr, Total: 60, Documents: sampleUnsorted()},
		Unsorted{Layout: layout, Run: evilAttr, NumNew: 1, New: []Member{{DocumentID: evilAttr, Title: evilRTL,
			URL: evilURL, Fit: "new"}}},
		Unsorted{Layout: layout, Run: evilAttr},
		Unsorted{Layout: layout, Run: evilAttr, Total: 4, Documents: sampleUnsorted(), MapOff: true},
	}
}

// mapVariants are the interest map's page drawing a flat run's map with
// nothing selected, a document and Unsorted selected; and without a map,
// in each state: no rebuild yet (where rebuilds stand, insight off), a
// rebuild that drew none, a map that failed (a long, hostile error), and
// the map turned off.
func mapVariants(layout Layout, at time.Time) []any {
	flat := &MapRun{Shape: "flat", Documents: 1, Interests: 1, FinishedAt: at}
	areas := &MapRun{Shape: "areas", Documents: 5237, Areas: 29, Interests: 182}
	page := func(state MapState, run *MapRun, q MapQuery) MapPage {
		return MapPage{Layout: layout, State: state, Run: run, Query: q,
			Rebuilds: InterestsState{State: "none", Changed: 7, RebuildAt: 20}}
	}
	failed := page(MapFailed, areas, MapQuery{})
	failed.Error = strings.Repeat("the map took longer than 2m0s ", 16) + evilScript + evilAttr
	off := page(MapNoRun, nil, MapQuery{})
	off.Rebuilds = InterestsState{State: "off"}
	return []any{
		page(MapReady, flat, MapQuery{View: MapViewSimilarity}),
		page(MapReady, areas, MapQuery{View: MapViewSimilarity, Select: MapSelection{Kind: MapSelectDocument, ID: evilQuotes}}),
		page(MapReady, areas, MapQuery{View: MapViewGroups, Select: MapSelection{Kind: MapSelectUnsorted}}),
		page(MapNoRun, nil, MapQuery{View: MapViewGroups}),
		off,
		page(MapNoMap, areas, MapQuery{}),
		failed,
		page(MapOff, nil, MapQuery{Select: MapSelection{Kind: MapSelectArea, ID: evilScript}}),
	}
}

// changesVariants are the changes before the first rebuild, after the
// first grouping, and after a rebuild that changed nothing.
func changesVariants(layout Layout, at time.Time) []any {
	first := []InterestEvent{{Event: "new", Area: true, To: &InterestRef{ID: "a", Label: evilScript, Area: true}},
		{Event: "new", To: &InterestRef{ID: "i", Label: evilAttr}}}
	return []any{
		Changes{Layout: layout},
		Changes{Layout: layout, Run: &ChangesRun{ComputedAt: at, Trigger: "first", Kind: "fresh",
			Changes: RunChanges{Created: 2}, Events: first}},
		Changes{Layout: layout, Run: &ChangesRun{ComputedAt: at, Trigger: "auto", Kind: "warm",
			Changes: RunChanges{Kept: 12}}},
	}
}

// goneVariants are a retired area that split, one that merged into one
// and into several, one that dissolved, and an ID nothing knows.
func goneVariants(layout Layout, at time.Time) []any {
	successor := func(event string, n int) []Successor {
		out := make([]Successor, 0, n)
		for i := range n {
			out = append(out, Successor{InterestRef: InterestRef{ID: evilAttr + strconv.Itoa(i), Label: evilScript,
				Area: true}, Event: event, Shared: i + 1})
		}
		return out
	}
	retired := func(r Retired) Gone { return Gone{Layout: layout, ID: evilAttr, Retired: &r, RetentionDays: 180} }
	return []any{
		retired(Retired{Area: true, Label: evilLong, RetiredAt: at, Successors: successor("split", 2)}),
		retired(Retired{RetiredAt: at, Successors: successor("merged", 1)}),
		retired(Retired{Label: evilQuotes, RetiredAt: at, Successors: successor("merged", 3)}),
		retired(Retired{Area: true, RetiredAt: at}),
		Gone{Layout: layout, ID: evilScript + evilLong, RetentionDays: 180},
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
		"interests-subnav": {InterestsViews{}, InterestsViews{OnMap: true}},
		"map-link": {"", mapHref(MapQuery{View: MapViewGroups, Select: MapSelection{Kind: MapSelectDocument,
			ID: evilAttr + evilScript}})},
		"library-subnav": {LibraryViews{},
			LibraryViews{OnFailures: true, Failed: 2971, Counted: true}},
		"state-badge": {evilScript, "dead"},
		"stackbar": {stateBar([]Count{{Name: "fetched", Count: 2}, {Name: "failed", Count: 1}}),
			causeBar("dead_link", 819, 926), []BarSegment(nil),
			[]BarSegment{{Class: evilAttr, X: evilScript, Width: evilQuotes}},
			InterestRun{Documents: 5254, Loose: 5, Unsorted: 398, New: 60}.Bar(), childBar(45, 86), childBar(3, 0)},
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
		"rebuild-state":   rebuildSamples(at),
		"rebuild-control": {Rebuild{Enabled: true, Queued: true}, Rebuild{}, Rebuild{Enabled: true}},
		"interests-empty": rebuildSamples(at),
		"interests-state": statesOf(sampleStates(at)),
		"interests-due":   statesOf(sampleStates(at)[3:7]),
		"action": {Action{Kind: evilAttr, Method: evilScript, Path: evilURL, body: map[string]any{evilAttr: evilScript},
			Field: evilQuotes, Join: evilAttr, Status: evilScript, Done: evilQuotes, DoneOff: evilScript}, Action{}},
		"poller": {DocumentJobs{DocumentID: evilAttr, Baseline: DocumentBaseline{Updated: at, Extraction: evilScript},
			Jobs: []JobLine{{Kind: "fetch"}}}.Poller(), Rebuild{Shown: evilScript}.Poller(),
			Rebuild{Enabled: true, Shown: evilScript}.Poller(),
			Rebuild{Enabled: true, State: InterestsState{State: "queued"}}.Poller(), Status{}.Pollers()[1]},
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
		"cohesion":      {0.5},
		"cohesion-line": {0.5},
		"similarity":    {0.5, 0.0, 1.0},
		"date":          {at},
		"interest-cards": {[]Interest{sampleArea(Interest{ID: evilAttr, Label: evilScript, Size: 7}),
			{ID: evilScript, Label: evilAttr, Summary: evilScript, Size: 9, Loose: 2, New: 1,
				Members: []Member{{DocumentID: evilAttr, BookmarkTitle: evilScript, URL: evilURL, Fit: "member"}}}}},
		"interest-item": {sampleArea(Interest{ID: evilAttr, Size: 3}), Interest{ID: evilRTL, Label: evilLong, Size: 4}},
		"area-card": {sampleArea(Interest{ID: evilAttr, Size: 3}), Interest{ID: evilAttr, Area: true, NumChildren: 1},
			Interest{ID: evilAttr, Area: true, Label: evilRTL, NumChildren: 2,
				Children: []Interest{{ID: "a", Label: evilLong, Size: 0}, {ID: "b", Size: 0}}}},
		"interest-card": {Interest{ID: evilAttr, Label: evilScript, Summary: evilQuotes, Size: 3, Loose: 1, New: 2}},
		"unsorted-card": {UnsortedCard{Documents: 398, New: 2, Href: unsortedHref(1, evilScript)},
			UnsortedCard{Documents: 3, Href: unsortedHref(1, "")}},
		"interest-ref": {(*InterestRef)(nil), &InterestRef{ID: evilAttr, Label: evilLong},
			&InterestRef{ID: evilScript, Area: true, Retired: true}, InterestRef{ID: evilQuotes, Label: evilRTL}},
		"nearest": {InterestRef{ID: evilAttr, Label: evilScript}, InterestRef{}},
		"new-band": {&NewBand{Members: []Member{{DocumentID: evilAttr, Title: evilLong, URL: evilURL, Fit: "new"},
			{DocumentID: evilScript, BookmarkTitle: evilRTL, URL: evilURL, Fit: evilAttr}}, More: 18}, &NewBand{}},
		"ranked-row": {RankedMember{Rank: 1234, Member: Member{DocumentID: evilAttr, Title: evilScript, URL: evilURL,
			Fit: "loose"}}, RankedMember{Member: Member{Fit: evilScript}}},
		"event-line": eventsOf(sampleChanges()),
		"fate": {Retired{Successors: []Successor{{Event: "split"}}}, Retired{Successors: []Successor{{Event: "merged"}}},
			Retired{Successors: []Successor{{Event: "merged"}, {Event: "split"}}}, Retired{RetiredAt: at}},
		"doc-place": {DocumentPlace{Fit: "member", Interest: InterestRef{ID: evilAttr, Label: evilScript},
			Area: InterestRef{ID: evilQuotes, Area: true}}, DocumentPlace{Fit: "unsorted"},
			DocumentPlace{Fit: "new", Interest: InterestRef{ID: evilAttr}}},
		"place-path": {DocumentPlace{Interest: InterestRef{ID: evilAttr, Label: evilLong},
			Area: InterestRef{ID: evilScript, Label: evilRTL, Area: true}}, DocumentPlace{Interest: InterestRef{ID: evilAttr}}},
	}
}

// rebuildSamples are the Interests' rebuild in every state it shows: the
// queue's read failed, a rebuild running and queued behind a closed queue,
// newer interests ready, insight off, and the scheduler's every state.
func rebuildSamples(at time.Time) []any {
	states := sampleStates(at)
	out := make([]any, 0, 5+len(states))
	out = append(out, Rebuild{Err: &PanelError{Message: evilScript, RequestID: evilAttr}},
		Rebuild{Enabled: true, Running: true, StartedAt: at}, Rebuild{Enabled: true, Queued: true, Hold: evilScript},
		Rebuild{Enabled: true, Ready: true}, Rebuild{State: InterestsState{State: "off"}})
	for _, s := range states {
		out = append(out, Rebuild{Enabled: true, State: s})
	}
	return out
}

// statesOf are states as samples.
func statesOf(states []InterestsState) []any {
	out := make([]any, 0, len(states))
	for _, s := range states {
		out = append(out, s)
	}
	return out
}

// eventsOf are events as samples.
func eventsOf(events []InterestEvent) []any {
	out := make([]any, 0, len(events))
	for _, e := range events {
		out = append(out, e)
	}
	return out
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
		"area-page", "interest-page", "lineage-note", "failures-page", "failures-live", "map-shell", "map-none",
		"layout.html", page + ".html":
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
		case PageInterests, PageInterest, PageUnsorted, PageChanges, PageMap, PageRetired:
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
	require.Len(t, refs, 14, "the stylesheet, htmx, actions.js, the d3 modules and map.js")
	entries, err := fs.ReadDir(files, "static")
	require.NoError(t, err)
	for _, e := range entries {
		hashed, err := r.assets.url(e.Name())
		require.NoError(t, err)
		assert.Contains(t, refs, hashed, "a page loads %s", e.Name())
	}
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

// scriptRefRE finds the scripts a page loads, by file, less its hash.
var scriptRefRE = regexp.MustCompile(`<script src="/ui/static/([a-z0-9.-]+)\.[0-9a-f]{16}\.js" defer></script>`)

// TestPages_Scripts: every page loads htmx, then actions.js, each deferred
// and under its hashed name; the interest map's page, with a map to draw,
// loads the d3 modules after them, in mapScripts' order, then map.js, and
// no other page, nor the map's without a map, loads any of them.
func TestPages_Scripts(t *testing.T) {
	r := newRenderer(t)
	base := []string{"htmx-2.0.11.min", "actions"}
	for page, data := range eachSample(t, r) {
		out := render(t, r, page, data)
		var loaded []string
		for _, m := range scriptRefRE.FindAllStringSubmatch(out, -1) {
			loaded = append(loaded, m[1])
		}
		want := base
		if m, ok := data.(MapPage); ok && m.Ready() {
			want = append(slices.Clone(base), "d3-dispatch-3.0.1.min", "d3-selection-3.0.0.min", "d3-timer-3.0.1.min",
				"d3-color-3.1.0.min", "d3-interpolate-3.0.1.min", "d3-ease-3.0.1.min", "d3-transition-3.0.1.min",
				"d3-drag-3.0.0.min", "d3-zoom-3.0.0.min", "d3-quadtree-3.0.1.min", "map")
		}
		assert.Equal(t, want, loaded, page)
		assert.Equal(t, len(want), strings.Count(out, "<script"), page)
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
