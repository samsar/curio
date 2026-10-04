package insight

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest/curio250"
)

// placedInto returns where run placed each document: its interest, "" for
// Unsorted, with the similarity.
func (f *engineFixture) placedInto(t *testing.T, runID string) map[string]store.Placement {
	t.Helper()
	rows := map[string]store.Placement{}
	r, err := f.db.Query(`SELECT document_id, coalesce(interest_id, ''), similarity FROM interest_placements
		WHERE run_id = ?`, runID)
	require.NoError(t, err)
	defer r.Close()
	for r.Next() {
		var p store.Placement
		require.NoError(t, r.Scan(&p.DocumentID, &p.InterestID, &p.Similarity))
		rows[p.DocumentID] = p
	}
	require.NoError(t, r.Err())
	return rows
}

// addVector creates a document indexed now with vector v and returns its
// ID.
func (f *engineFixture) addVector(t *testing.T, v []float32) string {
	t.Helper()
	ids := f.add(t, 0, 1)
	f.vectors.dvs[len(f.vectors.dvs)-1].Vector = v
	return ids[0]
}

// TestPlacer_Place: a document indexed since the run joins its nearest
// interest at LooseFitThreshold or above and waits in Unsorted below,
// with its cosine either way; one the run assigned, or one without
// vectors, is left alone.
func TestPlacer_Place(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 4, 0) // axis 2 is no interest's
	run := f.rebuild(t, f.engine(nil, nil, Config{}))
	axis1 := f.groups(t, run.ID)[4].ID
	p := f.placer()

	joined := f.addVector(t, []float32{0, 1, 0})
	loose := f.addVector(t, []float32{0, 0.5, 1})  // cos 0.447 to axis 1
	strays := f.addVector(t, []float32{0, 0.6, 1}) // cos 0.514 to axis 1
	unsorted := f.addVector(t, []float32{0, 0, 1})
	for _, id := range []string{joined, loose, strays, unsorted, f.vectorIDs()[0]} {
		p.Place(ctx, tenant, id)
	}
	bare := f.add(t, 1, 1)[0]
	f.vectors.dvs = f.vectors.dvs[:len(f.vectors.dvs)-1]
	p.Place(ctx, tenant, bare)

	got := f.placedInto(t, run.ID)
	require.Len(t, got, 4, "the assigned document and the one without vectors are left alone")
	assert.Equal(t, axis1, got[joined].InterestID)
	assert.InDelta(t, 1, got[joined].Similarity, 1e-6)
	assert.Empty(t, got[loose].InterestID, "under the threshold: Unsorted")
	assert.InDelta(t, 0.5/math.Sqrt(1.25), got[loose].Similarity, 1e-6, "with the cosine to the nearest")
	assert.Equal(t, axis1, got[strays].InterestID)
	assert.Empty(t, got[unsorted].InterestID)
	assert.Empty(t, f.lines("level=WARN"), "nothing warned")
}

// TestPlacer_Holds: no placement while the embeddings drifted, while a
// re-embedding's fresh rebuild is owed, or before a done run; a manual
// fresh rebuild owed doesn't hold it.
func TestPlacer_Holds(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 4)
	early := f.add(t, 0, 1)[0]
	f.placer().Place(ctx, tenant, early)
	_, err := f.store.LatestRun(ctx, tenant, "")
	require.ErrorIs(t, err, store.ErrNotFound, "no run, nothing placed")

	run := f.rebuild(t, f.engine(nil, nil, Config{}))
	doc := f.add(t, 0, 1)[0]
	drifted := NewPlacer(f.insights, f.vectors, PlacerOptions{Drift: func() string { return "the embeddings drifted" }})
	drifted.Place(ctx, tenant, doc)
	assert.Empty(t, f.placedInto(t, run.ID), "held while the embeddings drifted")

	require.NoError(t, f.store.OweFresh(ctx, tenant, store.FreshReindex))
	f.placer().Place(ctx, tenant, doc)
	assert.Empty(t, f.placedInto(t, run.ID), "held while a re-embedding's fresh rebuild is owed")
	placed, err := f.placer().Sweep(ctx, tenant)
	require.NoError(t, err)
	assert.Zero(t, placed, "the sweep too")

	_, err = f.db.Exec(`UPDATE insight_state SET fresh_owed = 'manual'`)
	require.NoError(t, err)
	f.placer().Place(ctx, tenant, doc)
	assert.Len(t, f.placedInto(t, run.ID), 1, "a manual fresh rebuild owed holds nothing")
}

// faultyPlacements fails or panics on placement writes.
type faultyPlacements struct {
	*faultyInsights
	err   error
	panic bool
}

func (f *faultyPlacements) PlaceDocument(ctx context.Context, tenantID string, p store.Placement) (bool, error) {
	if f.panic {
		panic("placement write")
	}
	if f.err != nil {
		return false, f.err
	}
	return f.faultyInsights.PlaceDocument(ctx, tenantID, p)
}

// TestPlacer_FailuresWarnOncePerRun: a failed placement, a panic included,
// warns once for a run and logs the rest at debug; the next run warns
// again.
func TestPlacer_FailuresWarnOncePerRun(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 4)
	e := f.engine(nil, nil, Config{})
	first := f.rebuild(t, e)
	faulty := &faultyPlacements{faultyInsights: f.insights, err: errLocked}
	p := NewPlacer(faulty, f.vectors, PlacerOptions{Log: f.debugLog()})
	p.Place(ctx, tenant, f.add(t, 0, 1)[0])
	faulty.err, faulty.panic = nil, true
	p.Place(ctx, tenant, f.add(t, 1, 1)[0])
	warnings := f.lines("level=WARN")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "interests: placement failed")
	assert.Contains(t, warnings[0], "run="+first.ID)
	assert.Contains(t, warnings[0], "database is locked")
	debug := f.lines(`level=DEBUG msg="interests: placement failed"`)
	require.Len(t, debug, 1, "the panic, after the warning")
	assert.Contains(t, debug[0], "panic: placement write")

	second := f.rebuild(t, e)
	p.Place(ctx, tenant, f.add(t, 0, 1)[0])
	warnings = f.lines("level=WARN")
	require.Len(t, warnings, 2, "a new run warns again")
	assert.Contains(t, warnings[1], "run="+second.ID)
}

// debugLog is a logger to the fixture's logs, from debug up.
func (f *engineFixture) debugLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(&f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// lines are the fixture's log lines containing s.
func (f *engineFixture) lines(s string) []string {
	var out []string
	for line := range strings.Lines(f.logs.String()) {
		if strings.Contains(line, s) {
			out = append(out, line)
		}
	}
	return out
}

// TestPlacer_ReadsANewRunsSpace: the space is cached per run, and read anew
// once a newer run is done, here under placements running at once.
func TestPlacer_ReadsANewRunsSpace(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 4)
	p := f.placer()
	first := f.rebuild(t, f.engine(nil, nil, Config{}))
	docs := make([]string, 0, 16)
	for i := range 16 {
		docs = append(docs, f.add(t, i%2, 1)[0])
	}
	var wg sync.WaitGroup
	for _, id := range docs[:8] {
		wg.Go(func() { p.Place(ctx, tenant, id) })
	}
	wg.Wait()
	assert.Len(t, f.placedInto(t, first.ID), 8)

	second := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	newer := slices.Concat(docs[8:], f.add(t, 0, 4))
	for _, id := range newer {
		wg.Go(func() { p.Place(ctx, tenant, id) })
	}
	wg.Wait()
	got := f.placedInto(t, second.ID)
	axis0 := f.groups(t, second.ID)[3+4+4].ID
	for _, id := range f.vectorIDs()[len(f.vectorIDs())-4:] {
		assert.Equal(t, axis0, got[id].InterestID, "placed in the new run's space, with its interests")
	}
	assert.Len(t, got, 4, "the documents the new run grouped are its own")
}

// TestPlacer_Sweep: the sweep places what the run neither assigned nor
// placed, keeps a placement made by the fast path, and leaves out a
// document it can't place, with a warning, rather than stall the rest.
func TestPlacer_Sweep(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3, 4)
	run := f.rebuild(t, f.engine(nil, nil, Config{}))
	axis0 := f.groups(t, run.ID)[3]
	p := f.placer()
	fast := f.add(t, 1, 1)[0]
	p.Place(ctx, tenant, fast)
	// Moved into axis 0's interest, on the map: at its circle's centre.
	_, err := f.db.Exec(`UPDATE interest_placements SET interest_id = ?, zoom_x = ?, zoom_y = ? WHERE document_id = ?`,
		axis0.ID, axis0.Map.ZoomX, axis0.Map.ZoomY, fast)
	require.NoError(t, err)
	swept := f.add(t, 0, 2)
	broken := f.addVector(t, []float32{float32(math.NaN()), 1})

	placed, err := p.Sweep(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, 2, placed)
	got := f.placedInto(t, run.ID)
	assert.Equal(t, axis0.ID, got[fast].InterestID, "the fast path's placement stands")
	for _, id := range swept {
		assert.Equal(t, axis0.ID, got[id].InterestID)
	}
	assert.NotContains(t, got, broken)
	warning := f.lines("level=WARN")
	require.Len(t, warning, 1)
	assert.Contains(t, warning[0], "the sweep left out documents it can't place")
	assert.Contains(t, warning[0], broken)

	again, err := p.Sweep(ctx, tenant)
	require.NoError(t, err)
	assert.Zero(t, again)
}

// insideCircle reports whether a dot of radius dot at x, y lies inside c,
// what rounding leaves aside.
func insideCircle(x, y, dot float64, c store.Circle) bool {
	return math.Hypot(x-c.X, y-c.Y)+dot <= c.R+0.02
}

// circleOf is interest id's zoom circle in run.
func (f *engineFixture) circleOf(t *testing.T, runID, id string) store.Circle {
	t.Helper()
	gs, err := f.store.RunGroups(context.Background(), runID)
	require.NoError(t, err)
	for _, g := range gs {
		if g.ID == id {
			require.NotNil(t, g.Map)
			return store.Circle{X: g.Map.ZoomX, Y: g.Map.ZoomY, R: g.Map.ZoomR}
		}
	}
	t.Fatalf("run %s has no interest %s", runID, id)
	return store.Circle{}
}

// placeOf is document id's placement in run, with its place on the map.
func (f *engineFixture) placeOf(t *testing.T, runID, id string) store.MapPlace {
	t.Helper()
	got, err := f.store.MapPositions(context.Background(), runID, []string{id})
	require.NoError(t, err)
	require.Len(t, got, 1, "document %s has a place", id)
	return got[0]
}

// TestPlacer_PlacesOnTheMap: a document placed into a run with a built map
// gets a place on both views: one whose vector is a mapped member's sits
// within 1% of the map of it, inside its interest's circle; one far from
// every interest inside Unsorted's disc; and placing it again puts it in
// the same place.
func TestPlacer_PlacesOnTheMap(t *testing.T) {
	ctx := context.Background()
	f := mapLibrary(t, "d")
	run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	as, err := f.store.RunAssignments(ctx, run.ID)
	require.NoError(t, err)
	var member store.InterestAssignment
	for _, a := range as {
		if a.Fit == store.InterestFitMember {
			member = a
			break
		}
	}
	var memberVec []float32
	for _, dv := range f.vectors.dvs {
		if dv.DocumentID == member.DocumentID {
			memberVec = slices.Clone(dv.Vector)
		}
	}
	p := f.placer()
	twin := f.addVector(t, memberVec)
	p.Place(ctx, tenant, twin)
	got := f.placeOf(t, run.ID, twin)
	assert.Equal(t, member.InterestID, got.InterestID)
	assert.LessOrEqual(t, math.Hypot(got.Map.MapX-member.Map.MapX, got.Map.MapY-member.Map.MapY), 0.01*store.MapExtent)
	assert.True(t, insideCircle(got.Map.ZoomX, got.Map.ZoomY, run.Map.DotRadius, f.circleOf(t, run.ID, member.InterestID)))

	far := make([]float32, mapDims)
	far[mapDims-1] = 1
	stray := f.addVector(t, far)
	p.Place(ctx, tenant, stray)
	got = f.placeOf(t, run.ID, stray)
	assert.Empty(t, got.InterestID, "into Unsorted")
	assert.True(t, insideCircle(got.Map.ZoomX, got.Map.ZoomY, run.Map.DotRadius, run.Map.Unsorted))

	p.Place(ctx, tenant, stray)
	assert.Equal(t, got, f.placeOf(t, run.ID, stray), "placed again, the same place")
	assert.Empty(t, f.lines("level=WARN"))
}

// TestPlacer_NoMapNoPlace: a placement into a run whose map failed, or one
// from before maps, has no place on the map.
func TestPlacer_NoMapNoPlace(t *testing.T) {
	ctx := context.Background()
	f := mapLibrary(t, "d")
	failing := func(context.Context, MapInput) (*Map, error) { return nil, errors.New("no room") }
	run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}).WithMapper(failing))
	doc := f.addAround(t, "d", 0, 1, rand.New(rand.NewPCG(1, 1)))[0]
	f.placer().Place(ctx, tenant, doc)
	assert.Contains(t, f.placedInto(t, run.ID), doc, "placed")
	got, err := f.store.MapPositions(ctx, run.ID, []string{doc})
	require.NoError(t, err)
	assert.Empty(t, got, "placed without a place")

	run = f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	_, err = f.db.Exec(`UPDATE interest_runs SET map_status = NULL, map_kind = NULL, map_error = NULL, map_ms = NULL,
		map_params = NULL, map_dot_radius = NULL, map_unsorted_x = NULL, map_unsorted_y = NULL, map_unsorted_r = NULL`)
	require.NoError(t, err)
	doc = f.addAround(t, "d", 1, 1, rand.New(rand.NewPCG(2, 2)))[0]
	f.placer().Place(ctx, tenant, doc)
	assert.Contains(t, f.placedInto(t, run.ID), doc, "placed")
	got, err = f.store.MapPositions(ctx, run.ID, []string{doc})
	require.NoError(t, err)
	assert.Empty(t, got, "a run without a map")
}

// TestPlacer_PlaceFallsBack: when the neighbour search fails, the
// placement is still written, at the fallback place inside its circle,
// and the run warns once.
func TestPlacer_PlaceFallsBack(t *testing.T) {
	ctx := context.Background()
	f := mapLibrary(t, "d")
	run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	f.vectors.search = func(context.Context) error { return errLocked }
	p := f.placer()
	docs := f.addAround(t, "d", 0, 2, rand.New(rand.NewPCG(3, 3)))
	for _, doc := range docs {
		p.Place(ctx, tenant, doc)
	}
	for _, doc := range docs {
		got := f.placeOf(t, run.ID, doc)
		require.NotEmpty(t, got.InterestID)
		assert.True(t, insideCircle(got.Map.ZoomX, got.Map.ZoomY, run.Map.DotRadius, f.circleOf(t, run.ID, got.InterestID)))
		assert.True(t, got.Map.MapX >= 0 && got.Map.MapX <= store.MapExtent && got.Map.MapY >= 0 && got.Map.MapY <= store.MapExtent)
	}
	warnings := f.lines("level=WARN")
	require.Len(t, warnings, 1, "one warning a run")
	assert.Contains(t, warnings[0], "fell back")
	assert.Contains(t, warnings[0], "database is locked")
}

// TestPlacer_StalledSearchFallsBack: a neighbour search that stalls gives
// up after its own timeout, short of the placement's, so the placement is
// still written, at the fallback place, and the run warns once.
func TestPlacer_StalledSearchFallsBack(t *testing.T) {
	ctx := context.Background()
	f := mapLibrary(t, "d")
	run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	f.vectors.search = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	doc := f.addAround(t, "d", 0, 1, rand.New(rand.NewPCG(5, 5)))[0]
	f.placer().WithNeighbourTimeout(20*time.Millisecond).Place(ctx, tenant, doc)
	got := f.placeOf(t, run.ID, doc)
	assert.True(t, insideCircle(got.Map.ZoomX, got.Map.ZoomY, run.Map.DotRadius, f.circleOf(t, run.ID, got.InterestID)))
	warnings := f.lines("level=WARN")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "fell back")
	assert.Contains(t, warnings[0], "deadline exceeded")
}

// TestPlacer_CancelledSearchIsQuiet: a search that fails because the
// placement's own context ended, as when the daemon stops mid-index,
// warns about nothing: the placement reports its cancellation at debug.
func TestPlacer_CancelledSearchIsQuiet(t *testing.T) {
	f := mapLibrary(t, "d")
	f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	ctx, cancel := context.WithCancel(context.Background())
	f.vectors.search = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}
	doc := f.addAround(t, "d", 0, 1, rand.New(rand.NewPCG(6, 6)))[0]
	NewPlacer(f.insights, f.vectors, PlacerOptions{Log: f.debugLog()}).Place(ctx, tenant, doc)
	assert.Empty(t, f.lines("level=WARN"))
	assert.Len(t, f.lines("fell back"), 1, "at debug")
	assert.Len(t, f.lines("placement cancelled"), 1)
}

// TestPlacer_SweepPastItsBudget: a sweep that has spent its neighbour
// budget still places every document, at the fallback place: on the
// document map, by its interest's anchor.
func TestPlacer_SweepPastItsBudget(t *testing.T) {
	ctx := context.Background()
	f := mapLibrary(t, "d")
	run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	docs := f.addAround(t, "d", 2, 3, rand.New(rand.NewPCG(4, 4)))
	placed, err := f.placer().WithNeighbourBudget(0).Sweep(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, 3, placed)
	gs, err := f.store.RunGroups(ctx, run.ID)
	require.NoError(t, err)
	anchors := map[string][2]float64{}
	for _, g := range gs {
		anchors[g.ID] = [2]float64{g.Map.AnchorX, g.Map.AnchorY}
	}
	for _, doc := range docs {
		got := f.placeOf(t, run.ID, doc)
		a := anchors[got.InterestID]
		assert.InDelta(t, placeOffset*store.MapExtent, math.Hypot(got.Map.MapX-a[0], got.Map.MapY-a[1]), 0.02,
			"%s sits by its interest's anchor", doc)
		assert.True(t, insideCircle(got.Map.ZoomX, got.Map.ZoomY, run.Map.DotRadius, f.circleOf(t, run.ID, got.InterestID)))
	}
}

// circleIn is where placement p of run should sit in the zoom view: its
// interest's circle, or Unsorted's disc.
func (f *engineFixture) circleIn(t *testing.T, run *store.InterestRun, p store.MapPlace) store.Circle {
	t.Helper()
	if p.InterestID == "" {
		return run.Map.Unsorted
	}
	return f.circleOf(t, run.ID, p.InterestID)
}

// TestPlacer_SweepRepairsWhat250Left: a placement 2.5.0 left on a built
// map, with no place or with one outside its new circle, is placed again
// by one sweep, which counts it, inside its circle; a second sweep writes
// nothing.
func TestPlacer_SweepRepairsWhat250Left(t *testing.T) {
	ctx := context.Background()
	for name, leave := range map[string]func(t *testing.T, f *engineFixture, p store.Placement){
		"placed by 2.5.0's index job": func(t *testing.T, f *engineFixture, p store.Placement) {
			require.True(t, curio250.PlaceDocument(t, f.db, tenant, p))
		},
		"placed by 2.5.0's sweep": func(t *testing.T, f *engineFixture, p store.Placement) {
			p.InterestID = ""
			require.True(t, curio250.PlaceIfAbsent(t, f.db, p))
		},
		"moved by 2.5.0 into another interest": func(t *testing.T, f *engineFixture, p store.Placement) {
			f.placer().Place(ctx, tenant, p.DocumentID)
			require.NotEqual(t, p.InterestID, f.placeOf(t, p.RunID, p.DocumentID).InterestID)
			require.True(t, curio250.PlaceDocument(t, f.db, tenant, p))
		},
		"moved by 2.5.0 into Unsorted": func(t *testing.T, f *engineFixture, p store.Placement) {
			f.placer().Place(ctx, tenant, p.DocumentID)
			p.InterestID = ""
			require.True(t, curio250.PlaceDocument(t, f.db, tenant, p))
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := mapLibrary(t, "d")
			run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
			doc := f.addAround(t, "d", 0, 1, rand.New(rand.NewPCG(7, 7)))[0]
			// Axis 3's interest, the smallest: never axis 0's.
			other := f.groups(t, run.ID)[12].ID
			leave(t, f, store.Placement{RunID: run.ID, DocumentID: doc, InterestID: other, Similarity: 0.6})

			placed, err := f.placer().Sweep(ctx, tenant)
			require.NoError(t, err)
			assert.Equal(t, 1, placed)
			got := f.placeOf(t, run.ID, doc)
			assert.NotEqual(t, other, got.InterestID, "placed where the placer puts it")
			assert.True(t, insideCircle(got.Map.ZoomX, got.Map.ZoomY, run.Map.DotRadius, f.circleIn(t, run, got)))
			again, err := f.placer().Sweep(ctx, tenant)
			require.NoError(t, err)
			assert.Zero(t, again)
			assert.Empty(t, f.lines("level=WARN"))
		})
	}
}

// TestPlacer_NoChurn: what the placer itself places on a built map, by
// the fast path or a sweep, into interests and into Unsorted, is never
// placed again by a sweep.
func TestPlacer_NoChurn(t *testing.T) {
	ctx := context.Background()
	f := mapLibrary(t, "d")
	run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	r := rand.New(rand.NewPCG(8, 8))
	p := f.placer()
	for g := range 4 {
		for _, doc := range f.addAround(t, "fast", g, 3, r) {
			p.Place(ctx, tenant, doc)
		}
	}
	far := make([]float32, mapDims)
	far[mapDims-1] = 1
	p.Place(ctx, tenant, f.addVector(t, far))
	for g := range 4 {
		f.addAround(t, "swept", g, 3, r)
	}
	f.addVector(t, far)
	placed, err := p.Sweep(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, 13, placed)
	counts, err := f.store.PlacementCounts(ctx, run.ID)
	require.NoError(t, err)
	assert.Positive(t, counts[""], "some in Unsorted")

	again, err := p.Sweep(ctx, tenant)
	require.NoError(t, err)
	assert.Zero(t, again)
}

// onRead is a chunk store that runs its hook, at, when it reads the
// vectors of the document after the first n, and fails that read with
// at's error unless it is nil.
type onRead struct {
	*vectorSource
	n     int
	reads int
	at    func(ctx context.Context) error
}

func (o *onRead) EmbeddingsForDocument(ctx context.Context, documentID string) ([]store.ChunkEmbedding, error) {
	if o.reads++; o.reads == o.n+1 {
		if err := o.at(ctx); err != nil {
			return nil, err
		}
	}
	return o.vectorSource.EmbeddingsForDocument(ctx, documentID)
}

// failingPlaceMany is an insight store whose PlaceMany fails from its
// call after the first n.
type failingPlaceMany struct {
	store.InsightStore
	n int
}

func (f *failingPlaceMany) PlaceMany(ctx context.Context, tenantID, runID string, ps []store.Placement) (int, error) {
	if f.n == 0 {
		return 0, errLocked
	}
	f.n--
	return f.InsightStore.PlaceMany(ctx, tenantID, runID, ps)
}

// TestPlacer_SweepWritesInBatches: a sweep writes its placements a batch
// at a time, every one in the end; one whose context ends, or whose write
// fails, keeps the batches written before and returns their count with
// the error, and the next sweep places the rest.
func TestPlacer_SweepWritesInBatches(t *testing.T) {
	setup := func(t *testing.T) (*engineFixture, *store.InterestRun) {
		t.Helper()
		f := mapLibrary(t, "d")
		run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
		f.addAround(t, "new", 1, 7, rand.New(rand.NewPCG(9, 9)))
		return f, run
	}
	t.Run("every one", func(t *testing.T) {
		f, run := setup(t)
		placed, err := f.placer().WithSweepBatch(2).Sweep(context.Background(), tenant)
		require.NoError(t, err)
		assert.Equal(t, 7, placed)
		assert.Len(t, f.placedInto(t, run.ID), 7)
	})
	t.Run("the context ends", func(t *testing.T) {
		f, run := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		chunks := &onRead{vectorSource: f.vectors, n: 5, at: func(ctx context.Context) error {
			cancel()
			return ctx.Err()
		}}
		p := NewPlacer(f.insights, chunks, PlacerOptions{Log: f.log()}).WithSweepBatch(2)
		placed, err := p.Sweep(ctx, tenant)
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 4, placed, "two batches of two; the fifth document's was never written")
		assert.Len(t, f.placedInto(t, run.ID), 4)

		placed, err = f.placer().Sweep(context.Background(), tenant)
		require.NoError(t, err)
		assert.Equal(t, 3, placed, "the next sweep places the rest")
	})
	t.Run("a write fails", func(t *testing.T) {
		f, run := setup(t)
		insights := &failingPlaceMany{InsightStore: f.insights, n: 1}
		p := NewPlacer(insights, f.vectors, PlacerOptions{Log: f.log()}).WithSweepBatch(3)
		placed, err := p.Sweep(context.Background(), tenant)
		require.ErrorIs(t, err, errLocked)
		assert.Equal(t, 3, placed, "the first batch")
		assert.Len(t, f.placedInto(t, run.ID), 3)
	})
	t.Run("a rebuild commits meanwhile", func(t *testing.T) {
		f, _ := setup(t)
		chunks := &onRead{vectorSource: f.vectors, n: 4, at: func(context.Context) error {
			f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
			return nil
		}}
		p := NewPlacer(f.insights, chunks, PlacerOptions{Log: f.log()}).WithSweepBatch(2)
		placed, err := p.Sweep(context.Background(), tenant)
		require.NoError(t, err)
		assert.Equal(t, 4, placed, "the batches after the commit write nothing into the run it replaced")
	})
}

// countingSearches is a chunk store that counts its vector searches.
type countingSearches struct {
	*vectorSource
	searches int
}

func (c *countingSearches) VectorSearch(ctx context.Context, tenantID string, q []float32, limit int, f store.SearchFilters) ([]store.ChunkHit, error) {
	c.searches++
	return c.vectorSource.VectorSearch(ctx, tenantID, q, limit, f)
}

// countingPositions is an insight store that counts its reads of places on
// the map.
type countingPositions struct {
	store.InsightStore
	reads int
}

func (c *countingPositions) MapPositions(ctx context.Context, runID string, ids []string) ([]store.MapPlace, error) {
	c.reads++
	return c.InsightStore.MapPositions(ctx, runID, ids)
}

// TestPlacer_MapOff: with the map off, the placer places documents into
// a run whose map is built without a place on it, searching for no
// neighbours and reading no places; once the map is on again, a sweep
// gives each its place, inside its circle.
func TestPlacer_MapOff(t *testing.T) {
	ctx := context.Background()
	f := mapLibrary(t, "d")
	run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	require.Equal(t, store.MapBuilt, run.Map.Status)
	chunks := &countingSearches{vectorSource: f.vectors}
	insights := &countingPositions{InsightStore: f.insights}
	off := NewPlacer(insights, chunks, PlacerOptions{MapOff: true, Log: f.log()})
	r := rand.New(rand.NewPCG(10, 10))
	docs := f.addAround(t, "d", 0, 1, r)
	off.Place(ctx, tenant, docs[0])
	docs = append(docs, f.addAround(t, "d", 1, 2, r)...)
	placed, err := off.Sweep(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, 2, placed)
	again, err := off.Sweep(ctx, tenant)
	require.NoError(t, err)
	assert.Zero(t, again, "placing without a place repairs nothing")
	assert.Zero(t, chunks.searches, "no neighbour searched")
	assert.Zero(t, insights.reads, "no place read")
	assert.Len(t, f.placedInto(t, run.ID), 3)
	none, err := f.store.MapPositions(ctx, run.ID, docs)
	require.NoError(t, err)
	assert.Empty(t, none, "placed without places")

	placed, err = f.placer().Sweep(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, 3, placed, "the map on again, each gets its place")
	for _, doc := range docs {
		got := f.placeOf(t, run.ID, doc)
		assert.True(t, insideCircle(got.Map.ZoomX, got.Map.ZoomY, run.Map.DotRadius, f.circleIn(t, run, got)))
	}
	assert.Empty(t, f.lines("level=WARN"))
}
