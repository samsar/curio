package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/urlutil"
)

// BookmarkResponse mirrors the openapi Bookmark schema. tenant_id is
// deliberately omitted (decisions.md: never echoed to clients).
type BookmarkResponse struct {
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

func bookmarkToResponse(b *store.Bookmark, state string) BookmarkResponse {
	return BookmarkResponse{
		ID:            b.ID,
		URL:           b.URL,
		Title:         b.Title,
		SavedAt:       b.SavedAt,
		Source:        b.Source,
		FolderPath:    b.FolderPath,
		Tags:          b.Tags,
		DocumentID:    b.DocumentID,
		DocumentState: state,
		CreatedAt:     b.CreatedAt,
		UpdatedAt:     b.UpdatedAt,
	}
}

// CreateBookmarkRequest is the POST /v1/bookmarks body.
type CreateBookmarkRequest struct {
	URL        string   `json:"url"`
	Title      string   `json:"title,omitempty"`
	FolderPath string   `json:"folder_path,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

// BookmarkCreatedResponse is the 201 body.
type BookmarkCreatedResponse struct {
	Bookmark BookmarkResponse `json:"bookmark"`
	JobID    string           `json:"job_id"`
}

// handleCreateBookmark saves a manual bookmark. The fetch job is created
// only for a URL the corpus didn't have: for a known document job_id is ""
// and document_state is that document's.
func (d Deps) handleCreateBookmark(w http.ResponseWriter, r *http.Request) {
	var req CreateBookmarkRequest
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	normURL, err := urlutil.Normalize(req.URL)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid url", err.Error())
		return
	}

	b := d.bookmarkRow(ImportBookmark{URL: normURL, Title: req.Title, FolderPath: req.FolderPath, Tags: req.Tags},
		store.SourceManual)
	res, err := d.Bookmarks.Ingest(r.Context(), b)
	if err != nil {
		writeError(w, err)
		return
	}
	jobID := ""
	if res.FetchJob != nil {
		jobID = res.FetchJob.ID
	}
	writeJSON(w, http.StatusCreated, BookmarkCreatedResponse{
		Bookmark: bookmarkToResponse(b, string(res.DocumentState)),
		JobID:    jobID,
	})
}

// bookmarkRow maps a requested bookmark, its URL already normalized, onto the
// row Ingest saves. An empty title or folder is stored as NULL, and a zero
// SavedAt means now.
func (d Deps) bookmarkRow(in ImportBookmark, source string) *store.Bookmark {
	savedAt := in.SavedAt
	if savedAt.IsZero() {
		savedAt = time.Now().UTC()
	}
	return &store.Bookmark{
		TenantID:   d.TenantID,
		URL:        in.URL,
		Title:      nonEmpty(in.Title),
		SavedAt:    savedAt,
		Source:     source,
		FolderPath: nonEmpty(in.FolderPath),
		Tags:       in.Tags,
	}
}

// nonEmpty is s as a nullable column value: nil when s is empty.
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// BookmarkListResponse mirrors the openapi BookmarkList schema.
type BookmarkListResponse struct {
	Items      []BookmarkResponse `json:"items"`
	NextCursor *string            `json:"next_cursor,omitempty"`
}

func (d Deps) handleListBookmarks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := store.ListBookmarksOpts{
		Source:     q.Get("source"),
		FolderPath: q.Get("folder"),
		Cursor:     q.Get("cursor"),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	bms, err := d.Bookmarks.List(r.Context(), d.TenantID, opts)
	if err != nil {
		writeError(w, err)
		return
	}

	items := make([]BookmarkResponse, 0, len(bms))
	for _, b := range bms {
		state := ""
		if b.DocumentID != nil {
			if doc, err := d.Documents.GetByID(r.Context(), *b.DocumentID); err == nil {
				state = string(doc.State)
			}
		}
		items = append(items, bookmarkToResponse(b, state))
	}

	resp := BookmarkListResponse{Items: items}
	if len(items) > 0 && opts.Limit > 0 && len(items) == opts.Limit {
		next := items[len(items)-1].ID
		resp.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d Deps) handleGetBookmark(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	b, err := d.Bookmarks.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	state := ""
	if b.DocumentID != nil {
		if doc, err := d.Documents.GetByID(r.Context(), *b.DocumentID); err == nil {
			state = string(doc.State)
		}
	}
	writeJSON(w, http.StatusOK, bookmarkToResponse(b, state))
}

func (d Deps) handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := d.Bookmarks.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
