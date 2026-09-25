package insight

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unitPoints returns a copy of pts with every vector normalized, since the
// clusterer expects prepared unit vectors.
func unitPoints(pts []Point) []Point {
	out := make([]Point, len(pts))
	for i, p := range pts {
		out[i] = Point{ID: p.ID, Vector: unitResidual(p.Vector, nil)}
	}
	return out
}

// twoGroupsPlusOutlier builds points: 4 near basis e0, 3 near e1, 1 near e3.
// The two groups are internally near-parallel (cosine ~1) and mutually
// orthogonal (cosine ~0), and the outlier is orthogonal to everything.
func twoGroupsPlusOutlier() []Point {
	return unitPoints([]Point{
		{ID: "a1", Vector: []float32{1, 0.10, 0, 0}},
		{ID: "a2", Vector: []float32{1, 0.05, 0, 0}},
		{ID: "a3", Vector: []float32{0.9, 0, 0.05, 0}},
		{ID: "a4", Vector: []float32{1, 0, 0.10, 0}},
		{ID: "b1", Vector: []float32{0, 1, 0.10, 0}},
		{ID: "b2", Vector: []float32{0, 1, 0, 0}},
		{ID: "b3", Vector: []float32{0, 0.9, 0, 0.05}},
		{ID: "out", Vector: []float32{0, 0, 0, 1}},
	})
}

// syntheticCorpus returns n unit vectors scattered around `centers` random
// directions. With noise near 1 the groups overlap at the default 0.5
// threshold, which is where tie-breaks and visiting order matter. Seeded, so
// every call with the same arguments returns the same corpus.
func syntheticCorpus(seed uint64, n, dim, centers int, noise float64) []Point {
	r := rand.New(rand.NewPCG(seed, 0))
	cs := make([][]float64, centers)
	for c := range cs {
		cs[c] = make([]float64, dim)
		for d := range cs[c] {
			cs[c][d] = r.NormFloat64()
		}
	}
	pts := make([]Point, n)
	for i := range pts {
		c := cs[r.IntN(centers)]
		v := make([]float32, dim)
		for d := range v {
			v[d] = float32(c[d] + noise*r.NormFloat64())
		}
		pts[i] = Point{ID: fmt.Sprintf("doc-%05d", i), Vector: v}
	}
	return unitPoints(pts)
}

func TestKNNGraphClusterer_TwoClustersAndNoise(t *testing.T) {
	c := NewKNNGraphClusterer(KNNGraphOptions{}) // defaults: K=10, minSim=0.5, minSize=3
	labels, err := c.Cluster(context.Background(), twoGroupsPlusOutlier())
	require.NoError(t, err)
	require.Len(t, labels, 8)

	// First four share a cluster; next three share a different one.
	groupA := labels[0]
	assert.NotEqual(t, NoiseLabel, groupA)
	for i := 1; i < 4; i++ {
		assert.Equal(t, groupA, labels[i], "a%d should share group A", i+1)
	}
	groupB := labels[4]
	assert.NotEqual(t, NoiseLabel, groupB)
	assert.NotEqual(t, groupA, groupB)
	for i := 5; i < 7; i++ {
		assert.Equal(t, groupB, labels[i], "b%d should share group B", i-3)
	}
	// The outlier is noise (its own singleton < min_cluster_size).
	assert.Equal(t, NoiseLabel, labels[7])

	// Exactly two clusters; the larger (A, size 4) is label 0.
	assert.Equal(t, 0, groupA)
	assert.Equal(t, 1, groupB)
}

func TestKNNGraphClusterer_Deterministic(t *testing.T) {
	c := NewKNNGraphClusterer(KNNGraphOptions{})
	pts := twoGroupsPlusOutlier()
	first, err := c.Cluster(context.Background(), pts)
	require.NoError(t, err)
	for range 5 {
		again, err := c.Cluster(context.Background(), pts)
		require.NoError(t, err)
		assert.Equal(t, first, again, "clustering must be deterministic across runs")
	}
}

// TestKNNGraphClusterer_InputOrderInvariant: shuffling the input must not
// change which cluster any point lands in. Label propagation is order-
// sensitive, so the clusterer works in ID order internally.
func TestKNNGraphClusterer_InputOrderInvariant(t *testing.T) {
	pts := syntheticCorpus(7, 1200, 32, 25, 1.0)
	c := NewKNNGraphClusterer(KNNGraphOptions{})

	want := labelsByID(t, c, pts)
	require.Greater(t, distinctClusters(slicesValues(want)), 1, "the corpus must actually cluster")

	r := rand.New(rand.NewPCG(99, 0))
	for i := range 5 {
		shuffled := slices.Clone(pts)
		r.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		assert.Equal(t, want, labelsByID(t, c, shuffled), "shuffle %d changed the partition", i)
	}
}

func labelsByID(t *testing.T, c Clusterer, pts []Point) map[string]int {
	t.Helper()
	labels, err := c.Cluster(context.Background(), pts)
	require.NoError(t, err)
	out := make(map[string]int, len(pts))
	for i, p := range pts {
		out[p.ID] = labels[i]
	}
	return out
}

func slicesValues(m map[string]int) []int {
	out := make([]int, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// serialNeighbors is the reference the parallel heap build must match: every
// candidate at or above the threshold, fully sorted (weight desc, index asc),
// the first k kept.
func serialNeighbors(vecs [][]float32, k int, minSim float64) [][]edge {
	out := make([][]edge, len(vecs))
	for i := range vecs {
		cands := []edge{}
		for j := range vecs {
			if j == i {
				continue
			}
			if w := dot(vecs[i], vecs[j]); w >= minSim {
				cands = append(cands, edge{to: j, w: w})
			}
		}
		slices.SortFunc(cands, func(a, b edge) int {
			if c := cmp.Compare(b.w, a.w); c != 0 {
				return c
			}
			return cmp.Compare(a.to, b.to)
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

func TestKNNGraphClusterer_Canceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewKNNGraphClusterer(KNNGraphOptions{}).Cluster(ctx, syntheticCorpus(1, 200, 8, 4, 0.5))
	require.ErrorIs(t, err, context.Canceled)
}

func TestKNNGraphClusterer_Empty(t *testing.T) {
	c := NewKNNGraphClusterer(KNNGraphOptions{})
	labels, err := c.Cluster(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, labels)
}

func TestKNNGraphClusterer_AllNoiseBelowMinSize(t *testing.T) {
	// Two well-separated points but min_cluster_size 3 → nothing survives.
	c := NewKNNGraphClusterer(KNNGraphOptions{MinClusterSize: 3})
	labels, err := c.Cluster(context.Background(), unitPoints([]Point{
		{ID: "x", Vector: []float32{1, 0}},
		{ID: "y", Vector: []float32{1, 0.01}},
	}))
	require.NoError(t, err)
	for _, l := range labels {
		assert.Equal(t, NoiseLabel, l)
	}
}

// distinctClusters counts unique non-noise labels.
func distinctClusters(labels []int) int {
	seen := map[int]bool{}
	for _, l := range labels {
		if l != NoiseLabel {
			seen[l] = true
		}
	}
	return len(seen)
}

func TestKNNGraphClusterer_RejectsBadVectors(t *testing.T) {
	cases := []struct {
		name string
		pts  []Point
	}{
		{"dim mismatch", []Point{{ID: "x", Vector: []float32{1, 0}}, {ID: "y", Vector: []float32{1, 0, 0}}}},
		{"empty vector", []Point{{ID: "x", Vector: []float32{}}}},
		{"not unit length", []Point{{ID: "x", Vector: []float32{1, 1}}}},
		{"NaN", []Point{{ID: "x", Vector: []float32{float32(math.NaN()), 0}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewKNNGraphClusterer(KNNGraphOptions{}).Cluster(context.Background(), tc.pts)
			require.Error(t, err)
		})
	}
}

func TestKNNGraphClusterer_ZeroVectorIsNoise(t *testing.T) {
	pts := append(twoGroupsPlusOutlier(), Point{ID: "zero", Vector: make([]float32, 4)})
	labels, err := NewKNNGraphClusterer(KNNGraphOptions{}).Cluster(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, NoiseLabel, labels[len(labels)-1])
}

// BenchmarkKNNGraphClusterer times a corpus-sized run: building the kNN graph
// is O(n²·d) and dominates.
func BenchmarkKNNGraphClusterer(b *testing.B) {
	pts := syntheticCorpus(1, 5000, 768, 50, 1.0)
	c := NewKNNGraphClusterer(KNNGraphOptions{})
	for b.Loop() {
		if _, err := c.Cluster(context.Background(), pts); err != nil {
			b.Fatal(err)
		}
	}
}
