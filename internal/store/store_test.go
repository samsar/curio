package store_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func TestDocState_Valid(t *testing.T) {
	for _, s := range []store.DocState{store.DocStatePending, store.DocStateFetched, store.DocStateFailed, store.DocStateDead} {
		assert.True(t, s.Valid(), s)
	}
	for _, s := range []store.DocState{"", "bogus", "Fetched", "running"} {
		assert.False(t, s.Valid(), s)
	}
}

func TestJobStatus_IsFinished(t *testing.T) {
	cases := map[store.JobStatus]bool{
		store.JobStatusPending: false,
		store.JobStatusRunning: false,
		store.JobStatusDone:    true,
		store.JobStatusFailed:  true,
		"":                     false,
		"bogus":                false,
	}
	for status, want := range cases {
		assert.Equal(t, want, status.IsFinished(), status)
	}
}

func TestClusterRunStatus_IsFinished(t *testing.T) {
	cases := map[store.ClusterRunStatus]bool{
		store.ClusterRunRunning: false,
		store.ClusterRunDone:    true,
		store.ClusterRunFailed:  true,
		"":                      false,
	}
	for status, want := range cases {
		assert.Equal(t, want, status.IsFinished(), status)
	}
}

func TestNewDocumentJob(t *testing.T) {
	job, err := store.NewDocumentJob("local", store.JobKindIndex, "doc-1")
	require.NoError(t, err)
	assert.Equal(t, "local", job.TenantID)
	assert.Equal(t, store.JobKindIndex, job.Kind)
	assert.Empty(t, job.ID, "the queue assigns it")
	assert.JSONEq(t, `{"document_id":"doc-1"}`, string(job.Payload))

	var p store.DocumentJobPayload
	require.NoError(t, json.Unmarshal(job.Payload, &p))
	assert.Equal(t, "doc-1", p.DocumentID)
}

func TestSearchFilters_IsEmpty(t *testing.T) {
	assert.True(t, store.SearchFilters{}.IsEmpty())
	assert.True(t, store.SearchFilters{ContentType: []string{}, Host: []string{}}.IsEmpty(), "empty slices filter nothing")
	for name, f := range map[string]store.SearchFilters{
		"content type": {ContentType: []string{"pdf"}},
		"host":         {Host: []string{"example.com"}},
		"source":       {Source: []string{"chrome"}},
		"exclude":      {ExcludeDocumentID: "doc-1"},
	} {
		assert.False(t, f.IsEmpty(), name)
	}
}
