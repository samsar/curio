package api

import (
	"encoding/json"
	"errors"
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

func (d Deps) handleCreateBookmark(w http.ResponseWriter, r *http.Request) {
	var req CreateBookmarkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	ctx := r.Context()

	normURL, err := urlutil.Normalize(req.URL)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid url", err.Error())
		return
	}

	// Find or create the underlying document.
	doc, err := d.Documents.GetByURL(ctx, d.TenantID, normURL)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, err)
		return
	}
	if doc == nil {
		doc = &store.Document{
			TenantID:    d.TenantID,
			URL:         normURL,
			ContentType: store.ContentTypeUnknown,
			State:       store.DocStatePending,
		}
		if err := d.Documents.Upsert(ctx, doc); err != nil {
			writeError(w, err)
			return
		}
	}

	// Create the bookmark.
	var titlePtr *string
	if req.Title != "" {
		titlePtr = &req.Title
	}
	var folderPtr *string
	if req.FolderPath != "" {
		folderPtr = &req.FolderPath
	}
	b := &store.Bookmark{
		TenantID:   d.TenantID,
		URL:        normURL,
		Title:      titlePtr,
		SavedAt:    time.Now().UTC(),
		Source:     store.SourceManual,
		FolderPath: folderPtr,
		Tags:       req.Tags,
		DocumentID: &doc.ID,
	}
	if err := d.Bookmarks.Create(ctx, b); err != nil {
		writeError(w, err)
		return
	}

	// Enqueue a fetch job if the document doesn't already have content.
	jobID := ""
	if doc.State == store.DocStatePending {
		payload, _ := json.Marshal(map[string]string{"document_id": doc.ID})
		job := &store.Job{
			TenantID: d.TenantID,
			Kind:     store.JobKindFetch,
			Payload:  payload,
		}
		if err := d.Queue.Enqueue(ctx, job); err != nil {
			writeError(w, err)
			return
		}
		jobID = job.ID
	}

	writeJSON(w, http.StatusCreated, BookmarkCreatedResponse{
		Bookmark: bookmarkToResponse(b, doc.State),
		JobID:    jobID,
	})
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
				state = doc.State
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
			state = doc.State
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
