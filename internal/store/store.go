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
	"time"
)

// Sentinel errors returned by store implementations.
var (
	// ErrNotFound: row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict: a uniqueness constraint was violated.
	ErrConflict = errors.New("store: conflict")
)

// State / kind / status constants. Keep in sync with CHECK constraints in
// migrations/001_initial.sql.
const (
	DocStatePending = "pending"
	DocStateFetched = "fetched"
	DocStateFailed  = "failed"
	DocStateDead    = "dead"

	ContentTypeArticle = "article"
	ContentTypeRepo    = "repo"
	ContentTypeVideo   = "video"
	ContentTypePDF     = "pdf"
	ContentTypeThread  = "thread"
	ContentTypeUnknown = "unknown"

	ExtractionStatusOK        = "ok"
	ExtractionStatusPartial   = "partial"
	ExtractionStatusPaywalled = "paywalled"
	ExtractionStatusError     = "error"

	SourceChrome  = "chrome"
	SourceSafari  = "safari"
	SourceFirefox = "firefox"
	SourceManual  = "manual"
	SourceHTML    = "html" // Netscape HTML export, any browser

	JobKindFetch     = "fetch"
	JobKindIndex     = "index"
	JobKindImport    = "import"
	JobKindCluster   = "cluster"
	JobKindSummarize = "summarize"

	JobStatusPending = "pending"
	JobStatusRunning = "running"
	JobStatusDone    = "done"
	JobStatusFailed  = "failed"

	// Insight-layer cluster run states. Keep in sync with the CHECK on
	// cluster_runs.status in migrations/004_insights.sql.
	ClusterRunRunning = "running"
	ClusterRunDone    = "done"
	ClusterRunFailed  = "failed"
)

// Document is the universal content record, deduplicated by (tenant_id, url).
type Document struct {
	ID                  string
	TenantID            string
	URL                 string
	URLCanonical        *string
	ContentType         string
	Title               *string
	Author              *string
	PublishedAt         *time.Time
	Language            *string
	WordCount           *int
	CurrentExtractionID *string
	State               string
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
	DocumentID *string // nil until first successful fetch
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
	Kind      string
	Payload   json.RawMessage
	Status    string
	Attempts  int
	RunAfter  time.Time
	LastError *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DocumentStore operates on the documents table.
type DocumentStore interface {
	// Upsert inserts a document or updates the existing one keyed by
	// (tenant_id, url). Returns the row's ID (auto-generated if empty on
	// input). Idempotent for the URL key.
	Upsert(ctx context.Context, d *Document) error
	GetByID(ctx context.Context, id string) (*Document, error)
	GetByURL(ctx context.Context, tenantID, url string) (*Document, error)
	UpdateState(ctx context.Context, id, state string) error
	SetCurrentExtraction(ctx context.Context, documentID, extractionID string) error
}

// ExtractionStore operates on the document_extractions table.
type ExtractionStore interface {
	Create(ctx context.Context, e *DocumentExtraction) error
	GetByID(ctx context.Context, id string) (*DocumentExtraction, error)
	ListByDocument(ctx context.Context, documentID string) ([]*DocumentExtraction, error)
}

// BookmarkStore operates on the bookmarks table.
type BookmarkStore interface {
	Create(ctx context.Context, b *Bookmark) error
	GetByID(ctx context.Context, id string) (*Bookmark, error)
	List(ctx context.Context, tenantID string, opts ListBookmarksOpts) ([]*Bookmark, error)
	Delete(ctx context.Context, id string) error
	LinkDocument(ctx context.Context, bookmarkID, documentID string) error

	// TagsForDocument returns the deduplicated set of tags across all
	// bookmarks (any source) that reference the document. Empty if none.
	// The indexer uses it to denormalize tags into chunks_fts for boosting.
	TagsForDocument(ctx context.Context, tenantID, documentID string) ([]string, error)
}

// ListBookmarksOpts are filters for BookmarkStore.List. Empty fields mean
// "no filter for that dimension." Pagination is cursor-based.
type ListBookmarksOpts struct {
	Source     string
	FolderPath string
	Limit      int    // 0 → impl default (50)
	Cursor     string // opaque, from a previous result's NextCursor
}

// Chunk is the indexed text segment unit. Each chunk owns one row in the
// chunks table, one in chunks_fts (BM25), and one in chunks_vec (vector ANN).
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
// matches everything. content_type and source map to indexed columns; host
// is matched against the document URL (there is no host column).
type SearchFilters struct {
	ContentType []string // documents.content_type IN (...)
	Host        []string // URL host (http/https) IN (...)
	Source      []string // EXISTS a bookmark with bookmarks.source IN (...)
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

	GetByIDs(ctx context.Context, ids []string) ([]*Chunk, error)
}

// JobQueue is the SQLite-backed work queue.
type JobQueue interface {
	Enqueue(ctx context.Context, j *Job) error
	// ClaimNext atomically marks the next runnable job (status=pending,
	// run_after<=now) as running and returns it. Returns ErrNotFound if
	// nothing is runnable.
	ClaimNext(ctx context.Context, kinds []string) (*Job, error)
	// MarkDone sets status=done. Idempotent on the assumption that only
	// one worker holds the claim.
	MarkDone(ctx context.Context, id string) error
	// MarkFailed bumps attempts, sets status=failed (or pending with a
	// future run_after if retrying), and records the error. The retry
	// policy lives in the queue impl, not in the caller. Returns
	// permanent=true when the job hit the terminal failed state (either
	// retry=false or attempts exhausted) so callers can do kind-specific
	// cleanup, e.g. updating a parent document's state.
	MarkFailed(ctx context.Context, id string, errMsg string, retry bool) (permanent bool, err error)
	GetByID(ctx context.Context, id string) (*Job, error)
}

// ClusterRun is one execution of the clustering job. Clustering fully
// recomputes from the corpus, so each run is a snapshot; the "current"
// interests are the clusters of the latest run with Status == ClusterRunDone.
type ClusterRun struct {
	ID           string
	TenantID     string
	Status       string          // running | done | failed
	Algo         string          // clusterer name, e.g. "knn-graph"
	Params       json.RawMessage // clusterer params; nil means absent
	NumDocuments int             // docs considered (those with vectors)
	NumClusters  int
	NumNoise     int // docs left unclustered
	Error        *string
	StartedAt    time.Time
	FinishedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Cluster is one topic within a run: a labeled, sized group of documents.
type Cluster struct {
	ID        string
	TenantID  string
	RunID     string
	Label     *string // topic name; nil until labeled
	Summary   *string // one-line description; nil if none
	Size      int     // member count (denormalized)
	Cohesion  float64 // mean member cosine to the medoid, 0..1
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ClusterMember links a cluster to one member document.
type ClusterMember struct {
	ClusterID  string
	DocumentID string
	Similarity float64 // cosine to the cluster medoid, 0..1
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

	// FinishRun sets the terminal status (done|failed), the counts, and
	// finished_at. errMsg is set only for failed runs.
	FinishRun(ctx context.Context, runID, status string, numDocuments, numClusters, numNoise int, errMsg *string) error

	// LatestRun returns the most recent run for the tenant matching status
	// (empty status matches any). ErrNotFound if there is none.
	LatestRun(ctx context.Context, tenantID, status string) (*ClusterRun, error)

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
