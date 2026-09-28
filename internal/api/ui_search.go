package api

import (
	"net/http"
	"strings"

	"github.com/samsar/curio/internal/ui"
)

// matchExcerptRunes is how much of a chunk's text a match without a BM25
// snippet shows.
const matchExcerptRunes = 300

// search answers GET /ui/search?q=: the form, and for a query, the results
// of the same search POST /v1/search runs, at the default k. A search that
// fails is shown where its results would be, with its status.
func (h pageHandlers) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	vm := ui.Search{Layout: pageLayout("Search", ui.NavSearch), Query: q}
	status := http.StatusOK
	if strings.TrimSpace(q) != "" {
		resp, err := h.d.search(r.Context(), SearchRequest{Query: q})
		if err != nil {
			status, _ = errorStatus(err)
			vm.Err = h.panelError(r, err)
		} else {
			vm.Results = searchResults(resp)
		}
	}
	h.page(w, r, status, ui.PageSearch, vm)
}

func searchResults(resp SearchResponse) *ui.SearchResults {
	res := &ui.SearchResults{Degraded: resp.Degraded, Warnings: resp.Warnings, TookMS: resp.TookMS}
	for _, item := range resp.Items {
		hit := ui.SearchHit{DocumentID: item.Document.ID, Title: deref(item.Document.Title), URL: item.Document.URL,
			Score: item.Score}
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
