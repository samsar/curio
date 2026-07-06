package insight

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoGroupsPlusOutlier builds points: 4 near basis e0, 3 near e1, 1 near e3.
// The two groups are internally near-parallel (cosine ~1) and mutually
// orthogonal (cosine ~0), and the outlier is orthogonal to everything.
func twoGroupsPlusOutlier() []Point {
	return []Point{
		{ID: "a1", Vector: []float32{1, 0.10, 0, 0}},
		{ID: "a2", Vector: []float32{1, 0.05, 0, 0}},
		{ID: "a3", Vector: []float32{0.9, 0, 0.05, 0}},
		{ID: "a4", Vector: []float32{1, 0, 0.10, 0}},
		{ID: "b1", Vector: []float32{0, 1, 0.10, 0}},
		{ID: "b2", Vector: []float32{0, 1, 0, 0}},
		{ID: "b3", Vector: []float32{0, 0.9, 0, 0.05}},
		{ID: "out", Vector: []float32{0, 0, 0, 1}},
	}
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

func TestKNNGraphClusterer_Empty(t *testing.T) {
	c := NewKNNGraphClusterer(KNNGraphOptions{})
	labels, err := c.Cluster(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, labels)
}

func TestKNNGraphClusterer_AllNoiseBelowMinSize(t *testing.T) {
	// Two well-separated points but min_cluster_size 3 → nothing survives.
	c := NewKNNGraphClusterer(KNNGraphOptions{MinClusterSize: 3})
	labels, err := c.Cluster(context.Background(), []Point{
		{ID: "x", Vector: []float32{1, 0}},
		{ID: "y", Vector: []float32{1, 0.01}},
	})
	require.NoError(t, err)
	for _, l := range labels {
		assert.Equal(t, NoiseLabel, l)
	}
}

func TestKNNGraphClusterer_DimMismatch(t *testing.T) {
	c := NewKNNGraphClusterer(KNNGraphOptions{})
	_, err := c.Cluster(context.Background(), []Point{
		{ID: "x", Vector: []float32{1, 0}},
		{ID: "y", Vector: []float32{1, 0, 0}},
	})
	require.Error(t, err)
}
