// Package eval is curio's retrieval-quality measurement harness. It scores a
// ranked list of retrieved document identifiers against a query's judged
// ones, using the standard ranking metrics (recall@k, precision@k, nDCG@k,
// MRR, success@k).
//
// Relevance is graded: a judged document has a grade from 1 to MaxGrade,
// and every metric but nDCG counts any grade as relevant. The metrics take
// plain string identifiers (curio uses document URLs, which are stable
// across machines) so the package has no dependency on the store, HTTP, or
// search — it is pure, deterministic, and trivially unit-testable.
//
// `curio eval` runs it over the daemon's search, and cmd/searcheval over
// the search engine on a copy of a home's database, so a change to
// retrieval can be shown to help rather than just feel better.
package eval

import (
	"cmp"
	"math"
	"slices"
)

// Grades maps the identifiers judged relevant to a query to their grades,
// each from 1 to MaxGrade. An identifier it lacks is not relevant.
type Grades map[string]int

// AtLeast is the judgments of grade least or higher.
func (g Grades) AtLeast(least int) Grades {
	out := make(Grades, len(g))
	for id, grade := range g {
		if grade >= least {
			out[id] = grade
		}
	}
	return out
}

// RecallAtK is the fraction of the judged documents, of any grade, that
// appear in the top k.
func RecallAtK(ranked []string, grades Grades, k int) float64 {
	if len(grades) == 0 {
		return 0
	}
	k = min(k, len(ranked))
	hits := 0
	for i := range k {
		if grades[ranked[i]] > 0 {
			hits++
		}
	}
	return float64(hits) / float64(len(grades))
}

// PrecisionAtK is the fraction of the top k ranks that hold a judged
// document: hits / k, with ranks past the end of the list counting as misses
// (trec_eval's P@k). Dividing by the number retrieved instead would score a
// search that returns fewer results higher.
func PrecisionAtK(ranked []string, grades Grades, k int) float64 {
	if k <= 0 {
		return 0
	}
	hits := 0
	for _, id := range ranked[:min(k, len(ranked))] {
		if grades[id] > 0 {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// NDCGAtK is the normalized discounted cumulative gain at k, with a gain
// of 2^g-1 for a document of grade g: the DCG of the top k over the ideal
// DCG, that of every judgment sorted by grade, the ones the ranking never
// holds included, cut at k. With every grade 1 it is binary nDCG.
func NDCGAtK(ranked []string, grades Grades, k int) float64 {
	if len(grades) == 0 || k <= 0 {
		return 0
	}
	var dcg float64
	for i, id := range ranked[:min(k, len(ranked))] {
		if g := grades[id]; g > 0 {
			dcg += gain(g) / math.Log2(float64(i+2))
		}
	}
	ideal := make([]int, 0, len(grades))
	for _, g := range grades {
		ideal = append(ideal, g)
	}
	slices.SortFunc(ideal, func(a, b int) int { return cmp.Compare(b, a) })
	var idcg float64
	for i, g := range ideal[:min(k, len(ideal))] {
		idcg += gain(g) / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// gain is a grade's gain in nDCG.
func gain(grade int) float64 { return float64(int(1)<<grade - 1) }

// FirstRank is the 1-based rank of the first judged document, of any
// grade, or 0 if none is retrieved.
func FirstRank(ranked []string, grades Grades) int {
	for i, id := range ranked {
		if grades[id] > 0 {
			return i + 1
		}
	}
	return 0
}

// ReciprocalRank is 1/FirstRank, or 0 if no judged document is retrieved.
// Averaged across queries this is MRR.
func ReciprocalRank(ranked []string, grades Grades) float64 {
	if r := FirstRank(ranked, grades); r > 0 {
		return 1.0 / float64(r)
	}
	return 0
}

// SuccessAtK is 1 when a judged document, of any grade, is in the top k,
// and 0 otherwise.
func SuccessAtK(ranked []string, grades Grades, k int) float64 {
	if r := FirstRank(ranked, grades); r > 0 && r <= k {
		return 1
	}
	return 0
}
