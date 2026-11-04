//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/store"
)

// summaryWord is a word only the article's summary says: a search finds
// the page by it through the summary list alone.
const summaryWord = "brewcraft"

// summaryAnswers answers the fitness check's made-up pages as a fit model
// does, and every other page with a summary that names summaryWord.
func summaryAnswers(prompt string) string {
	switch {
	case strings.Contains(prompt, "Kestrel K2"):
		return `{"kind":"article","site":"","author":"","about":"A crawler ships.","entities":["Kestrel K2"],` +
			`"claims":[],"descriptions":[]}`
	case strings.Contains(prompt, "Title: Sign in\n"):
		return `{"kind":"not_the_page","site":"","author":"","about":"","entities":[],"claims":[],"descriptions":[]}`
	case strings.Contains(prompt, "Castellbo"):
		return `{"kind":"profile","site":"","author":"Oriane Castellbo-Ferrant","about":"A bio.","entities":[],` +
			`"claims":[],"descriptions":[]}`
	}
	return `{"kind":"article","site":"","author":"","about":"An essay on ` + summaryWord + `, the home brewer's art.",` +
		`"entities":["Zymurgy"],"claims":[],"descriptions":["that ` + summaryWord + ` primer"]}`
}

// TestDaemon_SummarizesAPage: with summaries and the summary list turned
// on, a page saved is summarized once the summaries' model has passed its
// check, the summary is found by a word only it says, and the pass's queue
// drains.
func TestDaemon_SummarizesAPage(t *testing.T) {
	ctx := context.Background()
	ollama, ollamaURL := serveOllama(t, digestA, "0.34.4")
	ollama.summary = summaryAnswers
	pages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, articleHTML())
	}))
	t.Cleanup(pages.Close)
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen, ollamaURL)
	cfg, err := os.ReadFile(home.ConfigPath())
	require.NoError(t, err)
	// On explicitly, and the summary list with them: both are off by
	// default.
	summarized := strings.Replace(string(cfg), "summaries:\n  enabled: false\n", "summaries:\n  enabled: true\n", 1)
	summarized = strings.Replace(summarized, "search:\n  understand: false\n",
		"search:\n  understand: false\n  summary_weight: 1\n", 1)
	summarized = strings.Replace(summarized, "generation:\n", "generation:\n  base_url: "+strconv.Quote(ollamaURL)+"\n", 1)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(summarized), 0o600))
	// The pass yields a moment, not a minute, after a search.
	t.Setenv("CURIO_E2E_SUMMARIES", "quiet=100ms")
	baseURL := "http://" + listen
	ctl := daemonctl.New(home, daemonBin, baseURL)
	t.Cleanup(func() {
		if st, err := ctl.Status(context.Background()); err == nil && st.State == daemonctl.Running && st.PID > 0 {
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	})
	require.NoError(t, ctl.EnsureRunning(ctx), logTail(home))
	c := client.New(baseURL)
	health, err := c.Healthz(ctx)
	require.NoError(t, err)
	assert.Contains(t, health.GenerationRoles, "summaries")
	require.NotNil(t, health.Summaries)
	assert.True(t, health.Summaries.On)

	created, err := c.CreateBookmark(ctx, client.CreateBookmarkRequest{URL: pages.URL + "/zymurgy"})
	require.NoError(t, err)
	docID := *created.Bookmark.DocumentID
	var doc *client.Document
	require.Eventually(t, func() bool {
		doc, err = c.GetDocument(ctx, docID)
		return err == nil && doc.Summary != nil
	}, 60*time.Second, 100*time.Millisecond, logTail(home))
	assert.Equal(t, string(store.DocStateFetched), doc.State)
	assert.Equal(t, "article", doc.Summary.Kind)
	assert.Contains(t, doc.Summary.About, summaryWord)
	assert.True(t, doc.Summary.Current)
	assert.Equal(t, []string{"Zymurgy"}, doc.Summary.Entities, "a name the page holds")

	res, err := c.Search(ctx, client.SearchRequest{Query: summaryWord})
	require.NoError(t, err)
	require.NotEmpty(t, res.Items, "the summary list finds the page by a word only its summary says")
	assert.Equal(t, docID, res.Items[0].Document.ID)
	// The lists' counts, which the client doesn't read: the vector lists
	// rank a library's one page first whatever the query, so only the
	// summary list's own count shows it matched.
	hits := searchHits(t, baseURL, summaryWord)
	assert.Equal(t, 1, hits.Summary, "the summary list matched the page")
	assert.Zero(t, hits.BM25+hits.Fields+hits.Title, "no other list matched its words")

	require.Eventually(t, func() bool {
		q, err := c.Queue(ctx)
		if err != nil {
			return false
		}
		for _, k := range q.Kinds {
			if k.Kind == string(store.JobKindSummarize) {
				return k.Pending == 0 && k.Running == 0
			}
		}
		return false
	}, 30*time.Second, 100*time.Millisecond, "the summary pass's queue drains")
	stats, err := c.Stats(ctx)
	require.NoError(t, err)
	require.NotNil(t, stats.Summaries)
	assert.Equal(t, 1, stats.Summaries.Written)
	assert.Zero(t, stats.Summaries.ToWrite)
}

// listHits are a search's counts of the documents each list of words
// matched.
type listHits struct {
	BM25    int `json:"bm25_hits"`
	Fields  int `json:"fields_hits"`
	Title   int `json:"title_hits"`
	Summary int `json:"summary_hits"`
}

// searchHits searches query by its words through the daemon at baseURL,
// and returns the lists' counts from the API's answer.
func searchHits(t *testing.T, baseURL, query string) listHits {
	t.Helper()
	body, err := json.Marshal(map[string]string{"query": query})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/v1/search",
		bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", answer)
	var hits listHits
	require.NoError(t, json.Unmarshal(answer, &hits))
	return hits
}
