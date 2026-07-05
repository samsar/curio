// Command curio-mcp is the Model Context Protocol sidecar for curio. It
// exposes the saved-bookmark corpus to MCP clients (Claude Code, Claude
// Desktop, …) over stdio, talking to the curio daemon via its local HTTP
// API. The daemon is auto-started if it isn't already running.
//
// stdout is reserved for the MCP (JSON-RPC) channel; all diagnostics go to
// stderr.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/version"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	c, err := setup()
	if err != nil {
		log.Error("curio-mcp startup failed", "err", err)
		os.Exit(1)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "curio", Version: version.Version}, nil)
	registerTools(srv, c)

	log.Info("curio-mcp serving over stdio", "tools", []string{"search_bookmarks", "get_document", "find_related"})
	// Run blocks until the client disconnects (stdin EOF) or the session
	// ends — normal lifecycle for a stdio sidecar, not a crash. Log the
	// reason and exit 0 so clients (e.g. Claude Code) don't report a failure.
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Info("curio-mcp shut down", "reason", err)
	}
}

// setup resolves $CURIO_HOME, loads config, ensures the daemon is running,
// and returns an HTTP client pointed at it. Mirrors the CLI's bootstrap.
func setup() (*client.Client, error) {
	homePath, err := curiohome.Resolve()
	if err != nil {
		return nil, err
	}
	home, err := curiohome.Open(homePath)
	if errors.Is(err, curiohome.ErrNotInitialized) {
		home, err = curiohome.Init(homePath, "nomic-embed-text", 768)
	}
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(home.ConfigPath()) // missing file → defaults
	if err != nil {
		return nil, err
	}
	base := "http://" + cfg.Daemon.Listen

	daemonBin := os.Getenv("CURIO_DAEMON_BIN")
	if daemonBin == "" {
		if exe, exeErr := os.Executable(); exeErr == nil {
			daemonBin = filepath.Join(filepath.Dir(exe), "curio-daemon")
		}
	}
	if err := daemonctl.New(home, daemonBin, base).EnsureRunning(); err != nil {
		return nil, fmt.Errorf("ensure daemon running: %w", err)
	}
	return client.New(base), nil
}

func registerTools(s *mcp.Server, c *client.Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_bookmarks",
		Description: "Hybrid keyword + semantic search over the user's saved bookmarks and articles. " +
			"Returns the most relevant documents with snippets and their doc_id. " +
			"Optionally filter by content type, bookmark source, or URL host.",
	}, searchHandler(c))

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_document",
		Description: "Fetch one saved document's metadata and full extracted markdown by its doc_id " +
			"(as returned by search_bookmarks or find_related).",
	}, getDocHandler(c))

	mcp.AddTool(s, &mcp.Tool{
		Name: "find_related",
		Description: "Given a doc_id, find other saved documents related to it by embedding similarity " +
			"over the document's indexed content (vector nearest-neighbor, not title matching).",
	}, relatedHandler(c))
}

// --- shared shapes ---

type docHit struct {
	DocID   string  `json:"doc_id"`
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet,omitempty"`
}

type searchOutput struct {
	Results []docHit `json:"results"`
}

// --- search_bookmarks ---

type searchInput struct {
	Query       string   `json:"query" jsonschema:"natural-language search query"`
	K           int      `json:"k,omitempty" jsonschema:"max results to return (default 10)"`
	ContentType []string `json:"content_type,omitempty" jsonschema:"filter by content type: article, repo, video, pdf, thread, unknown"`
	Source      []string `json:"source,omitempty" jsonschema:"filter by bookmark source: chrome, safari, firefox, html, manual"`
	Host        []string `json:"host,omitempty" jsonschema:"filter by URL host, e.g. github.com"`
}

func searchHandler(c *client.Client) mcp.ToolHandlerFor[searchInput, searchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, searchOutput, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, searchOutput{}, errors.New("query is required")
		}
		k := in.K
		if k <= 0 {
			k = 10
		}
		var filters *client.SearchFilters
		if len(in.ContentType) > 0 || len(in.Source) > 0 || len(in.Host) > 0 {
			filters = &client.SearchFilters{ContentType: in.ContentType, Source: in.Source, Host: in.Host}
		}
		res, err := c.Search(ctx, client.SearchRequest{Query: in.Query, K: k, Filters: filters})
		if err != nil {
			return nil, searchOutput{}, fmt.Errorf("search: %w", err)
		}
		out := toSearchOutput(res.Items, "")
		return textResult(formatHits(in.Query, out.Results)), out, nil
	}
}

// --- get_document ---

type getDocInput struct {
	ID string `json:"id" jsonschema:"the document ID (doc_id from search_bookmarks)"`
}

type getDocOutput struct {
	DocID       string `json:"doc_id"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url"`
	ContentType string `json:"content_type,omitempty"`
	State       string `json:"state"`
	Markdown    string `json:"markdown"`
}

func getDocHandler(c *client.Client) mcp.ToolHandlerFor[getDocInput, getDocOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in getDocInput) (*mcp.CallToolResult, getDocOutput, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, getDocOutput{}, errors.New("id is required")
		}
		doc, err := c.GetDocument(ctx, in.ID)
		if err != nil {
			return nil, getDocOutput{}, fmt.Errorf("get document: %w", err)
		}
		out := getDocOutput{
			DocID: doc.ID, Title: docTitle(*doc), URL: doc.URL,
			ContentType: doc.ContentType, State: doc.State,
		}
		// Content is best-effort: a doc may not have an extraction yet.
		if md, cerr := c.GetDocumentContent(ctx, in.ID); cerr == nil {
			out.Markdown = md
		}
		text := out.Markdown
		if text == "" {
			text = fmt.Sprintf("# %s\n%s\n\n(no extracted content available; document state: %s)", out.Title, out.URL, out.State)
		}
		return textResult(text), out, nil
	}
}

// --- find_related ---

type relatedInput struct {
	ID string `json:"id" jsonschema:"document ID to find related documents for"`
	K  int    `json:"k,omitempty" jsonschema:"max related documents (default 5)"`
}

func relatedHandler(c *client.Client) mcp.ToolHandlerFor[relatedInput, searchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in relatedInput) (*mcp.CallToolResult, searchOutput, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, searchOutput{}, errors.New("id is required")
		}
		k := in.K
		if k <= 0 {
			k = 5
		}
		res, err := c.RelatedDocuments(ctx, in.ID, k)
		if err != nil {
			return nil, searchOutput{}, fmt.Errorf("find related: %w", err)
		}
		// The daemon already excludes the source document; keep the
		// client-side exclusion as belt-and-braces.
		out := toSearchOutput(res.Items, in.ID)
		if len(out.Results) > k {
			out.Results = out.Results[:k]
		}
		return textResult(formatHits("related to "+in.ID, out.Results)), out, nil
	}
}

// --- helpers ---

// toSearchOutput maps client search hits to docHits, optionally excluding one
// document ID (used by find_related to drop the source doc).
func toSearchOutput(items []client.SearchHit, excludeID string) searchOutput {
	out := searchOutput{Results: make([]docHit, 0, len(items))}
	for _, hit := range items {
		if hit.Document.ID == excludeID {
			continue
		}
		out.Results = append(out.Results, docHit{
			DocID:   hit.Document.ID,
			Title:   docTitle(hit.Document),
			URL:     hit.Document.URL,
			Score:   hit.Score,
			Snippet: firstSnippet(hit.Matches),
		})
	}
	return out
}

func docTitle(d client.Document) string {
	if d.Title != nil && strings.TrimSpace(*d.Title) != "" {
		return *d.Title
	}
	return d.URL
}

func firstSnippet(m []client.ChunkMatch) string {
	if len(m) == 0 {
		return ""
	}
	s := m[0].Snippet
	if s == "" {
		s = m[0].Text
	}
	// Strip the FTS emphasis markers — noise for an LLM reader.
	return strings.TrimSpace(strings.NewReplacer("<em>", "", "</em>", "").Replace(s))
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func formatHits(label string, hits []docHit) string {
	if len(hits) == 0 {
		return fmt.Sprintf("No results for %q.", label)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d results for %q:\n\n", len(hits), label)
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   doc_id: %s  (score %.4f)\n", i+1, h.Title, h.URL, h.DocID, h.Score)
		if h.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", h.Snippet)
		}
		b.WriteString("\n")
	}
	return b.String()
}
