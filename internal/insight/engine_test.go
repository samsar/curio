package insight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
)

const tenant = "local"

// vectorSource serves canned document vectors; the engine reads nothing else
// from the chunk store.
type vectorSource struct {
	store.ChunkStore
	dvs []store.DocVector
}

func (v *vectorSource) DocumentVectors(context.Context, string) ([]store.DocVector, error) {
	return v.dvs, nil
}

// engineFixture is a real SQLite document + insight store with a corpus of
// well-separated groups: group g has sizes[g] documents whose vectors all
// point along basis axis g.
type engineFixture struct {
	docs     *sqlitestore.Documents
	insights *sqlitestore.Insights
	vectors  *vectorSource
}

func newEngineFixture(t *testing.T, sizes ...int) *engineFixture {
	t.Helper()
	db := sqlitestore.NewEphemeralDB(t)
	f := &engineFixture{
		docs:     sqlitestore.NewDocuments(db),
		insights: sqlitestore.NewInsights(db),
		vectors:  &vectorSource{},
	}
	for g, n := range sizes {
		for i := range n {
			title := fmt.Sprintf("group%d topic%d item%d", g, g, i)
			d := &store.Document{
				TenantID: tenant, URL: fmt.Sprintf("https://example.com/%d/%d", g, i),
				Title: &title, State: store.DocStateFetched,
			}
			require.NoError(t, f.docs.Upsert(context.Background(), d))
			v := make([]float32, len(sizes))
			v[g] = 1
			f.vectors.dvs = append(f.vectors.dvs, store.DocVector{DocumentID: d.ID, Vector: v})
		}
	}
	return f
}

func (f *engineFixture) engine(c Clusterer, llm Labeler, cfg Config) *Engine {
	if c == nil {
		c = NewKNNGraphClusterer(KNNGraphOptions{})
	}
	return New(f.docs, f.vectors, f.insights, c, llm, cfg, slog.New(slog.DiscardHandler))
}

func TestRebuild_RecordsRunParams(t *testing.T) {
	f := newEngineFixture(t, 3, 4)
	runID, err := f.engine(nil, nil, Config{Center: true}).Rebuild(context.Background(), tenant)
	require.NoError(t, err)

	run, err := f.insights.GetRun(context.Background(), runID)
	require.NoError(t, err)
	assert.Equal(t, store.ClusterRunDone, run.Status)
	var params map[string]any
	require.NoError(t, json.Unmarshal(run.Params, &params))
	assert.Equal(t, true, params["center"], "the engine's centering is recorded with the clusterer's params")
	assert.InDelta(t, 0.5, params["min_similarity"], 1e-9)
}

func TestRebuild_UnencodableParamsCreateNoRun(t *testing.T) {
	f := newEngineFixture(t, 3)
	c := NewKNNGraphClusterer(KNNGraphOptions{MinSimilarity: math.NaN()})
	_, err := f.engine(c, nil, Config{}).Rebuild(context.Background(), tenant)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode clusterer params")

	_, err = f.insights.LatestRun(context.Background(), tenant, "")
	assert.ErrorIs(t, err, store.ErrNotFound, "no run row is created")
}

func TestPreparePoints_CenteringSeparatesAnisotropic(t *testing.T) {
	// A large shared component (anisotropy) dominates every vector; each group
	// adds a small distinct signal in orthogonal dims. Raw cross-group cosine
	// is ~0.98, so without centering everything collapses into one cluster —
	// exactly the nomic-embed-text mega-cluster failure mode.
	dvs := []store.DocVector{
		{DocumentID: "a1", Vector: []float32{10.0, 10.0, 8.0, 8.0}},
		{DocumentID: "a2", Vector: []float32{10.1, 9.9, 8.0, 8.0}},
		{DocumentID: "a3", Vector: []float32{9.9, 10.1, 8.0, 8.0}},
		{DocumentID: "b1", Vector: []float32{8.0, 8.0, 10.0, 10.0}},
		{DocumentID: "b2", Vector: []float32{8.0, 8.0, 10.1, 9.9}},
		{DocumentID: "b3", Vector: []float32{8.0, 8.0, 9.9, 10.1}},
	}
	c := NewKNNGraphClusterer(KNNGraphOptions{})

	raw, err := preparePoints(dvs, false)
	require.NoError(t, err)
	labels, err := c.Cluster(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, 1, distinctClusters(labels), "raw cosines are ~0.98, so all six collapse into one cluster")

	centered, err := preparePoints(dvs, true)
	require.NoError(t, err)
	labels, err = c.Cluster(context.Background(), centered)
	require.NoError(t, err)
	assert.Equal(t, 2, distinctClusters(labels), "centering removes the shared component and reveals two topics")
	assert.Equal(t, labels[0], labels[1])
	assert.Equal(t, labels[1], labels[2])
	assert.NotEqual(t, labels[0], labels[3])
}

func TestPreparePoints_RejectsMixedDims(t *testing.T) {
	_, err := preparePoints([]store.DocVector{
		{DocumentID: "a", Vector: []float32{1, 0}},
		{DocumentID: "b", Vector: []float32{1, 0, 0}},
	}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "document b")
}
