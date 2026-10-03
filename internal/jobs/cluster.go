package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/samsar/curio/internal/store"
)

// Rebuilder regroups a tenant's library into interests: the insight
// engine, as the cluster handler sees it.
type Rebuilder interface {
	Rebuild(ctx context.Context, tenantID string, trigger store.RunTrigger) (runID string, err error)
}

// clusterHandler builds the closure that rebuilds a tenant's interests.
// The rebuild is corpus-wide, so the tenant on the job is all the input it
// needs besides the trigger its payload names.
//
// It delegates to the insight engine, which records an interest run for the
// attempt (done or failed) and commits the new grouping on success.
// Transient failures (Ollama unreachable, DB busy) are returned bare so the
// worker retries with backoff; a missing engine is permanent.
func clusterHandler(d Deps) HandlerFunc {
	return func(ctx context.Context, job *store.Job) error {
		if d.Insight == nil {
			return fmt.Errorf("%w: insight engine not configured", ErrPermanent)
		}
		if _, err := d.Insight.Rebuild(ctx, job.TenantID, clusterTrigger(job.Payload)); err != nil {
			return fmt.Errorf("cluster: %w", err)
		}
		return nil
	}
}

// clusterTrigger is the trigger a cluster job's payload names. A payload
// that names none, or one this version doesn't know, such as the {} of a
// job queued before triggers were recorded, was a manual rebuild: those
// were the only ones.
func clusterTrigger(payload json.RawMessage) store.RunTrigger {
	var p store.ClusterJobPayload
	if json.Unmarshal(payload, &p) != nil || !p.Trigger.Valid() {
		return store.RunTriggerManual
	}
	return p.Trigger
}

// EnqueueRebuild queues a rebuild of the tenant's interests for trigger,
// unless one is already pending: then it returns that one. queued reports
// whether it queued one. A running rebuild doesn't count, so asking during
// one queues the next.
func EnqueueRebuild(ctx context.Context, q store.JobQueue, tenantID string, trigger store.RunTrigger) (job *store.Job, queued bool, err error) {
	payload, err := json.Marshal(store.ClusterJobPayload{Trigger: trigger})
	if err != nil {
		return nil, false, fmt.Errorf("encode cluster job payload: %w", err)
	}
	job = &store.Job{TenantID: tenantID, Kind: store.JobKindCluster, Payload: payload}
	if queued, err = q.EnqueueOnce(ctx, job); err != nil {
		return nil, false, fmt.Errorf("enqueue a rebuild of interests: %w", err)
	}
	return job, queued, nil
}
