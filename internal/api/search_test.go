package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// embedFunc adapts a function to search.Embedder.
type embedFunc func(ctx context.Context, texts []string) ([][]float32, error)

func (f embedFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

func unitVec() []float32 {
	v := make([]float32, store.EmbeddingDim)
	v[0] = 1
	return v
}

// newSearchServer serves /v1/search with emb as the query embedder, over
// three indexed documents that all mention "kafka".
func newSearchServer(t *testing.T, emb search.Embedder, cfg search.Config) *testServer {
	t.Helper()
	cfg.Log = slog.New(slog.DiscardHandler)
	s := newTestServer(t, func(d *Deps) {
		d.Search = search.New(d.Chunks, d.Documents, emb, cfg)
	})
	ctx := context.Background()
	for i := range 3 {
		doc := s.seedDocument(t, fmt.Sprintf("https://example.com/kafka/%d", i), store.DocStateFetched)
		ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test",
			Status: store.ExtractionStatusOK, FetchedAt: time.Now().UTC()}
		require.NoError(t, s.deps.Extractions.Create(ctx, ext))
		require.NoError(t, s.deps.Chunks.ReplaceForDocument(ctx, doc.ID, ext.ID, "", nil,
			[]store.ChunkInput{{Text: fmt.Sprintf("kafka partitions part %d", i), Embedding: unitVec()}}))
	}
	return s
}

func (s *testServer) search(t *testing.T, body string) response {
	t.Helper()
	return s.do(t, request{method: http.MethodPost, path: "/v1/search", contentType: "application/json", body: body})
}

func okEmbedder() search.Embedder {
	return embedFunc(func(context.Context, []string) ([][]float32, error) {
		return [][]float32{unitVec()}, nil
	})
}

func TestSearch_KBounds(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{DefaultK: 2})
	for _, body := range []string{`{"query":"kafka","k":-1}`, `{"query":"kafka","k":101}`} {
		t.Run(body, func(t *testing.T) {
			assertProblem(t, s.search(t, body), http.StatusBadRequest)
		})
	}
}

func TestSearch_DefaultK(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{DefaultK: 2})

	resp := s.search(t, `{"query":"kafka"}`)
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Len(t, got.Items, 2, "an omitted k uses search.default_k")
	assert.False(t, got.Degraded)
	assert.NotContains(t, resp.body, `"degraded"`, "omitted when false")

	resp = s.search(t, `{"query":"kafka","k":3}`)
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Len(t, got.Items, 3)
}

func TestSearch_DegradedResponse(t *testing.T) {
	// The embedder hangs until the engine's embed deadline, so the request
	// takes at least that long and took_ms must show it.
	hang := embedFunc(func(ctx context.Context, _ []string) ([][]float32, error) {
		<-ctx.Done()
		return nil, fmt.Errorf("ollama: %w", ctx.Err())
	})
	s := newSearchServer(t, hang, search.Config{EmbedTimeout: 20 * time.Millisecond})

	resp := s.search(t, `{"query":"kafka"}`)
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.True(t, got.Degraded)
	require.NotEmpty(t, got.Warnings)
	assert.Contains(t, got.Warnings[0], "keyword-only")
	assert.Len(t, got.Items, 3, "keyword results are returned")
	assert.GreaterOrEqual(t, got.TookMS, int64(20))
}

func TestSearch_UnsupportedFieldsAreRejected(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{})
	for _, body := range []string{
		`{"query":"kafka","weights":{"bm25":2}}`,
		`{"query":"kafka","collapse":"sum"}`,
		`{"query":"kafka","filters":{"saved_after":"2024-01-01T00:00:00Z"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			assertProblem(t, s.search(t, body), http.StatusBadRequest)
		})
	}
}
