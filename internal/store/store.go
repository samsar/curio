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

// MaxSearchK is the most documents one search or related query returns.
// The chunk fan-out grows with k, so it also bounds the chunk queries'
// LIMIT. Config validates search.default_k against it, and the API a
// request's k.
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

// ClusterRunStatus is a clustering run's state (cluster_runs.status).
type ClusterRunStatus string

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

	ClusterRunRunning ClusterRunStatus = "running"
	ClusterRunDone    ClusterRunStatus = "done"
	ClusterRunFailed  ClusterRunStatus = "failed"

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
	// FailureCauseJinaRefused: the site served a page curio can't use, and
	// the Jina Reader fallback refused the target (a domain block, a
	// publisher's opt-out, a deterministic 4xx).
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
	// FailureCauseRateLimited: the site, or GitHub or YouTube, rate-limited
	// the requests.
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

// IsFinished reports whether s is terminal (done or failed).
func (s ClusterRunStatus) IsFinished() bool {
	return s == ClusterRunDone || s == ClusterRunFailed
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
	// state becomes fetched, and it has no failure cause. ErrNotFound if
	// there is no such document.
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
	// failed job that targeted it and the markdown path of its current
	// extraction.
	ListWithLastError(ctx context.Context, tenantID string, opts ListDocumentsOpts) ([]DocumentWithError, error)
	// GetWithLastError returns one of the tenant's documents as
	// ListWithLastError lists it. ErrNotFound if the tenant has no such
	// document.
	GetWithLastError(ctx context.Context, tenantID, id string) (*DocumentWithError, error)
	// ListIDsWithContent returns the IDs of the tenant's documents in state
	// that have a current extraction: the ones an index job can work on.
	ListIDsWithContent(ctx context.Context, tenantID string, state DocState) ([]string, error)
	// CountByState counts the tenant's documents per state. States with no
	// documents are absent from the map.
	CountByState(ctx context.Context, tenantID string) (map[DocState]int, error)
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
	// List lists the tenant's bookmarks, newest first by CreatedAt (then by
	// ID, descending), each with its document's state.
	List(ctx context.Context, tenantID string, opts ListBookmarksOpts) ([]BookmarkWithState, error)
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

// BookmarkWithState is a bookmark and the state of the document it links
// to, read in the same query.
type BookmarkWithState struct {
	*Bookmark
	DocumentState DocState // empty when the bookmark links to no document
}

// ListBookmarksOpts are filters for BookmarkStore.List. Empty fields mean
// "no filter for that dimension."
type ListBookmarksOpts struct {
	Source string
	// FolderPath matches that folder and every folder under it, on path
	// segments and case-sensitively: "/Tech/AI" matches "/Tech/AI" and
	// "/Tech/AI/Agents" but not "/Tech/AIRPLANES" or "/tech/ai". Every
	// character is literal, a trailing "/" is ignored, and "/" alone is no
	// filter.
	FolderPath string
	Limit      int     // <= 0 means the impl default (50)
	After      PageKey // created_at and ID of the previous page's last row
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
	Snippet    string // BM25 only; empty for vector hits
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

// ChunkStore writes and queries the chunks tables + FTS5 + vec virtual tables.
type ChunkStore interface {
	// ReplaceForDocument atomically deletes all existing chunks for the
	// document and inserts the new set. Idempotent — safe to retry after
	// crashes or refetches.
	ReplaceForDocument(ctx context.Context, documentID, extractionID, title string, tags []string, chunks []ChunkInput) error

	// BM25Search runs FTS5 MATCH against chunk text and returns the top
	// matches for the given tenant, scoped by filters. Snippet is populated.
	// Chunks of failed and dead documents are never returned: they come
	// from an earlier fetch that no longer describes what the URL serves.
	// Pending documents (a refetch in flight) are searched.
	BM25Search(ctx context.Context, tenantID, query string, limit int, filters SearchFilters) ([]ChunkHit, error)

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

// JobQueue is the SQLite-backed work queue.
//
// The transitions out of running (MarkDone, MarkFailed, Requeue) only apply
// to a job that is currently running. Otherwise they change nothing and
// return ErrNotRunning, or ErrNotFound if the job doesn't exist.
type JobQueue interface {
	// Enqueue inserts j, filling in its ID and timestamps. A payload that
	// names a document_id (see DocumentJobPayload) links the job to that
	// document, which must exist: one that doesn't is an error wrapping
	// ErrNotFound.
	Enqueue(ctx context.Context, j *Job) error
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
	// MarkDone sets a running job to done.
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
	// is daemon-wide. Pending includes retries waiting on run_after. Kinds
	// with neither are absent from the map.
	QueueCounts(ctx context.Context) (map[JobKind]QueueCount, error)
}

// QueueCount is how many jobs of one kind are waiting and running.
type QueueCount struct {
	Pending, Running int
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

// ClusterRun is one execution of the clustering job. Clustering fully
// recomputes from the corpus, so each run is a snapshot; the "current"
// interests are the clusters of the latest run with Status == ClusterRunDone.
type ClusterRun struct {
	ID           string
	TenantID     string
	Status       ClusterRunStatus
	Algo         string          // clusterer name, e.g. "knn-graph"
	Params       json.RawMessage // clusterer params + the engine's "center"; nil means absent
	NumDocuments int             // docs considered (those with vectors)
	NumClusters  int
	NumNoise     int // docs left unclustered
	Error        *string
	StartedAt    time.Time
	FinishedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// RunResult is the outcome FinishRun records for a clustering run.
type RunResult struct {
	Status       ClusterRunStatus // done or failed
	NumDocuments int              // docs considered (those with vectors)
	NumClusters  int
	NumNoise     int     // docs left unclustered
	Error        *string // set only for failed runs
}

// Cluster is one topic within a run: a labeled, sized group of documents.
type Cluster struct {
	ID        string
	TenantID  string
	RunID     string
	Label     *string // topic name; nil until labeled
	Summary   *string // one-line description; nil if none
	Size      int     // member count (denormalized)
	Cohesion  float64 // mean member cosine to the centroid, 0..1
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ClusterMember links a cluster to one member document.
type ClusterMember struct {
	ClusterID  string
	DocumentID string
	Similarity float64 // cosine to the cluster centroid, 0..1
}

// ClusterWithMembers bundles a cluster and its members for an atomic write.
type ClusterWithMembers struct {
	Cluster Cluster
	Members []ClusterMember
}

// InsightStore persists clustering results (the insight layer). Clusters
// and runs carry tenant_id; cluster_documents inherits tenant scope through
// its parent cluster.
type InsightStore interface {
	// CreateRun inserts a new run (status defaults to running). Assigns ID
	// and StartedAt if empty.
	CreateRun(ctx context.Context, run *ClusterRun) error

	// ReplaceClusters writes all clusters + memberships for a run in one
	// transaction, replacing anything previously written for that run (so a
	// job retry is idempotent). Does not change the run's status.
	ReplaceClusters(ctx context.Context, runID string, clusters []ClusterWithMembers) error

	// FinishRun records a run's outcome and sets finished_at. res.Status
	// must be terminal (done or failed); anything else is an error and
	// changes nothing. ErrNotFound if there is no such run.
	FinishRun(ctx context.Context, runID string, res RunResult) error

	// LatestRun returns the most recent run for the tenant matching status
	// (empty status matches any). ErrNotFound if there is none.
	LatestRun(ctx context.Context, tenantID string, status ClusterRunStatus) (*ClusterRun, error)

	// GetRun returns a run by ID, or ErrNotFound.
	GetRun(ctx context.Context, id string) (*ClusterRun, error)

	// ListClusters returns a run's clusters ordered largest-first (size DESC,
	// then cohesion DESC). limit <= 0 means all.
	ListClusters(ctx context.Context, runID string, limit int) ([]*Cluster, error)

	// GetCluster returns a cluster by ID, or ErrNotFound.
	GetCluster(ctx context.Context, id string) (*Cluster, error)

	// ClusterMembers returns a cluster's member documents ordered by
	// similarity DESC. limit <= 0 means all.
	ClusterMembers(ctx context.Context, clusterID string, limit int) ([]ClusterMember, error)

	// PruneRunsExcept deletes every run for the tenant except keepRunID,
	// cascading its clusters + memberships. Keeps storage bounded to the
	// current snapshot.
	PruneRunsExcept(ctx context.Context, tenantID, keepRunID string) error
}
