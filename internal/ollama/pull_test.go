package ollama

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullModel_StreamsToSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/pull", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"status":"downloading","total":100,"completed":50}`)
		fmt.Fprintln(w, `{"status":"downloading","total":100,"completed":100}`)
		fmt.Fprintln(w, `{"status":"success"}`)
	}))
	defer srv.Close()

	require.NoError(t, PullModel(context.Background(), srv.URL, "llama3.2", slog.Default()))
}

func TestPullModel_SurfacesStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"error":"model 'nope' not found"}`)
	}))
	defer srv.Close()

	err := PullModel(context.Background(), srv.URL, "nope", slog.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPullModel_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, "boom")
	}))
	defer srv.Close()

	err := PullModel(context.Background(), srv.URL, "x", slog.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}
