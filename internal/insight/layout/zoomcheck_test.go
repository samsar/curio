package layout

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestZoom_CheckRefusesBrokenPacking: a view whose circles overlap, or
// whose interest or dot lies outside its circle, fails the zoom view's own
// check, which a drawn view passes.
func TestZoom_CheckRefusesBrokenPacking(t *testing.T) {
	cases := []struct {
		name  string
		shape string // "areas" or "flat"; "" for both
		spoil func(l *ZoomLayout)
		want  string
	}{
		{"Unsorted on the content", "", func(l *ZoomLayout) {
			l.Unsorted.X, l.Unsorted.Y = l.Interests[0].X, l.Interests[0].Y
		}, "top-level circles"},
		{"a dot outside its circle", "", func(l *ZoomLayout) {
			l.Docs[0] = XY{l.Interests[0].X + l.Interests[0].R, l.Interests[0].Y}
		}, "document 0 lies outside"},
		{"an Unsorted dot outside the disc", "", func(l *ZoomLayout) {
			l.Docs[13] = XY{l.Unsorted.X, l.Unsorted.Y + l.Unsorted.R}
		}, "document 13 lies outside"},
		{"two interests of an area on one place", "areas", func(l *ZoomLayout) {
			l.Interests[1].X, l.Interests[1].Y = l.Interests[0].X, l.Interests[0].Y
		}, "interests 0 and 1 of area 0 overlap"},
		{"an interest outside its area", "areas", func(l *ZoomLayout) {
			l.Interests[2].X += l.Areas[1].R
		}, "interest 2 lies outside area 1"},
		{"two areas on one place", "areas", func(l *ZoomLayout) {
			l.Areas[1].X, l.Areas[1].Y = l.Areas[0].X, l.Areas[0].Y
		}, "top-level circles 0 and 1 overlap"},
		{"two interests on one place", "flat", func(l *ZoomLayout) {
			l.Interests[3].X, l.Interests[3].Y = l.Interests[1].X, l.Interests[1].Y
		}, "top-level circles 1 and 3 overlap"},
	}
	for _, shape := range []string{"areas", "flat"} {
		z, err := newZoomer(context.Background(), fourInterests(shape == "areas"))
		require.NoError(t, err)
		drawn, err := z.draw()
		require.NoError(t, err)
		require.NoError(t, z.checkPacked(drawn))
		for _, tc := range cases {
			if tc.shape != "" && tc.shape != shape {
				continue
			}
			t.Run(shape+": "+tc.name, func(t *testing.T) {
				l := drawn
				l.Interests, l.Areas, l.Docs = slices.Clone(drawn.Interests), slices.Clone(drawn.Areas), slices.Clone(drawn.Docs)
				tc.spoil(&l)
				err := z.checkPacked(l)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
			})
		}
	}
}

// fourInterests is four interests of three documents each, in two areas
// when areas, then two documents in Unsorted.
func fourInterests(areas bool) ZoomInput {
	var in ZoomInput
	axis := func(d int) []float32 {
		v := make([]float32, 4)
		v[d] = 1
		return v
	}
	for i := range 4 {
		g := Group{Key: fmt.Sprintf("i%d", i), Area: -1, Centroid: axis(i)}
		if areas {
			g.Area = i / 2
		}
		in.Interests = append(in.Interests, g)
	}
	if areas {
		in.Areas = []Group{{Key: "a0", Area: -1, Centroid: axis(0)}, {Key: "a1", Area: -1, Centroid: axis(2)}}
	}
	for k := range 14 {
		interest := k / 3
		if k >= 12 {
			interest = -1
		}
		v := axis(k / 3 % 4)
		v[(k+1)%4] += 0.1 * float32(k%3)
		in.Keys = append(in.Keys, fmt.Sprintf("d%02d", k))
		in.Vectors = append(in.Vectors, v)
		in.Interest = append(in.Interest, interest)
		in.Nearest = append(in.Nearest, -1)
		in.Similarity = append(in.Similarity, 0)
	}
	return in
}

// TestScaledDistances_HoldsTheFitNearTouching: a warm level whose placed
// groups sit far apart at nearly one place fits no scale past maxFitScale
// times the touching scale, nor one under it by that factor; a fit within
// the bounds is kept.
func TestScaledDistances_HoldsTheFitNearTouching(t *testing.T) {
	r := []float64{5, 5, 5}
	const gap = 3.0
	placed := []bool{true, true, false}
	near := [][]float64{{0, 0.00018, 0.5}, {0.00018, 0, 0.5}, {0.5, 0.5, 0}}
	touching := touchingScale(near, r, gap)
	require.InDelta(t, 26.0, touching, 1e-9, "the median pair touches")
	// Two placed groups at nearly one place, drawn 39 dot radii apart, fit
	// a scale of about 216,667.
	out := scaledDistances(near, []XY{{0, 0}, {39, 0}, {0, 0}}, placed, r, gap)
	assert.InDelta(t, maxFitScale*touching*0.5, out[0][2], 1e-9, "held from above")

	apart := [][]float64{{0, 0.5, 0.5}, {0.5, 0, 0.5}, {0.5, 0.5, 0}}
	out = scaledDistances(apart, []XY{{0, 0}, {20, 0}, {0, 0}}, placed, r, gap)
	assert.InDelta(t, 20.0, out[0][1], 1e-9, "a fit of 40 is kept")
	out = scaledDistances(apart, []XY{{0, 0}, {1, 0}, {0, 0}}, placed, r, gap)
	assert.InDelta(t, touching/maxFitScale*0.5, out[0][1], 1e-9, "held from below")
}
