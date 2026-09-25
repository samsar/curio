package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/store"
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
	MarkdownPath   string         `json:"markdown_path,omitempty"`
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
			if ext.MarkdownPath != nil {
				er.MarkdownPath = *ext.MarkdownPath
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

// DocumentListItem is one row in the list response. Mirrors DocumentResponse
// but adds LastError from the join-with-jobs query AND MarkdownPath from
// the join-with-extractions query so debugging is one API call.
//
// MarkdownPath is the absolute on-disk path (content_dir + relative path)
// so the CLI can print something `cat`-friendly directly.
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

// DocumentListResponse is the body of GET /v1/documents.
type DocumentListResponse struct {
	Items []DocumentListItem `json:"items"`
}

func (d Deps) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	docs, err := d.Documents.ListWithLastError(r.Context(), d.TenantID, store.ListDocumentsOpts{
		State: store.DocState(r.URL.Query().Get("state")),
		Limit: listLimit(r),
	})
	if err != nil {
		writeError(w, err)
		return
	}

	contentDir := d.Home.ContentDir()
	out := DocumentListResponse{Items: make([]DocumentListItem, 0, len(docs))}
	for _, doc := range docs {
		item := DocumentListItem{
			ID:          doc.ID,
			URL:         doc.URL,
			Title:       doc.Title,
			ContentType: string(doc.ContentType),
			State:       string(doc.State),
			LastError:   doc.LastError,
			CreatedAt:   doc.CreatedAt,
			UpdatedAt:   doc.UpdatedAt,
		}
		if doc.MarkdownPath != "" {
			item.MarkdownPath = contentDir + "/" + doc.MarkdownPath
		}
		out.Items = append(out.Items, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// boolParam reports whether a query parameter is set to a truthy value
// ("1" or "true", case-insensitive).
func boolParam(r *http.Request, name string) bool {
	v := strings.ToLower(r.URL.Query().Get(name))
	return v == "1" || v == "true"
}

// handleRefetchDocument enqueues a fresh fetch job for the document. The
// existing extraction stays — when the new fetch finishes, it'll create
// a new extraction row and bump current_extraction_id. Returns 202 with
// the new job_id so the caller can poll.
//
// Dead documents (confirmed dead links) are refused with 409 unless
// ?force=1 — that's the whole point of the dead state. Force exists
// because the soft-404 heuristic can false-positive.
func (d Deps) handleRefetchDocument(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	doc, err := d.Documents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	if doc.State == store.DocStateDead && !boolParam(r, "force") {
		writeProblem(w, http.StatusConflict, "document is dead",
			"this document's URL was confirmed dead (404/410 or a not-found page); pass force=1 to refetch anyway")
		return
	}

	// The state reset and the new job commit together, so a failure can't
	// leave the document pending with no job behind it.
	job, err := d.Documents.RequeueFetch(r.Context(), d.TenantID, doc.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID})
}

// refetchAllDefaultStates is what refetch-all resets when no ?state= is
// given. Dead documents are left out: retrying confirmed dead links on every
// bulk refetch wastes the whole retry budget per URL. Asking for ?state=dead
// explicitly is the deliberate escape hatch.
var refetchAllDefaultStates = []store.DocState{store.DocStatePending, store.DocStateFetched, store.DocStateFailed}

// handleRefetchAll resets documents to pending and enqueues a fetch job for
// each, all in one transaction: either every matching document is requeued
// or none is. ?state= narrows it to one document state. Useful after a
// fetcher change to rebuild the corpus. Returns 202 with the number of jobs
// enqueued; there is no parent job to poll.
func (d Deps) handleRefetchAll(w http.ResponseWriter, r *http.Request) {
	state, err := docStateParam(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	states := refetchAllDefaultStates
	if state != "" {
		states = []store.DocState{state}
	}

	n, err := d.Documents.RequeueFetchByStates(r.Context(), d.TenantID, states)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"jobs_enqueued": n})
}

// docStateParam reads ?state. Empty means the caller's default; a value
// that isn't a document state is an error for a 400.
func docStateParam(r *http.Request) (store.DocState, error) {
	s := store.DocState(r.URL.Query().Get("state"))
	if s != "" && !s.Valid() {
		return "", fmt.Errorf("state %q must be one of: pending, fetched, failed, dead", s)
	}
	return s, nil
}

// handleReindexDocument enqueues an index job for the document — re-chunking
// and re-embedding its current extraction's markdown. Unlike refetch it does
// NOT re-fetch or reset state; the doc stays fetched. Useful after an
// embedding-model or chunker change, or to pick up new bookmark tags. The
// document must already have a current extraction.
func (d Deps) handleReindexDocument(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	doc, err := d.Documents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if doc.CurrentExtractionID == nil {
		writeProblem(w, http.StatusConflict, "no content",
			"document has no extraction to reindex; refetch it first")
		return
	}
	job, err := d.enqueueIndex(r.Context(), doc.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID})
}

// handleReindexAll enqueues index jobs for the documents in one state that
// have content. Defaults to state=fetched (the already-indexed set); ?state=
// overrides, validated as for refetch-all.
// Use after swapping the embedding model (same dimension) or the chunker.
//
// Index jobs change no document state, so a partial run strands nothing;
// it stops at the first failure and reports how far it got.
func (d Deps) handleReindexAll(w http.ResponseWriter, r *http.Request) {
	state, err := docStateParam(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	state = cmp.Or(state, store.DocStateFetched)
	// Only documents with an extraction: an index job for one without would
	// fail permanently and flip it to failed while its fetch is in flight.
	ids, err := d.Documents.ListIDsWithContent(r.Context(), d.TenantID, state)
	if err != nil {
		writeError(w, err)
		return
	}
	for i, id := range ids {
		if _, err := d.enqueueIndex(r.Context(), id); err != nil {
			writeProblem(w, http.StatusInternalServerError, "internal error",
				fmt.Sprintf("enqueued %d of %d index jobs before failing: %v", i, len(ids), err))
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"jobs_enqueued": len(ids)})
}

// enqueueIndex enqueues an index job for a document.
func (d Deps) enqueueIndex(ctx context.Context, docID string) (*store.Job, error) {
	job, err := store.NewDocumentJob(d.TenantID, store.JobKindIndex, docID)
	if err != nil {
		return nil, err
	}
	if err := d.Queue.Enqueue(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
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
	f, err := os.Open(filepath.Join(d.Home.ContentDir(), *ext.MarkdownPath))
	if errors.Is(err, fs.ErrNotExist) {
		// Deleting content from disk is supported (docs/data-model.md).
		writeProblem(w, http.StatusNotFound, "no content",
			"the extracted markdown is missing on disk; refetch the document")
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if _, err := io.Copy(w, f); err != nil {
		// The status line is out, so the client only sees a short body.
		d.Log.Warn("stream document content", "document_id", doc.ID, "err", err)
	}
}

func documentToResponse(doc *store.Document) DocumentResponse {
	return DocumentResponse{
		ID:           doc.ID,
		URL:          doc.URL,
		URLCanonical: doc.URLCanonical,
		ContentType:  string(doc.ContentType),
		Title:        doc.Title,
		Author:       doc.Author,
		PublishedAt:  doc.PublishedAt,
		Language:     doc.Language,
		WordCount:    doc.WordCount,
		State:        string(doc.State),
		CreatedAt:    doc.CreatedAt,
		UpdatedAt:    doc.UpdatedAt,
	}
}
