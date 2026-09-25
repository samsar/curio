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
// three indexed documents that all mention "kafka". Each option adjusts the
// Deps after the search engine is set.
func newSearchServer(t *testing.T, emb search.Embedder, cfg search.Config, options ...func(*Deps)) *testServer {
	t.Helper()
	cfg.Log = slog.New(slog.DiscardHandler)
	s := newTestServer(t, append([]func(*Deps){func(d *Deps) {
		d.Search = search.New(d.Chunks, d.Documents, emb, cfg)
	}}, options...)...)
	ctx := context.Background()
	for i := range 3 {
		doc := s.seedDocument(t, fmt.Sprintf("https://example.com/kafka/%d", i), store.DocStateFetched)
		ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test",
			Status: store.ExtractionStatusOK, FetchedAt: time.Now().UTC()}
		require.NoError(t, s.deps.Extractions.Create(ctx, ext))
		require.NoError(t, s.deps.Documents.SetCurrentExtraction(ctx, doc.ID, ext.ID))
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
		`{"query":"kafka","filters":{"folder":"/a"}}`,
		`{"query":"kafka","filters":{"tag":["t"]}}`,
	} {
		t.Run(body, func(t *testing.T) {
			assertProblem(t, s.search(t, body), http.StatusBadRequest)
		})
	}
}

func TestRelatedDocuments(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{})
	var ids []string
	rows, err := s.db.Query(`SELECT id FROM documents ORDER BY url`)
	require.NoError(t, err)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + ids[0] + "/related?k=5"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got RelatedResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, ids[0], got.DocID)
	require.Len(t, got.Items, 2, "every other indexed document")
	for _, hit := range got.Items {
		assert.NotEqual(t, ids[0], hit.Document.ID)
	}

	unindexed := s.seedDocument(t, "https://example.com/unindexed", store.DocStatePending)
	resp = s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + unindexed.ID + "/related"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Empty(t, got.Items)

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/documents/no-such-document/related"}),
		http.StatusNotFound)
	assert.Equal(t, `document "no-such-document" not found`, p.Detail)
}

// TestSearch_ExtractionLookupFailure: a hit whose markdown path can't be
// read fails the search rather than coming back without one.
func TestSearch_ExtractionLookupFailure(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{},
		func(d *Deps) { d.Extractions = failingExtractionLookup{d.Extractions} })
	p := assertProblem(t, s.search(t, `{"query":"kafka"}`), http.StatusInternalServerError)
	assert.Contains(t, p.Detail, errInjected.Error())
}
