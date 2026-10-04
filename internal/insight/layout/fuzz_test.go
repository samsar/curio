package layout_test

import (
	"context"
	"fmt"
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
