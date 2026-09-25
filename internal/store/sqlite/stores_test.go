package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// ---------- DocumentStore ----------

func TestDocuments_CreateAndGet(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)

	d := &store.Document{TenantID: "local", URL: "https://example.com/x"}
	require.NoError(t, docs.Create(ctx, d))
	assert.NotEmpty(t, d.ID)
	assert.False(t, d.CreatedAt.IsZero())
	assert.False(t, d.UpdatedAt.IsZero())
	assert.Equal(t, store.DocStatePending, d.State, "defaulted on insert")
	assert.Equal(t, store.ContentTypeUnknown, d.ContentType, "defaulted on insert")

	got, err := docs.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/x", got.URL)
	assert.Equal(t, store.DocStatePending, got.State)
	assert.Equal(t, store.ContentTypeUnknown, got.ContentType)
	assert.Equal(t, d.CreatedAt, got.CreatedAt)

	title := "Given"
	explicit := &store.Document{ID: "doc-explicit", TenantID: "local", URL: "https://example.com/y",
		State: store.DocStateDead, ContentType: store.ContentTypeRepo, Title: &title}
	require.NoError(t, docs.Create(ctx, explicit))
	got, err = docs.GetByID(ctx, "doc-explicit")
	require.NoError(t, err)
	assert.Equal(t, store.DocStateDead, got.State)
	assert.Equal(t, store.ContentTypeRepo, got.ContentType)
	require.NotNil(t, got.Title)
	assert.Equal(t, "Given", *got.Title)
}

func TestDocuments_Create_Rejects(t *testing.T) {
	ctx := context.Background()
	docs := NewDocuments(newTestDB(t))
	first := &store.Document{TenantID: "local", URL: "https://x.com/"}
	require.NoError(t, docs.Create(ctx, first))

	dup := &store.Document{TenantID: "local", URL: "https://x.com/"}
	err := docs.Create(ctx, dup)
	require.ErrorIs(t, err, store.ErrConflict)
	assert.Empty(t, dup.ID, "a failed create leaves the input as it was")

	sameID := &store.Document{ID: first.ID, TenantID: "local", URL: "https://x.com/other"}
	require.ErrorIs(t, docs.Create(ctx, sameID), store.ErrConflict)

	otherTenant := &store.Document{TenantID: "other", URL: "https://x.com/"}
	require.NoError(t, docs.Create(ctx, otherTenant), "the URL is unique per tenant")

	extID := uuid.NewString()
	require.Error(t, docs.Create(ctx, &store.Document{TenantID: "local", URL: "https://x.com/e",
		CurrentExtractionID: &extID}))
	require.Error(t, docs.Create(ctx, &store.Document{TenantID: "local"}))
	require.Error(t, docs.Create(ctx, &store.Document{URL: "https://x.com/t"}))
}

// TestDocuments_ApplyFetch: the fetch-derived columns describe the current
// extraction, so a second fetch that finds no author clears the first
// fetch's author instead of keeping it.
func TestDocuments_ApplyFetch(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs, exts := NewDocuments(db), NewExtractions(db)

	words := 1200
	d := &store.Document{TenantID: "local", URL: "https://example.com/post", WordCount: &words,
		State: store.DocStateFetched}
	require.NoError(t, docs.Create(ctx, d))
	newExtraction := func() string {
		e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK}
		require.NoError(t, exts.Create(ctx, e))
		return e.ID
	}
	str := func(s string) *string { return &s }
	published := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)

	first := newExtraction()
	require.NoError(t, docs.ApplyFetch(ctx, d.ID, store.FetchedMetadata{
		ExtractionID: first, ContentType: store.ContentTypeArticle,
		URLCanonical: str("https://example.com/post-canonical"), Title: str("Post"),
		Author: str("Ada"), Language: str("en"), PublishedAt: &published,
	}))
	got, err := docs.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStatePending, got.State, "pending until indexed")
	assert.Equal(t, store.ContentTypeArticle, got.ContentType)
	assert.Equal(t, first, *got.CurrentExtractionID)
	assert.Equal(t, "https://example.com/post-canonical", *got.URLCanonical)
	assert.Equal(t, "Ada", *got.Author)
	assert.Equal(t, published, *got.PublishedAt)

	second := newExtraction()
	require.NoError(t, docs.ApplyFetch(ctx, d.ID, store.FetchedMetadata{
		ExtractionID: second, ContentType: store.ContentTypeVideo, Title: str("Post, again"),
	}))
	got, err = docs.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, second, *got.CurrentExtractionID)
	assert.Equal(t, store.ContentTypeVideo, got.ContentType)
	assert.Equal(t, "Post, again", *got.Title)
	assert.Nil(t, got.URLCanonical, "cleared")
	assert.Nil(t, got.Author, "cleared")
	assert.Nil(t, got.Language, "cleared")
	assert.Nil(t, got.PublishedAt, "cleared")
	require.NotNil(t, got.WordCount)
	assert.Equal(t, 1200, *got.WordCount, "not a fetch-derived column")
	assert.Equal(t, d.CreatedAt, got.CreatedAt)

	err = docs.ApplyFetch(ctx, uuid.NewString(), store.FetchedMetadata{ExtractionID: second, ContentType: store.ContentTypeArticle})
	require.ErrorIs(t, err, store.ErrNotFound)

	err = docs.ApplyFetch(ctx, d.ID, store.FetchedMetadata{ExtractionID: uuid.NewString(), ContentType: store.ContentTypeArticle})
	require.ErrorContains(t, err, "current_extraction_id references missing")
	got, err = docs.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, second, *got.CurrentExtractionID, "a rejected fetch changes nothing")
}

func TestDocuments_GetByID_NotFound(t *testing.T) {
	docs := NewDocuments(newTestDB(t))
	_, err := docs.GetByID(context.Background(), uuid.NewString())
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestDocuments_UpdateState(t *testing.T) {
	ctx := context.Background()
	docs := NewDocuments(newTestDB(t))
	d := &store.Document{TenantID: "local", URL: "https://example.com/y", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Create(ctx, d))

	require.NoError(t, docs.UpdateState(ctx, d.ID, store.DocStateFetched))
	got, _ := docs.GetByID(ctx, d.ID)
	assert.Equal(t, store.DocStateFetched, got.State)
}

func TestDocuments_UpdateState_Missing(t *testing.T) {
	docs := NewDocuments(newTestDB(t))
	err := docs.UpdateState(context.Background(), uuid.NewString(), store.DocStateFetched)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// ---------- ExtractionStore ----------

func TestExtractions_CreateAndList(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	exts := NewExtractions(db)

	d := &store.Document{TenantID: "local", URL: "https://example.com/z", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Create(ctx, d))

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

// TestExtractions_Create_FetchedAt: the extraction comes back with the
// fetched_at that was stored, whether the caller gave one or the column
// defaulted it.
func TestExtractions_Create_FetchedAt(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	exts := NewExtractions(db)
	d := &store.Document{TenantID: "local", URL: "https://example.com/fetched-at"}
	require.NoError(t, NewDocuments(db).Create(ctx, d))

	given := time.Date(2024, 5, 1, 12, 30, 0, 123456789, time.UTC)
	cases := []struct {
		name      string
		fetchedAt time.Time
		want      func(t *testing.T, got time.Time)
	}{
		{"given", given, func(t *testing.T, got time.Time) {
			assert.Equal(t, given.Truncate(time.Millisecond), got, "stored to the millisecond")
		}},
		{"defaulted", time.Time{}, func(t *testing.T, got time.Time) {
			assert.WithinDuration(t, time.Now(), got, time.Minute)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
				FetchedAt: tc.fetchedAt}
			require.NoError(t, exts.Create(ctx, e))
			tc.want(t, e.FetchedAt)
			stored, err := exts.GetByID(ctx, e.ID)
			require.NoError(t, err)
			assert.Equal(t, stored.FetchedAt, e.FetchedAt)
		})
	}
}

func TestDocuments_SetCurrentExtractionTriggerEnforced(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)

	d := &store.Document{TenantID: "local", URL: "https://example.com/trig", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Create(ctx, d))

	// Pointing at a missing extraction ID must be rejected by the trigger.
	err := docs.SetCurrentExtraction(ctx, d.ID, uuid.NewString())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "current_extraction_id references missing")
}

// ---------- BookmarkStore ----------

func TestBookmarks_CRUD(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
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
	assert.False(t, b.CreatedAt.IsZero())
	assert.Equal(t, got.CreatedAt, b.CreatedAt, "Create returns the stored timestamps")
	assert.Equal(t, got.UpdatedAt, b.UpdatedAt)
	require.NotNil(t, got.FolderPath)
	assert.Equal(t, "/Tech/AI", *got.FolderPath)
	assert.Equal(t, []string{"a", "b"}, got.Tags)
}

func TestBookmarks_UniqueConflict(t *testing.T) {
	ctx := context.Background()
	bms := NewBookmarks(newTestDB(t))

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
	bms := NewBookmarks(newTestDB(t))

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
	db := newTestDB(t)
	bms := NewBookmarks(db)
	docs := NewDocuments(db)

	d := &store.Document{TenantID: "local", URL: "https://example.com/link", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Create(ctx, d))

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
	q := NewJobs(newTestDB(t))

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
	q := NewJobs(newTestDB(t))

	require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch}))
	require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindIndex}))

	// Only ask for index jobs.
	got, err := q.ClaimNext(ctx, []store.JobKind{store.JobKindIndex})
	require.NoError(t, err)
	assert.Equal(t, store.JobKindIndex, got.Kind)
}

// TestJobs_ClaimNext_Order: jobs are claimed in the order they became
// runnable, and in the order they were created when that ties.
func TestJobs_ClaimNext_Order(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	q := NewJobs(db)
	base := time.Now().UTC().Add(-time.Hour)
	insert := func(id string, runAfter, createdAt time.Time) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO jobs (id, tenant_id, kind, payload, run_after, created_at)
			VALUES (?, 'local', 'fetch', '{}', ?, ?)`, id, formatTime(runAfter), formatTime(createdAt))
		require.NoError(t, err)
	}
	// A retry that came due before a newer job was created goes first,
	// though it was created long before either.
	insert("due-later", base.Add(2*time.Minute), base.Add(2*time.Minute))
	insert("due-first", base.Add(time.Minute), base.Add(3*time.Minute))
	// Due together: the older one goes first.
	insert("tie-newer", base.Add(5*time.Minute), base.Add(5*time.Minute))
	insert("tie-older", base.Add(5*time.Minute), base.Add(4*time.Minute))

	want := []string{"due-first", "due-later", "tie-older", "tie-newer"}
	got := make([]string, 0, len(want))
	for range want {
		j, err := q.ClaimNext(ctx, []store.JobKind{store.JobKindFetch})
		require.NoError(t, err)
		got = append(got, j.ID)
	}
	assert.Equal(t, want, got)
}

func TestJobs_ClaimNext_NoneRunnable(t *testing.T) {
	q := NewJobs(newTestDB(t))
	_, err := q.ClaimNext(context.Background(), nil)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestJobs_ClaimNext_RespectsRunAfter(t *testing.T) {
	ctx := context.Background()
	q := NewJobs(newTestDB(t))

	future := time.Now().UTC().Add(time.Hour)
	require.NoError(t, q.Enqueue(ctx, &store.Job{
		TenantID: "local", Kind: store.JobKindFetch, RunAfter: future,
	}))
	_, err := q.ClaimNext(ctx, nil)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestJobs_Enqueue_DocumentID: jobs.document_id is derived from the
// payload at insert, which is stored as given, and a payload naming a
// missing document is refused.
func TestJobs_Enqueue_DocumentID(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	q := NewJobs(db)
	doc := &store.Document{TenantID: "local", URL: "https://example.com/doc"}
	require.NoError(t, NewDocuments(db).Create(ctx, doc))

	cases := []struct {
		name    string
		payload string
		want    sql.NullString
	}{
		{"document job", `{"document_id":"` + doc.ID + `"}`, sql.NullString{String: doc.ID, Valid: true}},
		{"extra fields", `{"reason": "model-swap", "document_id":"` + doc.ID + `"}`, sql.NullString{String: doc.ID, Valid: true}},
		{"no document", `{}`, sql.NullString{}},
		{"not an object", `[1, 2]`, sql.NullString{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &store.Job{TenantID: "local", Kind: store.JobKindIndex, Payload: json.RawMessage(tc.payload)}
			require.NoError(t, q.Enqueue(ctx, j))
			var got sql.NullString
			require.NoError(t, db.QueryRow(`SELECT document_id FROM jobs WHERE id = ?`, j.ID).Scan(&got))
			assert.Equal(t, tc.want, got)
			stored, err := q.GetByID(ctx, j.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.payload, string(stored.Payload), "the payload is stored byte for byte")
		})
	}

	missing, err := store.NewDocumentJob("local", store.JobKindFetch, uuid.NewString())
	require.NoError(t, err)
	err = q.Enqueue(ctx, missing)
	require.ErrorIs(t, err, store.ErrNotFound)
	assert.Contains(t, err.Error(), "fetch job")
	_, err = q.GetByID(ctx, missing.ID)
	assert.ErrorIs(t, err, store.ErrNotFound, "nothing was inserted")
}

func TestJobs_MarkFailed_RetryAndExhaust(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	q := NewJobs(db)
	q.MaxAttempts = 2 // make exhaustion fast

	require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch}))
	claimed, err := q.ClaimNext(ctx, nil)
	require.NoError(t, err)

	// First failure with retry → goes back to pending, attempts=1.
	permanent, err := q.MarkFailed(ctx, claimed.ID, "transient", true)
	require.NoError(t, err)
	assert.False(t, permanent, "first attempt isn't permanent")
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
	permanent, err = q.MarkFailed(ctx, claimed2.ID, "permanent", true)
	require.NoError(t, err)
	assert.True(t, permanent, "exhausted attempts should be reported permanent")
	got, _ = q.GetByID(ctx, claimed2.ID)
	assert.Equal(t, store.JobStatusFailed, got.Status)
}

// TestRetryBackoff: 30s doubled per failed attempt, capped at an hour,
// however many attempts.
func TestRetryBackoff(t *testing.T) {
	s := time.Second
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{1, 60 * s}, {2, 120 * s}, {3, 240 * s}, {4, 480 * s}, {5, 960 * s}, {6, 1920 * s},
		{7, time.Hour}, {8, time.Hour}, {9, time.Hour}, {10, time.Hour},
		{64, time.Hour}, {math.MaxInt, time.Hour},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, retryBackoff(tc.attempts), "attempts %d", tc.attempts)
	}
}

// TestJobs_ClaimNext_ConcurrentClaimOnce verifies the bug we'd otherwise
// only discover in M1 when the worker pool expands. Each pending job must
// be claimed by exactly one worker.
func TestJobs_ClaimNext_ConcurrentClaimOnce(t *testing.T) {
	ctx := context.Background()
	q := NewJobs(newTestDB(t))

	const nJobs = 20
	for i := 0; i < nJobs; i++ {
		require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch}))
	}

	// Workers report errors instead of asserting: require's FailNow must run
	// on the test goroutine.
	const nWorkers = 8
	var (
		wg      sync.WaitGroup
		claimed sync.Map
		dups    atomic.Int32
		empties atomic.Int32
		errs    = make(chan error, nWorkers)
	)
	for range nWorkers {
		wg.Go(func() {
			for {
				j, err := q.ClaimNext(ctx, nil)
				if errors.Is(err, store.ErrNotFound) {
					empties.Add(1)
					return
				}
				if err != nil {
					errs <- err
					return
				}
				if _, loaded := claimed.LoadOrStore(j.ID, true); loaded {
					dups.Add(1)
				}
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err, "claim")
	}
	got := 0
	claimed.Range(func(_, _ any) bool { got++; return true })
	assert.Equal(t, nJobs, got, "all jobs should be claimed exactly once")
	assert.Zero(t, dups.Load(), "no duplicate claims")
	assert.EqualValues(t, nWorkers, empties.Load(), "every worker stopped on an empty queue")
}

// enqueueWithStatus inserts a job directly in the given status, as if it had
// already gone through the queue.
func enqueueWithStatus(t *testing.T, q *Jobs, kind store.JobKind, status store.JobStatus, attempts int) *store.Job {
	t.Helper()
	j := &store.Job{TenantID: "local", Kind: kind, Status: status, Attempts: attempts}
	require.NoError(t, q.Enqueue(context.Background(), j))
	return j
}

func startedAt(t *testing.T, db *DB, id string) sql.NullString {
	t.Helper()
	var s sql.NullString
	require.NoError(t, db.QueryRow(`SELECT started_at FROM jobs WHERE id = ?`, id).Scan(&s))
	return s
}

func TestJobs_Requeue(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	q := NewJobs(db)

	require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch}))
	claimed, err := q.ClaimNext(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, 1, claimed.Attempts)
	require.True(t, startedAt(t, db, claimed.ID).Valid)

	require.NoError(t, q.Requeue(ctx, claimed.ID))

	got, err := q.GetByID(ctx, claimed.ID)
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusPending, got.Status)
	assert.Zero(t, got.Attempts, "the interrupted claim is refunded")
	assert.False(t, got.RunAfter.After(time.Now()))
	assert.False(t, startedAt(t, db, claimed.ID).Valid)

	// A requeued job is claimable straight away.
	again, err := q.ClaimNext(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, claimed.ID, again.ID)
}

func TestJobs_Requeue_NeverBelowZeroAttempts(t *testing.T) {
	ctx := context.Background()
	q := NewJobs(newTestDB(t))
	j := enqueueWithStatus(t, q, store.JobKindFetch, store.JobStatusRunning, 0)

	require.NoError(t, q.Requeue(ctx, j.ID))
	got, err := q.GetByID(ctx, j.ID)
	require.NoError(t, err)
	assert.Zero(t, got.Attempts)
}

// TestJobs_TransitionsRequireRunning: MarkDone, MarkFailed and Requeue only
// move a running job. Anything else is reported and left as it was.
func TestJobs_TransitionsRequireRunning(t *testing.T) {
	transitions := map[string]func(q *Jobs, id string) error{
		"MarkDone": func(q *Jobs, id string) error { return q.MarkDone(context.Background(), id) },
		"MarkFailed": func(q *Jobs, id string) error {
			_, err := q.MarkFailed(context.Background(), id, "boom", true)
			return err
		},
		"Requeue": func(q *Jobs, id string) error { return q.Requeue(context.Background(), id) },
	}
	for name, transition := range transitions {
		t.Run(name, func(t *testing.T) {
			for _, status := range []store.JobStatus{store.JobStatusPending, store.JobStatusDone, store.JobStatusFailed} {
				q := NewJobs(newTestDB(t))
				j := enqueueWithStatus(t, q, store.JobKindFetch, status, 1)

				err := transition(q, j.ID)
				require.ErrorIs(t, err, store.ErrNotRunning, "status %s", status)

				got, err := q.GetByID(context.Background(), j.ID)
				require.NoError(t, err)
				assert.Equal(t, status, got.Status)
				assert.Equal(t, 1, got.Attempts)
				assert.Nil(t, got.LastError)
			}

			q := NewJobs(newTestDB(t))
			assert.ErrorIs(t, transition(q, uuid.NewString()), store.ErrNotFound)
		})
	}
}

func TestJobs_RecoverOrphans(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	q := NewJobs(db)
	q.MaxAttempts = 3

	exhausted := enqueueWithStatus(t, q, store.JobKindFetch, store.JobStatusRunning, 3)
	orphan := enqueueWithStatus(t, q, store.JobKindIndex, store.JobStatusRunning, 1)
	otherKind := enqueueWithStatus(t, q, store.JobKindCluster, store.JobStatusRunning, 1)
	pending := enqueueWithStatus(t, q, store.JobKindFetch, store.JobStatusPending, 0)
	_, err := db.ExecContext(ctx, `UPDATE jobs SET started_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE status = 'running'`)
	require.NoError(t, err)

	failed, requeued, err := q.RecoverOrphans(ctx, []store.JobKind{store.JobKindFetch, store.JobKindIndex})
	require.NoError(t, err)
	assert.Equal(t, 1, requeued)
	require.Len(t, failed, 1)
	assert.Equal(t, exhausted.ID, failed[0].ID)
	assert.Equal(t, store.JobStatusFailed, failed[0].Status)

	got, err := q.GetByID(ctx, exhausted.ID)
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusFailed, got.Status)
	require.NotNil(t, got.LastError)
	assert.Contains(t, *got.LastError, "daemon exited")

	got, err = q.GetByID(ctx, orphan.ID)
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusPending, got.Status)
	assert.Equal(t, 1, got.Attempts, "the orphaned attempt stays spent")
	assert.False(t, got.RunAfter.After(time.Now()))
	assert.False(t, startedAt(t, db, orphan.ID).Valid)

	got, err = q.GetByID(ctx, otherKind.ID)
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusRunning, got.Status, "other kinds are not this caller's to recover")

	got, err = q.GetByID(ctx, pending.ID)
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusPending, got.Status)
	assert.Zero(t, got.Attempts)
}

func TestJobs_RecoverOrphans_RequiresKinds(t *testing.T) {
	_, _, err := NewJobs(newTestDB(t)).RecoverOrphans(context.Background(), nil)
	assert.Error(t, err)
}

// ---------- retention ----------

// TestJobs_PruneOlderThan_KeepsLiveWork: pruning removes finished jobs last
// updated before the cutoff. A pending or running job is work in flight,
// however old; deleting it would strand its document in pending with
// nothing left to move it on.
func TestJobs_PruneOlderThan_KeepsLiveWork(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	q := NewJobs(db)
	docs := NewDocuments(db)
	cutoff := time.Now().UTC().Add(-time.Hour)

	doc := &store.Document{TenantID: "local", URL: "https://example.com/queued"}
	require.NoError(t, docs.Create(ctx, doc))
	queued := &store.Job{TenantID: "local", Kind: store.JobKindFetch,
		Payload: json.RawMessage(`{"document_id":"` + doc.ID + `"}`)}
	require.NoError(t, q.Enqueue(ctx, queued))

	type job struct {
		status store.JobStatus
		old    bool
	}
	byJob := map[job]*store.Job{}
	for _, status := range []store.JobStatus{store.JobStatusPending, store.JobStatusRunning, store.JobStatusDone, store.JobStatusFailed} {
		for _, old := range []bool{true, false} {
			byJob[job{status, old}] = enqueueWithStatus(t, q, store.JobKindIndex, status, 1)
		}
	}
	backdate := func(id string) {
		_, err := db.Exec(`UPDATE jobs SET updated_at = ? WHERE id = ?`, formatTime(cutoff.Add(-time.Minute)), id)
		require.NoError(t, err)
	}
	backdate(queued.ID)
	for key, j := range byJob {
		if key.old {
			backdate(j.ID)
		}
	}

	n, err := q.PruneOlderThan(ctx, "local", cutoff)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)

	for key, j := range byJob {
		_, err := q.GetByID(ctx, j.ID)
		if key.old && key.status.IsFinished() {
			assert.ErrorIs(t, err, store.ErrNotFound, "%+v", key)
		} else {
			assert.NoError(t, err, "%+v", key)
		}
	}
	_, err = q.GetByID(ctx, queued.ID)
	assert.NoError(t, err, "the document's only fetch job survives")
}

func TestJobs_DeleteByStatus_FinishedOnly(t *testing.T) {
	ctx := context.Background()
	q := NewJobs(newTestDB(t))

	byStatus := map[store.JobStatus]*store.Job{}
	for _, status := range []store.JobStatus{store.JobStatusPending, store.JobStatusRunning, store.JobStatusDone, store.JobStatusFailed} {
		byStatus[status] = enqueueWithStatus(t, q, store.JobKindFetch, status, 1)
	}

	for _, status := range []store.JobStatus{store.JobStatusPending, store.JobStatusRunning, "bogus", ""} {
		n, err := q.DeleteByStatus(ctx, "local", status)
		require.Error(t, err, "status %q", status)
		assert.Zero(t, n)
	}
	for _, j := range byStatus {
		_, err := q.GetByID(ctx, j.ID)
		require.NoError(t, err, "a refused delete removes nothing")
	}

	n, err := q.DeleteByStatus(ctx, "local", store.JobStatusDone)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	_, err = q.GetByID(ctx, byStatus[store.JobStatusDone].ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// ---------- refetch ----------

func seedDoc(t *testing.T, docs *Documents, url string, state store.DocState) *store.Document {
	t.Helper()
	d := &store.Document{TenantID: "local", URL: url, State: state}
	require.NoError(t, docs.Create(context.Background(), d))
	return d
}

func docState(t *testing.T, docs *Documents, id string) store.DocState {
	t.Helper()
	d, err := docs.GetByID(context.Background(), id)
	require.NoError(t, err)
	return d.State
}

func fetchJobsFor(t *testing.T, q *Jobs, docID string) []*store.Job {
	t.Helper()
	all, err := q.ListWithDoc(context.Background(), "local", store.ListJobsOpts{Kind: store.JobKindFetch, Limit: 1000})
	require.NoError(t, err)
	var out []*store.Job
	for _, j := range all {
		var p store.DocumentJobPayload
		require.NoError(t, json.Unmarshal(j.Payload, &p))
		if p.DocumentID == docID {
			out = append(out, j.Job)
		}
	}
	return out
}

// failJobInserts makes every INSERT into jobs abort. The trigger is
// persistent, not TEMP: a TEMP trigger exists only on the connection that
// created it, and the pool has several.
func failJobInserts(t *testing.T, db *DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TRIGGER t_fail BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	require.NoError(t, err)
}

func countRows(t *testing.T, db *DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func TestDocuments_RequeueFetch(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs, q := NewDocuments(db), NewJobs(db)
	d := seedDoc(t, docs, "https://example.com/a", store.DocStateFailed)

	job, err := docs.RequeueFetch(ctx, "local", d.ID)
	require.NoError(t, err)

	assert.Equal(t, store.DocStatePending, docState(t, docs, d.ID))
	jobs := fetchJobsFor(t, q, d.ID)
	require.Len(t, jobs, 1)
	got := jobs[0]
	assert.Equal(t, job.ID, got.ID)
	assert.Equal(t, "local", got.TenantID)
	assert.Equal(t, store.JobStatusPending, got.Status)
	assert.Zero(t, got.Attempts)
	assert.JSONEq(t, `{"document_id":"`+d.ID+`"}`, string(got.Payload))
	assert.False(t, job.CreatedAt.IsZero(), "the returned job carries its stored timestamps")
}

func TestDocuments_RequeueFetch_NotFound(t *testing.T) {
	db := newTestDB(t)
	docs := NewDocuments(db)
	other := &store.Document{TenantID: "other", URL: "https://example.com/theirs"}
	require.NoError(t, docs.Create(context.Background(), other))

	for _, id := range []string{uuid.NewString(), other.ID} {
		_, err := docs.RequeueFetch(context.Background(), "local", id)
		assert.ErrorIs(t, err, store.ErrNotFound)
	}
	assert.Zero(t, countRows(t, db, "jobs"))
	assert.Equal(t, store.DocStatePending, docState(t, docs, other.ID), "another tenant's document is untouched")
}

func TestDocuments_RequeueFetch_Atomic(t *testing.T) {
	db := newTestDB(t)
	docs := NewDocuments(db)
	d := seedDoc(t, docs, "https://example.com/a", store.DocStateFailed)
	failJobInserts(t, db)

	_, err := docs.RequeueFetch(context.Background(), "local", d.ID)
	require.ErrorContains(t, err, "injected")
	assert.Equal(t, store.DocStateFailed, docState(t, docs, d.ID), "the state reset rolled back with the insert")
	assert.Zero(t, countRows(t, db, "jobs"))
}

func TestDocuments_RequeueFetchByStates(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs, q := NewDocuments(db), NewJobs(db)
	byState := map[store.DocState]*store.Document{}
	for _, st := range []store.DocState{store.DocStatePending, store.DocStateFetched, store.DocStateFailed, store.DocStateDead} {
		byState[st] = seedDoc(t, docs, "https://example.com/"+string(st), st)
	}
	other := &store.Document{TenantID: "other", URL: "https://example.com/theirs", State: store.DocStateFailed}
	require.NoError(t, docs.Create(ctx, other))

	n, err := docs.RequeueFetchByStates(ctx, "local",
		[]store.DocState{store.DocStatePending, store.DocStateFetched, store.DocStateFailed})
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, 3, countRows(t, db, "jobs"))

	for st, d := range byState {
		if st == store.DocStateDead {
			assert.Equal(t, store.DocStateDead, docState(t, docs, d.ID), "dead is left out unless asked for")
			assert.Empty(t, fetchJobsFor(t, q, d.ID))
			continue
		}
		assert.Equal(t, store.DocStatePending, docState(t, docs, d.ID), st)
		assert.Len(t, fetchJobsFor(t, q, d.ID), 1, st)
	}
	assert.Equal(t, store.DocStateFailed, docState(t, docs, other.ID), "another tenant's document is untouched")

	n, err = docs.RequeueFetchByStates(ctx, "local", []store.DocState{store.DocStateDead})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, store.DocStatePending, docState(t, docs, byState[store.DocStateDead].ID))
}

func TestDocuments_RequeueFetchByStates_Atomic(t *testing.T) {
	db := newTestDB(t)
	docs := NewDocuments(db)
	a := seedDoc(t, docs, "https://example.com/a", store.DocStateFailed)
	b := seedDoc(t, docs, "https://example.com/b", store.DocStateFetched)
	failJobInserts(t, db)

	_, err := docs.RequeueFetchByStates(context.Background(), "local",
		[]store.DocState{store.DocStateFetched, store.DocStateFailed})
	require.ErrorContains(t, err, "injected")
	assert.Equal(t, store.DocStateFailed, docState(t, docs, a.ID))
	assert.Equal(t, store.DocStateFetched, docState(t, docs, b.ID))
	assert.Zero(t, countRows(t, db, "jobs"))
}
