package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/retrieve"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/understand"
)

// plannedArticles is the planner's plan for the tests' question: articles,
// searched by their words.
func plannedArticles() understand.Outcome {
	return understand.Outcome{Status: understand.StatusPlanned, Model: "gemma4:26b", Took: time.Second,
		Plan: &understand.Plan{Kind: understand.KindList,
			Filters: search.FilterRequest{ContentType: []string{"article"}}}}
}

// rerankServer serves the dashboard over n kafka articles with a planner
// that plans articles and a reranker that reverses the head, logging
// what the daemon logs into rec.
func rerankServer(t *testing.T, n int, rec *logRecorder) (*apitest.Server, *apitest.Planner, *apitest.Reranker) {
	t.Helper()
	planner, reranker := apitest.NewPlanner(plannedArticles()), apitest.NewReranker(rerank.MaxDepth)
	srv := apitest.Start(t, apitest.WithPlanner(planner), apitest.WithReranker(reranker), func(d *api.Deps) {
		if rec != nil {
			d.Log = slog.New(rec)
		}
	})
	addSightings(t, srv, n)
	_, err := srv.DB.Exec(`UPDATE documents SET content_type = 'article'`)
	require.NoError(t, err)
	return srv, planner, reranker
}

// The ways a request reaches the search page: Enter, and the follow-up,
// are the dashboard's own htmx requests; a navigation is the person's or
// another site's.
var (
	ownHTMX  = http.Header{"Hx-Request": {"true"}, "Sec-Fetch-Site": {"same-origin"}}
	crossNav = http.Header{"Sec-Fetch-Site": {"cross-site"}, "Sec-Fetch-Mode": {"navigate"},
		"Sec-Fetch-Dest": {"document"}}
)

// followUpRE finds an understood page's follow-up and its request.
var followUpRE = regexp.MustCompile(`<p class="rerank-status js-only" id="rerank-status" hx-get="([^"]+)" ` +
	`hx-trigger="load"`)

// followUp is the request of p's follow-up, "" without one.
func followUp(t *testing.T, p page) string {
	t.Helper()
	m := followUpRE.FindAllStringSubmatch(p.body, -1)
	require.LessOrEqual(t, len(m), 1, "one follow-up at most")
	if len(m) == 0 {
		return ""
	}
	return html.UnescapeString(m[0][1])
}

// orderOf decodes an order token.
func orderOf(t *testing.T, token string) []string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	var order []string
	require.NoError(t, json.Unmarshal(raw, &order))
	return order
}

// TestUI_EnterSendsTheFollowUp: Enter's page, understood, carries one
// follow-up, its own page reordered by the plan its answer pushed; the
// reranker isn't asked until the follow-up is sent.
func TestUI_EnterSendsTheFollowUp(t *testing.T) {
	srv, planner, reranker := rerankServer(t, 3, nil)
	p := getWith(t, srv, "/ui/?q=kafka&understand=1", ownHTMX)
	require.Equal(t, http.StatusOK, p.status)
	assert.Equal(t, []string{"kafka"}, planner.Questions())
	assert.Empty(t, reranker.Questions(), "the instant results wait on no reranker")
	pushed := p.header.Get("HX-Push-Url")
	token := tokenOf(t, pushed)
	require.NotEmpty(t, token)
	assert.Equal(t, "/ui/?plan="+token+"&q=kafka&rerank=1", followUp(t, p))
	assert.NotContains(t, pushed, "order=")

	kept := getWith(t, srv, "/ui/?q=kafka&content_type=article&page=1&understand=1", ownHTMX)
	assert.Equal(t, "/ui/?content_type=article&plan="+tokenOf(t, kept.header.Get("HX-Push-Url"))+"&q=kafka&rerank=1",
		followUp(t, kept), "its type kept")
}

// TestUI_NoFollowUp: only a page whose own request asked the planner, and
// got a plan of two documents or more the reranker would reorder, sends
// one: not a skip, no reranker, a plan that didn't come, a ranking of one
// or none, a page past the last, a keystroke's, a plan's or an order's
// page, nor another site's link to an understood or a reranked page,
// which asks neither model.
func TestUI_NoFollowUp(t *testing.T) {
	srv, planner, reranker := rerankServer(t, 3, nil)
	token := encodedPlan(t, api.PlanJSON{Kind: "list", Filters: api.Filters{ContentType: []string{"article"}}})
	order := base64.RawURLEncoding.EncodeToString([]byte(`["a","b"]`))
	for _, path := range []string{"/ui/?q=kafka", "/ui/?q=kafka&plan=" + token,
		"/ui/?q=kafka&plan=" + token + "&order=" + order, "/ui/?q=kafka&understand=1&page=9",
		"/ui/?q=zyzzyva+quokka&understand=1&content_type=pdf"} {
		assert.Empty(t, followUp(t, getWith(t, srv, path, ownHTMX)), path)
	}
	for _, path := range []string{"/ui/?q=kafka&understand=1", "/ui/?q=kafka&plan=" + token + "&rerank=1"} {
		p := getWith(t, srv, path, crossNav)
		require.Equal(t, http.StatusOK, p.status, path)
		assert.Empty(t, followUp(t, p), path)
	}
	assert.Empty(t, reranker.Questions())
	assert.Len(t, planner.Questions(), 2, "the page past the last and the empty one; another site's none")

	reranker.SetDepth(0)
	assert.Empty(t, followUp(t, getWith(t, srv, "/ui/?q=kafka&understand=1", ownHTMX)), "a skip")
	reranker.SetDepth(rerank.MaxDepth)
	for _, status := range []understand.Status{understand.StatusCold, understand.StatusTimeout} {
		planner.Set(understand.Outcome{Status: status, Model: "gemma4:26b"}, nil)
		assert.Empty(t, followUp(t, getWith(t, srv, "/ui/?q=kafka&understand=1", ownHTMX)), status)
	}

	one, _, oneReranker := rerankServer(t, 1, nil)
	p := getWith(t, one, "/ui/?q=kafka&understand=1", ownHTMX)
	assert.Equal(t, 1, strings.Count(p.body, `<li class="result">`))
	assert.Empty(t, followUp(t, p), "a ranking of one")
	assert.Empty(t, oneReranker.Questions())

	bare := apitest.Start(t, apitest.WithPlanner(apitest.NewPlanner(plannedArticles())))
	addSightings(t, bare, 3)
	_, err := bare.DB.Exec(`UPDATE documents SET content_type = 'article'`)
	require.NoError(t, err)
	assert.Empty(t, followUp(t, getWith(t, bare, "/ui/?q=kafka&understand=1", ownHTMX)), "no reranker")
	assert.Empty(t, reranker.Questions())
}

// TestUI_FollowUpOnlyInTheHead: a page of an understood search sends its
// follow-up only when it holds some of the head the reranker would
// reorder: a later page past it would ask the reranker for an order
// nothing on it shows, and says nothing of reordering, the note of a
// model that failed its check included.
func TestUI_FollowUpOnlyInTheHead(t *testing.T) {
	srv, _, reranker := rerankServer(t, 25, nil)
	for _, tc := range []struct {
		depth, page int
		sends       bool
	}{
		{10, 1, true}, {10, 2, false}, {11, 2, true}, {5, 2, false}, {5, 3, false}, {rerank.MaxDepth, 3, true},
	} {
		reranker.SetDepth(tc.depth)
		path := fmt.Sprintf("/ui/?q=kafka&understand=1&page=%d", tc.page)
		p := getWith(t, srv, path, ownHTMX)
		require.Equal(t, http.StatusOK, p.status, path)
		require.Contains(t, p.body, `<li class="result">`, "%s: the page has results", path)
		assert.Equal(t, tc.sends, followUp(t, p) != "", "depth %d, %s", tc.depth, path)
	}
	reranker.SetDepth(5)
	reranker.SetState(rerank.State{On: true, Model: "m:1", Check: rerank.CheckUnfit})
	p := getWith(t, srv, "/ui/?q=kafka&understand=1&page=2", ownHTMX)
	assert.False(t, strings.Contains(p.body, `id="rerank-status"`), "nothing of reordering past the head")
	assert.Empty(t, reranker.Questions())
}

// TestUI_NoteInPlaceOfAFollowUp: a reranker whose model failed its check,
// or that rests after timeouts or evictions, would answer a follow-up
// with a note alone, after searching again: Enter's page says it at once,
// in the follow-up's slot and Go's words, from the reranker's memory,
// and sends none, so the reranker is asked nothing. A state that changes
// between Enter and its follow-up is TestUI_FollowUpAnswers'.
func TestUI_NoteInPlaceOfAFollowUp(t *testing.T) {
	srv, _, reranker := rerankServer(t, 3, nil)
	held := time.Now().Add(15 * time.Minute)
	for _, tc := range []struct {
		name  string
		state rerank.State
		words string
	}{
		{"unfit", rerank.State{On: true, Model: "m:1", Check: rerank.CheckUnfit, Unfit: "SECRET-UNFIT"},
			"Kept curio&#39;s order: m:1 didn&#39;t pass curio&#39;s check for reordering results; set " +
				"generation.roles.reranker.model to a model that passes, or search.rerank: false."},
		{"timeouts", rerank.State{On: true, Model: "m:1", Check: rerank.CheckFit, TimeoutHoldUntil: held},
			"Kept curio&#39;s order: reordering resumes at " + held.Local().Format(time.Kitchen) +
				"; curio doctor says why."},
		{"evictions", rerank.State{On: true, Model: "m:1", Check: rerank.CheckFit, EvictionHoldUntil: held},
			"Kept curio&#39;s order: reordering resumes at " + held.Local().Format(time.Kitchen)},
	} {
		reranker.SetState(tc.state)
		p := getWith(t, srv, "/ui/?q=kafka&understand=1", ownHTMX)
		require.Equal(t, http.StatusOK, p.status, tc.name)
		assert.Empty(t, followUp(t, p), tc.name)
		assert.Contains(t, p.body, `<p class="rerank-status" id="rerank-status">`+tc.words, tc.name)
		assert.NotContains(t, p.body, "SECRET-UNFIT", tc.name)
	}
	reranker.SetState(rerank.State{On: true, Model: "m:1", Check: rerank.CheckFit,
		TimeoutHoldUntil: time.Now().Add(-time.Minute)})
	assert.NotEmpty(t, followUp(t, getWith(t, srv, "/ui/?q=kafka&understand=1", ownHTMX)), "a hold that has ended")
	assert.Empty(t, reranker.Questions())
}

// TestUI_FollowUpReorders: the follow-up asks the reranker once and
// answers the page reordered, its address the order's, replacing the
// page's, never pushed; its pages keep the plan and the order, and what
// searches another ranking keeps neither; its head keeps Enter's time.
// That address, its second page and another site's link to it ask no
// model, and lay the same order over the same ranking: page 2 is the
// reordered ranking's next window.
func TestUI_FollowUpReorders(t *testing.T) {
	srv, planner, reranker := rerankServer(t, 12, nil)
	enter := getWith(t, srv, "/ui/?q=kafka&understand=1", ownHTMX)
	plan := tokenOf(t, enter.header.Get("HX-Push-Url"))
	first := append(resultIDs(enter.body), resultIDs(getPage(t, srv, "/ui/?q=kafka&page=2&plan="+plan,
		http.StatusOK))...)
	require.Len(t, first, 12)

	p := getWith(t, srv, followUp(t, enter), ownHTMX)
	require.Equal(t, http.StatusOK, p.status, p.body)
	assert.Equal(t, []string{"kafka"}, reranker.Questions())
	want := slices.Clone(first)
	slices.Reverse(want)
	assert.Equal(t, want[:10], resultIDs(p.body), "the head reversed")
	assert.Empty(t, p.header.Get("HX-Push-Url"))
	replaced := p.header.Get("HX-Replace-Url")
	u, err := url.Parse(replaced)
	require.NoError(t, err)
	assert.Equal(t, plan, u.Query().Get("plan"))
	assert.Equal(t, want, orderOf(t, u.Query().Get("order")))
	assert.False(t, u.Query().Has("rerank"))
	assert.Contains(t, p.body, `<p class="rerank-status" id="rerank-status">Reordered by m:1</p>`)
	assert.Equal(t, "Reordered by m:1", saidOf(t, p), "the follow-up's one line, read out")
	assert.Empty(t, saidOf(t, enter), "Enter's page says nothing of an order it hasn't")
	assert.Regexp(t, `<span id="search-took">\d+ ms</span>`, enter.body)
	assert.Regexp(t, `<span id="search-took" hx-preserve="true">\d+ ms</span>`, p.body,
		"the follow-up keeps the time Enter's results took to show, not its own")
	for _, m := range regexp.MustCompile(`id="pager-[a-z0-9-]+" href="([^"]+)"`).FindAllStringSubmatch(p.body, -1) {
		q := mustQuery(t, m[1])
		assert.Equal(t, plan, q.Get("plan"), m[1])
		assert.Equal(t, u.Query().Get("order"), q.Get("order"), m[1])
	}
	for _, m := range regexp.MustCompile(`<a (?:class="chip" )?href="(/ui/[^"]*)"`).FindAllStringSubmatch(p.body, -1) {
		if strings.Contains(m[1], "page=") || strings.HasPrefix(m[1], "/ui/documents/") {
			continue
		}
		q := mustQuery(t, m[1])
		assert.False(t, q.Has("order"), m[1])
		assert.False(t, q.Has("rerank"), m[1])
	}
	assert.Regexp(t, `<a class="chip" href="/ui/\?plan=[^"&]+&amp;q=kafka" aria-label="Remove: Articles"`, p.body,
		"the chip searches without it, in its first order")

	for _, header := range []http.Header{nil, ownHTMX, crossNav} {
		again := getWith(t, srv, replaced, header)
		require.Equal(t, http.StatusOK, again.status)
		assert.Equal(t, want[:10], resultIDs(again.body))
		assert.Contains(t, again.body, "Reordered as the first page was")
		assert.Regexp(t, `<span id="search-took">\d+ ms</span>`, again.body, "its own time, kept by nothing")
		two := getWith(t, srv, replaced+"&page=2", header)
		assert.Equal(t, want[10:], resultIDs(two.body), "the reordered ranking's next window")
	}
	assert.Len(t, reranker.Questions(), 1, "an order asks no model")
	assert.Len(t, planner.Questions(), 1)
}

// saidRE finds the search page's #rerank-said, the follow-up's status.
var saidRE = regexp.MustCompile(`<p class="visually-hidden" id="rerank-said" role="status" aria-live="polite">([^<]*)</p>`)

// saidOf is what p's #rerank-said says, its one line unescaped.
func saidOf(t *testing.T, p page) string {
	t.Helper()
	m := saidRE.FindAllStringSubmatch(p.body, -1)
	require.Len(t, m, 1, "one #rerank-said")
	return html.UnescapeString(m[0][1])
}

// mustQuery is href's query.
func mustQuery(t *testing.T, href string) url.Values {
	t.Helper()
	u, err := url.Parse(html.UnescapeString(href))
	require.NoError(t, err)
	return u.Query()
}

// TestUI_FollowUpAnswers: a follow-up whose reranker kept curio's order
// answers nothing to swap, a 204 with no header, but for a model that
// failed its check or a reranker resting, which say so in the follow-up's
// slot alone, in Go's words; one whose search failed answers a 204 too,
// and the failure is logged once, and so does one whose search was
// refused (TestUI_FollowUpLog holds what is logged of it).
func TestUI_FollowUpAnswers(t *testing.T) {
	rec := &logRecorder{}
	srv, _, reranker := rerankServer(t, 3, rec)
	enter := getWith(t, srv, "/ui/?q=kafka&understand=1", ownHTMX)
	path := followUp(t, enter)
	require.NotEmpty(t, path)
	hx := []string{"HX-Push-Url", "HX-Replace-Url", "HX-Retarget", "HX-Reselect", "HX-Reswap"}
	for _, status := range []rerank.Status{rerank.StatusSkipped, rerank.StatusCold, rerank.StatusChecking,
		rerank.StatusTimeout, rerank.StatusUnavailable, rerank.StatusInvalid} {
		reranker.SetStatus(status)
		p := getWith(t, srv, path, ownHTMX)
		assert.Equal(t, http.StatusNoContent, p.status, status)
		assert.Empty(t, p.body, status)
		for _, h := range hx {
			assert.Empty(t, p.header.Get(h), "%s: %s", status, h)
		}
	}

	held := time.Now().Add(15 * time.Minute)
	for _, tc := range []struct {
		status rerank.Status
		state  rerank.State
		words  string
	}{
		{rerank.StatusUnfit, rerank.State{On: true, Model: "m:1"}, "Kept curio&#39;s order: m:1 didn&#39;t pass " +
			"curio&#39;s check for reordering results; set generation.roles.reranker.model to a model that passes, " +
			"or search.rerank: false."},
		{rerank.StatusTimeout, rerank.State{On: true, Model: "m:1", TimeoutHoldUntil: held},
			"Kept curio&#39;s order: reordering resumes at " + held.Local().Format(time.Kitchen) +
				"; curio doctor says why."},
		{rerank.StatusUnavailable, rerank.State{On: true, Model: "m:1", EvictionHoldUntil: held},
			"Kept curio&#39;s order: reordering resumes at"},
	} {
		reranker.SetStatus(tc.status)
		reranker.SetState(tc.state)
		p := getWith(t, srv, path, ownHTMX)
		require.Equal(t, http.StatusOK, p.status, tc.status)
		assert.Equal(t, "#rerank-status", p.header.Get("HX-Retarget"), tc.status)
		assert.Equal(t, "#rerank-status", p.header.Get("HX-Reselect"), tc.status)
		assert.Equal(t, "outerHTML", p.header.Get("HX-Reswap"), tc.status)
		assert.Empty(t, p.header.Get("HX-Push-Url"), tc.status)
		assert.Empty(t, p.header.Get("HX-Replace-Url"), tc.status)
		assert.Contains(t, p.body, `<p class="rerank-status" id="rerank-status">`+tc.words, tc.status)
		assert.Contains(t, html.EscapeString(saidOf(t, p)), tc.words, "%s: read out", tc.status)
		assert.NotContains(t, p.body, "SECRET-RERANK-REASON", tc.status)
	}

	typed := getWith(t, srv, path, nil)
	require.Equal(t, http.StatusOK, typed.status)
	assert.Contains(t, typed.body, "Kept curio&#39;s order: reordering resumes at", "a typed address says it too")
	assert.Empty(t, typed.header.Get("HX-Retarget"))
	assert.Empty(t, saidOf(t, typed), "a page loaded announces nothing")

	reranker.SetError(errors.New("injected failure"))
	before := len(rec.errors())
	p := getWith(t, srv, path, ownHTMX)
	assert.Equal(t, http.StatusNoContent, p.status)
	assert.Empty(t, p.body)
	assert.Len(t, rec.errors(), before+1, "logged once")

	reranker.SetError(&retrieve.QueryError{Err: errors.New("injected refusal")})
	p = getWith(t, srv, path, ownHTMX)
	assert.Equal(t, http.StatusNoContent, p.status, "a refusal")
	assert.Empty(t, p.body, "a refusal")
}

// TestUI_OrderToken: what isn't an order this page made, or comes with
// what an order can't, is a 400 page that asks no model and searches
// nothing.
func TestUI_OrderToken(t *testing.T) {
	srv, planner, reranker := rerankServer(t, 2, nil)
	plan := encodedPlan(t, api.PlanJSON{Kind: "list"})
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	ids := func(n, size int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%0*d", size, i)
		}
		raw, err := json.Marshal(out)
		require.NoError(t, err)
		return string(raw)
	}
	base := "/ui/?q=kafka&plan=" + plan
	for name, path := range map[string]string{
		"not base64":             base + "&order=not*base64",
		"padded":                 base + "&order=" + base64.URLEncoding.EncodeToString([]byte(`["a"]`)),
		"over 4 KiB":             base + "&order=" + strings.Repeat("A", 4097),
		"not JSON":               base + "&order=" + enc("a,b"),
		"an object":              base + "&order=" + enc(`{"a":1}`),
		"a string":               base + "&order=" + enc(`"a"`),
		"null":                   base + "&order=" + enc(`null`),
		"two values":             base + "&order=" + enc(`["a"]["b"]`),
		"empty":                  base + "&order=" + enc(`[]`),
		"31 IDs":                 base + "&order=" + enc(ids(31, 4)),
		"a 65-byte ID":           base + "&order=" + enc(ids(1, 65)),
		"a control character":    base + "&order=" + enc(`["a\u0007b"]`),
		"a repeated ID":          base + "&order=" + enc(`["a","a"]`),
		"order without plan":     "/ui/?q=kafka&order=" + enc(`["a"]`),
		"order with rerank":      base + "&order=" + enc(`["a"]`) + "&rerank=1",
		"order with understand":  "/ui/?q=kafka&understand=1&order=" + enc(`["a"]`),
		"rerank not 1":           base + "&rerank=yes",
		"rerank without plan":    "/ui/?q=kafka&rerank=1",
		"rerank with understand": "/ui/?q=kafka&understand=1&rerank=1",
	} {
		p := getWith(t, srv, path, ownHTMX)
		assert.Equal(t, http.StatusBadRequest, p.status, name)
		assert.NotContains(t, p.body, `class="result"`, name)
	}
	assert.Empty(t, planner.Questions())
	assert.Empty(t, reranker.Questions())
	getPage(t, srv, base+"&order="+enc(ids(30, 64)), http.StatusOK)
}

// followUpLine is the one DEBUG line the follow-up of request id logged,
// waited for: the follow-up's handler may outlive its aborted client.
func followUpLine(t *testing.T, rec *logRecorder, id string) map[string]any {
	t.Helper()
	var line map[string]any
	require.Eventually(t, func() bool {
		for _, r := range rec.at(slog.LevelDebug) {
			if r["msg"] == "search page: follow-up" && r["request_id"] == id {
				line = r
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the follow-up's line")
	return line
}

// TestUI_FollowUpLog: each follow-up logs one line at DEBUG, how it ended
// and how long it took, with its request's ID and nothing of its query,
// plan or order; nothing else of it is logged but its access line, and
// the ERROR of a search that failed for the daemon's own reasons: a
// refused one is said by its line alone, without the refusal's error.
func TestUI_FollowUpLog(t *testing.T) {
	rec := &logRecorder{}
	srv, _, reranker := rerankServer(t, 3, rec)
	path := followUp(t, getWith(t, srv, "/ui/?q=kafka&understand=1", ownHTMX))
	require.NotEmpty(t, path)
	for _, tc := range []struct {
		name, status string
		set          func()
		errors       int
	}{
		{"reranked", "reranked", func() {}, 0},
		{"kept", "kept", func() { reranker.SetStatus(rerank.StatusSkipped) }, 0},
		{"note", "note", func() {
			reranker.SetStatus(rerank.StatusUnfit)
			reranker.SetState(rerank.State{On: true, Model: "m:1", Check: rerank.CheckUnfit})
		}, 0},
		{"failed", "failed", func() { reranker.SetError(errors.New("injected failure")) }, 1},
		{"refused", "failed", func() { reranker.SetError(&retrieve.QueryError{Err: errors.New("injected refusal")}) }, 0},
	} {
		tc.set()
		before := len(rec.errors())
		p := getWith(t, srv, path, ownHTMX)
		line := followUpLine(t, rec, p.header.Get("X-Request-Id"))
		assert.Equal(t, tc.status, line["status"], tc.name)
		assert.Contains(t, line, "took_ms", tc.name)
		assert.ElementsMatch(t, []string{"msg", "request_id", "status", "took_ms"}, slices.Collect(maps.Keys(line)),
			"%s: nothing of the query, plan or order", tc.name)
		assert.Len(t, rec.errors(), before+tc.errors, tc.name)
	}
	assert.Empty(t, rec.at(slog.LevelWarn))
	for _, info := range rec.at(slog.LevelInfo) {
		assert.Contains(t, []string{"http", "api listening"}, info["msg"], "only access lines at INFO")
	}
}

// blockingReranker reranks nothing until its call's context ends, telling
// a test when it is asked.
type blockingReranker struct{ asked chan struct{} }

func (blockingReranker) Decide(string, understand.Kind, bool) (int, rerank.Outcome) {
	return rerank.MaxDepth, rerank.Outcome{}
}

func (b blockingReranker) Rerank(ctx context.Context, _ rerank.Request) (rerank.Outcome, error) {
	b.asked <- struct{}{}
	<-ctx.Done()
	return rerank.Outcome{}, ctx.Err()
}

func (blockingReranker) State() rerank.State { return rerank.State{On: true, Model: "m:1"} }

// TestUI_FollowUpAborted: a follow-up the page aborts (a keystroke, Enter,
// a page, Ask) or leaves ends its rerank and is logged at DEBUG alone,
// aborted, its access line the 499 it answers: the person moved on, and
// nothing of the daemon's failed, so nothing reaches INFO but the access
// line, nor WARN or ERROR.
func TestUI_FollowUpAborted(t *testing.T) {
	rec := &logRecorder{}
	reranker := blockingReranker{asked: make(chan struct{}, 1)}
	srv := apitest.Start(t, apitest.WithPlanner(apitest.NewPlanner(plannedArticles())), func(d *api.Deps) {
		d.Reranker = reranker
		d.Log = slog.New(rec)
	})
	addSightings(t, srv, 3)
	_, err := srv.DB.Exec(`UPDATE documents SET content_type = 'article'`)
	require.NoError(t, err)
	path := followUp(t, getWith(t, srv, "/ui/?q=kafka&understand=1", ownHTMX))
	require.NotEmpty(t, path)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	maps.Copy(req.Header, ownHTMX)
	sent := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		sent <- err
	}()
	<-reranker.asked
	cancel()
	require.ErrorIs(t, <-sent, context.Canceled)

	var line map[string]any
	require.Eventually(t, func() bool {
		for _, r := range rec.at(slog.LevelDebug) {
			if r["msg"] == "search page: follow-up" {
				line = r
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "aborted", line["status"])
	require.Eventually(t, func() bool {
		for _, info := range rec.at(slog.LevelInfo) {
			if info["request_id"] == line["request_id"] {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the access line")
	for _, info := range rec.at(slog.LevelInfo) {
		assert.Contains(t, []string{"http", "api listening"}, info["msg"], "only access lines at INFO")
		if info["request_id"] == line["request_id"] {
			assert.EqualValues(t, 499, info["status"])
		}
	}
	assert.Empty(t, rec.at(slog.LevelWarn))
	assert.Empty(t, rec.errors())
}

// TestUI_SpeculationAsksNoModel: a browser's guess ahead of a navigation
// (Chrome's prefetch and prerender, the address bar's among them, by
// Sec-Purpose; the legacy prefetch and preview headers) that would ask a
// model, the planner (understand=1) or the reranker (rerank=1), is
// declined before anything is read: a 503 that is never stored, so the
// browser drops the guess and makes a real request when the person
// commits. One that asks no model is served as ever.
func TestUI_SpeculationAsksNoModel(t *testing.T) {
	states, runs, members, saves := new(atomic.Int32), new(atomic.Int32), new(atomic.Int32), new(atomic.Int32)
	planner, reranker := apitest.NewPlanner(plannedArticles()), apitest.NewReranker(rerank.MaxDepth)
	srv := apitest.Start(t, apitest.WithPlanner(planner), apitest.WithReranker(reranker), func(d *api.Deps) {
		d.Documents = countingStates{d.Documents, states}
		d.Insights = countingRuns{d.Insights, runs, members}
		d.Bookmarks = countingSaves{d.Bookmarks, saves}
	})
	addSightings(t, srv, 3)
	_, err := srv.DB.Exec(`UPDATE documents SET content_type = 'article'`)
	require.NoError(t, err)
	plan := encodedPlan(t, api.PlanJSON{Kind: "list", Filters: api.Filters{ContentType: []string{"article"}}})
	guesses := map[string]http.Header{
		"the address bar's prerender": {"Sec-Purpose": {"prefetch;prerender"}, "Sec-Fetch-Site": {"none"},
			"Sec-Fetch-Mode": {"navigate"}, "Sec-Fetch-Dest": {"document"}},
		"a speculation rule's prefetch": {"Sec-Purpose": {"prefetch"}, "Sec-Fetch-Site": {"same-origin"}},
		"a link's prefetch":             {"Purpose": {"prefetch"}},
		"Firefox's prefetch":            {"X-Moz": {"prefetch"}},
		"Safari's preview":              {"X-Purpose": {"preview"}},
	}
	for name, header := range guesses {
		for _, path := range []string{"/ui/?q=kafka&understand=1", "/ui/?q=kafka&plan=" + plan + "&rerank=1",
			"/ui/?q=kafka&understand=bogus"} {
			p := getWith(t, srv, path, header)
			assert.Equal(t, http.StatusServiceUnavailable, p.status, "%s: %s", name, path)
			assert.Equal(t, "no-store", p.header.Get("Cache-Control"), "%s: %s", name, path)
			assert.NotContains(t, p.body, `class="result"`, "%s: %s", name, path)
		}
	}
	assert.Empty(t, planner.Questions())
	assert.Empty(t, reranker.Questions())
	for what, n := range map[string]*atomic.Int32{"states": states, "runs": runs, "members": members, "saves": saves} {
		assert.Zero(t, n.Load(), what)
	}

	for name, header := range guesses {
		for _, path := range []string{"/ui/?q=kafka", "/ui/?q=kafka&plan=" + plan, "/ui/", "/ui/?q=kafka&mode=ask"} {
			p := getWith(t, srv, path, header)
			assert.Equal(t, http.StatusOK, p.status, "%s: %s", name, path)
			assert.Empty(t, p.header.Get("Cache-Control"), "%s: %s", name, path)
		}
	}
	assert.Empty(t, planner.Questions())
	getWith(t, srv, "/ui/?q=kafka&understand=1", http.Header{"Sec-Fetch-Site": {"none"}})
	assert.Equal(t, []string{"kafka"}, planner.Questions(), "the navigation the person then makes plans")
}
