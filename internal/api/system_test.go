package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/store"
)

func TestStats(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	for i, st := range []store.DocState{store.DocStateFetched, store.DocStateFetched, store.DocStateFailed, store.DocStatePending} {
		doc := s.seedDocument(t, "https://example.com/"+string(rune('a'+i)), st)
		require.NoError(t, s.deps.Bookmarks.Create(ctx, &store.Bookmark{TenantID: "local", URL: doc.URL,
			Source: store.SourceManual, SavedAt: time.Now().UTC(), DocumentID: &doc.ID}))
	}
	for _, st := range []store.JobStatus{store.JobStatusDone, store.JobStatusDone, store.JobStatusRunning} {
		require.NoError(t, s.deps.Queue.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch, Status: st}))
	}

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/stats"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got Stats
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, 4, got.BookmarksTotal)
	assert.Equal(t, 4, got.DocumentsTotal)
	assert.Equal(t, map[string]int{"fetched": 2, "failed": 1, "pending": 1}, got.DocumentsByState)
	assert.Equal(t, map[string]int{"done": 2, "running": 1}, got.JobsByStatus)
}

var errInjected = errors.New("injected")

type failingBookmarkCount struct{ store.BookmarkStore }

func (failingBookmarkCount) Count(context.Context, string) (int, error) { return 0, errInjected }

type failingDocumentCount struct{ store.DocumentStore }

func (failingDocumentCount) CountByState(context.Context, string) (map[store.DocState]int, error) {
	return nil, errInjected
}

type failingJobCount struct{ store.JobStore }

func (failingJobCount) CountByStatus(context.Context, string) (map[store.JobStatus]int, error) {
	return nil, errInjected
}

// TestStats_CountFailure: a count the store can't produce fails the request
// rather than showing up as a zero or a missing field.
func TestStats_CountFailure(t *testing.T) {
	cases := map[string]func(*Deps){
		"bookmarks": func(d *Deps) { d.Bookmarks = failingBookmarkCount{d.Bookmarks} },
		"documents": func(d *Deps) { d.Documents = failingDocumentCount{d.Documents} },
		"jobs":      func(d *Deps) { d.Queue = failingJobCount{d.Queue} },
	}
	for name, fault := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t, fault)
			assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/stats"}), http.StatusInternalServerError)
		})
	}
}

// pingingEmbedder is an embedder whose Ping fails with err.
type pingingEmbedder struct {
	embedder.Embedder
	err error
}

func (p pingingEmbedder) Ping(context.Context) error { return p.err }

// TestHealth_OllamaDetail: healthz turns the shared Ollama sentinels into
// advice, whichever client produced them, and quotes anything else.
func TestHealth_OllamaDetail(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantDetail string
	}{
		{"model not pulled", fmt.Errorf("%w: nomic-embed-text", ollama.ErrModelNotLoaded), "model not pulled"},
		{"unreachable", fmt.Errorf("%w: dial tcp: connection refused", ollama.ErrUnreachable), "ollama unreachable"},
		{"anything else", errors.New("decode response: unexpected EOF"), "decode response: unexpected EOF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, func(d *Deps) { d.Embedder = pingingEmbedder{d.Embedder, tc.err} })
			resp := s.do(t, request{method: http.MethodGet, path: "/v1/healthz"})
			require.Equal(t, http.StatusOK, resp.status, resp.body)
			var h Health
			require.NoError(t, json.Unmarshal([]byte(resp.body), &h))
			assert.False(t, h.OllamaReachable)
			assert.Contains(t, h.OllamaDetail, tc.wantDetail)
		})
	}
}
