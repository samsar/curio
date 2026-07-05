// Package jobs runs the background work loop.
//
// A Worker polls the JobQueue, claims one job at a time, dispatches to a
// kind-specific HandlerFunc, and marks the result. M0 runs a single
// worker; later milestones can run a pool — the claim-once semantics in
// store/sqlite/jobs.go are already proven safe under concurrency.
package jobs

import (
	"context"
	"errors"
	"log/slog"
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

// Worker polls the queue and dispatches jobs.
type Worker struct {
	queue        store.JobQueue
	handlers     map[string]HandlerFunc
	onPermFail   map[string]PermFailHook
	pollInterval time.Duration
	log          *slog.Logger
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

// Run loops until ctx is cancelled. Returns ctx.Err() on shutdown.
//
// On each tick, drains all available work before sleeping again. This keeps
// throughput high right after a burst is enqueued without driving up the
// poll frequency during quiet periods.
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("worker started", "poll_interval", w.pollInterval, "kinds", w.kinds())

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		// Drain whatever's ready right now.
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !w.tryOne(ctx) {
				break
			}
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
// (success or failure), false if the queue was empty. Errors during claim
// itself are logged but treated as "no work."
func (w *Worker) tryOne(ctx context.Context) bool {
	job, err := w.queue.ClaimNext(ctx, w.kinds())
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		w.log.Error("claim failed", "err", err)
		return false
	}

	log := w.log.With("job_id", job.ID, "kind", job.Kind, "attempt", job.Attempts)
	log.Info("job claimed")

	h := w.handlers[job.Kind]
	if h == nil {
		log.Warn("no handler registered, marking failed permanently")
		_, _ = w.queue.MarkFailed(ctx, job.ID, "no handler for kind", false)
		return true
	}

	start := time.Now()
	err = h(ctx, job)
	dur := time.Since(start)

	if err == nil {
		log.Info("job done", "duration_ms", dur.Milliseconds())
		if err := w.queue.MarkDone(ctx, job.ID); err != nil {
			log.Error("mark done failed", "err", err)
		}
		return true
	}

	retry := !errors.Is(err, ErrPermanent)
	log.Warn("job failed", "err", err, "retry", retry, "duration_ms", dur.Milliseconds())
	permanent, mfErr := w.queue.MarkFailed(ctx, job.ID, err.Error(), retry)
	if mfErr != nil {
		log.Error("mark failed errored", "err", mfErr)
	}
	if permanent {
		if hook := w.onPermFail[job.Kind]; hook != nil {
			if hookErr := hook(ctx, job, err); hookErr != nil {
				log.Warn("permanent-failure hook errored", "err", hookErr)
			}
		}
	}
	return true
}

func (w *Worker) kinds() []string {
	out := make([]string, 0, len(w.handlers))
	for k := range w.handlers {
		out = append(out, k)
	}
	return out
}
