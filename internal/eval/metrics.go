// Package eval is curio's retrieval-quality measurement harness. It scores a
// ranked list of retrieved document identifiers against a set of known-relevant
// ones, using the standard ranking metrics (recall@k, precision@k, NDCG@k, MRR).
//
// Relevance is binary (a document is relevant or not). The metrics take plain
// string identifiers (curio uses document URLs, which are stable across
// machines) so the package has no dependency on the store, HTTP, or search —
// it is pure, deterministic, and trivially unit-testable.
//
// This is the prerequisite the decisions log names before any "smarter search"
// work (M6): build the eval before the improvement, so a change can be shown to
// help rather than just feel better.
package eval

import "math"

// RecallAtK is the fraction of all relevant documents that appear in the top k.
func RecallAtK(ranked []string, relevant map[string]bool, k int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	k = min(k, len(ranked))
	hits := 0
	for i := range k {
		if relevant[ranked[i]] {
			hits++
		}
	}
	return float64(hits) / float64(len(relevant))
}

// PrecisionAtK is the fraction of the top k that is relevant.
func PrecisionAtK(ranked []string, relevant map[string]bool, k int) float64 {
	k = min(k, len(ranked))
	if k <= 0 {
		return 0
	}
	hits := 0
	for i := range k {
		if relevant[ranked[i]] {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// NDCGAtK is the normalized discounted cumulative gain at k with binary gains.
// The ideal ranking places min(k, |relevant|) relevant docs first.
func NDCGAtK(ranked []string, relevant map[string]bool, k int) float64 {
	if len(relevant) == 0 || k <= 0 {
		return 0
	}
	n := min(k, len(ranked))
	var dcg float64
	for i := range n {
		if relevant[ranked[i]] {
			dcg += 1.0 / math.Log2(float64(i+2))
		}
	}
	ideal := min(k, len(relevant))
	var idcg float64
	for i := range ideal {
		idcg += 1.0 / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// ReciprocalRank is 1/(rank of the first relevant document), or 0 if none is
// retrieved. Averaged across queries this is MRR.
func ReciprocalRank(ranked []string, relevant map[string]bool) float64 {
	for i, id := range ranked {
		if relevant[id] {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}
