package louvain

import (
	"cmp"
	"context"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planted returns n points around `centers` random directions in dim
// dimensions, unit length, and the center each was drawn around. Seeded.
func planted(seed uint64, n, dim, centers int, noise float64) ([][]float64, []int) {
	r := rand.New(rand.NewPCG(seed, 0))
	cs := make([][]float64, centers)
	for c := range cs {
		cs[c] = make([]float64, dim)
		for d := range cs[c] {
			cs[c][d] = r.NormFloat64()
		}
	}
	pts := make([][]float64, n)
	truth := make([]int, n)
	for i := range pts {
		c := r.IntN(centers)
		truth[i] = c
		v := make([]float64, dim)
		var s float64
		for d := range v {
			v[d] = cs[c][d] + noise*r.NormFloat64()
			s += v[d] * v[d]
		}
		for d := range v {
			v[d] /= math.Sqrt(s)
		}
		pts[i] = v
	}
	return pts, truth
}

// knn is the union of every point's top-k neighbours at or above minSim
// (ties to the lower index), weighted by the larger cosine: the shape of
// graph insight builds, serially.
func knn(pts [][]float64, k int, minSim float64) [][]Edge {
	weights := make([]map[int]float64, len(pts))
	for i := range weights {
		weights[i] = map[int]float64{}
	}
	for i := range pts {
		var cands []Edge
		for j := range pts {
			if j == i {
				continue
			}
			var w float64
			for d := range pts[i] {
				w += pts[i][d] * pts[j][d]
			}
			if w >= minSim {
				cands = append(cands, Edge{To: j, Weight: w})
			}
		}
		slices.SortFunc(cands, func(a, b Edge) int {
			if c := cmp.Compare(b.Weight, a.Weight); c != 0 {
				return c
			}
			return cmp.Compare(a.To, b.To)
		})
		for _, e := range cands[:min(k, len(cands))] {
			weights[i][e.To] = max(weights[i][e.To], e.Weight)
			weights[e.To][i] = max(weights[e.To][i], e.Weight)
		}
	}
	adj := make([][]Edge, len(pts))
	for i, ws := range weights {
		for to, w := range ws {
			adj[i] = append(adj[i], Edge{To: to, Weight: w})
		}
		slices.SortFunc(adj[i], func(a, b Edge) int { return cmp.Compare(a.To, b.To) })
	}
	return adj
}

// plantedGraph is a validated kNN graph over planted clusters, and the truth.
func plantedGraph(t testing.TB, seed uint64, n, dim, centers int, noise float64, k int, minSim float64) (*Graph, []int) {
	t.Helper()
	pts, truth := planted(seed, n, dim, centers, noise)
	g, err := NewGraph(knn(pts, k, minSim))
	require.NoError(t, err)
	return g, truth
}

// samePartition reports whether two labelings group the nodes alike.
func samePartition(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	fwd, back := map[int]int{}, map[int]int{}
	for i := range a {
		if v, ok := fwd[a[i]]; ok && v != b[i] {
			return false
		}
		if v, ok := back[b[i]]; ok && v != a[i] {
			return false
		}
		fwd[a[i]], back[b[i]] = b[i], a[i]
	}
	return true
}

func modularity(t testing.TB, g *Graph, comm []int, res float64) float64 {
	t.Helper()
	q, err := Modularity(g, comm, res)
	require.NoError(t, err)
	return q
}

func run(t testing.TB, g *Graph, init []int, opts Options) Result {
	t.Helper()
	r, err := Run(context.Background(), g, init, opts)
	require.NoError(t, err)
	return r
}

// requireCanonical checks comm is numbered 0..k-1 by first node.
func requireCanonical(t testing.TB, comm []int) {
	t.Helper()
	next := 0
	for i, c := range comm {
		require.LessOrEqual(t, c, next, "node %d: community %d before %d was used", i, c, next)
		require.GreaterOrEqual(t, c, 0)
		if c == next {
			next++
		}
	}
}

func TestNewGraph_Rejects(t *testing.T) {
	ok := func() [][]Edge {
		return [][]Edge{
			{{To: 1, Weight: 0.5}, {To: 2, Weight: 0.25}},
			{{To: 0, Weight: 0.5}},
			{{To: 0, Weight: 0.25}},
		}
	}
	cases := []struct {
		name   string
		mutate func([][]Edge) [][]Edge
		want   string
	}{
		{"neighbour out of range", func(a [][]Edge) [][]Edge { a[1] = append(a[1], Edge{To: 3, Weight: 1}); return a },
			"node 1: neighbour 3 out of range [0, 3)"},
		{"negative neighbour", func(a [][]Edge) [][]Edge { a[2] = []Edge{{To: -1, Weight: 1}, {To: 0, Weight: 0.25}}; return a },
			"node 2: neighbour -1 out of range [0, 3)"},
		{"self-loop", func(a [][]Edge) [][]Edge { a[1] = []Edge{{To: 0, Weight: 0.5}, {To: 1, Weight: 1}}; return a },
			"node 1: self-loop"},
		{"descending row", func(a [][]Edge) [][]Edge { a[0][0], a[0][1] = a[0][1], a[0][0]; return a },
			"node 0: neighbour 1 follows 2, want strictly ascending neighbours"},
		{"duplicate neighbour", func(a [][]Edge) [][]Edge { a[1] = append(a[1], Edge{To: 0, Weight: 0.5}); return a },
			"node 1: neighbour 0 follows 0, want strictly ascending neighbours"},
		{"zero weight", func(a [][]Edge) [][]Edge { a[0][1].Weight, a[2][0].Weight = 0, 0; return a },
			"node 0: edge to 2 weighs 0, want a positive finite weight"},
		{"negative weight", func(a [][]Edge) [][]Edge { a[0][1].Weight, a[2][0].Weight = -1, -1; return a },
			"node 0: edge to 2 weighs -1, want a positive finite weight"},
		{"NaN weight", func(a [][]Edge) [][]Edge { a[2][0].Weight = math.NaN(); return a },
			"node 2: edge to 0 weighs NaN, want a positive finite weight"},
		{"infinite weight", func(a [][]Edge) [][]Edge { a[1][0].Weight = math.Inf(1); return a },
			"node 1: edge to 0 weighs +Inf, want a positive finite weight"},
		{"no edge back", func(a [][]Edge) [][]Edge { a[2] = nil; return a },
			"node 0: edge to 2 has no edge back"},
		{"weight differs back", func(a [][]Edge) [][]Edge { a[1][0].Weight = math.Nextafter(0.5, 1); return a },
			"node 0: edge to 1 weighs 0.5, but 0.5000000000000001 back"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewGraph(tc.mutate(ok()))
			require.ErrorIs(t, err, ErrInvalidGraph)
			assert.Equal(t, "louvain: invalid graph: "+tc.want, err.Error())
		})
	}

	g, err := NewGraph(ok())
	require.NoError(t, err)
	assert.Equal(t, 3, g.Len())
	assert.InDelta(t, 1.5, g.m2, 1e-15)
	empty, err := NewGraph(nil)
	require.NoError(t, err)
	assert.Zero(t, empty.Len())
}

func TestInduced(t *testing.T) {
	g, err := NewGraph([][]Edge{
		{{To: 1, Weight: 0.9}, {To: 2, Weight: 0.8}},
		{{To: 0, Weight: 0.9}},
		{{To: 0, Weight: 0.8}, {To: 3, Weight: 0.7}},
		{{To: 2, Weight: 0.7}},
	})
	require.NoError(t, err)
	sub, err := g.Induced([]int{0, 2, 3})
	require.NoError(t, err)
	assert.Equal(t, [][]Edge{
		{{To: 1, Weight: 0.8}},
		{{To: 0, Weight: 0.8}, {To: 2, Weight: 0.7}},
		{{To: 1, Weight: 0.7}},
	}, sub.adj)
	assert.InDelta(t, 3.0, sub.m2, 1e-12, "degrees count only the subgraph's edges")

	for _, members := range [][]int{{0, 0}, {2, 1}, {0, 4}, {-1}} {
		_, err := g.Induced(members)
		require.Error(t, err, "%v", members)
	}
}

func TestRun_FindsPlantedClusters(t *testing.T) {
	g, truth := plantedGraph(t, 1, 400, 32, 4, 0.5, 10, 0.3)
	r := run(t, g, nil, Options{Resolution: 1, Seed: 1})
	assert.True(t, samePartition(truth, r.Communities))
	requireCanonical(t, r.Communities)
}

func TestRun_WarmStartKeepsAGoodPartition(t *testing.T) {
	g, truth := plantedGraph(t, 4, 400, 32, 4, 0.5, 10, 0.3)
	assert.True(t, samePartition(truth, run(t, g, truth, Options{Resolution: 1, Seed: 1}).Communities))

	// Nodes the start doesn't know find their cluster from wherever they
	// start.
	init := slices.Clone(truth)
	for i := 0; i < len(init); i += 7 {
		init[i] = -1
	}
	assert.True(t, samePartition(truth, run(t, g, init, Options{Resolution: 1, Seed: 1}).Communities))
}

// TestRun_StartNumberingDoesNotMatter: only which nodes start together
// counts, not the values that say so.
func TestRun_StartNumberingDoesNotMatter(t *testing.T) {
	g, truth := plantedGraph(t, 2, 300, 16, 6, 1.0, 10, 1e-9)
	init := make([]int, len(truth))
	shifted := make([]int, len(truth))
	for i, c := range truth {
		init[i] = c % 3 // three starting groups, each mixing two clusters
		shifted[i] = 1000 - 7*(c%3)
	}
	opts := Options{Resolution: 1, Seed: 5}
	assert.Equal(t, run(t, g, init, opts).Communities, run(t, g, shifted, opts).Communities)
}

func TestRun_IsAFixpoint(t *testing.T) {
	for seed := uint64(1); seed <= 6; seed++ {
		g, truth := plantedGraph(t, seed, 600, 16, 12, 1.0, 10, 1e-9)
		for _, res := range []float64{0.5, 1, 3} {
			opts := Options{Resolution: res, Seed: seed}
			fresh := run(t, g, nil, opts)
			assert.Equal(t, fresh.Communities, run(t, g, fresh.Communities, opts).Communities,
				"seed %d res %g: fresh", seed, res)

			start := slices.Clone(truth)
			for i := 0; i < len(start); i += 5 {
				start[i] = -1
			}
			warm := run(t, g, start, opts)
			assert.Equal(t, warm.Communities, run(t, g, warm.Communities, opts).Communities,
				"seed %d res %g: warm", seed, res)
		}
	}
}

func TestRun_ModularityNeverDecreases(t *testing.T) {
	g, truth := plantedGraph(t, 3, 500, 16, 10, 1.0, 10, 1e-9)
	r := rand.New(rand.NewPCG(3, 3))
	for trial := range 20 {
		start := slices.Clone(truth)
		for i := range start {
			if r.IntN(4) == 0 {
				start[i] = r.IntN(14) // a quarter of the nodes in a random group
			}
		}
		res := 0.5 + float64(trial%4)
		got := run(t, g, start, Options{Resolution: res, Seed: uint64(trial)})
		assert.GreaterOrEqual(t, modularity(t, g, got.Communities, res), modularity(t, g, start, res)-1e-9, "trial %d", trial)
	}
}

func TestRun_LevelsShrink(t *testing.T) {
	g, _ := plantedGraph(t, 5, 800, 16, 12, 1.0, 10, 1e-9)
	st := run(t, g, nil, Options{Resolution: 1, Seed: 1}).Stats
	require.NotEmpty(t, st.Levels)
	assert.Equal(t, g.Len(), st.Levels[0])
	for l := 1; l < len(st.Levels); l++ {
		assert.Less(t, st.Levels[l], st.Levels[l-1], "level %d", l)
	}
	assert.Positive(t, st.Passes)
	assert.Positive(t, st.Restarts, "the result's own warm start is the last restart")
	assert.False(t, st.PassCapHit)
	assert.False(t, st.RestartCapHit)
}

func TestBestCommunity_TieGoesToSmallest(t *testing.T) {
	// The node has edge weight 1 to communities 2 and 5 of equal degree:
	// their gains are bit-identical, and staying gains nothing.
	toComm := []float64{0, 0, 1, 0, 0, 1}
	tot := []float64{3, 0, 4, 0, 0, 4}
	assert.Equal(t, 2, bestCommunity(0, 1, []int{2, 5}, toComm, tot, 1, 10))
	// A gain within GainTolerance of the best so far loses to it.
	toComm[5] = 1 + GainTolerance/2
	assert.Equal(t, 2, bestCommunity(0, 1, []int{2, 5}, toComm, tot, 1, 10))
	toComm[5] = 1 + 4*GainTolerance
	assert.Equal(t, 5, bestCommunity(0, 1, []int{2, 5}, toComm, tot, 1, 10))
	// Staying wins a tie with moving.
	assert.Equal(t, 2, bestCommunity(2, 1, []int{2, 5}, []float64{0, 0, 1, 0, 0, 1}, tot, 1, 10))
}

func TestRun_Caps(t *testing.T) {
	g, _ := plantedGraph(t, 6, 600, 16, 12, 1.0, 20, 1e-9)
	st := run(t, g, nil, Options{Resolution: 1, Seed: 1, MaxPasses: 1}).Stats
	assert.True(t, st.PassCapHit, "one pass from singletons still moves nodes")

	// A graph whose first restart still changes the partition (checked
	// below) stops at a single restart.
	uncapped := run(t, g, nil, Options{Resolution: 4, Seed: 2}).Stats
	require.GreaterOrEqual(t, uncapped.Restarts, 2, "the premise: this run needs more than one restart")
	st = run(t, g, nil, Options{Resolution: 4, Seed: 2, MaxRestarts: 1}).Stats
	assert.True(t, st.RestartCapHit)
	assert.Equal(t, 1, st.Restarts)
}

func TestRun_RejectsBadInput(t *testing.T) {
	g, _ := plantedGraph(t, 1, 20, 4, 2, 0.5, 3, 0.1)
	ctx := context.Background()
	for _, res := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		_, err := Run(ctx, g, nil, Options{Resolution: res})
		require.Error(t, err, "resolution %g", res)
	}
	_, err := Run(ctx, g, make([]int, 19), Options{Resolution: 1})
	require.EqualError(t, err, "louvain: 19 starting communities for 20 nodes")
	_, err = Run(ctx, g, nil, Options{Resolution: 1, MaxPasses: -1})
	require.Error(t, err)
	_, err = Run(ctx, nil, nil, Options{Resolution: 1})
	require.Error(t, err)
}

func TestRun_Canceled(t *testing.T) {
	g, truth := plantedGraph(t, 1, 300, 16, 4, 0.8, 10, 0.2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Run(ctx, g, nil, Options{Resolution: 1})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, r.Communities, "no partial result")
	s, err := RefineSplit(ctx, g, truth, Options{Resolution: 1})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, s.Communities)
}

func TestRun_Deterministic(t *testing.T) {
	g, _ := plantedGraph(t, 3, 600, 16, 10, 1.0, 10, 1e-9)
	opts := Options{Resolution: 2, Seed: 7}
	first := run(t, g, nil, opts)
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	assert.Equal(t, first, run(t, g, nil, opts))
	runtime.GOMAXPROCS(4)
	assert.Equal(t, first, run(t, g, nil, opts))
}

func TestRun_EmptyAndEdgeless(t *testing.T) {
	empty, err := NewGraph([][]Edge{})
	require.NoError(t, err)
	assert.Empty(t, run(t, empty, nil, Options{Resolution: 1}).Communities)

	edgeless, err := NewGraph(make([][]Edge, 3))
	require.NoError(t, err)
	assert.Equal(t, []int{0, 1, 2}, run(t, edgeless, []int{0, 0, 0}, Options{Resolution: 1}).Communities)

	// An isolated node never stays in the group it started in.
	g, err := NewGraph([][]Edge{{{To: 1, Weight: 1}}, {{To: 0, Weight: 1}}, {}})
	require.NoError(t, err)
	assert.Equal(t, []int{0, 0, 1}, run(t, g, []int{4, 4, 4}, Options{Resolution: 1}).Communities)
}

func TestStartPartition(t *testing.T) {
	g, err := NewGraph([][]Edge{
		{{To: 1, Weight: 0.9}, {To: 2, Weight: 0.2}},
		{{To: 0, Weight: 0.9}},
		{{To: 0, Weight: 0.2}, {To: 3, Weight: 0.8}},
		{{To: 2, Weight: 0.8}},
		{{To: 5, Weight: 0.1}},
		{{To: 4, Weight: 0.1}},
	})
	require.NoError(t, err)
	// 1 joins 0's group; 2 joins 3's (0.8 beats 0.2); 4 has no assigned
	// neighbour and starts alone, and 5 then joins it.
	got, err := startPartition(g, []int{10, -1, -1, 20, -1, -1})
	require.NoError(t, err)
	assert.Equal(t, []int{0, 0, 1, 1, 2, 2}, got)
	got, err = startPartition(g, nil)
	require.NoError(t, err)
	assert.Equal(t, []int{0, 1, 2, 3, 4, 5}, got, "every node alone")
}

func TestAggregate(t *testing.T) {
	g, err := NewGraph([][]Edge{
		{{To: 1, Weight: 1}, {To: 3, Weight: 2}},
		{{To: 0, Weight: 1}, {To: 2, Weight: 4}},
		{{To: 1, Weight: 4}, {To: 3, Weight: 8}},
		{{To: 0, Weight: 2}, {To: 2, Weight: 8}},
	})
	require.NoError(t, err)
	next, nodeMap := baseLevel(g).aggregate([]int{2, 0, 2, 0})
	assert.Equal(t, []int{0, 1, 0, 1}, nodeMap, "numbered by first member")
	assert.Equal(t, [][]Edge{{{To: 1, Weight: 1 + 4 + 2 + 8}}, {{To: 0, Weight: 1 + 4 + 2 + 8}}}, next.edges)
	assert.Equal(t, []float64{0, 0}, next.self, "no edge inside either group")
	assert.Equal(t, []float64{3 + 12, 5 + 10}, next.k)
	assert.InDelta(t, g.m2, next.m2, 0)

	merged, _ := baseLevel(g).aggregate([]int{0, 0, 0, 0})
	assert.Equal(t, []float64{g.m2}, merged.self)
	assert.Equal(t, [][]Edge{{}}, merged.edges)
}

func TestRefineSplit_PartsAGluedCommunity(t *testing.T) {
	g, truth := plantedGraph(t, 8, 400, 32, 4, 0.5, 10, 0.3)
	// Glue clusters 0 and 1 together: the split check must part them.
	glued := slices.Clone(truth)
	for i, c := range glued {
		if c == 1 {
			glued[i] = 0
		}
	}
	opts := Options{Resolution: 1, Seed: 1}
	s, err := RefineSplit(context.Background(), g, glued, opts)
	require.NoError(t, err)
	assert.True(t, samePartition(truth, s.Communities))
	assert.Equal(t, 1, s.Divided)
	requireCanonical(t, s.Communities)
	assert.Greater(t, modularity(t, g, s.Communities, 1), modularity(t, g, glued, 1))

	// The planted truth has nothing to gain.
	s, err = RefineSplit(context.Background(), g, truth, opts)
	require.NoError(t, err)
	assert.True(t, samePartition(truth, s.Communities))
	assert.Zero(t, s.Divided)
}

func TestRefineSplit_LeavesAUniformClique(t *testing.T) {
	const n = 12
	adj := make([][]Edge, n)
	for i := range adj {
		for j := range n {
			if j != i {
				adj[i] = append(adj[i], Edge{To: j, Weight: 0.5})
			}
		}
	}
	g, err := NewGraph(adj)
	require.NoError(t, err)
	s, err := RefineSplit(context.Background(), g, make([]int, n), Options{Resolution: 1})
	require.NoError(t, err)
	assert.Equal(t, make([]int, n), s.Communities)
	assert.Zero(t, s.Divided)
}

func TestRefineSplit_NeverLowersModularity(t *testing.T) {
	g, truth := plantedGraph(t, 9, 500, 16, 10, 1.0, 10, 1e-9)
	r := rand.New(rand.NewPCG(9, 9))
	for trial := range 10 {
		comm := slices.Clone(truth)
		for i := range comm {
			comm[i] %= 2 + r.IntN(5) // merge clusters in assorted ways
		}
		s, err := RefineSplit(context.Background(), g, comm, Options{Resolution: 1, Seed: uint64(trial)})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, modularity(t, g, s.Communities, 1), modularity(t, g, comm, 1)-1e-9, "trial %d", trial)
	}
}

func TestGainOfSplit(t *testing.T) {
	// Two disconnected pairs glued into one community: Q is 0 glued and
	// 2 × (2/4 − (2/4)²) = 1/2 split. In GainTolerance's units, times
	// half the total degree (m = 2), the gain is 1.
	g, err := NewGraph([][]Edge{
		{{To: 1, Weight: 1}}, {{To: 0, Weight: 1}},
		{{To: 3, Weight: 1}}, {{To: 2, Weight: 1}},
	})
	require.NoError(t, err)
	lv := baseLevel(g)
	assert.InDelta(t, 1, gainOfSplit(lv, []int{0, 0, 1, 1}, 2, 1), 1e-15)
	assert.InDelta(t, 0, gainOfSplit(lv, []int{0, 0, 0, 0}, 1, 1), 1e-12)
}

func TestModularity(t *testing.T) {
	// Two triangles joined by one edge, every weight 1: m = 7, 2m = 14.
	// Each triangle holds 3 edges (6 counted from both ends) and degree 7:
	// Q = 2 × (6/14 − (7/14)²) = 5/14.
	g, err := NewGraph([][]Edge{
		{{To: 1, Weight: 1}, {To: 2, Weight: 1}},
		{{To: 0, Weight: 1}, {To: 2, Weight: 1}},
		{{To: 0, Weight: 1}, {To: 1, Weight: 1}, {To: 3, Weight: 1}},
		{{To: 2, Weight: 1}, {To: 4, Weight: 1}, {To: 5, Weight: 1}},
		{{To: 3, Weight: 1}, {To: 5, Weight: 1}},
		{{To: 3, Weight: 1}, {To: 4, Weight: 1}},
	})
	require.NoError(t, err)
	assert.InDelta(t, 5.0/14, modularity(t, g, []int{7, 7, 7, -2, -2, -2}, 1), 1e-15)
	assert.InDelta(t, 2*(6.0/14-2*0.25), modularity(t, g, []int{0, 0, 0, 1, 1, 1}, 2), 1e-15)

	_, err = Modularity(g, []int{0}, 1)
	require.Error(t, err)
	_, err = Modularity(g, make([]int, 6), math.NaN())
	require.Error(t, err)
	edgeless, err := NewGraph(make([][]Edge, 2))
	require.NoError(t, err)
	assert.Zero(t, modularity(t, edgeless, []int{0, 1}, 1))
}
