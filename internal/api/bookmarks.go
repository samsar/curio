package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
		d.writeError(w, r, err)
		return
	}
	normURL, err := urlutil.Normalize(req.URL)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid url", err.Error())
		return
	}

	b := d.bookmarkRow(ImportBookmark{URL: normURL, Title: req.Title, FolderPath: req.FolderPath, Tags: req.Tags},
		store.SourceManual)
	res, err := d.Bookmarks.Ingest(r.Context(), b)
	if errors.Is(err, store.ErrConflict) {
		writeProblem(w, r, http.StatusConflict, "conflict",
			fmt.Sprintf("a manual bookmark for %s already exists", normURL))
		return
	}
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	jobID := ""
	if res.FetchJob != nil {
		jobID = res.FetchJob.ID
	}
	d.writeJSON(w, r, http.StatusCreated, BookmarkCreatedResponse{
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

// handleListBookmarks pages through the tenant's bookmarks in ID order.
// next_cursor is set exactly when another page follows: the store is asked
// for one row more than the page holds.
func (d Deps) handleListBookmarks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := listLimit(r)
	bms, err := d.Bookmarks.List(r.Context(), d.TenantID, store.ListBookmarksOpts{
		Source:     q.Get("source"),
		FolderPath: q.Get("folder"),
		Cursor:     q.Get("cursor"),
		Limit:      limit + 1,
	})
	if err != nil {
		d.writeError(w, r, err)
		return
	}

	var resp BookmarkListResponse
	if len(bms) > limit {
		bms = bms[:limit]
		next := bms[limit-1].ID
		resp.NextCursor = &next
	}
	resp.Items = make([]BookmarkResponse, 0, len(bms))
	for _, b := range bms {
		state, err := d.documentState(r.Context(), b)
		if err != nil {
			d.writeError(w, r, err)
			return
		}
		resp.Items = append(resp.Items, bookmarkToResponse(b, state))
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

func (d Deps) handleGetBookmark(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	b, err := d.Bookmarks.GetByID(r.Context(), id)
	if err != nil {
		d.writeLookupError(w, r, "bookmark", id, err)
		return
	}
	state, err := d.documentState(r.Context(), b)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, bookmarkToResponse(b, state))
}

// documentState is the state of the document a bookmark links to, or "" if
// the document was deleted (the foreign key sets document_id to NULL). Any
// lookup failure is an error the caller reports as a 500, a missing
// document included: a dangling document_id is an inconsistency, not a
// missing resource.
func (d Deps) documentState(ctx context.Context, b *store.Bookmark) (string, error) {
	if b.DocumentID == nil {
		return "", nil
	}
	doc, err := d.Documents.GetByID(ctx, *b.DocumentID)
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("bookmark %s links to document %s, which doesn't exist", b.ID, *b.DocumentID)
	}
	if err != nil {
		return "", fmt.Errorf("bookmark %s: load document %s: %w", b.ID, *b.DocumentID, err)
	}
	return string(doc.State), nil
}

func (d Deps) handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := d.Bookmarks.Delete(r.Context(), id); err != nil {
		d.writeLookupError(w, r, "bookmark", id, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
