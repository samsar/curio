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
		Title:      store.NullableString(in.Title),
		SavedAt:    savedAt,
		Source:     source,
		FolderPath: store.NullableString(in.FolderPath),
		Tags:       in.Tags,
	}
}

// BookmarkListItem mirrors the openapi BookmarkListItem schema: a bookmark
// as GET /v1/bookmarks lists it, with what the list shows of its document.
// Each document field is omitted when it is empty, and all of them when
// the bookmark links to no document.
type BookmarkListItem struct {
	BookmarkResponse
	DocumentTitle        *string `json:"document_title,omitempty"`
	DocumentContentType  string  `json:"document_content_type,omitempty"`
	DocumentFailureCause string  `json:"document_failure_cause,omitempty"`
	DocumentLastError    string  `json:"document_last_error,omitempty"`
}

// BookmarkListResponse mirrors the openapi BookmarkList schema.
type BookmarkListResponse struct {
	Items      []BookmarkListItem `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

func (d Deps) handleListBookmarks(w http.ResponseWriter, r *http.Request) {
	opts, err := listBookmarksOpts(r)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	resp, err := d.listBookmarks(r.Context(), opts)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// listBookmarksOpts reads GET /v1/bookmarks' query: the order, the source
// filter, and what bookmarksPageOpts reads for that order. An order,
// filter or cursor the list can't take is a requestError.
func listBookmarksOpts(r *http.Request) (store.ListBookmarksOpts, error) {
	source := r.URL.Query().Get("source")
	if source != "" && !validSource(source) {
		return store.ListBookmarksOpts{}, badRequest("source %q must be one of: %s", source, sourceList)
	}
	order, err := bookmarkOrderParam(r)
	if err != nil {
		return store.ListBookmarksOpts{}, err
	}
	opts, err := bookmarksPageOpts(r, order)
	if err != nil {
		return store.ListBookmarksOpts{}, err
	}
	opts.Source = source
	return opts, nil
}

// bookmarksPageOpts reads the query of a page of bookmarks in order: the
// state, content_type, host, folder and cause filters, a cursor that order
// issued, and the page size in Limit. The Library's Date saved order reads
// its page with it too, having no source filter. A filter or cursor the
// list can't take is a requestError.
func bookmarksPageOpts(r *http.Request, order store.BookmarkOrder) (store.ListBookmarksOpts, error) {
	f, err := documentFilterParams(r)
	if err != nil {
		return store.ListBookmarksOpts{}, err
	}
	after, err := cursorParam(r, bookmarkCursorOrder(order))
	if err != nil {
		return store.ListBookmarksOpts{}, err
	}
	return store.ListBookmarksOpts{
		Order:       order,
		FolderPath:  r.URL.Query().Get("folder"),
		Host:        f.host,
		State:       f.state,
		ContentType: f.contentType,
		Cause:       f.cause,
		After:       after,
		Limit:       listLimit(r),
	}, nil
}

// bookmarkOrderParam reads ?order: created, the default, or saved.
func bookmarkOrderParam(r *http.Request) (store.BookmarkOrder, error) {
	order := store.BookmarkOrder(r.URL.Query().Get("order"))
	if order == "" {
		return store.BookmarkOrderCreated, nil
	}
	if !order.Valid() {
		return "", badRequest("order %q must be one of: %s, %s", order, store.BookmarkOrderCreated,
			store.BookmarkOrderSaved)
	}
	return order, nil
}

// bookmarkCursorOrder is the order the bookmark list's cursors record for
// a walk in order: none for the created order, the list's default.
func bookmarkCursorOrder(order store.BookmarkOrder) string {
	if order == store.BookmarkOrderSaved {
		return string(order)
	}
	return ""
}

// listBookmarks pages through the tenant's bookmarks that match opts, in
// opts.Order; opts.Limit is the page size, at least 1. NextCursor is set
// exactly when another page follows: the store is asked for one row more
// than the page holds. The cursor is the last row's key in the order the
// store walked (BookmarkOrder.Key), so the two can't drift apart.
func (d Deps) listBookmarks(ctx context.Context, opts store.ListBookmarksOpts) (BookmarkListResponse, error) {
	limit := opts.Limit
	opts.Limit = limit + 1
	bms, err := d.Bookmarks.List(ctx, d.TenantID, opts)
	if err != nil {
		return BookmarkListResponse{}, err
	}
	key := func(b store.BookmarkWithDocument) store.PageKey { return opts.Order.Key(b.Bookmark) }
	bms, next, err := onePage(bms, limit, bookmarkCursorOrder(opts.Order), key)
	if err != nil {
		return BookmarkListResponse{}, err
	}

	resp := BookmarkListResponse{Items: make([]BookmarkListItem, 0, len(bms)), NextCursor: next}
	for _, b := range bms {
		resp.Items = append(resp.Items, BookmarkListItem{
			BookmarkResponse:     bookmarkToResponse(b.Bookmark, string(b.DocumentState)),
			DocumentTitle:        b.DocumentTitle,
			DocumentContentType:  string(b.DocumentContentType),
			DocumentFailureCause: string(b.DocumentFailureCause),
			DocumentLastError:    b.DocumentLastError,
		})
	}
	return resp, nil
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
