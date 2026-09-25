package api

import (
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
	Skipped      int                           `json:"skipped"`       // the bookmark already existed for this source
	Filtered     int                           `json:"filtered"`      // dropped by Indexable
	JobsEnqueued int                           `json:"jobs_enqueued"` // fetches for URLs new to the corpus
	FilteredBy   map[importer.FilterReason]int `json:"filtered_by,omitempty"`
	Errors       []string                      `json:"errors,omitempty"` // first ~10
}

const importErrorsCap = 10

func (d Deps) handleImportBookmarks(w http.ResponseWriter, r *http.Request) {
	var req ImportRequest
	if err := decodeJSON(w, r, maxImportBody, &req); err != nil {
		d.writeError(w, r, err)
		return
	}
	if !validImportSource(req.Source) {
		writeProblem(w, r, http.StatusBadRequest, "bad request",
			"source must be one of: chrome, safari, firefox, html, manual")
		return
	}
	if len(req.Bookmarks) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "bookmarks list is empty")
		return
	}

	ctx := r.Context()
	resp := ImportResponse{
		Source:     req.Source,
		Total:      len(req.Bookmarks),
		FilteredBy: map[importer.FilterReason]int{},
	}

	for i, in := range req.Bookmarks {
		if err := ctx.Err(); err != nil {
			// The client has gone: stop writing rows nobody will hear about.
			// Each bookmark committed on its own, so a re-import picks up
			// where this one stopped.
			d.Log.Info("import abandoned by the client", "processed", i, "total", len(req.Bookmarks), "err", err)
			return
		}

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

		in.URL = normURL
		res, err := d.Bookmarks.Ingest(ctx, d.bookmarkRow(in, req.Source))
		switch {
		case errors.Is(err, store.ErrConflict):
			resp.Skipped++
			continue
		case err != nil:
			resp.appendError(normURL + ": " + err.Error())
			continue
		}
		resp.Created++
		if res.FetchJob != nil {
			resp.JobsEnqueued++
		}
	}

	d.writeJSON(w, r, http.StatusOK, resp)
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
		store.SourceManual, store.SourceHTML:
		return true
	}
	return false
}
