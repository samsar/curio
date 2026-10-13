package search

import (
	"cmp"
	"slices"
	"strings"

	"github.com/samsar/curio/internal/store"
)

// documentListDepth is how many documents each document list reads,
// whatever the request's window: twice the most a ranking holds, so a
// document a list ranks just past a window still adds to its score, and
// every window of a query is cut from one ranking.
const documentListDepth = 2 * store.MaxSearchK

// documentList is a list that ranks whole documents, not chunks: the
// fields list or the cards list, its weight, its hits best first, and
// whether it is semantic, read with the query's embedding, which a
// degraded search drops.
type documentList struct {
	weight   float64
	hits     []store.DocumentHit
	semantic bool
}

// addDocumentLists adds each list's reciprocal rank, weight/(k + rank),
// rank 1-based, to its documents in ranked, the documents the chunk lists
// ranked, in list order; a document only a list found joins ranked with
// no chunks. A list weighted 0 adds nothing. Sums are taken in a fixed
// order (the chunks' collapsed score, then each list in order) and the
// result sorted by score, then document ID, so a ranking computed again
// for every page puts every document in the same place.
func addDocumentLists(ranked []docScore, lists []documentList, k int) []docScore {
	at := make(map[string]int, len(ranked))
	for i, d := range ranked {
		at[d.documentID] = i
	}
	for _, l := range lists {
		if l.weight == 0 {
			continue
		}
		for rank, h := range l.hits {
			add := l.weight / float64(k+rank+1)
			if i, ok := at[h.DocumentID]; ok {
				ranked[i].score += add
				continue
			}
			at[h.DocumentID] = len(ranked)
			ranked = append(ranked, docScore{documentID: h.DocumentID, score: add})
		}
	}
	slices.SortFunc(ranked, func(a, b docScore) int {
		return cmp.Or(cmp.Compare(b.score, a.score), strings.Compare(a.documentID, b.documentID))
	})
	return ranked
}
