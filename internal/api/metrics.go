package api

import (
	"net/http"
	"time"
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

// ?window, in seconds: the last hour by default, and at most a day to keep
// the SQL bounded.
const (
	defaultMetricsWindow = int(time.Hour / time.Second)
	maxMetricsWindow     = int(24 * time.Hour / time.Second)
)

func (d Deps) handleMetrics(w http.ResponseWriter, r *http.Request) {
	window := time.Duration(intQuery(r, "window", defaultMetricsWindow, 1, maxMetricsWindow)) * time.Second

	rows, err := d.Queue.MetricsByKind(r.Context(), d.TenantID, window)
	if err != nil {
		d.writeError(w, r, err)
		return
	}

	resp := MetricsResponse{
		WindowSeconds: int(window / time.Second),
		ByKind:        make([]KindMetricsResponse, 0, len(rows)),
	}
	for _, m := range rows {
		resp.ByKind = append(resp.ByKind, KindMetricsResponse{
			Kind:                 string(m.Kind),
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
	d.writeJSON(w, r, http.StatusOK, resp)
}
