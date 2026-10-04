package layout_test

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight/layout"
	"github.com/samsar/curio/internal/insight/quality"
)

// tolerance is what rounding to 0.01 may cost an invariant.
const tolerance = 0.02

func zoom(t testing.TB, in layout.ZoomInput) layout.ZoomLayout {
	t.Helper()
	z, err := layout.Zoom(context.Background(), in)
	require.NoError(t, err)
	return z
}

// assertZoom checks the zoom view's invariants: every circle and dot on
// the map; an area's interests inside it and apart; the top-level circles
// and Unsorted's disc apart; every document's dot inside its circle; the
// dots of a circle a dot apart; and a dot no wider than 1% of the map.
func assertZoom(t testing.TB, in layout.ZoomInput, z layout.ZoomLayout) {
	t.Helper()
	require.Len(t, z.Interests, len(in.Interests))
	require.Len(t, z.Areas, len(in.Areas))
	require.Len(t, z.Docs, len(in.Keys))
	require.Greater(t, z.DotRadius, 0.0)
	require.LessOrEqual(t, z.DotRadius, 10.0)
	circles := slices.Concat(z.Interests, z.Areas, []layout.Circle{z.Unsorted})
	for _, c := range circles {
		require.True(t, c.R > 0 && c.X-c.R >= -tolerance && c.X+c.R <= layout.Extent+tolerance &&
			c.Y-c.R >= -tolerance && c.Y+c.R <= layout.Extent+tolerance, "circle %+v is on the map", c)
	}
	for i, c := range z.Interests {
		if len(in.Areas) > 0 {
			a := z.Areas[in.Interests[i].Area]
			require.LessOrEqual(t, math.Hypot(c.X-a.X, c.Y-a.Y)+c.R, a.R+tolerance, "interest %d inside its area", i)
		}
		for j := i + 1; j < len(z.Interests); j++ {
			if len(in.Areas) == 0 || in.Interests[i].Area == in.Interests[j].Area {
				require.False(t, overlap(c, z.Interests[j]), "interests %d and %d", i, j)
			}
		}
	}
	top := slices.Concat(z.Areas, []layout.Circle{z.Unsorted})
	if len(in.Areas) == 0 {
		top = slices.Concat(z.Interests, []layout.Circle{z.Unsorted})
	}
	for i := range top {
		for j := i + 1; j < len(top); j++ {
			require.False(t, overlap(top[i], top[j]), "top-level circles %d and %d", i, j)
		}
	}
	byCircle := map[int][]layout.XY{}
	for k, p := range z.Docs {
		c := z.Unsorted
		if l := in.Interest[k]; l >= 0 {
			c = z.Interests[l]
		}
		require.LessOrEqual(t, math.Hypot(p.X-c.X, p.Y-c.Y)+z.DotRadius, c.R+tolerance, "document %d inside its circle", k)
		byCircle[in.Interest[k]] = append(byCircle[in.Interest[k]], p)
	}
	for l, dots := range byCircle {
		closest := math.Inf(1)
		for a := range dots {
			for b := a + 1; b < len(dots); b++ {
				closest = math.Min(closest, math.Hypot(dots[a].X-dots[b].X, dots[a].Y-dots[b].Y))
			}
		}
		require.GreaterOrEqual(t, closest, 2*z.DotRadius-tolerance, "two dots of circle %d overlap", l)
	}
}

func overlap(a, b layout.Circle) bool {
	return math.Hypot(a.X-b.X, a.Y-b.Y) < a.R+b.R-tolerance
}

// TestZoom_Invariants holds the zoom view's invariants over the shapes a
// grouping can take.
func TestZoom_Invariants(t *testing.T) {
	sized := func(n int, areas bool) func() layout.ZoomInput {
		return func() layout.ZoomInput {
			return libraryShape{points: n, areas: 3, clustersPerArea: 3, dims: 32, generalists: 0.1}.build(uint64(n)).
				zoomInput(areas, "g")
		}
	}
	cases := []struct {
		name string
		in   func() layout.ZoomInput
	}{
		{"no documents", func() layout.ZoomInput { return layout.ZoomInput{} }},
		{"one unsorted document", func() layout.ZoomInput { return unsortedOnly(1) }},
		{"two", sized(2, false)},
		{"three", sized(3, true)},
		{"sixteen, flat", sized(16, false)},
		{"sixteen, areas", sized(16, true)},
		{"1,500, flat", sized(1500, false)},
		{"1,500, areas", sized(1500, true)},
		{"one area", func() layout.ZoomInput {
			return libraryShape{points: 200, areas: 1, clustersPerArea: 4, dims: 32, generalists: 0.1}.build(3).zoomInput(true, "g")
		}},
		{"an area of one interest", func() layout.ZoomInput {
			return libraryShape{points: 200, areas: 3, clustersPerArea: 1, dims: 32, generalists: 0.1}.build(4).zoomInput(true, "g")
		}},
		{"one interest", func() layout.ZoomInput {
			return libraryShape{points: 100, areas: 1, clustersPerArea: 1, dims: 32, generalists: 0.2}.build(5).zoomInput(false, "g")
		}},
		{"no interests", func() layout.ZoomInput { return unsortedOnly(40) }},
		{"no unsorted documents", func() layout.ZoomInput {
			return libraryShape{points: 300, areas: 2, clustersPerArea: 3, dims: 32}.build(6).zoomInput(true, "g")
		}},
		{"3,000 unsorted documents", func() layout.ZoomInput {
			in := libraryShape{points: 3300, areas: 2, clustersPerArea: 3, dims: 32, generalists: 0.92}.build(7).zoomInput(true, "g")
			require.GreaterOrEqual(t, count(in.Interest, -1), 3000)
			return in
		}},
		{"all-zero vectors", func() layout.ZoomInput {
			in := sized(60, true)()
			for i := range in.Vectors {
				in.Vectors[i] = make([]float32, len(in.Vectors[i]))
			}
			return in
		}},
		{"exact duplicates", func() layout.ZoomInput {
			in := sized(80, false)()
			for i := 40; i < 80; i++ {
				in.Vectors[i] = in.Vectors[i-40]
			}
			return in
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in()
			assertZoom(t, in, zoom(t, in))
		})
	}
}

// unsortedOnly is n documents in no interest.
func unsortedOnly(n int) layout.ZoomInput {
	in := layout.ZoomInput{}
	for i := range n {
		in.Keys = append(in.Keys, fmt.Sprintf("u-%04d", i))
		v := make([]float32, 8)
		v[i%8] = 1
		in.Vectors = append(in.Vectors, v)
		in.Interest = append(in.Interest, -1)
		in.Nearest = append(in.Nearest, -1)
		in.Similarity = append(in.Similarity, 0)
	}
	return in
}

func count(xs []int, x int) int {
	n := 0
	for _, y := range xs {
		if y == x {
			n++
		}
	}
	return n
}

// TestZoom_Deterministic: the same input twice gives the same view; the
// input shuffled (documents moved, groups renumbered consistently) gives
// every key the same place, and so do new keys for every group (identities
// minted afresh) and one goroutine.
func TestZoom_Deterministic(t *testing.T) {
	for _, areas := range []bool{false, true} {
		t.Run(fmt.Sprintf("areas %v", areas), func(t *testing.T) {
			in := small.build(8).zoomInput(areas, "g")
			first := zoom(t, in)
			assert.Equal(t, first, zoom(t, in))

			docPerm, groupPerm, areaPerm := shuffle(len(in.Keys), 1), shuffle(len(in.Interests), 2), shuffle(len(in.Areas), 3)
			shuffled := shuffledZoom(in, docPerm, groupPerm, areaPerm)
			assert.Equal(t, places(in, first), places(shuffled, zoom(t, shuffled)), "shuffled")

			renamed := in
			renamed.Interests, renamed.Areas = slices.Clone(in.Interests), slices.Clone(in.Areas)
			for i := range renamed.Interests {
				renamed.Interests[i].Key = fmt.Sprintf("fresh-%d", len(in.Interests)-i)
			}
			for a := range renamed.Areas {
				renamed.Areas[a].Key = fmt.Sprintf("other-%d", a*7)
			}
			again := zoom(t, renamed)
			assert.Equal(t, first.Docs, again.Docs, "new group keys")
			assert.Equal(t, first.Interests, again.Interests, "new group keys")

			prev := runtime.GOMAXPROCS(1)
			one := zoom(t, in)
			runtime.GOMAXPROCS(prev)
			assert.Equal(t, first, one, "one goroutine")
		})
	}
}

// places are a view's circles and dots by key.
func places(in layout.ZoomInput, z layout.ZoomLayout) map[string]any {
	out := map[string]any{"unsorted": z.Unsorted, "dot": z.DotRadius}
	for k, key := range in.Keys {
		out[key] = z.Docs[k]
	}
	for i, g := range in.Interests {
		out[g.Key] = z.Interests[i]
	}
	for a, g := range in.Areas {
		out[g.Key] = z.Areas[a]
	}
	return out
}

// shuffledZoom is in with document docPerm[x] moved to x, interest
// groupPerm[x] to x and area areaPerm[x] to x, every index renamed.
func shuffledZoom(in layout.ZoomInput, docPerm, groupPerm, areaPerm []int) layout.ZoomInput {
	interestAt := inverse(groupPerm)
	areaAt := inverse(areaPerm)
	rename := func(l int) int {
		if l < 0 {
			return l
		}
		return interestAt[l]
	}
	out := layout.ZoomInput{Seed: in.Seed, Prior: in.Prior}
	for _, i := range docPerm {
		out.Keys = append(out.Keys, in.Keys[i])
		out.Vectors = append(out.Vectors, in.Vectors[i])
		out.Interest = append(out.Interest, rename(in.Interest[i]))
		out.Nearest = append(out.Nearest, rename(in.Nearest[i]))
		out.Similarity = append(out.Similarity, in.Similarity[i])
	}
	for _, l := range groupPerm {
		g := in.Interests[l]
		if g.Area >= 0 {
			g.Area = areaAt[g.Area]
		}
		out.Interests = append(out.Interests, g)
	}
	for _, a := range areaPerm {
		out.Areas = append(out.Areas, in.Areas[a])
	}
	return out
}

func inverse(perm []int) []int {
	out := make([]int, len(perm))
	for x, i := range perm {
		out[i] = x
	}
	return out
}

// TestZoom_WarmIsStable: after 5% more documents arrive, a warm view from
// the previous one moves the interests' centres by a mean of at most 0.38%
// of the previous centres' diameter (measured per draw: 0.38%, 0.22%,
// 0.31%); held to 1.5 times that.
func TestZoom_WarmIsStable(t *testing.T) {
	const measured = 0.0038
	lib := standard.build(9)
	for draw := range 3 {
		perm := shuffle(len(lib.keys), uint64(200+draw))
		prevLib := lib.subset(without(len(lib.keys), perm[:len(perm)/20]))
		prevIn := prevLib.zoomInput(true, "g")
		prev := zoom(t, prevIn)
		in := lib.zoomInput(true, "g")
		in.Prior = priorZoom(prevIn, prev)
		next := zoom(t, in)
		assertZoom(t, in, next)
		d := quality.Displace(centres(prevIn, prev), centres(in, next))
		t.Logf("draw %d: interests' centres moved a mean of %.4f (aligned %.4f) of %d", draw, d.Mean, d.AlignedMean, d.Shared)
		assert.LessOrEqual(t, d.Mean, 1.5*measured)
	}
}

// priorZoom is a view as the next one's prior.
func priorZoom(in layout.ZoomInput, z layout.ZoomLayout) *layout.ZoomPrior {
	p := &layout.ZoomPrior{DotRadius: z.DotRadius, Places: map[string]layout.Circle{}}
	for i, g := range in.Interests {
		p.Places[g.Key] = z.Interests[i]
	}
	for a, g := range in.Areas {
		p.Places[g.Key] = z.Areas[a]
	}
	return p
}

// centres are a view's interests' centres by key.
func centres(in layout.ZoomInput, z layout.ZoomLayout) map[string][2]float64 {
	out := map[string][2]float64{}
	for i, g := range in.Interests {
		out[g.Key] = [2]float64{z.Interests[i].X, z.Interests[i].Y}
	}
	return out
}

// TestZoom_Cancelled: a context cancelled before the call lays nothing
// out; one cancelled while the circles are packed stops it; both errors
// wrap context.Canceled.
func TestZoom_Cancelled(t *testing.T) {
	in := small.build(10).zoomInput(true, "g")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := layout.Zoom(ctx, in)
	require.ErrorIs(t, err, context.Canceled)

	_, err = layout.Zoom(&cancelAfter{Context: context.Background(), n: 3}, in)
	require.ErrorIs(t, err, context.Canceled)
}

// TestZoom_RefusesBadInput: what the view can't draw is an error, never a
// panic.
func TestZoom_RefusesBadInput(t *testing.T) {
	base := func() layout.ZoomInput { return small.build(11).zoomInput(true, "g") }
	for name, spoil := range map[string]func(in *layout.ZoomInput){
		"fewer vectors":      func(in *layout.ZoomInput) { in.Vectors = in.Vectors[1:] },
		"fewer interests":    func(in *layout.ZoomInput) { in.Interest = in.Interest[1:] },
		"fewer nearest":      func(in *layout.ZoomInput) { in.Nearest = in.Nearest[1:] },
		"fewer similarities": func(in *layout.ZoomInput) { in.Similarity = in.Similarity[1:] },
		"a duplicate key":    func(in *layout.ZoomInput) { in.Keys[1] = in.Keys[0] },
		"an interest past k": func(in *layout.ZoomInput) { in.Interest[0] = len(in.Interests) },
		"a nearest past k":   func(in *layout.ZoomInput) { in.Interest[0], in.Nearest[0] = -1, len(in.Interests) },
		"a NaN similarity":   func(in *layout.ZoomInput) { in.Similarity[0] = math.NaN() },
		"a NaN component":    func(in *layout.ZoomInput) { in.Vectors[0] = slices.Repeat([]float32{float32(math.NaN())}, 40) },
		"an empty interest": func(in *layout.ZoomInput) {
			in.Interests = append(in.Interests, layout.Group{Key: "e", Area: 0, Centroid: in.Interests[0].Centroid})
		},
		"an empty area": func(in *layout.ZoomInput) {
			in.Areas = append(in.Areas, layout.Group{Key: "e", Centroid: in.Areas[0].Centroid})
		},
		"an interest in no area": func(in *layout.ZoomInput) { in.Interests[0].Area = -1 },
		"an area past the areas": func(in *layout.ZoomInput) { in.Interests[0].Area = len(in.Areas) },
		"a duplicate group key":  func(in *layout.ZoomInput) { in.Areas[0].Key = in.Interests[0].Key },
		"a short centroid":       func(in *layout.ZoomInput) { in.Interests[0].Centroid = in.Interests[0].Centroid[:3] },
		"a zero prior dot":       func(in *layout.ZoomInput) { in.Prior = &layout.ZoomPrior{} },
		"a prior of no radius": func(in *layout.ZoomInput) {
			in.Prior = &layout.ZoomPrior{DotRadius: 1, Places: map[string]layout.Circle{"x": {}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := base()
			in.Interests, in.Areas = slices.Clone(in.Interests), slices.Clone(in.Areas)
			in.Vectors = slices.Clone(in.Vectors)
			spoil(&in)
			_, err := layout.Zoom(context.Background(), in)
			require.Error(t, err)
		})
	}
}
