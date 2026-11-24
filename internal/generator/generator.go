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
// falls back to parsing Generate's prose. Likewise a provider that can't
// say how likely the first tokens of a reply are must not implement
// LogprobGenerator: the reranker scores by those probabilities, and has no
// prose to fall back to. And a provider that can't pass a reply on as it
// is written must not implement StreamGenerator: an answer that arrives
// whole after a minute is not one a person watches being written, and
// nothing buffers a reply to pretend otherwise.
package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Usage, when set, receives what the call cost as the provider reports
	// it, for any reply it gave, one it can't use included.
	Usage *Usage
	// Idle bounds a stream's silence between two pieces of its reply,
	// once the first has arrived (GenerateStream); 0 is
	// DefaultStreamIdle. The first piece waits for the model's load and
	// its reading of the prompt, which only the caller's context bounds.
	Idle time.Duration
	// RefuseLongPrompt asks the provider to refuse a prompt longer than
	// the model's context, ErrPromptTooLong, rather than cut it: Ollama
	// otherwise drops the prompt's head, its instructions, without a
	// word. A stream always refuses (GenerateStream); any other call asks
	// here, and one that doesn't is sent as it was measured.
	RefuseLongPrompt bool
}

// DefaultStreamIdle is a stream's idle bound when Options.Idle is 0.
// Ollama writes several pieces a second while a reply streams (95 tokens a
// second measured with gemma4:26b on 0.40.16); half a minute of silence is
// a server or a connection that hung.
const DefaultStreamIdle = 30 * time.Second

// Usage is what a call cost, as its provider reports it: the tokens of
// the prompt and of the reply, and how long loading the model, reading
// the prompt and writing the reply took. A provider that reports none of
// it leaves it zero.
type Usage struct {
	PromptTokens int
	OutputTokens int
	Load         time.Duration
	PromptEval   time.Duration
	Eval         time.Duration
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
	// ErrPromptTooLong: the provider refuses a prompt longer than the
	// model's context, which it was told not to cut: the caller's prompt
	// must be made to fit. It matches neither ErrBadRequest nor
	// ErrUnavailable: the model is fine, and the same prompt would be
	// refused again.
	ErrPromptTooLong = errors.New("the prompt is longer than the model's context")
	// ErrStalled: a stream went quiet past its idle bound (Options.Idle)
	// in the middle of its reply. It matches ErrUnavailable too.
	ErrStalled = fmt.Errorf("the model's provider stopped mid-reply: %w", ErrUnavailable)
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

// MaxTopTokens is the most first tokens FirstTokens may ask for: Ollama
// refuses a top_logprobs above 20 with a 400 ("top_logprobs must be
// between 0 and 20", measured on 0.40.14), which would read as the model
// refusing the request.
const MaxTopTokens = 20

// ErrNoLogprobs: the provider answered without the token probabilities
// asked for.
var ErrNoLogprobs = errors.New("the provider returned no token probabilities")

// TokenChoice is one token the model could write first, and its natural
// log probability.
type TokenChoice struct {
	Token   string
	Logprob float64
}

// LogprobGenerator is a Generator whose provider says how likely each of
// the likeliest first tokens of a reply is: what a reranker that asks a
// yes-or-no question scores a page by, without letting the model write
// past one token.
type LogprobGenerator interface {
	Generator
	// FirstTokens returns the top likeliest first tokens of the reply to
	// prompt, in the provider's order (likeliest first), decoding one token
	// greedily. With raw the prompt is sent as is, through no chat
	// template: for a model whose own template the caller writes. top is
	// in [1, MaxTopTokens]; any other is an error before a request. A reply
	// without the probabilities is an error wrapping ErrNoLogprobs.
	FirstTokens(ctx context.Context, prompt string, raw bool, top int, opts Options) ([]TokenChoice, error)
}

// StreamEnd is how a streamed reply ended: Truncated when it stopped at
// its token limit (Options.MaxTokens) rather than where the model ended it.
type StreamEnd struct {
	Truncated bool
}

// StreamGenerator is a Generator whose provider passes a reply on as the
// model writes it.
type StreamGenerator interface {
	Generator
	// GenerateStream writes the reply to prompt, passing emit each piece as
	// it arrives, in order: never an empty one, and each whole UTF-8 text,
	// so the pieces joined are the reply. opts.MaxTokens must be set: a
	// stream with no cap could hold the model for its whole context. A
	// reply cut at the cap ends Truncated, with no error. An error emit
	// returns stops the stream, and comes back wrapped. A stream that
	// fails after its first piece is never retried: what was emitted
	// stays, and the error says why the rest is missing.
	GenerateStream(ctx context.Context, prompt string, opts Options, emit func(piece string) error) (StreamEnd,
		error)
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
