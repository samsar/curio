package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
)

// fakeDaemon serves the subset of the curio HTTP API the MCP tools call.
func fakeDaemon(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"query": "q",
			"items": []map[string]any{{
				"document": map[string]any{
					"id": "doc-1", "url": "https://example.com/a", "title": "Alpha",
					"content_type": "article", "state": "fetched",
				},
				"score":   0.42,
				"matches": []map[string]any{{"chunk_id": "c1", "text": "alpha body", "snippet": "alpha <em>body</em>"}},
			}},
		})
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
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// connectMCP builds the MCP server with our tools (pointed at a fake daemon)
// and returns a connected in-memory client session.
func connectMCP(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	c := client.New(fakeDaemon(t).URL)

	srv := mcp.NewServer(&mcp.Implementation{Name: "curio-test", Version: "test"}, nil)
	registerTools(srv, c)

	clientT, serverT := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverT, nil)
	require.NoError(t, err)
	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := cli.Connect(ctx, clientT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
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
	// The fake daemon's only hit is doc-1 itself, which find_related drops.
	assert.Contains(t, textOf(res), "No results")
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
