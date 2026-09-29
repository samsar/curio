package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// dashboardRoutes lists router's routes outside /v1, as "METHOD pattern".
func dashboardRoutes(t *testing.T, router chi.Routes) []string {
	t.Helper()
	var routes []string
	require.NoError(t, chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/v1/") {
			routes = append(routes, method+" "+route)
		}
		return nil
	}))
	slices.Sort(routes)
	return routes
}

// TestDashboard_Routes: the pages add exactly these routes, all GETs, and
// the router without them has none outside /v1.
func TestDashboard_Routes(t *testing.T) {
	deps := Deps{Log: slog.New(slog.DiscardHandler), TenantID: "local"}
	router, err := newRouter(deps, testOrigin(t), testDashboard(t, pagesOn))
	require.NoError(t, err)
	assert.Equal(t, []string{
		"GET /",
		"GET /ui/",
		"GET /ui/documents/{id}",
		"GET /ui/interests",
		"GET /ui/interests/{id}",
		"GET /ui/library",
		"GET /ui/search",
		"GET /ui/static/{file}",
		"GET /ui/status",
	}, dashboardRoutes(t, router))

	router, err = newRouter(deps, testOrigin(t), testDashboard(t, UIOptions{}))
	require.NoError(t, err)
	assert.Empty(t, dashboardRoutes(t, router))
}

// withSearch gives a test server's Deps a search engine, whose queries all
// embed to the same vector.
func withSearch(d *Deps) {
	emb := countingEmbedder{new(atomic.Int32)}
	d.Search = search.New(d.Chunks, d.Documents, emb, search.Config{Log: slog.New(slog.DiscardHandler)})
}

// uiSamples are a request to each of the dashboard's routes for s, by
// route pattern: every route needs one, so TestDashboard_SecurityHeaders
// covers each.
func uiSamples(t *testing.T, s *testServer) map[string]string {
	t.Helper()
	doc := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	s.seedContent(t, doc, "# A\n\ntext")
	interest := s.seedInterest(t, "local", "Kafka", doc)
	return map[string]string{
		"/":                    "/",
		"/ui/":                 "/ui/",
		"/ui/search":           "/ui/search?q=text",
		"/ui/status":           "/ui/status",
		"/ui/library":          "/ui/library?state=fetched",
		"/ui/documents/{id}":   "/ui/documents/" + doc.ID,
		"/ui/interests":        "/ui/interests",
		"/ui/interests/{id}":   "/ui/interests/" + interest.ID,
		"/ui/static/{file}":    stylesheetURL(t, s),
		"an unknown /ui/ page": "/ui/nope",
	}
}

// stylesheetRE finds a page's stylesheet.
var stylesheetRE = regexp.MustCompile(`<link rel="stylesheet" href="(/ui/static/[^"]+)">`)

// stylesheetURL is the stylesheet the search home loads.
func stylesheetURL(t *testing.T, s *testServer) string {
	t.Helper()
	resp := s.do(t, request{method: http.MethodGet, path: "/ui/"})
	m := stylesheetRE.FindStringSubmatch(resp.body)
	require.NotNil(t, m, resp.body)
	return m[1]
}

// assertSecurityHeaders checks that resp carries the dashboard's headers,
// exactly, with csp as its CSP.
func assertSecurityHeaders(t *testing.T, resp response, csp string) {
	t.Helper()
	assert.Equal(t, []string{csp}, resp.header.Values("Content-Security-Policy"), resp.path)
	assert.Equal(t, []string{"nosniff"}, resp.header.Values("X-Content-Type-Options"), resp.path)
	assert.Equal(t, []string{"no-referrer"}, resp.header.Values("Referrer-Policy"), resp.path)
}

// TestDashboard_SecurityHeaders: every dashboard response carries the CSP,
// nosniff and no Referer: each route's, taken from the router so a new
// route needs a sample, and the 404, 405 and 403 answers.
func TestDashboard_SecurityHeaders(t *testing.T) {
	s := newTestServer(t, withSearch)
	samples := uiSamples(t, s)
	router, err := newRouter(s.deps, testOrigin(t), testDashboard(t, pagesOn))
	require.NoError(t, err)
	for _, route := range dashboardRoutes(t, router) {
		pattern := strings.TrimPrefix(route, "GET ")
		path, ok := samples[pattern]
		require.True(t, ok, "no sample request for %s", route)
		t.Run(pattern, func(t *testing.T) {
			resp := s.do(t, request{method: http.MethodGet, path: path})
			assert.Less(t, resp.status, http.StatusBadRequest, resp.body)
			assertSecurityHeaders(t, resp, ui.CSP)
		})
	}

	notFound := s.do(t, request{method: http.MethodGet, path: samples["an unknown /ui/ page"]})
	assert.Equal(t, http.StatusNotFound, notFound.status)
	assert.Equal(t, "text/html; charset=utf-8", notFound.contentType)
	assert.Contains(t, notFound.body, `<nav aria-label="Main">`)
	assertSecurityHeaders(t, notFound, ui.CSP)
	assertSecurityHeaders(t, s.do(t, request{method: http.MethodPost, path: "/ui/"}), ui.CSP)
	for _, refused := range []request{
		{method: http.MethodGet, path: "/ui/", origin: "https://attacker.example"},
		{method: http.MethodGet, path: "/ui/", host: "attacker.example:" + s.port},
	} {
		resp := s.do(t, refused)
		assert.Equal(t, http.StatusForbidden, resp.status)
		assertSecurityHeaders(t, resp, ui.CSP)
	}

	starting := newStartingTestServer(t)
	resp := starting.do(t, request{method: http.MethodGet, path: "/ui/"})
	assert.Equal(t, http.StatusServiceUnavailable, resp.status)
	assertSecurityHeaders(t, resp, ui.CSP)
}

// TestDashboard_GETOnly: the pages never take a method that could change
// something, since another site can send a browser to them.
func TestDashboard_GETOnly(t *testing.T) {
	s := newTestServer(t, withSearch)
	router, err := newRouter(s.deps, testOrigin(t), testDashboard(t, pagesOn))
	require.NoError(t, err)
	samples := uiSamples(t, s)
	for _, route := range dashboardRoutes(t, router) {
		method, pattern, _ := strings.Cut(route, " ")
		assert.Equal(t, http.MethodGet, method, route)
		for _, change := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			resp := s.do(t, request{method: change, path: samples[pattern]})
			assert.Equal(t, http.StatusMethodNotAllowed, resp.status, "%s %s", change, pattern)
			assert.Equal(t, "GET", resp.header.Get("Allow"), "%s %s", change, pattern)
		}
	}
}

// TestDashboard_Off: without the pages, / and /ui/ are the API's 404
// problem, or its starting problem while starting.
func TestDashboard_Off(t *testing.T) {
	s := newStartingTestServerUI(t, UIOptions{})
	for _, path := range []string{"/", "/ui/", "/ui/?q=x"} {
		getStarting(t, s, request{method: http.MethodGet, path: path})
	}
	s.ready(t)
	for _, path := range []string{"/", "/ui/", "/ui/status", "/ui/static/app.css"} {
		resp := s.do(t, request{method: http.MethodGet, path: path})
		assertProblem(t, resp, http.StatusNotFound)
		assert.Empty(t, resp.header.Get("Content-Security-Policy"))
	}
}

// TestDashboard_AccessPolicy: the pages answer under the API's access
// rules: another site's Origin, an opaque one, and a rebinding Host are
// refused.
func TestDashboard_AccessPolicy(t *testing.T) {
	s := newTestServer(t)
	for name, req := range map[string]request{
		"foreign origin": {origin: "https://attacker.example"},
		"null origin":    {origin: "null"},
		"rebinding host": {host: "attacker.example:" + s.port},
	} {
		t.Run(name, func(t *testing.T) {
			req.method, req.path = http.MethodGet, "/ui/"
			assertProblem(t, s.do(t, req), http.StatusForbidden)
		})
	}
	resp := s.do(t, request{method: http.MethodGet, path: "/ui/", origin: "http://localhost:" + s.port})
	assert.Equal(t, http.StatusOK, resp.status, "the daemon's own origin")
}

// secFetch is the Sec-Fetch-* headers of a browser's request.
func secFetch(site, mode, dest string) http.Header {
	h := http.Header{"Sec-Fetch-Site": {site}}
	if mode != "" {
		h.Set("Sec-Fetch-Mode", mode)
	}
	if dest != "" {
		h.Set("Sec-Fetch-Dest", dest)
	}
	return h
}

// TestSecFetchSite_Changes: a change a browser says came from anywhere but
// the daemon's own pages is refused; reads, and requests without the
// header (the CLI's and the MCP sidecar's), are not.
func TestSecFetchSite_Changes(t *testing.T) {
	var rec logRecorder
	s := newTestServer(t, func(d *Deps) { d.Log = slog.New(&rec) })
	var added atomic.Int32
	changes := []func() request{
		func() request {
			return request{method: http.MethodPost, path: "/v1/bookmarks", contentType: "application/json",
				body: fmt.Sprintf(`{"url":"https://example.com/%d"}`, added.Add(1))}
		},
		func() request {
			return request{method: http.MethodPut, path: "/v1/queue", contentType: "application/json",
				body: `{"paused":false}`}
		},
		func() request { return request{method: http.MethodDelete, path: "/v1/jobs?status=done"} },
	}
	refused := map[string]http.Header{
		"cross-site":    {"Sec-Fetch-Site": {"cross-site"}},
		"same-site":     {"Sec-Fetch-Site": {"same-site"}},
		"none":          {"Sec-Fetch-Site": {"none"}},
		"unknown value": {"Sec-Fetch-Site": {"same-origin-ish"}},
		"duplicated":    {"Sec-Fetch-Site": {"same-origin", "same-origin"}},
	}
	for name, header := range refused {
		for _, change := range changes {
			req := change()
			t.Run(name+" "+req.method+" "+req.path, func(t *testing.T) {
				req.header = header
				assertProblem(t, s.do(t, req), http.StatusForbidden)
			})
		}
	}
	assert.Zero(t, s.count(t, "bookmarks"), "nothing refused ran")

	for _, change := range changes {
		req := change()
		req.header = secFetch("same-origin", "cors", "empty")
		req.origin = "http://127.0.0.1:" + s.port
		assert.Less(t, s.do(t, req).status, http.StatusBadRequest, "same-origin %s %s", req.method, req.path)
		req = change()
		assert.Less(t, s.do(t, req).status, http.StatusBadRequest, "no header %s %s", req.method, req.path)
	}
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/stats", header: secFetch("cross-site", "cors", "empty")})
	assert.Equal(t, http.StatusOK, resp.status, "reads aren't changes")

	var logged []map[string]any
	for _, r := range rec.at(slog.LevelWarn) {
		if r["msg"] == "request rejected: cross-site change" {
			logged = append(logged, r)
		}
	}
	require.Len(t, logged, len(refused)*len(changes))
	assert.Contains(t, []any{"cross-site", "same-site", "none", "same-origin-ish", "same-origin, same-origin"},
		logged[0]["sec_fetch_site"])

	starting := newStartingTestServer(t)
	resp = starting.do(t, request{method: http.MethodPost, path: "/v1/documents/refetch-all",
		header: secFetch("cross-site", "no-cors", "empty")})
	assertProblem(t, resp, http.StatusForbidden)
}

// countingEmbedder counts the queries it embeds.
type countingEmbedder struct{ calls *atomic.Int32 }

func (e countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls.Add(1)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = unitVec()
	}
	return out, nil
}

// TestDashboard_CrossSiteSubresource: another site's page can't make the
// daemon search by loading /ui/?q= as an image, nor through the old
// search address, which is refused before it redirects; a user following a
// link from there, and the dashboard's own requests, get the page.
func TestDashboard_CrossSiteSubresource(t *testing.T) {
	var embeds atomic.Int32
	s := newTestServer(t, func(d *Deps) {
		d.Search = search.New(d.Chunks, d.Documents, countingEmbedder{&embeds}, search.Config{Log: slog.New(slog.DiscardHandler)})
	})
	path := "/ui/?q=kafka"
	for _, refused := range []string{path, "/ui/", "/ui/search?q=kafka"} {
		for _, site := range []string{"cross-site", "same-site"} {
			for _, h := range []http.Header{secFetch(site, "no-cors", "image"), secFetch(site, "cors", "empty"),
				secFetch(site, "navigate", "iframe")} {
				resp := s.do(t, request{method: http.MethodGet, path: refused, header: h})
				assertProblem(t, resp, http.StatusForbidden)
				assert.Empty(t, resp.header.Get("Location"), refused)
				assertSecurityHeaders(t, resp, ui.CSP)
			}
		}
	}
	assert.Zero(t, embeds.Load(), "no search ran")

	for name, h := range map[string]http.Header{
		"a navigation from another site": secFetch("cross-site", "navigate", "document"),
		"the dashboard's own htmx":       secFetch("same-origin", "cors", "empty"),
		"typed in the address bar":       secFetch("none", "navigate", "document"),
		"no browser":                     nil,
	} {
		resp := s.do(t, request{method: http.MethodGet, path: path, header: h})
		assert.Equal(t, http.StatusOK, resp.status, name)
	}
	assert.Equal(t, int32(4), embeds.Load())

	starting := newStartingTestServer(t)
	assertProblem(t, starting.do(t, request{method: http.MethodGet, path: "/ui/",
		header: secFetch("cross-site", "no-cors", "image")}), http.StatusForbidden)
	assert.Equal(t, http.StatusServiceUnavailable, starting.do(t, request{method: http.MethodGet, path: "/ui/",
		header: secFetch("cross-site", "navigate", "document")}).status)
}

// TestStarting_Page: while starting, the pages answer the starting page:
// 503, HTML, reloading itself, with the migration's progress; their
// assets are served; changes and the API get the starting problem. Ready
// swaps in the pages on the same server.
func TestStarting_Page(t *testing.T) {
	s := newStartingTestServer(t)
	get := func(path string) response {
		t.Helper()
		return s.do(t, request{method: http.MethodGet, path: path})
	}

	resp := get("/ui/")
	assert.Equal(t, http.StatusServiceUnavailable, resp.status)
	assert.Equal(t, "text/html; charset=utf-8", resp.contentType)
	assert.Equal(t, startingRetryAfter, resp.header.Get("Retry-After"))
	assert.Contains(t, resp.body, `<meta http-equiv="refresh" content="2">`)
	assert.Contains(t, resp.body, "Starting up: initializing.")

	s.startup.SetMigrating(6)
	assert.Contains(t, get("/ui/library?state=failed").body, "0 of 6 migrations applied")
	s.startup.MigrationApplied()
	assert.Contains(t, get("/ui/documents/some-id").body, "1 of 6 migrations applied")

	css := stylesheetRE.FindStringSubmatch(resp.body)
	require.NotNil(t, css)
	asset := get(css[1])
	assert.Equal(t, http.StatusOK, asset.status)
	assert.Equal(t, "text/css; charset=utf-8", asset.contentType)

	getStarting(t, s, request{method: http.MethodPost, path: "/ui/"})
	getStarting(t, s, request{method: http.MethodGet, path: "/v1/stats"})

	s.ready(t)
	resp = get("/ui/")
	assert.Equal(t, http.StatusOK, resp.status)
	assert.Contains(t, resp.body, `<h1 class="visually-hidden">Search</h1>`)
}

// TestPage_RenderFailure: a page that fails to render is a 500 error page,
// logged once under its request ID, never a truncated 200.
func TestPage_RenderFailure(t *testing.T) {
	var rec logRecorder
	deps := routerDeps(t, &rec)
	h := pageHandlers{d: deps, pages: testDashboard(t, pagesOn)}
	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	w := httptest.NewRecorder()
	// Status's template reads fields a Search doesn't have.
	h.page(w, req, http.StatusOK, ui.PageStatus, ui.Search{})

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), `<div class="big-code">500</div>`+"\n<h1>internal error</h1>")
	assert.NotContains(t, w.Body.String(), "<h1>Status</h1>")
	errs := rec.at(slog.LevelError)
	require.Len(t, errs, 1)
	assert.Equal(t, "request failed", errs[0]["msg"])
	assert.EqualValues(t, http.StatusInternalServerError, errs[0]["status"])
}

// TestSearchPage_ClientGone: a search that fails because its client has
// gone answers, and logs, the 499 reportError gives it, not the 500 of the
// error alone.
func TestSearchPage_ClientGone(t *testing.T) {
	var rec logRecorder
	deps := routerDeps(t, &rec, withSearch)
	h := pageHandlers{d: deps, pages: testDashboard(t, pagesOn)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	h.search(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/ui/?q=text", nil))

	assert.Equal(t, statusClientClosedRequest, w.Code)
	assert.Empty(t, rec.at(slog.LevelError))
	infos := rec.at(slog.LevelInfo)
	require.Len(t, infos, 1)
	assert.EqualValues(t, statusClientClosedRequest, infos[0]["status"])
}

// TestSearchHome_ClientGone: the home's reads failing because its client
// has gone are logged at info as 499s, never as the daemon's errors.
func TestSearchHome_ClientGone(t *testing.T) {
	var rec logRecorder
	deps := routerDeps(t, &rec, withSearch)
	h := pageHandlers{d: deps, pages: testDashboard(t, pagesOn)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	h.search(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/ui/", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `<div class="landing">`)
	assert.Empty(t, rec.at(slog.LevelError))
	infos := rec.at(slog.LevelInfo)
	require.Len(t, infos, 2, "the counts and the interests")
	for _, info := range infos {
		assert.EqualValues(t, statusClientClosedRequest, info["status"])
	}
}
