package jobs

import (
	"context"
	"fmt"

	"github.com/samsar/curio/internal/store"
)

// ClusterHandler builds the closure that recomputes a tenant's insight
// clusters (the M4 insight layer). Clustering is corpus-wide, so the tenant on
// the job is all the input it needs; the payload is currently empty.
//
// It delegates to the insight engine, which records a cluster_runs row for the
// attempt (done or failed) and atomically replaces the current clusters on
// success. Transient failures (Ollama unreachable, DB busy) are returned bare
// so the worker retries with backoff; a missing engine is permanent.
func ClusterHandler(d Deps) HandlerFunc {
	return func(ctx context.Context, job *store.Job) error {
		if d.Insight == nil {
			return fmt.Errorf("%w: insight engine not configured", ErrPermanent)
		}
		if _, err := d.Insight.Rebuild(ctx, job.TenantID); err != nil {
			return fmt.Errorf("cluster: %w", err)
		}
		return nil
	}
}
