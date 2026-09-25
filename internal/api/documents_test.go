package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func (s *testServer) docState(t *testing.T, id string) store.DocState {
	t.Helper()
	d, err := s.deps.Documents.GetByID(context.Background(), id)
	require.NoError(t, err)
	return d.State
}

// failJobInserts makes every job insert abort until the returned restore
// is called. Persistent rather than TEMP: a TEMP trigger lives on one pooled
// connection only.
func (s *testServer) failJobInserts(t *testing.T) (restore func()) {
	t.Helper()
	_, err := s.db.Exec(`CREATE TRIGGER t_fail BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	require.NoError(t, err)
	return func() {
		t.Helper()
		_, err := s.db.Exec(`DROP TRIGGER t_fail`)
		require.NoError(t, err)
	}
}

func TestRefetchDocument(t *testing.T) {
	s := newTestServer(t)
	dead := s.seedDocument(t, "https://example.com/gone", store.DocStateDead)

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/" + dead.ID + "/refetch"})
	assertProblem(t, resp, http.StatusConflict)
	assert.Equal(t, store.DocStateDead, s.docState(t, dead.ID))
	assert.Zero(t, s.count(t, "jobs"))

	resp = s.do(t, request{method: http.MethodPost, path: "/v1/documents/" + dead.ID + "/refetch?force=1"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	var body struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.body), &body))
	job, err := s.deps.Queue.GetByID(context.Background(), body.JobID)
	require.NoError(t, err)
	assert.Equal(t, store.JobKindFetch, job.Kind)
	assert.JSONEq(t, `{"document_id":"`+dead.ID+`"}`, string(job.Payload))
	assert.Equal(t, store.DocStatePending, s.docState(t, dead.ID))

	resp = s.do(t, request{method: http.MethodPost, path: "/v1/documents/00000000-0000-0000-0000-000000000000/refetch"})
	assertProblem(t, resp, http.StatusNotFound)
}

// TestRefetchDocument_StoreFailure: when the job can't be enqueued, the
// document keeps its state rather than sitting in pending with no job.
func TestRefetchDocument_StoreFailure(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFailed)
	s.failJobInserts(t)

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/" + doc.ID + "/refetch"})
	assertProblem(t, resp, http.StatusInternalServerError)
	assert.Equal(t, store.DocStateFailed, s.docState(t, doc.ID))
}

func TestRefetchAll(t *testing.T) {
	s := newTestServer(t)
	for _, st := range []store.DocState{store.DocStatePending, store.DocStateFetched, store.DocStateFailed, store.DocStateDead} {
		s.seedDocument(t, "https://example.com/"+string(st), st)
	}

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all?state=bogus"})
	assertProblem(t, resp, http.StatusBadRequest)
	assert.Zero(t, s.count(t, "jobs"))

	resp = s.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	assert.JSONEq(t, `{"jobs_enqueued":3}`, resp.body, "dead documents are skipped by default")
	assert.Equal(t, 3, s.count(t, "jobs"))

	resp = s.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all?state=dead"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	assert.JSONEq(t, `{"jobs_enqueued":1}`, resp.body)
}

func TestRefetchAll_StoreFailure(t *testing.T) {
	s := newTestServer(t)
	failed := s.seedDocument(t, "https://example.com/a", store.DocStateFailed)
	fetched := s.seedDocument(t, "https://example.com/b", store.DocStateFetched)
	s.failJobInserts(t)

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all"})
	assertProblem(t, resp, http.StatusInternalServerError)
	assert.Equal(t, store.DocStateFailed, s.docState(t, failed.ID))
	assert.Equal(t, store.DocStateFetched, s.docState(t, fetched.ID))
}

// TestReindexAll_StoreFailure: enqueue failures are reported, not skipped.
func TestReindexAll_StoreFailure(t *testing.T) {
	s := newTestServer(t)
	s.seedContent(t, s.seedDocument(t, "https://example.com/a", store.DocStateFetched), "# A")
	s.failJobInserts(t)

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/reindex-all"})
	assertProblem(t, resp, http.StatusInternalServerError)
	assert.Contains(t, resp.body, "enqueued 0 of 1")
}

// TestGetDocumentContent_MissingFile: content deleted from disk is a 404
// that says what to do, not a 500 carrying the absolute path.
func TestGetDocumentContent_MissingFile(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	ext := s.seedContent(t, doc, "# A")
	require.NoError(t, os.Remove(filepath.Join(s.deps.Home.ContentDir(), *ext.MarkdownPath)))

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + doc.ID + "/content"})
	assertProblem(t, resp, http.StatusNotFound)
	assert.Contains(t, resp.body, "refetch")
	assert.NotContains(t, resp.body, s.deps.Home.Path)
}

// TestReindexAll_OnlyDocumentsWithContent: an index job for a document with
// no extraction fails permanently and marks it failed, so reindex-all skips
// such documents; and it validates ?state like refetch-all.
func TestReindexAll_OnlyDocumentsWithContent(t *testing.T) {
	s := newTestServer(t)
	fetching := s.seedDocument(t, "https://example.com/fetching", store.DocStatePending)
	withContent := s.seedDocument(t, "https://example.com/fetched-once", store.DocStatePending)
	s.seedContent(t, withContent, "# Fetched once")

	assertProblem(t, s.do(t, request{method: http.MethodPost, path: "/v1/documents/reindex-all?state=bogus"}),
		http.StatusBadRequest)
	assert.Zero(t, s.count(t, "jobs"))

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/reindex-all?state=pending"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	assert.JSONEq(t, `{"jobs_enqueued":1}`, resp.body)
	var payload string
	require.NoError(t, s.db.QueryRow(`SELECT payload FROM jobs WHERE kind = 'index'`).Scan(&payload))
	assert.JSONEq(t, `{"document_id":"`+withContent.ID+`"}`, payload)
	assert.Equal(t, store.DocStatePending, s.docState(t, fetching.ID))
}
