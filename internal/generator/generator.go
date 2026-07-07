// Package generator defines curio's LLM text-generation interface and its
// implementations. It is deliberately separate from internal/embedder:
// embeddings and generation use different models and endpoints, and most of
// curio needs neither.
//
// The insight layer (M4) uses a Generator to label clusters. M6 (RAG / SOTA
// search) will reuse the same interface for answer synthesis and query
// rewriting, so the abstraction is intentionally small and provider-agnostic —
// a local Ollama chat model today, an Anthropic/Claude impl later, both behind
// this interface.
package generator

import "context"

// Options tunes a single generation call.
type Options struct {
	// Temperature controls sampling randomness. Lower is more deterministic;
	// labeling/extraction want a low value. Zero uses the provider default.
	Temperature float64
	// MaxTokens caps the generated length (Ollama num_predict). Zero uses the
	// model default.
	MaxTokens int
	// System is an optional system prompt / instruction.
	System string
}

// Generator produces text from a prompt. Implementations are safe for
// concurrent use.
type Generator interface {
	// Generate returns the model's completion for prompt. The returned text is
	// trimmed of surrounding whitespace.
	Generate(ctx context.Context, prompt string, opts Options) (string, error)
	// Model returns the configured model name (for logging / diagnostics).
	Model() string
	// Ping is a cheap reachability + model-loaded check.
	Ping(ctx context.Context) error
}
