package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samansartipi/curio/internal/store"
)

// ---------- DocumentStore ----------

func TestDocuments_UpsertAndGet(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	docs := NewDocuments(db)

	d := &store.Document{
		TenantID:    "local",
		URL:         "https://example.com/x",
		ContentType: store.ContentTypeArticle,
		State:       store.DocStatePending,
	}
	require.NoError(t, docs.Upsert(ctx, d))
	assert.NotEmpty(t, d.ID)
	assert.False(t, d.CreatedAt.IsZero())

	got, err := docs.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/x", got.URL)
	assert.Equal(t, store.DocStatePending, got.State)
}

func TestDocuments_UpsertIsIdempotentOnURL(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	docs := NewDocuments(db)

	d1 := &store.Document{TenantID: "local", URL: "https://x.com/", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Upsert(ctx, d1))

	title := "Updated"
	d2 := &store.Document{TenantID: "local", URL: "https://x.com/", ContentType: store.ContentTypeArticle, Title: &title}
	require.NoError(t, docs.Upsert(ctx, d2))

	// Same id should be returned for the (tenant, url) key.
	assert.Equal(t, d1.ID, d2.ID)

	got, err := docs.GetByURL(ctx, "local", "https://x.com/")
	require.NoError(t, err)
	require.NotNil(t, got.Title)
	assert.Equal(t, "Updated", *got.Title)
}

func TestDocuments_GetByID_NotFound(t *testing.T) {
	docs := NewDocuments(NewEphemeralDB(t))
	_, err := docs.GetByID(context.Background(), uuid.NewString())
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestDocuments_UpdateState(t *testing.T) {
	ctx := context.Background()
	docs := NewDocuments(NewEphemeralDB(t))
	d := &store.Document{TenantID: "local", URL: "https://example.com/y", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Upsert(ctx, d))

	require.NoError(t, docs.UpdateState(ctx, d.ID, store.DocStateFetched))
	got, _ := docs.GetByID(ctx, d.ID)
	assert.Equal(t, store.DocStateFetched, got.State)
}

func TestDocuments_UpdateState_Missing(t *testing.T) {
	docs := NewDocuments(NewEphemeralDB(t))
	err := docs.UpdateState(context.Background(), uuid.NewString(), store.DocStateFetched)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// ---------- ExtractionStore ----------

func TestExtractions_CreateAndList(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	docs := NewDocuments(db)
	exts := NewExtractions(db)

	d := &store.Document{TenantID: "local", URL: "https://example.com/z", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Upsert(ctx, d))

	mdPath := "z/extraction1.md"
	e1 := &store.DocumentExtraction{
		DocumentID:     d.ID,
		Fetcher:        "web2md",
		Status:         store.ExtractionStatusOK,
		MarkdownPath:   &mdPath,
		ExtractionMeta: json.RawMessage(`{"chars":1234}`),
	}
	require.NoError(t, exts.Create(ctx, e1))
	assert.NotEmpty(t, e1.ID)
	assert.False(t, e1.FetchedAt.IsZero())

	got, err := exts.GetByID(ctx, e1.ID)
	require.NoError(t, err)
	assert.Equal(t, "web2md", got.Fetcher)
	assert.JSONEq(t, `{"chars":1234}`, string(got.ExtractionMeta))

	// Second extraction
	e2 := &store.DocumentExtraction{
		DocumentID: d.ID, Fetcher: "jina", Status: store.ExtractionStatusOK,
	}
	require.NoError(t, exts.Create(ctx, e2))

	list, err := exts.ListByDocument(ctx, d.ID)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestDocuments_SetCurrentExtractionTriggerEnforced(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	docs := NewDocuments(db)

	d := &store.Document{TenantID: "local", URL: "https://example.com/trig", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Upsert(ctx, d))

	// Pointing at a missing extraction ID must be rejected by the trigger.
	err := docs.SetCurrentExtraction(ctx, d.ID, uuid.NewString())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "current_extraction_id references missing")
}

// ---------- BookmarkStore ----------

func TestBookmarks_CRUD(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	bms := NewBookmarks(db)

	folder := "/Tech/AI"
	b := &store.Bookmark{
		TenantID:   "local",
		URL:        "https://example.com/article",
		Source:     store.SourceManual,
		SavedAt:    time.Now().UTC(),
		FolderPath: &folder,
		Tags:       []string{"a", "b"},
	}
	require.NoError(t, bms.Create(ctx, b))
	assert.NotEmpty(t, b.ID)

	got, err := bms.GetByID(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, b.URL, got.URL)
	require.NotNil(t, got.FolderPath)
	assert.Equal(t, "/Tech/AI", *got.FolderPath)
	assert.Equal(t, []string{"a", "b"}, got.Tags)
}

func TestBookmarks_UniqueConflict(t *testing.T) {
	ctx := context.Background()
	bms := NewBookmarks(NewEphemeralDB(t))

	make := func() *store.Bookmark {
		return &store.Bookmark{
			TenantID: "local", URL: "https://example.com/dup",
			Source: store.SourceManual, SavedAt: time.Now().UTC(),
		}
	}
	require.NoError(t, bms.Create(ctx, make()))
	err := bms.Create(ctx, make())
	assert.ErrorIs(t, err, store.ErrConflict)
}

func TestBookmarks_ListFilters(t *testing.T) {
	ctx := context.Background()
	bms := NewBookmarks(NewEphemeralDB(t))

	mkAt := time.Now().UTC()
	for i, src := range []string{store.SourceChrome, store.SourceChrome, store.SourceSafari} {
		require.NoError(t, bms.Create(ctx, &store.Bookmark{
			TenantID: "local", URL: "https://example.com/" + string(rune('a'+i)),
			Source: src, SavedAt: mkAt,
		}))
	}

	chrome, err := bms.List(ctx, "local", store.ListBookmarksOpts{Source: store.SourceChrome})
	require.NoError(t, err)
	assert.Len(t, chrome, 2)

	safari, err := bms.List(ctx, "local", store.ListBookmarksOpts{Source: store.SourceSafari})
	require.NoError(t, err)
	assert.Len(t, safari, 1)
}

func TestBookmarks_LinkDocument(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	bms := NewBookmarks(db)
	docs := NewDocuments(db)

	d := &store.Document{TenantID: "local", URL: "https://example.com/link", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Upsert(ctx, d))

	b := &store.Bookmark{TenantID: "local", URL: "https://example.com/link", Source: store.SourceManual, SavedAt: time.Now().UTC()}
	require.NoError(t, bms.Create(ctx, b))

	require.NoError(t, bms.LinkDocument(ctx, b.ID, d.ID))
	got, _ := bms.GetByID(ctx, b.ID)
	require.NotNil(t, got.DocumentID)
	assert.Equal(t, d.ID, *got.DocumentID)
}

// ---------- JobQueue ----------

func TestJobs_EnqueueClaimDone(t *testing.T) {
	ctx := context.Background()
	q := NewJobs(NewEphemeralDB(t))

	j := &store.Job{TenantID: "local", Kind: store.JobKindFetch, Payload: json.RawMessage(`{"url":"x"}`)}
	require.NoError(t, q.Enqueue(ctx, j))

	claimed, err := q.ClaimNext(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, j.ID, claimed.ID)
	assert.Equal(t, store.JobStatusRunning, claimed.Status)
	assert.Equal(t, 1, claimed.Attempts)

	require.NoError(t, q.MarkDone(ctx, claimed.ID))
	got, _ := q.GetByID(ctx, claimed.ID)
	assert.Equal(t, store.JobStatusDone, got.Status)
}

func TestJobs_ClaimNext_FiltersByKind(t *testing.T) {
	ctx := context.Background()
	q := NewJobs(NewEphemeralDB(t))

	require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch}))
	require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindIndex}))

	// Only ask for index jobs.
	got, err := q.ClaimNext(ctx, []string{store.JobKindIndex})
	require.NoError(t, err)
	assert.Equal(t, store.JobKindIndex, got.Kind)
}

func TestJobs_ClaimNext_NoneRunnable(t *testing.T) {
	q := NewJobs(NewEphemeralDB(t))
	_, err := q.ClaimNext(context.Background(), nil)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestJobs_ClaimNext_RespectsRunAfter(t *testing.T) {
	ctx := context.Background()
	q := NewJobs(NewEphemeralDB(t))

	future := time.Now().UTC().Add(time.Hour)
	require.NoError(t, q.Enqueue(ctx, &store.Job{
		TenantID: "local", Kind: store.JobKindFetch, RunAfter: future,
	}))
	_, err := q.ClaimNext(ctx, nil)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestJobs_MarkFailed_RetryAndExhaust(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	q := NewJobs(db)
	q.MaxAttempts = 2 // make exhaustion fast

	require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch}))
	claimed, err := q.ClaimNext(ctx, nil)
	require.NoError(t, err)

	// First failure with retry → goes back to pending, attempts=1.
	require.NoError(t, q.MarkFailed(ctx, claimed.ID, "transient", true))
	got, _ := q.GetByID(ctx, claimed.ID)
	assert.Equal(t, store.JobStatusPending, got.Status)
	assert.Equal(t, 1, got.Attempts)
	require.NotNil(t, got.LastError)
	assert.Equal(t, "transient", *got.LastError)

	// Bypass the backoff window for the test by clearing run_after.
	_, err = db.ExecContext(ctx, `UPDATE jobs SET run_after = ? WHERE id = ?`, formatTime(time.Now().UTC()), claimed.ID)
	require.NoError(t, err)

	// Second attempt, fails again → status=failed because attempts (2) >= MaxAttempts (2).
	claimed2, err := q.ClaimNext(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, q.MarkFailed(ctx, claimed2.ID, "permanent", true))
	got, _ = q.GetByID(ctx, claimed2.ID)
	assert.Equal(t, store.JobStatusFailed, got.Status)
}

// TestJobs_ClaimNext_ConcurrentClaimOnce verifies the bug we'd otherwise
// only discover in M1 when the worker pool expands. Each pending job must
// be claimed by exactly one worker.
func TestJobs_ClaimNext_ConcurrentClaimOnce(t *testing.T) {
	ctx := context.Background()
	q := NewJobs(NewEphemeralDB(t))

	const nJobs = 20
	for i := 0; i < nJobs; i++ {
		require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch}))
	}

	const nWorkers = 8
	var (
		wg      sync.WaitGroup
		claimed sync.Map
		dups    atomic.Int32
		empties atomic.Int32
	)
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j, err := q.ClaimNext(ctx, nil)
				if errors.Is(err, store.ErrNotFound) {
					empties.Add(1)
					return
				}
				require.NoError(t, err)
				if _, loaded := claimed.LoadOrStore(j.ID, true); loaded {
					dups.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	got := 0
	claimed.Range(func(_, _ any) bool { got++; return true })
	assert.Equal(t, nJobs, got, "all jobs should be claimed exactly once")
	assert.Zero(t, dups.Load(), "no duplicate claims")
}
