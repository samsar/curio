package api

import (
	"net/http"

	"github.com/samansartipi/curio/internal/version"
)

// Health is the /v1/healthz response.
type Health struct {
	Status         string `json:"status"`
	Version        string `json:"version"`
	SchemaVersion  int    `json:"schema_version"`
	EmbeddingModel string `json:"embedding_model"`
	EmbeddingDim   int    `json:"embedding_dim"`
}

func (d Deps) handleHealth(w http.ResponseWriter, _ *http.Request) {
	meta, err := d.Home.Meta()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Health{
		Status:         "ok",
		Version:        version.String(),
		SchemaVersion:  meta.SchemaVersion,
		EmbeddingModel: meta.EmbeddingModel,
		EmbeddingDim:   meta.EmbeddingDim,
	})
}

// Stats is the /v1/stats response. M0 returns a coarse summary; detailed
// per-state and per-content-type breakdowns can be added once the
// stat-counter methods exist on the stores.
type Stats struct {
	Version string `json:"version"`
}

func (d Deps) handleStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Stats{
		Version: version.String(),
	})
}
