package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/version"
)

// Health is the /v1/healthz response. PID and Home identify which daemon
// answered: clients use them to confirm the process on the port is the one
// serving their $CURIO_HOME before trusting or signalling it.
type Health struct {
	Status          string `json:"status"`
	PID             int    `json:"pid"`
	Home            string `json:"home"`
	Version         string `json:"version"`
	SchemaVersion   int    `json:"schema_version"`
	EmbeddingModel  string `json:"embedding_model"`
	EmbeddingDim    int    `json:"embedding_dim"`
	OllamaReachable bool   `json:"ollama_reachable"`
	OllamaDetail    string `json:"ollama_detail,omitempty"`
}

// ollamaPingTimeout caps the Ollama check in /v1/healthz, the only part of
// the handler that waits on another service. Clients probe healthz with a
// much longer timeout (client.Healthz), so a slow Ollama is reported as
// ollama_reachable=false, never mistaken for a missing daemon.
const ollamaPingTimeout = 500 * time.Millisecond

func (d Deps) handleHealth(w http.ResponseWriter, r *http.Request) {
	meta, err := d.Home.Meta()
	if err != nil {
		writeError(w, err)
		return
	}

	// Fail-open: an unreachable Ollama doesn't make the whole daemon
	// unhealthy (the user can still list bookmarks, browse docs, etc.).
	reachable := true
	detail := ""
	if pinger, ok := d.Embedder.(interface {
		Ping(context.Context) error
	}); ok {
		pctx, cancel := context.WithTimeout(r.Context(), ollamaPingTimeout)
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
		PID:             os.Getpid(),
		Home:            d.Home.Path,
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

// handleStats reports corpus and queue counts. A count that can't be read
// fails the request: `curio import --follow` and `curio status` read absent
// fields as zero.
func (d Deps) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bookmarks, err := d.Bookmarks.Count(ctx, d.TenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	docsByState, err := d.Documents.CountByState(ctx, d.TenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	jobsByStatus, err := d.Queue.CountByStatus(ctx, d.TenantID)
	if err != nil {
		writeError(w, err)
		return
	}

	docsTotal := 0
	for _, n := range docsByState {
		docsTotal += n
	}
	writeJSON(w, http.StatusOK, Stats{
		Version:          version.String(),
		BookmarksTotal:   bookmarks,
		DocumentsTotal:   docsTotal,
		DocumentsByState: stringKeys(docsByState),
		JobsByStatus:     stringKeys(jobsByStatus),
	})
}

// stringKeys converts a count map keyed by a store enum to the wire shape.
func stringKeys[K ~string](m map[K]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, n := range m {
		out[string(k)] = n
	}
	return out
}
