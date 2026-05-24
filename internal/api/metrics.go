package api

import (
	"net/http"
	"strconv"
	"time"

	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
)

// MetricsResponse is the body of GET /v1/metrics. All numbers are derived
// from the jobs table on demand — there's no separate metrics state to
// maintain. Window is in seconds; "in-flight" counts ignore the window
// (they're "right now").
type MetricsResponse struct {
	WindowSeconds int                   `json:"window_seconds"`
	ByKind        []KindMetricsResponse `json:"by_kind"`
}

// KindMetricsResponse is one row per job kind in the window. P50/P95/P99
// cover only successful runs (failed jobs have unrepresentative durations
// — they typically hit MaxAttempts × backoff time, not actual work time).
type KindMetricsResponse struct {
	Kind                 string  `json:"kind"`
	Count                int     `json:"count"`
	Failed               int     `json:"failed"`
	MeanMS               float64 `json:"mean_ms"`
	P50MS                float64 `json:"p50_ms"`
	P95MS                float64 `json:"p95_ms"`
	P99MS                float64 `json:"p99_ms"`
	Running              int     `json:"running"`
	OldestRunningSeconds int     `json:"oldest_running_seconds"`
}

func (d Deps) handleMetrics(w http.ResponseWriter, r *http.Request) {
	// Default window: last hour. Users can override with ?window=3600
	// (seconds). Capped at 24h to keep the SQL bounded.
	window := time.Hour
	if v := r.URL.Query().Get("window"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 86400 {
			window = time.Duration(n) * time.Second
		}
	}

	jq, ok := d.Queue.(*sqlitestore.Jobs)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "not supported",
			"JobQueue impl does not expose metrics")
		return
	}
	rows, err := jq.MetricsByKind(r.Context(), d.TenantID, window)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := MetricsResponse{
		WindowSeconds: int(window / time.Second),
		ByKind:        make([]KindMetricsResponse, 0, len(rows)),
	}
	for _, m := range rows {
		resp.ByKind = append(resp.ByKind, KindMetricsResponse{
			Kind:                 m.Kind,
			Count:                m.Count,
			Failed:               m.Failed,
			MeanMS:               m.MeanMS,
			P50MS:                m.P50MS,
			P95MS:                m.P95MS,
			P99MS:                m.P99MS,
			Running:              m.Running,
			OldestRunningSeconds: m.OldestRunningSeconds,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
