// Package jobs runs the background work loop.
//
// A Worker polls the JobQueue, claims one job at a time, dispatches to a
// kind-specific HandlerFunc, and records the outcome. The daemon runs a pool
// of goroutines per Worker; the claim-once semantics in store/sqlite/jobs.go
// make that safe.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
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

// bookkeepingTimeout bounds each queue write the worker makes. It outlasts
// SQLite's 5s busy_timeout so a write that has to wait for the lock still
// lands, and it is the only limit: those writes run detached from the
// worker's context, so shutdown can't abort them halfway.
const bookkeepingTimeout = 10 * time.Second

// Worker polls the queue and dispatches jobs.
type Worker struct {
	queue        store.JobQueue
	handlers     map[string]HandlerFunc
	onPermFail   map[string]PermFailHook
	pollInterval time.Duration
	log          *slog.Logger

	inFlight sync.Map // job ID → struct{}, across every goroutine running this Worker
}

// WorkerOptions tunes the loop.
type WorkerOptions struct {
	PollInterval time.Duration // default 500ms
	Log          *slog.Logger  // default slog.Default()
}

func NewWorker(q store.JobQueue, opts WorkerOptions) *Worker {
	w := &Worker{
		queue:        q,
		handlers:     map[string]HandlerFunc{},
		onPermFail:   map[string]PermFailHook{},
		pollInterval: opts.PollInterval,
		log:          opts.Log,
	}
	if w.pollInterval <= 0 {
		w.pollInterval = 500 * time.Millisecond
	}
	if w.log == nil {
		w.log = slog.Default()
	}
	return w
}

// Register attaches a handler for a kind. Overwrites if called twice for
// the same kind — caller's responsibility to not do that.
func (w *Worker) Register(kind string, h HandlerFunc) {
	w.handlers[kind] = h
}

// OnPermanentFailure attaches a hook that fires after MarkFailed reports a
// job has hit terminal-failed state (retries exhausted, or wrapped with
// ErrPermanent). The hook is best-effort: errors are logged but don't
// re-fail the job. Use to clean up associated state, e.g., transition a
// parent document to state=failed or state=dead based on the cause.
func (w *Worker) OnPermanentFailure(kind string, h PermFailHook) {
	w.onPermFail[kind] = h
}

// RecoverOrphans settles jobs of this worker's kinds that a previous daemon
// left running (see store.JobQueue.RecoverOrphans), running the
// permanent-failure hook for each one that has no attempts left so its
// document ends up failed rather than pending forever. Call it once at
// startup, while the caller is the only daemon for the database and before
// any goroutine runs this Worker.
func (w *Worker) RecoverOrphans(ctx context.Context) error {
	failed, requeued, err := w.queue.RecoverOrphans(ctx, w.kinds())
	if err != nil {
		return fmt.Errorf("recover orphaned %v jobs: %w", w.kinds(), err)
	}
	if requeued > 0 || len(failed) > 0 {
		w.log.Info("recovered jobs orphaned by the previous daemon",
			"kinds", w.kinds(), "requeued", requeued, "failed", len(failed))
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
	sort.Strings(ids)
	return ids
}

// Run loops until ctx is cancelled. Returns ctx.Err() on shutdown.
//
// On each tick, drains all available work before sleeping again. This keeps
// throughput high right after a burst is enqueued without driving up the
// poll frequency during quiet periods.
//
// Cancelling ctx stops new claims and is passed to the running handler; the
// job's outcome is still recorded, and a job the shutdown interrupted goes
// back to the queue (see finish).
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("worker started", "poll_interval", w.pollInterval, "kinds", w.kinds())

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		// Drain whatever's ready right now.
		for w.tryOne(ctx) {
		}

		select {
		case <-ctx.Done():
			w.log.Info("worker stopping")
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// tryOne claims and dispatches one job. Returns true if a job was handled
// (success or failure), false if the queue was empty, the claim failed, or
// ctx is done.
func (w *Worker) tryOne(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}

	// The claim runs detached from ctx: cancelling mid-statement can report
	// failure for an UPDATE that has already committed, which would leave
	// the job running with nobody working on it.
	cctx, cancel := bookkeepingContext(ctx)
	job, err := w.queue.ClaimNext(cctx, w.kinds())
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
		if merr := w.queue.MarkDone(bctx, job.ID); merr != nil {
			log.Error("mark done failed", "err", merr)
			return
		}
		log.Info("job done", "duration_ms", dur.Milliseconds())

	case ctx.Err() != nil:
		// The shutdown interrupted the handler, so whatever it returned
		// (context.Canceled, a killed subprocess, even ErrPermanent) says
		// nothing about the job. Put it back and give the attempt back.
		if rerr := w.queue.Requeue(bctx, job.ID); rerr != nil {
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
	permanent, err := w.queue.MarkFailed(ctx, job.ID, cause.Error(), retry)
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
	if err := recoverPanic(log, "permanent-failure hook", func() error { return hook(ctx, job, cause) }); err != nil {
		log.Error("permanent-failure hook errored", "err", err)
	}
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

func (w *Worker) kinds() []string {
	out := make([]string, 0, len(w.handlers))
	for k := range w.handlers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
