package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// testServer runs the full router from NewServer on a real loopback listener
// backed by real SQLite stores.
type testServer struct {
	base string // http://127.0.0.1:<port>
	port string
	db   *sqlite.DB
	deps Deps
}

// newTestServer starts the server; each option adjusts its Deps first.
func newTestServer(t *testing.T, options ...func(*Deps)) *testServer {
	t.Helper()
	db := sqlitetest.NewDB(t)
	home, err := curiohome.Init(t.TempDir(), "nomic-embed-text", store.EmbeddingDim)
	require.NoError(t, err)

	deps := Deps{
		Home:           home,
		Documents:      sqlite.NewDocuments(db),
		Extractions:    sqlite.NewExtractions(db),
		Bookmarks:      sqlite.NewBookmarks(db),
		Chunks:         sqlite.NewChunks(db, store.EmbeddingDim),
		Queue:          sqlite.NewJobs(db),
		Insights:       sqlite.NewInsights(db),
		InsightEnabled: true,
		Log:            slog.New(slog.DiscardHandler),
	}
	for _, opt := range options {
		opt(&deps)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv, err := NewServer(ln, deps)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-served)
	})

	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	return &testServer{base: "http://" + ln.Addr().String(), port: port, db: db, deps: deps}
}

// request describes one call. Zero fields mean "what a normal client sends":
// the listener's own Host, no Origin, no body.
type request struct {
	method      string
	path        string
	host        string
	origin      string
	contentType string
	body        string
	chunked     bool // send body with no Content-Length
}

type response struct {
	status      int
	contentType string
	body        string
}

func (s *testServer) do(t *testing.T, req request) response {
	t.Helper()
	var body io.Reader
	if req.body != "" {
		body = strings.NewReader(req.body)
	}
	if req.chunked {
		// A reader NewRequest can't measure goes out chunked.
		body = io.MultiReader(body)
	}
	r, err := http.NewRequest(req.method, s.base+req.path, body)
	require.NoError(t, err)
	if req.host != "" {
		r.Host = req.host
	}
	if req.origin != "" {
		r.Header.Set("Origin", req.origin)
	}
	if req.contentType != "" {
		r.Header.Set("Content-Type", req.contentType)
	}
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return response{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), body: string(b)}
}

func (s *testServer) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func (s *testServer) seedDocument(t *testing.T, url string, state store.DocState) *store.Document {
	t.Helper()
	doc := &store.Document{TenantID: "local", URL: url, State: state}
	require.NoError(t, s.deps.Documents.Create(context.Background(), doc))
	return doc
}

// seedContent gives doc a current extraction whose markdown is on disk.
func (s *testServer) seedContent(t *testing.T, doc *store.Document, markdown string) *store.DocumentExtraction {
	t.Helper()
	ctx := context.Background()
	rel := filepath.Join(doc.ID, "content.md")
	full := filepath.Join(s.deps.Home.ContentDir(), rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
	require.NoError(t, os.WriteFile(full, []byte(markdown), 0o600))
	ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
		MarkdownPath: &rel, ExtractionMeta: []byte(`{"via":"test"}`)}
	require.NoError(t, s.deps.Extractions.Create(ctx, ext))
	require.NoError(t, s.deps.Documents.SetCurrentExtraction(ctx, doc.ID, ext.ID))
	doc.CurrentExtractionID = &ext.ID
	return ext
}

func assertProblem(t *testing.T, resp response, status int) {
	t.Helper()
	assert.Equal(t, status, resp.status, resp.body)
	assert.Equal(t, "application/problem+json", resp.contentType)
	var p Problem
	require.NoError(t, json.Unmarshal([]byte(resp.body), &p))
	assert.Equal(t, status, p.Status)
}

func TestServer_HostAllowlist(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		name  string
		host  string
		path  string
		allow bool
	}{
		{"ipv4 loopback", "127.0.0.1:" + s.port, "/v1/healthz", true},
		{"localhost", "localhost:" + s.port, "/v1/healthz", true},
		{"localhost in caps", "LOCALHOST:" + s.port, "/v1/healthz", true},
		{"ipv6 loopback", "[::1]:" + s.port, "/v1/healthz", true},
		{"ipv4-mapped loopback", "[::ffff:127.0.0.1]:" + s.port, "/v1/healthz", true},
		{"ipv6 loopback spelled out", "[0:0:0:0:0:0:0:1]:" + s.port, "/v1/healthz", true},
		{"another loopback address", "127.0.0.2:" + s.port, "/v1/healthz", false},
		{"rebinding name", "attacker.example:" + s.port, "/v1/healthz", false},
		{"rebinding name on an unknown route", "attacker.example:" + s.port, "/nope", false},
		{"loopback on another port", "127.0.0.1:1", "/v1/healthz", false},
		{"loopback without port", "localhost", "/v1/healthz", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.do(t, request{method: http.MethodGet, path: tc.path, host: tc.host})
			if tc.allow {
				assert.Equal(t, http.StatusOK, resp.status, resp.body)
				return
			}
			assertProblem(t, resp, http.StatusForbidden)
		})
	}
}

// An HTTP/1.0 request may omit Host entirely; the http.Client can't send
// that, so write it by hand.
func TestServer_EmptyHostRejected(t *testing.T) {
	s := newTestServer(t)
	conn, err := net.Dial("tcp", strings.TrimPrefix(s.base, "http://"))
	require.NoError(t, err)
	defer conn.Close()

	_, err = fmt.Fprint(conn, "GET /v1/healthz HTTP/1.0\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestServer_OriginCheck(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		name   string
		origin string
		allow  bool
	}{
		{"no origin (CLI, MCP, curl)", "", true},
		{"ipv4 loopback on our port", "http://127.0.0.1:" + s.port, true},
		{"localhost on our port", "http://localhost:" + s.port, true},
		{"ipv6 loopback on our port", "http://[::1]:" + s.port, true},
		{"foreign site", "https://attacker.example", false},
		{"opaque origin", "null", false},
		{"localhost on another port", "http://localhost:3000", false},
		{"https scheme", "https://127.0.0.1:" + s.port, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.do(t, request{method: http.MethodGet, path: "/v1/healthz", origin: tc.origin})
			if tc.allow {
				assert.Equal(t, http.StatusOK, resp.status, resp.body)
				return
			}
			assertProblem(t, resp, http.StatusForbidden)
		})
	}
}

func TestServer_NoCORSHeaders(t *testing.T) {
	s := newTestServer(t)
	r, err := http.NewRequest(http.MethodOptions, s.base+"/v1/bookmarks", nil)
	require.NoError(t, err)
	r.Header.Set("Origin", "http://localhost:"+s.port)
	r.Header.Set("Access-Control-Request-Method", http.MethodDelete)
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	for name := range resp.Header {
		assert.NotContains(t, strings.ToLower(name), "access-control", "the daemon never approves a preflight")
	}
}

// TestServer_CSRFBookmarkExploit replays a cross-site "simple" POST: a page
// submits a text/plain body, which browsers send without a CORS preflight.
// Accepted, it would create a bookmark and make the daemon fetch a LAN URL of
// the attacker's choosing.
func TestServer_CSRFBookmarkExploit(t *testing.T) {
	s := newTestServer(t)
	body := `{"url":"http://192.168.1.1/admin"}`
	cases := []struct {
		name   string
		req    request
		status int
	}{
		{
			name: "rebinding host and foreign origin",
			req: request{host: "attacker.example:" + s.port, origin: "https://attacker.example",
				contentType: "text/plain"},
			status: http.StatusForbidden,
		},
		{
			name:   "foreign origin",
			req:    request{origin: "https://attacker.example", contentType: "text/plain"},
			status: http.StatusForbidden,
		},
		{
			name:   "no origin but a non-JSON body",
			req:    request{contentType: "text/plain"},
			status: http.StatusUnsupportedMediaType,
		},
		{
			name:   "no content type",
			req:    request{},
			status: http.StatusUnsupportedMediaType,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.method, tc.req.path, tc.req.body = http.MethodPost, "/v1/bookmarks", body
			assertProblem(t, s.do(t, tc.req), tc.status)
		})
	}
	assert.Zero(t, s.count(t, "bookmarks"))
	assert.Zero(t, s.count(t, "documents"))
	assert.Zero(t, s.count(t, "jobs"))
}

// TestServer_CSRFRefetchAllExploit replays a body-less cross-site POST, which
// needs no Content-Type at all and so is stopped by the Origin check alone.
func TestServer_CSRFRefetchAllExploit(t *testing.T) {
	s := newTestServer(t)
	s.seedDocument(t, "https://example.com/gone", store.DocStateDead)

	for _, origin := range []string{"https://attacker.example", "null"} {
		resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all?state=dead", origin: origin})
		assertProblem(t, resp, http.StatusForbidden)
	}
	assert.Zero(t, s.count(t, "jobs"))
}

func TestServer_JSONBodies(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		name        string
		path        string
		contentType string
		body        string
		chunked     bool
		status      int
	}{
		{
			name: "json with a charset", path: "/v1/bookmarks", contentType: "application/json; charset=utf-8",
			body: `{"url":"https://example.com/a"}`, status: http.StatusCreated,
		},
		{
			name: "value past the limit", path: "/v1/bookmarks", contentType: "application/json",
			body:   `{"url":"https://example.com/` + strings.Repeat("a", maxJSONBody) + `"}`,
			status: http.StatusRequestEntityTooLarge,
		},
		{
			name: "small value padded past the limit", path: "/v1/bookmarks", contentType: "application/json",
			body:   `{"url":"https://example.com/b"}` + strings.Repeat(" ", maxJSONBody),
			status: http.StatusRequestEntityTooLarge,
		},
		{
			name: "truncated value", path: "/v1/bookmarks", contentType: "application/json",
			body: `{"url":`, status: http.StatusBadRequest,
		},
		{
			name: "second value after the first", path: "/v1/bookmarks", contentType: "application/json",
			body: `{"url":"https://example.com/c"} {"url":"https://example.com/d"}`, status: http.StatusBadRequest,
		},
		{
			name: "chunked json", path: "/v1/bookmarks", contentType: "application/json",
			body: `{"url":"https://example.com/f"}`, chunked: true, status: http.StatusCreated,
		},
		{
			name: "chunked text/plain", path: "/v1/bookmarks", contentType: "text/plain",
			body: `{"url":"https://example.com/g"}`, chunked: true, status: http.StatusUnsupportedMediaType,
		},
		{
			name: "import past 1 MiB gets the larger limit", path: "/v1/bookmarks/import", contentType: "application/json",
			body: `{"source":"manual","bookmarks":[{"url":"https://example.com/e","title":"` +
				strings.Repeat("t", 2*maxJSONBody) + `"}]}`,
			status: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.do(t, request{method: http.MethodPost, path: tc.path, contentType: tc.contentType,
				body: tc.body, chunked: tc.chunked})
			if tc.status >= http.StatusBadRequest {
				assertProblem(t, resp, tc.status)
				return
			}
			assert.Equal(t, tc.status, resp.status, resp.body)
		})
	}
	assert.Equal(t, 3, s.count(t, "bookmarks"), "only the accepted bodies created bookmarks")
}

// Body-less POSTs carry no Content-Type; they must keep working.
func TestServer_BodylessPostsNeedNoContentType(t *testing.T) {
	s := newTestServer(t)
	doc := s.seedDocument(t, "https://example.com/doc", store.DocStateFetched)

	cases := []struct {
		path   string
		status int
	}{
		{"/v1/documents/" + doc.ID + "/refetch", http.StatusAccepted},
		{"/v1/documents/refetch-all", http.StatusAccepted},
		{"/v1/documents/" + doc.ID + "/reindex", http.StatusConflict}, // no extraction yet: the handler's own answer
		{"/v1/documents/reindex-all", http.StatusAccepted},
		{"/v1/interests/rebuild", http.StatusAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp := s.do(t, request{method: http.MethodPost, path: tc.path})
			assert.Equal(t, tc.status, resp.status, resp.body)
		})
	}
}

// TestServer_ServeReturnsListenerFailure: a listener that fails ends Serve
// with its error; nothing waits for a cancellation that may never come.
func TestServer_ServeReturnsListenerFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv, err := NewServer(ln, Deps{Log: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	require.NoError(t, ln.Close())

	served := make(chan error, 1)
	go func() { served <- srv.Serve(context.Background()) }()
	select {
	case err := <-served:
		require.ErrorIs(t, err, net.ErrClosed)
		assert.Contains(t, err.Error(), "serve api")
	case <-time.After(5 * time.Second):
		t.Fatal("Serve kept running on a closed listener")
	}
}

// TestServer_HealthzWithStalledOllama: healthz waits on Ollama for at most
// ollamaPingTimeout, well inside the client's healthz probe timeout. A
// daemon whose Ollama accepts connections but never answers must still be
// found by the probe clients use to decide whether it is running.
func TestServer_HealthzWithStalledOllama(t *testing.T) {
	// Listening but never accepting: connections queue and nothing answers.
	stalled, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, stalled.Close()) })
	emb, err := embedder.NewOllama(embedder.OllamaOptions{
		BaseURL: "http://" + stalled.Addr().String(),
		Model:   "nomic-embed-text",
		Dim:     store.EmbeddingDim,
	})
	require.NoError(t, err)
	s := newTestServer(t, func(d *Deps) { d.Embedder = emb })

	h, err := client.New(s.base).Healthz(context.Background())
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), h.PID)
	assert.False(t, h.OllamaReachable)
	assert.NotEmpty(t, h.OllamaDetail)
}

func TestServer_HealthIdentity(t *testing.T) {
	s := newTestServer(t)
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/healthz"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)

	var h Health
	require.NoError(t, json.Unmarshal([]byte(resp.body), &h))
	assert.Equal(t, os.Getpid(), h.PID)
	assert.Equal(t, s.deps.Home.Path, h.Home)
}

// TestServer_RouterErrorsAreProblems: an unknown route and an unsupported
// method answer problem+json like every other error, and a 405 says which
// methods the route takes.
func TestServer_RouterErrorsAreProblems(t *testing.T) {
	s := newTestServer(t)
	for _, path := range []string{"/nope", "/v1/nope", "/v1/documents/x/nope"} {
		assertProblem(t, s.do(t, request{method: http.MethodGet, path: path}), http.StatusNotFound)
	}

	cases := []struct {
		method, path, allow string
	}{
		{http.MethodPut, "/v1/bookmarks", "GET, POST"},
		{http.MethodPost, "/v1/bookmarks/some-id", "GET, DELETE"},
		{http.MethodDelete, "/v1/documents/some-id", "GET"},
		{http.MethodPatch, "/v1/jobs", "GET, DELETE"},
		{http.MethodGet, "/v1/search", "POST"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r, err := http.NewRequest(tc.method, s.base+tc.path, nil)
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(r)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assertProblem(t, response{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"),
				body: string(body)}, http.StatusMethodNotAllowed)
			assert.Equal(t, tc.allow, resp.Header.Get("Allow"))
		})
	}
}
