package sqlite

import (
	"maps"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest/curio250"
)

// builtMap is a built map of a commit: kind, time, params, the dots'
// radius and Unsorted's disc.
func builtMap() *store.RunMap {
	return &store.RunMap{Status: store.MapBuilt, Kind: store.RunKindWarm, Took: 1500 * time.Millisecond,
		Params: []byte(`{"algorithm_version":1}`), DotRadius: 2.5, Unsorted: store.Circle{X: 900, Y: 500, R: 40}}
}

// mapped is c with a built map: every group and assignment placed, the
// interests' similar interests listed.
func mapped(c store.RunCommit) store.RunCommit {
	c.Outcome.Map = builtMap()
	c.Groups = append([]store.InterestGroup(nil), c.Groups...)
	for i := range c.Groups {
		g := &c.Groups[i]
		f := float64(i)
		g.Map = &store.GroupMap{ZoomX: 300 + 10*f, ZoomY: 400 + f, ZoomR: 50 + f, AnchorX: 100 + f, AnchorY: 200 + f}
		if g.ParentID != "" {
			for _, o := range c.Groups {
				if o.ParentID != "" && o.ID != g.ID {
					g.Similar = append(g.Similar, store.SimilarInterest{ID: o.ID, Cosine: 0.25 + f/100})
				}
			}
		}
	}
	c.Assignments = append([]store.InterestAssignment(nil), c.Assignments...)
	for i := range c.Assignments {
		f := float64(i)
		c.Assignments[i].Map = &store.MapPosition{MapX: 10 + f, MapY: 20 + f, ZoomX: 300 + f, ZoomY: 400 + f}
	}
	return c
}

// TestInsights_MapRoundTrip: a built map reads back as written: the run's
// map, every group's place and similar interests, every assignment's place,
// and the documents of the map. A failed map reads back with its error,
// time and params, and places nothing.
func TestInsights_MapRoundTrip(t *testing.T) {
	f := newInsightFixture(t, 7)
	first := f.run(t, "local")
	c := mapped(f.firstCommit(first))
	require.NoError(t, f.ins.CommitRun(f.ctx, c))

	run, err := f.ins.GetRun(f.ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, c.Outcome, run.RunOutcome, "the outcome, the map with it")
	latest, err := f.ins.LatestRun(f.ctx, "local", store.InterestRunDone)
	require.NoError(t, err)
	assert.Equal(t, builtMap(), latest.Map)

	groups, err := f.ins.RunGroups(f.ctx, first.ID)
	require.NoError(t, err)
	got := map[string]store.InterestGroup{}
	for _, g := range groups {
		got[g.ID] = g
	}
	for _, g := range c.Groups {
		assert.Equal(t, g.Map, got[g.ID].Map, g.ID)
		assert.Equal(t, g.Similar, got[g.ID].Similar, g.ID)
	}
	assert.Nil(t, got["area-1"].Similar, "an area lists none")
	assignments, err := f.ins.RunAssignments(f.ctx, first.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, c.Assignments, assignments)
	docs, err := f.ins.MapDocuments(f.ctx, first.ID)
	require.NoError(t, err)
	require.Len(t, docs, len(c.Assignments))
	for _, d := range docs {
		for _, a := range c.Assignments {
			if a.DocumentID == d.DocumentID {
				assert.Equal(t, a.Map, d.Map)
			}
		}
	}

	second := f.run(t, "local")
	failed := f.secondCommit(second, first.ID)
	failed.Outcome.Map = &store.RunMap{Status: store.MapFailed, Error: "the map took longer than 2m0s",
		Took: 2 * time.Minute, Params: []byte(`{"algorithm_version":1}`)}
	require.NoError(t, f.ins.CommitRun(f.ctx, failed))
	run, err = f.ins.GetRun(f.ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, failed.Outcome.Map, run.Map)
	groups, err = f.ins.RunGroups(f.ctx, second.ID)
	require.NoError(t, err)
	for _, g := range groups {
		assert.Nil(t, g.Map, g.ID)
	}
	assignments, err = f.ins.RunAssignments(f.ctx, second.ID)
	require.NoError(t, err)
	for _, a := range assignments {
		assert.Nil(t, a.Map, a.DocumentID)
	}
}

// TestInsights_MapRefusesBadCommits: a map that doesn't fit its rows or
// records no params, a group without its identity's level, or a similar
// list that isn't an interest's or names anything but other interests of
// the commit, is an error before anything is written: the run stays
// running and no identity is minted.
func TestInsights_MapRefusesBadCommits(t *testing.T) {
	for name, tc := range map[string]struct {
		spoil func(c *store.RunCommit)
		why   string
	}{
		"a built map missing a document's place": {func(c *store.RunCommit) { c.Assignments[2].Map = nil },
			"has a place on the map: false"},
		"a built map missing a group's place": {func(c *store.RunCommit) { c.Groups[1].Map = nil },
			"has a place on the map: false"},
		"a NaN position": {func(c *store.RunCommit) { c.Assignments[0].Map.MapX = math.NaN() }, "is not on the map"},
		"a position off the map": {func(c *store.RunCommit) { c.Assignments[0].Map.ZoomY = 1000.5 },
			"is not on the map"},
		"a circle off the map":         {func(c *store.RunCommit) { c.Groups[0].Map.ZoomX = 990 }, "is not on the map"},
		"a circle of no radius":        {func(c *store.RunCommit) { c.Groups[0].Map.ZoomR = 0 }, "is not on the map"},
		"a dot of no radius":           {func(c *store.RunCommit) { c.Outcome.Map.DotRadius = 0 }, "dot radius"},
		"Unsorted's disc of no radius": {func(c *store.RunCommit) { c.Outcome.Map.Unsorted.R = -1 }, "disc of Unsorted"},
		"a built map without a kind":   {func(c *store.RunCommit) { c.Outcome.Map.Kind = "" }, "a built map has kind"},
		"no map, with places":          {func(c *store.RunCommit) { c.Outcome.Map = nil }, "with the map built: false"},
		"an unknown map status":        {func(c *store.RunCommit) { c.Outcome.Map.Status = "drawn" }, "map status"},
		"a map without its params":     {func(c *store.RunCommit) { c.Outcome.Map.Params = nil }, "params"},
		"a map's params not JSON":      {func(c *store.RunCommit) { c.Outcome.Map.Params = []byte(`{"seed":`) }, "params"},
		"a failed map without its params": {func(c *store.RunCommit) {
			*c = unplaced(*c)
			c.Outcome.Map = &store.RunMap{Status: store.MapFailed, Error: "boom"}
		}, "params"},
		"a group without a level": {func(c *store.RunCommit) { c.Groups[2].Level = "" }, `has level ""`},
		"a group of another level than its identity": {
			func(c *store.RunCommit) { c.Groups[2].Level = store.InterestLevelArea }, "its new identity interest"},
		"a failed map with places": {func(c *store.RunCommit) {
			c.Outcome.Map = &store.RunMap{Status: store.MapFailed, Error: "boom", Params: []byte(`{}`)}
		}, "with the map built: false"},
		"a failed map without its error": {func(c *store.RunCommit) {
			*c = unplaced(*c)
			c.Outcome.Map = &store.RunMap{Status: store.MapFailed, Params: []byte(`{}`)}
		}, "a failed map has error"},
		"a similar interest the commit lacks": {func(c *store.RunCommit) {
			c.Groups[1].Similar = []store.SimilarInterest{{ID: "interest-9", Cosine: 0.5}}
		}, "as a similar interest"},
		"an area as a similar interest": {func(c *store.RunCommit) {
			c.Groups[1].Similar = []store.SimilarInterest{{ID: "area-1", Cosine: 0.5}}
		}, "as a similar interest"},
		"an area listing similar interests": {func(c *store.RunCommit) {
			c.Groups[0].Similar = []store.SimilarInterest{{ID: "interest-1", Cosine: 0.5}}
		}, "area area-1 lists similar interests"},
		"an interest similar to itself": {func(c *store.RunCommit) {
			c.Groups[1].Similar = []store.SimilarInterest{{ID: c.Groups[1].ID, Cosine: 1}}
		}, "as a similar interest"},
		"four similar interests": {func(c *store.RunCommit) {
			s := store.SimilarInterest{ID: "interest-2", Cosine: 0.5}
			c.Groups[1].Similar = []store.SimilarInterest{s, s, s, s}
		}, "at most 3"},
		"a NaN cosine": {func(c *store.RunCommit) {
			c.Groups[1].Similar = []store.SimilarInterest{{ID: "interest-2", Cosine: math.NaN()}}
		}, "at cosine NaN"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newInsightFixture(t, 7)
			run := f.run(t, "local")
			c := mapped(f.firstCommit(run))
			tc.spoil(&c)
			require.ErrorContains(t, f.ins.CommitRun(f.ctx, c), tc.why)
			got, err := f.ins.GetRun(f.ctx, run.ID)
			require.NoError(t, err)
			assert.Equal(t, store.InterestRunRunning, got.Status)
			assert.Empty(t, f.ids(t, `SELECT id FROM interests`), "no identity minted")
		})
	}
}

// unplaced is c without its places on the map.
func unplaced(c store.RunCommit) store.RunCommit {
	for i := range c.Groups {
		c.Groups[i].Map = nil
	}
	for i := range c.Assignments {
		c.Assignments[i].Map = nil
	}
	return c
}

// TestInsights_MapPrunedWithItsRun: a pruned run's places go with its rows,
// and the kept run's stay.
func TestInsights_MapPrunedWithItsRun(t *testing.T) {
	f := newInsightFixture(t, 9)
	first := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, mapped(f.firstCommit(first))))
	place := &store.MapPosition{MapX: 1, MapY: 2, ZoomX: 3, ZoomY: 4}
	_, err := f.ins.PlaceDocument(f.ctx, "local", store.Placement{RunID: first.ID, DocumentID: f.docs[7], Similarity: 0.2,
		Map: place})
	require.NoError(t, err)
	second := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, mapped(f.secondCommit(second, first.ID))))
	_, err = f.ins.PlaceDocument(f.ctx, "local", store.Placement{RunID: second.ID, DocumentID: f.docs[7], Similarity: 0.2,
		Map: place})
	require.NoError(t, err)

	require.NoError(t, f.ins.PruneRunsExcept(f.ctx, "local", second.ID))
	for _, table := range []string{"interest_groups", "interest_assignments", "interest_placements"} {
		assert.Empty(t, dumpRows(t, f.db, `SELECT * FROM `+table+` WHERE run_id = ?`, first.ID), table)
		assert.NotEmpty(t, dumpRows(t, f.db, `SELECT * FROM `+table+` WHERE run_id = ? AND zoom_x IS NOT NULL`,
			second.ID), table)
	}
}

// TestInsights_PlacementsWithPlaces: PlaceDocument writes a placement's
// place on the map, and placing it anew moves it; PlaceMany writes them
// too, but keeps a placement made already on the map, with its place.
func TestInsights_PlacementsWithPlaces(t *testing.T) {
	f := newInsightFixture(t, 10)
	d := f.docs
	run := f.run(t, "local")
	require.NoError(t, f.ins.CommitRun(f.ctx, mapped(f.firstCommit(run))))
	// at is a place whose zoom dot lies inside both interests' circles.
	at := func(x float64) *store.MapPosition {
		return &store.MapPosition{MapX: x, MapY: x + 1, ZoomX: 300 + x/100, ZoomY: 400 + x/100}
	}
	inUnsorted := &store.MapPosition{MapX: 400, MapY: 401, ZoomX: 905, ZoomY: 495}
	place := func(doc, interest string, p *store.MapPosition) {
		t.Helper()
		written, err := f.ins.PlaceDocument(f.ctx, "local", store.Placement{RunID: run.ID, DocumentID: doc,
			InterestID: interest, Similarity: 0.6, Map: p})
		require.NoError(t, err)
		require.True(t, written)
	}
	places := func() map[string]store.MapPlace {
		t.Helper()
		got, err := f.ins.MapPositions(f.ctx, run.ID, []string{d[0], d[7], d[8], d[9], "no-such-document"})
		require.NoError(t, err)
		out := map[string]store.MapPlace{}
		for _, p := range got {
			out[p.DocumentID] = p
		}
		return out
	}

	place(d[7], "interest-1", at(100))
	assert.Equal(t, *at(100), places()[d[7]].Map)
	place(d[7], "interest-2", at(200))
	assert.Equal(t, store.MapPlace{DocumentID: d[7], InterestID: "interest-2", Map: *at(200)}, places()[d[7]],
		"placed anew, moved")

	n, err := f.ins.PlaceMany(f.ctx, "local", run.ID, []store.Placement{
		{DocumentID: d[7], InterestID: "interest-1", Similarity: 0.5, Map: at(300)},
		{DocumentID: d[8], Similarity: 0.1, Map: inUnsorted},
		{DocumentID: d[9], InterestID: "interest-1", Similarity: 0.5},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	got := places()
	assert.Equal(t, *at(200), got[d[7]].Map, "the placement made already keeps its place")
	assert.Equal(t, store.MapPlace{DocumentID: d[8], Map: *inUnsorted}, got[d[8]], "into Unsorted")
	assert.NotContains(t, got, d[9], "a placement without a place")
	assert.Equal(t, store.MapPlace{DocumentID: d[0], InterestID: "interest-1", Map: store.MapPosition{MapX: 10, MapY: 20,
		ZoomX: 300, ZoomY: 400}}, got[d[0]], "an assignment's")
	assert.Len(t, got, 3)
	none, err := f.ins.MapPositions(f.ctx, run.ID, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestInsights_MapDocuments: the documents of a map are the run's assigned
// documents by ID, then those placed since by ID, without the ones now
// failed or dead and with the pending ones; an untitled one is named by its
// newest bookmark whose title isn't blank.
func TestInsights_MapDocuments(t *testing.T) {
	f := newInsightFixture(t, 10)
	d := f.docs
	run := f.run(t, "local")
	c := mapped(f.firstCommit(run))
	require.NoError(t, f.ins.CommitRun(f.ctx, c))
	for _, doc := range []string{d[9], d[8]} {
		_, err := f.ins.PlaceDocument(f.ctx, "local", store.Placement{RunID: run.ID, DocumentID: doc, InterestID: "interest-2",
			Similarity: 0.4, Map: &store.MapPosition{MapX: 5, MapY: 6, ZoomX: 330, ZoomY: 410}})
		require.NoError(t, err)
	}
	_, err := f.db.Exec(`UPDATE documents SET state = 'failed', failure_cause = 'other' WHERE id = ?`, d[1])
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE documents SET state = 'dead', failure_cause = 'dead_link' WHERE id = ?`, d[8])
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE documents SET state = 'pending' WHERE id = ?`, d[2])
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE documents SET title = 'A title' WHERE id = ?`, d[3])
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO bookmarks (id, tenant_id, document_id, url, title, saved_at, source) VALUES
		('b1', 'local', ?1, 'https://example.com/e', 'Older', '2026-01-01T00:00:00.000Z', 'chrome'),
		('b2', 'local', ?1, 'https://example.com/e', 'Newer', '2026-02-01T00:00:00.000Z', 'firefox'),
		('b3', 'local', ?1, 'https://example.com/e', ' ', '2026-03-01T00:00:00.000Z', 'safari')`, d[4])
	require.NoError(t, err)

	got, err := f.ins.MapDocuments(f.ctx, run.ID)
	require.NoError(t, err)
	assigned := []string{d[0], d[2], d[3], d[4], d[5], d[6]}
	slices.Sort(assigned)
	want := slices.Concat(assigned, []string{d[9]})
	ids := make([]string, len(got))
	for i, doc := range got {
		ids[i] = doc.DocumentID
	}
	assert.Equal(t, want, ids, "assigned by ID, then placed, without the failed and the dead")
	byID := map[string]store.MapDocument{}
	for _, doc := range got {
		byID[doc.DocumentID] = doc
	}
	assert.Equal(t, store.MapDocument{DocumentID: d[9], Placed: true, InterestID: "interest-2", Similarity: 0.4,
		Map: &store.MapPosition{MapX: 5, MapY: 6, ZoomX: 330, ZoomY: 410}, URL: "https://example.com/j"}, byID[d[9]])
	five := byID[d[5]]
	assert.Equal(t, store.InterestFitUnsorted, five.Fit)
	assert.Equal(t, "interest-1", five.NearestID)
	assert.Empty(t, five.InterestID)
	assert.Equal(t, store.InterestFitLoose, byID[d[2]].Fit)
	assert.Equal(t, "interest-1", byID[d[2]].InterestID)
	assert.Equal(t, "A title", byID[d[3]].Title)
	assert.Empty(t, byID[d[3]].BookmarkTitle)
	assert.Equal(t, "Newer", byID[d[4]].BookmarkTitle, "the newest bookmark whose title isn't blank")
}

// Places on mapped's map: inside interest-1's circle, outside interest-2's
// and Unsorted's disc; and inside interest-2's.
var (
	inInterestOne = &store.MapPosition{MapX: 1, MapY: 2, ZoomX: 262, ZoomY: 401}
	inInterestTwo = &store.MapPosition{MapX: 3, MapY: 4, ZoomX: 330, ZoomY: 410}
)

// offTheMap are the placements 2.5.0 leaves on a built map, each made of
// doc in run: a new one with no place, from its index jobs or its sweep,
// and one moved into another interest or into Unsorted, which keeps the
// place it had, outside its new circle.
var offTheMap = map[string]func(t *testing.T, f *insightFixture, run, doc string){
	"placed by 2.5.0's index job": func(t *testing.T, f *insightFixture, run, doc string) {
		require.True(t, curio250.PlaceDocument(t, f.db, "local", store.Placement{RunID: run, DocumentID: doc,
			InterestID: "interest-1", Similarity: 0.6}))
	},
	"placed by 2.5.0's sweep": func(t *testing.T, f *insightFixture, run, doc string) {
		require.True(t, curio250.PlaceIfAbsent(t, f.db, store.Placement{RunID: run, DocumentID: doc, Similarity: 0.2}))
	},
	"moved by 2.5.0 into another interest": func(t *testing.T, f *insightFixture, run, doc string) {
		f.placeOnMap(t, run, doc, "interest-1", inInterestOne)
		require.True(t, curio250.PlaceDocument(t, f.db, "local", store.Placement{RunID: run, DocumentID: doc,
			InterestID: "interest-2", Similarity: 0.6}))
	},
	"moved by 2.5.0 into Unsorted": func(t *testing.T, f *insightFixture, run, doc string) {
		f.placeOnMap(t, run, doc, "interest-1", inInterestOne)
		require.True(t, curio250.PlaceDocument(t, f.db, "local", store.Placement{RunID: run, DocumentID: doc,
			Similarity: 0.2}))
	},
}

// placeOnMap places doc into run's interest at p, as the placer does.
func (f *insightFixture) placeOnMap(t *testing.T, run, doc, interest string, p *store.MapPosition) {
	t.Helper()
	written, err := f.ins.PlaceDocument(f.ctx, "local", store.Placement{RunID: run, DocumentID: doc, InterestID: interest,
		Similarity: 0.6, Map: p})
	require.NoError(t, err)
	require.True(t, written)
}

// TestInsights_PlacementsOffTheMap: on a built map, each placement 2.5.0
// leaves is off the map: Unplaced lists it, and PlaceMany replaces it with
// a placement that has a place, and keeps that one after. A placement
// without a place replaces nothing, and one on the map is neither listed
// nor replaced.
func TestInsights_PlacementsOffTheMap(t *testing.T) {
	readAt := time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
	for name, leave := range offTheMap {
		t.Run(name, func(t *testing.T) {
			f := newInsightFixture(t, 9)
			d := f.docs
			f.indexed(t, readAt.Add(-time.Hour), d[:7]...)
			run := f.doneRunOf(t, readAt, func(r *store.InterestRun) store.RunCommit { return mapped(f.firstCommit(r)) })
			f.indexed(t, readAt.Add(time.Minute), d[7], d[8])
			f.placeOnMap(t, run.ID, d[8], "interest-2", inInterestTwo)
			leave(t, f, run.ID, d[7])
			unplaced := func() []string {
				t.Helper()
				got, err := f.ins.Unplaced(f.ctx, "local", run.ID, readAt)
				require.NoError(t, err)
				return got
			}
			placeMany := func(ps ...store.Placement) int {
				t.Helper()
				n, err := f.ins.PlaceMany(f.ctx, "local", run.ID, ps)
				require.NoError(t, err)
				return n
			}

			assert.Equal(t, []string{d[7]}, unplaced(), "off the map; d8 is on it")
			assert.Zero(t, placeMany(store.Placement{DocumentID: d[7], InterestID: "interest-2", Similarity: 0.5}),
				"a placement without a place repairs nothing")
			assert.Equal(t, 1, placeMany(
				store.Placement{DocumentID: d[7], InterestID: "interest-2", Similarity: 0.5, Map: inInterestTwo},
				store.Placement{DocumentID: d[8], InterestID: "interest-1", Similarity: 0.5, Map: inInterestOne},
			), "d7 repaired, d8 kept")
			got, err := f.ins.MapPositions(f.ctx, run.ID, []string{d[7], d[8]})
			require.NoError(t, err)
			assert.ElementsMatch(t, []store.MapPlace{
				{DocumentID: d[7], InterestID: "interest-2", Map: *inInterestTwo},
				{DocumentID: d[8], InterestID: "interest-2", Map: *inInterestTwo},
			}, got)
			assert.Empty(t, unplaced(), "on the map now")
			assert.Zero(t, placeMany(store.Placement{DocumentID: d[7], InterestID: "interest-1", Similarity: 0.5,
				Map: inInterestOne}), "the repaired placement is kept")
		})
	}
}

// TestInsights_PlacementsWithoutABuiltMap: a run with no map, or one whose
// map failed, has no placement off its map: Unplaced lists what the run
// neither assigned nor placed, as before maps, and PlaceMany keeps every
// placement made, 2.5.0's included.
func TestInsights_PlacementsWithoutABuiltMap(t *testing.T) {
	readAt := time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
	for name, m := range map[string]*store.RunMap{
		"no map":       nil,
		"a failed map": {Status: store.MapFailed, Error: "the map took longer than 2m0s", Params: []byte(`{}`)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newInsightFixture(t, 13)
			d := f.docs
			f.indexed(t, readAt.Add(-time.Hour), d[:7]...)
			run := f.doneRunOf(t, readAt, func(r *store.InterestRun) store.RunCommit {
				c := f.firstCommit(r)
				c.Outcome.Map = m
				return c
			})
			f.indexed(t, readAt.Add(time.Minute), d[7:]...)
			placed := d[7:11]
			for i, name := range slices.Sorted(maps.Keys(offTheMap)) {
				offTheMap[name](t, f, run.ID, placed[i])
			}
			f.placeOnMap(t, run.ID, d[11], "interest-2", nil)

			got, err := f.ins.Unplaced(f.ctx, "local", run.ID, readAt)
			require.NoError(t, err)
			assert.Equal(t, []string{d[12]}, got)
			ps := make([]store.Placement, 0, 5)
			for _, doc := range d[7:12] {
				ps = append(ps, store.Placement{DocumentID: doc, InterestID: "interest-1", Similarity: 0.5, Map: inInterestOne})
			}
			n, err := f.ins.PlaceMany(f.ctx, "local", run.ID, ps)
			require.NoError(t, err)
			assert.Zero(t, n, "every placement made is kept")
		})
	}
}
