package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/textutil"
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
	Model   string        // a chat/instruct model, e.g. "qwen3:4b-instruct"
	Timeout time.Duration // per-request; default 120s (generation is slow)
	// NumCtx is sent as options.num_ctx on every attempt; default 8192.
	// Without it Ollama sizes the context from the free VRAM, which for a
	// model with a 256K window can be far more memory than a label needs.
	NumCtx  int
	Retries int // extra attempts after the first; 0 = default 2, negative = none
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

// Ping checks that Ollama is reachable and the configured model is
// available, from its model listing; see ollama.Client.Ping. A model
// Ollama doesn't have is ErrModelMissing.
func (o *Ollama) Ping(ctx context.Context) error { return classify(o.client.Ping(ctx)) }

// Generate posts a single non-streaming completion request, retrying transient
// failures (see retryable) with a bounded backoff.
func (o *Ollama) Generate(ctx context.Context, prompt string, opts Options) (string, error) {
	resp, err := o.generate(ctx, o.request(prompt, opts))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Response), nil
}

// GenerateJSON posts a completion request whose format is schema, so Ollama
// decodes the reply under it, retrying as Generate does. See
// JSONGenerator.
func (o *Ollama) GenerateJSON(ctx context.Context, prompt string, schema json.RawMessage, opts Options) (
	json.RawMessage, error) {
	if opts.Temperature == nil {
		opts.Temperature = new(0.0)
	}
	req := o.request(prompt, opts)
	req.Format = schema
	resp, err := o.generate(ctx, req)
	if err != nil {
		return nil, err
	}
	reply := strings.TrimSpace(resp.Response)
	// Checked first: a reply cut off mid-value is no JSON either, and the
	// limit is what to say.
	if resp.DoneReason == doneLength {
		return nil, fmt.Errorf("ollama generate: %w (%d tokens)", ErrTruncated, opts.MaxTokens)
	}
	if !json.Valid([]byte(reply)) {
		return nil, fmt.Errorf("ollama generate: %w: %q", ErrNotJSON, textutil.TruncateBytes(reply, maxQuotedReply))
	}
	return json.RawMessage(reply), nil
}

// Loaded reports whether Ollama holds the model in memory now; see
// ollama.Client.Loaded.
func (o *Ollama) Loaded(ctx context.Context) (bool, error) {
	loaded, err := o.client.Loaded(ctx)
	return loaded, classify(err)
}

// Warm asks Ollama to load the model, a request without a prompt, with the
// num_ctx every completion of this generator sends: Ollama loads a model
// again for a request whose num_ctx differs from the one it runs with. It
// keeps the model keepAlive after. One attempt: whoever warms decides when
// to try again.
func (o *Ollama) Warm(ctx context.Context, keepAlive time.Duration) error {
	req := warmRequest{Model: o.client.Model(), KeepAlive: keepAliveParam(keepAlive),
		Options: generateOptions{NumCtx: o.numCtx}}
	var resp generateResponse
	if err := o.client.PostJSON(ctx, "/api/generate", req, &resp, maxReplyBytes); err != nil {
		return classify(fmt.Errorf("ollama load %s: %w", o.client.Model(), err))
	}
	if resp.DoneReason != doneLoad {
		return fmt.Errorf("ollama load %s: the reply is done_reason %q, not %q", o.client.Model(), resp.DoneReason,
			doneLoad)
	}
	return nil
}

// Unload asks Ollama to free the model now: a request without a prompt
// whose keep_alive is 0, which Ollama answers once the model is out of
// memory. One attempt.
func (o *Ollama) Unload(ctx context.Context) error {
	req := unloadRequest{Model: o.client.Model()}
	var resp generateResponse
	if err := o.client.PostJSON(ctx, "/api/generate", req, &resp, maxReplyBytes); err != nil {
		return classify(fmt.Errorf("ollama unload %s: %w", o.client.Model(), err))
	}
	if resp.DoneReason != doneUnload {
		return fmt.Errorf("ollama unload %s: the reply is done_reason %q, not %q", o.client.Model(),
			resp.DoneReason, doneUnload)
	}
	return nil
}

// request is the completion request for prompt under opts.
func (o *Ollama) request(prompt string, opts Options) generateRequest {
	return generateRequest{
		Model:     o.client.Model(),
		Prompt:    prompt,
		System:    opts.System,
		Stream:    false,
		Think:     false,
		KeepAlive: keepAliveParam(opts.KeepAlive),
		Options: generateOptions{
			NumCtx:      o.numCtx,
			Temperature: opts.Temperature,
			NumPredict:  opts.MaxTokens,
		},
	}
}

// generate posts req to /api/generate, retrying transient failures (see
// retryable) with a bounded backoff.
func (o *Ollama) generate(ctx context.Context, req generateRequest) (generateResponse, error) {
	for attempt := 1; ; attempt++ {
		var resp generateResponse
		err := o.client.PostJSON(ctx, "/api/generate", req, &resp, maxReplyBytes)
		if err == nil {
			return resp, nil
		}
		err = fmt.Errorf("ollama generate: %w", err)
		if attempt > o.retries || !retryable(ctx, err) {
			return generateResponse{}, classify(err)
		}
		select {
		case <-ctx.Done():
			return generateResponse{}, classify(fmt.Errorf("ollama generate: %w (last attempt: %w)", ctx.Err(), err))
		case <-time.After(o.backoff(attempt)):
		}
	}
}

// keepAliveParam is d as Ollama's keep_alive reads it, a Go duration
// ("30m0s"); "" for 0, which leaves the field out and the default to
// Ollama.
func keepAliveParam(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
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

// classify makes err, an Ollama call's failure, match the provider-neutral
// sentinel it is (generator.ErrUnreachable and the others), its message
// unchanged; nil stays nil, and a failure none fits stays as it is.
func classify(err error) error {
	var kind error
	var se *ollama.StatusError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ollama.ErrModelNotLoaded):
		kind = ErrModelMissing
	case errors.Is(err, ollama.ErrUnreachable):
		kind = ErrUnreachable
	case errors.As(err, &se):
		kind = statusKind(se.Code)
	}
	if kind == nil {
		return err
	}
	return &classified{err: err, kind: kind}
}

// statusKind is the sentinel an HTTP status Ollama answered with is.
func statusKind(code int) error {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusPaymentRequired || code == http.StatusForbidden:
		return ErrUnauthorized
	case code == http.StatusNotFound:
		return ErrModelMissing
	case code == http.StatusRequestTimeout || code == http.StatusTooManyRequests ||
		code >= http.StatusInternalServerError:
		return ErrUnavailable
	case code >= http.StatusBadRequest:
		return ErrBadRequest
	}
	return nil
}

// classified is an Ollama failure that also matches the sentinel kind,
// saying only what it said.
type classified struct {
	err  error
	kind error
}

func (c *classified) Error() string   { return c.err.Error() }
func (c *classified) Unwrap() []error { return []error{c.err, c.kind} }

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	System string `json:"system,omitempty"`
	Stream bool   `json:"stream"`
	// Think is always false, and always sent: Ollama turns thinking on for
	// a model that supports it when the field is missing, and the reasoning
	// then spends the caller's token budget (NumPredict) and time, leaving
	// an empty or unparseable reply. A model that can't turn thinking off
	// answers 4xx, which the insight engine meets with term labels.
	Think bool `json:"think"`
	// Format is the JSON schema the reply is decoded under, for
	// GenerateJSON.
	Format    json.RawMessage `json:"format,omitempty"`
	KeepAlive string          `json:"keep_alive,omitempty"`
	Options   generateOptions `json:"options"`
}

// warmRequest loads the model: a generate request without a prompt.
type warmRequest struct {
	Model     string          `json:"model"`
	Stream    bool            `json:"stream"`
	KeepAlive string          `json:"keep_alive,omitempty"`
	Options   generateOptions `json:"options"`
}

// unloadRequest frees the model: a generate request without a prompt whose
// keep_alive is 0, which must be sent, not left out.
type unloadRequest struct {
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	KeepAlive int    `json:"keep_alive"`
}

type generateOptions struct {
	NumCtx int `json:"num_ctx,omitempty"`
	// Temperature is a pointer so that 0, greedy decoding, is sent; nil
	// leaves the model's default.
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  int      `json:"num_predict,omitempty"`
}

type generateResponse struct {
	Response   string `json:"response"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason"`
}

// Ollama's done_reason values the generator reads: the reply stopped at
// num_predict, a request without a prompt loaded the model, and one with
// keep_alive 0 unloaded it.
const (
	doneLength = "length"
	doneLoad   = "load"
	doneUnload = "unload"
)

// maxQuotedReply bounds how much of a reply that isn't JSON its error
// quotes.
const maxQuotedReply = 120
