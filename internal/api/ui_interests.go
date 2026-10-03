package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// homeInterests is how many of the largest top-level groups the search
// home names.
const homeInterests = 6

// retentionDays is how many days a retired identity is remembered, which
// the page of an ID nothing knows names.
const retentionDays = int(insight.RetiredRetention / (24 * time.Hour))

// interests answers GET /ui/interests: a page of the latest rebuild's
// top-level groups, largest first, as GET /v1/interests lists them: areas
// naming their largest interests, or interests with a few members in the
// flat shape; Unsorted's card after the last page's; and a rebuild of
// them. A page past the last is a 404 that keeps the page's frame.
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
		vm.Page, vm.Run = page, interestRun(resp, time.Now())
		for _, in := range resp.Items {
			vm.Interests = append(vm.Interests, interestView(in))
		}
		vm.RunChanged = shown != "" && resp.RunID != "" && shown != resp.RunID
		shown = resp.RunID
		if vm.OutOfRange() != nil {
			status = http.StatusNotFound
		}
	}
	vm.Rebuild = h.rebuild(r, shown, poll != "")
	h.page(w, r, status, ui.PageInterests, vm)
}

// interestRun is the run a page of interests comes from, nil before the
// first is done; at now, whether it is recent enough for the page to tell
// what it changed.
func interestRun(resp InterestListResponse, now time.Time) *ui.InterestRun {
	if resp.RunID == "" {
		return nil
	}
	run := &ui.InterestRun{ID: resp.RunID, Shape: resp.Shape, Documents: resp.NumDocuments, Areas: resp.NumAreas,
		Interests: resp.NumInterests, Loose: resp.NumLoose, Unsorted: resp.NumUnsorted, New: resp.NumNew,
		NewUnsorted: resp.NumNewUnsorted, Total: resp.Total}
	if resp.ComputedAt != nil {
		run.ComputedAt = *resp.ComputedAt
		run.Recent = now.Sub(run.ComputedAt) < ui.ChangesNoticeFor
	}
	if b := resp.Rebuild; b != nil {
		run.Kind, run.Changes = b.Kind, runChanges(*b)
	}
	return run
}

// runChanges is what a rebuild did, in the page's terms.
func runChanges(b InterestRebuild) ui.RunChanges {
	return ui.RunChanges{Kept: b.Kept, Split: b.Split, Merged: b.Merged, Moved: b.Moved, Dissolved: b.Dissolved,
		Created: b.Created}
}

// rebuild is the page's rebuild. Whether one is queued or running comes
// from the queue's cluster pool: the queue is read after a click's
// rebuild is queued, where the scheduler's snapshot, which says the rest
// (held, failing, due, current, none), lags it until its next check. The
// running rebuild's start is read only while one runs, and no read walks
// the queued jobs, which an import makes thousands of. A poll (poll) also
// reads the latest done run, to offer a newer one than the run the page
// shows (shown) as a reload; the page itself has just read it.
func (h pageHandlers) rebuild(r *http.Request, shown string, poll bool) ui.Rebuild {
	ctx := r.Context()
	b := ui.Rebuild{Enabled: h.d.InsightEnabled, Shown: shown, State: interestsStateView(h.d.interestsState())}
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
	if !poll {
		return b
	}
	run, err := h.d.Insights.LatestRun(ctx, h.d.TenantID, store.InterestRunDone)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		b.Err = h.panelError(r, err)
	default:
		b.Ready = run.ID != shown
	}
	return b
}

// interestsStateView is where automatic rebuilds stand, in the pages'
// terms.
func interestsStateView(s InterestsState) ui.InterestsState {
	return ui.InterestsState{State: s.State, LastRebuildAt: s.LastRebuildAt, LastKind: s.LastKind,
		Changed: s.ChangedDocuments, RebuildAt: s.RebuildAt, FreshOwed: s.FreshOwed, HeldReason: s.HeldReason,
		RetryAt: s.RetryAt, LastError: s.LastError}
}

// interest answers GET /ui/interests/{id}: an area, with a page of its
// interests; or an interest, with the documents placed into it since on
// its first page, then a page of its members, then its loose fits, most
// similar first; each with the run it comes from and what that rebuild
// did to it. A page past the last is a 404 that keeps the head. A retired
// identity is a 410 saying what became of it, and one nothing knows a
// 404 saying what it may have been, both in the Interests' frame.
func (h pageHandlers) interest(w http.ResponseWriter, r *http.Request) {
	page, err := pageParam(r)
	if err != nil {
		h.writePageError(w, r, err, ui.NavInterests)
		return
	}
	id := chi.URLParam(r, "id")
	opts := interestOpts{Members: ui.InterestMembersPageSize, AreaMembers: ui.InterestCardMembers,
		Offset: ui.PageOffset(page, ui.InterestMembersPageSize), Window: ui.InterestsPageSize,
		WindowOffset: ui.PageOffset(page, ui.InterestsPageSize)}
	if page == 1 {
		opts.NewMembers = maxNewMembers
	}
	resp, err := h.d.interest(r.Context(), id, opts)
	var retired *retiredInterestError
	switch {
	case errors.As(err, &retired):
		h.gone(w, r, id, retiredView(retired.body))
		return
	case errors.Is(err, store.ErrNotFound):
		h.gone(w, r, id, nil)
		return
	case err != nil:
		h.writePageError(w, r, err, ui.NavNone)
		return
	}
	in := interestView(resp)
	vm := ui.InterestPage{Layout: h.pages.layout(in.Name(), ui.NavInterests), Interest: in, Page: page,
		RunAt: h.runFinished(r, resp.RunID)}
	status := http.StatusOK
	if vm.OutOfRange() != nil {
		status = http.StatusNotFound
	}
	h.page(w, r, status, ui.PageInterest, vm)
}

// gone answers an identity the latest rebuild doesn't hold, in the
// Interests' frame: retired, a 410 saying what became of it; nil, an ID
// nothing knows, a 404. Neither is a fault, so neither is the error page.
func (h pageHandlers) gone(w http.ResponseWriter, r *http.Request, id string, retired *ui.Retired) {
	vm := ui.Gone{ID: id, Retired: retired, RetentionDays: retentionDays}
	status, title := http.StatusNotFound, "No such interest"
	if retired != nil {
		status, title = http.StatusGone, retired.Name()
	}
	vm.Layout = h.pages.layout(title, ui.NavInterests)
	h.page(w, r, status, ui.PageRetired, vm)
}

// retiredView is a retired identity's 410 in the page's terms.
func retiredView(b RetiredInterest) *ui.Retired {
	out := &ui.Retired{Area: b.Level == string(store.InterestLevelArea), Label: b.Label, RetiredAt: b.RetiredAt}
	for _, s := range b.Successors {
		out.Successors = append(out.Successors, ui.Successor{InterestRef: ui.InterestRef{ID: s.ID, Label: s.Label,
			Area: s.Level == string(store.InterestLevelArea), Retired: s.Retired}, Event: s.Event, Shared: s.Shared})
	}
	return out
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

// unsorted answers GET /ui/interests/unsorted: a page of the latest
// rebuild's unsorted documents, nearest an interest first, each with that
// interest, as GET /v1/interests/unsorted lists them, and on the first
// page those placed into Unsorted since. Its pages name their run as the
// Interests' do. A page past the last is a 404 that keeps the head.
func (h pageHandlers) unsorted(w http.ResponseWriter, r *http.Request) {
	page, err := pageParam(r)
	if err != nil {
		h.writePageError(w, r, err, ui.NavInterests)
		return
	}
	opts := unsortedOpts{Limit: ui.InterestMembersPageSize, Offset: ui.PageOffset(page, ui.InterestMembersPageSize)}
	if page == 1 {
		opts.NewMembers = maxNewMembers
	}
	resp, err := h.d.unsorted(r.Context(), opts)
	if err != nil {
		h.writePageError(w, r, err, ui.NavNone)
		return
	}
	shown := ui.ShownRun(r.URL.Query())
	vm := ui.Unsorted{Layout: h.pages.layout("Unsorted", ui.NavInterests), Page: page, Run: resp.RunID,
		RunChanged: shown != "" && resp.RunID != "" && shown != resp.RunID, Total: resp.Total, NumNew: resp.NumNew}
	for _, m := range resp.Items {
		doc := ui.UnsortedDoc{Member: unsortedMember(m, fitUnsorted)}
		if m.NearestID != "" {
			doc.Nearest = ui.InterestRef{ID: m.NearestID, Label: m.NearestLabel}
		}
		vm.Documents = append(vm.Documents, doc)
	}
	for _, m := range resp.New {
		vm.New = append(vm.New, unsortedMember(m, fitNew))
	}
	status := http.StatusOK
	if vm.OutOfRange() != nil {
		status = http.StatusNotFound
	}
	h.page(w, r, status, ui.PageUnsorted, vm)
}

// unsortedMember is an unsorted document as a list names it, of fit.
func unsortedMember(m UnsortedMember, fit string) ui.Member {
	return ui.Member{DocumentID: m.DocID, Title: m.Title, BookmarkTitle: m.BookmarkTitle, URL: m.URL,
		Similarity: m.Similarity, Fit: fit}
}

// changes answers GET /ui/interests/changes: what the latest rebuild
// changed, as GET /v1/interests/changes says.
func (h pageHandlers) changes(w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.interestChanges(r.Context())
	if err != nil {
		h.writePageError(w, r, err, ui.NavNone)
		return
	}
	vm := ui.Changes{Layout: h.pages.layout("What changed", ui.NavInterests)}
	if resp.RunID != "" {
		run := &ui.ChangesRun{Events: eventViews(resp.Events)}
		if resp.ComputedAt != nil {
			run.ComputedAt = *resp.ComputedAt
		}
		if b := resp.Rebuild; b != nil {
			run.Trigger, run.Kind, run.Changes = b.Trigger, b.Kind, runChanges(*b)
		}
		vm.Run = run
	}
	h.page(w, r, http.StatusOK, ui.PageChanges, vm)
}

// interestView is an area or an interest in the page's terms.
func interestView(in InterestResponse) ui.Interest {
	out := ui.Interest{ID: in.ID, Area: in.Level == string(store.InterestLevelArea), ParentID: in.ParentID,
		ParentLabel: in.ParentLabel, Label: in.Label, Summary: in.Summary, Size: in.Size, Loose: in.Loose, New: in.New,
		Cohesion: in.Cohesion, NumChildren: in.NumChildren, Events: eventViews(in.Events)}
	for _, c := range in.Children {
		out.Children = append(out.Children, interestView(c))
	}
	out.Members = memberViews(in.Members)
	out.NewMembers = memberViews(in.NewMembers)
	return out
}

// memberViews are an interest's documents in the page's terms.
func memberViews(members []InterestMember) []ui.Member {
	out := make([]ui.Member, 0, len(members))
	for _, m := range members {
		out = append(out, ui.Member{DocumentID: m.DocID, Title: m.Title, BookmarkTitle: m.BookmarkTitle, URL: m.URL,
			Similarity: m.Similarity, Fit: m.Fit})
	}
	return out
}

// eventViews are a rebuild's events in the page's terms: each identity an
// event names is of the event's level, and a moved interest's area an
// area.
func eventViews(events []InterestEvent) []ui.InterestEvent {
	out := make([]ui.InterestEvent, 0, len(events))
	for _, e := range events {
		area := e.Level == string(store.InterestLevelArea)
		out = append(out, ui.InterestEvent{Event: e.Event, Area: area, From: refView(e.From, area),
			To: refView(e.To, area), In: refView(e.Area, true), Shared: e.Shared})
	}
	return out
}

// refView is r in the page's terms, an area's when area; nil for nil.
func refView(r *InterestRef, area bool) *ui.InterestRef {
	if r == nil {
		return nil
	}
	return &ui.InterestRef{ID: r.ID, Label: r.Label, Area: area, Retired: r.Retired}
}

// documentPlace is where the latest rebuild put document id, for its
// page's line: nil when it has it nowhere or there is no rebuild, and
// when the read fails, which the page does without, logging why.
func (h pageHandlers) documentPlace(r *http.Request, id string) *ui.DocumentPlace {
	p, err := h.d.documentPlace(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		h.quietError(r, err)
		return nil
	}
	out := &ui.DocumentPlace{Fit: string(p.Fit),
		Interest: ui.InterestRef{ID: p.InterestID, Label: p.InterestLabel},
		Area:     ui.InterestRef{ID: p.AreaID, Label: p.AreaLabel, Area: true},
		Nearest:  ui.InterestRef{ID: p.NearestID, Label: p.NearestLabel}}
	if p.Placed {
		out.Fit = fitNew
	}
	return out
}
