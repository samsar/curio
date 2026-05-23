package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOllama_Validation(t *testing.T) {
	_, err := NewOllama(OllamaOptions{})
	require.Error(t, err, "model required")

	_, err = NewOllama(OllamaOptions{Model: "x"})
	require.Error(t, err, "dim required")

	o, err := NewOllama(OllamaOptions{Model: "x", Dim: 1})
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:11434", o.baseURL)
	assert.Equal(t, "x", o.Model())
	assert.Equal(t, 1, o.Dimensions())
}

func TestOllama_Embed_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/embed", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req ollamaEmbedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "nomic-embed-text", req.Model)
		assert.Equal(t, []string{"hello", "world"}, req.Input)

		resp := ollamaEmbedResponse{
			Model: "nomic-embed-text",
			Embeddings: [][]float32{
				make([]float32, 4),
				make([]float32, 4),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer srv.Close()

	o, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "nomic-embed-text", Dim: 4})
	require.NoError(t, err)

	vecs, err := o.Embed(context.Background(), []string{"hello", "world"})
	require.NoError(t, err)
	require.Len(t, vecs, 2)
	assert.Len(t, vecs[0], 4)
}

func TestOllama_Embed_EmptyInput(t *testing.T) {
	o, _ := NewOllama(OllamaOptions{Model: "x", Dim: 4})
	out, err := o.Embed(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestOllama_Embed_DimensionMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 3-dim vectors even though caller expects 4.
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Embeddings: [][]float32{{1, 2, 3}},
		})
	}))
	defer srv.Close()

	o, _ := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "x", Dim: 4})
	_, err := o.Embed(context.Background(), []string{"hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dim 3, expected 4")
}

func TestOllama_Embed_CountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Embeddings: [][]float32{{1, 2, 3, 4}}, // returns 1 for 2 inputs
		})
	}))
	defer srv.Close()

	o, _ := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "x", Dim: 4})
	_, err := o.Embed(context.Background(), []string{"a", "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requested 2 embeddings, got 1")
}

func TestOllama_Embed_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not loaded"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	o, _ := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "x", Dim: 4})
	_, err := o.Embed(context.Background(), []string{"hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
	assert.Contains(t, err.Error(), "model not loaded")
}
