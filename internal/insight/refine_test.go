package insight

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copies returns n points with ID prefix and the vector v.
func copies(prefix string, n int, v []float32) []Point {
	out := make([]Point, n)
	for i := range out {
		out[i] = Point{ID: fmt.Sprintf("%s%d", prefix, i), Vector: v}
	}
	return out
}

// flatGrouping is a ShapeFlat grouping with the given interests.
func flatGrouping(interest []int) Grouping {
	g := Grouping{Shape: ShapeFlat, Area: make([]int, len(interest)), Interest: interest, Seeds: make([]Seed, len(interest))}
	for i := range g.Area {
		g.Area[i], g.Seeds[i] = NoiseLabel, noSeed
	}
	return g
}

func unit32(v ...float64) []float32 {
	var s float64
	for _, x := range v {
		s += x * x
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(x / math.Sqrt(s))
	}
	return out
}

func TestCentroids(t *testing.T) {
	pts := []Point{
		{ID: "a", Vector: unit32(1, 0)},
		{ID: "b", Vector: unit32(0, 1)},
		{ID: "c", Vector: unit32(1, 1)},
		{ID: "d", Vector: unit32(-1, 0)},
	}
	cents, err := Centroids(pts, []int{0, 0, NoiseLabel, 1})
	require.NoError(t, err)
	assert.InDeltaSlice(t, []float32{float32(math.Sqrt2 / 2), float32(math.Sqrt2 / 2)}, cents[0], 1e-7)
	assert.Equal(t, []float32{-1, 0}, cents[1])

	// A loose fit or an unsorted point never moves a centroid.
	more := append(slices.Clone(pts), Point{ID: "e", Vector: unit32(0, -1)})
	again, err := Centroids(more, []int{0, 0, NoiseLabel, 1, NoiseLabel})
	require.NoError(t, err)
	assert.Equal(t, cents, again)

	none, err := Centroids(pts, []int{-1, -1, -1, -1})
	require.NoError(t, err)
	assert.Empty(t, none)
	_, err = Centroids(pts, []int{0})
	require.Error(t, err)
	for _, bad := range []int{-2, 4, 1 << 40, math.MaxInt} {
		_, err = Centroids(pts, []int{0, 0, bad, 1})
		require.Error(t, err, "label %d", bad)
	}
}

// TestMergeNearDuplicates_UntilNothingJoins: A and B are joined at 0.86,
// and the joined centroid then reaches D at 0.871, which neither A nor B
// did (0.84). A single pass would leave that pair.
func TestMergeNearDuplicates_UntilNothingJoins(t *testing.T) {
	th := 15.34 * math.Pi / 180
	cphi := 0.84 / math.Cos(th)
	a := unit32(math.Cos(th), math.Sin(th), 0)
	b := unit32(math.Cos(th), -math.Sin(th), 0)
	d := unit32(cphi, 0, math.Sqrt(1-cphi*cphi))
	pts := slices.Concat(copies("a", 3, a), copies("b", 3, b), copies("d", 3, d))
	g := flatGrouping([]int{0, 0, 0, 1, 1, 1, 2, 2, 2})
	require.InDelta(t, 0.86, dot(a, b), 1e-3)
	require.InDelta(t, 0.84, dot(a, d), 1e-3)

	first, err := Centroids(pts, g.Interest)
	require.NoError(t, err)
	root := mergeRound(first, scopesOf(g, g.Interest, 3), MergeThreshold)
	assert.Equal(t, []int{0, 0, 2}, root, "one round joins A and B only")

	merged, n, err := MergeNearDuplicates(pts, g, MergeThreshold)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, make([]int, 9), merged.Interest, "the second round joins D")
	assert.Equal(t, []int{0, 0, 0, 1, 1, 1, 2, 2, 2}, g.Interest, "the input is not modified")
}

func TestMergeNearDuplicates_Scope(t *testing.T) {
	x := unit32(1, 0.05, 0)
	y := unit32(1, -0.05, 0)
	z := unit32(0, 0, 1)
	// Two near-identical interests (x, y) and one far one, in two areas:
	// x with z's in area 0, y in area 1.
	pts := slices.Concat(copies("x", 3, x), copies("z", 7, z), copies("y", 10, y))
	g := Grouping{Shape: ShapeAreas, Area: make([]int, 20), Interest: make([]int, 20), Seeds: make([]Seed, 20)}
	for i := range 20 {
		switch {
		case i < 3:
			g.Area[i], g.Interest[i] = 0, 1
		case i < 10:
			g.Area[i], g.Interest[i] = 0, 0
		default:
			g.Area[i], g.Interest[i] = 1, 2
		}
	}
	require.NoError(t, g.Validate(20))
	merged, n, err := MergeNearDuplicates(pts, g, MergeThreshold)
	require.NoError(t, err)
	assert.Zero(t, n, "never across areas")
	assert.Equal(t, g.Interest, merged.Interest)

	// The same interests in the flat shape share one scope.
	flat := flatGrouping(slices.Clone(g.Interest))
	merged, n, err = MergeNearDuplicates(pts, flat, MergeThreshold)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	// x and y joined (13 points) come first, z's 7 second.
	want := slices.Repeat([]int{0}, 20)
	for i := 3; i < 10; i++ {
		want[i] = 1
	}
	assert.Equal(t, want, merged.Interest)
	assert.Equal(t, flat.Area, merged.Area)
	assert.Equal(t, flat.Seeds, merged.Seeds, "the seeds stay, so a warm start re-derives the merge")
}

func TestMergeNearDuplicates_RejectsBadInput(t *testing.T) {
	pts := copies("a", 3, unit32(1, 0))
	g := flatGrouping([]int{0, 0, 0})
	_, _, err := MergeNearDuplicates(pts, flatGrouping([]int{0, 0}), MergeThreshold)
	require.Error(t, err, "a grouping of other points")
	// A NaN compares false with every cosine, so it would join everything.
	for _, th := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, _, err = MergeNearDuplicates(pts, g, th)
		require.Error(t, err, "threshold %g", th)
	}
	nan := slices.Clone(pts)
	nan[1].Vector = []float32{float32(math.NaN()), 0}
	_, _, err = MergeNearDuplicates(nan, g, MergeThreshold)
	require.Error(t, err, "a NaN vector")
}

func TestAssignStrays(t *testing.T) {
	// Interest 0 sits on e1, interest 1 on e2, in two areas.
	lo := float32(0.45) // just below 0.45 as a float32
	hi := math.Nextafter32(lo, 1)
	stray := func(x float32) []float32 {
		return []float32{x, 0, float32(math.Sqrt(1 - float64(x)*float64(x)))}
	}
	pts := slices.Concat(
		copies("m", 10, []float32{1, 0, 0}),
		copies("n", 10, []float32{0, 1, 0}),
		[]Point{
			{ID: "hi", Vector: stray(hi)},
			{ID: "lo", Vector: stray(lo)},
			{ID: "zero", Vector: make([]float32, 3)},
			{ID: "tie", Vector: unit32(1, 1, 0)},
		},
	)
	g := Grouping{Shape: ShapeAreas, Area: make([]int, 24), Interest: make([]int, 24), Seeds: make([]Seed, 24)}
	for i := range g.Area {
		switch {
		case i < 10:
		case i < 20:
			g.Area[i], g.Interest[i] = 1, 1
		default:
			g.Area[i], g.Interest[i] = NoiseLabel, NoiseLabel
		}
	}
	g.Area[20] = 1 // in area 1, but in none of its interests
	cents, err := Centroids(pts, g.Interest)
	require.NoError(t, err)
	before := struct {
		pts   []Point
		g     Grouping
		cents [][]float32
	}{make([]Point, len(pts)), g, make([][]float32, len(cents))}
	for i, p := range pts {
		before.pts[i] = Point{ID: p.ID, Vector: slices.Clone(p.Vector)}
	}
	for l, c := range cents {
		before.cents[l] = slices.Clone(c)
	}
	before.g.Area, before.g.Interest, before.g.Seeds = slices.Clone(g.Area), slices.Clone(g.Interest), slices.Clone(g.Seeds)
	fits, err := AssignStrays(pts, g, cents, LooseFitThreshold)
	require.NoError(t, err)
	assert.Equal(t, before.pts, pts, "inputs are not modified")
	assert.Equal(t, before.g, g)
	assert.Equal(t, before.cents, cents)
	assert.Equal(t, Fit{Kind: FitMember, Interest: 0, Similarity: 1}, fits[0])
	assert.Equal(t, Fit{Kind: FitMember, Interest: 1, Similarity: 1}, fits[10])
	assert.Equal(t, Fit{Kind: FitLoose, Interest: 0, Similarity: float64(hi)}, fits[20], "just above: a loose fit, here of another area's interest")
	assert.Equal(t, Fit{Kind: FitUnsorted, Interest: 0, Similarity: float64(lo)}, fits[21], "just below: unsorted, its nearest recorded")
	assert.Equal(t, Fit{Kind: FitUnsorted, Interest: 0, Similarity: 0}, fits[22], "a zero vector: unsorted, finite")
	assert.Equal(t, FitLoose, fits[23].Kind)
	assert.Equal(t, 0, fits[23].Interest, "a tie goes to the lower label")

	// The threshold is inclusive.
	at, err := AssignStrays(pts, g, cents, float64(lo))
	require.NoError(t, err)
	assert.Equal(t, FitLoose, at[21].Kind)

	// Without interests every point is unsorted.
	none := flatGrouping(slices.Repeat([]int{NoiseLabel}, len(pts)))
	fits, err = AssignStrays(pts, none, [][]float32{}, LooseFitThreshold)
	require.NoError(t, err)
	for _, f := range fits {
		assert.Equal(t, Fit{Kind: FitUnsorted, Interest: -1}, f)
	}

	_, err = AssignStrays(pts, g, cents[:1], LooseFitThreshold)
	require.EqualError(t, err, "insight: 1 centroids for 2 interests")
	_, err = AssignStrays(pts, g, [][]float32{{1, 0}, {0, 1}}, LooseFitThreshold)
	require.Error(t, err, "centroid width")
	_, err = AssignStrays(pts, g, [][]float32{{1, 0, float32(math.NaN())}, {0, 1, 0}}, LooseFitThreshold)
	require.Error(t, err, "a NaN centroid")
	_, err = AssignStrays(pts, g, cents, math.NaN())
	require.Error(t, err, "a NaN threshold")
}

func TestNumberInterests(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	// Area 1's interests come after area 0's whatever their size; within
	// an area, larger first, and a tie to the interest holding "c" over
	// the one holding "e".
	interest := []int{5, 5, 9, 9, 7, 7, 3, NoiseLabel}
	area := []int{1, 1, 0, 0, 0, 0, 0, NoiseLabel}
	assert.Equal(t, []int{3, 3, 0, 0, 1, 1, 2, NoiseLabel}, numberInterests(interest, area, ids))
}
