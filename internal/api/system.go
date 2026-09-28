package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/samsar/curio/internal/drift"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/version"
)

// Health is the /v1/healthz response. PID and Home identify which daemon
// answered: clients use them to confirm the process on the port is the one
// serving their $CURIO_HOME before trusting or signalling it.
// GenerationModel is the generation.model the daemon started with, which
// `curio up` compares with config.yaml's to know when a restart would
// change it. Upstreams are the services fetches depend on; like Ollama, a
// failing one doesn't make the daemon unhealthy, and neither does
// EmbeddingDrift. YouTubeFetcher is the yt-dlp the daemon found when it
// started, which it routes YouTube videos to for their transcripts, or
// empty: `curio up` tells from it whether a daemon predates an install.
// GitHubToken says whether the GitHub fetcher sends a token, from
// config.yaml or its environment: without one GitHub allows 60 API
// requests an hour, which a library with many github.com pages exceeds.
type Health struct {
	Status          string           `json:"status"`
	PID             int              `json:"pid"`
	Home            string           `json:"home"`
	Version         string           `json:"version"`
	SchemaVersion   int              `json:"schema_version"`
	EmbeddingModel  string           `json:"embedding_model"`
	EmbeddingDim    int              `json:"embedding_dim"`
	EmbeddingDrift  *EmbeddingDrift  `json:"embedding_drift,omitempty"`
	GenerationModel string           `json:"generation_model"`
	OllamaReachable bool             `json:"ollama_reachable"`
	OllamaDetail    string           `json:"ollama_detail,omitempty"`
	Upstreams       []UpstreamHealth `json:"upstreams"`
	YouTubeFetcher  string           `json:"youtube_fetcher,omitempty"`
	GitHubToken     bool             `json:"github_token"`
}

// EmbeddingDrift says what changed in the build that makes the home's
// embeddings (drift.Report), present only while it differs from the build
// recorded when the library was indexed.
type EmbeddingDrift struct {
	Changes   []DriftChange `json:"changes"`
	Fix       string        `json:"fix"`
	CheckedAt time.Time     `json:"checked_at"`
}

// DriftChange is one changed part of the build (drift.Change).
type DriftChange struct {
	What     string `json:"what"`
	Recorded string `json:"recorded"`
	Current  string `json:"current"`
}

// DriftMonitor watches the build that makes the home's embeddings: the
// daemon's is a *drift.Monitor.
type DriftMonitor interface {
	// Report is the last check's finding; no Ollama call.
	Report() drift.Report
	// Rebaseline makes the build serving now the recorded one.
	Rebaseline() error
}

// embeddingDrift is the drift the monitor last found, or nil when there is
// none or no monitor.
func (d Deps) embeddingDrift() *EmbeddingDrift {
	if d.Drift == nil {
		return nil
	}
	r := d.Drift.Report()
	if !r.Drifted() {
		return nil
	}
	changes := make([]DriftChange, 0, len(r.Changes))
	for _, c := range r.Changes {
		changes = append(changes, DriftChange{What: c.What, Recorded: c.Recorded, Current: c.Current})
	}
	return &EmbeddingDrift{Changes: changes, Fix: drift.Fix, CheckedAt: r.CheckedAt.UTC()}
}

// UpstreamHealth is an upstream's health on the wire (fetcher.UpstreamHealth).
// Times are UTC, and the ones that aren't set are omitted.
type UpstreamHealth struct {
	Name             string         `json:"name"`
	Enabled          bool           `json:"enabled"`
	State            string         `json:"state"`
	LastSuccessAt    time.Time      `json:"last_success_at,omitzero"`
	LastFailureAt    time.Time      `json:"last_failure_at,omitzero"`
	LastFailureClass string         `json:"last_failure_class,omitempty"`
	WindowSeconds    int            `json:"window_seconds"`
	Recent           map[string]int `json:"recent"`
	CooldownUntil    time.Time      `json:"cooldown_until,omitzero"`
}

// upstreams reports the health of each upstream the daemon tracks: an
// empty list, never null, when it tracks none.
func (d Deps) upstreams() []UpstreamHealth {
	var tracked []fetcher.UpstreamHealth
	if d.Upstreams != nil {
		tracked = d.Upstreams()
	}
	out := make([]UpstreamHealth, 0, len(tracked))
	for _, u := range tracked {
		out = append(out, UpstreamHealth{
			Name:             u.Name,
			Enabled:          u.Enabled,
			State:            string(u.State),
			LastSuccessAt:    u.LastSuccess.UTC(),
			LastFailureAt:    u.LastFailure.UTC(),
			LastFailureClass: string(u.LastFailureClass),
			WindowSeconds:    int(u.Window / time.Second),
			Recent:           stringKeys(u.Recent),
			CooldownUntil:    u.CooldownUntil.UTC(),
		})
	}
	return out
}

// ollamaPingTimeout caps the Ollama check in /v1/healthz, the only part of
// the handler that waits on another service. Clients probe healthz with a
// much longer timeout (client.Healthz), so a slow Ollama is reported as
// ollama_reachable=false, never mistaken for a missing daemon.
const ollamaPingTimeout = 500 * time.Millisecond

func (d Deps) handleHealth(w http.ResponseWriter, r *http.Request) {
	meta, err := d.Home.Meta()
	if err != nil {
		d.writeError(w, r, err)
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
			case errors.Is(err, ollama.ErrModelNotLoaded):
				detail = "model not pulled (try `ollama pull " + meta.EmbeddingModel + "`)"
			case errors.Is(err, ollama.ErrUnreachable):
				detail = "ollama unreachable (start it with `ollama serve` or `brew services start ollama`)"
			default:
				detail = err.Error()
			}
		}
	}

	d.writeJSON(w, r, http.StatusOK, Health{
		Status:          "ok",
		PID:             os.Getpid(),
		Home:            d.Home.Path,
		Version:         version.String(),
		SchemaVersion:   meta.SchemaVersion,
		EmbeddingModel:  meta.EmbeddingModel,
		EmbeddingDim:    meta.EmbeddingDim,
		EmbeddingDrift:  d.embeddingDrift(),
		GenerationModel: d.GenerationModel,
		OllamaReachable: reachable,
		OllamaDetail:    detail,
		Upstreams:       d.upstreams(),
		YouTubeFetcher:  d.YouTubeFetcher,
		GitHubToken:     d.GitHubToken,
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
		d.writeError(w, r, err)
		return
	}
	docsByState, err := d.Documents.CountByState(ctx, d.TenantID)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	jobsByStatus, err := d.Queue.CountByStatus(ctx, d.TenantID)
	if err != nil {
		d.writeError(w, r, err)
		return
	}

	docsTotal := 0
	for _, n := range docsByState {
		docsTotal += n
	}
	d.writeJSON(w, r, http.StatusOK, Stats{
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
