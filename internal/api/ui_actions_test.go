package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// TestDashboard_ActionsMatchTheAPI holds every change the pages render to
// the API, so a typo in an Action's method, path, query or body fails here
// rather than when someone clicks: over pages in each state, every control
// with a data-method asks for a route the router serves and the spec
// documents, with the query it takes, and sends a body, built as
// actions.js builds it, that the route's request decodes strictly and
// accepts. Its status is on the page, and announces. Every kind of Action
// appears on some page.
func TestDashboard_ActionsMatchTheAPI(t *testing.T) {
	s := newTestServer(t, withSearch)
	router, err := newRouter(s.deps, testOrigin(t), testDashboard(t, pagesOn))
	require.NoError(t, err)
	index, err := methodIndex(router)
	require.NoError(t, err)
	ops := specOperations(loadSpec(t))

	fetched := s.seedDocument(t, "https://example.com/fetched", store.DocStateFetched)
	s.seedContent(t, fetched, "# Fetched")
	failed := s.seedFailedDocument(t, "https://example.com/blocked", store.FailureCauseAntiBot)
	dead := s.seedDocument(t, "https://example.com/gone", store.DocStateDead)
	pending := s.seedDocument(t, "https://example.com/pending", store.DocStatePending)

	var pages []string
	get := func(path string) {
		t.Helper()
		resp := s.do(t, request{method: http.MethodGet, path: path})
		require.Equal(t, http.StatusOK, resp.status, path)
		pages = append(pages, resp.body)
	}
	get("/ui/status")
	get("/ui/interests")
	decodeQueue(t, putQueue(t, s, `{"paused":true,"throttle":"gentle","keep_awake":true,"schedule":"22:00-07:00"}`))
	get("/ui/status")
	for _, doc := range []*store.Document{fetched, failed, dead, pending} {
		get("/ui/documents/" + doc.ID)
	}
	s.seedInterest(t, "local", "Kafka", fetched)
	get("/ui/interests")

	seen := map[ui.ActionKind]bool{}
	for _, page := range pages {
		doc, err := html.Parse(strings.NewReader(page))
		require.NoError(t, err)
		for n := range doc.Descendants() {
			if n.Type != html.ElementNode || !hasNodeAttr(n, "data-method") {
				continue
			}
			kind := ui.ActionKind(nodeAttr(n, "data-kind"))
			seen[kind] = true
			t.Run(string(kind), func(t *testing.T) {
				checkAction(t, index, ops, doc, n)
			})
		}
	}
	for _, kind := range ui.ActionKinds {
		assert.True(t, seen[kind], "no page renders a %s action", kind)
	}
}

// actionQueries are the query parameters each route's actions may carry,
// and their values.
var actionQueries = map[string]url.Values{
	"POST /v1/documents/{id}/refetch": {"force": {"1"}},
}

// checkAction checks control, an element of page with a data-method,
// against the router's routes (index) and the spec's operations.
func checkAction(t *testing.T, index chi.Routes, ops map[string]*openapi3.Operation, page, control *html.Node) {
	t.Helper()
	method := nodeAttr(control, "data-method")
	require.Contains(t, []string{http.MethodPost, http.MethodPut}, method)
	target, err := url.Parse(nodeAttr(control, "data-path"))
	require.NoError(t, err)
	assert.Empty(t, target.Scheme+target.Host, "a path of the daemon's")
	pattern := index.Find(chi.NewRouteContext(), method, target.Path)
	require.NotEmpty(t, pattern, "%s %s isn't routed", method, target.Path)
	op := method + " " + pattern
	assert.Contains(t, ops, op, "the spec documents it")
	for key, values := range target.Query() {
		assert.Equal(t, actionQueries[op][key], values, "%s takes %s", op, key)
	}

	for _, body := range controlBodies(t, control) {
		if op != "PUT /v1/queue" {
			assert.Empty(t, body, "%s takes no body", op)
			continue
		}
		var req QueueUpdateRequest
		dec := json.NewDecoder(strings.NewReader(body))
		dec.DisallowUnknownFields()
		require.NoError(t, dec.Decode(&req), body)
		_, err := req.update()
		assert.NoError(t, err, body)
	}

	var status *html.Node
	for n := range page.Descendants() {
		if n.Type == html.ElementNode && nodeAttr(n, "id") == nodeAttr(control, "data-status") {
			status = n
		}
	}
	require.NotNil(t, status, "its status is on the page")
	assert.Equal(t, "action-status", nodeAttr(status, "class"))
	assert.Equal(t, "polite", nodeAttr(status, "aria-live"))
}

// sampleTimes are the values the walk types into a form's time fields.
var sampleTimes = []string{"22:00", "07:00"}

// controlBodies are the bodies control sends, built as actions.js builds
// them: a button's data-body, or none; a checkbox's checked state under
// its data-field, checked and not; a form's fields, the values of those
// sharing a name joined by its data-join, a time field's value a sample
// time.
func controlBodies(t *testing.T, control *html.Node) []string {
	t.Helper()
	encode := func(v any) string {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return string(b)
	}
	switch {
	case control.Data == "input" && nodeAttr(control, "type") == "checkbox":
		field := nodeAttr(control, "data-field")
		return []string{encode(map[string]bool{field: true}), encode(map[string]bool{field: false})}
	case control.Data == "form":
		values := map[string][]string{}
		times := slices.Clone(sampleTimes)
		for n := range control.Descendants() {
			if n.Type != html.ElementNode || n.Data != "input" || nodeAttr(n, "name") == "" {
				continue
			}
			value := nodeAttr(n, "value")
			if nodeAttr(n, "type") == "time" {
				require.NotEmpty(t, times, "more time fields than sample times")
				value, times = times[0], times[1:]
			}
			values[nodeAttr(n, "name")] = append(values[nodeAttr(n, "name")], value)
		}
		fields := map[string]string{}
		for name, vs := range values {
			fields[name] = strings.Join(vs, nodeAttr(control, "data-join"))
		}
		return []string{encode(fields)}
	}
	return []string{nodeAttr(control, "data-body")}
}

func nodeAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasNodeAttr(n *html.Node, key string) bool {
	return slices.ContainsFunc(n.Attr, func(a html.Attribute) bool { return a.Key == key })
}
