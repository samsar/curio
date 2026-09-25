// Package jobs runs the background work loop.
//
// A Worker claims one job at a time from the JobQueue, dispatches to a
// kind-specific HandlerFunc, and records the outcome. It wakes when the
// queue signals a new job of its kinds and otherwise polls, less often the
// longer it stays idle. The daemon runs a pool of goroutines per Worker;
// the claim-once semantics in store/sqlite/jobs.go make that safe.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/samsar/curio/internal/store"
)

// HandlerFunc executes one job. Return nil for success; return an error for
// failure. The worker decides retry vs. permanent failure based on whether
// the error is wrapped with ErrPermanent.
type HandlerFunc func(ctx context.Context, job *store.Job) error

// PermFailHook runs after a job reaches terminal failure (ErrPermanent or
// retries exhausted). cause is the handler error from the final attempt —
// hooks can inspect its chain (errors.Is) to pick kind-specific cleanup,
// e.g. marking a document dead vs failed.
type PermFailHook func(ctx context.Context, job *store.Job, cause error) error

// ErrPermanent wraps errors that should NOT be retried (bad input, missing
// rows, etc.). Transient failures (network, locked DB) should NOT use this
// wrapper — the worker will retry them with exponential backoff via the
// JobQueue's MarkFailed.
var ErrPermanent = errors.New("permanent failure")

// errOrphanExhausted is the cause handed to permanent-failure hooks for jobs
// that a previous daemon left running with no attempts to spare.
var errOrphanExhausted = fmt.Errorf("%w: the daemon exited mid-job and no attempts are left", ErrPermanent)

// bookkeepingTimeout bounds each queue write the worker makes, retries
// included. It outlasts SQLite's 5s busy_timeout so a write that has to wait
// for the lock still lands, and it is the only limit: those writes run
// detached from the worker's context, so shutdown can't abort them halfway.
const bookkeepingTimeout = 10 * time.Second

// bookkeepingRetry spaces the attempts of a failed bookkeeping write.
var bookkeepingRetry = backoff{initial: 50 * time.Millisecond, max: time.Second}

// Worker claims jobs from the queue and dispatches them.
type Worker struct {
	queue       store.JobQueue
	handlers    map[store.JobKind]HandlerFunc
	onPermFail  map[store.JobKind]PermFailHook
	idleDelays  backoff // between polls while there is nothing to claim
	log         *slog.Logger
	retryDelays backoff // between attempts of a failed bookkeeping write

	inFlight sync.Map // job ID → struct{}, across every goroutine running this Worker
}

// WorkerOptions tunes the loop.
type WorkerOptions struct {
	// PollInterval is the first wait after a claim that finds nothing. Each
	// idle poll after it doubles the wait, up to MaxPollInterval. Default
	// 500ms.
	PollInterval time.Duration
	// MaxPollInterval caps the wait between idle polls: it bounds how late a
	// job the queue doesn't signal (a retry coming due, a job from another
	// process) is noticed. Default 5s, and never below PollInterval.
	MaxPollInterval time.Duration
	Log             *slog.Logger // default slog.Default()
}

func NewWorker(q store.JobQueue, opts WorkerOptions) *Worker {
	poll := opts.PollInterval
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	maxPoll := opts.MaxPollInterval
	if maxPoll <= 0 {
		maxPoll = 5 * time.Second
	}
	w := &Worker{
		queue:       q,
		handlers:    map[store.JobKind]HandlerFunc{},
		onPermFail:  map[store.JobKind]PermFailHook{},
		idleDelays:  backoff{initial: poll, max: max(maxPoll, poll)},
		log:         opts.Log,
		retryDelays: bookkeepingRetry,
	}
	if w.log == nil {
		w.log = slog.Default()
	}
	return w
}

// Register attaches a handler for a kind. Overwrites if called twice for
// the same kind — caller's responsibility to not do that.
func (w *Worker) Register(kind store.JobKind, h HandlerFunc) {
	w.handlers[kind] = h
}

// OnPermanentFailure attaches a hook that fires after MarkFailed reports a
// job has hit terminal-failed state (retries exhausted, or wrapped with
// ErrPermanent). Use to clean up associated state, e.g., transition a
// parent document to state=failed or state=dead based on the cause.
//
// A hook that fails is retried like the queue writes (see finish), unless
// it returns an error wrapping ErrPermanent, so it must be idempotent. One
// that still fails is logged; it doesn't re-fail the job.
func (w *Worker) OnPermanentFailure(kind store.JobKind, h PermFailHook) {
	w.onPermFail[kind] = h
}

// RecoverOrphans settles jobs of this worker's kinds that a previous daemon
// left running (see store.JobQueue.RecoverOrphans), running the
// permanent-failure hook for each one that has no attempts left so its
// document ends up failed rather than pending forever. Call it once at
// startup, while the caller is the only daemon for the database and before
// any goroutine runs this Worker.
func (w *Worker) RecoverOrphans(ctx context.Context) error {
	kinds := w.kinds()
	failed, requeued, err := w.queue.RecoverOrphans(ctx, kinds)
	if err != nil {
		return fmt.Errorf("recover orphaned %v jobs: %w", kinds, err)
	}
	if requeued > 0 || len(failed) > 0 {
		w.log.Info("recovered jobs orphaned by the previous daemon",
			"kinds", kinds, "requeued", requeued, "failed", len(failed))
	}
	// Those jobs are committed as failed, so their hooks have to run even if
	// shutdown begins now: a document skipped here stays pending with no job.
	for _, job := range failed {
		bctx, cancel := bookkeepingContext(ctx)
		w.runPermFailHook(bctx, w.jobLog(job), job, errOrphanExhausted)
		cancel()
	}
	return nil
}

// InFlight returns the IDs of jobs this Worker is running right now.
func (w *Worker) InFlight() []string {
	var ids []string
	w.inFlight.Range(func(id, _ any) bool {
		ids = append(ids, id.(string))
		return true
	})
	slices.Sort(ids)
	return ids
}

// Run loops until ctx is cancelled. Returns ctx.Err() on shutdown.
//
// It drains all available work, then waits until the queue signals a job of
// its kinds or the idle poll comes due. Every claim takes SQLite's write
// lock, even one that finds nothing, so idle polls back off from
// PollInterval to MaxPollInterval; a claimed job resets them, and a failed
// claim backs off the same way. Polls still find the jobs no signal
// announces: retries whose run_after comes due, and jobs enqueued by
// another process.
//
// Cancelling ctx stops new claims and is passed to the running handler; the
// job's outcome is still recorded, and a job the shutdown interrupted goes
// back to the queue (see finish).
func (w *Worker) Run(ctx context.Context) error {
	kinds := w.kinds()
	w.log.Info("worker started", "poll_interval", w.idleDelays.initial,
		"max_poll_interval", w.idleDelays.max, "kinds", kinds)

	idle := w.idleDelays
	for {
		// Taken before the drain, so a job enqueued after its last claim
		// found nothing still ends the wait below.
		wake := w.queue.Enqueued(kinds)
		for w.tryOne(ctx, kinds) {
			idle = w.idleDelays
		}

		timer := time.NewTimer(idle.next())
		select {
		case <-ctx.Done():
			timer.Stop()
			w.log.Info("worker stopping")
			return ctx.Err()
		case <-wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// tryOne claims a job of kinds and dispatches it. Returns true if a job was
// handled (success or failure), false if the queue was empty, the claim
// failed, or ctx is done.
func (w *Worker) tryOne(ctx context.Context, kinds []store.JobKind) bool {
	if ctx.Err() != nil {
		return false
	}

	// The claim runs detached from ctx: cancelling mid-statement can report
	// failure for an UPDATE that has already committed, which would leave
	// the job running with nobody working on it.
	cctx, cancel := bookkeepingContext(ctx)
	job, err := w.queue.ClaimNext(cctx, kinds)
	cancel()
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		w.log.Error("claim failed", "err", err)
		return false
	}

	w.inFlight.Store(job.ID, struct{}{})
	defer w.inFlight.Delete(job.ID)

	log := w.jobLog(job)
	log.Info("job claimed")

	start := time.Now()
	err = w.invoke(ctx, log, job)
	w.finish(ctx, log, job, err, time.Since(start))
	return true
}

// invoke runs the job's handler, turning a panic into a permanent failure so
// one bad job can't take the daemon down with it.
func (w *Worker) invoke(ctx context.Context, log *slog.Logger, job *store.Job) error {
	h := w.handlers[job.Kind]
	if h == nil {
		return fmt.Errorf("%w: no handler registered for kind %q", ErrPermanent, job.Kind)
	}
	return recoverPanic(log, "job handler", func() error { return h(ctx, job) })
}

// finish records a handled job's outcome. ctx is the worker's context; the
// writes themselves run detached from it, so a shutdown that begins while a
// handler runs still gets that job's outcome recorded.
func (w *Worker) finish(ctx context.Context, log *slog.Logger, job *store.Job, err error, dur time.Duration) {
	bctx, cancel := bookkeepingContext(ctx)
	defer cancel()

	switch {
	case err == nil:
		if merr := w.retryBookkeeping(bctx, log, "mark done", func(ctx context.Context) error {
			return w.queue.MarkDone(ctx, job.ID)
		}); merr != nil {
			log.Error("mark done failed", "err", merr)
			return
		}
		log.Info("job done", "duration_ms", dur.Milliseconds())

	case ctx.Err() != nil:
		// The shutdown interrupted the handler, so whatever it returned
		// (context.Canceled, a killed subprocess, even ErrPermanent) says
		// nothing about the job. Put it back and give the attempt back.
		if rerr := w.retryBookkeeping(bctx, log, "requeue", func(ctx context.Context) error {
			return w.queue.Requeue(ctx, job.ID)
		}); rerr != nil {
			log.Error("requeue after shutdown failed", "err", rerr, "handler_err", err)
			return
		}
		log.Info("job interrupted by shutdown; requeued", "handler_err", err, "duration_ms", dur.Milliseconds())

	default:
		w.fail(bctx, log, job, err, dur)
	}
}

func (w *Worker) fail(ctx context.Context, log *slog.Logger, job *store.Job, cause error, dur time.Duration) {
	retry := !errors.Is(cause, ErrPermanent)
	log.Warn("job failed", "err", cause, "retry", retry, "duration_ms", dur.Milliseconds())
	var permanent bool
	err := w.retryBookkeeping(ctx, log, "mark failed", func(ctx context.Context) error {
		var err error
		permanent, err = w.queue.MarkFailed(ctx, job.ID, cause.Error(), retry)
		return err
	})
	if err != nil {
		log.Error("mark failed errored", "err", err)
		return
	}
	if permanent {
		w.runPermFailHook(ctx, log, job, cause)
	}
}

func (w *Worker) runPermFailHook(ctx context.Context, log *slog.Logger, job *store.Job, cause error) {
	hook := w.onPermFail[job.Kind]
	if hook == nil {
		return
	}
	err := w.retryBookkeeping(ctx, log, "permanent-failure hook", func(ctx context.Context) error {
		return recoverPanic(log, "permanent-failure hook", func() error { return hook(ctx, job, cause) })
	})
	if err != nil {
		log.Error("permanent-failure hook errored", "err", err)
	}
}

// retryBookkeeping runs write until it succeeds, fails with an error no
// retry can fix, or ctx, a bookkeeping context, expires; it returns write's
// last error. A queue write that never lands leaves its job running until
// the next restart. Transactions take the write lock at BEGIN, so a write
// that fails has usually found the lock held past busy_timeout, and a later
// attempt gets through. Repeating one is safe: the queue's transitions
// only move a running job, and hooks must be idempotent.
func (w *Worker) retryBookkeeping(ctx context.Context, log *slog.Logger, what string,
	write func(context.Context) error) error {
	delays := w.retryDelays
	for {
		err := write(ctx)
		if err == nil || !retryable(err) {
			return err
		}
		delay := delays.next()
		log.Warn(what+" failed; retrying", "err", err, "retry_in", delay)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
	}
}

// retryable reports whether a failed bookkeeping write might succeed if
// made again. Not when the job isn't in the state the write expects, or
// isn't there at all, or the error says it is permanent.
func retryable(err error) bool {
	return !errors.Is(err, store.ErrNotRunning) &&
		!errors.Is(err, store.ErrNotFound) &&
		!errors.Is(err, ErrPermanent)
}

// recoverPanic runs fn, converting a panic into an ErrPermanent error whose
// message starts with "panic:". The stack is logged, since the error can't
// carry it usefully.
func recoverPanic(log *slog.Logger, what string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error(what+" panicked", "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v (%w)", r, ErrPermanent)
		}
	}()
	return fn()
}

// bookkeepingContext detaches from the worker's context (keeping its values)
// and bounds the result by bookkeepingTimeout.
func bookkeepingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
}

func (w *Worker) jobLog(job *store.Job) *slog.Logger {
	return w.log.With("job_id", job.ID, "kind", job.Kind, "attempt", job.Attempts)
}

func (w *Worker) kinds() []store.JobKind {
	out := make([]store.JobKind, 0, len(w.handlers))
	for k := range w.handlers {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// backoff is a delay that doubles from initial up to max. The zero state
// starts at initial; a copy of a fresh backoff starts over.
type backoff struct {
	initial, max time.Duration
	cur          time.Duration
}

// next returns the delay to wait now and doubles the one after it.
func (b *backoff) next() time.Duration {
	if b.cur == 0 {
		b.cur = b.initial
	}
	d := b.cur
	b.cur = min(2*b.cur, b.max)
	return d
}
