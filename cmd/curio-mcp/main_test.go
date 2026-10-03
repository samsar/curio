package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/porttest"
	"github.com/samsar/curio/internal/store"
)

// fakeDaemon serves the subset of the curio HTTP API the MCP tools call and
// stores each /v1/search request body in lastSearch. The query "offline" gets
// a degraded (keyword-only) search response.
func fakeDaemon(t *testing.T, lastSearch *atomic.Value) http.Handler {
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
	// doc-unfetched has no content yet; doc-broken's content fails to load;
	// doc-missing doesn't exist.
	for _, id := range []string{"doc-unfetched", "doc-broken"} {
		mux.HandleFunc("/v1/documents/"+id, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id, "url": "https://example.com/" + id, "content_type": "unknown", "state": "pending",
			})
		})
	}
	mux.HandleFunc("/v1/documents/doc-unfetched/content", func(w http.ResponseWriter, _ *http.Request) {
		problem(t, w, http.StatusNotFound, "document has no extraction yet")
	})
	mux.HandleFunc("/v1/documents/doc-broken/content", func(w http.ResponseWriter, _ *http.Request) {
		problem(t, w, http.StatusInternalServerError, "database is locked")
	})
	mux.HandleFunc("/v1/documents/doc-missing", func(w http.ResponseWriter, _ *http.Request) {
		problem(t, w, http.StatusNotFound, `document "doc-missing" not found`)
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
	return mux
}

// serve runs handler on a loopback port until the test ends, returning the
// client for it.
func serve(t *testing.T, handler http.Handler) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return client.New(srv.URL)
}

// serveAt runs handler on addr until the test ends: a daemon started at the
// address a client already points at.
func serveAt(t *testing.T, addr string, handler http.Handler) error {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: handler}}
	srv.Start()
	t.Cleanup(srv.Close)
	return nil
}

// freeAddr is a loopback address nothing listens on.

// running is a daemon that is up: needing to start it is a test failure.
func running(t *testing.T, c *client.Client) daemon {
	return daemon{client: c, ensure: func(context.Context) error {
		t.Error("ensure called for a daemon that was running")
		return nil
	}}
}

// problem answers status with a problem+json body, as the daemon does.
func problem(t *testing.T, w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	assert.NoError(t, json.NewEncoder(w).Encode(client.Problem{Title: http.StatusText(status), Status: status, Detail: detail}))
}

// session is a connected MCP client plus what the fake daemon behind it saw.
type session struct {
	*mcp.ClientSession
	lastSearch atomic.Value // the most recent /v1/search request body
}

// connectMCP connects to the MCP server with our tools, over a fake daemon
// that is running.
func connectMCP(t *testing.T) *session {
	t.Helper()
	sess := &session{}
	sess.ClientSession = connect(t, running(t, serve(t, fakeDaemon(t, &sess.lastSearch))))
	return sess
}

// connect builds the MCP server with our tools over d and returns a
// connected in-memory client session.
func connect(t *testing.T, d daemon) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "curio-test", Version: "test"}, nil)
	registerTools(srv, d)

	clientT, serverT := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverT, nil)
	require.NoError(t, err)
	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := cli.Connect(ctx, clientT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// search calls search_bookmarks for "alpha".
func search(t *testing.T, cs *mcp.ClientSession) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search_bookmarks", Arguments: map[string]any{"query": "alpha"},
	})
	require.NoError(t, err)
	return res
}

// stoppedDaemon is a daemon that isn't running at a fresh address, and
// whose ensure starts it there once, however often it is called. starts
// counts the calls.
func stoppedDaemon(t *testing.T, starts *atomic.Int32) daemon {
	t.Helper()
	addr := porttest.FreeAddr(t)
	var (
		once     sync.Once
		startErr error
		searched atomic.Value
	)
	return daemon{
		client: client.New("http://" + addr),
		ensure: func(context.Context) error {
			starts.Add(1)
			once.Do(func() { startErr = serveAt(t, addr, fakeDaemon(t, &searched)) })
			return startErr
		},
	}
}

// TestMCP_RestartsAStoppedDaemon: a daemon that stopped during the session
// is started again by the next tool call, which then succeeds.
func TestMCP_RestartsAStoppedDaemon(t *testing.T) {
	var starts atomic.Int32
	res := search(t, connect(t, stoppedDaemon(t, &starts)))
	assert.False(t, res.IsError, textOf(res))
	assert.Contains(t, textOf(res), "doc_id: doc-1")
	assert.EqualValues(t, 1, starts.Load())
}

// TestMCP_RestartFailureIsReported: when the daemon can't be started, the
// tool error says both what failed and why the restart did.
func TestMCP_RestartFailureIsReported(t *testing.T) {
	d := daemon{
		client: client.New("http://" + porttest.FreeAddr(t)),
		ensure: func(context.Context) error { return errors.New("curio-daemon failed to start: boom") },
	}
	res := search(t, connect(t, d))
	assert.True(t, res.IsError)
	assert.Contains(t, textOf(res), "daemon unreachable")
	assert.Contains(t, textOf(res), "curio-daemon failed to start: boom")
}

// TestMCP_ServerErrorsAreNotRetried: a daemon that answered, even with an
// error, is running; it is neither restarted nor asked again.
func TestMCP_ServerErrorsAreNotRetried(t *testing.T) {
	var requests atomic.Int32
	failing := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		problem(t, w, http.StatusInternalServerError, "database is locked")
	}))
	res := search(t, connect(t, running(t, failing)))
	assert.True(t, res.IsError)
	assert.Contains(t, textOf(res), "database is locked")
	assert.EqualValues(t, 1, requests.Load())
}

// TestMCP_ConcurrentCallsToAStoppedDaemon: calls that find the daemon gone
// at the same time each ensure it (the real EnsureRunning serializes on
// daemon.start.lock, so one daemon starts) and each succeed.
func TestMCP_ConcurrentCallsToAStoppedDaemon(t *testing.T) {
	var starts atomic.Int32
	cs := connect(t, stoppedDaemon(t, &starts))
	var wg sync.WaitGroup
	results := make([]*mcp.CallToolResult, 2)
	errs := make([]error, len(results))
	for i := range results {
		wg.Go(func() {
			results[i], errs[i] = cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "search_bookmarks", Arguments: map[string]any{"query": "alpha"},
			})
		})
	}
	wg.Wait()
	for i, res := range results {
		require.NoError(t, errs[i])
		assert.False(t, res.IsError, textOf(res))
	}
	assert.GreaterOrEqual(t, starts.Load(), int32(1))
}

// startingDaemon is the real API as a daemon that is still migrating.
// ensure stands in for the controller's wait: it makes the daemon ready,
// once however often it is called, and counts the calls.
func startingDaemon(t *testing.T, ensures *atomic.Int32) (*apitest.Server, daemon) {
	t.Helper()
	srv := apitest.StartNotReady(t)
	srv.Startup.SetMigrating(6)
	doc := srv.AddDocument(t, "https://example.com/alpha", store.DocStateFetched)
	srv.AddContent(t, doc, "alpha body")
	ready := sync.OnceValue(srv.Ready)
	return srv, daemon{
		client: client.New(srv.URL),
		ensure: func(context.Context) error {
			ensures.Add(1)
			return ready()
		},
	}
}

// TestMCP_WaitsForAStartingDaemon: a tool call that finds the daemon still
// starting waits for it to be ready and sends the request again: nothing a
// starting daemon refused has run.
func TestMCP_WaitsForAStartingDaemon(t *testing.T) {
	var ensures atomic.Int32
	_, d := startingDaemon(t, &ensures)
	res := search(t, connect(t, d))
	assert.False(t, res.IsError, textOf(res))
	assert.Contains(t, textOf(res), "https://example.com/alpha")
	assert.EqualValues(t, 1, ensures.Load())
}

// TestMCP_ConcurrentCallsToAStartingDaemon: calls that all find the daemon
// starting each wait for it, and each succeed.
func TestMCP_ConcurrentCallsToAStartingDaemon(t *testing.T) {
	var ensures atomic.Int32
	_, d := startingDaemon(t, &ensures)
	cs := connect(t, d)
	var wg sync.WaitGroup
	results := make([]*mcp.CallToolResult, 3)
	errs := make([]error, len(results))
	for i := range results {
		wg.Go(func() {
			results[i], errs[i] = cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "search_bookmarks", Arguments: map[string]any{"query": "alpha"},
			})
		})
	}
	wg.Wait()
	for i, res := range results {
		require.NoError(t, errs[i])
		assert.False(t, res.IsError, textOf(res))
	}
	assert.GreaterOrEqual(t, ensures.Load(), int32(1))
}

// lockedController is a controller for srv's home whose lock the test
// process holds, as the daemon behind srv would.
func lockedController(t *testing.T, srv *apitest.Server) *daemonctl.Controller {
	t.Helper()
	lock, err := daemonctl.AcquireLock(srv.Home)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, lock.Release()) })
	return daemonctl.New(srv.Home, "/nonexistent/curio-daemon", srv.URL)
}

// TestMCP_StillStartingIsNotARestartFailure: when the daemon is still
// migrating after the tool call's wait, the tool error says so and when to
// try again; nothing failed to restart.
func TestMCP_StillStartingIsNotARestartFailure(t *testing.T) {
	srv := apitest.StartNotReady(t)
	srv.Startup.SetMigrating(6)
	srv.Startup.MigrationApplied()
	ctl := lockedController(t, srv)
	ctl.ReadyTimeout = 200 * time.Millisecond

	res := search(t, connect(t, daemon{client: client.New(srv.URL), ensure: ctl.EnsureRunning}))
	assert.True(t, res.IsError)
	assert.Contains(t, textOf(res), "curio-daemon is still starting")
	assert.Contains(t, textOf(res), "migrating the database, 1 of 6 migrations applied")
	assert.Contains(t, textOf(res), "try again in a minute")
	assert.NotContains(t, textOf(res), "restarting the daemon failed")
}

// envFor is what daemonctl.Discover would find for the daemon behind srv,
// without resolving the real home.
func envFor(srv *apitest.Server, ctl *daemonctl.Controller) daemonctl.Env {
	return daemonctl.Env{Home: srv.Home, Client: client.New(srv.URL), Controller: ctl}
}

// TestSetup_DoesNotWaitForAMigration: the sidecar starts serving MCP while
// the daemon migrates, and says so on stderr, rather than holding the
// client's handshake for the whole migration.
func TestSetup_DoesNotWaitForAMigration(t *testing.T) {
	srv := apitest.StartNotReady(t)
	srv.Startup.SetMigrating(6)
	ctl := lockedController(t, srv)
	var logs bytes.Buffer

	start := time.Now()
	d, err := setup(context.Background(), envFor(srv, ctl), slog.New(slog.NewTextHandler(&logs, nil)))
	require.NoError(t, err)
	assert.Less(t, time.Since(start), time.Second)
	require.NotNil(t, d.ensure)
	assert.Equal(t, mcpReadyWait, ctl.ReadyTimeout)
	assert.Equal(t, 1, strings.Count(logs.String(), "migrating its database"), logs.String())
	assert.Equal(t, 1, strings.Count(logs.String(), "tool calls will wait for it"), logs.String())
	assert.Contains(t, logs.String(), "0 of 6 migrations applied")
}

// TestSetup_Failures: a daemon that can't serve this home still fails the
// sidecar at startup.
func TestSetup_Failures(t *testing.T) {
	crashing := filepath.Join(t.TempDir(), "curio-daemon")
	require.NoError(t, os.WriteFile(crashing, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o700))
	cases := []struct {
		name string
		ctl  func(t *testing.T, home *curiohome.Home, url string) *daemonctl.Controller
		want string
	}{
		{"binary missing", func(t *testing.T, home *curiohome.Home, url string) *daemonctl.Controller {
			return daemonctl.New(home, filepath.Join(t.TempDir(), "curio-daemon"), url)
		}, "no such file"},
		{"crash on start", func(_ *testing.T, home *curiohome.Home, url string) *daemonctl.Controller {
			return daemonctl.New(home, crashing, url)
		}, "exit status 3"},
		{"port served for another home", func(t *testing.T, home *curiohome.Home, _ string) *daemonctl.Controller {
			other := apitest.Start(t)
			return daemonctl.New(home, crashing, other.URL)
		}, "daemon.listen"},
		{"silent lock holder", func(t *testing.T, home *curiohome.Home, _ string) *daemonctl.Controller {
			lock, err := daemonctl.AcquireLock(home)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, lock.Release()) })
			silent, err := net.Listen("tcp", "127.0.0.1:0") // bound, never accepting
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, silent.Close()) })
			ctl := daemonctl.New(home, crashing, "http://"+silent.Addr().String())
			ctl.StartTimeout, ctl.StopTimeout = 200*time.Millisecond, 200*time.Millisecond
			return ctl
		}, "starting up or shutting down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defaults := config.Default().Embedding
			home, err := curiohome.Init(t.TempDir(), defaults.Model, defaults.Dim)
			require.NoError(t, err)
			url := "http://" + porttest.FreeAddr(t)
			ctl := tc.ctl(t, home, url)
			e := daemonctl.Env{Home: home, Client: client.New(ctl.BaseURL), Controller: ctl}

			_, err = setup(context.Background(), e, slog.New(slog.DiscardHandler))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
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
	assert.True(t, got["list_interests"], "list_interests registered")
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

func TestMCP_GetDocument_Failures(t *testing.T) {
	cs := connectMCP(t)
	call := func(id string) *mcp.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "get_document", Arguments: map[string]any{"id": id},
		})
		require.NoError(t, err)
		return res
	}

	res := call("doc-unfetched")
	assert.False(t, res.IsError, "no content yet is an answer")
	assert.Contains(t, textOf(res), "no extracted content available; document state: pending")

	res = call("doc-broken")
	assert.True(t, res.IsError, "a content failure is reported")
	assert.Contains(t, textOf(res), "database is locked")
	assert.NotContains(t, textOf(res), "no extracted content")

	res = call("doc-missing")
	assert.True(t, res.IsError)
	assert.Contains(t, textOf(res), `document "doc-missing" not found`)
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

// listInterests calls list_interests with args against srv, decoding its
// structured output.
func listInterests(t *testing.T, srv *apitest.Server, args map[string]any) (*mcp.CallToolResult, listInterestsOutput) {
	t.Helper()
	cs := connect(t, running(t, client.New(srv.URL)))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_interests", Arguments: args})
	require.NoError(t, err)
	var out listInterestsOutput
	if !res.IsError {
		raw, err := json.Marshal(res.StructuredContent)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &out))
	}
	return res, out
}

// TestMCP_ListInterests_Areas: the outline lists each area, its interests
// with their sizes and ids, their documents with doc_ids, then Unsorted's
// size and what the latest rebuild changed.
func TestMCP_ListInterests_Areas(t *testing.T) {
	srv := apitest.Start(t)
	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	b := srv.AddDocument(t, "https://example.com/b", store.DocStateFetched)
	unsorted := srv.AddDocument(t, "https://example.com/u", store.DocStateFetched)
	run := srv.AddRun(t, apitest.RunSpec{Areas: []apitest.Area{{Label: "Programming", Interests: []apitest.Interest{
		{Label: "Go", Members: []*store.Document{a, b}}, {Label: "Rust", Size: 1}, {Label: "Zig", Size: 1}}}},
		Unsorted: []*store.Document{unsorted}})
	srv.Scheduler.Set(insight.Snapshot{State: insight.StateCurrent, RebuildAt: 5})

	res, out := listInterests(t, srv, map[string]any{"interests": 2, "members": 1})
	require.False(t, res.IsError, textOf(res))
	txt := textOf(res)
	assert.Contains(t, txt, "1 area, 3 interests across 5 documents. Rebuilt ")
	assert.Contains(t, txt, "1. Programming — 4 docs, 3 interests (area id: "+run.Areas[0]+")\n")
	assert.Contains(t, txt, "   - Go — 2 docs (interest id: "+run.Interests[0]+")\n     · https://example.com/a (doc_id: "+a.ID+")\n")
	assert.NotContains(t, txt, b.ID, "members=1")
	assert.Contains(t, txt, "   + 1 more interest (list_interests with this area's id)\n")
	assert.Contains(t, txt, "Unsorted: 1 document\n")
	assert.Contains(t, txt, "Latest rebuild: 3 new, 0 split, 0 merged, 0 moved, 0 dissolved\n")

	assert.Equal(t, run.ID, out.RunID)
	assert.Equal(t, "current", out.State)
	assert.Equal(t, "areas", out.Shape)
	assert.Equal(t, 5, out.NumDocuments)
	assert.Equal(t, 1, out.NumAreas)
	assert.Equal(t, 3, out.NumInterests)
	assert.Equal(t, 1, out.NumUnsorted)
	assert.Empty(t, out.Interests)
	require.Len(t, out.Areas, 1)
	assert.Equal(t, 3, out.Areas[0].NumChildren)
	require.Len(t, out.Areas[0].Children, 2)
	assert.Equal(t, []interestMemberOut{{DocID: a.ID, URL: a.URL}}, out.Areas[0].Children[0].Members)
	assert.Equal(t, &changeCounts{New: 3}, out.Changes)
}

// TestMCP_ListInterests_Flat: in a library of one level the outline lists
// the interests with their documents.
func TestMCP_ListInterests_Flat(t *testing.T) {
	srv := apitest.Start(t)
	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	run := srv.AddInterest(t, "Go", a)
	res, out := listInterests(t, srv, nil)
	txt := textOf(res)
	assert.Contains(t, txt, "1 interest across 1 document.")
	assert.NotContains(t, txt, "areas")
	assert.Contains(t, txt, "1. Go — 1 doc (interest id: "+run.Interests[0]+")\n   · https://example.com/a (doc_id: "+a.ID+")\n")
	assert.Equal(t, "flat", out.Shape)
	require.Len(t, out.Interests, 1)
	assert.Empty(t, out.Areas)
}

// TestMCP_ListInterests_ID: an id drills into an area's interests or an
// interest's documents; a retired one is an answer naming its successors,
// and an unknown one a tool error.
func TestMCP_ListInterests_ID(t *testing.T) {
	srv := apitest.Start(t)
	a := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	b := srv.AddDocument(t, "https://example.com/b", store.DocStateFetched)
	loose := srv.AddDocument(t, "https://example.com/loose", store.DocStateFetched)
	run := srv.AddAreas(t, apitest.Area{Label: "Programming", Interests: []apitest.Interest{
		{Label: "Go", Members: []*store.Document{a, b}, Loose: []*store.Document{loose}}, {Label: "Rust", Size: 1}}})

	res, out := listInterests(t, srv, map[string]any{"id": run.Areas[0]})
	txt := textOf(res)
	assert.Contains(t, txt, "Area Programming — 3 docs, 2 interests (area id: "+run.Areas[0]+")\n")
	assert.Contains(t, txt, "- Go — 2 docs (interest id: "+run.Interests[0]+")\n")
	assert.Contains(t, txt, "- Rust — 1 doc (interest id: "+run.Interests[1]+")\n")
	require.Len(t, out.Areas, 1)
	assert.Len(t, out.Areas[0].Children, 2)

	res, out = listInterests(t, srv, map[string]any{"id": run.Interests[0]})
	txt = textOf(res)
	assert.Contains(t, txt, "In area Programming (area id: "+run.Areas[0]+")\n")
	assert.Contains(t, txt, "· https://example.com/loose (doc_id: "+loose.ID+", loose fit)\n")
	require.Len(t, out.Interests, 1)
	assert.Len(t, out.Interests[0].Members, 3)
	assert.True(t, out.Interests[0].Members[2].Loose)

	split := srv.SplitInterest(t, run, run.Interests[0], apitest.Interest{Label: "Go Web", Members: []*store.Document{a}},
		apitest.Interest{Label: "Go Tools", Members: []*store.Document{b}})
	res, out = listInterests(t, srv, map[string]any{"id": run.Interests[0]})
	assert.False(t, res.IsError, "a retired id is information to follow")
	txt = textOf(res)
	assert.Contains(t, txt, "The interest Go was retired by the rebuild of ")
	assert.Contains(t, txt, "- Go Web (split; interest id: "+split.Interests[0]+")\n")
	require.NotNil(t, out.Retired)
	assert.Len(t, out.Retired.Successors, 2)

	res, _ = listInterests(t, srv, map[string]any{"id": "no-such-interest"})
	assert.True(t, res.IsError)
	assert.Contains(t, textOf(res), `interest "no-such-interest" not found`)
}

// TestMCP_ListInterests_Empty: before the first rebuild, the outline says
// why there are no interests.
func TestMCP_ListInterests_Empty(t *testing.T) {
	srv := apitest.Start(t)
	for _, tc := range []struct {
		snap insight.Snapshot
		want string
	}{
		{insight.Snapshot{State: insight.StateNone, Changed: 7, RebuildAt: 20},
			"No interests yet: the library is grouped once 20 documents are indexed (7 so far)."},
		{insight.Snapshot{State: insight.StateDue, Changed: 25, RebuildAt: 20},
			"No interests yet: the library is grouped for the first time once it stops changing for a while."},
		{insight.Snapshot{State: insight.StateQueued},
			"The library is being grouped for the first time; its interests appear when the rebuild finishes, " +
				"in a couple of minutes."},
		{insight.Snapshot{State: insight.StateHeld, HeldReason: "the embeddings drifted"},
			"No interests yet: rebuilds are held: the embeddings drifted. The user can run `curio reindex --all`."},
		{insight.Snapshot{State: insight.StateFailing, LastError: "ollama unreachable"},
			"No interests: the last rebuild failed: ollama unreachable"},
	} {
		srv.Scheduler.Set(tc.snap)
		res, out := listInterests(t, srv, nil)
		assert.Equal(t, tc.want, textOf(res))
		assert.Equal(t, string(tc.snap.State), out.State)
	}

	off := apitest.Start(t, func(d *api.Deps) { d.InsightEnabled = false })
	res, _ := listInterests(t, off, nil)
	assert.Contains(t, textOf(res), "turned off")
}

// TestMCP_ListInterests_Description: the tool tells the model what it
// lists and that IDs last, and nothing about rebuilding or thresholds.
func TestMCP_ListInterests_Description(t *testing.T) {
	cs := connectMCP(t)
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var desc string
	for _, tl := range res.Tools {
		if tl.Name == "list_interests" {
			desc = tl.Description
		}
	}
	assert.Contains(t, desc, "grouped into broad areas")
	assert.Contains(t, desc, "IDs are stable across rebuilds")
	assert.Contains(t, desc, "a retired id names the interests that took its documents")
	for _, gone := range []string{"curio interests rebuild", "min_similarity", "min_cluster_size"} {
		assert.NotContains(t, desc, gone)
	}
}
