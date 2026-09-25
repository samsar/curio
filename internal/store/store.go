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

// EmbeddingDim is the width of the vector index: chunks_vec is created as
// FLOAT[768] in migrations/001_initial.sql. Every stored and query embedding
// must have exactly this many components; a different width means rebuilding
// that table.
const EmbeddingDim = 768

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

// State / kind / status constants. Keep in sync with the CHECK constraints
// in migrations/.
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

// Valid reports whether s is one of the DocState constants.
func (s DocState) Valid() bool {
	switch s {
	case DocStatePending, DocStateFetched, DocStateFailed, DocStateDead:
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
	CreatedAt           time.Time
	UpdatedAt           time.Time
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
	// CreatedAt and UpdatedAt. An empty State or ContentType is stored as
	// pending or unknown; those defaults apply on insert only. A new
	// document has no extraction, so CurrentExtractionID must be nil. A
	// document that already exists for (tenant_id, url) is an error
	// wrapping ErrConflict.
	Create(ctx context.Context, d *Document) error
	GetByID(ctx context.Context, id string) (*Document, error)
	GetByURL(ctx context.Context, tenantID, url string) (*Document, error)
	UpdateState(ctx context.Context, id string, state DocState) error
	SetCurrentExtraction(ctx context.Context, documentID, extractionID string) error

	// ApplyFetch records a successful fetch on a document: it points
	// current_extraction_id at m.ExtractionID, which must exist, writes the
	// fetch-derived columns exactly as given, and sets the state to pending
	// until the index step marks it fetched. A nil field clears its column:
	// those columns describe the current extraction, so none may keep a
	// value from an earlier one. word_count is left alone. ErrNotFound if
	// there is no such document.
	ApplyFetch(ctx context.Context, id string, m FetchedMetadata) error

	// RequeueFetch resets the tenant's document to pending and enqueues a
	// fresh fetch job for it, atomically: either both happen or neither
	// does, so a document is never left pending with no job to move it on.
	// Returns the new job, or ErrNotFound if the tenant has no such document.
	RequeueFetch(ctx context.Context, tenantID, documentID string) (*Job, error)
	// RequeueFetchByStates does the same for every tenant document whose
	// state is one of states, in one transaction. Returns how many jobs it
	// enqueued.
	RequeueFetchByStates(ctx context.Context, tenantID string, states []DocState) (int, error)

	// ListWithLastError lists the tenant's documents, most recently updated
	// first (then by ID, descending), each with the error of the most recent
	// failed job that targeted it and the markdown path of its current
	// extraction.
	ListWithLastError(ctx context.Context, tenantID string, opts ListDocumentsOpts) ([]DocumentWithError, error)
	// ListIDsWithContent returns the IDs of the tenant's documents in state
	// that have a current extraction: the ones an index job can work on.
	ListIDsWithContent(ctx context.Context, tenantID string, state DocState) ([]string, error)
	// CountByState counts the tenant's documents per state. States with no
	// documents are absent from the map.
	CountByState(ctx context.Context, tenantID string) (map[DocState]int, error)
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
// mean "no filter for that dimension".
type ListDocumentsOpts struct {
	State DocState
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
	Delete(ctx context.Context, id string) error
	LinkDocument(ctx context.Context, bookmarkID, documentID string) error

	// TagsForDocument returns the deduplicated set of tags across all
	// bookmarks (any source) that reference the document. Empty if none.
	// The indexer uses it to denormalize tags into chunks_fts for boosting.
	TagsForDocument(ctx context.Context, tenantID, documentID string) ([]string, error)

	// Count returns how many bookmarks the tenant has.
	Count(ctx context.Context, tenantID string) (int, error)
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
// matches everything. Every dimension is checked on each hit's document
// after the search finds it, so a filter narrows the results without
// changing what the search reads; host is matched against the document URL
// (there is no host column).
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
// chunk vectors, in the same 768-d space. The insight layer clusters over
// these; find-related builds the equivalent on demand per document.
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
	BM25Search(ctx context.Context, tenantID, query string, limit int, filters SearchFilters) ([]ChunkHit, error)

	// VectorSearch runs an approximate-nearest-neighbor query against
	// chunks_vec, scoped by filters. The embedding length must match the
	// schema's vec dimension; mismatched lengths return an error.
	VectorSearch(ctx context.Context, tenantID string, embedding []float32, limit int, filters SearchFilters) ([]ChunkHit, error)

	// EmbeddingsForDocument reads the stored chunk vectors for a document
	// in chunk order. Returns an empty slice for documents with no indexed
	// chunks (not yet fetched/indexed, or failed).
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
}

// ListJobsOpts filters JobStore.ListWithDoc. Empty fields mean "no filter
// for that dimension".
type ListJobsOpts struct {
	Status JobStatus
	Kind   JobKind
	Limit  int     // <= 0 means the impl default (50)
	After  PageKey // updated_at and ID of the previous page's last row
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

// InsightStore persists clustering results (the M4 insight layer). Clusters
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
