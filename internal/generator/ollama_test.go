package generator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		opts.Model = "llama3.2"
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

func TestGenerate_ClientErrorsAreNotRetried(t *testing.T) {
	cases := []struct {
		name        string
		code        int
		body        string
		notLoaded   bool
		wantInError string
	}{
		{"model not found", http.StatusNotFound, `{"error":"model 'llama3.2' not found"}`, true, "not found"},
		{"bad request", http.StatusBadRequest, `{"error":"invalid options"}`, false, "invalid options"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOllama(t, status(tc.code, tc.body))
			_, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(context.Background(), "p", Options{})
			require.Error(t, err)
			assert.Equal(t, int32(1), f.calls.Load(), "exactly one attempt")
			assert.Equal(t, tc.notLoaded, errors.Is(err, ErrModelNotLoaded))
			var se *StatusError
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
		var se *StatusError
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

// countingTransport counts round trips, for servers that never see them.
type countingTransport struct {
	n    atomic.Int32
	next http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.next.RoundTrip(r)
}

func TestGenerate_ConnectionRefusedIsRetried(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing listens on the port any more

	g := newGen(t, OllamaOptions{BaseURL: srv.URL})
	ct := &countingTransport{next: http.DefaultTransport}
	g.client.Transport = ct

	_, err := g.Generate(context.Background(), "p", Options{})
	require.ErrorIs(t, err, ErrOllamaUnreachable)
	require.ErrorIs(t, err, syscall.ECONNREFUSED, "the cause stays in the chain")
	assert.Equal(t, int32(3), ct.n.Load())
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

	done := make(chan error, 1)
	go func() {
		_, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(ctx, "p", Options{})
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

func TestGenerate_BoundedBodies(t *testing.T) {
	t.Run("oversized reply is rejected, not retried", func(t *testing.T) {
		f := newFakeOllama(t, reply(strings.Repeat("a", maxResponseBody)))
		_, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(context.Background(), "p", Options{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds")
		assert.Equal(t, int32(1), f.calls.Load())
	})
	t.Run("error body is quoted up to the cap", func(t *testing.T) {
		f := newFakeOllama(t, status(http.StatusBadRequest, strings.Repeat("e", 10*maxErrorBody)))
		_, err := newGen(t, OllamaOptions{BaseURL: f.url}).Generate(context.Background(), "p", Options{})
		var se *StatusError
		require.ErrorAs(t, err, &se)
		assert.Len(t, se.Body, maxErrorBody)
	})
}

func TestPing_UnreachableKeepsCause(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	err := newGen(t, OllamaOptions{BaseURL: srv.URL}).Ping(context.Background())
	require.ErrorIs(t, err, ErrOllamaUnreachable)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
}
