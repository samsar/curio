// Package insight implements curio's insight layer: it groups documents by
// embedding similarity into labeled topic "interests".
//
// The design keeps the algorithm swappable. A Clusterer takes points (a doc ID
// + its vector) and returns a per-point label array (like scikit-learn's
// labels_, with -1 for noise); everything above it — centroid/cohesion math,
// labeling, persistence — is algorithm-agnostic. The engine runs
// KNNGraphClusterer (a kNN graph + deterministic label propagation, with a
// noise bucket).
//
// A Grouper is the richer contract: broad areas holding interests, started
// from the previous grouping, with a check that splits a group grown into two
// topics. LouvainGrouper implements it with package louvain on the same kNN
// graph, and FlatGrouper adapts any Clusterer. The steps around it don't
// depend on the algorithm: merging near-duplicate interests and finding where
// strays fit (MergeNearDuplicates, AssignStrays), carrying identities from one
// grouping to the next (Carry), and placing a new document into a stored
// grouping (RunSpace).
package insight

import (
	"cmp"
	"context"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/samsar/curio/internal/insight/louvain"
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
}

// KNNGraphClusterer clusters unit-length vectors with a k-nearest-neighbor
// graph over cosine similarity, then finds communities with deterministic
// label propagation. Communities below MinClusterSize become noise.
//
// The graph is the union of every node's top-K list: i and j are joined when
// either lists the other among its K most similar points at or above
// MinSimilarity, weighted by the larger of the two similarities.
//
// Callers normalize (and optionally mean-center) vectors first, so the dot
// product is the cosine; Cluster rejects a vector that is neither unit length
// nor zero. A zero vector gets no edges and ends up as noise.
type KNNGraphClusterer struct {
	k              int
	minSim         float64
	minClusterSize int
	maxIters       int
}

// NewKNNGraphClusterer constructs the clusterer, applying defaults.
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
	}
}

func (*KNNGraphClusterer) Name() string { return "knn-graph" }

func (c *KNNGraphClusterer) Params() map[string]any {
	return map[string]any{
		"k":                c.k,
		"min_similarity":   c.minSim,
		"min_cluster_size": c.minClusterSize,
		"max_iters":        c.maxIters,
	}
}

// Cluster implements Clusterer.
func (c *KNNGraphClusterer) Cluster(ctx context.Context, points []Point) ([]int, error) {
	n := len(points)
	if n == 0 {
		return []int{}, nil
	}
	if err := checkUnitVectors(points); err != nil {
		return nil, err
	}

	// Work in ID order so the result doesn't depend on the input order: label
	// propagation visits nodes in sequence and breaks ties by the smallest
	// label, and both follow node order.
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return strings.Compare(points[a].ID, points[b].ID) })
	vecs := make([][]float32, n)
	for i, idx := range order {
		vecs[i] = points[idx].Vector
	}

	neighbors, err := knnNeighbors(ctx, vecs, c.k, c.minSim)
	if err != nil {
		return nil, err
	}
	lab, err := c.propagate(ctx, unionGraph(neighbors))
	if err != nil {
		return nil, err
	}
	clusters := c.compact(lab)

	labels := make([]int, n)
	for i, idx := range order {
		labels[idx] = clusters[i]
	}
	return labels, nil
}

// propagate runs label propagation. Each node starts as its own label; visiting
// nodes in a fixed order and taking the max weighted vote (ties → smallest
// label) makes the result deterministic. Isolated nodes keep their unique
// label and fall out as noise in compact.
func (c *KNNGraphClusterer) propagate(ctx context.Context, adj [][]louvain.Edge) ([]int, error) {
	lab := make([]int, len(adj))
	for i := range lab {
		lab[i] = i
	}
	for range c.maxIters {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		changed := false
		for i, es := range adj {
			if len(es) == 0 {
				continue
			}
			if best := bestLabel(lab, es, lab[i]); best != lab[i] {
				lab[i] = best
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return lab, nil
}

// bestLabel returns the neighbor label with the largest total edge weight, the
// smallest label winning ties; cur is kept only if no neighbor scores.
func bestLabel(lab []int, es []louvain.Edge, cur int) int {
	score := make(map[int]float64, len(es))
	for _, e := range es {
		score[lab[e.To]] += e.Weight
	}
	best, bestScore := cur, math.Inf(-1)
	for _, l := range slices.Sorted(maps.Keys(score)) {
		if score[l] > bestScore { // strict → smallest label wins ties
			bestScore, best = score[l], l
		}
	}
	return best
}

// compact turns propagated labels into cluster ids: communities of at least
// MinClusterSize, ordered largest first (ties → smallest member index) and
// numbered 0..m-1. Every other node is NoiseLabel.
func (c *KNNGraphClusterer) compact(lab []int) []int {
	groups := make(map[int][]int)
	for i, l := range lab {
		groups[l] = append(groups[l], i) // members appended in ascending index order
	}
	var kept [][]int
	for _, l := range slices.Sorted(maps.Keys(groups)) {
		if len(groups[l]) >= c.minClusterSize {
			kept = append(kept, groups[l])
		}
	}
	slices.SortStableFunc(kept, func(a, b []int) int {
		if d := cmp.Compare(len(b), len(a)); d != 0 {
			return d
		}
		return cmp.Compare(a[0], b[0])
	})

	out := make([]int, len(lab))
	for i := range out {
		out[i] = NoiseLabel
	}
	for cid, members := range kept {
		for _, idx := range members {
			out[idx] = cid
		}
	}
	return out
}
