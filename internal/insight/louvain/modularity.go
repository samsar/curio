package louvain

import (
	"fmt"
	"math"
)

// Modularity returns the modularity Q (see the package doc) of the
// partition comm of g's nodes at the given resolution: comm holds a value
// per node, any ints, equal values sharing a community. It is 0 for a graph
// without edges, and an error when comm doesn't hold a value per node or
// the resolution isn't finite.
func Modularity(g *Graph, comm []int, resolution float64) (float64, error) {
	if g == nil {
		return 0, errNilGraph
	}
	if len(comm) != g.Len() {
		return 0, fmt.Errorf("louvain: %d communities for %d nodes", len(comm), g.Len())
	}
	if math.IsNaN(resolution) || math.IsInf(resolution, 0) {
		return 0, fmt.Errorf("louvain: resolution %g, want a finite value", resolution)
	}
	if g.m2 == 0 {
		return 0, nil
	}
	dense, k := relabel(comm)
	in := make([]float64, k)
	tot := make([]float64, k)
	for i, row := range g.adj {
		c := dense[i]
		tot[c] += g.k[i]
		for _, e := range row {
			if dense[e.To] == c {
				in[c] += e.Weight
			}
		}
	}
	var q float64
	for c := range k {
		share := tot[c] / g.m2
		q += in[c]/g.m2 - resolution*share*share
	}
	return q, nil
}
