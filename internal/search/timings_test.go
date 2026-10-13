package search

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// slowChunks takes at least bm25 for a keyword search and vector for a
// vector search.
type slowChunks struct {
	store.ChunkStore
	bm25, vector time.Duration
}

func (s slowChunks) BM25Search(ctx context.Context, tenantID, query string, limit int, filters store.SearchFilters) ([]store.ChunkHit, error) {
	time.Sleep(s.bm25)
	return s.ChunkStore.BM25Search(ctx, tenantID, query, limit, filters)
}

func (s slowChunks) VectorSearch(ctx context.Context, tenantID string, embedding []float32, limit int, filters store.SearchFilters) ([]store.ChunkHit, error) {
	time.Sleep(s.vector)
	return s.ChunkStore.VectorSearch(ctx, tenantID, embedding, limit, filters)
}

// slowDocs takes at least read for each document read.
type slowDocs struct {
	store.DocumentStore
	read time.Duration
}

func (s slowDocs) GetByID(ctx context.Context, id string) (*store.Document, error) {
	time.Sleep(s.read)
	return s.DocumentStore.GetByID(ctx, id)
}

// TestEngine_Timings: each stage reports at least the time its store or
// embedder took; the legs overlap, and the rest of the search follows
// them; a stage that didn't run reports 0, and a failed embed counts up to
// its failure.
func TestEngine_Timings(t *testing.T) {
	const minEmbed, minBM25, minVector, minRead = 6 * time.Millisecond, 4 * time.Millisecond,
		5 * time.Millisecond, 2 * time.Millisecond
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)
	slowEmbed := func(err error) Embedder {
		return embedFunc(func(context.Context, []string) ([][]float32, error) {
			time.Sleep(minEmbed)
			if err != nil {
				return nil, err
			}
			return [][]float32{filledVec(dim, 0.10)}, nil
		})
	}
	slow := slowChunks{ChunkStore: chunks, bm25: minBM25, vector: minVector}
	sdocs := slowDocs{DocumentStore: docs, read: minRead}
	search := func(t *testing.T, emb Embedder, query string) Timings {
		t.Helper()
		engine := New(slow, sdocs, emb, Config{Log: slog.New(slog.DiscardHandler)})
		res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: query, K: 3})
		require.NoError(t, err)
		return res.Timings
	}

	t.Run("hybrid", func(t *testing.T) {
		tm := search(t, slowEmbed(nil), "mvcc concurrency")
		assert.GreaterOrEqual(t, tm.Embed, minEmbed)
		assert.GreaterOrEqual(t, tm.BM25, minBM25)
		assert.GreaterOrEqual(t, tm.Vector, minVector)
		assert.GreaterOrEqual(t, tm.Fuse, time.Duration(0))
		assert.GreaterOrEqual(t, tm.Hydrate, 3*minRead, "one read for each of the 3 documents")
		assert.GreaterOrEqual(t, tm.Total, max(tm.BM25, tm.Embed+tm.Vector)+tm.Fuse+tm.Hydrate)
	})
	t.Run("no keyword terms", func(t *testing.T) {
		tm := search(t, slowEmbed(nil), "the and of")
		assert.Zero(t, tm.BM25, "BM25 never ran")
		assert.GreaterOrEqual(t, tm.Embed, minEmbed)
		assert.GreaterOrEqual(t, tm.Vector, minVector)
		assert.Positive(t, tm.Total)
	})
	t.Run("degraded", func(t *testing.T) {
		tm := search(t, slowEmbed(errRefused), "attention token")
		assert.GreaterOrEqual(t, tm.Embed, minEmbed, "the embed up to its failure")
		assert.Zero(t, tm.Vector, "no vector search after a failed embed")
		assert.GreaterOrEqual(t, tm.BM25, minBM25)
		assert.GreaterOrEqual(t, tm.Total, max(tm.BM25, tm.Embed))
	})
}
