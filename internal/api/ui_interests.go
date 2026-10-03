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

// homeInterests is how many of the largest top-level groups the search
// home names.
const homeInterests = 6

// interests answers GET /ui/interests: a page of the latest rebuild's
// top-level groups, largest first, as GET /v1/interests lists them: areas
// naming their largest interests, or interests with a few members in the
// flat shape; and a rebuild of them. A page past the last is a 404 that
// keeps the page's frame.
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
		// An area's card names its interests and their sizes, never their
		// members.
		resp, err := h.d.interests(r.Context(), interestsOpts{Limit: ui.InterestsPageSize,
			Offset: ui.PageOffset(page, ui.InterestsPageSize), Children: ui.AreaCardInterests,
			Members: ui.InterestCardMembers})
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
// first is done.
func interestRun(resp InterestListResponse) *ui.InterestRun {
	if resp.RunID == "" {
		return nil
	}
	run := &ui.InterestRun{ID: resp.RunID, Algo: resp.Algo, Shape: resp.Shape, Documents: resp.NumDocuments,
		Areas: resp.NumAreas, Interests: resp.NumInterests, Loose: resp.NumLoose, Unsorted: resp.NumUnsorted,
		Total: resp.Total}
	if resp.ComputedAt != nil {
		run.ComputedAt = *resp.ComputedAt
	}
	return run
}

// rebuild reads whether a rebuild is queued or running, from the queue's
// cluster pool, and what the newest rebuild came to when it isn't
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

// interest answers GET /ui/interests/{id}: an area, with every interest it
// holds; or an interest, with a page of its members, then its loose fits,
// most similar first; each with the run it comes from. A page past an
// interest's last is a 404 that keeps the interest's head; an area's page
// is one page, whatever ?page says. A retired identity is a 410 saying
// what became of it, and one the latest rebuild never heard of a 404:
// interests were regrouped when curio was upgraded, and identities from
// before then are gone.
func (h pageHandlers) interest(w http.ResponseWriter, r *http.Request) {
	page, err := pageParam(r)
	if err != nil {
		h.writePageError(w, r, err, ui.NavInterests)
		return
	}
	id := chi.URLParam(r, "id")
	resp, err := h.d.interest(r.Context(), id, interestOpts{Members: ui.InterestMembersPageSize,
		AreaMembers: ui.InterestCardMembers, Offset: ui.PageOffset(page, ui.InterestMembersPageSize)})
	var retired *retiredInterestError
	switch {
	case errors.As(err, &retired):
		h.errorPage(w, r, http.StatusGone, "interest retired", retired.Error(), ui.NavInterests)
		return
	case errors.Is(err, store.ErrNotFound):
		h.errorPage(w, r, http.StatusNotFound, "not found",
			fmt.Sprintf("interest %q not found: interests were regrouped when curio was upgraded, "+
				"so links from before then no longer work", id), ui.NavInterests)
		return
	case err != nil:
		h.writePageError(w, r, err, ui.NavNone)
		return
	}
	in := interestView(resp)
	title := cmp.Or(in.Label, "Unlabeled interest")
	if in.Area {
		title = cmp.Or(in.Label, "Unlabeled area")
	}
	vm := ui.InterestPage{Layout: h.pages.layout(title, ui.NavInterests), Interest: in, Page: page,
		RunAt: h.runFinished(r, resp.RunID)}
	status := http.StatusOK
	if vm.OutOfRange() != nil {
		status = http.StatusNotFound
	}
	h.page(w, r, status, ui.PageInterest, vm)
}

// runFinished is when the rebuild id finished, for a group's run line, or
// zero when that is unknown: the page does without the line for a run it
// can't read, logging why, and for one that is gone.
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

// interestView is an area or an interest in the page's terms.
func interestView(in InterestResponse) ui.Interest {
	out := ui.Interest{ID: in.ID, Area: in.Level == string(store.InterestLevelArea), ParentID: in.ParentID,
		ParentLabel: in.ParentLabel, Label: in.Label, Summary: in.Summary, Size: in.Size, Loose: in.Loose,
		Cohesion: in.Cohesion, NumChildren: in.NumChildren}
	for _, c := range in.Children {
		out.Children = append(out.Children, interestView(c))
	}
	for _, m := range in.Members {
		out.Members = append(out.Members, ui.Member{DocumentID: m.DocID, Title: m.Title, BookmarkTitle: m.BookmarkTitle,
			URL: m.URL, Similarity: m.Similarity, Loose: m.Fit == fitLoose})
	}
	return out
}
