package generator

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

// Sentinel errors so callers can format actionable messages (mirrors
// internal/embedder).
var (
	ErrOllamaUnreachable = errors.New("ollama unreachable")
	ErrModelNotLoaded    = errors.New("model not loaded")
)

// Ollama is a Generator backed by a local (or remote) Ollama server, using the
// /api/generate endpoint (single-turn completion, non-streaming).
//
// Unlike the embedder, generation is slow and occasionally flaky (the model
// may still be loading), so this client retries with a bounded backoff.
type Ollama struct {
	baseURL string
	model   string
	numCtx  int
	retries int
	client  *http.Client
}

// OllamaOptions configures a new Ollama generator.
type OllamaOptions struct {
	BaseURL string        // e.g. "http://localhost:11434"
	Model   string        // a chat/instruct model, e.g. "llama3.2"
	Timeout time.Duration // per-request; default 120s (generation is slow)
	NumCtx  int           // context window; default 8192
	Retries int           // extra attempts after the first; default 2
}

// NewOllama constructs an Ollama generator. It does NOT contact the server;
// the first Generate/Ping call surfaces connection errors.
func NewOllama(opts OllamaOptions) (*Ollama, error) {
	if opts.Model == "" {
		return nil, fmt.Errorf("ollama generator: model required")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "http://localhost:11434"
	}
	if _, err := url.Parse(opts.BaseURL); err != nil {
		return nil, fmt.Errorf("ollama generator: bad base_url: %w", err)
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	numCtx := opts.NumCtx
	if numCtx == 0 {
		numCtx = 8192
	}
	retries := opts.Retries
	if retries == 0 {
		retries = 2
	}
	return &Ollama{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		model:   opts.Model,
		numCtx:  numCtx,
		retries: retries,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

func (o *Ollama) Model() string { return o.model }

// Ping checks that Ollama is reachable and the configured model is available.
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
	for _, m := range parsed.Models {
		if m.Name == o.model || m.Model == o.model ||
			strings.HasPrefix(m.Name, o.model+":") ||
			strings.HasPrefix(m.Model, o.model+":") {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrModelNotLoaded, o.model)
}

// Generate posts a single non-streaming completion request, retrying transient
// failures with a bounded backoff. Context cancellation is never retried.
func (o *Ollama) Generate(ctx context.Context, prompt string, opts Options) (string, error) {
	body, err := json.Marshal(ollamaGenerateRequest{
		Model:  o.model,
		Prompt: prompt,
		System: opts.System,
		Stream: false,
		Options: ollamaGenOptions{
			NumCtx:      o.numCtx,
			Temperature: opts.Temperature,
			NumPredict:  opts.MaxTokens,
		},
	})
	if err != nil {
		return "", fmt.Errorf("ollama generate: encode request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= o.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		text, err := o.generateOnce(ctx, body)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			break
		}
	}
	return "", lastErr
}

func (o *Ollama) generateOnce(ctx context.Context, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama generate: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrOllamaUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 2048)
		n, _ := resp.Body.Read(buf)
		return "", fmt.Errorf("ollama generate: HTTP %d: %s", resp.StatusCode, string(buf[:n]))
	}

	var parsed ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("ollama generate: decode response: %w", err)
	}
	return strings.TrimSpace(parsed.Response), nil
}

type ollamaGenerateRequest struct {
	Model   string           `json:"model"`
	Prompt  string           `json:"prompt"`
	System  string           `json:"system,omitempty"`
	Stream  bool             `json:"stream"`
	Options ollamaGenOptions `json:"options"`
}

type ollamaGenOptions struct {
	NumCtx      int     `json:"num_ctx,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}
