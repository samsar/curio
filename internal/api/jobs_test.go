package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

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

func TestListJobs(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	title := "A"
	_, err := s.db.Exec(`UPDATE documents SET title = ? WHERE id = ?`, title, doc.ID)
	require.NoError(t, err)
	ext := s.seedContent(t, doc, "# A")
	for _, kind := range []store.JobKind{store.JobKindFetch, store.JobKindIndex} {
		job, err := store.NewDocumentJob("local", kind, doc.ID)
		require.NoError(t, err)
		job.Status = store.JobStatusDone
		require.NoError(t, s.deps.Queue.Enqueue(ctx, job))
	}
	require.NoError(t, s.deps.Queue.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindCluster}))

	list := func(query string) JobListResponse {
		t.Helper()
		resp := s.do(t, request{method: http.MethodGet, path: "/v1/jobs" + query})
		require.Equal(t, http.StatusOK, resp.status, resp.body)
		var got JobListResponse
		require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
		return got
	}

	assert.Len(t, list("").Items, 3)

	done := list("?status=done&kind=index")
	require.Len(t, done.Items, 1)
	j := done.Items[0]
	assert.Equal(t, "index", j.Kind)
	assert.Equal(t, "done", j.Status)
	assert.Equal(t, "https://example.com/a", j.DocURL)
	assert.Equal(t, "A", j.DocTitle)
	assert.Equal(t, filepath.Join(s.deps.Home.ContentDir(), *ext.MarkdownPath), j.MarkdownPath)
	assert.JSONEq(t, `{"document_id":"`+doc.ID+`"}`, string(j.Payload))

	pending := list("?status=pending&limit=1")
	require.Len(t, pending.Items, 1)
	assert.Equal(t, "cluster", pending.Items[0].Kind)
	assert.Empty(t, pending.Items[0].DocURL)
}

func TestDeleteJobs_BadRequests(t *testing.T) {
	s := newTestServer(t)
	for _, query := range []string{"", "?status=done&older_than=1d", "?older_than=soon", "?older_than=xd", "?older_than=-2d"} {
		t.Run(query, func(t *testing.T) {
			assertProblem(t, s.do(t, request{method: http.MethodDelete, path: "/v1/jobs" + query}), http.StatusBadRequest)
		})
	}
}

func TestParseExtendedDuration(t *testing.T) {
	cases := map[string]time.Duration{"30d": 30 * 24 * time.Hour, "2D": 48 * time.Hour, "90m": 90 * time.Minute, "0s": 0}
	for in, want := range cases {
		got, err := parseExtendedDuration(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "d", "-1d", "soon"} {
		_, err := parseExtendedDuration(in)
		assert.Error(t, err, in)
	}
}

func TestMetrics(t *testing.T) {
	s := newTestServer(t)
	// Two index jobs that ran for 1s and 3s and finished a minute ago, a
	// fetch that finished two hours ago, and a fetch running now.
	for i, row := range []struct {
		kind, status, started, updated string
	}{
		{"index", "done", "-61 seconds", "-60 seconds"},
		{"index", "done", "-63 seconds", "-60 seconds"},
		{"fetch", "done", "-7201 seconds", "-7200 seconds"},
		{"fetch", "running", "-10 seconds", "-10 seconds"},
	} {
		_, err := s.db.Exec(`INSERT INTO jobs (id, tenant_id, kind, payload, status, started_at, updated_at)
			VALUES (?, 'local', ?, '{}', ?, strftime('%Y-%m-%dT%H:%M:%fZ','now',?), strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
			fmt.Sprintf("job-%d", i), row.kind, row.status, row.started, row.updated)
		require.NoError(t, err)
	}

	metrics := func(query string) MetricsResponse {
		t.Helper()
		resp := s.do(t, request{method: http.MethodGet, path: "/v1/metrics" + query})
		require.Equal(t, http.StatusOK, resp.status, resp.body)
		var got MetricsResponse
		require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
		return got
	}

	hour := metrics("")
	assert.Equal(t, 3600, hour.WindowSeconds)
	require.Len(t, hour.ByKind, 2)
	fetch, index := hour.ByKind[0], hour.ByKind[1]
	assert.Equal(t, "fetch", fetch.Kind)
	assert.Zero(t, fetch.Count, "finished before the window")
	assert.Equal(t, 1, fetch.Running)
	assert.InDelta(t, 10, fetch.OldestRunningSeconds, 2)
	assert.Equal(t, "index", index.Kind)
	assert.Equal(t, 2, index.Count)
	assert.InDelta(t, 2000, index.MeanMS, 5)
	assert.InDelta(t, 3000, index.P95MS, 5)

	wide := metrics("?window=10800")
	assert.Equal(t, 10800, wide.WindowSeconds)
	assert.Equal(t, 1, wide.ByKind[0].Count, "the older fetch is inside three hours")

	assert.Equal(t, 3600, metrics("?window=999999").WindowSeconds, "past 24h falls back to the default")
}
