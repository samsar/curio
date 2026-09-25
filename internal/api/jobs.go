package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/store"
)

// JobResponse mirrors store.Job with timestamps as time.Time. Payload is
// passed through as raw JSON. DocURL and DocTitle come from a left-join
// on documents; they're empty for jobs that don't reference a doc
// (import, cluster, summarize) or when the doc was dropped.
type JobResponse struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	Attempts     int             `json:"attempts"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	LastError    *string         `json:"last_error,omitempty"`
	RunAfter     time.Time       `json:"run_after"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	DocURL       string          `json:"doc_url,omitempty"`
	DocTitle     string          `json:"doc_title,omitempty"`
	MarkdownPath string          `json:"markdown_path,omitempty"`
}

// JobListResponse is the body of GET /v1/jobs.
type JobListResponse struct {
	Items      []JobResponse `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// DeleteJobsResponse reports how many rows the operation removed.
type DeleteJobsResponse struct {
	Deleted int64  `json:"deleted"`
	Mode    string `json:"mode"`
}

// handleDeleteJobs supports two mutually exclusive modes:
//
//	?status=<done|failed>   exact-status delete; nothing else
//	?older_than=<duration>  prune finished jobs by updated_at; e.g. "30d", "24h"
//
// Both only ever remove finished (done/failed) jobs: a pending or running
// job is live work, and deleting it would strand its document in pending.
// Refusing to accept both at once avoids ambiguity. There's deliberately
// no "delete all" path — `rm ~/.curio/curio.db` is faster if that's
// genuinely what's wanted.
func (d Deps) handleDeleteJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := store.JobStatus(q.Get("status"))
	olderThan := q.Get("older_than")

	if status == "" && olderThan == "" {
		writeProblem(w, r, http.StatusBadRequest, "bad request",
			"specify ?status=<done|failed> or ?older_than=<duration>")
		return
	}
	if status != "" && olderThan != "" {
		writeProblem(w, r, http.StatusBadRequest, "bad request",
			"specify only one of ?status or ?older_than")
		return
	}
	if status != "" && !status.IsFinished() {
		writeProblem(w, r, http.StatusBadRequest, "bad request",
			fmt.Sprintf("status %q: only finished jobs (done, failed) can be deleted; "+
				"pending and running jobs are live work", status))
		return
	}

	if status != "" {
		n, err := d.Queue.DeleteByStatus(r.Context(), d.TenantID, status)
		if err != nil {
			d.writeError(w, r, err)
			return
		}
		d.writeJSON(w, r, http.StatusOK, DeleteJobsResponse{Deleted: n, Mode: "status=" + string(status)})
		return
	}

	dur, err := parseExtendedDuration(olderThan)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request",
			"older_than: "+err.Error())
		return
	}
	cutoff := time.Now().Add(-dur)
	n, err := d.Queue.PruneOlderThan(r.Context(), d.TenantID, cutoff)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, DeleteJobsResponse{Deleted: n, Mode: "older_than=" + olderThan})
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
			return 0, fmt.Errorf("invalid days: %w", err)
		}
		if n < 0 {
			return 0, fmt.Errorf("days must be non-negative")
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// handleListJobs pages through the tenant's jobs, most recently updated
// first. next_cursor is set exactly when another page follows.
func (d Deps) handleListJobs(w http.ResponseWriter, r *http.Request) {
	opts, err := jobFilters(r)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	if opts.After, err = cursorParam(r); err != nil {
		d.writeError(w, r, err)
		return
	}
	limit := listLimit(r)
	opts.Limit = limit + 1
	jobs, err := d.Queue.ListWithDoc(r.Context(), d.TenantID, opts)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	jobs, next, err := onePage(jobs, limit, func(j store.JobWithDoc) store.PageKey {
		return store.PageKey{At: j.UpdatedAt, ID: j.ID}
	})
	if err != nil {
		d.writeError(w, r, err)
		return
	}

	resp := JobListResponse{Items: make([]JobResponse, 0, len(jobs)), NextCursor: next}
	for _, j := range jobs {
		resp.Items = append(resp.Items, d.jobResponse(j))
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// handleGetJob returns one job as the list shows it: the job a 202 from
// refetch, reindex or interests/rebuild named, for clients to poll.
func (d Deps) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := d.Queue.GetWithDoc(r.Context(), d.TenantID, id)
	if err != nil {
		d.writeLookupError(w, r, "job", id, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, d.jobResponse(*job))
}

// jobResponse is the wire shape of a job and its document.
func (d Deps) jobResponse(j store.JobWithDoc) JobResponse {
	return JobResponse{
		ID:           j.ID,
		Kind:         string(j.Kind),
		Status:       string(j.Status),
		Attempts:     j.Attempts,
		Payload:      j.Payload,
		LastError:    j.LastError,
		RunAfter:     j.RunAfter,
		CreatedAt:    j.CreatedAt,
		UpdatedAt:    j.UpdatedAt,
		DocURL:       j.URL,
		DocTitle:     j.Title,
		MarkdownPath: d.contentPath(j.MarkdownPath),
	}
}

// jobFilters reads the job list's ?status and ?kind. Empty means no filter;
// a value the jobs table can't hold is a requestError.
func jobFilters(r *http.Request) (store.ListJobsOpts, error) {
	q := r.URL.Query()
	opts := store.ListJobsOpts{Status: store.JobStatus(q.Get("status")), Kind: store.JobKind(q.Get("kind"))}
	if opts.Status != "" && !opts.Status.Valid() {
		return store.ListJobsOpts{}, badRequest("status %q must be one of: pending, running, done, failed", opts.Status)
	}
	if opts.Kind != "" && !opts.Kind.Valid() {
		return store.ListJobsOpts{}, badRequest("kind %q must be one of: fetch, index, import, cluster, summarize", opts.Kind)
	}
	return opts, nil
}
