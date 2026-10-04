package layout_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight/layout"
)

// FuzzZoom draws random small hierarchies and holds the zoom view to its
// invariants. The bytes choose the shape (areas or flat, how many areas,
// interests and documents), each document's interest or Unsorted, and the
// vectors; every hierarchy is valid, so every one must draw.
func FuzzZoom(f *testing.F) {
	f.Add([]byte{0, 3, 20, 7, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{1, 2, 5, 40, 200, 13, 77, 0, 255, 128, 64, 32})
	f.Add([]byte{3, 1, 1, 1, 0})
	f.Add([]byte{2, 9, 9, 60, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9})
	f.Add([]byte{1, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		in := hierarchy(data)
		z, err := layout.Zoom(context.Background(), in)
		require.NoError(t, err)
		assertZoom(t, in, z)
	})
}

// FuzzZoomWarm draws a hierarchy warm, from the view of another as its
// prior, and holds it to the same invariants. Group i of the drawn hierarchy
// (its interests, then its areas) takes a new key when bit i of renamed is
// set, starting at its old key's place when bit i of started is, so carried
// groups, new ones that start at a predecessor and new ones that don't
// share an area or the top level in every mix.
func FuzzZoomWarm(f *testing.F) {
	// Two carried interests and a new one in an area: the alignment's
	// matrix is rank one.
	f.Add([]byte{149, 176, 99, 16}, []byte{81, 107, 167, 58, 16, 183, 87, 163}, uint16(0b101), uint16(0b101))
	f.Add([]byte{0, 3, 20, 7, 1, 2, 3, 4, 5, 6, 7, 8, 9}, []byte{0, 3, 20, 7, 1, 2, 3, 4, 5, 6, 7, 8, 9}, uint16(0), uint16(0))
	f.Add([]byte{1, 2, 5, 40, 200, 13, 77, 0, 255}, []byte{2, 6, 30, 1, 9, 9, 120, 45}, uint16(0b1001_0110), uint16(0b11))
	f.Add([]byte{2, 9, 9, 60, 9, 9, 9, 9, 9}, []byte{3, 6, 40, 2, 11, 250, 3}, uint16(0xffff), uint16(0xffff))
	f.Fuzz(func(t *testing.T, prior, data []byte, renamed, started uint16) {
		prev := hierarchy(prior)
		in := hierarchy(data)
		in.Prior = priorZoom(prev, zoom(t, prev))
		groups := slices.Concat(in.Interests, in.Areas)
		for i := range groups {
			if renamed&(1<<i) == 0 {
				continue
			}
			if started&(1<<i) != 0 {
				groups[i].Start = groups[i].Key
			}
			groups[i].Key = "new-" + groups[i].Key
		}
		in.Interests, in.Areas = groups[:len(in.Interests)], groups[len(in.Interests):]
		z, err := layout.Zoom(context.Background(), in)
		require.NoError(t, err)
		assertZoom(t, in, z)
	})
}

// hierarchy builds a valid zoom input from data.
func hierarchy(data []byte) layout.ZoomInput {
	at := 0
	next := func() int {
		if len(data) == 0 {
			return 0
		}
		b := int(data[at%len(data)])
		at++
		return b
	}
	const dims = 4
	areas := next() % 4 // 0: the flat shape
	interests := max(areas, 1+next()%7)
	docs := interests + next()%60
	unsortedShare := next() % 4
	in := layout.ZoomInput{}
	vec := func() []float32 {
		v := make([]float32, dims)
		for d := range v {
			v[d] = float32(next()%17) - 8
		}
		return v
	}
	for i := range interests {
		g := layout.Group{Key: fmt.Sprintf("i%d", i), Area: -1, Centroid: unitF32(f64(vec()))}
		if areas > 0 {
			g.Area = i % areas
		}
		in.Interests = append(in.Interests, g)
	}
	for a := range areas {
		in.Areas = append(in.Areas, layout.Group{Key: fmt.Sprintf("a%d", a), Area: -1, Centroid: unitF32(f64(vec()))})
	}
	for k := range docs {
		in.Keys = append(in.Keys, fmt.Sprintf("d%03d", (k*37)%1000))
		in.Vectors = append(in.Vectors, vec())
		interest := k % interests // the first of each interest's documents makes it hold one
		if k >= interests && next()%4 < unsortedShare {
			interest = -1
		}
		in.Interest = append(in.Interest, interest)
		in.Nearest = append(in.Nearest, next()%(interests+1)-1)
		in.Similarity = append(in.Similarity, float64(next())/255)
	}
	return in
}

func f64(v []float32) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return out
}
