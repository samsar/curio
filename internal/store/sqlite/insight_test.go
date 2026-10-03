package sqlite

import (
	"context"
	"math"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// insightFixture is a database with documents for runs to group.
type insightFixture struct {
	ctx  context.Context
	db   *DB
	ins  *Insights
	docs []string
}

func newInsightFixture(t *testing.T, n int) *insightFixture {
	t.Helper()
	return insightFixtureOn(t, newTestDB(t), n)
}

// insightFixtureOn is newInsightFixture over db.
func insightFixtureOn(t *testing.T, db *DB, n int) *insightFixture {
	t.Helper()
	urls := make([]string, n)
	for i := range urls {
		urls[i] = "https://example.com/" + string(rune('a'+i))
	}
	return &insightFixture{ctx: context.Background(), db: db, ins: NewInsights(db), docs: seedDocs(t, db, "local", urls...)}
}

// run creates a running run of tenant.
func (f *insightFixture) run(t *testing.T, tenant string) *store.InterestRun {
	t.Helper()
	r := &store.InterestRun{TenantID: tenant, Trigger: store.RunTriggerManual, Grouper: "test",
		RunOutcome: store.RunOutcome{Kind: store.RunKindFresh, Shape: store.InterestShapeAreas}}
	require.NoError(t, f.ins.CreateRun(f.ctx, r))
	return r
}

// labeledAt is the time the fixtures' labels were made.
var labeledAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// identity is a new identity of the fixtures' commits.
func identity(id string, level store.InterestLevel, label string, source store.LabelSource) store.Interest {
	return store.Interest{ID: id, Level: level, Label: label, Summary: label + ", in a sentence.", LabelSource: source,
		LabeledAt: &labeledAt}
}

func group(id, parent string, size, loose int, cohesion float64, centroid ...float32) store.InterestGroup {
	return store.InterestGroup{Interest: store.Interest{ID: id}, ParentID: parent, Size: size, Loose: loose,
		Cohesion: cohesion, Centroid: centroid}
}

func member(doc, interest, area string, sim float64, seeds ...int) store.InterestAssignment {
	a := store.InterestAssignment{DocumentID: doc, InterestID: interest, AreaID: area, Fit: store.InterestFitMember,
		Similarity: sim, AreaSeed: -1, InterestSeed: -1}
	if len(seeds) == 2 {
		a.AreaSeed, a.InterestSeed = seeds[0], seeds[1]
	}
	return a
}

func loose(doc, interest string, sim float64) store.InterestAssignment {
	return store.InterestAssignment{DocumentID: doc, InterestID: interest, Fit: store.InterestFitLoose,
		Similarity: sim, AreaSeed: -1, InterestSeed: -1}
}

func unsorted(doc, nearest, area string, sim float64) store.InterestAssignment {
	return store.InterestAssignment{DocumentID: doc, AreaID: area, Fit: store.InterestFitUnsorted, Similarity: sim,
		NearestID: nearest, AreaSeed: -1, InterestSeed: -1}
}

// firstCommit groups the fixture's first seven documents into an area
// holding two interests: d0, d1 members of interest-1 and d2 its loose fit,
// d3, d4 members of interest-2, d5 unsorted in the area and d6 outside it.
func (f *insightFixture) firstCommit(run *store.InterestRun) store.RunCommit {
	d := f.docs
	return store.RunCommit{
		RunID: run.ID, TenantID: run.TenantID,
		Outcome: store.RunOutcome{Kind: store.RunKindFresh, Shape: store.InterestShapeAreas, Mean: []float32{0.5, -0.25},
			NumDocuments: 7, NumAreas: 1, NumInterests: 2, NumLoose: 1, NumUnsorted: 2, Created: 2},
		NewIdentities: []store.Interest{
			identity("area-1", store.InterestLevelArea, "Area One", store.LabelSourceLLM),
			identity("interest-1", store.InterestLevelInterest, "Interest One", store.LabelSourceLLM),
			identity("interest-2", store.InterestLevelInterest, "Interest Two", store.LabelSourceTerms),
		},
		Groups: []store.InterestGroup{
			group("area-1", "", 4, 1, 0.4),
			group("interest-1", "area-1", 2, 1, 0.8, 1, 0),
			group("interest-2", "area-1", 2, 0, 0.7, 0, 1),
		},
		Assignments: []store.InterestAssignment{
			member(d[0], "interest-1", "area-1", 0.9, 0, 0), member(d[1], "interest-1", "area-1", 0.8, 0, 0),
			loose(d[2], "interest-1", 0.5),
			member(d[3], "interest-2", "area-1", 0.85, 0, 1), member(d[4], "interest-2", "area-1", 0.6, 0, 1),
			unsorted(d[5], "interest-1", "area-1", 0.3), unsorted(d[6], "interest-2", "", 0.2),
		},
	}
}

// secondCommit rebuilds from first: interest-1 is kept and relabeled,
// interest-2 is retired and split into interest-3, which takes d3 and d4.
func (f *insightFixture) secondCommit(run *store.InterestRun, prior string) store.RunCommit {
	d := f.docs
	relabeled := identity("interest-1", store.InterestLevelInterest, "Interest One, Renamed", store.LabelSourceLLM)
	return store.RunCommit{
		RunID: run.ID, TenantID: run.TenantID, PriorRunID: prior,
		Outcome: store.RunOutcome{Kind: store.RunKindWarm, SplitCheck: true, Shape: store.InterestShapeAreas,
			NumDocuments: 7, NumAreas: 1, NumInterests: 2, NumUnsorted: 3, ChangedDocuments: 1, ChangesSinceSplit: 0,
			Kept: 1, Created: 1, Split: 1},
		NewIdentities: []store.Interest{identity("interest-3", store.InterestLevelInterest, "Interest Three", store.LabelSourceLLM)},
		Relabels:      []store.Interest{relabeled},
		Groups: []store.InterestGroup{
			group("area-1", "", 4, 0, 0.4),
			group("interest-1", "area-1", 2, 0, 0.8, 1, 0),
			group("interest-3", "area-1", 2, 0, 0.7, 0, 1),
		},
		Assignments: []store.InterestAssignment{
			member(d[0], "interest-1", "area-1", 0.9), member(d[1], "interest-1", "area-1", 0.8),
			member(d[3], "interest-3", "area-1", 0.85), member(d[4], "interest-3", "area-1", 0.6),
			unsorted(d[2], "interest-1", "", 0.4), unsorted(d[5], "interest-1", "area-1", 0.3),
			unsorted(d[6], "interest-1", "", 0.2),
		},
		Lineage: []store.LineageRow{
			{OldID: "interest-1", NewID: "interest-1", Event: store.LineageKept, Shared: 2},
			{OldID: "interest-2", NewID: "interest-3", Event: store.LineageSplit, Shared: 2},
		},
	}
}

// commitTwo commits the first and second runs and returns them.
func (f *insightFixture) commitTwo(t *testing.T) (first, second *store.InterestRun) {
	t.Helper()
	first = f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(first)))
	second = f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.secondCommit(second, first.ID)))
	return first, second
}

// live lists the tenant's live identities.
func (f *insightFixture) live(t *testing.T, tenant string) []string {
	t.Helper()
	return f.ids(t, `SELECT id FROM interests WHERE tenant_id = ? AND retired_at IS NULL ORDER BY id`, tenant)
}

// ids reads the one column of query's rows.
func (f *insightFixture) ids(t *testing.T, query string, args ...any) []string {
	t.Helper()
	rows := dumpRows(t, f.db, query, args...)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row[0].(string))
	}
	return out
}

func groupIDs(gs []store.InterestGroup) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.ID)
	}
	return out
}

func documentIDs(as []store.InterestAssignment) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.DocumentID)
	}
	return out
}

func interestIDs(ins []store.Interest) []string {
	out := make([]string, 0, len(ins))
	for _, in := range ins {
		out = append(out, in.ID)
	}
	slices.Sort(out)
	return out
}

// TestInsights_CommitRoundTrip: a two-level commit reads back as written:
// the run's outcome, its identities, its groups with their centroids, its
// assignments of every fit with their seeds and areas, and, from the run
// built on it, the relabel, the retirement and the lineage.
func TestInsights_CommitRoundTrip(t *testing.T) {
	f := newInsightFixture(t, 7)
	ctx, ins, d := f.ctx, f.ins, f.docs
	readAt := time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
	first := &store.InterestRun{TenantID: "local", Trigger: store.RunTriggerFirst, Grouper: "louvain",
		Params: []byte(`{"seed":1}`), VectorsReadAt: &readAt,
		RunOutcome: store.RunOutcome{Kind: store.RunKindFresh, Shape: store.InterestShapeFlat}}
	require.NoError(t, ins.CreateRun(ctx, first))
	require.NotEmpty(t, first.ID)
	assert.Equal(t, store.InterestRunRunning, first.Status)
	assert.False(t, first.StartedAt.IsZero())
	require.NoError(t, ins.CommitRun(ctx, f.firstCommit(first)))

	run, err := ins.LatestRun(ctx, "local", store.InterestRunDone)
	require.NoError(t, err)
	assert.Equal(t, first.ID, run.ID)
	assert.Equal(t, store.RunTriggerFirst, run.Trigger)
	assert.Equal(t, "louvain", run.Grouper)
	assert.JSONEq(t, `{"seed":1}`, string(run.Params))
	require.NotNil(t, run.VectorsReadAt)
	assert.True(t, readAt.Equal(*run.VectorsReadAt))
	assert.Equal(t, f.firstCommit(first).Outcome, run.RunOutcome, "the commit's outcome, the shape included")
	require.NotNil(t, run.FinishedAt)
	assert.Nil(t, run.Error)

	area, err := ins.GetInterest(ctx, "area-1")
	require.NoError(t, err)
	assert.Equal(t, store.Interest{ID: "area-1", TenantID: "local", Level: store.InterestLevelArea, Label: "Area One",
		Summary: "Area One, in a sentence.", LabelSource: store.LabelSourceLLM, CreatedRunID: first.ID,
		LabeledAt: &labeledAt, CreatedAt: area.CreatedAt, UpdatedAt: area.UpdatedAt}, *area)
	created, err := ins.CreatedBy(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"area-1", "interest-1", "interest-2"}, interestIDs(created))

	groups, err := ins.RunGroups(ctx, first.ID)
	require.NoError(t, err)
	byID := map[string]store.InterestGroup{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	assert.Equal(t, []float32{1, 0}, byID["interest-1"].Centroid)
	assert.Nil(t, byID["area-1"].Centroid, "an area has none")
	assert.Equal(t, "Area One", byID["interest-2"].ParentLabel)
	top, err := ins.TopGroups(ctx, first.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"area-1"}, groupIDs(top))
	assert.Equal(t, 4, top[0].Size)
	assert.Equal(t, 1, top[0].Loose)
	assert.InDelta(t, 0.4, top[0].Cohesion, 0)
	assert.Empty(t, top[0].ParentID)
	assert.Nil(t, top[0].Centroid, "a page reads no centroid")
	children, err := ins.ChildGroups(ctx, first.ID, []string{"area-1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"interest-1", "interest-2"}, groupIDs(children))
	one, err := ins.GetGroup(ctx, first.ID, "interest-2")
	require.NoError(t, err)
	assert.Equal(t, "area-1", one.ParentID)
	assert.Equal(t, "Area One", one.ParentLabel)
	assert.Equal(t, "Interest Two", one.Label)
	assert.Equal(t, store.LabelSourceTerms, one.LabelSource)

	assignments, err := ins.RunAssignments(ctx, first.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, f.firstCommit(first).Assignments, assignments)
	members, err := ins.Members(ctx, first.ID, "interest-1", store.InterestFitMember, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{d[0], d[1]}, documentIDs(members))
	looseFits, err := ins.Members(ctx, first.ID, "interest-1", store.InterestFitLoose, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, []store.InterestAssignment{{DocumentID: d[2], InterestID: "interest-1", Fit: store.InterestFitLoose,
		Similarity: 0.5}}, looseFits)
	pile, err := ins.Unsorted(ctx, first.ID, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, []store.InterestAssignment{
		{DocumentID: d[5], Fit: store.InterestFitUnsorted, Similarity: 0.3, NearestID: "interest-1", NearestLabel: "Interest One"},
		{DocumentID: d[6], Fit: store.InterestFitUnsorted, Similarity: 0.2, NearestID: "interest-2", NearestLabel: "Interest Two"},
	}, pile)

	second := f.run(t, "local")
	require.NoError(t, ins.CommitRun(ctx, f.secondCommit(second, first.ID)))
	renamed, err := ins.GetInterest(ctx, "interest-1")
	require.NoError(t, err)
	assert.Equal(t, "Interest One, Renamed", renamed.Label)
	assert.Equal(t, first.ID, renamed.CreatedRunID, "a relabel keeps the identity")
	assert.Nil(t, renamed.RetiredAt)
	retired, err := ins.GetInterest(ctx, "interest-2")
	require.NoError(t, err)
	require.NotNil(t, retired.RetiredAt)
	assert.Equal(t, second.ID, retired.RetiredRunID)
	done, err := ins.GetRun(ctx, second.ID)
	require.NoError(t, err)
	require.NotNil(t, done.FinishedAt)
	assert.True(t, retired.RetiredAt.Equal(*done.FinishedAt), "one commit time")
	assert.Equal(t, []string{"area-1", "interest-1", "interest-3"}, f.live(t, "local"))
	gone, err := ins.RetiredBy(ctx, "local", second.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"interest-2"}, interestIDs(gone))
	created, err = ins.CreatedBy(ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"interest-3"}, interestIDs(created))

	lineage, err := ins.RunLineage(ctx, second.ID)
	require.NoError(t, err)
	want := f.secondCommit(second, first.ID).Lineage
	for i := range want {
		want[i].RunID = second.ID
	}
	assert.Equal(t, want, lineage)
	successors, err := ins.Successors(ctx, second.ID, "interest-2")
	require.NoError(t, err)
	assert.Equal(t, want[1:], successors)
}

// TestInsights_CommitIsAtomic: a commit that fails part way, here on its
// last assignment, which names a document that doesn't exist, writes
// nothing: its run stays running, the previous run stays current with its
// groups, and no identity is created, relabeled or retired.
func TestInsights_CommitIsAtomic(t *testing.T) {
	f := newInsightFixture(t, 7)
	first := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(first)))
	identitiesBefore := dumpRows(t, f.db, `SELECT * FROM interests ORDER BY id`)

	second := f.run(t, "local")
	c := f.secondCommit(second, first.ID)
	c.Assignments = append(c.Assignments, member("no-such-document", "interest-1", "area-1", 0.1))
	err := f.ins.CommitRun(f.ctx, c)
	require.Error(t, err)
	assert.True(t, isForeignKeyViolation(err), "the foreign key refused it: %v", err)

	run, err := f.ins.GetRun(f.ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, store.InterestRunRunning, run.Status)
	latest, err := f.ins.LatestRun(f.ctx, "local", store.InterestRunDone)
	require.NoError(t, err)
	assert.Equal(t, first.ID, latest.ID)
	top, err := f.ins.TopGroups(f.ctx, first.ID, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"area-1"}, groupIDs(top))
	assert.Equal(t, identitiesBefore, dumpRows(t, f.db, `SELECT * FROM interests ORDER BY id`),
		"no identity created, relabeled or retired")
	for _, table := range []string{"interest_groups", "interest_assignments", "interest_lineage"} {
		assert.Empty(t, dumpRows(t, f.db, `SELECT * FROM `+table+` WHERE run_id = ?`, second.ID), table)
	}
}

// TestInsights_CommitConflicts: a commit built from a run that is no
// longer the latest done one, or of a run that isn't running, is a
// conflict and writes nothing.
func TestInsights_CommitConflicts(t *testing.T) {
	f := newInsightFixture(t, 7)
	first := f.run(t, "local")
	stale := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(first)))

	err := f.ins.CommitRun(f.ctx, f.secondCommit(stale, ""))
	require.ErrorIs(t, err, store.ErrConflict, "built from no run, but the first is done")
	assert.Contains(t, err.Error(), first.ID)
	_, err = f.ins.GetInterest(f.ctx, "interest-3")
	require.ErrorIs(t, err, store.ErrNotFound, "nothing written")

	second := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.secondCommit(second, first.ID)))
	failed := f.run(t, "local")
	require.NoError(t, f.ins.FailRun(f.ctx, failed.ID, 7, "boom"))
	// Commits of nothing, which would retire every identity were they
	// to land.
	outcome := store.RunOutcome{Kind: store.RunKindWarm, Shape: store.InterestShapeAreas}
	for name, run := range map[string]string{"a done run": second.ID, "a failed run": failed.ID} {
		err := f.ins.CommitRun(f.ctx, store.RunCommit{RunID: run, TenantID: "local", PriorRunID: second.ID, Outcome: outcome})
		require.ErrorIs(t, err, store.ErrConflict, name)
	}
	assert.Equal(t, []string{"area-1", "interest-1", "interest-3"}, f.live(t, "local"))

	c := f.firstCommit(f.run(t, "local"))
	c.PriorRunID = second.ID
	c.Outcome.Kind = "lukewarm"
	require.ErrorContains(t, f.ins.CommitRun(f.ctx, c), "lukewarm", "checked before anything is written")
}

// TestInsights_FailRun: a running run fails with its error and documents;
// a finished one is refused and left as it was.
func TestInsights_FailRun(t *testing.T) {
	f := newInsightFixture(t, 7)
	run := f.run(t, "local")
	require.NoError(t, f.ins.FailRun(f.ctx, run.ID, 7, "boom"))
	got, err := f.ins.GetRun(f.ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.InterestRunFailed, got.Status)
	assert.Equal(t, 7, got.NumDocuments)
	require.NotNil(t, got.Error)
	assert.Equal(t, "boom", *got.Error)
	require.NotNil(t, got.FinishedAt)

	require.ErrorIs(t, f.ins.FailRun(f.ctx, run.ID, 9, "again"), store.ErrConflict)
	again, err := f.ins.GetRun(f.ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, got, again, "unchanged")

	done := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(done)))
	require.ErrorIs(t, f.ins.FailRun(f.ctx, done.ID, 7, "late"), store.ErrConflict)
	require.ErrorIs(t, f.ins.FailRun(f.ctx, "no-such-run", 7, "boom"), store.ErrNotFound)
}

// TestInsights_CreateRunChecks: a run without a tenant or grouper, or with
// a value outside an enum, is refused before anything is written.
func TestInsights_CreateRunChecks(t *testing.T) {
	f := newInsightFixture(t, 0)
	valid := func() *store.InterestRun {
		return &store.InterestRun{TenantID: "local", Trigger: store.RunTriggerAuto, Grouper: "louvain",
			RunOutcome: store.RunOutcome{Kind: store.RunKindWarm, Shape: store.InterestShapeFlat}}
	}
	for name, mutate := range map[string]func(*store.InterestRun){
		"no tenant":  func(r *store.InterestRun) { r.TenantID = "" },
		"no grouper": func(r *store.InterestRun) { r.Grouper = "" },
		"a trigger":  func(r *store.InterestRun) { r.Trigger = "cron" },
		"a kind":     func(r *store.InterestRun) { r.Kind = "" },
		"a shape":    func(r *store.InterestRun) { r.Shape = "tree" },
	} {
		r := valid()
		mutate(r)
		require.Error(t, f.ins.CreateRun(f.ctx, r), name)
	}
	assert.Empty(t, dumpRows(t, f.db, `SELECT id FROM interest_runs`))
	require.NoError(t, f.ins.CreateRun(f.ctx, valid()))
}

// TestInsights_LatestRun_SameStart: of two runs started in the same
// millisecond, the later one is the latest, whatever its status.
func TestInsights_LatestRun_SameStart(t *testing.T) {
	f := newInsightFixture(t, 7)
	done := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(done)))
	failed := f.run(t, "local")
	require.NoError(t, f.ins.FailRun(f.ctx, failed.ID, 0, "boom"))
	_, err := f.db.ExecContext(f.ctx, `UPDATE interest_runs SET started_at = ?`, formatTime(done.StartedAt))
	require.NoError(t, err)

	latest, err := f.ins.LatestRun(f.ctx, "local", "")
	require.NoError(t, err)
	assert.Equal(t, failed.ID, latest.ID)
	latest, err = f.ins.LatestRun(f.ctx, "local", store.InterestRunDone)
	require.NoError(t, err)
	assert.Equal(t, done.ID, latest.ID)
	_, err = f.ins.LatestRun(f.ctx, "other", "")
	require.ErrorIs(t, err, store.ErrNotFound)
}

// TestInsights_PagedOrders: a run's groups and an interest's documents
// come in a total order, ties broken by ID, so pages of any size read from
// successive offsets add up to the whole list, every row once.
func TestInsights_PagedOrders(t *testing.T) {
	f := newInsightFixture(t, 5)
	d := f.docs
	run := f.run(t, "local")
	ids := []string{"area-big", "area-cohesive", "area-small", "area-tie-a", "area-tie-b", "in-a", "in-b", "in-c"}
	c := store.RunCommit{RunID: run.ID, TenantID: "local",
		Outcome: store.RunOutcome{Kind: store.RunKindFresh, Shape: store.InterestShapeAreas, NumDocuments: 5}}
	for _, id := range ids {
		level := store.InterestLevelArea
		if id[:3] == "in-" {
			level = store.InterestLevelInterest
		}
		c.NewIdentities = append(c.NewIdentities, store.Interest{ID: id, Level: level})
	}
	// Written out of order: the ties on size and cohesion come by ID, and
	// the ties on similarity by document ID.
	c.Groups = []store.InterestGroup{
		group("area-tie-b", "", 2, 0, 0.5), group("area-small", "", 1, 0, 0.9), group("area-big", "", 5, 0, 0.1),
		group("area-tie-a", "", 2, 0, 0.5), group("area-cohesive", "", 2, 0, 0.8),
		group("in-c", "area-tie-a", 1, 0, 0.5), group("in-a", "area-big", 5, 0, 0.5),
		group("in-b", "area-tie-a", 1, 0, 0.5),
	}
	c.Assignments = []store.InterestAssignment{member(d[3], "in-a", "area-big", 0.5), member(d[0], "in-a", "area-big", 0.9),
		member(d[4], "in-a", "area-big", 0.5), member(d[1], "in-a", "area-big", 0.5), member(d[2], "in-a", "area-big", 0.7)}
	require.NoError(t, f.ins.CommitRun(f.ctx, c))

	all, err := f.ins.TopGroups(f.ctx, run.ID, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"area-big", "area-cohesive", "area-tie-a", "area-tie-b", "area-small"}, groupIDs(all))
	nested, err := f.ins.NestedGroups(f.ctx, run.ID, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"in-a", "in-b", "in-c"}, groupIDs(nested))
	assert.Equal(t, "area-tie-a", nested[1].ParentID)
	children, err := f.ins.ChildGroups(f.ctx, run.ID, []string{"area-tie-a", "area-big", "area-small"})
	require.NoError(t, err)
	assert.Equal(t, []string{"in-a", "in-b", "in-c"}, groupIDs(children), "by area, then in order")
	allMembers, err := f.ins.Members(f.ctx, run.ID, "in-a", store.InterestFitMember, 0, 0)
	require.NoError(t, err)
	byDocument := slices.Sorted(slices.Values([]string{d[1], d[3], d[4]}))
	assert.Equal(t, append([]string{d[0], d[2]}, byDocument...), documentIDs(allMembers))

	for size := 1; size <= 3; size++ {
		var groups []store.InterestGroup
		var members []store.InterestAssignment
		for offset := 0; offset < len(all)+size; offset += size {
			page, err := f.ins.TopGroups(f.ctx, run.ID, size, offset)
			require.NoError(t, err)
			assert.LessOrEqual(t, len(page), size)
			groups = append(groups, page...)
			memberPage, err := f.ins.Members(f.ctx, run.ID, "in-a", store.InterestFitMember, size, offset)
			require.NoError(t, err)
			members = append(members, memberPage...)
		}
		assert.Equal(t, groupIDs(all), groupIDs(groups), "groups %d a page", size)
		assert.Equal(t, documentIDs(allMembers), documentIDs(members), "members %d a page", size)
	}

	rest, err := f.ins.TopGroups(f.ctx, run.ID, 0, 3)
	require.NoError(t, err)
	assert.Equal(t, groupIDs(all[3:]), groupIDs(rest), "no limit: the rest from the offset")
	restMembers, err := f.ins.Members(f.ctx, run.ID, "in-a", store.InterestFitMember, -1, 3)
	require.NoError(t, err)
	assert.Equal(t, documentIDs(allMembers[3:]), documentIDs(restMembers))
	for _, offset := range []int{len(all), 1000, math.MaxInt} {
		past, err := f.ins.TopGroups(f.ctx, run.ID, 24, offset)
		require.NoError(t, err, "offset %d", offset)
		assert.Empty(t, past, "offset %d", offset)
	}

	for name, read := range map[string]func() error{
		"top":    func() error { _, err := f.ins.TopGroups(f.ctx, run.ID, 24, -1); return err },
		"nested": func() error { _, err := f.ins.NestedGroups(f.ctx, run.ID, 24, -1); return err },
		"members": func() error {
			_, err := f.ins.Members(f.ctx, run.ID, "in-a", store.InterestFitMember, 50, -1)
			return err
		},
		"unsorted": func() error { _, err := f.ins.Unsorted(f.ctx, run.ID, 50, -1); return err },
	} {
		require.ErrorContains(t, read(), "offset -1 is negative", name)
	}
	_, err = f.ins.Members(f.ctx, run.ID, "in-a", store.InterestFitUnsorted, 0, 0)
	require.Error(t, err, "an interest has no unsorted documents")
}

// TestInsights_Prune: pruning runs takes their groups, assignments and
// placements and keeps every identity and the lineage; deleting a document
// takes its rows in every run.
func TestInsights_Prune(t *testing.T) {
	f := newInsightFixture(t, 7)
	first, second := f.commitTwo(t)
	_, err := f.db.Exec(`INSERT INTO interest_placements (run_id, document_id, interest_id, similarity, placed_at)
		VALUES (?, ?, 'interest-1', 0.5, ?), (?, ?, 'interest-1', 0.5, ?)`,
		first.ID, f.docs[0], formatTime(time.Now()), second.ID, f.docs[0], formatTime(time.Now()))
	require.NoError(t, err)
	identities := dumpRows(t, f.db, `SELECT * FROM interests ORDER BY id`)

	_, err = f.db.Exec(deleteDocumentSQL, f.docs[0])
	require.NoError(t, err)
	for _, table := range []string{"interest_assignments", "interest_placements"} {
		assert.Empty(t, dumpRows(t, f.db, `SELECT run_id FROM `+table+` WHERE document_id = ?`, f.docs[0]), table)
	}

	require.NoError(t, f.ins.PruneRunsExcept(f.ctx, "local", second.ID))
	_, err = f.ins.GetRun(f.ctx, first.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	for _, table := range []string{"interest_groups", "interest_assignments", "interest_placements"} {
		assert.Empty(t, dumpRows(t, f.db, `SELECT * FROM `+table+` WHERE run_id = ?`, first.ID), table)
	}
	assert.NotEmpty(t, dumpRows(t, f.db, `SELECT * FROM interest_groups WHERE run_id = ?`, second.ID))
	assert.Equal(t, identities, dumpRows(t, f.db, `SELECT * FROM interests ORDER BY id`), "identities outlive runs")
	lineage, err := f.ins.RunLineage(f.ctx, second.ID)
	require.NoError(t, err)
	assert.Len(t, lineage, 2, "lineage outlives runs")
	require.Error(t, f.ins.PruneRunsExcept(f.ctx, "local"), "no run to keep")
}

// TestInsights_PruneRetired: identities retired before the cutoff go, with
// their lineage, while the current run holds the live ones; the others
// stay.
func TestInsights_PruneRetired(t *testing.T) {
	f := newInsightFixture(t, 7)
	_, second := f.commitTwo(t)
	require.NoError(t, f.ins.PruneRunsExcept(f.ctx, "local", second.ID))

	n, err := f.ins.PruneRetired(f.ctx, "local", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n, "retired just now")
	n, err = f.ins.PruneRetired(f.ctx, "local", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	_, err = f.ins.GetInterest(f.ctx, "interest-2")
	require.ErrorIs(t, err, store.ErrNotFound)
	assert.Equal(t, []string{"area-1", "interest-1", "interest-3"}, f.live(t, "local"), "live identities stay")
	lineage, err := f.ins.RunLineage(f.ctx, second.ID)
	require.NoError(t, err)
	require.Len(t, lineage, 1, "the pruned identity's lineage went with it")
	assert.Equal(t, "interest-1", lineage[0].OldID)
}

// TestInsights_TrimLineage: the trim keeps the current run's rows and a
// retired identity's, and drops older runs' rows of live identities.
func TestInsights_TrimLineage(t *testing.T) {
	f := newInsightFixture(t, 7)
	_, second := f.commitTwo(t)
	third := f.run(t, "local")
	c := f.secondCommit(third, second.ID)
	c.NewIdentities, c.Relabels = nil, nil
	c.Lineage = []store.LineageRow{
		{OldID: "interest-1", NewID: "interest-1", Event: store.LineageKept, Shared: 2},
		{OldID: "interest-3", NewID: "interest-3", Event: store.LineageKept, Shared: 2},
	}
	require.NoError(t, f.ins.CommitRun(f.ctx, c))

	require.NoError(t, f.ins.TrimLineage(f.ctx, "local", third.ID))
	rows := dumpRows(t, f.db, `SELECT run_id, old_id FROM interest_lineage ORDER BY run_id = ?, old_id`, third.ID)
	assert.Equal(t, [][]any{
		{second.ID, "interest-2"},
		{third.ID, "interest-1"}, {third.ID, "interest-3"},
	}, rows)
}

// TestInsights_Tenants: a tenant's commit retires and reads only its own
// identities, and its prunes and lineage trim take only its own rows.
func TestInsights_Tenants(t *testing.T) {
	f := newInsightFixture(t, 7)
	theirs := f.run(t, "other")
	c := f.firstCommit(theirs)
	for i := range c.NewIdentities {
		c.NewIdentities[i].ID = "other-" + c.NewIdentities[i].ID
	}
	for i := range c.Groups {
		c.Groups[i].ID = "other-" + c.Groups[i].ID
		if c.Groups[i].ParentID != "" {
			c.Groups[i].ParentID = "other-" + c.Groups[i].ParentID
		}
	}
	for i := range c.Assignments {
		for _, id := range []*string{&c.Assignments[i].InterestID, &c.Assignments[i].AreaID, &c.Assignments[i].NearestID} {
			if *id != "" {
				*id = "other-" + *id
			}
		}
	}
	// A row of a run other than the one local's trim keeps, whose identity
	// is live: what the trim deletes, were it local's.
	c.Lineage = []store.LineageRow{{OldID: "other-interest-1", NewID: "other-interest-1", Event: store.LineageKept, Shared: 2}}
	require.NoError(t, f.ins.CommitRun(f.ctx, c))
	theirLive := f.live(t, "other")

	_, second := f.commitTwo(t)
	assert.Equal(t, theirLive, f.live(t, "other"), "never retired by another tenant's commit")
	got, err := f.ins.GetInterests(f.ctx, "local", []string{"other-area-1", "area-1", "interest-2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"area-1", "interest-2"}, interestIDs(got), "retired or not, only the tenant's")
	latest, err := f.ins.LatestRun(f.ctx, "other", store.InterestRunDone)
	require.NoError(t, err)
	assert.Equal(t, theirs.ID, latest.ID)

	require.NoError(t, f.ins.PruneRunsExcept(f.ctx, "local", second.ID))
	_, err = f.ins.GetRun(f.ctx, theirs.ID)
	require.NoError(t, err, "another tenant's run is never pruned")
	_, err = f.ins.PruneRetired(f.ctx, "local", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, theirLive, f.live(t, "other"))
	require.NoError(t, f.ins.TrimLineage(f.ctx, "local", second.ID))
	lineage, err := f.ins.RunLineage(f.ctx, theirs.ID)
	require.NoError(t, err)
	assert.Len(t, lineage, 1, "another tenant's lineage is never trimmed")
}

// TestInsights_Placements: a run's placements into an interest or into
// Unsorted come newest first, and count per interest.
func TestInsights_Placements(t *testing.T) {
	f := newInsightFixture(t, 7)
	run := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(run)))
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	for i, p := range []struct {
		doc      string
		interest any
	}{{f.docs[0], "interest-1"}, {f.docs[1], "interest-1"}, {f.docs[2], nil}, {f.docs[3], "interest-2"}} {
		_, err := f.db.Exec(`INSERT INTO interest_placements (run_id, document_id, interest_id, similarity, placed_at)
			VALUES (?, ?, ?, 0.5, ?)`, run.ID, p.doc, p.interest, formatTime(at.Add(time.Duration(i)*time.Minute)))
		require.NoError(t, err)
	}

	got, err := f.ins.Placements(f.ctx, run.ID, "interest-1", 20)
	require.NoError(t, err)
	assert.Equal(t, []store.Placement{
		{RunID: run.ID, DocumentID: f.docs[1], InterestID: "interest-1", Similarity: 0.5, PlacedAt: at.Add(time.Minute)},
		{RunID: run.ID, DocumentID: f.docs[0], InterestID: "interest-1", Similarity: 0.5, PlacedAt: at},
	}, got)
	got, err = f.ins.Placements(f.ctx, run.ID, "interest-1", 1)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	got, err = f.ins.Placements(f.ctx, run.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, f.docs[2], got[0].DocumentID)
	assert.Empty(t, got[0].InterestID)
	counts, err := f.ins.PlacementCounts(f.ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"interest-1": 2, "interest-2": 1, "": 1}, counts)
}

// TestInsights_VectorBlobs: a stored mean or centroid that isn't a whole
// number of float32s is an error, not a vector.
func TestInsights_VectorBlobs(t *testing.T) {
	f := newInsightFixture(t, 7)
	run := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(run)))
	_, err := f.db.Exec(`UPDATE interest_runs SET mean = x'000000' WHERE id = ?`, run.ID)
	require.NoError(t, err)
	_, err = f.ins.GetRun(f.ctx, run.ID)
	require.ErrorContains(t, err, "3 bytes")
	_, err = f.db.Exec(`UPDATE interest_groups SET centroid = x'0000000000' WHERE interest_id = 'interest-1'`)
	require.NoError(t, err)
	_, err = f.ins.RunGroups(f.ctx, run.ID)
	require.ErrorContains(t, err, "5 bytes")
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

// indexed marks the documents fetched, their vectors written at at.
func (f *insightFixture) indexed(t *testing.T, at time.Time, ids ...string) {
	t.Helper()
	docs := NewDocuments(f.db)
	for _, id := range ids {
		require.NoError(t, docs.MarkFetched(f.ctx, id))
		_, err := f.db.Exec(`UPDATE documents SET indexed_at = ? WHERE id = ?`, formatTime(at), id)
		require.NoError(t, err)
	}
}

// doneRun commits the first commit as a run that read its vectors at
// readAt, and returns it as LatestRun reads it.
func (f *insightFixture) doneRun(t *testing.T, readAt time.Time) *store.InterestRun {
	t.Helper()
	run := &store.InterestRun{TenantID: "local", Trigger: store.RunTriggerFirst, Grouper: "test", VectorsReadAt: &readAt,
		RunOutcome: store.RunOutcome{Kind: store.RunKindFresh, Shape: store.InterestShapeAreas}}
	require.NoError(t, f.ins.CreateRun(f.ctx, run))
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(run)))
	done, err := f.ins.GetRun(f.ctx, run.ID)
	require.NoError(t, err)
	return done
}

// TestInsights_Changes: the documents changed since a run read its
// vectors, each kind counted once at a time; those the run couldn't group,
// indexed before its read, never; and the same count after a restart.
func TestInsights_Changes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "curio.db")
	db, err := Open(ctx, path)
	require.NoError(t, err)
	_, err = Migrate(ctx, db)
	require.NoError(t, err)
	f := insightFixtureOn(t, db, 10)
	d := f.docs
	readAt := time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
	// d7 had no chunk and d8 a non-finite vector: both indexed before the
	// run read, neither assigned.
	f.indexed(t, readAt.Add(-time.Hour), d...)
	run := f.doneRun(t, readAt)
	changes := func(ins *Insights) store.RunChanges {
		t.Helper()
		c, err := ins.Changes(ctx, run)
		require.NoError(t, err)
		return c
	}
	assert.Equal(t, store.RunChanges{}, changes(f.ins), "nothing changed since the read")

	docs := NewDocuments(db)
	f.indexed(t, readAt.Add(time.Second), d[9]) // after the read, before the next check
	_, err = db.Exec(`DELETE FROM documents WHERE id = ?`, d[0])
	require.NoError(t, err)
	_, err = docs.RequeueFetch(ctx, "local", d[1])
	require.NoError(t, err)
	require.NoError(t, docs.MarkFailed(ctx, d[2], store.FailureCauseDeadLink))
	f.indexed(t, readAt, d[3]) // in the millisecond the run read: counted
	got := changes(f.ins)
	assert.Equal(t, store.RunChanges{Added: 1, Left: 2, Deleted: 1, Reindexed: 1}, got)
	assert.Equal(t, 5, got.Total())

	f.indexed(t, readAt.Add(2*time.Second), d[1])
	assert.Equal(t, store.RunChanges{Added: 1, Left: 1, Deleted: 1, Reindexed: 2}, changes(f.ins),
		"a refetch left while pending, and is reindexed once fetched again")

	before := changes(f.ins)
	require.NoError(t, db.Close())
	reopened, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	assert.Equal(t, before, changes(NewInsights(reopened)), "the count is derived: a restart keeps it")
}

// TestInsights_OweFresh: a re-embedding's fresh rebuild is always owed, from
// now; a manual one only when nothing, or a manual one, is owed.
func TestInsights_OweFresh(t *testing.T) {
	f := newInsightFixture(t, 0)
	state := func() store.InsightState {
		t.Helper()
		st, err := f.ins.State(f.ctx, "local")
		require.NoError(t, err)
		return st
	}
	assert.Equal(t, store.InsightState{}, state(), "no row: nothing owed")
	backdate := func() {
		t.Helper()
		_, err := f.db.Exec(`UPDATE insight_state SET fresh_owed_at = '2026-01-01T00:00:00.000Z'`)
		require.NoError(t, err)
	}
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, f.ins.OweFresh(f.ctx, "local", store.FreshManual))
	assert.Equal(t, store.FreshManual, state().FreshOwed)
	assert.WithinDuration(t, time.Now(), state().FreshOwedAt, time.Minute)
	backdate()
	require.NoError(t, f.ins.OweFresh(f.ctx, "local", store.FreshManual))
	assert.True(t, state().FreshOwedAt.After(early), "asked again, owed from now")

	require.NoError(t, f.ins.OweFresh(f.ctx, "local", store.FreshReindex))
	assert.Equal(t, store.FreshReindex, state().FreshOwed)
	backdate()
	require.NoError(t, f.ins.OweFresh(f.ctx, "local", store.FreshManual))
	assert.Equal(t, store.InsightState{FreshOwed: store.FreshReindex, FreshOwedAt: early}, state(),
		"a manual request never takes a re-embedding's place")
	require.NoError(t, f.ins.OweFresh(f.ctx, "local", store.FreshReindex))
	assert.True(t, state().FreshOwedAt.After(early), "a second re-embedding owes it from now")

	require.ErrorContains(t, f.ins.OweFresh(f.ctx, "local", "shape"), "not one of the FreshReason constants")
	other, err := f.ins.State(f.ctx, "other")
	require.NoError(t, err)
	assert.Equal(t, store.InsightState{}, other, "per tenant")
}

// TestInsights_RecordFailure: a failed rebuild counts one failure, failing
// its run in the same transaction when there is one, and a done rebuild
// clears them.
func TestInsights_RecordFailure(t *testing.T) {
	f := newInsightFixture(t, 7)
	st, err := f.ins.RecordFailure(f.ctx, "local", "", 0, "read the vectors: database is locked")
	require.NoError(t, err)
	assert.Equal(t, 1, st.Failures)
	assert.Equal(t, "read the vectors: database is locked", st.LastError)
	assert.WithinDuration(t, time.Now(), st.LastFailureAt, time.Minute)

	run := f.run(t, "local")
	st, err = f.ins.RecordFailure(f.ctx, "local", run.ID, 7, "group: boom")
	require.NoError(t, err)
	assert.Equal(t, 2, st.Failures)
	assert.Equal(t, "group: boom", st.LastError)
	failed, err := f.ins.GetRun(f.ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.InterestRunFailed, failed.Status)
	assert.Equal(t, 7, failed.NumDocuments)
	require.NotNil(t, failed.Error)
	assert.Equal(t, "group: boom", *failed.Error)

	_, err = f.ins.RecordFailure(f.ctx, "local", run.ID, 7, "again")
	require.ErrorIs(t, err, store.ErrConflict, "a run that isn't running")
	got, err := f.ins.State(f.ctx, "local")
	require.NoError(t, err)
	assert.Equal(t, st, got, "and nothing counted")

	done := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(done)))
	got, err = f.ins.State(f.ctx, "local")
	require.NoError(t, err)
	assert.Equal(t, store.InsightState{}, got, "a done rebuild clears the failures")
}

// TestInsights_RecordAbandoned: a rebuild a daemon left unfinished fails
// every running run of the tenant's, and counts one failure.
func TestInsights_RecordAbandoned(t *testing.T) {
	f := newInsightFixture(t, 7)
	done := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(done)))
	left, again, other := f.run(t, "local"), f.run(t, "local"), f.run(t, "other")

	st, err := f.ins.RecordAbandoned(f.ctx, "local", "the daemon stopped")
	require.NoError(t, err)
	assert.Equal(t, 1, st.Failures)
	assert.Equal(t, "the daemon stopped", st.LastError)
	for id, want := range map[string]store.InterestRunStatus{
		done.ID: store.InterestRunDone, left.ID: store.InterestRunFailed, again.ID: store.InterestRunFailed,
		other.ID: store.InterestRunRunning,
	} {
		run, err := f.ins.GetRun(f.ctx, id)
		require.NoError(t, err)
		assert.Equal(t, want, run.Status, id)
		if want == store.InterestRunFailed {
			require.NotNil(t, run.Error)
			assert.Equal(t, "the daemon stopped", *run.Error)
			assert.NotNil(t, run.FinishedAt)
		}
	}

	st, err = f.ins.RecordAbandoned(f.ctx, "local", "the daemon stopped")
	require.NoError(t, err)
	assert.Equal(t, 2, st.Failures, "one failure each, a running run or not")
}

// TestInsights_CommitConsumesTheFreshRebuildOwed: a fresh run that read its
// vectors at or after the fresh rebuild was owed consumes it; a warm run,
// or a fresh one that read before, leaves it owed.
func TestInsights_CommitConsumesTheFreshRebuildOwed(t *testing.T) {
	owedAt := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		kind     store.RunKind
		readAt   time.Time
		consumed bool
	}{
		{"fresh, read after", store.RunKindFresh, owedAt.Add(time.Second), true},
		{"fresh, read as it was owed", store.RunKindFresh, owedAt, true},
		{"fresh, read before", store.RunKindFresh, owedAt.Add(-time.Second), false},
		{"warm", store.RunKindWarm, owedAt.Add(time.Second), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInsightFixture(t, 7)
			require.NoError(t, f.ins.OweFresh(f.ctx, "local", store.FreshReindex))
			_, err := f.db.Exec(`UPDATE insight_state SET fresh_owed_at = ?`, formatTime(owedAt))
			require.NoError(t, err)
			run := &store.InterestRun{TenantID: "local", Trigger: store.RunTriggerReindex, Grouper: "test",
				VectorsReadAt: &tc.readAt, RunOutcome: store.RunOutcome{Kind: tc.kind, Shape: store.InterestShapeAreas}}
			require.NoError(t, f.ins.CreateRun(f.ctx, run))
			c := f.firstCommit(run)
			c.Outcome.Kind = tc.kind
			require.NoError(t, f.ins.CommitRun(f.ctx, c))

			st, err := f.ins.State(f.ctx, "local")
			require.NoError(t, err)
			if tc.consumed {
				assert.Equal(t, store.InsightState{}, st)
			} else {
				assert.Equal(t, store.InsightState{FreshOwed: store.FreshReindex, FreshOwedAt: owedAt}, st)
			}
		})
	}
}

// TestInsights_PlaceDocument: a placement into the latest done run is
// written, and written anew; one into a run a rebuild replaced, of a
// document the run assigned, or of a document gone, writes nothing.
func TestInsights_PlaceDocument(t *testing.T) {
	f := newInsightFixture(t, 9)
	d := f.docs
	first := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(first)))
	place := func(run, doc, interest string, sim float64) bool {
		t.Helper()
		written, err := f.ins.PlaceDocument(f.ctx, "local", store.Placement{RunID: run, DocumentID: doc,
			InterestID: interest, Similarity: sim})
		require.NoError(t, err)
		return written
	}

	assert.True(t, place(first.ID, d[7], "interest-1", 0.6))
	assert.True(t, place(first.ID, d[8], "", 0.2), "into Unsorted")
	assert.True(t, place(first.ID, d[7], "interest-2", 0.7), "a document indexed again is placed anew")
	got, err := f.ins.Placements(f.ctx, first.ID, "interest-2", 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, d[7], got[0].DocumentID)
	assert.InDelta(t, 0.7, got[0].Similarity, 1e-9)
	assert.WithinDuration(t, time.Now(), got[0].PlacedAt, time.Minute)

	assert.False(t, place(first.ID, d[0], "interest-2", 0.9), "the run assigned it")
	assert.False(t, place(first.ID, "no-such-document", "interest-1", 0.5), "a document gone")
	assert.False(t, place("no-such-run", d[7], "interest-1", 0.5))

	second := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.secondCommit(second, first.ID)))
	placed, err := f.ins.PlaceDocument(f.ctx, "local", store.Placement{RunID: first.ID, DocumentID: d[8],
		InterestID: "interest-1", Similarity: 0.5})
	require.NoError(t, err)
	assert.False(t, placed, "a run a rebuild replaced")
	other, err := f.ins.PlaceDocument(f.ctx, "other", store.Placement{RunID: second.ID, DocumentID: d[8], Similarity: 0.1})
	require.NoError(t, err)
	assert.False(t, other, "another tenant's run")
}

// TestInsights_PlaceMany: the sweep's placements go into the latest done
// run, never over a placement made since, and leave out documents gone;
// into a replaced run, none.
func TestInsights_PlaceMany(t *testing.T) {
	f := newInsightFixture(t, 10)
	d := f.docs
	first := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.firstCommit(first)))
	_, err := f.ins.PlaceDocument(f.ctx, "local", store.Placement{RunID: first.ID, DocumentID: d[7],
		InterestID: "interest-2", Similarity: 0.8})
	require.NoError(t, err)

	n, err := f.ins.PlaceMany(f.ctx, "local", first.ID, []store.Placement{
		{DocumentID: d[7], InterestID: "interest-1", Similarity: 0.5},
		{DocumentID: d[8], InterestID: "interest-1", Similarity: 0.6},
		{DocumentID: d[9], Similarity: 0.1},
		{DocumentID: "no-such-document", Similarity: 0.1},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	counts, err := f.ins.PlacementCounts(f.ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"interest-1": 1, "interest-2": 1, "": 1}, counts, "the fast path's placement kept")

	second := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, f.secondCommit(second, first.ID)))
	n, err = f.ins.PlaceMany(f.ctx, "local", first.ID, []store.Placement{{DocumentID: d[9], Similarity: 0.1}})
	require.NoError(t, err)
	assert.Zero(t, n, "a run a rebuild replaced")
}

// TestInsights_Unplaced: the fetched documents indexed since a time that a
// run neither assigned nor placed.
func TestInsights_Unplaced(t *testing.T) {
	f := newInsightFixture(t, 10)
	d := f.docs
	readAt := time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
	f.indexed(t, readAt.Add(-time.Hour), d[:8]...)
	run := f.doneRun(t, readAt)
	f.indexed(t, readAt.Add(time.Minute), d[3], d[8], d[9])
	_, err := f.ins.PlaceDocument(f.ctx, "local", store.Placement{RunID: run.ID, DocumentID: d[9], Similarity: 0.1})
	require.NoError(t, err)

	got, err := f.ins.Unplaced(f.ctx, "local", run.ID, readAt)
	require.NoError(t, err)
	assert.Equal(t, []string{d[8]}, got, "d3 is assigned, d7 indexed before the read, d9 placed")
	assigned, err := f.ins.Assigned(f.ctx, run.ID, d[3])
	require.NoError(t, err)
	assert.True(t, assigned)
	assigned, err = f.ins.Assigned(f.ctx, run.ID, d[8])
	require.NoError(t, err)
	assert.False(t, assigned)
}
