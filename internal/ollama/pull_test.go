package ollama

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pullFrom returns a Client whose /api/pull is handled by h.
func pullFrom(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return newClient(t, srv.URL, "llama3.2")
}

func TestPull_StreamsToSuccess(t *testing.T) {
	c := pullFrom(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/pull", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"status":"downloading","total":100,"completed":50}`)
		fmt.Fprintln(w, `{"status":"downloading","total":100,"completed":100}`)
		fmt.Fprintln(w, `{"status":"success"}`)
	})
	require.NoError(t, c.Pull(context.Background(), quietLog()))
}

func TestPull_SurfacesStreamError(t *testing.T) {
	c := pullFrom(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"error":"model 'nope' not found"}`)
	})
	err := c.Pull(context.Background(), quietLog())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPull_HTTPError(t *testing.T) {
	c := pullFrom(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, "boom")
	})
	err := c.Pull(context.Background(), quietLog())
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
	err := c.Pull(context.Background(), quietLog())
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Contains(t, err.Error(), "stream ended before success")
}
