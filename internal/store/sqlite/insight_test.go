package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func TestInsights_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	docs := NewDocuments(db)
	ins := NewInsights(db)

	ids := seedDocs(t, db, "local",
		"https://example.com/a", "https://example.com/b", "https://example.com/c")
	for _, id := range ids {
		require.NoError(t, docs.UpdateState(ctx, id, store.DocStateFetched))
	}

	run := &store.ClusterRun{TenantID: "local", Algo: "knn-graph", Params: []byte(`{"k":10}`)}
	require.NoError(t, ins.CreateRun(ctx, run))
	require.NotEmpty(t, run.ID)
	assert.Equal(t, store.ClusterRunRunning, run.Status)
	assert.False(t, run.StartedAt.IsZero())

	label, summary := "Test Topic", "docs about testing"
	cw := store.ClusterWithMembers{
		Cluster: store.Cluster{TenantID: "local", Label: &label, Summary: &summary, Size: 3, Cohesion: 0.8},
		Members: []store.ClusterMember{
			{DocumentID: ids[0], Similarity: 0.9},
			{DocumentID: ids[1], Similarity: 0.7},
			{DocumentID: ids[2], Similarity: 0.6},
		},
	}
	require.NoError(t, ins.ReplaceClusters(ctx, run.ID, []store.ClusterWithMembers{cw}))
	require.NoError(t, ins.FinishRun(ctx, run.ID, store.ClusterRunDone, 3, 1, 0, nil))

	got, err := ins.LatestRun(ctx, "local", store.ClusterRunDone)
	require.NoError(t, err)
	assert.Equal(t, run.ID, got.ID)
	assert.Equal(t, 3, got.NumDocuments)
	assert.Equal(t, 1, got.NumClusters)
	require.NotNil(t, got.FinishedAt)

	clusters, err := ins.ListClusters(ctx, run.ID, 0)
	require.NoError(t, err)
	require.Len(t, clusters, 1)
	require.NotNil(t, clusters[0].Label)
	assert.Equal(t, "Test Topic", *clusters[0].Label)
	assert.Equal(t, 3, clusters[0].Size)

	c0, err := ins.GetCluster(ctx, clusters[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "local", c0.TenantID)

	members, err := ins.ClusterMembers(ctx, c0.ID, 0)
	require.NoError(t, err)
	require.Len(t, members, 3)
	// ordered by similarity descending
	assert.Equal(t, ids[0], members[0].DocumentID)
	assert.InDelta(t, 0.9, members[0].Similarity, 1e-6)

	// ReplaceClusters is idempotent: re-running replaces, not duplicates.
	require.NoError(t, ins.ReplaceClusters(ctx, run.ID, []store.ClusterWithMembers{cw}))
	clusters2, err := ins.ListClusters(ctx, run.ID, 0)
	require.NoError(t, err)
	assert.Len(t, clusters2, 1)

	// PruneRunsExcept drops other runs (and cascades their clusters).
	old := &store.ClusterRun{TenantID: "local", Algo: "knn-graph"}
	require.NoError(t, ins.CreateRun(ctx, old))
	require.NoError(t, ins.PruneRunsExcept(ctx, "local", run.ID))
	_, err = ins.GetRun(ctx, old.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = ins.GetRun(ctx, run.ID)
	assert.NoError(t, err)

	// Sentinel mapping.
	_, err = ins.GetCluster(ctx, "does-not-exist")
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = ins.LatestRun(ctx, "other-tenant", store.ClusterRunDone)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestChunks_DocumentVectors(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	docs := NewDocuments(db)
	ch := NewChunks(db, vecDim)

	ids := seedDocs(t, db, "local", "https://example.com/x", "https://example.com/y")
	for _, id := range ids {
		require.NoError(t, docs.UpdateState(ctx, id, store.DocStateFetched))
	}
	extX := latestExtractionID(t, db, ids[0])
	extY := latestExtractionID(t, db, ids[1])

	// doc X: chunks 0.1 and 0.3 → mean 0.2; doc Y: single chunk 0.5.
	require.NoError(t, ch.ReplaceForDocument(ctx, ids[0], extX, "X", nil, []store.ChunkInput{
		{Text: "a", Embedding: fillVec(0.1), TokenCount: 1},
		{Text: "b", Embedding: fillVec(0.3), TokenCount: 1},
	}))
	require.NoError(t, ch.ReplaceForDocument(ctx, ids[1], extY, "Y", nil, []store.ChunkInput{
		{Text: "c", Embedding: fillVec(0.5), TokenCount: 1},
	}))

	dvs, err := ch.DocumentVectors(ctx, "local")
	require.NoError(t, err)
	require.Len(t, dvs, 2)

	byID := map[string][]float32{}
	for _, dv := range dvs {
		byID[dv.DocumentID] = dv.Vector
	}
	require.Contains(t, byID, ids[0])
	require.Contains(t, byID, ids[1])
	require.Len(t, byID[ids[0]], vecDim)
	assert.InDelta(t, 0.2, byID[ids[0]][0], 1e-6)
	assert.InDelta(t, 0.5, byID[ids[1]][0], 1e-6)

	// A pending doc (never marked fetched) with chunks is excluded.
	ids3 := seedDocs(t, db, "local", "https://example.com/z")
	ext3 := latestExtractionID(t, db, ids3[0])
	require.NoError(t, ch.ReplaceForDocument(ctx, ids3[0], ext3, "Z", nil, []store.ChunkInput{
		{Text: "d", Embedding: fillVec(0.9), TokenCount: 1},
	}))
	dvs2, err := ch.DocumentVectors(ctx, "local")
	require.NoError(t, err)
	assert.Len(t, dvs2, 2)
}
