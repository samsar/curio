package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
)

var quietLog = slog.New(slog.DiscardHandler)

// startWorker runs w in the background and returns a function that cancels
// it and waits for Run to return.
func startWorker(t *testing.T, w *Worker) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return func() {
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	}
}

func getJob(t *testing.T, q store.JobQueue, id string) *store.Job {
	t.Helper()
	j, err := q.GetByID(context.Background(), id)
	require.NoError(t, err)
	return j
}

// waitForJob polls until cond holds for the job's current row. Lookup
// failures go to the per-attempt collector: require on t from the polling
// goroutine couldn't stop the test, only hide the error until the timeout.
func waitForJob(t *testing.T, q store.JobQueue, id string, cond func(*store.Job) bool) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		j, err := q.GetByID(context.Background(), id)
		require.NoError(c, err)
		assert.True(c, cond(j), "job %s: status %s", id, j.Status)
	}, 5*time.Second, 10*time.Millisecond)
}

func statusIs(status string) func(*store.Job) bool {
	return func(j *store.Job) bool { return j.Status == status }
}

// TestWorker_JobFinishedDuringShutdownIsDone: a handler that completes after
// shutdown has begun still gets its success recorded. Recording it on the
// already-cancelled worker context used to fail with context.Canceled and
// leave the row running, to be run again on the next start.
func TestWorker_JobFinishedDuringShutdownIsDone(t *testing.T) {
	q := sqlitestore.NewJobs(sqlitestore.NewEphemeralDB(t))
	job := &store.Job{TenantID: "local", Kind: store.JobKindSummarize}
	require.NoError(t, q.Enqueue(context.Background(), job))

	started := make(chan struct{})
	w := NewWorker(q, WorkerOptions{PollInterval: 10 * time.Millisecond, Log: quietLog})
	w.Register(store.JobKindSummarize, func(ctx context.Context, _ *store.Job) error {
		close(started)
		<-ctx.Done()
		return nil
	})

	stop := startWorker(t, w)
	<-started
	assert.Equal(t, []string{job.ID}, w.InFlight())
	stop()

	assert.Equal(t, store.JobStatusDone, getJob(t, q, job.ID).Status)
	assert.Empty(t, w.InFlight())
}

// TestWorker_JobInterruptedByShutdownIsRequeued: whatever a handler returns
// once shutdown has begun is a symptom of the shutdown, so the job goes back
// to pending with its attempt refunded, no permanent-failure hook runs, and
// its document is left alone.
func TestWorker_JobInterruptedByShutdownIsRequeued(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"context error", context.Canceled},
		{"killed subprocess", errors.New("web2md: signal: killed")},
		{"permanent error", fmt.Errorf("%w: document vanished", ErrPermanent)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, db, _ := newTestDeps(t)
			ctx := context.Background()

			doc := &store.Document{TenantID: "local", URL: "https://example.com/x", ContentType: store.ContentTypeArticle}
			require.NoError(t, deps.Documents.Upsert(ctx, doc))
			payload, err := json.Marshal(FetchPayload{DocumentID: doc.ID})
			require.NoError(t, err)
			job := &store.Job{TenantID: "local", Kind: store.JobKindFetch, Payload: payload}
			require.NoError(t, deps.Queue.Enqueue(ctx, job))

			// One earlier real failure, so there is an attempt and a
			// last_error that must survive the interruption.
			_, err = deps.Queue.ClaimNext(ctx, nil)
			require.NoError(t, err)
			_, err = deps.Queue.MarkFailed(ctx, job.ID, "earlier failure", true)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE jobs SET run_after = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`, job.ID)
			require.NoError(t, err)

			var hookCalls atomic.Int32
			started := make(chan struct{})
			w := NewWorker(deps.Queue, WorkerOptions{PollInterval: 10 * time.Millisecond, Log: quietLog})
			w.Register(store.JobKindFetch, func(ctx context.Context, _ *store.Job) error {
				close(started)
				<-ctx.Done()
				return tc.err
			})
			w.OnPermanentFailure(store.JobKindFetch, func(ctx context.Context, j *store.Job, cause error) error {
				hookCalls.Add(1)
				return MarkDocFailed(deps)(ctx, j, cause)
			})

			stop := startWorker(t, w)
			<-started
			stop()

			got := getJob(t, deps.Queue, job.ID)
			assert.Equal(t, store.JobStatusPending, got.Status)
			assert.Equal(t, 1, got.Attempts, "the interrupted attempt is refunded")
			require.NotNil(t, got.LastError)
			assert.Equal(t, "earlier failure", *got.LastError)
			assert.False(t, got.RunAfter.After(time.Now()), "runnable again right away")
			var startedAt sql.NullString
			require.NoError(t, db.QueryRow(`SELECT started_at FROM jobs WHERE id = ?`, job.ID).Scan(&startedAt))
			assert.False(t, startedAt.Valid)

			assert.Zero(t, hookCalls.Load())
			gotDoc, err := deps.Documents.GetByID(ctx, doc.ID)
			require.NoError(t, err)
			assert.Equal(t, store.DocStatePending, gotDoc.State)
		})
	}
}

// TestWorker_RetryableFailureConsumesAttempt pins the live-context path: a
// retryable error backs off with the attempt spent and no hook.
func TestWorker_RetryableFailureConsumesAttempt(t *testing.T) {
	q := sqlitestore.NewJobs(sqlitestore.NewEphemeralDB(t))
	job := &store.Job{TenantID: "local", Kind: store.JobKindSummarize}
	require.NoError(t, q.Enqueue(context.Background(), job))

	var hookCalls atomic.Int32
	w := NewWorker(q, WorkerOptions{PollInterval: 10 * time.Millisecond, Log: quietLog})
	w.Register(store.JobKindSummarize, func(context.Context, *store.Job) error { return errors.New("transient") })
	w.OnPermanentFailure(store.JobKindSummarize, func(context.Context, *store.Job, error) error {
		hookCalls.Add(1)
		return nil
	})

	stop := startWorker(t, w)
	waitForJob(t, q, job.ID, func(j *store.Job) bool { return j.LastError != nil })
	stop()

	got := getJob(t, q, job.ID)
	assert.Equal(t, store.JobStatusPending, got.Status)
	assert.Equal(t, 1, got.Attempts)
	assert.True(t, got.RunAfter.After(time.Now()), "retry is backed off")
	assert.Zero(t, hookCalls.Load())
}

// TestWorker_PanicIsContained: a panicking handler or hook fails its job
// permanently instead of crashing the daemon, and the worker carries on
// with the next job.
func TestWorker_PanicIsContained(t *testing.T) {
	cases := []struct {
		name        string
		handler     HandlerFunc
		hook        func()
		wantLastErr string
	}{
		{
			name: "handler panics",
			handler: func(context.Context, *store.Job) error {
				panic("boom")
			},
			hook:        func() {},
			wantLastErr: "panic: boom",
		},
		{
			name: "hook panics",
			handler: func(context.Context, *store.Job) error {
				return fmt.Errorf("%w: bad input", ErrPermanent)
			},
			hook:        func() { panic("hook boom") },
			wantLastErr: "permanent failure: bad input",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := sqlitestore.NewJobs(sqlitestore.NewEphemeralDB(t))
			ctx := context.Background()
			bad := &store.Job{TenantID: "local", Kind: store.JobKindSummarize, Payload: json.RawMessage(`{"bad":true}`)}
			good := &store.Job{TenantID: "local", Kind: store.JobKindSummarize, Payload: json.RawMessage(`{"bad":false}`)}
			require.NoError(t, q.Enqueue(ctx, bad))
			require.NoError(t, q.Enqueue(ctx, good))

			var hookedJobs []string
			w := NewWorker(q, WorkerOptions{PollInterval: 10 * time.Millisecond, Log: quietLog})
			w.Register(store.JobKindSummarize, func(ctx context.Context, j *store.Job) error {
				if j.ID == bad.ID {
					return tc.handler(ctx, j)
				}
				return nil
			})
			w.OnPermanentFailure(store.JobKindSummarize, func(_ context.Context, j *store.Job, _ error) error {
				hookedJobs = append(hookedJobs, j.ID)
				tc.hook()
				return nil
			})

			stop := startWorker(t, w)
			waitForJob(t, q, bad.ID, statusIs(store.JobStatusFailed))
			waitForJob(t, q, good.ID, statusIs(store.JobStatusDone))
			stop()

			gotBad := getJob(t, q, bad.ID)
			require.NotNil(t, gotBad.LastError)
			assert.True(t, strings.HasPrefix(*gotBad.LastError, tc.wantLastErr), *gotBad.LastError)
			assert.Equal(t, 1, gotBad.Attempts, "a panic is permanent, not retried")
			assert.Equal(t, []string{bad.ID}, hookedJobs)
		})
	}
}

// TestWorker_RecoverOrphans: an orphan with no attempts left fails and its
// document fails with it; other orphans of the worker's kinds are requeued
// with their attempt kept; other kinds are untouched.
func TestWorker_RecoverOrphans(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()

	enqueueRunning := func(kind string, attempts int, docID string) *store.Job {
		payload, err := json.Marshal(FetchPayload{DocumentID: docID})
		require.NoError(t, err)
		j := &store.Job{TenantID: "local", Kind: kind, Payload: payload,
			Status: store.JobStatusRunning, Attempts: attempts}
		require.NoError(t, deps.Queue.Enqueue(ctx, j))
		return j
	}
	newDoc := func(url string) *store.Document {
		d := &store.Document{TenantID: "local", URL: url, ContentType: store.ContentTypeArticle}
		require.NoError(t, deps.Documents.Upsert(ctx, d))
		return d
	}

	exhaustedDoc := newDoc("https://example.com/crashes-the-daemon")
	retryDoc := newDoc("https://example.com/unlucky")
	exhausted := enqueueRunning(store.JobKindFetch, 5, exhaustedDoc.ID)
	retry := enqueueRunning(store.JobKindFetch, 2, retryDoc.ID)
	otherKind := enqueueRunning(store.JobKindIndex, 5, retryDoc.ID)

	w := NewWorker(deps.Queue, WorkerOptions{Log: quietLog})
	w.Register(store.JobKindFetch, FetchHandler(deps))
	w.OnPermanentFailure(store.JobKindFetch, MarkDocFailed(deps))
	require.NoError(t, w.RecoverOrphans(ctx))

	assert.Equal(t, store.JobStatusFailed, getJob(t, deps.Queue, exhausted.ID).Status)
	gotDoc, err := deps.Documents.GetByID(ctx, exhaustedDoc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStateFailed, gotDoc.State)

	gotRetry := getJob(t, deps.Queue, retry.ID)
	assert.Equal(t, store.JobStatusPending, gotRetry.Status)
	assert.Equal(t, 2, gotRetry.Attempts, "an orphan keeps the attempt it used")
	gotDoc, err = deps.Documents.GetByID(ctx, retryDoc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStatePending, gotDoc.State)

	assert.Equal(t, store.JobStatusRunning, getJob(t, deps.Queue, otherKind.ID).Status)
}
