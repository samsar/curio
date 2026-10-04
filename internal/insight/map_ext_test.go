package insight_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/louvain"
)

// TestBuildMap_LeavesTheListsAlone: the map reads a grouping's neighbour
// lists and changes none, so the next grouping of the same documents,
// through the tests' shared pass cache, gets them as the pass found them.
func TestBuildMap_LeavesTheListsAlone(t *testing.T) {
	ctx := context.Background()
	docs := fixtureLibrary(4).docs[:300]
	r := group(t, grouper(), docs, insight.GroupInput{Shape: insight.ShapeFlat})
	require.NotNil(t, r.g.Neighbours)
	vecs := make([][]float32, len(r.points))
	for i, p := range r.points {
		vecs[i] = p.Vector
	}
	cached, err := lists.get(ctx, vecs)
	require.NoError(t, err)
	before := make([][]louvain.Edge, len(cached))
	for i, es := range cached {
		before[i] = slices.Clone(es)
	}

	_, err = insight.BuildMap(ctx, coldInput(r))
	require.NoError(t, err)
	again := group(t, grouper(), docs, insight.GroupInput{Shape: insight.ShapeFlat})
	after, err := lists.get(ctx, vecs)
	require.NoError(t, err)
	assert.Equal(t, before, [][]louvain.Edge(after), "the cache's lists are as the pass found them")
	fresh, err := insight.NearestNeighbours(ctx, vecs)
	require.NoError(t, err)
	assert.Equal(t, [][]louvain.Edge(fresh), again.g.Neighbours)
}
