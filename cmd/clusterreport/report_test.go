package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/quality"
	"github.com/samsar/curio/internal/store"
)

// fixedReport is a report with every section, the stored run's included,
// and the n/a values a small library leaves.
func fixedReport() *report {
	share := func(v float64) *float64 { return &v }
	within := 0
	rep := &report{
		Database: "copy.db", Documents: 5254, DroppedNonFinite: 1, Grouper: "louvain",
		Params: map[string]any{"center": true}, Draws: 2, Seed: 0,
		Fresh: freshReport{
			Shape: insight.ShapeAreas,
			Areas: &level{Groups: 30, Coverage: 0.924, Sizes: quality.Sizes{Interests: 30, Median: 151.5, P90: 322.1, Max: 594},
				Cohesion: share(0.5007), Silhouette: share(0.0536)},
			Interests: level{Groups: 187, Coverage: 0.923, Sizes: quality.Sizes{Interests: 187, Median: 20, P90: 49, Max: 110,
				SmallShare: 0.043}, Cohesion: share(0.6414), Silhouette: share(0.0937)},
			InterestsBeforeMerge: 190, Merged: 3, Loose: 3, Unsorted: 402,
			NearDuplicates: nearDuplicates{Threshold: 0.85, WithinAnArea: &within, Anywhere: 0, LenientThreshold: 0.74,
				AnywhereLenient: 10},
		},
		Baseline: baselineReport{Clusterer: "knn-graph", Params: map[string]any{"k": 10, "min_similarity": 0.5},
			Interests: level{Groups: 325, Coverage: 0.702, Sizes: quality.Sizes{Interests: 325, Median: 6, P90: 21, Max: 133,
				SmallShare: 0.345}, Cohesion: share(0.776), Silhouette: share(0.1626)}},
		Warm: warmReport{
			Added: keptReport{Draws: []keptDraw{{Draw: 0, Seed: 5001, kept: kept{share(0.954), share(0.966)}},
				{Draw: 1, Seed: 5002, kept: kept{share(0.955), share(0.97)}}},
				Mean: kept{share(0.9545), share(0.968)}, Min: kept{share(0.954), share(0.966)}},
			Mixed: keptReport{Draws: []keptDraw{{Draw: 0, Seed: 5008, kept: kept{share(0.964), nil}}},
				Mean: kept{share(0.964), nil}, Min: kept{share(0.964), nil}},
		},
		FreshRebuild: keptReport{Draws: []keptDraw{{Draw: 0, Seed: 5001, kept: kept{share(0.707), share(0.793)}}},
			Mean: kept{share(0.707), share(0.793)}, Min: kept{share(0.707), share(0.793)}},
		Chain: chainReport{
			Steps: []chainStep{
				{Draw: 0, Step: 0, LibraryPercent: 60, Documents: 3150, Areas: 22, Interests: 140, FreshAreas: 22,
					FreshInterests: 140, Cohesion: share(0.6497), FreshCohesion: share(0.6497), Gap: share(0)},
				{Draw: 0, Step: 4, LibraryPercent: 80, Documents: 4202, SplitCheck: true, Areas: 27, Interests: 162,
					FreshAreas: 25, FreshInterests: 152, Cohesion: share(0.6451), FreshCohesion: share(0.6343),
					Gap: share(0.0108), Kept: kept{share(0.839), share(0.955)}},
				{Draw: 1, Step: 8, LibraryPercent: 100, Documents: 5254, SplitCheck: true, Areas: 31, Interests: 185,
					FreshAreas: 30, FreshInterests: 187, Kept: kept{share(0.799), share(0.933)}},
			},
			End:              []chainEnd{{Draw: 1, Areas: 31, Interests: 185}},
			MeanEndAreas:     31,
			MeanEndInterests: 185, EndInterestsDiffPercent: share(-1.07),
			WorstGap:   &worstGap{Draw: 0, Step: 4, Gap: 0.0108},
			SplitSteps: keptStats{Mean: kept{share(0.819), share(0.944)}, Min: kept{share(0.799), share(0.933)}},
		},
		StoredRun: &storedReport{
			RunID: "run-1", Trigger: store.RunTriggerFirst, Kind: store.RunKindFresh, Shape: store.InterestShapeAreas,
			FinishedAt: new(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)), SameParams: true,
			Counts:       runCounts{Documents: 5254, Areas: 2, Interests: 3, Loose: 3, Unsorted: 402, Created: 5},
			LabelSources: map[string]int{"llm": 4, "terms": 1},
			Labels: []labelRow{
				{ID: "a1", Label: "Software Engineering", LabelSource: store.LabelSourceLLM, Size: 412, Interests: []labelRow{
					{ID: "i1", Label: "AI Agent Engineering", LabelSource: store.LabelSourceLLM, Size: 86},
					{ID: "i2", Label: "AI Agent Tooling", LabelSource: store.LabelSourceLLM, Size: 45},
				}},
				{ID: "a2", Label: "Personal Finance", LabelSource: store.LabelSourceTerms, Size: 120, Interests: []labelRow{
					{ID: "i3", Label: "Index Investing", LabelSource: store.LabelSourceLLM, Size: 60},
				}},
			},
			Agreement: agreement{Shared: 5254, AreasARI: share(1), InterestsARI: share(1), SameFits: 5254, Identical: true},
		},
		TimingsMS: timings{Read: 8382, Fresh: 2137, Baseline: 1974, Warm: 23338, FreshRebuild: 2030, Chain: 69656,
			Map: 61012, StoredRun: 4, Total: 168533},
	}
	rep.Map = mapReport{
		Cold: coldMapReport{TookMS: 6912, AllocBytes: 412 << 20, NP5: 0.2941, NP15: 0.3612, AreaPurity: share(0.8631),
			SpaceAreaPurity: share(0.8693)},
		SeedToSeed: 0.0164, PeakRSSBytes: 1900 << 20,
	}
	rep.Map.Warm.Added = warmMapsReport{
		Draws: []warmMapDraw{
			{Draw: 0, Seed: 5001, Kind: "warm", ColdMS: 6540, WarmMS: 2611, NP5: 0.2902,
				Documents: displacement{Mean: 0.0134, Max: 0.21, AlignedMean: 0.012, AlignedMax: 0.2},
				Interests: displacement{Mean: 0.006, Max: 0.04, AlignedMean: 0.005, AlignedMax: 0.03}},
		},
		Documents: displacement{Mean: 0.0134, Max: 0.0134, AlignedMean: 0.012, AlignedMax: 0.012},
		Interests: displacement{Mean: 0.006, Max: 0.006, AlignedMean: 0.005, AlignedMax: 0.005}, MeanNP5: 0.2902,
	}
	rep.Map.Warm.Mixed = warmMapsReport{
		Draws: []warmMapDraw{
			{Draw: 0, Seed: 5008, Kind: "warm", ColdMS: 6498, WarmMS: 2588, NP5: 0.2915,
				Documents: displacement{Mean: 0.0141, Max: 0.25, AlignedMean: 0.0131, AlignedMax: 0.24},
				Interests: displacement{Mean: 0.0071, Max: 0.05, AlignedMean: 0.0062, AlignedMax: 0.04}},
			{Draw: 1, Seed: 5009, Kind: "warm", ColdMS: 6503, WarmMS: 2570, NP5: 0.2899,
				Documents: displacement{Mean: 0.0127, Max: 0.19, AlignedMean: 0.0119, AlignedMax: 0.18},
				Interests: displacement{Mean: 0.0055, Max: 0.03, AlignedMean: 0.0049, AlignedMax: 0.03}},
		},
		Documents: displacement{Mean: 0.0134, Max: 0.0141, AlignedMean: 0.0125, AlignedMax: 0.0131},
		Interests: displacement{Mean: 0.0063, Max: 0.0071, AlignedMean: 0.00555, AlignedMax: 0.0062}, MeanNP5: 0.2907,
	}
	rep.Chain.Fresh.Areas, rep.Chain.Fresh.Interests, rep.Chain.Fresh.Cohesion = 30, 187, share(0.6414)
	rep.StoredRun.Duplicates.Scopes = []scopeDuplicates{{Scope: "Software Engineering",
		LabelDuplicates: quality.LabelDuplicates{Near: 1, Interests: 2, Pairs: [][2]string{{"AI Agent Engineering", "AI Agent Tooling"}}}}}
	rep.StoredRun.Duplicates.WithinScopes.Near = 1
	rep.StoredRun.Duplicates.AllInterests = quality.LabelDuplicates{Near: 1}
	return rep
}

func TestReport_Text(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "report.txt"))
	require.NoError(t, err)
	var got bytesWriter
	require.NoError(t, fixedReport().writeText(&got))
	assert.Equal(t, string(want), got.String())
}

// bytesWriter counts its writes: the report is written in one.
type bytesWriter struct {
	data   []byte
	writes int
}

func (w *bytesWriter) Write(p []byte) (int, error) {
	w.writes++
	w.data = append(w.data, p...)
	return len(p), nil
}

func (w *bytesWriter) String() string { return string(w.data) }

func TestReport_TextIsOneWrite(t *testing.T) {
	var w bytesWriter
	require.NoError(t, fixedReport().writeText(&w))
	assert.Equal(t, 1, w.writes)
}

func TestReport_JSONSaysNullForNA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, writeJSON(path, fixedReport()))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var rep map[string]any
	require.NoError(t, json.Unmarshal(raw, &rep))
	mixed := rep["warm"].(map[string]any)["mixed"].(map[string]any)
	assert.Nil(t, mixed["mean"].(map[string]any)["areas"], "a level without old groups keeps no names: null")
	step := rep["chain"].(map[string]any)["steps"].([]any)[0].(map[string]any)
	assert.Nil(t, step["kept"].(map[string]any)["interests"], "step 0 kept nothing: null")
	end := rep["chain"].(map[string]any)["steps"].([]any)[2].(map[string]any)
	assert.Nil(t, end["gap"])
	stored := rep["stored_run"].(map[string]any)
	assert.Equal(t, true, stored["agreement"].(map[string]any)["identical"])
}

func TestMeasure_StopsWhenCancelled(t *testing.T) {
	lib := newSynthetic(60, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newMeasurer(ctx, slog.New(slog.DiscardHandler), lib.docs, 1, 0).measure(&library{docs: lib.docs})
	require.ErrorIs(t, err, context.Canceled)
}
