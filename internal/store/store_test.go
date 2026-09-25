package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
