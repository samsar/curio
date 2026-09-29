package api

import (
	"cmp"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

const (
	// interestCardMembers is how many of an interest's documents its card
	// on the Interests page lists.
	interestCardMembers = 3
	// homeInterests is how many of the largest interests the search home
	// names.
	homeInterests = 6
)

// interests answers GET /ui/interests: the latest clustering run's
// largest interests, each with a few members, as GET /v1/interests lists
// them, and a rebuild of them.
//
// Its poller asks for the rebuild alone (?poll=rebuild, with the run the
// page shows), which never reads the interests: they are the page's
// costliest read, and a newer run is offered as a reload instead.
func (h pageHandlers) interests(w http.ResponseWriter, r *http.Request) {
	poll, err := pollParam(r, ui.PollRebuild)
	if err != nil {
		h.writePageError(w, r, err, ui.NavInterests)
		return
	}
	vm := ui.Interests{Layout: h.pages.layout("Interests", ui.NavInterests), Poll: poll}
	shown := ui.ShownRun(r.URL.Query())
	if poll == "" {
		resp, err := h.d.interests(r.Context(), defaultInterestLimit, interestCardMembers)
		if err != nil {
			h.writePageError(w, r, err, ui.NavNone)
			return
		}
		if resp.RunID != "" {
			vm.Run = &ui.InterestRun{ID: resp.RunID, Algo: resp.Algo, Documents: resp.NumDocuments,
				Noise: resp.NumNoise, Interests: resp.NumClusters}
			if resp.ComputedAt != nil {
				vm.Run.ComputedAt = *resp.ComputedAt
			}
		}
		for _, in := range resp.Items {
			vm.Interests = append(vm.Interests, interestView(in))
		}
		shown = resp.RunID
	}
	vm.Rebuild = h.rebuild(r, shown)
	h.page(w, r, http.StatusOK, ui.PageInterests, vm)
}

// rebuild reads whether a rebuild is queued or running, from the queue's
// cluster pool, and what the newest clustering run came to when it isn't
// shown, the run the page shows. The running rebuild's start is read only
// while one runs, and no read walks the queued jobs, which an import
// makes thousands of.
func (h pageHandlers) rebuild(r *http.Request, shown string) ui.Rebuild {
	ctx := r.Context()
	b := ui.Rebuild{Enabled: h.d.InsightEnabled, Shown: shown}
	queue, err := h.d.queueState(ctx)
	if err != nil {
		b.Err = h.panelError(r, err)
		return b
	}
	for _, k := range queue.Kinds {
		if k.Kind == string(store.JobKindCluster) {
			b.Queued, b.Running = k.Pending > 0, k.Running > 0
		}
	}
	if b.Queued && queue.State == queueClosed {
		b.Hold, b.OpensAt = queue.Reason, queue.OpensAt
	}
	if b.Running {
		running, err := h.d.listJobs(ctx, store.ListJobsOpts{Kind: store.JobKindCluster,
			Status: store.JobStatusRunning, Limit: 1})
		switch {
		case err != nil:
			h.quietError(r, err)
		case len(running.Items) > 0:
			b.StartedAt = running.Items[0].UpdatedAt // its claim
		}
	}
	run, err := h.d.Insights.LatestRun(ctx, h.d.TenantID, "")
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		b.Err = h.panelError(r, err)
	case run.ID != shown:
		b.NewRun, b.RunError = string(run.Status), deref(run.Error)
	}
	return b
}

// interest answers GET /ui/interests/{id}: one interest and its members,
// as many as GET /v1/interests/{id} gives by default.
func (h pageHandlers) interest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	resp, err := h.d.interest(r.Context(), id, defaultOneInterestMembers)
	if err != nil {
		h.lookupError(w, r, "interest", id, err)
		return
	}
	in := interestView(resp)
	h.page(w, r, http.StatusOK, ui.PageInterest, ui.InterestPage{
		Layout: h.pages.layout(cmp.Or(in.Label, "Unlabeled interest"), ui.NavInterests), Interest: in})
}

func interestView(in InterestResponse) ui.Interest {
	out := ui.Interest{ID: in.ID, Label: in.Label, Summary: in.Summary, Size: in.Size, Cohesion: in.Cohesion}
	for _, m := range in.Members {
		out.Members = append(out.Members, ui.Member{DocumentID: m.DocID, Title: m.Title, URL: m.URL,
			Similarity: m.Similarity})
	}
	return out
}
