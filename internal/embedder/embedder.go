// Package embedder turns text into dense vectors for ANN search.
//
// The Embedder interface is batched even when only one impl exists, because
// the indexer naturally produces many chunks per document and Ollama's HTTP
// API supports batched embeddings — calling per-chunk would dominate the
// indexer's wall time.
package embedder

import "context"

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
