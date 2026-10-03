package louvain

import (
	"context"
	"slices"
	"sync"
	"testing"
)

// benchGraph is an area-like graph of 5,000 nodes: every point's 10 nearest
// neighbours at cosine 0.40 or above, over points around 30 centers, with a
// warm start from its own fresh result where 5% of the nodes are new.
var benchGraph = sync.OnceValues(func() (*Graph, []int) {
	pts, _ := planted(11, 5000, 32, 30, 1.0)
	g, err := NewGraph(knn(pts, 10, 0.40))
	if err != nil {
		panic(err)
	}
	fresh, err := Run(context.Background(), g, nil, Options{Resolution: 1, Seed: 1})
	if err != nil {
		panic(err)
	}
	warm := slices.Clone(fresh.Communities)
	for i := 0; i < len(warm); i += 20 {
		warm[i] = -1
	}
	return g, warm
})

func BenchmarkRun(b *testing.B) {
	g, warm := benchGraph()
	opts := Options{Resolution: 1, Seed: 1}
	ctx := context.Background()
	b.Run("fresh", func(b *testing.B) {
		for b.Loop() {
			if _, err := Run(ctx, g, nil, opts); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("warm", func(b *testing.B) {
		for b.Loop() {
			if _, err := Run(ctx, g, warm, opts); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("warm+split", func(b *testing.B) {
		for b.Loop() {
			r, err := Run(ctx, g, warm, opts)
			if err != nil {
				b.Fatal(err)
			}
			s, err := RefineSplit(ctx, g, r.Communities, opts)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := Run(ctx, g, s.Communities, opts); err != nil {
				b.Fatal(err)
			}
		}
	})
}
