package quality

import (
	"cmp"
	"math"
	"slices"
)

// The interest map's measures: how well a map of the points keeps their
// neighbourhoods and their areas together, and how far a map's points moved
// from a previous one. A map is a position per point; positions are compared
// by Euclidean distance, ties to the lower index.

// NeighbourPreservation is the mean over the points of the share of each
// point's first k nearest neighbours in the space (near[i], best first, as
// the grouping's neighbour pass finds them) that are among its k nearest on
// the map. A point with fewer than k neighbours in the space is measured on
// those it has; one with none is left out. It is 0 for no point measured.
func NeighbourPreservation(near [][]int, pos [][2]float64, k int) float64 {
	onMap := MapNeighbours(pos, k)
	var sum float64
	measured := 0
	for i, list := range near {
		list = list[:min(k, len(list))]
		if len(list) == 0 {
			continue
		}
		found := 0
		for _, j := range list {
			if slices.Contains(onMap[i], j) {
				found++
			}
		}
		sum += float64(found) / float64(len(list))
		measured++
	}
	if measured == 0 {
		return 0
	}
	return sum / float64(measured)
}

// MapNeighbours are each point's k nearest other points on the map, nearest
// first, ties to the lower index.
func MapNeighbours(pos [][2]float64, k int) [][]int {
	type cand struct {
		j int
		d float64
	}
	out := make([][]int, len(pos))
	for i, p := range pos {
		cands := make([]cand, 0, len(pos)-1)
		for j, q := range pos {
			if j != i {
				cands = append(cands, cand{j, (p[0]-q[0])*(p[0]-q[0]) + (p[1]-q[1])*(p[1]-q[1])})
			}
		}
		slices.SortFunc(cands, func(a, b cand) int { return cmp.Or(cmp.Compare(a.d, b.d), cmp.Compare(a.j, b.j)) })
		row := make([]int, min(k, len(cands)))
		for x := range row {
			row[x] = cands[x].j
		}
		out[i] = row
	}
	return out
}

// MapPurity is the mean over the labelled points (label not Noise) of the
// share of each one's k nearest labelled points on the map that share its
// label: how well the map keeps areas together. It is 0 with fewer than two
// labelled points.
func MapPurity(pos [][2]float64, labels []int, k int) float64 {
	var idx []int
	for i, l := range labels {
		if l != Noise {
			idx = append(idx, i)
		}
	}
	sub := make([][2]float64, len(idx))
	for x, i := range idx {
		sub[x] = pos[i]
	}
	near := MapNeighbours(sub, k)
	return purity(len(idx), func(x int) (int, []int) {
		ls := make([]int, len(near[x]))
		for y, z := range near[x] {
			ls[y] = labels[idx[z]]
		}
		return labels[idx[x]], ls
	})
}

// SpacePurity is MapPurity in the space: each labelled point's first k
// labelled neighbours of near[i] (best first) stand for its nearest on the
// map. A point whose list holds no labelled point is left out.
func SpacePurity(near [][]int, labels []int, k int) float64 {
	var idx []int
	for i, l := range labels {
		if l != Noise {
			idx = append(idx, i)
		}
	}
	return purity(len(idx), func(x int) (int, []int) {
		i := idx[x]
		var ls []int
		for _, j := range near[i] {
			if labels[j] != Noise {
				ls = append(ls, labels[j])
				if len(ls) == k {
					break
				}
			}
		}
		return labels[i], ls
	})
}

// purity averages, over n points, the share of a point's neighbours'
// labels equal to its own, leaving out a point without neighbours.
func purity(n int, of func(x int) (int, []int)) float64 {
	var sum float64
	measured := 0
	for x := range n {
		own, ls := of(x)
		if len(ls) == 0 {
			continue
		}
		same := 0
		for _, l := range ls {
			if l == own {
				same++
			}
		}
		sum += float64(same) / float64(len(ls))
		measured++
	}
	if measured == 0 {
		return 0
	}
	return sum / float64(measured)
}

// Displacement is how far the points two maps share moved: the mean and
// the largest distance between a shared key's positions, each a share of
// prev's diameter, as served (Mean, Max) and after the least-squares
// similarity transform (rotation or reflection, uniform scale, translation)
// that best maps next's shared points onto prev's (AlignedMean,
// AlignedMax). Shared counts them; with none, every share is 0.
type Displacement struct {
	Shared                  int
	Mean, Max               float64
	AlignedMean, AlignedMax float64
}

// Displace measures the displacement from prev to next.
func Displace(prev, next map[string][2]float64) Displacement {
	keys := make([]string, 0, len(prev))
	for key := range prev {
		if _, ok := next[key]; ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	d := Displacement{Shared: len(keys)}
	all := make([][2]float64, 0, len(prev))
	for _, p := range prev {
		all = append(all, p)
	}
	diameter := Diameter(all)
	if len(keys) == 0 || diameter == 0 {
		return d
	}
	src, dst := make([][2]float64, len(keys)), make([][2]float64, len(keys))
	for x, key := range keys {
		src[x], dst[x] = next[key], prev[key]
	}
	d.Mean, d.Max = shift(src, dst, diameter)
	d.AlignedMean, d.AlignedMax = shift(similarityAlign(src, dst), dst, diameter)
	return d
}

// shift is the mean and largest distance from a[x] to b[x], over scale.
func shift(a, b [][2]float64, scale float64) (mean, largest float64) {
	for x := range a {
		d := math.Hypot(a[x][0]-b[x][0], a[x][1]-b[x][1])
		mean += d
		largest = math.Max(largest, d)
	}
	return mean / float64(len(a)) / scale, largest / scale
}

// similarityAlign maps src onto dst by the least-squares similarity
// transform (Umeyama's), reflection allowed.
func similarityAlign(src, dst [][2]float64) [][2]float64 {
	cs, cd := mean2(src), mean2(dst)
	var sxx, sxy, syx, syy, ss float64
	for x := range src {
		ax, ay := src[x][0]-cs[0], src[x][1]-cs[1]
		bx, by := dst[x][0]-cd[0], dst[x][1]-cd[1]
		sxx += ax * bx
		sxy += ax * by
		syx += ay * bx
		syy += ay * by
		ss += ax*ax + ay*ay
	}
	// The best rotation's angle and the best reflection's, and the larger
	// of the two traces they reach.
	rot := math.Atan2(sxy-syx, sxx+syy)
	refl := math.Atan2(sxy+syx, sxx-syy)
	rotTrace := math.Hypot(sxx+syy, sxy-syx)
	reflTrace := math.Hypot(sxx-syy, sxy+syx)
	c, s, flip, trace := math.Cos(rot), math.Sin(rot), 1.0, rotTrace
	if reflTrace > rotTrace {
		c, s, flip, trace = math.Cos(refl), math.Sin(refl), -1.0, reflTrace
	}
	scale := 1.0
	if ss > 0 {
		scale = trace / ss
	}
	out := make([][2]float64, len(src))
	for x, p := range src {
		ax, ay := p[0]-cs[0], flip*(p[1]-cs[1])
		out[x] = [2]float64{cd[0] + scale*(c*ax-s*ay), cd[1] + scale*(s*ax+c*ay)}
	}
	return out
}

func mean2(p [][2]float64) [2]float64 {
	var m [2]float64
	for _, q := range p {
		m[0] += q[0]
		m[1] += q[1]
	}
	return [2]float64{m[0] / float64(len(p)), m[1] / float64(len(p))}
}

// Diameter is the largest distance between two of the points: over their
// convex hull, so it costs O(n log n + h²).
func Diameter(pos [][2]float64) float64 {
	h := hull(pos)
	var d float64
	for i := range h {
		for j := i + 1; j < len(h); j++ {
			d = math.Max(d, math.Hypot(h[i][0]-h[j][0], h[i][1]-h[j][1]))
		}
	}
	return d
}

// hull is the points' convex hull by Andrew's monotone chain.
func hull(pos [][2]float64) [][2]float64 {
	pts := slices.Clone(pos)
	slices.SortFunc(pts, func(a, b [2]float64) int { return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1])) })
	if len(pts) < 3 {
		return pts
	}
	cross := func(o, a, b [2]float64) float64 { return (a[0]-o[0])*(b[1]-o[1]) - (a[1]-o[1])*(b[0]-o[0]) }
	var h [][2]float64
	for _, p := range pts {
		for len(h) >= 2 && cross(h[len(h)-2], h[len(h)-1], p) <= 0 {
			h = h[:len(h)-1]
		}
		h = append(h, p)
	}
	lower := len(h) + 1
	for i := len(pts) - 2; i >= 0; i-- {
		for len(h) >= lower && cross(h[len(h)-2], h[len(h)-1], pts[i]) <= 0 {
			h = h[:len(h)-1]
		}
		h = append(h, pts[i])
	}
	return h[:len(h)-1]
}
