package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamOf is a handler that streams pieces as lines, flushing each, then
// the last line, done with doneReason and a context array as Ollama sends
// it.
func streamOf(doneReason string, pieces ...string) func(http.ResponseWriter, *http.Request, int, <-chan struct{}) {
	return func(w http.ResponseWriter, _ *http.Request, _ int, _ <-chan struct{}) {
		for _, p := range pieces {
			line, _ := json.Marshal(map[string]any{"model": "m", "response": p, "done": false})
			fmt.Fprintf(w, "%s\n", line)
			w.(http.Flusher).Flush()
		}
		fmt.Fprintf(w, `{"model":"m","response":"","done":true,"done_reason":%q,"context":[1,2,3],`+
			`"prompt_eval_count":12,"eval_count":%d,"eval_duration":5000000}`+"\n", doneReason, len(pieces))
	}
}

// collect streams prompt and returns the pieces emitted.
func collect(t *testing.T, g *Ollama, opts Options) ([]string, StreamEnd, error) {
	t.Helper()
	var pieces []string
	end, err := g.GenerateStream(context.Background(), "p", opts, func(p string) error {
		pieces = append(pieces, p)
		return nil
	})
	return pieces, end, err
}

// TestGenerateStream_Pieces: N lines are N pieces, in order, each emitted
// once; the empty piece of the last line isn't; the done line's usage is
// reported.
func TestGenerateStream_Pieces(t *testing.T) {
	f := newFakeOllama(t, streamOf("stop", "The ", "answer ", "[1]."))
	var usage Usage
	pieces, end, err := collect(t, newGen(t, OllamaOptions{BaseURL: f.url}), Options{MaxTokens: 64, Usage: &usage})
	require.NoError(t, err)
	assert.Equal(t, []string{"The ", "answer ", "[1]."}, pieces)
	assert.False(t, end.Truncated)
	assert.Equal(t, Usage{PromptTokens: 12, OutputTokens: 3, Eval: 5 * time.Millisecond}, usage)
}

// TestGenerateStream_Request pins every field of the request: a stream,
// thinking off, a prompt too long refused rather than cut, the
// generator's num_ctx, the token cap, the temperature and the keep-alive.
func TestGenerateStream_Request(t *testing.T) {
	var body map[string]any
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, call int, release <-chan struct{}) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		streamOf("stop", "x")(w, r, call, release)
	})
	g := newGen(t, OllamaOptions{BaseURL: f.url, Model: "gemma4:26b"})
	_, _, err := collect(t, g, Options{MaxTokens: 768, Temperature: new(0.0), KeepAlive: 30 * time.Minute})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"model": "gemma4:26b", "prompt": "p", "stream": true, "think": false, "keep_alive": "30m0s",
		"truncate": false,
		"options":  map[string]any{"num_ctx": float64(8192), "num_predict": float64(768), "temperature": float64(0)},
	}, body)
}

// TestGenerateStream_PromptTooLong: Ollama's refusal of a prompt longer
// than the context, in its 0.40.16 wording and an older one, is
// ErrPromptTooLong with Ollama's token counts kept, never ErrBadRequest or
// ErrUnavailable, and never retried; the same refusal of a call that
// doesn't stream is too.
func TestGenerateStream_PromptTooLong(t *testing.T) {
	for _, refusal := range []string{
		`{"error":"request (12013 tokens) exceeds the available context size (2048 tokens), try increasing it"}`,
		`{"error":"the input length exceeds the context length"}`,
	} {
		f := newFakeOllama(t, status(http.StatusBadRequest, refusal))
		g := newGen(t, OllamaOptions{BaseURL: f.url})
		_, _, err := collect(t, g, Options{MaxTokens: 64})
		require.ErrorIs(t, err, ErrPromptTooLong)
		assert.NotErrorIs(t, err, ErrBadRequest)
		assert.NotErrorIs(t, err, ErrUnavailable)
		assert.ErrorContains(t, err, "exceeds the")
		assert.Equal(t, int32(1), f.calls.Load(), "never retried")

		_, err = g.Generate(context.Background(), "p", Options{})
		require.ErrorIs(t, err, ErrPromptTooLong, "not streamed")
	}
}

// TestRequests_TruncateOnlyStreams: only a stream, or a call that asks
// to refuse a long prompt, sends truncate; every other call's body is as
// it was measured, without the key.
func TestRequests_TruncateOnlyStreams(t *testing.T) {
	var bodies []map[string]any
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, _ int, _ <-chan struct{}) {
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		bodies = append(bodies, body)
		switch {
		case body["keep_alive"] == float64(0):
			fmt.Fprint(w, `{"done":true,"done_reason":"unload"}`)
		case body["prompt"] == nil:
			fmt.Fprint(w, `{"done":true,"done_reason":"load"}`)
		case body["format"] != nil:
			fmt.Fprint(w, `{"response":"{\"a\":1}","done":true,"done_reason":"stop"}`)
		case body["logprobs"] != nil:
			fmt.Fprint(w, `{"response":"yes","done":true,"done_reason":"stop","logprobs":[{"token":"yes",`+
				`"logprob":-0.1,"top_logprobs":[{"token":"yes","logprob":-0.1}]}]}`)
		default:
			fmt.Fprint(w, `{"response":"ok","done":true,"done_reason":"stop"}`)
		}
	})
	g := newGen(t, OllamaOptions{BaseURL: f.url})
	ctx := context.Background()
	_, err := g.Generate(ctx, "p", Options{})
	require.NoError(t, err)
	_, err = g.GenerateJSON(ctx, "p", json.RawMessage(`{"type":"object"}`), Options{})
	require.NoError(t, err)
	_, err = g.FirstTokens(ctx, "p", false, 5, Options{})
	require.NoError(t, err)
	require.NoError(t, g.Warm(ctx, time.Minute))
	require.NoError(t, g.Unload(ctx))
	require.Len(t, bodies, 5)
	for _, body := range bodies {
		assert.NotContains(t, body, "truncate", "%v", body)
	}

	bodies = nil
	_, err = g.GenerateJSON(ctx, "p", json.RawMessage(`{"type":"object"}`), Options{RefuseLongPrompt: true})
	require.NoError(t, err)
	_, err = g.Generate(ctx, "p", Options{RefuseLongPrompt: true})
	require.NoError(t, err)
	require.Len(t, bodies, 2)
	for _, body := range bodies {
		assert.Equal(t, false, body["truncate"], "asked: %v", body)
	}
}

// TestGenerateStream_NeedsACap: a stream without a token cap is refused
// before any request.
func TestGenerateStream_NeedsACap(t *testing.T) {
	f := newFakeOllama(t, streamOf("stop", "x"))
	_, _, err := collect(t, newGen(t, OllamaOptions{BaseURL: f.url}), Options{})
	require.ErrorContains(t, err, "token cap")
	assert.Zero(t, f.calls.Load())
}

// TestGenerateStream_Truncated: a reply cut at its cap ends Truncated,
// with its pieces and no error.
func TestGenerateStream_Truncated(t *testing.T) {
	f := newFakeOllama(t, streamOf("length", "cut ", "short"))
	pieces, end, err := collect(t, newGen(t, OllamaOptions{BaseURL: f.url}), Options{MaxTokens: 2})
	require.NoError(t, err)
	assert.True(t, end.Truncated)
	assert.Equal(t, []string{"cut ", "short"}, pieces)
}

// TestGenerateStream_UTF8: pieces are decoded JSON strings, so each is
// valid UTF-8 and the pieces joined are the reply, whatever the script.
func TestGenerateStream_UTF8(t *testing.T) {
	want := []string{"ü", "🦊", "👩\u200d💻", "東京"}
	f := newFakeOllama(t, streamOf("stop", want...))
	pieces, _, err := collect(t, newGen(t, OllamaOptions{BaseURL: f.url}), Options{MaxTokens: 64})
	require.NoError(t, err)
	assert.Equal(t, strings.Join(want, ""), strings.Join(pieces, ""))
	for _, p := range pieces {
		assert.True(t, utf8.ValidString(p), "%q", p)
	}
}

// TestGenerateStream_Failures: how a stream that fails is reported, and
// that what it emitted before stays emitted.
func TestGenerateStream_Failures(t *testing.T) {
	twoThen := func(rest string) func(http.ResponseWriter, *http.Request, int, <-chan struct{}) {
		return func(w http.ResponseWriter, _ *http.Request, _ int, _ <-chan struct{}) {
			fmt.Fprint(w, `{"response":"a","done":false}`+"\n"+`{"response":"b","done":false}`+"\n"+rest)
		}
	}
	for _, tc := range []struct {
		name    string
		handle  func(http.ResponseWriter, *http.Request, int, <-chan struct{})
		pieces  []string
		is      []error
		message string
	}{
		{"an error line mid-stream", twoThen(`{"error":"model runner has unexpectedly stopped"}` + "\n"),
			[]string{"a", "b"}, []error{ErrUnavailable}, "unexpectedly stopped"},
		{"the stream ends without its last line", twoThen(""), []string{"a", "b"},
			[]error{ErrUnavailable, io.ErrUnexpectedEOF}, "before its last line"},
		{"a line over its bound", twoThen(`{"response":"` + strings.Repeat("x", maxStreamLine) + `"}` + "\n"),
			[]string{"a", "b"}, []error{ErrUnavailable}, "exceeds 1048576 bytes"},
		{"a line that isn't JSON", twoThen("<html>\n"), []string{"a", "b"}, []error{ErrUnavailable}, "decode"},
		{"404: not pulled", status(http.StatusNotFound, `{"error":"model 'm' not found"}`), nil,
			[]error{ErrModelMissing}, "not found"},
		{"400: refused", status(http.StatusBadRequest, `{"error":"invalid option"}`), nil,
			[]error{ErrBadRequest}, "invalid option"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOllama(t, tc.handle)
			pieces, _, err := collect(t, newGen(t, OllamaOptions{BaseURL: f.url}), Options{MaxTokens: 64})
			require.Error(t, err)
			for _, want := range tc.is {
				assert.ErrorIs(t, err, want)
			}
			assert.ErrorContains(t, err, tc.message)
			assert.Equal(t, tc.pieces, pieces)
			assert.Equal(t, int32(1), f.calls.Load(), "never retried")
		})
	}
}

// TestGenerateStream_OverItsBound: a stream past its bound fails saying
// so, the line the bound cut short unread, every whole line before it
// emitted.
func TestGenerateStream_OverItsBound(t *testing.T) {
	const lines = 9 // each about 1 MB, so the ninth crosses 8 MiB
	piece := strings.Repeat("x", 1_000_000)
	f := newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ <-chan struct{}) {
		for range lines {
			fmt.Fprintf(w, `{"response":%q,"done":false}`+"\n", piece)
		}
	})
	pieces, _, err := collect(t, newGen(t, OllamaOptions{BaseURL: f.url}), Options{MaxTokens: 64})
	require.ErrorIs(t, err, ErrUnavailable)
	assert.ErrorContains(t, err, "the stream exceeds 8388608 bytes")
	assert.Len(t, pieces, lines-1)
}

// TestGenerateStream_FailsMidStreamOnce: a stream that fails after two
// pieces is never asked again: the two pieces are emitted once, and the
// error says the rest is missing.
func TestGenerateStream_FailsMidStreamOnce(t *testing.T) {
	f := newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ <-chan struct{}) {
		fmt.Fprint(w, `{"response":"one ","done":false}`+"\n"+`{"response":"two","done":false}`+"\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // drops the connection
	})
	pieces, _, err := collect(t, newGen(t, OllamaOptions{BaseURL: f.url}), Options{MaxTokens: 64})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnreachable) || errors.Is(err, ErrUnavailable), "%v", err)
	assert.Equal(t, []string{"one ", "two"}, pieces)
	assert.Equal(t, int32(1), f.calls.Load())
}

// TestGenerateStream_RetriesBeforeStreaming: a 503 before the stream
// opens is retried, as a whole call's is.
func TestGenerateStream_RetriesBeforeStreaming(t *testing.T) {
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, call int, release <-chan struct{}) {
		if call == 1 {
			status(http.StatusServiceUnavailable, `{"error":"loading"}`)(w, r, call, release)
			return
		}
		streamOf("stop", "ok")(w, r, call, release)
	})
	pieces, _, err := collect(t, newGen(t, OllamaOptions{BaseURL: f.url}), Options{MaxTokens: 64})
	require.NoError(t, err)
	assert.Equal(t, []string{"ok"}, pieces)
	assert.Equal(t, int32(2), f.calls.Load())
}

// TestGenerateStream_Stalled: after the first piece, a silence past the
// idle bound fails the stream with ErrStalled, which is ErrUnavailable.
func TestGenerateStream_Stalled(t *testing.T) {
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, _ int, release <-chan struct{}) {
		fmt.Fprint(w, `{"response":"first","done":false}`+"\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	pieces, _, err := collect(t, newGen(t, OllamaOptions{BaseURL: f.url}),
		Options{MaxTokens: 64, Idle: 50 * time.Millisecond})
	require.ErrorIs(t, err, ErrStalled)
	require.ErrorIs(t, err, ErrUnavailable)
	assert.Equal(t, []string{"first"}, pieces)
}

// TestGenerateStream_EmitBlocksPastIdle: an emit that blocks (a client
// that stopped reading the answer) counts toward the idle bound: past it
// the stream's request is closed, which frees Ollama's runner while the
// emit still blocks, and the stream ends ErrStalled once emit returns.
func TestGenerateStream_EmitBlocksPastIdle(t *testing.T) {
	gone := make(chan struct{})
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, _ int, release <-chan struct{}) {
		fmt.Fprint(w, `{"response":"first","done":false}`+"\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(gone)
		case <-release:
		}
	})
	_, err := newGen(t, OllamaOptions{BaseURL: f.url}).GenerateStream(context.Background(), "p",
		Options{MaxTokens: 64, Idle: 20 * time.Millisecond}, func(string) error {
			select {
			case <-gone:
				return nil
			case <-time.After(5 * time.Second):
				return errors.New("Ollama's request stayed open while emit blocked")
			}
		})
	require.ErrorIs(t, err, ErrStalled)
}

// TestGenerateStream_FirstLineWaits: the idle bound starts with the first
// line: a slow load before it is the caller's context's to bound.
func TestGenerateStream_FirstLineWaits(t *testing.T) {
	loaded := make(chan struct{})
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, call int, release <-chan struct{}) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-loaded:
		case <-release:
			return
		}
		streamOf("stop", "late")(w, r, call, release)
	})
	time.AfterFunc(150*time.Millisecond, func() { close(loaded) })
	pieces, _, err := collect(t, newGen(t, OllamaOptions{BaseURL: f.url}),
		Options{MaxTokens: 64, Idle: 20 * time.Millisecond})
	require.NoError(t, err)
	assert.Equal(t, []string{"late"}, pieces)
}

// TestGenerateStream_Cancelled: the caller's context ending mid-stream
// closes the connection, which the server sees at once, and is the
// caller's error.
func TestGenerateStream_Cancelled(t *testing.T) {
	gone := make(chan struct{})
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, _ int, release <-chan struct{}) {
		fmt.Fprint(w, `{"response":"first","done":false}`+"\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(gone)
		case <-release:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	var pieces []string
	_, err := newGen(t, OllamaOptions{BaseURL: f.url}).GenerateStream(ctx, "p", Options{MaxTokens: 64},
		func(p string) error {
			pieces = append(pieces, p)
			cancel()
			return nil
		})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"first"}, pieces)
	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatal("the server never saw the connection close")
	}
}

// TestGenerateStream_EmitStops: an error emit returns stops the stream and
// comes back wrapped.
func TestGenerateStream_EmitStops(t *testing.T) {
	f := newFakeOllama(t, streamOf("stop", "a", "b", "c"))
	stop := errors.New("client gone")
	var pieces []string
	_, err := newGen(t, OllamaOptions{BaseURL: f.url}).GenerateStream(context.Background(), "p",
		Options{MaxTokens: 64}, func(p string) error {
			pieces = append(pieces, p)
			return stop
		})
	require.ErrorIs(t, err, stop)
	assert.Equal(t, []string{"a"}, pieces)
}
