package insight_test

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// servedVectors serves the vectors of the documents a test chose; the
// engine reads nothing else from the chunk store.
type servedVectors struct {
	store.ChunkStore
	dvs []store.DocVector
}

func (s *servedVectors) DocumentVectors(context.Context, string) ([]store.DocVector, error) {
	return s.dvs, nil
}

// libraryEngine is fixture library 1 in a database, its documents created
// with the fixture's IDs and with titles of their topic, and an engine
// that groups the documents served with the shipped grouper and term
// labels.
type libraryEngine struct {
	ctx    context.Context
	lib    library
	ins    *sqlitestore.Insights
	served *servedVectors
	engine *insight.Engine
	tenant string
}

func newLibraryEngine(t *testing.T) *libraryEngine {
	t.Helper()
	ctx := context.Background()
	db := sqlitetest.NewDB(t)
	docs := sqlitestore.NewDocuments(db)
	lib := fixtureLibrary(1)
	for i, dv := range lib.docs {
		title := fmt.Sprintf("Note%04d", i)
		if lib.topic[i] >= 0 {
			title = fmt.Sprintf("Topic%02d Area%d piece %d", lib.topic[i], lib.area[i], i)
		}
		require.NoError(t, docs.Create(ctx, &store.Document{ID: dv.DocumentID, TenantID: store.LocalTenantID,
			URL: "https://example.com/" + dv.DocumentID, Title: &title, State: store.DocStateFetched}))
	}
	e := &libraryEngine{ctx: ctx, lib: lib, ins: sqlitestore.NewInsights(db), served: &servedVectors{},
		tenant: store.LocalTenantID}
	e.engine = insight.New(docs, e.served, e.ins, grouper(), nil,
		insight.Config{Labeling: insight.LabelingTerms, Center: true}, slog.New(slog.DiscardHandler))
	return e
}

// rebuild groups the library's documents at idx.
func (e *libraryEngine) rebuild(t *testing.T, idx []int, trigger store.RunTrigger) *store.InterestRun {
	t.Helper()
	e.served.dvs = e.lib.subset(idx)
	runID, err := e.engine.Rebuild(e.ctx, e.tenant, trigger)
	require.NoError(t, err)
	run, err := e.ins.GetRun(e.ctx, runID)
	require.NoError(t, err)
	return run
}

// snapshot is what a run holds: its groups and assignments.
type snapshot struct {
	groups      []store.InterestGroup
	assignments []store.InterestAssignment
}

func (e *libraryEngine) snapshot(t *testing.T, runID string) snapshot {
	t.Helper()
	gs, err := e.ins.RunGroups(e.ctx, runID)
	require.NoError(t, err)
	as, err := e.ins.RunAssignments(e.ctx, runID)
	require.NoError(t, err)
	for i := range gs {
		gs[i].RunID = "" // two runs' groups compare as what they hold
	}
	slices.SortFunc(gs, func(a, b store.InterestGroup) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(as, func(a, b store.InterestAssignment) int { return strings.Compare(a.DocumentID, b.DocumentID) })
	return snapshot{groups: gs, assignments: as}
}

// TestEngine_FixtureLibrary pins the engine's stability on fixture library
// 1, through the store: a library grouped fresh at 95% and rebuilt warm
// once the last 5% arrive keeps at least 90% of its interest and area
// identities, mean of 3 draws (measured 96.3% · 100%; per draw 97.1%,
// 91.9% and 100% of interests), without the split check, retiring only
// what carry-over gave no heir; and a rebuild of the unchanged library
// changes nothing.
func TestEngine_FixtureLibrary(t *testing.T) {
	skipUnderRace(t)
	t.Parallel()
	var kept [3][2]float64
	t.Run("draws", func(t *testing.T) {
		for d := range kept {
			t.Run(strconv.Itoa(d), func(t *testing.T) {
				t.Parallel()
				e := newLibraryEngine(t)
				prevIdx, newIdx := changeDraw(len(e.lib.docs), "add", d)
				first := e.rebuild(t, prevIdx, store.RunTriggerFirst)
				require.Equal(t, store.RunKindFresh, first.Kind)
				require.Equal(t, store.InterestShapeAreas, first.Shape)
				before := e.snapshot(t, first.ID)

				warm := e.rebuild(t, newIdx, store.RunTriggerManual)
				assert.Equal(t, store.RunKindWarm, warm.Kind)
				assert.False(t, warm.SplitCheck, "100 changes are under 4 × 95")
				assert.Equal(t, 100, warm.ChangedDocuments)
				assert.Equal(t, 100, warm.ChangesSinceSplit)
				kept[d][0], kept[d][1] = e.identitiesKept(t, before)
				e.assertLineage(t, before, warm)

				after := e.snapshot(t, warm.ID)
				again := e.rebuild(t, newIdx, store.RunTriggerManual)
				assert.Equal(t, after, e.snapshot(t, again.ID), "an unchanged library: the same groups, identities, labels and assignments")
			})
		}
	})
	var interests, areas float64
	for _, k := range kept {
		interests += k[0] / 3
		areas += k[1] / 3
	}
	assert.GreaterOrEqual(t, interests, 0.90)
	assert.GreaterOrEqual(t, areas, 0.90)
}

// identitiesKept is the share of before's interest and area identities
// still live.
func (e *libraryEngine) identitiesKept(t *testing.T, before snapshot) (interests, areas float64) {
	t.Helper()
	ids := make([]string, 0, len(before.groups))
	for _, g := range before.groups {
		ids = append(ids, g.ID)
	}
	now, err := e.ins.GetInterests(e.ctx, e.tenant, ids)
	require.NoError(t, err)
	live, total := map[store.InterestLevel]int{}, map[store.InterestLevel]int{}
	for _, in := range now {
		total[in.Level]++
		if in.RetiredAt == nil {
			live[in.Level]++
		}
	}
	return float64(live[store.InterestLevelInterest]) / float64(total[store.InterestLevelInterest]),
		float64(live[store.InterestLevelArea]) / float64(total[store.InterestLevelArea])
}

// assertLineage checks run's lineage and retirements against Carry over
// before's groups and run's: every identity it retired is one carry-over
// gave no heir, and names run, and its lineage rows are Carry's.
func (e *libraryEngine) assertLineage(t *testing.T, before snapshot, run *store.InterestRun) {
	t.Helper()
	after := e.snapshot(t, run.ID)
	old := groupsOf(before)
	cur := groupsOf(after)
	ids := make([]string, 0, len(after.assignments))
	for _, a := range after.assignments {
		ids = append(ids, a.DocumentID)
	}
	areas, err := insight.Carry(insight.CarryInput{Old: old.oldGroups(store.InterestLevelArea),
		New: cur.newGroups(store.InterestLevelArea, nil), Library: ids})
	require.NoError(t, err)
	interests, err := insight.Carry(insight.CarryInput{Old: old.oldGroups(store.InterestLevelInterest),
		New: cur.newGroups(store.InterestLevelInterest, cur.byLevel[store.InterestLevelArea]), Library: ids, Areas: &areas})
	require.NoError(t, err)

	var want []store.LineageRow
	var heirless []string
	for level, c := range map[store.InterestLevel]insight.Carried{store.InterestLevelArea: areas, store.InterestLevelInterest: interests} {
		for _, l := range c.Lineage {
			want = append(want, store.LineageRow{RunID: run.ID, OldID: l.OldID, NewID: cur.byLevel[level][l.New],
				Event: store.LineageEvent(l.Event), Shared: l.Shared})
		}
		for _, f := range c.Fates {
			if !f.Kept {
				heirless = append(heirless, f.ID)
			}
		}
	}
	got, err := e.ins.RunLineage(e.ctx, run.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, want, got, "the stored lineage is Carry's")
	retired, err := e.ins.RetiredBy(e.ctx, e.tenant, run.ID)
	require.NoError(t, err)
	retiredIDs := make([]string, 0, len(retired))
	for _, r := range retired {
		assert.Equal(t, run.ID, r.RetiredRunID)
		require.NotNil(t, r.RetiredAt)
		retiredIDs = append(retiredIDs, r.ID)
	}
	assert.ElementsMatch(t, heirless, retiredIDs, "retired exactly the old groups without an heir")
}

// runGroups are a run's groups as carry-over matches them: each level's
// identities, sorted, and their members.
type runGroups struct {
	byLevel   map[store.InterestLevel][]string
	members   map[string][]string // an area's community, an interest's members
	parent    map[string]string
	areaIndex map[string]int
}

func groupsOf(s snapshot) runGroups {
	r := runGroups{byLevel: map[store.InterestLevel][]string{}, members: map[string][]string{},
		parent: map[string]string{}, areaIndex: map[string]int{}}
	for _, g := range s.groups {
		r.byLevel[g.Level] = append(r.byLevel[g.Level], g.ID)
		r.parent[g.ID] = g.ParentID
	}
	for i, id := range r.byLevel[store.InterestLevelArea] {
		r.areaIndex[id] = i
	}
	for _, a := range s.assignments {
		if a.AreaID != "" {
			r.members[a.AreaID] = append(r.members[a.AreaID], a.DocumentID)
		}
		if a.Fit == store.InterestFitMember {
			r.members[a.InterestID] = append(r.members[a.InterestID], a.DocumentID)
		}
	}
	return r
}

func (r runGroups) oldGroups(level store.InterestLevel) []insight.OldGroup {
	out := make([]insight.OldGroup, 0, len(r.byLevel[level]))
	for _, id := range r.byLevel[level] {
		out = append(out, insight.OldGroup{ID: id, Members: r.members[id], Parent: r.parent[id]})
	}
	return out
}

func (r runGroups) newGroups(level store.InterestLevel, areas []string) []insight.NewGroup {
	out := make([]insight.NewGroup, 0, len(r.byLevel[level]))
	for _, id := range r.byLevel[level] {
		g := insight.NewGroup{Members: r.members[id], Parent: -1}
		if p := r.parent[id]; p != "" && areas != nil {
			g.Parent = r.areaIndex[p]
		}
		out = append(out, g)
	}
	return out
}
