package setuptest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Ollama is a fake Ollama on a loopback port: /api/version, /api/tags,
// a streaming /api/pull that adds the model to the tags, and /api/embed,
// which answers with vectors as wide as the model's width (1024 unless
// set) and, like curio's real requests, insists on truncate false and a
// keep_alive. It can stop answering and start again on the same address.
type Ollama struct {
	// URL is its base URL, the same across a Stop and a Start.
	URL string

	t    testing.TB
	addr string

	mu       sync.Mutex
	srv      *httptest.Server
	version  string
	models   []string       // pulled, by the name Ollama runs
	widths   map[string]int // embedding width by model; 1024 unless set
	pullFail map[string]string
	pulls    []string
	embeds   []string // the model of each embed request
	onPull   func(model string)
}

// NewOllama starts a fake Ollama at version with models pulled, until the
// test ends.
func NewOllama(t testing.TB, version string, models ...string) *Ollama {
	t.Helper()
	o := &Ollama{t: t, version: version, widths: map[string]int{}, pullFail: map[string]string{}}
	for _, m := range models {
		o.models = append(o.models, runName(m))
	}
	o.srv = httptest.NewServer(o)
	o.URL, o.addr = o.srv.URL, o.srv.Listener.Addr().String()
	t.Cleanup(o.Stop)
	return o
}

// Stop stops answering: connections to its address are refused.
func (o *Ollama) Stop() {
	o.mu.Lock()
	srv := o.srv
	o.srv = nil
	o.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}

// Start answers again, on the same address.
func (o *Ollama) Start() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.srv != nil {
		return
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", o.addr)
	if err != nil {
		o.t.Errorf("setuptest: listen again on %s: %v", o.addr, err)
		return
	}
	srv := httptest.NewUnstartedServer(o)
	_ = srv.Listener.Close() // replaced by the listener on the old address
	srv.Listener = ln
	srv.Start()
	o.srv = srv
}

// SetVersion changes the version it reports.
func (o *Ollama) SetVersion(v string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.version = v
}

// SetWidth makes model embed width-wide vectors.
func (o *Ollama) SetWidth(model string, width int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.widths[runName(model)] = width
}

// FailPull makes pulls of model fail with msg, as Ollama reports an error
// in the stream.
func (o *Ollama) FailPull(model, msg string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pullFail[runName(model)] = msg
}

// OnPull, when set, runs as each pull starts.
func (o *Ollama) OnPull(f func(model string)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.onPull = f
}

// Has reports whether model is pulled.
func (o *Ollama) Has(model string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Contains(o.models, runName(model))
}

// Pulls are the models pulled, in order.
func (o *Ollama) Pulls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.pulls)
}

// Embeds are the models of the embed requests, in order.
func (o *Ollama) Embeds() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.embeds)
}

func (o *Ollama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/version":
		o.mu.Lock()
		v := o.version
		o.mu.Unlock()
		fmt.Fprintf(w, `{"version":%q}`, v)
	case "/api/tags":
		o.tags(w)
	case "/api/pull":
		o.pull(w, r)
	case "/api/embed":
		o.embed(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (o *Ollama) tags(w http.ResponseWriter) {
	type entry struct {
		Name   string `json:"name"`
		Model  string `json:"model"`
		Digest string `json:"digest"`
	}
	o.mu.Lock()
	var list struct {
		Models []entry `json:"models"`
	}
	for _, m := range o.models {
		list.Models = append(list.Models, entry{m, m, digest(m)})
	}
	o.mu.Unlock()
	writeJSON(w, list)
}

// pull streams a pull's progress: the manifest, one layer in three steps,
// the checks, then success, by which time the model is in the tags.
func (o *Ollama) pull(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	model := runName(req.Model)
	o.mu.Lock()
	o.pulls = append(o.pulls, req.Model)
	failure, onPull := o.pullFail[model], o.onPull
	o.mu.Unlock()
	if onPull != nil {
		onPull(req.Model)
	}
	d := digest(model)
	lines := []string{`{"status":"pulling manifest"}`}
	for _, done := range []int{0, 500, 1000} {
		lines = append(lines, fmt.Sprintf(`{"status":"pulling %s","digest":%q,"total":1000,"completed":%d}`, d[7:19], d, done))
	}
	if failure != "" {
		lines = append(lines, fmt.Sprintf(`{"error":%q}`, failure))
	} else {
		lines = append(lines, `{"status":"verifying sha256 digest"}`, `{"status":"writing manifest"}`, `{"status":"success"}`)
	}
	for i, line := range lines {
		if i == len(lines)-1 && failure == "" {
			// In the tags before success is said, as Ollama writes the
			// manifest first: a client that checks the tags on success
			// finds it there.
			o.mu.Lock()
			if !slices.Contains(o.models, model) {
				o.models = append(o.models, model)
			}
			o.mu.Unlock()
		}
		fmt.Fprintln(w, line)
		w.(http.Flusher).Flush()
	}
}

// embed answers /api/embed as Ollama does for a pulled model, and with a
// 400 for a request curio must never send: one that lets Ollama truncate,
// or keeps no model loaded.
func (o *Ollama) embed(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"unreadable request"}`, http.StatusBadRequest)
		return
	}
	var raw map[string]json.RawMessage
	var req struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if json.Unmarshal(body, &raw) != nil || json.Unmarshal(body, &req) != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if string(raw["truncate"]) != "false" || len(raw["keep_alive"]) == 0 {
		http.Error(w, `{"error":"setuptest: embed requests carry truncate:false and a keep_alive"}`, http.StatusBadRequest)
		return
	}
	model := runName(req.Model)
	o.mu.Lock()
	o.embeds = append(o.embeds, req.Model)
	pulled := slices.Contains(o.models, model)
	width, ok := o.widths[model]
	o.mu.Unlock()
	if !pulled {
		http.Error(w, fmt.Sprintf(`{"error":"model %q not found, try pulling it first"}`, req.Model), http.StatusNotFound)
		return
	}
	if !ok {
		width = 1024
	}
	resp := struct {
		Embeddings [][]float32 `json:"embeddings"`
	}{}
	for range req.Input {
		v := make([]float32, width)
		v[0] = 1
		resp.Embeddings = append(resp.Embeddings, v)
	}
	writeJSON(w, resp)
}

// writeJSON answers with v as JSON. A write that fails is a client that
// hung up, which has what it gets.
func writeJSON(w http.ResponseWriter, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

// runName is the name Ollama runs for model: lower-cased, ":latest" when
// untagged.
func runName(model string) string {
	model = strings.ToLower(model)
	if !strings.Contains(model[strings.LastIndex(model, "/")+1:], ":") {
		model += ":latest"
	}
	return model
}

// digest is a fake manifest digest for model.
func digest(model string) string {
	sum := sha256.Sum256([]byte(model))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Silent returns the base URL of a server that accepts connections and
// never answers, until the test ends: a hung Ollama or daemon, which
// every probe must give up on.
func Silent(t testing.TB) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setuptest: listen: %v", err)
	}
	var conns []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close() // the accept loop ends with it
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close() // held open only to never answer
		}
	})
	return "http://" + ln.Addr().String()
}
