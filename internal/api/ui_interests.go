package api

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"time"

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

// interests answers GET /ui/interests: a page of the latest clustering
// run's interests, largest first, each with a few members, as GET
// /v1/interests lists them, and a rebuild of them. A page past the last is
// a 404 that keeps the page's frame.
//
// Its links between pages carry the run they show (?run=). The engine
// prunes the previous run once a rebuild finishes, so a page asked for
// from a run that is gone shows the newest run's, and says so.
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
	status := http.StatusOK
	if poll == "" {
		page, err := pageParam(r)
		if err != nil {
			h.writePageError(w, r, err, ui.NavInterests)
			return
		}
		resp, err := h.d.interests(r.Context(), interestsOpts{Limit: ui.InterestsPageSize,
			Offset: ui.PageOffset(page, ui.InterestsPageSize), Members: interestCardMembers})
		if err != nil {
			h.writePageError(w, r, err, ui.NavNone)
			return
		}
		vm.Page, vm.Run = page, interestRun(resp)
		for _, in := range resp.Items {
			vm.Interests = append(vm.Interests, interestView(in))
		}
		vm.RunChanged = shown != "" && resp.RunID != "" && shown != resp.RunID
		shown = resp.RunID
		if vm.OutOfRange() != nil {
			status = http.StatusNotFound
		}
	}
	vm.Rebuild = h.rebuild(r, shown)
	h.page(w, r, status, ui.PageInterests, vm)
}

// interestRun is the run a page of interests comes from, nil before the
// first finished.
func interestRun(resp InterestListResponse) *ui.InterestRun {
	if resp.RunID == "" {
		return nil
	}
	run := &ui.InterestRun{ID: resp.RunID, Algo: resp.Algo, Documents: resp.NumDocuments, Noise: resp.NumNoise,
		Interests: resp.NumClusters}
	if resp.ComputedAt != nil {
		run.ComputedAt = *resp.ComputedAt
	}
	return run
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
		b.Hold = queue.Reason
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

// interest answers GET /ui/interests/{id}: one interest and a page of its
// members, most similar first, with the run it comes from. A page past the
// last is a 404 that keeps the interest's head. Interests get new IDs with
// every rebuild, so one that isn't found leads back to Interests.
func (h pageHandlers) interest(w http.ResponseWriter, r *http.Request) {
	page, err := pageParam(r)
	if err != nil {
		h.writePageError(w, r, err, ui.NavInterests)
		return
	}
	id := chi.URLParam(r, "id")
	resp, err := h.d.interest(r.Context(), id, ui.InterestMembersPageSize,
		ui.PageOffset(page, ui.InterestMembersPageSize))
	if errors.Is(err, store.ErrNotFound) {
		h.errorPage(w, r, http.StatusNotFound, "not found",
			fmt.Sprintf("interest %q not found: interests get new IDs each time they are rebuilt", id), ui.NavInterests)
		return
	}
	if err != nil {
		h.writePageError(w, r, err, ui.NavNone)
		return
	}
	in := interestView(resp)
	vm := ui.InterestPage{Layout: h.pages.layout(cmp.Or(in.Label, "Unlabeled interest"), ui.NavInterests),
		Interest: in, Page: page, RunAt: h.runFinished(r, resp.RunID)}
	status := http.StatusOK
	if vm.OutOfRange() != nil {
		status = http.StatusNotFound
	}
	h.page(w, r, status, ui.PageInterest, vm)
}

// runFinished is when the clustering run id finished, for an interest's
// run line, or zero when that is unknown: the page does without the line
// for a run it can't read, logging why, and for one that is gone.
func (h pageHandlers) runFinished(r *http.Request, id string) time.Time {
	run, err := h.d.Insights.GetRun(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		h.quietError(r, fmt.Errorf("interest's run %s: %w", id, err))
	case run.FinishedAt != nil:
		return *run.FinishedAt
	}
	return time.Time{}
}

func interestView(in InterestResponse) ui.Interest {
	out := ui.Interest{ID: in.ID, Label: in.Label, Summary: in.Summary, Size: in.Size, Cohesion: in.Cohesion}
	for _, m := range in.Members {
		out.Members = append(out.Members, ui.Member{DocumentID: m.DocID, Title: m.Title, BookmarkTitle: m.BookmarkTitle,
			URL: m.URL, Similarity: m.Similarity})
	}
	return out
}
