package generator

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
)

// TestNew_Ollama: an ollama spec builds a generator for its model that
// asks the spec's server.
func TestNew_Ollama(t *testing.T) {
	var asked string
	f := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request, _ int, _ <-chan struct{}) {
		var req generateRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "/api/generate", r.URL.Path)
		asked = req.Model
		reply("NAME: Go")(w, r, 0, nil)
	})
	g, err := New(config.ModelSpec{Provider: "ollama", Model: "gemma4:12b", BaseURL: f.url, TimeoutSeconds: 5})
	require.NoError(t, err)
	assert.Equal(t, "gemma4:12b", g.Model())
	got, err := g.Generate(context.Background(), "p", Options{})
	require.NoError(t, err)
	assert.Equal(t, "NAME: Go", got)
	assert.Equal(t, "gemma4:12b", asked)
}

// TestNew_Errors: a provider New doesn't know, or a spec its client
// refuses, is an error and no Generator at all, not a nil pointer in a
// non-nil interface.
func TestNew_Errors(t *testing.T) {
	for _, tc := range []struct {
		spec config.ModelSpec
		want string
	}{
		{config.ModelSpec{Provider: "openai", Model: "m", BaseURL: "http://localhost:1", TimeoutSeconds: 5},
			`generation provider "openai" is not supported`},
		{config.ModelSpec{Provider: "ollama", BaseURL: "http://localhost:1", TimeoutSeconds: 5}, "model required"},
	} {
		g, err := New(tc.spec)
		require.ErrorContains(t, err, tc.want)
		assert.True(t, g == nil, "a nil interface")
	}
}
