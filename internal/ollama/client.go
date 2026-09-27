// Package ollama is curio's client for the Ollama HTTP API: base-URL
// validation, the model check behind Ping, the server's version and the
// model's digest, pulling a missing model and keeping it pulled, and
// bounded JSON requests. internal/embedder and internal/generator build
// their endpoint-specific calls on one Client each, so both report
// failures through the same sentinels.
package ollama

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
	"time"
)

// DefaultBaseURL is where a local Ollama listens.
const DefaultBaseURL = "http://localhost:11434"

// Sentinels shared by every Ollama call, so callers such as /v1/healthz can
// turn a failure into advice without knowing which client failed.
var (
	// ErrUnreachable: the request never got an answer, or /api/tags answered
	// with an error status. The transport error stays in the chain.
	ErrUnreachable = errors.New("ollama unreachable")
	// ErrModelNotLoaded: Ollama answered but doesn't have the model, either
	// missing from /api/tags or a 404 from an endpoint that names it.
	ErrModelNotLoaded = errors.New("model not loaded")
)

const (
	// maxErrorBody bounds how much of a failed response is quoted in errors.
	maxErrorBody = 2 << 10
	// MaxResponseBody bounds a model listing or a completion. Callers whose
	// replies grow with the request, like a batch of embeddings, size their
	// own limit.
	MaxResponseBody = 1 << 20
)

// StatusError is a non-2xx answer from Ollama. Body holds the start of the
// response, which carries Ollama's JSON error message.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body) }

// Client talks to one Ollama server about one model.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
	// pullBackoff is how long KeepPulled waits before retry n (1-based).
	pullBackoff func(retry int) time.Duration
}

// New returns a Client for model at baseURL (DefaultBaseURL when empty),
// whose requests time out after timeout (0: no limit beyond the caller's
// context). It does not contact the server.
func New(baseURL, model string, timeout time.Duration) (*Client, error) {
	if model == "" {
		return nil, errors.New("model required")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("bad base_url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("bad base_url %q: want http(s)://host[:port]", baseURL)
	}
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		model:       model,
		http:        &http.Client{Timeout: timeout},
		pullBackoff: pullBackoff,
	}, nil
}

// Model is the model this client asks for.
func (c *Client) Model() string { return c.model }

// BaseURL is the server's address, without a trailing slash.
func (c *Client) BaseURL() string { return c.baseURL }

// Ping reports whether Ollama is reachable and has the model: nil, or an
// error wrapping ErrUnreachable or ErrModelNotLoaded. The model must be
// pulled under the very name Ollama would run for it: names compare case
// insensitively, and an untagged name means name:latest, as Ollama reads
// it. So "nomic-embed-text" matches "nomic-embed-text:latest", but
// "qwen3-embedding" is not loaded when only "qwen3-embedding:0.6b" is. The
// caller bounds it with ctx.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.lookup(ctx)
	return err
}

// tagEntry is one model /api/tags lists.
type tagEntry struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	Digest string `json:"digest"`
}

// lookup finds the model in GET /api/tags, matching each entry's name and
// model against it (see Ping).
func (c *Client) lookup(ctx context.Context) (tagEntry, error) {
	resp, err := c.send(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return tagEntry{}, err
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return tagEntry{}, fmt.Errorf("%w: /api/tags: %w", ErrUnreachable, statusError(resp))
	}
	var tags struct {
		Models []tagEntry `json:"models"`
	}
	if err := decodeJSON(resp.Body, &tags, MaxResponseBody); err != nil {
		return tagEntry{}, fmt.Errorf("/api/tags: %w", err)
	}
	want := normalizeModel(c.model)
	for _, m := range tags.Models {
		if normalizeModel(m.Name) == want || normalizeModel(m.Model) == want {
			return m, nil
		}
	}
	return tagEntry{}, fmt.Errorf("%w: %s", ErrModelNotLoaded, c.model)
}

// ModelDigest is the manifest digest of the model as Ollama has it pulled,
// the one `ollama list` shows, read from GET /api/tags. It changes when a
// pull brings a different build of the model. A model that isn't pulled is
// ErrModelNotLoaded.
func (c *Client) ModelDigest(ctx context.Context) (string, error) {
	m, err := c.lookup(ctx)
	if err != nil {
		return "", err
	}
	if m.Digest == "" {
		return "", fmt.Errorf("/api/tags lists %s without a digest", m.Name)
	}
	return m.Digest, nil
}

// Version is the Ollama server's version, from GET /api/version.
func (c *Client) Version(ctx context.Context) (string, error) {
	resp, err := c.send(ctx, http.MethodGet, "/api/version", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return "", fmt.Errorf("%w: /api/version: %w", ErrUnreachable, statusError(resp))
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(resp.Body, &v, MaxResponseBody); err != nil {
		return "", fmt.Errorf("/api/version: %w", err)
	}
	if v.Version == "" {
		return "", errors.New("/api/version: the reply has no version")
	}
	return v.Version, nil
}

// normalizeModel is the name Ollama runs for name: lower-cased, with
// ":latest" when it has no tag. The tag follows a ':' in the last path
// segment; a ':' before the last '/' belongs to a registry host's port.
func normalizeModel(name string) string {
	name = strings.ToLower(name)
	if !strings.Contains(name[strings.LastIndex(name, "/")+1:], ":") {
		name += ":latest"
	}
	return name
}

// PostJSON posts in as JSON to path and decodes a 2xx reply of at most
// maxBody bytes into out. A non-2xx reply is a *StatusError, which also
// wraps ErrModelNotLoaded for a 404: Ollama's answer when the model named in
// the request isn't pulled.
func (c *Client) PostJSON(ctx context.Context, path string, in, out any, maxBody int64) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	resp, err := c.send(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !ok(resp) {
		err := statusError(resp)
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %w", ErrModelNotLoaded, err)
		}
		return err
	}
	return decodeJSON(resp.Body, out, maxBody)
}

// send makes one request. A transport failure wraps ErrUnreachable and its
// cause, so errors.Is sees both (syscall.ECONNREFUSED,
// context.DeadlineExceeded, ...).
func (c *Client) send(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	return c.sendWith(ctx, c.http, method, path, body)
}

func (c *Client) sendWith(ctx context.Context, hc *http.Client, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return resp, nil
}

func ok(resp *http.Response) bool {
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// statusError describes a non-2xx response, quoting at most maxErrorBody
// bytes of its body.
func statusError(resp *http.Response) error {
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var err error = &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	if rerr != nil {
		err = fmt.Errorf("%w (reading body: %w)", err, rerr)
	}
	return err
}

// decodeJSON decodes a reply of at most maxBody bytes. A longer one is
// rejected rather than cut short, so it reads as an oversized reply, not as
// a connection that dropped mid-stream.
func decodeJSON(body io.Reader, v any, maxBody int64) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxBody+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if int64(len(raw)) > maxBody {
		return fmt.Errorf("response exceeds %d bytes", maxBody)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// EnsureModel makes sure the model is available, pulling it if Ollama
// reports it missing, and blocks until the pull finishes. It returns Ping's
// error for anything else: an unreachable Ollama has nothing to pull to.
func (c *Client) EnsureModel(ctx context.Context, log *slog.Logger) error {
	return c.ensureModel(ctx, log, slog.LevelInfo)
}

// ensureModel is EnsureModel announcing the start of a pull at level.
func (c *Client) ensureModel(ctx context.Context, log *slog.Logger, level slog.Level) error {
	err := c.Ping(ctx)
	if !errors.Is(err, ErrModelNotLoaded) {
		return err
	}
	log.Log(ctx, level, "pulling ollama model", "model", c.model)
	return c.Pull(ctx, log)
}

// KeepPulled runs EnsureModel until the model is ready or ctx ends, waiting
// between attempts with a capped exponential backoff: Ollama is often
// started after the daemon, and a model that is never pulled leaves every
// index job or LLM label failing. The first attempt announces its pull at
// INFO and its failure at WARN; retries repeat both at DEBUG, so an Ollama
// that can't reach its registry doesn't log at every retry. Success is INFO.
// Nothing is logged once ctx is done, since an interrupted pull is shutdown,
// not a missing model.
func (c *Client) KeepPulled(ctx context.Context, log *slog.Logger) {
	announce, failure := slog.LevelInfo, slog.LevelWarn
	for retry := 1; ; retry++ {
		err := c.ensureModel(ctx, log, announce)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			log.Info("ollama model ready", "model", c.model)
			return
		}
		wait := c.pullBackoff(retry)
		log.Log(ctx, failure, "ollama model not ready; retrying in the background",
			"model", c.model, "err", err, "retry_in", wait)
		announce, failure = slog.LevelDebug, slog.LevelDebug

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

const (
	firstPullRetry = 5 * time.Second
	maxPullRetry   = 5 * time.Minute
)

// pullBackoff waits 5s before the first retry, doubling up to 5 minutes.
func pullBackoff(retry int) time.Duration {
	wait := firstPullRetry
	for range retry - 1 {
		if wait >= maxPullRetry/2 {
			return maxPullRetry
		}
		wait *= 2
	}
	return wait
}
