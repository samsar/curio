package insight

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/samsar/curio/internal/insight/louvain"
)

// The graphs LouvainGrouper groups on, both cut from one pass that finds
// every point's graphK nearest neighbours: the flat shape uses every edge
// of positive weight (Louvain takes no other), the areas shape each point's
// areaK nearest at areaMinSimilarity or above. A sorted top-20 list cut to
// its first 10 is exactly the top-10 list, so one O(n²·d) pass serves both.
const (
	graphK            = 20
	areaK             = 10
	areaMinSimilarity = 0.40
)

// KNNGraph returns the undirected graph KNNGraphClusterer runs label
// propagation on, over points in the order given: the union of every
// point's top-k neighbours at or above minSim, weighted by the larger of
// the two similarities, each node's edges ascending by neighbour and every
// edge listed from both ends. Points must be unit length or zero, as for
// Cluster. Neighbour ties are broken by index, so a caller that wants the
// clusterer's exact graph passes points sorted by ID, as Cluster sorts
// them. k must be at least 1 and minSim a number. It is exported to
// measure partitions on that graph.
func KNNGraph(ctx context.Context, points []Point, k int, minSim float64) ([][]louvain.Edge, error) {
	if k < 1 {
		return nil, fmt.Errorf("insight: %d nearest neighbours, want at least 1", k)
	}
	if math.IsNaN(minSim) {
		return nil, errors.New("insight: a NaN minimum similarity")
	}
	if len(points) == 0 {
		return [][]louvain.Edge{}, nil
	}
	if err := checkUnitVectors(points); err != nil {
		return nil, err
	}
	neighbors, err := knnNeighbors(ctx, vectorsOf(points), k, minSim)
	if err != nil {
		return nil, err
	}
	return unionGraph(neighbors), nil
}

// neighbourLists holds every point's graphK nearest neighbours of positive
// similarity, best first (ties to the lower index), the pass both of
// LouvainGrouper's graphs are cut from.
type neighbourLists [][]louvain.Edge

// nearestNeighbours makes the one O(n²·d) pass of a grouping.
func nearestNeighbours(ctx context.Context, vecs [][]float32) (neighbourLists, error) {
	// The smallest positive float keeps every positive similarity and no
	// other.
	return knnNeighbors(ctx, vecs, graphK, math.SmallestNonzeroFloat64)
}

// flatGraph is the union of the full lists: KNNGraph at graphK and any
// positive similarity.
func (l neighbourLists) flatGraph() [][]louvain.Edge { return unionGraph(l) }

// areaGraph is the union of each list's first areaK entries at or above
// areaMinSimilarity: KNNGraph at areaK and areaMinSimilarity, since a list
// is sorted best first.
func (l neighbourLists) areaGraph() [][]louvain.Edge {
	cut := make([][]louvain.Edge, len(l))
	for i, es := range l {
		es = es[:min(areaK, len(es))]
		n := 0
		for n < len(es) && es[n].Weight >= areaMinSimilarity {
			n++
		}
		cut[i] = es[:n]
	}
	return unionGraph(cut)
}

func vectorsOf(points []Point) [][]float32 {
	vecs := make([][]float32, len(points))
	for i, p := range points {
		vecs[i] = p.Vector
	}
	return vecs
}

// unitTolerance bounds |‖v‖²-1| for a vector to count as unit length. Rounding
// a normalized vector of a few thousand dimensions to float32 stays well
// inside it.
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
// ordered by similarity descending, then index ascending; k is at least 1.
// Building this is essentially all of the clusterer's cost (O(n²·d)), and
// rows are independent, so they are spread over GOMAXPROCS workers that share
// nothing but a row counter; each worker writes only the slots of the rows it
// took.
func knnNeighbors(ctx context.Context, vecs [][]float32, k int, minSim float64) ([][]louvain.Edge, error) {
	n := len(vecs)
	out := make([][]louvain.Edge, n)
	// A row has n-1 candidates, so a larger k keeps them all.
	k = min(k, n-1)
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
	heap []louvain.Edge
}

func newTopK(k int) *topK { return &topK{k: k, heap: make([]louvain.Edge, 0, k)} }

// row scores node i against every other node and returns its top k, best
// first.
func (t *topK) row(vecs [][]float32, i int, minSim float64) []louvain.Edge {
	t.heap = t.heap[:0]
	for j, v := range vecs {
		if j == i {
			continue
		}
		if w := dot(vecs[i], v); w >= minSim {
			t.offer(louvain.Edge{To: j, Weight: w})
		}
	}
	best := slices.Clone(t.heap)
	slices.SortFunc(best, func(a, b louvain.Edge) int {
		if c := cmp.Compare(b.Weight, a.Weight); c != 0 {
			return c
		}
		return cmp.Compare(a.To, b.To)
	})
	return best
}

// worse reports whether a ranks below b: lower similarity, or on a tie the
// higher index.
func worse(a, b louvain.Edge) bool {
	if a.Weight != b.Weight {
		return a.Weight < b.Weight
	}
	return a.To > b.To
}

func (t *topK) offer(e louvain.Edge) {
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
func unionGraph(neighbors [][]louvain.Edge) [][]louvain.Edge {
	weights := make([]map[int]float64, len(neighbors))
	for i := range weights {
		weights[i] = make(map[int]float64)
	}
	for i, es := range neighbors {
		for _, e := range es {
			if w, ok := weights[i][e.To]; !ok || e.Weight > w {
				weights[i][e.To] = e.Weight
			}
			if w, ok := weights[e.To][i]; !ok || e.Weight > w {
				weights[e.To][i] = e.Weight
			}
		}
	}
	adj := make([][]louvain.Edge, len(weights))
	for i, ws := range weights {
		es := make([]louvain.Edge, 0, len(ws))
		for to, w := range ws {
			es = append(es, louvain.Edge{To: to, Weight: w})
		}
		slices.SortFunc(es, func(a, b louvain.Edge) int { return cmp.Compare(a.To, b.To) })
		adj[i] = es
	}
	return adj
}

// dot is the dot product of two equal-length vectors.
func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}
