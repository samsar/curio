package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
)

// JobResponse mirrors store.Job with timestamps as time.Time. Payload is
// passed through as raw JSON. DocURL and DocTitle come from a left-join
// on documents; they're empty for jobs that don't reference a doc
// (import, cluster, summarize) or when the doc was dropped.
type JobResponse struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Attempts  int             `json:"attempts"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	LastError *string         `json:"last_error,omitempty"`
	RunAfter  time.Time       `json:"run_after"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	DocURL    string          `json:"doc_url,omitempty"`
	DocTitle  string          `json:"doc_title,omitempty"`
}

// JobListResponse is the body of GET /v1/jobs.
type JobListResponse struct {
	Items []JobResponse `json:"items"`
}

func (d Deps) handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := q.Get("status") // e.g. "failed", "running"
	kind := q.Get("kind")
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	jq, ok := d.Queue.(*sqlitestore.Jobs)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "not supported",
			"JobQueue impl does not expose listing")
		return
	}
	jobs, err := jq.ListWithDoc(r.Context(), d.TenantID, status, kind, limit)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := JobListResponse{Items: make([]JobResponse, 0, len(jobs))}
	for _, j := range jobs {
		resp.Items = append(resp.Items, JobResponse{
			ID:        j.ID,
			Kind:      j.Kind,
			Status:    j.Status,
			Attempts:  j.Attempts,
			Payload:   j.Payload,
			LastError: j.LastError,
			RunAfter:  j.RunAfter,
			CreatedAt: j.CreatedAt,
			UpdatedAt: j.UpdatedAt,
			DocURL:    j.URL,
			DocTitle:  j.Title,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// silence: store import is used via the type assertion above.
var _ = store.JobStatusFailed
