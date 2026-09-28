package ollama

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pullFrom returns a Client whose /api/pull is handled by h.
func pullFrom(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return newClient(t, srv.URL, "qwen3:4b-instruct")
}

// TestPull_StreamsToSuccess: every line before the success line reaches
// the callback, in order, with its layer's digest and counts.
func TestPull_StreamsToSuccess(t *testing.T) {
	c := pullFrom(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/pull", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"status":"pulling a1","digest":"sha256:a1","total":100,"completed":50}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, `{"status":"pulling a1","digest":"sha256:a1","total":100,"completed":100}`)
		fmt.Fprintln(w, `{"status":"pulling b2","digest":"sha256:b2","total":10,"completed":10}`)
		fmt.Fprintln(w, `{"status":"success"}`)
	})
	var got []PullProgress
	require.NoError(t, c.Pull(context.Background(), func(p PullProgress) { got = append(got, p) }))
	assert.Equal(t, []PullProgress{
		{Status: "pulling manifest"},
		{Status: "pulling a1", Digest: "sha256:a1", Total: 100, Completed: 50},
		{Status: "pulling a1", Digest: "sha256:a1", Total: 100, Completed: 100},
		{Status: "pulling b2", Digest: "sha256:b2", Total: 10, Completed: 10},
		{Status: "success"},
	}, got)
	require.NoError(t, c.Pull(context.Background(), nil), "no callback is fine")
}

func TestPull_SurfacesStreamError(t *testing.T) {
	c := pullFrom(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"error":"model 'nope' not found"}`)
	})
	err := c.Pull(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPull_HTTPError(t *testing.T) {
	c := pullFrom(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, "boom")
	})
	err := c.Pull(context.Background(), nil)
	var se *StatusError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, http.StatusInternalServerError, se.Code)
	assert.Contains(t, err.Error(), "HTTP 500: boom")
}

// TestPull_StreamEndsBeforeSuccess: progress lines and then EOF is a pull
// that was cut off (Ollama crashed, the connection dropped), not a model
// that is ready.
func TestPull_StreamEndsBeforeSuccess(t *testing.T) {
	c := pullFrom(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"status":"downloading","total":100,"completed":40}`)
	})
	calls := 0
	err := c.Pull(context.Background(), func(PullProgress) { calls++ })
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Contains(t, err.Error(), "stream ended before success")
	assert.Equal(t, 2, calls)
}

// TestPull_OversizeLine: a line past maxPullLine fails the pull, naming
// the model, rather than buffering whatever the server sends.
func TestPull_OversizeLine(t *testing.T) {
	c := pullFrom(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintf(w, `{"status":"%s"}`+"\n", strings.Repeat("x", maxPullLine))
		fmt.Fprintln(w, `{"status":"success"}`)
	})
	err := c.Pull(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ollama pull qwen3:4b-instruct")
	assert.Contains(t, err.Error(), fmt.Sprintf("exceeds %d bytes", maxPullLine))
}

// TestPull_Stall: a stream that goes quiet for the idle bound fails,
// naming the model, instead of waiting for a context that may never end:
// the daemon's KeepPulled would never retry.
func TestPull_Stall(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	c := pullFrom(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling a1","digest":"sha256:a1","total":100,"completed":10}`)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	c.pullIdle = 200 * time.Millisecond

	start := time.Now()
	calls := 0
	err := c.Pull(context.Background(), func(PullProgress) { calls++ })
	require.ErrorIs(t, err, errPullStalled)
	assert.Contains(t, err.Error(), "ollama pull qwen3:4b-instruct: no progress from Ollama for 200ms")
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, 1, calls)
}

// TestPull_ProgressKeepsItAlive: lines that keep coming reset the idle
// bound, however long the pull takes in all.
func TestPull_ProgressKeepsItAlive(t *testing.T) {
	c := pullFrom(t, func(w http.ResponseWriter, _ *http.Request) {
		for i := range 6 {
			fmt.Fprintf(w, `{"status":"pulling a1","digest":"sha256:a1","total":6,"completed":%d}`+"\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
		fmt.Fprintln(w, `{"status":"success"}`)
	})
	c.pullIdle = 150 * time.Millisecond
	require.NoError(t, c.Pull(context.Background(), nil))
}
