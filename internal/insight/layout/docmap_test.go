package layout_test

import (
	"context"
	"errors"
	"math"
	"runtime"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight/layout"
	"github.com/samsar/curio/internal/insight/quality"
)

// small is the determinism tests' library: 300 points in 6 clusters.
var small = libraryShape{points: 300, areas: 2, clustersPerArea: 3, dims: 40, generalists: 0.05}

func docMap(t *testing.T, in layout.DocMapInput) []layout.XY {
	t.Helper()
	pos, err := layout.DocMap(context.Background(), in)
	require.NoError(t, err)
	return pos
}

// TestDocMap_Deterministic: the same input twice gives the same positions;
// the input shuffled (keys, vectors and neighbour lists remapped) gives each
// key the same position; and so does one goroutine.
func TestDocMap_Deterministic(t *testing.T) {
	lib := small.build(7)
	in := lib.docInput(3)
	first := docMap(t, in)
	assert.Equal(t, first, docMap(t, in), "the same input")

	perm := shuffle(len(lib.keys), 11)
	assert.Equal(t, keyed(lib.keys, first), keyed(shuffledKeys(lib.keys, perm), docMap(t, shuffledDocInput(in, perm))),
		"the input shuffled")

	prev := runtime.GOMAXPROCS(1)
	one := docMap(t, in)
	runtime.GOMAXPROCS(prev)
	assert.Equal(t, first, one, "one goroutine")

	warm := in
	warm.Prior, warm.Warm = priorOf(lib.keys[:250], first[:250]), true
	again := docMap(t, warm)
	assert.Equal(t, keyed(lib.keys, again), keyed(shuffledKeys(lib.keys, perm), docMap(t, shuffledDocInput(warm, perm))),
		"a warm start, shuffled")
}

// shuffledDocInput is in with point perm[x] moved to x and every neighbour
// index renamed to match.
func shuffledDocInput(in layout.DocMapInput, perm []int) layout.DocMapInput {
	n := len(perm)
	at := make([]int, n) // where the input's point i went
	for x, i := range perm {
		at[i] = x
	}
	out := in
	out.Keys, out.Vectors, out.Neighbours = make([]string, n), make([][]float32, n), make([][]layout.Neighbour, n)
	for x, i := range perm {
		out.Keys[x], out.Vectors[x] = in.Keys[i], in.Vectors[i]
		for _, nb := range in.Neighbours[i] {
			out.Neighbours[x] = append(out.Neighbours[x], layout.Neighbour{Index: at[nb.Index], Similarity: nb.Similarity})
		}
	}
	return out
}

func shuffledKeys(keys []string, perm []int) []string {
	out := make([]string, len(perm))
	for x, i := range perm {
		out[x] = keys[i]
	}
	return out
}

// TestDocMap_OnTheMap: every position is a number inside [0, Extent] over
// the shapes a library can take, and a map of two distinct positions or
// more spans at least 90% of the map on its longer side.
func TestDocMap_OnTheMap(t *testing.T) {
	zero := func(n int) layout.DocMapInput {
		in := layout.DocMapInput{}
		for i := range n {
			in.Keys = append(in.Keys, string(rune('a'+i%26))+string(rune('a'+i/26)))
			in.Vectors = append(in.Vectors, make([]float32, 8))
			in.Neighbours = append(in.Neighbours, nil)
		}
		return in
	}
	sized := func(n int) layout.DocMapInput {
		return libraryShape{points: n, areas: 2, clustersPerArea: 2, dims: 32, generalists: 0.1}.build(uint64(n)).docInput(1)
	}
	twoIslands := func() layout.DocMapInput {
		lib := libraryShape{points: 120, areas: 2, clustersPerArea: 1, dims: 32}.build(5)
		in := lib.docInput(1)
		// Keep only neighbours of the same cluster: two components.
		for i, list := range in.Neighbours {
			in.Neighbours[i] = slices.DeleteFunc(slices.Clone(list), func(nb layout.Neighbour) bool {
				return lib.cluster[nb.Index] != lib.cluster[i]
			})
		}
		return in
	}
	duplicates := func() layout.DocMapInput {
		in := sized(40)
		for i := 20; i < 40; i++ {
			in.Vectors[i] = in.Vectors[i-20]
		}
		in.Neighbours = nearest(in.Vectors, 20)
		return in
	}
	noNeighbours := func() layout.DocMapInput {
		in := sized(50)
		for i := range in.Neighbours {
			in.Neighbours[i] = nil
		}
		return in
	}
	cases := []struct {
		name  string
		in    func() layout.DocMapInput
		heavy bool
	}{
		{"none", func() layout.DocMapInput { return zero(0) }, false},
		{"one", func() layout.DocMapInput { return sized(1) }, false},
		{"two", func() layout.DocMapInput { return sized(2) }, false},
		{"three", func() layout.DocMapInput { return sized(3) }, false},
		{"sixteen", func() layout.DocMapInput { return sized(16) }, false},
		{"1,500", func() layout.DocMapInput { return sized(1500) }, true},
		{"all-zero vectors", func() layout.DocMapInput { return zero(30) }, false},
		{"no neighbours", noNeighbours, false},
		{"two disconnected clusters", twoIslands, false},
		{"exact duplicates", duplicates, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.heavy {
				skipUnderRace(t)
			}
			in := tc.in()
			pos := docMap(t, in)
			require.Len(t, pos, len(in.Keys))
			assertSpans(t, pos)
		})
	}
}

// assertSpans checks every position is on the map and, with two distinct
// positions or more, that the longer side spans at least 900.
func assertSpans(t *testing.T, pos []layout.XY) {
	t.Helper()
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	distinct := map[layout.XY]bool{}
	for _, p := range pos {
		require.False(t, math.IsNaN(p.X) || math.IsNaN(p.Y))
		require.True(t, p.X >= 0 && p.X <= layout.Extent && p.Y >= 0 && p.Y <= layout.Extent, "%v is on the map", p)
		x0, y0, x1, y1 = math.Min(x0, p.X), math.Min(y0, p.Y), math.Max(x1, p.X), math.Max(y1, p.Y)
		distinct[p] = true
	}
	if len(distinct) >= 2 {
		assert.GreaterOrEqual(t, math.Max(x1-x0, y1-y0), 900.0)
	}
}

// TestDocMap_KeepsNeighbourhoods holds the cold map of the standard library
// (1,600 points, 12 planted clusters of 6 subtopics in 4 areas, 48
// dimensions) to floors 0.03 under what it measured: NP5 (the share of each
// point's 5 nearest by cosine among its 5 nearest on the map), measured
// 0.339, at or above 0.309; area purity@5, measured 1.000, at or above
// 0.97; and NP5 at least 0.10 above the points' own first two principal
// components', measured 0.078.
func TestDocMap_KeepsNeighbourhoods(t *testing.T) {
	skipUnderRace(t)
	lib := standard.build(1)
	in := lib.docInput(1)
	pos := positions(docMap(t, in))
	near := indexes(in.Neighbours)
	np5 := quality.NeighbourPreservation(near, pos, 5)
	purity := quality.MapPurity(pos, lib.area, 5)
	pca := quality.NeighbourPreservation(near, principalComponents(lib.vectors), 5)
	t.Logf("NP5 %.3f, area purity@5 %.3f, PCA's NP5 %.3f", np5, purity, pca)
	assert.GreaterOrEqual(t, np5, 0.309)
	assert.GreaterOrEqual(t, purity, 0.97)
	assert.GreaterOrEqual(t, np5, pca+0.10)
}

// principalComponents are the points' coordinates on their first two
// principal axes, by power iteration on the scatter matrix.
func principalComponents(vecs [][]float32) [][2]float64 {
	dim := len(vecs[0])
	mean := make([]float64, dim)
	for _, v := range vecs {
		for d, x := range v {
			mean[d] += float64(x) / float64(len(vecs))
		}
	}
	center := func(v []float32) []float64 {
		out := make([]float64, dim)
		for d, x := range v {
			out[d] = float64(x) - mean[d]
		}
		return out
	}
	axes := make([][]float64, 2)
	for a := range axes {
		v := make([]float64, dim)
		for d := range v {
			v[d] = math.Sin(float64(d*(a+3) + 1))
		}
		for range 500 {
			next := make([]float64, dim)
			for _, row := range vecs {
				c := center(row)
				var s float64
				for d := range c {
					s += c[d] * v[d]
				}
				for d := range c {
					next[d] += s * c[d]
				}
			}
			for _, prev := range axes[:a] {
				var p float64
				for d := range next {
					p += next[d] * prev[d]
				}
				for d := range next {
					next[d] -= p * prev[d]
				}
			}
			var n float64
			for _, x := range next {
				n += x * x
			}
			for d := range next {
				next[d] /= math.Sqrt(n)
			}
			v = next
		}
		axes[a] = v
	}
	out := make([][2]float64, len(vecs))
	for i, row := range vecs {
		c := center(row)
		for a, ax := range axes {
			for d := range c {
				out[i][a] += c[d] * ax[d]
			}
		}
	}
	return out
}

// TestDocMap_WarmIsStable: after 5% more points arrive, a warm map from the
// previous one moves the points they share by a mean of at most 0.35% of
// the previous map's diameter (measured per draw: 0.29%, 0.33%, 0.35%);
// held to 1.5 times that and never above 3%.
func TestDocMap_WarmIsStable(t *testing.T) {
	skipUnderRace(t)
	const measured = 0.0035
	lib := standard.build(2)
	for draw := range 3 {
		t.Run(strconv.Itoa(draw), func(t *testing.T) {
			t.Parallel()
			perm := shuffle(len(lib.keys), uint64(100+draw))
			held := perm[:len(perm)/20]
			prevLib := lib.subset(without(len(lib.keys), held))
			prev := docMap(t, prevLib.docInput(1))
			in := lib.docInput(1)
			in.Prior, in.Warm = priorOf(prevLib.keys, prev), true
			next := docMap(t, in)
			d := quality.Displace(keyed(prevLib.keys, prev), keyed(lib.keys, next))
			t.Logf("mean displacement %.4f (aligned %.4f) of %d shared", d.Mean, d.AlignedMean, d.Shared)
			assert.Equal(t, len(prevLib.keys), d.Shared)
			assert.LessOrEqual(t, d.Mean, 1.5*measured)
			assert.LessOrEqual(t, d.Mean, 0.03)
		})
	}
}

// without is 0..n-1 minus drop, ascending.
func without(n int, drop []int) []int {
	gone := make(map[int]bool, len(drop))
	for _, i := range drop {
		gone[i] = true
	}
	var out []int
	for i := range n {
		if !gone[i] {
			out = append(out, i)
		}
	}
	return out
}

// TestDocMap_AlignsToThePrior: a cold map given a prior it shares points
// with is turned, scaled and moved onto it; without one, it lies along its
// principal axis.
func TestDocMap_AlignsToThePrior(t *testing.T) {
	lib := small.build(4)
	in := lib.docInput(1)
	plain := docMap(t, in)
	// The prior: the plain map turned a quarter and mirrored.
	prior := make(map[string]layout.XY, len(plain))
	for i, p := range plain {
		prior[lib.keys[i]] = layout.XY{X: 1000 - p.Y, Y: p.X}
	}
	in.Prior = prior
	aligned := docMap(t, in)
	d := quality.Displace(keyed(lib.keys, plain), keyed(lib.keys, aligned))
	assert.Greater(t, d.Mean, 0.1, "the map turned")
	back := quality.Displace(func() map[string][2]float64 {
		out := map[string][2]float64{}
		for k, p := range prior {
			out[k] = [2]float64{p.X, p.Y}
		}
		return out
	}(), keyed(lib.keys, aligned))
	assert.Less(t, back.Mean, 0.01, "onto the prior")
}

// cancelAfter is a context whose Err is nil for its first n calls and
// context.Canceled after.
type cancelAfter struct {
	context.Context
	n int
}

func (c *cancelAfter) Err() error {
	if c.n--; c.n < 0 {
		return context.Canceled
	}
	return nil
}

// TestDocMap_Cancelled: a context cancelled before the call lays nothing
// out; one cancelled during the descent stops it; both errors wrap
// context.Canceled.
func TestDocMap_Cancelled(t *testing.T) {
	lib := small.build(5)
	in := lib.docInput(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pos, err := layout.DocMap(ctx, in)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, pos)

	// Warm, the descent starts at once: a few checks in, it is under way.
	in.Prior, in.Warm = priorOf(lib.keys, docMap(t, in)), true
	pos, err = layout.DocMap(&cancelAfter{Context: context.Background(), n: 20}, in)
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "descent")
	assert.Nil(t, pos)
}

// TestDocMap_RefusesBadInput: what the map can't lay out is an error,
// never a panic.
func TestDocMap_RefusesBadInput(t *testing.T) {
	base := func() layout.DocMapInput { return small.build(6).subset([]int{0, 1, 2, 3}).docInput(1) }
	for name, spoil := range map[string]func(in *layout.DocMapInput){
		"fewer vectors":         func(in *layout.DocMapInput) { in.Vectors = in.Vectors[:3] },
		"fewer neighbour lists": func(in *layout.DocMapInput) { in.Neighbours = in.Neighbours[:3] },
		"a duplicate key":       func(in *layout.DocMapInput) { in.Keys[1] = in.Keys[0] },
		"an empty key":          func(in *layout.DocMapInput) { in.Keys[2] = "" },
		"a neighbour past n":    func(in *layout.DocMapInput) { in.Neighbours[0] = []layout.Neighbour{{Index: 4, Similarity: 0.5}} },
		"a negative neighbour":  func(in *layout.DocMapInput) { in.Neighbours[0] = []layout.Neighbour{{Index: -1, Similarity: 0.5}} },
		"its own neighbour":     func(in *layout.DocMapInput) { in.Neighbours[1] = []layout.Neighbour{{Index: 1, Similarity: 1}} },
		"a neighbour twice":     func(in *layout.DocMapInput) { in.Neighbours[1] = []layout.Neighbour{{2, 0.5}, {2, 0.5}} },
		"a NaN similarity":      func(in *layout.DocMapInput) { in.Neighbours[1] = []layout.Neighbour{{2, math.NaN()}} },
		"a NaN component":       func(in *layout.DocMapInput) { in.Vectors[3] = []float32{float32(math.NaN())} },
		"an infinite component": func(in *layout.DocMapInput) { in.Vectors[3][0] = float32(math.Inf(1)) },
		"vectors of two widths": func(in *layout.DocMapInput) { in.Vectors[3] = in.Vectors[3][:5] },
		"an infinite prior":     func(in *layout.DocMapInput) { in.Prior = map[string]layout.XY{"x": {X: math.Inf(1)}} },
	} {
		t.Run(name, func(t *testing.T) {
			in := base()
			in.Vectors = slices.Clone(in.Vectors)
			in.Vectors[3] = slices.Clone(in.Vectors[3])
			spoil(&in)
			_, err := layout.DocMap(context.Background(), in)
			require.Error(t, err)
			assert.False(t, errors.Is(err, context.Canceled))
		})
	}
}
