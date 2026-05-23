package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFuse_SingleList(t *testing.T) {
	out := Fuse([][]RankedItem{
		{{"a", 1}, {"b", 2}, {"c", 3}},
	}, nil, 60)
	assert.Equal(t, []string{"a", "b", "c"}, ids(out))
}

func TestFuse_TwoLists_OverlapBoosts(t *testing.T) {
	// "b" appears in both lists, "a" and "c" only in one.
	out := Fuse([][]RankedItem{
		{{"a", 1}, {"b", 2}, {"c", 3}},
		{{"b", 1}, {"c", 2}, {"a", 3}},
	}, nil, 60)
	// "b" should top (1/62 + 1/61) > "a" (1/61 + 1/63) > "c" (1/63 + 1/62).
	assert.Equal(t, []string{"b", "a", "c"}, ids(out))
}

func TestFuse_Weights(t *testing.T) {
	// Same lists, but second retriever gets 10x weight → its top item wins.
	out := Fuse([][]RankedItem{
		{{"a", 1}, {"b", 2}},
		{{"b", 1}, {"a", 2}},
	}, []float64{1.0, 10.0}, 60)
	assert.Equal(t, "b", out[0].ID, "high-weight retriever's #1 should dominate")
}

func TestFuse_ItemOnlyInOneList(t *testing.T) {
	out := Fuse([][]RankedItem{
		{{"a", 1}},
		{{"b", 1}},
	}, nil, 60)
	assert.Len(t, out, 2)
}

func TestFuse_EmptyInputs(t *testing.T) {
	assert.Empty(t, Fuse(nil, nil, 60))
	assert.Empty(t, Fuse([][]RankedItem{nil, nil}, nil, 60))
}

func TestFuse_DefaultKWhenZero(t *testing.T) {
	// k<=0 should default to 60 rather than dividing by zero.
	out := Fuse([][]RankedItem{{{"a", 1}}}, nil, 0)
	require := assert.New(t)
	require.Len(out, 1)
	require.Greater(out[0].Score, 0.0)
}

func TestFuse_DeterministicTiebreak(t *testing.T) {
	// Two items with identical scores must always sort by ID for stable output.
	out := Fuse([][]RankedItem{
		{{"b", 1}, {"a", 1}},
	}, nil, 60)
	// Both have score 1/61. Tiebreak by ID ascending.
	assert.Equal(t, []string{"a", "b"}, ids(out))
}

func ids(s []ScoredID) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.ID
	}
	return out
}
