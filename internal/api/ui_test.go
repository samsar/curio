package api_test

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xhtml "golang.org/x/net/html"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/drift"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
	"github.com/samsar/curio/internal/ui/uitest"
)

// These tests drive the dashboard's pages over the real API (apitest): each
// view's status, type, CSP and content, and that every page is inert.

// noRedirects answers with a redirect instead of following it.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

// page is one answer from the dashboard.
type page struct {
	status int
	header http.Header
	body   string
}

func get(t *testing.T, srv *apitest.Server, path string) page {
	t.Helper()
	return getWith(t, srv, path, nil)
}

// getWith is get with more request headers.
func getWith(t *testing.T, srv *apitest.Server, path string, header http.Header) page {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	maps.Copy(req.Header, header)
	resp, err := noRedirects.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return page{status: resp.StatusCode, header: resp.Header, body: string(body)}
}

// getPage gets the page at path, checks it answered status as inert HTML
// with csp, and returns its body.
func getPageCSP(t *testing.T, srv *apitest.Server, path string, status int, csp string) string {
	t.Helper()
	p := get(t, srv, path)
	require.Equal(t, status, p.status, "%s: %s", path, p.body)
	assert.Equal(t, "text/html; charset=utf-8", p.header.Get("Content-Type"), path)
	assert.Equal(t, csp, p.header.Get("Content-Security-Policy"), path)
	uitest.AssertInert(t, p.body)
	return p.body
}

func getPage(t *testing.T, srv *apitest.Server, path string, status int) string {
	t.Helper()
	return getPageCSP(t, srv, path, status, ui.CSP)
}

// logRecorder keeps every record logged, for asserting what the pages log.
type logRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *logRecorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logRecorder) WithGroup(string) slog.Handler      { return h }

// errors returns the attributes of each error record.
func (h *logRecorder) errors() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]any
	for _, r := range h.records {
		if r.Level != slog.LevelError {
			continue
		}
		attrs := map[string]any{"msg": r.Message}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Resolve().Any()
			return true
		})
		out = append(out, attrs)
	}
	return out
}

// titled creates a document with title, in state.
func titled(t *testing.T, srv *apitest.Server, url, title string, state store.DocState) *store.Document {
	t.Helper()
	doc := srv.AddDocument(t, url, state)
	_, err := srv.DB.Exec(`UPDATE documents SET title = ? WHERE id = ?`, title, doc.ID)
	require.NoError(t, err)
	doc.Title = &title
	return doc
}

// bookmark saves a bookmark of url from source in folder.
func bookmark(t *testing.T, srv *apitest.Server, url, source, folder string, tags ...string) *store.Bookmark {
	t.Helper()
	b := &store.Bookmark{TenantID: apitest.TenantID, URL: url, Source: source, SavedAt: time.Now().UTC(),
		FolderPath: store.NullableString(folder), Tags: tags}
	_, err := srv.Deps.Bookmarks.Ingest(context.Background(), b)
	require.NoError(t, err)
	return b
}

// save ingests b, a bookmark of the test tenant, saved now unless it says
// when.
func save(t *testing.T, srv *apitest.Server, b store.Bookmark) *store.Bookmark {
	t.Helper()
	b.TenantID = apitest.TenantID
	if b.SavedAt.IsZero() {
		b.SavedAt = time.Now().UTC()
	}
	_, err := srv.Deps.Bookmarks.Ingest(context.Background(), &b)
	require.NoError(t, err)
	return &b
}

// failJob records a failed fetch job for doc whose error is msg.
func failJob(t *testing.T, srv *apitest.Server, doc *store.Document, msg string) {
	t.Helper()
	job, err := store.NewDocumentJob(apitest.TenantID, store.JobKindFetch, doc.ID)
	require.NoError(t, err)
	job.Status = store.JobStatusFailed
	require.NoError(t, srv.Deps.Queue.Enqueue(context.Background(), job))
	_, err = srv.DB.Exec(`UPDATE jobs SET last_error = ? WHERE id = ?`, msg, job.ID)
	require.NoError(t, err)
}

func TestUI_Root(t *testing.T) {
	srv := apitest.Start(t)
	p := get(t, srv, "/")
	assert.Equal(t, http.StatusFound, p.status)
	assert.Equal(t, "/ui/", p.header.Get("Location"))
	assert.Equal(t, ui.CSP, p.header.Get("Content-Security-Policy"))

	body := getPage(t, srv, "/ui", http.StatusOK)
	assert.Contains(t, body, `<h1 class="visually-hidden">Search</h1>`, "/ui is /ui/, the search home")
	assert.Contains(t, body, "<span>"+strings.TrimPrefix(srv.URL, "http://")+" · this Mac only</span>",
		"the footer names where the daemon listens")
}

// TestUI_SearchRedirect: the search page's old address answers with the
// home and its query as sent, never another site.
func TestUI_SearchRedirect(t *testing.T) {
	srv := apitest.Start(t)
	for query, location := range map[string]string{
		"":                                    "/ui/",
		"?q=kafka&content_type=pdf":           "/ui/?q=kafka&content_type=pdf",
		"?q=%3Cscript%3E&next=//evil.example": "/ui/?q=%3Cscript%3E&next=//evil.example",
		"?page=2&q=a+b":                       "/ui/?page=2&q=a+b",
	} {
		p := get(t, srv, "/ui/search"+query)
		assert.Equal(t, http.StatusFound, p.status, query)
		assert.Equal(t, location, p.header.Get("Location"), query)
		assert.Equal(t, ui.CSP, p.header.Get("Content-Security-Policy"), query)
	}
}

func TestUI_Status(t *testing.T) {
	srv := apitest.Start(t)
	fetched := titled(t, srv, "https://example.com/fetched", "A fetched page", store.DocStateFetched)
	failed := srv.AddDocument(t, "https://example.com/failed", store.DocStateFailed)
	failJob(t, srv, failed, "HTTP 503")
	bookmark(t, srv, fetched.URL, store.SourceChrome, "/Reading")
	bookmark(t, srv, "https://example.com/new", store.SourceSafari, "")

	body := getPage(t, srv, "/ui/status", http.StatusOK)
	assert.Contains(t, body, "<title>Status · curio</title>")
	assert.Contains(t, body, "<h1>Status</h1>")
	assert.Contains(t, body, `<a href="/ui/status" aria-current="page">`)
	assert.Contains(t, body, `<span class="label">Documents</span><span class="value">3</span>`)
	assert.Contains(t, body, `<span class="label">Fetched</span><span class="value">1 <small>33%</small></span>`)
	assert.Contains(t, body, `<span class="label">Bookmarks</span><span class="value">2</span>`)
	for i, state := range []string{"pending", "fetched", "failed", "dead"} {
		assert.Contains(t, body, fmt.Sprintf(`<a id="legend-%d" href="/ui/library?state=%s">`, i, state))
	}
	// The state bar's proportions are attributes: a third each of pending,
	// fetched and failed.
	assert.Contains(t, body, `<rect class="fill-warn" x="0.000" y="0" width="33.333" height="10"/>`+
		`<rect class="fill-ok" x="33.333" y="0" width="33.334" height="10"/>`+
		`<rect class="fill-danger" x="66.667" y="0" width="33.333" height="10"/>`)
	assert.NotContains(t, body, "style=")
	assert.Contains(t, body, `<span class="badge queue-open">open</span>`)
	assert.Contains(t, body, `<th scope="col" class="num">Finished, 10m</th>`)
	assert.Contains(t, body, `<td>fetch</td><td class="num">0 of 16</td><td class="num">1</td><td class="num">0</td>`,
		"the new page's fetch job")
	assert.Contains(t, body, `<span class="why">Working: 1 job waiting</span>`)
	assert.Contains(t, body, `id="queue-pause" data-kind="pause"`)
	assert.Contains(t, body, "1 job queued, and none finished in the last 10m.")
	assert.Contains(t, body, `qwen3-embedding:0.6b<span class="sub">1024 dimensions</span>`)
	assert.NotContains(t, body, "Recently saved")
	assert.NotContains(t, body, `class="callout`, "a healthy daemon needs no attention")
	assert.Contains(t, body, `<a class="more" href="/ui/failures">All failures →</a>`,
		"the Failures tab, which counts the dead links the card counts too")

	// Paused: the queue and the progress say why nothing starts, and the
	// button resumes.
	_, err := srv.Deps.Gate.Update(context.Background(), jobsPause())
	require.NoError(t, err)
	body = getPage(t, srv, "/ui/status", http.StatusOK)
	assert.Contains(t, body, `<span class="badge queue-closed">closed</span><span class="why">Paused</span>`)
	assert.Contains(t, body, "1 job queued. Paused: none start until the queue opens.")
	assert.Contains(t, body, `id="queue-pause" data-kind="resume"`)
}

// failingCount fails the bookmark count, which stats reads.
type failingCount struct{ store.BookmarkStore }

var errInjected = errors.New("injected failure")

func (failingCount) Count(context.Context, string) (int, error) {
	return 0, fmt.Errorf("count bookmarks: %w", errInjected)
}

// failingSummary fails the failure summary, which Status's failures card
// reads.
type failingSummary struct{ store.DocumentStore }

func (failingSummary) FailureSummary(context.Context, string, int) (store.FailureSummary, error) {
	return store.FailureSummary{}, fmt.Errorf("summarize failures: %w", errInjected)
}

// TestUI_StatusPanelsDegrade: a panel whose read fails shows its error
// and request ID, logged once, and the rest of the page still renders:
// the counts' failure in the Library and Jobs cards, the summary's in the
// failures card.
func TestUI_StatusPanelsDegrade(t *testing.T) {
	var rec logRecorder
	srv := apitest.Start(t, func(d *api.Deps) {
		d.Bookmarks = failingCount{d.Bookmarks}
		d.Documents = failingSummary{d.Documents}
		d.Log = slog.New(&rec)
	})
	p := get(t, srv, "/ui/status")
	require.Equal(t, http.StatusOK, p.status)
	uitest.AssertInert(t, p.body)
	id := p.header.Get("X-Request-Id")
	assert.Equal(t, 2, strings.Count(p.body, "Couldn't read this: count bookmarks: injected failure."),
		"the Library and Jobs cards")
	assert.Contains(t, p.body, "Why documents failed")
	assert.Equal(t, 1, strings.Count(p.body, "Couldn't read this: summarize failures: injected failure."))
	assert.Contains(t, p.body, "Request "+id)
	assert.Contains(t, p.body, `<span class="badge queue-open">open</span>`, "the queue panel renders")
	assert.Contains(t, p.body, "qwen3-embedding:0.6b", "the health panel renders")
	assert.Contains(t, p.body, "<p>Nothing queued.</p>", "the progress panel renders")

	errs := rec.errors()
	require.Len(t, errs, 2, "each failed read logged once")
	for _, e := range errs {
		assert.Equal(t, id, e["request_id"])
		assert.ErrorIs(t, e["err"].(error), errInjected)
	}
}

// TestUI_StatusAttention: what needs attention opens Status, before the
// board: the drift, then a failing Jina Reader, whose copy never calls a
// refusal a failure.
func TestUI_StatusAttention(t *testing.T) {
	now := time.Now()
	srv := apitest.Start(t, func(d *api.Deps) {
		d.Drift = apitest.NewDrift(now, drift.Change{What: "model digest", Recorded: "sha256:aaa", Current: "sha256:bbb"})
		d.Upstreams = func() []fetcher.UpstreamHealth {
			return []fetcher.UpstreamHealth{{Name: "jina", Enabled: true, State: fetcher.UpstreamFailing,
				LastSuccess: now.Add(-40 * time.Minute), LastFailure: now.Add(-time.Minute),
				LastFailureClass: fetcher.CallChallenged, Window: 15 * time.Minute,
				Recent: map[fetcher.CallClass]int{fetcher.CallChallenged: 6, fetcher.CallRefused: 2}}}
		}
	})
	body := getPage(t, srv, "/ui/status", http.StatusOK)
	board := strings.Index(body, `<div class="board">`)
	drifted := strings.Index(body, `<p class="callout-title">The embeddings drifted</p>`)
	failing := strings.Index(body, `<p class="callout-title">Jina Reader is failing</p>`)
	require.Positive(t, drifted)
	require.Positive(t, failing)
	assert.Less(t, drifted, failing, "the drift first")
	assert.Less(t, failing, board, "before the board")
	assert.Contains(t, body, "<li>model digest: sha256:aaa → sha256:bbb</li>")
	assert.Contains(t, body, "for more than one site, since its last good answer, <time")
	assert.Contains(t, body, "The last failure was challenged, <time")
	assert.NotContains(t, body, "refus")
	assert.NotContains(t, body, "Ollama isn't ready", "the apitest embedder has no Ollama to ping")
}

// TestUI_StatusDegraded: a degraded Jina Reader's callout counts its calls
// in the window its health reports, as its health does: an ok, judged or
// refused call is a healthy answer, and each other class is a failure.
func TestUI_StatusDegraded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window time.Duration
		recent map[fetcher.CallClass]int
		want   string
	}{
		{"refusals are answers", 15 * time.Minute, map[fetcher.CallClass]int{fetcher.CallOK: 10, fetcher.CallJudged: 3,
			fetcher.CallRefused: 5, fetcher.CallRateLimited: 4, fetcher.CallNetwork: 2},
			"<p>6 of its 24 calls in the last 15m failed."},
		{"every failure class", 30 * time.Minute, map[fetcher.CallClass]int{fetcher.CallRefused: 7,
			fetcher.CallChallenged: 1, fetcher.CallForbidden: 1, fetcher.CallAuth: 1, fetcher.CallServerError: 1},
			"<p>4 of its 11 calls in the last 30m failed."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			srv := apitest.Start(t, func(d *api.Deps) {
				d.Upstreams = func() []fetcher.UpstreamHealth {
					return []fetcher.UpstreamHealth{{Name: "jina", Enabled: true, State: fetcher.UpstreamDegraded,
						LastSuccess: now.Add(-time.Minute), LastFailure: now.Add(-2 * time.Minute),
						LastFailureClass: fetcher.CallNetwork, Window: tc.window, Recent: tc.recent}}
				}
			})
			body := getPage(t, srv, "/ui/status", http.StatusOK)
			degraded := strings.Index(body, `<p class="callout-title">Jina Reader is degraded</p>`)
			require.Positive(t, degraded)
			assert.Less(t, degraded, strings.Index(body, `<div class="board">`), "before the board")
			assert.Contains(t, body, tc.want)
		})
	}
}

// TestUI_StatusFailures: the causes with the most failed and dead
// documents, most first, each leading to its card on the Failures tab,
// with a bar scaled to the most; the card shows five, and leads to the
// tab.
func TestUI_StatusFailures(t *testing.T) {
	srv := apitest.Start(t)
	for i := range 3 {
		srv.AddFailedDocument(t, fmt.Sprintf("https://blocked.example/%d", i), store.FailureCauseAntiBot)
	}
	srv.AddFailedDocument(t, "https://gone.example/a", store.FailureCauseDeadLink)

	body := getPage(t, srv, "/ui/status", http.StatusOK)
	assert.Contains(t, body, `<h2 id="failures">`)
	assert.Contains(t, body, `<a class="more" href="/ui/failures">All failures →</a>`)
	blocked := `<li><a class="label" href="/ui/failures#cause-anti_bot" title="Blocked by bot protection">Blocked by bot protection</a>` +
		`<svg class="stackbar" viewBox="0 0 100 10" preserveAspectRatio="none" aria-hidden="true"><rect class="fill-track" x="0" y="0" width="100" height="10"/>` +
		`<rect class="fill-danger" x="0.000" y="0" width="100.000" height="10"/></svg><span class="n">3</span></li>`
	dead := `<li><a class="label" href="/ui/failures#cause-dead_link" title="Dead link">Dead link</a>` +
		`<svg class="stackbar" viewBox="0 0 100 10" preserveAspectRatio="none" aria-hidden="true"><rect class="fill-track" x="0" y="0" width="100" height="10"/>` +
		`<rect class="fill-neutral" x="0.000" y="0" width="33.333" height="10"/></svg><span class="n">1</span></li>`
	assert.Contains(t, body, blocked)
	assert.Contains(t, body, dead)
	assert.Less(t, strings.Index(body, blocked), strings.Index(body, dead), "the most first")

	for cause, n := range map[store.FailureCause]int{store.FailureCauseLoginWall: 6, store.FailureCauseTLS: 5,
		store.FailureCauseUnreachable: 4, store.FailureCauseTimeout: 2} {
		for i := range n {
			srv.AddFailedDocument(t, fmt.Sprintf("https://%s.example/%d", cause, i), cause)
		}
	}
	body = getPage(t, srv, "/ui/status", http.StatusOK)
	assert.Equal(t, 5, strings.Count(body, `<a class="label" href="/ui/failures#cause-`), "five causes")
	assert.Contains(t, body, `<rect class="fill-danger" x="0.000" y="0" width="100.000" height="10"/></svg><span class="n">6</span>`,
		"scaled to the most")
	assert.NotContains(t, body, "cause-dead_link", "the sixth isn't drawn")
}

// failingEmbedder can't embed a query, as when Ollama is down.
type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("ollama unreachable")
}

func TestUI_Search(t *testing.T) {
	srv := apitest.Start(t)
	doc := titled(t, srv, "https://example.com/kafka", "Kafka partitions", store.DocStateFetched)
	srv.AddContent(t, doc, "kafka partitions and <script>alert(1)</script> consumer groups")
	_, err := srv.DB.Exec(`UPDATE documents SET content_type = 'article' WHERE id = ?`, doc.ID)
	require.NoError(t, err)

	form := getPage(t, srv, "/ui/", http.StatusOK)
	assert.Contains(t, form, `<form class="search-hero" action="/ui/" method="get" role="search">`)
	assert.Contains(t, form, `hx-get="/ui/" hx-trigger="input changed delay:400ms, search" hx-sync="this:replace"`+
		` hx-include="closest form"`)
	assert.Contains(t, form, `hx-target="#results" hx-select="#results > *" hx-select-oob="#search-scope" hx-swap="innerHTML"`)
	assert.Contains(t, form, `hx-push-url="true"`)
	assert.Contains(t, form, `hx-indicator="#searching"`)
	assert.NotContains(t, form, `<ol class="results">`)
	assert.NotContains(t, form, `class="header-search"`, "the page is its own search box")
	assert.Contains(t, form, `<button class="btn btn-primary" type="submit" aria-label="Search">`,
		"named where a phone shows only its icon")
	assert.Contains(t, form, `<div id="results" aria-live="polite">`+"\n"+`<div class="landing">`)

	body := getPage(t, srv, "/ui/?q=kafka", http.StatusOK)
	assert.Contains(t, body, `value="kafka"`)
	assert.Contains(t, body, `<div id="results" aria-live="polite">`+"\n"+`<div class="results">`)
	assert.Contains(t, body, `placeholder="Search your library"`)
	assert.NotContains(t, body, `class="landing"`)
	assert.Contains(t, body, `<a href="/ui/documents/`+doc.ID+`" title="Kafka partitions">Kafka partitions</a>`)
	assert.Contains(t, body, `<span class="path" title="https://example.com/kafka"><b>example.com</b> › kafka</span>`)
	assert.NotContains(t, body, `rel="noopener noreferrer"`, "results link to their document's page, not out")
	assert.Contains(t, body, "<mark>kafka</mark>")
	assert.Contains(t, body, `<div class="result-foot"><span class="tag">article</span>`, "the hit's type")
	assert.Contains(t, body, "<strong>1 document</strong>")
	assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;", "the chunk's markup is text")
	assert.Regexp(t, `bm25 \d+\.\d{3}`, body)
	assert.Regexp(t, `vector \d+\.\d{3}`, body)
	assert.NotContains(t, body, "semantic search unavailable")

	// htmx asks for the same page a plain GET gets, and selects its results
	// and, out of band, the type links, which carry the query typed.
	took := regexp.MustCompile(`· \d+ ms`)
	htmx := getWith(t, srv, "/ui/?q=kafka", http.Header{"Hx-Request": {"true"}, "Hx-Current-Url": {srv.URL + "/ui/"}})
	require.Equal(t, http.StatusOK, htmx.status)
	assert.Equal(t, took.ReplaceAllString(body, ""), took.ReplaceAllString(htmx.body, ""))
	assert.Contains(t, htmx.body, `<div class="search-scope" id="search-scope">`)
	assert.Contains(t, htmx.body, `<a href="/ui/?content_type=pdf&amp;q=kafka">PDFs</a>`)

	assert.Contains(t, getPage(t, apitest.Start(t), "/ui/?q=kafka", http.StatusOK),
		"<h2>Nothing in your library matches</h2>")
}

// TestUI_SearchDegraded: without semantic search the page shows the
// keyword results under the engine's warning.
func TestUI_SearchDegraded(t *testing.T) {
	srv := apitest.Start(t, func(d *api.Deps) {
		d.Search = search.New(d.Chunks, d.Documents, failingEmbedder{}, search.Config{Log: slog.New(slog.DiscardHandler)})
	})
	doc := titled(t, srv, "https://example.com/kafka", "Kafka partitions", store.DocStateFetched)
	srv.AddContent(t, doc, "kafka partitions")

	body := getPage(t, srv, "/ui/?q=kafka", http.StatusOK)
	assert.Contains(t, body, `<p class="callout-title">Keyword results only</p>`+"\n"+
		`<div class="callout-body"><p>semantic search unavailable (`)
	assert.Contains(t, body, "Kafka partitions")
}

// countingEmbedder counts the queries the search engine embeds.
type countingEmbedder struct {
	apitest.Embedder
	calls *int
	mu    *sync.Mutex
}

func (e countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	*e.calls++
	e.mu.Unlock()
	return e.Embedder.Embed(ctx, texts)
}

// TestUI_SearchEmptyQuery: without a query the page is the home, and the
// engine isn't asked.
func TestUI_SearchEmptyQuery(t *testing.T) {
	calls, mu := 0, new(sync.Mutex)
	srv := apitest.Start(t, func(d *api.Deps) {
		emb := countingEmbedder{Embedder: apitest.Embedder{Dim: config.Default().Embedding.Dim}, calls: &calls, mu: mu}
		d.Search = search.New(d.Chunks, d.Documents, emb, search.Config{Log: slog.New(slog.DiscardHandler)})
	})
	for _, q := range []string{"", "?q=", "?q=%20%20", "?content_type=video"} {
		assert.Contains(t, getPage(t, srv, "/ui/"+q, http.StatusOK), `<div class="landing">`, q)
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, calls)
}

// countingStates counts the reads of the documents by state, which stats
// makes.
type countingStates struct {
	store.DocumentStore
	calls *atomic.Int32
}

func (c countingStates) CountByState(ctx context.Context, tenantID string) (map[store.DocState]int, error) {
	c.calls.Add(1)
	return c.DocumentStore.CountByState(ctx, tenantID)
}

// countingRuns counts the reads of the latest clustering run, which the
// interests read makes, and of any interest's members.
type countingRuns struct {
	store.InsightStore
	calls, members *atomic.Int32
}

func (c countingRuns) LatestRun(ctx context.Context, tenantID string, status store.ClusterRunStatus) (*store.ClusterRun, error) {
	c.calls.Add(1)
	return c.InsightStore.LatestRun(ctx, tenantID, status)
}

func (c countingRuns) ClusterMembers(ctx context.Context, clusterID string, limit, offset int) ([]store.ClusterMember, error) {
	c.members.Add(1)
	return c.InsightStore.ClusterMembers(ctx, clusterID, limit, offset)
}

// failingRuns fails the latest clustering run's read.
type failingRuns struct{ store.InsightStore }

func (failingRuns) LatestRun(context.Context, string, store.ClusterRunStatus) (*store.ClusterRun, error) {
	return nil, fmt.Errorf("latest run: %w", errInjected)
}

// TestUI_SearchHomeReadsOnlyWithoutAQuery: search as you type renders the
// whole page on every keystroke, so with a query the page reads the search
// alone, htmx's request as a plain GET; the home's reads, the counts and
// the interests, run once each without one, and neither runs for a type
// the page refuses.
func TestUI_SearchHomeReadsOnlyWithoutAQuery(t *testing.T) {
	states, runs, members := new(atomic.Int32), new(atomic.Int32), new(atomic.Int32)
	srv := apitest.Start(t, func(d *api.Deps) {
		d.Documents = countingStates{d.Documents, states}
		d.Insights = countingRuns{d.Insights, runs, members}
	})
	srv.AddInterest(t, "Kafka", srv.AddDocument(t, "https://example.com/kafka", store.DocStateFetched))
	for _, tc := range []struct {
		path   string
		header http.Header
		status int
		reads  int32
	}{
		{"/ui/?q=kafka", nil, http.StatusOK, 0},
		{"/ui/?q=kafka", http.Header{"Hx-Request": {"true"}}, http.StatusOK, 0},
		{"/ui/?q=kafka&content_type=pdf", nil, http.StatusOK, 0},
		{"/ui/", nil, http.StatusOK, 1},
		{"/ui/?q=", nil, http.StatusOK, 1},
		{"/ui/?q=%20%20", nil, http.StatusOK, 1},
		{"/ui/?q=", http.Header{"Hx-Request": {"true"}}, http.StatusOK, 1},
		{"/ui/?content_type=bogus", nil, http.StatusBadRequest, 0},
	} {
		states.Store(0)
		runs.Store(0)
		p := getWith(t, srv, tc.path, tc.header)
		require.Equal(t, tc.status, p.status, tc.path)
		assert.Equal(t, tc.reads, states.Load(), "%s: the documents by state", tc.path)
		assert.Equal(t, tc.reads, runs.Load(), "%s: the latest run", tc.path)
		assert.Zero(t, members.Load(), "%s: the landing names interests, never reads their members", tc.path)
	}
}

// TestUI_SearchHome: without a query, the landing names the six largest
// interests, largest first, with a link to all of them, and the box says
// how many documents there are to search.
func TestUI_SearchHome(t *testing.T) {
	srv := apitest.Start(t)
	body := getPage(t, srv, "/ui/", http.StatusOK)
	assert.Contains(t, body, `placeholder="Search your library"`, "nothing fetched")
	assert.Contains(t, body, `<label class="visually-hidden" for="q">Search your library</label>`)
	assert.Contains(t, body, `<div class="landing">`)
	assert.NotContains(t, body, "explore-line", "no run yet")

	empty := apitest.Start(t)
	empty.AddEmptyClusterRun(t, 5)
	body = getPage(t, empty, "/ui/", http.StatusOK)
	assert.Contains(t, body, `<div class="landing">`)
	assert.NotContains(t, body, "explore-line", "a run without interests")

	in := srv.AddInterests(t, apitest.Interest{Label: "Smallest topic", Size: 2},
		apitest.Interest{Label: "Mobile Ecosystems and Strategy", Size: 90}, apitest.Interest{Size: 50},
		apitest.Interest{Label: "Identity and Access Management", Size: 70}, apitest.Interest{Label: "Rust", Size: 30},
		apitest.Interest{Label: "Kafka <streams>", Size: 60}, apitest.Interest{Label: "Go", Size: 40})
	body = getPage(t, srv, "/ui/", http.StatusOK)
	links := regexp.MustCompile(`<li><a href="/ui/interests/([^"]+)"`).FindAllStringSubmatch(body, -1)
	order := make([]string, 0, len(links))
	for _, m := range links {
		order = append(order, m[1])
	}
	assert.Equal(t, []string{in[1].ID, in[3].ID, in[5].ID, in[2].ID, in[6].ID, in[4].ID}, order, "the six largest, largest first")
	assert.Contains(t, body, `<nav class="explore-line" aria-label="Your interests"><span class="label">Your interests</span><ul>`)
	assert.Contains(t, body, `<li><a href="/ui/interests/`+in[1].ID+`" title="Mobile Ecosystems and Strategy">Mobile Ecosystems</a></li>`)
	assert.Contains(t, body, `title="Identity and Access Management">Identity and Access Management</a></li>`)
	assert.Contains(t, body, `<li><a href="/ui/interests/`+in[2].ID+`"><span class="unlabeled">Unlabeled interest</span></a></li>`)
	assert.Contains(t, body, `title="Kafka &lt;streams&gt;">Kafka &lt;streams&gt;</a>`)
	assert.Contains(t, body, `<a class="all" href="/ui/interests">All 7 →</a>`)
	assert.NotContains(t, body, "Smallest topic")

	titled(t, srv, "https://example.com/a", "A", store.DocStateFetched)
	srv.AddDocument(t, "https://example.com/pending", store.DocStatePending)
	assert.Contains(t, getPage(t, srv, "/ui/", http.StatusOK), `placeholder="Search your 1 document"`)
	srv.AddDocument(t, "https://example.com/b", store.DocStateFetched)
	assert.Contains(t, getPage(t, srv, "/ui/", http.StatusOK), `placeholder="Search your 2 documents"`)
}

// TestUI_SearchHomeDegrades: the home does without a read that fails: the
// page answers, leaving out what the read was for, and the failure is
// logged once.
func TestUI_SearchHomeDegrades(t *testing.T) {
	for name, fail := range map[string]func(*api.Deps){
		"interests": func(d *api.Deps) { d.Insights = failingRuns{d.Insights} },
		"counts":    func(d *api.Deps) { d.Bookmarks = failingCount{d.Bookmarks} },
	} {
		t.Run(name, func(t *testing.T) {
			var rec logRecorder
			srv := apitest.Start(t, fail, func(d *api.Deps) { d.Log = slog.New(&rec) })
			titled(t, srv, "https://example.com/a", "A", store.DocStateFetched)
			srv.AddInterest(t, "Kafka")

			p := get(t, srv, "/ui/")
			require.Equal(t, http.StatusOK, p.status)
			uitest.AssertInert(t, p.body)
			assert.Contains(t, p.body, `<div class="landing">`)
			assert.NotContains(t, p.body, "panel-error")
			if name == "interests" {
				assert.NotContains(t, p.body, "explore-line")
				assert.Contains(t, p.body, `placeholder="Search your 1 document"`)
			} else {
				assert.Contains(t, p.body, "explore-line")
				assert.Contains(t, p.body, `placeholder="Search your library"`)
			}
			errs := rec.errors()
			require.Len(t, errs, 1)
			assert.Equal(t, "request failed", errs[0]["msg"])
			assert.Equal(t, p.header.Get("X-Request-Id"), errs[0]["request_id"])
			assert.ErrorIs(t, errs[0]["err"].(error), errInjected)
		})
	}
}

// TestUI_SearchType: a type limits the search, and the page keeps it: the
// chosen tab is marked, and the form carries it for the next search. A
// type the page doesn't offer still limits it; one that isn't a type is a
// 400 page that starts over from the home, and no search runs.
func TestUI_SearchType(t *testing.T) {
	embeds, mu := 0, new(sync.Mutex)
	srv := apitest.Start(t, func(d *api.Deps) {
		emb := countingEmbedder{Embedder: apitest.Embedder{Dim: config.Default().Embedding.Dim}, calls: &embeds, mu: mu}
		d.Search = search.New(d.Chunks, d.Documents, emb, search.Config{Log: slog.New(slog.DiscardHandler)})
	})
	article := titled(t, srv, "https://example.com/article", "Kafka article", store.DocStateFetched)
	srv.AddContent(t, article, "kafka partitions explained")
	video := titled(t, srv, "https://example.com/video", "Kafka video", store.DocStateFetched)
	srv.AddContent(t, video, "kafka partitions on video")
	_, err := srv.DB.Exec(`UPDATE documents SET content_type = CASE id WHEN ? THEN 'article' ELSE 'video' END
		WHERE id IN (?, ?)`, article.ID, article.ID, video.ID)
	require.NoError(t, err)

	body := getPage(t, srv, "/ui/?q=kafka&content_type=video", http.StatusOK)
	assert.Contains(t, body, `href="/ui/documents/`+video.ID+`"`)
	assert.NotContains(t, body, article.ID)
	assert.Contains(t, body, `<a href="/ui/?content_type=video&amp;q=kafka" aria-current="page">Videos</a>`)
	assert.Contains(t, body, `<a href="/ui/?q=kafka">All</a>`)
	assert.Contains(t, body, `<input type="hidden" name="content_type" value="video">`)

	all := getPage(t, srv, "/ui/?q=kafka", http.StatusOK)
	assert.Contains(t, all, article.ID)
	assert.Contains(t, all, video.ID)
	assert.Contains(t, all, `<a href="/ui/?q=kafka" aria-current="page">All</a>`)
	assert.NotContains(t, all, `name="content_type"`)

	thread := getPage(t, srv, "/ui/?q=kafka&content_type=thread", http.StatusOK)
	assert.Contains(t, thread, "<h2>Nothing in your library matches</h2>")
	assert.NotRegexp(t, `aria-current="page">(All|Articles|Repos|Videos|PDFs)<`, thread, "a type the page doesn't offer")
	assert.Contains(t, thread, `<input type="hidden" name="content_type" value="thread">`)

	home := getPage(t, srv, "/ui/?content_type=pdf", http.StatusOK)
	assert.Contains(t, home, `<div class="landing">`)
	assert.Contains(t, home, `<a href="/ui/?content_type=pdf" aria-current="page">PDFs</a>`)

	mu.Lock()
	searched := embeds
	mu.Unlock()
	require.Positive(t, searched, "the searches above embed their query")
	for _, path := range []string{"/ui/?q=kafka&content_type=bogus", "/ui/?content_type=bogus"} {
		body := getPage(t, srv, path, http.StatusBadRequest)
		assert.Contains(t, body, `<div class="big-code">400</div>`+"\n<h1>bad request</h1>", path)
		assert.Contains(t, body, `must be one of: article, repo, video, pdf, thread, unknown`, path)
		assert.Contains(t, body, `<a class="btn btn-primary" href="/ui/">Start over</a>`, path)
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, searched, embeds, "a type that isn't one runs no search")
}

// moreRE finds the Library's next-page link.
var moreRE = regexp.MustCompile(`<div class="load-more" id="more"><a class="btn" href="([^"]+)"`)

// docLinkRE finds the documents a list links to, by their names.
var docLinkRE = regexp.MustCompile(`<a class="doc-title(?: untitled| from-bookmark)?" href="/ui/documents/([^"]+)"`)

// assetRE finds the first asset a page loads.
var assetRE = regexp.MustCompile(`(?:href|src)="(/ui/static/[^"]+)"`)

func TestUI_Library(t *testing.T) {
	srv := apitest.Start(t)
	paper := titled(t, srv, "https://arxiv.example/paper", "A paper", store.DocStateFetched)
	failedDoc := srv.AddDocument(t, "https://arxiv.example/failed", store.DocStateFailed)
	failJob(t, srv, failedDoc, "HTTP 503 from the origin")
	other := srv.AddDocument(t, "https://other.example/x", store.DocStateFetched)
	_, err := srv.DB.Exec(`UPDATE documents SET content_type = 'pdf' WHERE id IN (?, ?)`, paper.ID, failedDoc.ID)
	require.NoError(t, err)
	bookmark(t, srv, paper.URL, store.SourceChrome, "/Research/ML")
	save(t, srv, store.Bookmark{URL: failedDoc.URL, Title: new(" A failed <paper> "), Source: store.SourceSafari})

	all := getPage(t, srv, "/ui/library", http.StatusOK)
	for _, doc := range []*store.Document{paper, failedDoc, other} {
		assert.Contains(t, all, `href="/ui/documents/`+doc.ID+`"`)
	}
	assert.Contains(t, all, `<span class="doc-error" title="HTTP 503 from the origin"><span class="err-cause">Other</span> `+
		`<span class="msg">HTTP 503 from the origin</span></span>`)
	assert.Contains(t, all, `<a class="doc-title from-bookmark" href="/ui/documents/`+failedDoc.ID+
		`" title="A failed &lt;paper&gt;">A failed &lt;paper&gt;</a>`+"\n"+
		`<span class="doc-sub"><span class="host" title="https://arxiv.example/failed">arxiv.example/failed</span>`,
		"an untitled document by its bookmark's title, its short URL under it")
	assert.Contains(t, all, `<a class="doc-title untitled" href="/ui/documents/`+other.ID+
		`" title="https://other.example/x">other.example/x</a>`+"\n"+
		`<span class="doc-sub"><span class="host" title="https://other.example/x">other.example</span>`,
		"one without a titled bookmark by its address, its host under it")
	assert.Contains(t, all, `<a href="/ui/library" aria-current="page">All <span class="count">3</span></a>`)
	assert.Contains(t, all, `<a href="/ui/library?state=fetched">Fetched <span class="count">2</span></a>`)
	assert.Contains(t, all, `<p class="lede">3 documents from 2 bookmarks.</p>`)
	assert.Contains(t, all, `<option value="">Any type</option>`)
	assert.Contains(t, all, `<label class="field"><span>Order</span><select class="select" name="order">`+
		`<option value="updated">Last updated</option><option value="saved">Date saved</option></select></label>`)
	assert.Contains(t, all, `<th scope="col" class="c-when">Updated</th>`)
	assert.NotContains(t, all, "table-note")
	assert.Contains(t, all, `<p class="pager-summary mt-4" id="showing">Showing 3 of 3 documents, most recently updated first.</p>`)
	assertLibraryTable(t, all, 3)

	filtered := func(query string) string {
		t.Helper()
		return getPage(t, srv, "/ui/library?"+query, http.StatusOK)
	}
	pdf := filtered("content_type=pdf&host=arxiv.example")
	assert.Contains(t, pdf, paper.ID)
	assert.Contains(t, pdf, failedDoc.ID)
	assert.NotContains(t, pdf, other.ID)
	assert.Contains(t, pdf, `<option selected>pdf</option>`)
	assert.Contains(t, pdf, `value="arxiv.example"`)
	assert.NotContains(t, stateTabsOf(t, pdf), `class="count"`, "the whole library's counts don't count a filtered list")
	assert.Contains(t, pdf, `<a id="subnav-failures" href="/ui/failures">Failures <span class="count">1</span></a>`,
		"the Failures tab counts the whole library's failures, whatever the filters")
	assert.Contains(t, pdf, `<a href="/ui/library?content_type=pdf&amp;host=arxiv.example&amp;state=failed">Failed</a>`,
		"a tab keeps the other filters")
	assert.Contains(t, pdf, `>Showing 2 documents, most recently updated first.</p>`)
	header := regexp.MustCompile(`(?s)<form class="header-search".*?</form>`).FindString(pdf)
	require.NotEmpty(t, header)
	assert.Equal(t, 1, strings.Count(header, "<input"), "the header searches the library, not the filters")
	assert.NotContains(t, header, "content_type")
	onlyFailed := filtered("state=failed")
	assert.Contains(t, onlyFailed, failedDoc.ID)
	assert.NotContains(t, onlyFailed, paper.ID)
	assert.Contains(t, onlyFailed, `<a href="/ui/library?state=failed" aria-current="page">Failed <span class="count">1</span></a>`)
	assert.Contains(t, onlyFailed, `>Showing 1 of 1 document, most recently updated first.</p>`)

	// Apply sends the form's fields: the hidden state keeps the tab.
	assert.Contains(t, onlyFailed, `<input type="hidden" name="state" value="failed">`)
	applied := filtered("state=failed&content_type=&host=arxiv.example&folder=")
	assert.Contains(t, applied, `<a href="/ui/library?host=arxiv.example&amp;state=failed" aria-current="page">Failed</a>`)
	assert.Contains(t, applied, failedDoc.ID)
	assert.NotContains(t, applied, paper.ID)
	inFolder := filtered("folder=/Research")
	assert.Contains(t, inFolder, paper.ID)
	assert.NotContains(t, inFolder, failedDoc.ID)
	none := filtered("host=nothing.example")
	assert.Contains(t, none, "<h2>No documents match</h2>")
	assert.Contains(t, none, "host <code>nothing.example</code>")
	assert.Contains(t, none, `<a class="btn" href="/ui/library">Clear filters</a>`)

	for _, query := range []string{"state=bogus", "content_type=bogus", "cursor=not-a-cursor", "order=bogus",
		"order=saved&state=bogus", "order=SAVED"} {
		body := getPage(t, srv, "/ui/library?"+query, http.StatusBadRequest)
		assert.Contains(t, body, `<div class="big-code">400</div>`+"\n<h1>bad request</h1>", query)
		assert.Contains(t, body, `<a class="btn btn-primary" href="/ui/library">Start over</a>`, query)
	}
	for _, query := range []string{"order=updated", "order=", "order=updated&content_type=&host=&folder=", "source=bogus"} {
		body := getPage(t, srv, "/ui/library?"+query, http.StatusOK)
		assert.Contains(t, body, `<th scope="col" class="c-when">Updated</th>`, "%s: Last updated", query)
		assert.Contains(t, body, `>Showing 3 of 3 documents, most recently updated first.</p>`, query)
	}
}

// TestUI_LibrarySaved: order=saved lists saves, newest saved first, a page
// saved in two browsers twice, each with its document and the browser it
// came from, its folder on hover, under a Saved column, a note, and a
// Showing line that counts saves: of the library's bookmarks with no
// filter at all. The tabs keep the order and count documents; every link
// keeps it; source is no filter; and a cursor of the other order is a 400
// page, either way.
func TestUI_LibrarySaved(t *testing.T) {
	srv := apitest.Start(t)
	now := time.Now().UTC().Truncate(time.Second)
	paper := titled(t, srv, "https://arxiv.example/paper", "A paper", store.DocStateFetched)
	_, err := srv.DB.Exec(`UPDATE documents SET content_type = 'pdf' WHERE id = ?`, paper.ID)
	require.NoError(t, err)
	blocked := srv.AddFailedDocument(t, "https://blocked.example/a", store.FailureCauseAntiBot)
	failJob(t, srv, blocked, "permanent failure: native: origin blocked the request (likely anti-bot)")
	bare := srv.AddDocument(t, "https://bare.example/x", store.DocStateFetched)
	save(t, srv, store.Bookmark{URL: paper.URL, Source: store.SourceChrome, FolderPath: new("/Research/ML"),
		SavedAt: now.Add(-72 * time.Hour)})
	save(t, srv, store.Bookmark{URL: paper.URL, Title: new("Saved as <this>"), Source: store.SourceSafari,
		SavedAt: now.Add(-time.Hour)})
	save(t, srv, store.Bookmark{URL: blocked.URL, Title: new("Blocked page"), Source: store.SourceFirefox,
		SavedAt: now.Add(-48 * time.Hour)})
	save(t, srv, store.Bookmark{URL: bare.URL, Source: store.SourceChrome, SavedAt: now.Add(-96 * time.Hour)})
	require.NoError(t, srv.Deps.Bookmarks.Create(context.Background(), &store.Bookmark{TenantID: apitest.TenantID,
		URL: "https://gone.example/x", Title: new("No <document>"), Source: store.SourceManual,
		SavedAt: now.Add(-24 * time.Hour)}))

	body := getPage(t, srv, "/ui/library?order=saved", http.StatusOK)
	assertLibraryTable(t, body, 5)
	links := docLinkRE.FindAllStringSubmatch(body, -1)
	linked := make([]string, 0, len(links))
	for _, m := range links {
		linked = append(linked, m[1])
	}
	assert.Equal(t, []string{paper.ID, blocked.ID, paper.ID, bare.ID}, linked,
		"newest saved first, the paper once per save, the save without a document unlinked")
	assert.Contains(t, body, `<option value="updated">Last updated</option><option value="saved" selected>Date saved</option>`)
	assert.Contains(t, body, `<th scope="col" class="c-when">Saved</th>`)
	for _, saved := range []time.Duration{time.Hour, 24 * time.Hour, 48 * time.Hour, 72 * time.Hour, 96 * time.Hour} {
		assert.Contains(t, body, `<td class="c-when when"><time datetime="`+now.Add(-saved).Format(time.RFC3339)+`"`)
	}
	assert.Contains(t, body, `<a class="doc-title" href="/ui/documents/`+paper.ID+`" title="A paper">A paper</a>`+"\n"+
		`<span class="doc-sub"><span class="host" title="https://arxiv.example/paper">arxiv.example/paper</span>`+
		`<span class="sep wide">·</span><span class="wide">safari</span>`, "a titled document by its title, whatever the save's")
	assert.Contains(t, body, `<span class="sep wide">·</span><span class="wide" title="/Research/ML">chrome</span>`)
	assert.Contains(t, body, `<a class="doc-title from-bookmark" href="/ui/documents/`+blocked.ID+
		`" title="Blocked page">Blocked page</a>`)
	assert.Contains(t, body, `<span class="err-cause">Blocked by bot protection</span> `+
		`<span class="msg">origin blocked the request (likely anti-bot)</span>`)
	assert.Contains(t, body, `<span class="doc-title from-bookmark" title="No &lt;document&gt;">No &lt;document&gt;</span>`+
		"\n"+`<span class="doc-sub"><span class="host" title="https://gone.example/x">gone.example/x</span>`)
	assert.Contains(t, body, `<td class="state-cell"></td>`+"\n"+`<td class="c-type"></td>`, "a save without a document")
	assert.Contains(t, body, `<a class="doc-title untitled" href="/ui/documents/`+bare.ID+`" title="https://bare.example/x">`+
		`bare.example/x</a>`+"\n"+`<span class="doc-sub"><span class="host" title="https://bare.example/x">bare.example</span>`)
	assert.Equal(t, 1, strings.Count(body, `<p class="table-note">Newest saves first, one row per bookmark: a page saved `+
		`in two browsers is listed twice. Safari keeps no save dates, so its bookmarks are dated when curio imported them.</p>`))
	assert.Contains(t, body, `<p class="pager-summary mt-4" id="showing">Showing 5 of 5 saves.</p>`)
	assert.Contains(t, body, `<p class="lede">3 documents from 5 bookmarks.</p>`)
	assert.Contains(t, body, `<a href="/ui/library?order=saved" aria-current="page">All <span class="count">3</span></a>`)
	assert.Contains(t, body, `<a href="/ui/library?order=saved&amp;state=fetched">Fetched <span class="count">2</span></a>`)
	assert.Equal(t, body, getPage(t, srv, "/ui/library?order=saved&source=chrome", http.StatusOK), "source is no filter")

	fetched := getPage(t, srv, "/ui/library?order=saved&state=fetched", http.StatusOK)
	assertLibraryTable(t, fetched, 3)
	assert.Contains(t, fetched, `<a href="/ui/library?order=saved&amp;state=fetched" aria-current="page">Fetched `+
		`<span class="count">2</span></a>`, "a tab counts documents, in either order")
	assert.Contains(t, fetched, `>Showing 3 saves.</p>`, "saves, but no count of them in a state")
	onHost := getPage(t, srv, "/ui/library?order=saved&host=blocked.example&cause=anti_bot", http.StatusOK)
	assertLibraryTable(t, onHost, 1)
	assert.Contains(t, onHost, `>Showing 1 save.</p>`)
	assert.NotContains(t, stateTabsOf(t, onHost), `class="count"`)
	assert.Contains(t, onHost, `<a id="subnav-failures" href="/ui/failures">Failures <span class="count">1</span></a>`)
	assert.Contains(t, onHost, `<input type="hidden" name="cause" value="anti_bot">`)
	inFolder := getPage(t, srv, "/ui/library?order=saved&folder=/Research&content_type=pdf", http.StatusOK)
	assertLibraryTable(t, inFolder, 1)
	assert.Contains(t, inFolder, `title="/Research/ML">chrome</span>`)

	paged := getPage(t, srv, "/ui/library?order=saved&limit=2", http.StatusOK)
	next := html.UnescapeString(moreRE.FindStringSubmatch(paged)[1])
	more := html.UnescapeString(moreHxRE.FindStringSubmatch(paged)[1])
	for _, link := range []string{next, more} {
		assert.Contains(t, link, "&limit=2&order=saved", link)
	}
	assert.NotContains(t, next, "shown=", "the plain link carries no count")
	assert.Contains(t, more, "&shown=2")
	assert.Contains(t, getPage(t, srv, more, http.StatusOK), `>Showing 4 of 5 saves.</p>`)

	none := getPage(t, srv, "/ui/library?order=saved&host=nothing.example", http.StatusOK)
	assert.Contains(t, none, "<h2>No saves match</h2>")
	assert.Contains(t, none, `<a class="btn" href="/ui/library?order=saved">Clear filters</a>`)
	assert.Contains(t, getPage(t, apitest.Start(t), "/ui/library?order=saved", http.StatusOK),
		"<h2>Your library is empty</h2>")

	documents := html.UnescapeString(moreRE.FindStringSubmatch(getPage(t, srv, "/ui/library?limit=1",
		http.StatusOK))[1])
	for _, path := range []string{
		strings.Replace(next, "&order=saved", "", 1),
		strings.Replace(next, "&order=saved", "&order=updated", 1),
		documents + "&order=saved",
	} {
		body := getPage(t, srv, path, http.StatusBadRequest)
		assert.Contains(t, body, "invalid cursor: another list or order issued it", path)
		assert.Contains(t, body, `<a class="btn btn-primary" href="/ui/library">Start over</a>`, path)
	}
}

// assertLibraryTable checks a Library page's table: widths from the
// colgroup, and rows rows of the four cells its columns name, Type and
// Updated last, which a phone folds away.
func assertLibraryTable(t *testing.T, page string, rows int) {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(page))
	require.NoError(t, err)
	var cols []string
	var trs []*xhtml.Node
	for n := range doc.Descendants() {
		switch {
		case n.Type != xhtml.ElementNode:
		case n.Data == "col":
			cols = append(cols, attr(n, "class"))
		case n.Data == "tbody" && attr(n, "id") == "rows":
			for tr := range n.ChildNodes() {
				if tr.Type == xhtml.ElementNode {
					trs = append(trs, tr)
				}
			}
		}
	}
	assert.Equal(t, []string{"", "c-state", "c-type", "c-when"}, cols)
	require.Len(t, trs, rows)
	for _, tr := range trs {
		var cells []string
		for td := range tr.ChildNodes() {
			if td.Type == xhtml.ElementNode {
				require.Equal(t, "td", td.Data)
				cells = append(cells, attr(td, "class"))
			}
		}
		assert.Equal(t, []string{"", "state-cell", "c-type", "c-when when"}, cells)
	}
}

// stateTabsOf is the markup of the Library's state tabs in page.
func stateTabsOf(t *testing.T, page string) string {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(page))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type == xhtml.ElementNode && attr(n, "role") == "group" && attr(n, "aria-label") == "State" {
			var b strings.Builder
			require.NoError(t, xhtml.Render(&b, n))
			return b.String()
		}
	}
	require.Fail(t, "no state tabs")
	return ""
}

func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// TestUI_LibraryFailures: a failed or dead row says why under its name,
// by its cause and the start of its error, with the whole error on
// hover; a document a refetch recovered shows no old failure.
func TestUI_LibraryFailures(t *testing.T) {
	srv := apitest.Start(t)
	recovered := titled(t, srv, "https://example.com/recovered", "Recovered", store.DocStateFetched)
	failJob(t, srv, recovered, "an old failure a refetch recovered from")
	blocked := srv.AddFailedDocument(t, "https://blocked.example/a", store.FailureCauseAntiBot)
	const blockedErr = "permanent failure: native: origin blocked the request (likely anti-bot)"
	failJob(t, srv, blocked, blockedErr)
	srv.AddFailedDocument(t, "https://gone.example/a", store.FailureCauseDeadLink)

	body := getPage(t, srv, "/ui/library", http.StatusOK)
	assert.NotContains(t, body, "an old failure")
	assert.Contains(t, body, `<span class="doc-error" title="`+blockedErr+`"><span class="err-cause">Blocked by bot protection</span> `+
		`<span class="msg">origin blocked the request (likely anti-bot)</span></span>`)
	assert.Contains(t, body, `<span class="doc-error"><span class="err-cause">Dead link</span> </span>`)
	assert.Equal(t, 2, strings.Count(body, `<span class="doc-error"`))
}

// moreHxRE finds the Library's "load more" request, htmx's.
var moreHxRE = regexp.MustCompile(`<div class="load-more" id="more"><a class="btn" href="[^"]+" hx-get="([^"]+)"`)

// hrefRE finds a page's links.
var hrefRE = regexp.MustCompile(`href="([^"]*)"`)

// TestUI_LibraryPages: following the next-page link one row at a time
// visits every matching document, or in the Date saved order every save,
// once, with the order and the filters kept, the cause among them, which
// the page has no control for yet. Load more asks for the next page with
// the rows shown, so its Showing line counts them all; no link carries
// that count.
func TestUI_LibraryPages(t *testing.T) {
	srv := apitest.Start(t)
	byHost, byCause, fetched := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range 5 {
		keep := srv.AddDocument(t, fmt.Sprintf("https://keep.example/%d", i), store.DocStateFetched)
		skip := srv.AddDocument(t, fmt.Sprintf("https://skip.example/%d", i), store.DocStateFetched)
		byHost[keep.ID], fetched[keep.ID], fetched[skip.ID] = true, true, true
		byCause[srv.AddFailedDocument(t, fmt.Sprintf("https://blocked.example/%d", i), store.FailureCauseAntiBot).ID] = true
		srv.AddFailedDocument(t, fmt.Sprintf("https://walled.example/%d", i), store.FailureCauseLoginWall)
		// The saves: the kept documents', all saved at one time, a tie the
		// cursor pages through by ID.
		save(t, srv, store.Bookmark{URL: keep.URL, Source: store.SourceChrome,
			SavedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
	}
	for _, tc := range []struct {
		filter string
		want   map[string]bool
		// The Showing lines of the first page and of the page load more
		// appends to it: the counts only with state the one filter.
		first, appended string
	}{
		{"host=keep.example", byHost, "Showing 1 document,", "Showing 2 documents,"},
		{"cause=anti_bot", byCause, "Showing 1 document,", "Showing 2 documents,"},
		{"state=fetched", fetched, "Showing 1 of 10 documents,", "Showing 2 of 10 documents,"},
		{"order=saved", byHost, "Showing 1 of 5 saves.", "Showing 2 of 5 saves."},
		{"order=saved&state=fetched", byHost, "Showing 1 save.", "Showing 2 saves."},
	} {
		t.Run(tc.filter, func(t *testing.T) {
			got := map[string]int{}
			path := "/ui/library?" + tc.filter + "&limit=1"
			for i := range 20 {
				body := getPage(t, srv, path, http.StatusOK)
				for _, href := range hrefRE.FindAllStringSubmatch(body, -1) {
					assert.NotContains(t, href[1], "shown=", "links and tabs never carry it")
				}
				ids := docLinkRE.FindAllStringSubmatch(body, -1)
				require.Len(t, ids, 1, path)
				got[ids[0][1]]++
				assert.Contains(t, body, `<p class="pager-summary mt-4" id="showing">`+tc.first,
					"a plain link's page shows its own rows")
				m := moreRE.FindStringSubmatch(body)
				if m == nil {
					break
				}
				if i == 0 {
					hx := moreHxRE.FindStringSubmatch(body)
					require.NotNil(t, hx)
					appended := getPage(t, srv, html.UnescapeString(hx[1]), http.StatusOK)
					assert.Contains(t, appended, `<p class="pager-summary mt-4" id="showing">`+tc.appended)
					next := moreHxRE.FindStringSubmatch(appended)
					require.NotNil(t, next)
					assert.Contains(t, html.UnescapeString(next[1]), "shown=2")
				}
				path = html.UnescapeString(m[1])
				assert.Contains(t, path, tc.filter)
				assert.Contains(t, path, "limit=1")
				assert.Contains(t, body, `hx-select-oob="#more,#showing">Load 1 more</a></div>`)
			}
			require.Len(t, got, len(tc.want))
			for id, n := range got {
				assert.True(t, tc.want[id], id)
				assert.Equal(t, 1, n, "visited once: %s", id)
			}
		})
	}

	form := getPage(t, srv, "/ui/library?cause=anti_bot", http.StatusOK)
	assert.Contains(t, form, `<input type="hidden" name="cause" value="anti_bot">`, "filtering again keeps the cause")
	body := getPage(t, srv, "/ui/library?cause=bogus", http.StatusBadRequest)
	assert.Contains(t, body, `<div class="big-code">400</div>`+"\n<h1>bad request</h1>")
}

// TestUI_LibraryShown: shown counts the rows a page appends to, in either
// order, and anything but a count up to a million counts none.
func TestUI_LibraryShown(t *testing.T) {
	srv := apitest.Start(t)
	doc := srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	bookmark(t, srv, doc.URL, store.SourceChrome, "")
	for shown, want := range map[string]string{
		"":        "Showing 1 of 1 document,",
		"0":       "Showing 1 of 1 document,",
		"-1":      "Showing 1 of 1 document,",
		"two":     "Showing 1 of 1 document,",
		"1.5":     "Showing 1 of 1 document,",
		"1000001": "Showing 1 of 1 document,",
		"2":       "Showing 3 documents,", // more than there are: the list moved on
		"1000000": "Showing 1,000,001 documents,",
	} {
		body := getPage(t, srv, "/ui/library?state=fetched&shown="+shown, http.StatusOK)
		assert.Contains(t, body, `<p class="pager-summary mt-4" id="showing">`+want, "shown=%s", shown)
	}
	for shown, want := range map[string]string{
		"":        "Showing 1 of 1 save.",
		"two":     "Showing 1 of 1 save.",
		"1000001": "Showing 1 of 1 save.",
		"2":       "Showing 3 saves.",
		"1000000": "Showing 1,000,001 saves.",
	} {
		body := getPage(t, srv, "/ui/library?order=saved&shown="+shown, http.StatusOK)
		assert.Contains(t, body, `<p class="pager-summary mt-4" id="showing">`+want, "saved, shown=%s", shown)
	}
}

// TestUI_LibraryCountsDegrade: the Library does without counts it can't
// read: the tabs show none, the page answers, and the failure is logged
// once.
func TestUI_LibraryCountsDegrade(t *testing.T) {
	var rec logRecorder
	srv := apitest.Start(t, func(d *api.Deps) {
		d.Bookmarks = failingCount{d.Bookmarks}
		d.Log = slog.New(&rec)
	})
	srv.AddDocument(t, "https://example.com/a", store.DocStateFetched)
	p := get(t, srv, "/ui/library?state=fetched")
	require.Equal(t, http.StatusOK, p.status)
	uitest.AssertInert(t, p.body)
	assert.Contains(t, p.body, `<a href="/ui/library?state=fetched" aria-current="page">Fetched</a>`)
	assert.NotContains(t, p.body, `class="count"`)
	assert.NotContains(t, p.body, "panel-error")
	assert.Contains(t, p.body, `<p class="lede">Every page curio saved for you.</p>`)
	assert.Contains(t, p.body, `>Showing 1 document, most recently updated first.</p>`)
	errs := rec.errors()
	require.Len(t, errs, 1)
	assert.Equal(t, p.header.Get("X-Request-Id"), errs[0]["request_id"])
	assert.ErrorIs(t, errs[0]["err"].(error), errInjected)
}

// TestUI_LibraryCause: a cause filter, in either order, is a line under the
// toolbar naming it by its label and code, whose Clear keeps every other
// filter and the order; the page it leads to has no line.
func TestUI_LibraryCause(t *testing.T) {
	srv := apitest.Start(t)
	blocked := srv.AddFailedDocument(t, "https://blocked.example/a", store.FailureCauseAntiBot)
	srv.AddFailedDocument(t, "https://other.example/a", store.FailureCauseAntiBot)
	save(t, srv, store.Bookmark{URL: blocked.URL, Source: store.SourceChrome})
	for query, cleared := range map[string]string{
		"cause=anti_bot":                              "/ui/library",
		"cause=anti_bot&host=blocked.example":         "/ui/library?host=blocked.example",
		"order=saved&cause=anti_bot":                  "/ui/library?order=saved",
		"order=saved&cause=anti_bot&state=failed":     "/ui/library?order=saved&state=failed",
		"cause=anti_bot&host=blocked.example&limit=7": "/ui/library?host=blocked.example&limit=7",
	} {
		body := getPage(t, srv, "/ui/library?"+query, http.StatusOK)
		line := regexp.MustCompile(`<p class="cause-line">.*</p>`).FindString(body)
		assert.Equal(t, `<p class="cause-line"><span class="muted">Why they failed:</span> <strong>Blocked by bot protection</strong> `+
			`<code>anti_bot</code> <a id="clear-cause" href="`+html.EscapeString(cleared)+
			`" aria-label="Clear the cause filter">Clear</a></p>`, line, query)
		assert.NotContains(t, getPage(t, srv, cleared, http.StatusOK), "cause-line", "%s: cleared", query)
	}
	assert.NotContains(t, getPage(t, srv, "/ui/library?host=blocked.example", http.StatusOK), "cause-line")
}

// TestUI_Failures: the Library's Failures tab over the real API, under the
// Library's navigation item and head: the totals, a card per cause, most
// first, each host tag leading to the Library of exactly the documents it
// counts, the causes without documents, and a cause a newer daemon wrote,
// shown by its code without a link or a refetch, since the Library and
// refetch-all refuse it.
func TestUI_Failures(t *testing.T) {
	srv := apitest.Start(t)
	for i := range 3 {
		srv.AddFailedDocument(t, fmt.Sprintf("https://blocked.example/%d", i), store.FailureCauseAntiBot)
	}
	srv.AddFailedDocument(t, "https://walled.example/a", store.FailureCauseAntiBot)
	srv.AddFailedDocument(t, "https://gone.example/a", store.FailureCauseDeadLink)
	newer := srv.AddFailedDocument(t, "https://newer.example/a", store.FailureCauseOther)
	_, err := srv.DB.Exec(`UPDATE documents SET failure_cause = '<new_cause>' WHERE id = ?`, newer.ID)
	require.NoError(t, err)
	titled(t, srv, "https://example.com/fetched", "Fetched", store.DocStateFetched)

	body := getPage(t, srv, "/ui/failures", http.StatusOK)
	assert.Contains(t, body, "<title>Failures · curio</title>")
	assert.Contains(t, body, `<a href="/ui/library" aria-current="page">`, "under the Library's item")
	assert.Equal(t, 1, strings.Count(body, `aria-current="page"><svg class="icon"`), "and no other")
	assert.Contains(t, body, `<form class="header-search"`)
	assert.Contains(t, body, "<h1>Library</h1>\n"+`<p class="lede">7 documents from 0 bookmarks.</p>`)
	assert.Contains(t, body, `<a id="subnav-failures" href="/ui/failures" aria-current="page">Failures <span class="count">6</span></a>`)
	assert.Contains(t, body, `<p class="failures-totals">6 documents couldn&#39;t be fetched: 5 failed and 1 dead link, grouped by why.`)

	page := pageDoc(t, body)
	var cards []string
	for n := range page.Descendants() {
		if n.Type == xhtml.ElementNode && n.Data == "li" && attr(n, "class") == "card cause" {
			cards = append(cards, attr(n, "id"))
		}
	}
	assert.Equal(t, []string{"cause-anti_bot", "cause-<new_cause>", "cause-dead_link"}, cards,
		"by count, most first, then by cause")
	tags := regexp.MustCompile(`<a class="tag" id="(host-anti_bot-\d)" href="([^"]+)" title="([^"]+)"><span class="name">[^<]+</span> <span class="n">(\d+)</span></a>`).
		FindAllStringSubmatch(body, -1)
	require.Len(t, tags, 2)
	assert.Equal(t, []string{"host-anti_bot-0", "/ui/library?cause=anti_bot&amp;host=blocked.example", "blocked.example", "3"},
		tags[0][1:])
	assert.Equal(t, "host-anti_bot-1", tags[1][1])
	listed := getPage(t, srv, html.UnescapeString(tags[0][2]), http.StatusOK)
	assert.Len(t, docLinkRE.FindAllString(listed, -1), 3, "the tag's Library lists what it counts")
	assert.Contains(t, body, `<a class="btn btn-sm btn-ghost" id="view-dead_link" href="/ui/library?cause=dead_link">`)
	assert.Len(t, docLinkRE.FindAllString(getPage(t, srv, "/ui/library?cause=dead_link", http.StatusOK), -1), 1,
		"View in Library finds the dead links without a state")

	unknown := elementByID(page, "cause-<new_cause>")
	require.NotNil(t, unknown)
	for n := range unknown.Descendants() {
		assert.False(t, n.Type == xhtml.ElementNode && (n.Data == "a" || n.Data == "button" || hasAttribute(n, "data-method")),
			"no link or refetch: <%s>", n.Data)
	}
	assert.Contains(t, body, `<h2>&lt;new_cause&gt; <code>&lt;new_cause&gt;</code></h2>`)
	assert.Contains(t, body, `<span class="tag" title="newer.example"><span class="name">newer.example</span> <span class="n">1</span></span>`)
	assert.Contains(t, body, "Causes without documents: Behind a login, Refused by Jina Reader,")
	assert.NotContains(t, body, "Causes without documents: Dead link")

	empty := getPage(t, apitest.Start(t), "/ui/failures", http.StatusOK)
	assert.Contains(t, empty, "<h2>Nothing failed</h2>")
	assert.Contains(t, empty, `Failures <span class="count">0</span>`)
}

// TestUI_FailuresDegrade: the Failures tab does without what it can't
// read: a summary that fails is its error in the groups' region, logged
// once, the head still there without the subnav's count; counts that fail
// leave the lede's fallback, logged once, the groups still there.
func TestUI_FailuresDegrade(t *testing.T) {
	var rec logRecorder
	srv := apitest.Start(t, func(d *api.Deps) {
		d.Documents = failingSummary{d.Documents}
		d.Log = slog.New(&rec)
	})
	p := get(t, srv, "/ui/failures")
	require.Equal(t, http.StatusOK, p.status)
	uitest.AssertInert(t, p.body)
	live := regexp.MustCompile(`(?s)<div class="failures" id="failures-live">.*?</div></div>`).FindString(p.body)
	assert.Contains(t, live, "Couldn't read this: summarize failures: injected failure.")
	assert.Contains(t, live, "Request "+p.header.Get("X-Request-Id"))
	assert.Contains(t, p.body, `<a id="subnav-failures" href="/ui/failures" aria-current="page">Failures</a>`)
	assert.Contains(t, p.body, `<p class="lede">0 documents from 0 bookmarks.</p>`)
	require.Len(t, rec.errors(), 1)

	var counts logRecorder
	srv = apitest.Start(t, func(d *api.Deps) {
		d.Bookmarks = failingCount{d.Bookmarks}
		d.Log = slog.New(&counts)
	})
	srv.AddFailedDocument(t, "https://blocked.example/a", store.FailureCauseAntiBot)
	p = get(t, srv, "/ui/failures")
	require.Equal(t, http.StatusOK, p.status)
	assert.Contains(t, p.body, `<p class="lede">Every page curio saved for you.</p>`)
	assert.NotContains(t, p.body, "panel-error")
	assert.Contains(t, p.body, `id="cause-anti_bot"`)
	assert.Contains(t, p.body, `Failures <span class="count">1</span>`, "the summary counts")
	errs := counts.errors()
	require.Len(t, errs, 1)
	assert.ErrorIs(t, errs[0]["err"].(error), errInjected)
}

// hostileMarkdown is a stored page trying everything.
const hostileMarkdown = "# Hostile\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n" +
	"[click](javascript:alert(1)) <span style=\"position:fixed\">styled</span>\n\n" +
	"![diagram](https://img.example/d.png) [relative](/about)\n"

func TestUI_Document(t *testing.T) {
	srv := apitest.Start(t)
	doc := titled(t, srv, "https://site.example/post", "A post", store.DocStateFetched)
	srv.AddContent(t, doc, hostileMarkdown)
	related := titled(t, srv, "https://site.example/other", "Hostile relative", store.DocStateFetched)
	srv.AddContent(t, related, "# Hostile relative\n\nhostile script alert")
	bookmark(t, srv, doc.URL, store.SourceChrome, "/Reading/Web", "security", "<b>tag</b>")
	failJob(t, srv, doc, "an old failure a refetch recovered from")

	body := getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	assert.Contains(t, body, "<h1>A post</h1>")
	assert.Contains(t, body, `<dd class="mono">`+doc.ID+"</dd>")
	assert.Contains(t, body, srv.Home.ContentDir(), "the absolute markdown path")
	assert.Contains(t, body, "by apitest")
	assert.Contains(t, body, `<a href="https://site.example/about" rel="nofollow noreferrer noopener" target="_blank">relative</a>`)
	assert.Contains(t, body, `>[image: diagram]</a>`)
	assert.NotContains(t, body, "<img")
	assert.Contains(t, body, `<a href="/ui/documents/`+doc.ID+`?images=1">Load images</a>`)
	assert.Contains(t, body, `<a href="/ui/documents/`+related.ID+`" title="Hostile relative">Hostile relative</a>`)
	assert.Regexp(t, `<strong>chrome</strong><svg class="icon"[^>]*>.*?</svg><code>/Reading/Web</code>`, body)
	assert.Contains(t, body, `<span class="tag">&lt;b&gt;tag&lt;/b&gt;</span>`)
	assert.NotContains(t, body, "an old failure", "a fetched document's old failure isn't current")
	assert.NotContains(t, body, `class="callout callout-danger`)
	assert.Contains(t, body, "<code>curio refetch "+doc.ID+"</code>")
	assert.Contains(t, body, "<code>curio reindex "+doc.ID+"</code>")

	for _, on := range []string{"1", "true"} {
		images := getPageCSP(t, srv, "/ui/documents/"+doc.ID+"?images="+on, http.StatusOK, ui.CSPWithImages)
		assert.Contains(t, images, `<img src="https://img.example/d.png" alt="diagram" loading="lazy">`)
		assert.NotContains(t, images, "Load images")
	}
	assert.NotContains(t, getPage(t, srv, "/ui/documents/"+doc.ID+"?images=yes", http.StatusOK), "<img")

	missing := getPage(t, srv, "/ui/documents/no-such-document", http.StatusNotFound)
	assert.Contains(t, missing, `<a class="btn btn-primary" href="/ui/">Start over</a>`, "from the search home")
}

// TestUI_DocumentNamedByBookmark: an untitled document's page is named
// by its newest titled bookmark, as the Library names it: its heading,
// styled as a fallback, and its tab, with its address under it as ever.
// One with no titled bookmark is named by its address.
func TestUI_DocumentNamedByBookmark(t *testing.T) {
	srv := apitest.Start(t)
	blocked := srv.AddFailedDocument(t, "https://blocked.example/a/b/", store.FailureCauseAntiBot)
	save(t, srv, store.Bookmark{URL: blocked.URL, Title: new("  A <blocked> page "), Source: store.SourceChrome})
	body := getPage(t, srv, "/ui/documents/"+blocked.ID, http.StatusOK)
	assert.Contains(t, body, `<title>A &lt;blocked&gt; page · curio</title>`)
	assert.Contains(t, body, `<h1 class="from-bookmark">A &lt;blocked&gt; page</h1>`+"\n"+`<p class="doc-url">`)
	assert.Contains(t, body, `<a href="https://blocked.example/a/b/" rel="noopener noreferrer" target="_blank">`+
		`https://blocked.example/a/b/</a>`)

	bare := srv.AddDocument(t, "https://bare.example/x", store.DocStatePending)
	save(t, srv, store.Bookmark{URL: bare.URL, Title: new(" "), Source: store.SourceChrome})
	body = getPage(t, srv, "/ui/documents/"+bare.ID, http.StatusOK)
	assert.Contains(t, body, `<title>https://bare.example/x · curio</title>`)
	assert.Contains(t, body, "<h1>bare.example/x</h1>", "a blank bookmark title names nothing")
}

// TestUI_DocumentRemoteImagesOn: with ui.load_remote_images, every
// document page shows its https images, with the CSP that allows them.
func TestUI_DocumentRemoteImagesOn(t *testing.T) {
	srv := apitest.StartUI(t, api.UIOptions{Enabled: true, LoadRemoteImages: true})
	doc := srv.AddDocument(t, "https://site.example/post", store.DocStateFetched)
	srv.AddContent(t, doc, hostileMarkdown)
	body := getPageCSP(t, srv, "/ui/documents/"+doc.ID, http.StatusOK, ui.CSPWithImages)
	assert.Contains(t, body, `<img src="https://img.example/d.png" alt="diagram" loading="lazy">`)
	assert.NotContains(t, body, "Load images")
	getPage(t, srv, "/ui/", http.StatusOK)
}

// countingExtractions counts the extractions read by ID.
type countingExtractions struct {
	store.ExtractionStore
	reads *atomic.Int32
}

func (c countingExtractions) GetByID(ctx context.Context, id string) (*store.DocumentExtraction, error) {
	c.reads.Add(1)
	return c.ExtractionStore.GetByID(ctx, id)
}

// TestUI_DocumentReadsItsExtractionOnce: the page's facts about how its
// text was fetched, and the text, come from one read of the extraction.
func TestUI_DocumentReadsItsExtractionOnce(t *testing.T) {
	reads := new(atomic.Int32)
	srv := apitest.Start(t, func(d *api.Deps) { d.Extractions = countingExtractions{d.Extractions, reads} })
	doc := srv.AddDocument(t, "https://site.example/post", store.DocStateFetched)
	srv.AddContent(t, doc, "# A post\n\nthe text")
	reads.Store(0)
	assert.Contains(t, getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK), "<p>the text</p>")
	assert.EqualValues(t, 1, reads.Load())
}

// TestUI_DocumentUnformatted: a stored text too costly to format, here a
// line opening a thousand blockquotes, is shown as it is stored, escaped.
func TestUI_DocumentUnformatted(t *testing.T) {
	srv := apitest.Start(t)
	doc := srv.AddDocument(t, "https://site.example/deep", store.DocStateFetched)
	srv.AddContent(t, doc, strings.Repeat(">", 1000)+" <script>alert(1)</script> ![x](https://img.example/x.png)\n")
	body := getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	assert.Contains(t, body, "Shown as stored, unformatted: a line opens too many blockquotes and lists to format quickly.")
	assert.Contains(t, body, strings.Repeat("&gt;", 1000)+" &lt;script&gt;alert(1)&lt;/script&gt; ![x](https://img.example/x.png)\n</pre>")
	assert.NotContains(t, body, "<blockquote>")
	assert.NotContains(t, body, "Load images", "nothing to load")
}

// TestUI_DocumentUnlinkableLinksOverBudget: links a page can't keep count
// against the link budget too, here a thousand to a 16 KiB ftp URL.
func TestUI_DocumentUnlinkableLinksOverBudget(t *testing.T) {
	srv := apitest.Start(t)
	doc := srv.AddDocument(t, "https://site.example/refs", store.DocStateFetched)
	src := "[x]: ftp://a.example/" + strings.Repeat("a", 16<<10) + "\n\n" + strings.Repeat("[x]\n\n", 1000)
	srv.AddContent(t, doc, src)
	body := getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	assert.Contains(t, body, "Shown as stored, unformatted: its links repeat too much text to format quickly.")
	assert.Contains(t, body, `<pre class="source">`+html.EscapeString(src)+"</pre>")
	assert.NotContains(t, body, `<a href="ftp:`)
}

// TestUI_DocumentStates: a document without text, one whose text is gone
// from disk, a failed one with why and its last error, and a dead one.
func TestUI_DocumentStates(t *testing.T) {
	srv := apitest.Start(t)
	pending := srv.AddDocument(t, "https://example.com/pending", store.DocStatePending)
	body := getPage(t, srv, "/ui/documents/"+pending.ID, http.StatusOK)
	assert.Contains(t, body, "<h2>Not fetched yet</h2>")
	assert.Contains(t, body, "None yet: related documents come from its indexed text.")
	assert.Contains(t, body, "<code>curio refetch "+pending.ID+"</code>")
	assert.NotContains(t, body, "curio reindex", "nothing to reindex before a fetch")
	assert.NotContains(t, body, `class="callout`)

	gone := srv.AddDocument(t, "https://example.com/gone", store.DocStateFetched)
	ext := srv.AddContent(t, gone, "# Gone")
	require.NoError(t, os.Remove(srv.Home.ContentDir()+"/"+*ext.MarkdownPath))
	assert.Contains(t, getPage(t, srv, "/ui/documents/"+gone.ID, http.StatusOK),
		"The extracted text is missing on disk; refetch the document.")

	failed := srv.AddFailedDocument(t, "https://example.com/blocked", store.FailureCauseAntiBot)
	failJob(t, srv, failed, "permanent failure: native: HTTP 403 <from> the origin")
	body = getPage(t, srv, "/ui/documents/"+failed.ID, http.StatusOK)
	assert.Contains(t, body, `<div class="callout callout-danger mt-4" role="alert">`)
	assert.Contains(t, body, `<p class="callout-title">Blocked by bot protection</p>`)
	assert.Contains(t, body, "<p>The site blocked curio&#39;s request: a 403 or 503, or a bot check or block page.</p>")
	assert.Contains(t, body, `<details class="more-text"><summary>Full error</summary>`+
		"<pre>permanent failure: native: HTTP 403 &lt;from&gt; the origin</pre></details>")
	assert.Contains(t, body, "<h2>No text</h2>")
	assert.Contains(t, body, "<code>curio refetch "+failed.ID+"</code>")

	dead := srv.AddDocument(t, "https://example.com/dead", store.DocStateDead)
	failJob(t, srv, dead, "permanent failure: native: dead link (HTTP 404)")
	body = getPage(t, srv, "/ui/documents/"+dead.ID, http.StatusOK)
	assert.Contains(t, body, `<div class="callout mt-4" role="note">`)
	assert.Contains(t, body, `<p class="callout-title">This page is gone</p>`)
	assert.Contains(t, body, "<pre>permanent failure: native: dead link (HTTP 404)</pre>")
	assert.Contains(t, body, "<code>curio refetch --force "+dead.ID+"</code>", "a dead link refetches only forced")
}

// TestUI_DocumentTruncated: a text past the render cap shows its start and
// says where the rest is.
func TestUI_DocumentTruncated(t *testing.T) {
	srv := apitest.Start(t)
	doc := srv.AddDocument(t, "https://example.com/huge", store.DocStateFetched)
	line := strings.Repeat("word ", 199) + "end\n"
	var md strings.Builder
	for md.Len() <= ui.MaxRenderedMarkdown {
		md.WriteString(line)
	}
	md.WriteString("the last line\n")
	srv.AddContent(t, doc, md.String())
	body := getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK)
	assert.Contains(t, body, "This is the start of the text; all of it is in <code>"+srv.Home.ContentDir())
	assert.NotContains(t, body, "the last line")
	assert.NotContains(t, body, "Load images", "offered only for a text with remote images")
}

// failingDocumentBookmarks fails reading a document's bookmarks.
type failingDocumentBookmarks struct{ store.BookmarkStore }

func (failingDocumentBookmarks) ListByDocument(context.Context, string, string) ([]*store.Bookmark, error) {
	return nil, fmt.Errorf("list bookmarks of document: %w", errInjected)
}

// TestUI_DocumentPanelsDegrade: a document panel whose read fails shows
// its error, logged once, and the rest of the page renders.
func TestUI_DocumentPanelsDegrade(t *testing.T) {
	var rec logRecorder
	srv := apitest.Start(t, func(d *api.Deps) {
		d.Bookmarks = failingDocumentBookmarks{d.Bookmarks}
		d.Log = slog.New(&rec)
	})
	doc := titled(t, srv, "https://site.example/post", "A post", store.DocStateFetched)
	srv.AddContent(t, doc, "# A post\n\nthe text")
	p := get(t, srv, "/ui/documents/"+doc.ID)
	require.Equal(t, http.StatusOK, p.status)
	uitest.AssertInert(t, p.body)
	assert.Contains(t, p.body, "Couldn't read this: list bookmarks of document: injected failure.")
	assert.Contains(t, p.body, "<p>the text</p>", "the text panel renders")
	errs := rec.errors()
	require.Len(t, errs, 1)
	assert.Equal(t, p.header.Get("X-Request-Id"), errs[0]["request_id"])
}

func TestUI_Interests(t *testing.T) {
	srv := apitest.Start(t)
	assert.Contains(t, getPage(t, srv, "/ui/interests", http.StatusOK), "<code>curio interests rebuild</code>")

	a := titled(t, srv, "https://example.com/a", "Kafka partitions", store.DocStateFetched)
	b := srv.AddDocument(t, "https://example.com/b", store.DocStateFetched)
	c := srv.AddDocument(t, "https://example.com/c", store.DocStateFetched)
	d := srv.AddDocument(t, "https://example.com/d", store.DocStateFetched)
	interest := srv.AddInterest(t, "Stream <processing>", a, b, c, d)

	list := getPage(t, srv, "/ui/interests", http.StatusOK)
	assert.Contains(t, list, `<a href="/ui/interests/`+interest.ID+`">Stream &lt;processing&gt;</a>`)
	assert.Contains(t, list, "<b>4</b> documents")
	assert.Contains(t, list, `<meter class="meter" min="0" max="1" value="0.90">0.90</meter>0.90</span>`, "cohesion")
	assert.Contains(t, list, `<a href="/ui/documents/`+a.ID+`" title="Kafka partitions">Kafka partitions</a>`)
	assert.Contains(t, list, "1 topic curio found in your library, largest first.</p>", "the run's count, all shown")
	assert.Contains(t, list, `title="https://example.com/c">example.com/c</a>`, "a card lists 3 members")
	assert.NotContains(t, list, "example.com/d", "and no more")
	assert.NotContains(t, list, `class="pager"`, "one page")

	one := getPage(t, srv, "/ui/interests/"+interest.ID, http.StatusOK)
	assert.Contains(t, one, "<h1>Stream &lt;processing&gt;</h1>")
	assert.Contains(t, one, `<span class="badge badge-accent plain">4 documents</span>`)
	assert.Regexp(t, `<span class="sep">·</span><span class="text">run of \d{4}-\d\d-\d\d \d\d:\d\d</span></div>`, one)
	assert.Contains(t, one, `<a class="doc-title untitled" href="/ui/documents/`+b.ID+
		`" title="https://example.com/b">example.com/b</a>`)
	assert.Contains(t, one, `<td class="num muted">2</td>`, "ranked")
	assert.Contains(t, one, `</meter>0.80</span></td>`)
	assert.NotContains(t, one, `class="pager"`, "one page")

	// Interests get new IDs with every rebuild: one not found leads back to
	// them.
	gone := getPage(t, srv, "/ui/interests/no-such-interest", http.StatusNotFound)
	assert.Contains(t, gone, "<p>interest &#34;no-such-interest&#34; not found: interests get new IDs each time "+
		"they are rebuilt</p>")
	assert.Contains(t, gone, `<a class="btn btn-primary" href="/ui/interests">Start over</a>`)
	_, err := srv.DB.Exec(`UPDATE clusters SET tenant_id = 'other' WHERE id = ?`, interest.ID)
	require.NoError(t, err)
	getPage(t, srv, "/ui/interests/"+interest.ID, http.StatusNotFound)
}

// TestUI_InterestsPages: the Interests show 24 cards a page, largest
// first, the rest on the pages after, whose links name the run; a page
// past the last is a 404 in the page's frame, and a page that isn't a
// number a 400. Untitled members are named by their bookmarks.
func TestUI_InterestsPages(t *testing.T) {
	srv := apitest.Start(t)
	lens := srv.AddDocument(t, "https://d1.awsstatic.com/whitepapers/AWS-Serverless-Applications-Lens.pdf",
		store.DocStateFetched)
	save(t, srv, store.Bookmark{URL: lens.URL, Title: new("AWS Serverless Application Lens"), Source: store.SourceChrome})
	bare := srv.AddDocument(t, "https://example.com/bare", store.DocStateFetched)
	interests := make([]apitest.Interest, 0, 30)
	for i := range 30 {
		interests = append(interests, apitest.Interest{Label: fmt.Sprintf("Topic %02d", i), Size: 100 - i})
	}
	interests[0].Members = []*store.Document{lens, bare}
	clusters := srv.AddInterests(t, interests...)
	run := clusters[0].RunID

	first := getPage(t, srv, "/ui/interests", http.StatusOK)
	assert.Equal(t, 24, strings.Count(first, `<li class="card interest">`))
	assert.Contains(t, first, `<p class="lede">30 topics curio found in your library, largest first.</p>`)
	assert.Contains(t, first, "Topic 23")
	assert.NotContains(t, first, "Topic 24")
	assert.Contains(t, first, `<span class="pager-summary">Interests 1–24 of 30</span>`)
	assert.Contains(t, first, `<a class="step" href="/ui/interests?page=2&amp;run=`+run+`" rel="next">`)
	assert.Contains(t, first, `<li><a class="from-bookmark" href="/ui/documents/`+lens.ID+
		`" title="AWS Serverless Application Lens">AWS Serverless Application Lens</a></li>`)
	assert.Contains(t, first, `<li><a class="untitled" href="/ui/documents/`+bare.ID+
		`" title="https://example.com/bare">example.com/bare</a></li>`)

	second := getPage(t, srv, "/ui/interests?page=2&run="+run, http.StatusOK)
	assert.Equal(t, 6, strings.Count(second, `<li class="card interest">`))
	assert.Contains(t, second, "largest first. Page 2 of 2.</p>")
	assert.Contains(t, second, "Topic 24")
	assert.Contains(t, second, `<span class="pager-summary">Interests 25–30 of 30</span>`)
	assert.NotContains(t, second, `role="note"`, "the run it came from")
	assert.NotContains(t, getPage(t, srv, "/ui/interests?page=2", http.StatusOK), `role="note"`, "no run named")

	for _, path := range []string{"/ui/interests?page=3", "/ui/interests?page=9223372036854775807"} {
		p := get(t, srv, path)
		require.Equal(t, http.StatusNotFound, p.status, path)
		uitest.AssertInert(t, p.body)
		assert.Equal(t, ui.CSP, p.header.Get("Content-Security-Policy"), path)
		assert.Equal(t, "nosniff", p.header.Get("X-Content-Type-Options"), path)
		assert.Equal(t, "no-referrer", p.header.Get("Referrer-Policy"), path)
		assert.Contains(t, p.body, `<p class="lede">30 topics curio found in your library, largest first.</p>`, path)
		assert.Contains(t, p.body, `aria-label="Coverage"`, path)
		assert.Contains(t, p.body, `id="rebuild-poll"`, path)
		assert.Contains(t, p.body, "<p>This list has 2 pages.</p>", path)
		assert.Contains(t, p.body, `<a class="btn" href="/ui/interests?page=2&amp;run=`+run+`">Last page</a>`, path)
		assert.NotContains(t, p.body, "interest-grid", path)
	}
	for _, page := range []string{"0", "-1", "x", "1.5", "99999999999999999999"} {
		bad := getPage(t, srv, "/ui/interests?page="+page, http.StatusBadRequest)
		assert.Contains(t, bad, `page &#34;`+page+`&#34; must be a whole number, 1 or more`)
		assert.Contains(t, bad, `<a class="btn btn-primary" href="/ui/interests">Start over</a>`)
	}
}

// TestUI_InterestsRunChanged: a page asked for from a run that is gone,
// pruned by a rebuild, shows the newest run's, and says so.
func TestUI_InterestsRunChanged(t *testing.T) {
	srv := apitest.Start(t)
	interests := make([]apitest.Interest, 0, 30)
	for i := range 30 {
		interests = append(interests, apitest.Interest{Label: fmt.Sprintf("Topic %02d", i), Size: 100 - i})
	}
	run := srv.AddInterests(t, interests...)[0].RunID
	const old = "00000000-0000-0000-0000-000000000000"
	body := getPage(t, srv, "/ui/interests?page=2&run="+old, http.StatusOK)
	assert.Contains(t, body, `<div class="callout callout-info mb-4" role="note">`)
	assert.Contains(t, body, `this page lists the new run's. <a href="/ui/interests?run=`+run+
		`">Start again from page 1</a>.</p>`)
	assert.NotContains(t, body, old, "the run asked for is compared, never shown")
	assert.Contains(t, body, "Topic 24", "the new run's second page")
	assert.Contains(t, getPage(t, srv, "/ui/interests?run="+old, http.StatusOK), `role="note"`)
	assert.NotContains(t, getPage(t, srv, "/ui/interests?page=2&run="+run, http.StatusOK), `role="note"`)
}

// TestUI_InterestPages: an interest's members, 50 a page, ranked across
// the pages, named by their bookmarks where they are untitled; a page past
// the last is a 404 that keeps the interest's head.
func TestUI_InterestPages(t *testing.T) {
	srv := apitest.Start(t)
	docs := make([]*store.Document, 0, 60)
	for i := range 60 {
		docs = append(docs, srv.AddDocument(t, fmt.Sprintf("https://example.com/%02d", i), store.DocStateFetched))
	}
	save(t, srv, store.Bookmark{URL: docs[55].URL, Title: new("Saved <as> this"), Source: store.SourceSafari})
	interest := srv.AddInterest(t, "Sixty", docs...)
	href := "/ui/interests/" + interest.ID

	first := getPage(t, srv, href, http.StatusOK)
	assert.Equal(t, 50, strings.Count(first, `<td class="num muted">`))
	assert.Contains(t, first, `<td class="num muted">50</td>`)
	assert.Contains(t, first, `<span class="pager-summary">Documents 1–50 of 60, most similar first</span>`)
	assert.Contains(t, first, `<a class="step" href="`+href+`?page=2" rel="next">`)

	second := getPage(t, srv, href+"?page=2", http.StatusOK)
	assert.Equal(t, 10, strings.Count(second, `<td class="num muted">`))
	assert.Contains(t, second, `<td class="num muted">51</td>`)
	assert.Contains(t, second, `<td class="num muted">60</td>`)
	assert.Contains(t, second, `<span class="pager-summary">Documents 51–60 of 60, most similar first</span>`)
	assert.Contains(t, second, `<a class="doc-title from-bookmark" href="/ui/documents/`+docs[55].ID+
		`" title="Saved &lt;as&gt; this">Saved &lt;as&gt; this</a>`+"\n"+
		`<span class="doc-sub"><span class="host" title="`+docs[55].URL+`">example.com/55</span>`)

	past := getPage(t, srv, href+"?page=3", http.StatusNotFound)
	assert.Contains(t, past, "<h1>Sixty</h1>")
	assert.Contains(t, past, `<span class="badge badge-accent plain">60 documents</span>`)
	assert.Contains(t, past, "<h2>No page 3</h2>")
	assert.NotContains(t, past, "<table")
	getPage(t, srv, href+"?page=0", http.StatusBadRequest)
}

// TestUI_InterestRunDegrades: the interest's page does without its run
// line when the run can't be read, logging why once, and when the run is
// gone, which is no failure.
func TestUI_InterestRunDegrades(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		logged int
	}{
		"unreadable": {fmt.Errorf("get run: %w", errInjected), 1},
		"gone":       {store.ErrNotFound, 0},
	} {
		t.Run(name, func(t *testing.T) {
			var rec logRecorder
			srv := apitest.Start(t, func(d *api.Deps) {
				d.Insights = failingRunRead{d.Insights, tc.err}
				d.Log = slog.New(&rec)
			})
			interest := srv.AddInterest(t, "Kafka", srv.AddDocument(t, "https://example.com/a", store.DocStateFetched))
			p := get(t, srv, "/ui/interests/"+interest.ID)
			require.Equal(t, http.StatusOK, p.status, p.body)
			assert.Contains(t, p.body, "<h1>Kafka</h1>")
			assert.NotContains(t, p.body, "run of")
			assert.NotContains(t, p.body, "panel-error")
			errs := rec.errors()
			require.Len(t, errs, tc.logged)
			if tc.logged > 0 {
				assert.Equal(t, p.header.Get("X-Request-Id"), errs[0]["request_id"])
				assert.ErrorIs(t, errs[0]["err"].(error), errInjected)
			}
		})
	}
}

// failingRunRead fails the read of a clustering run by its ID with err.
type failingRunRead struct {
	store.InsightStore
	err error
}

func (f failingRunRead) GetRun(context.Context, string) (*store.ClusterRun, error) { return nil, f.err }

// TestUI_InterestsEmptyRun: a run that grouped nothing says so, rather
// than counting zero topics "largest first".
func TestUI_InterestsEmptyRun(t *testing.T) {
	srv := apitest.Start(t)
	srv.AddEmptyClusterRun(t, 5)

	page := getPage(t, srv, "/ui/interests", http.StatusOK)
	assert.Contains(t, page, `<p class="lede">The last clustering run found no topics in your library.</p>`)
	assert.Contains(t, page, "<h2>No interests in this run</h2>")
	assert.NotContains(t, page, "largest first")
}

// jobsPause pauses the queue.
func jobsPause() jobs.QueueUpdate { return jobs.QueueUpdate{Paused: new(true)} }

// fingerprint is every row of the tables a page could change, for proving
// that none did.
func fingerprint(t *testing.T, srv *apitest.Server) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"documents", "document_extractions", "bookmarks", "jobs", "chunks", "clusters",
		"cluster_documents", "cluster_runs", "queue_settings"} {
		rows, err := srv.DB.Query("SELECT * FROM " + table + " ORDER BY rowid")
		require.NoError(t, err, table)
		cols, err := rows.Columns()
		require.NoError(t, err)
		var dump strings.Builder
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			require.NoError(t, rows.Scan(ptrs...))
			fmt.Fprintln(&dump, vals...)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		out[table] = dump.String()
	}
	return out
}

// hxGetRE finds the URLs a page's htmx GETs, its pollers' among them.
var hxGetRE = regexp.MustCompile(`hx-get="([^"]+)"`)

// TestUI_GETNeverWrites: crawling every page, with every parameter they
// take, and every poll they render, with a fetch and a rebuild in flight,
// changes nothing in the database.
func TestUI_GETNeverWrites(t *testing.T) {
	ctx := context.Background()
	srv := apitest.Start(t)
	doc := titled(t, srv, "https://example.com/a", "Kafka", store.DocStateFetched)
	srv.AddContent(t, doc, hostileMarkdown)
	failed := srv.AddDocument(t, "https://example.com/failed", store.DocStateFailed)
	failJob(t, srv, failed, "HTTP 503")
	bookmark(t, srv, doc.URL, store.SourceChrome, "/Reading")
	save(t, srv, store.Bookmark{URL: failed.URL, Title: new("Failed"), Source: store.SourceSafari})
	interest := srv.AddInterest(t, "Kafka", doc)
	paged := make([]apitest.Interest, 0, 30)
	for i := range 30 {
		paged = append(paged, apitest.Interest{Label: fmt.Sprintf("Topic %d", i), Size: 30 - i,
			Members: []*store.Document{doc}})
	}
	run := srv.AddInterests(t, paged...)[0].RunID
	queued := srv.AddDocument(t, "https://example.com/queued", store.DocStateFetched)
	_, err := srv.Deps.Documents.RequeueFetch(ctx, apitest.TenantID, queued.ID)
	require.NoError(t, err)
	enqueueRebuild(t, srv)
	failRun(t, srv, "boom")
	_, err = srv.Deps.Gate.Update(ctx, jobsPause())
	require.NoError(t, err)
	before := fingerprint(t, srv)

	var polls []string
	for _, path := range []string{"/ui/status", "/ui/documents/" + queued.ID, "/ui/interests", "/ui/failures"} {
		for _, m := range hxGetRE.FindAllStringSubmatch(getPage(t, srv, path, http.StatusOK), -1) {
			polls = append(polls, html.UnescapeString(m[1]))
		}
	}
	require.Len(t, polls, 5, "Status's two, the document's, the Interests' and the Failures tab's")
	for _, poll := range polls {
		getPage(t, srv, poll, http.StatusOK)
	}

	home := getPage(t, srv, "/ui/", http.StatusOK)
	library := getPage(t, srv, "/ui/library?limit=1", http.StatusOK)
	cursor := html.UnescapeString(moreRE.FindStringSubmatch(library)[1])
	more := html.UnescapeString(moreHxRE.FindStringSubmatch(library)[1])
	saves := getPage(t, srv, "/ui/library?order=saved&limit=1", http.StatusOK)
	savesCursor := html.UnescapeString(moreRE.FindStringSubmatch(saves)[1])
	savesMore := html.UnescapeString(moreHxRE.FindStringSubmatch(saves)[1])
	for _, path := range []string{
		"/ui/library?order=saved", savesCursor, savesMore, "/ui/library?order=bogus",
		strings.Replace(savesCursor, "order=saved", "order=updated", 1), cursor + "&order=saved",
		"/", "/ui/", "/ui", "/ui/?q=kafka", "/ui/?q=kafka&content_type=article", "/ui/?content_type=pdf",
		"/ui/?content_type=bogus", "/ui/search", "/ui/search?q=kafka", "/ui/status", "/ui/library",
		"/ui/library?state=failed", "/ui/library?content_type=article&host=example.com&folder=/Reading", cursor, more,
		"/ui/library?state=bogus", "/ui/documents/" + doc.ID, "/ui/documents/" + doc.ID + "?images=1",
		"/ui/documents/" + failed.ID, "/ui/documents/nope", "/ui/interests", "/ui/interests/" + interest.ID, "/ui/nope",
		assetRE.FindStringSubmatch(home)[1], "/ui/static/nope.css", "/ui/status?poll=bogus",
		"/ui/documents/" + doc.ID + "?poll=jobs", "/ui/interests?poll=live", "/ui/failures",
		"/ui/failures?poll=bogus", "/ui/library?cause=other&host=example.com",
		"/ui/interests?page=2&run=" + run, "/ui/interests?page=2&run=00000000-0000-0000-0000-000000000000",
		"/ui/interests?page=3", "/ui/interests?page=x", "/ui/interests/" + interest.ID + "?page=2",
		"/ui/interests/" + interest.ID + "?page=0",
	} {
		p := get(t, srv, path)
		assert.Less(t, p.status, http.StatusInternalServerError, path)
	}
	assert.Equal(t, before, fingerprint(t, srv))
}
