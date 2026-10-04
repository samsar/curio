package api

import (
	"errors"
	"net/http"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// interestMap answers GET /ui/interests/map: the interest map, a second
// view of the Interests. With a map to draw, the page is the shell
// static/map.js draws it into, which reads the map from GET
// /v1/interests/map itself, once: the page reads the latest run and
// nothing else, through the endpoint's own mapRun, so the two agree on
// whether there is a map. Without one it says why, in the endpoint's
// terms. Either is a 200, as the Interests are before their first
// rebuild. The view and the selection the address asks for (?view=,
// ?select=) are checked before anything is read: one the map can't show
// is a 400.
func (h pageHandlers) interestMap(w http.ResponseWriter, r *http.Request) {
	q, err := ui.ParseMapQuery(r.URL.Query())
	if err != nil {
		h.writePageError(w, r, badRequest("%w", err), ui.NavInterests)
		return
	}
	vm := ui.MapPage{Layout: h.pages.layout("Interest map", ui.NavInterests), State: ui.MapReady, Query: q,
		Rebuilds: interestsStateView(h.d.interestsState())}
	run, err := h.d.mapRun(r.Context())
	var none *mapUnavailableError
	switch {
	case errors.As(err, &none):
		vm.State, vm.Error = ui.MapState(none.body.Reason), none.body.MapError
	case err != nil:
		h.writePageError(w, r, err, ui.NavInterests)
		return
	}
	if run != nil {
		vm.Run = mapRunView(run)
	}
	h.page(w, r, http.StatusOK, ui.PageMap, vm)
}

// mapRunView is the rebuild a map is of, in the page's terms.
func mapRunView(run *store.InterestRun) *ui.MapRun {
	out := &ui.MapRun{Shape: string(run.Shape), Documents: run.NumDocuments, Areas: run.NumAreas,
		Interests: run.NumInterests}
	if run.FinishedAt != nil {
		out.FinishedAt = *run.FinishedAt
	}
	return out
}
