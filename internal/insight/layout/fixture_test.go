package layout_test

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/samsar/curio/internal/insight/layout"
)

// skipUnderRace skips a property test of a layout under -race: each lays out
// a library of 1,500 points several times, single-threaded, where the race
// detector costs about ten times the CPU and finds nothing the determinism
// tests at GOMAXPROCS 1 and the default don't. make test runs the insight
// packages a second time without -race, so they still run on every push.
func skipUnderRace(t *testing.T) {
	t.Helper()
	if raceDetector {
		t.Skip("a property of the layout: make test runs it without -race")
	}
}

// library is a seeded synthetic library with planted structure: areas are
// random directions, each holding clusters around it, each cluster holding
// subtopics around it; a point is normalize(subtopic + noise·u) for a random
// unit u; generalists, a share of the points, are random unit vectors of
// their own subspace. Points are named p-00000, … in order; cluster and
// area are -1 for a generalist.
type library struct {
	keys          []string
	vectors       [][]float32
	cluster, area []int
	clusters      int
}

// subtopics is how many subtopics a cluster holds.
const subtopics = 6

// libraryShape sizes a library.
type libraryShape struct {
	points, areas, clustersPerArea, dims int
	generalists                          float64
}

// standard is the quality tests' library: 1,600 points in 12 clusters of 4
// areas, in 48 dimensions, 6% generalists.
var standard = libraryShape{points: 1600, areas: 4, clustersPerArea: 3, dims: 48, generalists: 0.06}

func (s libraryShape) build(seed uint64) library {
	r := rand.New(rand.NewPCG(seed, 0x1a7))
	topic := s.dims - 8 // the generalists' own subspace is the last 8 dimensions
	unit := func(lo, hi int) []float64 {
		v := make([]float64, s.dims)
		var sum float64
		for d := lo; d < hi; d++ {
			v[d] = r.NormFloat64()
			sum += v[d] * v[d]
		}
		for d := lo; d < hi; d++ {
			v[d] /= math.Sqrt(sum)
		}
		return v
	}
	around := func(c []float64, spread float64) []float64 {
		u := unit(0, topic)
		v := make([]float64, s.dims)
		for d := range v {
			v[d] = c[d] + spread*u[d]
		}
		return v
	}
	centers := make([][][]float64, 0, s.areas*s.clustersPerArea) // each cluster's subtopics
	areaOf := make([]int, 0, s.areas*s.clustersPerArea)
	for a := range s.areas {
		c := unit(0, topic)
		for range s.clustersPerArea {
			cl := around(c, 0.6)
			subs := make([][]float64, 0, subtopics)
			for range subtopics {
				subs = append(subs, around(cl, 0.5))
			}
			centers = append(centers, subs)
			areaOf = append(areaOf, a)
		}
	}
	lib := library{clusters: len(centers)}
	for i := range s.points {
		var v []float64
		cl := -1
		if r.Float64() < s.generalists {
			v = unit(topic, s.dims)
		} else {
			cl = r.IntN(len(centers))
			v = around(centers[cl][r.IntN(subtopics)], 0.45)
		}
		lib.keys = append(lib.keys, fmt.Sprintf("p-%05d", i))
		lib.vectors = append(lib.vectors, unitF32(v))
		lib.cluster = append(lib.cluster, cl)
		a := -1
		if cl >= 0 {
			a = areaOf[cl]
		}
		lib.area = append(lib.area, a)
	}
	return lib
}

func unitF32(v []float64) []float32 {
	var s float64
	for _, x := range v {
		s += x * x
	}
	out := make([]float32, len(v))
	if s == 0 {
		return out
	}
	for i, x := range v {
		out[i] = float32(x / math.Sqrt(s))
	}
	return out
}

func cos32(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// nearest is each point's k nearest neighbours of positive cosine, best
// first, ties to the lower index, as the grouping's pass finds them.
func nearest(vecs [][]float32, k int) [][]layout.Neighbour {
	out := make([][]layout.Neighbour, len(vecs))
	for i, a := range vecs {
		var cands []layout.Neighbour
		for j, b := range vecs {
			if j == i {
				continue
			}
			if s := cos32(a, b); s > 0 {
				cands = append(cands, layout.Neighbour{Index: j, Similarity: s})
			}
		}
		slices.SortFunc(cands, func(x, y layout.Neighbour) int {
			return cmp.Or(cmp.Compare(y.Similarity, x.Similarity), cmp.Compare(x.Index, y.Index))
		})
		out[i] = cands[:min(k, len(cands))]
	}
	return out
}

// indexes are each list's neighbours' indexes.
func indexes(lists [][]layout.Neighbour) [][]int {
	out := make([][]int, len(lists))
	for i, l := range lists {
		for _, nb := range l {
			out[i] = append(out[i], nb.Index)
		}
	}
	return out
}

func (lib library) docInput(seed uint64) layout.DocMapInput {
	return layout.DocMapInput{Keys: lib.keys, Vectors: lib.vectors, Neighbours: nearest(lib.vectors, 20), Seed: seed}
}

// subset is the library's points at idx, in order.
func (lib library) subset(idx []int) library {
	out := library{clusters: lib.clusters}
	for _, i := range idx {
		out.keys = append(out.keys, lib.keys[i])
		out.vectors = append(out.vectors, lib.vectors[i])
		out.cluster = append(out.cluster, lib.cluster[i])
		out.area = append(out.area, lib.area[i])
	}
	return out
}

// zoomInput is the library grouped as planted: each cluster an interest
// (its points its documents), each area an area when areas, the generalists
// unsorted, nearest the interest whose centroid is closest. Group keys are
// prefix + the cluster's or area's number.
func (lib library) zoomInput(areas bool, prefix string) layout.ZoomInput {
	in := layout.ZoomInput{Keys: lib.keys, Vectors: lib.vectors}
	seen := map[int]bool{}
	for _, cl := range lib.cluster {
		if cl >= 0 {
			seen[cl] = true
		}
	}
	clusters := slices.Sorted(maps.Keys(seen))
	present := map[int]int{} // cluster → interest index
	for x, cl := range clusters {
		present[cl] = x
	}
	areaIndex := map[int]int{}
	members := make([][]int, len(clusters))
	for i, cl := range lib.cluster {
		if cl >= 0 {
			members[present[cl]] = append(members[present[cl]], i)
		}
	}
	for x, cl := range clusters {
		g := layout.Group{Key: fmt.Sprintf("%si%02d", prefix, cl), Area: -1, Centroid: centroidOf(lib.vectors, members[x])}
		if areas {
			a := lib.area[members[x][0]]
			ai, ok := areaIndex[a]
			if !ok {
				ai = len(in.Areas)
				areaIndex[a] = ai
				in.Areas = append(in.Areas, layout.Group{Key: fmt.Sprintf("%sa%02d", prefix, a), Area: -1})
			}
			g.Area = ai
		}
		in.Interests = append(in.Interests, g)
	}
	for a := range in.Areas {
		var docs []int
		for x := range clusters {
			if in.Interests[x].Area == a {
				docs = append(docs, members[x]...)
			}
		}
		in.Areas[a].Centroid = centroidOf(lib.vectors, docs)
	}
	for i, cl := range lib.cluster {
		near, sim := -1, 0.0
		interest := -1
		if cl >= 0 {
			interest = present[cl]
		} else {
			for x, g := range in.Interests {
				if s := cos32(lib.vectors[i], g.Centroid); near < 0 || s > sim {
					near, sim = x, s
				}
			}
		}
		in.Interest = append(in.Interest, interest)
		in.Nearest = append(in.Nearest, near)
		in.Similarity = append(in.Similarity, sim)
	}
	return in
}

func centroidOf(vecs [][]float32, idx []int) []float32 {
	sum := make([]float64, len(vecs[0]))
	for _, i := range idx {
		for d, x := range vecs[i] {
			sum[d] += float64(x)
		}
	}
	return unitF32(sum)
}

// positions are a layout's points as quality measures them.
func positions(p []layout.XY) [][2]float64 {
	out := make([][2]float64, len(p))
	for i, q := range p {
		out[i] = [2]float64{q.X, q.Y}
	}
	return out
}

// keyed are a layout's points by key.
func keyed(keys []string, p []layout.XY) map[string][2]float64 {
	out := make(map[string][2]float64, len(keys))
	for i, k := range keys {
		out[k] = [2]float64{p[i].X, p[i].Y}
	}
	return out
}

// priorOf is a layout's points as a prior.
func priorOf(keys []string, p []layout.XY) map[string]layout.XY {
	out := make(map[string]layout.XY, len(keys))
	for i, k := range keys {
		out[k] = p[i]
	}
	return out
}

// shuffle returns a seeded permutation of 0..n-1.
func shuffle(n int, seed uint64) []int {
	return rand.New(rand.NewPCG(seed, 0x5f1e)).Perm(n)
}
