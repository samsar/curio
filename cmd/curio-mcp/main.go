// Command curio-mcp is the Model Context Protocol sidecar for curio. It
// exposes the saved-bookmark corpus to MCP clients (Claude Code, Claude
// Desktop, …) over stdio, talking to the curio daemon via its local HTTP
// API. The daemon is auto-started if it isn't already running, and started
// again if it stops during the session; a tool call that finds it still
// starting waits for it.
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
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/version"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	env, err := daemonctl.Discover("", "")
	if err != nil {
		log.Error("curio-mcp startup failed", "err", err)
		os.Exit(1)
	}
	d, err := setup(context.Background(), env, log)
	if err != nil {
		log.Error("curio-mcp startup failed", "err", err)
		os.Exit(1)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "curio", Version: version.Version}, nil)
	registerTools(srv, d)

	log.Info("curio-mcp serving over stdio", "tools", []string{"search_bookmarks", "get_document", "find_related", "list_interests"})
	// Run blocks until the client disconnects (stdin EOF) or the session
	// ends — normal lifecycle for a stdio sidecar, not a crash. Log the
	// reason and exit 0 so clients (e.g. Claude Code) don't report a failure.
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Info("curio-mcp shut down", "reason", err)
	}
}

// daemon is the sidecar's handle on the curio daemon: a client, and a way
// to start the daemon when a call finds it gone.
type daemon struct {
	client *client.Client
	// ensure returns once the daemon is ready, starting it if need be
	// (daemonctl.Controller.EnsureRunning).
	ensure func(context.Context) error
}

// mcpReadyWait bounds how long a tool call waits for a starting daemon to
// become ready. MCP clients cancel a stdio tool call after about 60s (the
// Claude desktop app does, whatever MCP_TOOL_TIMEOUT says), so this leaves
// room for the retried request itself; a call that runs out says the
// daemon is still starting.
const mcpReadyWait = 30 * time.Second

// setup ensures the daemon env finds is started, and returns the handle
// the tools use. Starting it here rather than at the first tool call makes
// a daemon that can't start, or a port served for another home, fail the
// sidecar at startup, where the MCP client shows it. It doesn't wait for a
// starting daemon to be ready: a migration can outlast the time an MCP
// client gives a server to connect (30s by default in Claude Code), and
// tool calls wait for it instead.
func setup(ctx context.Context, env daemonctl.Env, log *slog.Logger) (daemon, error) {
	ctl := env.Controller
	ctl.ReadyTimeout = mcpReadyWait
	ctl.OnMigrating = func(s client.Startup) {
		log.Info("curio-daemon is migrating its database", "pid", s.PID, "progress", s.Progress())
	}
	starting, err := ctl.EnsureStarted(ctx)
	if err != nil {
		return daemon{}, fmt.Errorf("ensure daemon started: %w", err)
	}
	if starting != nil {
		log.Info("curio-daemon is still starting; tool calls will wait for it",
			"pid", starting.PID, "progress", starting.Progress())
	}
	return daemon{client: env.Client, ensure: ctl.EnsureRunning}, nil
}

// call runs fn, one request to the daemon. The sidecar lives for a whole
// client session, and the daemon may stop underneath it (`curio daemon
// stop` after a config change, an upgrade, a crash) or still be starting.
// So when fn finds the daemon unreachable or starting, call ensures it is
// running and runs fn once more, which is safe for any request: an
// unreachable daemon received nothing, and a starting one ran nothing.
// Other errors are returned as they are; there is no second attempt.
func call[T any](ctx context.Context, d daemon, fn func(context.Context) (T, error)) (T, error) {
	v, err := fn(ctx)
	if !errors.Is(err, client.ErrDaemonUnreachable) && !errors.Is(err, client.ErrStarting) {
		return v, err
	}
	if startErr := d.ensure(ctx); startErr != nil {
		if errors.Is(startErr, daemonctl.ErrStillStarting) {
			return v, fmt.Errorf("%w; try again in a minute", startErr)
		}
		return v, fmt.Errorf("%w; restarting the daemon failed: %w", err, startErr)
	}
	return fn(ctx)
}

func registerTools(s *mcp.Server, d daemon) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_bookmarks",
		Description: "Hybrid keyword + semantic search over the user's saved bookmarks and articles. " +
			"Returns the most relevant documents with snippets and their doc_id. " +
			"Optionally filter by content type, bookmark source, or URL host.",
	}, searchHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_document",
		Description: "Fetch one saved document's metadata and full extracted markdown by its doc_id " +
			"(as returned by search_bookmarks or find_related).",
	}, getDocHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "find_related",
		Description: "Given a doc_id, find other saved documents related to it by embedding similarity " +
			"over the document's indexed content (vector nearest-neighbor, not title matching).",
	}, relatedHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_interests",
		Description: "List the user's inferred interests: topic clusters discovered across their saved " +
			"library, each with a label, summary, size, and representative documents (with doc_ids). " +
			"Use this to understand what the user reads about at a high level, or to pick a topic to " +
			"drill into with search_bookmarks / get_document. If empty, clustering hasn't run yet " +
			"(the user can run `curio interests rebuild`).",
	}, listInterestsHandler(d))
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
	// Degraded means semantic search was unavailable and Results are
	// keyword-only; Warnings says why.
	Degraded bool     `json:"degraded,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// --- search_bookmarks ---

type searchInput struct {
	Query       string   `json:"query" jsonschema:"natural-language search query"`
	K           int      `json:"k,omitempty" jsonschema:"max results to return, 1 to 100 (default: the daemon's search.default_k, 10 unless configured)"`
	ContentType []string `json:"content_type,omitempty" jsonschema:"filter by content type: article, repo, video, pdf, thread, unknown"`
	Source      []string `json:"source,omitempty" jsonschema:"filter by bookmark source: chrome, safari, firefox, html, manual"`
	Host        []string `json:"host,omitempty" jsonschema:"filter by URL host, e.g. github.com"`
}

func searchHandler(d daemon) mcp.ToolHandlerFor[searchInput, searchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, searchOutput, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, searchOutput{}, errors.New("query is required")
		}
		var filters *client.SearchFilters
		if len(in.ContentType) > 0 || len(in.Source) > 0 || len(in.Host) > 0 {
			filters = &client.SearchFilters{ContentType: in.ContentType, Source: in.Source, Host: in.Host}
		}
		req := client.SearchRequest{Query: in.Query, K: in.K, Filters: filters}
		res, err := call(ctx, d, func(ctx context.Context) (*client.SearchResponse, error) {
			return d.client.Search(ctx, req)
		})
		if err != nil {
			return nil, searchOutput{}, fmt.Errorf("search: %w", err)
		}
		out := toSearchOutput(res.Items, "")
		out.Degraded, out.Warnings = res.Degraded, res.Warnings
		text := formatHits(in.Query, out.Results)
		if res.Degraded {
			text = degradedNote(res.Warnings) + "\n\n" + text
		}
		return textResult(text), out, nil
	}
}

// degradedNote tells the model that the hits are keyword-only. The daemon's
// warnings already say that and why, so they are quoted as they are.
func degradedNote(warnings []string) string {
	if len(warnings) == 0 {
		return "Note: semantic search unavailable; keyword-only results."
	}
	return "Note: " + strings.Join(warnings, "; ") + "."
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

func getDocHandler(d daemon) mcp.ToolHandlerFor[getDocInput, getDocOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in getDocInput) (*mcp.CallToolResult, getDocOutput, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, getDocOutput{}, errors.New("id is required")
		}
		doc, err := call(ctx, d, func(ctx context.Context) (*client.Document, error) {
			return d.client.GetDocument(ctx, in.ID)
		})
		if client.IsNotFound(err) {
			return nil, getDocOutput{}, fmt.Errorf("document %q not found", in.ID)
		}
		if err != nil {
			return nil, getDocOutput{}, fmt.Errorf("get document: %w", err)
		}
		out := getDocOutput{
			DocID: doc.ID, Title: docTitle(*doc), URL: doc.URL,
			ContentType: doc.ContentType, State: doc.State,
		}
		// A document that hasn't been fetched yet has no content, so a 404
		// here is an answer. Any other failure is reported, not passed off
		// as a document without content.
		out.Markdown, err = call(ctx, d, func(ctx context.Context) (string, error) {
			return d.client.GetDocumentContent(ctx, in.ID)
		})
		if err != nil && !client.IsNotFound(err) {
			return nil, getDocOutput{}, fmt.Errorf("get the content of document %q: %w", in.ID, err)
		}
		if out.Markdown == "" {
			return textResult(fmt.Sprintf("# %s\n%s\n\n(no extracted content available; document state: %s)",
				out.Title, out.URL, out.State)), out, nil
		}
		return textResult(out.Markdown), out, nil
	}
}

// --- find_related ---

type relatedInput struct {
	ID string `json:"id" jsonschema:"document ID to find related documents for"`
	K  int    `json:"k,omitempty" jsonschema:"max related documents (default 5)"`
}

func relatedHandler(d daemon) mcp.ToolHandlerFor[relatedInput, searchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in relatedInput) (*mcp.CallToolResult, searchOutput, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, searchOutput{}, errors.New("id is required")
		}
		k := in.K
		if k <= 0 {
			k = 5
		}
		res, err := call(ctx, d, func(ctx context.Context) (*client.RelatedResponse, error) {
			return d.client.RelatedDocuments(ctx, in.ID, k)
		})
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

// --- list_interests ---

type listInterestsInput struct {
	Limit   int `json:"limit,omitempty" jsonschema:"max interests to return (default 20)"`
	Members int `json:"members,omitempty" jsonschema:"documents to include per interest (default 5)"`
}

type interestMemberOut struct {
	DocID string `json:"doc_id"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

type interestOut struct {
	ID       string              `json:"id"`
	Label    string              `json:"label,omitempty"`
	Summary  string              `json:"summary,omitempty"`
	Size     int                 `json:"size"`
	Cohesion float64             `json:"cohesion"`
	Members  []interestMemberOut `json:"members,omitempty"`
}

type listInterestsOutput struct {
	Interests    []interestOut `json:"interests"`
	NumDocuments int           `json:"num_documents"`
	NumClusters  int           `json:"num_clusters"`
	NumNoise     int           `json:"num_noise"`
}

func listInterestsHandler(d daemon) mcp.ToolHandlerFor[listInterestsInput, listInterestsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInterestsInput) (*mcp.CallToolResult, listInterestsOutput, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		members := in.Members
		if members <= 0 {
			members = 5
		}
		res, err := call(ctx, d, func(ctx context.Context) (*client.InterestList, error) {
			return d.client.ListInterests(ctx, client.ListInterestsOpts{Limit: limit, Members: members})
		})
		if err != nil {
			return nil, listInterestsOutput{}, fmt.Errorf("list interests: %w", err)
		}
		out := listInterestsOutput{
			NumDocuments: res.NumDocuments,
			NumClusters:  res.NumClusters,
			NumNoise:     res.NumNoise,
			Interests:    make([]interestOut, 0, len(res.Items)),
		}
		for _, it := range res.Items {
			item := interestOut{
				ID: it.ID, Label: it.Label, Summary: it.Summary,
				Size: it.Size, Cohesion: it.Cohesion,
			}
			for _, m := range it.Members {
				item.Members = append(item.Members, interestMemberOut{DocID: m.DocID, Title: m.Title, URL: m.URL})
			}
			out.Interests = append(out.Interests, item)
		}
		return textResult(formatInterests(out)), out, nil
	}
}

func formatInterests(out listInterestsOutput) string {
	if len(out.Interests) == 0 {
		return "No interests computed yet. Ask the user to run `curio interests rebuild`."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d interests across %d documents", len(out.Interests), out.NumDocuments)
	if out.NumNoise > 0 {
		fmt.Fprintf(&b, " (%d unclustered)", out.NumNoise)
	}
	b.WriteString(":\n\n")
	for i, it := range out.Interests {
		label := it.Label
		if label == "" {
			label = "(unlabeled)"
		}
		fmt.Fprintf(&b, "%d. %s — %d docs\n", i+1, label, it.Size)
		if it.Summary != "" {
			fmt.Fprintf(&b, "   %s\n", it.Summary)
		}
		for _, m := range it.Members {
			t := m.Title
			if t == "" {
				t = m.URL
			}
			fmt.Fprintf(&b, "   - %s (doc_id: %s)\n", t, m.DocID)
		}
		b.WriteString("\n")
	}
	return b.String()
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
