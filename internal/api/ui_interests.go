package api

import (
	"cmp"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/ui"
)

// interests answers GET /ui/interests: the latest clustering run's
// interests, each with a few members, as GET /v1/interests lists them.
func (h pageHandlers) interests(w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.interests(r.Context(), defaultInterestLimit, defaultInterestMembers)
	if err != nil {
		h.writePageError(w, r, err, ui.NavNone)
		return
	}
	vm := ui.Interests{Layout: pageLayout("Interests", ui.NavInterests)}
	if resp.RunID != "" {
		vm.Run = &ui.InterestRun{Algo: resp.Algo, Documents: resp.NumDocuments, Noise: resp.NumNoise}
		if resp.ComputedAt != nil {
			vm.Run.ComputedAt = *resp.ComputedAt
		}
	}
	for _, in := range resp.Items {
		vm.Interests = append(vm.Interests, interestView(in))
	}
	h.page(w, r, http.StatusOK, ui.PageInterests, vm)
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
		Layout: pageLayout(cmp.Or(in.Label, "Unlabeled interest"), ui.NavInterests), Interest: in})
}

func interestView(in InterestResponse) ui.Interest {
	out := ui.Interest{ID: in.ID, Label: in.Label, Summary: in.Summary, Size: in.Size, Cohesion: in.Cohesion}
	for _, m := range in.Members {
		out.Members = append(out.Members, ui.Member{DocumentID: m.DocID, Title: m.Title, URL: m.URL,
			Similarity: m.Similarity})
	}
	return out
}
