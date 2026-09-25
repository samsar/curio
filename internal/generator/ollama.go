package generator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/ollama"
)

// maxReplyBytes bounds a completion. A label reply is a few hundred bytes;
// anything near this is not a reply we can use.
const maxReplyBytes = ollama.MaxResponseBody

// Ollama is a Generator backed by a local (or remote) Ollama server, using the
// /api/generate endpoint (single-turn completion, non-streaming).
//
// Unlike the embedder, generation is slow and occasionally flaky (the model
// may still be loading), so this client retries transient failures with a
// bounded backoff; see retryable for which failures qualify.
type Ollama struct {
	client  *ollama.Client
	numCtx  int
	retries int
	backoff func(attempt int) time.Duration // wait before retry number attempt (1-based)
}

// OllamaOptions configures a new Ollama generator.
type OllamaOptions struct {
	BaseURL string        // default ollama.DefaultBaseURL
	Model   string        // a chat/instruct model, e.g. "llama3.2"
	Timeout time.Duration // per-request; default 120s (generation is slow)
	NumCtx  int           // context window; default 8192
	Retries int           // extra attempts after the first; 0 = default 2, negative = none
}

// NewOllama constructs an Ollama generator. It does NOT contact the server;
// the first Generate/Ping call surfaces connection errors.
func NewOllama(opts OllamaOptions) (*Ollama, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	numCtx := opts.NumCtx
	if numCtx == 0 {
		numCtx = 8192
	}
	retries := opts.Retries
	switch {
	case retries == 0:
		retries = 2
	case retries < 0:
		retries = 0
	}
	client, err := ollama.New(opts.BaseURL, opts.Model, timeout)
	if err != nil {
		return nil, fmt.Errorf("ollama generator: %w", err)
	}
	return &Ollama{
		client:  client,
		numCtx:  numCtx,
		retries: retries,
		backoff: func(attempt int) time.Duration { return time.Duration(attempt) * 500 * time.Millisecond },
	}, nil
}

func (o *Ollama) Model() string { return o.client.Model() }

// Client is the underlying Ollama client, for keeping the model pulled.
func (o *Ollama) Client() *ollama.Client { return o.client }

// Ping checks that Ollama is reachable and the configured model is available;
// see ollama.Client.Ping.
func (o *Ollama) Ping(ctx context.Context) error { return o.client.Ping(ctx) }

// Generate posts a single non-streaming completion request, retrying transient
// failures (see retryable) with a bounded backoff.
func (o *Ollama) Generate(ctx context.Context, prompt string, opts Options) (string, error) {
	req := generateRequest{
		Model:  o.client.Model(),
		Prompt: prompt,
		System: opts.System,
		Stream: false,
		Options: generateOptions{
			NumCtx:      o.numCtx,
			Temperature: opts.Temperature,
			NumPredict:  opts.MaxTokens,
		},
	}
	for attempt := 1; ; attempt++ {
		var resp generateResponse
		err := o.client.PostJSON(ctx, "/api/generate", req, &resp, maxReplyBytes)
		if err == nil {
			return strings.TrimSpace(resp.Response), nil
		}
		err = fmt.Errorf("ollama generate: %w", err)
		if attempt > o.retries || !retryable(ctx, err) {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("ollama generate: %w (last attempt: %w)", ctx.Err(), err)
		case <-time.After(o.backoff(attempt)):
		}
	}
}

// retryable reports whether a failed attempt is worth repeating: a 5xx (the
// model may still be loading) or a refused or dropped connection. Per-attempt
// timeouts are final — at the default 120 s each, retrying would triple
// exactly the stall a caller needs bounded, and the insight engine already
// falls back to term labels. So are 4xx answers (a missing model won't appear
// on a retry), undecodable replies, and anything once ctx is done.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var se *ollama.StatusError
	if errors.As(err, &se) {
		return se.Code >= http.StatusInternalServerError
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

type generateRequest struct {
	Model   string          `json:"model"`
	Prompt  string          `json:"prompt"`
	System  string          `json:"system,omitempty"`
	Stream  bool            `json:"stream"`
	Options generateOptions `json:"options"`
}

type generateOptions struct {
	NumCtx      int     `json:"num_ctx,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

type generateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}
