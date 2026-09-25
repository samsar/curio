package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/ollama"
)

func TestNewOllama_Validation(t *testing.T) {
	_, err := NewOllama(OllamaOptions{Dim: 1})
	require.Error(t, err, "model required")

	_, err = NewOllama(OllamaOptions{Model: "x"})
	require.Error(t, err, "dim required")

	_, err = NewOllama(OllamaOptions{Model: "x", Dim: 1, BaseURL: "localhost:11434"})
	require.Error(t, err, "base URL without a scheme")

	o, err := NewOllama(OllamaOptions{Model: "x", Dim: 1})
	require.NoError(t, err)
	assert.Equal(t, ollama.DefaultBaseURL, o.Client().BaseURL())
	assert.Equal(t, "x", o.Model())
	assert.Equal(t, 1, o.Dimensions())
}

func TestOllama_Embed_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/embed", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req embedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "nomic-embed-text", req.Model)
		assert.Equal(t, []string{"hello", "world"}, req.Input)

		resp := embedResponse{
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Return 3-dim vectors even though caller expects 4.
		_ = json.NewEncoder(w).Encode(embedResponse{
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(embedResponse{
			Embeddings: [][]float32{{1, 2, 3, 4}}, // returns 1 for 2 inputs
		})
	}))
	defer srv.Close()

	o, _ := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "x", Dim: 4})
	_, err := o.Embed(context.Background(), []string{"a", "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requested 2 embeddings, got 1")
}

// TestOllama_Embed_ModelNotPulled: Ollama answers 404 for a model it
// doesn't have, which healthz and the logs report as a missing model.
func TestOllama_Embed_ModelNotPulled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"model \"x\" not found, try pulling it first"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	o, _ := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "x", Dim: 4})
	_, err := o.Embed(context.Background(), []string{"hi"})
	require.ErrorIs(t, err, ollama.ErrModelNotLoaded)
	assert.Contains(t, err.Error(), "try pulling it first")
}

// TestOllama_ClosedPort: a refused connection is ErrUnreachable with the
// syscall error kept in the chain, for Embed and Ping alike.
func TestOllama_ClosedPort(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing listens on the port any more

	o, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "x", Dim: 4})
	require.NoError(t, err)

	_, err = o.Embed(context.Background(), []string{"hi"})
	require.ErrorIs(t, err, ollama.ErrUnreachable)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)

	err = o.Ping(context.Background())
	require.ErrorIs(t, err, ollama.ErrUnreachable)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
}

// TestOllama_Embed_ReplyBoundedByBatch: the reply limit grows with the
// batch, so a full batch of real-size vectors fits and a runaway reply
// doesn't.
func TestOllama_Embed_ReplyBoundedByBatch(t *testing.T) {
	const dim = 768
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = -0.0123456789 // near the longest JSON a float32 gets
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := embedResponse{}
		for range req.Input {
			resp.Embeddings = append(resp.Embeddings, vec)
		}
		if len(req.Input) == 1 {
			// A runaway reply: far more vectors than were asked for.
			for range 200 {
				resp.Embeddings = append(resp.Embeddings, vec)
			}
		}
		body, err := json.Marshal(resp)
		if !assert.NoError(t, err) {
			return
		}
		_, _ = w.Write(body) // the client hangs up on a runaway reply at its limit
	}))
	defer srv.Close()
	o, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "x", Dim: dim})
	require.NoError(t, err)

	batch := make([]string, 32)
	vecs, err := o.Embed(context.Background(), batch)
	require.NoError(t, err)
	assert.Len(t, vecs, 32)

	_, err = o.Embed(context.Background(), []string{"one"})
	require.ErrorContains(t, err, "exceeds")
}

func TestOllama_Embed_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"model not loaded"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	o, _ := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "x", Dim: 4})
	_, err := o.Embed(context.Background(), []string{"hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
	assert.Contains(t, err.Error(), "model not loaded")
}
