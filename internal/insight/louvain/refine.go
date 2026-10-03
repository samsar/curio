package louvain

import (
	"context"
	"fmt"
	"slices"
)

// Split is RefineSplit's result.
type Split struct {
	// Communities holds every node's community, numbered by first node.
	Communities []int
	// Divided counts the communities that were split.
	Divided int
	// Stats covers the runs inside the communities; Levels and the
	// restart fields stay zero.
	Stats Stats
}

// RefineSplit offers every community of comm (a value per node, any ints,
// equal values together) a split. Louvain runs inside the community alone,
// from singletons, scoring moves by the modularity of the whole graph (each
// node keeps its degree in g and the total degree stays g's), and the split
// is kept only when it raises that modularity by more than GainTolerance.
// Local moving and aggregation can merge communities but never split one, so
// a warm start alone holds a community together after it has grown into two
// topics; this is the step that parts them, the role of Leiden's
// refinement. A caller that wants a fixpoint runs Run from the result.
func RefineSplit(ctx context.Context, g *Graph, comm []int, opts Options) (Split, error) {
	if g == nil {
		return Split{}, errNilGraph
	}
	n := g.Len()
	opts, err := opts.resolve()
	if err != nil {
		return Split{}, err
	}
	if len(comm) != n {
		return Split{}, fmt.Errorf("louvain: %d communities for %d nodes", len(comm), n)
	}
	out, k := relabel(comm)
	if g.m2 == 0 {
		return Split{Communities: out}, nil
	}

	// Each community's members, ascending, by a counting sort.
	start := make([]int, k+1)
	for _, c := range out {
		start[c+1]++
	}
	for c := range k {
		start[c+1] += start[c]
	}
	members := make([]int, n)
	fill := slices.Clone(start[:k])
	for i, c := range out {
		members[fill[c]] = i
		fill[c]++
	}

	var res Split
	local := make([]int, n) // a node's index in its community plus one
	next := k
	for c := range k {
		if err := ctx.Err(); err != nil {
			return Split{}, err
		}
		ms := members[start[c]:start[c+1]]
		if len(ms) < 2 {
			continue
		}
		sub := subLevel(g, ms, local)
		parts, _, err := passLevels(ctx, sub, identity(len(ms)), opts, &res.Stats)
		if err != nil {
			return Split{}, err
		}
		numParts := 0
		for _, p := range parts {
			numParts = max(numParts, p+1)
		}
		if numParts < 2 || gainOfSplit(sub, parts, numParts, opts.Resolution) <= GainTolerance {
			continue
		}
		res.Divided++
		// Part 0 keeps the number c and the others take new ones; the
		// renumbering below orders them all by first node.
		for idx, p := range parts {
			if p > 0 {
				out[ms[idx]] = next + p - 1
			}
		}
		next += numParts - 1
	}
	res.Communities, _ = renumber(out)
	return res, nil
}

// subLevel is the level of community members ms of g alone: the edges
// between them, every node keeping its degree in g and the total degree
// g's. local is scratch of g's size, all zero, and is left so.
func subLevel(g *Graph, ms, local []int) *level {
	for k, i := range ms {
		local[i] = k + 1
	}
	sub := &level{n: len(ms), edges: make([][]Edge, len(ms)), self: make([]float64, len(ms)), k: make([]float64, len(ms)), m2: g.m2}
	for k, i := range ms {
		sub.k[k] = g.k[i]
		for _, e := range g.adj[i] {
			if j := local[e.To]; j > 0 {
				sub.edges[k] = append(sub.edges[k], Edge{To: j - 1, Weight: e.Weight})
			}
		}
	}
	for _, i := range ms {
		local[i] = 0
	}
	return sub
}

// gainOfSplit is the change in modularity from splitting the community sub
// into numParts parts, in GainTolerance's units: times half the total
// degree, as local moving scores a move.
func gainOfSplit(sub *level, parts []int, numParts int, resolution float64) float64 {
	in := make([]float64, numParts)
	tot := make([]float64, numParts)
	var inAll, totAll float64
	for i, es := range sub.edges {
		p := parts[i]
		tot[p] += sub.k[i]
		totAll += sub.k[i]
		for _, e := range es {
			inAll += e.Weight
			if parts[e.To] == p {
				in[p] += e.Weight
			}
		}
	}
	// Each term is a community's share of Q times the total degree.
	gain := -(inAll - resolution*totAll*totAll/sub.m2)
	for p := range numParts {
		gain += in[p] - resolution*tot[p]*tot[p]/sub.m2
	}
	return gain / 2
}

// relabel numbers the communities of comm, any ints, 0..k-1 in the order of
// their first member, and returns them and k.
func relabel(comm []int) ([]int, int) {
	ids := make(map[int]int)
	out := make([]int, len(comm))
	for i, c := range comm {
		id, ok := ids[c]
		if !ok {
			id = len(ids)
			ids[c] = id
		}
		out[i] = id
	}
	return out, len(ids)
}
