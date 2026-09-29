package ui

import (
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
// health, how far along its work is, and the jobs.
type Status struct {
	Layout   Layout
	Counts   CountsPanel
	Queue    QueuePanel
	Progress ProgressPanel
	Health   HealthPanel
	Failures FailuresPanel
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

// QueuePanel is the queue gate and the pools' load.
type QueuePanel struct {
	Err      *PanelError
	Open     bool
	Reason   string    // why it is closed
	OpensAt  time.Time // when a closed queue opens; zero when unknown
	Throttle string
	Schedule string // HH:MM-HH:MM; empty for none
	Kinds    []KindLoad
	// KeepAwake is the setting; KeepAwakeActive whether the Mac is held
	// awake now, and PowerSource what it runs on, while the setting is on.
	KeepAwake       bool
	KeepAwakeActive bool
	PowerSource     string
}

// KindLoad is one pool's load.
type KindLoad struct {
	Kind                    string
	Running, Limit, Pending int
}

// ProgressPanel is the queue's progress estimate.
type ProgressPanel struct {
	Err      *PanelError
	Progress Progress
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

// Document is one document's page.
type Document struct {
	Layout     Layout
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

// Interests is the latest clustering run's interests.
type Interests struct {
	Layout    Layout
	Run       *InterestRun // nil before the first run finished
	Interests []Interest
}

// InterestRun is the clustering run the interests come from.
type InterestRun struct {
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
