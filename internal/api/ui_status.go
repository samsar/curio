package api

import (
	"net/http"
	"time"

	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// statusCauses is how many failure causes Status's card draws: the few
// that account for most failures.
const statusCauses = 5

// status answers GET /ui/status: counts, the queue and its progress,
// health, and why documents failed, with what needs attention first. Each
// panel reads on its own, and one that fails shows its error while the
// others render.
//
// failures reads the cause and URL of every failed and dead document, about
// a millisecond per 3,000: fine once per page load, but a region of the page
// that is polled must leave the failures card out.
func (h pageHandlers) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vm := ui.Status{Layout: h.pages.layout("Status", ui.NavStatus)}

	stats, err := h.d.stats(ctx)
	vm.Counts = countsPanel(stats)
	vm.Counts.Err = h.panelError(r, err)

	queue, err := h.d.queueState(ctx)
	vm.Queue = queuePanel(queue)
	vm.Queue.Err = h.panelError(r, err)
	if vm.Queue.Err != nil {
		vm.Progress.Err = vm.Queue.Err // the same failure, logged once
	} else {
		metrics, err := h.d.metrics(ctx, ui.ProgressWindow)
		vm.Progress = ui.ProgressPanel{Err: h.panelError(r, err), Progress: estimateProgress(queue, metrics)}
	}

	health, err := h.d.health(ctx)
	vm.Health = healthPanel(health)
	vm.Health.Err = h.panelError(r, err)

	failures, err := h.d.failures(ctx)
	vm.Failures = failuresPanel(failures)
	vm.Failures.Err = h.panelError(r, err)

	h.page(w, r, http.StatusOK, ui.PageStatus, vm)
}

// countsPanel shows every document state, in lifecycle order, whether or
// not it has documents, and the statuses that have jobs.
func countsPanel(s Stats) ui.CountsPanel {
	p := ui.CountsPanel{Documents: s.DocumentsTotal, Bookmarks: s.BookmarksTotal}
	for _, state := range []store.DocState{store.DocStatePending, store.DocStateFetched, store.DocStateFailed,
		store.DocStateDead} {
		p.ByState = append(p.ByState, ui.Count{Name: string(state), Count: s.DocumentsByState[string(state)]})
	}
	for _, status := range []store.JobStatus{store.JobStatusPending, store.JobStatusRunning, store.JobStatusDone,
		store.JobStatusFailed} {
		if n := s.JobsByStatus[string(status)]; n > 0 {
			p.Jobs = append(p.Jobs, ui.Count{Name: string(status), Count: n})
		}
	}
	return p
}

func queuePanel(q QueueResponse) ui.QueuePanel {
	p := ui.QueuePanel{Open: q.State == queueOpen, Reason: q.Reason, OpensAt: q.OpensAt, Throttle: q.Throttle,
		Schedule: q.Schedule, KeepAwake: q.KeepAwake, KeepAwakeActive: q.KeepAwakeActive, PowerSource: q.PowerSource}
	for _, k := range q.Kinds {
		p.Kinds = append(p.Kinds, ui.KindLoad{Kind: k.Kind, Running: k.Running, Limit: k.Limit, Pending: k.Pending})
	}
	return p
}

// estimateProgress estimates when the fetch and index jobs queued now will
// be done, from how many of them finished in the metrics' window: the
// work of the library growing. Clustering runs are left out.
func estimateProgress(q QueueResponse, m MetricsResponse) ui.Progress {
	finished := map[string]int{}
	for _, k := range m.ByKind {
		finished[k.Kind] = k.Count
	}
	var work []ui.KindWork
	for _, k := range q.Kinds {
		if k.Kind == string(store.JobKindFetch) || k.Kind == string(store.JobKindIndex) {
			work = append(work, ui.KindWork{Kind: k.Kind, Pending: k.Pending, Running: k.Running,
				Finished: finished[k.Kind]})
		}
	}
	return ui.EstimateProgress(work, ui.ProgressWindow, q.State == queueOpen)
}

func healthPanel(h Health) ui.HealthPanel {
	p := ui.HealthPanel{Version: h.Version, OllamaReachable: h.OllamaReachable, OllamaDetail: h.OllamaDetail,
		EmbeddingModel: h.EmbeddingModel, EmbeddingDim: h.EmbeddingDim, GenerationModel: h.GenerationModel,
		YouTubeFetcher: h.YouTubeFetcher}
	if d := h.EmbeddingDrift; d != nil {
		p.Drift = &ui.Drift{Fix: d.Fix}
		for _, c := range d.Changes {
			p.Drift.Changes = append(p.Drift.Changes, ui.DriftChange{What: c.What, Recorded: c.Recorded, Current: c.Current})
		}
	}
	for _, u := range h.Upstreams {
		up := ui.Upstream{Name: u.Name, Enabled: u.Enabled, State: u.State, LastSuccess: u.LastSuccessAt,
			LastFailure: u.LastFailureAt, LastFailureClass: u.LastFailureClass, CooldownUntil: u.CooldownUntil,
			Window: time.Duration(u.WindowSeconds) * time.Second}
		for class, n := range u.Recent {
			up.Calls += n
			if fetcher.CallClass(class).Failed() {
				up.Failed += n
			}
		}
		p.Upstreams = append(p.Upstreams, up)
	}
	return p
}

// failuresPanel shows the statusCauses causes with the most failed and dead
// documents, most first, as failures counts them.
func failuresPanel(f FailuresResponse) ui.FailuresPanel {
	p := ui.FailuresPanel{Total: f.Total}
	for _, c := range f.Causes[:min(len(f.Causes), statusCauses)] {
		p.Causes = append(p.Causes, ui.Count{Name: c.Cause, Count: c.Count})
	}
	return p
}
