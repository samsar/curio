package cli

import (
	"bytes"
	"context"
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

// TestPredatesUnderstanding: only a 400 whose detail opens with the
// decoder's words for the understand field is an older daemon.
func TestPredatesUnderstanding(t *testing.T) {
	refused := func(status int, detail string) error {
		return &client.APIError{Status: status, Problem: client.Problem{Detail: detail}}
	}
	assert.True(t, predatesUnderstanding(fmt.Errorf("search: %w",
		refused(http.StatusBadRequest, `unknown field "understand": this curio-daemon doesn't support it`))))
	assert.False(t, predatesUnderstanding(refused(http.StatusBadRequest, `unknown field "plan": ...`)))
	assert.False(t, predatesUnderstanding(refused(http.StatusBadRequest, "filters.site: \"*\" is not a domain")))
	assert.False(t, predatesUnderstanding(refused(http.StatusInternalServerError, `unknown field "understand"`)))
	assert.False(t, predatesUnderstanding(errors.New(`unknown field "understand"`)), "not the daemon's answer")
	assert.False(t, predatesUnderstanding(nil))
}
