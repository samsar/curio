package sqlite

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/samsar/curio/internal/store"
)

// BenchmarkVectorSearch times nearest-neighbor search over 20,000 random
// unit vectors of 1024 dimensions, a new home's width, in 200 documents:
// knn is the bare vec0 KNN query at k=100, and VectorSearch the store's
// search for 50 hits, which over-fetches k=500 before its document
// predicates. Only the search is timed. Compare builds with and without
// sqlite-vec's NEON kernels:
//
//	CGO_CFLAGS="-O2 -g" go test -tags=sqlite_fts5,sqlite_json -run '^$' -bench VectorSearch ./internal/store/sqlite/
//	CGO_CFLAGS="-O2 -g -DSQLITE_VEC_ENABLE_NEON" go test -tags=sqlite_fts5,sqlite_json -run '^$' -bench VectorSearch ./internal/store/sqlite/
func BenchmarkVectorSearch(b *testing.B) {
	const (
		dim       = 1024
		documents = 200
		perDoc    = 100
	)
	ctx := context.Background()
	db, _ := openUnmigrated(b)
	if _, err := Migrate(ctx, db); err != nil {
		b.Fatal(err)
	}
	if err := EnsureVectorIndex(ctx, db, dim); err != nil {
		b.Fatal(err)
	}
	ch := NewChunks(db, dim)
	rng := rand.New(rand.NewPCG(1, 2))
	docs, exts := NewDocuments(db), NewExtractions(db)
	for d := range documents {
		doc := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/%d", d), State: store.DocStateFetched}
		if err := docs.Create(ctx, doc); err != nil {
			b.Fatal(err)
		}
		ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "bench", Status: store.ExtractionStatusOK}
		if err := exts.Create(ctx, ext); err != nil {
			b.Fatal(err)
		}
		chunks := make([]store.ChunkInput, perDoc)
		for i := range chunks {
			chunks[i] = store.ChunkInput{Text: "chunk", Embedding: unitVector(rng, dim)}
		}
		if err := ch.ReplaceForDocument(ctx, doc.ID, ext.ID, "", nil, chunks); err != nil {
			b.Fatal(err)
		}
	}
	query := unitVector(rng, dim)

	b.Run("knn", func(b *testing.B) {
		blob, err := sqlitevec.SerializeFloat32(query)
		if err != nil {
			b.Fatal(err)
		}
		for b.Loop() {
			n, err := knn(ctx, db, blob)
			if err != nil {
				b.Fatal(err)
			}
			if n != 100 {
				b.Fatalf("got %d neighbors, want 100", n)
			}
		}
	})
	b.Run("VectorSearch", func(b *testing.B) {
		for b.Loop() {
			hits, err := ch.VectorSearch(ctx, "local", query, 50, store.SearchFilters{})
			if err != nil {
				b.Fatal(err)
			}
			if len(hits) != 50 {
				b.Fatalf("got %d hits, want 50", len(hits))
			}
		}
	})
}

// knn runs vec0's KNN query for the 100 vectors nearest query and counts
// the rows.
func knn(ctx context.Context, db *DB, query []byte) (int, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT chunk_id, distance FROM chunks_vec WHERE embedding MATCH ? AND k = 100`, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

// unitVector is a random vector of unit length, dim wide.
func unitVector(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var sum float64
	for i := range v {
		x := rng.NormFloat64()
		v[i] = float32(x)
		sum += x * x
	}
	norm := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= norm
	}
	return v
}
