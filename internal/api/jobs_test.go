package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// TestDeleteJobs_FinishedOnly: live work can't be deleted through the API.
func TestDeleteJobs_FinishedOnly(t *testing.T) {
	s := newTestServer(t)
	for _, status := range []string{store.JobStatusPending, store.JobStatusRunning, store.JobStatusDone, store.JobStatusFailed} {
		require.NoError(t, s.deps.Queue.Enqueue(context.Background(),
			&store.Job{TenantID: "local", Kind: store.JobKindFetch, Status: status}))
	}

	for _, status := range []string{store.JobStatusPending, store.JobStatusRunning, "bogus"} {
		resp := s.do(t, request{method: http.MethodDelete, path: "/v1/jobs?status=" + status})
		assertProblem(t, resp, http.StatusBadRequest)
		assert.Contains(t, resp.body, "only finished jobs")
	}
	assert.Equal(t, 4, s.count(t, "jobs"))

	resp := s.do(t, request{method: http.MethodDelete, path: "/v1/jobs?status=done"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.JSONEq(t, `{"deleted":1,"mode":"status=done"}`, resp.body)

	resp = s.do(t, request{method: http.MethodDelete, path: "/v1/jobs?older_than=0s"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.JSONEq(t, `{"deleted":1,"mode":"older_than=0s"}`, resp.body, "prune takes only the failed job")
	assert.Equal(t, 2, s.count(t, "jobs"))
}
