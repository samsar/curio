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
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
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

func statusIs(status store.JobStatus) func(*store.Job) bool {
	return func(j *store.Job) bool { return j.Status == status }
}

// TestWorker_JobFinishedDuringShutdownIsDone: a handler that completes after
// shutdown has begun still gets its success recorded, so the row isn't left
// running and the job isn't run again on the next start.
func TestWorker_JobFinishedDuringShutdownIsDone(t *testing.T) {
	q := sqlitestore.NewJobs(sqlitetest.NewDB(t))
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
			require.NoError(t, deps.Documents.Create(ctx, doc))
			job := docJob(t, store.JobKindFetch, doc.ID)
			require.NoError(t, deps.Queue.Enqueue(ctx, job))

			// One earlier real failure, so there is an attempt and a
			// last_error that must survive the interruption.
			_, err := deps.Queue.ClaimNext(ctx, nil)
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
	q := sqlitestore.NewJobs(sqlitetest.NewDB(t))
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
			q := sqlitestore.NewJobs(sqlitetest.NewDB(t))
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

	enqueueRunning := func(kind store.JobKind, attempts int, docID string) *store.Job {
		j := docJob(t, kind, docID)
		j.Status, j.Attempts = store.JobStatusRunning, attempts
		require.NoError(t, deps.Queue.Enqueue(ctx, j))
		return j
	}
	newDoc := func(url string) *store.Document {
		d := &store.Document{TenantID: "local", URL: url, ContentType: store.ContentTypeArticle}
		require.NoError(t, deps.Documents.Create(ctx, d))
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

// shutdownAfterRecovery is a queue whose RecoverOrphans is followed at once
// by a shutdown signal, as when `curio daemon stop` catches a daemon that is
// still starting.
type shutdownAfterRecovery struct {
	store.JobQueue
	shutdown context.CancelFunc
}

func (q shutdownAfterRecovery) RecoverOrphans(ctx context.Context, kinds []store.JobKind) ([]*store.Job, int, error) {
	defer q.shutdown()
	return q.JobQueue.RecoverOrphans(ctx, kinds)
}

// TestWorker_RecoverOrphans_HooksOutliveShutdown: once an exhausted orphan is
// committed as failed, its document is failed too, even if shutdown begins
// in between; otherwise the document would stay pending with no job.
func TestWorker_RecoverOrphans_HooksOutliveShutdown(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	doc := &store.Document{TenantID: "local", URL: "https://example.com/crashes-the-daemon",
		ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(context.Background(), doc))
	orphan := docJob(t, store.JobKindFetch, doc.ID)
	orphan.Status, orphan.Attempts = store.JobStatusRunning, 5
	require.NoError(t, deps.Queue.Enqueue(context.Background(), orphan))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := NewWorker(shutdownAfterRecovery{JobQueue: deps.Queue, shutdown: cancel}, WorkerOptions{Log: quietLog})
	w.Register(store.JobKindFetch, FetchHandler(deps))
	w.OnPermanentFailure(store.JobKindFetch, MarkDocFailed(deps))
	require.NoError(t, w.RecoverOrphans(ctx))

	require.Error(t, ctx.Err(), "shutdown had begun when the hook ran")
	assert.Equal(t, store.JobStatusFailed, getJob(t, deps.Queue, orphan.ID).Status)
	got, err := deps.Documents.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStateFailed, got.State)
}

func TestBackoff(t *testing.T) {
	ms := time.Millisecond
	want := []time.Duration{50 * ms, 100 * ms, 200 * ms, 400 * ms, 800 * ms, time.Second, time.Second}
	b := backoff{initial: 50 * time.Millisecond, max: time.Second}
	got := make([]time.Duration, 0, len(want))
	for range want {
		got = append(got, b.next())
	}
	assert.Equal(t, want, got)

	fresh := backoff{initial: 50 * time.Millisecond, max: time.Second}
	again := fresh
	again.next()
	assert.Equal(t, 50*time.Millisecond, fresh.next(), "a copy has its own state")
}

// TestRetryBookkeeping: a failed queue write is retried until it lands,
// except when retrying can't help, and gives up when its context expires.
func TestRetryBookkeeping(t *testing.T) {
	busy := errors.New("database is locked")
	cases := []struct {
		name      string
		errs      []error // returned by successive attempts; nil after they run out
		wantCalls int
		wantErr   error
	}{
		{"succeeds at once", nil, 1, nil},
		{"succeeds on the third attempt", []error{busy, busy}, 3, nil},
		{"not running", []error{fmt.Errorf("job j: %w", store.ErrNotRunning)}, 1, store.ErrNotRunning},
		{"not found", []error{store.ErrNotFound}, 1, store.ErrNotFound},
		{"permanent", []error{fmt.Errorf("%w: panic", ErrPermanent)}, 1, ErrPermanent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWorker(nil, WorkerOptions{Log: quietLog})
			w.retryDelays = backoff{initial: time.Microsecond, max: time.Microsecond}
			calls := 0
			err := w.retryBookkeeping(context.Background(), quietLog, "write", func(context.Context) error {
				calls++
				if calls <= len(tc.errs) {
					return tc.errs[calls-1]
				}
				return nil
			})
			assert.Equal(t, tc.wantCalls, calls)
			if tc.wantErr == nil {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, tc.wantErr)
			}
		})
	}

	t.Run("gives up when the context expires", func(t *testing.T) {
		w := NewWorker(nil, WorkerOptions{Log: quietLog})
		w.retryDelays = backoff{initial: time.Hour, max: time.Hour}
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		err := w.retryBookkeeping(ctx, quietLog, "write", func(context.Context) error {
			calls++
			cancel()
			return busy
		})
		assert.ErrorIs(t, err, busy, "the write's error, not the context's")
		assert.Equal(t, 1, calls, "no attempt after the context expired")
	})
}

// failingMarkDone is a queue whose MarkDone fails with fails[i] on attempt
// i and then passes through.
type failingMarkDone struct {
	store.JobQueue
	fails []error
	calls atomic.Int32
}

func (q *failingMarkDone) MarkDone(ctx context.Context, id string) error {
	n := int(q.calls.Add(1))
	if n <= len(q.fails) {
		return q.fails[n-1]
	}
	return q.JobQueue.MarkDone(ctx, id)
}

// TestWorker_MarkDoneRetried: a job whose MarkDone fails on a busy database
// is not left running; one that isn't running any more is left alone.
func TestWorker_MarkDoneRetried(t *testing.T) {
	busy := errors.New("database is locked")
	cases := []struct {
		name       string
		fails      []error
		wantCalls  int32
		wantStatus store.JobStatus
	}{
		{"busy twice", []error{busy, busy}, 3, store.JobStatusDone},
		{"not running", []error{store.ErrNotRunning}, 1, store.JobStatusRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &failingMarkDone{JobQueue: sqlitestore.NewJobs(sqlitetest.NewDB(t)), fails: tc.fails}
			job := &store.Job{TenantID: "local", Kind: store.JobKindSummarize}
			require.NoError(t, q.Enqueue(context.Background(), job))

			handled := make(chan struct{})
			w := NewWorker(q, WorkerOptions{PollInterval: time.Hour, Log: quietLog})
			w.retryDelays = backoff{initial: time.Microsecond, max: time.Microsecond}
			w.Register(store.JobKindSummarize, func(context.Context, *store.Job) error {
				close(handled)
				return nil
			})
			stop := startWorker(t, w)
			<-handled
			require.Eventually(t, func() bool { return q.calls.Load() >= tc.wantCalls },
				5*time.Second, time.Millisecond)
			stop() // Run returns only after the job's bookkeeping.

			assert.Equal(t, tc.wantCalls, q.calls.Load())
			assert.Equal(t, tc.wantStatus, getJob(t, q, job.ID).Status)
		})
	}
}

// countingQueue counts claims, and reports the first claim that finds
// nothing, when the worker has gone idle.
type countingQueue struct {
	store.JobQueue
	claims atomic.Int32
	idle   chan struct{}
}

func newCountingQueue(t *testing.T) *countingQueue {
	return &countingQueue{JobQueue: sqlitestore.NewJobs(sqlitetest.NewDB(t)), idle: make(chan struct{}, 1)}
}

func (q *countingQueue) ClaimNext(ctx context.Context, kinds []store.JobKind) (*store.Job, error) {
	j, err := q.JobQueue.ClaimNext(ctx, kinds)
	q.claims.Add(1)
	if errors.Is(err, store.ErrNotFound) {
		select {
		case q.idle <- struct{}{}:
		default:
		}
	}
	return j, err
}

// fetchWorker is a fetch-only worker whose handler reports each job.
func fetchWorker(q store.JobQueue, opts WorkerOptions) (*Worker, <-chan *store.Job) {
	handled := make(chan *store.Job, 10)
	opts.Log = quietLog
	w := NewWorker(q, opts)
	w.Register(store.JobKindFetch, func(_ context.Context, j *store.Job) error {
		handled <- j
		return nil
	})
	return w, handled
}

// TestWorker_WakesOnEnqueue: with polling slowed to ten minutes, only the
// queue's signal can explain a job enqueued after the worker went idle
// being claimed at once.
func TestWorker_WakesOnEnqueue(t *testing.T) {
	q := newCountingQueue(t)
	w, handled := fetchWorker(q, WorkerOptions{PollInterval: 10 * time.Minute, MaxPollInterval: 10 * time.Minute})
	stop := startWorker(t, w)
	defer stop()
	<-q.idle

	job := &store.Job{TenantID: "local", Kind: store.JobKindFetch}
	require.NoError(t, q.Enqueue(context.Background(), job))
	select {
	case got := <-handled:
		assert.Equal(t, job.ID, got.ID)
	case <-time.After(time.Second):
		t.Fatal("the enqueued job was not claimed")
	}
}

// TestWorker_IgnoresOtherKinds: an index job doesn't wake a fetch worker,
// which would only make a claim that takes the write lock for nothing.
func TestWorker_IgnoresOtherKinds(t *testing.T) {
	q := newCountingQueue(t)
	w, _ := fetchWorker(q, WorkerOptions{PollInterval: 10 * time.Minute, MaxPollInterval: 10 * time.Minute})
	stop := startWorker(t, w)
	defer stop()
	<-q.idle
	claims := q.claims.Load()

	require.NoError(t, q.Enqueue(context.Background(), &store.Job{TenantID: "local", Kind: store.JobKindIndex}))
	assert.Never(t, func() bool { return q.claims.Load() > claims }, 100*time.Millisecond, 5*time.Millisecond)
}

// TestWorker_IdlePollsBackOff: an idle worker polls less and less often,
// up to MaxPollInterval, instead of every PollInterval.
func TestWorker_IdlePollsBackOff(t *testing.T) {
	q := newCountingQueue(t)
	w, _ := fetchWorker(q, WorkerOptions{PollInterval: 2 * time.Millisecond, MaxPollInterval: 16 * time.Millisecond})
	stop := startWorker(t, w)
	<-time.After(300 * time.Millisecond)
	stop()

	// Polling every 2ms would be about 150 claims; backing off to 16ms,
	// about 20.
	claims := q.claims.Load()
	assert.LessOrEqual(t, claims, int32(40))
	assert.GreaterOrEqual(t, claims, int32(5), "it still polls")
}

// TestWorker_PollsForJobsComingDue: a job that isn't runnable yet raises
// no signal when it comes due; the idle poll finds it.
func TestWorker_PollsForJobsComingDue(t *testing.T) {
	q := newCountingQueue(t)
	w, handled := fetchWorker(q, WorkerOptions{PollInterval: 10 * time.Millisecond, MaxPollInterval: 20 * time.Millisecond})
	// run_after is stored to the millisecond.
	due := time.Now().Add(100 * time.Millisecond).Truncate(time.Millisecond)
	job := &store.Job{TenantID: "local", Kind: store.JobKindFetch, RunAfter: due}
	require.NoError(t, q.Enqueue(context.Background(), job))

	stop := startWorker(t, w)
	defer stop()
	select {
	case got := <-handled:
		assert.Equal(t, job.ID, got.ID)
		assert.False(t, time.Now().Before(due), "claimed before it was due")
	case <-time.After(5 * time.Second):
		t.Fatal("the job was not claimed once due")
	}
}
