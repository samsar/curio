package insight

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight/louvain"
)

// serialNeighbors is the reference the parallel heap build must match: every
// candidate at or above the threshold, fully sorted (weight desc, index asc),
// the first k kept.
func serialNeighbors(vecs [][]float32, k int, minSim float64) [][]louvain.Edge {
	out := make([][]louvain.Edge, len(vecs))
	for i := range vecs {
		cands := []louvain.Edge{}
		for j := range vecs {
			if j == i {
				continue
			}
			if w := dot(vecs[i], vecs[j]); w >= minSim {
				cands = append(cands, louvain.Edge{To: j, Weight: w})
			}
		}
		slices.SortFunc(cands, func(a, b louvain.Edge) int {
			if c := cmp.Compare(b.Weight, a.Weight); c != 0 {
				return c
			}
			return cmp.Compare(a.To, b.To)
		})
		out[i] = cands[:min(k, len(cands))]
	}
	return out
}

func TestKNNNeighbors_MatchesSerialReference(t *testing.T) {
	pts := syntheticCorpus(3, 600, 16, 10, 1.0)
	// Exact duplicates tie on weight, so the index tie-break is exercised.
	for i := range 40 {
		pts = append(pts, Point{ID: fmt.Sprintf("dup-%02d", i), Vector: pts[i*7].Vector})
	}
	vecs := make([][]float32, len(pts))
	for i, p := range pts {
		vecs[i] = p.Vector
	}
	for _, k := range []int{1, 5, 10, 50} {
		t.Run(fmt.Sprint("k=", k), func(t *testing.T) {
			got, err := knnNeighbors(context.Background(), vecs, k, 0.3)
			require.NoError(t, err)
			want := serialNeighbors(vecs, k, 0.3)
			for i := range want {
				require.Equal(t, want[i], got[i], "row %d", i)
			}
		})
	}
}

// TestKNNGraph_IsTheClusterersGraph: the exported graph is the union of
// the serial reference's top-k lists, symmetric, weighted by the larger
// similarity, each row ordered by neighbour index.
func TestKNNGraph_IsTheClusterersGraph(t *testing.T) {
	pts := syntheticCorpus(4, 300, 16, 8, 1.0)
	got, err := KNNGraph(context.Background(), pts, 7, 0.3)
	require.NoError(t, err)
	want := unionGraph(serialNeighbors(vectorsOf(pts), 7, 0.3))
	require.Equal(t, want, got)
	for i, row := range got {
		require.True(t, slices.IsSortedFunc(row, func(a, b louvain.Edge) int { return cmp.Compare(a.To, b.To) }), "row %d", i)
		for _, e := range row {
			back, ok := slices.BinarySearchFunc(got[e.To], i, func(b louvain.Edge, to int) int { return cmp.Compare(b.To, to) })
			require.True(t, ok, "edge %d-%d listed from both ends", i, e.To)
			require.Equal(t, e.Weight, got[e.To][back].Weight)
		}
	}
	_, err = louvain.NewGraph(got)
	require.NoError(t, err, "a graph louvain accepts")

	empty, err := KNNGraph(context.Background(), nil, 7, 0.3)
	require.NoError(t, err)
	assert.Empty(t, empty)
	_, err = KNNGraph(context.Background(), []Point{{ID: "x", Vector: []float32{2, 0}}}, 7, 0.3)
	require.Error(t, err, "not unit length")
}

// TestNeighbourLists_DeriveBothGraphs: the one top-20 pass yields, edge for
// edge, the graphs two separate builds would. Exact duplicates tie on
// weight, so the index tie-break is exercised.
func TestNeighbourLists_DeriveBothGraphs(t *testing.T) {
	pts := syntheticCorpus(5, 500, 16, 10, 1.0)
	for i := range 40 {
		pts = append(pts, Point{ID: fmt.Sprintf("dup-%02d", i), Vector: pts[i*11].Vector})
	}
	lists, err := nearestNeighbours(context.Background(), vectorsOf(pts))
	require.NoError(t, err)

	area, err := KNNGraph(context.Background(), pts, areaK, areaMinSimilarity)
	require.NoError(t, err)
	assert.Equal(t, area, lists.areaGraph())
	flat, err := KNNGraph(context.Background(), pts, graphK, math.SmallestNonzeroFloat64)
	require.NoError(t, err)
	assert.Equal(t, flat, lists.flatGraph())
	for _, row := range flat {
		for _, e := range row {
			require.Positive(t, e.Weight)
		}
	}
}

func TestNearestNeighbours_Canceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := nearestNeighbours(ctx, vectorsOf(syntheticCorpus(1, 200, 8, 4, 0.5)))
	require.ErrorIs(t, err, context.Canceled)
}
