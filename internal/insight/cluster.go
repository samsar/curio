// Package insight implements curio's M4 insight layer: it clusters documents
// by embedding similarity into labeled topic "interests".
//
// The design keeps the algorithm swappable. A Clusterer takes points (a doc ID
// + its vector) and returns a per-point label array (like scikit-learn's
// labels_, with -1 for noise); everything above it — medoid/cohesion math,
// labeling, persistence — is algorithm-agnostic. The shipped implementation is
// KNNGraphClusterer (a kNN graph + deterministic label propagation, with a
// noise bucket); a density-based HDBSCAN implementation could drop in behind
// the same interface later, validated against the same eval harness.
package insight

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// NoiseLabel marks a point that belongs to no cluster.
const NoiseLabel = -1

// Point is one item to cluster.
type Point struct {
	ID     string
	Vector []float32
}

// Clusterer partitions points into clusters plus noise.
//
// Cluster returns a label per input point: a non-negative cluster id, or
// NoiseLabel. len(result) == len(points) and result[i] corresponds to
// points[i]. Implementations MUST be deterministic — identical input yields
// identical labels — so runs are reproducible and unit-testable.
type Clusterer interface {
	Cluster(ctx context.Context, points []Point) ([]int, error)
	// Name identifies the algorithm (stored on the run for provenance).
	Name() string
	// Params returns the algorithm's parameters (stored as JSON on the run).
	Params() map[string]any
}

// KNNGraphOptions configures KNNGraphClusterer. Zero values fall back to
// documented defaults.
type KNNGraphOptions struct {
	// K is the number of nearest neighbors each node connects to. Default 10.
	K int
	// MinSimilarity is the cosine threshold below which an edge is dropped.
	// Default 0.5. Being rank-based (top-K) AND threshold-based, the graph
	// adapts to varying density while still cutting weak links.
	MinSimilarity float64
	// MinClusterSize drops communities smaller than this to noise. Default 3.
	MinClusterSize int
	// MaxIters caps label-propagation iterations. Default 20.
	MaxIters int
	// Center subtracts the corpus mean vector before clustering. Embedding
	// models like nomic-embed-text are anisotropic (their vectors sit in a
	// narrow cone), so raw cosines are uniformly high and everything collapses
	// into one giant cluster; centering removes that shared component so the
	// residual topical structure drives the graph. No default is applied here —
	// the config layer owns it (default true) — so a zero-value options struct
	// clusters on raw vectors.
	Center bool
}

// KNNGraphClusterer clusters via a mutual-k-nearest-neighbor graph over cosine
// similarity, then finds communities with deterministic label propagation.
// Communities below MinClusterSize become noise.
type KNNGraphClusterer struct {
	k              int
	minSim         float64
	minClusterSize int
	maxIters       int
	center         bool
}

// NewKNNGraphClusterer constructs the clusterer, applying defaults. Center is
// taken as-is (its default is owned by the config layer), so a zero-value
// options struct clusters on raw vectors.
func NewKNNGraphClusterer(opts KNNGraphOptions) *KNNGraphClusterer {
	if opts.K <= 0 {
		opts.K = 10
	}
	if opts.MinSimilarity <= 0 {
		opts.MinSimilarity = 0.5
	}
	if opts.MinClusterSize <= 0 {
		opts.MinClusterSize = 3
	}
	if opts.MaxIters <= 0 {
		opts.MaxIters = 20
	}
	return &KNNGraphClusterer{
		k:              opts.K,
		minSim:         opts.MinSimilarity,
		minClusterSize: opts.MinClusterSize,
		maxIters:       opts.MaxIters,
		center:         opts.Center,
	}
}

func (c *KNNGraphClusterer) Name() string { return "knn-graph" }

func (c *KNNGraphClusterer) Params() map[string]any {
	return map[string]any{
		"k":                c.k,
		"min_similarity":   c.minSim,
		"min_cluster_size": c.minClusterSize,
		"max_iters":        c.maxIters,
		"center":           c.center,
	}
}

// edge is a weighted graph edge to another node index.
type edge struct {
	to int
	w  float64
}

// Cluster implements Clusterer.
func (c *KNNGraphClusterer) Cluster(ctx context.Context, points []Point) ([]int, error) {
	n := len(points)
	labels := make([]int, n)
	for i := range labels {
		labels[i] = NoiseLabel
	}
	if n == 0 {
		return labels, nil
	}

	dim := len(points[0].Vector)
	if dim == 0 {
		return nil, fmt.Errorf("insight: point %s has an empty vector", points[0].ID)
	}
	for _, p := range points {
		if len(p.Vector) != dim {
			return nil, fmt.Errorf("insight: point %s has dim %d, want %d", p.ID, len(p.Vector), dim)
		}
	}

	// Optionally subtract the corpus mean vector to strip the shared anisotropy
	// component before normalizing (see KNNGraphOptions.Center).
	var mean []float64
	if c.center {
		mean = corpusMean(points, dim)
	}

	// Normalize to unit length so a dot product is the cosine similarity.
	norm := make([][]float32, n)
	for i, p := range points {
		norm[i] = unitResidual(p.Vector, mean)
	}

	// Directed top-K neighbors per node, then unioned into an undirected,
	// weighted adjacency (keeping the larger weight if an edge appears from
	// both directions).
	adjMap := make([]map[int]float64, n)
	for i := range adjMap {
		adjMap[i] = make(map[int]float64)
	}
	for i := range n {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, e := range topKNeighbors(norm, i, c.k, c.minSim) {
			if w, ok := adjMap[i][e.to]; !ok || e.w > w {
				adjMap[i][e.to] = e.w
			}
			if w, ok := adjMap[e.to][i]; !ok || e.w > w {
				adjMap[e.to][i] = e.w
			}
		}
	}
	adj := make([][]edge, n)
	for i := range adjMap {
		es := make([]edge, 0, len(adjMap[i]))
		for to, w := range adjMap[i] {
			es = append(es, edge{to: to, w: w})
		}
		sort.Slice(es, func(a, b int) bool { return es[a].to < es[b].to })
		adj[i] = es
	}

	// Label propagation. Each node starts as its own label; iterating in a
	// fixed order with the max weighted-vote (ties → smallest label) makes the
	// result deterministic. Isolated nodes keep their unique label and fall
	// out as noise below.
	lab := make([]int, n)
	for i := range lab {
		lab[i] = i
	}
	for iter := 0; iter < c.maxIters; iter++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		changed := false
		for i := range n {
			if len(adj[i]) == 0 {
				continue
			}
			score := make(map[int]float64, len(adj[i]))
			for _, e := range adj[i] {
				score[lab[e.to]] += e.w
			}
			keys := make([]int, 0, len(score))
			for k := range score {
				keys = append(keys, k)
			}
			sort.Ints(keys)
			best, bestScore := lab[i], math.Inf(-1)
			for _, k := range keys {
				if score[k] > bestScore { // strict → smallest key wins ties
					bestScore, best = score[k], k
				}
			}
			if best != lab[i] {
				lab[i] = best
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// Group by final label; keep groups >= MinClusterSize, order them
	// largest-first (ties → smallest member index), and compact to 0..m-1.
	groups := make(map[int][]int)
	for i, l := range lab {
		groups[l] = append(groups[l], i) // members appended in ascending index order
	}
	type grp struct {
		members []int
	}
	kept := make([]grp, 0, len(groups))
	labelKeys := make([]int, 0, len(groups))
	for l := range groups {
		labelKeys = append(labelKeys, l)
	}
	sort.Ints(labelKeys)
	for _, l := range labelKeys {
		if len(groups[l]) >= c.minClusterSize {
			kept = append(kept, grp{members: groups[l]})
		}
	}
	sort.SliceStable(kept, func(a, b int) bool {
		if len(kept[a].members) != len(kept[b].members) {
			return len(kept[a].members) > len(kept[b].members)
		}
		return kept[a].members[0] < kept[b].members[0]
	})
	for cid, g := range kept {
		for _, idx := range g.members {
			labels[idx] = cid
		}
	}
	return labels, nil
}

// topKNeighbors returns up to k neighbors of node i with cosine >= minSim,
// sorted by similarity desc then index asc (deterministic).
func topKNeighbors(norm [][]float32, i, k int, minSim float64) []edge {
	cands := make([]edge, 0, 16)
	for j := range norm {
		if j == i {
			continue
		}
		w := dot(norm[i], norm[j])
		if w >= minSim {
			cands = append(cands, edge{to: j, w: w})
		}
	}
	sort.Slice(cands, func(a, b int) bool {
		if cands[a].w != cands[b].w {
			return cands[a].w > cands[b].w
		}
		return cands[a].to < cands[b].to
	})
	if len(cands) > k {
		cands = cands[:k]
	}
	return cands
}

// corpusMean returns the element-wise mean of all point vectors (dim-length),
// or nil for fewer than two points (nothing to center against). Shared by the
// clusterer and the engine's cluster summarizer so both operate in the same
// (centered) space.
func corpusMean(points []Point, dim int) []float64 {
	if len(points) < 2 {
		return nil
	}
	mean := make([]float64, dim)
	for _, p := range points {
		for d, v := range p.Vector {
			mean[d] += float64(v)
		}
	}
	for d := range mean {
		mean[d] /= float64(len(points))
	}
	return mean
}

// unitResidual returns the unit vector of v, first subtracting mean when it is
// non-nil (mean-centering). A zero residual yields a zero vector, so the point
// gets no edges and falls out as noise.
func unitResidual(v []float32, mean []float64) []float32 {
	if mean == nil {
		return unit(v)
	}
	centered := make([]float64, len(v))
	var sum float64
	for i, x := range v {
		c := float64(x) - mean[i]
		centered[i] = c
		sum += c * c
	}
	out := make([]float32, len(v))
	if sum == 0 {
		return out
	}
	inv := 1.0 / math.Sqrt(sum)
	for i, c := range centered {
		out[i] = float32(c * inv)
	}
	return out
}

// unit returns a unit-length copy of v (dot(unit(a), unit(b)) == cosine(a,b)).
// A zero vector is returned unchanged (all similarities become 0 → no edges).
func unit(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		out := make([]float32, len(v))
		copy(out, v)
		return out
	}
	inv := 1.0 / math.Sqrt(sum)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out
}

// dot is the dot product of two equal-length vectors.
func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}
