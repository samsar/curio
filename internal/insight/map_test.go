package insight

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight/layout"
	"github.com/samsar/curio/internal/insight/louvain"
	"github.com/samsar/curio/internal/store"
)

// TestMapExtent: the store's map and the layouts' share one square.
func TestMapExtent(t *testing.T) {
	assert.InDelta(t, layout.Extent, store.MapExtent, 0)
}

// mapDims is the width of the map tests' vectors.
const mapDims = 12

// addAround creates n documents named prefix-g-i, fetched and indexed now,
// whose vectors spread around axis g, never so far that another axis is
// their largest, and returns their IDs.
func (f *engineFixture) addAround(t *testing.T, prefix string, g, n int, r *rand.Rand) []string {
	t.Helper()
	f.dim = mapDims
	ids := make([]string, 0, n)
	for range n {
		i := f.added
		f.added++
		id := fmt.Sprintf("%s-%d-%03d", prefix, g, i)
		title := fmt.Sprintf("group%d topic%d item%d", g, g, i)
		require.NoError(t, f.docs.Create(context.Background(), &store.Document{ID: id, TenantID: tenant,
			URL: "https://example.com/" + id, Title: &title}))
		f.indexed(t, id)
		v := make([]float32, mapDims)
		for d := range v {
			v[d] = float32(0.15 * r.NormFloat64())
		}
		v[g]++
		f.vectors.dvs = append(f.vectors.dvs, store.DocVector{DocumentID: id, Vector: v})
		ids = append(ids, id)
	}
	return ids
}

// mapLibrary is a fixture of four groups of documents around their axes,
// 30, 24, 18 and 12 of them.
func mapLibrary(t *testing.T, prefix string) *engineFixture {
	t.Helper()
	f := newEngineFixture(t)
	r := rand.New(rand.NewPCG(5, 6))
	for g, n := range []int{30, 24, 18, 12} {
		f.addAround(t, prefix, g, n, r)
	}
	return f
}

// places are a run's documents' places, by document ID.
func (f *engineFixture) places(t *testing.T, runID string) map[string]store.MapPosition {
	t.Helper()
	as, err := f.store.RunAssignments(context.Background(), runID)
	require.NoError(t, err)
	out := make(map[string]store.MapPosition, len(as))
	for _, a := range as {
		require.NotNil(t, a.Map, "document %s has a place", a.DocumentID)
		out[a.DocumentID] = *a.Map
	}
	return out
}

// docMap and zoomView are a run's places on one of the views.
func docMap(ps map[string]store.MapPosition) map[string][2]float64 {
	out := make(map[string][2]float64, len(ps))
	for id, p := range ps {
		out[id] = [2]float64{p.MapX, p.MapY}
	}
	return out
}

func zoomView(ps map[string]store.MapPosition) map[string][2]float64 {
	out := make(map[string][2]float64, len(ps))
	for id, p := range ps {
		out[id] = [2]float64{p.ZoomX, p.ZoomY}
	}
	return out
}

// meanShift is the mean distance between the places a and b share.
func meanShift(a, b map[string][2]float64) float64 {
	var sum float64
	n := 0
	for id, p := range a {
		if q, ok := b[id]; ok {
			sum += math.Hypot(p[0]-q[0], p[1]-q[1])
			n++
		}
	}
	return sum / float64(n)
}

// TestRebuild_DrawsTheMap: a rebuild draws its map, built and fresh the
// first time, with the params MapParams records, and gives every group and
// document a place on it and every interest its most similar interests.
func TestRebuild_DrawsTheMap(t *testing.T) {
	f := mapLibrary(t, "d")
	run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
	f.assertInvariants(t, run)
	require.Equal(t, store.MapBuilt, run.Map.Status)
	assert.Equal(t, store.RunKindFresh, run.Map.Kind)
	params, err := MapParams(true, MapSeed)
	require.NoError(t, err)
	assert.JSONEq(t, string(params), string(run.Map.Params))
	assert.Positive(t, run.Map.DotRadius)
	gs, err := f.store.RunGroups(context.Background(), run.ID)
	require.NoError(t, err)
	for _, g := range gs {
		assert.Len(t, g.Similar, 3, "the three others")
		for _, s := range g.Similar {
			assert.NotEqual(t, g.ID, s.ID)
		}
	}
	assert.Contains(t, f.logLine(t, "interests rebuilt"), " map=built map_kind=fresh map_ms=")
}

// TestRebuild_MapFailures: a map that fails, runs out of time or panics
// fails alone: the run is done without one, says why, warns once and says
// map=failed. A rebuild cancelled while its map is drawn fails as
// cancelled and counts no failure.
func TestRebuild_MapFailures(t *testing.T) {
	blocks := func(ctx context.Context, _ MapInput) (*Map, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	for _, tc := range []struct {
		name   string
		mapper func(ctx context.Context, in MapInput) (*Map, error)
		reason string
	}{
		{"an error", func(context.Context, MapInput) (*Map, error) { return nil, errors.New("no room") }, "no room"},
		{"out of time", blocks, "the map took longer than 50ms"},
		{"a panic", func(context.Context, MapInput) (*Map, error) { panic("lost the lattice") }, "panic: lost the lattice"},
		{"an invalid map", func(context.Context, MapInput) (*Map, error) { return &Map{Kind: store.RunKindFresh}, nil },
			"insight: a map of 0 documents"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mapLibrary(t, "d")
			run := f.rebuild(t, f.engine(nil, nil, Config{}).WithMapper(tc.mapper).WithMapTimeout(50*time.Millisecond))
			assert.Equal(t, store.InterestRunDone, run.Status)
			require.NotNil(t, run.Map)
			assert.Equal(t, store.MapFailed, run.Map.Status)
			assert.True(t, strings.HasPrefix(run.Map.Error, tc.reason), "the reason %q", run.Map.Error)
			assert.NotContains(t, run.Map.Error, "\n", "one line")
			assert.NotEmpty(t, run.Map.Params)
			f.assertInvariants(t, run)
			warning := f.logLine(t, "interests: map failed")
			assert.Contains(t, warning, "level=WARN")
			assert.Contains(t, warning, "run="+run.ID)
			if tc.name == "a panic" {
				assert.Contains(t, warning, "stack=", "the stack goes to the log")
			}
			assert.Contains(t, f.logLine(t, "interests rebuilt"), " map=failed")
		})
	}

	t.Run("the rebuild cancelled", func(t *testing.T) {
		f := mapLibrary(t, "d")
		ctx, cancel := context.WithCancel(context.Background())
		mapper := func(mctx context.Context, _ MapInput) (*Map, error) {
			cancel()
			<-mctx.Done()
			return nil, mctx.Err()
		}
		runID, err := f.engine(nil, nil, Config{}).WithMapper(mapper).Rebuild(ctx, tenant, store.RunTriggerManual)
		require.ErrorIs(t, err, context.Canceled)
		run, err := f.store.GetRun(context.Background(), runID)
		require.NoError(t, err)
		assert.Equal(t, store.InterestRunFailed, run.Status, "failed as cancelled")
		assert.Nil(t, run.Map)
		st, err := f.store.State(context.Background(), tenant)
		require.NoError(t, err)
		assert.Zero(t, st.Failures, "no failure counted")
		assert.NotContains(t, f.logs.String(), "interests: map failed")
	})
}

// TestRebuild_MapStartsWarm: a rebuild after a change draws its map warm
// from the previous one, which moves the documents less than a map drawn
// from nothing; an unchanged library's map is the previous one verbatim.
func TestRebuild_MapStartsWarm(t *testing.T) {
	more := func(f *engineFixture) { f.addAround(t, "d", 1, 4, rand.New(rand.NewPCG(7, 8))) }
	f := mapLibrary(t, "d")
	e := f.engine(nil, nil, Config{Center: true})
	first := f.rebuild(t, e)
	before := f.places(t, first.ID)
	more(f)
	warm := f.rebuild(t, e)
	assert.Equal(t, store.RunKindWarm, warm.Map.Kind)
	after := f.places(t, warm.ID)

	again := f.rebuild(t, e)
	assert.Equal(t, store.RunKindWarm, again.Map.Kind)
	assert.Equal(t, after, f.places(t, again.ID), "an unchanged library: the same places")
	assert.InDelta(t, warm.Map.DotRadius, again.Map.DotRadius, 0)
	assert.Equal(t, warm.Map.Unsorted, again.Map.Unsorted)
	assert.Less(t, meanShift(docMap(before), docMap(after)), meanShift(docMap(before), docMap(coldControl(t, more))),
		"warm: the documents move less")
}

// coldControl is the first map of the map library, with more added when
// set, in a database of its own: a map drawn from nothing, aligned to
// nothing.
func coldControl(t *testing.T, more func(f *engineFixture)) map[string]store.MapPosition {
	t.Helper()
	c := mapLibrary(t, "d")
	if more != nil {
		more(c)
	}
	return c.places(t, c.rebuild(t, c.engine(nil, nil, Config{Center: true})).ID)
}

// TestRebuild_MapRules: which view starts warm, and which is aligned, by
// what the rebuild owes and what it found.
func TestRebuild_MapRules(t *testing.T) {
	ctx := context.Background()
	t.Run("a re-embedding is fresh, aligned to the previous map", func(t *testing.T) {
		more := func(f *engineFixture) { f.addAround(t, "d", 3, 8, rand.New(rand.NewPCG(11, 12))) }
		f := mapLibrary(t, "d")
		e := f.engine(nil, nil, Config{Center: true})
		prior := f.places(t, f.rebuild(t, e).ID)
		more(f)
		require.NoError(t, f.store.OweFresh(ctx, tenant, store.FreshReindex))
		run := f.rebuild(t, e)
		assert.Equal(t, store.RunKindFresh, run.Map.Kind)
		aligned := meanShift(docMap(prior), docMap(f.places(t, run.ID)))
		cold := meanShift(docMap(prior), docMap(coldControl(t, more)))
		assert.Less(t, aligned, cold, "aligned: closer to the previous map than one drawn from nothing")
	})
	t.Run("a drift is fresh", func(t *testing.T) {
		f := mapLibrary(t, "d")
		f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
		drifted := Config{Center: true, Drift: func() string { return "the embeddings drifted" }}
		assert.Equal(t, store.RunKindFresh, f.rebuild(t, f.engine(nil, nil, drifted)).Map.Kind)
	})
	t.Run("after a failed map, fresh and unaligned", func(t *testing.T) {
		f := mapLibrary(t, "d")
		failing := func(context.Context, MapInput) (*Map, error) { return nil, errors.New("no room") }
		f.rebuild(t, f.engine(nil, nil, Config{Center: true}).WithMapper(failing))
		run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
		assert.Equal(t, store.RunKindFresh, run.Map.Kind)
		assert.Equal(t, coldControl(t, nil), f.places(t, run.ID), "what a map drawn from nothing draws")
	})
	t.Run("a new shape draws the zoom view cold", func(t *testing.T) {
		f := mapLibrary(t, "d")
		f.rebuild(t, f.engine(areasByAxis(0, 0, 1, 1), nil, Config{Center: true}))
		run := f.rebuild(t, f.engine(nil, nil, Config{Center: true}))
		require.Equal(t, store.InterestShapeFlat, run.Shape)
		assert.Equal(t, zoomView(coldControl(t, nil)), zoomView(f.places(t, run.ID)))
	})
	t.Run("an unchanged library regrouped on request keeps its document map", func(t *testing.T) {
		f := mapLibrary(t, "d")
		g := &togglingGrouper{}
		e := f.engine(g, nil, Config{Center: true})
		prior := f.places(t, f.rebuild(t, e).ID)
		require.NoError(t, f.store.OweFresh(ctx, tenant, store.FreshManual))
		g.merge = true
		run := f.rebuild(t, e)
		require.Equal(t, 3, run.NumInterests, "regrouped")
		assert.Equal(t, store.RunKindWarm, run.Map.Kind)
		after := f.places(t, run.ID)
		assert.Equal(t, docMap(prior), docMap(after), "the document map verbatim")
		assert.NotEqual(t, zoomView(prior), zoomView(after), "the zoom view drawn anew")
	})
}

// togglingGrouper groups by axis, the first two axes as one interest once
// merge is set.
type togglingGrouper struct{ merge bool }

func (*togglingGrouper) Name() string           { return "toggling" }
func (*togglingGrouper) Params() map[string]any { return nil }
func (g *togglingGrouper) Group(ctx context.Context, in GroupInput) (Grouping, error) {
	return FlatGrouper(clusterFunc(func(ctx context.Context, points []Point) ([]int, error) {
		labels, err := byAxis(ctx, points)
		for i, l := range labels {
			if g.merge && l == 1 {
				labels[i] = 0
			}
		}
		return labels, err
	})).Group(ctx, in)
}

// TestRebuild_MapIsDeterministic: two engines over one library, in two
// databases, give every document the same places, though their new
// identities differ; so does a third served the vectors in another order.
func TestRebuild_MapIsDeterministic(t *testing.T) {
	got := make([]map[string]store.MapPosition, 0, 3)
	for x := range 3 {
		f := mapLibrary(t, "d")
		if x == 2 {
			r := rand.New(rand.NewPCG(1, 2))
			r.Shuffle(len(f.vectors.dvs), func(i, j int) { f.vectors.dvs[i], f.vectors.dvs[j] = f.vectors.dvs[j], f.vectors.dvs[i] })
		}
		e := f.engine(NewLouvainGrouper(nil), nil, Config{Center: true})
		f.rebuild(t, e)
		f.addAround(t, "d", 2, 5, rand.New(rand.NewPCG(9, 10)))
		got = append(got, f.places(t, f.rebuild(t, e).ID))
	}
	assert.Equal(t, got[0], got[1], "two databases")
	assert.Equal(t, got[0], got[2], "the vectors in another order")
}

// TestBuildMap_RefusesBadInput: what doesn't describe one grouping is an
// error.
func TestBuildMap_RefusesBadInput(t *testing.T) {
	points := slices.Concat(copies("a", 3, []float32{1, 0}), copies("b", 3, []float32{0, 1}))
	g := flatGrouping([]int{0, 0, 0, 1, 1, 1})
	cents, err := Centroids(points, g.Interest)
	require.NoError(t, err)
	fits, err := AssignStrays(points, g, cents, LooseFitThreshold)
	require.NoError(t, err)
	good := MapInput{Points: points, Grouping: g, Centroids: cents, Fits: fits, InterestKeys: []string{"i0", "i1"},
		AreaKeys: []string{}}
	m, err := BuildMap(context.Background(), good)
	require.NoError(t, err)
	require.NoError(t, m.Validate(6, 0, 2))
	for name, spoil := range map[string]func(in *MapInput){
		"fewer fits":      func(in *MapInput) { in.Fits = in.Fits[1:] },
		"fewer keys":      func(in *MapInput) { in.InterestKeys = in.InterestKeys[1:] },
		"fewer centroids": func(in *MapInput) { in.Centroids = in.Centroids[1:] },
		"fewer starts":    func(in *MapInput) { in.InterestStarts = []string{""} },
		"a duplicate key": func(in *MapInput) { in.InterestKeys = []string{"i0", "i0"} },
		"bad neighbours":  func(in *MapInput) { in.Grouping.Neighbours = make([][]louvain.Edge, 2) },
	} {
		t.Run(name, func(t *testing.T) {
			in := good
			spoil(&in)
			_, err := BuildMap(context.Background(), in)
			assert.Error(t, err)
		})
	}
}
