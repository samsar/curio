package api_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/starprompt"
	"github.com/samsar/curio/internal/store"
)

// starServer serves the dashboard over two kafka documents, counting
// searches toward the star prompt in a star.json of its own, whose path it
// returns.
func starServer(t *testing.T) (*apitest.Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "star.json")
	srv := apitest.Start(t, func(d *api.Deps) { d.Star = starprompt.Open(path, false, nil) },
		apitest.WithReranker(apitest.NewReranker(rerank.MaxDepth)))
	for _, u := range []string{"https://example.com/talk", "https://example.com/notes"} {
		doc := titled(t, srv, u, "Kafka "+u[len("https://example.com/"):], store.DocStateFetched)
		srv.AddContent(t, doc, "kafka notes about partitions")
	}
	return srv, path
}

// TestUI_StarAsk: the fifth search submitted (understand=1) whose first
// page found something asks for a star, once, in a quiet line under its
// results; a keystroke's search, a later page, a plan's link, an
// understood page's follow-up, an order's page and another site's link
// count nothing, and no other page shows the ask.
func TestUI_StarAsk(t *testing.T) {
	srv, path := starServer(t)
	submitted := http.Header{"Sec-Fetch-Site": {"same-origin"}, "Hx-Request": {"true"}}
	const starLine = `<p class="star-ask">`
	plan := encodedPlan(t, api.PlanJSON{Kind: "list"})
	order := base64.RawURLEncoding.EncodeToString([]byte(`["a"]`))

	uncounted := []struct {
		path   string
		header http.Header
	}{
		{"/ui/?q=kafka", submitted},                     // a keystroke
		{"/ui/?q=kafka", nil},                           // a keystroke without htmx
		{"/ui/?q=kafka&understand=1&page=2", submitted}, // a later page
		{"/ui/?q=kafka&understand=1", http.Header{"Sec-Fetch-Site": {"cross-site"},
			"Sec-Fetch-Mode": {"navigate"}, "Sec-Fetch-Dest": {"document"}}}, // another site's link
		{"/ui/?understand=1", submitted},                             // the home
		{"/ui/?q=kafka&plan=" + plan, submitted},                     // a plan's link
		{"/ui/?q=kafka&plan=" + plan + "&rerank=1", nil},             // the follow-up's address, typed
		{"/ui/?q=kafka&plan=" + plan + "&order=" + order, submitted}, // an order's page
	}
	for range starprompt.Threshold {
		for _, u := range uncounted {
			p := getWith(t, srv, u.path, u.header)
			require.Equal(t, http.StatusOK, p.status, "%s: %s", u.path, p.body)
			assert.NotContains(t, p.body, starLine, u.path)
		}
		followUp := getWith(t, srv, "/ui/?q=kafka&plan="+plan+"&rerank=1", submitted)
		require.Equal(t, http.StatusOK, followUp.status, "reordered")
		assert.NotContains(t, followUp.body, starLine)
	}
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "nothing counted")

	for i := 1; i < starprompt.Threshold; i++ {
		assert.NotContains(t, getPage(t, srv, "/ui/?q=kafka&understand=1", http.StatusOK), starLine, "search %d", i)
	}
	body := getPage(t, srv, "/ui/?q=kafka&understand=1", http.StatusOK)
	assert.Contains(t, body, starLine+starprompt.Message+` <a href="`+starprompt.RepoURL+
		`" rel="noopener noreferrer" target="_blank">Star curio on GitHub</a>. `+
		strings.ReplaceAll(starprompt.Once, "'", "&#39;")+"</p>")
	assert.Equal(t, 1, strings.Count(body, starLine))

	for range 3 {
		assert.NotContains(t, getPage(t, srv, "/ui/?q=kafka&understand=1", http.StatusOK), starLine, "asked once")
	}
	var rec struct {
		Searches int    `json:"searches"`
		ShownIn  string `json:"shown_in"`
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &rec))
	assert.Equal(t, starprompt.Threshold, rec.Searches)
	assert.Equal(t, string(starprompt.SurfaceDashboard), rec.ShownIn)
}

// TestUI_StarAskNeedsResults: a submitted search that found nothing by its
// words, which shows no results or only what is nearest in meaning,
// counts nothing.
func TestUI_StarAskNeedsResults(t *testing.T) {
	srv, path := starServer(t)
	for range starprompt.Threshold * 2 {
		assert.NotContains(t, getPage(t, srv, "/ui/?q=zyzzyva+quokka&understand=1", http.StatusOK), "star-ask")
	}
	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestUI_AsksNeverCountTowardTheStar: a question asked as the dashboard
// asks it, same-origin JSON to POST /v1/ask, counts nothing toward the one
// ask for a star, however many are asked.
func TestUI_AsksNeverCountTowardTheStar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "star.json")
	srv := apitest.Start(t, func(d *api.Deps) { d.Star = starprompt.Open(path, false, nil) },
		apitest.WithAsker(apitest.NewAsker(ask.Done{Status: ask.StatusAnswered, PromptVersion: ask.PromptVersion,
			Route: ask.RouteAnswer})))
	for range starprompt.Threshold * 2 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/ask",
			strings.NewReader(`{"question":"kafka"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist, "nothing counted")
}
