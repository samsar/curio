package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// The listing, count and metrics queries behind the debug endpoints. Rows are
// written with raw SQL where a test needs to pin updated_at or started_at:
// every UPDATE through the stores sets updated_at to now.

// insertDoc inserts a document with the given state and updated_at.
func insertDoc(t *testing.T, db *DB, tenantID, url string, state store.DocState, updatedAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	_, err := db.Exec(`INSERT INTO documents (id, tenant_id, url, state, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, tenantID, url, state, formatTime(updatedAt))
	require.NoError(t, err)
	return id
}

// attachExtraction gives a document a current extraction with markdownPath.
func attachExtraction(t *testing.T, db *DB, docID, markdownPath string) {
	t.Helper()
	ext := &store.DocumentExtraction{DocumentID: docID, Fetcher: "test", Status: store.ExtractionStatusOK,
		MarkdownPath: &markdownPath}
	require.NoError(t, NewExtractions(db).Create(context.Background(), ext))
	// A raw UPDATE, so the document keeps the updated_at its test pinned.
	_, err := db.Exec(`UPDATE documents SET current_extraction_id = ? WHERE id = ?`, ext.ID, docID)
	require.NoError(t, err)
}

// jobRow is a job inserted as-is, timestamps included.
type jobRow struct {
	tenantID  string
	kind      store.JobKind
	status    store.JobStatus
	docID     string // payload and column document_id; empty for a {} payload
	lastError string
	startedAt time.Time // zero means NULL
	updatedAt time.Time
}

func insertJobRow(t *testing.T, db *DB, j jobRow) string {
	t.Helper()
	id := uuid.NewString()
	payload := "{}"
	var docID any
	if j.docID != "" {
		payload = `{"document_id":"` + j.docID + `"}`
		docID = j.docID
	}
	var lastErr, startedAt any
	if j.lastError != "" {
		lastErr = j.lastError
	}
	if !j.startedAt.IsZero() {
		startedAt = formatTime(j.startedAt)
	}
	tenantID := j.tenantID
	if tenantID == "" {
		tenantID = "local"
	}
	_, err := db.Exec(`INSERT INTO jobs (id, tenant_id, kind, payload, document_id, status, last_error, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tenantID, j.kind, payload, docID, j.status, lastErr, startedAt, formatTime(j.updatedAt))
	require.NoError(t, err)
	return id
}

func TestDocuments_ListWithLastError(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	now := time.Now().UTC()

	fetched := insertDoc(t, db, "local", "https://example.com/fetched", store.DocStateFetched, now.Add(-1*time.Minute))
	attachExtraction(t, db, fetched, fetched+"/x.md")
	failed := insertDoc(t, db, "local", "https://example.com/failed", store.DocStateFailed, now.Add(-2*time.Minute))
	pending := insertDoc(t, db, "local", "https://example.com/pending", store.DocStatePending, now.Add(-3*time.Minute))
	insertDoc(t, db, "other", "https://example.com/theirs", store.DocStateFailed, now)

	insertJobRow(t, db, jobRow{kind: store.JobKindFetch, status: store.JobStatusFailed, docID: failed,
		lastError: "older failure", updatedAt: now.Add(-10 * time.Minute)})
	insertJobRow(t, db, jobRow{kind: store.JobKindIndex, status: store.JobStatusFailed, docID: failed,
		lastError: "newest failure", updatedAt: now.Add(-5 * time.Minute)})
	insertJobRow(t, db, jobRow{kind: store.JobKindFetch, status: store.JobStatusPending, docID: pending,
		lastError: "retrying", updatedAt: now})

	ids := func(items []store.DocumentWithError) []string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.ID
		}
		return out
	}

	all, err := docs.ListWithLastError(ctx, "local", store.ListDocumentsOpts{})
	require.NoError(t, err)
	assert.Equal(t, []string{fetched, failed, pending}, ids(all), "most recently updated first, this tenant only")
	assert.Equal(t, fetched+"/x.md", all[0].MarkdownPath)
	assert.Empty(t, all[0].LastError)
	assert.Equal(t, "newest failure", all[1].LastError, "the most recent failed job's error")
	assert.Empty(t, all[1].MarkdownPath)
	assert.Empty(t, all[2].LastError, "a pending job's error is not a failure yet")
	assert.Equal(t, store.DocStatePending, all[2].State)

	onlyFailed, err := docs.ListWithLastError(ctx, "local", store.ListDocumentsOpts{State: store.DocStateFailed})
	require.NoError(t, err)
	assert.Equal(t, []string{failed}, ids(onlyFailed))

	limited, err := docs.ListWithLastError(ctx, "local", store.ListDocumentsOpts{Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{fetched, failed}, ids(limited))
}

func TestDocuments_ListIDsWithContent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	now := time.Now().UTC()

	fetchedWith := insertDoc(t, db, "local", "https://example.com/a", store.DocStateFetched, now)
	attachExtraction(t, db, fetchedWith, "a.md")
	insertDoc(t, db, "local", "https://example.com/b", store.DocStateFetched, now)
	pendingWith := insertDoc(t, db, "local", "https://example.com/c", store.DocStatePending, now)
	attachExtraction(t, db, pendingWith, "c.md")
	insertDoc(t, db, "local", "https://example.com/d", store.DocStatePending, now)
	theirs := insertDoc(t, db, "other", "https://example.com/e", store.DocStateFetched, now)
	attachExtraction(t, db, theirs, "e.md")

	got, err := docs.ListIDsWithContent(ctx, "local", store.DocStateFetched)
	require.NoError(t, err)
	assert.Equal(t, []string{fetchedWith}, got)

	got, err = docs.ListIDsWithContent(ctx, "local", store.DocStatePending)
	require.NoError(t, err)
	assert.Equal(t, []string{pendingWith}, got)

	got, err = docs.ListIDsWithContent(ctx, "local", store.DocStateDead)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDocuments_CountByState(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := time.Now().UTC()
	for i, st := range []store.DocState{store.DocStateFetched, store.DocStateFetched, store.DocStateFailed, store.DocStateDead} {
		insertDoc(t, db, "local", fmt.Sprintf("https://example.com/%d", i), st, now)
	}
	insertDoc(t, db, "other", "https://example.com/theirs", store.DocStatePending, now)

	got, err := NewDocuments(db).CountByState(ctx, "local")
	require.NoError(t, err)
	assert.Equal(t, map[store.DocState]int{
		store.DocStateFetched: 2,
		store.DocStateFailed:  1,
		store.DocStateDead:    1,
	}, got)

	none, err := NewDocuments(db).CountByState(ctx, "nobody")
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestJobs_ListWithDoc(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	q := NewJobs(db)
	now := time.Now().UTC()

	docID := insertDoc(t, db, "local", "https://example.com/post", store.DocStateFetched, now)
	_, err := db.Exec(`UPDATE documents SET title = 'A Post' WHERE id = ?`, docID)
	require.NoError(t, err)
	attachExtraction(t, db, docID, docID+"/post.md")

	fetch := insertJobRow(t, db, jobRow{kind: store.JobKindFetch, status: store.JobStatusDone, docID: docID,
		updatedAt: now.Add(-3 * time.Minute)})
	index := insertJobRow(t, db, jobRow{kind: store.JobKindIndex, status: store.JobStatusFailed, docID: docID,
		lastError: "embed failed", updatedAt: now.Add(-2 * time.Minute)})
	cluster := insertJobRow(t, db, jobRow{kind: store.JobKindCluster, status: store.JobStatusDone,
		updatedAt: now.Add(-1 * time.Minute)})
	// A deleted document's jobs stay, with no document to join.
	goneDoc := insertDoc(t, db, "local", "https://example.com/gone", store.DocStateFailed, now)
	gone := insertJobRow(t, db, jobRow{kind: store.JobKindFetch, status: store.JobStatusFailed,
		docID: goneDoc, updatedAt: now.Add(-4 * time.Minute)})
	_, err = db.Exec(`DELETE FROM documents WHERE id = ?`, goneDoc)
	require.NoError(t, err)
	var goneDocID sql.NullString
	require.NoError(t, db.QueryRow(`SELECT document_id FROM jobs WHERE id = ?`, gone).Scan(&goneDocID))
	assert.False(t, goneDocID.Valid, "ON DELETE SET NULL")
	insertJobRow(t, db, jobRow{tenantID: "other", kind: store.JobKindFetch, status: store.JobStatusDone, updatedAt: now})

	ids := func(items []store.JobWithDoc) []string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.ID
		}
		return out
	}

	all, err := q.ListWithDoc(ctx, "local", store.ListJobsOpts{})
	require.NoError(t, err)
	require.Equal(t, []string{cluster, index, fetch, gone}, ids(all), "most recently updated first, this tenant only")
	for _, j := range all[1:3] {
		assert.Equal(t, "https://example.com/post", j.URL)
		assert.Equal(t, "A Post", j.Title)
		assert.Equal(t, docID+"/post.md", j.MarkdownPath)
	}
	require.NotNil(t, all[1].LastError)
	assert.Equal(t, "embed failed", *all[1].LastError)
	for _, j := range []store.JobWithDoc{all[0], all[3]} {
		assert.Empty(t, j.URL, "no document to join: %s", j.Kind)
		assert.Empty(t, j.Title)
		assert.Empty(t, j.MarkdownPath)
	}

	failed, err := q.ListWithDoc(ctx, "local", store.ListJobsOpts{Status: store.JobStatusFailed})
	require.NoError(t, err)
	assert.Equal(t, []string{index, gone}, ids(failed))

	fetches, err := q.ListWithDoc(ctx, "local", store.ListJobsOpts{Kind: store.JobKindFetch})
	require.NoError(t, err)
	assert.Equal(t, []string{fetch, gone}, ids(fetches))

	failedFetches, err := q.ListWithDoc(ctx, "local", store.ListJobsOpts{Status: store.JobStatusFailed, Kind: store.JobKindFetch})
	require.NoError(t, err)
	assert.Equal(t, []string{gone}, ids(failedFetches))

	limited, err := q.ListWithDoc(ctx, "local", store.ListJobsOpts{Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{cluster}, ids(limited))

	for _, listed := range all {
		got, err := q.GetWithDoc(ctx, "local", listed.ID)
		require.NoError(t, err)
		assert.Equal(t, listed, *got, "a job reads the same alone as in the list")
	}
	other := insertJobRow(t, db, jobRow{tenantID: "other", kind: store.JobKindFetch, status: store.JobStatusDone, updatedAt: now})
	for _, id := range []string{other, "no-such-job"} {
		_, err := q.GetWithDoc(ctx, "local", id)
		assert.ErrorIs(t, err, store.ErrNotFound, "%s: another tenant's job doesn't exist here", id)
	}
}

func TestJobs_CountByStatus(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := time.Now().UTC()
	for _, st := range []store.JobStatus{store.JobStatusPending, store.JobStatusPending, store.JobStatusRunning, store.JobStatusDone} {
		insertJobRow(t, db, jobRow{kind: store.JobKindFetch, status: st, updatedAt: now})
	}
	insertJobRow(t, db, jobRow{tenantID: "other", kind: store.JobKindFetch, status: store.JobStatusFailed, updatedAt: now})

	got, err := NewJobs(db).CountByStatus(ctx, "local")
	require.NoError(t, err)
	assert.Equal(t, map[store.JobStatus]int{
		store.JobStatusPending: 2,
		store.JobStatusRunning: 1,
		store.JobStatusDone:    1,
	}, got)
}

func TestJobs_MetricsByKind(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := time.Now().UTC()
	finishedAt := now.Add(-time.Minute)

	// Ten successful fetches taking 100ms, 200ms, ..., 1000ms.
	for i := 1; i <= 10; i++ {
		dur := time.Duration(i) * 100 * time.Millisecond
		insertJobRow(t, db, jobRow{kind: store.JobKindFetch, status: store.JobStatusDone,
			startedAt: finishedAt.Add(-dur), updatedAt: finishedAt})
	}
	// A failure counts, but its duration stays out of the percentiles.
	insertJobRow(t, db, jobRow{kind: store.JobKindFetch, status: store.JobStatusFailed,
		startedAt: finishedAt.Add(-time.Hour), updatedAt: finishedAt})
	// Finished before the window: not counted at all.
	insertJobRow(t, db, jobRow{kind: store.JobKindFetch, status: store.JobStatusDone,
		startedAt: now.Add(-3 * time.Hour), updatedAt: now.Add(-2 * time.Hour)})
	// Running now, whatever the window.
	insertJobRow(t, db, jobRow{kind: store.JobKindIndex, status: store.JobStatusRunning,
		startedAt: now.Add(-30 * time.Second), updatedAt: now.Add(-30 * time.Second)})
	insertJobRow(t, db, jobRow{kind: store.JobKindIndex, status: store.JobStatusRunning,
		startedAt: now.Add(-3 * time.Hour), updatedAt: now.Add(-3 * time.Hour)})
	insertJobRow(t, db, jobRow{tenantID: "other", kind: store.JobKindCluster, status: store.JobStatusDone,
		startedAt: finishedAt.Add(-time.Second), updatedAt: finishedAt})

	got, err := NewJobs(db).MetricsByKind(ctx, "local", time.Hour)
	require.NoError(t, err)
	require.Len(t, got, 2)

	fetch := got[0]
	assert.Equal(t, store.JobKindFetch, fetch.Kind, "sorted by kind")
	assert.Equal(t, 11, fetch.Count)
	assert.Equal(t, 1, fetch.Failed)
	assert.InDelta(t, 550, fetch.MeanMS, 1)
	assert.InDelta(t, 600, fetch.P50MS, 1)
	assert.InDelta(t, 1000, fetch.P95MS, 1)
	assert.InDelta(t, 1000, fetch.P99MS, 1)
	assert.Zero(t, fetch.Running)

	index := got[1]
	assert.Equal(t, store.JobKindIndex, index.Kind)
	assert.Zero(t, index.Count)
	assert.Equal(t, 2, index.Running)
	assert.InDelta(t, 3*60*60, index.OldestRunningSeconds, 5)

	wide, err := NewJobs(db).MetricsByKind(ctx, "local", 3*time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, wide)
	assert.Equal(t, 12, wide[0].Count, "a wider window takes in the older job")
}

func TestBookmarks_Count(t *testing.T) {
	ctx := context.Background()
	bms := NewBookmarks(newTestDB(t))
	for _, b := range []*store.Bookmark{
		{TenantID: "local", URL: "https://example.com/a", Source: store.SourceChrome},
		{TenantID: "local", URL: "https://example.com/a", Source: store.SourceSafari},
		{TenantID: "local", URL: "https://example.com/b", Source: store.SourceChrome},
		{TenantID: "other", URL: "https://example.com/a", Source: store.SourceChrome},
	} {
		b.SavedAt = time.Now().UTC()
		require.NoError(t, bms.Create(ctx, b))
	}

	n, err := bms.Count(ctx, "local")
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	n, err = bms.Count(ctx, "nobody")
	require.NoError(t, err)
	assert.Zero(t, n)
}
