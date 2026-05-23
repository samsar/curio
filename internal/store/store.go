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
	DocStatePending  = "pending"
	DocStateFetched  = "fetched"
	DocStateFailed   = "failed"
	DocStateDead     = "dead"

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

	JobKindFetch     = "fetch"
	JobKindIndex     = "index"
	JobKindImport    = "import"
	JobKindCluster   = "cluster"
	JobKindSummarize = "summarize"

	JobStatusPending = "pending"
	JobStatusRunning = "running"
	JobStatusDone    = "done"
	JobStatusFailed  = "failed"
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
}

// ListBookmarksOpts are filters for BookmarkStore.List. Empty fields mean
// "no filter for that dimension." Pagination is cursor-based.
type ListBookmarksOpts struct {
	Source     string
	FolderPath string
	Limit      int    // 0 → impl default (50)
	Cursor     string // opaque, from a previous result's NextCursor
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
	// policy lives in the queue impl, not in the caller.
	MarkFailed(ctx context.Context, id string, errMsg string, retry bool) error
	GetByID(ctx context.Context, id string) (*Job, error)
}
