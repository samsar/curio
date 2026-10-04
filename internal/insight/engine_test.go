package insight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

const tenant = "local"

// vectorSource serves canned document vectors: each document's mean, and
// that vector as its one chunk's. The engine and the placer read nothing
// else from the chunk store.
type vectorSource struct {
	store.ChunkStore
	dvs []store.DocVector
	err error // what DocumentVectors fails with, when set
}

func (v *vectorSource) DocumentVectors(context.Context, string) ([]store.DocVector, error) {
	return v.dvs, v.err
}

func (v *vectorSource) EmbeddingsForDocument(_ context.Context, documentID string) ([]store.ChunkEmbedding, error) {
	for _, dv := range v.dvs {
		if dv.DocumentID == documentID {
			return []store.ChunkEmbedding{{ChunkID: "chunk-" + documentID, Embedding: dv.Vector}}, nil
		}
	}
	return nil, nil
}

// faultyInsights wraps the real insight store. LatestRun fails with latestErr
// and CommitRun with commitErr when set, and failed records every run
// FailRun or RecordFailure failed.
type faultyInsights struct {
	store.InsightStore
	latestErr, commitErr error
	failed               []string
}

func (f *faultyInsights) LatestRun(ctx context.Context, tenantID string, status store.InterestRunStatus) (*store.InterestRun, error) {
	if f.latestErr != nil {
		return nil, f.latestErr
	}
	return f.InsightStore.LatestRun(ctx, tenantID, status)
}

func (f *faultyInsights) CommitRun(ctx context.Context, c store.RunCommit) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	return f.InsightStore.CommitRun(ctx, c)
}

func (f *faultyInsights) FailRun(ctx context.Context, runID string, numDocuments int, msg string) error {
	if err := f.InsightStore.FailRun(ctx, runID, numDocuments, msg); err != nil {
		return err
	}
	f.failed = append(f.failed, runID)
	return nil
}

func (f *faultyInsights) RecordFailure(ctx context.Context, tenantID, runID string, numDocuments int, msg string) (store.InsightState, error) {
	st, err := f.InsightStore.RecordFailure(ctx, tenantID, runID, numDocuments, msg)
	if err == nil && runID != "" {
		f.failed = append(f.failed, runID)
	}
	return st, err
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
// point along basis axis g. Its clock is the engines', and the time each
// document is indexed at, a second a step, so what a run counts as changed
// never hangs on two clocks' milliseconds.
type engineFixture struct {
	db       *sqlitestore.DB
	docs     *faultyDocs
	store    *sqlitestore.Insights // unwrapped, for assertions
	insights *faultyInsights
	vectors  *vectorSource
	dim      int
	added    int // documents add created, for unique URLs
	clock    time.Time
	logs     bytes.Buffer
}

func newEngineFixture(t *testing.T, sizes ...int) *engineFixture {
	t.Helper()
	db := sqlitetest.NewDB(t)
	f := &engineFixture{
		db:      db,
		docs:    &faultyDocs{DocumentStore: sqlitestore.NewDocuments(db), fail: map[string]error{}},
		store:   sqlitestore.NewInsights(db),
		vectors: &vectorSource{},
		dim:     max(len(sizes), 2),
		clock:   time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
	f.insights = &faultyInsights{InsightStore: f.store}
	for g, n := range sizes {
		f.add(t, g, n)
	}
	return f
}

// tick moves the clock a second on and returns it.
func (f *engineFixture) tick() time.Time {
	f.clock = f.clock.Add(time.Second)
	return f.clock
}

// add creates n documents along axis g, fetched and indexed now, and
// returns their IDs.
func (f *engineFixture) add(t *testing.T, g, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for range n {
		i := f.added
		f.added++
		title := fmt.Sprintf("group%d topic%d item%d", g, g, i)
		d := &store.Document{TenantID: tenant, URL: fmt.Sprintf("https://example.com/%d/%d", g, i), Title: &title}
		require.NoError(t, f.docs.Create(context.Background(), d))
		f.indexed(t, d.ID)
		v := make([]float32, f.dim)
		v[g] = 1
		f.vectors.dvs = append(f.vectors.dvs, store.DocVector{DocumentID: d.ID, Vector: v})
		ids = append(ids, d.ID)
	}
	return ids
}

// indexed marks a document fetched, its vectors written now.
func (f *engineFixture) indexed(t *testing.T, id string) {
	t.Helper()
	require.NoError(t, f.docs.MarkFetched(context.Background(), id))
	_, err := f.db.Exec(`UPDATE documents SET indexed_at = ? WHERE id = ?`, f.tick().Format(storeTime), id)
	require.NoError(t, err)
}

// storeTime is the store's timestamp layout.
const storeTime = "2006-01-02T15:04:05.000Z"

// failed marks the documents failed, and serves their vectors no more.
func (f *engineFixture) failed(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		require.NoError(t, f.docs.MarkFailed(context.Background(), id, store.FailureCauseOther))
	}
	f.vectors.dvs = slices.DeleteFunc(f.vectors.dvs, func(dv store.DocVector) bool { return slices.Contains(ids, dv.DocumentID) })
}

func (f *engineFixture) engine(g Grouper, llm Labeler, cfg Config) *Engine {
	if g == nil {
		g = FlatGrouper(byAxis)
	}
	e := New(f.docs, f.vectors, f.insights, g, llm, cfg, slog.New(slog.NewTextHandler(&f.logs, nil)))
	e.now = func() time.Time { return f.clock }
	return e
}

// placer is a Placer over the fixture's stores, logging to its logs.
func (f *engineFixture) placer() *Placer {
	return NewPlacer(f.insights, f.vectors, nil, slog.New(slog.NewTextHandler(&f.logs, nil)))
}

// rebuild runs a manual Rebuild that must succeed and returns its run.
func (f *engineFixture) rebuild(t *testing.T, e *Engine) *store.InterestRun {
	t.Helper()
	f.tick()
	runID, err := e.Rebuild(context.Background(), tenant, store.RunTriggerManual)
	require.NoError(t, err)
	run, err := f.store.GetRun(context.Background(), runID)
	require.NoError(t, err)
	return run
}

// assertCurrentRun checks that runID is the latest done run and that it
// still has its groups.
func (f *engineFixture) assertCurrentRun(t *testing.T, runID string) {
	t.Helper()
	done, err := f.store.LatestRun(context.Background(), tenant, store.InterestRunDone)
	require.NoError(t, err)
	assert.Equal(t, runID, done.ID)
	groups, err := f.store.RunGroups(context.Background(), runID)
	require.NoError(t, err)
	assert.NotEmpty(t, groups, "the run's groups are intact")
}

// groups returns a run's groups by size; the fixture's sizes are distinct.
func (f *engineFixture) groups(t *testing.T, runID string) map[int]store.InterestGroup {
	t.Helper()
	gs, err := f.store.RunGroups(context.Background(), runID)
	require.NoError(t, err)
	out := map[int]store.InterestGroup{}
	for _, g := range gs {
		out[g.Size] = g
	}
	return out
}

// labels returns a run's interest labels by size.
func (f *engineFixture) labels(t *testing.T, runID string) map[int]string {
	t.Helper()
	out := map[int]string{}
	for size, g := range f.groups(t, runID) {
		out[size] = g.Label
	}
	return out
}

// members lists each interest's member documents, sorted, by its identity.
func (f *engineFixture) members(t *testing.T, runID string) map[string][]string {
	t.Helper()
	as, err := f.store.RunAssignments(context.Background(), runID)
	require.NoError(t, err)
	out := map[string][]string{}
	for _, a := range as {
		if a.Fit == store.InterestFitMember {
			out[a.InterestID] = append(out[a.InterestID], a.DocumentID)
		}
	}
	for _, ids := range out {
		slices.Sort(ids)
	}
	return out
}

// live lists the tenant's live identities, sorted.
func (f *engineFixture) live(t *testing.T) []string {
	t.Helper()
	rows, err := f.db.Query(`SELECT id FROM interests WHERE tenant_id = ? AND retired_at IS NULL ORDER BY id`, tenant)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		out = append(out, id)
	}
	require.NoError(t, rows.Err())
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

// axisOf is a point's largest component.
func axisOf(p Point) int {
	best := 0
	for d, v := range p.Vector {
		if v > p.Vector[best] {
			best = d
		}
	}
	return best
}

// byAxis clusters each point by its largest component, so with the fixture
// cluster g is group g.
var byAxis = clusterFunc(func(_ context.Context, points []Point) ([]int, error) {
	labels := make([]int, len(points))
	for i, p := range points {
		labels[i] = axisOf(p)
	}
	return labels, nil
})

// groupFunc adapts a function to Grouper, named "stub".
type groupFunc func(ctx context.Context, in GroupInput) (Grouping, error)

func (f groupFunc) Group(ctx context.Context, in GroupInput) (Grouping, error) { return f(ctx, in) }
func (groupFunc) Name() string                                                 { return "stub" }
func (groupFunc) Params() map[string]any                                       { return map[string]any{"stub": 1} }

// areasByAxis groups each point into the interest of its axis, inside the
// area areaOf gives the axis, seeded by both: a warm, two-level grouping
// of the fixture.
func areasByAxis(areaOf ...int) groupFunc {
	return func(_ context.Context, in GroupInput) (Grouping, error) {
		n := len(in.Points)
		g := Grouping{Shape: ShapeAreas, Area: make([]int, n), Interest: make([]int, n), Seeds: make([]Seed, n)}
		for i, p := range in.Points {
			axis := axisOf(p)
			g.Area[i], g.Interest[i] = areaOf[axis], axis
			g.Seeds[i] = Seed{Area: areaOf[axis], Interest: axis}
		}
		return g, nil
	}
}

// labelFunc adapts a function to Labeler.
type labelFunc func(ctx context.Context, info ClusterInfo) (Label, error)

func (f labelFunc) Label(ctx context.Context, info ClusterInfo) (Label, error) { return f(ctx, info) }
func (labelFunc) Name() string                                                 { return "test-llm" }

// sizeNames names each group after its size, counting the calls.
func sizeNames(calls *int) labelFunc {
	return func(_ context.Context, info ClusterInfo) (Label, error) {
		*calls++
		return Label{Name: fmt.Sprintf("LLM %d", info.Size), Summary: "About it."}, nil
	}
}

func TestRebuild_First(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5)
	runID, err := f.engine(nil, nil, Config{Center: true}).Rebuild(context.Background(), tenant, store.RunTriggerFirst)
	require.NoError(t, err)
	run, err := f.store.GetRun(context.Background(), runID)
	require.NoError(t, err)

	assert.Equal(t, store.InterestRunDone, run.Status)
	assert.Equal(t, store.RunTriggerFirst, run.Trigger)
	assert.Equal(t, store.RunKindFresh, run.Kind)
	assert.False(t, run.SplitCheck)
	assert.Equal(t, store.InterestShapeFlat, run.Shape)
	assert.Zero(t, run.ChangedDocuments)
	assert.Zero(t, run.ChangesSinceSplit)
	assert.Equal(t, "test", run.Grouper)
	require.NotNil(t, run.VectorsReadAt)
	assert.Len(t, run.Mean, 3, "the centering mean, for placing documents later")
	assert.Equal(t, 12, run.NumDocuments)
	assert.Equal(t, 3, run.NumInterests)
	assert.Zero(t, run.NumAreas)
	assert.Equal(t, 3, run.Created)
	assert.Zero(t, run.Kept+run.Split+run.Merged+run.Moved+run.Dissolved)
	f.assertInvariants(t, run)
	for size, label := range f.labels(t, run.ID) {
		assert.True(t, strings.HasPrefix(label, "Group"), "group of %d got term label %q", size, label)
	}
}

// assertInvariants checks a committed run against its rows: every document
// once, a member, a loose fit or unsorted; and the tenant's live
// identities exactly the run's groups.
func (f *engineFixture) assertInvariants(t *testing.T, run *store.InterestRun) {
	t.Helper()
	as, err := f.store.RunAssignments(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Len(t, as, run.NumDocuments)
	fits := map[store.InterestFit]int{}
	for _, a := range as {
		fits[a.Fit]++
	}
	assert.Equal(t, run.NumLoose, fits[store.InterestFitLoose])
	assert.Equal(t, run.NumUnsorted, fits[store.InterestFitUnsorted])
	gs, err := f.store.RunGroups(context.Background(), run.ID)
	require.NoError(t, err)
	members := 0
	ids := make([]string, 0, len(gs))
	for _, g := range gs {
		ids = append(ids, g.ID)
		if g.Level == store.InterestLevelInterest {
			members += g.Size
		}
	}
	assert.Equal(t, run.NumDocuments, members+run.NumLoose+run.NumUnsorted)
	slices.Sort(ids)
	assert.Equal(t, ids, f.live(t), "the live identities are the run's groups")
	assertMapped(t, run, gs, as)
}

// assertMapped checks a run has a map, built with a place for every group
// and assignment, or failed with none, and every interest its similar
// interests.
func assertMapped(t *testing.T, run *store.InterestRun, gs []store.InterestGroup, as []store.InterestAssignment) {
	t.Helper()
	require.NotNil(t, run.Map, "every run draws a map")
	built := run.Map.Status == store.MapBuilt
	for _, g := range gs {
		assert.Equal(t, built, g.Map != nil, "group %s's place", g.ID)
		if g.Level == store.InterestLevelInterest {
			assert.NotNil(t, g.Similar, "interest %s's similar interests", g.ID)
		}
	}
	for _, a := range as {
		assert.Equal(t, built, a.Map != nil, "document %s's place", a.DocumentID)
	}
}

func TestRebuild_RecordsRunParams(t *testing.T) {
	f := newEngineFixture(t, 3, 4)
	run := f.rebuild(t, f.engine(NewLouvainGrouper(slog.New(slog.DiscardHandler)), nil, Config{Center: true}))
	assert.Equal(t, "louvain", run.Grouper)
	var params map[string]any
	require.NoError(t, json.Unmarshal(run.Params, &params))
	assert.Equal(t, true, params["center"], "the engine's centering is recorded with the grouper's params")
	assert.InDelta(t, 0.40, params["area_min_similarity"], 1e-9)

	f = newEngineFixture(t, 3, 4)
	run = f.rebuild(t, f.engine(nil, nil, Config{}))
	assert.JSONEq(t, `{"center":false}`, string(run.Params), "a grouper without params")
}

// nanParams is a grouper whose params JSON can't encode.
type nanParams struct{ Grouper }

func (nanParams) Params() map[string]any { return map[string]any{"threshold": math.NaN()} }

func TestRebuild_UnencodableParamsCreateNoRun(t *testing.T) {
	f := newEngineFixture(t, 3)
	_, err := f.engine(nanParams{FlatGrouper(byAxis)}, nil, Config{}).Rebuild(context.Background(), tenant, store.RunTriggerManual)
	require.ErrorContains(t, err, "encode grouper params")
	_, err = f.store.LatestRun(context.Background(), tenant, "")
	assert.ErrorIs(t, err, store.ErrNotFound, "no run row is created")
}

// TestRebuild_UnchangedLibrary: a rebuild of an unchanged library is warm
// and keeps every group, identity, label and assignment, asking the
// labeler nothing.
func TestRebuild_UnchangedLibrary(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5)
	calls := 0
	e := f.engine(nil, sizeNames(&calls), Config{Labeling: LabelingLLM, Center: true})
	first := f.rebuild(t, e)
	require.Equal(t, 3, calls)
	want := f.groups(t, first.ID)
	wantMembers := f.members(t, first.ID)

	calls = 0
	second := f.rebuild(t, e)
	assert.Zero(t, calls, "no group needs a name")
	assert.Equal(t, store.RunKindWarm, second.Kind)
	assert.Equal(t, 3, second.Kept)
	assert.Zero(t, second.Created+second.Dissolved+second.ChangedDocuments)
	got := f.groups(t, second.ID)
	for size, g := range want {
		assert.Equal(t, g.ID, got[size].ID, "size %d", size)
		assert.Equal(t, g.Label, got[size].Label, "size %d", size)
		assert.Equal(t, g.LabeledAt, got[size].LabeledAt, "size %d: not relabeled", size)
	}
	assert.Equal(t, wantMembers, f.members(t, second.ID))
	f.assertInvariants(t, second)
	_, err := f.store.GetRun(context.Background(), first.ID)
	require.ErrorIs(t, err, store.ErrNotFound, "the older run is pruned")
	lineage, err := f.store.RunLineage(context.Background(), second.ID)
	require.NoError(t, err)
	assert.Len(t, lineage, 3, "a kept row each")
}

// TestRebuild_CenterFlipIsFresh: a change of the engine's params makes the
// next rebuild fresh, and the groups that survive it keep their names.
func TestRebuild_CenterFlipIsFresh(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5)
	first := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	second := f.rebuild(t, f.engine(nil, nil, Config{Center: false}))
	assert.Equal(t, store.RunKindFresh, second.Kind)
	assert.Equal(t, 3, second.Kept, "carried by overlap")
	before, after := f.groups(t, first.ID), f.groups(t, second.ID)
	for size, g := range before {
		assert.Equal(t, g.ID, after[size].ID)
	}
}

// TestRebuild_FreshWhenTheGroupingIs: a warm-eligible run is recorded fresh
// when its grouping didn't start from the prior: a grouping of another
// shape, or a library whose documents the prior never saw.
func TestRebuild_FreshWhenTheGroupingIs(t *testing.T) {
	t.Run("another shape", func(t *testing.T) {
		f := newEngineFixture(t, 6, 5, 7, 4)
		areas := areasByAxis(0, 0, 1, 1)
		first := f.rebuild(t, f.engine(areas, nil, Config{}))
		require.Equal(t, store.InterestShapeAreas, first.Shape)
		flat := groupFunc(func(ctx context.Context, in GroupInput) (Grouping, error) {
			assert.Equal(t, ShapeAreas, in.Shape, "the prior's shape")
			assert.NotNil(t, in.Prior)
			return FlatGrouper(byAxis).Group(ctx, in)
		})
		second := f.rebuild(t, f.engine(flat, nil, Config{}))
		assert.Equal(t, store.RunKindFresh, second.Kind)
		assert.Equal(t, store.InterestShapeFlat, second.Shape)
		assert.Zero(t, second.NumAreas)
		assert.Equal(t, 4, second.Kept, "the interests carry over")
		retired, err := f.store.RetiredBy(context.Background(), tenant, second.ID)
		require.NoError(t, err)
		assert.Len(t, retired, 2, "the areas are gone")
	})
	t.Run("no seed for any point", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		f.rebuild(t, f.engine(nil, nil, Config{}))
		f.failed(t, f.vectorIDs()...)
		f.add(t, 0, 3)
		f.add(t, 1, 4)
		second := f.rebuild(t, f.engine(nil, nil, Config{}))
		assert.Equal(t, store.RunKindFresh, second.Kind)
		assert.Equal(t, 14, second.ChangedDocuments, "7 added, 7 gone")
	})
}

// recordingGrouper records the Split of every GroupInput it is given.
type recordingGrouper struct {
	Grouper
	splits []bool
}

func (r *recordingGrouper) Group(ctx context.Context, in GroupInput) (Grouping, error) {
	r.splits = append(r.splits, in.Split)
	return r.Grouper.Group(ctx, in)
}

// TestRebuild_SplitCheckCadence: the split check runs once the changes a
// warm run absorbs, added to those since the last split check, reach four
// times the threshold, max(5, ⌈5% of the prior's documents⌉); the run that
// runs it, and a fresh one, reset the count.
func TestRebuild_SplitCheckCadence(t *testing.T) {
	f := newEngineFixture(t, 10, 10) // the threshold is 5 up to 100 documents: a split check every 20 changes
	g := &recordingGrouper{Grouper: FlatGrouper(byAxis)}
	e := f.engine(g, nil, Config{})
	first := f.rebuild(t, e)
	assert.False(t, first.SplitCheck)
	type step struct {
		added           int
		split           bool
		sinceSplitCheck int
	}
	for i, s := range []step{
		{added: 6, sinceSplitCheck: 6},
		{added: 6, sinceSplitCheck: 12},
		{added: 7, sinceSplitCheck: 19},
		{added: 1, split: true, sinceSplitCheck: 0},
		{added: 3, sinceSplitCheck: 3},
	} {
		f.add(t, i%2, s.added)
		run := f.rebuild(t, e)
		assert.Equal(t, store.RunKindWarm, run.Kind, "step %d", i)
		assert.Equal(t, s.added, run.ChangedDocuments, "step %d", i)
		assert.Equal(t, s.split, run.SplitCheck, "step %d", i)
		assert.Equal(t, s.split, g.splits[len(g.splits)-1], "step %d: the grouper was asked", i)
		assert.Equal(t, s.sinceSplitCheck, run.ChangesSinceSplit, "step %d", i)
	}

	f.add(t, 0, 30)
	fresh := f.rebuild(t, f.engine(g, nil, Config{Center: true}))
	assert.Equal(t, store.RunKindFresh, fresh.Kind)
	assert.False(t, fresh.SplitCheck, "a fresh run is its own split check")
	assert.False(t, g.splits[len(g.splits)-1])
	assert.Zero(t, fresh.ChangesSinceSplit)
}

// vectorIDs are the documents whose vectors the fixture serves.
func (f *engineFixture) vectorIDs() []string {
	ids := make([]string, 0, len(f.vectors.dvs))
	for _, dv := range f.vectors.dvs {
		ids = append(ids, dv.DocumentID)
	}
	return ids
}

// TestRebuild_ChangeCount: the documents added since the prior, plus those
// it grouped that are gone: failed, pending again, or deleted, whose rows
// went with them.
func TestRebuild_ChangeCount(t *testing.T) {
	f := newEngineFixture(t, 4, 4)
	e := f.engine(nil, nil, Config{})
	f.rebuild(t, e)
	deleted, failed := f.vectors.dvs[0].DocumentID, f.vectors.dvs[1].DocumentID
	_, err := f.db.Exec(`DELETE FROM documents WHERE id = ?`, deleted)
	require.NoError(t, err)
	f.vectors.dvs = slices.DeleteFunc(f.vectors.dvs, func(dv store.DocVector) bool { return dv.DocumentID == deleted })
	f.failed(t, failed)
	f.add(t, 1, 3)
	run := f.rebuild(t, e)
	assert.Equal(t, 5, run.ChangedDocuments, "3 added, 2 gone")
	assert.Equal(t, 9, run.NumDocuments)
}

// TestRebuild_ReindexedMembersCount: a member the prior grouped, indexed
// again since, is a change: it raises the run's changed_documents, and
// brings the split check closer.
func TestRebuild_ReindexedMembersCount(t *testing.T) {
	f := newEngineFixture(t, 10, 10) // the threshold is 5: a split check every 20 changes
	g := &recordingGrouper{Grouper: FlatGrouper(byAxis)}
	e := f.engine(g, nil, Config{})
	f.rebuild(t, e)
	ids := f.vectorIDs()
	for _, id := range ids[:6] {
		f.indexed(t, id)
	}
	run := f.rebuild(t, e)
	assert.Equal(t, store.RunKindWarm, run.Kind)
	assert.Equal(t, 6, run.ChangedDocuments, "6 reindexed")
	assert.Equal(t, 6, run.ChangesSinceSplit)
	assert.False(t, run.SplitCheck)

	for _, id := range ids[6:20] {
		f.indexed(t, id)
	}
	run = f.rebuild(t, e)
	assert.Equal(t, 14, run.ChangedDocuments)
	assert.True(t, run.SplitCheck, "6 + 14 reindexed reach 4 × 5")
	assert.True(t, g.splits[len(g.splits)-1])
}

func TestRebuild_EmptyCorpus(t *testing.T) {
	t.Run("no prior run records an empty done run", func(t *testing.T) {
		f := newEngineFixture(t)
		run := f.rebuild(t, f.engine(nil, nil, Config{}))
		assert.Equal(t, store.InterestRunDone, run.Status)
		assert.Equal(t, store.InterestShapeFlat, run.Shape)
		require.NotNil(t, run.Map)
		assert.Equal(t, store.MapBuilt, run.Map.Status, "an empty map: Unsorted's disc alone")
		run.Map = nil
		assert.Equal(t, store.RunOutcome{Kind: store.RunKindFresh, Shape: store.InterestShapeFlat}, run.RunOutcome)
	})
	t.Run("a prior done run is kept without a new row", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		prior := f.rebuild(t, f.engine(nil, nil, Config{}))
		f.vectors.dvs = nil

		f.assertNothingToGroup(t, f.engine(nil, nil, Config{}), prior)
	})
	t.Run("a store error is not mistaken for no prior run", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		prior := f.rebuild(t, f.engine(nil, nil, Config{}))
		f.vectors.dvs = nil
		f.insights.latestErr = errLocked

		_, err := f.engine(nil, nil, Config{}).Rebuild(context.Background(), tenant, store.RunTriggerManual)
		require.ErrorIs(t, err, errLocked)
		f.insights.latestErr = nil
		latest, err := f.store.LatestRun(context.Background(), tenant, "")
		require.NoError(t, err)
		assert.Equal(t, prior.ID, latest.ID, "no new run row")
	})
}

// TestRebuild_SkipsNonFiniteVectors: one NaN or infinite document vector
// must not fail the run (with centering on, it would make the corpus mean
// NaN and blame a healthy document). It is left out with a warning that
// names it, and the healthy documents group as they would without it.
func TestRebuild_SkipsNonFiniteVectors(t *testing.T) {
	for name, bad := range map[string]float32{"NaN": float32(math.NaN()), "+Inf": float32(math.Inf(1))} {
		t.Run(name, func(t *testing.T) {
			f := newEngineFixture(t, 3, 4)
			cfg := Config{Center: true}
			want := f.members(t, f.rebuild(t, f.engine(nil, nil, cfg)).ID)

			title := "corrupted vector"
			doc := &store.Document{TenantID: tenant, URL: "https://example.com/bad", Title: &title, State: store.DocStateFetched}
			require.NoError(t, f.docs.Create(context.Background(), doc))
			f.vectors.dvs = append(f.vectors.dvs, store.DocVector{DocumentID: doc.ID, Vector: []float32{1, bad}})

			run := f.rebuild(t, f.engine(nil, nil, cfg))
			assert.Equal(t, want, f.members(t, run.ID))
			assert.Equal(t, 7, run.NumDocuments, "only the finite vectors are grouped")
			warning := f.logLine(t, "vectors have NaN or infinite values")
			assert.Contains(t, warning, "level=WARN")
			assert.Contains(t, warning, doc.ID)
			assert.Contains(t, warning, "count=1")
		})
	}
}

// TestRebuild_AllVectorsNonFinite: a library whose every vector is NaN or
// infinite has nothing to group, like an empty one: the rebuild fails
// keeping the prior run, writes no run row and retires no identity, so the
// interests and their LLM names survive until the vectors are re-embedded.
func TestRebuild_AllVectorsNonFinite(t *testing.T) {
	f := newEngineFixture(t, 3, 4)
	calls := 0
	prior := f.rebuild(t, f.engine(nil, sizeNames(&calls), Config{Labeling: LabelingLLM, Center: true}))
	labels := f.labels(t, prior.ID)
	for _, dv := range f.vectors.dvs {
		dv.Vector[0] = float32(math.NaN())
	}

	f.assertNothingToGroup(t, f.engine(nil, sizeNames(&calls), Config{Labeling: LabelingLLM, Center: true}), prior)
	assert.Equal(t, labels, f.labels(t, prior.ID), "the LLM names kept")
	assert.Contains(t, f.logLine(t, "vectors have NaN or infinite values"), "count=7")
}

// assertNothingToGroup runs a rebuild that finds no vector to group, and
// checks that it failed and counted, so the scheduler backs off, keeping
// prior: no new run row, prior current, no identity retired.
func (f *engineFixture) assertNothingToGroup(t *testing.T, e *Engine, prior *store.InterestRun) {
	t.Helper()
	ctx := context.Background()
	live := f.live(t)

	runID, err := e.Rebuild(ctx, tenant, store.RunTriggerAuto)
	require.ErrorIs(t, err, errNothingToGroup)
	assert.Empty(t, runID)
	latest, err := f.store.LatestRun(ctx, tenant, "")
	require.NoError(t, err)
	assert.Equal(t, prior.ID, latest.ID, "no new run row")
	f.assertCurrentRun(t, prior.ID)
	assert.Equal(t, live, f.live(t), "no identity retired")
	st, err := f.store.State(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, 1, st.Failures, "it counts as a failed rebuild")
	assert.Contains(t, st.LastError, "nothing to group")
	assert.Contains(t, f.logLine(t, "interests: rebuild failed"), "level=WARN")
}

var errLocked = errors.New("database is locked")

func TestRebuild_Failures(t *testing.T) {
	failing := groupFunc(func(context.Context, GroupInput) (Grouping, error) { return Grouping{}, errors.New("boom") })

	t.Run("a failure before the run is created leaves no run", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		f.insights.latestErr = errLocked
		runID, err := f.engine(nil, nil, Config{}).Rebuild(context.Background(), tenant, store.RunTriggerManual)
		require.ErrorIs(t, err, errLocked)
		assert.Empty(t, runID)
		f.insights.latestErr = nil
		_, err = f.store.LatestRun(context.Background(), tenant, "")
		require.ErrorIs(t, err, store.ErrNotFound)
	})
	t.Run("a failed run keeps the prior run, and is the newest until the next", func(t *testing.T) {
		ctx := context.Background()
		f := newEngineFixture(t, 3, 4)
		prior := f.rebuild(t, f.engine(nil, nil, Config{}))
		live := f.live(t)

		first, err := f.engine(failing, nil, Config{}).Rebuild(ctx, tenant, store.RunTriggerManual)
		require.ErrorContains(t, err, "boom")
		assert.Contains(t, f.insights.failed, first)
		newest, err := f.store.LatestRun(ctx, tenant, "")
		require.NoError(t, err)
		assert.Equal(t, first, newest.ID, "the failure is kept, for the Interests page to report")
		require.NotNil(t, newest.Error)
		assert.Contains(t, *newest.Error, "boom")
		f.assertCurrentRun(t, prior.ID)
		assert.Equal(t, live, f.live(t))

		second, err := f.engine(failing, nil, Config{}).Rebuild(ctx, tenant, store.RunTriggerManual)
		require.Error(t, err)
		_, err = f.store.GetRun(ctx, first)
		assert.ErrorIs(t, err, store.ErrNotFound, "a later failure replaces it")
		f.assertCurrentRun(t, prior.ID)

		done := f.rebuild(t, f.engine(nil, nil, Config{}))
		for _, id := range []string{prior.ID, second} {
			_, err = f.store.GetRun(ctx, id)
			assert.ErrorIs(t, err, store.ErrNotFound, "a success prunes every other run")
		}
		f.assertCurrentRun(t, done.ID)
	})
	t.Run("a failed commit", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		prior := f.rebuild(t, f.engine(nil, nil, Config{}))
		live := f.live(t)
		f.add(t, 0, 3)
		f.insights.commitErr = errLocked
		runID, err := f.engine(nil, nil, Config{}).Rebuild(context.Background(), tenant, store.RunTriggerManual)
		require.ErrorIs(t, err, errLocked)
		assert.Contains(t, f.insights.failed, runID)
		f.assertCurrentRun(t, prior.ID)
		assert.Equal(t, live, f.live(t))
	})
	t.Run("a failed first run is kept as the only row", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		runID, err := f.engine(failing, nil, Config{}).Rebuild(context.Background(), tenant, store.RunTriggerFirst)
		require.Error(t, err)
		run, err := f.store.GetRun(context.Background(), runID)
		require.NoError(t, err)
		assert.Equal(t, store.InterestRunFailed, run.Status)
		assert.Equal(t, 7, run.NumDocuments)
	})
	t.Run("nothing is pruned when the last good run can't be read", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		prior := f.rebuild(t, f.engine(nil, nil, Config{}))
		latest := &latestFailsLater{faultyInsights: f.insights}
		e := New(f.docs, f.vectors, latest, failing, nil, Config{}, slog.New(slog.NewTextHandler(&f.logs, nil)))
		runID, err := e.Rebuild(context.Background(), tenant, store.RunTriggerManual)
		require.Error(t, err)

		run, err := f.store.GetRun(context.Background(), runID)
		require.NoError(t, err, "the failed run is not pruned either")
		assert.Equal(t, store.InterestRunFailed, run.Status)
		f.assertCurrentRun(t, prior.ID)
		assert.Contains(t, f.logs.String(), "skip pruning runs")
	})
}

// latestFailsLater answers the first read of the latest done run and fails
// every later one, as a database that went away mid-rebuild would.
type latestFailsLater struct {
	*faultyInsights
	reads int
}

func (l *latestFailsLater) LatestRun(ctx context.Context, tenantID string, status store.InterestRunStatus) (*store.InterestRun, error) {
	if l.reads++; l.reads > 1 {
		return nil, errLocked
	}
	return l.faultyInsights.LatestRun(ctx, tenantID, status)
}

func TestRebuild_CanceledRunIsMarkedFailed(t *testing.T) {
	cases := []struct {
		name string
		// build returns the engine; cancel is the run's own cancel func.
		build func(f *engineFixture, cancel context.CancelFunc) *Engine
	}{
		{"during grouping", func(f *engineFixture, cancel context.CancelFunc) *Engine {
			return f.engine(groupFunc(func(ctx context.Context, _ GroupInput) (Grouping, error) {
				cancel()
				return Grouping{}, ctx.Err()
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
			f.add(t, 0, 3)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runID, err := tc.build(f, cancel).Rebuild(ctx, tenant, store.RunTriggerManual)
			require.ErrorIs(t, err, context.Canceled)
			assert.Contains(t, f.insights.failed, runID, "the run is marked failed although its context is gone")
			f.assertCurrentRun(t, prior.ID)
			st, err := f.store.State(context.Background(), tenant)
			require.NoError(t, err)
			assert.Zero(t, st.Failures, "a cancelled rebuild failed nothing: its job runs again")
			assert.NotContains(t, f.logs.String(), "interests: rebuild failed")
		})
	}
}

// TestRebuild_FailuresAreCounted: every failed rebuild but a cancelled one
// counts, whether it failed before creating a run or after, with a warning
// that says when the next is tried; a done rebuild clears them.
func TestRebuild_FailuresAreCounted(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 4)
	state := func() store.InsightState {
		t.Helper()
		st, err := f.store.State(ctx, tenant)
		require.NoError(t, err)
		return st
	}

	f.vectors.err = errLocked
	runID, err := f.engine(nil, nil, Config{}).Rebuild(ctx, tenant, store.RunTriggerAuto)
	require.ErrorIs(t, err, errLocked)
	assert.Empty(t, runID)
	assert.Equal(t, 1, state().Failures, "a vector read that failed counts")
	assert.Contains(t, state().LastError, "read document vectors")
	warning := f.logLine(t, "interests: rebuild failed")
	assert.Contains(t, warning, "level=WARN")
	assert.Contains(t, warning, "failures=1")
	assert.Contains(t, warning, "retry_at="+RetryAt(state()).Format("2006-01-02T15:04:05"))

	f.vectors.err = nil
	failing := groupFunc(func(context.Context, GroupInput) (Grouping, error) { return Grouping{}, errors.New("boom") })
	runID, err = f.engine(failing, nil, Config{}).Rebuild(ctx, tenant, store.RunTriggerAuto)
	require.ErrorContains(t, err, "boom")
	assert.Equal(t, 2, state().Failures, "so does a run that failed")
	assert.Contains(t, state().LastError, "boom")
	run, err := f.store.GetRun(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, store.InterestRunFailed, run.Status)

	f.rebuild(t, f.engine(nil, nil, Config{}))
	assert.Equal(t, store.InsightState{}, state(), "a done rebuild clears them")
}

// TestRebuild_Abandoned: a rebuild a daemon left unfinished fails the runs
// it left running and counts one failure, with a warning.
func TestRebuild_Abandoned(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 4)
	prior := f.rebuild(t, f.engine(nil, nil, Config{}))
	left := &store.InterestRun{TenantID: tenant, Trigger: store.RunTriggerAuto, Grouper: "test",
		RunOutcome: store.RunOutcome{Kind: store.RunKindWarm, Shape: store.InterestShapeFlat}}
	require.NoError(t, f.store.CreateRun(ctx, left))

	require.NoError(t, f.engine(nil, nil, Config{}).Abandoned(ctx, tenant))
	run, err := f.store.GetRun(ctx, left.ID)
	require.NoError(t, err)
	assert.Equal(t, store.InterestRunFailed, run.Status)
	require.NotNil(t, run.Error)
	assert.Contains(t, *run.Error, "the daemon stopped during this rebuild")
	st, err := f.store.State(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, 1, st.Failures)
	assert.Contains(t, f.logLine(t, "interests: rebuild failed"), "failures=1")
	f.assertCurrentRun(t, prior.ID)
}

// TestRebuild_FreshOwed: a fresh rebuild owed makes the next rebuild fresh,
// which consumes it; one owed after a run read its vectors survives that
// run's commit, warm or fresh.
func TestRebuild_FreshOwed(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 4)
	e := f.engine(nil, nil, Config{})
	f.rebuild(t, e)
	state := func() store.InsightState {
		t.Helper()
		st, err := f.store.State(ctx, tenant)
		require.NoError(t, err)
		return st
	}
	// owe owes a fresh rebuild for reason as of the fixture's clock.
	owe := func(reason store.FreshReason) {
		t.Helper()
		require.NoError(t, f.store.OweFresh(ctx, tenant, reason))
		_, err := f.db.Exec(`UPDATE insight_state SET fresh_owed_at = ?`, f.tick().Format(storeTime))
		require.NoError(t, err)
	}
	// owingWhileGrouping owes reason while the grouping runs, after the
	// run read its vectors.
	owingWhileGrouping := func(reason store.FreshReason) *Engine {
		return f.engine(groupFunc(func(ctx context.Context, in GroupInput) (Grouping, error) {
			owe(reason)
			return FlatGrouper(byAxis).Group(ctx, in)
		}), nil, Config{})
	}

	owe(store.FreshManual)
	fresh := f.rebuild(t, e)
	assert.Equal(t, store.RunKindFresh, fresh.Kind, "owed: fresh though warm-eligible")
	assert.Equal(t, 2, fresh.Kept, "the names carry over")
	assert.Equal(t, store.InsightState{}, state(), "consumed")

	warm := f.rebuild(t, f.engine(nil, nil, Config{}))
	require.Equal(t, store.RunKindWarm, warm.Kind)

	// The stub grouper's params differ from the axis grouper's, so this
	// run is fresh too; it read its vectors before the owe.
	stub := f.rebuild(t, owingWhileGrouping(store.FreshReindex))
	require.Equal(t, store.RunKindFresh, stub.Kind)
	assert.Equal(t, store.FreshReindex, state().FreshOwed, "owed after the fresh run read: kept")

	f.rebuild(t, e)
	assert.Equal(t, store.InsightState{}, state())
	again := f.rebuild(t, e)
	require.Equal(t, store.RunKindWarm, again.Kind)
	warmOwed := f.engine(&owingGrouper{Grouper: FlatGrouper(byAxis), owe: func() { owe(store.FreshReindex) }}, nil, Config{})
	run := f.rebuild(t, warmOwed)
	require.Equal(t, store.RunKindWarm, run.Kind)
	assert.Equal(t, store.FreshReindex, state().FreshOwed, "a warm run never consumes it")
}

// TestRebuild_AReindexOwedMidDrain: a rebuild that finds index jobs left
// before or after it reads the vectors, while a re-embedding owes a fresh
// rebuild, groups fresh but leaves that rebuild owed, its vectors perhaps
// of both builds; the first that finds none either side consumes it. A
// queue it can't read fails the rebuild.
func TestRebuild_AReindexOwedMidDrain(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 4)
	f.rebuild(t, f.engine(nil, nil, Config{}))
	require.NoError(t, f.store.OweFresh(ctx, tenant, store.FreshReindex))
	_, err := f.db.Exec(`UPDATE insight_state SET fresh_owed_at = ?`, f.tick().Format(storeTime))
	require.NoError(t, err)
	// indexing's engine finds index jobs left before it reads the vectors,
	// and after, as told.
	indexing := func(before, after bool, err error) *Engine {
		vectors := &readVectors{vectorSource: f.vectors}
		e := f.engine(nil, nil, Config{Indexing: func(context.Context) (bool, error) {
			if vectors.read {
				return after, err
			}
			return before, err
		}})
		e.chunks = vectors
		return e
	}
	owed := func() store.FreshReason {
		t.Helper()
		st, err := f.store.State(ctx, tenant)
		require.NoError(t, err)
		return st.FreshOwed
	}

	for name, left := range map[string][2]bool{
		"index jobs left after the read":  {false, true},
		"the drain ended during the read": {true, false},
	} {
		run := f.rebuild(t, indexing(left[0], left[1], nil))
		assert.Equal(t, store.RunKindFresh, run.Kind, name)
		assert.Equal(t, store.FreshReindex, owed(), "%s: still owed", name)
	}

	f.tick()
	_, err = indexing(false, false, errLocked).Rebuild(ctx, tenant, store.RunTriggerManual)
	require.ErrorIs(t, err, errLocked)
	assert.Equal(t, store.FreshReindex, owed())

	run := f.rebuild(t, indexing(false, false, nil))
	assert.Equal(t, store.RunKindFresh, run.Kind)
	assert.Empty(t, owed(), "drained: consumed")
}

// readVectors serves a vectorSource's vectors, and notes it did.
type readVectors struct {
	*vectorSource
	read bool
}

func (r *readVectors) DocumentVectors(ctx context.Context, tenantID string) ([]store.DocVector, error) {
	r.read = true
	return r.vectorSource.DocumentVectors(ctx, tenantID)
}

// owingGrouper runs owe as it groups, keeping its grouper's name and
// params, so a warm-eligible run stays warm.
type owingGrouper struct {
	Grouper
	owe func()
}

func (o *owingGrouper) Group(ctx context.Context, in GroupInput) (Grouping, error) {
	o.owe()
	return o.Grouper.Group(ctx, in)
}

// TestRebuild_ParamsChanged: a run grouped by this grouper with today's
// params hasn't; another grouper's, or other params', has.
func TestRebuild_ParamsChanged(t *testing.T) {
	f := newEngineFixture(t, 3, 4)
	run := f.rebuild(t, f.engine(nil, nil, Config{}))
	assert.False(t, f.engine(nil, nil, Config{}).ParamsChanged(run))
	assert.True(t, f.engine(nil, nil, Config{Center: true}).ParamsChanged(run))
	assert.True(t, f.engine(areasByAxis(0, 1), nil, Config{}).ParamsChanged(run))
}

// TestRebuild_PlacesWhatWasIndexedMeanwhile: the documents indexed while a
// rebuild ran are placed in its run once it commits, and the replaced
// run's placements go with it.
func TestRebuild_PlacesWhatWasIndexedMeanwhile(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 5)
	placer := f.placer()
	first := f.rebuild(t, f.engine(nil, nil, Config{Placer: placer}))
	early := f.add(t, 0, 1)
	placer.Place(ctx, tenant, early[0])
	counts, err := f.store.PlacementCounts(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, 1, counts[f.groups(t, first.ID)[3].ID], "placed into the first run")

	var during []string
	indexing := groupFunc(func(ctx context.Context, in GroupInput) (Grouping, error) {
		during = f.add(t, 1, 2)
		return FlatGrouper(byAxis).Group(ctx, in)
	})
	f.logs.Reset()
	second := f.rebuild(t, f.engine(indexing, nil, Config{Placer: placer}))
	assert.Equal(t, 9, second.NumDocuments, "the early document is grouped, the two indexed meanwhile aren't")
	placed, err := f.store.Placements(ctx, second.ID, f.groups(t, second.ID)[5].ID, 0)
	require.NoError(t, err)
	got := make([]string, 0, len(placed))
	for _, p := range placed {
		got = append(got, p.DocumentID)
	}
	assert.ElementsMatch(t, during, got, "placed into their interest after the commit")
	assert.Contains(t, f.logLine(t, "interests rebuilt"), " placed_after=2")
	_, err = f.store.GetRun(ctx, first.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	var left int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM interest_placements WHERE run_id = ?`, first.ID).Scan(&left))
	assert.Zero(t, left, "the first run's placements went with it")
}

// TestRebuild_LogLine: a rebuild says what it did in one INFO line.
func TestRebuild_LogLine(t *testing.T) {
	f := newEngineFixture(t, 6, 5, 7, 4)
	run := f.rebuild(t, f.engine(areasByAxis(0, 0, 1, 1), nil, Config{}))
	line := f.logLine(t, "interests rebuilt")
	assert.Contains(t, line, "level=INFO")
	for _, field := range []string{
		"run=" + run.ID, "trigger=manual", "kind=fresh", "split_check=false", "shape=areas", "documents=22",
		"areas=2", "interests=4", "kept=0", "created=4", "split=0", "merged=0", "moved=0", "dissolved=0",
		"areas_kept=0", "areas_created=2", "areas_dissolved=0", "loose=0", "unsorted=0", "changed=0",
		"read_ms=", "group_ms=", "label_ms=", "labels_llm=0", "labels_terms=6", "persist_ms=", "placed_after=0",
		"map=built", "map_kind=fresh", "map_ms=",
	} {
		assert.Contains(t, line, " "+field, field)
	}
	assert.NotContains(t, line, "embeddings_drifted")

	f.logs.Reset()
	drifted := Config{Drift: func() string { return "the embeddings drifted" }}
	f.rebuild(t, f.engine(areasByAxis(0, 0, 1, 1), nil, drifted))
	assert.Contains(t, f.logLine(t, "interests rebuilt"), " embeddings_drifted=true",
		"a rebuild asked for while the embeddings drifted says so")
}

// TestRebuild_TwoLevels: a grouping in areas is committed with each area's
// sums and its interests' parents, and its documents' areas.
func TestRebuild_TwoLevels(t *testing.T) {
	f := newEngineFixture(t, 6, 5, 7, 4)
	run := f.rebuild(t, f.engine(areasByAxis(0, 0, 1, 1), nil, Config{}))
	assert.Equal(t, 2, run.NumAreas)
	assert.Equal(t, 4, run.NumInterests)
	top, err := f.store.TopGroups(context.Background(), run.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, top, 2)
	for _, area := range top {
		assert.Equal(t, store.InterestLevelArea, area.Level)
		assert.Equal(t, 11, area.Size)
		assert.Nil(t, area.Centroid)
		children, err := f.store.ChildGroups(context.Background(), run.ID, []string{area.ID})
		require.NoError(t, err)
		require.Len(t, children, 2)
		assert.Equal(t, area.Size, children[0].Size+children[1].Size)
	}
	as, err := f.store.RunAssignments(context.Background(), run.ID)
	require.NoError(t, err)
	for _, a := range as {
		assert.NotEmpty(t, a.AreaID)
		assert.Equal(t, store.InterestFitMember, a.Fit)
	}
	f.assertInvariants(t, run)
}

func TestRebuild_LLMFailureFallsBackForTheWholeRun(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5, 6, 7)
	calls := 0
	llm := labelFunc(func(context.Context, ClusterInfo) (Label, error) {
		calls++
		return Label{}, errors.New("ollama unreachable: connection refused")
	})
	run := f.rebuild(t, f.engine(nil, llm, Config{Labeling: LabelingLLM}))

	assert.Equal(t, 1, calls, "the LLM isn't asked again once it has failed")
	labels := f.labels(t, run.ID)
	require.Len(t, labels, 5)
	for size, label := range labels {
		assert.True(t, strings.HasPrefix(label, "Group"), "group of %d got term label %q", size, label)
	}
	warn := f.logLine(t, "llm labeling fell back")
	assert.Contains(t, warn, "level=WARN")
	assert.Contains(t, warn, "groups=5 of=5", "groups never offered to the model count too")
	assert.Contains(t, warn, "connection refused")
}

func TestRebuild_UnparseableReplyFallsBackForOneGroup(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5, 6, 7)
	calls := 0
	llm := labelFunc(func(_ context.Context, info ClusterInfo) (Label, error) {
		calls++
		if info.Size == 5 {
			return Label{}, fmt.Errorf("llm labeler: %w", ErrUnparseableLabel)
		}
		return Label{Name: fmt.Sprintf("LLM %d", info.Size)}, nil
	})
	run := f.rebuild(t, f.engine(nil, llm, Config{Labeling: LabelingLLM}))

	assert.Equal(t, 5, calls, "the model is still asked about every group")
	labels := f.labels(t, run.ID)
	for _, size := range []int{3, 4, 6, 7} {
		assert.Equal(t, fmt.Sprintf("LLM %d", size), labels[size])
	}
	assert.True(t, strings.HasPrefix(labels[5], "Group2"), "got %q", labels[5])
	assert.Equal(t, store.LabelSourceTerms, f.groups(t, run.ID)[5].LabelSource)
	assert.Equal(t, store.LabelSourceLLM, f.groups(t, run.ID)[3].LabelSource)
	assert.Contains(t, f.logLine(t, "llm labeling fell back"), "groups=1 of=5")
}

func TestRebuild_LabelingBudget(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5)
	calls := 0
	llm := labelFunc(func(ctx context.Context, _ ClusterInfo) (Label, error) {
		calls++
		<-ctx.Done() // a hung model: only the budget ends the call
		return Label{}, ctx.Err()
	})
	e := f.engine(nil, llm, Config{Labeling: LabelingLLM, LabelingTimeout: 50 * time.Millisecond})

	done := make(chan error, 1)
	var runID string
	go func() {
		var err error
		runID, err = e.Rebuild(context.Background(), tenant, store.RunTriggerManual)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Rebuild did not finish after the labeling budget ran out")
	}
	assert.Equal(t, 1, calls)
	for size, label := range f.labels(t, runID) {
		assert.True(t, strings.HasPrefix(label, "Group"), "group of %d got term label %q", size, label)
	}
	warn := f.logLine(t, "llm labeling fell back")
	assert.Contains(t, warn, "groups=3 of=3")
	assert.Contains(t, warn, "deadline exceeded")
}

// TestRebuild_FallbackReasonIsTheFallbacks: the fallback WARN names why a
// group fell back, not a later group's taken name that its retry resolved.
func TestRebuild_FallbackReasonIsTheFallbacks(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5)
	llm := labelFunc(func(_ context.Context, info ClusterInfo) (Label, error) {
		switch {
		case info.Size == 5: // labeled first: an unusable reply falls back
			return Label{}, fmt.Errorf("%w: no NAME line", ErrUnparseableLabel)
		case info.Taken != "":
			return Label{Name: "Beta"}, nil
		default: // sizes 4 then 3: the second takes 4's name, then retries
			return Label{Name: "Alpha"}, nil
		}
	})
	run := f.rebuild(t, f.engine(nil, llm, Config{Labeling: LabelingLLM}))
	assert.Equal(t, "Alpha", f.labels(t, run.ID)[4])
	assert.Equal(t, "Beta", f.labels(t, run.ID)[3], "the retry found a free name")
	warn := f.logLine(t, "llm labeling fell back")
	assert.Contains(t, warn, "groups=1 of=3")
	assert.Contains(t, warn, "no NAME line")
	assert.NotContains(t, warn, "was taken")
}

func TestRebuild_LabelsLargestGroupsFirst(t *testing.T) {
	f := newEngineFixture(t, 3, 4, 5, 6, 7)
	var order []int
	llm := labelFunc(func(_ context.Context, info ClusterInfo) (Label, error) {
		order = append(order, info.Size)
		return Label{Name: fmt.Sprintf("Topic %d", info.Size)}, nil
	})
	f.rebuild(t, f.engine(nil, llm, Config{Labeling: LabelingLLM}))
	assert.Equal(t, []int{7, 6, 5, 4, 3}, order)
	assert.NotContains(t, f.logs.String(), "llm labeling fell back")
}

func TestRebuild_LabelingOff(t *testing.T) {
	f := newEngineFixture(t, 3, 4)
	calls := 0
	run := f.rebuild(t, f.engine(nil, sizeNames(&calls), Config{Labeling: LabelingOff}))
	assert.Zero(t, calls)
	for _, g := range f.groups(t, run.ID) {
		assert.Empty(t, g.Label)
		assert.Empty(t, g.LabelSource)
		assert.Nil(t, g.LabeledAt)
	}
	// Labeling turned on later names the carried groups.
	run = f.rebuild(t, f.engine(nil, sizeNames(&calls), Config{Labeling: LabelingLLM}))
	assert.Equal(t, 2, calls)
	assert.Equal(t, map[int]string{3: "LLM 3", 4: "LLM 4"}, f.labels(t, run.ID))
}

// TestRebuild_TermLabelsAreRelabeled: a carried term label is named by the
// LLM once it answers, and kept as it is while it doesn't.
func TestRebuild_TermLabelsAreRelabeled(t *testing.T) {
	f := newEngineFixture(t, 3, 4)
	first := f.groups(t, f.rebuild(t, f.engine(nil, nil, Config{Labeling: LabelingTerms})).ID)

	down := labelFunc(func(context.Context, ClusterInfo) (Label, error) { return Label{}, errors.New("connection refused") })
	second := f.groups(t, f.rebuild(t, f.engine(nil, down, Config{Labeling: LabelingLLM})).ID)
	for size, g := range first {
		assert.Equal(t, g.Label, second[size].Label, "the term labels stay")
		assert.Equal(t, g.LabeledAt, second[size].LabeledAt, "and aren't rewritten")
	}

	calls := 0
	third := f.rebuild(t, f.engine(nil, sizeNames(&calls), Config{Labeling: LabelingLLM}))
	assert.Equal(t, 2, calls)
	got := f.groups(t, third.ID)
	assert.Equal(t, "LLM 3", got[3].Label)
	assert.Equal(t, store.LabelSourceLLM, got[3].LabelSource)
	assert.Equal(t, first[3].ID, got[3].ID, "the identity is kept")

	calls = 0
	f.rebuild(t, f.engine(nil, sizeNames(&calls), Config{Labeling: LabelingLLM}))
	assert.Zero(t, calls, "carried LLM labels are never regenerated")
}

// TestRebuild_CarriedDuplicateIsRelabeled: two carried interests with one
// label that land in one area: only the smaller is named anew.
func TestRebuild_CarriedDuplicateIsRelabeled(t *testing.T) {
	f := newEngineFixture(t, 6, 7, 5, 8)
	calls := 0
	named := func(ctx context.Context, info ClusterInfo) (Label, error) {
		if info.Children != nil {
			return Label{Name: fmt.Sprintf("Area %d", info.Size)}, nil
		}
		return sizeNames(&calls)(ctx, info)
	}
	first := f.rebuild(t, f.engine(areasByAxis(0, 1, 0, 1), labelFunc(named), Config{Labeling: LabelingLLM}))
	byLabel := map[string]string{}
	for _, g := range f.groups(t, first.ID) {
		byLabel[g.Label] = g.ID
	}
	// Name the interests of 6 and 7 alike: in different areas they may be.
	_, err := f.db.Exec(`UPDATE interests SET label = 'Shared Name' WHERE id IN (?, ?)`, byLabel["LLM 6"], byLabel["LLM 7"])
	require.NoError(t, err)

	var asked []ClusterInfo
	second := f.rebuild(t, f.engine(areasByAxis(0, 0, 1, 1), labelFunc(func(_ context.Context, info ClusterInfo) (Label, error) {
		if info.Children == nil {
			asked = append(asked, info)
		}
		return Label{Name: "Distinct Name"}, nil
	}), Config{Labeling: LabelingLLM}))
	require.Len(t, asked, 1, "only the smaller of the two interests is named anew")
	assert.Equal(t, 6, asked[0].Size)
	assert.Contains(t, asked[0].Siblings, "Shared Name")
	got := f.groups(t, second.ID)
	assert.Equal(t, "Shared Name", got[7].Label)
	assert.Equal(t, "Distinct Name", got[6].Label)
	assert.Equal(t, byLabel["LLM 6"], got[6].ID, "renamed, not replaced")
}

// TestRebuild_CarriedDuplicateWithoutAName: the smaller of two carried
// interests with one label, which no term label can name apart because
// its titles use only its sibling's words, loses its label rather than
// keep a duplicate.
func TestRebuild_CarriedDuplicateWithoutAName(t *testing.T) {
	f := newEngineFixture(t, 3, 4)
	terms := f.engine(nil, nil, Config{Labeling: LabelingTerms})
	first := f.groups(t, f.rebuild(t, terms).ID)
	// Every word of the smaller group's titles ("group0 topic0 item0" to
	// "item2").
	const shared = "Group0 Topic0 Item0 Item1 Item2"
	_, err := f.db.Exec(`UPDATE interests SET label = ? WHERE id IN (?, ?)`, shared, first[3].ID, first[4].ID)
	require.NoError(t, err)

	second := f.groups(t, f.rebuild(t, terms).ID)
	assert.Equal(t, shared, second[4].Label, "the larger keeps the label")
	assert.Equal(t, first[3].ID, second[3].ID, "the identity is kept")
	assert.Empty(t, second[3].Label)
	assert.Empty(t, second[3].LabelSource)
	assert.Nil(t, second[3].LabeledAt)
}

// TestRebuild_LabelsAreUniqueInTheirScope: a model that answers one name
// for every group still leaves no two labels with one key in an area, nor
// two areas alike: each group that repeats a name is asked once more,
// told the name is taken, then gets a term label that skips its siblings'
// words.
func TestRebuild_LabelsAreUniqueInTheirScope(t *testing.T) {
	f := newEngineFixture(t, 6, 5, 7, 4, 5)
	asked := map[int][]ClusterInfo{}
	llm := labelFunc(func(_ context.Context, info ClusterInfo) (Label, error) {
		asked[info.Size] = append(asked[info.Size], info)
		return Label{Name: "Same Name!", Summary: "Always the same."}, nil
	})
	// Sizes 5 and 5 are both 5 documents; their areas tell them apart.
	run := f.rebuild(t, f.engine(areasByAxis(0, 0, 1, 1, 1), llm, Config{Labeling: LabelingLLM}))

	top, err := f.store.TopGroups(context.Background(), run.ID, 0, 0)
	require.NoError(t, err)
	areaKeys := make([]string, 0, len(top))
	for _, area := range top {
		areaKeys = append(areaKeys, LabelKey(area.Label))
		children, err := f.store.ChildGroups(context.Background(), run.ID, []string{area.ID})
		require.NoError(t, err)
		var keys []string
		words := map[string]string{}
		for _, c := range children {
			if c.Label == "" {
				continue
			}
			keys = append(keys, LabelKey(c.Label))
			if c.LabelSource == store.LabelSourceTerms {
				for w := range strings.FieldsSeq(LabelKey(c.Label)) {
					assert.NotContains(t, words, w, "a term label skips the words its siblings use")
				}
			}
			for w := range strings.FieldsSeq(LabelKey(c.Label)) {
				words[w] = c.ID
			}
		}
		assert.Len(t, slices.Compact(slices.Sorted(slices.Values(keys))), len(keys), "distinct in area %s", area.Label)
	}
	assert.Len(t, slices.Compact(slices.Sorted(slices.Values(areaKeys))), len(areaKeys), "distinct areas")
	assert.Equal(t, 1, strings.Count(strings.Join(areaKeys, "|"), "same name"), "one area takes the name")

	// The first interest of each area takes the name; every other group
	// is asked twice, the second time told the name it took.
	retried := 0
	for _, infos := range asked {
		for i, info := range infos {
			if info.Taken != "" {
				retried++
				assert.Equal(t, "Same Name!", info.Taken)
				assert.Empty(t, infos[i-1].Taken, "the first try is told nothing")
			}
		}
	}
	assert.Equal(t, 4, retried, "of 7 groups, the first interest of each area and the first area take the name")
}

func TestRebuild_TitleLookupErrors(t *testing.T) {
	t.Run("a deleted document is skipped", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		f.docs.fail[f.vectors.dvs[0].DocumentID] = fmt.Errorf("get: %w", store.ErrNotFound)
		run := f.rebuild(t, f.engine(nil, nil, Config{}))
		f.assertCurrentRun(t, run.ID)
	})
	t.Run("any other error fails the run", func(t *testing.T) {
		f := newEngineFixture(t, 3, 4)
		f.docs.fail[f.vectors.dvs[0].DocumentID] = errLocked
		runID, err := f.engine(nil, nil, Config{}).Rebuild(context.Background(), tenant, store.RunTriggerManual)
		require.ErrorIs(t, err, errLocked)
		assert.Contains(t, f.insights.failed, runID)
	})
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

	raw, _, err := PreparePoints(dvs, false)
	require.NoError(t, err)
	labels, err := c.Cluster(context.Background(), raw)
	require.NoError(t, err)
	assert.Equal(t, 1, distinctClusters(labels), "raw cosines are ~0.98, so all six collapse into one cluster")

	centered, _, err := PreparePoints(dvs, true)
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
			_, _, err := PreparePoints(tc.dvs, true)
			require.EqualError(t, err, tc.want)
		})
	}
}

func TestPreparePoints_Mean(t *testing.T) {
	dvs := []store.DocVector{
		{DocumentID: "a", Vector: []float32{3, 1}},
		{DocumentID: "b", Vector: []float32{1, 3}},
	}
	points, mean, err := PreparePoints(dvs, true)
	require.NoError(t, err)
	assert.Equal(t, []float64{2, 2}, mean, "the mean the points were centered on")
	assert.InDeltaSlice(t, []float32{float32(math.Sqrt2) / 2, -float32(math.Sqrt2) / 2}, points[0].Vector, 1e-7)

	_, mean, err = PreparePoints(dvs, false)
	require.NoError(t, err)
	assert.Nil(t, mean, "not centering")
	_, mean, err = PreparePoints(dvs[:1], true)
	require.NoError(t, err)
	assert.Nil(t, mean, "nothing to center against")

	points, mean, err = PreparePoints(nil, true)
	require.NoError(t, err)
	assert.Empty(t, points)
	assert.NotNil(t, points)
	assert.Nil(t, mean)
}

func TestPreparePoints_RejectsNonFinite(t *testing.T) {
	for _, x := range []float32{float32(math.NaN()), float32(math.Inf(-1))} {
		dvs := []store.DocVector{
			{DocumentID: "a", Vector: []float32{1, 0}},
			{DocumentID: "b", Vector: []float32{x, 0}},
		}
		_, _, err := PreparePoints(dvs, true)
		require.EqualError(t, err, "document b has a NaN or infinite component")
	}
}
