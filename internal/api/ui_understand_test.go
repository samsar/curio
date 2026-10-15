package api_test

import (
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// plannedSummer is the planner's plan for the tests' question: videos saved
// in summer 2018, a rewrite and a name.
func plannedSummer() understand.Outcome {
	return understand.Outcome{Status: understand.StatusPlanned, Model: "gemma4:26b", Took: 1300 * time.Millisecond,
		Plan: &understand.Plan{Kind: understand.KindList, Filters: search.FilterRequest{
			ContentType: []string{"video"},
			SavedAfter:  "2018-06-01T00:00:00-04:00", SavedBefore: "2018-09-23T00:00:00-04:00"},
			Rewrites: []string{"kafka streams talk"}, Names: []string{"Kafka Summit"}}}
}

// understandServer serves the dashboard with planner, over a kafka video
// and an undated kafka article.
func understandServer(t *testing.T, planner *apitest.Planner) *apitest.Server {
	t.Helper()
	srv := apitest.Start(t, apitest.WithPlanner(planner))
	for _, d := range []struct{ url, ct string }{{"https://example.com/talk", "video"},
		{"https://example.com/notes", "article"}} {
		doc := titled(t, srv, d.url, "Kafka "+d.ct, store.DocStateFetched)
		srv.AddContent(t, doc, "kafka "+d.ct+" about partitions")
		_, err := srv.DB.Exec(`UPDATE documents SET content_type = ? WHERE id = ?`, d.ct, doc.ID)
		require.NoError(t, err)
	}
	return srv
}

// tokenOf is the plan token a page's link carries.
func tokenOf(t *testing.T, href string) string {
	t.Helper()
	u, err := url.Parse(html.UnescapeString(href))
	require.NoError(t, err)
	return u.Query().Get("plan")
}

// planOf decodes a plan token.
func planOf(t *testing.T, token string) api.PlanJSON {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	var p api.PlanJSON
	require.NoError(t, json.Unmarshal(raw, &p))
	return p
}

// TestUI_UnderstandsOnlyItsOwnRequests: ?understand=1 asks the planner
// when the dashboard sends it, the user navigates to it, or a client that
// isn't a browser does; another site's link, a top-level navigation the
// dashboard answers, searches the words and asks no model. A plan token
// is searched by wherever it comes from, since it asks none.
func TestUI_UnderstandsOnlyItsOwnRequests(t *testing.T) {
	navigate := func(site string) http.Header {
		return http.Header{"Sec-Fetch-Site": {site}, "Sec-Fetch-Mode": {"navigate"}, "Sec-Fetch-Dest": {"document"}}
	}
	for _, tc := range []struct {
		name   string
		header http.Header
		asks   bool
	}{
		{"the dashboard's own request", http.Header{"Sec-Fetch-Site": {"same-origin"}, "Hx-Request": {"true"}}, true},
		{"the user's address bar or bookmark", navigate("none"), true},
		{"no browser", nil, true},
		{"another site's link", navigate("cross-site"), false},
		{"a link from another port on this host", navigate("same-site"), false},
		{"conflicting headers", http.Header{"Sec-Fetch-Site": {"same-origin", "cross-site"},
			"Sec-Fetch-Mode": {"navigate"}, "Sec-Fetch-Dest": {"document"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner := apitest.NewPlanner(plannedSummer())
			srv := understandServer(t, planner)
			p := getWith(t, srv, "/ui/?q=kafka&understand=1", tc.header)
			require.Equal(t, http.StatusOK, p.status, p.body)
			assert.Contains(t, p.body, "Kafka", "the words are searched either way")
			if tc.asks {
				assert.Equal(t, []string{"kafka"}, planner.Questions())
				return
			}
			assert.Empty(t, planner.Questions(), "another site made the planner load its model")
		})
	}

	t.Run("a plan token from another site", func(t *testing.T) {
		planner := apitest.NewPlanner(plannedSummer())
		srv := understandServer(t, planner)
		token := encodedPlan(t, api.PlanJSON{Kind: "list", Filters: api.Filters{ContentType: []string{"video"}}})
		p := getWith(t, srv, "/ui/?q=kafka&plan="+token, navigate("cross-site"))
		require.Equal(t, http.StatusOK, p.status, p.body)
		assert.Contains(t, p.body, "Kafka video")
		assert.NotContains(t, p.body, "Kafka article", "searched by the plan's filter")
		assert.Empty(t, planner.Questions())
	})
}

// encodedPlan is p as a ?plan token, base64url JSON.
func encodedPlan(t *testing.T, p api.PlanJSON) string {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// TestUI_KeystrokesNeverUnderstand: search as you type, with or without
// htmx, and limited to a type, never asks the planner; the home doesn't
// either.
func TestUI_KeystrokesNeverUnderstand(t *testing.T) {
	planner := apitest.NewPlanner(plannedSummer())
	srv := understandServer(t, planner)
	for _, tc := range []struct {
		path   string
		header http.Header
	}{
		{"/ui/?q=kafka", nil},
		{"/ui/?q=kafka", http.Header{"Hx-Request": {"true"}}},
		{"/ui/?q=kafka&content_type=video", nil},
		{"/ui/", nil},
		{"/ui/?understand=1", nil},
	} {
		p := getWith(t, srv, tc.path, tc.header)
		require.Equal(t, http.StatusOK, p.status, tc.path)
		assert.NotContains(t, p.body, `class="understood`, tc.path)
		assert.Empty(t, p.header.Get("HX-Push-Url"), tc.path)
	}
	assert.Empty(t, planner.Questions())
}

// TestUI_Understand: ?understand=1, which Enter sends, asks the planner
// once and shows what it understood: a chip per filter, each searching
// without it, the undated documents its dates left out, what else it
// searched and looked for, and the search of the words; the page's other
// pages and types keep its plan.
func TestUI_Understand(t *testing.T) {
	planner := apitest.NewPlanner(plannedSummer())
	srv := understandServer(t, planner)
	body := getPage(t, srv, "/ui/?q=kafka&understand=1", http.StatusOK)
	assert.Equal(t, []string{"kafka"}, planner.Questions())

	chips := regexp.MustCompile(`<a class="chip" href="([^"]+)" aria-label="Remove: ([^"]+)" title="Remove: [^"]+">`+
		`<span class="chip-label">([^<]+)</span><svg`).FindAllStringSubmatch(body, -1)
	require.Len(t, chips, 2, body)
	assert.Equal(t, "Videos", chips[0][2])
	assert.Equal(t, "Videos", chips[0][3])
	assert.Equal(t, "Saved Jun 1 – Sep 22, 2018", html.UnescapeString(chips[1][3]))
	withoutType := planOf(t, tokenOf(t, chips[0][1]))
	assert.Empty(t, withoutType.Filters.ContentType, "the chip searches without its filter")
	assert.Equal(t, "2018-06-01T00:00:00-04:00", withoutType.Filters.SavedAfter, "and keeps the rest")
	assert.Equal(t, []string{"kafka streams talk"}, withoutType.Rewrites)
	withoutSaved := planOf(t, tokenOf(t, chips[1][1]))
	assert.Empty(t, withoutSaved.Filters.SavedAfter)
	assert.Equal(t, []string{"video"}, withoutSaved.Filters.ContentType)

	// The video has no bookmark, so the saved window can't judge it.
	assert.Contains(t, body, `<span class="understood-undated">1 undated left out · <a href="/ui/?plan=`)
	assert.Contains(t, body, `<p class="understood-also" title="Also searched: kafka streams talk">`)
	assert.Contains(t, body, `<p class="understood-also" title="Looked for: Kafka Summit">`)
	assert.Contains(t, body, `<a class="understood-words" href="/ui/?q=kafka">Search your words instead</a>`)
	assert.Contains(t, body, `<a href="/ui/?content_type=video&amp;plan=`, "the type tabs keep the plan")
	assert.NotContains(t, body, "role=\"alert\"")
	undated := regexp.MustCompile(`1 undated left out · <a href="([^"]+)">include them</a>`).FindStringSubmatch(body)
	require.NotNil(t, undated)
	assert.True(t, planOf(t, tokenOf(t, undated[1])).Filters.IncludeUndated)

	// A plan whose filters the guards dropped, all of them, has no chip to
	// be understood as; what else it searched is still said.
	rewrites := plannedSummer()
	rewrites.Plan.Filters = search.FilterRequest{}
	planner.Set(rewrites, nil)
	body = getPage(t, srv, "/ui/?q=kafka&understand=1", http.StatusOK)
	assert.Contains(t, body, `<div class="understood">`)
	assert.NotContains(t, body, "Understood as")
	assert.NotContains(t, body, `class="chip"`)
	assert.Contains(t, body, `<p class="understood-also" title="Also searched: kafka streams talk">`)
}

// TestUI_PlanToken: ?plan= searches by the plan it carries and asks no
// model; a token that isn't one, or a plan with understand=1, is a 400
// page before anything is read.
func TestUI_PlanToken(t *testing.T) {
	planner := apitest.NewPlanner(plannedSummer())
	srv := understandServer(t, planner)
	plan := base64.RawURLEncoding.EncodeToString([]byte(`{"kind":"list","filters":{"content_type":["article"]}}`))
	body := getPage(t, srv, "/ui/?q=kafka&plan="+plan, http.StatusOK)
	assert.Equal(t, 1, strings.Count(body, `<li class="result">`), "the plan's articles alone")
	assert.Contains(t, body, `<span class="chip-label">Articles</span>`)

	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, path := range map[string]string{
		"not base64":         "/ui/?q=kafka&plan=not*base64",
		"padded base64":      "/ui/?q=kafka&plan=" + base64.URLEncoding.EncodeToString([]byte(`{"kind":"one"}`)),
		"over 4 KiB":         "/ui/?q=kafka&plan=" + strings.Repeat("A", 4097),
		"not JSON":           "/ui/?q=kafka&plan=" + enc("kind=one"),
		"an unknown key":     "/ui/?q=kafka&plan=" + enc(`{"kind":"one","site":"example.com"}`),
		"two values":         "/ui/?q=kafka&plan=" + enc(`{"kind":"one"}{"kind":"one"}`),
		"a plan refused":     "/ui/?q=kafka&plan=" + enc(`{"kind":"summary"}`),
		"a filter refused":   "/ui/?q=kafka&plan=" + enc(`{"kind":"one","filters":{"saved_after":"soon"}}`),
		"understand and one": "/ui/?q=kafka&understand=1&plan=" + plan,
		"understand not 1":   "/ui/?q=kafka&understand=yes",
	} {
		getPage(t, srv, path, http.StatusBadRequest)
		assert.Empty(t, planner.Questions(), name)
	}
}

// TestUI_UnderstandPushesThePlan: htmx's understood request is answered
// with the address to push: the plan's when there is one, its words'
// otherwise, so a reload never plans again.
func TestUI_UnderstandPushesThePlan(t *testing.T) {
	planner := apitest.NewPlanner(plannedSummer())
	srv := understandServer(t, planner)
	htmx := http.Header{"Hx-Request": {"true"}}
	p := getWith(t, srv, "/ui/?q=kafka&understand=1", htmx)
	require.Equal(t, http.StatusOK, p.status)
	pushed := p.header.Get("HX-Push-Url")
	require.True(t, strings.HasPrefix(pushed, "/ui/?plan="), pushed)
	assert.Equal(t, "video", planOf(t, tokenOf(t, pushed)).Filters.ContentType[0])
	assert.Contains(t, pushed, "&q=kafka")

	again := getWith(t, srv, pushed, htmx)
	require.Equal(t, http.StatusOK, again.status)
	assert.Equal(t, pushed, again.header.Get("HX-Push-Url"), "a page of the plan pushes itself")
	assert.Len(t, planner.Questions(), 1, "following the pushed address asks no model")

	planner.Set(understand.Outcome{Status: understand.StatusCold, Model: "gemma4:26b"}, nil)
	cold := getWith(t, srv, "/ui/?q=kafka&content_type=video&understand=1", htmx)
	assert.Equal(t, "/ui/?content_type=video&q=kafka", cold.header.Get("HX-Push-Url"), "the words' address")

	plain := getWith(t, srv, "/ui/?q=kafka&understand=1", nil)
	assert.Empty(t, plain.header.Get("HX-Push-Url"), "without htmx the address is the request's")
}

// TestUI_LongPlanStaysAddressable: a planner's plan at its caps, of the
// characters HTML's or JSON's escaping writes longest, every filter the
// planner sets among its filters, is a token the page takes: the address
// pushed and every link carrying the plan (chips, the undated line, type
// tabs) answer 200, the token under 3 KiB.
func TestUI_LongPlanStaysAddressable(t *testing.T) {
	texts := func(fill string, n, size int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strings.Repeat(fill, size/len(fill))[:size-1] + strconv.Itoa(i)
		}
		return out
	}
	for name, fill := range map[string]string{"HTML's escapes": "&<>", "JSON's escapes": `"\`} {
		t.Run(name, func(t *testing.T) {
			out := plannedSummer()
			out.Plan.Names, out.Plan.Rewrites = texts(fill, search.MaxNames, 100), texts(fill, search.MaxRewrites, 160)
			out.Plan.Filters.ContentType = []string{"video", "repo", "pdf"}
			out.Plan.Filters.Source = []string{"chrome", "safari", "firefox"}
			out.Plan.Filters.PublishedAfter, out.Plan.Filters.PublishedBefore = "2016-01-01", "2017-01-01"
			srv := understandServer(t, apitest.NewPlanner(out))
			p := getWith(t, srv, "/ui/?q=kafka&understand=1", http.Header{"Hx-Request": {"true"}})
			require.Equal(t, http.StatusOK, p.status)
			pushed := p.header.Get("HX-Push-Url")
			assert.Less(t, len(tokenOf(t, pushed)), 3<<10)

			body := getPage(t, srv, pushed, http.StatusOK)
			links := regexp.MustCompile(`href="(/ui/\?[^"]*plan=[^"]+)"`).FindAllStringSubmatch(body, -1)
			require.NotEmpty(t, links)
			for _, l := range links {
				getPage(t, srv, html.UnescapeString(l[1]), http.StatusOK)
			}
		})
	}
}

// TestUI_UnderstandFallbacks: a search of the words says why in a quiet
// line, worded from the status alone, never the daemon's reason; a
// timeout names the planner's limit, not what it took (asking whether the
// model is loaded adds to that); off says nothing.
func TestUI_UnderstandFallbacks(t *testing.T) {
	planner := apitest.NewPlanner(understand.Outcome{})
	srv := understandServer(t, planner)
	for status, want := range map[understand.Status]string{
		understand.StatusCold:     "is loading; press Enter again in a few seconds.",
		understand.StatusChecking: "curio is checking gemma4:26b; press Enter again in a few seconds.",
		understand.StatusUnfit: "gemma4:26b didn&#39;t pass curio&#39;s check for understanding questions; set " +
			"generation.roles.planner.model to a model that passes, or search.understand: false.",
		understand.StatusTimeout:     "understanding took longer than 5 s; curio doctor says what helps",
		understand.StatusUnavailable: "gemma4:26b isn&#39;t available; curio doctor says why.",
		understand.StatusInvalid:     "the model&#39;s answer couldn&#39;t be used.",
		understand.StatusTooLong:     "the question is too long to understand; a shorter one can be.",
	} {
		planner.Set(understand.Outcome{Status: status, Reason: "SECRET-REASON", Model: "gemma4:26b",
			Took: 6020 * time.Millisecond}, nil)
		body := getPage(t, srv, "/ui/?q=kafka&understand=1", http.StatusOK)
		assert.Contains(t, body, `<p class="understood-note">Searched your words: `, status)
		assert.Contains(t, body, want, status)
		assert.NotContains(t, body, "SECRET-REASON", status)
		assert.Equal(t, 2, strings.Count(body, `<li class="result">`), "%s: the literal results", status)
	}
	planner.Set(understand.Outcome{Status: understand.StatusOff, Reason: "SECRET-REASON"}, nil)
	body := getPage(t, srv, "/ui/?q=kafka&understand=1", http.StatusOK)
	assert.NotContains(t, body, "understood-note")
	assert.NotContains(t, body, "SECRET-REASON")
}
