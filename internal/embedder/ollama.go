package embedder

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
	Model   string        // e.g. "qwen3-embedding:0.6b"
	Dim     int           // expected output dimension; every reply is checked against it
	Timeout time.Duration // per-request; default 60s

	// NumCtx is sent as options.num_ctx on embed calls. Ollama checks each
	// input against the smaller of it and the model's own context (32K
	// tokens for Qwen3-Embedding), so it is the binding limit; the
	// chunker's 3500-byte cap keeps chunks far inside it. See decisions.md.
	// Default 8192.
	NumCtx int
}

// embedKeepAlive is how long Ollama keeps the embedding model loaded after
// a request. Its default, 5 minutes, unloads the model between the bursts
// of a paused, throttled or scheduled import, and every burst then reloads
// it. It is a maximum idle time, not a reservation: Ollama still unloads an
// idle model to make room for another.
const embedKeepAlive = "30m"

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
// swaps models server-side, the indexer errors (ErrWrongDimension) rather
// than storing vectors of the wrong size. A text longer than the model's
// context fails the whole batch with ErrInputTooLong.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	var parsed embedResponse
	err := o.client.PostJSON(ctx, "/api/embed", embedRequest{
		Model:     o.client.Model(),
		Input:     texts,
		Truncate:  false,
		KeepAlive: embedKeepAlive,
		Options:   embedOptions{NumCtx: o.numCtx},
	}, &parsed, o.maxEmbedReplyBytes(len(texts)))
	if isContextLengthError(err) {
		return nil, fmt.Errorf("ollama embed: %w: %w", ErrInputTooLong, err)
	}
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}

	if len(parsed.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embed: requested %d embeddings, got %d",
			len(texts), len(parsed.Embeddings))
	}
	for i, vec := range parsed.Embeddings {
		if len(vec) != o.dim {
			return nil, fmt.Errorf("ollama embed: %w: embedding[%d] has dim %d, expected %d",
				ErrWrongDimension, i, len(vec), o.dim)
		}
	}
	return parsed.Embeddings, nil
}

// isContextLengthError reports whether err is Ollama refusing an input
// longer than the context: a 400 whose message mentions the context length
// ("the input length exceeds the context length" from Ollama 0.34.4, "input
// length exceeds maximum context length" from older releases).
func isContextLengthError(err error) bool {
	var se *ollama.StatusError
	return errors.As(err, &se) && se.Code == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(se.Body), "context length")
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
	// Truncate is always false, and always sent: Ollama takes a missing
	// field as true, and then embeds an over-long input cut to the context
	// and answers 200, a vector for text the chunk doesn't hold. False
	// makes it a 400.
	Truncate bool `json:"truncate"`
	// KeepAlive is embedKeepAlive, a duration Ollama parses.
	KeepAlive string       `json:"keep_alive"`
	Options   embedOptions `json:"options,omitzero"`
}

type embedOptions struct {
	NumCtx int `json:"num_ctx,omitempty"`
}

type embedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}
