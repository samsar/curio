package insight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

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

// faultyInsights wraps the real insight store. LatestRun fails with latestErr
// when set, and finished records the status of every FinishRun that succeeded.
type faultyInsights struct {
	store.InsightStore
	latestErr error
	finished  map[string]string
}

func (f *faultyInsights) LatestRun(ctx context.Context, tenantID, status string) (*store.ClusterRun, error) {
	if f.latestErr != nil {
		return nil, f.latestErr
	}
	return f.InsightStore.LatestRun(ctx, tenantID, status)
}

func (f *faultyInsights) FinishRun(ctx context.Context, runID, status string, docs, clusters, noise int, msg *string) error {
	if err := f.InsightStore.FinishRun(ctx, runID, status, docs, clusters, noise, msg); err != nil {
		return err
	}
	f.finished[runID] = status
	return nil
}

// faultyDocs wraps the real document store; GetByID fails for the IDs in fail.
type faultyDocs struct {
	store.DocumentStore
	fail map[string]error
}

func (f *faultyDocs) GetByID(ctx context.Context, id string) (*store.Document, error) {
	if err := f.fail[id]; err != nil {
		return nil, err
	}
	return f.DocumentStore.GetByID(ctx, id)
}

// engineFixture is a real SQLite document + insight store with a corpus of
// well-separated groups: group g has sizes[g] documents whose vectors all
// point along basis axis g.
type engineFixture struct {
	docs     *faultyDocs
	store    *sqlitestore.Insights // unwrapped, for assertions
	insights *faultyInsights
	vectors  *vectorSource
	logs     bytes.Buffer
}

func newEngineFixture(t *testing.T, sizes ...int) *engineFixture {
	t.Helper()
	db := sqlitestore.NewEphemeralDB(t)
	docs := sqlitestore.NewDocuments(db)
	f := &engineFixture{
		docs:    &faultyDocs{DocumentStore: docs, fail: map[string]error{}},
		store:   sqlitestore.NewInsights(db),
		vectors: &vectorSource{},
	}
	f.insights = &faultyInsights{InsightStore: f.store, finished: map[string]string{}}
	for g, n := range sizes {
		for i := range n {
			title := fmt.Sprintf("group%d topic%d item%d", g, g, i)
			d := &store.Document{
				TenantID: tenant, URL: fmt.Sprintf("https://example.com/%d/%d", g, i),
				Title: &title, State: store.DocStateFetched,
			}
			require.NoError(t, docs.Upsert(context.Background(), d))
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
	return New(f.docs, f.vectors, f.insights, c, llm, cfg, slog.New(slog.NewTextHandler(&f.logs, nil)))
}

// rebuild runs a Rebuild that must succeed and returns its run ID.
func (f *engineFixture) rebuild(t *testing.T, e *Engine) string {
	t.Helper()
	runID, err := e.Rebuild(context.Background(), tenant)
	require.NoError(t, err)
	return runID
}

// assertCurrentRun checks that runID is the latest done run and that it
// still has its clusters.
func (f *engineFixture) assertCurrentRun(t *testing.T, runID string) {
	t.Helper()
	done, err := f.store.LatestRun(context.Background(), tenant, store.ClusterRunDone)
	require.NoError(t, err)
	assert.Equal(t, runID, done.ID)
	clusters, err := f.store.ListClusters(context.Background(), runID, 0)
	require.NoError(t, err)
	assert.NotEmpty(t, clusters, "the run's clusters are intact")
}

// clustersBySize maps each cluster size of a run to its label.
func (f *engineFixture) clustersBySize(t *testing.T, runID string) map[int]string {
	t.Helper()
	clusters, err := f.store.ListClusters(context.Background(), runID, 0)
	require.NoError(t, err)
	out := map[int]string{}
	for _, c := range clusters {
		require.NotNil(t, c.Label, "cluster of size %d is unlabeled", c.Size)
		out[c.Size] = *c.Label
	}
	return out
}

// logLine returns the one log line containing msg, failing unless exactly one
// was written.
func (f *engineFixture) logLine(t *testing.T, msg string) string {
	t.Helper()
	var found []string
	for line := range strings.Lines(f.logs.String()) {
		if strings.Contains(line, msg) {
			found = append(found, line)
		}
	}
	require.Len(t, found, 1, "log lines containing %q", msg)
	return found[0]
}

// clusterFunc adapts a function to Clusterer.
type clusterFunc func(ctx context.Context, points []Point) ([]int, error)

func (f clusterFunc) Cluster(ctx context.Context, points []Point) ([]int, error) {
	return f(ctx, points)
}
func (clusterFunc) Name() string { return "test" }

// Params returns nil, which the Clusterer interface allows.
func (clusterFunc) Params() map[string]any { return nil }

// byAxis clusters each point by its largest component, so with the fixture
// cluster g is group g: numbered smallest group first when sizes ascend.
var byAxis = clusterFunc(func(_ context.Context, points []Point) ([]int, error) {
	labels := make([]int, len(points))
	for i, p := range points {
		for d, v := range p.Vector {
			if v > p.Vector[labels[i]] {
				labels[i] = d
			}
		}
	}
	return labels, nil
})

// labelFunc adapts a function to Labeler.
type labelFunc func(ctx context.Context, info ClusterInfo) (Label, error)

func (f labelFunc) Label(ctx context.Context, info ClusterInfo) (Label, error) { return f(ctx, info) }
func (labelFunc) Name() string                                                 { return "test-llm" }

func TestRebuild_RecordsRunParams(t *testing.T) {
	f := newEngineFixture(t, 3, 4)
	runID, err := f.engine(nil, nil, Config{Center: true}).Rebuild(context.Background(), tenant)
	require.NoError(t, err)

	run, err := f.store.GetRun(context.Background(), runID)
	require.NoError(t, err)
	assert.Equal(t, store.ClusterRunDone, run.Status)
	var params map[string]any
	require.NoError(t, json.Unmarshal(run.Params, &params))
	assert.Equal(t, true, params["center"], "the engine's centering is recorded with the clusterer's params")
	assert.InDelta(t, 0.5, params["min_similarity"], 1e-9)
}

func TestRebuild_NilClustererParams(t *testing.T) {
	f := newEngineFixture(t, 3, 4)
	runID := f.rebuild(t, f.engine(byAxis, nil, Config{}))

	run, err := f.store.GetRun(context.Background(), runID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"center":false}`, string(run.Params))
}

func TestRebuild_UnencodableParamsCreateNoRun(t *testing.T) {
	f := newEngineFixture(t, 3)
	c := NewKNNGraphClusterer(KNNGraphOptions{MinSimilarity: math.NaN()})
	_, err := f.engine(c, nil, Config{}).Rebuild(context.Background(), tenant)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode clusterer params")

	_, err = f.store.LatestRun(context.Background(), tenant, "")
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

func TestPreparePoints_RejectsBadDims(t *testing.T) {
	cases := []struct {
		name string
		dvs  []store.DocVector
		want string
	}{
		{"mixed dims", []store.DocVector{
			{DocumentID: "a", Vector: []float32{1, 0}},
			{DocumentID: "b", Vector: []float32{1, 0, 0}},
		}, "document b has a 3-dimensional vector, want 2"},
		{"empty vector", []store.DocVector{
			{DocumentID: "a"},
			{DocumentID: "b", Vector: []float32{1}},
		}, "document a has an empty vector"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := preparePoints(tc.dvs, true)
			require.EqualError(t, err, tc.want)
		})
	}
}

var errLocked = errors.New("database is locked")

func TestRebuild_EmptyCorpus(t *testing.T) {
	t.Run("no prior run records an empty done run", func(t *testing.T) {
		f := newEngineFixture(t)
		runID := f.rebuild(t, f.engine(nil, nil, Config{}))
		run, err := f.store.GetRun(context.Background(), runID)
		require.NoError(t, err)
		assert.Equal(t, store.ClusterRunDone, run.Status)
		assert.Zero(t, run.NumDocuments)
	})
	t.Run("a prior done run is kept without a new row", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		prior := f.rebuild(t, f.engine(nil, nil, Config{}))
		f.vectors.dvs = nil

		got := f.rebuild(t, f.engine(nil, nil, Config{}))
		assert.Equal(t, prior, got)
		latest, err := f.store.LatestRun(context.Background(), tenant, "")
		require.NoError(t, err)
		assert.Equal(t, prior, latest.ID, "no new run row")
		f.assertCurrentRun(t, prior)
	})
	t.Run("a store error is not mistaken for no prior run", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		prior := f.rebuild(t, f.engine(nil, nil, Config{}))
		f.vectors.dvs = nil
		f.insights.latestErr = errLocked

		_, err := f.engine(nil, nil, Config{}).Rebuild(context.Background(), tenant)
		require.ErrorIs(t, err, errLocked)
		latest, err := f.store.LatestRun(context.Background(), tenant, "")
		require.NoError(t, err)
		assert.Equal(t, prior, latest.ID, "no new run row")
		f.assertCurrentRun(t, prior)
	})
}

func TestRebuild_Failures(t *testing.T) {
	failing := clusterFunc(func(context.Context, []Point) ([]int, error) { return nil, errors.New("boom") })

	t.Run("a failed run keeps the prior run and is pruned", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		prior := f.rebuild(t, f.engine(nil, nil, Config{}))

		runID, err := f.engine(failing, nil, Config{}).Rebuild(context.Background(), tenant)
		require.Error(t, err)
		assert.Equal(t, store.ClusterRunFailed, f.insights.finished[runID])
		_, err = f.store.GetRun(context.Background(), runID)
		assert.ErrorIs(t, err, store.ErrNotFound, "the failed run is pruned")
		f.assertCurrentRun(t, prior)
	})
	t.Run("a failed first run is kept as the only row", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		runID, err := f.engine(failing, nil, Config{}).Rebuild(context.Background(), tenant)
		require.Error(t, err)
		run, err := f.store.GetRun(context.Background(), runID)
		require.NoError(t, err)
		assert.Equal(t, store.ClusterRunFailed, run.Status)
	})
	t.Run("nothing is pruned when the last good run can't be read", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		prior := f.rebuild(t, f.engine(nil, nil, Config{}))
		f.insights.latestErr = errLocked

		runID, err := f.engine(failing, nil, Config{}).Rebuild(context.Background(), tenant)
		require.Error(t, err)
		f.insights.latestErr = nil

		run, err := f.store.GetRun(context.Background(), runID)
		require.NoError(t, err, "the failed run is not pruned either")
		assert.Equal(t, store.ClusterRunFailed, run.Status)
		f.assertCurrentRun(t, prior)
		assert.Contains(t, f.logs.String(), "skip pruning cluster runs")
	})
	t.Run("a successful run prunes older runs", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		first := f.rebuild(t, f.engine(nil, nil, Config{}))
		second := f.rebuild(t, f.engine(nil, nil, Config{}))
		_, err := f.store.GetRun(context.Background(), first)
		assert.ErrorIs(t, err, store.ErrNotFound)
		f.assertCurrentRun(t, second)
	})
}

func TestRebuild_CanceledRunIsMarkedFailed(t *testing.T) {
	cases := []struct {
		name string
		// build returns the engine; cancel is the run's own cancel func.
		build func(f *engineFixture, cancel context.CancelFunc) *Engine
	}{
		{"during clustering", func(f *engineFixture, cancel context.CancelFunc) *Engine {
			return f.engine(clusterFunc(func(ctx context.Context, _ []Point) ([]int, error) {
				cancel()
				return nil, ctx.Err()
			}), nil, Config{})
		}},
		{"during labeling", func(f *engineFixture, cancel context.CancelFunc) *Engine {
			return f.engine(nil, labelFunc(func(ctx context.Context, _ ClusterInfo) (Label, error) {
				cancel()
				return Label{}, ctx.Err()
			}), Config{Labeling: LabelingLLM})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEngineFixture(t, 3, 4)
			prior := f.rebuild(t, f.engine(nil, nil, Config{}))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runID, err := tc.build(f, cancel).Rebuild(ctx, tenant)
			require.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, store.ClusterRunFailed, f.insights.finished[runID],
				"the run is marked failed although its context is gone")
			f.assertCurrentRun(t, prior)
		})
	}
}

func TestRebuild_LLMFailureFallsBackForTheWholeRun(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5, 6, 7)
	calls := 0
	llm := labelFunc(func(context.Context, ClusterInfo) (Label, error) {
		calls++
		return Label{}, errors.New("ollama unreachable: connection refused")
	})
	runID := f.rebuild(t, f.engine(byAxis, llm, Config{Labeling: LabelingLLM}))

	assert.Equal(t, 1, calls, "the LLM isn't asked again once it has failed")
	labels := f.clustersBySize(t, runID)
	require.Len(t, labels, 5)
	for size, label := range labels {
		assert.True(t, strings.HasPrefix(label, "Group"), "cluster of size %d got term label %q", size, label)
	}
	warn := f.logLine(t, "llm labeling fell back")
	assert.Contains(t, warn, "level=WARN")
	assert.Contains(t, warn, "clusters=5 of=5", "clusters never offered to the model count too")
	assert.Contains(t, warn, "connection refused")
}

func TestRebuild_UnparseableReplyFallsBackForOneCluster(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5, 6, 7)
	calls := 0
	llm := labelFunc(func(_ context.Context, info ClusterInfo) (Label, error) {
		calls++
		if info.Size == 5 {
			return Label{}, fmt.Errorf("llm labeler: %w", ErrUnparseableLabel)
		}
		return Label{Name: fmt.Sprintf("LLM %d", info.Size)}, nil
	})
	runID := f.rebuild(t, f.engine(byAxis, llm, Config{Labeling: LabelingLLM}))

	assert.Equal(t, 5, calls, "the model is still asked about every cluster")
	labels := f.clustersBySize(t, runID)
	for _, size := range []int{3, 4, 6, 7} {
		assert.Equal(t, fmt.Sprintf("LLM %d", size), labels[size])
	}
	assert.True(t, strings.HasPrefix(labels[5], "Group2"), "got %q", labels[5])
	assert.Contains(t, f.logLine(t, "llm labeling fell back"), "clusters=1 of=5")
}

func TestRebuild_LabelingBudget(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5)
	calls := 0
	llm := labelFunc(func(ctx context.Context, _ ClusterInfo) (Label, error) {
		calls++
		<-ctx.Done() // a hung model: only the budget ends the call
		return Label{}, ctx.Err()
	})
	e := f.engine(byAxis, llm, Config{Labeling: LabelingLLM, LabelingTimeout: 50 * time.Millisecond})

	done := make(chan error, 1)
	var runID string
	go func() {
		var err error
		runID, err = e.Rebuild(context.Background(), tenant)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Rebuild did not finish after the labeling budget ran out")
	}
	assert.Equal(t, 1, calls)
	for size, label := range f.clustersBySize(t, runID) {
		assert.True(t, strings.HasPrefix(label, "Group"), "cluster of size %d got term label %q", size, label)
	}
	warn := f.logLine(t, "llm labeling fell back")
	assert.Contains(t, warn, "clusters=3 of=3")
	assert.Contains(t, warn, "deadline exceeded")
}

func TestRebuild_LabelsLargestClustersFirst(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5, 6, 7) // byAxis numbers these smallest first
	var order []int
	llm := labelFunc(func(_ context.Context, info ClusterInfo) (Label, error) {
		order = append(order, info.Size)
		return Label{Name: "x"}, nil
	})
	f.rebuild(t, f.engine(byAxis, llm, Config{Labeling: LabelingLLM}))
	assert.Equal(t, []int{7, 6, 5, 4, 3}, order)
	assert.NotContains(t, f.logs.String(), "llm labeling fell back")
}

func TestRebuild_TitleLookupErrors(t *testing.T) {
	t.Run("a deleted document is skipped", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		f.docs.fail[f.vectors.dvs[0].DocumentID] = fmt.Errorf("get: %w", store.ErrNotFound)
		runID := f.rebuild(t, f.engine(nil, nil, Config{}))
		f.assertCurrentRun(t, runID)
	})
	t.Run("any other error fails the run", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		f.docs.fail[f.vectors.dvs[0].DocumentID] = errLocked
		runID, err := f.engine(nil, nil, Config{}).Rebuild(context.Background(), tenant)
		require.ErrorIs(t, err, errLocked)
		assert.Equal(t, store.ClusterRunFailed, f.insights.finished[runID])
	})
}
