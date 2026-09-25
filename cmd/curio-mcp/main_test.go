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

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
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
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

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
	addr := freeAddr(t)
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
		client: client.New("http://" + freeAddr(t)),
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

// startingDaemon is the real API as a daemon that is still starting, with
// the test process holding its home's lock the way the daemon would, so a
// controller for that home trusts it. ensure makes it ready, once however
// often it is called, and counts the calls.
func startingDaemon(t *testing.T, ensures *atomic.Int32) (*apitest.Server, daemon) {
	t.Helper()
	srv := apitest.StartNotReady(t)
	srv.Startup.SetMigrating(6)
	doc := srv.AddDocument(t, "https://example.com/alpha", store.DocStateFetched)
	srv.AddContent(t, doc, "alpha body")
	var once sync.Once
	return srv, daemon{
		client: client.New(srv.URL),
		ensure: func(context.Context) error {
			ensures.Add(1)
			once.Do(func() { srv.Ready(t) })
			return nil
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
			home, err := curiohome.Init(t.TempDir(), "nomic-embed-text", store.EmbeddingDim)
			require.NoError(t, err)
			url := "http://" + freeAddr(t)
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
