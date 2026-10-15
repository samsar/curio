package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOllama serves the endpoints the check calls: the model listing and
// version, /api/ps of what it holds, /api/generate for loads, unloads,
// probes and plans, and /api/embed. It holds the planner's model once
// loaded, and the embedder once it embedded, unless evicts says it drops
// the planner's then.
type fakeOllama struct {
	t      *testing.T
	model  string
	unfit  bool // answers the "no filter" probe with a filter
	evicts bool // an embed unloads the planner's model

	mu       sync.Mutex
	held     map[string]bool
	unloads  int
	prompted []string
}

const embedModel = "qwen3-embedding:0.6b"

func newFake(t *testing.T, model string) *fakeOllama {
	return &fakeOllama{t: t, model: model, held: map[string]bool{}}
}

func (f *fakeOllama) serve(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeOllama) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/tags":
		fmt.Fprintf(w, `{"models":[{"name":%q},{"name":%q}]}`, f.model, embedModel)
	case "/api/version":
		fmt.Fprint(w, `{"version":"0.40.4"}`)
	case "/api/ps":
		var models []string
		for name, held := range f.held {
			if held {
				models = append(models, fmt.Sprintf(`{"name":%q,"model":%q,"size":9100000000,`+
					`"size_vram":9100000000,"context_length":8192}`, name, name))
			}
		}
		fmt.Fprintf(w, `{"models":[%s]}`, strings.Join(models, ","))
	case "/api/embed":
		f.held[embedModel] = true
		if f.evicts {
			f.held[f.model] = false
		}
		vec := make([]float32, 1024)
		vec[0] = 1
		reply, err := json.Marshal(struct {
			Embeddings [][]float32 `json:"embeddings"`
		}{[][]float32{vec}})
		require.NoError(f.t, err)
		_, _ = w.Write(reply)
	case "/api/generate":
		f.generate(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOllama) generate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model     string          `json:"model"`
		Prompt    *string         `json:"prompt"`
		KeepAlive json.RawMessage `json:"keep_alive"`
	}
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&req))
	assert.Equal(f.t, f.model, req.Model)
	switch {
	case req.Prompt == nil && string(req.KeepAlive) == "0":
		f.held[f.model] = false
		f.unloads++
		fmt.Fprint(w, `{"response":"","done":true,"done_reason":"unload"}`)
		return
	case req.Prompt == nil:
		f.held[f.model] = true
		fmt.Fprint(w, `{"response":"","done":true,"done_reason":"load"}`)
		return
	}
	f.held[f.model] = true
	_, question, _ := strings.Cut(*req.Prompt, "\nQuestion: ")
	question = strings.TrimSuffix(question, "\n")
	f.prompted = append(f.prompted, question)
	answer := `{"kind":"list","names":[],"rewrites":[],"filters":{}}`
	switch {
	case strings.Contains(question, "October 2017"):
		answer = `{"kind":"list","names":[],"rewrites":[],"filters":{"saved_after":"2017-10-01",` +
			`"saved_before":"2017-11-01","content_type":["video"]}}`
	case strings.Contains(question, "transformer"):
		answer = `{"kind":"one","names":["Attention Is All You Need"],"rewrites":[],"filters":{}}`
	case f.unfit && question == "postgres vacuum tuning":
		answer = `{"kind":"list","names":[],"rewrites":[],"filters":{"source":["chrome"]}}`
	}
	reply, err := json.Marshal(answer)
	require.NoError(f.t, err)
	fmt.Fprintf(w, `{"response":%s,"done":true,"done_reason":"stop"}`, reply)
}

// configAt is a config.yaml pointing every model at the fake.
func configAt(t *testing.T, url string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, "embedding:\n  base_url: %s\n"+
		"generation:\n  base_url: %s\n", url, url), 0o600))
	return path
}

func runCheck(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	code = run(context.Background(), args, &out, &errs)
	return code, out.String(), errs.String()
}

// TestCheck_Fit: a model that passes the check and plans every question
// passes, the report saying how it loaded, its check, its plans' times
// against the timeout and what Ollama holds after a query embed; the
// model it loaded is unloaded at the end.
func TestCheck_Fit(t *testing.T) {
	f := newFake(t, "gemma4:12b")
	url := f.serve(t)
	code, out, errs := runCheck(t, "-model", "gemma4:12b", "-config", configAt(t, url))
	require.Equal(t, exitOK, code, "%s\n%s", out, errs)
	for _, want := range []string{
		"planner-check: gemma4:12b at " + url + " (Ollama 0.40.4)\n",
		"load:    cold, ",
		"check:   passed (",
		"plans:   8 of 8 planned; p50 ",
		"the timeout is 5s\n",
		"memory:  after a query embed, Ollama holds\n",
		"gemma4:12b: 9.1 GB, 9.1 GB in VRAM, context 8192\n",
		"both models loaded\n",
		"unload:  gemma4:12b, which this check loaded\n",
	} {
		assert.Contains(t, out, want)
	}
	assert.Equal(t, 1, f.unloads)
	assert.Len(t, f.prompted, 3+len(questions), "the probes, then the questions")
}

// TestCheck_Unfit: a model that fails the check fails, its plans still
// timed; one the daemon had loaded stays loaded.
func TestCheck_Unfit(t *testing.T) {
	f := newFake(t, "llama3.2:3b")
	f.unfit = true
	f.held["llama3.2:3b"] = true
	code, out, _ := runCheck(t, "-model", "llama3.2:3b", "-config", configAt(t, f.serve(t)))
	assert.Equal(t, exitFailure, code)
	assert.Contains(t, out, "load:    loaded already")
	assert.Contains(t, out, `check:   failed "no filter"`)
	assert.Contains(t, out, "plans:   8 of 8 planned")
	assert.NotContains(t, out, "unload:")
	assert.Zero(t, f.unloads)
}

// TestCheck_Evicted: a planner's model the embed drops is said to be
// unloaded for the embedder.
func TestCheck_Evicted(t *testing.T) {
	f := newFake(t, "gemma4:26b")
	f.evicts = true
	_, out, _ := runCheck(t, "-model", "gemma4:26b", "-config", configAt(t, f.serve(t)))
	assert.Contains(t, out, "gemma4:26b was unloaded for the embedder: searches would reload it")
}

// TestCheck_NeverPulls: a model Ollama doesn't have fails at once, saying
// how to pull it; nothing is loaded or asked.
func TestCheck_NeverPulls(t *testing.T) {
	f := newFake(t, "gemma4:12b")
	code, _, errs := runCheck(t, "-model", "gemma4:27b", "-config", configAt(t, f.serve(t)))
	assert.Equal(t, exitFailure, code)
	assert.Contains(t, errs, "doesn't have gemma4:27b, and plannercheck never pulls a model: `ollama pull gemma4:27b`")
	assert.Empty(t, f.prompted)
}

// TestRun_Usage: a stray argument is a usage error, -h is not.
func TestRun_Usage(t *testing.T) {
	code, _, errs := runCheck(t, "gemma4:12b")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errs, "usage: plannercheck [-model <name>] [-config <config.yaml>]")
	code, _, _ = runCheck(t, "-h")
	assert.Equal(t, exitOK, code)
}
