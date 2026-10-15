// Package generator defines curio's LLM text-generation interface and its
// Ollama implementation. It is deliberately separate from internal/embedder:
// embeddings and generation use different models and endpoints, and most of
// curio needs neither.
//
// The insight engine uses a Generator to label clusters, and search's
// planner a JSONGenerator to understand a question. The interfaces are small
// and provider-agnostic, so another provider can sit behind them without
// changing their callers. A provider that can't hold a reply to a JSON
// schema must not implement JSONGenerator: callers type-assert it, and none
// falls back to parsing Generate's prose.
package generator

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Options tunes a single generation call.
type Options struct {
	// Temperature controls sampling randomness: nil uses the provider's
	// default, and 0 decodes greedily, the same answer every time. Lower is
	// more deterministic; labeling and extraction want a low value.
	Temperature *float64
	// MaxTokens caps the generated length (Ollama num_predict). Zero uses the
	// model default.
	MaxTokens int
	// System is an optional system prompt / instruction.
	System string
	// KeepAlive is how long the provider keeps the model loaded after the
	// call. Zero uses the provider default (Ollama's is 5 minutes).
	KeepAlive time.Duration
}

// Errors of a structured reply the model gave but that can't be used.
var (
	// ErrTruncated: the reply stopped at MaxTokens before it was complete.
	ErrTruncated = errors.New("the reply was cut off at its token limit")
	// ErrNotJSON: the reply isn't one JSON value.
	ErrNotJSON = errors.New("the reply isn't JSON")
)

// What a call that got no reply says, whatever the provider: each
// provider's failures match one of these (errors.Is) while keeping their
// own message, so a caller decides what a failure means of the model
// without knowing whose it is. A failure that matches none says nothing
// of the model either, and a caller must treat it like ErrUnavailable,
// never as the model's fault.
var (
	// ErrUnreachable: the provider didn't answer: down, refusing
	// connections, or slower than the call's timeout.
	ErrUnreachable = errors.New("the model's provider didn't answer")
	// ErrModelMissing: the provider doesn't have the model (Ollama: not
	// pulled).
	ErrModelMissing = errors.New("the provider doesn't have the model")
	// ErrUnauthorized: the provider refuses the caller, not the request:
	// a missing or wrong key, an account out of credit (401, 402, 403).
	ErrUnauthorized = errors.New("the model's provider refuses the caller")
	// ErrUnavailable: the provider can't serve the call now: rate limited,
	// overloaded or failing (408, 429, 5xx). A retry later may succeed.
	ErrUnavailable = errors.New("the model's provider can't serve the call now")
	// ErrBadRequest: the provider refuses the request as it was made, and
	// would refuse it again: a prompt too long for the model, an option or
	// output format it doesn't support (400, 413, 422 and the other 4xx).
	ErrBadRequest = errors.New("the model's provider refuses the request")
)

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

// JSONGenerator is a Generator whose provider decodes under a JSON schema,
// so the reply is a value of that schema's shape rather than prose to
// parse.
type JSONGenerator interface {
	Generator
	// GenerateJSON returns the model's reply to prompt, generated under
	// schema (a JSON Schema): one JSON value, or an error wrapping
	// ErrNotJSON. A reply cut off at opts.MaxTokens is an error wrapping
	// ErrTruncated. A structured reply is a decision rather than prose, so
	// with opts.Temperature nil it is decoded greedily, at temperature 0.
	GenerateJSON(ctx context.Context, prompt string, schema json.RawMessage, opts Options) (json.RawMessage, error)
}

// Warmer is a generator whose model can be asked about, and loaded, apart
// from a prompt: a request to a model that isn't loaded waits for its
// load, seconds for a large one, and a request cancelled meanwhile can
// abort the load itself.
type Warmer interface {
	// Loaded reports whether the model is in memory now.
	Loaded(ctx context.Context) (bool, error)
	// Warm loads the model with the context size every call of this
	// generator asks for, and keeps it keepAlive after (0: the provider's
	// default). A load with another context size would load it again.
	Warm(ctx context.Context, keepAlive time.Duration) error
	// Unload asks the provider to free the model's memory now, whatever
	// keep-alive the calls before asked for. A model not loaded stays so.
	Unload(ctx context.Context) error
}
