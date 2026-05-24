package api

import (
	"encoding/json"
	"fmt"
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

// DeleteJobsResponse reports how many rows the operation removed.
type DeleteJobsResponse struct {
	Deleted int64  `json:"deleted"`
	Mode    string `json:"mode"`
}

// handleDeleteJobs supports two mutually exclusive modes:
//
//	?status=<failed|done|...>   exact-status delete; nothing else
//	?older_than=<duration>      prune by updated_at; e.g. "30d", "24h"
//
// Refusing to accept both at once avoids ambiguity. There's deliberately
// no "delete all" path — `rm ~/.curio/curio.db` is faster if that's
// genuinely what's wanted.
func (d Deps) handleDeleteJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := q.Get("status")
	olderThan := q.Get("older_than")

	if status == "" && olderThan == "" {
		writeProblem(w, http.StatusBadRequest, "bad request",
			"specify ?status=<name> or ?older_than=<duration>")
		return
	}
	if status != "" && olderThan != "" {
		writeProblem(w, http.StatusBadRequest, "bad request",
			"specify only one of ?status or ?older_than")
		return
	}

	jq, ok := d.Queue.(*sqlitestore.Jobs)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "not supported",
			"JobQueue impl does not expose delete")
		return
	}

	if status != "" {
		n, err := jq.DeleteByStatus(r.Context(), d.TenantID, status)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, DeleteJobsResponse{Deleted: n, Mode: "status=" + status})
		return
	}

	dur, err := parseExtendedDuration(olderThan)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "bad request",
			"older_than: "+err.Error())
		return
	}
	cutoff := time.Now().Add(-dur)
	n, err := jq.PruneOlderThan(r.Context(), d.TenantID, cutoff)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, DeleteJobsResponse{Deleted: n, Mode: "older_than=" + olderThan})
}

// parseExtendedDuration accepts standard Go durations plus "Nd" (days),
// which time.ParseDuration doesn't natively support. "30d" → 720h.
func parseExtendedDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Days suffix: convert to hours and re-parse.
	if last := s[len(s)-1]; last == 'd' || last == 'D' {
		var n int
		if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &n); err != nil {
			return 0, fmt.Errorf("invalid days: %v", err)
		}
		if n < 0 {
			return 0, fmt.Errorf("days must be non-negative")
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
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
