package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
)

// fakeDaemon serves the subset of the curio HTTP API the MCP tools call and
// stores each /v1/search request body in lastSearch. The query "offline" gets
// a degraded (keyword-only) search response.
func fakeDaemon(t *testing.T, lastSearch *atomic.Value) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
			return
		}
		lastSearch.Store(req)
		resp := map[string]any{
			"query": req["query"],
			"items": []map[string]any{{
				"document": map[string]any{
					"id": "doc-1", "url": "https://example.com/a", "title": "Alpha",
					"content_type": "article", "state": "fetched",
				},
				"score":   0.42,
				"matches": []map[string]any{{"chunk_id": "c1", "text": "alpha body", "snippet": "alpha <em>body</em>"}},
			}},
		}
		if req["query"] == "offline" {
			resp["degraded"] = true
			resp["warnings"] = []string{"semantic search unavailable (connection refused); keyword-only results"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/v1/documents/doc-1/content", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# Alpha\n\nfull markdown body"))
	})
	mux.HandleFunc("/v1/documents/doc-1", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "doc-1", "url": "https://example.com/a", "title": "Alpha",
			"content_type": "article", "state": "fetched",
		})
	})
	mux.HandleFunc("/v1/documents/doc-1/related", func(w http.ResponseWriter, _ *http.Request) {
		// Includes the source doc itself to exercise the sidecar's
		// belt-and-braces exclusion (the real daemon already drops it).
		_ = json.NewEncoder(w).Encode(map[string]any{
			"doc_id": "doc-1",
			"items": []map[string]any{
				{
					"document": map[string]any{
						"id": "doc-1", "url": "https://example.com/a", "title": "Alpha",
						"content_type": "article", "state": "fetched",
					},
					"score": 0.99,
				},
				{
					"document": map[string]any{
						"id": "doc-2", "url": "https://example.com/b", "title": "Beta",
						"content_type": "article", "state": "fetched",
					},
					"score": 0.61,
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// session is a connected MCP client plus what the fake daemon behind it saw.
type session struct {
	*mcp.ClientSession
	lastSearch atomic.Value // the most recent /v1/search request body
}

// connectMCP builds the MCP server with our tools (pointed at a fake daemon)
// and returns a connected in-memory client session.
func connectMCP(t *testing.T) *session {
	t.Helper()
	ctx := context.Background()
	sess := &session{}
	c := client.New(fakeDaemon(t, &sess.lastSearch).URL)

	srv := mcp.NewServer(&mcp.Implementation{Name: "curio-test", Version: "test"}, nil)
	registerTools(srv, c)

	clientT, serverT := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverT, nil)
	require.NoError(t, err)
	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := cli.Connect(ctx, clientT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	sess.ClientSession = cs
	return sess
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMCP_ListsAllTools(t *testing.T) {
	cs := connectMCP(t)
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = true
	}
	assert.True(t, got["search_bookmarks"], "search_bookmarks registered")
	assert.True(t, got["get_document"], "get_document registered")
	assert.True(t, got["find_related"], "find_related registered")
}

func TestMCP_SearchBookmarks(t *testing.T) {
	cs := connectMCP(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search_bookmarks",
		Arguments: map[string]any{"query": "alpha"},
	})
	require.NoError(t, err)
	txt := textOf(res)
	assert.Contains(t, txt, "Alpha")
	assert.Contains(t, txt, "doc_id: doc-1")
	assert.NotContains(t, txt, "<em>", "FTS emphasis markers should be stripped")
}

func TestMCP_GetDocument(t *testing.T) {
	cs := connectMCP(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_document",
		Arguments: map[string]any{"id": "doc-1"},
	})
	require.NoError(t, err)
	assert.Contains(t, textOf(res), "full markdown body")
}

func TestMCP_FindRelated_ExcludesSelf(t *testing.T) {
	cs := connectMCP(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "find_related",
		Arguments: map[string]any{"id": "doc-1"},
	})
	require.NoError(t, err)
	txt := textOf(res)
	// doc-2 is a real neighbor; doc-1 (the source, echoed by the fake
	// daemon) must be dropped client-side.
	assert.Contains(t, txt, "doc_id: doc-2")
	assert.NotContains(t, txt, "doc_id: doc-1")
}

func TestMCP_SearchRequiresQuery(t *testing.T) {
	cs := connectMCP(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search_bookmarks",
		Arguments: map[string]any{"query": ""},
	})
	// A handler error surfaces as a tool error result, not a transport error.
	if err == nil {
		assert.True(t, res.IsError, "empty query should be a tool error")
	}
}

func TestMCP_SearchLeavesKToTheDaemon(t *testing.T) {
	cs := connectMCP(t)
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search_bookmarks",
		Arguments: map[string]any{"query": "alpha"},
	})
	require.NoError(t, err)
	req := cs.lastSearch.Load().(map[string]any)
	assert.NotContains(t, req, "k", "an omitted k is left to the daemon's search.default_k")
}

func TestMCP_SearchDegraded(t *testing.T) {
	cs := connectMCP(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search_bookmarks",
		Arguments: map[string]any{"query": "offline"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	txt := textOf(res)
	assert.True(t, strings.HasPrefix(txt,
		"Note: semantic search unavailable (connection refused); keyword-only results.\n\n"), txt)
	assert.Contains(t, txt, "doc_id: doc-1", "the keyword results are still listed")

	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out searchOutput
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.True(t, out.Degraded)
	assert.NotEmpty(t, out.Warnings)
	assert.Len(t, out.Results, 1)
}

func TestDegradedNote(t *testing.T) {
	assert.Equal(t, "Note: semantic search unavailable; keyword-only results.", degradedNote(nil),
		"a degraded response without warnings still gets the note")
	assert.Equal(t, "Note: first; second.", degradedNote([]string{"first", "second"}))
}
