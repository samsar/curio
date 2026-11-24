package api_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/modelgate"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// askServer serves the dashboard over a kafka article with asker, a
// planner and a reranker that record what they are asked, and opts.
func askServer(t *testing.T, asker *apitest.Asker, opts ...func(*api.Deps)) (*apitest.Server, *apitest.Planner,
	*apitest.Reranker) {
	t.Helper()
	planner, reranker := apitest.NewPlanner(plannedSummer()), apitest.NewReranker(rerank.MaxDepth)
	opts = append(opts, apitest.WithPlanner(planner), apitest.WithReranker(reranker))
	if asker != nil {
		opts = append(opts, apitest.WithAsker(asker))
	}
	srv := apitest.Start(t, opts...)
	doc := titled(t, srv, "https://example.com/kafka", "Kafka notes", store.DocStateFetched)
	srv.AddContent(t, doc, "kafka notes about partitions")
	return srv, planner, reranker
}

// TestUI_AskMode: ?mode=ask opens the page in Ask mode, its question in
// the box, which keeps Search's words for a page without the script, the
// switch pressed for Ask and the answer's shell on the page, and reads
// nothing for it but the asker's memory: no search (the hits' summaries
// unread), nothing of the home, no plan, no rerank, no ask, from the
// dashboard or from another site's link alike.
func TestUI_AskMode(t *testing.T) {
	for name, header := range map[string]http.Header{
		"the user's own":      nil,
		"another site's link": {"Sec-Fetch-Site": {"cross-site"}, "Sec-Fetch-Mode": {"navigate"}, "Sec-Fetch-Dest": {"document"}},
	} {
		t.Run(name, func(t *testing.T) {
			asker := apitest.NewAsker()
			reads := map[string]*atomic.Int32{"states": {}, "runs": {}, "members": {}, "saves": {}, "summaries": {}}
			srv, planner, reranker := askServer(t, asker, func(d *api.Deps) {
				d.Documents = countingStates{d.Documents, reads["states"]}
				d.Insights = countingRuns{d.Insights, reads["runs"], reads["members"]}
				d.Bookmarks = countingSaves{d.Bookmarks, reads["saves"]}
				d.Summaries = countingSummaries{d.Summaries, reads["summaries"]}
			})
			getPage(t, srv, "/ui/?q=kafka", http.StatusOK)
			require.Positive(t, reads["summaries"].Load(), "a search reads its hits' summaries")
			for _, n := range reads {
				n.Store(0)
			}
			before := asker.States()
			p := getWith(t, srv, "/ui/?q=kafka&mode=ask", header)
			require.Equal(t, http.StatusOK, p.status, p.body)
			assert.Contains(t, p.body, `<div class="search-page" data-mode="ask">`)
			assert.Contains(t, p.body, `<button type="button" id="mode-search" aria-pressed="false"`)
			assert.Contains(t, p.body, `<button type="button" id="mode-ask" aria-pressed="true"`)
			assert.Contains(t, p.body, `name="q" value="kafka" placeholder="Search your library"`)
			assert.Contains(t, p.body, `aria-label="Search"><svg`)
			assert.Contains(t, p.body, `<section class="ask js-only" id="ask"`)
			assert.Contains(t, p.body, `/ui/static/ask.`)
			assert.NotContains(t, p.body, `class="result"`, "nothing searched")
			assert.NotContains(t, p.body, "Kafka notes")
			assert.Empty(t, planner.Questions())
			assert.Empty(t, reranker.Questions())
			assert.Empty(t, asker.Requests())
			assert.Equal(t, 1, asker.States()-before, "the asker's memory, read once")
			for what, n := range reads {
				assert.Zero(t, n.Load(), what)
			}
		})
	}
}

// countingSummaries counts the reads of the hits' summaries, which every
// search with hits makes.
type countingSummaries struct {
	store.SummaryStore
	calls *atomic.Int32
}

func (c countingSummaries) CurrentSummaries(ctx context.Context, tenantID string, ids []string) (map[string]*store.Summary,
	error) {
	c.calls.Add(1)
	return c.SummaryStore.CurrentSummaries(ctx, tenantID, ids)
}

// TestUI_AskModeTakesTheQuestionAlone: a mode that isn't one, and Ask
// mode with any of a search's parameters, are 400 pages before anything is
// read or asked.
func TestUI_AskModeTakesTheQuestionAlone(t *testing.T) {
	asker := apitest.NewAsker()
	srv, planner, reranker := askServer(t, asker)
	token := encodedPlan(t, api.PlanJSON{Kind: "list"})
	for _, path := range []string{
		"/ui/?mode=bogus", "/ui/?q=x&mode=Ask",
		"/ui/?q=x&mode=ask&plan=" + token, "/ui/?q=x&mode=ask&understand=1", "/ui/?q=x&mode=ask&rerank=1",
		"/ui/?q=x&mode=ask&order=WyJhIl0", "/ui/?q=x&mode=ask&page=2", "/ui/?q=x&mode=ask&content_type=pdf",
	} {
		getPage(t, srv, path, http.StatusBadRequest)
	}
	assert.Empty(t, planner.Questions())
	assert.Empty(t, reranker.Questions())
	assert.Empty(t, asker.Requests())
	assert.Zero(t, asker.States(), "refused before Ask is read")
	for _, path := range []string{"/ui/?q=kafka&mode=search", "/ui/?q=kafka"} {
		body := getPage(t, srv, path, http.StatusOK)
		assert.Contains(t, body, `<div class="search-page" data-mode="search">`, path)
		assert.Contains(t, body, "Kafka notes", "%s: searched as ever", path)
	}
}

// TestUI_AskReadOncePerPage: what the asker knows is read once for a
// whole page, from its memory, and never for htmx's requests, a
// keystroke's, Enter's or a page's, whose answer is swapped into the
// results alone.
func TestUI_AskReadOncePerPage(t *testing.T) {
	asker := apitest.NewAsker()
	srv, _, _ := askServer(t, asker)
	htmx := http.Header{"Hx-Request": {"true"}, "Sec-Fetch-Site": {"same-origin"}}
	for _, tc := range []struct {
		path   string
		header http.Header
		reads  int
	}{
		{"/ui/?q=kafka", htmx, 0},
		{"/ui/?q=kafka&understand=1", htmx, 0},
		{"/ui/?q=kafka&page=2", htmx, 0},
		{"/ui/?q=", htmx, 0},
		{"/ui/?q=kafka", nil, 1},
		{"/ui/", nil, 1},
		{"/ui/?q=kafka&mode=ask", nil, 1},
	} {
		before := asker.States()
		p := getWith(t, srv, tc.path, tc.header)
		require.Equal(t, http.StatusOK, p.status, tc.path)
		assert.Equal(t, tc.reads, asker.States()-before, tc.path)
	}
}

// TestUI_AskOff: a page that can't ask has no switch, no shell and no
// ask.js; an address that asked for Ask mode searches the words, saying
// why in the kind's own words, told by the state's facts, never the
// asker's reason, its check's or its failure's. A hold, a model not
// checked yet, or a reason no fact names, still asks: its answer says
// why it didn't come.
func TestUI_AskOff(t *testing.T) {
	const secret = "SECRET-REASON"
	failure := modelgate.Failure{Cause: "unavailable:generate", At: time.Now(), Err: secret}
	for _, tc := range []struct {
		name  string
		state *ask.State // nil for no asker
		words string
	}{
		{"no asker", nil, "Ask is off (ask.enabled: false)."},
		{"off", &ask.State{Model: "m:1", Reason: secret, Failure: failure}, "Ask is off (ask.enabled: false)."},
		{"off by default", &ask.State{OffByDefault: true, Model: "m:1", Reason: secret},
			"Ask is off by default for m:1, which isn&#39;t measured for writing answers; ask.enabled: true turns " +
				"it on (then restart)."},
		{"withheld", &ask.State{On: true, Withheld: true, Model: "m:1", Reason: secret},
			"Ask would send your question and pages off this machine"},
		{"can't stream", &ask.State{On: true, NoStream: true, Model: "m:1", Reason: secret},
			"Ask is off: the answers role&#39;s provider can&#39;t stream answers."},
		{"unfit", &ask.State{On: true, Model: "m:1", Check: modelgate.CheckUnfit, Unfit: secret, Failure: failure},
			"m:1 didn&#39;t pass curio&#39;s check for writing answers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asker *apitest.Asker
			if tc.state != nil {
				asker = apitest.NewAsker()
				asker.SetState(*tc.state)
			}
			srv, _, _ := askServer(t, asker)
			for _, path := range []string{"/ui/", "/ui/?q=kafka"} {
				body := getPage(t, srv, path, http.StatusOK)
				assert.NotContains(t, body, `id="mode-ask"`, path)
				assert.NotContains(t, body, `id="ask"`, path)
				assert.NotContains(t, body, `/ui/static/ask.`, path)
				assert.NotContains(t, body, "Searched your words: Ask", "%s: no off line unless asked", path)
			}
			body := getPage(t, srv, "/ui/?q=kafka&mode=ask", http.StatusOK)
			assert.Contains(t, body, `<div class="search-page" data-mode="search">`)
			assert.Contains(t, body, `<p class="understood-note">Searched your words: `+tc.words)
			assert.Less(t, strings.Index(body, "Searched your words: Ask"), strings.Index(body, "Kafka notes"),
				"above the words' results")
			assert.NotContains(t, body, secret)
			assert.NotContains(t, body, `id="ask"`)
		})
	}
	for name, st := range map[string]ask.State{
		"unchecked":    {On: true, Model: "m:1", Check: modelgate.CheckUnchecked},
		"timeout held": {On: true, Model: "m:1", Check: modelgate.CheckFit, TimeoutHoldUntil: time.Now().Add(time.Hour)},
		"eviction held": {On: true, Model: "m:1", Check: modelgate.CheckFit,
			EvictionHoldUntil: time.Now().Add(time.Hour)},
		"a failure, a model not pulled": {On: true, Model: "m:1", Check: modelgate.CheckUnchecked,
			Failure: modelgate.Failure{Cause: "unavailable:missing", At: time.Now(), Err: secret}},
		// Not worded as any kind: its ask ends off, in words that name no cause.
		"a reason no fact names": {On: true, Model: "m:1", Check: modelgate.CheckFit, Reason: secret},
	} {
		t.Run(name, func(t *testing.T) {
			asker := apitest.NewAsker()
			asker.SetState(st)
			srv, _, _ := askServer(t, asker)
			body := getPage(t, srv, "/ui/?q=kafka", http.StatusOK)
			assert.Contains(t, body, `id="mode-ask"`)
			assert.Contains(t, body, `id="ask"`)
			assert.NotContains(t, body, secret)
		})
	}
}

// TestUI_AskNotes: the page words each status an ask can end with, each
// flag of its answer, the evidence route's, and each way an ask ends
// without its done event, in a template ask.js shows by key, and holds
// the problem type that tells a starting daemon's 503 from a busy one's;
// a status added to ask without its words fails here.
func TestUI_AskNotes(t *testing.T) {
	asker := apitest.NewAsker()
	asker.SetState(ask.State{On: true, Model: "m:1", Check: modelgate.CheckFit})
	srv, _, _ := askServer(t, asker)
	body := getPage(t, srv, "/ui/?q=kafka", http.StatusOK)
	keys := make([]string, 0, len(ask.Statuses())+3)
	for _, s := range ask.Statuses() {
		keys = append(keys, string(s))
	}
	keys = append(keys, "partial", "uncited", "invalid", "unsided", "unjudged", "failed", "question", "busy", "starting",
		"refused", "lost", "request")
	for _, key := range keys {
		open := `<template data-note="` + key + `">`
		at := strings.Index(body, open)
		require.GreaterOrEqual(t, at, 0, key)
		text, _, _ := strings.Cut(body[at+len(open):], "</template>")
		assert.NotEmpty(t, strings.TrimSpace(text), key)
	}
	assert.Contains(t, body, `<template data-note="timeout">The answer took longer than 90 s (ask.timeout_seconds)`)
	assert.Contains(t, body, `<template data-note="question">curio couldn&#39;t take the question as written: a `+
		`question is at most `+strconv.Itoa(ask.MaxQuestionBytes)+` bytes.</template>`)
	assert.Contains(t, body, `data-starting-type="`+api.StartingProblemType+`"`)
	assert.Contains(t, body, `<template data-note="unavailable">m:1 isn&#39;t available: it isn&#39;t pulled, or `+
		`Ollama isn&#39;t running; curio doctor says why.</template>`)
}

// TestUI_PlannerAndRerankerUnaskedByAskMode: understanding stays Enter's,
// in Search mode: Ask mode's page sends nothing to the planner or the
// reranker, its htmx request included.
func TestUI_PlannerAndRerankerUnaskedByAskMode(t *testing.T) {
	asker := apitest.NewAsker()
	srv, planner, reranker := askServer(t, asker)
	planner.Set(understand.Outcome{Status: understand.StatusPlanned, Plan: &understand.Plan{Kind: understand.KindOne}},
		nil)
	p := getWith(t, srv, "/ui/?q=kafka&mode=ask", http.Header{"Hx-Request": {"true"}})
	require.Equal(t, http.StatusOK, p.status)
	assert.Empty(t, planner.Questions())
	assert.Empty(t, reranker.Questions())
	assert.Empty(t, p.header.Get("HX-Push-Url"))
}
