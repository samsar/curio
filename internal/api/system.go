package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/samansartipi/curio/internal/embedder"
	"github.com/samansartipi/curio/internal/store"
	sqlitestore "github.com/samansartipi/curio/internal/store/sqlite"
	"github.com/samansartipi/curio/internal/version"
)

// Health is the /v1/healthz response.
type Health struct {
	Status          string `json:"status"`
	Version         string `json:"version"`
	SchemaVersion   int    `json:"schema_version"`
	EmbeddingModel  string `json:"embedding_model"`
	EmbeddingDim    int    `json:"embedding_dim"`
	OllamaReachable bool   `json:"ollama_reachable"`
	OllamaDetail    string `json:"ollama_detail,omitempty"`
}

func (d Deps) handleHealth(w http.ResponseWriter, r *http.Request) {
	meta, err := d.Home.Meta()
	if err != nil {
		writeError(w, err)
		return
	}

	// Cheap ollama ping with a tight timeout so /healthz stays fast.
	// Fail-open: an unreachable Ollama doesn't make the whole daemon
	// unhealthy (the user can still list bookmarks, browse docs, etc.).
	reachable := true
	detail := ""
	if pinger, ok := d.Embedder.(interface {
		Ping(context.Context) error
	}); ok {
		pctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		defer cancel()
		if err := pinger.Ping(pctx); err != nil {
			reachable = false
			switch {
			case errors.Is(err, embedder.ErrModelNotLoaded):
				detail = "model not pulled (try `ollama pull " + meta.EmbeddingModel + "`)"
			case errors.Is(err, embedder.ErrOllamaUnreachable):
				detail = "ollama unreachable (start it with `ollama serve` or `brew services start ollama`)"
			default:
				detail = err.Error()
			}
		}
	}

	writeJSON(w, http.StatusOK, Health{
		Status:          "ok",
		Version:         version.String(),
		SchemaVersion:   meta.SchemaVersion,
		EmbeddingModel:  meta.EmbeddingModel,
		EmbeddingDim:    meta.EmbeddingDim,
		OllamaReachable: reachable,
		OllamaDetail:    detail,
	})
}

// Stats is the /v1/stats response.
type Stats struct {
	Version          string         `json:"version"`
	BookmarksTotal   int            `json:"bookmarks_total"`
	DocumentsTotal   int            `json:"documents_total"`
	DocumentsByState map[string]int `json:"documents_by_state,omitempty"`
	JobsByStatus     map[string]int `json:"jobs_by_status,omitempty"`
}

func (d Deps) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := Stats{Version: version.String()}

	// Cheap counts via the concrete SQLite stores. The interface layer
	// doesn't expose count methods today; we type-assert when the impl
	// supports it. Falls back to zero/absent fields when it doesn't.
	if jq, ok := d.Queue.(*sqlitestore.Jobs); ok {
		if m, err := jq.CountByStatus(ctx, d.TenantID); err == nil {
			out.JobsByStatus = m
		}
	}

	if list, err := d.Bookmarks.List(ctx, d.TenantID, store.ListBookmarksOpts{Limit: 100000}); err == nil {
		out.BookmarksTotal = len(list)
	}
	// Documents total + by-state via the stats helper if present.
	if ds, ok := d.Documents.(*sqlitestore.Documents); ok {
		if total, by, err := ds.CountByState(ctx, d.TenantID); err == nil {
			out.DocumentsTotal = total
			out.DocumentsByState = by
		}
	}

	writeJSON(w, http.StatusOK, out)
}
