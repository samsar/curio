package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// search answers GET /ui/?q=&content_type=&page=, the dashboard's home:
// the form, and for a query, a page of the results of the same search POST
// /v1/search runs, ui.SearchPageSize of them from the page's offset,
// limited to the content type when one is given. A search that fails is
// shown where its results would be, with its status. A type that isn't
// one is a 400 page, as the Library answers it, and so is a page no search
// can have, before anything is searched; without a query the page is
// ignored. A page past this search's last is its answer, a 200 with the
// out-of-range card: the page's status is its search's, and the search
// answered, as POST /v1/search answers an offset past its total.
//
// Search as you type renders the whole page on every keystroke, so with a
// query the page reads nothing but the search; without one it reads what
// the home shows instead (home).
func (h pageHandlers) search(w http.ResponseWriter, r *http.Request) {
	contentType, err := contentTypeParam(r)
	if err != nil {
		h.writePageError(w, r, err, ui.NavSearch)
		return
	}
	q := r.URL.Query().Get("q")
	vm := ui.Search{Layout: h.pages.layout("Search", ui.NavSearch), Query: q, Type: string(contentType)}
	status := http.StatusOK
	if strings.TrimSpace(q) == "" {
		vm.Home = h.home(r)
		h.page(w, r, status, ui.PageSearch, vm)
		return
	}
	if vm.Page, err = searchPageParam(r); err != nil {
		h.writePageError(w, r, err, ui.NavSearch)
		return
	}
	req := SearchRequest{Query: q, K: ui.SearchPageSize, Offset: ui.PageOffset(vm.Page, ui.SearchPageSize)}
	if contentType != "" {
		req.Filters.ContentType = []string{string(contentType)}
	}
	resp, err := h.d.search(r.Context(), req)
	if err != nil {
		status, vm.Err = h.reportPanel(r, err)
	} else {
		vm.Results = searchResults(resp)
	}
	h.page(w, r, status, ui.PageSearch, vm)
}

// searchPageParam reads the search page's ?page (ui.PageParam) as pageParam
// reads a numbered list's, and refuses a page past ui.MaxSearchPages as
// well: a search ranks store.MaxSearchK documents at most, so no search has
// one, and the page's offset would be one POST /v1/search refuses.
func searchPageParam(r *http.Request) (int, error) {
	s := r.URL.Query().Get(ui.PageParam)
	if s == "" {
		return 1, nil
	}
	page, err := strconv.Atoi(s)
	if err != nil || page < 1 || page > ui.MaxSearchPages {
		return 0, badRequest("%s %q must be a whole number from 1 to %d", ui.PageParam, s, ui.MaxSearchPages)
	}
	return page, nil
}

// home is what the search page shows without a query: how many documents
// there are to search, for the box's placeholder, and the latest run's
// largest interests, without their members. The home does without a read
// that fails: it is logged, and what it was for is left out.
func (h pageHandlers) home(r *http.Request) *ui.SearchHome {
	ctx := r.Context()
	home := &ui.SearchHome{}
	if st, err := h.d.stats(ctx); err != nil {
		h.quietError(r, err)
	} else {
		home.Searchable = st.DocumentsByState[string(store.DocStateFetched)]
	}
	interests, err := h.d.interests(ctx, interestsOpts{Limit: homeInterests})
	if err != nil {
		h.quietError(r, err)
		return home
	}
	home.AllInterests = interests.NumClusters
	for _, in := range interests.Items {
		home.Interests = append(home.Interests, interestView(in))
	}
	return home
}

func searchResults(resp SearchResponse) *ui.SearchResults {
	res := &ui.SearchResults{Degraded: resp.Degraded, Warnings: resp.Warnings, TookMS: resp.TookMS,
		Total: resp.Total, Capped: resp.Capped}
	for _, item := range resp.Items {
		hit := ui.SearchHit{DocumentID: item.Document.ID, Title: deref(item.Document.Title),
			BookmarkTitle: strings.TrimSpace(item.BookmarkTitle), URL: item.Document.URL,
			ContentType: item.Document.ContentType, Score: item.Score}
		for _, m := range item.Matches {
			hit.Matches = append(hit.Matches, ui.Match{Segments: ui.Passage(m.Snippet, m.Text), BM25: m.BM25Score,
				Vector: m.VectorScore})
		}
		res.Hits = append(res.Hits, hit)
	}
	return res
}
