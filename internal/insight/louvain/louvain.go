// Package louvain finds communities in a weighted undirected graph with the
// Louvain method (Blondel et al., 2008), and can start from a given
// partition.
//
// The objective is Newman-Girvan modularity with resolution γ:
//
//	Q = Σ_c [ in_c / 2m − γ (tot_c / 2m)² ]
//
// where in_c is the weight of the edges inside community c counted from both
// ends, tot_c the total degree of c's nodes and 2m the total degree. A higher
// γ gives more, smaller communities.
//
// Run alternates two phases until a level merges nothing. Local moving visits
// a level's nodes in a seeded order, pass after pass until a pass moves
// nothing, and moves each node to the neighbouring community that raises Q
// most. Aggregation then makes each community one node of the next level.
//
// Start: without a starting partition every node starts alone, as in plain
// Louvain. With one (a warm start), nodes given equal values start together,
// and each node given a negative value, in node order, starts in the
// community it is most strongly connected to among those assigned so far
// (ties to the smallest), or alone when it has no assigned neighbour. A node
// without edges is always a community of its own: no move changes Q for it,
// so it would otherwise stay wherever it started.
//
// Ties: a node moves only when its best community beats staying by more than
// GainTolerance, and candidates are compared in ascending community number,
// so the smallest number wins a tie.
//
// Fixpoint: the top level of a multi-level result need not be a local optimum
// of the first level, so a warm start from it can still move nodes. Run
// restarts from its own result until a restart changes nothing, so a result
// r always satisfies Run(g, r) = r: an unchanged library rebuilt from its
// last grouping gets the same grouping. Every restart that changes the
// partition raises Q, so the restarts end.
//
// Determinism: a result depends only on the graph, the starting partition
// and the Options, never on map order, GOMAXPROCS or the clock. Results are
// numbered 0..k-1 in the order of each community's first node.
//
// Caps: local moving stops after MaxPasses passes on a level and the
// restarts after MaxRestarts; Stats reports either cap being hit.
package louvain

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
)

// GainTolerance is how much a move must raise modularity, in edge-weight
// units (the gain times half the total degree), before a node makes it, so
// float noise never moves a node back and forth.
const GainTolerance = 1e-12

// Defaults for the Options caps.
const (
	DefaultMaxPasses   = 100
	DefaultMaxRestarts = 20
)

// pcgStream is the second PCG seed word; the first is Options.Seed.
const pcgStream = 0x10fa2

// Options configures Run and RefineSplit.
type Options struct {
	// Resolution is γ; it must be finite and positive.
	Resolution float64
	// Seed seeds the order local moving visits nodes in: a permutation
	// per level.
	Seed uint64
	// MaxPasses caps local moving's passes over one level, and MaxRestarts
	// the restarts from a run's own result. Zero takes the default.
	MaxPasses   int
	MaxRestarts int
}

// resolve validates o and applies the defaults.
func (o Options) resolve() (Options, error) {
	if math.IsNaN(o.Resolution) || math.IsInf(o.Resolution, 0) || o.Resolution <= 0 {
		return o, fmt.Errorf("louvain: resolution %g, want a finite positive value", o.Resolution)
	}
	if o.MaxPasses < 0 || o.MaxRestarts < 0 {
		return o, fmt.Errorf("louvain: negative cap (passes %d, restarts %d)", o.MaxPasses, o.MaxRestarts)
	}
	if o.MaxPasses == 0 {
		o.MaxPasses = DefaultMaxPasses
	}
	if o.MaxRestarts == 0 {
		o.MaxRestarts = DefaultMaxRestarts
	}
	return o, nil
}

// Result is a partition Run found.
type Result struct {
	// Communities holds every node's community, numbered 0..k-1 in the
	// order of each community's first node.
	Communities []int
	Stats       Stats
}

// Stats describes how a run went.
type Stats struct {
	// Levels is the node count of each level the last pass through the
	// levels visited, the graph's own first; each is smaller than the one
	// before it.
	Levels []int
	// Passes counts local-moving passes over every level and restart.
	Passes int
	// PassCapHit reports that local moving on some level was stopped by
	// MaxPasses while it still moved nodes.
	PassCapHit bool
	// Restarts counts the warm starts from the run's own result, the one
	// that found nothing to change included.
	Restarts int
	// RestartCapHit reports that the restarts were stopped by MaxRestarts
	// while they still changed the partition, so the result may not be a
	// fixpoint.
	RestartCapHit bool
}

// Run finds communities of g, starting from init: nil starts every node
// alone; otherwise init holds a value per node, equal non-negative values
// starting together and a negative value starting beside its neighbours
// (see the package doc). It returns ctx's error, and no partition, when ctx
// ends first.
func Run(ctx context.Context, g *Graph, init []int, opts Options) (Result, error) {
	if g == nil {
		return Result{}, errNilGraph
	}
	opts, err := opts.resolve()
	if err != nil {
		return Result{}, err
	}
	start, err := startPartition(g, init)
	if err != nil {
		return Result{}, err
	}
	var st Stats
	cur, levels, err := passLevels(ctx, baseLevel(g), start, opts, &st)
	if err != nil {
		return Result{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if st.Restarts == opts.MaxRestarts {
			st.RestartCapHit = true
			break
		}
		st.Restarts++
		next, nextLevels, err := passLevels(ctx, baseLevel(g), cur, opts, &st)
		if err != nil {
			return Result{}, err
		}
		levels = nextLevels
		if slices.Equal(next, cur) {
			break
		}
		cur = next
	}
	st.Levels = levels
	return Result{Communities: cur, Stats: st}, nil
}

// startPartition turns init into communities numbered 0..k-1 by first node.
func startPartition(g *Graph, init []int) ([]int, error) {
	n := g.Len()
	if init == nil {
		return identity(n), nil
	}
	if len(init) != n {
		return nil, fmt.Errorf("louvain: %d starting communities for %d nodes", len(init), n)
	}
	comm := make([]int, n)
	ids := make(map[int]int)
	next := 0
	for i, c := range init {
		comm[i] = -1
		switch {
		case g.k[i] == 0:
			comm[i] = next
			next++
		case c >= 0:
			id, ok := ids[c]
			if !ok {
				id = next
				ids[c] = id
				next++
			}
			comm[i] = id
		}
	}
	placeBesideNeighbours(g, comm, next)
	out, _ := renumber(comm)
	return out, nil
}

// placeBesideNeighbours gives every node still without a community (-1),
// in node order, the community it is most strongly connected to among
// those assigned so far, ties to the smallest, or a new one numbered from
// next when it has no assigned neighbour.
func placeBesideNeighbours(g *Graph, comm []int, next int) {
	weight := make([]float64, len(comm))
	var touched []int
	for i := range comm {
		if comm[i] >= 0 {
			continue
		}
		for _, e := range g.adj[i] {
			c := comm[e.To]
			if c < 0 {
				continue
			}
			if weight[c] == 0 { // weights are positive: zero is untouched
				touched = append(touched, c)
			}
			weight[c] += e.Weight
		}
		best, bestW := -1, 0.0
		for _, c := range touched {
			if w := weight[c]; w > bestW || (w == bestW && c < best) {
				best, bestW = c, w
			}
			weight[c] = 0
		}
		touched = touched[:0]
		if best < 0 {
			best = next
			next++
		}
		comm[i] = best
	}
}

// passLevels runs local moving and aggregation from the partition start of
// lv's nodes until a level merges nothing, and returns every node's
// community numbered by first node and the node count of each level. It
// adds its passes to st.
func passLevels(ctx context.Context, lv *level, start []int, opts Options, st *Stats) (comm, levels []int, err error) {
	n := lv.n
	if lv.m2 == 0 {
		return identity(n), []int{n}, nil
	}
	rng := rand.New(rand.NewPCG(opts.Seed, pcgStream)) //nolint:gosec // G404: a seeded visiting order, not a secret
	comm = slices.Clone(start)
	membership := identity(n)
	for {
		levels = append(levels, lv.n)
		passes, capped, err := lv.localMove(ctx, comm, opts, rng)
		st.Passes += passes
		st.PassCapHit = st.PassCapHit || capped
		if err != nil {
			return nil, nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		next, nodeMap := lv.aggregate(comm)
		for i, node := range membership {
			membership[i] = nodeMap[node]
		}
		if next.n == lv.n {
			break // the level merged nothing
		}
		lv = next
		comm = identity(lv.n)
	}
	// Aggregated nodes are numbered by first member at every level, so the
	// top level's communities already are numbered by first node.
	return membership, levels, nil
}

// level is one level's graph: its nodes are communities of the level below.
type level struct {
	n     int
	edges [][]Edge  // to other nodes, ascending by neighbour
	self  []float64 // weight inside the node, counted from both ends
	k     []float64 // degree, self included
	m2    float64   // total degree
}

// baseLevel is g as the first level.
func baseLevel(g *Graph) *level {
	return &level{n: g.Len(), edges: g.adj, self: make([]float64, g.Len()), k: g.k, m2: g.m2}
}

// localMove moves nodes, in one seeded order for every pass, to the
// neighbouring community that most raises modularity, until a pass moves
// nothing or MaxPasses passes have run (capped reports the latter). comm
// holds communities in [0, n) and is updated in place.
func (lv *level) localMove(ctx context.Context, comm []int, opts Options, rng *rand.Rand) (passes int, capped bool, err error) {
	tot := make([]float64, lv.n)
	for i, c := range comm {
		tot[c] += lv.k[i]
	}
	toComm := make([]float64, lv.n)
	seen := make([]bool, lv.n)
	var touched []int
	order := rng.Perm(lv.n)
	for passes < opts.MaxPasses {
		if err := ctx.Err(); err != nil {
			return passes, false, err
		}
		passes++
		moved := false
		for _, i := range order {
			own := comm[i]
			for _, e := range lv.edges[i] {
				c := comm[e.To]
				if !seen[c] {
					seen[c] = true
					touched = append(touched, c)
				}
				toComm[c] += e.Weight
			}
			tot[own] -= lv.k[i]
			slices.Sort(touched)
			best := bestCommunity(own, lv.k[i], touched, toComm, tot, opts.Resolution, lv.m2)
			tot[best] += lv.k[i]
			if best != own {
				comm[i] = best
				moved = true
			}
			for _, c := range touched {
				toComm[c] = 0
				seen[c] = false
			}
			touched = touched[:0]
		}
		if !moved {
			return passes, false, nil
		}
	}
	return passes, true, nil
}

// bestCommunity picks the community for a node of degree ki now outside
// own: the candidate with the largest gain, where a candidate must beat the
// best so far (staying, to begin with) by more than GainTolerance.
// candidates are ascending, so the smallest wins a tie. toComm holds the
// node's edge weight to each community and tot each community's degree
// without the node.
func bestCommunity(own int, ki float64, candidates []int, toComm, tot []float64, resolution, m2 float64) int {
	best, bestGain := own, toComm[own]-resolution*tot[own]*ki/m2
	for _, c := range candidates {
		if c == own {
			continue
		}
		if gain := toComm[c] - resolution*tot[c]*ki/m2; gain > bestGain+GainTolerance {
			best, bestGain = c, gain
		}
	}
	return best
}

// aggregate builds the next level, one node per community of comm numbered
// by first member, and returns it with the map from lv's nodes to it.
func (lv *level) aggregate(comm []int) (*level, []int) {
	nodeMap, m := renumber(comm)
	next := &level{n: m, edges: make([][]Edge, m), self: make([]float64, m), k: make([]float64, m), m2: lv.m2}
	// Each community's members, ascending, by a counting sort.
	start := make([]int, m+1)
	for _, c := range nodeMap {
		start[c+1]++
	}
	for c := range m {
		start[c+1] += start[c]
	}
	members := make([]int, lv.n)
	fill := slices.Clone(start[:m])
	for i, c := range nodeMap {
		members[fill[c]] = i
		fill[c]++
	}

	acc := make([]float64, m)
	seen := make([]bool, m)
	var touched []int
	for c := range m {
		for _, i := range members[start[c]:start[c+1]] {
			next.self[c] += lv.self[i]
			next.k[c] += lv.k[i]
			for _, e := range lv.edges[i] {
				d := nodeMap[e.To]
				if d == c {
					next.self[c] += e.Weight
					continue
				}
				if !seen[d] {
					seen[d] = true
					touched = append(touched, d)
				}
				acc[d] += e.Weight
			}
		}
		slices.Sort(touched)
		es := make([]Edge, len(touched))
		for t, d := range touched {
			es[t] = Edge{To: d, Weight: acc[d]}
			acc[d] = 0
			seen[d] = false
		}
		next.edges[c] = es
		touched = touched[:0]
	}
	return next, nodeMap
}

// renumber numbers the communities of comm, values in [0, len(comm)),
// 0..k-1 in the order of their first member, and returns them and k.
func renumber(comm []int) ([]int, int) {
	id := make([]int, len(comm))
	for c := range id {
		id[c] = -1
	}
	out := make([]int, len(comm))
	k := 0
	for i, c := range comm {
		if id[c] < 0 {
			id[c] = k
			k++
		}
		out[i] = id[c]
	}
	return out, k
}

func identity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
