package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/samsar/curio/internal/store"
)

// Rebuilder regroups a tenant's library into interests: the insight
// engine, as the cluster handler sees it.
type Rebuilder interface {
	Rebuild(ctx context.Context, tenantID string, trigger store.RunTrigger) (runID string, err error)
	// Abandoned records a rebuild a daemon left unfinished as failed,
	// without running it again.
	Abandoned(ctx context.Context, tenantID string) error
}

// errRebuildAbandoned fails a cluster job a daemon left running: its
// rebuild is recorded as failed, not run again.
var errRebuildAbandoned = fmt.Errorf("%w: the daemon stopped during this rebuild; it is recorded as failed, "+
	"and the next is queued after the backoff", ErrPermanent)

// clusterHandler builds the closure that rebuilds a tenant's interests.
// The rebuild is corpus-wide, so the tenant on the job is all the input it
// needs besides the trigger its payload names.
//
// It delegates to the insight engine, which records an interest run for the
// attempt (done or failed) and commits the new grouping on success. A
// cluster job gets one attempt: every rebuild error is permanent, and the
// engine counts it as a failed rebuild, which the scheduler retries after
// its backoff. A job claimed again after the daemon stopped during it (the
// orphan RecoverOrphans requeued, its attempt kept) is not run again: the
// rebuild may be what killed the daemon, so the engine records it as
// failed, and the scheduler backs off. Nothing else claims a cluster job
// twice: an interrupted one is requeued with its attempt refunded.
func clusterHandler(d Deps) HandlerFunc {
	return func(ctx context.Context, job *store.Job) error {
		if d.Insight == nil {
			return fmt.Errorf("%w: insight engine not configured", ErrPermanent)
		}
		kick(d)
		if job.Attempts > 1 {
			if err := d.Insight.Abandoned(ctx, job.TenantID); err != nil {
				return fmt.Errorf("%w: cluster: %w", ErrPermanent, err)
			}
			return errRebuildAbandoned
		}
		if _, err := d.Insight.Rebuild(ctx, job.TenantID, clusterTrigger(job.Payload)); err != nil {
			return fmt.Errorf("%w: cluster: %w", ErrPermanent, err)
		}
		return nil
	}
}

// abandonedRebuild is the cluster pool's permanent-failure hook: a job
// RecoverOrphans failed for good, out of attempts after the daemon stopped
// during it, is a rebuild left unfinished, as clusterHandler records one.
// A job its handler failed counted its own failure, if any.
func abandonedRebuild(d Deps) PermFailHook {
	return func(ctx context.Context, job *store.Job, cause error) error {
		if !errors.Is(cause, errOrphanExhausted) || d.Insight == nil {
			return nil
		}
		return d.Insight.Abandoned(ctx, job.TenantID)
	}
}

// kick asks the interest scheduler to check now, when there is one.
func kick(d Deps) {
	if d.KickInterests != nil {
		d.KickInterests()
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
