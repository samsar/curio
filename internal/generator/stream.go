package generator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/samsar/curio/internal/ollama"
)

// The bounds of a stream's reading. Each line of Ollama's stream is a
// piece of the reply, a few dozen bytes, but the last carries the
// prompt's tokens as a `context` array: 11.8 KB for a 2.2K-token prompt
// on Ollama 0.40.16, about 60 KB at a full num_ctx of 8192, so a line may
// take 1 MiB. A whole stream may take 8 MiB: a reply of 2,048 tokens is a
// few hundred KB of lines.
const (
	maxStreamLine  = 1 << 20
	maxStreamBytes = 8 << 20
)

// errStreamIdle is the cause a stream's context is cancelled with when no
// line arrives within its idle bound.
var errStreamIdle = errors.New("no line within the idle bound")

// streamLine is one line of /api/generate's stream: a piece of the reply,
// or the last line with why the reply ended and what it cost, or an error
// Ollama met mid-reply. The prompt's `context` tokens are left unread.
type streamLine struct {
	generateResponse
	Error string `json:"error"`
}

// GenerateStream posts a streaming completion request and passes each
// piece of the reply to emit as Ollama writes it. Opening the stream is
// retried as Generate retries; once a line has been read nothing is. See
// StreamGenerator.
func (o *Ollama) GenerateStream(ctx context.Context, prompt string, opts Options,
	emit func(piece string) error) (StreamEnd, error) {
	if opts.MaxTokens <= 0 {
		return StreamEnd{}, fmt.Errorf("ollama generate: a stream needs a token cap, got %d", opts.MaxTokens)
	}
	idle := opts.Idle
	if idle <= 0 {
		idle = DefaultStreamIdle
	}
	req := o.request(prompt, opts)
	req.Stream = true
	// An answer's prompt fits its context by construction, at a token
	// estimate; a tokenizer denser than measured would otherwise lose the
	// instructions without a word. Ollama 0.40.16 honours it streamed or
	// not: a 400 before any line (ErrPromptTooLong).
	req.Truncate = new(false)

	streamCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	body, err := o.openStream(streamCtx, req)
	if err != nil {
		return StreamEnd{}, err
	}
	defer body.Close()

	// The idle timer starts with the first line: until then Ollama may be
	// loading the model and reading the prompt, which ctx alone bounds.
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	limited := &io.LimitedReader{R: body, N: maxStreamBytes + 1}
	lines := bufio.NewScanner(limited)
	lines.Buffer(make([]byte, 0, 4<<10), maxStreamLine)
	for lines.Scan() {
		if timer == nil {
			timer = time.AfterFunc(idle, func() { cancel(errStreamIdle) })
		} else {
			timer.Reset(idle)
		}
		raw := bytes.TrimSpace(lines.Bytes())
		if len(raw) == 0 {
			continue
		}
		var line streamLine
		if err := json.Unmarshal(raw, &line); err != nil {
			if limited.N <= 0 {
				break // the stream's bound cut this line short
			}
			return StreamEnd{}, unavailable(fmt.Errorf("ollama generate: decode stream: %w", err))
		}
		if line.Error != "" {
			return StreamEnd{}, unavailable(fmt.Errorf("ollama generate: %s", line.Error))
		}
		if line.Response != "" {
			if err := emit(line.Response); err != nil {
				return StreamEnd{}, fmt.Errorf("ollama generate: %w", err)
			}
		}
		if line.Done {
			line.report(opts.Usage)
			return StreamEnd{Truncated: line.DoneReason == doneLength}, nil
		}
	}
	return StreamEnd{}, streamError(ctx, streamCtx, lines.Err(), limited.N <= 0, idle)
}

// openStream opens req's stream, retrying a failure to open it (see
// retryable) with a bounded backoff, as generate does.
func (o *Ollama) openStream(ctx context.Context, req generateRequest) (io.ReadCloser, error) {
	for attempt := 1; ; attempt++ {
		body, err := o.client.PostStream(ctx, "/api/generate", req)
		if err == nil {
			return body, nil
		}
		err = fmt.Errorf("ollama generate: %w", err)
		if attempt > o.retries || !retryable(ctx, err) {
			return nil, classify(err)
		}
		select {
		case <-ctx.Done():
			return nil, classify(fmt.Errorf("ollama generate: %w (last attempt: %w)", ctx.Err(), err))
		case <-time.After(o.backoff(attempt)):
		}
	}
}

// streamError says why a stream stopped before its last line: the
// caller's context ended (its own error), the stream went quiet past idle
// (ErrStalled), a line or the stream ran past its bound, the connection
// failed (ErrUnreachable), or the stream simply ended (io.ErrUnexpectedEOF).
func streamError(ctx, streamCtx context.Context, readErr error, overLong bool, idle time.Duration) error {
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("ollama generate: %w", ctx.Err())
	case errors.Is(context.Cause(streamCtx), errStreamIdle):
		return fmt.Errorf("ollama generate: %w: nothing for %s", ErrStalled, idle)
	case errors.Is(readErr, bufio.ErrTooLong):
		return unavailable(fmt.Errorf("ollama generate: a line of the stream exceeds %d bytes: %w", maxStreamLine,
			readErr))
	case overLong:
		return unavailable(fmt.Errorf("ollama generate: the stream exceeds %d bytes", maxStreamBytes))
	case readErr != nil:
		return classify(fmt.Errorf("ollama generate: read stream: %w: %w", ollama.ErrUnreachable, readErr))
	}
	return unavailable(fmt.Errorf("ollama generate: the stream ended before its last line: %w",
		io.ErrUnexpectedEOF))
}

// unavailable makes err match ErrUnavailable, its message unchanged: a
// stream that broke in a way a later call may not.
func unavailable(err error) error { return &classified{err: err, kind: ErrUnavailable} }
