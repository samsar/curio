package sqlite

import (
	"context"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func TestInsights_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	ins := NewInsights(db)

	ids := seedDocs(t, db, "local",
		"https://example.com/a", "https://example.com/b", "https://example.com/c")
	for _, id := range ids {
		require.NoError(t, docs.MarkFetched(ctx, id))
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
	require.NoError(t, ins.FinishRun(ctx, run.ID, store.RunResult{Status: store.ClusterRunDone, NumDocuments: 3, NumClusters: 1}))

	got, err := ins.LatestRun(ctx, "local", store.ClusterRunDone)
	require.NoError(t, err)
	assert.Equal(t, run.ID, got.ID)
	assert.Equal(t, 3, got.NumDocuments)
	assert.Equal(t, 1, got.NumClusters)
	require.NotNil(t, got.FinishedAt)

	clusters, err := ins.ListClusters(ctx, run.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, clusters, 1)
	require.NotNil(t, clusters[0].Label)
	assert.Equal(t, "Test Topic", *clusters[0].Label)
	assert.Equal(t, 3, clusters[0].Size)

	c0, err := ins.GetCluster(ctx, clusters[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "local", c0.TenantID)

	members, err := ins.ClusterMembers(ctx, c0.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, members, 3)
	// ordered by similarity descending
	assert.Equal(t, ids[0], members[0].DocumentID)
	assert.InDelta(t, 0.9, members[0].Similarity, 1e-6)

	// ReplaceClusters is idempotent: re-running replaces, not duplicates.
	require.NoError(t, ins.ReplaceClusters(ctx, run.ID, []store.ClusterWithMembers{cw}))
	clusters2, err := ins.ListClusters(ctx, run.ID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, clusters2, 1)

	// PruneRunsExcept drops other runs (and cascades their clusters), and
	// never every run.
	old := &store.ClusterRun{TenantID: "local", Algo: "knn-graph"}
	require.NoError(t, ins.CreateRun(ctx, old))
	failed := &store.ClusterRun{TenantID: "local", Algo: "knn-graph"}
	require.NoError(t, ins.CreateRun(ctx, failed))
	require.NoError(t, ins.PruneRunsExcept(ctx, "local", run.ID, failed.ID))
	_, err = ins.GetRun(ctx, old.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	for _, kept := range []string{run.ID, failed.ID} {
		_, err = ins.GetRun(ctx, kept)
		assert.NoError(t, err)
	}
	require.Error(t, ins.PruneRunsExcept(ctx, "local"), "no run to keep")
	_, err = ins.GetRun(ctx, run.ID)
	assert.NoError(t, err, "nothing pruned")

	// Sentinel mapping.
	_, err = ins.GetCluster(ctx, "does-not-exist")
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = ins.LatestRun(ctx, "other-tenant", store.ClusterRunDone)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestChunks_DocumentVectors(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	ch := NewChunks(db, vecDim)

	ids := seedDocs(t, db, "local", "https://example.com/x", "https://example.com/y")
	for _, id := range ids {
		require.NoError(t, docs.MarkFetched(ctx, id))
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

func TestInsights_FinishRun_RequiresTerminalStatus(t *testing.T) {
	ctx := context.Background()
	ins := NewInsights(newTestDB(t))
	run := &store.ClusterRun{TenantID: "local", Algo: "knn-graph"}
	require.NoError(t, ins.CreateRun(ctx, run))

	for _, status := range []store.ClusterRunStatus{store.ClusterRunRunning, "", "bogus"} {
		err := ins.FinishRun(ctx, run.ID, store.RunResult{Status: status, NumDocuments: 7})
		require.Error(t, err, "status %q", status)
	}
	got, err := ins.GetRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ClusterRunRunning, got.Status, "a refused finish changes nothing")
	assert.Zero(t, got.NumDocuments)
	assert.Nil(t, got.FinishedAt)
}

// TestInsights_LatestRun_SameStart: of two runs started in the same
// millisecond, the later one is the latest, whatever its status.
func TestInsights_LatestRun_SameStart(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ins := NewInsights(db)
	done := &store.ClusterRun{TenantID: "local", Algo: "knn-graph"}
	require.NoError(t, ins.CreateRun(ctx, done))
	require.NoError(t, ins.FinishRun(ctx, done.ID, store.RunResult{Status: store.ClusterRunDone}))
	failed := &store.ClusterRun{TenantID: "local", Algo: "knn-graph"}
	require.NoError(t, ins.CreateRun(ctx, failed))
	msg := "boom"
	require.NoError(t, ins.FinishRun(ctx, failed.ID, store.RunResult{Status: store.ClusterRunFailed, Error: &msg}))
	_, err := db.ExecContext(ctx, `UPDATE cluster_runs SET started_at = ?`, formatTime(done.StartedAt))
	require.NoError(t, err)

	latest, err := ins.LatestRun(ctx, "local", "")
	require.NoError(t, err)
	assert.Equal(t, failed.ID, latest.ID)
	latest, err = ins.LatestRun(ctx, "local", store.ClusterRunDone)
	require.NoError(t, err)
	assert.Equal(t, done.ID, latest.ID)
}

// TestInsights_PagedOrders: a run's clusters and a cluster's members come
// in a total order, ties broken by ID, so pages of any size read from
// successive offsets add up to the whole list, every row once.
func TestInsights_PagedOrders(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ins := NewInsights(db)
	ids := seedDocs(t, db, "local", "https://example.com/a", "https://example.com/b", "https://example.com/c",
		"https://example.com/d", "https://example.com/e")

	run := &store.ClusterRun{TenantID: "local", Algo: "knn-graph"}
	require.NoError(t, ins.CreateRun(ctx, run))
	cluster := func(id string, size int, cohesion float64, members ...store.ClusterMember) store.ClusterWithMembers {
		return store.ClusterWithMembers{Cluster: store.Cluster{ID: id, TenantID: "local", Size: size,
			Cohesion: cohesion}, Members: members}
	}
	// Written out of order: the ties on size and cohesion come by ID, and
	// the ties on similarity by document ID.
	members := []store.ClusterMember{{DocumentID: ids[3], Similarity: 0.5}, {DocumentID: ids[0], Similarity: 0.9},
		{DocumentID: ids[4], Similarity: 0.5}, {DocumentID: ids[1], Similarity: 0.5}, {DocumentID: ids[2], Similarity: 0.7}}
	require.NoError(t, ins.ReplaceClusters(ctx, run.ID, []store.ClusterWithMembers{
		cluster("c-tie-b", 2, 0.5), cluster("c-small", 1, 0.9), cluster("c-big", 5, 0.1, members...),
		cluster("c-tie-a", 2, 0.5), cluster("c-cohesive", 2, 0.8),
	}))

	all, err := ins.ListClusters(ctx, run.ID, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"c-big", "c-cohesive", "c-tie-a", "c-tie-b", "c-small"}, clusterIDs(all))
	allMembers, err := ins.ClusterMembers(ctx, "c-big", 0, 0)
	require.NoError(t, err)
	byDocument := slices.Sorted(slices.Values([]string{ids[1], ids[3], ids[4]}))
	assert.Equal(t, append([]string{ids[0], ids[2]}, byDocument...), memberIDs(allMembers))

	for size := 1; size <= 3; size++ {
		var clusters []*store.Cluster
		var members []store.ClusterMember
		for offset := 0; offset < len(all)+size; offset += size {
			page, err := ins.ListClusters(ctx, run.ID, size, offset)
			require.NoError(t, err)
			assert.LessOrEqual(t, len(page), size)
			clusters = append(clusters, page...)
			memberPage, err := ins.ClusterMembers(ctx, "c-big", size, offset)
			require.NoError(t, err)
			members = append(members, memberPage...)
		}
		assert.Equal(t, clusterIDs(all), clusterIDs(clusters), "clusters %d a page", size)
		assert.Equal(t, memberIDs(allMembers), memberIDs(members), "members %d a page", size)
	}

	rest, err := ins.ListClusters(ctx, run.ID, 0, 3)
	require.NoError(t, err)
	assert.Equal(t, clusterIDs(all[3:]), clusterIDs(rest), "no limit: the rest from the offset")
	restMembers, err := ins.ClusterMembers(ctx, "c-big", -1, 3)
	require.NoError(t, err)
	assert.Equal(t, memberIDs(allMembers[3:]), memberIDs(restMembers))

	for _, offset := range []int{len(all), 1000, math.MaxInt} {
		past, err := ins.ListClusters(ctx, run.ID, 24, offset)
		require.NoError(t, err, "offset %d", offset)
		assert.Empty(t, past, "offset %d", offset)
		pastMembers, err := ins.ClusterMembers(ctx, "c-big", 50, offset)
		require.NoError(t, err, "offset %d", offset)
		assert.Empty(t, pastMembers, "offset %d", offset)
	}

	_, err = ins.ListClusters(ctx, run.ID, 24, -1)
	require.ErrorContains(t, err, "offset -1 is negative")
	_, err = ins.ClusterMembers(ctx, "c-big", 50, -1)
	require.ErrorContains(t, err, "offset -1 is negative")
}

func clusterIDs(clusters []*store.Cluster) []string {
	out := make([]string, 0, len(clusters))
	for _, c := range clusters {
		out = append(out, c.ID)
	}
	return out
}

func memberIDs(members []store.ClusterMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.DocumentID)
	}
	return out
}
