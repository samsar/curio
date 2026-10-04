package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
	"github.com/samsar/curio/internal/ui/uitest"
)

// mapPage gets the interest map's page, which must answer status.
func mapPage(t *testing.T, s *testServer, query string, status int) response {
	t.Helper()
	resp := s.do(t, request{method: http.MethodGet, path: "/ui/interests/map" + query})
	require.Equal(t, status, resp.status, resp.body)
	assertSecurityHeaders(t, resp, ui.CSP)
	uitest.AssertInert(t, resp.body)
	return resp
}

// mapRoot is the map page's root, nil without one.
func mapRoot(t *testing.T, page string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && nodeAttr(n, "id") == "interest-map" {
			return n
		}
	}
	return nil
}

// TestInterestMapPage_AgreesWithTheEndpoint: the map's page and GET
// /v1/interests/map tell one story, from one read of the latest run: no
// rebuild yet, a rebuild that drew no map, a map that failed, the map off,
// and a map to draw. Every reason the endpoint gives is a state of the
// page's.
func TestInterestMapPage_AgreesWithTheEndpoint(t *testing.T) {
	for reason, state := range map[string]ui.MapState{mapNoRun: ui.MapNoRun, mapNoMap: ui.MapNoMap,
		mapFailed: ui.MapFailed, mapOff: ui.MapOff} {
		assert.Equal(t, state, ui.MapState(reason), "the page's state is the endpoint's reason")
	}

	s := newTestServer(t)
	assert.Equal(t, mapNoRun, mapProblem(t, s).Reason)
	assert.Contains(t, mapPage(t, s, "", http.StatusOK).body, "<h2>No interests yet</h2>")

	d := s.docs(t, "agree", 3)
	f := s.newRun(t, store.InterestShapeAreas)
	f.interest("Kafka", f.area("Engineering"), 0, d[:2], nil)
	f.unsorted("", d[2])
	f.commit(t)
	assert.Equal(t, mapNoMap, mapProblem(t, s).Reason)
	page := mapPage(t, s, "", http.StatusOK).body
	assert.Contains(t, page, "<h2>No map yet</h2>")
	assert.Contains(t, page, "3 documents in 1 area holding 1 interest", "the run's counts, though it drew no map")
	assert.Nil(t, mapRoot(t, page))

	failure := strings.Repeat("the layout gave up ", 30) + `<img src=x onerror="alert(1)">`
	f = s.newRun(t, store.InterestShapeAreas)
	f.interest("Kafka", f.area("Engineering"), 0, d[:2], nil)
	f.c.Outcome.Map = &store.RunMap{Status: store.MapFailed, Error: failure, Params: []byte(`{}`)}
	f.commit(t)
	p := mapProblem(t, s)
	assert.Equal(t, mapFailed, p.Reason)
	assert.Equal(t, failure, p.MapError)
	page = mapPage(t, s, "", http.StatusOK).body
	assert.Contains(t, page, "<h2>The map failed</h2>")
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	var shown *html.Node
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && nodeAttr(n, "class") == "state-error" {
			shown = n
		}
	}
	require.NotNil(t, shown)
	assert.Equal(t, failure, nodeAttr(shown, "title"), "whole on hover")
	assert.Equal(t, html.ElementNode, shown.Type)
	assert.Less(t, len(shown.FirstChild.Data), len(failure), "cut")
	assert.NotContains(t, page, "<img", "escaped")

	f = s.newRun(t, store.InterestShapeAreas)
	f.interest("Kafka", f.area("Engineering"), 0, d[:2], nil)
	f.unsorted("", d[2])
	f.mapped()
	run := f.commit(t)
	assert.Equal(t, run, getAs[InterestMapResponse](t, s, "/v1/interests/map").RunID)
	root := mapRoot(t, mapPage(t, s, "", http.StatusOK).body)
	require.NotNil(t, root, "a map to draw")
	assert.Equal(t, "/v1/interests/map", nodeAttr(root, "data-src"))

	off := newTestServer(t, func(d *Deps) { d.MapOff = true })
	assert.Equal(t, mapOff, mapProblem(t, off).Reason)
	assert.Contains(t, mapPage(t, off, "", http.StatusOK).body, "<h2>The map is off</h2>")
}

// TestInterestMapPage_InsightOff: with finding interests turned off, a run
// that drew no map, or whose map failed, sends the reader to the setting:
// a rebuild would be refused (409).
func TestInterestMapPage_InsightOff(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.InsightEnabled = false })
	d := s.docs(t, "off", 2)
	f := s.newRun(t, store.InterestShapeAreas)
	f.interest("Kafka", f.area("Engineering"), 0, d, nil)
	f.commit(t)
	page := mapPage(t, s, "", http.StatusOK).body
	assert.Contains(t, page, "<h2>No map yet</h2>")
	assert.Contains(t, page, "set <code>insight.enabled: true</code> in config.yaml")
	assert.NotContains(t, page, "curio interests rebuild", "a rebuild would be refused")

	f = s.newRun(t, store.InterestShapeAreas)
	f.interest("Kafka", f.area("Engineering"), 0, d, nil)
	f.c.Outcome.Map = &store.RunMap{Status: store.MapFailed, Error: "no room", Params: []byte(`{}`)}
	f.commit(t)
	page = mapPage(t, s, "", http.StatusOK).body
	assert.Contains(t, page, "<h2>The map failed</h2>")
	assert.Contains(t, page, "set <code>insight.enabled: true</code> in config.yaml")
	assert.NotContains(t, page, "curio interests rebuild", "a rebuild would be refused")
}

// TestInterestMapPage_ReadsOneRun: with a map to draw, the page reads the
// latest run once and nothing more: map.js reads the map itself. A query
// the map can't show is a 400 before anything is read.
func TestInterestMapPage_ReadsOneRun(t *testing.T) {
	var reads insightReads
	s := newTestServer(t, func(d *Deps) { d.Insights = countingInsights{InsightStore: d.Insights, n: &reads} })
	d := s.docs(t, "reads", 3)
	f := s.newRun(t, store.InterestShapeAreas)
	f.interest("Kafka", f.area("Engineering"), 0, d, nil)
	f.mapped()
	f.commit(t)

	for _, query := range []string{"?view=map", "?select=nope", "?select=area:", "?select=planet:x"} {
		resp := mapPage(t, s, query, http.StatusBadRequest)
		assert.Contains(t, resp.body, "want", query)
	}
	assert.Zero(t, reads.total(), "a refused query reads nothing")

	mapPage(t, s, "?view=groups&select=document:"+url.QueryEscape(d[0].ID), http.StatusOK)
	assert.EqualValues(t, 1, reads.latest.Load())
	assert.EqualValues(t, 1, reads.total(), "the latest run, and nothing else")
}

// TestInterestMapPage_HostileSelection: a selection's ID is the page's
// data, however odd: an attribute that holds it exactly, and nothing it
// could make run.
func TestInterestMapPage_HostileSelection(t *testing.T) {
	s := newTestServer(t)
	d := s.docs(t, "hostile", 2)
	f := s.newRun(t, store.InterestShapeFlat)
	f.interest("Kafka", "", 0, d, nil)
	f.mapped()
	f.commit(t)
	const id = `"><script>alert(1)</script>`
	resp := mapPage(t, s, "?select="+url.QueryEscape("interest:"+id), http.StatusOK)
	root := mapRoot(t, resp.body)
	require.NotNil(t, root)
	assert.Equal(t, "interest:"+id, nodeAttr(root, "data-select"))
	assert.Equal(t, "similarity", nodeAttr(root, "data-view"))
	assert.NotContains(t, resp.body, "<script>alert")
}

// TestShowOnMap_Links: an area's, an interest's, Unsorted's and a placed
// document's pages lead to them on the map; with the map off none does,
// and neither does a document the latest rebuild has nowhere.
func TestShowOnMap_Links(t *testing.T) {
	for _, mapOff := range []bool{false, true} {
		s := newTestServer(t, withSearch, func(d *Deps) { d.MapOff = mapOff })
		docs := s.docs(t, "links", 3)
		f := s.newRun(t, store.InterestShapeAreas)
		area := f.area("Engineering")
		interest := f.interest("Kafka", area, 0, docs[:2], nil)
		f.unsorted(interest, docs[2])
		f.mapped()
		f.commit(t)
		nowhere := s.seedDocument(t, "https://example.com/nowhere", store.DocStateFetched)
		for path, want := range map[string]string{
			"/ui/interests/" + area:       "/ui/interests/map?select=area%3A" + area,
			"/ui/interests/" + interest:   "/ui/interests/map?select=interest%3A" + interest,
			"/ui/interests/unsorted":      "/ui/interests/map?select=unsorted",
			"/ui/documents/" + docs[0].ID: "/ui/interests/map?select=document%3A" + docs[0].ID,
			"/ui/documents/" + docs[2].ID: "/ui/interests/map?select=document%3A" + docs[2].ID,
			"/ui/documents/" + nowhere.ID: "",
		} {
			resp := s.do(t, request{method: http.MethodGet, path: path})
			require.Equal(t, http.StatusOK, resp.status, path)
			got := mapLinks(t, resp.body)
			if want == "" || mapOff {
				assert.Empty(t, got, "%s, map off %v", path, mapOff)
			} else {
				assert.Equal(t, []string{want}, got, path)
			}
		}
		// The subnav's Map leads to the map's page whatever its state.
		page := s.do(t, request{method: http.MethodGet, path: "/ui/interests"}).body
		assert.Contains(t, page, `<a href="/ui/interests/map">Map</a></nav>`)
	}
}

// mapLinks are the Show on map links of page.
func mapLinks(t *testing.T, page string) []string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	var out []string
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && nodeAttr(n, "class") == "map-link" {
			out = append(out, nodeAttr(n, "href"))
		}
	}
	return out
}

// TestInterestMapPage_Flat: a flat run's map is drawn as an areas run's
// is: the page is its shell, its lede counting interests alone.
func TestInterestMapPage_Flat(t *testing.T) {
	s := newTestServer(t)
	d := s.docs(t, "flat", 2)
	f := s.newRun(t, store.InterestShapeFlat)
	f.interest("Kafka", "", 0, d, nil)
	f.mapped()
	f.commit(t)
	page := mapPage(t, s, "", http.StatusOK).body
	require.NotNil(t, mapRoot(t, page))
	assert.Contains(t, page, `<p class="lede">2 documents in 1 interest, as the rebuild of `)
	var body InterestMapResponse
	require.NoError(t, json.Unmarshal([]byte(s.do(t, request{method: http.MethodGet, path: "/v1/interests/map"}).body),
		&body))
	assert.Equal(t, "flat", body.Shape)
}
