package insight_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/quality"
	"github.com/samsar/curio/internal/store"
)

// The stability properties a rebuild must keep, pinned on fixture library 1
// (see fixture_test.go) through the whole grouping a rebuild makes: Group,
// the merge, centroids, strays and carry-over. Each threshold is the
// design's target unless noted, beside the value the fixture measured.

// TestStability_UnchangedLibrary: a warm rebuild of an unchanged library
// gives the identical grouping and keeps every name, also starting from a
// run that ran the split check (measured: identical, 100% · 100%). It
// guards Louvain's fixpoint: without the restarts a warm start from a
// multi-level result moves nodes.
func TestStability_UnchangedLibrary(t *testing.T) {
	t.Parallel()
	lib := fixtureLibrary(1)
	fresh := rebuild(t, lib.docs, nil, false)
	split := rebuild(t, lib.docs, &fresh, true)
	for _, from := range []run{fresh, split} {
		again := rebuild(t, lib.docs, &from, false)
		assert.Equal(t, from.g.Area, again.g.Area)
		assert.Equal(t, from.g.Interest, again.g.Interest)
		assert.Equal(t, from.g.Seeds, again.g.Seeds)
		interests, areas := namesKept(t, from, again)
		assert.InDelta(t, 1, interests, 0)
		assert.InDelta(t, 1, areas, 0)
	}
}

// changeDraw is one 5% change of the fixture: the previous library, the new
// one. A mixed change adds 2.5% and removes 2.5%.
func changeDraw(n int, kind string, draw int) (prevIdx, newIdx []int) {
	seed := uint64(5000 + draw + 1)
	if kind == "mixed" {
		seed += 7
	}
	perm := shuffled(n, seed)
	if kind == "add" {
		return without(n, perm[:n/20]), without(n, nil)
	}
	return without(n, perm[:n/40]), without(n, perm[n/40:n/20])
}

// TestStability_NamesKeptAfterAChange: after a 5% change, a warm rebuild
// keeps most names. Targets, means of 3 draws: added, at least 90% of
// interests and of areas (measured 96.3% · 100%); mixed, at least 90% ·
// 88% (measured 93.2% · 96.7%). It guards the warm start: rebuilding
// fresh keeps 86.8% · 100% and 83.2% · 93.0% of the names here, and about
// 74% of interest names on the owner's library.
func TestStability_NamesKeptAfterAChange(t *testing.T) {
	t.Parallel()
	lib := fixtureLibrary(1)
	n := len(lib.docs)
	for _, tc := range []struct {
		kind                   string
		minInterests, minAreas float64
	}{
		{"add", 0.90, 0.90},
		{"mixed", 0.90, 0.88},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			var kept [3][2]float64
			t.Run("draws", func(t *testing.T) {
				for d := range kept {
					t.Run(strconv.Itoa(d), func(t *testing.T) {
						t.Parallel()
						prevIdx, newIdx := changeDraw(n, tc.kind, d)
						prev := rebuild(t, lib.subset(prevIdx), nil, false)
						next := rebuild(t, lib.subset(newIdx), &prev, false)
						kept[d][0], kept[d][1] = namesKept(t, prev, next)
					})
				}
			})
			var interests, areas float64
			for _, k := range kept {
				interests += k[0] / 3
				areas += k[1] / 3
			}
			assert.GreaterOrEqual(t, interests, tc.minInterests)
			assert.GreaterOrEqual(t, areas, tc.minAreas)
		})
	}
}

// chainEnd runs the chain of rebuilds from 60% of the library to all of it
// in 5% steps, warm, the merge at every step, the split check every
// splitEvery-th step (never when 0), with documents arriving in the order
// seed draws, and returns its last run.
func chainEnd(t *testing.T, lib library, seed uint64, splitEvery int) run {
	t.Helper()
	const steps = 8
	n := len(lib.docs)
	perm := shuffled(n, seed)
	step := int(math.Round(0.05 * float64(n)))
	var prev run
	for k := 0; k <= steps; k++ {
		docs := lib.subset(without(n, perm[:(steps-k)*step]))
		if k == 0 {
			prev = rebuild(t, docs, nil, false)
			require.Equal(t, insight.ShapeAreas, prev.g.Shape, "the chain starts in the areas shape")
			continue
		}
		prev = rebuild(t, docs, &prev, splitEvery > 0 && k%splitEvery == 0)
	}
	return prev
}

func cohesion(r run) float64 {
	vecs := make([][]float32, len(r.points))
	for i, p := range r.points {
		vecs[i] = p.Vector
	}
	groups := quality.Groups(r.g.Interest)
	return quality.CohesionOf(vecs, groups, quality.Centroids(vecs, groups)).Mean
}

// TestStability_Chain: warm rebuilds from 60% to 100% of the library, the
// split check every 4th, end near a fresh grouping of the whole library:
// an interest count within 10% and mean cohesion within 0.03 (measured,
// mean of 3 draws: 33 interests against 36, 8.3% fewer, and cohesion
// 0.002 lower). It guards against warm starts drifting from what the
// library holds. The same chain without the split check ends with fewer
// interests (measured 31): local moving and aggregation merge communities
// but never split one, so this guards the split check.
func TestStability_Chain(t *testing.T) {
	t.Parallel()
	lib := fixtureLibrary(1)
	fresh := rebuild(t, lib.docs, nil, false)
	freshCount := float64(numGroups(fresh.g.Interest))
	// Every chain runs at once; ends[0] split every 4th step, ends[1] never.
	var ends [2][3]run
	t.Run("chains", func(t *testing.T) {
		for v, every := range []int{4, 0} {
			for d := range 3 {
				t.Run(fmt.Sprintf("split every %d, draw %d", every, d), func(t *testing.T) {
					t.Parallel()
					ends[v][d] = chainEnd(t, lib, uint64(8080+1010*d), every)
				})
			}
		}
	})
	mean := func(f func(run) float64, rs [3]run) float64 {
		var m float64
		for _, r := range rs {
			m += f(r) / 3
		}
		return m
	}
	count := func(r run) float64 { return float64(numGroups(r.g.Interest)) }
	assert.InDelta(t, freshCount, mean(count, ends[0]), 0.10*freshCount, "interests against a fresh grouping")
	assert.InDelta(t, cohesion(fresh), mean(cohesion, ends[0]), 0.03, "cohesion against a fresh grouping")
	assert.Less(t, mean(count, ends[1]), mean(count, ends[0]), "the split check parts what warm starts merged")
}

// TestStability_NoNearDuplicatesLeft: after the merge no two interests of
// one scope (an area, or the whole grouping in the flat shape) have
// centroids at the merge threshold or above, fresh or warm. Measured: the
// merge joins 7 interests of the fresh grouping (the five pieces Louvain
// cut one topic into, and three pairs) and 7 of the warm one with the
// split check, among them a piece of the planted pair's first topic that
// it puts back with the interest holding the pair; Louvain itself keeps
// the planted pair together. It guards the merge's repeated rounds, which
// a single pass doesn't give (see TestMergeNearDuplicates_UntilNothingJoins).
func TestStability_NoNearDuplicatesLeft(t *testing.T) {
	t.Parallel()
	lib := fixtureLibrary(1)
	n := len(lib.docs)
	prevIdx, _ := changeDraw(n, "add", 0)
	prev := rebuild(t, lib.subset(prevIdx), nil, false)
	small := fixtureShape{docs: 800, areas: 4, topicDims: 32, generalistDims: 64}.build(4)
	for _, r := range []run{
		rebuild(t, lib.docs, nil, false),
		rebuild(t, lib.docs, &prev, true),
		rebuild(t, small.docs, nil, false),
	} {
		require.Positive(t, r.merged, "the premise: the merge joined something")
		scope := make([]int, len(r.centroids))
		for i, l := range r.g.Interest {
			if l != insight.NoiseLabel {
				scope[l] = r.g.Area[i]
			}
		}
		for a := range r.centroids {
			for b := a + 1; b < len(r.centroids); b++ {
				if scope[a] == scope[b] {
					assert.Less(t, dot(r.centroids[a], r.centroids[b]), insight.MergeThreshold, "%s interests %d and %d", r.g.Shape, a, b)
				}
			}
		}
	}
}

// smallLibrary is 35 documents around 4 topics, with the fixture's noise
// and offset: a library the size of the one that got no interests at all
// from the old defaults.
func smallLibrary(seed uint64) []store.DocVector {
	r := rand.New(rand.NewPCG(seed, 0x35))
	const dim = 32
	norm := func(v []float64) []float64 {
		var s float64
		for _, x := range v {
			s += x * x
		}
		for d := range v {
			v[d] /= math.Sqrt(s)
		}
		return v
	}
	gauss := func() []float64 {
		v := make([]float64, dim)
		for d := range v {
			v[d] = r.NormFloat64()
		}
		return norm(v)
	}
	offset := gauss()
	topics := [][]float64{gauss(), gauss(), gauss(), gauss()}
	docs := make([]store.DocVector, 35)
	for i := range docs {
		t, u := topics[i%4], gauss()
		v := make([]float64, dim)
		for d := range v {
			v[d] = t[d] + docNoise*u[d]
		}
		v = norm(v)
		vec := make([]float32, dim)
		for d := range vec {
			vec[d] = float32(v[d] + offsetNorm*offset[d])
		}
		docs[i] = store.DocVector{DocumentID: fmt.Sprintf("doc-%02d", i), Vector: vec}
	}
	return docs
}

// TestStability_SmallLibrariesGetInterests: 35 documents around 4 topics
// get at least 2 interests in the flat shape on every one of 10 seeds,
// never none (measured: 4 on every seed). It guards the flat shape below
// the gate and its resolution.
func TestStability_SmallLibrariesGetInterests(t *testing.T) {
	t.Parallel()
	for seed := range uint64(10) {
		r := rebuild(t, smallLibrary(seed), nil, false)
		assert.Equal(t, insight.ShapeFlat, r.g.Shape)
		assert.GreaterOrEqual(t, numGroups(r.g.Interest), 2, "seed %d", seed)
	}
}
