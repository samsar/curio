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
// Its pollers ask for less: ?poll=live for the counts, the queue and its
// progress (a few covering index walks, every 2 seconds), and ?poll=health
// for health, whose read pings Ollama, every 15 seconds. failures reads
// the cause and URL of every failed and dead document, about a
// millisecond per 3,000, so only the page reads it.
func (h pageHandlers) status(w http.ResponseWriter, r *http.Request) {
	poll, err := pollParam(r, ui.PollLive, ui.PollHealth)
	if err != nil {
		h.writePageError(w, r, err, ui.NavStatus)
		return
	}
	vm := ui.Status{Layout: h.pages.layout("Status", ui.NavStatus), Poll: poll}
	if poll != ui.PollHealth {
		h.statusLive(r, &vm)
	}
	if poll != ui.PollLive {
		health, err := h.d.health(r.Context())
		panel := healthPanel(health)
		panel.Err = h.panelError(r, err)
		vm.Health = &panel
	}
	if poll == "" {
		failures, err := h.d.failures(r.Context())
		panel := failuresPanel(failures)
		panel.Err = h.panelError(r, err)
		vm.Failures = &panel
	}
	h.page(w, r, http.StatusOK, ui.PageStatus, vm)
}

// statusLive reads what Status's 2-second poll shows into vm: the counts,
// the queue and its progress, and each pool's finished jobs, which come
// with the progress's metrics.
func (h pageHandlers) statusLive(r *http.Request, vm *ui.Status) {
	ctx := r.Context()
	stats, err := h.d.stats(ctx)
	counts := countsPanel(stats)
	counts.Err = h.panelError(r, err)
	vm.Counts = &counts

	queue, err := h.d.queueState(ctx)
	panel := queuePanel(queue)
	panel.Err = h.panelError(r, err)
	progress := ui.ProgressPanel{Err: panel.Err} // the same failure, logged once
	if panel.Err == nil {
		metrics, err := h.d.metrics(ctx, ui.ProgressWindow)
		progress = ui.ProgressPanel{Err: h.panelError(r, err), Progress: estimateProgress(queue, metrics),
			Reason: queue.Reason}
		if err == nil {
			addFinished(&panel, metrics)
		}
	}
	vm.Queue, vm.Progress = &panel, &progress
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
	p := ui.QueuePanel{Open: q.State == queueOpen, Paused: q.Paused, Reason: q.Reason, OpensAt: q.OpensAt,
		Throttle: q.Throttle, Schedule: q.Schedule, KeepAwake: q.KeepAwake, KeepAwakeActive: q.KeepAwakeActive,
		PowerSource: q.PowerSource}
	for _, k := range q.Kinds {
		p.Kinds = append(p.Kinds, ui.KindLoad{Kind: k.Kind, Running: k.Running, Limit: k.Limit, Pending: k.Pending,
			DueLater: k.DueLater, NextDue: k.NextDue})
	}
	return p
}

// addFinished gives p's pools how many of their jobs finished, done or
// failed, in m's window.
func addFinished(p *ui.QueuePanel, m MetricsResponse) {
	finished := map[string]int{}
	for _, k := range m.ByKind {
		finished[k.Kind] = k.Count
	}
	for i := range p.Kinds {
		p.Kinds[i].Finished = finished[p.Kinds[i].Kind]
	}
	p.FinishedIn = time.Duration(m.WindowSeconds) * time.Second
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
				DueLater: k.DueLater, NextDue: k.NextDue, Finished: finished[k.Kind]})
		}
	}
	return ui.EstimateProgress(work, ui.ProgressWindow, q.State == queueOpen)
}

func healthPanel(h Health) ui.HealthPanel {
	p := ui.HealthPanel{Version: h.Version, OllamaReachable: h.OllamaReachable, OllamaDetail: h.OllamaDetail,
		EmbeddingModel: h.EmbeddingModel, EmbeddingDim: h.EmbeddingDim, GenerationModel: h.GenerationModel,
		YouTubeFetcher: h.YouTubeFetcher}
	if d := h.EmbeddingDrift; d != nil {
		p.Drift = &ui.Drift{Verified: d.Verification.Verified, Detail: d.Verification.Detail, Fix: d.Fix}
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
