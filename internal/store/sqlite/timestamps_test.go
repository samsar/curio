package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// updated_at is written by each UPDATE statement; no trigger maintains it.

const pinnedUpdatedAt = "2000-01-01T00:00:00.000Z"

func pinUpdatedAt(t *testing.T, db *DB, table, id string) {
	t.Helper()
	res, err := db.Exec(`UPDATE `+table+` SET updated_at = ? WHERE id = ?`, pinnedUpdatedAt, id)
	require.NoError(t, err)
	require.NoError(t, ensureRow(res, table))
}

func storedUpdatedAt(t *testing.T, db *DB, table, id string) time.Time {
	t.Helper()
	var s string
	require.NoError(t, db.QueryRow(`SELECT updated_at FROM `+table+` WHERE id = ?`, id).Scan(&s))
	ts, err := parseTime(s)
	require.NoError(t, err)
	return ts
}

// TestUpdatedAt_SetByEveryUpdate pins each row's updated_at in the past,
// runs one store UPDATE on it, and expects updated_at to have moved to now.
func TestUpdatedAt_SetByEveryUpdate(t *testing.T) {
	ctx := context.Background()

	newDoc := func(t *testing.T, db *DB, state store.DocState) *store.Document {
		t.Helper()
		d := &store.Document{TenantID: "local", URL: "https://example.com/doc", State: state}
		require.NoError(t, NewDocuments(db).Create(ctx, d))
		return d
	}
	newExtraction := func(t *testing.T, db *DB, docID string) string {
		t.Helper()
		e := &store.DocumentExtraction{DocumentID: docID, Fetcher: "test", Status: store.ExtractionStatusOK}
		require.NoError(t, NewExtractions(db).Create(ctx, e))
		return e.ID
	}
	newJob := func(t *testing.T, db *DB, status store.JobStatus, attempts int) string {
		t.Helper()
		return enqueueWithStatus(t, NewJobs(db), store.JobKindFetch, status, attempts).ID
	}

	cases := []struct {
		name  string
		table string
		// setup creates the row and returns its ID; update runs the store
		// method under test on it.
		setup  func(t *testing.T, db *DB) string
		update func(t *testing.T, db *DB, id string)
	}{
		{
			name: "Documents.ApplyFetch", table: "documents",
			setup: func(t *testing.T, db *DB) string { return newDoc(t, db, store.DocStatePending).ID },
			update: func(t *testing.T, db *DB, id string) {
				require.NoError(t, NewDocuments(db).ApplyFetch(ctx, id, store.FetchedMetadata{
					ExtractionID: newExtraction(t, db, id), ContentType: store.ContentTypeArticle}))
			},
		},
		{
			name: "Documents.UpdateState", table: "documents",
			setup: func(t *testing.T, db *DB) string { return newDoc(t, db, store.DocStatePending).ID },
			update: func(t *testing.T, db *DB, id string) {
				require.NoError(t, NewDocuments(db).UpdateState(ctx, id, store.DocStateFetched))
			},
		},
		{
			name: "Documents.SetCurrentExtraction", table: "documents",
			setup: func(t *testing.T, db *DB) string { return newDoc(t, db, store.DocStatePending).ID },
			update: func(t *testing.T, db *DB, id string) {
				require.NoError(t, NewDocuments(db).SetCurrentExtraction(ctx, id, newExtraction(t, db, id)))
			},
		},
		{
			name: "Documents.RequeueFetch", table: "documents",
			setup: func(t *testing.T, db *DB) string { return newDoc(t, db, store.DocStateFailed).ID },
			update: func(t *testing.T, db *DB, id string) {
				_, err := NewDocuments(db).RequeueFetch(ctx, "local", id)
				require.NoError(t, err)
			},
		},
		{
			name: "Documents.RequeueFetchByStates", table: "documents",
			setup: func(t *testing.T, db *DB) string { return newDoc(t, db, store.DocStateFailed).ID },
			update: func(t *testing.T, db *DB, _ string) {
				n, err := NewDocuments(db).RequeueFetchByStates(ctx, "local", []store.DocState{store.DocStateFailed})
				require.NoError(t, err)
				require.Equal(t, 1, n)
			},
		},
		{
			name: "Bookmarks.LinkDocument", table: "bookmarks",
			setup: func(t *testing.T, db *DB) string {
				b := &store.Bookmark{TenantID: "local", URL: "https://example.com/doc",
					Source: store.SourceManual, SavedAt: time.Now().UTC()}
				require.NoError(t, NewBookmarks(db).Create(ctx, b))
				return b.ID
			},
			update: func(t *testing.T, db *DB, id string) {
				doc := newDoc(t, db, store.DocStatePending)
				require.NoError(t, NewBookmarks(db).LinkDocument(ctx, id, doc.ID))
			},
		},
		{
			name: "Jobs.ClaimNext", table: "jobs",
			setup: func(t *testing.T, db *DB) string { return newJob(t, db, store.JobStatusPending, 0) },
			update: func(t *testing.T, db *DB, _ string) {
				_, err := NewJobs(db).ClaimNext(ctx, nil)
				require.NoError(t, err)
			},
		},
		{
			name: "Jobs.MarkDone", table: "jobs",
			setup: func(t *testing.T, db *DB) string { return newJob(t, db, store.JobStatusRunning, 1) },
			update: func(t *testing.T, db *DB, id string) {
				require.NoError(t, NewJobs(db).MarkDone(ctx, id))
			},
		},
		{
			name: "Jobs.MarkFailed retry", table: "jobs",
			setup: func(t *testing.T, db *DB) string { return newJob(t, db, store.JobStatusRunning, 1) },
			update: func(t *testing.T, db *DB, id string) {
				permanent, err := NewJobs(db).MarkFailed(ctx, id, "transient", true)
				require.NoError(t, err)
				require.False(t, permanent)
			},
		},
		{
			name: "Jobs.MarkFailed permanent", table: "jobs",
			setup: func(t *testing.T, db *DB) string { return newJob(t, db, store.JobStatusRunning, 1) },
			update: func(t *testing.T, db *DB, id string) {
				permanent, err := NewJobs(db).MarkFailed(ctx, id, "bad input", false)
				require.NoError(t, err)
				require.True(t, permanent)
			},
		},
		{
			name: "Jobs.Requeue", table: "jobs",
			setup: func(t *testing.T, db *DB) string { return newJob(t, db, store.JobStatusRunning, 1) },
			update: func(t *testing.T, db *DB, id string) {
				require.NoError(t, NewJobs(db).Requeue(ctx, id))
			},
		},
		{
			name: "Jobs.RecoverOrphans requeue", table: "jobs",
			setup: func(t *testing.T, db *DB) string { return newJob(t, db, store.JobStatusRunning, 1) },
			update: func(t *testing.T, db *DB, _ string) {
				_, requeued, err := NewJobs(db).RecoverOrphans(ctx, []store.JobKind{store.JobKindFetch})
				require.NoError(t, err)
				require.Equal(t, 1, requeued)
			},
		},
		{
			name: "Jobs.RecoverOrphans fail", table: "jobs",
			setup: func(t *testing.T, db *DB) string { return newJob(t, db, store.JobStatusRunning, 5) },
			update: func(t *testing.T, db *DB, _ string) {
				failed, _, err := NewJobs(db).RecoverOrphans(ctx, []store.JobKind{store.JobKindFetch})
				require.NoError(t, err)
				require.Len(t, failed, 1)
			},
		},
		{
			name: "Insights.FinishRun", table: "cluster_runs",
			setup: func(t *testing.T, db *DB) string {
				run := &store.ClusterRun{TenantID: "local", Algo: "test"}
				require.NoError(t, NewInsights(db).CreateRun(ctx, run))
				return run.ID
			},
			update: func(t *testing.T, db *DB, id string) {
				require.NoError(t, NewInsights(db).FinishRun(ctx, id, store.RunResult{Status: store.ClusterRunDone}))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			id := tc.setup(t, db)
			pinUpdatedAt(t, db, tc.table, id)

			before := time.Now().UTC().Add(-time.Second)
			tc.update(t, db, id)

			got := storedUpdatedAt(t, db, tc.table, id)
			assert.True(t, got.After(before), "updated_at %s did not advance to now", got)
		})
	}
}

// TestJobs_ReturnedJobsCarryStoredUpdatedAt: RETURNING reports the row as
// the statement wrote it, so a job handed back by a transition has the
// updated_at a later read sees.
func TestJobs_ReturnedJobsCarryStoredUpdatedAt(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	q := NewJobs(db)
	q.MaxAttempts = 2

	pending := enqueueWithStatus(t, q, store.JobKindFetch, store.JobStatusPending, 0)
	pinUpdatedAt(t, db, "jobs", pending.ID)
	claimed, err := q.ClaimNext(ctx, nil)
	require.NoError(t, err)
	got, err := q.GetByID(ctx, claimed.ID)
	require.NoError(t, err)
	assert.Equal(t, got.UpdatedAt, claimed.UpdatedAt, "ClaimNext")

	orphan := enqueueWithStatus(t, q, store.JobKindIndex, store.JobStatusRunning, 2)
	pinUpdatedAt(t, db, "jobs", orphan.ID)
	failed, _, err := q.RecoverOrphans(ctx, []store.JobKind{store.JobKindIndex})
	require.NoError(t, err)
	require.Len(t, failed, 1)
	got, err = q.GetByID(ctx, orphan.ID)
	require.NoError(t, err)
	assert.Equal(t, got.UpdatedAt, failed[0].UpdatedAt, "RecoverOrphans")
}

// TestUpdates_WriteOneRow: a one-row store UPDATE is one row change. The
// triggers that used to maintain updated_at wrote every row twice.
func TestUpdates_WriteOneRow(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	// total_changes() is per connection, so pin the pool to one.
	db.SetMaxOpenConns(1)
	docs := NewDocuments(db)
	d := &store.Document{TenantID: "local", URL: "https://example.com/doc"}
	require.NoError(t, docs.Create(ctx, d))

	totalChanges := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT total_changes()`).Scan(&n))
		return n
	}
	before := totalChanges()
	require.NoError(t, docs.UpdateState(ctx, d.ID, store.DocStateFetched))
	assert.Equal(t, 1, totalChanges()-before)

	var triggers int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master
		WHERE type = 'trigger' AND name LIKE 'trg\_%\_updated\_at' ESCAPE '\'`).Scan(&triggers))
	assert.Zero(t, triggers)
}
