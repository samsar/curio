package api_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/starprompt"
	"github.com/samsar/curio/internal/store"
)

// starServer serves the dashboard over two kafka documents, counting
// searches toward the star prompt in a star.json of its own, whose path it
// returns.
func starServer(t *testing.T) (*apitest.Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "star.json")
	srv := apitest.Start(t, func(d *api.Deps) { d.Star = starprompt.Open(path, false, nil) })
	for _, u := range []string{"https://example.com/talk", "https://example.com/notes"} {
		doc := titled(t, srv, u, "Kafka "+u[len("https://example.com/"):], store.DocStateFetched)
		srv.AddContent(t, doc, "kafka notes about partitions")
	}
	return srv, path
}

// TestUI_StarAsk: the fifth search submitted (understand=1) whose first
// page found something asks for a star, once, in a quiet line under its
// results; a keystroke's search, a later page, a plan's link and another
// site's link count nothing, and no other page shows the ask.
func TestUI_StarAsk(t *testing.T) {
	srv, path := starServer(t)
	submitted := http.Header{"Sec-Fetch-Site": {"same-origin"}, "Hx-Request": {"true"}}
	const ask = `<p class="star-ask">`

	uncounted := []struct {
		path   string
		header http.Header
	}{
		{"/ui/?q=kafka", submitted},                     // a keystroke
		{"/ui/?q=kafka", nil},                           // a keystroke without htmx
		{"/ui/?q=kafka&understand=1&page=2", submitted}, // a later page
		{"/ui/?q=kafka&understand=1", http.Header{"Sec-Fetch-Site": {"cross-site"},
			"Sec-Fetch-Mode": {"navigate"}, "Sec-Fetch-Dest": {"document"}}}, // another site's link
		{"/ui/?understand=1", submitted}, // the home
	}
	for range starprompt.Threshold {
		for _, u := range uncounted {
			p := getWith(t, srv, u.path, u.header)
			require.Equal(t, http.StatusOK, p.status, "%s: %s", u.path, p.body)
			assert.NotContains(t, p.body, ask, u.path)
		}
	}
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "nothing counted")

	for i := 1; i < starprompt.Threshold; i++ {
		assert.NotContains(t, getPage(t, srv, "/ui/?q=kafka&understand=1", http.StatusOK), ask, "search %d", i)
	}
	body := getPage(t, srv, "/ui/?q=kafka&understand=1", http.StatusOK)
	assert.Contains(t, body, ask+starprompt.Message+` <a href="`+starprompt.RepoURL+
		`" rel="noopener noreferrer" target="_blank">Star curio on GitHub</a>. `+
		strings.ReplaceAll(starprompt.Once, "'", "&#39;")+"</p>")
	assert.Equal(t, 1, strings.Count(body, ask))

	for range 3 {
		assert.NotContains(t, getPage(t, srv, "/ui/?q=kafka&understand=1", http.StatusOK), ask, "asked once")
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
