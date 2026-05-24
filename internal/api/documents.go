package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
)

// DocumentResponse mirrors the openapi Document schema. tenant_id omitted.
type DocumentResponse struct {
	ID                string              `json:"id"`
	URL               string              `json:"url"`
	URLCanonical      *string             `json:"url_canonical,omitempty"`
	ContentType       string              `json:"content_type"`
	Title             *string             `json:"title,omitempty"`
	Author            *string             `json:"author,omitempty"`
	PublishedAt       *time.Time          `json:"published_at,omitempty"`
	Language          *string             `json:"language,omitempty"`
	WordCount         *int                `json:"word_count,omitempty"`
	State             string              `json:"state"`
	CurrentExtraction *ExtractionResponse `json:"current_extraction,omitempty"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
}

// ExtractionResponse mirrors the Extraction schema.
type ExtractionResponse struct {
	ID             string         `json:"id"`
	FetchedAt      time.Time      `json:"fetched_at"`
	Fetcher        string         `json:"fetcher"`
	Status         string         `json:"status"`
	ErrorMessage   *string        `json:"error_message,omitempty"`
	ExtractionMeta map[string]any `json:"extraction_meta,omitempty"`
}

func (d Deps) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	doc, err := d.Documents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	resp := documentToResponse(doc)
	if doc.CurrentExtractionID != nil {
		ext, err := d.Extractions.GetByID(r.Context(), *doc.CurrentExtractionID)
		if err == nil {
			er := &ExtractionResponse{
				ID:           ext.ID,
				FetchedAt:    ext.FetchedAt,
				Fetcher:      ext.Fetcher,
				Status:       ext.Status,
				ErrorMessage: ext.ErrorMessage,
			}
			if len(ext.ExtractionMeta) > 0 {
				// Best-effort; ignore decode failures so the request still succeeds.
				var meta map[string]any
				if e := decodeMetaJSON(ext.ExtractionMeta, &meta); e == nil {
					er.ExtractionMeta = meta
				}
			}
			resp.CurrentExtraction = er
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleRefetchDocument enqueues a fresh fetch job for the document. The
// existing extraction stays — when the new fetch finishes, it'll create
// a new extraction row and bump current_extraction_id. Returns 202 with
// the new job_id so the caller can poll.
func (d Deps) handleRefetchDocument(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	doc, err := d.Documents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	// Reset state so the next fetch starts clean and so /v1/stats reflects
	// that this document is once again pending.
	_ = d.Documents.UpdateState(r.Context(), doc.ID, store.DocStatePending)

	payload, _ := json.Marshal(map[string]string{"document_id": doc.ID})
	job := &store.Job{
		TenantID: d.TenantID,
		Kind:     store.JobKindFetch,
		Payload:  payload,
	}
	if err := d.Queue.Enqueue(r.Context(), job); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID})
}

// handleRefetchAll enqueues a fetch job for every document for the tenant
// (or just those in a particular state, via ?state=). Useful after a
// fetcher change to rebuild the corpus.
func (d Deps) handleRefetchAll(w http.ResponseWriter, r *http.Request) {
	wantState := r.URL.Query().Get("state") // empty = all states

	ds, ok := d.Documents.(*sqlite.Documents)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "not supported",
			"DocumentStore impl does not expose bulk listing")
		return
	}
	ids, err := ds.ListIDs(r.Context(), d.TenantID, wantState)
	if err != nil {
		writeError(w, err)
		return
	}

	enqueued := 0
	for _, id := range ids {
		_ = d.Documents.UpdateState(r.Context(), id, store.DocStatePending)
		payload, _ := json.Marshal(map[string]string{"document_id": id})
		if err := d.Queue.Enqueue(r.Context(), &store.Job{
			TenantID: d.TenantID,
			Kind:     store.JobKindFetch,
			Payload:  payload,
		}); err != nil {
			continue
		}
		enqueued++
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"jobs_enqueued": enqueued})
}

// handleGetDocumentContent streams the extracted markdown.
func (d Deps) handleGetDocumentContent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	doc, err := d.Documents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if doc.CurrentExtractionID == nil {
		writeProblem(w, http.StatusNotFound, "no content", "document has no extraction yet")
		return
	}
	ext, err := d.Extractions.GetByID(r.Context(), *doc.CurrentExtractionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if ext.MarkdownPath == nil {
		writeProblem(w, http.StatusNotFound, "no content", "extraction has no markdown path")
		return
	}
	fullPath := filepath.Join(d.Home.ContentDir(), *ext.MarkdownPath)
	f, err := os.Open(fullPath)
	if err != nil {
		writeError(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if _, err := copyAll(w, f); err != nil {
		// Headers already flushed; nothing useful to surface.
		return
	}
}

func documentToResponse(doc *store.Document) DocumentResponse {
	return DocumentResponse{
		ID:           doc.ID,
		URL:          doc.URL,
		URLCanonical: doc.URLCanonical,
		ContentType:  doc.ContentType,
		Title:        doc.Title,
		Author:       doc.Author,
		PublishedAt:  doc.PublishedAt,
		Language:     doc.Language,
		WordCount:    doc.WordCount,
		State:        doc.State,
		CreatedAt:    doc.CreatedAt,
		UpdatedAt:    doc.UpdatedAt,
	}
}
