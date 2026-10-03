package insight_test

import (
	"context"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
)

// byID maps a grouping's per-point values to the points' IDs.
type byID map[string]struct {
	area, interest int
	seed           insight.Seed
}

func groupingByID(points []insight.Point, g insight.Grouping) byID {
	out := make(byID, len(points))
	for i, p := range points {
		out[p.ID] = struct {
			area, interest int
			seed           insight.Seed
		}{g.Area[i], g.Interest[i], g.Seeds[i]}
	}
	return out
}

// TestLouvainGrouper_OrderInvariant: shuffled input gives every document
// the same area, interest and seeds, fresh, warm, and warm with the split
// check. The grouper works in ID order, and every numbering is by size and
// smallest member ID.
func TestLouvainGrouper_OrderInvariant(t *testing.T) {
	skipUnderRace(t)
	t.Parallel()
	lib := fixtureLibrary(3)
	n := len(lib.docs)
	prev := rebuild(t, lib.subset(without(n, shuffled(n, 77)[:n/20])), nil, false)
	points, _, err := insight.PreparePoints(lib.docs, true)
	require.NoError(t, err)
	for name, in := range map[string]insight.GroupInput{
		"fresh":      {Shape: insight.ShapeFlat},
		"warm":       {Shape: prev.g.Shape, Prior: prev.seeds()},
		"warm+split": {Shape: prev.g.Shape, Prior: prev.seeds(), Split: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in.Points = points
			want, err := grouper().Group(context.Background(), in)
			require.NoError(t, err)
			r := rand.New(rand.NewPCG(5, 5))
			for s := range 5 {
				shuffledPoints := slices.Clone(points)
				r.Shuffle(n, func(a, b int) { shuffledPoints[a], shuffledPoints[b] = shuffledPoints[b], shuffledPoints[a] })
				in.Points = shuffledPoints
				got, err := grouper().Group(context.Background(), in)
				require.NoError(t, err)
				require.Equal(t, groupingByID(points, want), groupingByID(shuffledPoints, got), "shuffle %d", s)
			}
		})
	}
}

// TestLouvainGrouper_Idempotent: a grouping warm-started from its own
// seeds, nothing changed, is the same grouping at both levels, including
// one that came out of the split check. This rests on Louvain's fixpoint:
// a multi-level result is not a local optimum of the first level, and
// without the restarts a warm start from it moves nodes.
func TestLouvainGrouper_Idempotent(t *testing.T) {
	skipUnderRace(t)
	t.Parallel()
	for _, tc := range []struct {
		name string
		lib  library
	}{
		{"areas", fixtureLibrary(4)},
		{"flat", fixtureShape{docs: 600, areas: 4, topicDims: 32, generalistDims: 64}.build(4)},
	} {
		points, _, err := insight.PreparePoints(tc.lib.docs, true)
		require.NoError(t, err)
		for _, split := range []bool{false, true} {
			first, err := grouper().Group(context.Background(), insight.GroupInput{Shape: insight.ShapeFlat, Points: points})
			require.NoError(t, err)
			if split {
				first, err = grouper().Group(context.Background(), insight.GroupInput{
					Shape: first.Shape, Points: points, Prior: seedsOf(points, first), Split: true,
				})
				require.NoError(t, err)
			}
			again, err := grouper().Group(context.Background(), insight.GroupInput{
				Shape: first.Shape, Points: points, Prior: seedsOf(points, first),
			})
			require.NoError(t, err)
			assert.Equal(t, tc.name, string(first.Shape))
			assert.Equal(t, first.Shape, again.Shape)
			assert.Equal(t, first.Area, again.Area, "%s, from a split run %v", tc.name, split)
			assert.Equal(t, first.Interest, again.Interest, "%s, from a split run %v", tc.name, split)
			assert.Equal(t, first.Seeds, again.Seeds, "%s, from a split run %v", tc.name, split)
		}
	}
}

func seedsOf(points []insight.Point, g insight.Grouping) map[string]insight.Seed {
	m := make(map[string]insight.Seed, len(points))
	for i, p := range points {
		m[p.ID] = g.Seeds[i]
	}
	return m
}

// TestLouvainGrouper_SameOnAnyGOMAXPROCS: the parallel neighbour pass and
// everything after it give one answer however many threads run them.
func TestLouvainGrouper_SameOnAnyGOMAXPROCS(t *testing.T) {
	lib := fixtureShape{docs: 1200, areas: 6, topicDims: 32, generalistDims: 64}.build(5)
	points, _, err := insight.PreparePoints(lib.docs, true)
	require.NoError(t, err)
	in := insight.GroupInput{Shape: insight.ShapeFlat, Points: points}
	lg := insight.NewLouvainGrouper(nil) // no shared cache: every call makes its own pass
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	one, err := lg.Group(context.Background(), in)
	require.NoError(t, err)
	runtime.GOMAXPROCS(4)
	four, err := lg.Group(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, one, four)
}
