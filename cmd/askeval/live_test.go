package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/devtool"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/understand"
)

// liveDim is the width of the live test's library and its fake embedder.
const liveDim = 8

// newModelServer is an Ollama server that serves every model as loaded:
// the writer's stream answers the fitness check's probes as a fit model
// does and any other prompt with a cited sentence; a structured call gets
// a reply that isn't JSON, which the planner's and the reranker's checks
// refuse; an embed is a vector of liveDim for each input.
func newModelServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string          `json:"model"`
			Prompt    string          `json:"prompt"`
			Stream    bool            `json:"stream"`
			Format    json.RawMessage `json:"format"`
			KeepAlive any             `json:"keep_alive"`
			Input     []string        `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch r.URL.Path {
		case "/api/ps", "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"gemma4:26b","model":"gemma4:26b"}]}`)
		case "/api/embed":
			vectors := make([]string, len(req.Input))
			for i := range vectors {
				vectors[i] = "[1" + strings.Repeat(",0", liveDim-1) + "]"
			}
			fmt.Fprintf(w, `{"embeddings":[%s]}`, strings.Join(vectors, ","))
		case "/api/generate":
			switch {
			case req.Prompt == "":
				fmt.Fprint(w, `{"done":true,"done_reason":"load"}`)
			case req.Stream:
				line, _ := json.Marshal(map[string]any{"response": probeAnswer(req.Prompt), "done": false})
				fmt.Fprintf(w, "%s\n{\"response\":\"\",\"done\":true,\"done_reason\":\"stop\"}\n", line)
			default:
				fmt.Fprint(w, `{"response":"not json","done":true,"done_reason":"stop"}`)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// probeAnswer is a fit writer's answer to prompt: each probe's as the
// check wants it, and for any other question one sentence citing [1].
func probeAnswer(prompt string) string {
	switch question := prompt[strings.LastIndex(prompt, "Question: "):]; {
	case strings.Contains(question, "sourdough"):
		return "Let it cool for at least an hour before slicing [2]."
	case strings.Contains(question, "mercury"):
		return ask.Abstention
	case strings.Contains(question, "spring tides"):
		return "Spring tides come at the new moon and the full moon [3]."
	}
	return "Tides follow the moon [1]."
}

// newLiveLibrary writes a copy's database at dir/copy.db holding one
// fetched page whose one chunk mentions tides, and returns its path.
func newLiveLibrary(t *testing.T, dir string) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(dir, "copy.db")
	db, err := sqlite.Open(ctx, path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	_, err = sqlite.Migrate(ctx, db)
	require.NoError(t, err)
	require.NoError(t, sqlite.EnsureVectorIndex(ctx, db, liveDim))
	docs, extractions := sqlite.NewDocuments(db), sqlite.NewExtractions(db)
	doc := &store.Document{ID: "doc-tides", TenantID: store.LocalTenantID, URL: "https://example.com/tides"}
	require.NoError(t, docs.Create(ctx, doc))
	markdown := filepath.Join(doc.ID, "extraction.md")
	ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
		FetchedAt: time.Now().UTC(), MarkdownPath: &markdown}
	require.NoError(t, extractions.Create(ctx, ext))
	require.NoError(t, docs.SetCurrentExtraction(ctx, doc.ID, ext.ID))
	vector := make([]float32, liveDim)
	vector[0] = 1
	require.NoError(t, sqlite.NewChunks(db, liveDim).ReplaceForDocument(ctx, doc.ID, ext.ID, "", nil,
		[]store.ChunkInput{{Text: "Tides follow the moon, twice a month at their strongest.", Embedding: vector}}))
	require.NoError(t, docs.MarkFetched(ctx, doc.ID, 0))
	return path
}

// TestAskOne_ReplayRecordsLive: a replay that records asks a question its
// book lacks live, through the planner and the reranker of the variant it
// records, so the ask's understanding and reranking are theirs, never
// off: here the planner failed its check, and the reranker skips a search
// without a plan, as its routes do. The ask is written to the book.
func TestAskOne_ReplayRecordsLive(t *testing.T) {
	url := newModelServer(t)
	dir := t.TempDir()
	lib, err := devtool.OpenCopy(t.Context(), newLiveLibrary(t, dir))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, lib.Close()) })
	cfg := config.Default()
	cfg.Embedding.BaseURL, cfg.Embedding.Dim = url, lib.Width
	cfg.Generation.BaseURL, cfg.Generation.Model = url, "gemma4:26b"
	cfg.Search.Rerank, cfg.Ask.Rerank = new(true), true
	book, recorded := filepath.Join(dir, "answers.jsonl"), filepath.Join(dir, "answers-out.jsonl")
	require.NoError(t, os.WriteFile(book, nil, 0o600))
	log := slog.New(slog.DiscardHandler)
	opts := options{answers: book, answersOut: recorded, judgeModel: "none", today: time.Now()}

	s, err := build(t.Context(), opts, cfg, lib, log)
	require.NoError(t, err)
	defer s.close(log)
	checks := s.checks(t.Context(), opts, log)
	require.Equal(t, "fit", checks["writer"], "%v", checks)

	q := question{ID: "q1", Question: "how do tides work", Facts: []string{"-"}}
	var got ask.Retrieved
	require.NoError(t, s.asker.Ask(t.Context(), request("ask", q), func(e ask.Event) error {
		if r, ok := e.(ask.Retrieved); ok {
			got = r
		}
		return nil
	}))
	require.NotNil(t, got.Understanding, "understood")
	assert.Equal(t, understand.StatusUnfit, got.Understanding.Status)
	require.NotNil(t, got.Reranking, "reranked")
	assert.Equal(t, rerank.StatusSkipped, got.Reranking.Status, got.Reranking.Reason)

	a, err := s.askOne(t.Context(), q.ID, "ask", q, log)
	require.NoError(t, err)
	assert.Equal(t, string(ask.StatusAnswered), a.status)
	written, err := os.ReadFile(recorded)
	require.NoError(t, err)
	assert.Contains(t, string(written), `"id":"q1"`, "recorded in the book")
}
