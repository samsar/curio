package eval

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func rel(ids ...string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func TestRecallAtK(t *testing.T) {
	ranked := []string{"a", "b", "c", "d"}
	r := rel("a", "c")
	assert.InDelta(t, 1.0, RecallAtK(ranked, r, 4), 1e-9)
	assert.InDelta(t, 0.5, RecallAtK(ranked, r, 2), 1e-9) // only "a" in top 2
	assert.InDelta(t, 0.0, RecallAtK(ranked, rel(), 4), 1e-9)
}

func TestPrecisionAtK(t *testing.T) {
	ranked := []string{"a", "b", "c", "d"}
	r := rel("a", "c")
	assert.InDelta(t, 0.5, PrecisionAtK(ranked, r, 2), 1e-9) // 1 of 2
	assert.InDelta(t, 0.5, PrecisionAtK(ranked, r, 4), 1e-9) // 2 of 4
}

func TestNDCGAtK(t *testing.T) {
	ranked := []string{"a", "b", "c", "d"}
	r := rel("a", "c")
	// dcg = 1/log2(2) + 1/log2(4) = 1 + 0.5 = 1.5
	// idcg = 1/log2(2) + 1/log2(3) = 1 + 0.63093 = 1.63093
	assert.InDelta(t, 1.5/1.6309297535714573, NDCGAtK(ranked, r, 4), 1e-9)

	// Perfect ranking scores 1.0.
	assert.InDelta(t, 1.0, NDCGAtK([]string{"a", "c", "b"}, r, 3), 1e-9)
	assert.InDelta(t, 0.0, NDCGAtK(ranked, rel(), 4), 1e-9)
}

func TestReciprocalRank(t *testing.T) {
	assert.InDelta(t, 1.0, ReciprocalRank([]string{"a", "b"}, rel("a")), 1e-9)
	assert.InDelta(t, 0.5, ReciprocalRank([]string{"x", "a"}, rel("a")), 1e-9)
	assert.InDelta(t, 0.0, ReciprocalRank([]string{"x", "y"}, rel("a")), 1e-9)
}

func TestEvaluate(t *testing.T) {
	qs := &QuerySet{Queries: []Query{
		{Query: "q0", Relevant: []string{"a"}},
		{Query: "q1", Relevant: []string{"x"}},
	}}
	ranked := [][]string{
		{"a", "b"}, // perfect for q0
		{"y", "z"}, // miss for q1
	}
	rep := Evaluate(qs, ranked, 10)
	assert.Equal(t, 10, rep.K)
	assert.Len(t, rep.Results, 2)
	assert.InDelta(t, 0.5, rep.MeanRecall, 1e-9)
	assert.InDelta(t, 0.5, rep.MeanNDCG, 1e-9)
	assert.InDelta(t, 0.5, rep.MRR, 1e-9)
}
