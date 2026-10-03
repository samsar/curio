package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"slices"
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
// own, which end up unsorted; and a common offset puts every vector in a
// narrow cone, as embedding models do, so centering matters. Documents are
// named doc-00000, … in ID order, each with its topic.
type synthetic struct {
	docs  []store.DocVector
	topic []int // -1 for a generalist
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
	offset := unit(0, dim)
	lib := synthetic{docs: make([]store.DocVector, n), topic: make([]int, n)}
	for i := range lib.docs {
		var v []float64
		lib.topic[i] = -1
		if r.Float64() < 0.08 {
			v = unit(topicDims, dim)
		} else {
			x, k := r.Float64()*total, 0
			for ; k < len(topics)-1; k++ {
				if x -= topics[k].weight; x < 0 {
					break
				}
			}
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
// on a synthetic two-level library in a database, with one document whose
// vector is NaN, Engine.Rebuild's fresh run of 95% of the library is
// identical to the report's fresh grouping, through the stored-run
// agreement; and once the rest arrives, the engine's warm rebuild keeps as
// many interest and area names as the report's warm rebuild of the same
// draw says.
func TestReport_MatchesTheEngine(t *testing.T) {
	ctx := context.Background()
	quiet := slog.New(slog.DiscardHandler)
	db := sqlitetest.NewDB(t)
	docs, insights := sqlite.NewDocuments(db), sqlite.NewInsights(db)
	lib := newSynthetic(1100, 1)
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

	// The last 5% arrive, and a warm rebuild absorbs them.
	fetch(minusDocs(lib.docs, prev)...)
	served.dvs = append(slices.Clone(lib.docs), nan)
	warmID, err := engine.Rebuild(ctx, store.LocalTenantID, store.RunTriggerAuto)
	require.NoError(t, err)
	warm, err := insights.GetRun(ctx, warmID)
	require.NoError(t, err)
	require.Equal(t, store.RunKindWarm, warm.Kind)
	require.False(t, warm.SplitCheck, "55 changes are under 4 × 53")
	require.Equal(t, store.InterestShapeAreas, warm.Shape)

	read, err = readLibrary(ctx, served, insights)
	require.NoError(t, err)
	m = newMeasurer(ctx, quiet, read.docs, 1, 0)
	_, k, err := m.warmDraw(changeAdded, 0)
	require.NoError(t, err)
	require.NotNil(t, k.Interests)
	require.NotNil(t, k.Areas)
	assert.Equal(t, warm.Kept, int(math.Round(*k.Interests*float64(first.NumInterests))),
		"the report's names kept are the engine's kept interests")
	assert.Equal(t, liveAreas(t, insights, stored.Labels), int(math.Round(*k.Areas*float64(first.NumAreas))),
		"and its live areas")
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
