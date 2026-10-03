// Package client is the HTTP client the curio CLI uses to talk to
// curio-daemon. Thin wrapper over net/http; types are duplicated from the
// api package so the CLI doesn't import server-side concerns transitively.
package client

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client targets a running curio-daemon.
type Client struct {
	base string
	http *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		base: baseURL,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// Health mirrors api.Health. PID and Home are zero for a daemon that
// predates them, which also means it holds no single-instance lock, and
// Upstreams and GenerationModel are empty for one that predates them.
// EmbeddingDrift is nil unless the daemon reports a drift: the build that
// makes the embeddings changed, and a re-embedded sample showed the
// vectors did, or couldn't be checked.
type Health struct {
	Status          string           `json:"status"`
	PID             int              `json:"pid,omitempty"`
	Home            string           `json:"home,omitempty"`
	Version         string           `json:"version"`
	SchemaVersion   int              `json:"schema_version"`
	EmbeddingModel  string           `json:"embedding_model"`
	EmbeddingDim    int              `json:"embedding_dim"`
	EmbeddingDrift  *EmbeddingDrift  `json:"embedding_drift,omitempty"`
	GenerationModel string           `json:"generation_model,omitempty"`
	OllamaReachable bool             `json:"ollama_reachable"`
	OllamaDetail    string           `json:"ollama_detail,omitempty"`
	Upstreams       []UpstreamHealth `json:"upstreams,omitempty"`
	// YouTubeFetcher is the yt-dlp the daemon routes YouTube videos to;
	// empty when they go to its default fetcher, or it predates saying so.
	YouTubeFetcher string `json:"youtube_fetcher,omitempty"`
	// GitHubToken is whether the daemon's GitHub fetcher sends a token;
	// nil from a daemon that predates saying so.
	GitHubToken *bool `json:"github_token,omitempty"`
}

// EmbeddingDrift mirrors api.EmbeddingDrift: what changed in the build
// that makes the home's embeddings since the library was indexed, the
// evidence it was reported on, and the command that fixes it.
// Verification is nil from a daemon that predates verifying a change.
type EmbeddingDrift struct {
	Changes      []DriftChange      `json:"changes"`
	Verification *DriftVerification `json:"verification,omitempty"`
	Fix          string             `json:"fix"`
	CheckedAt    time.Time          `json:"checked_at"`
}

// DriftVerification mirrors api.DriftVerification: whether a re-embedded
// sample showed the change, and Detail, the daemon's wording of it.
// MinCosine is nil unless Verified.
type DriftVerification struct {
	Verified  bool      `json:"verified"`
	Sampled   int       `json:"sampled"`
	Changed   int       `json:"changed"`
	MinCosine *float64  `json:"min_cosine,omitempty"`
	Detail    string    `json:"detail"`
	SampledAt time.Time `json:"sampled_at"`
}

// DriftChange mirrors api.DriftChange. What is DriftModelDigest or
// DriftOllamaVersion, or one this client doesn't know.
type DriftChange struct {
	What     string `json:"what"`
	Recorded string `json:"recorded"`
	Current  string `json:"current"`
}

// The parts of the embedding build a daemon reports changed.
const (
	DriftModelDigest   = "model_digest"
	DriftOllamaVersion = "ollama_version"
)

// UpstreamHealth mirrors api.UpstreamHealth: how the requests to a service
// fetches depend on have gone. Unset times are zero.
type UpstreamHealth struct {
	Name             string         `json:"name"`
	Enabled          bool           `json:"enabled"`
	State            string         `json:"state"`
	LastSuccessAt    time.Time      `json:"last_success_at,omitzero"`
	LastFailureAt    time.Time      `json:"last_failure_at,omitzero"`
	LastFailureClass string         `json:"last_failure_class,omitempty"`
	WindowSeconds    int            `json:"window_seconds"`
	Recent           map[string]int `json:"recent,omitempty"`
	CooldownUntil    time.Time      `json:"cooldown_until,omitzero"`
	// SitePauses are the sites the upstream holds back for now, each until
	// a time; empty when none is.
	SitePauses []SitePause `json:"site_pauses,omitempty"`
}

// SitePause mirrors api.SitePause: an upstream's block of one site's reads.
type SitePause struct {
	Site  string    `json:"site"`
	Until time.Time `json:"until"`
}

// Upstream states a daemon reports, mirroring the fetcher's. A daemon may
// report one this client doesn't know.
const (
	UpstreamDisabled = "disabled"
	UpstreamIdle     = "idle"
	UpstreamOK       = "ok"
	UpstreamDegraded = "degraded"
	UpstreamPaused   = "paused"
	UpstreamFailing  = "failing"
)

// Call classes a daemon counts an upstream's requests by, mirroring the
// fetcher's: three healthy answers, then the failures.
const (
	CallOK          = "ok"
	CallJudged      = "judged"
	CallRefused     = "refused"
	CallChallenged  = "challenged"
	CallForbidden   = "forbidden"
	CallRateLimited = "rate_limited"
	CallAuth        = "auth"
	CallServerError = "server_error"
	CallNetwork     = "network"
)

// healthzTimeout bounds Healthz. A ready daemon answers well within it
// whatever state Ollama is in, because the handler gives up on its Ollama
// check after 500ms (api.ollamaPingTimeout), and a starting daemon answers
// at once. Only a port held by something that doesn't answer (a wedged
// process, some other server) runs it out.
const healthzTimeout = 2 * time.Second

// Healthz returns the health of a daemon that serves the full API. A daemon
// that is still starting answers with an *APIError matching ErrStarting,
// whose Startup says which daemon it is and how far along. Every client
// finds the daemon with it, so it gives up after healthzTimeout rather than
// the client's 30s.
func (c *Client) Healthz(ctx context.Context) (*Health, error) {
	ctx, cancel := context.WithTimeout(ctx, healthzTimeout)
	defer cancel()
	var h Health
	if err := c.do(ctx, http.MethodGet, "/v1/healthz", nil, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// Stats mirrors api.Stats.
type Stats struct {
	Version          string         `json:"version"`
	BookmarksTotal   int            `json:"bookmarks_total"`
	DocumentsTotal   int            `json:"documents_total"`
	DocumentsByState map[string]int `json:"documents_by_state,omitempty"`
	JobsByStatus     map[string]int `json:"jobs_by_status,omitempty"`
}

// Metrics mirrors api.MetricsResponse.
type Metrics struct {
	WindowSeconds int           `json:"window_seconds"`
	ByKind        []KindMetrics `json:"by_kind"`
}

type KindMetrics struct {
	Kind                 string  `json:"kind"`
	Count                int     `json:"count"`
	Failed               int     `json:"failed"`
	MeanMS               float64 `json:"mean_ms"`
	P50MS                float64 `json:"p50_ms"`
	P95MS                float64 `json:"p95_ms"`
	P99MS                float64 `json:"p99_ms"`
	Running              int     `json:"running"`
	OldestRunningSeconds int     `json:"oldest_running_seconds"`
}

func (c *Client) Metrics(ctx context.Context, windowSeconds int) (*Metrics, error) {
	path := "/v1/metrics"
	if windowSeconds > 0 {
		path += "?window=" + strconv.Itoa(windowSeconds)
	}
	var m Metrics
	if err := c.do(ctx, http.MethodGet, path, nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Client) Stats(ctx context.Context) (*Stats, error) {
	var s Stats
	if err := c.do(ctx, http.MethodGet, "/v1/stats", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Bookmark mirrors api.BookmarkResponse.
type Bookmark struct {
	ID            string    `json:"id"`
	URL           string    `json:"url"`
	Title         *string   `json:"title,omitempty"`
	SavedAt       time.Time `json:"saved_at"`
	Source        string    `json:"source"`
	FolderPath    *string   `json:"folder_path,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	DocumentID    *string   `json:"document_id,omitempty"`
	DocumentState string    `json:"document_state,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// BookmarkCreated mirrors api.BookmarkCreatedResponse.
type BookmarkCreated struct {
	Bookmark Bookmark `json:"bookmark"`
	JobID    string   `json:"job_id"`
}

// CreateBookmarkRequest is the POST body.
type CreateBookmarkRequest struct {
	URL        string   `json:"url"`
	Title      string   `json:"title,omitempty"`
	FolderPath string   `json:"folder_path,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

func (c *Client) CreateBookmark(ctx context.Context, req CreateBookmarkRequest) (*BookmarkCreated, error) {
	var out BookmarkCreated
	if err := c.do(ctx, http.MethodPost, "/v1/bookmarks", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BookmarkListOpts filters and pages ListBookmarks.
type BookmarkListOpts struct {
	Source string
	Folder string
	Limit  int
	Cursor string // a previous page's NextCursor
}

// BookmarkList mirrors api.BookmarkListResponse. NextCursor is empty on the
// last page.
type BookmarkList struct {
	Items      []Bookmark `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

func (c *Client) ListBookmarks(ctx context.Context, opts BookmarkListOpts) (*BookmarkList, error) {
	q := url.Values{}
	if opts.Source != "" {
		q.Set("source", opts.Source)
	}
	if opts.Folder != "" {
		q.Set("folder", opts.Folder)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	path := "/v1/bookmarks"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var out BookmarkList
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ImportBookmark mirrors api.ImportBookmark.
type ImportBookmark struct {
	URL        string    `json:"url"`
	Title      string    `json:"title,omitempty"`
	FolderPath string    `json:"folder_path,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	SavedAt    time.Time `json:"saved_at,omitzero"` // zero when the source has no date; the daemon uses now
}

// ImportRequest mirrors api.ImportRequest.
type ImportRequest struct {
	Source    string           `json:"source"`
	Bookmarks []ImportBookmark `json:"bookmarks"`
	// DryRun counts what the import would do and writes nothing. A daemon
	// that predates it refuses the field (400, unknown field).
	DryRun bool `json:"dry_run,omitempty"`
}

// ImportResponse mirrors api.ImportResponse.
type ImportResponse struct {
	Source       string         `json:"source"`
	Total        int            `json:"total"`
	Created      int            `json:"created"`
	Skipped      int            `json:"skipped"`
	Filtered     int            `json:"filtered"`
	JobsEnqueued int            `json:"jobs_enqueued"`
	FilteredBy   map[string]int `json:"filtered_by,omitempty"`
	Errors       []string       `json:"errors,omitempty"`
	DryRun       bool           `json:"dry_run,omitempty"`
	// NewURLs are, for a dry run, the URLs the import would fetch.
	NewURLs []string `json:"new_urls,omitempty"`
}

func (c *Client) ImportBookmarks(ctx context.Context, req ImportRequest) (*ImportResponse, error) {
	var out ImportResponse
	if err := c.do(ctx, http.MethodPost, "/v1/bookmarks/import", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Document mirrors api.DocumentResponse.
type Document struct {
	ID                string      `json:"id"`
	URL               string      `json:"url"`
	URLCanonical      *string     `json:"url_canonical,omitempty"`
	ContentType       string      `json:"content_type"`
	Title             *string     `json:"title,omitempty"`
	Author            *string     `json:"author,omitempty"`
	PublishedAt       *time.Time  `json:"published_at,omitempty"`
	Language          *string     `json:"language,omitempty"`
	State             string      `json:"state"`
	FailureCause      string      `json:"failure_cause,omitempty"` // why a failed or dead document failed
	CurrentExtraction *Extraction `json:"current_extraction,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

// Extraction mirrors api.ExtractionResponse. MarkdownPath is the absolute
// path of the extracted markdown on the daemon's machine.
type Extraction struct {
	ID             string         `json:"id"`
	FetchedAt      time.Time      `json:"fetched_at"`
	Fetcher        string         `json:"fetcher"`
	Status         string         `json:"status"`
	MarkdownPath   string         `json:"markdown_path,omitempty"`
	ErrorMessage   *string        `json:"error_message,omitempty"`
	ExtractionMeta map[string]any `json:"extraction_meta,omitempty"`
}

func (c *Client) GetDocument(ctx context.Context, id string) (*Document, error) {
	var out Document
	if err := c.do(ctx, http.MethodGet, "/v1/documents/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LookupDocument returns the document for rawURL, which the daemon
// normalizes the way ingest stored it. A URL with no document is an
// *APIError with Status 404 (IsNotFound).
func (c *Client) LookupDocument(ctx context.Context, rawURL string) (*Document, error) {
	var out Document
	path := "/v1/documents/lookup?" + url.Values{"url": {rawURL}}.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDocumentContent returns the extracted markdown of a document. A
// document with no content yet is an *APIError with Status 404.
func (c *Client) GetDocumentContent(ctx context.Context, id string) (string, error) {
	path := "/v1/documents/" + id + "/content"
	resp, err := c.send(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("GET %s: read content: %w", path, err)
	}
	return string(body), nil
}

// SearchRequest body. K 0 is omitted, so the daemon's search.default_k applies.
type SearchRequest struct {
	Query   string         `json:"query"`
	K       int            `json:"k,omitempty"`
	Filters *SearchFilters `json:"filters,omitempty"`
}

// SearchFilters scopes a search. Nil/omitted dimensions are not filtered.
type SearchFilters struct {
	ContentType []string `json:"content_type,omitempty"`
	Host        []string `json:"host,omitempty"`
	Source      []string `json:"source,omitempty"`
}

// SearchHit is the part of api.SearchHitResponse the CLI reads: it leaves
// out bookmark_title, which only the dashboard shows.
type SearchHit struct {
	Document     Document     `json:"document"`
	Score        float64      `json:"score"`
	MarkdownPath string       `json:"markdown_path,omitempty"`
	Matches      []ChunkMatch `json:"matches,omitempty"`
}

// ChunkMatch mirrors api.ChunkMatchJSON.
type ChunkMatch struct {
	ChunkID     string   `json:"chunk_id"`
	Text        string   `json:"text"`
	Snippet     string   `json:"snippet,omitempty"`
	BM25Score   *float64 `json:"bm25_score,omitempty"`
	VectorScore *float64 `json:"vector_score,omitempty"`
}

// DocumentListItem mirrors api.DocumentListItem: the list endpoint's
// debug-oriented subset of a document, plus its last error and absolute
// markdown path.
type DocumentListItem struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	Title        *string   `json:"title,omitempty"`
	ContentType  string    `json:"content_type"`
	State        string    `json:"state"`
	FailureCause string    `json:"failure_cause,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	MarkdownPath string    `json:"markdown_path,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DocumentList mirrors api.DocumentListResponse. NextCursor is empty on the
// last page.
type DocumentList struct {
	Items      []DocumentListItem `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

// ListDocumentsOpts filters and pages ListDocuments.
type ListDocumentsOpts struct {
	State  string // pending | fetched | failed | dead
	Limit  int
	Cursor string // a previous page's NextCursor
}

func (c *Client) ListDocuments(ctx context.Context, opts ListDocumentsOpts) (*DocumentList, error) {
	q := url.Values{}
	if opts.State != "" {
		q.Set("state", opts.State)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	path := "/v1/documents"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out DocumentList
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RefetchResponse is the body of a successful refetch.
type RefetchResponse struct {
	JobID string `json:"job_id"`
}

// RefetchDocument enqueues a refetch. force overrides the daemon's refusal
// to refetch documents in state=dead (confirmed dead links).
func (c *Client) RefetchDocument(ctx context.Context, docID string, force bool) (*RefetchResponse, error) {
	path := "/v1/documents/" + docID + "/refetch"
	if force {
		path += "?force=1"
	}
	var out RefetchResponse
	if err := c.do(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RefetchAllResponse is the body of a bulk refetch.
type RefetchAllResponse struct {
	JobsEnqueued int `json:"jobs_enqueued"`
}

// RefetchAllOpts narrows RefetchAll. Empty fields leave the daemon's
// default: every document but the dead ones, whatever their cause.
type RefetchAllOpts struct {
	State string // pending | fetched | failed | dead
	Cause string // a failure cause, such as anti_bot; dead_link needs State dead
}

func (c *Client) RefetchAll(ctx context.Context, opts RefetchAllOpts) (*RefetchAllResponse, error) {
	q := url.Values{}
	if opts.State != "" {
		q.Set("state", opts.State)
	}
	if opts.Cause != "" {
		q.Set("cause", opts.Cause)
	}
	path := "/v1/documents/refetch-all"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out RefetchAllResponse
	if err := c.do(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReindexResponse is the body of a single-document reindex.
type ReindexResponse struct {
	JobID string `json:"job_id"`
}

func (c *Client) ReindexDocument(ctx context.Context, docID string) (*ReindexResponse, error) {
	var out ReindexResponse
	if err := c.do(ctx, http.MethodPost, "/v1/documents/"+docID+"/reindex", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReindexAllResponse is the body of a bulk reindex.
type ReindexAllResponse struct {
	JobsEnqueued int `json:"jobs_enqueued"`
}

func (c *Client) ReindexAll(ctx context.Context, state string) (*ReindexAllResponse, error) {
	path := "/v1/documents/reindex-all"
	if state != "" {
		path += "?state=" + url.QueryEscape(state)
	}
	var out ReindexAllResponse
	if err := c.do(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// JobListOpts filters and pages ListJobs.
type JobListOpts struct {
	Status string
	Kind   string
	Limit  int
	Cursor string // a previous page's NextCursor
}

// Job mirrors api.JobResponse.
type Job struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	Attempts     int             `json:"attempts"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	LastError    *string         `json:"last_error,omitempty"`
	RunAfter     time.Time       `json:"run_after"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	DocURL       string          `json:"doc_url,omitempty"`
	DocTitle     string          `json:"doc_title,omitempty"`
	MarkdownPath string          `json:"markdown_path,omitempty"`
}

// JobList mirrors api.JobListResponse. NextCursor is empty on the last
// page.
type JobList struct {
	Items      []Job  `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// GetJob returns one job, as ListJobs shows it: poll it with the job_id a
// refetch, reindex or interests rebuild returned.
func (c *Client) GetJob(ctx context.Context, id string) (*Job, error) {
	var out Job
	if err := c.do(ctx, http.MethodGet, "/v1/jobs/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteJobsResponse mirrors api.DeleteJobsResponse.
type DeleteJobsResponse struct {
	Deleted int64  `json:"deleted"`
	Mode    string `json:"mode"`
}

// DeleteJobsByStatus removes jobs in a finished status (done or failed).
// The server rejects any other status — there's no "delete all" path on
// purpose, and live work can't be deleted.
func (c *Client) DeleteJobsByStatus(ctx context.Context, status string) (*DeleteJobsResponse, error) {
	var out DeleteJobsResponse
	path := "/v1/jobs?status=" + url.QueryEscape(status)
	if err := c.do(ctx, http.MethodDelete, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PruneJobsOlderThan removes finished jobs whose updated_at is older than
// the given duration string. Accepts standard Go duration syntax plus "Nd"
// (days), e.g. "30d", "24h", "2h30m".
func (c *Client) PruneJobsOlderThan(ctx context.Context, duration string) (*DeleteJobsResponse, error) {
	var out DeleteJobsResponse
	path := "/v1/jobs?older_than=" + url.QueryEscape(duration)
	if err := c.do(ctx, http.MethodDelete, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListJobs(ctx context.Context, opts JobListOpts) (*JobList, error) {
	q := url.Values{}
	if opts.Status != "" {
		q.Set("status", opts.Status)
	}
	if opts.Kind != "" {
		q.Set("kind", opts.Kind)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	path := "/v1/jobs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out JobList
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SearchResponse is the part of api.SearchResponse the CLI and curio-mcp
// read: it leaves out total and capped, which only the dashboard's pages
// use. Degraded means semantic search was unavailable and Items are
// keyword-only; Warnings says why.
type SearchResponse struct {
	Query      string      `json:"query"`
	TookMS     int64       `json:"took_ms"`
	BM25Hits   int         `json:"bm25_hits"`
	VectorHits int         `json:"vector_hits"`
	Degraded   bool        `json:"degraded,omitempty"`
	Warnings   []string    `json:"warnings,omitempty"`
	Items      []SearchHit `json:"items"`
}

func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	var out SearchResponse
	if err := c.do(ctx, http.MethodPost, "/v1/search", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RelatedResponse mirrors api.RelatedResponse.
type RelatedResponse struct {
	DocID  string      `json:"doc_id"`
	TookMS int64       `json:"took_ms"`
	Items  []SearchHit `json:"items"`
}

// RelatedDocuments returns documents similar to docID, ranked by embedding
// similarity over the document's indexed content. Empty items when the
// document has no indexed chunks yet.
func (c *Client) RelatedDocuments(ctx context.Context, docID string, k int) (*RelatedResponse, error) {
	path := "/v1/documents/" + docID + "/related"
	if k > 0 {
		path += "?k=" + strconv.Itoa(k)
	}
	var out RelatedResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InterestMember mirrors api.InterestMember: a member, a loose fit, or a
// document placed into the interest since the rebuild (Fit says which).
// MarkdownPath is the absolute on-disk path to its markdown.
type InterestMember struct {
	DocID         string  `json:"doc_id"`
	Title         string  `json:"title,omitempty"`
	BookmarkTitle string  `json:"bookmark_title,omitempty"`
	URL           string  `json:"url"`
	State         string  `json:"state"`
	MarkdownPath  string  `json:"markdown_path,omitempty"`
	Similarity    float64 `json:"similarity"`
	Fit           string  `json:"fit"`
}

// InterestRef mirrors api.InterestRef.
type InterestRef struct {
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	Retired bool   `json:"retired"`
}

// InterestEvent mirrors api.InterestEvent: kept, moved, split, merged,
// dissolved or new.
type InterestEvent struct {
	Event  string       `json:"event"`
	Level  string       `json:"level"`
	From   *InterestRef `json:"from,omitempty"`
	To     *InterestRef `json:"to,omitempty"`
	Area   *InterestRef `json:"area,omitempty"`
	Shared int          `json:"shared,omitempty"`
}

// Interest levels.
const (
	LevelArea     = "area"
	LevelInterest = "interest"
)

// Interest mirrors api.InterestResponse: an area, with its interests, or
// an interest, with its members.
type Interest struct {
	ID          string           `json:"id"`
	RunID       string           `json:"run_id"`
	Level       string           `json:"level"`
	ParentID    string           `json:"parent_id,omitempty"`
	ParentLabel string           `json:"parent_label,omitempty"`
	Label       string           `json:"label,omitempty"`
	Summary     string           `json:"summary,omitempty"`
	Size        int              `json:"size"`
	Loose       int              `json:"loose"`
	New         int              `json:"new"`
	Cohesion    float64          `json:"cohesion"`
	NumChildren int              `json:"num_children,omitempty"`
	Children    []Interest       `json:"children,omitempty"`
	Members     []InterestMember `json:"members,omitempty"`
	NewMembers  []InterestMember `json:"new_members,omitempty"`
	Events      []InterestEvent  `json:"events,omitempty"`
}

// InterestRebuild mirrors api.InterestRebuild: what the latest rebuild
// was and did.
type InterestRebuild struct {
	Trigger          string `json:"trigger"`
	Kind             string `json:"kind"`
	SplitCheck       bool   `json:"split_check"`
	ChangedDocuments int    `json:"changed_documents"`
	Kept             int    `json:"kept"`
	Created          int    `json:"created"`
	Split            int    `json:"split"`
	Merged           int    `json:"merged"`
	Moved            int    `json:"moved"`
	Dissolved        int    `json:"dissolved"`
}

// The states of the next rebuild (InterestsState.State). A state this
// client doesn't know reads as StateCurrent.
const (
	StateOff        = "off"
	StateRebuilding = "rebuilding"
	StateQueued     = "queued"
	StateFailing    = "failing"
	StateCurrent    = "current"
	StateNone       = "none"
)

// InterestsState mirrors api.InterestsState.
type InterestsState struct {
	State     string `json:"state"`
	LastError string `json:"last_error,omitempty"`
}

// InterestList mirrors api.InterestListResponse.
type InterestList struct {
	RunID        string           `json:"run_id,omitempty"`
	ComputedAt   *time.Time       `json:"computed_at,omitempty"`
	Algo         string           `json:"algo,omitempty"`
	Shape        string           `json:"shape,omitempty"`
	NumDocuments int              `json:"num_documents"`
	NumAreas     int              `json:"num_areas"`
	NumInterests int              `json:"num_interests"`
	NumLoose     int              `json:"num_loose"`
	NumUnsorted  int              `json:"num_unsorted"`
	NumNew       int              `json:"num_new"`
	Total        int              `json:"total"`
	Rebuild      *InterestRebuild `json:"rebuild,omitempty"`
	Next         InterestsState   `json:"next"`
	Items        []Interest       `json:"items"`
}

// ListInterestsOpts filters GET /v1/interests. A zero size is the
// daemon's default.
type ListInterestsOpts struct {
	Limit    int    // top-level groups
	Offset   int    // groups to skip
	Children int    // interests per area
	Members  int    // members per interest
	Level    string // LevelInterest lists every interest; "" the top-level groups
}

func (c *Client) ListInterests(ctx context.Context, opts ListInterestsOpts) (*InterestList, error) {
	q := url.Values{}
	setPositive(q, "limit", opts.Limit)
	setPositive(q, "offset", opts.Offset)
	setPositive(q, "children", opts.Children)
	setPositive(q, "members", opts.Members)
	if opts.Level != "" {
		q.Set("level", opts.Level)
	}
	var out InterestList
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/interests", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetInterestOpts page GET /v1/interests/{id}. A zero Members is the
// daemon's default.
type GetInterestOpts struct {
	Members int // an interest's members and loose fits; each of an area's interests' members
	Offset  int // an interest's members and loose fits to skip
}

// GetInterest returns an area or an interest of the latest rebuild. A
// retired one is an *APIError whose Retired says what became of it.
func (c *Client) GetInterest(ctx context.Context, id string, opts GetInterestOpts) (*Interest, error) {
	q := url.Values{}
	setPositive(q, "members", opts.Members)
	setPositive(q, "offset", opts.Offset)
	var out Interest
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/interests/"+url.PathEscape(id), q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UnsortedMember mirrors api.UnsortedMember.
type UnsortedMember struct {
	DocID         string  `json:"doc_id"`
	Title         string  `json:"title,omitempty"`
	BookmarkTitle string  `json:"bookmark_title,omitempty"`
	URL           string  `json:"url"`
	State         string  `json:"state"`
	MarkdownPath  string  `json:"markdown_path,omitempty"`
	Similarity    float64 `json:"similarity"`
	NearestID     string  `json:"nearest_id,omitempty"`
	NearestLabel  string  `json:"nearest_label,omitempty"`
}

// UnsortedPage mirrors api.UnsortedPage.
type UnsortedPage struct {
	RunID  string           `json:"run_id,omitempty"`
	Total  int              `json:"total"`
	NumNew int              `json:"num_new"`
	Items  []UnsortedMember `json:"items"`
	New    []UnsortedMember `json:"new"`
}

// UnsortedOpts page GET /v1/interests/unsorted.
type UnsortedOpts struct {
	Limit  int
	Offset int
}

// UnsortedInterests returns a page of the documents in no interest,
// nearest first.
func (c *Client) UnsortedInterests(ctx context.Context, opts UnsortedOpts) (*UnsortedPage, error) {
	q := url.Values{}
	setPositive(q, "limit", opts.Limit)
	setPositive(q, "offset", opts.Offset)
	var out UnsortedPage
	if err := c.do(ctx, http.MethodGet, withQuery("/v1/interests/unsorted", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InterestChanges mirrors api.InterestChanges.
type InterestChanges struct {
	RunID      string           `json:"run_id,omitempty"`
	ComputedAt *time.Time       `json:"computed_at,omitempty"`
	Rebuild    *InterestRebuild `json:"rebuild,omitempty"`
	Events     []InterestEvent  `json:"events"`
}

// InterestChanges returns what the latest rebuild did.
func (c *Client) InterestChanges(ctx context.Context) (*InterestChanges, error) {
	var out InterestChanges
	if err := c.do(ctx, http.MethodGet, "/v1/interests/changes", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RebuildInterestsResponse is the body of POST /v1/interests/rebuild.
type RebuildInterestsResponse struct {
	JobID string `json:"job_id"`
}

// RebuildInterests queues a rebuild of the interests, or finds the one
// already queued, and returns its job ID to poll.
func (c *Client) RebuildInterests(ctx context.Context) (*RebuildInterestsResponse, error) {
	var out RebuildInterestsResponse
	if err := c.do(ctx, http.MethodPost, "/v1/interests/rebuild", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RetiredInterest mirrors api.RetiredInterest's members: what became of a
// retired area or interest.
type RetiredInterest struct {
	ID         string              `json:"id"`
	Level      string              `json:"level"`
	Label      string              `json:"label,omitempty"`
	RetiredAt  time.Time           `json:"retired_at"`
	RunID      string              `json:"run_id"`
	Successors []InterestSuccessor `json:"successors"`
}

// InterestSuccessor mirrors api.InterestSuccessor.
type InterestSuccessor struct {
	ID      string `json:"id"`
	Level   string `json:"level"`
	Label   string `json:"label,omitempty"`
	Event   string `json:"event"`
	Shared  int    `json:"shared"`
	Retired bool   `json:"retired"`
}

// RetiredOf returns what became of a retired interest, if err is the
// daemon's answer for one, and nil otherwise.
func RetiredOf(err error) *RetiredInterest {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retired
	}
	return nil
}

// setPositive sets name to n in q when n is positive.
func setPositive(q url.Values, name string, n int) {
	if n > 0 {
		q.Set(name, strconv.Itoa(n))
	}
}

// withQuery is path with q, when q has any value.
func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// Queue mirrors api.QueueResponse: the queue gate's settings, whether the
// workers may start jobs now, and each pool's limit and load.
type Queue struct {
	Paused    bool   `json:"paused"`
	Throttle  string `json:"throttle"`
	Schedule  string `json:"schedule,omitempty"` // HH:MM-HH:MM; empty when there is none
	KeepAwake bool   `json:"keep_awake"`
	// KeepAwakeActive: the daemon holds the Mac awake now.
	KeepAwakeActive bool `json:"keep_awake_active"`
	// PowerSource is PowerAC, PowerBattery or PowerUnknown, while
	// KeepAwake is on; empty otherwise.
	PowerSource string      `json:"power_source,omitempty"`
	State       string      `json:"state"`
	Reason      string      `json:"reason,omitempty"`  // why it is closed
	OpensAt     time.Time   `json:"opens_at,omitzero"` // set while closed outside the schedule
	Kinds       []QueueKind `json:"kinds"`
}

// QueueKind mirrors api.QueueKindResponse. DueLater is how many of
// Pending can't run yet, and NextDue when the first of them can; a daemon
// that doesn't report them leaves both zero.
type QueueKind struct {
	Kind     string    `json:"kind"`
	Limit    int       `json:"limit"`
	Running  int       `json:"running"`
	Pending  int       `json:"pending"`
	DueLater int       `json:"due_later"`
	NextDue  time.Time `json:"next_due,omitzero"`
}

// Queue states and reasons, throttles, the schedule that clears the
// schedule, and power sources, mirroring the API's. A daemon may report a
// state or reason this client doesn't know.
const (
	QueueOpen             = "open"
	QueueClosed           = "closed"
	ReasonPaused          = "paused"
	ReasonOutsideSchedule = "outside_schedule"
	ThrottleNormal        = "normal"
	ThrottleGentle        = "gentle"
	ScheduleOff           = "off"
	PowerAC               = "ac"
	PowerBattery          = "battery"
	PowerUnknown          = "unknown"
)

// QueueUpdate is the body of PUT /v1/queue: only the fields set are sent,
// and only those change.
type QueueUpdate struct {
	Paused    *bool  `json:"paused,omitempty"`
	Throttle  string `json:"throttle,omitempty"`
	Schedule  string `json:"schedule,omitempty"` // HH:MM-HH:MM, or ScheduleOff
	KeepAwake *bool  `json:"keep_awake,omitempty"`
}

// Queue reads the queue's state.
func (c *Client) Queue(ctx context.Context) (*Queue, error) {
	var q Queue
	if err := c.do(ctx, http.MethodGet, "/v1/queue", nil, &q); err != nil {
		return nil, err
	}
	return &q, nil
}

// UpdateQueue changes the queue settings u sets and returns the queue
// afterwards.
func (c *Client) UpdateQueue(ctx context.Context, u QueueUpdate) (*Queue, error) {
	var q Queue
	if err := c.do(ctx, http.MethodPut, "/v1/queue", u, &q); err != nil {
		return nil, err
	}
	return &q, nil
}

// ErrDaemonUnreachable means no connection to the daemon could be made at
// the client's base URL: nothing is listening there, or the address doesn't
// resolve. The request never reached a daemon, so it is safe to start one
// and send it again, which is what the MCP sidecar does. Any other
// transport failure (a timeout, a connection cut mid-request) is returned
// as it is, wrapping context.DeadlineExceeded or context.Canceled where
// that is the cause.
var ErrDaemonUnreachable = errors.New("daemon unreachable")

// ErrStarting matches the answer of a daemon that is up but still starting
// (migrating its database, say): 503 with the starting problem type. It
// ran nothing, so the request can be sent again once the daemon is ready.
var ErrStarting = errors.New("daemon starting")

// startingProblemType mirrors api.StartingProblemType.
const startingProblemType = "urn:curio:problem:daemon-starting"

// retiredProblemType mirrors api.InterestRetiredProblemType.
const retiredProblemType = "urn:curio:problem:interest-retired"

// Phases a starting daemon reports, mirroring api's. A daemon may report
// one this client doesn't know; it is still starting.
const (
	PhaseInitializing = "initializing"
	PhaseMigrating    = "migrating"
)

// Startup mirrors the members api.Starting adds to the starting problem on
// /v1/healthz: which daemon answered, and how far along it is.
type Startup struct {
	PID        int                `json:"pid"`
	Home       string             `json:"home"`
	Version    string             `json:"version"`
	Phase      string             `json:"phase"`
	Migrations *MigrationProgress `json:"migrations,omitempty"` // present while migrating
}

// MigrationProgress mirrors api.MigrationProgress.
type MigrationProgress struct {
	Applied int `json:"applied"`
	Total   int `json:"total"`
}

// Progress says what the daemon is doing, e.g. "migrating the database, 2
// of 6 migrations applied" or "initializing".
func (s Startup) Progress() string {
	if s.Phase == PhaseMigrating && s.Migrations != nil {
		return fmt.Sprintf("migrating the database, %d of %d migrations applied",
			s.Migrations.Applied, s.Migrations.Total)
	}
	return s.Phase
}

// StartupOf returns what a starting daemon's healthz answer reported, if
// err is one, and nil otherwise.
func StartupOf(err error) *Startup {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Startup
	}
	return nil
}

// Problem is the RFC 7807 problem body the daemon answers errors with.
type Problem struct {
	Type      string `json:"type,omitempty"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// APIError is a non-2xx answer from the daemon. Callers branch on Status
// with errors.As, and on a starting daemon with errors.Is(err, ErrStarting).
type APIError struct {
	Status  int
	Problem Problem
	// Startup is what a starting daemon's healthz answer reported; nil for
	// any other answer.
	Startup *Startup
	// Retired is what became of a retired interest, for the 410 the daemon
	// answers it with; nil for any other answer.
	Retired *RetiredInterest
}

// Error is the problem's detail, or its title when there is none. A server
// error also names its request ID and where to look it up, since the
// cause is in the daemon's log; a starting daemon's answer isn't one.
func (e *APIError) Error() string {
	msg := cmp.Or(e.Problem.Detail, e.Problem.Title, http.StatusText(e.Status), fmt.Sprintf("HTTP %d", e.Status))
	if e.Status < http.StatusInternalServerError || e.starting() {
		return msg
	}
	if e.Problem.RequestID == "" {
		return msg + " (see `curio daemon logs`)"
	}
	return fmt.Sprintf("%s (request %s; see `curio daemon logs`)", msg, e.Problem.RequestID)
}

// Is reports whether e is a starting daemon's answer, for ErrStarting.
func (e *APIError) Is(target error) bool {
	return target == ErrStarting && e.starting()
}

func (e *APIError) starting() bool {
	return e.Status == http.StatusServiceUnavailable && e.Problem.Type == startingProblemType
}

// IsNotFound reports whether err is the daemon answering 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// maxErrorBody bounds how much of an error response is read. A problem is
// a few hundred bytes; anything past this is not one.
const maxErrorBody = 64 << 10

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	defer drain(resp.Body) // before the Close, so the connection can be reused
	if out == nil {
		return nil
	}
	// Decoding ignores fields this client doesn't know, so a newer daemon's
	// additions don't break it.
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// send makes a request and returns a 2xx response, whose body the caller
// closes. A non-2xx answer is an *APIError and a failure to connect wraps
// ErrDaemonUnreachable.
func (c *Client) send(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%s %s: encode request: %w", method, path, err)
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, buf)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// The *url.Error already names the method and URL.
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" && ctx.Err() == nil {
			return nil, fmt.Errorf("%w: %w", ErrDaemonUnreachable, err)
		}
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		defer drain(resp.Body)
		return nil, decodeError(resp)
	}
	return resp, nil
}

// decodeError reads an error response into an *APIError: the problem the
// daemon sent, or, for a body that isn't one (a proxy, an older daemon),
// the status text with the body as the detail. A starting daemon's healthz
// answer also fills in Startup.
func decodeError(resp *http.Response) *APIError {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")) // "" when unparsable
	var p Problem
	switch {
	case err != nil:
		p = Problem{Title: http.StatusText(resp.StatusCode), Status: resp.StatusCode,
			Detail: fmt.Sprintf("reading the error response: %v", err)}
	case mediaType != "application/problem+json" || json.Unmarshal(body, &p) != nil:
		p = Problem{Title: http.StatusText(resp.StatusCode), Status: resp.StatusCode,
			Detail: strings.TrimSpace(string(body))}
	}
	apiErr := &APIError{Status: resp.StatusCode, Problem: p}
	apiErr.Problem.RequestID = cmp.Or(apiErr.Problem.RequestID, resp.Header.Get("X-Request-Id"))
	switch {
	case apiErr.starting():
		apiErr.Startup = decodeStartup(body)
	case apiErr.Status == http.StatusGone && p.Type == retiredProblemType:
		apiErr.Retired = decodeRetired(body)
	}
	return apiErr
}

// decodeRetired reads the members a retired interest's problem adds, or
// returns nil for one without them.
func decodeRetired(body []byte) *RetiredInterest {
	var r RetiredInterest
	if json.Unmarshal(body, &r) != nil || r.ID == "" {
		return nil
	}
	return &r
}

// decodeStartup reads the members a starting daemon's healthz answer adds
// to its problem, or returns nil for a starting problem without them: only
// healthz names the daemon, and other routes send the problem alone. Other
// problems aren't read for them, so members of the same names in a foreign
// problem can't spoil its decoding.
func decodeStartup(body []byte) *Startup {
	var s Startup
	if json.Unmarshal(body, &s) != nil || s.PID == 0 || s.Home == "" {
		return nil
	}
	return &s
}

// drain reads what is left of a response body, up to maxErrorBody, so that
// closing it returns the connection to the pool for the next request.
func drain(body io.Reader) {
	// Draining is only for connection reuse; a failure costs a new dial.
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxErrorBody))
}
