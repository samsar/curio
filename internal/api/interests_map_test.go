package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest/curio250"
	"github.com/samsar/curio/internal/ui"
)

// mapped gives the run a built map: every group and assignment a place,
// each from its order, and every interest the others as similar
// interests, at most three.
func (f *runFixture) mapped() {
	f.c.Outcome.Map = &store.RunMap{Status: store.MapBuilt, Kind: store.RunKindWarm, Took: 2500 * time.Millisecond,
		Params: []byte(`{"seed":1}`), DotRadius: 1.5, Unsorted: store.Circle{X: 900, Y: 500, R: 60}}
	for i := range f.c.Groups {
		g := &f.c.Groups[i]
		x := float64(i)
		g.Map = &store.GroupMap{ZoomX: 100 + 20*x, ZoomY: 300, ZoomR: 10, AnchorX: 50 + x, AnchorY: 60 + x}
		if !slices.Contains(f.interests, g.ID) {
			continue
		}
		g.Similar = []store.SimilarInterest{}
		for _, other := range f.interests {
			if other != g.ID && len(g.Similar) < 3 {
				g.Similar = append(g.Similar, store.SimilarInterest{ID: other, Cosine: 0.5 - 0.01*float64(len(g.Similar))})
			}
		}
	}
	for i := range f.c.Assignments {
		x := float64(i)
		f.c.Assignments[i].Map = &store.MapPosition{MapX: 10 + x, MapY: 20 + x, ZoomX: 30 + x, ZoomY: 40 + x}
	}
}

// placeMapped places doc into run's interest ("" for Unsorted) at place on
// the map.
func (s *testServer) placeMapped(t *testing.T, run, interest string, doc *store.Document, place store.MapPosition) {
	t.Helper()
	placed, err := s.insights().PlaceDocument(context.Background(), "local", store.Placement{RunID: run, DocumentID: doc.ID,
		InterestID: interest, Similarity: 0.6, Map: &place})
	require.NoError(t, err)
	require.True(t, placed)
}

// mapPlace is a place on the document map at x, x and in the zoom view at
// zx, zy.
func mapPlace(x, zx, zy float64) store.MapPosition {
	return store.MapPosition{MapX: x, MapY: x, ZoomX: zx, ZoomY: zy}
}

// mapAt is the index of document id in the map's columns.
func mapAt(t *testing.T, resp InterestMapResponse, id string) int {
	t.Helper()
	for i, d := range resp.Documents.ID {
		if d == id {
			return i
		}
	}
	t.Fatalf("document %s is not on the map", id)
	return -1
}

// TestInterestMap: the latest rebuild's map, every column one value per
// document: members, a loose fit and an unsorted one as the rebuild put
// them (wherever their places are), documents placed since into an
// interest and into Unsorted as new, and none failed or dead since; each
// title by its fallbacks.
func TestInterestMap(t *testing.T) {
	s := newTestServer(t)
	d := s.docs(t, "map", 7)
	_, err := s.db.Exec(`UPDATE documents SET title = ? WHERE id = ?`, "  A   title\n on two lines ", d[0].ID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE documents SET title = ? WHERE id = ?`, strings.Repeat("é", 250), d[1].ID)
	require.NoError(t, err)
	_, err = s.deps.Bookmarks.Ingest(context.Background(), &store.Bookmark{TenantID: "local", URL: d[2].URL,
		Title: new("Saved as this"), Source: store.SourceChrome, SavedAt: time.Now().UTC()})
	require.NoError(t, err)

	f := s.newRun(t, store.InterestShapeAreas)
	area := f.area("Engineering")
	kafka := f.interest("Kafka", area, 0, []*store.Document{d[0], d[1], d[6]}, []*store.Document{d[2]})
	joins := f.interest("Joins", area, 0, d[3:4], nil)
	f.unsorted(kafka, d[4])
	f.unsorted("", d[5])
	f.mapped()
	run := f.commit(t)
	placed, placedUnsorted := s.docs(t, "placed", 1)[0], s.docs(t, "placed-unsorted", 1)[0]
	s.placeMapped(t, run, joins, placed, mapPlace(700, 142, 301))
	s.placeMapped(t, run, "", placedUnsorted, mapPlace(800, 905, 495))
	_, err = s.db.Exec(`UPDATE documents SET state = 'failed', failure_cause = 'other' WHERE id = ?`, d[5].ID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE documents SET state = 'dead', failure_cause = 'dead_link' WHERE id = ?`, d[6].ID)
	require.NoError(t, err)

	resp := getAs[InterestMapResponse](t, s, "/v1/interests/map")
	assert.Equal(t, run, resp.RunID)
	assert.Equal(t, "areas", resp.Shape)
	assert.Equal(t, InterestMapView{Kind: "warm", TookMS: 2500, Extent: 1000, DotRadius: 1.5,
		Unsorted: MapCircle{X: 900, Y: 500, R: 60}}, resp.Map)
	require.Len(t, resp.Areas, 1)
	assert.Equal(t, area, resp.Areas[0].ID)
	require.Len(t, resp.Interests, 2)
	assert.Equal(t, []string{kafka, joins}, []string{resp.Interests[0].ID, resp.Interests[1].ID}, "largest first")
	assert.Equal(t, 0, resp.Interests[0].Area)
	assert.Equal(t, []MapSimilar{{Interest: 1, Cosine: 0.5}}, resp.Interests[0].Similar)

	c := resp.Documents
	n := len(c.ID)
	assert.Equal(t, 7, n, "seven the rebuild assigned, less the failed and the dead one, and two placed")
	for _, col := range [][]any{anys(c.Title), anys(c.Host), anys(c.Interest), anys(c.Nearest), anys(c.Area), anys(c.Fit),
		anys(c.Similarity), anys(c.MX), anys(c.MY), anys(c.ZX), anys(c.ZY)} {
		assert.Len(t, col, n)
	}
	assert.ElementsMatch(t, []string{placed.ID, placedUnsorted.ID}, c.ID[n-2:], "the placed ones last")
	assert.True(t, c.ID[n-2] < c.ID[n-1], "by ID")
	for _, want := range []struct {
		doc               *store.Document
		fit               string
		interest, nearest int
		area              int
	}{
		{d[0], "member", 0, -1, 0},
		{d[2], "loose", 0, -1, 0},
		{d[3], "member", 1, -1, 0},
		{d[4], "unsorted", -1, 0, -1},
		{placed, "new", 1, -1, 0},
		{placedUnsorted, "new", -1, -1, -1},
	} {
		i := mapAt(t, resp, want.doc.ID)
		assert.Equal(t, want.fit, c.Fit[i], want.doc.URL)
		assert.Equal(t, want.interest, c.Interest[i], want.doc.URL)
		assert.Equal(t, want.nearest, c.Nearest[i], want.doc.URL)
		assert.Equal(t, want.area, c.Area[i], want.doc.URL)
	}
	assert.InDelta(t, 700, c.MX[mapAt(t, resp, placed.ID)], 0)
	assert.InDelta(t, 142, c.ZX[mapAt(t, resp, placed.ID)], 0)
	assert.Equal(t, "A title on two lines", c.Title[mapAt(t, resp, d[0].ID)])
	assert.Equal(t, strings.Repeat("é", 200), c.Title[mapAt(t, resp, d[1].ID)])
	assert.Equal(t, "Saved as this", c.Title[mapAt(t, resp, d[2].ID)], "the bookmark's title")
	assert.Equal(t, "example.com", c.Title[mapAt(t, resp, d[3].ID)], "the host")
	assert.Equal(t, "example.com", c.Host[mapAt(t, resp, d[3].ID)])
}

// TestInterestMap_LeavesOutDocumentsOffTheMap: a document 2.5.0 placed
// on a built map is left out of it, and the rest of the map is as it was:
// one placed with no place, by its index jobs or its sweep, and one moved
// into another interest or into Unsorted, which keeps a place outside its
// new circle.
func TestInterestMap_LeavesOutDocumentsOffTheMap(t *testing.T) {
	for name, tc := range map[string]struct {
		onMap bool // the document is on the map before 2.5.0 writes
		write func(t *testing.T, s *testServer, p store.Placement)
	}{
		"placed by 2.5.0's index job": {write: func(t *testing.T, s *testServer, p store.Placement) {
			require.True(t, curio250.PlaceDocument(t, s.db, "local", p))
		}},
		"placed by 2.5.0's sweep": {write: func(t *testing.T, s *testServer, p store.Placement) {
			p.InterestID = ""
			require.True(t, curio250.PlaceIfAbsent(t, s.db, p))
		}},
		"moved by 2.5.0 into another interest": {onMap: true, write: func(t *testing.T, s *testServer, p store.Placement) {
			require.True(t, curio250.PlaceDocument(t, s.db, "local", p))
		}},
		"moved by 2.5.0 into Unsorted": {onMap: true, write: func(t *testing.T, s *testServer, p store.Placement) {
			p.InterestID = ""
			require.True(t, curio250.PlaceDocument(t, s.db, "local", p))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t)
			d := s.docs(t, "map", 4)
			f := s.newRun(t, store.InterestShapeAreas)
			area := f.area("Engineering")
			kafka := f.interest("Kafka", area, 0, d[:2], nil)
			joins := f.interest("Joins", area, 0, d[2:3], nil)
			f.unsorted(kafka, d[3])
			f.mapped()
			run := f.commit(t)
			docs := s.docs(t, "placed", 3)
			s.placeMapped(t, run, kafka, docs[0], mapPlace(700, 121, 301))
			s.placeMapped(t, run, "", docs[1], mapPlace(800, 905, 495))
			off := docs[2]
			if tc.onMap {
				s.placeMapped(t, run, kafka, off, mapPlace(750, 122, 302))
			}
			before := getAs[InterestMapResponse](t, s, "/v1/interests/map")
			want := before
			if tc.onMap {
				want = without(t, before, off.ID)
			}

			tc.write(t, s, store.Placement{RunID: run, DocumentID: off.ID, InterestID: joins, Similarity: 0.6})
			got := getAs[InterestMapResponse](t, s, "/v1/interests/map")
			assert.Equal(t, want, got)
			assert.NotContains(t, got.Documents.ID, off.ID)
			assert.Len(t, got.Documents.ID, 6, "the four the rebuild assigned and two placed on the map")
		})
	}
}

// without is resp without document id.
func without(t *testing.T, resp InterestMapResponse, id string) InterestMapResponse {
	t.Helper()
	i := mapAt(t, resp, id)
	c := &resp.Documents
	c.ID, c.Title, c.Host, c.Fit = drop(c.ID, i), drop(c.Title, i), drop(c.Host, i), drop(c.Fit, i)
	c.Interest, c.Nearest, c.Area = drop(c.Interest, i), drop(c.Nearest, i), drop(c.Area, i)
	c.Similarity, c.MX, c.MY, c.ZX, c.ZY = drop(c.Similarity, i), drop(c.MX, i), drop(c.MY, i), drop(c.ZX, i), drop(c.ZY, i)
	return resp
}

// drop is xs without its i-th element, in a new slice.
func drop[T any](xs []T, i int) []T { return slices.Delete(slices.Clone(xs), i, i+1) }

// TestMapResponse_Inconsistencies: a document, with a place on the map or
// without, in an interest the run lacks, and an interest similar to one
// the run lacks, are errors, not documents left out.
func TestMapResponse_Inconsistencies(t *testing.T) {
	finished := time.Now()
	run := &store.InterestRun{ID: "run", FinishedAt: &finished, RunOutcome: store.RunOutcome{Shape: store.InterestShapeFlat,
		Map: &store.RunMap{Status: store.MapBuilt, Kind: store.RunKindFresh, DotRadius: 1,
			Unsorted: store.Circle{X: 900, Y: 500, R: 60}}}}
	place := &store.GroupMap{ZoomX: 100, ZoomY: 100, ZoomR: 10, AnchorX: 1, AnchorY: 1}
	kafka := store.InterestGroup{Interest: store.Interest{ID: "kafka", Level: store.InterestLevelInterest}, Size: 1, Map: place}
	for name, tc := range map[string]struct {
		groups []store.InterestGroup
		docs   []store.MapDocument
		err    string
	}{
		"a document in an interest the run lacks": {groups: []store.InterestGroup{kafka},
			docs: []store.MapDocument{{DocumentID: "d", Placed: true, InterestID: "joins",
				Map: &store.MapPosition{ZoomX: 100, ZoomY: 100}}},
			err: "document d: interest joins isn't one of the run's"},
		"one without a place": {groups: []store.InterestGroup{kafka},
			docs: []store.MapDocument{{DocumentID: "d", Placed: true, InterestID: "joins"}},
			err:  "document d: interest joins isn't one of the run's"},
		"a similar interest the run lacks": {groups: []store.InterestGroup{{Interest: kafka.Interest, Size: 1, Map: place,
			Similar: []store.SimilarInterest{{ID: "joins", Cosine: 0.5}}}},
			err: "interest kafka of run run is similar to joins, which the run lacks"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := mapResponse(run, tc.groups, tc.docs)
			require.EqualError(t, err, tc.err)
		})
	}
}

// TestMapTitle_FallsBackToTheURL: a document with no title, no bookmark
// title and no host is named by its URL.
func TestMapTitle_FallsBackToTheURL(t *testing.T) {
	const u = "file:///Users/me/notes.html"
	assert.Equal(t, u, mapTitle(store.MapDocument{URL: u}, ui.Host(u)))
}

func anys[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// TestInterestMap_Flat: a flat run's map has no areas, and every
// document's area is -1.
func TestInterestMap_Flat(t *testing.T) {
	s := newTestServer(t)
	d := s.docs(t, "flat", 3)
	f := s.newRun(t, store.InterestShapeFlat)
	f.interest("Kafka", "", 0, d[:2], nil)
	f.unsorted("", d[2])
	f.mapped()
	f.commit(t)
	resp := getAs[InterestMapResponse](t, s, "/v1/interests/map")
	assert.Empty(t, resp.Areas)
	assert.NotNil(t, resp.Areas, "an empty list, never null")
	require.Len(t, resp.Interests, 1)
	assert.Equal(t, -1, resp.Interests[0].Area)
	assert.Empty(t, resp.Interests[0].Similar)
	assert.Equal(t, []int{-1, -1, -1}, resp.Documents.Area)
}

// mapProblem is the map endpoint's 404, which says why there is no map.
func mapProblem(t *testing.T, s *testServer) InterestMapUnavailable {
	t.Helper()
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests/map"})
	require.Equal(t, http.StatusNotFound, resp.status, resp.body)
	assert.Equal(t, "application/problem+json", resp.contentType)
	var p InterestMapUnavailable
	require.NoError(t, json.Unmarshal([]byte(resp.body), &p))
	assert.Equal(t, InterestMapProblemType, p.Type)
	assert.Equal(t, "interest map unavailable", p.Title)
	assert.Equal(t, http.StatusNotFound, p.Status)
	assert.Equal(t, "/v1/interests/map", p.Instance)
	return p
}

// TestInterestMap_Unavailable: without a map the endpoint answers 404 with
// why: no rebuild yet, one that drew no map, or one whose map failed.
func TestInterestMap_Unavailable(t *testing.T) {
	s := newTestServer(t)
	problem := func() InterestMapUnavailable {
		t.Helper()
		return mapProblem(t, s)
	}
	p := problem()
	assert.Equal(t, "no_run", p.Reason)
	assert.Empty(t, p.RunID)
	assert.Contains(t, p.Detail, "the first one draws the map")

	d := s.docs(t, "u", 2)
	f := s.newRun(t, store.InterestShapeFlat)
	f.interest("Kafka", "", 0, d, nil)
	before := f.commit(t)
	p = problem()
	assert.Equal(t, "no_map", p.Reason)
	assert.Equal(t, before, p.RunID)
	assert.Equal(t, "the latest rebuild drew no map: a rebuild to draw it is due, "+
		"and `curio interests rebuild` draws it now", p.Detail)

	f = s.newRun(t, store.InterestShapeFlat)
	f.interest("Kafka, again", "", 0, d, nil)
	f.c.Outcome.Map = &store.RunMap{Status: store.MapFailed, Error: "the map took longer than 2m0s", Params: []byte(`{}`)}
	failed := f.commit(t)
	p = problem()
	assert.Equal(t, "map_failed", p.Reason)
	assert.Equal(t, failed, p.RunID)
	assert.Equal(t, "the map took longer than 2m0s", p.MapError)
	assert.Equal(t, "the latest rebuild's map failed (the map took longer than 2m0s): the next rebuild, "+
		"once enough of the library changes, or `curio interests rebuild`, draws it again", p.Detail)
}

// unreadableInsights is an insight store that can't be read: any call
// panics, which the server answers 500.
type unreadableInsights struct{ store.InsightStore }

// TestInterestMap_Off: with the map off, the endpoint answers 404 map_off,
// naming the setting that turns it on, and reads nothing.
func TestInterestMap_Off(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.MapOff, d.Insights = true, unreadableInsights{} })
	p := mapProblem(t, s)
	assert.Equal(t, "map_off", p.Reason)
	assert.Empty(t, p.RunID)
	assert.Empty(t, p.MapError)
	assert.Equal(t, "the interest map is off (insight.map: false in config.yaml): remove the setting, "+
		"or set it to true, and restart the daemon", p.Detail)
}

// rebuildMidRead is an insight store whose latest done run changes between
// reads, as rebuilds that commit while a map is read leave it.
type rebuildMidRead struct {
	store.InsightStore
	runs  []string // the runs LatestRun answers, in turn, the last for ever after
	reads int
}

func (r *rebuildMidRead) LatestRun(ctx context.Context, _ string, _ store.InterestRunStatus) (*store.InterestRun, error) {
	id := r.runs[min(r.reads, len(r.runs)-1)]
	r.reads++
	return r.GetRun(ctx, id)
}

// TestInterestMap_RereadsAcrossARebuild: a rebuild that commits while the
// map is read is answered from the newer run, read once more; one that
// changes it again is answered as read.
func TestInterestMap_RereadsAcrossARebuild(t *testing.T) {
	s := newTestServer(t)
	d := s.docs(t, "r", 3)
	runs := make([]string, 0, 3)
	for i := range 3 {
		f := s.newRun(t, store.InterestShapeFlat)
		f.interest("Kafka", "", 0, d[i:i+1], nil)
		f.mapped()
		runs = append(runs, f.c.RunID)
		ctx := context.Background()
		f.c.Outcome.NumDocuments = 1
		require.NoError(t, s.insights().CommitRun(ctx, f.c))
	}
	// Read run 0, then find run 1: read run 1, and find it still latest.
	s.deps.Insights = &rebuildMidRead{InsightStore: s.insights(), runs: []string{runs[0], runs[1]}}
	resp, err := s.deps.interestMap(context.Background())
	require.NoError(t, err)
	assert.Equal(t, runs[1], resp.RunID)
	assert.Equal(t, []string{d[1].ID}, resp.Documents.ID)

	// Read run 0, find run 1, read it, find run 2: answered as read.
	s.deps.Insights = &rebuildMidRead{InsightStore: s.insights(), runs: []string{runs[0], runs[1], runs[1], runs[2]}}
	resp, err = s.deps.interestMap(context.Background())
	require.NoError(t, err)
	assert.Equal(t, runs[1], resp.RunID)
}
