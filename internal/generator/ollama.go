package generator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/ollama"
)

// Sentinel errors so callers can format actionable messages (mirrors
// internal/embedder).
var (
	ErrOllamaUnreachable = errors.New("ollama unreachable")
	ErrModelNotLoaded    = errors.New("model not loaded")
)

// StatusError is a non-200 answer from Ollama. Body holds the start of the
// response, which carries Ollama's JSON error message.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body) }

const (
	// maxErrorBody bounds how much of a failed response is quoted in errors.
	maxErrorBody = 2 << 10
	// maxResponseBody bounds a completion or model listing. A label reply
	// is a few hundred bytes; anything near this is not a reply we can use.
	maxResponseBody = 1 << 20
)

// Ollama is a Generator backed by a local (or remote) Ollama server, using the
// /api/generate endpoint (single-turn completion, non-streaming).
//
// Unlike the embedder, generation is slow and occasionally flaky (the model
// may still be loading), so this client retries transient failures with a
// bounded backoff; see retryable for which failures qualify.
type Ollama struct {
	baseURL string
	model   string
	numCtx  int
	retries int
	backoff func(attempt int) time.Duration // wait before retry number attempt (1-based)
	client  *http.Client
}

// OllamaOptions configures a new Ollama generator.
type OllamaOptions struct {
	BaseURL string        // e.g. "http://localhost:11434"
	Model   string        // a chat/instruct model, e.g. "llama3.2"
	Timeout time.Duration // per-request; default 120s (generation is slow)
	NumCtx  int           // context window; default 8192
	Retries int           // extra attempts after the first; 0 = default 2, negative = none
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
	switch {
	case retries == 0:
		retries = 2
	case retries < 0:
		retries = 0
	}
	return &Ollama{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		model:   opts.Model,
		numCtx:  numCtx,
		retries: retries,
		backoff: func(attempt int) time.Duration { return time.Duration(attempt) * 500 * time.Millisecond },
		client:  &http.Client{Timeout: timeout},
	}, nil
}

func (o *Ollama) Model() string { return o.model }

// Ping checks that Ollama is reachable and the configured model is available.
func (o *Ollama) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("ollama ping: new request: %w", err)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOllamaUnreachable, err)
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
	if err := readJSON(resp.Body, &parsed); err != nil {
		return fmt.Errorf("ollama ping: /api/tags: %w", err)
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

// EnsureModel makes sure the model is available locally, pulling it from the
// Ollama registry if it isn't. It blocks until the pull finishes. A no-op when
// the model is already present; returns the underlying error when Ollama is
// unreachable (nothing to pull to).
func (o *Ollama) EnsureModel(ctx context.Context, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	err := o.Ping(ctx)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrOllamaUnreachable) {
		return err
	}
	log.Info("pulling generation model", "model", o.model)
	if perr := ollama.PullModel(ctx, o.baseURL, o.model, log); perr != nil {
		return perr
	}
	log.Info("generation model ready", "model", o.model)
	return nil
}

// Generate posts a single non-streaming completion request, retrying transient
// failures (see retryable) with a bounded backoff.
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

	for attempt := 1; ; attempt++ {
		text, err := o.generateOnce(ctx, body)
		if err == nil {
			return text, nil
		}
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
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code >= http.StatusInternalServerError
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
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
		return "", fmt.Errorf("%w: %w", ErrOllamaUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama generate: %w", statusError(resp))
	}
	var parsed ollamaGenerateResponse
	if err := readJSON(resp.Body, &parsed); err != nil {
		return "", fmt.Errorf("ollama generate: %w", err)
	}
	return strings.TrimSpace(parsed.Response), nil
}

// statusError describes a non-200 response. Ollama answers 404 when the model
// isn't pulled, so a 404 also wraps ErrModelNotLoaded.
func statusError(resp *http.Response) error {
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var err error = &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	if rerr != nil {
		err = fmt.Errorf("%w (reading body: %w)", err, rerr)
	}
	if resp.StatusCode == http.StatusNotFound {
		err = fmt.Errorf("%w: %w", ErrModelNotLoaded, err)
	}
	return err
}

// readJSON decodes a successful response of at most maxResponseBody bytes. A
// longer body is rejected rather than cut short, so it reads as an oversized
// reply, not as a connection that dropped mid-stream.
func readJSON(body io.Reader, v any) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxResponseBody+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(raw) > maxResponseBody {
		return fmt.Errorf("response exceeds %d bytes", maxResponseBody)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
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
