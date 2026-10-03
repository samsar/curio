package louvain

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
)

// ErrInvalidGraph is wrapped by every error NewGraph returns.
var ErrInvalidGraph = errors.New("louvain: invalid graph")

var errNilGraph = errors.New("louvain: nil graph")

// Edge is one weighted edge from a node to its neighbour To.
type Edge struct {
	To     int
	Weight float64
}

// Graph is a weighted undirected graph NewGraph has validated: no
// self-loops, positive finite weights, each node's edges strictly ascending
// by neighbour, and every edge listed from both ends with the same weight.
type Graph struct {
	adj [][]Edge
	k   []float64 // degree
	m2  float64   // total degree, twice the total edge weight
}

// NewGraph validates adj, node i's edges being adj[i], and returns it as a
// Graph. The Graph keeps adj, so the caller must not change it afterwards.
// Every error wraps ErrInvalidGraph and names the node and neighbour at
// fault.
func NewGraph(adj [][]Edge) (*Graph, error) {
	for i, row := range adj {
		for k, e := range row {
			if err := checkEdge(len(adj), i, e); err != nil {
				return nil, err
			}
			if k > 0 && row[k-1].To >= e.To {
				return nil, fmt.Errorf("%w: node %d: neighbour %d follows %d, want strictly ascending neighbours",
					ErrInvalidGraph, i, e.To, row[k-1].To)
			}
		}
	}
	// Rows are known to be sorted now, so the way back can be searched.
	for i, row := range adj {
		for _, e := range row {
			back, ok := slices.BinarySearchFunc(adj[e.To], i, func(b Edge, to int) int { return cmp.Compare(b.To, to) })
			if !ok {
				return nil, fmt.Errorf("%w: node %d: edge to %d has no edge back", ErrInvalidGraph, i, e.To)
			}
			if w := adj[e.To][back].Weight; math.Float64bits(w) != math.Float64bits(e.Weight) {
				return nil, fmt.Errorf("%w: node %d: edge to %d weighs %g, but %g back",
					ErrInvalidGraph, i, e.To, e.Weight, w)
			}
		}
	}
	return newGraph(adj), nil
}

// checkEdge validates one edge of node i on its own.
func checkEdge(n, i int, e Edge) error {
	switch {
	case e.To < 0 || e.To >= n:
		return fmt.Errorf("%w: node %d: neighbour %d out of range [0, %d)", ErrInvalidGraph, i, e.To, n)
	case e.To == i:
		return fmt.Errorf("%w: node %d: self-loop", ErrInvalidGraph, i)
	case !(e.Weight > 0) || math.IsInf(e.Weight, 1): // written to reject NaN
		return fmt.Errorf("%w: node %d: edge to %d weighs %g, want a positive finite weight",
			ErrInvalidGraph, i, e.To, e.Weight)
	}
	return nil
}

// newGraph wraps an adjacency already known to be valid.
func newGraph(adj [][]Edge) *Graph {
	g := &Graph{adj: adj, k: make([]float64, len(adj))}
	for i, row := range adj {
		for _, e := range row {
			g.k[i] += e.Weight
		}
		g.m2 += g.k[i]
	}
	return g
}

// Len returns the number of nodes.
func (g *Graph) Len() int { return len(g.adj) }

// Induced returns the subgraph on members, which must be strictly ascending
// node indexes; node k of the subgraph is members[k]. A subgraph of a valid
// graph is valid, so it is not checked again.
func (g *Graph) Induced(members []int) (*Graph, error) {
	n := len(g.adj)
	local := make([]int, n) // a node's index in members plus one; 0 outside
	prev := -1
	for k, i := range members {
		if i < 0 || i >= n {
			return nil, fmt.Errorf("louvain: induced subgraph: member %d out of range [0, %d)", i, n)
		}
		if i <= prev {
			return nil, fmt.Errorf("louvain: induced subgraph: member %d follows %d, want strictly ascending members", i, prev)
		}
		prev = i
		local[i] = k + 1
	}
	adj := make([][]Edge, len(members))
	for k, i := range members {
		for _, e := range g.adj[i] {
			if j := local[e.To]; j > 0 {
				adj[k] = append(adj[k], Edge{To: j - 1, Weight: e.Weight})
			}
		}
	}
	return newGraph(adj), nil
}
