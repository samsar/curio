package api

import (
	"net/http"
	"strings"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// matchExcerptRunes is how much of a chunk's text a match without a BM25
// snippet shows.
const matchExcerptRunes = 300

// search answers GET /ui/?q=&content_type=, the dashboard's home: the form,
// and for a query, the results of the same search POST /v1/search runs, at
// the default k, limited to the content type when one is given. A search
// that fails is shown where its results would be, with its status. A type
// that isn't one is a 400 page, as the Library answers it.
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
	} else {
		req := SearchRequest{Query: q}
		if contentType != "" {
			req.Filters.ContentType = []string{string(contentType)}
		}
		resp, err := h.d.search(r.Context(), req)
		if err != nil {
			status, vm.Err = h.reportPanel(r, err)
		} else {
			vm.Results = searchResults(resp)
		}
	}
	h.page(w, r, status, ui.PageSearch, vm)
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
	res := &ui.SearchResults{Degraded: resp.Degraded, Warnings: resp.Warnings, TookMS: resp.TookMS}
	for _, item := range resp.Items {
		hit := ui.SearchHit{DocumentID: item.Document.ID, Title: deref(item.Document.Title), URL: item.Document.URL,
			ContentType: item.Document.ContentType, Score: item.Score}
		for _, m := range item.Matches {
			hit.Matches = append(hit.Matches, ui.Match{Segments: matchSegments(m), BM25: m.BM25Score, Vector: m.VectorScore})
		}
		res.Hits = append(res.Hits, hit)
	}
	return res
}

// matchSegments is what a match shows: its BM25 snippet with the matched
// terms marked, or, for a match only the vector search found, the start of
// its text.
func matchSegments(m ChunkMatchJSON) []ui.Segment {
	if m.Snippet != "" {
		return ui.Highlight(m.Snippet)
	}
	return []ui.Segment{{Text: ui.Excerpt(m.Text, matchExcerptRunes)}}
}
