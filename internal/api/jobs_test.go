package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// TestDeleteJobs_FinishedOnly: live work can't be deleted through the API,
// however old it is.
func TestDeleteJobs_FinishedOnly(t *testing.T) {
	s := newTestServer(t)
	// Inserted directly with an old updated_at: timestamps are stored at
	// millisecond precision, so a job enqueued moments before the request
	// may not be strictly older than a "now" cutoff. The AFTER UPDATE
	// trigger rules out backdating an existing row instead.
	for _, status := range []store.JobStatus{store.JobStatusPending, store.JobStatusRunning, store.JobStatusDone, store.JobStatusFailed} {
		_, err := s.db.Exec(`INSERT INTO jobs (id, tenant_id, kind, payload, status, updated_at)
			VALUES (?, 'local', ?, '{}', ?, '2000-01-01T00:00:00.000Z')`,
			"job-"+string(status), store.JobKindFetch, status)
		require.NoError(t, err)
	}

	for _, status := range []store.JobStatus{store.JobStatusPending, store.JobStatusRunning, "bogus"} {
		resp := s.do(t, request{method: http.MethodDelete, path: "/v1/jobs?status=" + string(status)})
		assertProblem(t, resp, http.StatusBadRequest)
		assert.Contains(t, resp.body, "only finished jobs")
	}
	assert.Equal(t, 4, s.count(t, "jobs"))

	resp := s.do(t, request{method: http.MethodDelete, path: "/v1/jobs?status=done"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.JSONEq(t, `{"deleted":1,"mode":"status=done"}`, resp.body)

	resp = s.do(t, request{method: http.MethodDelete, path: "/v1/jobs?older_than=1d"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.JSONEq(t, `{"deleted":1,"mode":"older_than=1d"}`, resp.body, "prune takes only the failed job")

	var live []store.JobStatus
	rows, err := s.db.Query(`SELECT status FROM jobs ORDER BY status`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var st store.JobStatus
		require.NoError(t, rows.Scan(&st))
		live = append(live, st)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []store.JobStatus{store.JobStatusPending, store.JobStatusRunning}, live)
}
