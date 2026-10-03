package insight

import (
	"context"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
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
	drifted := NewPlacer(f.insights, f.vectors, func() string { return "the embeddings drifted" }, nil)
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
	p := NewPlacer(faulty, f.vectors, nil, f.debugLog())
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
	axis0 := f.groups(t, run.ID)[3].ID
	p := f.placer()
	fast := f.add(t, 1, 1)[0]
	p.Place(ctx, tenant, fast)
	_, err := f.db.Exec(`UPDATE interest_placements SET interest_id = ? WHERE document_id = ?`, axis0, fast)
	require.NoError(t, err)
	swept := f.add(t, 0, 2)
	broken := f.addVector(t, []float32{float32(math.NaN()), 1})

	placed, err := p.Sweep(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, 2, placed)
	got := f.placedInto(t, run.ID)
	assert.Equal(t, axis0, got[fast].InterestID, "the fast path's placement stands")
	for _, id := range swept {
		assert.Equal(t, axis0, got[id].InterestID)
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
