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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
}

func TestUI_Overview(t *testing.T) {
	srv := apitest.Start(t)
	fetched := titled(t, srv, "https://example.com/fetched", "A fetched page", store.DocStateFetched)
	failed := srv.AddDocument(t, "https://example.com/failed", store.DocStateFailed)
	failJob(t, srv, failed, "HTTP 503")
	bookmark(t, srv, fetched.URL, store.SourceChrome, "/Reading")
	bookmark(t, srv, "https://example.com/new", store.SourceSafari, "")

	body := getPage(t, srv, "/ui/", http.StatusOK)
	assert.Contains(t, body, "3 documents · 2 bookmarks")
	for _, state := range []string{"pending", "fetched", "failed", "dead"} {
		assert.Contains(t, body, `<a href="/ui/library?state=`+state+`">`)
	}
	assert.Contains(t, body, `<span class="badge ok">open</span>`)
	assert.Contains(t, body, "<td>fetch</td><td>0 of 16</td><td>1</td>", "the new page's fetch job")
	assert.Contains(t, body, "1 jobs queued, and none finished in the last 10m.")
	assert.Contains(t, body, "qwen3-embedding:0.6b (1024 dimensions)")
	assert.Contains(t, body, `<a href="/ui/documents/`+fetched.ID+`">https://example.com/fetched</a>`)
	assert.Contains(t, body, "https://example.com/new")

	// Paused: the progress says why nothing starts.
	_, err := srv.Deps.Gate.Update(context.Background(), jobsPause())
	require.NoError(t, err)
	body = getPage(t, srv, "/ui/", http.StatusOK)
	assert.Contains(t, body, `<span class="badge warn">closed</span> paused`)
	assert.Contains(t, body, "1 jobs queued. The queue is closed (paused), so none start until it opens.")
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
	assert.Contains(t, p.body, `<span class="badge ok">open</span>`, "the queue panel renders")
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

	form := getPage(t, srv, "/ui/search", http.StatusOK)
	assert.Contains(t, form, `hx-trigger="input changed delay:400ms, search" hx-sync="this:replace"`)
	assert.Contains(t, form, `hx-target="#results" hx-select="#results > *" hx-swap="innerHTML"`)
	assert.Contains(t, form, `hx-push-url="true"`)
	assert.NotContains(t, form, `class="hits"`)

	body := getPage(t, srv, "/ui/search?q=kafka", http.StatusOK)
	assert.Contains(t, body, `value="kafka"`)
	assert.Contains(t, body, `<a href="/ui/documents/`+doc.ID+`">Kafka partitions</a>`)
	assert.Contains(t, body, `<a href="https://example.com/kafka" rel="noopener noreferrer" target="_blank">`)
	assert.Contains(t, body, "<mark>kafka</mark>")
	assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;", "the chunk's markup is text")
	assert.Regexp(t, `bm25 \d+\.\d{3}`, body)
	assert.Regexp(t, `vector \d+\.\d{3}`, body)
	assert.NotContains(t, body, "semantic search unavailable")

	// htmx asks for the same page a plain GET gets, and selects its results.
	took := regexp.MustCompile(`in \d+ ms`)
	htmx := getWith(t, srv, "/ui/search?q=kafka", http.Header{"Hx-Request": {"true"}, "Hx-Current-Url": {srv.URL + "/ui/search"}})
	require.Equal(t, http.StatusOK, htmx.status)
	assert.Equal(t, took.ReplaceAllString(body, ""), took.ReplaceAllString(htmx.body, ""))

	assert.Contains(t, getPage(t, apitest.Start(t), "/ui/search?q=kafka", http.StatusOK),
		"Nothing in your library matches.")
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
	assert.Contains(t, body, `<div class="banner warn"><p>semantic search unavailable (`)
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
var moreRE = regexp.MustCompile(`<a class="more" href="([^"]+)"`)

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
		assert.Contains(t, all, `<a href="/ui/documents/`+doc.ID+`">`)
	}
	assert.Contains(t, all, `<td class="error">HTTP 503 from the origin</td>`)
	assert.Contains(t, all, `<option value="">any</option>`)

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
	assert.Contains(t, filtered("host=nothing.example"), "No documents match.")

	for _, query := range []string{"state=bogus", "content_type=bogus", "cursor=not-a-cursor"} {
		body := getPage(t, srv, "/ui/library?"+query, http.StatusBadRequest)
		assert.Contains(t, body, "<h1>400 bad request</h1>", query)
		assert.Contains(t, body, `<a href="/ui/library">Start over</a>`, query)
	}
}

// TestUI_LibraryPages: following the next-page link one row at a time
// visits every matching document once, with the filters kept.
func TestUI_LibraryPages(t *testing.T) {
	srv := apitest.Start(t)
	want := map[string]bool{}
	for i := range 5 {
		doc := srv.AddDocument(t, fmt.Sprintf("https://keep.example/%d", i), store.DocStateFetched)
		want[doc.ID] = true
		srv.AddDocument(t, fmt.Sprintf("https://skip.example/%d", i), store.DocStateFetched)
	}
	idRE := regexp.MustCompile(`<a href="/ui/documents/([^"]+)">`)
	got := map[string]int{}
	path := "/ui/library?host=keep.example&limit=1"
	for range 10 {
		body := getPage(t, srv, path, http.StatusOK)
		ids := idRE.FindAllStringSubmatch(body, -1)
		require.Len(t, ids, 1, path)
		got[ids[0][1]]++
		m := moreRE.FindStringSubmatch(body)
		if m == nil {
			break
		}
		path = html.UnescapeString(m[1])
		assert.Contains(t, path, "host=keep.example")
		assert.Contains(t, path, "limit=1")
		assert.Contains(t, body, `hx-select-oob="#more"`)
	}
	require.Len(t, got, len(want))
	for id, n := range got {
		assert.True(t, want[id], id)
		assert.Equal(t, 1, n, "visited once: %s", id)
	}
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
	assert.Contains(t, body, "<code>"+doc.ID+"</code>")
	assert.Contains(t, body, srv.Home.ContentDir(), "the absolute markdown path")
	assert.Contains(t, body, "by apitest")
	assert.Contains(t, body, `<a href="https://site.example/about" rel="nofollow noreferrer noopener" target="_blank">relative</a>`)
	assert.Contains(t, body, `>[image: diagram]</a>`)
	assert.NotContains(t, body, "<img")
	assert.Contains(t, body, `<a href="/ui/documents/`+doc.ID+`?images=1">Load images</a>`)
	assert.Contains(t, body, `<a href="/ui/documents/`+related.ID+`">Hostile relative</a>`)
	assert.Contains(t, body, "<strong>chrome</strong> in <code>/Reading/Web</code>")
	assert.Contains(t, body, `<span class="tag">&lt;b&gt;tag&lt;/b&gt;</span>`)
	assert.NotContains(t, body, "an old failure", "a fetched document's old failure isn't current")
	assert.Contains(t, body, "<code>curio refetch "+doc.ID+"</code>")

	images := getPageCSP(t, srv, "/ui/documents/"+doc.ID+"?images=1", http.StatusOK, ui.CSPWithImages)
	assert.Contains(t, images, `<img src="https://img.example/d.png" alt="diagram" loading="lazy">`)
	assert.NotContains(t, images, "Load images")
	getPage(t, srv, "/ui/documents/"+doc.ID+"?images=yes", http.StatusOK)

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

// TestUI_DocumentStates: a document without text, one whose text is gone
// from disk, and a failed one with its last error.
func TestUI_DocumentStates(t *testing.T) {
	srv := apitest.Start(t)
	pending := srv.AddDocument(t, "https://example.com/pending", store.DocStatePending)
	assert.Contains(t, getPage(t, srv, "/ui/documents/"+pending.ID, http.StatusOK), "Not fetched yet.")
	assert.Contains(t, getPage(t, srv, "/ui/documents/"+pending.ID, http.StatusOK),
		"None yet: related documents come from its indexed text.")

	gone := srv.AddDocument(t, "https://example.com/gone", store.DocStateFetched)
	ext := srv.AddContent(t, gone, "# Gone")
	require.NoError(t, os.Remove(srv.Home.ContentDir()+"/"+*ext.MarkdownPath))
	assert.Contains(t, getPage(t, srv, "/ui/documents/"+gone.ID, http.StatusOK),
		"The extracted text is missing on disk; refetch the document.")

	for _, state := range []store.DocState{store.DocStateFailed, store.DocStateDead} {
		doc := srv.AddDocument(t, "https://example.com/"+string(state), state)
		failJob(t, srv, doc, "HTTP 404 <from> the origin")
		assert.Contains(t, getPage(t, srv, "/ui/documents/"+doc.ID, http.StatusOK),
			"Last error: HTTP 404 &lt;from&gt; the origin", state)
	}
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
	interest := srv.AddInterest(t, "Stream <processing>", a, b)
	_, err := srv.DB.Exec(`UPDATE clusters SET size = 150 WHERE id = ?`, interest.ID)
	require.NoError(t, err)

	list := getPage(t, srv, "/ui/interests", http.StatusOK)
	assert.Contains(t, list, `<a href="/ui/interests/`+interest.ID+`">Stream &lt;processing&gt;</a>`)
	assert.Contains(t, list, "150 documents · cohesion 0.90")
	assert.Contains(t, list, `<a href="/ui/documents/`+a.ID+`">Kafka partitions</a>`)

	one := getPage(t, srv, "/ui/interests/"+interest.ID, http.StatusOK)
	assert.Contains(t, one, "<h1>Stream &lt;processing&gt;</h1>")
	assert.Contains(t, one, "showing 2 of 150")
	assert.Contains(t, one, `<a href="/ui/documents/`+b.ID+`">https://example.com/b</a>`)
	assert.Contains(t, one, "<td>0.80</td>")

	getPage(t, srv, "/ui/interests/no-such-interest", http.StatusNotFound)
	_, err = srv.DB.Exec(`UPDATE clusters SET tenant_id = 'other' WHERE id = ?`, interest.ID)
	require.NoError(t, err)
	getPage(t, srv, "/ui/interests/"+interest.ID, http.StatusNotFound)
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

	cursor := html.UnescapeString(moreRE.FindStringSubmatch(getPage(t, srv, "/ui/library?limit=1", http.StatusOK))[1])
	for _, path := range []string{
		"/", "/ui/", "/ui", "/ui/search", "/ui/search?q=kafka", "/ui/library", "/ui/library?state=failed",
		"/ui/library?content_type=article&host=example.com&folder=/Reading", cursor, "/ui/library?state=bogus",
		"/ui/documents/" + doc.ID, "/ui/documents/" + doc.ID + "?images=1", "/ui/documents/" + failed.ID,
		"/ui/documents/nope", "/ui/interests", "/ui/interests/" + interest.ID, "/ui/nope",
	} {
		p := get(t, srv, path)
		assert.Less(t, p.status, http.StatusInternalServerError, path)
	}
	assert.Equal(t, before, fingerprint(t, srv))
}
