//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/store"
)

// interestsTiming is the interest scheduler's timing for these tests
// (CURIO_E2E_INTERESTS, read by e2e builds alone): a check every 200 ms,
// and the library settled after quiet seconds, rather than minutes.
func interestsTiming(settle time.Duration) string {
	return fmt.Sprintf("interval=200ms,settle=%s,max_wait=30s,max_wait_first=30s", settle)
}

// topicWords are what the pages served are about: page n about the n mod
// 5th, so that every 20 pages hold 4 on each topic, enough for the
// grouping to find interests among them.
var topicWords = strings.Fields("astronomy botany chemistry dentistry ecology")

// topicHTML is an article page about topic n, one of topicWords.
func topicHTML(n int) string {
	word := topicWords[n%len(topicWords)]
	var body strings.Builder
	for i := range 5 {
		fmt.Fprintf(&body, "<p>Part %d of a long read on %s. Students of %s learn its history, its methods "+
			"and its open questions, and why %s still matters to anyone curious about the world. This part "+
			"covers one more step in that story, with examples drawn from practice.</p>\n", i+1, word, word, word)
	}
	title := fmt.Sprintf("A Field Guide to %s, Volume %d", word, n)
	return "<!doctype html><html lang=\"en\"><head><title>" + title + "</title></head><body><article><h1>" +
		title + "</h1>\n" + body.String() + "</article></body></html>"
}

// interestsEnv is a daemon on the interest timing, and the topic pages it
// imports.
type interestsEnv struct {
	home  *curiohome.Home
	ctl   *daemonctl.Controller
	c     *client.Client
	base  string // the daemon's URL
	pages string // the topic pages' server's URL
}

// interestsDaemon starts a daemon for a new home on the interest timing,
// serving topic pages at /topic/<n>.
func interestsDaemon(t *testing.T, settle time.Duration) interestsEnv {
	t.Helper()
	env := newInterestsEnv(t, settle)
	require.NoError(t, env.ctl.EnsureRunning(context.Background()), logTail(env.home))
	return env
}

// newInterestsEnv is interestsDaemon's home and pages, with no daemon
// started yet.
func newInterestsEnv(t *testing.T, settle time.Duration) interestsEnv {
	t.Helper()
	t.Setenv("CURIO_E2E_INTERESTS", interestsTiming(settle))
	_, ollamaURL := serveOllama(t, digestA, "0.34.4")
	pages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n int
		if _, err := fmt.Sscanf(r.URL.Path, "/topic/%d", &n); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, topicHTML(n))
	}))
	t.Cleanup(pages.Close)
	listen := freeLoopbackAddr(t)
	home := newHome(t, listen, ollamaURL)
	ctl := daemonctl.New(home, daemonBin, "http://"+listen)
	t.Cleanup(func() {
		if st, err := ctl.Status(context.Background()); err == nil && st.State == daemonctl.Running && st.PID > 0 {
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	})
	return interestsEnv{home: home, ctl: ctl, c: client.New("http://" + listen), base: "http://" + listen, pages: pages.URL}
}

// importTopics imports the topic pages from first to last, inclusive.
func importTopics(t *testing.T, c *client.Client, pages string, first, last int) {
	t.Helper()
	req := client.ImportRequest{Source: "html"}
	for n := first; n <= last; n++ {
		req.Bookmarks = append(req.Bookmarks, client.ImportBookmark{URL: fmt.Sprintf("%s/topic/%d", pages, n)})
	}
	res, err := c.ImportBookmarks(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, last-first+1, res.Created, "%+v", res)
}

// fetchedDocuments counts the library's fetched documents.
func fetchedDocuments(c *client.Client) (int, error) {
	stats, err := c.Stats(context.Background())
	if err != nil {
		return 0, err
	}
	return stats.DocumentsByState[string(store.DocStateFetched)], nil
}

// TestDaemon_AnImportIsGroupedUnasked: an import of 20 pages is grouped
// once it settles, with no rebuild asked for: the first rebuild, which the
// dashboard's interest pages show, its map's page the shell its script
// draws in; and 5 more pages, 5% of 20 at the floor, are regrouped the
// same way.
func TestDaemon_AnImportIsGroupedUnasked(t *testing.T) {
	ctx := context.Background()
	env := interestsDaemon(t, time.Second)
	home, ctl, c, pages := env.home, env.ctl, env.c, env.pages

	importTopics(t, c, pages, 0, 19)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		list, err := c.ListInterests(ctx, client.ListInterestsOpts{})
		require.NoError(collect, err)
		require.NotNil(collect, list.Rebuild)
		assert.Equal(collect, string(store.RunTriggerFirst), list.Rebuild.Trigger)
		assert.Equal(collect, 20, list.NumDocuments)
		health, err := c.Healthz(ctx)
		require.NoError(collect, err)
		require.NotNil(collect, health.Interests)
		assert.Equal(collect, client.StateCurrent, health.Interests.State)
	}, 30*time.Second, 50*time.Millisecond, "the first rebuild, unasked\n%s", logTail(home))
	list, err := c.ListInterests(ctx, client.ListInterestsOpts{})
	require.NoError(t, err)
	require.NotEmpty(t, list.Items)
	for _, path := range []string{"/ui/interests", "/ui/interests/" + list.Items[0].ID, "/ui/interests/unsorted",
		"/ui/interests/changes", "/ui/interests/map"} {
		_, body := getDashboard(t, env.base, path)
		assert.Contains(t, body, `<a href="/ui/interests" aria-current="page">`, path)
	}
	_, body := getDashboard(t, env.base, "/ui/interests/map?view=interests&select=interest%3A"+list.Items[0].ID)
	assert.Contains(t, body, `id="interest-map" data-src="/v1/interests/map"`)
	assert.Contains(t, body, `data-select="interest:`+list.Items[0].ID+`"`)
	// The rebuild drew its map; no client wraps it yet.
	var drawn struct {
		RunID     string `json:"run_id"`
		Documents struct {
			ID []string `json:"id"`
		} `json:"documents"`
	}
	getAPI(t, env.base, "/v1/interests/map", &drawn)
	assert.Equal(t, list.RunID, drawn.RunID)
	assert.Len(t, drawn.Documents.ID, 20)

	importTopics(t, c, pages, 20, 24)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		list, err := c.ListInterests(ctx, client.ListInterestsOpts{})
		require.NoError(collect, err)
		require.NotNil(collect, list.Rebuild)
		assert.Equal(collect, string(store.RunTriggerAuto), list.Rebuild.Trigger)
		assert.Equal(collect, 25, list.NumDocuments)
		assert.Equal(collect, 5, list.Rebuild.ChangedDocuments)
	}, 30*time.Second, 50*time.Millisecond, "the next, after 5 changes\n%s", logTail(home))

	stopped, err := ctl.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
}

// TestDaemon_APausedQueueHoldsTheRebuild: a rebuild the scheduler queues
// while the queue is paused waits, queued, and runs once it is resumed.
func TestDaemon_APausedQueueHoldsTheRebuild(t *testing.T) {
	ctx := context.Background()
	env := interestsDaemon(t, 3*time.Second)
	home, ctl, c, pages := env.home, env.ctl, env.c, env.pages

	importTopics(t, c, pages, 0, 19)
	require.Eventually(t, func() bool {
		n, err := fetchedDocuments(c)
		return err == nil && n == 20
	}, 30*time.Second, 20*time.Millisecond, logTail(home))
	_, err := c.UpdateQueue(ctx, client.QueueUpdate{Paused: new(true)})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		health, err := c.Healthz(ctx)
		require.NoError(collect, err)
		require.NotNil(collect, health.Interests)
		assert.Equal(collect, client.StateQueued, health.Interests.State)
		jobs, err := c.ListJobs(ctx, client.JobListOpts{Kind: string(store.JobKindCluster), Status: string(store.JobStatusPending)})
		require.NoError(collect, err)
		assert.Len(collect, jobs.Items, 1)
	}, 30*time.Second, 50*time.Millisecond, "queued once the library settles\n%s", logTail(home))
	assert.Never(t, func() bool {
		list, err := c.ListInterests(ctx, client.ListInterestsOpts{})
		return err != nil || list.RunID != ""
	}, time.Second, 50*time.Millisecond, "the paused queue holds the rebuild")

	_, err = c.UpdateQueue(ctx, client.QueueUpdate{Paused: new(false)})
	require.NoError(t, err)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		list, err := c.ListInterests(ctx, client.ListInterestsOpts{})
		require.NoError(collect, err)
		assert.NotEmpty(collect, list.RunID)
	}, 30*time.Second, 50*time.Millisecond, "resumed, it runs\n%s", logTail(home))

	stopped, err := ctl.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
}

// TestDaemon_TheMapSwitch: with insight.map off, an import is grouped
// unasked without a map: the map answers 404 map_off and healthz says the
// map is off. Turned on again, the daemon queues a rebuild unasked to draw
// it, and the map answers with every document.
func TestDaemon_TheMapSwitch(t *testing.T) {
	ctx := context.Background()
	env := newInterestsEnv(t, time.Second)
	home, ctl, c := env.home, env.ctl, env.c
	cfg, err := os.ReadFile(home.ConfigPath())
	require.NoError(t, err)
	on := string(cfg)
	off := strings.Replace(on, "insight:\n  labeling: terms\n", "insight:\n  labeling: terms\n  map: false\n", 1)
	require.NotEqual(t, on, off)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(off), 0o600))
	require.NoError(t, ctl.EnsureRunning(ctx), logTail(home))

	importTopics(t, c, env.pages, 0, 19)
	var first string
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		list, err := c.ListInterests(ctx, client.ListInterestsOpts{})
		require.NoError(collect, err)
		require.NotNil(collect, list.Rebuild)
		assert.Equal(collect, 20, list.NumDocuments)
		first = list.RunID
		health, err := c.Healthz(ctx)
		require.NoError(collect, err)
		require.NotNil(collect, health.Interests)
		assert.Equal(collect, client.StateCurrent, health.Interests.State)
		require.NotNil(collect, health.Interests.Map)
		assert.Equal(collect, client.MapOff, health.Interests.Map.Status)
	}, 30*time.Second, 50*time.Millisecond, "grouped without a map\n%s", logTail(home))
	var problem struct {
		Reason string `json:"reason"`
	}
	getAPIStatus(t, env.base, "/v1/interests/map", http.StatusNotFound, &problem)
	assert.Equal(t, "map_off", problem.Reason)
	_, page := getDashboard(t, env.base, "/ui/interests/map")
	assert.Contains(t, page, "<h2>The map is off</h2>", "the page says what the endpoint does")
	assert.NotContains(t, page, `id="interest-map"`)

	stopped, err := ctl.Stop(ctx)
	require.NoError(t, err)
	require.True(t, stopped)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(on), 0o600))
	require.NoError(t, ctl.EnsureRunning(ctx), logTail(home))
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		list, err := c.ListInterests(ctx, client.ListInterestsOpts{})
		require.NoError(collect, err)
		require.NotNil(collect, list.Rebuild)
		assert.NotEqual(collect, first, list.RunID, "rebuilt")
		assert.Equal(collect, string(store.RunTriggerAuto), list.Rebuild.Trigger)
		assert.Zero(collect, list.Rebuild.ChangedDocuments)
		health, err := c.Healthz(ctx)
		require.NoError(collect, err)
		require.NotNil(collect, health.Interests)
		require.NotNil(collect, health.Interests.Map)
		assert.Equal(collect, client.MapBuilt, health.Interests.Map.Status)
	}, 30*time.Second, 50*time.Millisecond, "the map on again, a rebuild draws it unasked\n%s", logTail(home))
	var drawn struct {
		Documents struct {
			ID []string `json:"id"`
		} `json:"documents"`
	}
	getAPI(t, env.base, "/v1/interests/map", &drawn)
	assert.Len(t, drawn.Documents.ID, 20)

	stopped, err = ctl.Stop(ctx)
	require.NoError(t, err)
	assert.True(t, stopped)
}

// getAPI gets path from the daemon at baseURL, which must answer 200, into
// v.
func getAPI(t *testing.T, baseURL, path string, v any) {
	t.Helper()
	getAPIStatus(t, baseURL, path, http.StatusOK, v)
}

// getAPIStatus gets path from the daemon at baseURL, which must answer
// status, into v.
func getAPIStatus(t *testing.T, baseURL, path string, status int, v any) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, status, resp.StatusCode, "%s: %s", path, body)
	require.NoError(t, json.Unmarshal(body, v), path)
}
