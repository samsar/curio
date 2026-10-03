package ui

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/store"
)

// Page names, for Renderer.Page: one template per page, each with its view
// model below.
const (
	PageSearch    = "search"    // Search
	PageStatus    = "status"    // Status
	PageLibrary   = "library"   // Library
	PageFailures  = "failures"  // Failures
	PageDocument  = "document"  // Document
	PageInterests = "interests" // Interests
	PageInterest  = "interest"  // InterestPage
	PageUnsorted  = "unsorted"  // Unsorted
	PageChanges   = "changes"   // Changes
	PageRetired   = "retired"   // Gone
	PageError     = "error"     // ErrorPage
	PageStarting  = "starting"  // Starting
)

// Nav is an item of the pages' navigation.
type Nav string

// The navigation, in its order; NavNone marks no item current.
const (
	NavNone      Nav = ""
	NavSearch    Nav = "search"
	NavLibrary   Nav = "library"
	NavInterests Nav = "interests"
	NavStatus    Nav = "status"
)

// Layout is what every page's frame shows.
type Layout struct {
	Title   string // the page's, in <title>
	Nav     Nav    // the current navigation item
	Version string // the daemon's
	Listen  string // the address the daemon listens on; empty when unknown
}

// PanelError is why a panel's data couldn't be read: the message, and the
// request ID the daemon logged it under. The rest of the page still shows.
type PanelError struct {
	Message   string
	RequestID string
}

// Status is what curio is doing and whether what it needs works: what
// needs attention, the library's counts, the queue, why documents failed,
// health, how far along its work is, and the jobs. A panel is nil when the
// page wasn't asked for it: a poll reads, and renders, only the panels its
// live regions show.
type Status struct {
	Layout Layout
	// Poll is the live regions the page was asked for, PollLive or
	// PollHealth, or "" for the whole page.
	Poll     string
	Counts   *CountsPanel
	Queue    *QueuePanel
	Progress *ProgressPanel
	Health   *HealthPanel
	Failures *FailuresPanel
}

// Pollers are the whole page's pollers, none for a poll's answer: one
// refreshes what changes by the second, and after every change the page
// makes; the other, health, which pings Ollama, every 15 seconds.
func (s Status) Pollers() []Poller {
	if s.Poll != "" {
		return nil
	}
	return []Poller{
		{ID: statusPoller, Href: statusPollHref(PollLive), Every: pollEvery, OnChange: true, Regions: statusLiveRegions},
		{ID: healthPoller, Href: statusPollHref(PollHealth), Every: healthPollEvery, Regions: statusHealthRegions},
	}
}

// CountsPanel counts the library and the jobs.
type CountsPanel struct {
	Err       *PanelError
	Documents int
	ByState   []Count // every document state, in lifecycle order
	Bookmarks int
	Jobs      []Count // by status, the statuses with jobs
}

// State is how many documents are in state.
func (p CountsPanel) State(state string) int {
	for _, c := range p.ByState {
		if c.Name == state {
			return c.Count
		}
	}
	return 0
}

// Count is how many there are of Name.
type Count struct {
	Name  string
	Count int
}

// QueuePanel is the queue gate, its settings and the pools' load.
type QueuePanel struct {
	Err      *PanelError
	Open     bool
	Paused   bool
	Reason   string    // why it is closed
	OpensAt  time.Time // when a closed queue opens; zero when unknown
	Throttle string
	Schedule string // HH:MM-HH:MM; empty for none
	Kinds    []KindLoad
	// FinishedIn is the window the pools' Finished counts cover, 0 when
	// they couldn't be read.
	FinishedIn time.Duration
	// KeepAwake is the setting; KeepAwakeActive whether the Mac is held
	// awake now, and PowerSource what it runs on, while the setting is on.
	KeepAwake       bool
	KeepAwakeActive bool
	PowerSource     string
}

// KindLoad is one pool's load, and how many of its jobs finished, done or
// failed, in the panel's FinishedIn. DueLater is how many of its pending
// jobs can't run yet, and NextDue when the first of them can (zero when
// none).
type KindLoad struct {
	Kind                              string
	Running, Limit, Pending, Finished int
	DueLater                          int
	NextDue                           time.Time
}

// Waiting is how many jobs wait in the pools.
func (p QueuePanel) Waiting() int {
	n := 0
	for _, k := range p.Kinds {
		n += k.Pending
	}
	return n
}

// OnlyDueLater reports whether jobs wait, none runs, and every one that
// waits can't run yet: the queue is open, but has nothing to work on
// before NextDue.
func (p QueuePanel) OnlyDueLater() bool {
	later := 0
	for _, k := range p.Kinds {
		if k.Running > 0 {
			return false
		}
		later += k.DueLater
	}
	return later > 0 && later == p.Waiting()
}

// NextDue is when the first job due later in the pools can run; zero when
// none waits for a time.
func (p QueuePanel) NextDue() time.Time {
	var next time.Time
	for _, k := range p.Kinds {
		if !k.NextDue.IsZero() && (next.IsZero() || k.NextDue.Before(next)) {
			next = k.NextDue
		}
	}
	return next
}

// Toggle is the queue's Pause button, or Resume while it is paused.
func (p QueuePanel) Toggle() Action {
	if p.Paused {
		return resumeAction()
	}
	return pauseAction()
}

// ThrottleChoice is one button of the throttle control.
type ThrottleChoice struct {
	ID, Label string
	Pressed   bool
	Action    Action
}

// Throttles are the throttle control's buttons, the setting's pressed.
func (p QueuePanel) Throttles() []ThrottleChoice {
	choices := make([]ThrottleChoice, 0, 2)
	for _, t := range []store.Throttle{store.ThrottleNormal, store.ThrottleGentle} {
		choices = append(choices, ThrottleChoice{ID: "throttle-" + string(t), Label: throttleLabel(t),
			Pressed: p.Throttle == string(t), Action: throttleAction(t)})
	}
	return choices
}

// KeepAwakeSwitch is the keep-awake switch's Action.
func (QueuePanel) KeepAwakeSwitch() Action { return keepAwakeAction() }

// SaveSchedule is the schedule form's Action, and ScheduleOff its Turn off
// button's.
func (QueuePanel) SaveSchedule() Action { return scheduleAction() }

func (QueuePanel) ScheduleOff() Action { return scheduleOffAction() }

// ScheduleStart and ScheduleEnd are the schedule's times, HH:MM, for the
// form's fields: empty without a schedule.
func (p QueuePanel) ScheduleStart() string {
	start, _, _ := strings.Cut(p.Schedule, "-")
	return start
}

func (p QueuePanel) ScheduleEnd() string {
	_, end, _ := strings.Cut(p.Schedule, "-")
	return end
}

// KeepAwakeHint says what keep-awake does now: whether it holds the Mac
// awake and, when it doesn't, why not, in the keeper's order.
func (p QueuePanel) KeepAwakeHint() string {
	switch {
	case !p.KeepAwake:
		return "Off: jobs wait while the Mac sleeps."
	case p.KeepAwakeActive:
		return "Holding the Mac awake while jobs run (AC power)."
	case p.Paused:
		return "On, not holding the Mac awake: the queue is paused."
	case p.queued() == 0:
		return "On, not holding the Mac awake: nothing is queued."
	case p.PowerSource == powerBattery:
		return "On, not holding the Mac awake: it runs on battery."
	case p.PowerSource != powerAC:
		return "On, not holding the Mac awake: its power source is unknown."
	}
	return "On, not holding the Mac awake yet."
}

// The power sources GET /v1/queue reports keep-awake's Mac running on.
const (
	powerAC      = "ac"
	powerBattery = "battery"
)

// queued is how many jobs the pools wait on or run.
func (p QueuePanel) queued() int {
	n := 0
	for _, k := range p.Kinds {
		n += k.Pending + k.Running
	}
	return n
}

// ProgressPanel is the queue's progress estimate, and why a closed queue
// holds the work.
type ProgressPanel struct {
	Err      *PanelError
	Progress Progress
	Reason   string
}

// HealthPanel is the daemon's health and what it depends on.
type HealthPanel struct {
	Err             *PanelError
	Version         string
	OllamaReachable bool
	OllamaDetail    string
	EmbeddingModel  string
	EmbeddingDim    int
	GenerationModel string
	Drift           *Drift // nil when the embeddings haven't drifted
	Upstreams       []Upstream
	YouTubeFetcher  string // the yt-dlp videos go to; empty for none
	// Interests is where automatic rebuilds of the interests stand.
	Interests InterestsState
}

// Drift is what changed in the build that makes the embeddings since the
// library was indexed, the evidence it was reported on, and the command
// that fixes it. Verified is whether a re-embedded sample showed the
// change; Detail is the daemon's wording of the evidence.
type Drift struct {
	Changes  []DriftChange
	Verified bool
	Detail   string
	Fix      string
}

// DriftChange is one changed part of the embedding build.
type DriftChange struct {
	What, Recorded, Current string
}

// Upstream is the health of a service fetches depend on (the Jina Reader
// fallback). Zero times are unset.
type Upstream struct {
	Name             string
	Enabled          bool
	State            string
	LastSuccess      time.Time // its last healthy answer
	LastFailure      time.Time
	LastFailureClass string
	CooldownUntil    time.Time
	// Calls and Failed count its calls in the last Window, and those that
	// failed: its health's own reckoning, which counts a refusal of a
	// target as a healthy answer.
	Window        time.Duration
	Calls, Failed int
}

// Label is the upstream's name for people.
func (u Upstream) Label() string {
	if u.Name == "jina" {
		return "Jina Reader"
	}
	return u.Name
}

// Tone is how well the upstream is doing, for its status dot: ok, warn,
// danger, or neutral when it is off or its state is unknown.
func (u Upstream) Tone() string {
	if !u.Enabled {
		return "neutral"
	}
	switch u.State {
	case "ok", "idle":
		return "ok"
	case "degraded", "paused":
		return "warn"
	case "failing":
		return "danger"
	}
	return "neutral"
}

// Alert is the tone of the callout Status opens with for the upstream:
// danger while it is failing, warn while it is degraded, and "" for none.
// A pause is the upstream's own request, and idle, ok and off need
// nothing.
func (u Upstream) Alert() string {
	if !u.Enabled {
		return ""
	}
	switch u.State {
	case "failing":
		return "danger"
	case "degraded":
		return "warn"
	}
	return ""
}

// FailuresPanel is why the library's documents failed: how many failed or
// are dead, and the causes with the most of them, most first.
type FailuresPanel struct {
	Err    *PanelError
	Total  int
	Causes []Count // by failure cause
}

// Most is the count of the cause with the most documents, which the
// causes' bars are scaled to, or 0 for none.
func (p FailuresPanel) Most() int {
	if len(p.Causes) == 0 {
		return 0
	}
	return p.Causes[0].Count
}

// Search is the search page: the form, and either the home, without a
// query, or a page of the results of its query.
type Search struct {
	Layout Layout
	Query  string
	Type   string // the content type the search is limited to; empty for all
	// Page is the page of results shown, from 1; 0 is the first.
	Page    int
	Home    *SearchHome    // set exactly when there is no query
	Err     *PanelError    // the search failed
	Results *SearchResults // nil without a query, or when it failed
}

// SearchPageSize is how many results a page of search shows. It is fixed,
// not search.default_k, so that the pages tile the ranking of
// store.MaxSearchK documents a search ranks exactly.
const SearchPageSize = 10

// MaxSearchPages is how many pages a search has at most.
const MaxSearchPages = store.MaxSearchK / SearchPageSize

func (s Search) page() int { return max(s.Page, 1) }

// Pages is how many pages the results fill: none when nothing matched.
func (s Search) Pages() int {
	if s.Results == nil {
		return 0
	}
	return pageCount(s.Results.Total, SearchPageSize)
}

// OutOfRange is the page when it is past the results' last, nil otherwise,
// and nil when nothing matched, which the page says instead.
func (s Search) OutOfRange() *PageOutOfRange {
	if s.Pages() == 0 {
		return nil
	}
	return outOfRange(s.page(), s.Pages(), s.pageHref)
}

// CappedNote reports whether the page says the ranking stops at the cap:
// on the last page of a search that matched more than it ranks.
func (s Search) CappedNote() bool {
	return s.Results != nil && s.Results.Capped && s.page() == s.Pages()
}

// Pager is the pager under the results, nil when they fit on one page or
// the page is past the last.
func (s Search) Pager() *Pager {
	if s.Results == nil {
		return nil
	}
	return newPager(pageSpan{Page: s.page(), Size: SearchPageSize, Shown: len(s.Results.Hits), Total: s.Results.Total,
		Noun: "Results", Href: s.pageHref})
}

// pageHref is page of the query's results, limited to the type shown.
func (s Search) pageHref(page int) string { return searchHref(s.Query, s.Type, page) }

// SearchHome is what the search page shows without a query, under the
// box. Zero values are unknown: a read the home does without is left out.
type SearchHome struct {
	Searchable int // fetched documents
	// Interests are the latest rebuild's largest top-level groups (areas,
	// or interests in the flat shape), largest first, without members;
	// AllInterests how many it has.
	Interests    []Interest
	AllInterests int
}

// Placeholder is the search box's placeholder: how many documents there
// are to search, when the home knows.
func (s Search) Placeholder() string {
	if s.Home == nil || s.Home.Searchable <= 0 {
		return "Search your library"
	}
	return "Search your " + count(s.Home.Searchable, "document", "documents")
}

// searchTypes are the content types the search page offers to limit a
// search to, in the order it offers them; "" is all of them.
var searchTypes = []struct {
	label       string
	contentType store.ContentType
}{
	{"All", ""}, {"Articles", store.ContentTypeArticle}, {"Repos", store.ContentTypeRepo},
	{"Videos", store.ContentTypeVideo}, {"PDFs", store.ContentTypePDF},
}

// Tab is one link of a segmented control: its label, the page it leads
// to, whether it is the page's current choice, and its count when it has
// one.
type Tab struct {
	Label   string
	Href    string
	Current bool
	Count   int
	Counted bool
}

// TypeTabs are the search's type filter: a link to the first page of this
// query limited to each offered type, the current one marked. A type the
// page doesn't offer (thread, unknown) still limits the search, and marks
// none.
func (s Search) TypeTabs() []Tab {
	tabs := make([]Tab, 0, len(searchTypes))
	for _, t := range searchTypes {
		tabs = append(tabs, Tab{Label: t.label, Href: searchHref(s.Query, string(t.contentType), 1),
			Current: s.Type == string(t.contentType)})
	}
	return tabs
}

// SearchResults is a page of a query's ranked hits.
type SearchResults struct {
	Degraded bool     // semantic search was unavailable: keyword results only
	Warnings []string // why
	TookMS   int64
	// Total is how many documents the query's ranking holds, at most
	// store.MaxSearchK, and Capped whether more matched than it ranks.
	Total  int
	Capped bool
	Hits   []SearchHit // the page's
}

// Count is how many documents match, as the results' head counts them:
// "1 document", "37 documents", or, past the cap, "100+ documents".
func (r SearchResults) Count() string {
	if r.Capped {
		return num(r.Total) + "+ documents"
	}
	return count(r.Total, "document", "documents")
}

// Verb agrees with Count.
func (r SearchResults) Verb() string {
	if r.Total == 1 && !r.Capped {
		return "matches"
	}
	return "match"
}

// SearchHit is one ranked document and the chunks that matched in it.
type SearchHit struct {
	DocumentID string
	Title      string // empty for an untitled document
	// BookmarkTitle names an untitled document: its newest titled
	// bookmark's title.
	BookmarkTitle string
	URL           string
	ContentType   string
	Score         float64 // the fused score
	Matches       []Match
}

// Ref names the hit as a list names a document: by its title, by its
// bookmark's, or by its short URL.
func (h SearchHit) Ref() DocRef {
	return DocRef{ID: h.DocumentID, Title: h.Title, Fallback: h.BookmarkTitle, URL: h.URL}
}

// MatchKind says which retrievers found the hit's matching chunks, in
// words: keyword search's words, the vector search's meaning, or both; ""
// when it has no matches.
func (h SearchHit) MatchKind() string {
	keyword, meaning := false, false
	for _, m := range h.Matches {
		keyword = keyword || m.BM25 != nil
		meaning = meaning || m.Vector != nil
	}
	switch {
	case keyword && meaning:
		return "keyword + meaning"
	case keyword:
		return "keyword only"
	case meaning:
		return "meaning only"
	}
	return ""
}

// shownMatches is how many of a hit's matches its result shows; the rest
// are a click away.
const shownMatches = 2

// ShownMatches are the matches the result shows.
func (h SearchHit) ShownMatches() []Match { return h.Matches[:min(len(h.Matches), shownMatches)] }

// MoreMatches are the matches after ShownMatches, none when there are
// no more.
func (h SearchHit) MoreMatches() []Match {
	if len(h.Matches) <= shownMatches {
		return nil
	}
	return h.Matches[shownMatches:]
}

// Match is a matching chunk: its passage (Passage), with each retriever's
// score when it returned the chunk.
type Match struct {
	Segments []Segment
	BM25     *float64
	Vector   *float64
}

// Scores are the match's retriever scores, for Show scores: "bm25 22.523 ·
// vector 0.710", or the one a retriever gave.
func (m Match) Scores() string {
	var parts []string
	if m.BM25 != nil {
		parts = append(parts, "bm25 "+score(m.BM25))
	}
	if m.Vector != nil {
		parts = append(parts, "vector "+score(m.Vector))
	}
	return strings.Join(parts, " · ")
}

// Library is a page of the library under the filters: documents, most
// recently updated first, or in the Date saved order saves, newest saved
// first.
type Library struct {
	Layout     Layout
	Filters    LibraryFilters
	Counts     *LibraryCounts // nil when they couldn't be read
	Rows       []LibraryRow
	NextCursor string // empty on the last page
	PageSize   int    // how many rows a page holds
	// Shown is how many rows the pages before this one showed above it:
	// set when "load more" appends this page to them, 0 on a page of its
	// own.
	Shown int
}

// LibraryCounts are the whole library's counts, whatever the filters.
type LibraryCounts struct {
	Documents, Bookmarks int
	ByState              map[string]int // documents by state
}

// Head is the Library's head: its lede counts the library, and its subnav
// leads to the Failures tab with how many documents failed or are dead,
// both from the counts, whatever the filters.
func (l Library) Head() LibraryHead {
	views := LibraryViews{}
	if c := l.Counts; c != nil {
		views.Failed = c.ByState[string(store.DocStateFailed)] + c.ByState[string(store.DocStateDead)]
		views.Counted = true
	}
	return LibraryHead{Counts: l.Counts, Views: views}
}

// LibraryHead heads the Library's two views, Documents and Failures (the
// library-head partial): the library's counts for the lede, nil when they
// couldn't be read, and the subnav between the views.
type LibraryHead struct {
	Counts *LibraryCounts
	Views  LibraryViews
}

// LibraryViews is the Library's subnav (the library-subnav partial): which
// view is current, Documents or Failures, and how many documents failed or
// are dead, the Failures tab's count, when it is known.
type LibraryViews struct {
	OnFailures bool
	Failed     int
	Counted    bool
}

// libraryStates are the Library's state tabs, in their order; "" is every
// state.
var libraryStates = []struct {
	label string
	state store.DocState
}{
	{"All", ""}, {"Fetched", store.DocStateFetched}, {"Pending", store.DocStatePending},
	{"Failed", store.DocStateFailed}, {"Dead", store.DocStateDead},
}

// StateTabs are the Library's state filter: a link to the first page of
// each state under the page's other filters, the current one marked, each
// with its count when the counts apply.
func (l Library) StateTabs() []Tab {
	counted := l.CountsApply()
	tabs := make([]Tab, 0, len(libraryStates))
	for _, s := range libraryStates {
		tab := Tab{Label: s.label, Href: stateTabHref(l.Filters, string(s.state)),
			Current: l.Filters.State == string(s.state)}
		if counted {
			tab.Count, tab.Counted = l.Counts.state(string(s.state)), true
		}
		tabs = append(tabs, tab)
	}
	return tabs
}

// state is how many documents are in state, or in any for "".
func (c LibraryCounts) state(state string) int {
	if state == "" {
		return c.Documents
	}
	return c.ByState[state]
}

// CountsApply reports whether the library's counts count what the page
// lists: they are the whole library's by state, so they apply when state
// is the one filter, if any.
func (l Library) CountsApply() bool {
	f := l.Filters
	return l.Counts != nil && f.ContentType == "" && f.Host == "" && f.Folder == "" && f.Cause == ""
}

// ShownThrough is how many rows the page shows through this one: those
// the pages before it showed, and its own.
func (l Library) ShownThrough() int { return l.Shown + len(l.Rows) }

// OfTotal is how many rows the rows shown are of, for "Showing N of M":
// in the Date saved order the library's bookmarks, with no filter at all,
// and otherwise the current state's documents, when the counts apply. It
// is 0 when there is no such count, or it falls behind the rows shown, as
// it does when the library changed between pages.
func (l Library) OfTotal() int {
	var total int
	switch saved := l.Filters.Saved(); {
	case saved && l.Counts != nil && !l.Filters.Filtered():
		total = l.Counts.Bookmarks
	case !saved && l.CountsApply():
		total = l.Counts.state(l.Filters.State)
	default:
		return 0
	}
	if l.ShownThrough() > total {
		return 0
	}
	return total
}

// The Library's orders, as its order parameter names them: Last updated,
// the default, lists documents, and Date saved lists saves. Links leave
// the default out.
const (
	OrderUpdated = "updated"
	OrderSaved   = "saved"
)

// LibraryFilters are a Library page's query: its order, and the filters
// GET /v1/documents and GET /v1/bookmarks take. Empty fields filter
// nothing; Limit 0 is the default page size. The order is no filter.
type LibraryFilters struct {
	Order                                   string // "" for Last updated, or OrderSaved
	State, ContentType, Host, Folder, Cause string
	Limit                                   int
}

// Saved reports whether the page lists saves, in the Date saved order.
func (f LibraryFilters) Saved() bool { return f.Order == OrderSaved }

// Filter is one filter a Library page applies: its name and value.
type Filter struct {
	Name, Value string
}

// Active are the filters that filter something, in the form's order.
func (f LibraryFilters) Active() []Filter {
	var active []Filter
	for _, filter := range []Filter{{"state", f.State}, {"type", f.ContentType}, {"host", f.Host},
		{"folder", f.Folder}, {"cause", f.Cause}} {
		if filter.Value != "" {
			active = append(active, filter)
		}
	}
	return active
}

// Filtered reports whether any filter filters something.
func (f LibraryFilters) Filtered() bool { return len(f.Active()) > 0 }

// LibraryRow is one row of the list: a document, or in the Date saved
// order a save and its document. LastError and FailureCause are set only
// while the document is failed or dead.
type LibraryRow struct {
	DocumentID string // empty for a save linked to no document
	Title      string // the document's; empty for an untitled document
	// BookmarkTitle names an untitled document: its newest titled
	// bookmark's title, or in the Date saved order the save's own.
	BookmarkTitle string
	URL           string
	State         string // the document's; empty for a save linked to none
	ContentType   string
	When          time.Time // the time column's: when the document was updated, or the page saved
	Source        string    // the browser a save came from; empty for a document
	Folder        string    // the folder it was saved in
	LastError     string
	FailureCause  string
}

// Cell is the row's document cell: its name, where it lives under it,
// and, for a save, the browser it came from.
func (r LibraryRow) Cell() DocCell {
	ref := DocRef{ID: r.DocumentID, Title: r.Title, Fallback: r.BookmarkTitle, URL: r.URL}
	return DocCell{Ref: ref, Where: ref.where(), Source: r.Source, Folder: r.Folder, ContentType: r.ContentType,
		When: r.When, FailureCause: r.FailureCause, LastError: r.LastError}
}

// DocRef names a document in a list (the doc-title partial): by its
// title; while it has none, by Fallback, a bookmark's title, styled as
// one; or by its short URL. It links to its page when it has an ID.
type DocRef struct {
	ID, Title, URL string
	Fallback       string
}

// Named reports whether a title or a fallback names the document, rather
// than its address.
func (r DocRef) Named() bool { return r.Title != "" || r.Fallback != "" }

// Name is what a list calls the document: its title, its fallback, or its
// short URL.
func (r DocRef) Name() string {
	if r.Named() {
		return cmp.Or(r.Title, r.Fallback)
	}
	return shortURL(r.URL)
}

// Hover is the whole of its name, shown on hover: its title, its
// fallback, or its URL.
func (r DocRef) Hover() string { return cmp.Or(r.Title, r.Fallback, r.URL) }

// Class is the class its name carries besides doc-title: from-bookmark
// for a fallback, untitled for its address, and none for its title.
func (r DocRef) Class() string {
	switch {
	case r.Title != "":
		return ""
	case r.Fallback != "":
		return "from-bookmark"
	}
	return "untitled"
}

// where is what a list shows under its name: its short URL when a title or
// a fallback names it, or its host when its address already does.
func (r DocRef) where() string {
	if r.Named() {
		return shortURL(r.URL)
	}
	return host(r.URL)
}

// DocCell is a document as a list's first column shows it (the doc-cell
// partial): its name, where it lives, what the row's other columns show
// where a phone folds them away, and, for a failed one, why.
type DocCell struct {
	Ref   DocRef
	Where string // under its name: where it lives
	// Source is the browser a save came from, and Folder the folder it was
	// saved in, for a list of saves; a list of documents leaves both empty.
	Source       string
	Folder       string
	ContentType  string    // shown here on a phone, whose table has no Type column
	When         time.Time // shown here on a phone, whose table has no time column
	FailureCause string
	LastError    string
}

// Failures is the Library's Failures tab: the library's failed and dead
// documents grouped by why they failed, from one read of the failure
// summary. A poll's answer (Poll PollCauses) holds its live regions alone,
// the Library's subnav and the groups, and reads the summary alone.
type Failures struct {
	Layout Layout
	Poll   string
	// Counts are the library's, for the head's lede: nil when they
	// couldn't be read, and in a poll's answer, which has no lede.
	Counts *LibraryCounts
	Err    *PanelError // the summary couldn't be read
	Total  int         // failed and dead documents
	Groups []FailureGroup
}

// Head is the Failures tab's head: the Library's, its subnav counting the
// summary's documents, which it knows exactly when the summary was read.
func (f Failures) Head() LibraryHead { return LibraryHead{Counts: f.Counts, Views: f.Views()} }

// Views is the Library's subnav as the Failures tab shows it, current,
// counting the summary's documents when it was read.
func (f Failures) Views() LibraryViews {
	if f.Err != nil {
		return LibraryViews{OnFailures: true}
	}
	return LibraryViews{OnFailures: true, Failed: f.Total, Counted: true}
}

// Dead is how many of the documents are dead links.
func (f Failures) Dead() int {
	for _, g := range f.Groups {
		if g.DeadLinks() {
			return g.Count
		}
	}
	return 0
}

// Failed is how many of the documents failed for a cause other than a
// dead link.
func (f Failures) Failed() int { return f.Total - f.Dead() }

// Totals is the sentence over the groups: how many documents couldn't be
// fetched, how many of them failed and how many are dead links when there
// are both, and what a group's refetch does.
func (f Failures) Totals() string {
	s := count(f.Total, "document", "documents") + " couldn't be fetched"
	if failed, dead := f.Failed(), f.Dead(); failed > 0 && dead > 0 {
		s += ": " + num(failed) + " failed and " + count(dead, "dead link", "dead links")
	}
	return s + ", grouped by why. Refetching a group queues a fresh fetch for each of its documents."
}

// Absent are the causes no document failed for, in store.FailureCauses'
// order, for the footnote under the groups.
func (f Failures) Absent() []string {
	var absent []string
	for _, cause := range store.FailureCauses() {
		if !slices.ContainsFunc(f.Groups, func(g FailureGroup) bool { return g.Cause == string(cause) }) {
			absent = append(absent, string(cause))
		}
	}
	return absent
}

// Pollers are the page's one poller, none for a poll's answer: it
// refreshes the groups and the subnav's count after every change the
// page makes and when the tab comes back into view, never on a timer,
// since the summary reads every failed document.
func (f Failures) Pollers() []Poller {
	if f.Poll != "" {
		return nil
	}
	return []Poller{{ID: failuresPoller, Href: failuresPollHref(), OnChange: true, Regions: failuresRegions}}
}

// FailureGroup is the documents that failed for one cause, and the hosts
// most of them are on, most first, each with how many of the cause's
// documents it holds. A cause this build doesn't know, one a newer daemon
// wrote, is shown by its code, and neither the Library nor refetch-all
// takes it.
type FailureGroup struct {
	Cause string
	Count int
	Hosts []Count
}

// ID is the id of the group's card, which Status's rows lead to.
func (g FailureGroup) ID() string { return causeCardID(g.Cause) }

// Known reports whether the cause is one this build knows, which the
// group's links and refetch need.
func (g FailureGroup) Known() bool { return store.FailureCause(g.Cause).Valid() }

// DeadLinks reports whether the group is the dead links, which are
// refetched only after a confirm.
func (g FailureGroup) DeadLinks() bool { return g.Cause == string(store.FailureCauseDeadLink) }

// Refetch is the group's refetch of a known cause: the dead links', which
// names their state, or the cause's.
func (g FailureGroup) Refetch() Action {
	if g.DeadLinks() {
		return refetchDeadLinksAction(g.Count)
	}
	return refetchCauseAction(g.Cause, g.Count)
}

// Command is the command that does what the group's Refetch does, for a
// page without JavaScript: "" for a cause this build doesn't know.
func (g FailureGroup) Command() string {
	switch {
	case !g.Known():
		return ""
	case g.DeadLinks():
		return "curio refetch --all --state dead --cause " + g.Cause
	}
	return "curio refetch --all --cause " + g.Cause
}

// Document is one document's page. A poll's answer (Poll PollJobs) holds
// its live regions alone, Jobs and their poller, and reads nothing else.
type Document struct {
	Layout     Layout
	Poll       string
	Meta       DocumentMeta
	Extraction *Extraction // nil before its first fetch
	// LastError is its most recent failed job's error, and FailureCause
	// why it failed, set only while the document is failed or dead: an
	// older failure a refetch recovered from isn't current.
	LastError    string
	FailureCause string
	Text         TextPanel
	Related      RelatedPanel
	Bookmarks    BookmarksPanel
	Jobs         DocumentJobs
	// Place is where the latest rebuild put it, or placed it since; nil
	// when it has it nowhere, before the first, or when that couldn't be
	// read.
	Place *DocumentPlace
}

// Refetch is the document's Refetch button; ForcedRefetch is the one that
// refetches a dead link, behind its confirm.
func (d Document) Refetch() Action { return refetchAction(d.Meta.ID) }

func (d Document) ForcedRefetch() Action { return forcedRefetchAction(d.Meta.ID) }

// Reindex is the document's Reindex button.
func (d Document) Reindex() Action { return reindexAction(d.Meta.ID) }

// DocumentJobs is a document's live region: its jobs in flight, and what
// came of them since the page was rendered. Baseline is what the page
// shows, Current what the library holds now; a page's own render has them
// equal.
type DocumentJobs struct {
	DocumentID string
	State      string // the document's, now
	Baseline   DocumentBaseline
	Current    DocumentBaseline
	Err        *PanelError // the jobs couldn't be read
	Jobs       []JobLine   // queued and running, newest first
	// AttemptLimit is how many attempts a job gets.
	AttemptLimit int
	// Hold is why the queue holds the queued jobs, the gate's reason, or ""
	// while it is open or unknown.
	Hold string
}

// InFlight reports whether a job of the document is queued or running.
func (j DocumentJobs) InFlight() bool { return len(j.Jobs) > 0 }

// Queued reports whether a job of the document waits in the queue.
func (j DocumentJobs) Queued() bool {
	for _, l := range j.Jobs {
		if !l.Running {
			return true
		}
	}
	return false
}

// DocumentOutcome is what came of a document's jobs since its page was
// rendered, for the page to offer a reload.
type DocumentOutcome string

// What came of a document's jobs.
const (
	OutcomeNone        DocumentOutcome = ""
	OutcomeTextChanged DocumentOutcome = "text-changed" // a new extraction is current
	OutcomeFailed      DocumentOutcome = "failed"       // it failed, or is dead
	OutcomeUpdated     DocumentOutcome = "updated"      // anything else changed
)

// Outcome is what came of the document's jobs: a new text as soon as it is
// current; otherwise, once no job is in flight, a failure or an update when
// the document changed.
func (j DocumentJobs) Outcome() DocumentOutcome {
	switch {
	case j.Current.Extraction != j.Baseline.Extraction:
		return OutcomeTextChanged
	case j.Err != nil || j.InFlight() || j.Current.Updated.Equal(j.Baseline.Updated):
		return OutcomeNone
	case j.State == string(store.DocStateFailed) || j.State == string(store.DocStateDead):
		return OutcomeFailed
	}
	return OutcomeUpdated
}

// Poller is the document's poller: every 2 seconds while a job is in
// flight, the document waits for one, or its jobs couldn't be read (a read
// that failed says nothing about whether one runs), and after every change
// the page makes, carrying the page's baseline. It lists itself, so its
// answer decides whether it keeps polling.
func (j DocumentJobs) Poller() Poller {
	p := Poller{ID: documentPoller, Href: documentPollHref(j.DocumentID, j.Baseline), OnChange: true,
		Regions: []string{documentJobsID, documentPoller}}
	if j.Err != nil || j.InFlight() || j.State == string(store.DocStatePending) {
		p.Every = pollEvery
	}
	return p
}

// JobLine is one of a document's jobs in flight: its kind, whether it
// runs, is queued or waits for a time to come, and the attempts it has
// used. A waiting job is pending with a run_after still ahead when the jobs
// were read: a retry backing off, or a job deferred for a hold. Its
// LastError says why: the error of the attempt it retries after, or what
// a deferral waits for.
type JobLine struct {
	Kind      string
	Running   bool
	Waiting   bool
	Attempts  int
	RunAfter  time.Time
	LastError string
}

// Attempt is the attempt the job is on, running, or waits to make.
func (l JobLine) Attempt() int {
	if l.Running {
		return l.Attempts
	}
	return l.Attempts + 1
}

// DocumentMeta is what the library knows about a document. Zero values are
// unknown.
type DocumentMeta struct {
	ID    string
	Title string
	// BookmarkTitle names an untitled document: its newest titled
	// bookmark's title. Empty for a titled document, or one without.
	BookmarkTitle string
	URL           string
	CanonicalURL  string
	ContentType   string
	State         string
	Author        string
	Language      string
	PublishedAt   time.Time
	WordCount     int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	MarkdownPath  string // absolute; empty when it has none
}

// Extraction is how the document's current text was fetched.
type Extraction struct {
	Fetcher      string
	Via          string // what served it: readability, jina, pdf-local, github-api, yt-dlp
	Status       string
	ErrorMessage string
	FetchedAt    time.Time
}

// TextState is what a document page can show of its text.
type TextState string

const (
	// TextShown: the rendered text.
	TextShown TextState = "shown"
	// TextNotFetched: the document has no text yet.
	TextNotFetched TextState = "not-fetched"
	// TextMissing: its markdown was deleted from disk.
	TextMissing TextState = "missing"
)

// TextPanel is a document's rendered text.
type TextPanel struct {
	Err   *PanelError
	State TextState
	Text  Text
	// Truncated: only the first MaxRenderedMarkdown bytes are shown; the
	// whole text is at MarkdownPath.
	Truncated    bool
	MarkdownPath string
	// OfferImages shows the link that reloads the page with its remote
	// images.
	OfferImages bool
}

// RelatedPanel is the documents most like this one.
type RelatedPanel struct {
	Err  *PanelError
	Docs []RelatedDoc
}

// RelatedDoc is one related document and its similarity.
type RelatedDoc struct {
	DocumentID string
	Title      string
	// BookmarkTitle names an untitled document: its newest titled
	// bookmark's title.
	BookmarkTitle string
	URL           string
	Score         float64
}

// Ref names the document as a list names one.
func (d RelatedDoc) Ref() DocRef {
	return DocRef{ID: d.DocumentID, Title: d.Title, Fallback: d.BookmarkTitle, URL: d.URL}
}

// BookmarksPanel is the document's bookmarks.
type BookmarksPanel struct {
	Err       *PanelError
	Bookmarks []DocumentBookmark
}

// DocumentBookmark is one bookmark of the document.
type DocumentBookmark struct {
	Source  string
	Folder  string
	Title   string
	Tags    []string
	SavedAt time.Time
}

// Interests is a page of the latest rebuild's top-level groups, its areas
// each with its largest interests, or its interests in the flat shape;
// Unsorted's card after the last page's; and a rebuild of them. A poll's
// answer (Poll PollRebuild) holds the rebuild's live regions alone, and
// reads no interest.
type Interests struct {
	Layout Layout
	Poll   string
	// Page is the page shown, from 1; 0 is the first.
	Page int
	// RunChanged is set when the page was asked for from a page of another
	// run: the interests were rebuilt since, and this page is the newer
	// run's.
	RunChanged bool
	Run        *InterestRun // nil before the first rebuild is done
	Interests  []Interest   // the page's groups: areas, or flat interests
	Rebuild    Rebuild
}

// Page sizes of the numbered lists of interests (the Interests' and an
// area's) and of an interest's members, or of Unsorted. 24 cards fill rows
// of 3, 2 and 1.
const (
	InterestsPageSize       = 24
	InterestMembersPageSize = 50
)

// Card counts: an area's card names its largest interests, and an
// interest's its most similar members.
const (
	AreaCardInterests   = 5
	InterestCardMembers = 3
)

// ChangesNoticeFor is how long after a rebuild the Interests page tells
// what it split, merged or dissolved.
const ChangesNoticeFor = 7 * 24 * time.Hour

func (v Interests) page() int { return max(v.Page, 1) }

// Pages is how many pages the run's top-level groups fill.
func (v Interests) Pages() int {
	if v.Run == nil {
		return 0
	}
	return pageCount(v.Run.Total, InterestsPageSize)
}

// OutOfRange is the page when it is past the last, nil otherwise.
func (v Interests) OutOfRange() *PageOutOfRange {
	return outOfRange(v.page(), v.Pages(), v.pageHref)
}

// Pager is the pager under the cards, nil when they fit on one page.
func (v Interests) Pager() *Pager {
	if v.Run == nil {
		return nil
	}
	noun := "Interests"
	if v.Run.HasAreas() {
		noun = "Areas"
	}
	return newPager(pageSpan{Page: v.page(), Size: InterestsPageSize, Shown: len(v.Interests), Total: v.Run.Total,
		Noun: noun, Href: v.pageHref})
}

// FirstGrouping reports whether the library is being grouped for the
// first time: no rebuild is done, and one is queued or running, as the
// queue or the scheduler says.
func (v Interests) FirstGrouping() bool {
	return v.Run == nil && (v.Rebuild.InFlight() || v.Rebuild.State.InFlight())
}

// FirstPage is the first page of the run shown.
func (v Interests) FirstPage() string { return v.pageHref(1) }

// Unsorted is Unsorted's card, last on the last page of the groups, or
// alone on a run without any; nil on the other pages, past the last, and
// when nothing is unsorted or placed there since.
func (v Interests) Unsorted() *UnsortedCard {
	r := v.Run
	if r == nil || r.Unsorted+r.NewUnsorted <= 0 || v.OutOfRange() != nil || v.page() < v.Pages() {
		return nil
	}
	return &UnsortedCard{Documents: r.Unsorted, New: r.NewUnsorted, Href: unsortedHref(1, r.ID)}
}

// pageHref is page of the run shown: every link between pages names the
// run, so that a page asked for after a rebuild knows it changed.
func (v Interests) pageHref(page int) string {
	run := ""
	if v.Run != nil {
		run = v.Run.ID
	}
	return interestsPageHref(page, run)
}

// UnsortedCard is the Interests' card of the documents in no interest: how
// many the run left unsorted, how many were placed there since, and its
// page.
type UnsortedCard struct {
	Documents, New int
	Href           string
}

// InterestsState is where automatic rebuilds of the interests stand, as
// the scheduler's last check found them: one of its states, or off with
// insight off; a state this build doesn't know reads as current. The
// interests-state partial says it as `curio status` does. Changed and
// RebuildAt are the documents changed since the rebuild and how many make
// the next one due; before the first, the documents indexed and how many
// the first waits for. The fields a state doesn't use are zero.
type InterestsState struct {
	State         string
	LastRebuildAt time.Time
	LastKind      string
	Changed       int
	RebuildAt     int
	FreshOwed     string
	HeldReason    string
	RetryAt       time.Time
	LastError     string
}

// The states automatic rebuilds are in (insight.RebuildState), and off.
const (
	rebuildsRebuilding = "rebuilding"
	rebuildsQueued     = "queued"
	rebuildsHeld       = "held"
	rebuildsFailing    = "failing"
	rebuildsDue        = "due"
	rebuildsCurrent    = "current"
	rebuildsNone       = "none"
	rebuildsUnknown    = "unknown"
	rebuildsOff        = "off"
)

// InFlight reports whether the scheduler saw a rebuild queued or running.
func (s InterestsState) InFlight() bool {
	return s.State == rebuildsQueued || s.State == rebuildsRebuilding
}

// Retrying reports whether a failed rebuild waits for its backoff, which
// ends at RetryAt; past it, the next waits for the library to settle.
func (s InterestsState) Retrying() bool { return s.RetryAt.After(time.Now()) }

// Tone is the state's status dot: ok while rebuilds run on their own,
// warn while they are held or failing, neutral when they are off, the
// daemon hasn't checked, or the state is one this build doesn't know.
func (s InterestsState) Tone() string {
	switch s.State {
	case rebuildsCurrent, rebuildsNone, rebuildsDue, rebuildsQueued, rebuildsRebuilding:
		return "ok"
	case rebuildsHeld, rebuildsFailing:
		return "warn"
	}
	return "neutral"
}

// Rebuild is the Interests page's rebuild: whether the page offers one,
// whether one is queued or running, as the queue says, where automatic
// rebuilds stand otherwise, as the scheduler says, and, in a poll's
// answer, whether a newer rebuild is done than the one the page shows.
type Rebuild struct {
	Enabled bool        // config.yaml's insight.enabled
	Err     *PanelError // the queue, or a poll's latest run, couldn't be read
	Queued  bool
	Running bool
	// StartedAt is when the running rebuild started; zero when unknown.
	StartedAt time.Time
	// Hold is why the queue holds a queued rebuild, the gate's reason; ""
	// while it is open.
	Hold string
	// State is the scheduler's snapshot.
	State InterestsState
	// Shown is the run the page shows, "" for none: the latest done one.
	Shown string
	// Ready is set in a poll's answer when the latest done run isn't
	// Shown: newer interests are ready to load.
	Ready bool
}

// InFlight reports whether the queue holds a rebuild, queued or running.
// The queue, not the scheduler's snapshot, says so: the queue is read
// after a click's rebuild is queued, and the snapshot only at the
// scheduler's next check.
func (b Rebuild) InFlight() bool { return b.Queued || b.Running }

// ShowsState reports whether the rebuild's line says where automatic
// rebuilds stand: nothing is in flight and the snapshot has something to
// say. A snapshot of a rebuild queued or running that the queue no longer
// holds is behind it; unknown and off say nothing here.
func (b Rebuild) ShowsState() bool {
	if b.InFlight() {
		return false
	}
	switch b.State.State {
	case rebuildsQueued, rebuildsRebuilding, rebuildsUnknown, rebuildsOff:
		return false
	}
	return true
}

// Action is the Rebuild button's.
func (Rebuild) Action() Action { return rebuildAction() }

// Poller is the rebuild's poller: every 2 seconds while a rebuild is in
// flight, as the queue or the scheduler says, or a read failed; every 30
// seconds otherwise while insight is on, so that a rebuild the scheduler
// queues reaches an open page; and after every change the page makes,
// carrying the run the page shows. It lists itself, so its answer decides
// how it keeps polling.
func (b Rebuild) Poller() Poller {
	p := Poller{ID: rebuildPoller, Href: interestsPollHref(b.Shown), OnChange: true,
		Regions: []string{rebuildStateID, rebuildControlID, rebuildPoller}}
	switch {
	case b.Err != nil || b.InFlight() || b.State.InFlight():
		p.Every = pollEvery
	case b.Enabled:
		p.Every = idlePollEvery
	}
	return p
}

// InterestRun is the rebuild the interests come from.
type InterestRun struct {
	ID         string
	ComputedAt time.Time
	Kind       string // fresh or warm
	Shape      string // flat or areas
	Documents  int
	Areas      int
	Interests  int
	Loose      int // documents in no interest, close to one
	Unsorted   int // documents close to none
	// New counts the documents placed since the rebuild, and NewUnsorted
	// those of them placed into Unsorted.
	New, NewUnsorted int
	// Total is how many top-level groups it has: its areas, or its
	// interests in the flat shape.
	Total int
	// Changes are what it did to the interests before it, and Recent is
	// set while it finished less than ChangesNoticeFor ago.
	Changes RunChanges
	Recent  bool
}

// HasAreas reports whether the run groups its interests in areas.
func (r InterestRun) HasAreas() bool { return r.Shape == string(store.InterestShapeAreas) }

// Members is how many of the run's documents are members of an interest:
// its loose fits and the unsorted aside.
func (r InterestRun) Members() int { return max(r.Documents-r.Loose-r.Unsorted, 0) }

// The tones of the coverage bar's parts, each a fill- and swatch- class
// over app.css's tokens: the accent, its border's, the neutral dot and the
// bar's track.
const (
	toneAccent     = "accent"
	toneAccentSoft = "accent-soft"
	toneNeutral    = "neutral"
	toneTrack      = "track"
)

// CoveragePart is a part of the coverage bar and its legend: how many
// documents it counts, what it calls them, and its tone.
type CoveragePart struct {
	Count     int
	One, Many string
	Tone      string
}

// Label is what the part calls its count.
func (p CoveragePart) Label() string {
	if p.Count == 1 {
		return p.One
	}
	return p.Many
}

// Coverage are the parts of the run's documents and of those placed since,
// in the bar's order: members, loose fits, new, and unsorted, which the
// track is left to show.
func (r InterestRun) Coverage() []CoveragePart {
	return []CoveragePart{
		{Count: r.Members(), One: "in an interest", Many: "in an interest", Tone: toneAccent},
		{Count: r.Loose, One: "loose fit", Many: "loose fits", Tone: toneAccentSoft},
		{Count: r.New, One: "new since the rebuild", Many: "new since the rebuild", Tone: toneNeutral},
		{Count: r.Unsorted, One: "unsorted", Many: "unsorted", Tone: toneTrack},
	}
}

// CoverageTotal is what the bar's parts are shares of: the run's documents
// and those placed since.
func (r InterestRun) CoverageTotal() int { return r.Documents + r.New }

// Bar is the coverage bar.
func (r InterestRun) Bar() []BarSegment { return coverageBar(r.Coverage(), r.CoverageTotal()) }

// Legend is the bar's legend, in its order: the members and the unsorted,
// and the loose fits and new documents when there are any.
func (r InterestRun) Legend() []CoveragePart {
	var out []CoveragePart
	for _, p := range r.Coverage() {
		if p.Count > 0 || p.Tone == toneAccent || p.Tone == toneTrack {
			out = append(out, p)
		}
	}
	return out
}

// ShowsChanges reports whether the page tells what the run changed: it
// finished less than ChangesNoticeFor ago, and split, merged or dissolved
// an interest.
func (r InterestRun) ShowsChanges() bool { return r.Recent && r.Changes.Notable() }

// RunChanges is what a rebuild did to the interests before it: how many it
// split, merged, moved and dissolved, and how many it created; and to how
// many it did nothing but keep them.
type RunChanges struct {
	Kept, Split, Merged, Moved, Dissolved, Created int
}

// Notable reports whether the rebuild split, merged or dissolved an
// interest: names the reader knew changed.
func (c RunChanges) Notable() bool { return c.Split+c.Merged+c.Dissolved > 0 }

// Summary counts what the rebuild changed, each count only when it has
// one: "5 interests split, 1 merged, 2 moved, 9 new"; "" for nothing.
func (c RunChanges) Summary() string {
	var parts []string
	for _, n := range []struct {
		count int
		what  string
	}{{c.Split, "split"}, {c.Merged, "merged"}, {c.Dissolved, "dissolved"}, {c.Moved, "moved"}, {c.Created, "new"}} {
		switch {
		case n.count <= 0:
		case len(parts) == 0:
			parts = append(parts, count(n.count, "interest", "interests")+" "+n.what)
		default:
			parts = append(parts, num(n.count)+" "+n.what)
		}
	}
	return strings.Join(parts, ", ")
}

// Interest is an area or an interest, and some of its interests or its
// members, and on its own page what the latest rebuild did to it.
type Interest struct {
	ID          string
	Area        bool   // an area, which holds interests; an interest otherwise
	ParentID    string // an interest's area, "" for none
	ParentLabel string
	Label       string // empty while unlabeled
	Summary     string
	Size        int // members: an area's interests'
	Loose       int // loose fits: an area's interests'
	New         int // documents placed since the rebuild: an area's interests'
	Cohesion    float64
	NumChildren int        // an area's interests
	Children    []Interest // some of an area's interests, largest first
	Members     []Member
	// NewMembers are the newest of the documents placed into an interest
	// since the rebuild.
	NewMembers []Member
	// Events are what the latest rebuild did to it.
	Events []InterestEvent
}

// Name is the group's label, or what an unlabeled one is called.
func (in Interest) Name() string { return groupName(in.Label, in.Area) }

// Parent is an interest's area as an identity a page names, or nil.
func (in Interest) Parent() *InterestRef {
	if in.ParentID == "" {
		return nil
	}
	return &InterestRef{ID: in.ParentID, Label: in.ParentLabel, Area: true}
}

// Largest is the size of an area's largest interest listed, which their
// bars are scaled to: 0 for none.
func (in Interest) Largest() int {
	largest := 0
	for _, c := range in.Children {
		largest = max(largest, c.Size)
	}
	return largest
}

// MoreChildren is how many of an area's interests its card leaves out.
func (in Interest) MoreChildren() int { return max(in.NumChildren-len(in.Children), 0) }

// groupName is a group's label, or what an unlabeled area or interest is
// called.
func groupName(label string, area bool) string {
	switch {
	case label != "":
		return label
	case area:
		return "Unlabeled area"
	}
	return "Unlabeled interest"
}

// InterestRef names an area or an interest a page links to: by its label,
// on one line, or by what an unlabeled one is called; a retired one is
// marked, and its link leads to the page that says what became of it.
type InterestRef struct {
	ID, Label string
	Area      bool
	Retired   bool
}

// Name is the identity's label, or what an unlabeled one is called.
func (r InterestRef) Name() string { return groupName(r.Label, r.Area) }

// InterestEvent is what the latest rebuild did to an area or an interest
// (Area): kept, moved (In is the area the interest is in now), split or
// merged, from From toward To, which holds Shared of From's members;
// dissolved, From alone; or new, To alone.
type InterestEvent struct {
	Event    string
	Area     bool
	From, To *InterestRef
	In       *InterestRef
	Shared   int
}

// The events a rebuild records that a page tells apart (store.LineageEvent
// but kept, which says nothing, and the two without a lineage row).
const (
	eventSplit     = "split"
	eventMerged    = "merged"
	eventMoved     = "moved"
	eventDissolved = "dissolved"
	eventNew       = "new"
)

// LineageLine is one line of a group's lineage note, from its own side:
// Kind is one of the lineage kinds below, Other the identity it names, nil
// for moved and new, and Shared the documents it shares.
type LineageLine struct {
	Kind   string
	Other  *InterestRef
	Shared int
}

// The lines of a lineage note.
const (
	lineageSplitFrom  = "split-from"  // it split off from Other
	lineageSplitOff   = "split-off"   // Other split off from it
	lineageTookIn     = "took-in"     // it took in Other, merged into it
	lineageMergedInto = "merged-into" // part of it merged into Other
	lineageMoved      = "moved"       // it moved here from another area
	lineageNew        = "new"         // the rebuild created it
)

// Lineage is the group's lineage note, a line for each event of the latest
// rebuild that changed it, in the events' order. Kept alone says nothing:
// the run before is pruned, so what it gained and lost can't be told.
func (in Interest) Lineage() []LineageLine {
	var out []LineageLine
	is := func(r *InterestRef) bool { return r != nil && r.ID == in.ID }
	for _, e := range in.Events {
		switch {
		case e.Event == eventSplit && is(e.To):
			out = append(out, LineageLine{Kind: lineageSplitFrom, Other: e.From, Shared: e.Shared})
		case e.Event == eventSplit && is(e.From):
			out = append(out, LineageLine{Kind: lineageSplitOff, Other: e.To, Shared: e.Shared})
		case e.Event == eventMerged && is(e.To):
			out = append(out, LineageLine{Kind: lineageTookIn, Other: e.From, Shared: e.Shared})
		case e.Event == eventMerged && is(e.From):
			out = append(out, LineageLine{Kind: lineageMergedInto, Other: e.To, Shared: e.Shared})
		case e.Event == eventMoved && is(e.To):
			out = append(out, LineageLine{Kind: lineageMoved})
		case e.Event == eventNew && is(e.To):
			out = append(out, LineageLine{Kind: lineageNew})
		}
	}
	return out
}

// Member is a document of an interest, of Fit: one of its members, a loose
// fit, or a document placed into it since the rebuild (new).
type Member struct {
	DocumentID string
	Title      string
	// BookmarkTitle names an untitled document: its newest titled
	// bookmark's title.
	BookmarkTitle string
	URL           string
	Similarity    float64
	Fit           string
}

// The fits a document has in the interests: a member, a loose fit (close
// to an interest, not grouped with it), unsorted (close to none), or new
// (placed since the rebuild).
const (
	fitMember   = "member"
	fitLoose    = "loose"
	fitUnsorted = "unsorted"
	fitNew      = "new"
)

// Ref is how a list names the member.
func (m Member) Ref() DocRef {
	return DocRef{ID: m.DocumentID, Title: m.Title, Fallback: m.BookmarkTitle, URL: m.URL}
}

// Cell is the member as a list's document cell: its name, where it lives
// under it.
func (m Member) Cell() DocCell {
	ref := m.Ref()
	return DocCell{Ref: ref, Where: ref.where()}
}

// NewBand is a list's band of the documents placed since the rebuild,
// newest first, above its ranked documents: those listed, and how many
// more there are.
type NewBand struct {
	Members []Member
	More    int
}

// newBand is the band of members, of new in all, on page: the first page
// alone shows it, and none when nothing was placed.
func newBand(page int, members []Member, all int) *NewBand {
	if page > 1 || len(members) == 0 {
		return nil
	}
	return &NewBand{Members: members, More: max(all-len(members), 0)}
}

// InterestPage is an area's page, with a page of its interests; or an
// interest's, with the documents placed into it since, then a page of its
// members, then its loose fits, most similar first. Either says what the
// latest rebuild did to it.
type InterestPage struct {
	Layout Layout
	// Interest's Members are the page's; its Size and Loose count them
	// all. An area's Children are the page's, and NumChildren counts them
	// all.
	Interest Interest
	// Page is the page shown, from 1; 0 is the first.
	Page int
	// RunAt is when the rebuild the interest comes from finished; zero
	// when unknown.
	RunAt time.Time
}

func (p InterestPage) page() int { return max(p.Page, 1) }

// Pages is how many pages an area's interests fill, or an interest's
// members and loose fits.
func (p InterestPage) Pages() int {
	if p.Interest.Area {
		return pageCount(p.Interest.NumChildren, InterestsPageSize)
	}
	return pageCount(p.Interest.Size+p.Interest.Loose, InterestMembersPageSize)
}

// OutOfRange is the page when it is past the last, nil otherwise.
func (p InterestPage) OutOfRange() *PageOutOfRange {
	return outOfRange(p.page(), p.Pages(), p.pageHref)
}

// Pager is the pager under the interests or the members, nil when they
// fit on one page.
func (p InterestPage) Pager() *Pager {
	in := p.Interest
	if in.Area {
		return newPager(pageSpan{Page: p.page(), Size: InterestsPageSize, Shown: len(in.Children),
			Total: in.NumChildren, Noun: "Interests", Href: p.pageHref})
	}
	return newPager(pageSpan{Page: p.page(), Size: InterestMembersPageSize, Shown: len(in.Members),
		Total: in.Size + in.Loose, Noun: "Documents", Suffix: ", most similar first", Href: p.pageHref})
}

func (p InterestPage) pageHref(page int) string { return interestPageHref(p.Interest.ID, page) }

// NewBand is the documents placed into the interest since the rebuild, on
// its first page.
func (p InterestPage) NewBand() *NewBand {
	return newBand(p.page(), p.Interest.NewMembers, p.Interest.New)
}

// RankedMember is a member and its rank among all the interest's members
// and loose fits, from 1, most similar first, the loose fits after the
// members.
type RankedMember struct {
	Rank int
	Member
}

// RankedMembers are the page's members with their ranks, which continue
// from the pages before.
func (p InterestPage) RankedMembers() []RankedMember { return p.ranked(fitMember) }

// RankedLoose are the page's loose fits with their ranks, which continue
// from the members'.
func (p InterestPage) RankedLoose() []RankedMember { return p.ranked(fitLoose) }

// ranked are the page's documents of fit, ranked: the page lists the
// members, then the loose fits, so ranks run on across both.
func (p InterestPage) ranked(fit string) []RankedMember {
	first := PageOffset(p.page(), InterestMembersPageSize) + 1
	var out []RankedMember
	for i, m := range p.Interest.Members {
		if m.Fit == fit {
			out = append(out, RankedMember{Rank: first + i, Member: m})
		}
	}
	return out
}

// Unsorted is a page of the latest rebuild's unsorted documents, nearest
// an interest first, each with the interest it is nearest, and on the
// first page the documents placed into Unsorted since.
type Unsorted struct {
	Layout Layout
	// Page is the page shown, from 1; 0 is the first.
	Page int
	// RunChanged is set when the page was asked for from a page of another
	// run, as on the Interests.
	RunChanged bool
	// Run is the rebuild the page is of, "" before the first is done.
	Run string
	// Total is how many documents the run left unsorted, and Documents the
	// page's.
	Total     int
	Documents []UnsortedDoc
	// New are the newest documents placed into Unsorted since the
	// rebuild, of NumNew.
	New    []Member
	NumNew int
}

// UnsortedDoc is an unsorted document: in no interest, and nearest
// Nearest, whose ID is "" when there is no interest.
type UnsortedDoc struct {
	Member
	Nearest InterestRef
}

func (u Unsorted) page() int { return max(u.Page, 1) }

// Pages is how many pages the unsorted documents fill.
func (u Unsorted) Pages() int { return pageCount(u.Total, InterestMembersPageSize) }

// OutOfRange is the page when it is past the last, nil otherwise, and
// nil before the first rebuild, which the page says instead.
func (u Unsorted) OutOfRange() *PageOutOfRange {
	if u.Run == "" {
		return nil
	}
	return outOfRange(u.page(), u.Pages(), u.pageHref)
}

// Pager is the pager under the documents, nil when they fit on one page.
func (u Unsorted) Pager() *Pager {
	return newPager(pageSpan{Page: u.page(), Size: InterestMembersPageSize, Shown: len(u.Documents), Total: u.Total,
		Noun: "Documents", Suffix: ", nearest first", Href: u.pageHref})
}

// NewBand is the documents placed into Unsorted since the rebuild, on the
// first page.
func (u Unsorted) NewBand() *NewBand { return newBand(u.page(), u.New, u.NumNew) }

// FirstPage is the first page of the run shown.
func (u Unsorted) FirstPage() string { return u.pageHref(1) }

// pageHref is page of the run shown, as the Interests' pages are.
func (u Unsorted) pageHref(page int) string { return unsortedHref(page, u.Run) }

// Changes is what the latest rebuild changed, its events grouped by kind.
type Changes struct {
	Layout Layout
	Run    *ChangesRun // nil before the first rebuild is done
}

// ChangesRun is a rebuild and what it did: when it finished, what started
// it and its kind, its counts, and its events, kept aside, areas before
// interests within each kind.
type ChangesRun struct {
	ComputedAt time.Time
	Trigger    string
	Kind       string
	Changes    RunChanges
	Events     []InterestEvent
}

// FirstGrouping reports whether the rebuild was the library's first
// grouping: it kept nothing, and everything it did was create.
func (r ChangesRun) FirstGrouping() bool {
	if r.Changes.Kept > 0 || len(r.Events) == 0 {
		return false
	}
	for _, e := range r.Events {
		if e.Event != eventNew {
			return false
		}
	}
	return true
}

// EventGroup is the events of one kind.
type EventGroup struct {
	Event  string
	Events []InterestEvent
}

// Groups are the events by kind, in the order the API lists them.
func (r ChangesRun) Groups() []EventGroup {
	var out []EventGroup
	for _, e := range r.Events {
		if n := len(out); n > 0 && out[n-1].Event == e.Event {
			out[n-1].Events = append(out[n-1].Events, e)
			continue
		}
		out = append(out, EventGroup{Event: e.Event, Events: []InterestEvent{e}})
	}
	return out
}

// Gone is the page of an area or interest a link names that the latest
// rebuild doesn't hold: one a rebuild retired (Retired), whose page is a
// 410 saying what became of it, or an ID nothing knows, a 404.
type Gone struct {
	Layout  Layout
	ID      string
	Retired *Retired // nil for an ID nothing knows
	// RetentionDays is how long a retired identity is remembered.
	RetentionDays int
}

// Retired is a retired area or interest: when a rebuild retired it, and
// the identities that took its documents there.
type Retired struct {
	Area       bool
	Label      string
	RetiredAt  time.Time
	Successors []Successor
}

// Name is the identity's label, or what an unlabeled one is called.
func (r Retired) Name() string { return groupName(r.Label, r.Area) }

// Successor is an identity that took part of a retired one's documents:
// it split off from it or it merged into it, sharing Shared of them.
type Successor struct {
	InterestRef
	Event  string
	Shared int
}

// The fates of a retired identity, which its page's sentence says: it
// split into its successors, merged into them, both (its documents went
// to them), or dissolved.
const (
	fateSplit     = "split"
	fateMerged    = "merged"
	fateMixed     = "mixed"
	fateDissolved = "dissolved"
)

// Fate is how the retired identity's sentence goes, from its successors'
// events.
func (r Retired) Fate() string {
	if len(r.Successors) == 0 {
		return fateDissolved
	}
	fate := r.Successors[0].Event
	for _, s := range r.Successors[1:] {
		if s.Event != fate {
			return fateMixed
		}
	}
	switch fate {
	case eventSplit:
		return fateSplit
	case eventMerged:
		return fateMerged
	}
	return fateMixed
}

// DocumentPlace is where the latest rebuild put a document, or where it
// was placed since: a member or a loose fit of Interest, in Area when the
// interest has one; unsorted, nearest Nearest; or new, placed into
// Interest, or into Unsorted. An identity whose ID is "" is none.
type DocumentPlace struct {
	Fit      string
	Interest InterestRef
	Area     InterestRef
	Nearest  InterestRef
}

// ErrorPage is a page's error: its status and message, and the request ID
// the daemon logged it under.
type ErrorPage struct {
	Layout    Layout
	Status    int
	Title     string
	Message   string
	RequestID string
	// Retry is the page to go back to, when there is one to suggest.
	Retry Nav
}

// Starting is what the pages show while the daemon is starting.
type Starting struct {
	Layout Layout
	Phase  string
	// Migrating is set while it migrates its database: Applied of Total.
	Migrating      bool
	Applied, Total int
}
