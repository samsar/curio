package main

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
)

func TestChangeDraw(t *testing.T) {
	const n = 1000
	t.Run("added", func(t *testing.T) {
		prev, next := changeDraw(n, changeAdded, 0, 0)
		assert.Len(t, prev, 950, "round(5% of 1,000) held out of the previous library")
		assert.Equal(t, without(n, nil), next, "the new library is everything")
		assert.True(t, slices.IsSorted(prev))
		assert.Empty(t, minus(prev, next), "the previous library is in the new one")
	})
	t.Run("mixed", func(t *testing.T) {
		prev, next := changeDraw(n, changeMixed, 0, 0)
		assert.Len(t, prev, 975, "round(2.5% of 1,000) held out of the previous library")
		assert.Len(t, next, 975, "and as many removed from the new one")
		assert.True(t, slices.IsSorted(prev))
		assert.True(t, slices.IsSorted(next))
		added, removed := minus(next, prev), minus(prev, next)
		assert.Len(t, added, 25)
		assert.Len(t, removed, 25)
		assert.Empty(t, minus(added, minus(added, removed)), "what is added is never what is removed")
	})
	t.Run("deterministic, one permutation per draw", func(t *testing.T) {
		for _, kind := range []changeKind{changeAdded, changeMixed} {
			a, _ := changeDraw(n, kind, 1, 0)
			b, _ := changeDraw(n, kind, 1, 0)
			c, _ := changeDraw(n, kind, 2, 0)
			assert.Equal(t, a, b, kind)
			assert.NotEqual(t, a, c, kind)
		}
	})
	t.Run("the seed shifts every draw", func(t *testing.T) {
		for _, kind := range []changeKind{changeAdded, changeMixed} {
			a, _ := changeDraw(n, kind, 0, 1)
			b, _ := changeDraw(n, kind, 1, 0)
			assert.Equal(t, a, b, kind)
		}
		assert.Equal(t, uint64(5001), changeSeed(changeAdded, 0, 0), "the research's draws")
		assert.Equal(t, uint64(5010), changeSeed(changeMixed, 2, 0))
	})
}

func TestChainDraw(t *testing.T) {
	const n = 1000
	libs := chainDraw(n, 0, 0)
	require.Len(t, libs, chainSteps+1)
	for k, lib := range libs {
		assert.Len(t, lib, n-(chainSteps-k)*50, "step %d", k)
		assert.True(t, slices.IsSorted(lib), "step %d", k)
		if k > 0 {
			assert.Empty(t, minus(libs[k-1], lib), "step %d grows step %d's library", k, k-1)
		}
	}
	assert.Equal(t, without(n, nil), libs[chainSteps], "the chain ends at the whole library")
	assert.Equal(t, 60, libraryPercent(0))
	assert.Equal(t, 100, libraryPercent(chainSteps))
	assert.Equal(t, chainDraw(n, 1, 0), chainDraw(n, 0, chainSeedStride), "the seed shifts the draws")
	assert.NotEqual(t, libs, chainDraw(n, 1, 0))
}

// minus is a minus b, both ascending.
func minus(a, b []int) []int {
	var out []int
	for _, x := range a {
		if _, ok := slices.BinarySearch(b, x); !ok {
			out = append(out, x)
		}
	}
	return out
}

// handGrouping is a grouping of documents d00, d01, … with the given
// areas (nil in the flat shape) and interests.
func handGrouping(area, interest []int) *grouping {
	g := insight.Grouping{Shape: insight.ShapeAreas, Area: area, Interest: interest}
	if area == nil {
		g.Shape = insight.ShapeFlat
		g.Area = slices.Repeat([]int{insight.NoiseLabel}, len(interest))
	}
	ids := make([]string, len(interest))
	for i := range ids {
		ids[i] = groupID(i)
	}
	return &grouping{ids: ids, g: g}
}

// withFits gives gp's documents their fits: a member of its interest, a
// loose fit of the interest loose names for it, or unsorted.
func withFits(gp *grouping, loose map[int]int) *grouping {
	gp.fits = make([]insight.Fit, len(gp.ids))
	for i, l := range gp.g.Interest {
		in, isLoose := loose[i]
		switch {
		case l != insight.NoiseLabel:
			gp.fits[i] = insight.Fit{Kind: insight.FitMember, Interest: l}
		case isLoose:
			gp.fits[i] = insight.Fit{Kind: insight.FitLoose, Interest: in}
		default:
			gp.fits[i] = insight.Fit{Kind: insight.FitUnsorted, Interest: insight.NoiseLabel}
		}
	}
	return gp
}

func TestNamesKept(t *testing.T) {
	const x = insight.NoiseLabel
	// Two areas of ten, each holding two interests of five; the last
	// document of each area is in none of them.
	areas := []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	interests := []int{0, 0, 0, 0, 0, 1, 1, 1, 1, x, 2, 2, 2, 2, 2, 3, 3, 3, 3, x}
	share := func(v float64) *float64 { return &v }

	cases := []struct {
		name       string
		prev, next *grouping
		want       kept
	}{
		{"unchanged", handGrouping(areas, interests), handGrouping(areas, interests),
			kept{Interests: share(1), Areas: share(1)}},
		{"renumbered", handGrouping(areas, interests),
			handGrouping([]int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
				[]int{3, 3, 3, 3, 3, 2, 2, 2, 2, x, 1, 1, 1, 1, 1, 0, 0, 0, 0, x}),
			kept{Interests: share(1), Areas: share(1)}},
		// Interest 0's five documents split three and two: neither half
		// holds more than 70% of it, so its name goes.
		{"an interest split in half", handGrouping(areas, interests),
			handGrouping(areas, []int{0, 0, 0, 4, 4, 1, 1, 1, 1, x, 2, 2, 2, 2, 2, 3, 3, 3, 3, x}),
			kept{Interests: share(0.75), Areas: share(1)}},
		// Interest 0's three members stay together, and the two documents
		// that fitted it loosely join interest 1. An interest's name follows
		// its members alone, as the engine's carry-over does: counted with
		// its loose fits, interest 0 would keep three of five, too few.
		{"loose fits are not members",
			withFits(handGrouping(areas, []int{0, 0, 0, x, x, 1, 1, 1, 1, x, 2, 2, 2, 2, 2, 3, 3, 3, 3, x}),
				map[int]int{3: 0, 4: 0}),
			handGrouping(areas, []int{0, 0, 0, 1, 1, 1, 1, 1, 1, x, 2, 2, 2, 2, 2, 3, 3, 3, 3, x}),
			kept{Interests: share(1), Areas: share(1)}},
		{"flat: no areas to keep", handGrouping(nil, interests), handGrouping(nil, interests),
			kept{Interests: share(1)}},
		{"from flat to areas", handGrouping(nil, interests), handGrouping(areas, interests),
			kept{Interests: share(1)}},
		{"no interests to keep", handGrouping(nil, slices.Repeat([]int{x}, 20)), handGrouping(areas, interests),
			kept{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := namesKept(tc.prev, tc.next)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMeanMin(t *testing.T) {
	share := func(v float64) *float64 { return &v }
	mean, low := meanMin([]kept{
		{Interests: share(0.9), Areas: share(1)},
		{Interests: share(0.8)},
		{},
	})
	assert.InDelta(t, 0.85, *mean.Interests, 1e-12, "draws without a share are left out")
	assert.InDelta(t, 0.8, *low.Interests, 1e-12)
	assert.InDelta(t, 1.0, *mean.Areas, 1e-12)
	assert.InDelta(t, 1.0, *low.Areas, 1e-12)

	mean, low = meanMin([]kept{{}, {}})
	assert.Equal(t, kept{}, mean, "no draw has a share: n/a")
	assert.Equal(t, kept{}, low)
}
