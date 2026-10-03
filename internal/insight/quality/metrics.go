package quality

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/samsar/curio/internal/insight/louvain"
)

// Cohesion describes how tight interests are.
//
// An interest's cohesion is the mean over its members of max(0, cos(member,
// centroid)), the centroid being the normalized mean of the members' vectors
// (Centroids). The engine stores an interest's cohesion
// (interest_groups.cohesion) as the mean of its members' similarity to the
// same centroid, rounded to float32 as a run stores it, without the clamp:
// the two agree up to that rounding unless a member is opposite its own
// interest's centroid, which a grouping's interests don't hold in
// practice.
type Cohesion struct {
	// Mean is the unweighted mean over interests: the mean of the stored
	// cohesion column.
	Mean float64 `json:"mean"`
	// Median is the median over interests.
	Median float64 `json:"median"`
	// DocWeighted is the mean member similarity over all clustered points,
	// so a large interest counts for its size.
	DocWeighted float64 `json:"doc_weighted"`
	// PerInterest is each interest's cohesion, in Groups' order.
	PerInterest []float64 `json:"-"`
}

// CohesionOf measures groups against their centroids.
func CohesionOf(vecs [][]float32, groups [][]int, centroids [][]float64) Cohesion {
	c := Cohesion{PerInterest: make([]float64, len(groups))}
	if len(groups) == 0 {
		return c
	}
	var docSum float64
	docs := 0
	for g, members := range groups {
		var s float64
		for _, i := range members {
			s += max(dot32(vecs[i], centroids[g]), 0)
		}
		docSum += s
		docs += len(members)
		c.PerInterest[g] = s / float64(len(members))
	}
	q := QuantilesOf(c.PerInterest)
	c.Mean, c.Median = q.Mean, q.Median
	c.DocWeighted = docSum / float64(docs)
	return c
}

// Separation describes how distinct interests are from each other.
type Separation struct {
	// Nearest summarizes, over interests, the cosine between an interest's
	// centroid and the nearest other interest's centroid.
	Nearest Quantiles `json:"nearest"`
	// Duplicates counts, per threshold, the pairs of interests whose
	// centroids' cosine is at or above it: near-duplicates, pairs that
	// read as one interest cut in two.
	Duplicates []DuplicateCount `json:"duplicates"`
	// Pairs lists the pairs at or above the lowest threshold (indexes in
	// Groups' order), most similar first.
	Pairs []Pair `json:"-"`
	// NearestPer is each interest's nearest centroid cosine, in Groups'
	// order.
	NearestPer []float64 `json:"-"`
}

// DuplicateCount is how many near-duplicate pairs one threshold finds.
type DuplicateCount struct {
	Threshold float64 `json:"threshold"`
	// Pairs is how many pairs reach it, and Interests how many interests
	// are in at least one.
	Pairs     int `json:"pairs"`
	Interests int `json:"interests"`
}

// Pair is two interests (indexes in Groups' order) and their centroids'
// cosine.
type Pair struct {
	A, B   int
	Cosine float64
}

// Separate measures how far apart the centroids are, counting the pairs at
// or above each threshold as near-duplicates.
func Separate(centroids [][]float64, thresholds []float64) Separation {
	m := len(centroids)
	s := Separation{NearestPer: make([]float64, m)}
	for i := range s.NearestPer {
		s.NearestPer[i] = math.Inf(-1)
	}
	lowest := math.Inf(1)
	for _, t := range thresholds {
		lowest = min(lowest, t)
	}
	for i := range m {
		for j := i + 1; j < m; j++ {
			c := dot64(centroids[i], centroids[j])
			s.NearestPer[i] = max(s.NearestPer[i], c)
			s.NearestPer[j] = max(s.NearestPer[j], c)
			if c >= lowest {
				s.Pairs = append(s.Pairs, Pair{A: i, B: j, Cosine: c})
			}
		}
	}
	if m < 2 {
		s.NearestPer = nil
	}
	slices.SortFunc(s.Pairs, func(a, b Pair) int { return cmp.Compare(b.Cosine, a.Cosine) })
	for _, t := range thresholds {
		d := DuplicateCount{Threshold: t}
		in := map[int]bool{}
		for _, p := range s.Pairs {
			if p.Cosine >= t {
				d.Pairs++
				in[p.A], in[p.B] = true, true
			}
		}
		d.Interests = len(in)
		s.Duplicates = append(s.Duplicates, d)
	}
	s.Nearest = QuantilesOf(s.NearestPer)
	return s
}

// SplitHalfCosines is the null distribution near-duplicate thresholds are
// chosen from: each group of at least minSize members is split at random
// into two halves, and the cosine between the halves' centroids recorded. A
// pair of interests whose centroids are as close as the two halves of one
// interest are, by this measure, one interest cut in two.
func SplitHalfCosines(vecs [][]float32, groups [][]int, minSize int, seed uint64) []float64 {
	r := rand.New(rand.NewPCG(seed, 0x5eed)) //nolint:gosec // G404: a seeded, reproducible split, not a secret
	var out []float64
	for _, g := range groups {
		if len(g) < minSize {
			continue
		}
		perm := slices.Clone(g)
		r.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		half := len(perm) / 2
		out = append(out, dot64(centroid(vecs, perm[:half]), centroid(vecs, perm[half:])))
	}
	return out
}

// Silhouette returns the mean silhouette, with cosine distance (1 - cosine),
// over every clustered point: for a point in interest A, a is its mean
// distance to A's other members and b the smallest mean distance to the
// members of another interest, and its silhouette is (b-a)/max(a,b), 0 for
// the only member of an interest. It is exact, not sampled: with unit vectors
// a point's mean cosine to a group is its dot product with the group's
// vector sum over the group's size, so the cost is points × interests ×
// dimensions. 0 with fewer than two interests.
func Silhouette(vecs [][]float32, labels []int) float64 {
	groups := Groups(labels)
	if len(groups) < 2 {
		return 0
	}
	dim := len(vecs[groups[0][0]])
	sums := make([][]float64, len(groups))
	of := make(map[int]int, len(labels)) // point → group index
	for g, members := range groups {
		sums[g] = make([]float64, dim)
		for _, i := range members {
			of[i] = g
			for d, v := range vecs[i] {
				sums[g][d] += float64(v)
			}
		}
	}
	var points []int
	for _, members := range groups {
		points = append(points, members...)
	}
	sil := make([]float64, len(points))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(points)) {
		wg.Go(func() {
			for {
				k := int(next.Add(1)) - 1
				if k >= len(points) {
					return
				}
				i := points[k]
				own := of[i]
				size := len(groups[own])
				if size < 2 {
					continue // silhouette 0
				}
				self := dot32(vecs[i], f32to64(vecs[i]))
				a := 1 - (dot32(vecs[i], sums[own])-self)/float64(size-1)
				b := math.Inf(1)
				for g := range groups {
					if g == own {
						continue
					}
					b = min(b, 1-dot32(vecs[i], sums[g])/float64(len(groups[g])))
				}
				if d := max(a, b); d > 0 {
					sil[k] = (b - a) / d
				}
			}
		})
	}
	wg.Wait()
	var total float64
	for _, s := range sil {
		total += s
	}
	return total / float64(len(sil))
}

func f32to64(v []float32) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return out
}

// Modularity returns the Newman-Girvan modularity at the given resolution
// of the partition labels on the weighted undirected graph adj (every edge
// listed from both ends, as insight.KNNGraph returns it), each noise point
// counting as a community of its own: louvain.Modularity, which says how.
// It fails when adj isn't a valid graph (louvain.NewGraph) or labels doesn't
// hold a label per node.
func Modularity(adj [][]louvain.Edge, labels []int, resolution float64) (float64, error) {
	g, err := louvain.NewGraph(adj)
	if err != nil {
		return 0, fmt.Errorf("quality: modularity: %w", err)
	}
	q, err := louvain.Modularity(g, singletons(labels), resolution)
	if err != nil {
		return 0, fmt.Errorf("quality: modularity: %w", err)
	}
	return q, nil
}

// Strays describes the points left out of every interest and where they
// would go.
type Strays struct {
	Count int `json:"count"`
	// Nearest summarizes each stray's cosine to its nearest interest
	// centroid.
	Nearest Quantiles `json:"nearest"`
	// Attach counts, per threshold, the strays at or above it: those that
	// would join their nearest interest as a loose fit.
	Attach []Attachment `json:"attach"`
	// MemberLOO summarizes members' leave-one-out cosine to their own
	// interest's centroid (the centroid of the other members), the
	// comparable figure for a document already in an interest: the
	// centroid a stray is measured against doesn't include it either.
	MemberLOO Quantiles `json:"member_loo"`
	// NearestPer and NearestGroup are, per point, the nearest centroid's
	// cosine and group index (in Groups' order) for a stray, NaN and -1
	// for a member.
	NearestPer   []float64 `json:"-"`
	NearestGroup []int     `json:"-"`
}

// Attachment is how many strays reach one threshold.
type Attachment struct {
	Threshold float64 `json:"threshold"`
	Count     int     `json:"count"`
	// ShareOfStrays is Count over the strays; CoverageAfter is the
	// partition's coverage once they join.
	ShareOfStrays float64 `json:"share_of_strays"`
	CoverageAfter float64 `json:"coverage_after"`
}

// StraysOf finds each stray's nearest interest and counts the strays that
// reach each threshold.
func StraysOf(vecs [][]float32, labels []int, groups [][]int, centroids [][]float64, thresholds []float64) Strays {
	n := len(labels)
	s := Strays{NearestPer: make([]float64, n), NearestGroup: make([]int, n)}
	var nearest []float64
	clustered := 0
	for i, l := range labels {
		s.NearestPer[i], s.NearestGroup[i] = math.NaN(), -1
		if l != Noise {
			clustered++
			continue
		}
		s.Count++
		if len(centroids) == 0 {
			continue
		}
		best, bg := math.Inf(-1), -1
		for g, c := range centroids {
			if v := dot32(vecs[i], c); v > best {
				best, bg = v, g
			}
		}
		s.NearestPer[i], s.NearestGroup[i] = best, bg
		nearest = append(nearest, best)
	}
	s.Nearest = QuantilesOf(nearest)
	for _, t := range thresholds {
		a := Attachment{Threshold: t}
		for _, v := range nearest {
			if v >= t {
				a.Count++
			}
		}
		if s.Count > 0 {
			a.ShareOfStrays = float64(a.Count) / float64(s.Count)
		}
		if n > 0 {
			a.CoverageAfter = float64(clustered+a.Count) / float64(n)
		}
		s.Attach = append(s.Attach, a)
	}
	s.MemberLOO = QuantilesOf(memberLOO(vecs, groups))
	return s
}

// memberLOO returns every member's cosine to the normalized sum of its
// interest's other members, for interests of two or more.
func memberLOO(vecs [][]float32, groups [][]int) []float64 {
	var out []float64
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		sum := make([]float64, len(vecs[members[0]]))
		for _, i := range members {
			for d, v := range vecs[i] {
				sum[d] += float64(v)
			}
		}
		rest := make([]float64, len(sum))
		for _, i := range members {
			for d, v := range vecs[i] {
				rest[d] = sum[d] - float64(v)
			}
			normalize(rest)
			out = append(out, dot32(vecs[i], rest))
		}
	}
	return out
}

// AttachStrays returns labels with every stray whose nearest interest
// centroid (s.NearestPer, s.NearestGroup from StraysOf over the same labels
// and groups) is at or above threshold moved into that interest. Centroids
// are not updated as strays join.
func AttachStrays(labels []int, groups [][]int, s Strays, threshold float64) []int {
	out := slices.Clone(labels)
	for i, l := range labels {
		if l != Noise || s.NearestGroup[i] < 0 || !(s.NearestPer[i] >= threshold) {
			continue
		}
		out[i] = labels[groups[s.NearestGroup[i]][0]]
	}
	return out
}
