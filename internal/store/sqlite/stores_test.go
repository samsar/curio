package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func TestDocuments_UpsertAndGet(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
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
	db := newTestDB(t)
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
	docs := NewDocuments(newTestDB(t))
	_, err := docs.GetByID(context.Background(), uuid.NewString())
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestDocuments_UpdateState(t *testing.T) {
	ctx := context.Background()
	docs := NewDocuments(newTestDB(t))
	d := &store.Document{TenantID: "local", URL: "https://example.com/y", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Upsert(ctx, d))

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
	db := newTestDB(t)
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

// TestJobs_PruneOlderThan_KeepsLiveWork: pruning removes finished jobs only.
// A pending or running job is work in flight; deleting it would strand its
// document in pending with nothing left to move it on.
func TestJobs_PruneOlderThan_KeepsLiveWork(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	q := NewJobs(db)
	docs := NewDocuments(db)

	doc := &store.Document{TenantID: "local", URL: "https://example.com/queued"}
	require.NoError(t, docs.Upsert(ctx, doc))
	queued := &store.Job{TenantID: "local", Kind: store.JobKindFetch,
		Payload: json.RawMessage(`{"document_id":"` + doc.ID + `"}`)}
	require.NoError(t, q.Enqueue(ctx, queued))

	byStatus := map[store.JobStatus]*store.Job{}
	for _, status := range []store.JobStatus{store.JobStatusPending, store.JobStatusRunning, store.JobStatusDone, store.JobStatusFailed} {
		byStatus[status] = enqueueWithStatus(t, q, store.JobKindIndex, status, 1)
	}

	// A cutoff in the future makes every row "old": updated_at can't be
	// backdated, the AFTER UPDATE trigger resets it.
	n, err := q.PruneOlderThan(ctx, "local", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)

	for status, j := range byStatus {
		_, err := q.GetByID(ctx, j.ID)
		if status.IsFinished() {
			assert.ErrorIs(t, err, store.ErrNotFound, status)
		} else {
			assert.NoError(t, err, status)
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
	require.NoError(t, docs.Upsert(context.Background(), d))
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
		var p struct {
			DocumentID string `json:"document_id"`
		}
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
	require.NoError(t, docs.Upsert(context.Background(), other))

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
	require.NoError(t, docs.Upsert(ctx, other))

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
