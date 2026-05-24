package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/samsar/curio/internal/importer"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/urlutil"
)

// ImportRequest is the body of POST /v1/bookmarks/import.
//
// CLI parses bookmark files locally and POSTs the result — keeps the
// daemon's filesystem footprint small and makes hosted mode trivial
// (the daemon never needs to read a user's local files).
type ImportRequest struct {
	Source    string           `json:"source"` // chrome | safari | firefox | html | manual
	Bookmarks []ImportBookmark `json:"bookmarks"`
}

// ImportBookmark is one parsed bookmark from the client. Title and
// folder are optional; URL is required.
type ImportBookmark struct {
	URL        string    `json:"url"`
	Title      string    `json:"title,omitempty"`
	FolderPath string    `json:"folder_path,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	SavedAt    time.Time `json:"saved_at,omitempty"`
}

// ImportResponse summarizes what happened. Counts always present, errors
// only when non-empty.
type ImportResponse struct {
	Source       string                        `json:"source"`
	Total        int                           `json:"total"`
	Created      int                           `json:"created"`
	Skipped      int                           `json:"skipped"`  // unique-conflict on existing bookmark
	Filtered     int                           `json:"filtered"` // dropped by Indexable
	JobsEnqueued int                           `json:"jobs_enqueued"`
	FilteredBy   map[importer.FilterReason]int `json:"filtered_by,omitempty"`
	Errors       []string                      `json:"errors,omitempty"` // first ~10
}

const importErrorsCap = 10

func (d Deps) handleImportBookmarks(w http.ResponseWriter, r *http.Request) {
	var req ImportRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	if !validImportSource(req.Source) {
		writeProblem(w, http.StatusBadRequest, "bad request",
			"source must be one of: chrome, safari, firefox, html, manual")
		return
	}
	if len(req.Bookmarks) == 0 {
		writeProblem(w, http.StatusBadRequest, "bad request", "bookmarks list is empty")
		return
	}

	ctx := r.Context()
	resp := ImportResponse{
		Source:     req.Source,
		Total:      len(req.Bookmarks),
		FilteredBy: map[importer.FilterReason]int{},
	}

	for _, in := range req.Bookmarks {
		// Filter first; cheaper to reject before any DB work.
		ok, why := importer.Indexable(in.URL)
		if !ok {
			resp.Filtered++
			resp.FilteredBy[why]++
			continue
		}

		normURL, err := urlutil.Normalize(in.URL)
		if err != nil {
			resp.Filtered++
			resp.FilteredBy[importer.ReasonInvalidURL]++
			continue
		}

		// Find or create the underlying document.
		doc, err := d.Documents.GetByURL(ctx, d.TenantID, normURL)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			resp.appendError(err.Error())
			continue
		}
		if doc == nil {
			doc = &store.Document{
				TenantID:    d.TenantID,
				URL:         normURL,
				ContentType: store.ContentTypeUnknown,
				State:       store.DocStatePending,
			}
			if err := d.Documents.Upsert(ctx, doc); err != nil {
				resp.appendError(err.Error())
				continue
			}
		}

		// Create the bookmark; ErrConflict is the expected dedup case.
		var titlePtr, folderPtr *string
		if in.Title != "" {
			t := in.Title
			titlePtr = &t
		}
		if in.FolderPath != "" {
			f := in.FolderPath
			folderPtr = &f
		}
		saved := in.SavedAt
		if saved.IsZero() {
			saved = time.Now().UTC()
		}
		b := &store.Bookmark{
			TenantID:   d.TenantID,
			URL:        normURL,
			Title:      titlePtr,
			SavedAt:    saved,
			Source:     req.Source,
			FolderPath: folderPtr,
			Tags:       in.Tags,
			DocumentID: &doc.ID,
		}
		if err := d.Bookmarks.Create(ctx, b); err != nil {
			if errors.Is(err, store.ErrConflict) {
				resp.Skipped++
				continue
			}
			resp.appendError(err.Error())
			continue
		}
		resp.Created++

		// Only enqueue a fetch job if the document still needs one.
		if doc.State == store.DocStatePending {
			payload, _ := json.Marshal(map[string]string{"document_id": doc.ID})
			if err := d.Queue.Enqueue(ctx, &store.Job{
				TenantID: d.TenantID,
				Kind:     store.JobKindFetch,
				Payload:  payload,
			}); err != nil {
				resp.appendError(err.Error())
				continue
			}
			resp.JobsEnqueued++
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (r *ImportResponse) appendError(msg string) {
	if len(r.Errors) >= importErrorsCap {
		return
	}
	r.Errors = append(r.Errors, msg)
}

func validImportSource(s string) bool {
	switch s {
	case store.SourceChrome, store.SourceSafari, store.SourceFirefox,
		store.SourceManual, "html":
		return true
	}
	return false
}
