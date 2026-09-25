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
// predates them, which also means it holds no single-instance lock.
type Health struct {
	Status          string `json:"status"`
	PID             int    `json:"pid,omitempty"`
	Home            string `json:"home,omitempty"`
	Version         string `json:"version"`
	SchemaVersion   int    `json:"schema_version"`
	EmbeddingModel  string `json:"embedding_model"`
	EmbeddingDim    int    `json:"embedding_dim"`
	OllamaReachable bool   `json:"ollama_reachable"`
	OllamaDetail    string `json:"ollama_detail,omitempty"`
}

// healthzTimeout bounds Healthz. A daemon answers well within it whatever
// state Ollama is in, because the handler gives up on its Ollama check after
// 500ms (api.ollamaPingTimeout); only a port held by something that doesn't
// answer (a daemon still migrating, some other server) runs it out.
const healthzTimeout = 2 * time.Second

// Healthz returns the daemon health blob. Every client finds the daemon with
// it, so it gives up after healthzTimeout rather than the client's 30s.
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

type BookmarkListOpts struct {
	Source string
	Folder string
	Limit  int
	Cursor string
}

type BookmarkList struct {
	Items      []Bookmark `json:"items"`
	NextCursor *string    `json:"next_cursor,omitempty"`
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

// SearchHit mirrors api.SearchHitResponse.
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
	LastError    string    `json:"last_error,omitempty"`
	MarkdownPath string    `json:"markdown_path,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DocumentList mirrors api.DocumentListResponse.
type DocumentList struct {
	Items []DocumentListItem `json:"items"`
}

// ListDocumentsOpts filters for ListDocuments.
type ListDocumentsOpts struct {
	State string // pending | fetched | failed | dead
	Limit int
}

func (c *Client) ListDocuments(ctx context.Context, opts ListDocumentsOpts) (*DocumentList, error) {
	q := url.Values{}
	if opts.State != "" {
		q.Set("state", opts.State)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
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

func (c *Client) RefetchAll(ctx context.Context, state string) (*RefetchAllResponse, error) {
	path := "/v1/documents/refetch-all"
	if state != "" {
		path += "?state=" + url.QueryEscape(state)
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

// JobListOpts filters for ListJobs.
type JobListOpts struct {
	Status string
	Kind   string
	Limit  int
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

// JobList mirrors api.JobListResponse.
type JobList struct {
	Items []Job `json:"items"`
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

// SearchResponse mirrors api.SearchResponse. Degraded means semantic search
// was unavailable and Items are keyword-only; Warnings says why.
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

// InterestMember mirrors api.InterestMember. MarkdownPath is the absolute
// on-disk path to the member document's markdown.
type InterestMember struct {
	DocID        string  `json:"doc_id"`
	Title        string  `json:"title,omitempty"`
	URL          string  `json:"url"`
	MarkdownPath string  `json:"markdown_path,omitempty"`
	Similarity   float64 `json:"similarity"`
}

// Interest mirrors api.InterestResponse (a labeled cluster).
type Interest struct {
	ID       string           `json:"id"`
	Label    string           `json:"label,omitempty"`
	Summary  string           `json:"summary,omitempty"`
	Size     int              `json:"size"`
	Cohesion float64          `json:"cohesion"`
	Members  []InterestMember `json:"members,omitempty"`
}

// InterestList mirrors api.InterestListResponse.
type InterestList struct {
	RunID        string     `json:"run_id,omitempty"`
	ComputedAt   *time.Time `json:"computed_at,omitempty"`
	Algo         string     `json:"algo,omitempty"`
	NumDocuments int        `json:"num_documents"`
	NumClusters  int        `json:"num_clusters"`
	NumNoise     int        `json:"num_noise"`
	Items        []Interest `json:"items"`
}

// ListInterestsOpts filters GET /v1/interests.
type ListInterestsOpts struct {
	Limit   int // max interests
	Members int // members to include per interest (0 = server default)
}

func (c *Client) ListInterests(ctx context.Context, opts ListInterestsOpts) (*InterestList, error) {
	q := url.Values{}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Members > 0 {
		q.Set("members", strconv.Itoa(opts.Members))
	}
	path := "/v1/interests"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out InterestList
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetInterest(ctx context.Context, id string, members int) (*Interest, error) {
	path := "/v1/interests/" + id
	if members > 0 {
		path += "?members=" + strconv.Itoa(members)
	}
	var out Interest
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RebuildInterestsResponse is the body of POST /v1/interests/rebuild.
type RebuildInterestsResponse struct {
	JobID string `json:"job_id"`
}

// RebuildInterests triggers a fresh clustering run. Returns the job id to poll.
func (c *Client) RebuildInterests(ctx context.Context) (*RebuildInterestsResponse, error) {
	var out RebuildInterestsResponse
	if err := c.do(ctx, http.MethodPost, "/v1/interests/rebuild", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ErrDaemonUnreachable means no connection to the daemon could be made at
// the client's base URL: nothing is listening there, or the address doesn't
// resolve. The request never reached a daemon, so it is safe to start one
// and send it again, which is what the MCP sidecar does. Any other
// transport failure (a timeout, a connection cut mid-request) is returned
// as it is, wrapping context.DeadlineExceeded or context.Canceled where
// that is the cause.
var ErrDaemonUnreachable = errors.New("daemon unreachable")

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
// with errors.As.
type APIError struct {
	Status  int
	Problem Problem
}

// Error is the problem's detail, or its title when there is none. A server
// error also names its request ID and where to look it up, since the
// cause is in the daemon's log.
func (e *APIError) Error() string {
	msg := cmp.Or(e.Problem.Detail, e.Problem.Title, http.StatusText(e.Status), fmt.Sprintf("HTTP %d", e.Status))
	if e.Status < http.StatusInternalServerError {
		return msg
	}
	if e.Problem.RequestID == "" {
		return msg + " (see `curio daemon logs`)"
	}
	return fmt.Sprintf("%s (request %s; see `curio daemon logs`)", msg, e.Problem.RequestID)
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
// the status text with the body as the detail.
func decodeError(resp *http.Response) *APIError {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")) // "" when unparsable
	var problem Problem
	switch {
	case err != nil:
		problem = Problem{Title: http.StatusText(resp.StatusCode), Status: resp.StatusCode,
			Detail: fmt.Sprintf("reading the error response: %v", err)}
	case mediaType != "application/problem+json" || json.Unmarshal(body, &problem) != nil:
		problem = Problem{Title: http.StatusText(resp.StatusCode), Status: resp.StatusCode,
			Detail: strings.TrimSpace(string(body))}
	}
	problem.RequestID = cmp.Or(problem.RequestID, resp.Header.Get("X-Request-Id"))
	return &APIError{Status: resp.StatusCode, Problem: problem}
}

// drain reads what is left of a response body, up to maxErrorBody, so that
// closing it returns the connection to the pool for the next request.
func drain(body io.Reader) {
	// Draining is only for connection reuse; a failure costs a new dial.
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxErrorBody))
}
