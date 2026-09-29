package ui

import "time"

// Page names, for Renderer.Page: one template per page, each with its view
// model below.
const (
	PageOverview  = "overview"  // Overview
	PageSearch    = "search"    // Search
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
	NavOverview  Nav = "overview"
	NavSearch    Nav = "search"
	NavLibrary   Nav = "library"
	NavInterests Nav = "interests"
)

// Layout is what every page's frame shows.
type Layout struct {
	Title   string // the page's, in <title>
	Nav     Nav    // the current navigation item
	Version string // the daemon's
}

// PanelError is why a panel's data couldn't be read: the message, and the
// request ID the daemon logged it under. The rest of the page still shows.
type PanelError struct {
	Message   string
	RequestID string
}

// Overview is the dashboard's home: counts, the queue, how far along its
// work is, health, and what was saved last.
type Overview struct {
	Layout   Layout
	Counts   CountsPanel
	Queue    QueuePanel
	Progress ProgressPanel
	Health   HealthPanel
	Recent   RecentPanel
}

// CountsPanel counts the library and the jobs.
type CountsPanel struct {
	Err       *PanelError
	Documents int
	ByState   []Count // every document state, in lifecycle order
	Bookmarks int
	Jobs      []Count // by status, the statuses with jobs
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
	LastSuccess      time.Time
	LastFailure      time.Time
	LastFailureClass string
	CooldownUntil    time.Time
}

// RecentPanel is the newest bookmarks.
type RecentPanel struct {
	Err       *PanelError
	Bookmarks []RecentBookmark
}

// RecentBookmark is a bookmark and the state of its document.
type RecentBookmark struct {
	Title         string // empty for an untitled bookmark
	URL           string
	Source        string
	SavedAt       time.Time
	DocumentID    string // empty when its document was deleted
	DocumentState string
}

// Search is the search page: the form, and the results of its query.
type Search struct {
	Layout  Layout
	Query   string
	Err     *PanelError    // the search failed
	Results *SearchResults // nil before a query is given, or when it failed
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
	DocumentID string
	Title      string // empty for an untitled document
	URL        string
	Score      float64 // the fused score
	Matches    []Match
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
	Rows       []LibraryRow
	NextCursor string // empty on the last page
}

// LibraryFilters are a Library page's query, the parameters GET
// /v1/documents takes. Empty fields filter nothing; Limit 0 is the default
// page size.
type LibraryFilters struct {
	State, ContentType, Host, Folder, Cause string
	Limit                                   int
}

// LibraryRow is one document in the list.
type LibraryRow struct {
	DocumentID  string
	Title       string // empty for an untitled document
	URL         string
	State       string
	ContentType string
	UpdatedAt   time.Time
	LastError   string
}

// Document is one document's page.
type Document struct {
	Layout     Layout
	Meta       DocumentMeta
	Extraction *Extraction // nil before its first fetch
	// LastError is its most recent failed job's error, set only while the
	// document is failed or dead: an older failure a refetch recovered
	// from isn't current.
	LastError string
	Text      TextPanel
	Related   RelatedPanel
	Bookmarks BookmarksPanel
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
	Noise      int
}

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
