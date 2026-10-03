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
	"cmp"
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
		Name:        "list_interests",
		Description: listInterestsDescription,
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

const listInterestsDescription = "List the user's interests: the topics curio found across their saved " +
	"library, each with a label, summary, size, and representative documents (with doc_ids). In a large " +
	"library interests are grouped into broad areas, and the outline lists each area with its largest " +
	"interests; a small library has one level of interests. Pass an area's or interest's id to see all of " +
	"an area's interests, or an interest's documents and loose fits (documents close to it but not " +
	"grouped with it). IDs are stable across rebuilds; a retired id names the interests that took its " +
	"documents. Use this to understand what the user reads " +
	"about at a high level, or to pick a topic to drill into with search_bookmarks / get_document."

// Defaults of list_interests' sizes.
const (
	defaultTopGroups          = 20
	defaultInterestsPerArea   = 8
	defaultMembersPerInterest = 2
)

type listInterestsInput struct {
	Limit     int    `json:"limit,omitempty" jsonschema:"areas to list, or interests in a library of one level (default 20)"`
	Interests int    `json:"interests,omitempty" jsonschema:"interests to list of each area (default 8)"`
	Members   int    `json:"members,omitempty" jsonschema:"documents to list of each interest (default 2)"`
	ID        string `json:"id,omitempty" jsonschema:"an area's or interest's id: lists all of an area's interests, or an interest's documents"`
}

type interestMemberOut struct {
	DocID string `json:"doc_id"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
	Loose bool   `json:"loose,omitempty"`
}

type interestOut struct {
	ID      string              `json:"id"`
	Label   string              `json:"label,omitempty"`
	Summary string              `json:"summary,omitempty"`
	Size    int                 `json:"size"`
	Members []interestMemberOut `json:"members,omitempty"`
}

type areaOut struct {
	ID       string        `json:"id"`
	Label    string        `json:"label,omitempty"`
	Summary  string        `json:"summary,omitempty"`
	Size     int           `json:"size"`
	Children []interestOut `json:"children,omitempty"`
	// NumChildren is the area's interests, listed or not.
	NumChildren int `json:"num_children"`
}

type successorOut struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Event string `json:"event"`
}

// changeCounts are what the latest rebuild did to the interests.
type changeCounts struct {
	New       int `json:"new"`
	Split     int `json:"split"`
	Merged    int `json:"merged"`
	Moved     int `json:"moved"`
	Dissolved int `json:"dissolved"`
}

type retiredOut struct {
	ID         string         `json:"id"`
	Label      string         `json:"label,omitempty"`
	RetiredAt  time.Time      `json:"retired_at"`
	Successors []successorOut `json:"successors"`
}

type listInterestsOutput struct {
	RunID        string        `json:"run_id,omitempty"`
	State        string        `json:"state"`
	Shape        string        `json:"shape,omitempty"`
	NumDocuments int           `json:"num_documents"`
	NumAreas     int           `json:"num_areas"`
	NumInterests int           `json:"num_interests"`
	NumUnsorted  int           `json:"num_unsorted"`
	Areas        []areaOut     `json:"areas,omitempty"`
	Interests    []interestOut `json:"interests,omitempty"`
	Changes      *changeCounts `json:"changes,omitempty"`
	// Retired is what became of a retired id asked for.
	Retired *retiredOut `json:"retired,omitempty"`
}

func listInterestsHandler(d daemon) mcp.ToolHandlerFor[listInterestsInput, listInterestsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listInterestsInput) (*mcp.CallToolResult, listInterestsOutput, error) {
		if id := strings.TrimSpace(in.ID); id != "" {
			return listOneInterest(ctx, d, id, in.Members)
		}
		res, err := call(ctx, d, func(ctx context.Context) (*client.InterestList, error) {
			return d.client.ListInterests(ctx, client.ListInterestsOpts{
				Limit:    cmp.Or(in.Limit, defaultTopGroups),
				Children: cmp.Or(in.Interests, defaultInterestsPerArea),
				Members:  cmp.Or(in.Members, defaultMembersPerInterest),
			})
		})
		if err != nil {
			return nil, listInterestsOutput{}, fmt.Errorf("list interests: %w", err)
		}
		out := outlineOutput(res)
		return textResult(formatOutline(res, out)), out, nil
	}
}

// listOneInterest answers list_interests for one id: an area with all its
// interests, an interest with its documents, or what became of a retired
// one, which is no error: the model can follow its successors.
func listOneInterest(ctx context.Context, d daemon, id string, members int) (*mcp.CallToolResult, listInterestsOutput, error) {
	in, err := call(ctx, d, func(ctx context.Context) (*client.Interest, error) {
		return d.client.GetInterest(ctx, id, client.GetInterestOpts{Members: members})
	})
	if r := client.RetiredOf(err); r != nil {
		out := listInterestsOutput{State: client.StateCurrent, Retired: &retiredOut{ID: r.ID, Label: r.Label,
			RetiredAt: r.RetiredAt, Successors: make([]successorOut, 0, len(r.Successors))}}
		for _, s := range r.Successors {
			out.Retired.Successors = append(out.Retired.Successors, successorOut{ID: s.ID, Label: s.Label, Event: s.Event})
		}
		return textResult(formatRetired(r)), out, nil
	}
	if err != nil {
		return nil, listInterestsOutput{}, fmt.Errorf("get interest: %w", err)
	}
	out := listInterestsOutput{State: client.StateCurrent, RunID: in.RunID}
	if in.Level == client.LevelArea {
		out.Areas = []areaOut{toAreaOut(*in)}
	} else {
		out.Interests = []interestOut{toInterestOut(*in)}
	}
	return textResult(formatOne(*in)), out, nil
}

// outlineOutput is a list's structured output.
func outlineOutput(res *client.InterestList) listInterestsOutput {
	out := listInterestsOutput{RunID: res.RunID, State: res.Next.State, Shape: res.Shape, NumDocuments: res.NumDocuments,
		NumAreas: res.NumAreas, NumInterests: res.NumInterests, NumUnsorted: res.NumUnsorted}
	for _, it := range res.Items {
		if it.Level == client.LevelArea {
			out.Areas = append(out.Areas, toAreaOut(it))
		} else {
			out.Interests = append(out.Interests, toInterestOut(it))
		}
	}
	if r := res.Rebuild; r != nil {
		out.Changes = &changeCounts{New: r.Created, Split: r.Split, Merged: r.Merged, Moved: r.Moved, Dissolved: r.Dissolved}
	}
	return out
}

func toAreaOut(in client.Interest) areaOut {
	out := areaOut{ID: in.ID, Label: in.Label, Summary: in.Summary, Size: in.Size, NumChildren: in.NumChildren}
	for _, c := range in.Children {
		out.Children = append(out.Children, toInterestOut(c))
	}
	return out
}

func toInterestOut(in client.Interest) interestOut {
	out := interestOut{ID: in.ID, Label: in.Label, Summary: in.Summary, Size: in.Size}
	for _, m := range in.Members {
		out.Members = append(out.Members, interestMemberOut{DocID: m.DocID, Title: cmp.Or(m.Title, m.BookmarkTitle),
			URL: m.URL, Loose: m.Fit == "loose"})
	}
	return out
}

// formatOutline is a list as text: a header with the counts and the
// latest rebuild, each area with its interests and their documents (or
// each interest, in a library of one level), then Unsorted's size and what
// the latest rebuild changed. Without groups it says why.
func formatOutline(res *client.InterestList, out listInterestsOutput) string {
	if res.RunID == "" {
		return noInterests(res.Next)
	}
	if res.Total == 0 {
		return fmt.Sprintf("The latest rebuild found no interests among %s.", plural(res.NumDocuments, "document"))
	}
	var b strings.Builder
	if res.Shape == "areas" {
		fmt.Fprintf(&b, "%s, ", plural(res.NumAreas, "area"))
	}
	fmt.Fprintf(&b, "%s across %s", plural(res.NumInterests, "interest"), plural(res.NumDocuments, "document"))
	if r := res.Rebuild; r != nil && res.ComputedAt != nil {
		fmt.Fprintf(&b, ". Rebuilt %s (%s, %s)", res.ComputedAt.UTC().Format("2006-01-02 15:04 UTC"), r.Kind, r.Trigger)
	}
	b.WriteString(":\n\n")
	for i, it := range out.Areas {
		fmt.Fprintf(&b, "%d. %s — %s, %s (area id: %s)\n", i+1, cmp.Or(it.Label, "(unlabeled area)"),
			plural(it.Size, "doc"), plural(it.NumChildren, "interest"), it.ID)
		if it.Summary != "" {
			fmt.Fprintf(&b, "   %s\n", it.Summary)
		}
		for _, c := range it.Children {
			writeInterest(&b, "   - ", "     ", c)
		}
		if more := it.NumChildren - len(it.Children); more > 0 {
			fmt.Fprintf(&b, "   + %s (list_interests with this area's id)\n", plural(more, "more interest"))
		}
		b.WriteString("\n")
	}
	for i, it := range out.Interests {
		writeInterest(&b, fmt.Sprintf("%d. ", i+1), "   ", it)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Unsorted: %s\n", plural(res.NumUnsorted, "document"))
	if c := out.Changes; c != nil {
		fmt.Fprintf(&b, "Latest rebuild: %d new, %d split, %d merged, %d moved, %d dissolved\n",
			c.New, c.Split, c.Merged, c.Moved, c.Dissolved)
	}
	return b.String()
}

// writeInterest writes an interest and its documents: head starts its
// line, indent its documents'.
func writeInterest(b *strings.Builder, head, indent string, it interestOut) {
	fmt.Fprintf(b, "%s%s — %s (interest id: %s)\n", head, cmp.Or(it.Label, "(unlabeled)"), plural(it.Size, "doc"), it.ID)
	for _, m := range it.Members {
		fit := ""
		if m.Loose {
			fit = ", loose fit"
		}
		fmt.Fprintf(b, "%s· %s (doc_id: %s%s)\n", indent, cmp.Or(m.Title, m.URL), m.DocID, fit)
	}
}

// formatOne is one area with all its interests, or one interest with its
// documents, as text.
func formatOne(in client.Interest) string {
	var b strings.Builder
	if in.Level == client.LevelArea {
		fmt.Fprintf(&b, "Area %s — %s, %s (area id: %s)\n", cmp.Or(in.Label, "(unlabeled)"),
			plural(in.Size, "doc"), plural(in.NumChildren, "interest"), in.ID)
		if in.Summary != "" {
			fmt.Fprintf(&b, "%s\n", in.Summary)
		}
		b.WriteString("\n")
		for _, c := range toAreaOut(in).Children {
			writeInterest(&b, "- ", "  ", c)
		}
		return b.String()
	}
	it := toInterestOut(in)
	if in.ParentID != "" {
		fmt.Fprintf(&b, "In area %s (area id: %s)\n", cmp.Or(in.ParentLabel, "(unlabeled)"), in.ParentID)
	}
	writeInterest(&b, "", "", it)
	if in.Summary != "" {
		fmt.Fprintf(&b, "%s\n", in.Summary)
	}
	return b.String()
}

// formatRetired says when a retired id was retired, and which interests
// took its documents.
func formatRetired(r *client.RetiredInterest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The %s %s was retired by the rebuild of %s.", r.Level, cmp.Or(r.Label, r.ID),
		r.RetiredAt.UTC().Format("2006-01-02 15:04 UTC"))
	if len(r.Successors) == 0 {
		b.WriteString(" It dissolved: its documents went to other interests or to Unsorted.")
		return b.String()
	}
	b.WriteString(" Its documents went to:\n")
	for _, s := range r.Successors {
		fmt.Fprintf(&b, "- %s (%s; %s id: %s)\n", cmp.Or(s.Label, "(unlabeled)"), s.Event, s.Level, s.ID)
	}
	return b.String()
}

// plural is n and noun, "s" added unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// noInterests says why there are no interests yet, from where automatic
// rebuilds stand.
func noInterests(next client.InterestsState) string {
	switch next.State {
	case client.StateQueued, client.StateRebuilding:
		return "The library is being grouped for the first time; its interests appear when the rebuild finishes, " +
			"in a couple of minutes."
	case client.StateNone:
		return fmt.Sprintf("No interests yet: the library is grouped once %d documents are indexed (%d so far).",
			next.RebuildAt, next.ChangedDocuments)
	case client.StateDue:
		return "No interests yet: the library is grouped for the first time once it stops changing for a while."
	case client.StateHeld:
		return "No interests yet: rebuilds are held: " + next.HeldReason + ". The user can run `curio reindex --all`."
	case client.StateFailing:
		return "No interests: the last rebuild failed: " + next.LastError
	case client.StateOff:
		return "Interests are turned off in the user's curio config (insight.enabled)."
	}
	return "No interests yet: the library hasn't been grouped."
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
