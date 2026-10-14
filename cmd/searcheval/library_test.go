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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
)

// The synthetic library is 24 documents, https://example.com/doc/00 to
// /doc/23, one chunk each. Document i's chunk is embedded at (i, 0, ..., 0),
// 8 wide, so a query embedded at (a, 0, ..., 0) ranks the documents by
// |i - a|. Every chunk's text is six words, "<w1> <w2> note NN about
// things", so a keyword's BM25 score depends on how often the chunk holds
// it alone, and documents holding it equally often rank in the order they
// were written. The keywords:
//
//	alpha: documents 3 and 5
//	beta:  documents 12, 20 and 22
//	gamma: document 7
//	delta: document 16, and document 18 twice
//
// Documents 0 to 19 are fetched; 20 is pending with its chunk, which search
// reads; 21 is pending without a chunk; 22 failed and 23 is dead, both with
// their chunks, which the chunk lists leave out.
//
// The failed 22 and the dead 23 were bookmarked (savedAs), which the
// document lists find them by: their fields hold the bookmark's title, and
// their cards, "<title> — example.com", are embedded where the query their
// title names is (cardAt), so the cards list ranks each first for it.
const (
	libraryDocs = 24
	testDim     = 8
	testModel   = "test-embed:1"
)

// keywords are the documents' first two words; plain ones match no query.
var keywords = map[int]string{
	3: "alpha words", 5: "alpha words",
	12: "beta words", 20: "beta words", 22: "beta words",
	7:  "gamma words",
	16: "delta words", 18: "delta delta",
}

// queryAt is where each query of testdata/queries.yaml is embedded on the
// documents' line.
var queryAt = map[string]float32{"alpha": 3.25, "beta": 20.25, "gamma": 0.75, "delta": 16.6}

// savedAs are the bookmarks the failed and the dead documents were saved
// as.
var savedAs = map[int]string{22: "Beta archive", 23: "Delta field notes"}

// cardAt is where the bookmarked documents' cards are embedded: at the
// query their titles name.
var cardAt = map[string]float32{"Beta archive — example.com": 20.25, "Delta field notes — example.com": 16.6}

func docURL(i int) string { return fmt.Sprintf("https://example.com/doc/%02d", i) }

// newLibrary writes the synthetic library's database at dir/copy.db and
// returns its path.
func newLibrary(t *testing.T, dir string) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(dir, "copy.db")
	db, err := sqlite.Open(ctx, path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	_, err = sqlite.Migrate(ctx, db)
	require.NoError(t, err)
	require.NoError(t, sqlite.EnsureVectorIndex(ctx, db, testDim))
	docs, extractions, chunks := sqlite.NewDocuments(db), sqlite.NewExtractions(db), sqlite.NewChunks(db, testDim)
	bookmarks := sqlite.NewBookmarks(db)
	for i := range libraryDocs {
		doc := &store.Document{ID: fmt.Sprintf("doc-%02d", i), TenantID: store.LocalTenantID, URL: docURL(i)}
		require.NoError(t, docs.Create(ctx, doc))
		if title, ok := savedAs[i]; ok {
			_, err := bookmarks.Ingest(ctx, &store.Bookmark{TenantID: store.LocalTenantID, URL: docURL(i),
				Title: &title, Source: store.SourceChrome, SavedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
			require.NoError(t, err)
		}
		if i == 21 {
			continue // pending, never fetched: no chunk
		}
		ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
			FetchedAt: time.Now().UTC()}
		require.NoError(t, extractions.Create(ctx, ext))
		require.NoError(t, docs.SetCurrentExtraction(ctx, doc.ID, ext.ID))
		words := keywords[i]
		if words == "" {
			words = "plain words"
		}
		text := fmt.Sprintf("%s note %02d about things", words, i)
		require.NoError(t, chunks.ReplaceForDocument(ctx, doc.ID, ext.ID, "", nil,
			[]store.ChunkInput{{Text: text, Embedding: at(float32(i))}}))
		switch i {
		case 20: // pending, as a refetch in flight leaves it
		case 22:
			require.NoError(t, docs.MarkFailed(ctx, doc.ID, store.FailureCauseAntiBot))
		case 23:
			require.NoError(t, docs.MarkFailed(ctx, doc.ID, store.FailureCauseDeadLink))
		default:
			require.NoError(t, docs.MarkFetched(ctx, doc.ID, 0))
		}
	}
	return path
}

// at is the vector at x on the documents' line.
func at(x float32) []float32 {
	v := make([]float32, testDim)
	v[0] = x
	return v
}

// farAway is the vector of any text the fake doesn't know: far past every
// document.
var farAway = at(1000)

// fakeOllama is an Ollama with testModel pulled, which embeds each
// query of testdata/queries.yaml at its place on the line (queryAt), each
// bookmarked document's card at its own (cardAt), and any other text far
// away. It fails a test that asks it to pull.
type fakeOllama struct {
	url string

	mu       sync.Mutex
	models   []string       // what /api/tags lists
	failText map[string]int // texts /api/embed answers 500 for, and how many more times; -1 for always
	embeds   []string       // every text embedded, in order
}

func newFakeOllama(t *testing.T) *fakeOllama {
	t.Helper()
	f := &fakeOllama{models: []string{testModel}, failText: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			f.tags(t, w)
		case "/api/embed":
			f.embed(t, w, r)
		case "/api/pull":
			t.Errorf("searcheval asked Ollama to pull a model")
			http.Error(w, "no pulls", http.StatusForbidden)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func (f *fakeOllama) tags(t *testing.T, w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	type model struct {
		Name   string `json:"name"`
		Model  string `json:"model"`
		Digest string `json:"digest"`
	}
	resp := struct {
		Models []model `json:"models"`
	}{Models: []model{}}
	for _, m := range f.models {
		resp.Models = append(resp.Models, model{Name: m, Model: m, Digest: "sha256:0"})
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		t.Errorf("encode the tags: %v", err)
	}
}

func (f *fakeOllama) embed(t *testing.T, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("decode an embed request: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Model != testModel {
		t.Errorf("embed with %q, not %q", req.Model, testModel)
	}
	out := struct {
		Embeddings [][]float32 `json:"embeddings"`
	}{}
	for _, text := range req.Input {
		f.embeds = append(f.embeds, text)
		if n := f.failText[text]; n != 0 {
			if n > 0 {
				f.failText[text] = n - 1
			}
			http.Error(w, `{"error":"model runner has unexpectedly stopped"}`, http.StatusInternalServerError)
			return
		}
		v := farAway
		if x, ok := queryAt[text]; ok {
			v = at(x)
		} else if x, ok := cardAt[text]; ok {
			v = at(x)
		}
		out.Embeddings = append(out.Embeddings, v)
	}
	if err := json.NewEncoder(w).Encode(out); err != nil {
		t.Errorf("encode the embeddings: %v", err)
	}
}

// sent is what the fake embedded besides the cards: the warm-up and the
// queries, in order. The cards, which name the library's host, are
// embedded before any query.
func (f *fakeOllama) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, text := range f.embeds {
		if !strings.Contains(text, "example.com") {
			out = append(out, text)
		}
	}
	return out
}

// cards is how many cards the fake embedded.
func (f *fakeOllama) cards() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, text := range f.embeds {
		if strings.Contains(text, "example.com") {
			n++
		}
	}
	return n
}

// failOn makes /api/embed fail for text, every time.
func (f *fakeOllama) failOn(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failText[text] = -1
}

// failOnce makes /api/embed fail for text the next time alone, as an
// Ollama busy with the daemon's indexing does.
func (f *fakeOllama) failOnce(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failText[text] = 1
}

// withModels sets what /api/tags lists.
func (f *fakeOllama) withModels(models ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models = models
}

// writeConfig writes a config.yaml in dir for the synthetic library,
// embedding through the Ollama at baseURL with no query prefix, its search
// the chunk lists alone (the document lists weighted 0) and search's keys
// (each a line indented under search:) besides, and returns its path.
func writeConfig(t *testing.T, dir, baseURL, search string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	cfg := fmt.Sprintf("embedding:\n  model: %s\n  dim: %d\n  base_url: %s\n  query_prefix: \"\"\n"+
		"search:\n  fields_weight: 0\n  cards_weight: 0\n  title_weight: 0\n%s", testModel, testDim, baseURL, search)
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o600))
	return path
}

// fixture is a synthetic library, its config and its Ollama.
type fixture struct {
	dir    string
	db     string
	config string
	ollama *fakeOllama
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{dir: dir, db: newLibrary(t, dir), ollama: newFakeOllama(t)}
	f.config = writeConfig(t, dir, f.ollama.url, "")
	return f
}

// queries is testdata/queries.yaml's path.
var queries = filepath.Join("testdata", "queries.yaml")

// evalResult is a run's exit code and output.
type evalResult struct {
	code           int
	stdout, stderr string
}

// runEval runs the command with args.
func runEval(t *testing.T, args ...string) evalResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return evalResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// args are the flags that run the fixture with qs and its config, then
// extra.
func (f *fixture) args(qs string, extra ...string) []string {
	return append([]string{"-db", f.db, "-queries", qs, "-config", f.config}, extra...)
}

// readReport reads a JSON report.
func readReport(t *testing.T, path string) *report {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var rep report
	require.NoError(t, json.Unmarshal(raw, &rep))
	return &rep
}
