package embedder

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samsar/curio/internal/ollama"
)

// Ollama is an Embedder backed by a local (or remote) Ollama server.
//
// It uses the batched /api/embed endpoint. The older /api/embeddings takes
// one prompt per call; calling it per chunk would dominate indexer wall
// time.
type Ollama struct {
	client *ollama.Client
	dim    int
	numCtx int
}

// OllamaOptions configures a new Ollama embedder.
type OllamaOptions struct {
	BaseURL string        // default ollama.DefaultBaseURL
	Model   string        // e.g. "nomic-embed-text"
	Dim     int           // expected output dimension; every reply is checked against it
	Timeout time.Duration // per-request; default 60s

	// NumCtx is sent as options.num_ctx on embed calls. It is advisory:
	// Ollama clamps it to the model's own context length, which is 2048
	// tokens for nomic-embed-text (its GGUF metadata), so the chunker's
	// byte cap is what keeps inputs in range. See decisions.md. Default 8192.
	NumCtx int
}

// NewOllama constructs an Ollama embedder. It does not contact the server:
// the first Embed or Ping surfaces connection errors, and /v1/healthz pings
// on every request.
func NewOllama(opts OllamaOptions) (*Ollama, error) {
	if opts.Dim <= 0 {
		return nil, errors.New("ollama embedder: dim must be positive")
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	numCtx := opts.NumCtx
	if numCtx == 0 {
		numCtx = 8192
	}
	client, err := ollama.New(opts.BaseURL, opts.Model, timeout)
	if err != nil {
		return nil, fmt.Errorf("ollama embedder: %w", err)
	}
	return &Ollama{client: client, dim: opts.Dim, numCtx: numCtx}, nil
}

func (o *Ollama) Dimensions() int { return o.dim }
func (o *Ollama) Model() string   { return o.client.Model() }

// Client is the underlying Ollama client, for keeping the model pulled.
func (o *Ollama) Client() *ollama.Client { return o.client }

// Ping reports whether Ollama is reachable with the model pulled; see
// ollama.Client.Ping.
func (o *Ollama) Ping(ctx context.Context) error { return o.client.Ping(ctx) }

// maxEmbedReplyBytes bounds the /api/embed reply for n texts: at most 32
// bytes of JSON per vector component (a float in shortest form plus its
// comma needs at most 25) and 64 KiB for everything else.
func (o *Ollama) maxEmbedReplyBytes(n int) int64 {
	return int64(n)*int64(o.dim)*32 + 64<<10
}

// Embed posts texts to /api/embed in one batched call and checks that the
// reply has one vector of the configured dimension per text: if Ollama
// swaps models server-side, the indexer errors rather than storing vectors
// of the wrong size.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	var parsed embedResponse
	err := o.client.PostJSON(ctx, "/api/embed", embedRequest{
		Model:   o.client.Model(),
		Input:   texts,
		Options: embedOptions{NumCtx: o.numCtx},
	}, &parsed, o.maxEmbedReplyBytes(len(texts)))
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}

	if len(parsed.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embed: requested %d embeddings, got %d",
			len(texts), len(parsed.Embeddings))
	}
	for i, vec := range parsed.Embeddings {
		if len(vec) != o.dim {
			return nil, fmt.Errorf("ollama embed: embedding[%d] has dim %d, expected %d",
				i, len(vec), o.dim)
		}
	}
	return parsed.Embeddings, nil
}

type embedRequest struct {
	Model   string       `json:"model"`
	Input   []string     `json:"input"`
	Options embedOptions `json:"options,omitzero"`
}

type embedOptions struct {
	NumCtx int `json:"num_ctx,omitempty"`
}

type embedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}
