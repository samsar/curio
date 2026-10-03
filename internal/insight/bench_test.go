package insight_test

import (
	"context"
	"sync"
	"testing"

	"github.com/samsar/curio/internal/insight"
)

// benchShape is a library the owner's size: 5,000 documents of 1,024
// dimensions in 30 areas.
var benchShape = fixtureShape{docs: 5000, areas: 30, topicDims: 512, generalistDims: 512}

var benchLibrary = sync.OnceValues(func() ([]insight.Point, map[string]insight.Seed) {
	lib := benchShape.build(1)
	points, _, err := insight.PreparePoints(lib.docs, true)
	if err != nil {
		panic(err)
	}
	// The previous grouping: the library less 5%, the documents a warm
	// rebuild then starts as new.
	n := len(lib.docs)
	prevPoints, _, err := insight.PreparePoints(lib.subset(without(n, shuffled(n, 1)[:n/20])), true)
	if err != nil {
		panic(err)
	}
	prev, err := insight.NewLouvainGrouper(nil).Group(context.Background(), insight.GroupInput{Shape: insight.ShapeFlat, Points: prevPoints})
	if err != nil {
		panic(err)
	}
	prior := make(map[string]insight.Seed, len(prevPoints))
	for i, p := range prevPoints {
		prior[p.ID] = prev.Seeds[i]
	}
	return points, prior
})

func BenchmarkNearestNeighbours(b *testing.B) {
	points, _ := benchLibrary()
	vecs := make([][]float32, len(points))
	for i, p := range points {
		vecs[i] = p.Vector
	}
	for b.Loop() {
		if _, err := insight.NearestNeighbours(context.Background(), vecs); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGroup times what a rebuild of 5,000 documents computes: the
// neighbour pass, both Louvain levels, the merge and the strays. With the
// pass made beforehand it times Group alone without the pass: the graphs
// cut from it and Louvain at both levels, with its restarts and, warm, the
// split check.
func BenchmarkGroup(b *testing.B) {
	points, prior := benchLibrary()
	lists, err := insight.NearestNeighbours(context.Background(), vectorsOf(points))
	if err != nil {
		b.Fatal(err)
	}
	made := func(context.Context, [][]float32) (insight.NeighbourLists, error) { return lists, nil }
	fresh := insight.GroupInput{Points: points, Shape: insight.ShapeFlat}
	warm := insight.GroupInput{Points: points, Shape: insight.ShapeAreas, Prior: prior, Split: true}
	for _, tc := range []struct {
		name string
		in   insight.GroupInput
		made bool
	}{
		{"fresh", fresh, false},
		{"warm+split", warm, false},
		{"fresh, pass made, Group only", fresh, true},
		{"warm+split, pass made, Group only", warm, true},
	} {
		g := insight.NewLouvainGrouper(nil)
		if tc.made {
			g = g.WithNeighbours(made)
		}
		b.Run(tc.name, func(b *testing.B) {
			var out insight.Grouping
			for b.Loop() {
				if out, err = g.Group(context.Background(), tc.in); err != nil {
					b.Fatal(err)
				}
				if !tc.made {
					mergeAndStrays(b, points, out)
				}
			}
			b.ReportMetric(float64(numGroups(out.Area)), "areas")
			b.ReportMetric(float64(numGroups(out.Interest)), "interests")
		})
	}
}

// BenchmarkMergeAndStrays times the steps after Group: the merge, the
// centroids and the strays.
func BenchmarkMergeAndStrays(b *testing.B) {
	points, prior := benchLibrary()
	g, err := insight.NewLouvainGrouper(nil).Group(context.Background(), insight.GroupInput{
		Points: points, Shape: insight.ShapeAreas, Prior: prior, Split: true,
	})
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		mergeAndStrays(b, points, g)
	}
}

func mergeAndStrays(b *testing.B, points []insight.Point, g insight.Grouping) {
	merged, _, err := insight.MergeNearDuplicates(points, g, insight.MergeThreshold)
	if err != nil {
		b.Fatal(err)
	}
	cents, err := insight.Centroids(points, merged.Interest)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := insight.AssignStrays(points, merged, cents, insight.LooseFitThreshold); err != nil {
		b.Fatal(err)
	}
}

func vectorsOf(points []insight.Point) [][]float32 {
	vecs := make([][]float32, len(points))
	for i, p := range points {
		vecs[i] = p.Vector
	}
	return vecs
}
