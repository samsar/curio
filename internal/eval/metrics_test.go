package eval

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rel judges ids relevant at grade 1.
func rel(ids ...string) Grades {
	m := make(Grades, len(ids))
	for _, id := range ids {
		m[id] = 1
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

	// Fewer results than k: the missing ranks are misses, not ignored.
	assert.InDelta(t, 0.1, PrecisionAtK([]string{"a"}, rel("a"), 10), 1e-9)
	assert.InDelta(t, 0.0, PrecisionAtK(nil, rel("a"), 10), 1e-9)
	assert.InDelta(t, 0.0, PrecisionAtK(ranked, r, 0), 1e-9)
	assert.InDelta(t, 0.0, PrecisionAtK(ranked, r, -1), 1e-9)
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
		{Query: "q0", Relevant: []Judgment{{URL: "a", Grade: 1}}},
		{Query: "q1", Relevant: []Judgment{{URL: "x", Grade: 1}}},
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

// binaryNDCG is nDCG@k with binary gains, as curio eval computed it before
// grades: the reference graded nDCG must equal at grade 1.
func binaryNDCG(ranked []string, relevant map[string]bool, k int) float64 {
	if len(relevant) == 0 || k <= 0 {
		return 0
	}
	var dcg float64
	for i := range min(k, len(ranked)) {
		if relevant[ranked[i]] {
			dcg += 1.0 / math.Log2(float64(i+2))
		}
	}
	var idcg float64
	for i := range min(k, len(relevant)) {
		idcg += 1.0 / math.Log2(float64(i+2))
	}
	return dcg / idcg
}

// TestNDCGAtK_GradeOneIsBinary: with every judgment at grade 1, graded
// nDCG is the binary nDCG curio eval always reported, over random
// rankings, judgment sets and cutoffs.
func TestNDCGAtK_GradeOneIsBinary(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range 2000 {
		perm := rng.Perm(30)[:rng.IntN(31)]
		ranked := make([]string, 0, len(perm))
		for _, i := range perm {
			ranked = append(ranked, strconv.Itoa(i))
		}
		relevant := map[string]bool{}
		grades := Grades{}
		for range rng.IntN(12) {
			id := strconv.Itoa(rng.IntN(40))
			relevant[id], grades[id] = true, 1
		}
		k := rng.IntN(25)
		assert.InDelta(t, binaryNDCG(ranked, relevant, k), NDCGAtK(ranked, grades, k), 1e-12,
			"trial %d: k %d, ranked %v, relevant %v", trial, k, ranked, relevant)
	}
}

// TestNDCGAtK_Graded: a grade g gains 2^g-1, and the ideal ranking sorts
// every judgment by grade, the ones never ranked included, so a judgment
// the ranking misses lowers nDCG as it lowers recall.
func TestNDCGAtK_Graded(t *testing.T) {
	ranked := []string{"a", "b", "c", "d"}
	// DCG@3 = (2^1-1)/log2(2) + 0 + (2^2-1)/log2(4) = 1 + 3/2 = 2.5.
	// Ideal gains, sorted: 3 (c), 1 (a); IDCG@3 = 3/log2(2) + 1/log2(3)
	// = 3 + 0.6309297535714574 = 3.6309297535714574.
	assert.InDelta(t, 2.5/3.6309297535714574, NDCGAtK(ranked, Grades{"a": 1, "c": 2}, 3), 1e-12)
	assert.InDelta(t, 1.0, RecallAtK(ranked, Grades{"a": 1, "c": 2}, 3), 1e-12)

	// e, grade 2, is never ranked: DCG@3 is still 2.5, and the ideal gains
	// are 3, 3, 1: IDCG@3 = 3 + 3/log2(3) + 1/log2(4)
	// = 3 + 1.8927892607143721 + 0.5 = 5.392789260714372.
	missed := Grades{"a": 1, "c": 2, "e": 2}
	assert.InDelta(t, 2.5/5.392789260714372, NDCGAtK(ranked, missed, 3), 1e-12)
	assert.InDelta(t, 2.0/3, RecallAtK(ranked, missed, 3), 1e-12)

	// The grade-2 document first is the ideal ranking.
	assert.InDelta(t, 1.0, NDCGAtK([]string{"c", "a"}, Grades{"a": 1, "c": 2}, 10), 1e-12)
}

func TestFirstRankAndSuccess(t *testing.T) {
	ranked := []string{"x1", "x2", "x3", "x4", "x5", "a", "x6"}
	g := Grades{"a": 2, "z": 1}
	assert.Equal(t, 6, FirstRank(ranked, g))
	assert.InDelta(t, 1.0/6, ReciprocalRank(ranked, g), 1e-12)
	assert.Zero(t, SuccessAtK(ranked, g, 1))
	assert.Zero(t, SuccessAtK(ranked, g, 5))
	assert.InDelta(t, 1.0, SuccessAtK(ranked, g, 10), 0)

	none := Grades{"z": 1}
	assert.Zero(t, FirstRank(ranked, none))
	assert.Zero(t, ReciprocalRank(ranked, none))
	for _, k := range []int{1, 5, 10} {
		assert.Zero(t, SuccessAtK(ranked, none, k), "k %d", k)
	}
}

func TestScoreQuery(t *testing.T) {
	ranked := make([]string, 0, 100)
	for i := range 100 {
		ranked = append(ranked, strconv.Itoa(i))
	}
	q := Query{Query: "q", Relevant: []Judgment{{URL: "5", Grade: 2}, {URL: "50", Grade: 1}, {URL: "x", Grade: 2}}}
	s := ScoreQuery(q, ranked)
	assert.InDelta(t, 1.0/3, s.RecallAt10, 1e-12)
	assert.InDelta(t, 2.0/3, s.RecallAt100, 1e-12)
	require.NotNil(t, s.HighRecallAt10)
	assert.InDelta(t, 0.5, *s.HighRecallAt10, 1e-12, "5 of the grade-2 judgments 5 and x")
	assert.Equal(t, 6, s.FirstRank)
	assert.InDelta(t, 1.0/6, s.RR, 1e-12)
	assert.Equal(t, []float64{0, 0, 1}, []float64{s.SuccessAt1, s.SuccessAt5, s.SuccessAt10})
	// DCG@10 = 3/log2(7); IDCG@10 = 3/log2(2) + 3/log2(3) + 1/log2(4).
	assert.InDelta(t, (3/math.Log2(7))/(3+3/math.Log2(3)+0.5), s.NDCGAt10, 1e-12)

	low := ScoreQuery(Query{Query: "q", Relevant: []Judgment{{URL: "5", Grade: 1}}}, ranked)
	assert.Nil(t, low.HighRecallAt10, "no grade-2 judgment, no high recall")
}

func TestGroups(t *testing.T) {
	qs := &QuerySet{Queries: []Query{
		{ID: "a", Kind: "topic", Tags: []string{"owner", "recent"}},
		{ID: "b"},
		{ID: "c", Kind: "keyword", Tags: []string{"topic"}},
		{ID: "d", Kind: "topic", Tags: []string{"owner"}},
	}}
	groups := Groups(qs)
	got := make([]string, 0, len(groups))
	for _, g := range groups {
		got = append(got, fmt.Sprintf("%s %s %v", g.Type, g.Name, g.Queries))
	}
	assert.Equal(t, []string{
		"all  [0 1 2 3]",
		"kind topic [0 3]",
		"kind keyword [2]",
		"tag owner [0 3]",
		"tag recent [0]",
		"tag topic [2]",
	}, got, "a kind and a tag of one name are two groups; a query without a kind is in none")
}

func TestSummarize(t *testing.T) {
	half, one := 0.5, 1.0
	scores := []QueryScore{
		{RecallAt10: 1, RecallAt100: 1, HighRecallAt10: &one, NDCGAt10: 0.8, RR: 1, SuccessAt1: 1, SuccessAt5: 1, SuccessAt10: 1},
		{RecallAt10: 0, RecallAt100: 0.5, NDCGAt10: 0, RR: 0, FirstRank: 0},
		{RecallAt10: 0.5, RecallAt100: 1, HighRecallAt10: &half, NDCGAt10: 0.4, RR: 0.25, SuccessAt10: 1},
	}
	s := Summarize(Group{Type: GroupTag, Name: "owner", Queries: []int{0, 1, 2}}, scores)
	assert.Equal(t, GroupTag, s.Type)
	assert.Equal(t, "owner", s.Name)
	assert.Equal(t, 3, s.N)
	assert.Equal(t, 2, s.NHigh)
	require.NotNil(t, s.HighRecallAt10)
	assert.InDelta(t, 0.75, *s.HighRecallAt10, 1e-12, "the mean over the queries that have one")
	assert.InDelta(t, 0.5, s.RecallAt10, 1e-12)
	assert.InDelta(t, 2.5/3, s.RecallAt100, 1e-12)
	assert.InDelta(t, 0.4, s.NDCGAt10, 1e-12)
	assert.InDelta(t, 1.25/3, s.MRR, 1e-12)
	assert.InDelta(t, 1.0/3, s.SuccessAt1, 1e-12)
	assert.InDelta(t, 1.0/3, s.SuccessAt5, 1e-12)
	assert.InDelta(t, 2.0/3, s.SuccessAt10, 1e-12)

	none := Summarize(Group{Type: GroupKind, Name: "x", Queries: []int{1}}, scores)
	assert.Zero(t, none.NHigh)
	assert.Nil(t, none.HighRecallAt10)
	raw, err := json.Marshal(none)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"high_recall_at_10":null`)
}
