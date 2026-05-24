// Package client is the HTTP client the curio CLI uses to talk to
// curio-daemon. Thin wrapper over net/http; types are duplicated from the
// api package so the CLI doesn't import server-side concerns transitively.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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

// Healthz returns the daemon health blob.
type Health struct {
	Status          string `json:"status"`
	Version         string `json:"version"`
	SchemaVersion   int    `json:"schema_version"`
	EmbeddingModel  string `json:"embedding_model"`
	EmbeddingDim    int    `json:"embedding_dim"`
	OllamaReachable bool   `json:"ollama_reachable"`
	OllamaDetail    string `json:"ollama_detail,omitempty"`
}

func (c *Client) Healthz(ctx context.Context) (*Health, error) {
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
	SavedAt    time.Time `json:"saved_at,omitempty"`
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

// Document mirrors api.DocumentResponse (subset used by CLI).
type Document struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	ContentType string    `json:"content_type"`
	Title       *string   `json:"title,omitempty"`
	Author      *string   `json:"author,omitempty"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
}

// SearchRequest body.
type SearchRequest struct {
	Query string `json:"query"`
	K     int    `json:"k,omitempty"`
}

// SearchHit mirrors api.SearchHitResponse.
type SearchHit struct {
	Document Document     `json:"document"`
	Score    float64      `json:"score"`
	Matches  []ChunkMatch `json:"matches,omitempty"`
}

// ChunkMatch mirrors api.ChunkMatchJSON.
type ChunkMatch struct {
	ChunkID     string   `json:"chunk_id"`
	Text        string   `json:"text"`
	Snippet     string   `json:"snippet,omitempty"`
	BM25Score   *float64 `json:"bm25_score,omitempty"`
	VectorScore *float64 `json:"vector_score,omitempty"`
}

// Document mirrors api.DocumentListItem. (Single-doc get returns more
// fields; the list endpoint only carries the debug-relevant subset plus
// last_error.)
type DocumentListItem struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Title       *string   `json:"title,omitempty"`
	ContentType string    `json:"content_type"`
	State       string    `json:"state"`
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
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

func (c *Client) RefetchDocument(ctx context.Context, docID string) (*RefetchResponse, error) {
	var out RefetchResponse
	if err := c.do(ctx, http.MethodPost, "/v1/documents/"+docID+"/refetch", nil, &out); err != nil {
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

// JobListOpts filters for ListJobs.
type JobListOpts struct {
	Status string
	Kind   string
	Limit  int
}

// Job mirrors api.JobResponse.
type Job struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Attempts  int             `json:"attempts"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	LastError *string         `json:"last_error,omitempty"`
	RunAfter  time.Time       `json:"run_after"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// JobList mirrors api.JobListResponse.
type JobList struct {
	Items []Job `json:"items"`
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

type SearchResponse struct {
	Query      string      `json:"query"`
	TookMS     int64       `json:"took_ms"`
	BM25Hits   int         `json:"bm25_hits"`
	VectorHits int         `json:"vector_hits"`
	Items      []SearchHit `json:"items"`
}

func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	var out SearchResponse
	if err := c.do(ctx, http.MethodPost, "/v1/search", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ErrDaemonUnreachable: the daemon isn't accepting connections at base URL.
// CLI uses this to decide whether to auto-start.
var ErrDaemonUnreachable = errors.New("daemon unreachable")

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		buf = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, buf)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// net.OpError, ECONNREFUSED, etc. are all "daemon down" from the
		// CLI's perspective.
		return fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
