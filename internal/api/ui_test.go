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
	assert.Contains(t, body, "<h1>Overview</h1>", "/ui is /ui/")
	assert.Contains(t, body, "<span>"+strings.TrimPrefix(srv.URL, "http://")+" · this Mac only</span>",
		"the footer names where the daemon listens")
}

func TestUI_Overview(t *testing.T) {
	srv := apitest.Start(t)
	fetched := titled(t, srv, "https://example.com/fetched", "A fetched page", store.DocStateFetched)
	failed := srv.AddDocument(t, "https://example.com/failed", store.DocStateFailed)
	failJob(t, srv, failed, "HTTP 503")
	bookmark(t, srv, fetched.URL, store.SourceChrome, "/Reading")
	bookmark(t, srv, "https://example.com/new", store.SourceSafari, "")

	body := getPage(t, srv, "/ui/", http.StatusOK)
	assert.Contains(t, body, `<span class="label">Documents</span><span class="value">3</span>`)
	assert.Contains(t, body, `<span class="label">Fetched</span><span class="value">1 <small>33%</small></span>`)
	assert.Contains(t, body, `<span class="label">Bookmarks</span><span class="value">2</span>`)
	for _, state := range []string{"pending", "fetched", "failed", "dead"} {
		assert.Contains(t, body, `<a href="/ui/library?state=`+state+`">`)
	}
	// The state bar's proportions are attributes: a third each of pending,
	// fetched and failed.
	assert.Contains(t, body, `<rect class="fill-warn" x="0.000" y="0" width="33.333" height="10"/>`+
		`<rect class="fill-ok" x="33.333" y="0" width="33.334" height="10"/>`+
		`<rect class="fill-danger" x="66.667" y="0" width="33.333" height="10"/>`)
	assert.NotContains(t, body, "style=")
	assert.Contains(t, body, `<span class="badge queue-open">open</span>`)
	assert.Contains(t, body, `<td>fetch</td><td class="num">0 of 16</td><td class="num">1</td>`, "the new page's fetch job")
	assert.Contains(t, body, "1 job queued, and none finished in the last 10m.")
	assert.Contains(t, body, `qwen3-embedding:0.6b<span class="sub">1024 dimensions</span>`)
	assert.Contains(t, body, `<a class="doc-title untitled" href="/ui/documents/`+fetched.ID+
		`" title="https://example.com/fetched">example.com/fetched</a>`, "a bookmark links to its document")
	assert.Contains(t, body, `title="https://example.com/new">example.com/new</a>`)

	// Paused: the progress says why nothing starts.
	_, err := srv.Deps.Gate.Update(context.Background(), jobsPause())
	require.NoError(t, err)
	body = getPage(t, srv, "/ui/", http.StatusOK)
	assert.Contains(t, body, `<span class="badge queue-closed">closed</span><span class="why">paused`)
	assert.Contains(t, body, "1 job queued. The queue is closed (paused), so none start until it opens.")
}

// failingCount fails the bookmark count, which the Overview's counts read.
type failingCount struct{ store.BookmarkStore }

var errInjected = errors.New("injected failure")

func (failingCount) Count(context.Context, string) (int, error) {
	return 0, fmt.Errorf("count bookmarks: %w", errInjected)
}

// TestUI_OverviewPanelsDegrade: a panel whose read fails shows its error
// and request ID, logged once, and the rest of the page still renders.
func TestUI_OverviewPanelsDegrade(t *testing.T) {
	var rec logRecorder
	srv := apitest.Start(t, func(d *api.Deps) {
		d.Bookmarks = failingCount{d.Bookmarks}
		d.Log = slog.New(&rec)
	})
	p := get(t, srv, "/ui/")
	require.Equal(t, http.StatusOK, p.status)
	uitest.AssertInert(t, p.body)
	id := p.header.Get("X-Request-Id")
	assert.Contains(t, p.body, "Couldn't read this: count bookmarks: injected failure.")
	assert.Contains(t, p.body, "Request "+id)
	assert.Contains(t, p.body, `<span class="badge queue-open">open</span>`, "the queue panel renders")
	assert.Contains(t, p.body, "qwen3-embedding:0.6b", "the health panel renders")

	errs := rec.errors()
	require.Len(t, errs, 1)
	assert.Equal(t, id, errs[0]["request_id"])
	assert.ErrorIs(t, errs[0]["err"].(error), errInjected)
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

	form := getPage(t, srv, "/ui/search", http.StatusOK)
	assert.Contains(t, form, `hx-get="/ui/search" hx-trigger="input changed delay:400ms, search" hx-sync="this:replace"`)
	assert.Contains(t, form, `hx-target="#results" hx-select="#results > *" hx-swap="innerHTML"`)
	assert.Contains(t, form, `hx-push-url="true"`)
	assert.Contains(t, form, `hx-indicator="#searching"`)
	assert.NotContains(t, form, `<ol class="results">`)
	assert.NotContains(t, form, `class="header-search"`, "the page is its own search box")
	assert.Contains(t, form, `<button class="btn btn-primary" type="submit" aria-label="Search">`,
		"named where a phone shows only its icon")

	body := getPage(t, srv, "/ui/search?q=kafka", http.StatusOK)
	assert.Contains(t, body, `value="kafka"`)
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

	// htmx asks for the same page a plain GET gets, and selects its results.
	took := regexp.MustCompile(`· \d+ ms`)
	htmx := getWith(t, srv, "/ui/search?q=kafka", http.Header{"Hx-Request": {"true"}, "Hx-Current-Url": {srv.URL + "/ui/search"}})
	require.Equal(t, http.StatusOK, htmx.status)
	assert.Equal(t, took.ReplaceAllString(body, ""), took.ReplaceAllString(htmx.body, ""))

	assert.Contains(t, getPage(t, apitest.Start(t), "/ui/search?q=kafka", http.StatusOK),
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

	body := getPage(t, srv, "/ui/search?q=kafka", http.StatusOK)
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

// TestUI_SearchEmptyQuery: without a query the page is the form alone,
// and the engine isn't asked.
func TestUI_SearchEmptyQuery(t *testing.T) {
	calls, mu := 0, new(sync.Mutex)
	srv := apitest.Start(t, func(d *api.Deps) {
		emb := countingEmbedder{Embedder: apitest.Embedder{Dim: config.Default().Embedding.Dim}, calls: &calls, mu: mu}
		d.Search = search.New(d.Chunks, d.Documents, emb, search.Config{Log: slog.New(slog.DiscardHandler)})
	})
	for _, q := range []string{"", "?q=", "?q=%20%20"} {
		getPage(t, srv, "/ui/search"+q, http.StatusOK)
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, calls)
}

// moreRE finds the Library's next-page link.
var moreRE = regexp.MustCompile(`<div class="load-more" id="more"><a class="btn" href="([^"]+)"`)

// docLinkRE finds the documents a list links to, by their names.
var docLinkRE = regexp.MustCompile(`<a class="doc-title(?: untitled)?" href="/ui/documents/([^"]+)"`)

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

	all := getPage(t, srv, "/ui/library", http.StatusOK)
	for _, doc := range []*store.Document{paper, failedDoc, other} {
		assert.Contains(t, all, `href="/ui/documents/`+doc.ID+`"`)
	}
	assert.Contains(t, all, `<span class="doc-error" title="HTTP 503 from the origin"><span class="err-cause">Other</span> `+
		`<span class="msg">HTTP 503 from the origin</span></span>`)
	assert.Contains(t, all, `<option value="">Any state</option>`)
	assert.Contains(t, all, `<option value="">Any type</option>`)
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
	onlyFailed := filtered("state=failed")
	assert.Contains(t, onlyFailed, failedDoc.ID)
	assert.NotContains(t, onlyFailed, paper.ID)
	inFolder := filtered("folder=/Research")
	assert.Contains(t, inFolder, paper.ID)
	assert.NotContains(t, inFolder, failedDoc.ID)
	none := filtered("host=nothing.example")
	assert.Contains(t, none, "<h2>No documents match</h2>")
	assert.Contains(t, none, "host <code>nothing.example</code>")
	assert.Contains(t, none, `<a class="btn" href="/ui/library">Clear filters</a>`)

	for _, query := range []string{"state=bogus", "content_type=bogus", "cursor=not-a-cursor"} {
		body := getPage(t, srv, "/ui/library?"+query, http.StatusBadRequest)
		assert.Contains(t, body, `<div class="big-code">400</div>`+"\n<h1>bad request</h1>", query)
		assert.Contains(t, body, `<a class="btn btn-primary" href="/ui/library">Start over</a>`, query)
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

// TestUI_LibraryPages: following the next-page link one row at a time
// visits every matching document once, with the filters kept, the cause
// among them, which the page has no control for yet.
func TestUI_LibraryPages(t *testing.T) {
	srv := apitest.Start(t)
	byHost, byCause := map[string]bool{}, map[string]bool{}
	for i := range 5 {
		byHost[srv.AddDocument(t, fmt.Sprintf("https://keep.example/%d", i), store.DocStateFetched).ID] = true
		srv.AddDocument(t, fmt.Sprintf("https://skip.example/%d", i), store.DocStateFetched)
		byCause[srv.AddFailedDocument(t, fmt.Sprintf("https://blocked.example/%d", i), store.FailureCauseAntiBot).ID] = true
		srv.AddFailedDocument(t, fmt.Sprintf("https://walled.example/%d", i), store.FailureCauseLoginWall)
	}
	for _, tc := range []struct {
		filter string
		want   map[string]bool
	}{{"host=keep.example", byHost}, {"cause=anti_bot", byCause}} {
		t.Run(tc.filter, func(t *testing.T) {
			got := map[string]int{}
			path := "/ui/library?" + tc.filter + "&limit=1"
			for range 10 {
				body := getPage(t, srv, path, http.StatusOK)
				ids := docLinkRE.FindAllStringSubmatch(body, -1)
				require.Len(t, ids, 1, path)
				got[ids[0][1]]++
				m := moreRE.FindStringSubmatch(body)
				if m == nil {
					break
				}
				path = html.UnescapeString(m[1])
				assert.Contains(t, path, tc.filter)
				assert.Contains(t, path, "limit=1")
				assert.Contains(t, body, `hx-select-oob="#more">Load 1 more</a></div>`)
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

	getPage(t, srv, "/ui/documents/no-such-document", http.StatusNotFound)
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
	_, err := srv.DB.Exec(`UPDATE clusters SET size = 150 WHERE id = ?`, interest.ID)
	require.NoError(t, err)

	list := getPage(t, srv, "/ui/interests", http.StatusOK)
	assert.Contains(t, list, `<a href="/ui/interests/`+interest.ID+`">Stream &lt;processing&gt;</a>`)
	assert.Contains(t, list, "<b>150</b> documents")
	assert.Contains(t, list, `<meter class="meter" min="0" max="1" value="0.90">0.90</meter>0.90</span>`, "cohesion")
	assert.Contains(t, list, `<a href="/ui/documents/`+a.ID+`" title="Kafka partitions">Kafka partitions</a>`)
	assert.Contains(t, list, "1 topic curio found in your library, largest first.</p>", "the run's count, all shown")
	assert.Contains(t, list, `title="https://example.com/c">example.com/c</a>`, "a card lists 3 members")
	assert.NotContains(t, list, "example.com/d", "and no more")

	one := getPage(t, srv, "/ui/interests/"+interest.ID, http.StatusOK)
	assert.Contains(t, one, "<h1>Stream &lt;processing&gt;</h1>")
	assert.Contains(t, one, "showing 4 of 150")
	assert.Contains(t, one, `<a class="doc-title untitled" href="/ui/documents/`+b.ID+
		`" title="https://example.com/b">example.com/b</a>`)
	assert.Contains(t, one, `<td class="num muted">2</td>`, "ranked")
	assert.Contains(t, one, `</meter>0.80</span></td>`)

	getPage(t, srv, "/ui/interests/no-such-interest", http.StatusNotFound)
	_, err = srv.DB.Exec(`UPDATE clusters SET tenant_id = 'other' WHERE id = ?`, interest.ID)
	require.NoError(t, err)
	getPage(t, srv, "/ui/interests/"+interest.ID, http.StatusNotFound)
}

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

// TestUI_GETNeverWrites: crawling every page, with every parameter they
// take, changes nothing in the database.
func TestUI_GETNeverWrites(t *testing.T) {
	srv := apitest.Start(t)
	doc := titled(t, srv, "https://example.com/a", "Kafka", store.DocStateFetched)
	srv.AddContent(t, doc, hostileMarkdown)
	failed := srv.AddDocument(t, "https://example.com/failed", store.DocStateFailed)
	failJob(t, srv, failed, "HTTP 503")
	bookmark(t, srv, doc.URL, store.SourceChrome, "/Reading")
	interest := srv.AddInterest(t, "Kafka", doc)
	_, err := srv.Deps.Gate.Update(context.Background(), jobsPause())
	require.NoError(t, err)
	before := fingerprint(t, srv)

	overview := getPage(t, srv, "/ui/", http.StatusOK)
	cursor := html.UnescapeString(moreRE.FindStringSubmatch(getPage(t, srv, "/ui/library?limit=1", http.StatusOK))[1])
	for _, path := range []string{
		"/", "/ui/", "/ui", "/ui/search", "/ui/search?q=kafka", "/ui/library", "/ui/library?state=failed",
		"/ui/library?content_type=article&host=example.com&folder=/Reading", cursor, "/ui/library?state=bogus",
		"/ui/documents/" + doc.ID, "/ui/documents/" + doc.ID + "?images=1", "/ui/documents/" + failed.ID,
		"/ui/documents/nope", "/ui/interests", "/ui/interests/" + interest.ID, "/ui/nope",
		assetRE.FindStringSubmatch(overview)[1], "/ui/static/nope.css",
	} {
		p := get(t, srv, path)
		assert.Less(t, p.status, http.StatusInternalServerError, path)
	}
	assert.Equal(t, before, fingerprint(t, srv))
}
