// Package insight implements curio's insight layer: it clusters documents by
// embedding similarity into labeled topic "interests".
//
// The design keeps the algorithm swappable. A Clusterer takes points (a doc ID
// + its vector) and returns a per-point label array (like scikit-learn's
// labels_, with -1 for noise); everything above it — centroid/cohesion math,
// labeling, persistence — is algorithm-agnostic. The shipped implementation is
// KNNGraphClusterer (a kNN graph + deterministic label propagation, with a
// noise bucket); a density-based HDBSCAN implementation could drop in behind
// the same interface later, validated against the same eval harness.
package insight

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

// edge is a weighted graph edge to another node index.
type edge struct {
	to int
	w  float64
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

// unitTolerance bounds |‖v‖²-1| for a vector to count as unit length. Rounding
// a normalized 768-dimensional vector to float32 stays well inside it.
const unitTolerance = 1e-3

// checkUnitVectors verifies every point has the same non-zero dimension and
// is unit length or zero.
func checkUnitVectors(points []Point) error {
	dim := len(points[0].Vector)
	if dim == 0 {
		return fmt.Errorf("insight: point %s has an empty vector", points[0].ID)
	}
	for _, p := range points {
		if len(p.Vector) != dim {
			return fmt.Errorf("insight: point %s has dim %d, want %d", p.ID, len(p.Vector), dim)
		}
		// Written to reject NaN, which fails every comparison.
		if sq := dot(p.Vector, p.Vector); sq != 0 && !(math.Abs(sq-1) <= unitTolerance) {
			return fmt.Errorf("insight: point %s is not unit length (squared norm %g)", p.ID, sq)
		}
	}
	return nil
}

// knnNeighbors returns each node's top-k neighbors with similarity >= minSim,
// ordered by similarity descending, then index ascending. Building this is
// essentially all of the clusterer's cost (O(n²·d)), and rows are
// independent, so they are spread over GOMAXPROCS workers that share nothing
// but a row counter; each worker writes only the slots of the rows it took.
func knnNeighbors(ctx context.Context, vecs [][]float32, k int, minSim float64) ([][]edge, error) {
	n := len(vecs)
	out := make([][]edge, n)
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), n) {
		wg.Go(func() {
			best := newTopK(k)
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				out[i] = best.row(vecs, i, minSim)
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// topK keeps one row's k best neighbor candidates in a min-heap whose root is
// the worst candidate kept, so a row costs O(n log k) rather than a full sort
// of every candidate above the threshold.
type topK struct {
	k    int
	heap []edge
}

func newTopK(k int) *topK { return &topK{k: k, heap: make([]edge, 0, k)} }

// row scores node i against every other node and returns its top k, best
// first.
func (t *topK) row(vecs [][]float32, i int, minSim float64) []edge {
	t.heap = t.heap[:0]
	for j, v := range vecs {
		if j == i {
			continue
		}
		if w := dot(vecs[i], v); w >= minSim {
			t.offer(edge{to: j, w: w})
		}
	}
	best := slices.Clone(t.heap)
	slices.SortFunc(best, func(a, b edge) int {
		if c := cmp.Compare(b.w, a.w); c != 0 {
			return c
		}
		return cmp.Compare(a.to, b.to)
	})
	return best
}

// worse reports whether a ranks below b: lower similarity, or on a tie the
// higher index.
func worse(a, b edge) bool {
	if a.w != b.w {
		return a.w < b.w
	}
	return a.to > b.to
}

func (t *topK) offer(e edge) {
	if len(t.heap) < t.k {
		t.heap = append(t.heap, e)
		t.siftUp(len(t.heap) - 1)
		return
	}
	if worse(t.heap[0], e) {
		t.heap[0] = e
		t.siftDown(0)
	}
}

func (t *topK) siftUp(i int) {
	h := t.heap
	for i > 0 {
		parent := (i - 1) / 2
		if !worse(h[i], h[parent]) {
			return
		}
		h[i], h[parent] = h[parent], h[i]
		i = parent
	}
}

func (t *topK) siftDown(i int) {
	h := t.heap
	for {
		child := 2*i + 1
		if child >= len(h) {
			return
		}
		if r := child + 1; r < len(h) && worse(h[r], h[child]) {
			child = r
		}
		if !worse(h[child], h[i]) {
			return
		}
		h[i], h[child] = h[child], h[i]
		i = child
	}
}

// unionGraph merges the directed top-K lists into an undirected adjacency,
// keeping the larger weight when an edge appears from both directions. Each
// node's edges are ordered by neighbor index.
func unionGraph(neighbors [][]edge) [][]edge {
	weights := make([]map[int]float64, len(neighbors))
	for i := range weights {
		weights[i] = make(map[int]float64)
	}
	for i, es := range neighbors {
		for _, e := range es {
			if w, ok := weights[i][e.to]; !ok || e.w > w {
				weights[i][e.to] = e.w
			}
			if w, ok := weights[e.to][i]; !ok || e.w > w {
				weights[e.to][i] = e.w
			}
		}
	}
	adj := make([][]edge, len(weights))
	for i, ws := range weights {
		es := make([]edge, 0, len(ws))
		for to, w := range ws {
			es = append(es, edge{to: to, w: w})
		}
		slices.SortFunc(es, func(a, b edge) int { return cmp.Compare(a.to, b.to) })
		adj[i] = es
	}
	return adj
}

// propagate runs label propagation. Each node starts as its own label; visiting
// nodes in a fixed order and taking the max weighted vote (ties → smallest
// label) makes the result deterministic. Isolated nodes keep their unique
// label and fall out as noise in compact.
func (c *KNNGraphClusterer) propagate(ctx context.Context, adj [][]edge) ([]int, error) {
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
func bestLabel(lab []int, es []edge, cur int) int {
	score := make(map[int]float64, len(es))
	for _, e := range es {
		score[lab[e.to]] += e.w
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

// dot is the dot product of two equal-length vectors.
func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}
