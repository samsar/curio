// Package embedder turns text into dense vectors for ANN search.
//
// The Embedder interface is batched even when only one impl exists, because
// the indexer naturally produces many chunks per document and Ollama's HTTP
// API supports batched embeddings — calling per-chunk would dominate the
// indexer's wall time.
package embedder

import (
	"context"
	"errors"
)

// Errors an Embedder returns for a request that fails the same way however
// often it is repeated, so a caller can stop retrying. Use errors.Is.
var (
	// ErrInputTooLong: a text is longer than the model's context, the
	// smaller of num_ctx and the model's own. It is never cut to fit.
	ErrInputTooLong = errors.New("input longer than the embedding model's context")
	// ErrWrongDimension: a vector came back with another width than the
	// configured one, the home's, which the vector index can't store.
	ErrWrongDimension = errors.New("embedding has the wrong dimension")
)

// Embedder converts text to vectors.
type Embedder interface {
	// Embed returns one vector per input text, in the same order. All
	// vectors share the dimensionality returned by Dimensions().
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Dimensions returns the embedding length: the home's width, which
	// its marker records and its vector index is sized to.
	Dimensions() int

	// Model is the model identifier (e.g., "qwen3-embedding:0.6b"), the
	// one the home's marker records.
	Model() string
}
