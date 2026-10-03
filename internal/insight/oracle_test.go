package insight_test

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/community"
	"gonum.org/v1/gonum/graph/iterator"
	"gonum.org/v1/gonum/graph/simple"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/louvain"
)

// oracleTolerance bounds how far apart gonum's modularity and ours may land.
// Measured over these 27 graphs: |ΔQ| at most 0.0087, ours above gonum's
// (its restarts from its own result polish a partition), and at most
// 0.0058 below.
const oracleTolerance = 0.01

// TestLouvain_MatchesGonum holds package louvain to gonum's Louvain
// (community.Modularize), the oracle it is checked against and never
// linked: on the graphs a grouping cuts at the shipped settings, the area
// graph and each area's subgraph at resolution 1, and the flat graph at
// r(n), the two find partitions of the same modularity within
// oracleTolerance.
func TestLouvain_MatchesGonum(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var worst, shortfall float64
	check := func(name string, adj [][]louvain.Edge, res float64) {
		g, err := louvain.NewGraph(adj)
		require.NoError(t, err)
		ours, err := louvain.Run(ctx, g, nil, louvain.Options{Resolution: res, Seed: 1})
		require.NoError(t, err)
		qOurs, err := louvain.Modularity(g, ours.Communities, res)
		require.NoError(t, err)
		theirs := make([]int, len(adj))
		reduced := community.Modularize(adjGraph(adj), res, rand.NewPCG(1, 0x10fa1))
		for c, members := range reduced.Communities() {
			for _, node := range members {
				theirs[node.ID()] = c
			}
		}
		qGonum, err := louvain.Modularity(g, theirs, res)
		require.NoError(t, err)
		assert.InDelta(t, qGonum, qOurs, oracleTolerance, name)
		worst = max(worst, math.Abs(qGonum-qOurs))
		shortfall = max(shortfall, qGonum-qOurs)
	}
	for _, seed := range []uint64{1, 2} {
		lib := fixtureLibrary(seed)
		points, _, err := insight.PreparePoints(lib.docs, true) // IDs ascend: the grouper's order
		require.NoError(t, err)
		areaAdj, err := insight.KNNGraph(ctx, points, 10, 0.40)
		require.NoError(t, err)
		check(fmt.Sprintf("fixture %d area graph", seed), areaAdj, 1)

		areaGraph, err := louvain.NewGraph(areaAdj)
		require.NoError(t, err)
		areas, err := louvain.Run(ctx, areaGraph, nil, louvain.Options{Resolution: 1, Seed: 1})
		require.NoError(t, err)
		for a, members := range communities(areas.Communities) {
			if len(members) < insight.MinAreaSize {
				continue
			}
			check(fmt.Sprintf("fixture %d area %d", seed, a), induced(areaAdj, members), 1)
		}

		for _, n := range []int{35, 300, 1000, 2000} {
			shape := defaultShape
			shape.docs = n
			points, _, err := insight.PreparePoints(shape.build(seed).docs, true)
			require.NoError(t, err)
			flatAdj, err := insight.KNNGraph(ctx, points, 20, math.SmallestNonzeroFloat64)
			require.NoError(t, err)
			check(fmt.Sprintf("fixture %d flat %d", seed, n), flatAdj, math.Max(1, 8*math.Sqrt(float64(n)/5254)))
		}
	}
	t.Logf("against gonum: largest |ΔQ| %.5f, largest shortfall %.5f", worst, shortfall)
}

// communities lists each community's members, ascending.
func communities(comm []int) [][]int {
	var out [][]int
	for i, c := range comm {
		for len(out) <= c {
			out = append(out, nil)
		}
		out[c] = append(out[c], i)
	}
	return out
}

// induced is the subgraph of adj on members (ascending), reindexed.
func induced(adj [][]louvain.Edge, members []int) [][]louvain.Edge {
	local := make(map[int]int, len(members))
	for k, i := range members {
		local[i] = k
	}
	out := make([][]louvain.Edge, len(members))
	for k, i := range members {
		for _, e := range adj[i] {
			if j, ok := local[e.To]; ok {
				out[k] = append(out[k], louvain.Edge{To: j, Weight: e.Weight})
			}
		}
	}
	return out
}

// adjGraph presents an adjacency to gonum as a weighted undirected graph
// whose iterators run in index order, so gonum sees the same graph the same
// way on every run (its own graphs iterate maps).
type adjGraph [][]louvain.Edge

var _ graph.WeightedUndirected = adjGraph(nil)

func (g adjGraph) Node(id int64) graph.Node {
	if id < 0 || id >= int64(len(g)) {
		return nil
	}
	return simple.Node(id)
}

func (g adjGraph) Nodes() graph.Nodes {
	nodes := make([]graph.Node, len(g))
	for i := range nodes {
		nodes[i] = simple.Node(i)
	}
	return iterator.NewOrderedNodes(nodes)
}

func (g adjGraph) From(id int64) graph.Nodes {
	if id < 0 || id >= int64(len(g)) {
		return graph.Empty
	}
	nodes := make([]graph.Node, len(g[id]))
	for i, e := range g[id] {
		nodes[i] = simple.Node(e.To)
	}
	return iterator.NewOrderedNodes(nodes)
}

// find returns the weight of the edge from x to y, if there is one.
func (g adjGraph) find(xid, yid int64) (float64, bool) {
	if xid < 0 || xid >= int64(len(g)) {
		return 0, false
	}
	es := g[xid]
	i, ok := slices.BinarySearchFunc(es, int(yid), func(e louvain.Edge, to int) int { return cmp.Compare(e.To, to) })
	if !ok {
		return 0, false
	}
	return es[i].Weight, true
}

func (g adjGraph) HasEdgeBetween(xid, yid int64) bool {
	_, ok := g.find(xid, yid)
	return ok
}

func (g adjGraph) Edge(uid, vid int64) graph.Edge { return g.WeightedEdgeBetween(uid, vid) }

func (g adjGraph) EdgeBetween(xid, yid int64) graph.Edge { return g.WeightedEdgeBetween(xid, yid) }

func (g adjGraph) WeightedEdge(uid, vid int64) graph.WeightedEdge {
	return g.WeightedEdgeBetween(uid, vid)
}

func (g adjGraph) WeightedEdgeBetween(xid, yid int64) graph.WeightedEdge {
	w, ok := g.find(xid, yid)
	if !ok {
		return nil
	}
	return simple.WeightedEdge{F: simple.Node(xid), T: simple.Node(yid), W: w}
}

// Weight reports the edge weight between x and y; a node has no self-loop.
func (g adjGraph) Weight(xid, yid int64) (float64, bool) {
	if xid == yid {
		return 0, true
	}
	return g.find(xid, yid)
}
