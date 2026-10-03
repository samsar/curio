// Package store defines curio's storage interfaces and domain types.
//
// Concrete implementations live in subpackages (e.g., internal/store/sqlite).
// Other packages depend on these interfaces, not on the SQLite impl directly,
// so we can swap to Postgres + pgvector for hosted mode without rippling
// changes through the codebase.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Sentinel errors returned by store implementations.
var (
	// ErrNotFound: row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict: a uniqueness constraint was violated.
	ErrConflict = errors.New("store: conflict")
	// ErrNotRunning: a job transition that only applies to a running job
	// found the job in another status. Nothing was changed.
	ErrNotRunning = errors.New("store: job is not running")
)

// MaxEmbeddingDim is the widest embedding the vector index takes: sqlite-vec's
// SQLITE_VEC_VEC0_MAX_DIMENSIONS. A home's width is fixed when the home is
// created, recorded in its marker, and must lie in [1, MaxEmbeddingDim].
const MaxEmbeddingDim = 8192

// LocalTenantID is the tenant of a single-user install: the daemon scopes
// every row to it, server-side, and never shows it to clients.
const LocalTenantID = "local"

// MaxSearchK is the most documents one search or related query returns,
// and the most one search ranks: its pages are windows of that ranking.
// The chunk fan-out is sized from it, so it also bounds the chunk queries'
// LIMIT. Config validates search.default_k against it, and the API a
// request's k and offset.
const MaxSearchK = 100

// NullableString is s as the value of a nullable text column: nil when s is
// empty, so an absent value is stored as NULL rather than "".
func NullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// DocState is a document's lifecycle state (documents.state).
type DocState string

// ContentType classifies a document's content (documents.content_type).
type ContentType string

// JobKind names the work a job does (jobs.kind).
type JobKind string

// JobStatus is a job's place in the queue (jobs.status).
type JobStatus string

// Throttle is how hard the daemon works through its queue
// (queue_settings.throttle).
type Throttle string

// State / kind / status / throttle constants. Keep in sync with the CHECK
// constraints in migrations/.
const (
	DocStatePending DocState = "pending"
	DocStateFetched DocState = "fetched"
	DocStateFailed  DocState = "failed"
	DocStateDead    DocState = "dead"

	ContentTypeArticle ContentType = "article"
	ContentTypeRepo    ContentType = "repo"
	ContentTypeVideo   ContentType = "video"
	ContentTypePDF     ContentType = "pdf"
	ContentTypeThread  ContentType = "thread"
	ContentTypeUnknown ContentType = "unknown"

	JobKindFetch     JobKind = "fetch"
	JobKindIndex     JobKind = "index"
	JobKindImport    JobKind = "import"
	JobKindCluster   JobKind = "cluster"
	JobKindSummarize JobKind = "summarize"

	JobStatusPending JobStatus = "pending"
	JobStatusRunning JobStatus = "running"
	JobStatusDone    JobStatus = "done"
	JobStatusFailed  JobStatus = "failed"

	// ThrottleNormal runs every worker the daemon has.
	ThrottleNormal Throttle = "normal"
	// ThrottleGentle runs fewer at once, to spare the machine.
	ThrottleGentle Throttle = "gentle"
)

// Extraction statuses (document_extractions.status) and bookmark sources
// (bookmarks.source).
const (
	ExtractionStatusOK        = "ok"
	ExtractionStatusPartial   = "partial"
	ExtractionStatusPaywalled = "paywalled"
	ExtractionStatusError     = "error"

	SourceChrome  = "chrome"
	SourceSafari  = "safari"
	SourceFirefox = "firefox"
	SourceManual  = "manual"
	SourceHTML    = "html" // Netscape HTML export, any browser
)

// FailureCause is why a failed or dead document failed
// (documents.failure_cause): what the site did, as the fetcher read it, or
// the step that gave up. Unlike the enums above it has no CHECK constraint
// (migrations/014 says why); the store checks Valid on every write instead.
type FailureCause string

// Failure causes, in the order FailureCauses lists them.
const (
	// FailureCauseDeadLink: the content is gone (a 404 or 410, or a page
	// that says so). The one cause of a dead document.
	FailureCauseDeadLink FailureCause = "dead_link"
	// FailureCauseAntiBot: the site blocked the request (a 403 or 503, a
	// challenge or block page).
	FailureCauseAntiBot FailureCause = "anti_bot"
	// FailureCauseLoginWall: a login page, or too little text to be the
	// article.
	FailureCauseLoginWall FailureCause = "login_wall"
	// FailureCauseJinaRefused: the Jina Reader fallback refused the target
	// (a publisher's opt-out, a deterministic 4xx), normally after the site
	// served a page curio can't use.
	FailureCauseJinaRefused FailureCause = "jina_refused"
	// FailureCauseTLS: the site's certificate failed verification.
	FailureCauseTLS FailureCause = "tls"
	// FailureCauseUnreachable: the host doesn't resolve, or refuses
	// connections.
	FailureCauseUnreachable FailureCause = "unreachable"
	// FailureCauseTimeout: the site, or the tool fetching it, took too long.
	FailureCauseTimeout FailureCause = "timeout"
	// FailureCauseNetwork: any other transport failure (a reset, a TLS
	// alert, a redirect loop, our own network down).
	FailureCauseNetwork FailureCause = "network"
	// FailureCauseRateLimited: the site, GitHub, YouTube or Jina Reader
	// rate-limited the requests (a 429), or Jina Reader blocked the site's
	// keyless reads for longer than the job could wait.
	FailureCauseRateLimited FailureCause = "rate_limited"
	// FailureCauseHTTPError: any other HTTP status, or an error page naming
	// one.
	FailureCauseHTTPError FailureCause = "http_error"
	// FailureCauseUnsupported: a URL or content curio can't read (a channel
	// page, a GitHub profile, a file that isn't HTML, a PDF it can't
	// extract).
	FailureCauseUnsupported FailureCause = "unsupported"
	// FailureCauseTooLarge: the response was over the body cap.
	FailureCauseTooLarge FailureCause = "too_large"
	// FailureCauseIndex: the fetch succeeded, and indexing gave up.
	FailureCauseIndex FailureCause = "index"
	// FailureCauseOther: anything else.
	FailureCauseOther FailureCause = "other"
)

// failureCauses is every FailureCause, in the order of their constants.
var failureCauses = []FailureCause{
	FailureCauseDeadLink, FailureCauseAntiBot, FailureCauseLoginWall, FailureCauseJinaRefused,
	FailureCauseTLS, FailureCauseUnreachable, FailureCauseTimeout, FailureCauseNetwork,
	FailureCauseRateLimited, FailureCauseHTTPError, FailureCauseUnsupported, FailureCauseTooLarge,
	FailureCauseIndex, FailureCauseOther,
}

// FailureCauses returns every FailureCause, in the order of their
// constants, in a slice the caller may keep.
func FailureCauses() []FailureCause { return slices.Clone(failureCauses) }

// Valid reports whether c is one of the FailureCause constants.
func (c FailureCause) Valid() bool { return slices.Contains(failureCauses, c) }

// State is the state a document that failed for c is in: dead for a dead
// link, failed for every other cause.
func (c FailureCause) State() DocState {
	if c == FailureCauseDeadLink {
		return DocStateDead
	}
	return DocStateFailed
}

// Valid reports whether s is one of the DocState constants.
func (s DocState) Valid() bool {
	switch s {
	case DocStatePending, DocStateFetched, DocStateFailed, DocStateDead:
		return true
	}
	return false
}

// Valid reports whether t is one of the ContentType constants.
func (t ContentType) Valid() bool {
	switch t {
	case ContentTypeArticle, ContentTypeRepo, ContentTypeVideo, ContentTypePDF, ContentTypeThread,
		ContentTypeUnknown:
		return true
	}
	return false
}

// Valid reports whether s is one of the JobStatus constants.
func (s JobStatus) Valid() bool {
	switch s {
	case JobStatusPending, JobStatusRunning, JobStatusDone, JobStatusFailed:
		return true
	}
	return false
}

// Valid reports whether k is one of the JobKind constants.
func (k JobKind) Valid() bool {
	switch k {
	case JobKindFetch, JobKindIndex, JobKindImport, JobKindCluster, JobKindSummarize:
		return true
	}
	return false
}

// Valid reports whether t is one of the Throttle constants.
func (t Throttle) Valid() bool {
	switch t {
	case ThrottleNormal, ThrottleGentle:
		return true
	}
	return false
}

// Gentle throttle caps: how many jobs of a kind run at once under
// ThrottleGentle. Index jobs are what keep Ollama, and so the machine,
// busy; capping fetches spares the network and extraction. Cluster runs
// one at a time anyway, and kinds without a cap are never throttled.
const (
	gentleFetchCap = 4
	gentleIndexCap = 1
)

// Cap is how many jobs of kind t lets run at once, and whether it caps
// kind at all: the gentle throttle caps fetches and index jobs, and the
// normal one caps nothing.
func (t Throttle) Cap(kind JobKind) (int, bool) {
	if t != ThrottleGentle {
		return 0, false
	}
	switch kind {
	case JobKindFetch:
		return gentleFetchCap, true
	case JobKindIndex:
		return gentleIndexCap, true
	case JobKindImport, JobKindCluster, JobKindSummarize:
	}
	return 0, false
}

// Limit is how many jobs of kind run at once, from a pool of that size,
// under t.
func (t Throttle) Limit(kind JobKind, pool int) int {
	if c, capped := t.Cap(kind); capped {
		return min(c, pool)
	}
	return pool
}

// IsFinished reports whether s is terminal (done or failed). Only finished
// jobs may be deleted: removing a pending or running job would strand its
// document in pending with nothing left to move it on.
func (s JobStatus) IsFinished() bool {
	return s == JobStatusDone || s == JobStatusFailed
}

// Document is the universal content record, deduplicated by (tenant_id, url).
type Document struct {
	ID                  string
	TenantID            string
	URL                 string
	URLCanonical        *string
	ContentType         ContentType
	Title               *string
	Author              *string
	PublishedAt         *time.Time
	Language            *string
	WordCount           *int
	CurrentExtractionID *string
	State               DocState
	// FailureCause is why a failed or dead document failed, and empty for
	// every other: the store keeps it set exactly when State is failed or
	// dead, and State equal to its State().
	FailureCause FailureCause
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// DocumentExtraction is one fetch attempt against a document.
type DocumentExtraction struct {
	ID             string
	DocumentID     string
	FetchedAt      time.Time
	Fetcher        string
	Status         string
	MarkdownPath   *string
	RawPath        *string
	ExtractionMeta json.RawMessage // nullable; nil means absent
	ErrorMessage   *string
}

// Bookmark is the v1 reference table row.
type Bookmark struct {
	ID         string
	TenantID   string
	DocumentID *string // the document Ingest linked it to; nil if that document was deleted
	URL        string
	Title      *string
	SavedAt    time.Time
	Source     string
	FolderPath *string
	Tags       []string // serialized to JSON in storage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Job is a unit of background work.
type Job struct {
	ID        string
	TenantID  string
	Kind      JobKind
	Payload   json.RawMessage
	Status    JobStatus
	Attempts  int
	RunAfter  time.Time
	LastError *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DocumentStore operates on the documents table.
type DocumentStore interface {
	// Create inserts a new document and fills in its ID (when empty),
	// CreatedAt and UpdatedAt. An empty ContentType is stored as unknown,
	// and an empty State as pending, or as FailureCause.State() when a
	// cause is given; those defaults apply on insert only. A cause must be
	// valid and go with the state (FailureCause.State()), and a failed or
	// dead document must have one; anything else is an error, and nothing
	// is inserted. A new document has no extraction, so
	// CurrentExtractionID must be nil. A document that already exists for
	// (tenant_id, url) is an error wrapping ErrConflict.
	Create(ctx context.Context, d *Document) error
	GetByID(ctx context.Context, id string) (*Document, error)
	// GetByURL returns the tenant's document for url, which must already
	// be normalized the way ingest stores it (urlutil.Normalize). A URL
	// with no document is ErrNotFound.
	GetByURL(ctx context.Context, tenantID, url string) (*Document, error)
	// MarkFailed records that a document failed for cause, which must be
	// valid (an invalid one is an error, and nothing is written): its state
	// becomes cause.State(), dead for a dead link and failed for any other
	// cause. ErrNotFound if there is no such document.
	MarkFailed(ctx context.Context, id string, cause FailureCause) error
	// MarkFetched records that a document was fetched and indexed: its
	// state becomes fetched, it has no failure cause, and its indexed_at,
	// when its vectors were written, is now (the time its updated_at gets
	// in the same statement). It is the only write of indexed_at.
	// ErrNotFound if there is no such document.
	MarkFetched(ctx context.Context, id string) error
	SetCurrentExtraction(ctx context.Context, documentID, extractionID string) error

	// ApplyFetch records a successful fetch on a document: it points
	// current_extraction_id at m.ExtractionID, which must exist, writes the
	// fetch-derived columns exactly as given, and sets the state to pending,
	// with no failure cause, until the index step marks it fetched. A nil
	// field clears its column: those columns describe the current
	// extraction, so none may keep a value from an earlier one. word_count
	// is left alone. ErrNotFound if there is no such document.
	ApplyFetch(ctx context.Context, id string, m FetchedMetadata) error

	// RequeueFetch resets the tenant's document to pending, clearing its
	// failure cause, and enqueues a fresh fetch job for it, atomically:
	// either both happen or neither does, so a document is never left
	// pending with no job to move it on. Returns the new job, or
	// ErrNotFound if the tenant has no such document.
	RequeueFetch(ctx context.Context, tenantID, documentID string) (*Job, error)
	// RequeueFetchByStates does the same for every tenant document whose
	// state is one of states and, unless cause is empty, that failed for
	// cause, in one transaction. Returns how many jobs it enqueued.
	RequeueFetchByStates(ctx context.Context, tenantID string, states []DocState, cause FailureCause) (int, error)

	// ListWithLastError lists the tenant's documents, most recently updated
	// first (then by ID, descending), each with the error of the most recent
	// failed job that targeted it, the markdown path of its current
	// extraction, and, for an untitled one, its bookmark title.
	ListWithLastError(ctx context.Context, tenantID string, opts ListDocumentsOpts) ([]DocumentWithError, error)
	// GetWithLastError returns one of the tenant's documents as
	// ListWithLastError lists it. ErrNotFound if the tenant has no such
	// document.
	GetWithLastError(ctx context.Context, tenantID, id string) (*DocumentWithError, error)
	// GetByIDsWithLastError returns the tenant's documents with those IDs,
	// each as ListWithLastError lists it, in no particular order, in one
	// read however many there are. An ID that names no document of the
	// tenant's is left out. No IDs return none.
	GetByIDsWithLastError(ctx context.Context, tenantID string, ids []string) ([]DocumentWithError, error)
	// ListIDsWithContent returns the IDs of the tenant's documents in state
	// that have a current extraction: the ones an index job can work on.
	ListIDsWithContent(ctx context.Context, tenantID string, state DocState) ([]string, error)
	// CountByState counts the tenant's documents per state. States with no
	// documents are absent from the map.
	CountByState(ctx context.Context, tenantID string) (map[DocState]int, error)
	// LastIndexedAt is when the tenant last had a document's vectors
	// written (MarkFetched): the latest indexed_at, of a document fetched
	// now or since refetched. Zero when no document was ever indexed.
	LastIndexedAt(ctx context.Context, tenantID string) (time.Time, error)
	// FailureSummary counts the tenant's failed and dead documents by
	// failure cause, naming for each cause at most topHosts of the hosts
	// its documents are on.
	FailureSummary(ctx context.Context, tenantID string, topHosts int) (FailureSummary, error)
}

// FailureSummary is how many of a tenant's documents failed, for each
// cause.
type FailureSummary struct {
	Total int // failed and dead documents: the sum of the causes' counts
	// Causes are the causes with documents, the most documents first, then
	// by cause.
	Causes []CauseCount
}

// CauseCount is how many documents failed for one cause, and the hosts
// most of them are on.
type CauseCount struct {
	Cause FailureCause
	Count int
	// Hosts are the hosts with the most of the cause's documents, the most
	// first, then by host. A host is the URL's authority as the documents
	// list's host filter matches it: lowercased, port and all, "www."
	// kept. A document whose URL has none counts toward Count alone.
	Hosts []HostCount
}

// HostCount is how many of a cause's documents are on one host.
type HostCount struct {
	Host  string
	Count int
}

// FetchedMetadata is what a fetch learned about a document, for
// DocumentStore.ApplyFetch.
type FetchedMetadata struct {
	ExtractionID string
	ContentType  ContentType // one of the ContentType constants
	URLCanonical *string     // the final URL after redirects, when it differs
	Title        *string
	Author       *string
	Language     *string
	PublishedAt  *time.Time
}

// PageKey is a position in a list ordered newest first by a timestamp and
// then by ID: the timestamp and ID of the last row a page returned. The
// next page is the rows strictly after it. The zero PageKey is the start of
// the list.
type PageKey struct {
	At time.Time
	ID string
}

// IsZero reports whether k is the start of a list.
func (k PageKey) IsZero() bool {
	return k.ID == "" && k.At.IsZero()
}

// ListDocumentsOpts filters DocumentStore.ListWithLastError. Empty fields
// mean "no filter for that dimension"; the ones set must all match.
type ListDocumentsOpts struct {
	State       DocState
	ContentType ContentType
	// Host matches documents whose http or https URL has exactly this host,
	// ASCII case-insensitively as DNS names are. Every character is
	// literal. It is SearchFilters.Host's rule.
	Host string
	// Folder matches documents with a bookmark in that folder or any folder
	// under it, by ListBookmarksOpts.FolderPath's rule: on path segments,
	// case-sensitively, every character literal, a trailing "/" ignored and
	// "/" alone no filter.
	Folder string
	// Cause matches documents that failed for it: failed ones, or dead ones
	// for FailureCauseDeadLink.
	Cause FailureCause
	Limit int     // <= 0 means the impl default (50)
	After PageKey // updated_at and ID of the previous page's last row
}

// DocumentWithError is a document plus what a debug listing shows next to
// it, so one query answers "which documents are broken, and where does
// their content live".
type DocumentWithError struct {
	*Document
	LastError    string // of the most recent failed job for the document; empty if none
	MarkdownPath string // current extraction's, relative to the content dir; empty if none
	// BookmarkTitle names a document with no title of its own: the title
	// of its most recently saved bookmark (by SavedAt, then ID) whose title
	// isn't blank. It is empty for a titled document, and for one with no
	// such bookmark.
	BookmarkTitle string
}

// ExtractionStore operates on the document_extractions table.
type ExtractionStore interface {
	Create(ctx context.Context, e *DocumentExtraction) error
	GetByID(ctx context.Context, id string) (*DocumentExtraction, error)
	ListByDocument(ctx context.Context, documentID string) ([]*DocumentExtraction, error)
}

// BookmarkStore operates on the bookmarks table.
type BookmarkStore interface {
	// Ingest saves a bookmark together with its document, in one
	// transaction: it finds the tenant's document for b.URL or creates it
	// pending, inserts the bookmark linked to it, and enqueues a fetch job
	// only if it created the document. Either all of that commits or none
	// of it does. b.URL must already be normalized, since it is the
	// document's dedup key, and b.DocumentID on input is ignored. On success
	// b.ID (generated when empty), b.DocumentID, b.CreatedAt and b.UpdatedAt
	// are set. A bookmark that already exists for (tenant_id, url, source)
	// is an error wrapping ErrConflict, and nothing is written.
	Ingest(ctx context.Context, b *Bookmark) (IngestResult, error)
	// Create inserts the bookmark row alone, linked to b.DocumentID as
	// given. Ingest is the API path; Create is the low-level insert.
	Create(ctx context.Context, b *Bookmark) error
	GetByID(ctx context.Context, id string) (*Bookmark, error)
	// List lists the tenant's bookmarks in opts.Order, each with what a
	// list of them shows of its document: newest first by CreatedAt, when
	// curio added them, by default, or by SavedAt, when the browser saved
	// them, in BookmarkOrderSaved; then by ID, descending. An order outside
	// the BookmarkOrder constants is an error, and nothing is read.
	List(ctx context.Context, tenantID string, opts ListBookmarksOpts) ([]BookmarkWithDocument, error)
	// ListByDocument returns the tenant's bookmarks linked to the document,
	// newest saved first (by SavedAt, then by ID, descending). It is
	// unpaged: a URL has at most one bookmark per source.
	ListByDocument(ctx context.Context, tenantID, documentID string) ([]*Bookmark, error)
	Delete(ctx context.Context, id string) error
	LinkDocument(ctx context.Context, bookmarkID, documentID string) error

	// TagsForDocument returns the deduplicated set of tags across all
	// bookmarks (any source) that reference the document. Empty if none.
	// The indexer uses it to denormalize tags into chunks_fts for boosting.
	TagsForDocument(ctx context.Context, tenantID, documentID string) ([]string, error)

	// Count returns how many bookmarks the tenant has.
	Count(ctx context.Context, tenantID string) (int, error)

	// PreviewIngest reports what Ingest would find for each of urls under
	// source, writing nothing: whether the tenant has the URL's document,
	// and a bookmark of it from source. urls are normalized, as Ingest
	// takes them; the result has an entry for each distinct one. It is one
	// statement however many URLs there are, and none for an empty list.
	PreviewIngest(ctx context.Context, tenantID, source string, urls []string) (map[string]IngestPreview, error)
}

// IngestPreview is what BookmarkStore.Ingest would find for one URL.
type IngestPreview struct {
	// DocumentExists: the tenant has its document, so Ingest would link
	// to it and enqueue no fetch.
	DocumentExists bool
	// BookmarkExists: the tenant has a bookmark of it from the source, so
	// Ingest would refuse it (ErrConflict).
	BookmarkExists bool
}

// IngestResult reports what BookmarkStore.Ingest did about the bookmark's
// document.
type IngestResult struct {
	DocumentState   DocState // the document's state after the ingest
	DocumentCreated bool     // this call created the document
	FetchJob        *Job     // enqueued for a document this call created; nil otherwise
}

// BookmarkWithDocument is a bookmark and what a list of bookmarks shows of
// the document it links to, read in the same query. The document fields
// are zero when the bookmark links to no document.
type BookmarkWithDocument struct {
	*Bookmark
	DocumentState        DocState
	DocumentTitle        *string
	DocumentContentType  ContentType
	DocumentFailureCause FailureCause
	DocumentLastError    string // of the most recent failed job for the document
}

// BookmarkOrder is the order BookmarkStore.List lists bookmarks in.
type BookmarkOrder string

const (
	// BookmarkOrderCreated lists bookmarks newest first by when curio
	// added them (CreatedAt). It is the default: an empty order means it.
	BookmarkOrderCreated BookmarkOrder = "created"
	// BookmarkOrderSaved lists them newest first by when the browser saved
	// them (SavedAt).
	BookmarkOrderSaved BookmarkOrder = "saved"
)

// Valid reports whether o is one of the BookmarkOrder constants.
func (o BookmarkOrder) Valid() bool {
	return o == BookmarkOrderCreated || o == BookmarkOrderSaved
}

// Key is b's position in a list in order o, the key of ListBookmarksOpts'
// After: its SavedAt in BookmarkOrderSaved, its CreatedAt otherwise, and
// its ID.
func (o BookmarkOrder) Key(b *Bookmark) PageKey {
	if o == BookmarkOrderSaved {
		return PageKey{At: b.SavedAt, ID: b.ID}
	}
	return PageKey{At: b.CreatedAt, ID: b.ID}
}

// ListBookmarksOpts filter and page BookmarkStore.List. Empty fields mean
// "no filter for that dimension"; the ones set must all match.
type ListBookmarksOpts struct {
	Order  BookmarkOrder // empty means BookmarkOrderCreated
	Source string
	// FolderPath matches that folder and every folder under it, on path
	// segments and case-sensitively: "/Tech/AI" matches "/Tech/AI" and
	// "/Tech/AI/Agents" but not "/Tech/AIRPLANES" or "/tech/ai". Every
	// character is literal, a trailing "/" is ignored, and "/" alone is no
	// filter.
	FolderPath string
	// Host matches bookmarks whose http or https URL has exactly this
	// host, ASCII case-insensitively as DNS names are. Every character is
	// literal. It is ListDocumentsOpts.Host's rule, on the bookmark's URL,
	// which is its document's.
	Host string
	// State, ContentType and Cause match the bookmark's document, by
	// ListDocumentsOpts' rules. A bookmark linked to no document matches
	// none of them.
	State       DocState
	ContentType ContentType
	Cause       FailureCause
	Limit       int     // <= 0 means the impl default (50)
	After       PageKey // the previous page's last row's key in Order (BookmarkOrder.Key)
}

// Chunk is the indexed text segment unit. Each chunk owns one row in the
// chunks table, one entry in the chunks_fts index (BM25), and one row in
// chunks_vec (vector ANN).
type Chunk struct {
	ID           string
	DocumentID   string
	ExtractionID string
	Ord          int
	Text         string
	TokenCount   int
}

// ChunkInput is the writer-side struct for ReplaceForDocument. The store
// generates the ID and persists text + embedding atomically.
type ChunkInput struct {
	Text       string
	TokenCount int
	Embedding  []float32 // length must match the configured embedding.Dim
}

// ChunkHit is a single search result, surfaced before fusion. Score is
// normalized so higher = better regardless of retriever — callers don't
// need to know whether BM25 or vec-distance produced it.
type ChunkHit struct {
	ChunkID    string
	DocumentID string
	Score      float64
}

// SearchFilters scopes a search to documents matching all of the set
// dimensions (values within one dimension are OR'd). An empty filter set
// matches every document search reads. Every dimension is checked on each
// hit's document after the search finds it, so a filter narrows the results
// without changing what the search reads; host is matched against the
// document URL (there is no host column).
type SearchFilters struct {
	ContentType []string // documents.content_type IN (...)
	// Host matches documents whose http or https URL has exactly this host,
	// ASCII case-insensitively as DNS names are. Every character is literal.
	Host   []string
	Source []string // EXISTS a bookmark with bookmarks.source IN (...)
	// ExcludeDocumentID drops one document from the results. Used by
	// find-related to exclude the source document; not exposed through
	// the public search API.
	ExcludeDocumentID string
}

// IsEmpty reports whether no filter dimension is set.
func (f SearchFilters) IsEmpty() bool {
	return len(f.ContentType) == 0 && len(f.Host) == 0 && len(f.Source) == 0 &&
		f.ExcludeDocumentID == ""
}

// ChunkEmbedding is one stored chunk vector, read back from chunks_vec.
type ChunkEmbedding struct {
	ChunkID   string
	Embedding []float32
}

// DocVector is a document's mean-pooled embedding — the average of its stored
// chunk vectors, in the same space and of the same width. The insight layer
// clusters over these; find-related builds the equivalent on demand per
// document.
type DocVector struct {
	DocumentID string
	Vector     []float32
}

// MeanVector is the element-wise mean of the vectors added to it, summed in
// float64 and rounded to float32 once. DocumentVectors pools each
// document's chunk vectors with it, and the insight layer a document it
// places between rebuilds, so the two see one vector for the same chunks.
// The zero value has no vectors.
type MeanVector struct {
	sum []float64
	n   int
}

// Add adds v to the mean. Every vector must have the first one's width:
// one of another width is an error, and is not added.
func (m *MeanVector) Add(v []float32) error {
	if m.n == 0 {
		m.sum = make([]float64, len(v))
	} else if len(v) != len(m.sum) {
		return fmt.Errorf("a %d-dimensional vector for a mean of %d-dimensional ones", len(v), len(m.sum))
	}
	for i, x := range v {
		m.sum[i] += float64(x)
	}
	m.n++
	return nil
}

// Mean returns the mean of the vectors added, nil when there were none.
func (m *MeanVector) Mean() []float32 {
	if m.n == 0 {
		return nil
	}
	mean := make([]float32, len(m.sum))
	for i, s := range m.sum {
		mean[i] = float32(s / float64(m.n))
	}
	return mean
}

// ChunkSample is how many documents ChunkStore.SampleChunks represents by
// their longest chunk, and how many by a random one.
type ChunkSample struct {
	Longest int
	Random  int
}

// SampledChunk is a chunk ChunkStore.SampleChunks chose: its text exactly
// as stored (without the document prefix the embedder was sent) and the
// vector stored for it.
type SampledChunk struct {
	ChunkID    string
	DocumentID string
	Text       string
	Embedding  []float32
}

// ChunkStore writes and queries the chunks tables + FTS5 + vec virtual tables.
type ChunkStore interface {
	// ReplaceForDocument atomically deletes all existing chunks for the
	// document and inserts the new set. Idempotent — safe to retry after
	// crashes or refetches.
	ReplaceForDocument(ctx context.Context, documentID, extractionID, title string, tags []string, chunks []ChunkInput) error

	// BM25Search runs FTS5 MATCH against chunk text and returns the top
	// matches for the given tenant, scoped by filters, best first, equal
	// scores in the order the chunks were written. Chunks of failed and
	// dead documents are never returned: they come from an earlier fetch
	// that no longer describes what the URL serves. Pending documents (a
	// refetch in flight) are searched.
	BM25Search(ctx context.Context, tenantID, query string, limit int, filters SearchFilters) ([]ChunkHit, error)

	// Snippets returns, by chunk ID, the snippet of each of chunkIDs for
	// query, a MATCH expression as BM25Search takes it: the passage of its
	// text around the matched terms, each between <em> and </em>. A chunk
	// the query doesn't match, or that no longer exists, is absent. No IDs,
	// or a blank query, which BM25Search matches nothing for, is no
	// snippets, read with no query.
	Snippets(ctx context.Context, query string, chunkIDs []string) (map[string]string, error)

	// VectorSearch runs an approximate-nearest-neighbor query against
	// chunks_vec, scoped by filters, and like BM25Search never returns
	// chunks of failed or dead documents. It over-fetches neighbors, so
	// excluded chunks nearest the query don't crowd out the hits it
	// returns. The embedding length must match the schema's vec dimension;
	// mismatched lengths return an error.
	VectorSearch(ctx context.Context, tenantID string, embedding []float32, limit int, filters SearchFilters) ([]ChunkHit, error)

	// EmbeddingsForDocument reads the stored chunk vectors for a document
	// in chunk order, whatever its state. Returns an empty slice for a
	// document with no indexed chunks (never indexed). A document that
	// failed or went dead after an earlier successful fetch keeps that
	// fetch's chunks.
	EmbeddingsForDocument(ctx context.Context, documentID string) ([]ChunkEmbedding, error)

	// DocumentVectors returns one mean-pooled vector per fetched document in
	// the tenant that has at least one indexed chunk, in a single pass.
	// Documents with no chunks are omitted. Each vector has length == the
	// configured embedding dim. This is the corpus-wide input to clustering.
	DocumentVectors(ctx context.Context, tenantID string) ([]DocVector, error)

	// SampleChunks returns a sample of the tenant's fetched documents'
	// chunks, each with its text as stored and its stored vector, at most
	// one chunk per document, in no particular order. n.Longest documents
	// are represented by their longest chunk in bytes: those whose longest
	// chunks are the longest, ties going to the chunk written first (the
	// lower seq), within a document and between documents. n.Random more
	// documents are then chosen uniformly among the rest, and one chunk
	// uniformly from each. A library with fewer documents gives fewer
	// chunks; one with none, an empty sample. It reads every chunk's
	// length, one pass over the chunks table.
	SampleChunks(ctx context.Context, tenantID string, n ChunkSample) ([]SampledChunk, error)

	// GetByIDs returns the chunks with the given IDs, in no particular
	// order. IDs that match no chunk (a reindex replaced it since it was
	// retrieved, say) are left out; none matching is an empty result, not
	// an error.
	GetByIDs(ctx context.Context, ids []string) ([]*Chunk, error)
}

// DocumentJobPayload is the payload of a job that works on one document
// (fetch and index).
type DocumentJobPayload struct {
	DocumentID string `json:"document_id"`
}

// NewDocumentJob builds a job of kind for one document, ready to enqueue.
func NewDocumentJob(tenantID string, kind JobKind, documentID string) (*Job, error) {
	payload, err := json.Marshal(DocumentJobPayload{DocumentID: documentID})
	if err != nil {
		return nil, fmt.Errorf("encode %s job payload: %w", kind, err)
	}
	return &Job{TenantID: tenantID, Kind: kind, Payload: payload}, nil
}

// DeferralBudget is how long after its creation a job may still be
// deferred (JobQueue.Defer); past it, the worker counts a deferral as a
// failed attempt. A day is 24 of GitHub's hourly resets, time for about
// 700 repositories at 2 calls each without a token, and the longest
// Retry-After the fetchers honor. An upstream that holds curio off for
// longer won't serve it: a visible failure that a refetch retries beats a
// document pending for days. The budget counts from created_at, which the
// queue already keeps, and a refetch enqueues a new job with a new budget.
const DeferralBudget = 24 * time.Hour

// JobQueue is the SQLite-backed work queue.
//
// The transitions out of running (MarkDone, MarkFailed, Requeue, Defer)
// only apply to a job that is currently running. Otherwise they change
// nothing and return ErrNotRunning, or ErrNotFound if the job doesn't
// exist.
type JobQueue interface {
	// Enqueue inserts j, filling in its ID and timestamps. A payload that
	// names a document_id (see DocumentJobPayload) links the job to that
	// document, which must exist: one that doesn't is an error wrapping
	// ErrNotFound.
	Enqueue(ctx context.Context, j *Job) error
	// EnqueueOnce inserts j, as Enqueue does, unless j's tenant already
	// has a pending job of j's kind: then it fills j in from that job (ID,
	// payload, status, run_after and timestamps) and inserts nothing.
	// queued reports whether it inserted. The check and the insert are one
	// write transaction, so callers racing each other queue one job. A
	// running job of the kind doesn't count.
	EnqueueOnce(ctx context.Context, j *Job) (queued bool, err error)
	// ClaimNext atomically marks the next runnable job (status=pending,
	// run_after<=now) as running, counts the attempt (attempts+1), and
	// returns it. Returns ErrNotFound if nothing is runnable.
	ClaimNext(ctx context.Context, kinds []JobKind) (*Job, error)
	// Enqueued returns a channel that is closed once a job of one of kinds
	// (any kind when kinds is empty) has been enqueued, or put back to
	// pending, through this queue in this process, and committed. Wakeups
	// may be spurious: whoever wakes claims to find out. Take the channel
	// before a ClaimNext that finds nothing, so a job enqueued in between
	// still closes it. Jobs enqueued by another process, and pending jobs
	// that come due by run_after, close nothing: find those by polling.
	Enqueued(kinds []JobKind) <-chan struct{}
	// MarkDone sets a running job to done and clears its last_error: a
	// done job has nothing left to explain.
	MarkDone(ctx context.Context, id string) error
	// MarkFailed records errMsg on a running job and either sends it back to
	// pending with a backoff run_after, or sets it failed when retry is false
	// or its attempts (counted by ClaimNext) are exhausted. The retry policy
	// lives in the queue impl, not in the caller. Returns permanent=true for
	// the terminal case so callers can do kind-specific cleanup, e.g.
	// updating a parent document's state.
	MarkFailed(ctx context.Context, id string, errMsg string, retry bool) (permanent bool, err error)
	// Requeue sends a running job back to pending, runnable now, and refunds
	// the attempt its claim counted: the run was interrupted (the daemon is
	// shutting down), so it says nothing about the job. last_error is kept.
	Requeue(ctx context.Context, id string) error
	// Defer sends a running job back to pending, runnable at until, refunds
	// the attempt its claim counted, and records reason, why it waits, as
	// its last_error: the handler was held back, by a limit curio keeps
	// itself or a hold an upstream names the end of, so the run says
	// nothing about the job. The job isn't
	// runnable before until, so Defer closes no Enqueued channel: workers
	// find it by polling, as they find a retry's backoff.
	Defer(ctx context.Context, id string, until time.Time, reason string) error
	// RecoverOrphans handles jobs of the given kinds left running by a daemon
	// that exited without recording their outcome (crash, SIGKILL, a shutdown
	// that timed out). Each goes back to pending, runnable now, keeping the
	// attempt it used, so a job that keeps taking the daemon down runs out of
	// attempts. One with none left is set failed instead and returned, for the
	// caller's permanent-failure cleanup; requeued counts the rest. kinds must
	// be non-empty; jobs of other kinds are untouched.
	RecoverOrphans(ctx context.Context, kinds []JobKind) (failed []*Job, requeued int, err error)
	GetByID(ctx context.Context, id string) (*Job, error)
}

// JobStore is the queue as the API sees it: the claim-and-transition
// methods workers use (JobQueue), plus listing, counts, metrics and
// retention. Workers depend on JobQueue alone.
type JobStore interface {
	JobQueue
	// ListWithDoc lists the tenant's jobs, most recently updated first (then
	// by ID, descending), each joined to the document it works on.
	ListWithDoc(ctx context.Context, tenantID string, opts ListJobsOpts) ([]JobWithDoc, error)
	// GetWithDoc returns one of the tenant's jobs joined to its document, as
	// ListWithDoc lists it. ErrNotFound if the tenant has no such job.
	GetWithDoc(ctx context.Context, tenantID, id string) (*JobWithDoc, error)
	// CountByStatus counts the tenant's jobs per status. Statuses with no
	// jobs are absent from the map.
	CountByStatus(ctx context.Context, tenantID string) (map[JobStatus]int, error)
	// MetricsByKind reports per-kind durations and failures over jobs that
	// finished within window, plus what is running right now.
	MetricsByKind(ctx context.Context, tenantID string, window time.Duration) ([]KindMetrics, error)
	// DeleteByStatus deletes the tenant's jobs in status, which must be a
	// finished one (see JobStatus.IsFinished). Returns how many it deleted.
	DeleteByStatus(ctx context.Context, tenantID string, status JobStatus) (int64, error)
	// PruneOlderThan deletes the tenant's finished jobs last updated before
	// the cutoff. Pending and running jobs are kept however old they are.
	PruneOlderThan(ctx context.Context, tenantID string, before time.Time) (int64, error)
	// QueueCounts counts the pending and running jobs of each kind, across
	// tenants: workers claim across tenants, so the queue they work through
	// is daemon-wide. Pending counts every pending job; DueLater and NextDue
	// single out those whose run_after is still ahead. Kinds with neither
	// pending nor running jobs are absent from the map.
	QueueCounts(ctx context.Context) (map[JobKind]QueueCount, error)
	// AttemptLimit is how many attempts a job gets: MarkFailed fails it
	// for good once its attempts reach this many.
	AttemptLimit() int
}

// QueueCount is how many jobs of one kind are waiting and running.
type QueueCount struct {
	Pending, Running int
	// DueLater is how many of the pending jobs can't run yet: their
	// run_after, a retry's backoff or a deferral's hold, is still ahead.
	// NextDue is the earliest of those run_afters; zero when there are
	// none.
	DueLater int
	NextDue  time.Time
}

// ListJobsOpts filters JobStore.ListWithDoc. Empty fields mean "no filter
// for that dimension".
type ListJobsOpts struct {
	Status     JobStatus
	Kind       JobKind
	DocumentID string  // the jobs that work on this document
	Limit      int     // <= 0 means the impl default (50)
	After      PageKey // updated_at and ID of the previous page's last row
}

// JobWithDoc is a job plus the URL, title and current markdown path of the
// document it works on. All three are empty for a job without a document
// (cluster) or whose document has been deleted.
type JobWithDoc struct {
	*Job
	URL          string
	Title        string
	MarkdownPath string // relative to the content dir
}

// KindMetrics is the performance picture for one job kind over a window.
// Durations run from started_at to updated_at and cover successful jobs
// only: a failed job's time is dominated by retry backoff, not work.
type KindMetrics struct {
	Kind                 JobKind
	Count                int // done + failed in the window
	Failed               int
	MeanMS               float64
	P50MS                float64
	P95MS                float64
	P99MS                float64
	Running              int // running now; not bounded by the window
	OldestRunningSeconds int // age of the oldest running job
}

// QueueSettings are the queue gate's settings: whether the queue is paused,
// how hard it runs, and the daily window it may run in, and whether the
// daemon keeps the Mac awake while there is work. They are daemon-wide,
// like the claims they gate, and comparable with ==.
type QueueSettings struct {
	Paused   bool
	Throttle Throttle
	Schedule DailyWindow // zero: no schedule, the queue may run at any time
	// KeepAwake holds the Mac out of idle sleep while the workers have
	// queued work, the queue isn't paused and it runs on AC power
	// (internal/keepawake). It never gates a claim.
	KeepAwake bool
}

// DefaultQueueSettings are the settings of a daemon nobody has paused,
// throttled or scheduled.
func DefaultQueueSettings() QueueSettings {
	return QueueSettings{Throttle: ThrottleNormal}
}

// QueueSettingsStore keeps the queue gate's settings across restarts.
type QueueSettingsStore interface {
	// Get returns the stored settings, or DefaultQueueSettings when none
	// have been stored.
	Get(ctx context.Context) (QueueSettings, error)
	// Put stores s, replacing what was there. An invalid throttle or
	// schedule is an error, and nothing is written.
	Put(ctx context.Context, s QueueSettings) error
}

// minutesPerDay bounds a DailyWindow's ends.
const minutesPerDay = 24 * 60

// DailyWindow is a span of every day on a wall clock, from Start up to but
// not including End, both in minutes after midnight (0 to 1439). An End
// before Start wraps midnight: 22:00-07:00 runs overnight. The zero value
// is no window, and contains every time.
//
// A wall clock skips or repeats times when daylight saving starts or ends.
// Contains goes by the wall clock alone, so a window is open twice in a
// repeated hour; NextStart moves a start that falls in a skipped hour to
// the moment the clock resumes.
type DailyWindow struct {
	Start, End int
}

// ParseDailyWindow parses "HH:MM-HH:MM", hours 0 to 23 with one or two
// digits and minutes with two. Equal ends are an error: the window would be
// either empty or the whole day, and neither needs a schedule.
func ParseDailyWindow(s string) (DailyWindow, error) {
	startText, endText, ok := strings.Cut(s, "-")
	if !ok {
		return DailyWindow{}, errors.New("want two times of day joined by '-', HH:MM-HH:MM")
	}
	start, err := parseMinuteOfDay(startText)
	if err != nil {
		return DailyWindow{}, err
	}
	end, err := parseMinuteOfDay(endText)
	if err != nil {
		return DailyWindow{}, err
	}
	if start == end {
		return DailyWindow{}, errors.New("the start and end are the same time")
	}
	return DailyWindow{Start: start, End: end}, nil
}

// parseMinuteOfDay parses "HH:MM" into minutes after midnight.
func parseMinuteOfDay(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a time of day from 00:00 to 23:59", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// String formats w as "HH:MM-HH:MM", which ParseDailyWindow reads back.
func (w DailyWindow) String() string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.Start/60, w.Start%60, w.End/60, w.End%60)
}

// IsZero reports whether w is the zero window: no schedule.
func (w DailyWindow) IsZero() bool {
	return w == DailyWindow{}
}

// Valid reports whether w is the zero window, or has both ends in the day
// and different.
func (w DailyWindow) Valid() bool {
	inDay := func(m int) bool { return m >= 0 && m < minutesPerDay }
	return w.IsZero() || (inDay(w.Start) && inDay(w.End) && w.Start != w.End)
}

// Contains reports whether t's wall clock, in t's location, is in w.
func (w DailyWindow) Contains(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if w.Start < w.End {
		return w.Start <= m && m < w.End
	}
	return m >= w.Start || m < w.End
}

// NextStart returns the first time after t, in t's location, at which w
// opens: its start today, tomorrow or the day after. A start that falls in
// a daylight-saving gap opens when the gap ends, unless the whole window
// falls in the gap, and then it opens the next day. It returns the zero
// time for the zero window or an invalid one.
func (w DailyWindow) NextStart(t time.Time) time.Time {
	if w.IsZero() || !w.Valid() {
		return time.Time{}
	}
	year, month, day := t.Date()
	// Three days always suffice: daylight-saving changes are months apart,
	// so at most one of the days has a gap.
	for offset := range 3 {
		c := time.Date(year, month, day+offset, w.Start/60, w.Start%60, 0, 0, t.Location())
		if c.Hour()*60+c.Minute() != w.Start {
			c = gapEnd(c)
		}
		if c.After(t) && w.Contains(c) {
			return c
		}
	}
	return time.Time{}
}

// gapEnd returns when the daylight-saving gap that time.Date moved c out of
// ends. time.Date doesn't promise which side of a gap it lands a skipped
// time on (Go 1.26 puts New York's 02:30 before its gap, at 01:30 EST, and
// Lord Howe's 02:15 after its, at 02:45), so the gap's end is whichever
// boundary of c's zone period is nearer.
func gapEnd(c time.Time) time.Time {
	start, end := c.ZoneBounds() // zero at either end of the zone's history
	switch {
	case start.IsZero():
		return end
	case end.IsZero(), c.Sub(start) < end.Sub(c):
		return start
	default:
		return end
	}
}

// InterestRunStatus is an interest run's state (interest_runs.status).
type InterestRunStatus string

// Interest run statuses. A run starts running and ends done, when its
// grouping is committed, or failed.
const (
	InterestRunRunning InterestRunStatus = "running"
	InterestRunDone    InterestRunStatus = "done"
	InterestRunFailed  InterestRunStatus = "failed"
)

// Valid reports whether s is one of the InterestRunStatus constants.
func (s InterestRunStatus) Valid() bool {
	switch s {
	case InterestRunRunning, InterestRunDone, InterestRunFailed:
		return true
	}
	return false
}

// IsFinished reports whether s is terminal (done or failed).
func (s InterestRunStatus) IsFinished() bool {
	return s == InterestRunDone || s == InterestRunFailed
}

// RunTrigger is what started an interest run (interest_runs.trigger).
type RunTrigger string

// Run triggers.
const (
	// RunTriggerFirst: the daemon found no done run at start.
	RunTriggerFirst RunTrigger = "first"
	// RunTriggerAuto: the library changed enough since the last run.
	RunTriggerAuto RunTrigger = "auto"
	// RunTriggerManual: someone asked for a rebuild.
	RunTriggerManual RunTrigger = "manual"
	// RunTriggerReindex: the library was re-embedded.
	RunTriggerReindex RunTrigger = "reindex"
	// RunTriggerParams: the grouper's parameters changed.
	RunTriggerParams RunTrigger = "params"
	// RunTriggerShape: the library crossed the gate between the shapes.
	RunTriggerShape RunTrigger = "shape"
)

// Valid reports whether t is one of the RunTrigger constants.
func (t RunTrigger) Valid() bool {
	switch t {
	case RunTriggerFirst, RunTriggerAuto, RunTriggerManual, RunTriggerReindex, RunTriggerParams, RunTriggerShape:
		return true
	}
	return false
}

// RunKind is whether an interest run started from the previous grouping
// (interest_runs.kind).
type RunKind string

// Run kinds.
const (
	RunKindFresh RunKind = "fresh"
	RunKindWarm  RunKind = "warm"
)

// Valid reports whether k is one of the RunKind constants.
func (k RunKind) Valid() bool { return k == RunKindFresh || k == RunKindWarm }

// InterestShape is how a run's grouping is organized
// (interest_runs.shape).
type InterestShape string

// Interest shapes.
const (
	// InterestShapeFlat is one level of interests.
	InterestShapeFlat InterestShape = "flat"
	// InterestShapeAreas is broad areas, each holding interests.
	InterestShapeAreas InterestShape = "areas"
)

// Valid reports whether s is one of the InterestShape constants.
func (s InterestShape) Valid() bool { return s == InterestShapeFlat || s == InterestShapeAreas }

// InterestLevel is which level of a grouping an identity is
// (interests.level).
type InterestLevel string

// Interest levels.
const (
	InterestLevelArea     InterestLevel = "area"
	InterestLevelInterest InterestLevel = "interest"
)

// Valid reports whether l is one of the InterestLevel constants.
func (l InterestLevel) Valid() bool { return l == InterestLevelArea || l == InterestLevelInterest }

// LabelSource is what named an identity (interests.label_source).
type LabelSource string

// Label sources.
const (
	LabelSourceLLM   LabelSource = "llm"
	LabelSourceTerms LabelSource = "terms"
	// LabelSourceUser is a rename; nothing writes it yet.
	LabelSourceUser LabelSource = "user"
)

// Valid reports whether s is one of the LabelSource constants.
func (s LabelSource) Valid() bool {
	return s == LabelSourceLLM || s == LabelSourceTerms || s == LabelSourceUser
}

// InterestFit is how a document of a run sits in its grouping
// (interest_assignments.fit).
type InterestFit string

// Fits.
const (
	// InterestFitMember: the grouping put the document in an interest.
	InterestFitMember InterestFit = "member"
	// InterestFitLoose: in no interest, but close to one.
	InterestFitLoose InterestFit = "loose"
	// InterestFitUnsorted: close to none.
	InterestFitUnsorted InterestFit = "unsorted"
)

// Valid reports whether f is one of the InterestFit constants.
func (f InterestFit) Valid() bool {
	return f == InterestFitMember || f == InterestFitLoose || f == InterestFitUnsorted
}

// LineageEvent is what a run did to an old identity toward a new one
// (interest_lineage.event).
type LineageEvent string

// Lineage events. A dissolved identity and a new one have no lineage row:
// the run that retired the one, or created the other, says so.
const (
	// LineageKept: the new group inherits the old identity, where it was.
	LineageKept LineageEvent = "kept"
	// LineageMoved: the new group inherits it, in another area.
	LineageMoved LineageEvent = "moved"
	// LineageSplit: a part of the old identity went to a group of its own.
	LineageSplit LineageEvent = "split"
	// LineageMerged: a part of it went to a group shared with others.
	LineageMerged LineageEvent = "merged"
)

// Valid reports whether e is one of the LineageEvent constants.
func (e LineageEvent) Valid() bool {
	switch e {
	case LineageKept, LineageMoved, LineageSplit, LineageMerged:
		return true
	}
	return false
}

// RunOutcome is what an interest run found and did. CreateRun records what
// a run sets out to do (Kind, SplitCheck, Shape), and CommitRun all of it.
type RunOutcome struct {
	Kind       RunKind
	SplitCheck bool // whether the run ran the split check
	Shape      InterestShape
	// Mean is the mean the run centered the vectors on, nil when it
	// didn't, for placing documents in its space.
	Mean []float32

	NumDocuments int // documents grouped: members, loose fits and unsorted
	NumAreas     int
	NumInterests int
	NumLoose     int
	NumUnsorted  int
	// ChangedDocuments are the documents that changed since the previous
	// run read its vectors (RunChanges: added, left, deleted or
	// reindexed), and ChangesSinceSplit those absorbed since the last
	// split check.
	ChangedDocuments  int
	ChangesSinceSplit int

	// What the run did to the previous run's interest identities. Kept
	// includes Moved, and Kept + Created is the run's interests.
	Kept, Created, Split, Merged, Moved, Dissolved int
}

// InterestRun is one rebuild of a tenant's interests. A done run never
// changes; the tenant's current grouping is its latest done run.
type InterestRun struct {
	ID       string
	TenantID string
	Status   InterestRunStatus
	Trigger  RunTrigger
	Grouper  string          // the grouper's name, e.g. "louvain"
	Params   json.RawMessage // the grouper's params and the engine's; nil means absent
	// VectorsReadAt is when the run read the library's vectors; nil when
	// unknown.
	VectorsReadAt *time.Time
	RunOutcome
	Error      *string // set only for a failed run
	StartedAt  time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Interest is an identity: an area or an interest as it outlives runs,
// with its label. Label, Summary and LabelSource are empty until labeled.
type Interest struct {
	ID           string
	TenantID     string
	Level        InterestLevel
	Label        string
	Summary      string
	LabelSource  LabelSource
	CreatedRunID string     // the run that minted it
	LabeledAt    *time.Time // when it was last labeled
	// RetiredAt is when a run that no longer held it retired it, and
	// RetiredRunID that run; nil and "" while it is live.
	RetiredAt    *time.Time
	RetiredRunID string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// InterestGroup is an identity as one run found it. ParentID is an
// interest's area, "" for an area or a flat interest; ParentLabel is that
// area's label, on reads. Size counts its members and Loose its loose fits.
// Centroid is the unit mean of an interest's members in the run's space,
// nil for an area; only RunGroups reads it.
type InterestGroup struct {
	RunID string
	Interest
	ParentID    string
	ParentLabel string
	Size        int
	Loose       int
	Cohesion    float64
	Centroid    []float32
}

// InterestAssignment is where one document sits in a run's grouping: a
// member or a loose fit of InterestID, or unsorted (InterestID "") with
// NearestID the interest it is closest to ("" when there is none) and
// NearestLabel, on reads, that interest's label. Similarity is to
// InterestID's centroid, or NearestID's. AreaID is the area whose community
// holds the document, "" outside every area; the seeds are what the next
// run's warm start reads, -1 for none.
type InterestAssignment struct {
	DocumentID   string
	InterestID   string
	AreaID       string
	Fit          InterestFit
	Similarity   float64
	NearestID    string
	NearestLabel string
	AreaSeed     int
	InterestSeed int
}

// LineageRow is what a run did to an old identity toward a new one: Shared
// is how many of the old identity's members the new one holds.
type LineageRow struct {
	RunID  string
	OldID  string
	NewID  string
	Event  LineageEvent
	Shared int
}

// Placement is a document placed into a run's grouping since it was
// committed: into InterestID, or into Unsorted when that is "".
type Placement struct {
	RunID      string
	DocumentID string
	InterestID string
	Similarity float64
	PlacedAt   time.Time
}

// DocumentPlace is where one document sits in a run's grouping: the run's
// assignment of it, or, when the run didn't assign it, the placement made
// since (Placed). Fit is the assignment's fit, empty for a placement.
// Interest is its interest, "" for Unsorted, and Area that interest's area,
// "" for none; Nearest is an unsorted assignment's nearest interest, ""
// when there is none. Each label is "" while unlabeled.
type DocumentPlace struct {
	Placed                    bool
	Fit                       InterestFit
	InterestID, InterestLabel string
	AreaID, AreaLabel         string
	NearestID, NearestLabel   string
	Similarity                float64
}

// RunChanges are the documents that changed since a run R read its
// vectors (InsightStore.Changes): what the next rebuild would see
// differently. A document R couldn't group (it had no chunk, or a vector
// R dropped as non-finite) was indexed before R's read, so it never
// counts.
type RunChanges struct {
	// Added are fetched, indexed at or after R's read, and not assigned
	// in R (placed since or not).
	Added int
	// Left were assigned in R and are pending (a refetch in flight),
	// failed or dead now.
	Left int
	// Deleted were assigned in R and are gone: their assignments went
	// with them.
	Deleted int
	// Reindexed were assigned in R and are fetched, indexed again at or
	// after R's read.
	Reindexed int
}

// Total is every change: what the scheduler holds to the threshold, and a
// run records as its ChangedDocuments.
func (c RunChanges) Total() int { return c.Added + c.Left + c.Deleted + c.Reindexed }

// FreshReason is why a tenant's next rebuild must be fresh, not warm from
// the current grouping (insight_state.fresh_owed).
type FreshReason string

// Fresh reasons.
const (
	// FreshReindex: `curio reindex --all` re-embedded the library, so the
	// current grouping's seeds came from vectors no longer stored.
	FreshReindex FreshReason = "reindex"
	// FreshManual: someone asked for a fresh rebuild.
	FreshManual FreshReason = "manual"
)

// Valid reports whether r is one of the FreshReason constants.
func (r FreshReason) Valid() bool { return r == FreshReindex || r == FreshManual }

// InsightState is what a tenant's rebuilds can't derive from runs
// (insight_state): the fresh rebuild owed, and the failures since the last
// done rebuild. The zero value is a tenant that owes nothing and has no
// failure.
type InsightState struct {
	// FreshOwed is why the next rebuild must be fresh, "" when it needn't
	// be, and FreshOwedAt when it was last owed; zero with it.
	FreshOwed   FreshReason
	FreshOwedAt time.Time
	// Failures counts the rebuilds that failed since the last done one;
	// LastFailureAt and LastError are the latest's, zero without one.
	Failures      int
	LastFailureAt time.Time
	LastError     string
}

// RunCommit is a run's grouping, written at once by CommitRun.
type RunCommit struct {
	RunID    string
	TenantID string
	// PriorRunID is the tenant's latest done run the grouping was built
	// from, "" for none: a commit made from a run that is no longer the
	// latest would undo what that one did.
	PriorRunID string
	Outcome    RunOutcome
	// NewIdentities are the identities the run mints: ID, Level and the
	// label fields; the run is their CreatedRunID.
	NewIdentities []Interest
	// Relabels are carried identities named anew: ID and the label fields.
	Relabels []Interest
	// Groups are the run's groups: ID (the identity), ParentID, Size,
	// Loose, Cohesion and Centroid.
	Groups      []InterestGroup
	Assignments []InterestAssignment
	// Lineage is the run's lineage; their RunID is the run's.
	Lineage []LineageRow
	// ReadMidReindex says index jobs were pending or running before or
	// after the run read its vectors, while a re-embedding owed a fresh
	// rebuild: some of the vectors may be of the old build, so the commit
	// leaves that rebuild owed (FreshReindex), for one that reads the new
	// build's alone.
	ReadMidReindex bool
}

// InsightStore persists interests: the runs that group a tenant's library,
// the identities that outlive them, what each run did to them, the
// documents placed between rebuilds, and what the tenant's next rebuild
// can't derive from runs (InsightState). Runs and identities carry
// tenant_id; groups, assignments and placements inherit their tenant
// through their run.
//
// Paged reads return at most limit rows from offset in their order; limit
// <= 0 means every one from offset, an offset at or past the end returns
// none, and a negative one is an error before any read. A run's groups and
// assignments are written once and never change, so these orders never do
// either, and an offset names the same rows on every read of a run.
type InsightStore interface {
	// CreateRun inserts a running run with its Trigger, Kind, SplitCheck,
	// Shape, Grouper, Params and VectorsReadAt, and fills in its ID (when
	// empty) and timestamps. A missing tenant or grouper, or an invalid
	// enum, is an error, and nothing is written; a run that exists is
	// ErrConflict.
	CreateRun(ctx context.Context, run *InterestRun) error
	// CommitRun writes c in one transaction: it fails with ErrConflict,
	// writing nothing, unless c.PriorRunID is still the tenant's latest
	// done run; inserts the new identities, relabels the carried ones,
	// writes the groups, assignments and lineage; retires every live
	// identity of the tenant that c.Groups doesn't hold, at the commit's
	// time and by this run; and moves the run from running to done with
	// c.Outcome. It also clears the tenant's failures, a done rebuild
	// being what they count up to, and, for a fresh run, the fresh
	// rebuild owed when it was owed at or before the run read its vectors
	// (one owed again since is still owed), unless a re-embedding owes it
	// and c.ReadMidReindex. A run that isn't running is ErrConflict. Any
	// failure rolls back all of it.
	CommitRun(ctx context.Context, c RunCommit) error
	// FailRun moves a running run to failed with msg, numDocuments and its
	// finish time, counting no failure: for a rebuild that was cancelled.
	// A finished run is ErrConflict and left as it is; an unknown one
	// ErrNotFound.
	FailRun(ctx context.Context, runID string, numDocuments int, msg string) error
	// RecordFailure counts a failed rebuild of the tenant: one more
	// failure, failing now with msg. With a runID it also fails that run,
	// as FailRun does, in the same transaction (a run that isn't running
	// is ErrConflict, and nothing is written); with "" the rebuild failed
	// before it created one. It returns the tenant's state after.
	RecordFailure(ctx context.Context, tenantID, runID string, numDocuments int, msg string) (InsightState, error)
	// RecordAbandoned counts a rebuild of the tenant that a daemon left
	// unfinished as one failure with msg, and fails every running run of
	// the tenant with msg, in one transaction. It returns the tenant's
	// state after.
	RecordAbandoned(ctx context.Context, tenantID, msg string) (InsightState, error)
	// State returns the tenant's insight state; the zero value when it has
	// none.
	State(ctx context.Context, tenantID string) (InsightState, error)
	// OweFresh records that the tenant's next rebuild must be fresh, for
	// reason, owed from now. FreshReindex always is; FreshManual is
	// written only when nothing, or a manual rebuild, is owed, so it never
	// takes the place of a re-embedding's. An invalid reason is an error.
	OweFresh(ctx context.Context, tenantID string, reason FreshReason) error
	// Changes counts the documents of run's tenant that changed since run
	// read its vectors (its StartedAt when unknown), from one read
	// snapshot. The run is the tenant's done run; its NumDocuments tells
	// the deleted documents from its assignments left.
	Changes(ctx context.Context, run *InterestRun) (RunChanges, error)
	// LatestRun returns the tenant's newest run in status, any status when
	// it is "". ErrNotFound if there is none.
	LatestRun(ctx context.Context, tenantID string, status InterestRunStatus) (*InterestRun, error)
	// GetRun returns a run by ID, or ErrNotFound.
	GetRun(ctx context.Context, id string) (*InterestRun, error)

	// TopGroups returns a page of a run's top-level groups (its areas, or
	// its interests in the flat shape), by size, then cohesion, both
	// descending, then by ID.
	TopGroups(ctx context.Context, runID string, limit, offset int) ([]InterestGroup, error)
	// ChildGroups returns the interests of a run's areas, area by area in
	// the order of areaIDs' identities, each area's in TopGroups' order,
	// in one read.
	ChildGroups(ctx context.Context, runID string, areaIDs []string) ([]InterestGroup, error)
	// NestedGroups returns a page of an areas-shaped run's interests, each
	// with its area's label, in TopGroups' order.
	NestedGroups(ctx context.Context, runID string, limit, offset int) ([]InterestGroup, error)
	// GetGroup returns the identity id as run found it, with its area's
	// label; ErrNotFound when the run doesn't hold it.
	GetGroup(ctx context.Context, runID, id string) (*InterestGroup, error)

	// Members returns a page of an interest's documents of fit (member or
	// loose), most similar first, then by document ID.
	Members(ctx context.Context, runID, interestID string, fit InterestFit, limit, offset int) ([]InterestAssignment, error)
	// Unsorted returns a page of a run's unsorted documents, most similar
	// to their nearest interest first, then by document ID, each with that
	// interest's label.
	Unsorted(ctx context.Context, runID string, limit, offset int) ([]InterestAssignment, error)

	// RunAssignments returns every assignment of a run, in no particular
	// order.
	RunAssignments(ctx context.Context, runID string) ([]InterestAssignment, error)
	// RunGroups returns every group of a run with its centroid, in no
	// particular order.
	RunGroups(ctx context.Context, runID string) ([]InterestGroup, error)
	// RunLineage returns a run's lineage rows, by old identity, then new.
	RunLineage(ctx context.Context, runID string) ([]LineageRow, error)
	// Successors returns the run's lineage rows of the old identity oldID,
	// by new identity.
	Successors(ctx context.Context, runID, oldID string) ([]LineageRow, error)

	// GetInterest returns an identity by ID, live or retired, or
	// ErrNotFound.
	GetInterest(ctx context.Context, id string) (*Interest, error)
	// GetInterests returns the tenant's identities with those IDs, in no
	// particular order, in one read. An ID the tenant has none of is left
	// out.
	GetInterests(ctx context.Context, tenantID string, ids []string) ([]Interest, error)
	// CreatedBy returns the identities a run minted, in no particular
	// order.
	CreatedBy(ctx context.Context, runID string) ([]Interest, error)
	// RetiredBy returns the tenant's identities a run retired, in no
	// particular order.
	RetiredBy(ctx context.Context, tenantID, runID string) ([]Interest, error)

	// Placements returns up to limit of the documents placed into a run's
	// interest, or into its Unsorted when interestID is "", newest first.
	Placements(ctx context.Context, runID, interestID string, limit int) ([]Placement, error)
	// PlacementCounts counts a run's placements per interest, "" counting
	// those in Unsorted. An interest with none is absent.
	PlacementCounts(ctx context.Context, runID string) (map[string]int, error)
	// Assigned reports whether run assigned the document.
	Assigned(ctx context.Context, runID, documentID string) (bool, error)
	// DocumentPlace returns where run put the document: its assignment,
	// or, when the run didn't assign it, its placement since, each with
	// its interest's area and the labels, in one read. ErrNotFound when
	// the run has neither, or there is no such run.
	DocumentPlace(ctx context.Context, runID, documentID string) (*DocumentPlace, error)
	// PlaceDocument writes p, placing a document into its run, or places
	// it anew, only while p.RunID is the tenant's latest done run, the run
	// didn't assign the document, and the document exists; written
	// reports whether it did. p.PlacedAt is ignored: the placement is
	// timed by the write.
	PlaceDocument(ctx context.Context, tenantID string, p Placement) (written bool, err error)
	// PlaceMany writes the placements into runID, in one transaction,
	// while runID is the tenant's latest done run: a document placed
	// already keeps its placement, and a document gone is left out. It
	// returns how many it wrote; none when the run is no longer the
	// latest. Each placement's RunID and PlacedAt are ignored.
	PlaceMany(ctx context.Context, tenantID, runID string, ps []Placement) (int, error)
	// Unplaced lists the tenant's fetched documents indexed at or after
	// since that runID neither assigned nor placed, in no particular
	// order.
	Unplaced(ctx context.Context, tenantID, runID string, since time.Time) ([]string, error)

	// PruneRunsExcept deletes every run of the tenant except keepRunIDs, at
	// least one, with their groups, assignments and placements. Identities
	// and lineage outlive them.
	PruneRunsExcept(ctx context.Context, tenantID string, keepRunIDs ...string) error
	// PruneRetired deletes the tenant's identities retired before the
	// cutoff, with their lineage, and returns how many. Live identities
	// are never deleted.
	PruneRetired(ctx context.Context, tenantID string, before time.Time) (int, error)
	// TrimLineage deletes the tenant's lineage rows of runs other than
	// keepRunID whose old identity is live: what they say is told again,
	// or superseded, by the runs since. A retired identity's rows stay,
	// for its successors.
	TrimLineage(ctx context.Context, tenantID, keepRunID string) error
}

// ClusterJobPayload is the payload of a cluster job: what asked for the
// rebuild. A job queued before the trigger was recorded has none.
type ClusterJobPayload struct {
	Trigger RunTrigger `json:"trigger,omitempty"`
}
