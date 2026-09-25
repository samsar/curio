package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// TestJobs_Enqueued: every path that makes a fetch job claimable wakes a
// wait on fetch jobs taken before it, once committed; paths that enqueue
// nothing, fail, or enqueue another kind don't.
func TestJobs_Enqueued(t *testing.T) {
	ctx := context.Background()
	newBookmark := func(url string, source string) *store.Bookmark {
		return &store.Bookmark{TenantID: "local", URL: url, Source: source, SavedAt: time.Now().UTC()}
	}
	cases := []struct {
		name string
		// setup runs before the wait is taken; act after.
		setup func(t *testing.T, db *DB) string
		act   func(t *testing.T, db *DB, id string)
		wake  bool
	}{
		{
			name: "Enqueue",
			act: func(t *testing.T, db *DB, _ string) {
				require.NoError(t, NewJobs(db).Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch}))
			},
			wake: true,
		},
		{
			name: "Enqueue of another kind",
			act: func(t *testing.T, db *DB, _ string) {
				require.NoError(t, NewJobs(db).Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindIndex}))
			},
		},
		{
			name: "Ingest of a new URL",
			act: func(t *testing.T, db *DB, _ string) {
				_, err := NewBookmarks(db).Ingest(ctx, newBookmark("https://example.com/new", store.SourceChrome))
				require.NoError(t, err)
			},
			wake: true,
		},
		{
			name: "Ingest of a known URL",
			setup: func(t *testing.T, db *DB) string {
				_, err := NewBookmarks(db).Ingest(ctx, newBookmark("https://example.com/known", store.SourceChrome))
				require.NoError(t, err)
				return ""
			},
			act: func(t *testing.T, db *DB, _ string) {
				res, err := NewBookmarks(db).Ingest(ctx, newBookmark("https://example.com/known", store.SourceSafari))
				require.NoError(t, err)
				require.Nil(t, res.FetchJob)
			},
		},
		{
			name: "duplicate Ingest",
			setup: func(t *testing.T, db *DB) string {
				_, err := NewBookmarks(db).Ingest(ctx, newBookmark("https://example.com/dup", store.SourceChrome))
				require.NoError(t, err)
				return ""
			},
			act: func(t *testing.T, db *DB, _ string) {
				_, err := NewBookmarks(db).Ingest(ctx, newBookmark("https://example.com/dup", store.SourceChrome))
				require.ErrorIs(t, err, store.ErrConflict)
			},
		},
		{
			name: "RequeueFetch",
			setup: func(t *testing.T, db *DB) string {
				return seedDoc(t, NewDocuments(db), "https://example.com/f", store.DocStateFailed).ID
			},
			act: func(t *testing.T, db *DB, id string) {
				_, err := NewDocuments(db).RequeueFetch(ctx, "local", id)
				require.NoError(t, err)
			},
			wake: true,
		},
		{
			name: "RequeueFetch rolled back",
			setup: func(t *testing.T, db *DB) string {
				d := seedDoc(t, NewDocuments(db), "https://example.com/f", store.DocStateFailed)
				failJobInserts(t, db)
				return d.ID
			},
			act: func(t *testing.T, db *DB, id string) {
				_, err := NewDocuments(db).RequeueFetch(ctx, "local", id)
				require.ErrorContains(t, err, "injected")
			},
		},
		{
			name: "RequeueFetchByStates",
			setup: func(t *testing.T, db *DB) string {
				return seedDoc(t, NewDocuments(db), "https://example.com/f", store.DocStateFailed).ID
			},
			act: func(t *testing.T, db *DB, _ string) {
				n, err := NewDocuments(db).RequeueFetchByStates(ctx, "local", []store.DocState{store.DocStateFailed})
				require.NoError(t, err)
				require.Equal(t, 1, n)
			},
			wake: true,
		},
		{
			name: "RequeueFetchByStates with nothing to requeue",
			act: func(t *testing.T, db *DB, _ string) {
				n, err := NewDocuments(db).RequeueFetchByStates(ctx, "local", []store.DocState{store.DocStateFailed})
				require.NoError(t, err)
				require.Zero(t, n)
			},
		},
		{
			name: "Requeue",
			setup: func(t *testing.T, db *DB) string {
				return enqueueWithStatus(t, NewJobs(db), store.JobKindFetch, store.JobStatusRunning, 1).ID
			},
			act: func(t *testing.T, db *DB, id string) {
				require.NoError(t, NewJobs(db).Requeue(ctx, id))
			},
			wake: true,
		},
		{
			name: "RecoverOrphans",
			setup: func(t *testing.T, db *DB) string {
				return enqueueWithStatus(t, NewJobs(db), store.JobKindFetch, store.JobStatusRunning, 1).ID
			},
			act: func(t *testing.T, db *DB, _ string) {
				_, requeued, err := NewJobs(db).RecoverOrphans(ctx, []store.JobKind{store.JobKindFetch})
				require.NoError(t, err)
				require.Equal(t, 1, requeued)
			},
			wake: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			var id string
			if tc.setup != nil {
				id = tc.setup(t, db)
			}
			q := NewJobs(db)
			fetch := q.Enqueued([]store.JobKind{store.JobKindFetch})
			tc.act(t, db, id)
			assert.Equal(t, tc.wake, isClosed(fetch))
		})
	}
}

func TestJobs_Enqueued_Waits(t *testing.T) {
	ctx := context.Background()
	q := NewJobs(newTestDB(t))
	fetch := q.Enqueued([]store.JobKind{store.JobKindFetch})
	fetchOrIndex := q.Enqueued([]store.JobKind{store.JobKindIndex, store.JobKindFetch})
	anyKind := q.Enqueued(nil)
	assert.Equal(t, fetch, q.Enqueued([]store.JobKind{store.JobKindFetch}), "waits on the same kinds share a channel")
	assert.Equal(t, fetchOrIndex, q.Enqueued([]store.JobKind{store.JobKindFetch, store.JobKindIndex}), "in any order")

	require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindIndex}))
	assert.False(t, isClosed(fetch))
	assert.True(t, isClosed(fetchOrIndex))
	assert.True(t, isClosed(anyKind))

	next := q.Enqueued([]store.JobKind{store.JobKindIndex, store.JobKindFetch})
	assert.False(t, isClosed(next), "a wait after the signal gets a new channel")

	running := &store.Job{TenantID: "local", Kind: store.JobKindFetch, Status: store.JobStatusRunning}
	require.NoError(t, q.Enqueue(ctx, running))
	assert.False(t, isClosed(fetch), "a job inserted as running is not claimable")
}
