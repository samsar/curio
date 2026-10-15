package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/ollama"
)

// fakeOllama serves /api/generate with a scripted handler and counts calls.
type fakeOllama struct {
	calls atomic.Int32
	url   string
}

// newFakeOllama starts a server whose handler gets the 1-based call number.
// Handlers that block must also return on release, which is closed before
// the server shuts down.
func newFakeOllama(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, call int, release <-chan struct{})) *fakeOllama {
	t.Helper()
	f := &fakeOllama{}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, int(f.calls.Add(1)), release)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first: unblocks handlers before Close waits on them
	f.url = srv.URL
	return f
}

// newGen builds a client with no backoff between retries.
func newGen(t *testing.T, opts OllamaOptions) *Ollama {
	t.Helper()
	if opts.Model == "" {
		opts.Model = "qwen3:4b-instruct"
	}
	g, err := NewOllama(opts)
	require.NoError(t, err)
	g.backoff = func(int) time.Duration { return 0 }
	return g
}

func reply(text string) func(http.ResponseWriter, *http.Request, int, <-chan struct{}) {
	return func(w http.ResponseWriter, _ *http.Request, _ int, _ <-chan struct{}) {
		fmt.Fprintf(w, `{"response":%q,"done":true}`, text)
	}
}

func status(code int, body string) func(http.ResponseWriter, *http.Request, int, <-chan struct{}) {
	return func(w http.ResponseWriter, _ *http.Request, _ int, _ <-chan struct{}) {
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}
}

func TestGenerate_Success(t *testing.T) {
	f := newFakeOllama(t, reply("  NAME: Go\n"))
	got, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(context.Background(), "p", Options{})
	require.NoError(t, err)
	assert.Equal(t, "NAME: Go", got)
}

// TestGenerate_RequestTurnsThinkingOff pins what every /api/generate body
// carries, on the first attempt and on a retry, read from the raw JSON so a
// missing key fails: think false, which Ollama would otherwise take as on
// for a model that thinks, and an explicit num_ctx.
func TestGenerate_RequestTurnsThinkingOff(t *testing.T) {
	bodies := make(chan map[string]json.RawMessage, 2)
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, call int, release <-chan struct{}) {
		var raw map[string]json.RawMessage
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		bodies <- raw
		if call == 1 {
			status(http.StatusServiceUnavailable, "loading")(w, r, call, release)
			return
		}
		reply("NAME: Go")(w, r, call, release)
	})

	_, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(context.Background(), "p",
		Options{Temperature: new(0.2), MaxTokens: 120})
	require.NoError(t, err)
	require.Len(t, bodies, 2, "the first attempt and one retry")
	for range 2 {
		body := <-bodies
		assert.JSONEq(t, `"qwen3:4b-instruct"`, string(body["model"]))
		assert.JSONEq(t, `false`, string(body["think"]), "think is always sent, as false")
		assert.JSONEq(t, `false`, string(body["stream"]))
		assert.JSONEq(t, `{"num_ctx":8192,"temperature":0.2,"num_predict":120}`, string(body["options"]))
		assert.NotContains(t, body, "format", "prose has no schema")
		assert.NotContains(t, body, "keep_alive", "the provider's default without Options.KeepAlive")
	}
}

// TestGenerate_DefaultTemperatureIsLeftOut: a nil temperature is the
// model's default, so the field is left out rather than sent as 0, which
// would decode greedily.
func TestGenerate_DefaultTemperatureIsLeftOut(t *testing.T) {
	body := make(chan map[string]json.RawMessage, 1)
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, call int, release <-chan struct{}) {
		var raw map[string]json.RawMessage
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		body <- raw
		reply("ok")(w, r, call, release)
	})
	_, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(context.Background(), "p", Options{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"num_ctx":8192}`, string((<-body)["options"]))
}

// planSchema is a small schema for the GenerateJSON tests.
const planSchema = `{"type":"object","properties":{"kind":{"type":"string","enum":["one","list"]}},` +
	`"required":["kind"],"additionalProperties":false}`

// TestGenerateJSON_Request pins what a structured request carries on the
// wire, read from the raw JSON so a missing key fails: the schema as
// format, think and stream false, temperature 0 present though the caller
// set none, the token cap, the generator's num_ctx and the keep-alive.
func TestGenerateJSON_Request(t *testing.T) {
	body := make(chan map[string]json.RawMessage, 1)
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, call int, release <-chan struct{}) {
		var raw map[string]json.RawMessage
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		body <- raw
		reply(` {"kind":"one"} `)(w, r, call, release)
	})
	got, err := newGen(t, OllamaOptions{BaseURL: f.url}).GenerateJSON(context.Background(), "plan this",
		json.RawMessage(planSchema), Options{MaxTokens: 256, KeepAlive: 30 * time.Minute})
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"one"}`, string(got))

	raw := <-body
	assert.JSONEq(t, `"qwen3:4b-instruct"`, string(raw["model"]))
	assert.JSONEq(t, `"plan this"`, string(raw["prompt"]))
	assert.JSONEq(t, `false`, string(raw["stream"]))
	assert.JSONEq(t, `false`, string(raw["think"]))
	assert.JSONEq(t, planSchema, string(raw["format"]))
	assert.JSONEq(t, `"30m0s"`, string(raw["keep_alive"]))
	assert.JSONEq(t, `{"num_ctx":8192,"temperature":0,"num_predict":256}`, string(raw["options"]),
		"temperature 0 is sent, not left to the model's default")
}

// TestGenerateJSON_Replies: a reply is returned only when it is JSON; one
// cut off at the token limit is ErrTruncated whatever it holds, and one
// that isn't JSON is an error quoting no more than 120 bytes of it. Neither
// is retried: the same request would get the same reply.
func TestGenerateJSON_Replies(t *testing.T) {
	long := `{"kind":"` + strings.Repeat("x", 500)
	cases := []struct {
		name      string
		body      string
		want      string
		truncated bool
		errText   string // with ErrNotJSON
	}{
		{"JSON", `{"response":"{\"kind\":\"list\"}","done":true,"done_reason":"stop"}`, `{"kind":"list"}`, false, ""},
		{"cut off at the limit", `{"response":"{\"kind\":\"li","done":true,"done_reason":"length"}`, "", true, ""},
		{"cut off, though it parses", `{"response":"{}","done":true,"done_reason":"length"}`, "", true, ""},
		{"not JSON", `{"response":"Sure! Here is the plan","done":true,"done_reason":"stop"}`, "", false,
			`the reply isn't JSON: "Sure! Here is the plan"`},
		{"empty", `{"response":"","done":true,"done_reason":"stop"}`, "", false, `the reply isn't JSON: ""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ <-chan struct{}) {
				fmt.Fprint(w, tc.body)
			})
			got, err := newGen(t, OllamaOptions{BaseURL: f.url}).GenerateJSON(context.Background(), "p",
				json.RawMessage(planSchema), Options{MaxTokens: 8})
			assert.Equal(t, int32(1), f.calls.Load())
			switch {
			case tc.truncated:
				require.ErrorIs(t, err, ErrTruncated)
			case tc.errText != "":
				require.ErrorIs(t, err, ErrNotJSON)
				require.ErrorContains(t, err, tc.errText)
			default:
				require.NoError(t, err)
				assert.JSONEq(t, tc.want, string(got))
			}
		})
	}
	t.Run("a long reply that isn't JSON is quoted in part", func(t *testing.T) {
		f := newFakeOllama(t, reply(long))
		_, err := newGen(t, OllamaOptions{BaseURL: f.url}).GenerateJSON(context.Background(), "p",
			json.RawMessage(planSchema), Options{})
		require.ErrorContains(t, err, "the reply isn't JSON")
		assert.Contains(t, err.Error(), strconv.Quote(long[:120]))
		assert.NotContains(t, err.Error(), long[:121])
	})
}

// TestGenerateJSON_Failures: a missing model is ErrModelNotLoaded at once,
// and a 5xx is retried as Generate retries it.
func TestGenerateJSON_Failures(t *testing.T) {
	t.Run("model not pulled", func(t *testing.T) {
		f := newFakeOllama(t, status(http.StatusNotFound, `{"error":"model 'qwen3:4b-instruct' not found"}`))
		_, err := newGen(t, OllamaOptions{BaseURL: f.url}).GenerateJSON(context.Background(), "p",
			json.RawMessage(planSchema), Options{})
		require.ErrorIs(t, err, ollama.ErrModelNotLoaded)
		assert.Equal(t, int32(1), f.calls.Load())
	})
	t.Run("a 5xx is retried", func(t *testing.T) {
		f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, call int, release <-chan struct{}) {
			if call == 1 {
				status(http.StatusServiceUnavailable, "loading")(w, r, call, release)
				return
			}
			reply(`{"kind":"one"}`)(w, r, call, release)
		})
		got, err := newGen(t, OllamaOptions{BaseURL: f.url}).GenerateJSON(context.Background(), "p",
			json.RawMessage(planSchema), Options{})
		require.NoError(t, err)
		assert.JSONEq(t, `{"kind":"one"}`, string(got))
		assert.Equal(t, int32(2), f.calls.Load())
	})
}

// TestWarm: a warm-up is a generate request with the model, no prompt, the
// keep-alive, and the num_ctx every completion sends, so it loads the
// runner the plans and labels then use; Ollama answers done_reason "load".
func TestWarm(t *testing.T) {
	bodies := make(chan map[string]json.RawMessage, 2)
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, _ int, _ <-chan struct{}) {
		var raw map[string]json.RawMessage
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		bodies <- raw
		if _, warm := raw["prompt"]; !warm {
			fmt.Fprint(w, `{"model":"qwen3:4b-instruct","response":"","done":true,"done_reason":"load"}`)
			return
		}
		fmt.Fprint(w, `{"response":"{\"kind\":\"one\"}","done":true,"done_reason":"stop"}`)
	})
	g := newGen(t, OllamaOptions{BaseURL: f.url, NumCtx: 4096})
	require.NoError(t, g.Warm(context.Background(), 30*time.Minute))
	_, err := g.GenerateJSON(context.Background(), "p", json.RawMessage(planSchema), Options{})
	require.NoError(t, err)

	warm, plan := <-bodies, <-bodies
	assert.JSONEq(t, `"qwen3:4b-instruct"`, string(warm["model"]))
	assert.NotContains(t, warm, "prompt")
	assert.NotContains(t, warm, "format")
	assert.JSONEq(t, `"30m0s"`, string(warm["keep_alive"]))
	assert.JSONEq(t, `{"num_ctx":4096}`, string(warm["options"]))
	var warmOpts, planOpts struct {
		NumCtx int `json:"num_ctx"`
	}
	require.NoError(t, json.Unmarshal(warm["options"], &warmOpts))
	require.NoError(t, json.Unmarshal(plan["options"], &planOpts))
	assert.Equal(t, planOpts.NumCtx, warmOpts.NumCtx, "a warm-up with another num_ctx would load the model again")
}

func TestWarm_Failures(t *testing.T) {
	t.Run("model not pulled", func(t *testing.T) {
		f := newFakeOllama(t, status(http.StatusNotFound, `{"error":"model 'qwen3:4b-instruct' not found"}`))
		err := newGen(t, OllamaOptions{BaseURL: f.url}).Warm(context.Background(), time.Minute)
		require.ErrorIs(t, err, ollama.ErrModelNotLoaded)
	})
	t.Run("an answer that isn't a load", func(t *testing.T) {
		f := newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ <-chan struct{}) {
			fmt.Fprint(w, `{"response":"hi","done":true,"done_reason":"stop"}`)
		})
		err := newGen(t, OllamaOptions{BaseURL: f.url}).Warm(context.Background(), time.Minute)
		require.ErrorContains(t, err, `done_reason "stop", not "load"`)
	})
	t.Run("no answer", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		err := newGen(t, OllamaOptions{BaseURL: srv.URL}).Warm(context.Background(), time.Minute)
		require.ErrorIs(t, err, ollama.ErrUnreachable)
	})
}

// TestUnload: an unload is a generate request with the model, no prompt
// and keep_alive 0, sent as a number: left out, Ollama would keep its
// default. Ollama answers done_reason "unload".
func TestUnload(t *testing.T) {
	bodies := make(chan map[string]json.RawMessage, 1)
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, _ int, _ <-chan struct{}) {
		var raw map[string]json.RawMessage
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		bodies <- raw
		fmt.Fprint(w, `{"model":"qwen3:4b-instruct","response":"","done":true,"done_reason":"unload"}`)
	})
	require.NoError(t, newGen(t, OllamaOptions{BaseURL: f.url}).Unload(context.Background()))
	body := <-bodies
	assert.JSONEq(t, `"qwen3:4b-instruct"`, string(body["model"]))
	assert.JSONEq(t, `0`, string(body["keep_alive"]))
	assert.NotContains(t, body, "prompt")
	assert.Equal(t, int32(1), f.calls.Load(), "one attempt")

	f = newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ <-chan struct{}) {
		fmt.Fprint(w, `{"response":"","done":true,"done_reason":"load"}`)
	})
	require.ErrorContains(t, newGen(t, OllamaOptions{BaseURL: f.url}).Unload(context.Background()),
		`done_reason "load", not "unload"`)
}

// TestFailuresAreClassified: every failure of a call Ollama didn't answer
// matches the provider-neutral sentinel it is, its message and Ollama's
// own sentinels kept; a reply the model gave matches none of them.
func TestFailuresAreClassified(t *testing.T) {
	sentinels := []error{ErrUnreachable, ErrModelMissing, ErrUnauthorized, ErrUnavailable, ErrBadRequest}
	for _, tc := range []struct {
		name string
		code int
		want error
	}{
		{"not pulled", http.StatusNotFound, ErrModelMissing},
		{"no key", http.StatusUnauthorized, ErrUnauthorized},
		{"out of credit", http.StatusPaymentRequired, ErrUnauthorized},
		{"forbidden", http.StatusForbidden, ErrUnauthorized},
		{"rate limited", http.StatusTooManyRequests, ErrUnavailable},
		{"a request timeout", http.StatusRequestTimeout, ErrUnavailable},
		{"a server error", http.StatusInternalServerError, ErrUnavailable},
		{"overloaded", http.StatusServiceUnavailable, ErrUnavailable},
		{"a bad request", http.StatusBadRequest, ErrBadRequest},
		{"too large", http.StatusRequestEntityTooLarge, ErrBadRequest},
		{"unprocessable", http.StatusUnprocessableEntity, ErrBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOllama(t, status(tc.code, `{"error":"boom"}`))
			g := newGen(t, OllamaOptions{BaseURL: f.url, Retries: -1})
			_, err := g.GenerateJSON(context.Background(), "p", json.RawMessage(planSchema), Options{})
			for _, s := range sentinels {
				assert.Equal(t, errors.Is(s, tc.want), errors.Is(err, s), "%v", s)
			}
			var se *ollama.StatusError
			require.ErrorAs(t, err, &se)
			assert.Equal(t, tc.code, se.Code)
			assert.Equal(t, fmt.Sprintf("ollama generate: HTTP %d: {\"error\":\"boom\"}", tc.code), strings.ReplaceAll(
				err.Error(), ollama.ErrModelNotLoaded.Error()+": ", ""))
			assert.ErrorIs(t, g.Warm(context.Background(), time.Minute), tc.want, "a warm-up's")
			assert.ErrorIs(t, g.Unload(context.Background()), tc.want, "an unload's")
		})
	}
	t.Run("no answer", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		g := newGen(t, OllamaOptions{BaseURL: srv.URL, Retries: -1})
		_, err := g.GenerateJSON(context.Background(), "p", json.RawMessage(planSchema), Options{})
		require.ErrorIs(t, err, ErrUnreachable)
		require.ErrorIs(t, err, ollama.ErrUnreachable)
		_, err = g.Loaded(context.Background())
		require.ErrorIs(t, err, ErrUnreachable)
	})
	t.Run("an answer that isn't JSON", func(t *testing.T) {
		f := newFakeOllama(t, reply("Sure!"))
		_, err := newGen(t, OllamaOptions{BaseURL: f.url}).GenerateJSON(context.Background(), "p",
			json.RawMessage(planSchema), Options{})
		require.ErrorIs(t, err, ErrNotJSON)
		for _, s := range sentinels {
			assert.NotErrorIs(t, err, s)
		}
	})
}

// TestPing_Classified: a model Ollama's listing lacks is ErrModelMissing,
// Ollama's own sentinel kept; no answer is ErrUnreachable.
func TestPing_Classified(t *testing.T) {
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, _ int, _ <-chan struct{}) {
		assert.Equal(t, "/api/tags", r.URL.Path)
		fmt.Fprint(w, `{"models":[{"name":"gemma4:26b"}]}`)
	})
	require.NoError(t, newGen(t, OllamaOptions{BaseURL: f.url, Model: "gemma4:26b"}).Ping(context.Background()))
	err := newGen(t, OllamaOptions{BaseURL: f.url, Model: "gemma4:12b"}).Ping(context.Background())
	require.ErrorIs(t, err, ErrModelMissing)
	require.ErrorIs(t, err, ollama.ErrModelNotLoaded)

	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	require.ErrorIs(t, newGen(t, OllamaOptions{BaseURL: srv.URL}).Ping(context.Background()), ErrUnreachable)
}

// TestLoaded: the generator asks Ollama's /api/ps about its own model.
func TestLoaded(t *testing.T) {
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, _ int, _ <-chan struct{}) {
		assert.Equal(t, "/api/ps", r.URL.Path)
		fmt.Fprint(w, `{"models":[{"name":"qwen3:4b-instruct","model":"qwen3:4b-instruct"}]}`)
	})
	loaded, err := newGen(t, OllamaOptions{BaseURL: f.url}).Loaded(context.Background())
	require.NoError(t, err)
	assert.True(t, loaded)
	loaded, err = newGen(t, OllamaOptions{BaseURL: f.url, Model: "gemma4:26b"}).Loaded(context.Background())
	require.NoError(t, err)
	assert.False(t, loaded)
}

// The Ollama generator is what callers that need structure or warmth
// type-assert to.
var (
	_ JSONGenerator = (*Ollama)(nil)
	_ Warmer        = (*Ollama)(nil)
)

func TestGenerate_ClientErrorsAreNotRetried(t *testing.T) {
	cases := []struct {
		name        string
		code        int
		body        string
		notLoaded   bool
		wantInError string
	}{
		{"model not found", http.StatusNotFound, `{"error":"model 'qwen3:4b-instruct' not found"}`, true, "not found"},
		{"bad request", http.StatusBadRequest, `{"error":"invalid options"}`, false, "invalid options"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOllama(t, status(tc.code, tc.body))
			_, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(context.Background(), "p", Options{})
			require.Error(t, err)
			assert.Equal(t, int32(1), f.calls.Load(), "exactly one attempt")
			assert.Equal(t, tc.notLoaded, errors.Is(err, ollama.ErrModelNotLoaded))
			var se *ollama.StatusError
			require.ErrorAs(t, err, &se)
			assert.Equal(t, tc.code, se.Code)
			assert.Contains(t, err.Error(), tc.wantInError)
		})
	}
}

func TestGenerate_ServerErrorsAreRetried(t *testing.T) {
	t.Run("recovers on the second attempt", func(t *testing.T) {
		f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, call int, release <-chan struct{}) {
			if call == 1 {
				status(http.StatusServiceUnavailable, "loading")(w, r, call, release)
				return
			}
			reply("ok")(w, r, call, release)
		})
		got, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(context.Background(), "p", Options{})
		require.NoError(t, err)
		assert.Equal(t, "ok", got)
		assert.Equal(t, int32(2), f.calls.Load())
	})
	t.Run("gives up after the retries", func(t *testing.T) {
		f := newFakeOllama(t, status(http.StatusInternalServerError, "boom"))
		_, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(context.Background(), "p", Options{})
		var se *ollama.StatusError
		require.ErrorAs(t, err, &se)
		assert.Equal(t, http.StatusInternalServerError, se.Code)
		assert.Equal(t, int32(3), f.calls.Load(), "the first attempt plus the default 2 retries")
	})
	t.Run("negative retries means a single attempt", func(t *testing.T) {
		f := newFakeOllama(t, status(http.StatusInternalServerError, "boom"))
		_, err := newGen(t, OllamaOptions{BaseURL: f.url, Retries: -1}).Generate(context.Background(), "p", Options{})
		require.Error(t, err, "a failed attempt is never reported as success")
		assert.Equal(t, int32(1), f.calls.Load())
	})
}

func TestGenerate_ConnectionRefusedIsRetried(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing listens on the port any more

	g := newGen(t, OllamaOptions{BaseURL: srv.URL})
	var retries int
	g.backoff = func(int) time.Duration {
		retries++
		return 0
	}

	_, err := g.Generate(context.Background(), "p", Options{})
	require.ErrorIs(t, err, ollama.ErrUnreachable)
	require.ErrorIs(t, err, syscall.ECONNREFUSED, "the cause stays in the chain")
	assert.Equal(t, 2, retries, "the first attempt plus the default 2 retries")
}

func TestGenerate_AttemptTimeoutIsNotRetried(t *testing.T) {
	f := newFakeOllama(t, func(_ http.ResponseWriter, r *http.Request, _ int, release <-chan struct{}) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	_, err := newGen(t, OllamaOptions{BaseURL: f.url, Timeout: 50 * time.Millisecond}).
		Generate(context.Background(), "p", Options{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int32(1), f.calls.Load())
}

func TestGenerate_CallerCancelStopsPromptly(t *testing.T) {
	entered := make(chan struct{})
	f := newFakeOllama(t, func(_ http.ResponseWriter, r *http.Request, _ int, release <-chan struct{}) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-entered
		cancel()
	}()

	g := newGen(t, OllamaOptions{BaseURL: f.url})
	done := make(chan error, 1)
	go func() {
		_, err := g.Generate(ctx, "p", Options{})
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("Generate did not return after its context was canceled")
	}
	assert.Equal(t, int32(1), f.calls.Load())
}

func TestGenerate_OversizedReplyIsRejectedNotRetried(t *testing.T) {
	f := newFakeOllama(t, reply(strings.Repeat("a", maxReplyBytes)))
	_, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(context.Background(), "p", Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	assert.Equal(t, int32(1), f.calls.Load())
}

func TestNewOllama_Validation(t *testing.T) {
	_, err := NewOllama(OllamaOptions{})
	require.Error(t, err, "model required")
	_, err = NewOllama(OllamaOptions{Model: "m", BaseURL: "ftp://localhost"})
	require.Error(t, err, "http(s) only")

	g, err := NewOllama(OllamaOptions{Model: "m"})
	require.NoError(t, err)
	assert.Equal(t, "m", g.Model())
	assert.Equal(t, ollama.DefaultBaseURL, g.client.BaseURL())
}
