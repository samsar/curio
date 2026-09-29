package ui

import (
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
	PageDocument  = "document"  // Document
	PageInterests = "interests" // Interests
	PageInterest  = "interest"  // Interest
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
// failed, in the panel's FinishedIn.
type KindLoad struct {
	Kind                              string
	Running, Limit, Pending, Finished int
}

// Waiting is how many jobs wait in the pools.
func (p QueuePanel) Waiting() int {
	n := 0
	for _, k := range p.Kinds {
		n += k.Pending
	}
	return n
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
}

// Drift is what changed in the build that makes the embeddings since the
// library was indexed, and the command that fixes it.
type Drift struct {
	Changes []DriftChange
	Fix     string
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
// query, or the results of its query.
type Search struct {
	Layout  Layout
	Query   string
	Type    string         // the content type the search is limited to; empty for all
	Home    *SearchHome    // set exactly when there is no query
	Err     *PanelError    // the search failed
	Results *SearchResults // nil without a query, or when it failed
}

// SearchHome is what the search page shows without a query, under the
// box. Zero values are unknown: a read the home does without is left out.
type SearchHome struct {
	Searchable   int        // fetched documents
	Interests    []Interest // the latest run's largest, largest first, without members
	AllInterests int        // how many interests the run found
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

// TypeTabs are the search's type filter: a link to this query limited to
// each offered type, the current one marked. A type the page doesn't
// offer (thread, unknown) still limits the search, and marks none.
func (s Search) TypeTabs() []Tab {
	tabs := make([]Tab, 0, len(searchTypes))
	for _, t := range searchTypes {
		tabs = append(tabs, Tab{Label: t.label, Href: searchHref(s.Query, string(t.contentType)),
			Current: s.Type == string(t.contentType)})
	}
	return tabs
}

// SearchResults is a query's ranked hits.
type SearchResults struct {
	Degraded bool     // semantic search was unavailable: keyword results only
	Warnings []string // why
	TookMS   int64
	Hits     []SearchHit
}

// SearchHit is one ranked document and the chunks that matched in it.
type SearchHit struct {
	DocumentID  string
	Title       string // empty for an untitled document
	URL         string
	ContentType string
	Score       float64 // the fused score
	Matches     []Match
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

// Match is a matching chunk: its highlighted snippet, or the start of its
// text for a match only the vector search found, with each retriever's
// score when it returned the chunk.
type Match struct {
	Segments []Segment
	BM25     *float64
	Vector   *float64
}

// Library is a page of documents under the filters.
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

// OfTotal is how many documents the rows shown are of, for "Showing N of
// M": the current state's count, or 0 when the counts don't apply, or
// fall behind the rows shown, as they do when the library changed between
// pages.
func (l Library) OfTotal() int {
	if !l.CountsApply() {
		return 0
	}
	if total := l.Counts.state(l.Filters.State); l.ShownThrough() <= total {
		return total
	}
	return 0
}

// LibraryFilters are a Library page's query, the parameters GET
// /v1/documents takes. Empty fields filter nothing; Limit 0 is the default
// page size.
type LibraryFilters struct {
	State, ContentType, Host, Folder, Cause string
	Limit                                   int
}

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

// LibraryRow is one document in the list. LastError and FailureCause are
// set only while the document is failed or dead.
type LibraryRow struct {
	DocumentID   string
	Title        string // empty for an untitled document
	URL          string
	State        string
	ContentType  string
	UpdatedAt    time.Time
	LastError    string
	FailureCause string
}

// Cell is the row's document cell: a titled document's short URL under its
// title, an untitled one's host under its short URL.
func (r LibraryRow) Cell() DocCell {
	where := shortURL(r.URL)
	if r.Title == "" {
		where = host(r.URL)
	}
	return DocCell{Ref: DocRef{ID: r.DocumentID, Title: r.Title, URL: r.URL}, Where: where,
		ContentType: r.ContentType, When: r.UpdatedAt, FailureCause: r.FailureCause, LastError: r.LastError}
}

// DocRef names a document in a list (the doc-title partial): by its
// title, or by its short URL while it has none, linking to its page when
// it has an ID.
type DocRef struct {
	ID, Title, URL string
}

// DocCell is a document as a list's first column shows it (the doc-cell
// partial): its name, where it lives, what the row's other columns show
// where a phone folds them away, and, for a failed one, why.
type DocCell struct {
	Ref   DocRef
	Where string // under its name: where it lives
	// Source is the browser a bookmark came from, for a list of pages as
	// they were saved, which no page lists yet; a list of documents leaves
	// it empty.
	Source       string
	ContentType  string    // shown here on a phone, whose table has no Type column
	When         time.Time // shown here on a phone, whose table has no time column
	FailureCause string
	LastError    string
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
// flight or the document waits for one, and after every change the page
// makes, carrying the page's baseline. It lists itself, so its answer
// decides whether it keeps polling.
func (j DocumentJobs) Poller() Poller {
	p := Poller{ID: documentPoller, Href: documentPollHref(j.DocumentID, j.Baseline), OnChange: true,
		Regions: []string{documentJobsID, documentPoller}}
	if j.InFlight() || j.State == string(store.DocStatePending) {
		p.Every = pollEvery
	}
	return p
}

// JobLine is one of a document's jobs in flight: its kind, whether it
// runs or waits, the attempts it has used, and, for a retry, when it may
// run again.
type JobLine struct {
	Kind     string
	Running  bool
	Attempts int
	RunAfter time.Time
}

// Retrying reports whether the job waits to be tried again.
func (l JobLine) Retrying() bool { return !l.Running && l.Attempts > 0 }

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
	ID           string
	Title        string
	URL          string
	CanonicalURL string
	ContentType  string
	State        string
	Author       string
	Language     string
	PublishedAt  time.Time
	WordCount    int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	MarkdownPath string // absolute; empty when it has none
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
	URL        string
	Score      float64
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

// Interests is the latest clustering run's interests, and a rebuild of
// them. A poll's answer (Poll PollRebuild) holds the rebuild's live
// regions alone, and reads no interest.
type Interests struct {
	Layout    Layout
	Poll      string
	Run       *InterestRun // nil before the first run finished
	Interests []Interest
	Rebuild   Rebuild
}

// Rebuild is the Interests page's rebuild: whether the page offers one,
// whether one is queued or running, and what the newest clustering run
// came to when it isn't the run the page shows.
type Rebuild struct {
	Enabled bool        // config.yaml's insight.enabled
	Err     *PanelError // the queue or the runs couldn't be read
	Queued  bool
	Running bool
	// StartedAt is when the running rebuild started; zero when unknown.
	StartedAt time.Time
	// Hold is why the queue holds a queued rebuild, the gate's reason, and
	// OpensAt when it opens; "" while it is open.
	Hold    string
	OpensAt time.Time
	// Shown is the run the page shows, "" for none: the latest done one.
	Shown string
	// NewRun is the status of the newest run, of any status, when it isn't
	// Shown, and RunError its error: "" when it is.
	NewRun   string
	RunError string
}

// InFlight reports whether a rebuild is queued or running.
func (b Rebuild) InFlight() bool { return b.Queued || b.Running }

// RebuildOutcome is what came of the newest rebuild, for the page to say.
type RebuildOutcome string

// What came of a rebuild.
const (
	RebuildNone   RebuildOutcome = ""
	RebuildReady  RebuildOutcome = "ready"  // a newer run's interests are ready
	RebuildFailed RebuildOutcome = "failed" // the newest run failed
)

// Outcome is what came of the newest rebuild: a newer done run is ready to
// load; a failed one is reported once no rebuild is in flight, which may
// yet replace it.
func (b Rebuild) Outcome() RebuildOutcome {
	switch {
	case b.NewRun == string(store.ClusterRunDone):
		return RebuildReady
	case b.NewRun == string(store.ClusterRunFailed) && !b.InFlight():
		return RebuildFailed
	}
	return RebuildNone
}

// Action is the Rebuild button's.
func (Rebuild) Action() Action { return rebuildAction() }

// Poller is the rebuild's poller: every 2 seconds while a rebuild is in
// flight, and after every change the page makes, carrying the run the page
// shows. It lists itself, so its answer decides whether it keeps polling.
func (b Rebuild) Poller() Poller {
	p := Poller{ID: rebuildPoller, Href: interestsPollHref(b.Shown), OnChange: true,
		Regions: []string{rebuildStateID, rebuildControlID, rebuildPoller}}
	if b.InFlight() {
		p.Every = pollEvery
	}
	return p
}

// InterestRun is the clustering run the interests come from.
type InterestRun struct {
	ID         string
	ComputedAt time.Time
	Algo       string
	Documents  int
	Noise      int // documents in no interest
	Interests  int // how many interests it found, shown or not
}

// Clustered is how many of the run's documents are in an interest.
func (r InterestRun) Clustered() int { return clustered(r.Documents, r.Noise) }

// Interest is one interest (a labeled cluster) and some of its members.
type Interest struct {
	ID       string
	Label    string // empty while unlabeled
	Summary  string
	Size     int
	Cohesion float64
	Members  []Member
}

// Member is a document of an interest.
type Member struct {
	DocumentID string
	Title      string
	URL        string
	Similarity float64
}

// Cell is the member as a list's document cell, with its host under its
// name.
func (m Member) Cell() DocCell {
	return DocCell{Ref: DocRef{ID: m.DocumentID, Title: m.Title, URL: m.URL}, Where: host(m.URL)}
}

// InterestPage is one interest's page, with its members up to the page's
// cap.
type InterestPage struct {
	Layout   Layout
	Interest Interest
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
