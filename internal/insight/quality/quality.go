// Package quality measures a clustering of documents into interests: how
// much of the library it covers, how its sizes are spread, how tight and how
// distinct its interests are, how it scores on the kNN graph it was cut
// from, where the documents it leaves out would go, how much it changes when
// the library does, and whether its labels repeat.
//
// Everything here is pure: a partition is a label per point (insight.NoiseLabel
// for a point in no interest) over the same prepared unit vectors the engine
// clusters (insight.PreparePoints), so every similarity is the cosine in the
// space the clustering saw. Nothing reads a store or calls a model. It serves
// tests and cmd/clusterreport, the developer's report on a grouping (make
// cluster-report); no part of curio's binaries uses it.
//
// Percentiles are linear interpolations between the closest ranks (numpy's
// default): for n sorted values the p-th percentile sits at rank p·(n-1).
package quality

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/louvain"
)

// Noise marks a point in no interest.
const Noise = insight.NoiseLabel

// SmallInterest is the size at or below which an interest counts as small
// in Sizes.SmallShare.
const SmallInterest = 4

// Options configures Evaluate.
type Options struct {
	// Graph is the reference graph modularity is measured on (see
	// Modularity); nil leaves modularity out.
	Graph [][]louvain.Edge
	// DuplicateThresholds are the centroid cosines at or above which two
	// interests count as a near-duplicate pair (see Separate).
	DuplicateThresholds []float64
	// StrayThresholds are the cosines at which Strays counts how many
	// unclustered points would join their nearest interest.
	StrayThresholds []float64
}

// Report is every partition-level metric for one clustering.
type Report struct {
	Documents  int        `json:"documents"`
	Coverage   float64    `json:"coverage"`
	Sizes      Sizes      `json:"sizes"`
	Cohesion   Cohesion   `json:"cohesion"`
	Separation Separation `json:"separation"`
	// Silhouette is the mean silhouette over clustered points (see
	// Silhouette).
	Silhouette float64 `json:"silhouette"`
	// Modularity is the partition's modularity on Options.Graph, nil
	// without one.
	Modularity *float64 `json:"modularity,omitempty"`
	Strays     Strays   `json:"strays"`
}

// Evaluate measures the partition labels of vecs, the prepared unit vectors
// the clustering saw (labels[i] is vecs[i]'s interest, or Noise). It fails
// when the lengths differ or Options.Graph isn't a graph of the points.
func Evaluate(vecs [][]float32, labels []int, opts Options) (Report, error) {
	if len(vecs) != len(labels) {
		return Report{}, fmt.Errorf("quality: %d labels for %d vectors", len(labels), len(vecs))
	}
	groups := Groups(labels)
	centroids := Centroids(vecs, groups)
	r := Report{
		Documents:  len(labels),
		Coverage:   Coverage(labels),
		Sizes:      SizesOf(groups, len(labels)),
		Cohesion:   CohesionOf(vecs, groups, centroids),
		Separation: Separate(centroids, opts.DuplicateThresholds),
		Silhouette: Silhouette(vecs, labels),
		Strays:     StraysOf(vecs, labels, groups, centroids, opts.StrayThresholds),
	}
	if opts.Graph != nil {
		q, err := Modularity(opts.Graph, labels, 1)
		if err != nil {
			return Report{}, err
		}
		r.Modularity = &q
	}
	return r, nil
}

// Groups returns each interest's member indexes in ascending order, largest
// interest first, ties going to the smaller label. Noise points are left out.
func Groups(labels []int) [][]int {
	by := make(map[int][]int)
	for i, l := range labels {
		if l != Noise {
			by[l] = append(by[l], i)
		}
	}
	keys := slices.Sorted(maps.Keys(by))
	slices.SortStableFunc(keys, func(a, b int) int { return cmp.Compare(len(by[b]), len(by[a])) })
	out := make([][]int, len(keys))
	for i, k := range keys {
		out[i] = by[k]
	}
	return out
}

// Relabel returns labels renumbered 0..m-1 in Groups' order (largest
// interest first), with Noise kept.
func Relabel(labels []int) []int {
	out := make([]int, len(labels))
	for i := range out {
		out[i] = Noise
	}
	for g, members := range Groups(labels) {
		for _, i := range members {
			out[i] = g
		}
	}
	return out
}

// Coverage is the share of points in an interest: 0 with no points.
func Coverage(labels []int) float64 {
	if len(labels) == 0 {
		return 0
	}
	in := 0
	for _, l := range labels {
		if l != Noise {
			in++
		}
	}
	return float64(in) / float64(len(labels))
}

// Sizes describes how interest sizes are spread.
type Sizes struct {
	// Interests is how many there are.
	Interests int     `json:"interests"`
	Mean      float64 `json:"mean"`
	Median    float64 `json:"median"`
	P90       float64 `json:"p90"`
	Max       int     `json:"max"`
	// SmallShare is the share of interests with SmallInterest or fewer
	// members.
	SmallShare float64 `json:"small_share"`
	// Top10Share is the share of all points (clustered or not) in the 10
	// largest interests.
	Top10Share float64 `json:"top10_share"`
}

// SizesOf describes groups (as Groups returns them, largest first) out of n
// points.
func SizesOf(groups [][]int, n int) Sizes {
	s := Sizes{Interests: len(groups)}
	if len(groups) == 0 {
		return s
	}
	sizes := make([]float64, len(groups))
	small, top, total := 0, 0, 0
	for i, g := range groups {
		sizes[i] = float64(len(g))
		total += len(g)
		if len(g) <= SmallInterest {
			small++
		}
		if i < 10 {
			top += len(g)
		}
		s.Max = max(s.Max, len(g))
	}
	slices.Sort(sizes)
	s.Mean = float64(total) / float64(len(groups))
	s.Median = Percentile(sizes, 0.5)
	s.P90 = Percentile(sizes, 0.9)
	s.SmallShare = float64(small) / float64(len(groups))
	if n > 0 {
		s.Top10Share = float64(top) / float64(n)
	}
	return s
}

// Percentile returns the p-th percentile (0 ≤ p ≤ 1) of sorted, linearly
// interpolated between the closest ranks; NaN when sorted is empty.
func Percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return math.NaN()
	}
	pos := p * float64(n-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, n-1)
	frac := pos - float64(lo)
	return sorted[lo] + frac*(sorted[hi]-sorted[lo])
}

// Quantiles summarizes a distribution.
type Quantiles struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	P05    float64 `json:"p05"`
	P10    float64 `json:"p10"`
	P25    float64 `json:"p25"`
	Median float64 `json:"median"`
	P75    float64 `json:"p75"`
	P90    float64 `json:"p90"`
	Max    float64 `json:"max"`
}

// QuantilesOf summarizes xs (not modified); the zero Quantiles for none.
func QuantilesOf(xs []float64) Quantiles {
	if len(xs) == 0 {
		return Quantiles{}
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	var sum float64
	for _, x := range s {
		sum += x
	}
	return Quantiles{
		N:      len(s),
		Mean:   sum / float64(len(s)),
		P05:    Percentile(s, 0.05),
		P10:    Percentile(s, 0.10),
		P25:    Percentile(s, 0.25),
		Median: Percentile(s, 0.5),
		P75:    Percentile(s, 0.75),
		P90:    Percentile(s, 0.9),
		Max:    s[len(s)-1],
	}
}

// Centroids returns each group's unit centroid: the mean of its members'
// vectors, normalized (left zero if the mean is zero). This is the centroid
// the engine summarizes an interest by, computed the same way.
func Centroids(vecs [][]float32, groups [][]int) [][]float64 {
	out := make([][]float64, len(groups))
	for g, members := range groups {
		out[g] = centroid(vecs, members)
	}
	return out
}

func centroid(vecs [][]float32, members []int) []float64 {
	if len(members) == 0 {
		return nil
	}
	c := make([]float64, len(vecs[members[0]]))
	for _, i := range members {
		for d, v := range vecs[i] {
			c[d] += float64(v)
		}
	}
	normalize(c)
	return c
}

func normalize(c []float64) {
	var n float64
	for _, v := range c {
		n += v * v
	}
	if n == 0 {
		return
	}
	n = math.Sqrt(n)
	for d := range c {
		c[d] /= n
	}
}

// dot32 is the dot product of a point and a float64 vector, summed in the
// order the engine sums a member's similarity.
func dot32(v []float32, c []float64) float64 {
	var s float64
	for d, x := range v {
		s += float64(x) * c[d]
	}
	return s
}

func dot64(a, b []float64) float64 {
	var s float64
	for d := range a {
		s += a[d] * b[d]
	}
	return s
}
