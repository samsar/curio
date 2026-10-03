package insight_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
)

// TestRunSpace_JudgesLikeTheStrayStep: placing a document from its raw
// vector, the run's stored mean and centroids, gives what the grouping saw
// on its prepared vector: the nearest centroid and its cosine, and for a
// stray the same interest and verdict AssignStrays gave.
func TestRunSpace_JudgesLikeTheStrayStep(t *testing.T) {
	lib := fixtureLibrary(1)
	r := rebuild(t, lib.docs, nil, false)
	space, err := insight.NewRunSpace(r.mean, r.centroids)
	require.NoError(t, err)
	for i, dv := range lib.docs {
		got, err := space.Place(dv.Vector)
		require.NoError(t, err)
		best, bestSim := -1, math.Inf(-1)
		for l, c := range r.centroids {
			if s := dot(r.points[i].Vector, c); s > bestSim {
				best, bestSim = l, s
			}
		}
		require.Equal(t, best, got.Interest, "document %s", dv.DocumentID)
		require.InDelta(t, bestSim, got.Similarity, 1e-5, "document %s", dv.DocumentID)
		if f := r.fits[i]; f.Kind != insight.FitMember {
			require.Equal(t, f.Interest, got.Interest, "document %s", dv.DocumentID)
			require.InDelta(t, f.Similarity, got.Similarity, 1e-5, "document %s", dv.DocumentID)
			require.Equal(t, f.Kind == insight.FitLoose, got.Joined, "document %s", dv.DocumentID)
		}
	}
}

// TestRunSpace_PlacementKeepsEveryName pins the property that placing new
// documents between rebuilds never costs a name: a placement only adds to
// an interest, so carry-over from the run to the run with its placements
// keeps every interest and every area.
func TestRunSpace_PlacementKeepsEveryName(t *testing.T) {
	lib := fixtureLibrary(2)
	n := len(lib.docs)
	added := shuffled(n, 4242)[:n/20]
	prev := rebuild(t, lib.subset(without(n, added)), nil, false)
	space, err := insight.NewRunSpace(prev.mean, prev.centroids)
	require.NoError(t, err)

	areaOf := make(map[int]int)
	for i, l := range prev.g.Interest {
		if l != insight.NoiseLabel {
			areaOf[l] = prev.g.Area[i]
		}
	}
	next := prev
	next.ids = append([]string(nil), prev.ids...)
	next.g.Interest = append([]int(nil), prev.g.Interest...)
	next.g.Area = append([]int(nil), prev.g.Area...)
	joined := 0
	for _, i := range added {
		p, err := space.Place(lib.docs[i].Vector)
		require.NoError(t, err)
		next.ids = append(next.ids, lib.docs[i].DocumentID)
		interest, area := insight.NoiseLabel, insight.NoiseLabel
		if p.Joined {
			interest, area = p.Interest, areaOf[p.Interest]
			joined++
		}
		next.g.Interest = append(next.g.Interest, interest)
		next.g.Area = append(next.g.Area, area)
	}
	require.Positive(t, joined, "the premise: some documents join an interest")
	interests, areas := namesKept(t, prev, next)
	assert.InDelta(t, 1, interests, 0)
	assert.InDelta(t, 1, areas, 0)
}

func TestRunSpace_Rejects(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mean      []float64
		centroids [][]float32
	}{
		{"widths differ", []float64{0, 0}, [][]float32{{1, 0, 0}}},
		{"a NaN mean", []float64{math.NaN(), 0}, nil},
		{"an infinite centroid", nil, [][]float32{{1, 0}, {float32(math.Inf(1)), 0}}},
		{"a centroid without width", nil, [][]float32{{}}},
		{"a later centroid without width", nil, [][]float32{{1, 0}, {}}},
		{"a centroid without the mean's width", []float64{0, 0}, [][]float32{{}}},
		{"an empty mean, centroid widths differ", []float64{}, [][]float32{{1, 0}, {1, 0, 0}}},
	} {
		_, err := insight.NewRunSpace(tc.mean, tc.centroids)
		require.Error(t, err, tc.name)
	}

	space, err := insight.NewRunSpace([]float64{1, 1}, [][]float32{{1, 0}, {0, 1}})
	require.NoError(t, err)
	for _, v := range [][]float32{{1}, {1, 0, 0}, {}, {float32(math.NaN()), 0}} {
		_, err := space.Place(v)
		require.Error(t, err, "%v", v)
	}

	zero, err := space.Place([]float32{1, 1})
	require.NoError(t, err)
	assert.Equal(t, insight.Placement{Interest: 0}, zero, "a zero residual: unjoined, similarity 0")

	empty, err := insight.NewRunSpace(nil, nil)
	require.NoError(t, err)
	got, err := empty.Place([]float32{3, 4})
	require.NoError(t, err)
	assert.Equal(t, insight.Placement{Interest: -1}, got, "a run without interests")
}

// TestRunSpace_EmptyMeanIsNotCentered: a stored run without centering may
// decode its mean as an empty, non-nil slice; it places as nil does.
func TestRunSpace_EmptyMeanIsNotCentered(t *testing.T) {
	centroids := [][]float32{{1, 0}, {0, 1}}
	for _, mean := range [][]float64{nil, {}} {
		space, err := insight.NewRunSpace(mean, centroids)
		require.NoError(t, err)
		got, err := space.Place([]float32{0, 2})
		require.NoError(t, err)
		assert.Equal(t, insight.Placement{Interest: 1, Similarity: 1, Joined: true}, got, "mean %#v", mean)
		_, err = space.Place([]float32{1, 0, 0})
		require.Error(t, err, "the centroids fix the width")
	}
}

// dot is the cosine of two unit vectors, summed as package insight sums it.
func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}
