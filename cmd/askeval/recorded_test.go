package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/devtool"
	"github.com/samsar/curio/internal/ollama"
)

// recordedReport is a made-up earlier report of qf, judged by gemma4:12b
// and qwen3:4b-instruct: question q1 answered from a source, both judges'
// labels kept.
func recordedReport(qf questionFile) report {
	yes, no := true, false
	return report{QuestionsSHA256: qf.sha256, Judge: "gemma4:12b", Judge2: "qwen3:4b-instruct",
		PromptVersion: "0123456789abcdef", Variants: []variantResult{{Name: "ask", Questions: []questionResult{
			{ID: "q1", Kind: "one", Route: "answer", Status: "answered", Citations: []int{1},
				Sources: []string{"https://Example.com/bread#top"}, FirstMS: 6900, TotalMS: 7600,
				Sentences: []sentenceEntry{
					{Text: "Bread cools for an hour [1].", Cited: true, Claim: true, Support: "supported",
						Claim2: &yes, Support2: "partly"},
					{Text: "Enjoy.", Claim: false, Support: "none", Claim2: &no, Support2: "none"}},
				Facts: []factEntry{{Fact: "an hour", Stated: true, Stated2: &yes}}},
		}}}}
}

// writeReport writes r as a report file under dir.
func writeReport(t *testing.T, dir string, r report) string {
	t.Helper()
	path := filepath.Join(dir, "report.json")
	data, err := json.Marshal(r)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// TestReadRecorded: a report of another question file, or judged by other
// judges, is refused, naming why.
func TestReadRecorded(t *testing.T) {
	dir := t.TempDir()
	qpath := filepath.Join(dir, "q.yaml")
	require.NoError(t, os.WriteFile(qpath, []byte(questionsYAML), 0o600))
	qf, err := readQuestions(qpath)
	require.NoError(t, err)
	opts := options{judgeModel: "gemma4:12b", judge2Model: "qwen3:4b-instruct"}
	opts.recorded = writeReport(t, dir, recordedReport(qf))
	got, err := readRecorded(opts, qf)
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef", got.PromptVersion)

	opts.judge2Model = ""
	_, err = readRecorded(opts, qf)
	require.NoError(t, err, "without a second judge its labels go unused")
	opts.judge2Model = "qwen3:4b-instruct"

	for name, change := range map[string]func(*report){
		"another question file": func(r *report) { r.QuestionsSHA256 = "other" },
		"another judge":         func(r *report) { r.Judge = "gemma4:26b" },
		"another second judge":  func(r *report) { r.Judge2 = "" },
	} {
		r := recordedReport(qf)
		change(&r)
		opts.recorded = writeReport(t, dir, r)
		_, err := readRecorded(opts, qf)
		require.Error(t, err, name)
	}
}

// TestAnsweredOf: a recorded answer is scored as it was judged: its
// sentences, their citations read as the answer route reads them, its
// cited pages, both judges' labels, and its timings marked replayed.
func TestAnsweredOf(t *testing.T) {
	r := recordedReport(questionFile{sha256: "x"})
	q := question{ID: "q1", Kind: "one", Question: "How long should bread cool?",
		Sources: []string{"https://example.com/bread"}, Facts: []string{"an hour"}}
	a, ok := r.answeredOf(q)
	require.True(t, ok)
	assert.Equal(t, []string{"Bread cools for an hour [1].", "Enjoy."}, a.sents)
	assert.Equal(t, []bool{true, false}, a.sentCited)
	assert.Equal(t, [][]string{{"1"}, nil}, a.sentCitations)
	assert.Equal(t, []string{"https://example.com/bread"}, a.citedURLs)
	assert.True(t, a.judged)
	assert.True(t, a.judged2)
	assert.Equal(t, "partly", a.verdicts2.Sentences[0].Support)
	assert.True(t, a.timingsReplayed)
	assert.Equal(t, stageMS{FirstToken: 6900, Total: 7600}, a.stages)

	m := measure([]answered{a})
	assert.InDelta(t, 1, m.SourceRecall, 1e-9)
	assert.InDelta(t, 1, m.FactCoverage, 1e-9)
	assert.InDelta(t, 0.5, measure(byJudge2([]answered{a})).CitationSupport, 1e-9)

	_, ok = r.answeredOf(question{ID: "q9"})
	assert.False(t, ok)
}

// fakeOllama is an Ollama server that holds models, answers /api/ps
// with them, and unloads one asked to.
type fakeOllama struct {
	mu       sync.Mutex
	held     []string
	unloaded []string
	url      string
}

func newFakeOllama(t *testing.T, held ...string) *fakeOllama {
	t.Helper()
	f := &fakeOllama{held: held}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		switch r.URL.Path {
		case "/api/ps":
			models := make([]string, 0, len(f.held))
			for _, m := range f.held {
				models = append(models, fmt.Sprintf(`{"name":%q,"model":%q}`, m, m))
			}
			fmt.Fprintf(w, `{"models":[%s]}`, strings.Join(models, ","))
		case "/api/generate":
			f.unloaded = append(f.unloaded, req.Model)
			fmt.Fprint(w, `{"done":true,"done_reason":"unload"}`)
		case "/api/embed":
			f.unloaded = append(f.unloaded, req.Model)
			fmt.Fprint(w, `{"embeddings":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// TestHoldings_PerServer: what Ollama holds is read at each server the
// run's models use; a model held at one and loaded by the run at the
// other is unloaded only where it wasn't held, and a model evicted is
// named with its server.
func TestHoldings_PerServer(t *testing.T) {
	a := newFakeOllama(t, "gemma4:26b", "qwen3-embedding:0.6b")
	b := newFakeOllama(t)
	cfg := config.Default()
	cfg.Embedding.BaseURL = a.url
	cfg.Generation.BaseURL = b.url
	models := []served{servedOf(a.url, "qwen3-embedding:0.6b"), servedOf(a.url, "gemma4:26b"),
		servedOf(b.url, "gemma4:26b")}
	before, err := holdings(t.Context(), models)
	require.NoError(t, err)
	assert.Len(t, before[a.url], 2)
	assert.Empty(t, before[b.url])

	s := &setup{cfg: cfg, models: models, before: before, stop: func() {},
		loaded: []served{servedOf(a.url, "gemma4:26b"), servedOf(b.url, "gemma4:26b")}}
	a.mu.Lock()
	a.held = []string{"qwen3-embedding:0.6b"}
	a.mu.Unlock()
	assert.Equal(t, []string{"gemma4:26b at " + serverOf(a.url)}, s.evicted(t.Context(),
		slog.New(slog.DiscardHandler)))

	s.answers, _ = openBook("", "")
	s.close(slog.New(slog.DiscardHandler))
	assert.Empty(t, a.unloaded, "held before at a: neither the writer nor the embedder unloaded there")
	assert.Equal(t, []string{"gemma4:26b"}, b.unloaded, "loaded by the run at b alone")
	assert.Equal(t, served{baseURL: ollama.DefaultBaseURL, model: "gemma4:26b"}, servedOf("", "gemma4:26b"))
}

// TestBuild_LiveRoles: a run that may ask a model live, recording a
// replay's missing asks included, gets the planner and the reranker of
// the variant it records; a replay alone asks no model and gets neither.
func TestBuild_LiveRoles(t *testing.T) {
	o := newFakeOllama(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "copy.db")
	require.NoError(t, os.WriteFile(db, nil, 0o600))
	lib, err := devtool.OpenCopy(t.Context(), db)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, lib.Close()) })
	cfg := config.Default()
	cfg.Embedding.BaseURL, cfg.Embedding.Dim, cfg.Generation.BaseURL = o.url, lib.Width, o.url
	book := filepath.Join(dir, "answers.jsonl")
	require.NoError(t, os.WriteFile(book, nil, 0o600))

	for _, tc := range []struct {
		name         string
		answersOut   string
		planned, rrk bool
	}{
		{"a replay that records", filepath.Join(dir, "answers-out.jsonl"), true, true},
		{"a replay alone", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := build(t.Context(), options{answers: book, answersOut: tc.answersOut, judgeModel: "none",
				today: time.Now()}, cfg, lib, slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			defer s.close(slog.New(slog.DiscardHandler))
			assert.Equal(t, tc.planned, s.planner != nil, "the planner")
			assert.Equal(t, tc.rrk, s.rerank != nil, "the reranker")
			assert.True(t, s.asker.State().On, "the writer is measured whatever ask.enabled says")
		})
	}
}
