package retrieve

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

func hitsOf(ids ...string) []search.Hit {
	out := make([]search.Hit, len(ids))
	for i, id := range ids {
		out[i] = search.Hit{Document: &store.Document{ID: id}}
	}
	return out
}

func idsOf(hits []search.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Document.ID
	}
	return out
}

// TestReorderHits: an order lays out the head, the rest following in the
// ranking's order; an ID the ranking doesn't hold is counted stale.
func TestReorderHits(t *testing.T) {
	out, stale := reorderHits(hitsOf("a", "b", "c", "d"), []string{"c", "a", "x"})
	assert.Equal(t, []string{"c", "a", "b", "d"}, idsOf(out))
	assert.Equal(t, 1, stale)
	assert.Equal(t, []string{"c", "a"}, headIDs(out, 2))
	assert.Equal(t, []string{"c", "a", "b", "d"}, headIDs(out, 9), "at most the hits")
}

// TestWindows: a reranked head is read from the top, past the window and
// the head both, and the window cut from the reordered hits.
func TestWindows(t *testing.T) {
	sreq := search.Request{K: 10, Offset: 20}
	assert.Equal(t, sreq, rerankWindow(sreq, rerankPlan{}, 10), "no rerank: the window as asked")
	assert.Equal(t, search.Request{K: 30, Offset: 0}, rerankWindow(sreq, rerankPlan{depth: 30}, 10))
	assert.Equal(t, search.Request{K: 30, Offset: 0},
		rerankWindow(search.Request{K: 10}, rerankPlan{depth: 30}, 10), "the head deeper than the window")
	hits := hitsOf("a", "b", "c")
	assert.Equal(t, []string{"b", "c"}, idsOf(hitWindow(hits, 1, 5)))
	assert.Empty(t, hitWindow(hits, 3, 5))
}
