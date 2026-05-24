package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Ollama is an Embedder backed by a local (or remote) Ollama server.
//
// Uses the /api/embed endpoint (batched), introduced in Ollama 2024. The
// older /api/embeddings endpoint accepts only one prompt per call; calling
// it per-chunk would dominate indexer wall time, so we don't.
type Ollama struct {
	baseURL string
	model   string
	dim     int
	numCtx  int
	client  *http.Client
}

// OllamaOptions configures a new Ollama embedder.
type OllamaOptions struct {
	BaseURL string        // e.g. "http://localhost:11434"
	Model   string        // e.g. "nomic-embed-text"
	Dim     int           // expected output dimension; validated on first call
	Timeout time.Duration // per-request; default 60s

	// NumCtx overrides the model's context window for embed calls. Ollama
	// defaults to 2048 even for models like nomic-embed-text that support
	// 8192; without this override, large chunks fail with HTTP 400
	// "input length exceeds the context length". Default 8192.
	NumCtx int
}

// NewOllama constructs an Ollama embedder. Does NOT contact the server; the
// first Embed call is what surfaces connection errors. Daemon startup
// pre-flights via a /healthz dependency check separately.
func NewOllama(opts OllamaOptions) (*Ollama, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = "http://localhost:11434"
	}
	if opts.Model == "" {
		return nil, fmt.Errorf("ollama: model required")
	}
	if opts.Dim <= 0 {
		return nil, fmt.Errorf("ollama: dim must be positive")
	}
	if _, err := url.Parse(opts.BaseURL); err != nil {
		return nil, fmt.Errorf("ollama: bad base_url: %w", err)
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	numCtx := opts.NumCtx
	if numCtx == 0 {
		numCtx = 8192
	}
	return &Ollama{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		model:   opts.Model,
		dim:     opts.Dim,
		numCtx:  numCtx,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

func (o *Ollama) Dimensions() int { return o.dim }
func (o *Ollama) Model() string   { return o.model }

// Ping is a cheap reachability check against /api/tags. Returns nil if
// Ollama is reachable AND the configured model is loaded; otherwise
// returns a wrapped sentinel so callers can distinguish the failure
// modes. Caller controls timeout via ctx.
func (o *Ollama) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOllamaUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrOllamaUnreachable, resp.StatusCode)
	}

	var parsed struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("decode /api/tags: %w", err)
	}
	wanted := o.model
	for _, m := range parsed.Models {
		// Ollama returns "nomic-embed-text:latest" or similar; accept
		// any prefix match.
		if m.Name == wanted || m.Model == wanted ||
			strings.HasPrefix(m.Name, wanted+":") ||
			strings.HasPrefix(m.Model, wanted+":") {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrModelNotLoaded, wanted)
}

// Sentinel errors so callers can format actionable messages.
var (
	ErrOllamaUnreachable = errors.New("ollama unreachable")
	ErrModelNotLoaded    = errors.New("model not loaded")
)

// Embed posts texts to /api/embed in a single batched call. Verifies the
// returned vectors have the expected dimension on first call — if Ollama
// happens to swap models server-side, the indexer should error rather than
// silently store mismatched-dim vectors.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(ollamaEmbedRequest{
		Model:   o.model,
		Input:   texts,
		Options: ollamaOptions{NumCtx: o.numCtx},
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read up to 2KB of the body for the error message; Ollama returns
		// useful JSON errors that help debugging.
		buf := make([]byte, 2048)
		n, _ := resp.Body.Read(buf)
		return nil, fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, string(buf[:n]))
	}

	var parsed ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	if len(parsed.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama: requested %d embeddings, got %d",
			len(texts), len(parsed.Embeddings))
	}
	for i, vec := range parsed.Embeddings {
		if len(vec) != o.dim {
			return nil, fmt.Errorf("ollama: embedding[%d] has dim %d, expected %d",
				i, len(vec), o.dim)
		}
	}
	return parsed.Embeddings, nil
}

type ollamaEmbedRequest struct {
	Model   string        `json:"model"`
	Input   []string      `json:"input"`
	Options ollamaOptions `json:"options,omitempty"`
}

type ollamaOptions struct {
	NumCtx int `json:"num_ctx,omitempty"`
}

type ollamaEmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}
