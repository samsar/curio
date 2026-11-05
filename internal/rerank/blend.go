package rerank

import (
	"cmp"
	"slices"
)

// judgement is a method's reading of the candidates it was shown, in
// their first-stage order: each one's place in the reranker's order (from
// 1) and its score in (0, 1], or unscored, which keeps its place.
type judgement struct {
	place  []int
	score  []float64
	scored []bool
	// bounded counts the pointwise scores one side of which was missing
	// from the provider's top tokens, and so bounded by the least likely
	// of them.
	bounded int
}

// unscored counts the candidates j couldn't score.
func (j judgement) unscored() int {
	n := 0
	for _, ok := range j.scored {
		if !ok {
			n++
		}
	}
	return n
}

// The position blend's weights of the first ranking: its top three are
// trusted most, the rest of the first page less, the rest least.
const (
	topWeight  = 0.75
	pageWeight = 0.60
	restWeight = 0.40
	// rrfK is RRF's constant, the search engine's.
	rrfK = 60
)

// positionWeight is the position blend's weight of the first ranking at
// rank r.
func positionWeight(r int) float64 {
	switch {
	case r <= 3:
		return topWeight
	case r <= 10:
		return pageWeight
	}
	return restWeight
}

// blendKey is a scored candidate's key under b, higher first: rank is its
// first-stage rank, place and score the reranker's.
func blendKey(b Blend, rank, place int, score float64) float64 {
	switch b {
	case BlendPosition:
		w := positionWeight(rank)
		return w/float64(rank) + (1-w)*score
	case BlendRRF:
		return 1/float64(rrfK+rank) + 1/float64(rrfK+place)
	case BlendPure:
	}
	return score
}

// arrange is cands, a ranking's head in its first-stage order, as the
// reranker leaves it: each candidate pinned keeps its place, and the
// others fill the places left in order of key, highest first, ties by
// first-stage rank.
func arrange(cands []Candidate, pinned []bool, key []float64) []Candidate {
	var free []int
	for i := range cands {
		if !pinned[i] {
			free = append(free, i)
		}
	}
	moved := slices.Clone(free)
	slices.SortStableFunc(moved, func(a, b int) int {
		if c := cmp.Compare(key[b], key[a]); c != 0 {
			return c
		}
		return cmp.Compare(cands[a].Rank, cands[b].Rank)
	})
	out := slices.Clone(cands)
	for slot, from := range moved {
		out[free[slot]] = cands[from]
	}
	return out
}

// reorder is cands as j, the judgement of shown (cands less those held,
// in order), and b leave them: held and unscored candidates keep their
// places, the others are arranged by their blended keys.
func reorder(cands []Candidate, held []bool, j judgement, b Blend) []Candidate {
	pinned := make([]bool, len(cands))
	key := make([]float64, len(cands))
	k := 0
	for i := range cands {
		if held[i] {
			pinned[i] = true
			continue
		}
		if !j.scored[k] {
			pinned[i] = true
		} else {
			key[i] = blendKey(b, cands[i].Rank, j.place[k], j.score[k])
		}
		k++
	}
	return arrange(cands, pinned, key)
}

// placesByScore are the places of scores' scored entries in descending
// order of score, ties by their order, from 1; 0 for an unscored one.
func placesByScore(score []float64, scored []bool) []int {
	var idx []int
	for i, ok := range scored {
		if ok {
			idx = append(idx, i)
		}
	}
	slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(score[b], score[a]) })
	place := make([]int, len(score))
	for p, i := range idx {
		place[i] = p + 1
	}
	return place
}
