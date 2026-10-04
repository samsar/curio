package insight_test

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/quality"
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
// labels. Its clock times the runs' reads and the documents' indexing, a
// second a step.
type libraryEngine struct {
	ctx     context.Context
	lib     library
	db      *sqlitestore.DB
	docs    *sqlitestore.Documents
	ins     *sqlitestore.Insights
	served  *servedVectors
	engine  *insight.Engine
	tenant  string
	clock   time.Time
	fetched map[int]bool // the library's documents fetched in the database
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
			URL: "https://example.com/" + dv.DocumentID, Title: &title}))
	}
	e := &libraryEngine{ctx: ctx, lib: lib, db: db, docs: docs, ins: sqlitestore.NewInsights(db),
		served: &servedVectors{}, tenant: store.LocalTenantID, clock: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		fetched: map[int]bool{}}
	e.engine = insight.New(docs, e.served, e.ins, grouper(), nil,
		insight.Config{Labeling: insight.LabelingTerms, Center: true}, slog.New(slog.DiscardHandler)).
		WithClock(func() time.Time { return e.clock })
	return e
}

// rebuild groups the library's documents at idx: they are fetched, those
// not fetched before indexed now, and the others failed.
func (e *libraryEngine) rebuild(t *testing.T, idx []int, trigger store.RunTrigger) *store.InterestRun {
	t.Helper()
	want := make(map[int]bool, len(idx))
	for _, i := range idx {
		want[i] = true
	}
	for i, dv := range e.lib.docs {
		switch {
		case want[i] && !e.fetched[i]:
			e.clock = e.clock.Add(time.Second)
			require.NoError(t, e.docs.MarkFetched(e.ctx, dv.DocumentID))
			_, err := e.db.Exec(`UPDATE documents SET indexed_at = ? WHERE id = ?`,
				e.clock.Format("2006-01-02T15:04:05.000Z"), dv.DocumentID)
			require.NoError(t, err)
		case !want[i] && e.fetched[i]:
			require.NoError(t, e.docs.MarkFailed(e.ctx, dv.DocumentID, store.FailureCauseOther))
		}
	}
	e.fetched = want
	e.clock = e.clock.Add(time.Second)
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
// changes nothing, its map included. The maps are held to floors on the
// same draws (mapFloors).
func TestEngine_FixtureLibrary(t *testing.T) {
	skipUnderRace(t)
	t.Parallel()
	var kept [3][2]float64
	var maps [3]mapMeasures
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
				firstMap := e.mapOf(t, first.ID)

				warm := e.rebuild(t, newIdx, store.RunTriggerManual)
				assert.Equal(t, store.RunKindWarm, warm.Kind)
				assert.False(t, warm.SplitCheck, "100 changes are under 4 × 95")
				assert.Equal(t, 100, warm.ChangedDocuments)
				assert.Equal(t, 100, warm.ChangesSinceSplit)
				kept[d][0], kept[d][1] = e.identitiesKept(t, before)
				e.assertLineage(t, before, warm)
				maps[d] = e.measureMaps(t, firstMap, warm, newIdx)

				after := e.snapshot(t, warm.ID)
				again := e.rebuild(t, newIdx, store.RunTriggerManual)
				assert.Equal(t, after, e.snapshot(t, again.ID),
					"an unchanged library: the same groups, identities, labels, assignments and places")
				assert.Equal(t, warm.Map.DotRadius, again.Map.DotRadius)
				assert.Equal(t, warm.Map.Unsorted, again.Map.Unsorted)
			})
		}
	})
	assertMapFloors(t, maps)
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

// mapMeasures are one draw's maps, measured: the warm run's document map's
// NP5 and area purity@5, the space's purity, a cold map's NP5 of the same
// library, and how far the warm run moved the documents and the interests'
// centres from the first run's.
type mapMeasures struct {
	np5, purity, spacePurity, coldNP5 float64
	docShift, interestShift           float64
}

// runMap is a run's places on its map: its documents' on the document map
// and its interests' centres in the zoom view.
type runMap struct{ docs, interests map[string][2]float64 }

// mapOf reads run's map, before a later run prunes it.
func (e *libraryEngine) mapOf(t *testing.T, runID string) runMap {
	t.Helper()
	return runMap{docs: docPlaces(e.mapPlaces(t, runID)), interests: e.interestCentres(t, runID)}
}

// measureMaps measures a draw's maps: first, the map of the library before,
// and warm's of the library at idx.
func (e *libraryEngine) measureMaps(t *testing.T, first runMap, warm *store.InterestRun, idx []int) mapMeasures {
	t.Helper()
	points, _, err := insight.PreparePoints(e.lib.subset(idx), true)
	require.NoError(t, err)
	vecs := make([][]float32, len(points))
	for i, p := range points {
		vecs[i] = p.Vector
	}
	pass, err := lists.get(e.ctx, vecs)
	require.NoError(t, err)
	near := make([][]int, len(pass))
	for i, es := range pass {
		for _, edge := range es {
			near[i] = append(near[i], edge.To)
		}
	}
	at := e.mapPlaces(t, warm.ID)
	pos := make([][2]float64, len(points))
	areas := make([]int, len(points))
	areaIndex := map[string]int{}
	for i, p := range points {
		pos[i] = [2]float64{at[p.ID].Map.MapX, at[p.ID].Map.MapY}
		areas[i] = quality.Noise
		if a := at[p.ID]; a.Fit != store.InterestFitUnsorted && a.AreaID != "" {
			if _, ok := areaIndex[a.AreaID]; !ok {
				areaIndex[a.AreaID] = len(areaIndex)
			}
			areas[i] = areaIndex[a.AreaID]
		}
	}
	m := mapMeasures{np5: quality.NeighbourPreservation(near, pos, 5), purity: quality.MapPurity(pos, areas, 5),
		spacePurity: quality.SpacePurity(near, areas, 5)}

	r := group(t, grouper(), e.lib.subset(idx), insight.GroupInput{Shape: insight.ShapeFlat})
	cold, err := insight.BuildMap(e.ctx, coldInput(r))
	require.NoError(t, err)
	coldPos := make([][2]float64, len(points))
	for i, d := range cold.Docs {
		coldPos[i] = [2]float64{d.MapX, d.MapY}
	}
	m.coldNP5 = quality.NeighbourPreservation(near, coldPos, 5)

	m.docShift = quality.Displace(first.docs, docPlaces(at)).Mean
	m.interestShift = quality.Displace(first.interests, e.interestCentres(t, warm.ID)).Mean
	return m
}

// coldInput is the map input of a grouping drawn from nothing.
func coldInput(r run) insight.MapInput {
	in := insight.MapInput{Points: r.points, Grouping: r.g, Centroids: r.centroids, Fits: r.fits, Center: true,
		Seed: insight.MapSeed}
	for a := range numGroups(r.g.Area) {
		in.AreaKeys = append(in.AreaKeys, fmt.Sprintf("a%d", a))
	}
	for l := range numGroups(r.g.Interest) {
		in.InterestKeys = append(in.InterestKeys, fmt.Sprintf("i%d", l))
	}
	return in
}

// mapPlaces are a run's assignments, by document.
func (e *libraryEngine) mapPlaces(t *testing.T, runID string) map[string]store.InterestAssignment {
	t.Helper()
	as, err := e.ins.RunAssignments(e.ctx, runID)
	require.NoError(t, err)
	out := make(map[string]store.InterestAssignment, len(as))
	for _, a := range as {
		require.NotNil(t, a.Map)
		out[a.DocumentID] = a
	}
	return out
}

func docPlaces(as map[string]store.InterestAssignment) map[string][2]float64 {
	out := make(map[string][2]float64, len(as))
	for id, a := range as {
		out[id] = [2]float64{a.Map.MapX, a.Map.MapY}
	}
	return out
}

// interestCentres are a run's interests' centres in the zoom view, by
// identity.
func (e *libraryEngine) interestCentres(t *testing.T, runID string) map[string][2]float64 {
	t.Helper()
	gs, err := e.ins.RunGroups(e.ctx, runID)
	require.NoError(t, err)
	out := map[string][2]float64{}
	for _, g := range gs {
		if g.Level == store.InterestLevelInterest {
			require.NotNil(t, g.Map)
			out[g.ID] = [2]float64{g.Map.ZoomX, g.Map.ZoomY}
		}
	}
	return out
}

// assertMapFloors holds the draws' maps to floors set 0.03 under what they
// measured and bounds 1.5 times what they measured (docs/decisions.md,
// "Interest map", the fixture table): the warm map's NP5, its area purity
// against the space's, how far it moved the documents and the interests'
// centres, and its NP5 against a cold map's of the same library, which it
// may beat (it started from a settled map) but not trail by more than 0.02.
func assertMapFloors(t *testing.T, maps [3]mapMeasures) {
	t.Helper()
	for d, m := range maps {
		t.Logf("draw %d: NP5 %.3f (cold %.3f), area purity@5 %.3f (space %.3f), documents moved %.4f, "+
			"interests' centres %.4f", d, m.np5, m.coldNP5, m.purity, m.spacePurity, m.docShift, m.interestShift)
		assert.GreaterOrEqual(t, m.np5, mapFloors.np5, "draw %d", d)
		assert.GreaterOrEqual(t, m.purity, m.spacePurity-0.05, "draw %d", d)
		assert.LessOrEqual(t, m.docShift, mapFloors.docShift, "draw %d", d)
		assert.LessOrEqual(t, m.interestShift, mapFloors.interestShift, "draw %d", d)
		assert.GreaterOrEqual(t, m.np5, m.coldNP5-0.02, "draw %d: warm against cold", d)
	}
}

// mapFloors are the fixture's floors and bounds. Measured per draw: NP5
// 0.223, 0.222 and 0.224 (a cold map's 0.200 each); documents moved 1.20%,
// 1.46% and 1.23% of the first map's diameter, and interests' centres
// 0.50%, 0.92% and 0.69%.
var mapFloors = struct{ np5, docShift, interestShift float64 }{np5: 0.192, docShift: 0.0219, interestShift: 0.0138}
