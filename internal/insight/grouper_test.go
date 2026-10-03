package insight

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight/louvain"
)

func TestShapeGate(t *testing.T) {
	c := NewLouvainGrouper(nil).c
	cases := []struct {
		cur      Shape
		n        int
		coverage float64
		want     Shape
		measured bool // whether the area pass runs at all
	}{
		{ShapeFlat, 999, 1, ShapeFlat, false},
		{ShapeFlat, 1000, 0.80, ShapeAreas, true},
		{ShapeFlat, 1000, math.Nextafter(0.80, 0), ShapeFlat, true},
		{ShapeFlat, 5000, 0.75, ShapeFlat, true},
		{ShapeAreas, 900, 0.70, ShapeAreas, true},
		{ShapeAreas, 900, math.Nextafter(0.70, 0), ShapeFlat, true},
		{ShapeAreas, 899, 1, ShapeFlat, false},
		{ShapeAreas, 950, 0.75, ShapeAreas, true}, // between the thresholds: stays
		{ShapeAreas, 5000, 0.79, ShapeAreas, true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.measured, c.needsCoverage(tc.cur, tc.n), "%s n=%d", tc.cur, tc.n)
		if tc.measured {
			assert.Equal(t, tc.want, c.shapeAfter(tc.cur, tc.n, tc.coverage), "%s n=%d coverage %v", tc.cur, tc.n, tc.coverage)
		}
	}
}

func TestFlatResolution(t *testing.T) {
	c := NewLouvainGrouper(nil).c
	for n, want := range map[int]float64{35: 1, 82: 1, 100: 1.104, 300: 1.912, 1000: 3.490, 5254: 8} {
		assert.InDelta(t, want, c.flatResolution(n), 5e-4, "r(%d)", n)
	}
}

func TestGrouping_Validate(t *testing.T) {
	areas := func() Grouping {
		g := Grouping{Shape: ShapeAreas, Area: make([]int, 13), Interest: make([]int, 13), Seeds: make([]Seed, 13)}
		for i := range g.Interest {
			g.Interest[i] = i / 5 // 5, 5, 3
		}
		return g
	}
	require.NoError(t, areas().Validate(13))
	flat := Grouping{Shape: ShapeFlat, Area: []int{-1, -1, -1}, Interest: []int{0, 0, 0}, Seeds: []Seed{noSeed, noSeed, {-1, 4}}}
	require.NoError(t, flat.Validate(3))
	require.NoError(t, emptyGrouping().Validate(0))

	cases := []struct {
		name   string
		mutate func(*Grouping)
		want   string
	}{
		{"short slice", func(g *Grouping) { g.Seeds = g.Seeds[1:] }, "grouping of 13 areas, 13 interests and 12 seeds for 13 points"},
		{"unknown shape", func(g *Grouping) { g.Shape = "tree" }, `unknown shape "tree"`},
		{"label below noise", func(g *Grouping) { g.Interest[0] = -2 }, "point 0 has interest -2"},
		// Every label used makes at most one group per point, so a larger
		// label is refused before anything is sized by it.
		{"label past the points", func(g *Grouping) { g.Interest[1] = 13 }, "point 1 has interest 13"},
		{"the largest label", func(g *Grouping) { g.Interest[2] = math.MaxInt }, fmt.Sprintf("point 2 has interest %d", math.MaxInt)},
		{"a huge area", func(g *Grouping) { g.Area[3] = 1 << 40 }, "point 3 has area 1099511627776"},
		{"gap in labels", func(g *Grouping) {
			for i := 10; i < 13; i++ {
				g.Interest[i] = 3
			}
		}, "interest 2 is unused among 0..3"},
		{"interest without area", func(g *Grouping) { g.Area[4] = NoiseLabel }, "point 4 is in interest 0 but in no area"},
		{"interest across areas", func(g *Grouping) {
			for i := 5; i < 13; i++ {
				g.Area[i] = 1
			}
			g.Interest[5] = 0
			g.Interest[0] = 1
		}, "interest 0 spans areas 0 and 1"},
		{"small area", func(g *Grouping) {
			for i := 10; i < 13; i++ {
				g.Area[i] = 1
			}
		}, "area 1 has 3 members, want at least 10"},
		{"small interest", func(g *Grouping) { g.Interest[12] = NoiseLabel }, "interest 2 has 2 members, want at least 3"},
		{"area in the flat shape", func(g *Grouping) { g.Shape = ShapeFlat }, "point 0 has area 0 (seed 0) in the flat shape"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := areas()
			tc.mutate(&g)
			assert.EqualError(t, g.Validate(13), "insight: "+tc.want)
		})
	}
}

func TestLouvainGrouper_Params(t *testing.T) {
	lg := NewLouvainGrouper(nil)
	assert.Equal(t, "louvain", lg.Name())
	raw, err := json.Marshal(lg.Params())
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	for _, key := range []string{
		"algorithm_version", "graph_k", "area_k", "area_min_similarity", "area_resolution", "interest_resolution",
		"min_area_size", "min_interest_size", "flat_resolution", "areas_from_documents", "areas_from_coverage",
		"flat_below_documents", "flat_below_coverage", "seed", "visit_order", "new_documents",
	} {
		assert.Contains(t, got, key)
	}
	assert.InDelta(t, 0.40, got["area_min_similarity"], 0)
	assert.Equal(t, map[string]any{"scale": 8.0, "reference_documents": 5254.0}, got["flat_resolution"])

	// An override shows in the params, so a run records what made it.
	lg.c.seed = 9
	assert.Equal(t, uint64(9), lg.Params()["seed"])
}

func TestLouvainGrouper_RejectsBadInput(t *testing.T) {
	ok := twoGroupsPlusOutlier()
	cases := []struct {
		name string
		in   GroupInput
		want string
	}{
		{"duplicate ID", GroupInput{Shape: ShapeFlat, Points: append(slices.Clone(ok), ok[2])}, "point a3 appears twice"},
		{"unknown shape", GroupInput{Shape: "tree", Points: ok}, `unknown shape "tree"`},
		{"no shape", GroupInput{Points: ok}, `unknown shape ""`},
		{"mixed dims", GroupInput{Shape: ShapeFlat, Points: append(slices.Clone(ok), Point{ID: "z", Vector: []float32{1}})}, "point z has dim 1, want 4"},
		{"not unit", GroupInput{Shape: ShapeFlat, Points: append(slices.Clone(ok), Point{ID: "z", Vector: []float32{1, 1, 0, 0}})}, "not unit length"},
		{"NaN", GroupInput{Shape: ShapeFlat, Points: append(slices.Clone(ok), Point{ID: "z", Vector: []float32{float32(math.NaN()), 0, 0, 0}})}, "not unit length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, g := range []Grouper{NewLouvainGrouper(nil), FlatGrouper(NewKNNGraphClusterer(KNNGraphOptions{}))} {
				_, err := g.Group(context.Background(), tc.in)
				require.ErrorContains(t, err, tc.want, g.Name())
			}
		})
	}
}

func TestLouvainGrouper_Empty(t *testing.T) {
	g, err := NewLouvainGrouper(nil).Group(context.Background(), GroupInput{Shape: ShapeAreas})
	require.NoError(t, err)
	assert.Equal(t, emptyGrouping(), g)
}

func TestLouvainGrouper_ZeroVectorIsAStray(t *testing.T) {
	pts := append(twoGroupsPlusOutlier(), Point{ID: "zero", Vector: make([]float32, 4)})
	g, err := NewLouvainGrouper(nil).Group(context.Background(), GroupInput{Shape: ShapeFlat, Points: pts})
	require.NoError(t, err)
	require.NoError(t, g.Validate(len(pts)))
	assert.Equal(t, NoiseLabel, g.Interest[len(pts)-1])
	assert.NotEqual(t, NoiseLabel, g.Interest[0], "the groups still group")
}

func TestLouvainGrouper_DoesNotMutate(t *testing.T) {
	pts := syntheticCorpus(3, 400, 16, 8, 0.8)
	lg := NewLouvainGrouper(nil)
	first, err := lg.Group(context.Background(), GroupInput{Shape: ShapeFlat, Points: pts})
	require.NoError(t, err)
	prior := map[string]Seed{}
	for i, p := range pts {
		prior[p.ID] = first.Seeds[i]
	}
	pointsCopy := make([]Point, len(pts))
	for i, p := range pts {
		pointsCopy[i] = Point{ID: p.ID, Vector: slices.Clone(p.Vector)}
	}
	priorCopy := maps.Clone(prior)

	_, err = lg.Group(context.Background(), GroupInput{Shape: ShapeFlat, Points: pts, Prior: prior, Split: true})
	require.NoError(t, err)
	assert.Equal(t, pointsCopy, pts)
	assert.Equal(t, priorCopy, prior)
}

func TestLouvainGrouper_Canceled(t *testing.T) {
	pts := syntheticCorpus(1, 300, 8, 4, 0.5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewLouvainGrouper(nil).Group(ctx, GroupInput{Shape: ShapeFlat, Points: pts})
	require.ErrorIs(t, err, context.Canceled, "during the graph pass")

	// The pass finishes; Louvain then finds the context ended.
	ctx, cancel = context.WithCancel(context.Background())
	lg := NewLouvainGrouper(nil)
	lg.neighbours = func(ctx context.Context, vecs [][]float32) (neighbourLists, error) {
		defer cancel()
		return nearestNeighbours(ctx, vecs)
	}
	_, err = lg.Group(ctx, GroupInput{Shape: ShapeFlat, Points: pts})
	require.ErrorIs(t, err, context.Canceled, "during Louvain")
}

func newCapture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestLouvainGrouper_WarnsOnceAtACap(t *testing.T) {
	log, buf := newCapture()
	lg := NewLouvainGrouper(log)
	lg.c.maxPasses = 1 // every level stops after one pass that still moved nodes
	pts := syntheticCorpus(7, 1200, 32, 25, 1.0)
	_, err := lg.Group(context.Background(), GroupInput{Shape: ShapeAreas, Points: pts})
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 1, "one warning per Group call however many runs hit a cap")
	assert.Contains(t, lines[0], "level=WARN")
	assert.Contains(t, lines[0], "pass=area")
	assert.Contains(t, lines[0], "nodes=1200")
	assert.Contains(t, lines[0], "cap=passes limit=1")

	buf.Reset()
	lg = NewLouvainGrouper(log)
	_, err = lg.Group(context.Background(), GroupInput{Shape: ShapeAreas, Points: pts})
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "no cap, no warning")
}

// TestLouvainGrouper_OneGraphPass: the gate's area pass reuses the one
// nearest-neighbour pass, whatever the shape ends up.
func TestLouvainGrouper_OneGraphPass(t *testing.T) {
	big := syntheticCorpus(7, 1200, 32, 25, 1.0)
	small := syntheticCorpus(8, 300, 16, 6, 0.8)
	for _, tc := range []struct {
		name  string
		shape Shape
		pts   []Point
	}{
		{"flat, below the gate", ShapeFlat, small},
		{"flat, gate measured", ShapeFlat, big},
		{"areas, gate measured", ShapeAreas, big},
		{"areas, too small", ShapeAreas, small},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var passes atomic.Int32
			lg := NewLouvainGrouper(nil)
			lg.neighbours = func(ctx context.Context, vecs [][]float32) (neighbourLists, error) {
				passes.Add(1)
				return nearestNeighbours(ctx, vecs)
			}
			_, err := lg.Group(context.Background(), GroupInput{Shape: tc.shape, Points: tc.pts})
			require.NoError(t, err)
			assert.Equal(t, int32(1), passes.Load())
		})
	}
}

// TestInterests_AreaWithoutAnInterestIsOne: an area whose interest pass
// finds no community of the minimum size becomes one interest.
func TestInterests_AreaWithoutAnInterestIsOne(t *testing.T) {
	// Twelve nodes in six disjoint pairs, given as one area.
	adj := make([][]louvain.Edge, 12)
	for i := 0; i < 12; i += 2 {
		adj[i] = []louvain.Edge{{To: i + 1, Weight: 1}}
		adj[i+1] = []louvain.Edge{{To: i, Weight: 1}}
	}
	g, err := louvain.NewGraph(adj)
	require.NoError(t, err)
	pts := make([]Point, 12)
	for i := range pts {
		pts[i] = Point{ID: string(rune('a' + i))}
	}
	p := &groupPass{ctx: context.Background(), c: NewLouvainGrouper(nil).c, points: pts, ids: idsOf(pts)}
	areaComm := make([]int, 12)
	got, err := p.interests(g, areaComm, make([]int, 12), false)
	require.NoError(t, err)
	assert.Equal(t, make([]int, 12), got.Interest, "one interest holds the whole area")
	for i, s := range got.Seeds {
		assert.Equal(t, Seed{Area: 0, Interest: i / 2}, s, "the seeds keep the communities found")
	}
}

func TestLouvainGrouper_ShapeFollowsTheGate(t *testing.T) {
	big := syntheticCorpus(7, 1200, 32, 25, 1.0)
	lg := NewLouvainGrouper(nil)
	g, err := lg.Group(context.Background(), GroupInput{Shape: ShapeFlat, Points: big})
	require.NoError(t, err)
	require.Equal(t, ShapeAreas, g.Shape, "the corpus opens the gate")

	// Below the 900 points that close it, the same documents go flat.
	sub := big[:850]
	prior := map[string]Seed{}
	for i, p := range big {
		prior[p.ID] = g.Seeds[i]
	}
	flat, err := lg.Group(context.Background(), GroupInput{Shape: ShapeAreas, Points: sub, Prior: prior, Split: true})
	require.NoError(t, err)
	assert.Equal(t, ShapeFlat, flat.Shape)
	require.NoError(t, flat.Validate(len(sub)))
	fresh, err := lg.Group(context.Background(), GroupInput{Shape: ShapeFlat, Points: sub})
	require.NoError(t, err)
	assert.Equal(t, fresh, flat, "a change of shape is a fresh pass: Prior and Split are ignored")
}

func TestFlatGrouper(t *testing.T) {
	c := NewKNNGraphClusterer(KNNGraphOptions{})
	fg := FlatGrouper(c)
	assert.Equal(t, c.Name(), fg.Name())
	assert.Equal(t, c.Params(), fg.Params())

	pts := syntheticCorpus(7, 1200, 32, 25, 1.0)
	got, err := fg.Group(context.Background(), GroupInput{Shape: ShapeAreas, Points: pts})
	require.NoError(t, err)
	require.NoError(t, got.Validate(len(pts)))
	assert.Equal(t, ShapeFlat, got.Shape, "flat whatever the input")
	assert.Zero(t, got.Splits)
	labels, err := c.Cluster(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, numberGroups(labels, idsOf(pts), MinInterestSize), got.Interest, "the clusterer's clusters, renumbered")
	for i := range pts {
		require.Equal(t, NoiseLabel, got.Area[i])
		require.Equal(t, noSeed, got.Seeds[i])
	}

	prior := map[string]Seed{pts[0].ID: {Area: 3, Interest: 4}}
	with, err := fg.Group(context.Background(), GroupInput{Shape: ShapeFlat, Points: pts, Prior: prior, Split: true})
	require.NoError(t, err)
	assert.Equal(t, got, with, "Prior and Split are ignored")

	short := clusterFunc(func(context.Context, []Point) ([]int, error) { return []int{0}, nil })
	_, err = FlatGrouper(short).Group(context.Background(), GroupInput{Shape: ShapeFlat, Points: pts})
	require.EqualError(t, err, "insight: test returned 1 labels for 1200 points")

	// A cluster below the minimum interest size is left out.
	pairs := clusterFunc(func(_ context.Context, points []Point) ([]int, error) {
		labels := make([]int, len(points))
		for i := range labels {
			labels[i] = min(i/2, 2) // sizes 2, 2, and the rest
		}
		return labels, nil
	})
	got, err = FlatGrouper(pairs).Group(context.Background(), GroupInput{Shape: ShapeFlat, Points: pts[:10]})
	require.NoError(t, err)
	assert.Equal(t, []int{-1, -1, -1, -1, 0, 0, 0, 0, 0, 0}, got.Interest)
}

func TestNumberGroups(t *testing.T) {
	ids := []string{"f", "e", "d", "c", "b", "a", "g"}
	// Two groups of 3 tie on size: the one holding "a" comes first; the
	// pair is too small; a negative value is in no group.
	assert.Equal(t, []int{1, 0, 1, 0, 1, 0, -1}, numberGroups([]int{7, 9, 7, 9, 7, 9, -1}, ids, 3))
	assert.Equal(t, []int{-1, -1, 0, 0, 0, -1, -1}, numberGroups([]int{1, 1, 2, 2, 2, 5, 6}, ids, 3))
}
