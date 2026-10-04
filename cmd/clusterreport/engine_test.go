package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// synthetic is a seeded library with two levels of planted structure, a
// smaller cousin of internal/insight's fixture library: areas are random
// directions of a topic subspace, each holding 3 to 6 topics around it of
// skewed weights; a document is normalize(topic + 0.8·u) for a random unit
// u of the subspace; 8% are generalists, random in a subspace of their
// own, which end up unsorted; 4% lean towards a topic, normalize(0.8·topic
// + g) for a generalist's g, too far from its documents for the area
// graph's edges and some near enough its centroid for a loose fit; and a
// common offset puts every vector in a narrow cone, as embedding models
// do, so centering matters. Documents are named doc-00000, … in ID order,
// each with its topic.
type synthetic struct {
	docs  []store.DocVector
	topic []int // -1 for a generalist or a document leaning towards a topic
}

func newSynthetic(n int, seed uint64) synthetic {
	const areas, topicDims, generalistDims = 6, 32, 64
	r := rand.New(rand.NewPCG(seed, 0x5e7))
	dim := topicDims + generalistDims
	unit := func(lo, hi int) []float64 {
		v := make([]float64, dim)
		var s float64
		for d := lo; d < hi; d++ {
			v[d] = r.NormFloat64()
			s += v[d] * v[d]
		}
		for d := lo; d < hi; d++ {
			v[d] /= math.Sqrt(s)
		}
		return v
	}
	around := func(c []float64, spread float64) []float64 {
		u := unit(0, topicDims)
		v := make([]float64, dim)
		var s float64
		for d := range v {
			v[d] = c[d] + spread*u[d]
			s += v[d] * v[d]
		}
		for d := range v {
			v[d] /= math.Sqrt(s)
		}
		return v
	}
	type topic struct {
		center []float64
		weight float64
	}
	topics := make([]topic, 0, 6*areas)
	for range areas {
		center, weight := unit(0, topicDims), 0.5+r.Float64()
		for range 3 + r.IntN(4) {
			topics = append(topics, topic{center: around(center, 0.5), weight: weight * (0.3 + r.Float64())})
		}
	}
	var total float64
	for _, t := range topics {
		total += t.weight
	}
	pick := func() int {
		x, k := r.Float64()*total, 0
		for ; k < len(topics)-1; k++ {
			if x -= topics[k].weight; x < 0 {
				break
			}
		}
		return k
	}
	offset := unit(0, dim)
	lib := synthetic{docs: make([]store.DocVector, n), topic: make([]int, n)}
	for i := range lib.docs {
		var v []float64
		lib.topic[i] = -1
		switch x := r.Float64(); {
		case x < 0.08:
			v = unit(topicDims, dim)
		case x < 0.12:
			v = lean(topics[pick()].center, unit(topicDims, dim))
		default:
			k := pick()
			v, lib.topic[i] = around(topics[k].center, 0.8), k
		}
		vec := make([]float32, dim)
		for d := range vec {
			vec[d] = float32(v[d] + offset[d])
		}
		lib.docs[i] = store.DocVector{DocumentID: fmt.Sprintf("doc-%05d", i), Vector: vec}
	}
	return lib
}

// lean is normalize(0.8·center + g): a document leaning towards the topic
// at center.
func lean(center, g []float64) []float64 {
	v := make([]float64, len(center))
	var s float64
	for d := range v {
		v[d] = 0.8*center[d] + g[d]
		s += v[d] * v[d]
	}
	for d := range v {
		v[d] /= math.Sqrt(s)
	}
	return v
}

// servedVectors serves the vectors a test chose; neither the engine nor
// the report reads anything else from the chunk store.
type servedVectors struct {
	store.ChunkStore
	dvs []store.DocVector
}

func (s *servedVectors) DocumentVectors(context.Context, string) ([]store.DocVector, error) {
	return s.dvs, nil
}

// TestReport_MatchesTheEngine holds the report's pipeline to the engine's:
// on a synthetic two-level library in a database, with loose fits and one
// document whose vector is NaN, Engine.Rebuild's fresh run of 95% of the
// library is identical to the report's fresh grouping, fit for fit,
// through the stored-run agreement; and once the rest arrives, the
// engine's warm rebuild keeps as many interest and area names as the
// report's warm rebuild of the same draw says. The library's seed is one
// whose warm rebuild renames an interest and whose split check would
// rename more, so a pipeline that keeps too many names, or runs the check,
// shows.
func TestReport_MatchesTheEngine(t *testing.T) {
	t.Parallel() // its maps take seconds under -race, as TestRun_FlatLibrary's do
	ctx := context.Background()
	quiet := slog.New(slog.DiscardHandler)
	db := sqlitetest.NewDB(t)
	docs, insights := sqlite.NewDocuments(db), sqlite.NewInsights(db)
	lib := newSynthetic(1100, 33)
	nan := store.DocVector{DocumentID: "doc-nan", Vector: make([]float32, len(lib.docs[0].Vector))}
	nan.Vector[0] = float32(math.NaN())
	for i, dv := range append(slices.Clone(lib.docs), nan) {
		title := "A note"
		if i < len(lib.topic) && lib.topic[i] >= 0 {
			title = fmt.Sprintf("Topic%02d piece %d", lib.topic[i], i)
		}
		require.NoError(t, docs.Create(ctx, &store.Document{ID: dv.DocumentID, TenantID: store.LocalTenantID,
			URL: "https://example.com/" + dv.DocumentID, Title: &title}))
	}
	fetch := func(dvs ...store.DocVector) {
		for _, dv := range dvs {
			require.NoError(t, docs.MarkFetched(ctx, dv.DocumentID))
		}
	}
	served := &servedVectors{}
	engine := insight.New(docs, served, insights, insight.NewLouvainGrouper(quiet), nil,
		insight.Config{Labeling: insight.LabelingTerms, Center: true}, quiet)
	prevIdx, _ := changeDraw(len(lib.docs), changeAdded, 0, 0)
	prev := make([]store.DocVector, len(prevIdx))
	for k, i := range prevIdx {
		prev[k] = lib.docs[i]
	}

	// The first rebuild, fresh, of 95% of the library.
	served.dvs = append(slices.Clone(prev), nan)
	fetch(served.dvs...)
	firstID, err := engine.Rebuild(ctx, store.LocalTenantID, store.RunTriggerFirst)
	require.NoError(t, err)
	first, err := insights.GetRun(ctx, firstID)
	require.NoError(t, err)
	require.Equal(t, store.RunKindFresh, first.Kind)
	require.Equal(t, store.InterestShapeAreas, first.Shape)
	require.Positive(t, first.NumLoose, "the library holds loose fits, whose fits the comparison checks too")

	read, err := readLibrary(ctx, served, insights)
	require.NoError(t, err)
	assert.Equal(t, 1, read.dropped, "the NaN vector is dropped, as the engine drops it")
	require.Len(t, read.docs, len(prev))
	require.NotNil(t, read.stored)
	require.Equal(t, firstID, read.stored.run.ID)
	m := newMeasurer(ctx, quiet, read.docs, 1, 0)
	full, err := m.wholeFrom(insight.ShapeFlat)
	require.NoError(t, err)
	stored, err := storedOf(read.stored, full, m.grouper.Name(), runParams(m.grouper))
	require.NoError(t, err)
	assert.True(t, stored.SameParams, "the run's params are the report's")
	ag := stored.Agreement
	assert.Equal(t, len(prev), ag.Shared)
	require.NotNil(t, ag.InterestsARI)
	require.NotNil(t, ag.AreasARI)
	assert.InDelta(t, 1, *ag.InterestsARI, 0)
	assert.InDelta(t, 1, *ag.AreasARI, 0)
	assert.Equal(t, len(prev), ag.SameFits)
	assert.True(t, ag.Identical, "the engine's fresh run is the report's fresh grouping")
	assert.Equal(t, first.NumInterests, stored.Counts.Interests)
	firstRun := read.stored

	// The report's cold map of its fresh grouping is the engine's map of
	// its fresh run, drawn beside the warm rebuild: the maps take seconds
	// under -race.
	t.Run("the cold map", func(t *testing.T) {
		t.Parallel()
		cold, err := m.drawMap(full, labelKeys(full), nil, insight.MapSeed)
		require.NoError(t, err)
		assertSameMap(t, firstRun, full, cold.m)
	})

	// The last 5% arrive, and a warm rebuild absorbs them.
	t.Run("the warm rebuild", func(t *testing.T) {
		t.Parallel()
		fetch(minusDocs(lib.docs, prev)...)
		served.dvs = append(slices.Clone(lib.docs), nan)
		warmID, err := engine.Rebuild(ctx, store.LocalTenantID, store.RunTriggerAuto)
		require.NoError(t, err)
		warm, err := insights.GetRun(ctx, warmID)
		require.NoError(t, err)
		require.Equal(t, store.RunKindWarm, warm.Kind)
		require.False(t, warm.SplitCheck, "55 changes are under 4 × 53")
		require.Less(t, warm.Kept, first.NumInterests, "seed 33's warm rebuild renames an interest, which the comparison needs")
		require.Equal(t, store.InterestShapeAreas, warm.Shape)

		after, err := readLibrary(ctx, served, insights)
		require.NoError(t, err)
		whole := newMeasurer(ctx, quiet, after.docs, 1, 0)
		before, next, k, err := whole.warmDraw(changeAdded, 0)
		require.NoError(t, err)
		require.NotNil(t, k.Interests)
		require.NotNil(t, k.Areas)
		assert.Equal(t, warm.Kept, int(math.Round(*k.Interests*float64(first.NumInterests))),
			"the report's names kept are the engine's kept interests")
		assert.Equal(t, liveAreas(t, insights, stored.Labels), int(math.Round(*k.Areas*float64(first.NumAreas))),
			"and its live areas")

		// The engine's warm map started from its first run's: so does the
		// report's, given that run's places.
		keys, err := carriedKeys(before, next)
		require.NoError(t, err)
		prior := insight.NewPriorMap(firstRun.run, firstRun.groups, firstRun.assignments)
		warmMap, err := whole.drawMap(next, keys, prior, insight.MapSeed)
		require.NoError(t, err)
		assert.Equal(t, store.RunKindWarm, warmMap.m.Kind)
		require.NotNil(t, after.stored)
		assert.Equal(t, docMapOf(t, after.stored.assignments), docPlaces(next, warmMap.m),
			"the engine's warm document map is the report's")
	})
}

// docMapOf are stored assignments' places on the document map, by
// document.
func docMapOf(t *testing.T, as []store.InterestAssignment) map[string][2]float64 {
	t.Helper()
	out := make(map[string][2]float64, len(as))
	for _, a := range as {
		require.NotNil(t, a.Map, "document %s has a place", a.DocumentID)
		out[a.DocumentID] = [2]float64{a.Map.MapX, a.Map.MapY}
	}
	return out
}

// assertSameMap checks the stored run's map is the report's map m of gp,
// value for value: the dots' radius and Unsorted's disc, every document's
// four coordinates, and every group's circle and anchor, groups matched by
// their members (an area by the documents its community holds).
func assertSameMap(t *testing.T, stored *storedRun, gp *grouping, m *insight.Map) {
	t.Helper()
	require.NotNil(t, stored.run.Map)
	require.Equal(t, store.MapBuilt, stored.run.Map.Status)
	assert.Equal(t, m.DotRadius, stored.run.Map.DotRadius)
	assert.Equal(t, m.Unsorted, stored.run.Map.Unsorted)
	at := make(map[string]int, len(gp.ids))
	for i, id := range gp.ids {
		at[id] = i
	}
	areaMembers, interestMembers := map[string][]string{}, map[string][]string{}
	for _, a := range stored.assignments {
		require.NotNil(t, a.Map)
		assert.Equal(t, m.Docs[at[a.DocumentID]], *a.Map, "document %s", a.DocumentID)
		if a.AreaID != "" {
			areaMembers[a.AreaID] = append(areaMembers[a.AreaID], a.DocumentID)
		}
		if a.Fit == store.InterestFitMember {
			interestMembers[a.InterestID] = append(interestMembers[a.InterestID], a.DocumentID)
		}
	}
	toolArea, toolInterest := map[string]int{}, map[string]int{}
	for a, docs := range membersBy(gp, gp.g.Area, false) {
		toolArea[docs] = a
	}
	for l, docs := range membersBy(gp, gp.g.Interest, true) {
		toolInterest[docs] = l
	}
	require.Len(t, stored.groups, len(m.Areas)+len(m.Interests))
	for _, g := range stored.groups {
		require.NotNil(t, g.Map, g.ID)
		if g.Level == store.InterestLevelArea {
			a, ok := toolArea[strings.Join(sorted(areaMembers[g.ID]), ",")]
			require.True(t, ok, "area %s is one of the report's", g.ID)
			assert.Equal(t, m.Areas[a], *g.Map, "area %s", g.ID)
			continue
		}
		l, ok := toolInterest[strings.Join(sorted(interestMembers[g.ID]), ",")]
		require.True(t, ok, "interest %s is one of the report's", g.ID)
		assert.Equal(t, m.Interests[l], *g.Map, "interest %s", g.ID)
	}
}

// membersBy are each group's documents of labels, joined in ID order: an
// interest's members (membersOnly), an area's community.
func membersBy(gp *grouping, labels []int, membersOnly bool) []string {
	groups := make([][]string, numGroups(labels))
	for i, l := range labels {
		if l == insight.NoiseLabel || membersOnly && gp.fits[i].Kind != insight.FitMember {
			continue
		}
		groups[l] = append(groups[l], gp.ids[i])
	}
	out := make([]string, len(groups))
	for l, ids := range groups {
		out[l] = strings.Join(sorted(ids), ",")
	}
	return out
}

func sorted(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

// minusDocs is all without the documents of some, by ID.
func minusDocs(all, some []store.DocVector) []store.DocVector {
	gone := make(map[string]bool, len(some))
	for _, dv := range some {
		gone[dv.DocumentID] = true
	}
	var out []store.DocVector
	for _, dv := range all {
		if !gone[dv.DocumentID] {
			out = append(out, dv)
		}
	}
	return out
}

// liveAreas counts the areas of a label table still live.
func liveAreas(t *testing.T, insights store.InsightStore, table []labelRow) int {
	t.Helper()
	ids := make([]string, 0, len(table))
	for _, row := range table {
		ids = append(ids, row.ID)
	}
	identities, err := insights.GetInterests(context.Background(), store.LocalTenantID, ids)
	require.NoError(t, err)
	require.Len(t, identities, len(ids))
	live := 0
	for _, in := range identities {
		if in.RetiredAt == nil {
			live++
		}
	}
	return live
}
