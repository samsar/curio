package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// TestSearch_HitsWithoutMatches: a hit found by what it was saved as, with
// no passage, says what it was saved as, named by its bookmark's title when
// it has none of its own; a dead one says its page is gone, a failed one
// that it couldn't be read.
func TestSearch_HitsWithoutMatches(t *testing.T) {
	srv := apitest.Start(t, apitest.DocumentLists)
	srv.AddBookmark(t, "https://example.com/later", store.SourceChrome, "Sourdough hydration", "/Bookmarks Bar/Baking")
	srv.AddDocument(t, "https://example.com/gone", store.DocStateDead)
	srv.AddBookmark(t, "https://example.com/gone", store.SourceSafari, "Sourdough starters", "")
	srv.AddDocument(t, "https://example.com/walled", store.DocStateFailed)
	srv.AddBookmark(t, "https://example.com/walled", store.SourceSafari, "Sourdough levain", "")

	out := mustRun(t, srv, "search", "sourdough")
	assert.Contains(t, out, `3 results for "sourdough"`)
	assert.Contains(t, out, " 1. Sourdough", "an untitled hit is named by its bookmark")
	assert.Contains(t, out, "    saved as: Sourdough hydration · Baking\n", "its folder without the browser's root")
	assert.Contains(t, out, "    saved as: Sourdough starters\n")
	assert.Contains(t, out, "    the page is no longer available (dead); its bookmark matched\n")
	assert.Contains(t, out, "    the page couldn't be read (failed); its bookmark matched\n")
	assert.Equal(t, 2, strings.Count(out, "; its bookmark matched"), "the pending page is still there")
}

// TestSavedAs: the line names the parts the bookmark docmeta.SavedAs
// picks has, joined by " · ".
func TestSavedAs(t *testing.T) {
	cases := []struct {
		name      string
		bookmarks []client.SearchHitBookmark
		want      string
	}{
		{"none", nil, ""},
		{"title and folder", []client.SearchHitBookmark{{Title: "T", FolderPath: "/F/G"}}, "T · F / G"},
		{"a folder alone", []client.SearchHitBookmark{{FolderPath: "/Favorites/F"}}, "F"},
		{"a root alone", []client.SearchHitBookmark{{Title: "T", FolderPath: "/Bookmarks Bar"}}, "T"},
		{"nothing to say", []client.SearchHitBookmark{{Source: store.SourceChrome}}, ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, savedAs(tc.bookmarks), tc.name)
	}
}

// TestSearch_Filters: each filter flag reaches the daemon under its own
// field, which refuses a value it can't take by that name; a folder whose
// name holds a comma is one folder; and a date-filtered search says how
// many documents it couldn't judge, which a search without one doesn't.
func TestSearch_Filters(t *testing.T) {
	srv := apitest.Start(t, apitest.DocumentLists)
	srv.AddBookmark(t, "https://example.com/comma", store.SourceChrome, "Sourdough comma", "/Bookmarks Bar/A,B")
	srv.AddBookmark(t, "https://example.com/plain", store.SourceChrome, "Sourdough plain", "/Bookmarks Bar/A")
	srv.AddDocument(t, "https://example.com/undated", store.DocStatePending)
	srv.AddBookmark(t, "https://example.com/undated", store.SourceSafari, "Sourdough undated", "")
	_, err := srv.DB.Exec(`UPDATE bookmarks SET saved_date = 'unknown' WHERE source = 'safari'`)
	require.NoError(t, err)

	for flag, field := range map[string]string{
		"--saved-after=last week":                                  "saved_after",
		"--saved-before=2024-13-01":                                "saved_before",
		"--published-after=2024-01-01T10:00":                       "published_after",
		"--published-before=yesterday":                             "published_before",
		"--site=https://example.com":                               "site",
		"--folder=/":                                               "folder",
		"--host=" + strings.Repeat("a.example,", 32) + "b.example": "host",
		"--source=opera":                                           "source",
		"--type=essay":                                             "content_type",
		"--include-undated":                                        "include_undated",
	} {
		_, err := runCLI(t, srv, "search", "sourdough", flag)
		require.Error(t, err, flag)
		assert.Contains(t, err.Error(), "filters."+field+": ", flag)
	}

	out := mustRun(t, srv, "search", "sourdough", "--folder", "/Bookmarks Bar/A,B")
	assert.Contains(t, out, "1 results")
	assert.Contains(t, out, "https://example.com/comma")
	assert.NotContains(t, out, "no date")

	out = mustRun(t, srv, "search", "sourdough", "--saved-after", "2000-01-01", "--site", "www.example.com")
	assert.Contains(t, out, "2 results")
	assert.Contains(t, out, "1 document has no date the date filters can judge; left out (--include-undated keeps them)\n")
	out = mustRun(t, srv, "search", "sourdough", "--saved-after", "2000-01-01", "--include-undated")
	assert.Contains(t, out, "3 results")
	assert.Contains(t, out, "1 document has no date the date filters can judge; kept (--include-undated)\n")
}

// TestSearch_UnderstandsByDefault: curio search asks for understanding and
// says what it understood before the results; --literal searches the words
// alone; a filter flag wins over the plan's of its dimension.
func TestSearch_UnderstandsByDefault(t *testing.T) {
	planner := apitest.NewPlanner(understand.Outcome{Status: understand.StatusPlanned, Model: "gemma4:26b",
		Took: 1340 * time.Millisecond, Plan: &understand.Plan{Kind: understand.KindList,
			Filters:  search.FilterRequest{ContentType: []string{"article"}},
			Rewrites: []string{"sourdough levain"}, Names: []string{"Tartine Bread"}}})
	srv := apitest.Start(t, apitest.DocumentLists, apitest.WithPlanner(planner))
	srv.AddBookmark(t, "https://example.com/bread", store.SourceChrome, "Sourdough bread", "")

	out := mustRun(t, srv, "search", "sourdough", "loaves")
	assert.True(t, strings.HasPrefix(out, "understood (gemma4:26b, 1.3 s): list · Articles\n"+
		"also searched: sourdough levain · looked for: Tartine Bread\n\n"), out)
	assert.Equal(t, []string{"sourdough loaves"}, planner.Questions())

	out = mustRun(t, srv, "search", "--literal", "sourdough")
	assert.NotContains(t, out, "understood")
	assert.Len(t, planner.Questions(), 1, "--literal asks no model")

	out = mustRun(t, srv, "search", "--type", "video", "sourdough")
	assert.True(t, strings.HasPrefix(out, "understood (gemma4:26b, 1.3 s): list\n"), "the plan as applied: %s", out)

	planner.Set(understand.Outcome{Status: understand.StatusCold, Reason: "gemma4:26b is loading"}, nil)
	out = mustRun(t, srv, "search", "sourdough")
	assert.True(t, strings.HasPrefix(out, "searched your words: gemma4:26b is loading\n\n"), out)
}

// TestSearch_OlderDaemon: a daemon from before understanding refuses the
// field with its unknown-field 400; curio search searches the words once
// more without it, and says why on stderr.
func TestSearch_OlderDaemon(t *testing.T) {
	var bodies []string
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		bodies = append(bodies, string(raw))
		if strings.Contains(string(raw), `"understand"`) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"type":"about:blank","title":"bad request","status":400,"detail":`+
				`"unknown field \"understand\": this curio-daemon (version v2.6.0) doesn't support it. If the client is `+
				`newer, restart the daemon: run `+"`curio daemon stop`"+`, and the next command starts the current one"}`)
			return
		}
		fmt.Fprint(w, `{"query":"kafka","took_ms":3,"bm25_hits":0,"vector_hits":0,"total":0,"capped":false,"items":[]}`)
	}))
	t.Cleanup(old.Close)

	var stderr bytes.Buffer
	res, err := sendSearch(context.Background(), client.New(old.URL), client.SearchRequest{Query: "kafka",
		Understand: true}, &stderr)
	require.NoError(t, err)
	assert.Equal(t, "kafka", res.Query)
	assert.Equal(t, []string{`{"query":"kafka","understand":true}`, `{"query":"kafka"}`}, bodies)
	assert.Equal(t, "note: this curio-daemon predates understanding questions; searched your words "+
		"(restart it: curio daemon stop)\n", stderr.String())

	bodies = nil
	stderr.Reset()
	_, err = sendSearch(context.Background(), client.New(old.URL), client.SearchRequest{Query: "kafka",
		Filters: &client.SearchFilters{Site: []string{"example.com"}}}, &stderr)
	require.NoError(t, err)
	assert.Len(t, bodies, 1, "a literal search is sent once")
	assert.Empty(t, stderr.String())
}

// TestPredates: only a 400 whose detail opens with the decoder's words for
// the field is an older daemon.
func TestPredates(t *testing.T) {
	refused := func(status int, detail string) error {
		return &client.APIError{Status: status, Problem: client.Problem{Detail: detail}}
	}
	assert.True(t, predates(fmt.Errorf("search: %w",
		refused(http.StatusBadRequest, `unknown field "understand": this curio-daemon doesn't support it`)), "understand"))
	assert.True(t, predates(refused(http.StatusBadRequest, `unknown field "rerank": ...`), "rerank"))
	assert.False(t, predates(refused(http.StatusBadRequest, `unknown field "rerank": ...`), "understand"))
	assert.False(t, predates(refused(http.StatusBadRequest, `unknown field "plan": ...`), "understand"))
	assert.False(t, predates(refused(http.StatusBadRequest, "filters.site: \"*\" is not a domain"), "understand"))
	assert.False(t, predates(refused(http.StatusInternalServerError, `unknown field "understand"`), "understand"))
	assert.False(t, predates(errors.New(`unknown field "understand"`), "understand"), "not the daemon's answer")
	assert.False(t, predates(nil, "understand"))
}

// refusingDaemon is a daemon whose decoder refuses fields, as a daemon that
// predates them does: the first of them a request sends. It records each
// body.
func refusingDaemon(t *testing.T, fields ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		bodies = append(bodies, string(raw))
		var got map[string]json.RawMessage
		assert.NoError(t, json.Unmarshal(raw, &got))
		for _, f := range fields {
			if _, sent := got[f]; sent {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"type":"about:blank","title":"bad request","status":400,"detail":`+
					`"unknown field \"%s\": this curio-daemon (version v2.6.0) doesn't support it. If the client is `+
					`newer, restart the daemon: run `+"`curio daemon stop`"+`, and the next command starts the current one"}`,
					f)
				return
			}
		}
		fmt.Fprint(w, `{"query":"kafka","took_ms":3,"bm25_hits":0,"vector_hits":0,"total":0,"capped":false,"items":[]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

// TestSearch_DaemonBeforeReranking: a daemon that predates reranking but
// understands has the search sent again without rerank; one that predates
// both, without understand, then without rerank, each said once on
// stderr.
func TestSearch_DaemonBeforeReranking(t *testing.T) {
	const understood = "note: this curio-daemon predates understanding questions; searched your words " +
		"(restart it: curio daemon stop)\n"
	const reranked = "note: this curio-daemon predates reranking; kept its first ranking (restart it: curio " +
		"daemon stop)\n"
	for _, tc := range []struct {
		name   string
		fields []string
		bodies []string
		notes  string
	}{
		{"before reranking", []string{"rerank"}, []string{`{"query":"kafka","understand":true,"rerank":true}`,
			`{"query":"kafka","understand":true}`}, reranked},
		{"before both", []string{"understand", "rerank"}, []string{
			`{"query":"kafka","understand":true,"rerank":true}`, `{"query":"kafka","rerank":true}`,
			`{"query":"kafka"}`}, understood + reranked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, bodies := refusingDaemon(t, tc.fields...)
			var stderr bytes.Buffer
			res, err := sendSearch(context.Background(), client.New(srv.URL), client.SearchRequest{Query: "kafka",
				Understand: true, Rerank: true}, &stderr)
			require.NoError(t, err)
			assert.Equal(t, "kafka", res.Query)
			assert.Equal(t, tc.bodies, *bodies)
			assert.Equal(t, tc.notes, stderr.String())
		})
	}
}

// TestRerankingLines: a reranked search says by what, how deep and how
// long; one that kept its first ranking why, unless it was off or its kind
// isn't reranked.
func TestRerankingLines(t *testing.T) {
	for _, tc := range []struct {
		r    *client.Reranking
		want []string
	}{
		{&client.Reranking{Status: "reranked", Model: "gemma4:26b", Method: "listwise", Depth: 20, TookMS: 3450},
			[]string{"reranked (gemma4:26b, listwise, top 20, 3.5 s)"}},
		{&client.Reranking{Status: "supplied", Order: []string{"a", "b"}},
			[]string{"reranked as the first page was, by the order sent"}},
		{&client.Reranking{Status: "supplied", Order: []string{"a", "b"}, Stale: 1},
			[]string{"reranked as the first page was, by the order sent; 1 of its documents left the head"}},
		{&client.Reranking{Status: "cold", Reason: "gemma4:26b is loading"},
			[]string{"kept the first ranking: gemma4:26b is loading"}},
		{&client.Reranking{Status: "timeout", Reason: "reranking took longer than 7s"},
			[]string{"kept the first ranking: reranking took longer than 7s"}},
		{&client.Reranking{Status: "too_long", Reason: "the question is 600 bytes"},
			[]string{"kept the first ranking: the question is 600 bytes"}},
		{&client.Reranking{Status: "off", Reason: "search reranking is off (search.rerank: false)"}, nil},
		{&client.Reranking{Status: "skipped", Reason: "searches for keywords keep their first ranking"}, nil},
		{nil, nil},
	} {
		assert.Equal(t, tc.want, rerankingLines(tc.r))
	}
	var out bytes.Buffer
	renderHeader(&out, &client.Understanding{Status: "cold", Reason: "gemma4:26b is loading"},
		&client.Reranking{Status: "cold", Reason: "gemma4:26b is loading"}, time.UTC)
	assert.Equal(t, "searched your words: gemma4:26b is loading\nkept the first ranking: gemma4:26b is loading\n\n",
		out.String(), "the rerank line follows the understood line")
	out.Reset()
	renderHeader(&out, nil, &client.Reranking{Status: "off"}, time.UTC)
	assert.Empty(t, out.String())
}

func TestRenderSearchResultsEscapesControls(t *testing.T) {
	title := "A title\x1b[2J"
	res := &client.SearchResponse{Query: "q", Items: []client.SearchHit{{
		Document: client.Document{ID: "d1", URL: "https://example.com/\x1b]8;;x\x07", Title: &title},
		Matches:  []client.ChunkMatch{{Snippet: "a snippet\x1b]52;c;Zm9v\x07"}},
	}}}
	var b bytes.Buffer
	renderSearchResults(&b, res)
	assert.NotContains(t, b.String(), "\x1b")
	assert.NotContains(t, b.String(), "\x07")
	assert.Contains(t, b.String(), `\x1b`)
}
