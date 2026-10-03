package louvain

import (
	"cmp"
	"context"
	"encoding/binary"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// decodeGraph reads a small symmetric graph with positive weights from
// data: a node count, then (from, to, weight) triples; a repeated pair
// keeps its first weight and a self-loop is skipped.
func decodeGraph(data []byte) [][]Edge {
	if len(data) == 0 {
		return [][]Edge{}
	}
	n := int(data[0]%16) + 1
	data = data[1:]
	w := map[[2]int]float64{}
	for len(data) >= 3 {
		i, j := int(data[0])%n, int(data[1])%n
		weight := float64(data[2]%8+1) / 4
		data = data[3:]
		if i == j {
			continue
		}
		if _, ok := w[[2]int{i, j}]; ok {
			continue
		}
		w[[2]int{i, j}], w[[2]int{j, i}] = weight, weight
		if len(w) >= 4*n {
			break
		}
	}
	adj := make([][]Edge, n)
	for e, weight := range w {
		adj[e[0]] = append(adj[e[0]], Edge{To: e[1], Weight: weight})
	}
	for i := range adj {
		slices.SortFunc(adj[i], func(a, b Edge) int { return cmp.Compare(a.To, b.To) })
	}
	return adj
}

// FuzzRun checks Run's contract on small graphs: a community per node,
// numbered by first node, the same result twice, a fixpoint, and never a
// lower modularity than the start.
func FuzzRun(f *testing.F) {
	f.Add([]byte{5, 0, 1, 3, 1, 2, 3, 2, 0, 3, 3, 4, 7}, []byte{0, 0, 1, 1, 255}, uint8(4), uint64(1))
	f.Add([]byte{11, 0, 1, 7, 1, 2, 7, 2, 0, 7, 3, 4, 7, 4, 5, 7, 5, 3, 7, 2, 3, 0, 6, 7, 1, 7, 8, 1, 8, 9, 2}, []byte(nil), uint8(1), uint64(9))
	f.Add([]byte{3}, []byte{1, 1, 1}, uint8(0), uint64(0))
	f.Add([]byte{15, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 0, 1, 14, 2}, []byte{0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1}, uint8(12), uint64(3))
	f.Fuzz(func(t *testing.T, graph, start []byte, res uint8, seed uint64) {
		adj := decodeGraph(graph)
		g, err := NewGraph(adj)
		require.NoError(t, err, "the decoder builds valid graphs")
		var init []int
		if len(start) > 0 {
			init = make([]int, g.Len())
			for i := range init {
				init[i] = int(start[i%len(start)]%5) - 1 // -1..3
			}
		}
		opts := Options{Resolution: 0.25 + float64(res%16)/4, Seed: seed}
		ctx := context.Background()
		r, err := Run(ctx, g, init, opts)
		require.NoError(t, err)
		require.Len(t, r.Communities, g.Len())
		requireCanonical(t, r.Communities)

		again, err := Run(ctx, g, init, opts)
		require.NoError(t, err)
		require.Equal(t, r, again, "deterministic")

		if !r.Stats.RestartCapHit {
			warm, err := Run(ctx, g, r.Communities, opts)
			require.NoError(t, err)
			require.Equal(t, r.Communities, warm.Communities, "a fixpoint")
		}

		begin, err := startPartition(g, init)
		require.NoError(t, err)
		q0, err := Modularity(g, begin, opts.Resolution)
		require.NoError(t, err)
		q, err := Modularity(g, r.Communities, opts.Resolution)
		require.NoError(t, err)
		require.GreaterOrEqual(t, q, q0-1e-9)
	})
}

// FuzzNewGraph feeds NewGraph arbitrary adjacencies: it never panics, and a
// graph it accepts is one Run can partition.
func FuzzNewGraph(f *testing.F) {
	edge := func(to int32, w float64) []byte {
		b := binary.LittleEndian.AppendUint32(nil, uint32(to))
		return binary.LittleEndian.AppendUint64(b, math.Float64bits(w))
	}
	valid := append([]byte{2, 1}, edge(1, 0.5)...)
	valid = append(append(valid, 1), edge(0, 0.5)...)
	f.Add(valid)
	f.Add(append([]byte{1, 1}, edge(0, 1)...))                          // a self-loop
	f.Add(append([]byte{2, 1}, edge(5, 1)...))                          // out of range
	f.Add(append([]byte{2, 1}, edge(1, math.NaN())...))                 // NaN weight
	f.Add(append(append([]byte{2, 1}, edge(1, 0.5)...), 1, 0, 0, 0, 0)) // truncated
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		n := int(data[0] % 8)
		data = data[1:]
		adj := make([][]Edge, n)
		for i := 0; i < n && len(data) > 0; i++ {
			count := int(data[0] % 6)
			data = data[1:]
			for range count {
				if len(data) < 12 {
					break
				}
				to := int(int32(binary.LittleEndian.Uint32(data)))
				w := math.Float64frombits(binary.LittleEndian.Uint64(data[4:]))
				adj[i] = append(adj[i], Edge{To: to, Weight: w})
				data = data[12:]
			}
		}
		g, err := NewGraph(adj)
		if err != nil {
			require.ErrorIs(t, err, ErrInvalidGraph)
			return
		}
		r, err := Run(context.Background(), g, nil, Options{Resolution: 1})
		require.NoError(t, err)
		require.Len(t, r.Communities, n)
	})
}
