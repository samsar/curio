// Package search runs the hybrid BM25 + vector retrieval and fuses results
// via Reciprocal Rank Fusion (RRF).
//
// RRF reference: Cormack, Clarke, Buettcher — "Reciprocal Rank Fusion
// outperforms Condorcet and individual Rank Learning Methods" (SIGIR 2009).
//
// The classic formula is:
//
//	score(d) = Σ over retrievers r of  weight_r / (k + rank_r(d))
//
// where k=60 is the standard smoothing constant and rank starts at 1.
// Items absent from a retriever's list contribute zero for that retriever.
package search

import (
	"sort"
)

// ScoredID is one item with its fused score. Caller-supplied ranked lists
// are converted to RankedItems before being fused.
type ScoredID struct {
	ID    string
	Score float64
}

// RankedItem represents an item's position in a single retriever's output.
// Rank is 1-based.
type RankedItem struct {
	ID   string
	Rank int
}

// Fuse merges any number of ranked retriever outputs into a single score-
// ordered list using Reciprocal Rank Fusion. Items appearing in multiple
// retrievers accumulate score across them.
//
//   - weights:  one float per ranked list; nil means all weights = 1.0
//   - k:        smoothing constant; pass 60 unless you have a reason
//
// Output is sorted by score descending (higher = better).
func Fuse(rankedLists [][]RankedItem, weights []float64, k int) []ScoredID {
	if k <= 0 {
		k = 60
	}
	if weights == nil {
		weights = make([]float64, len(rankedLists))
		for i := range weights {
			weights[i] = 1.0
		}
	}

	scores := make(map[string]float64)
	for li, list := range rankedLists {
		var w float64 = 1.0
		if li < len(weights) {
			w = weights[li]
		}
		for _, item := range list {
			if item.Rank < 1 {
				continue
			}
			scores[item.ID] += w / float64(k+item.Rank)
		}
	}

	out := make([]ScoredID, 0, len(scores))
	for id, s := range scores {
		out = append(out, ScoredID{ID: id, Score: s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// Deterministic tiebreak.
		return out[i].ID < out[j].ID
	})
	return out
}
