package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/samsar/curio/internal/importer"
	"github.com/samsar/curio/internal/store"
)

// ImportRequest is the body of POST /v1/bookmarks/import.
//
// CLI parses bookmark files locally and POSTs the result — keeps the
// daemon's filesystem footprint small and makes hosted mode trivial
// (the daemon never needs to read a user's local files).
type ImportRequest struct {
	Source    string           `json:"source"` // chrome | safari | firefox | html | manual
	Bookmarks []ImportBookmark `json:"bookmarks"`
	// DryRun counts what the import would do and writes nothing.
	DryRun bool `json:"dry_run,omitempty"`
}

// ImportBookmark is one parsed bookmark from the client. Title and
// folder are optional; URL is required.
type ImportBookmark struct {
	URL        string    `json:"url"`
	Title      string    `json:"title,omitempty"`
	FolderPath string    `json:"folder_path,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	SavedAt    time.Time `json:"saved_at,omitzero"` // zero (absent): the source has no date, so it is stored dateless
}

// ImportResponse summarizes what happened, or for a dry run what would
// have. Counts always present, errors only when non-empty.
type ImportResponse struct {
	Source       string                        `json:"source"`
	Total        int                           `json:"total"`
	Created      int                           `json:"created"`
	Skipped      int                           `json:"skipped"`       // the bookmark already existed for this source
	Filtered     int                           `json:"filtered"`      // dropped by Indexable
	JobsEnqueued int                           `json:"jobs_enqueued"` // fetches for URLs new to the corpus
	FilteredBy   map[importer.FilterReason]int `json:"filtered_by,omitempty"`
	Errors       []string                      `json:"errors,omitempty"` // first ~10
	DryRun       bool                          `json:"dry_run,omitempty"`
	// NewURLs are, for a dry run, the URLs whose documents the import
	// would create and fetch, one each, in request order.
	NewURLs []string `json:"new_urls,omitempty"`
}

const importErrorsCap = 10

func (d Deps) handleImportBookmarks(w http.ResponseWriter, r *http.Request) {
	var req ImportRequest
	if err := decodeJSON(w, r, maxImportBody, &req); err != nil {
		d.writeError(w, r, err)
		return
	}
	if !store.ValidSource(req.Source) {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "source must be one of: "+sourceList)
		return
	}
	if len(req.Bookmarks) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "bookmarks list is empty")
		return
	}

	resp := ImportResponse{
		Source:     req.Source,
		Total:      len(req.Bookmarks),
		FilteredBy: map[importer.FilterReason]int{},
	}
	if req.DryRun {
		d.previewImport(w, r, req, resp)
		return
	}

	abandoned := d.ingest(r.Context(), req, &resp)
	// The rows stored are there whether the client stayed or not, and
	// claim no date until their source is classified.
	if resp.Created > 0 {
		d.SavedDates.Classify(req.Source)
	}
	if abandoned {
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// ingest saves req's bookmarks one by one, counting what it did in resp.
// It reports whether the client went away first: it then stops writing
// rows nobody will hear about. Each bookmark committed on its own, so a
// re-import picks up where this one stopped.
func (d Deps) ingest(ctx context.Context, req ImportRequest, resp *ImportResponse) (abandoned bool) {
	for i, in := range req.Bookmarks {
		if err := ctx.Err(); err != nil {
			d.Log.Info("import abandoned by the client", "processed", i, "total", len(req.Bookmarks), "err", err)
			return true
		}
		norm, ok := resp.classify(in.URL)
		if !ok {
			continue
		}
		in.URL = norm
		res, err := d.Bookmarks.Ingest(ctx, d.bookmarkRow(in, req.Source))
		switch {
		case errors.Is(err, store.ErrConflict):
			resp.Skipped++
			continue
		case err != nil:
			resp.appendError(norm + ": " + err.Error())
			continue
		}
		resp.Created++
		if res.FetchJob != nil {
			resp.JobsEnqueued++
		}
	}
	return false
}

// previewImport answers a dry run: the response the import of req would
// get against the library as it is now, writing nothing. It looks every
// distinct URL up at once, then replays Ingest's rules in request order:
// a URL seen earlier in the request, or one the source has a bookmark of,
// is skipped; any other is created, and fetched when its document is new.
func (d Deps) previewImport(w http.ResponseWriter, r *http.Request, req ImportRequest, resp ImportResponse) {
	urls := make([]string, 0, len(req.Bookmarks))
	var distinct []string
	seen := map[string]bool{}
	for _, in := range req.Bookmarks {
		norm, ok := resp.classify(in.URL)
		if !ok {
			continue
		}
		urls = append(urls, norm)
		if !seen[norm] {
			seen[norm] = true
			distinct = append(distinct, norm)
		}
	}
	found, err := d.Bookmarks.PreviewIngest(r.Context(), d.TenantID, req.Source, distinct)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	clear(seen)
	for _, u := range urls {
		switch {
		case seen[u], found[u].BookmarkExists:
			resp.Skipped++
		default:
			resp.Created++
			if !found[u].DocumentExists {
				resp.JobsEnqueued++
				resp.NewURLs = append(resp.NewURLs, u)
			}
		}
		seen[u] = true
	}
	resp.DryRun = true
	d.writeJSON(w, r, http.StatusOK, resp)
}

// classify is the import's verdict on one bookmark's URL, a dry run's and
// the real import's alike: the URL it is saved under, or false with the
// filter counted in r.
func (r *ImportResponse) classify(rawURL string) (string, bool) {
	norm, why := importer.Classify(rawURL)
	if why != "" {
		r.Filtered++
		r.FilteredBy[why]++
		return "", false
	}
	return norm, true
}

func (r *ImportResponse) appendError(msg string) {
	if len(r.Errors) >= importErrorsCap {
		return
	}
	r.Errors = append(r.Errors, msg)
}

// sourceList names the bookmark sources, for error details.
var sourceList = strings.Join(store.Sources(), ", ")
