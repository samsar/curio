package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/ui/uitest"
)

// Hostile values for the samples' strings: markup, an attribute breakout,
// a script URL, and quotes and angle brackets.
const (
	evilScript = `<script>alert(1)</script>`
	evilAttr   = `" onerror="alert(1)`
	evilURL    = `javascript:alert(1)`
	evilQuotes = `'"<>&`
)

func newRenderer(t testing.TB) *Renderer {
	t.Helper()
	r, err := New()
	require.NoError(t, err)
	return r
}

// samples are a typed view model for every page, each with hostile values
// in its strings and every optional part set, so every branch that shows
// stored content runs.
func samples(t testing.TB, r *Renderer) map[string]any {
	t.Helper()
	at := time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)
	layout := func(nav Nav) Layout { return Layout{Title: evilScript, Nav: nav, Version: evilQuotes} }
	panelErr := &PanelError{Message: evilScript, RequestID: evilAttr}
	text, err := r.RenderMarkdown([]byte("# "+evilScript+"\n\n<img src=x onerror=alert(1)> [x]("+evilURL+
		") ![alt "+evilAttr+"](https://img.example/a.png)\n"), "https://example.com/post", false)
	require.NoError(t, err)
	member := Member{DocumentID: evilAttr, Title: evilScript, URL: evilURL, Similarity: 0.8}
	interest := Interest{ID: evilAttr, Label: evilScript, Summary: evilQuotes, Size: 7, Cohesion: 0.7,
		Members: []Member{member, {DocumentID: "doc", URL: "https://example.com/" + evilQuotes}}}

	return map[string]any{
		PageOverview: Overview{
			Layout: layout(NavOverview),
			Counts: CountsPanel{Documents: 3, Bookmarks: 4,
				ByState: []Count{{Name: evilAttr, Count: 1}, {Name: "fetched", Count: 2}},
				Jobs:    []Count{{Name: evilScript, Count: 5}}},
			Queue: QueuePanel{Open: false, Reason: evilScript, OpensAt: at, Throttle: evilQuotes,
				Schedule: evilAttr, Kinds: []KindLoad{{Kind: evilScript, Running: 1, Limit: 4, Pending: 9}},
				KeepAwake: true, KeepAwakeActive: true, PowerSource: evilQuotes},
			Progress: ProgressPanel{Progress: EstimateProgress([]KindWork{{Kind: "fetch", Pending: 30, Finished: 12}},
				ProgressWindow, true)},
			Health: HealthPanel{Version: evilQuotes, OllamaDetail: evilScript, EmbeddingModel: evilAttr,
				EmbeddingDim: 1024, GenerationModel: evilScript, YouTubeFetcher: evilAttr,
				Drift: &Drift{Changes: []DriftChange{{What: evilScript, Recorded: evilAttr, Current: evilURL}},
					Fix: evilQuotes},
				Upstreams: []Upstream{{Name: evilScript, Enabled: true, State: evilAttr, LastSuccess: at,
					LastFailure: at, LastFailureClass: evilQuotes, CooldownUntil: at}}},
			Recent: RecentPanel{Bookmarks: []RecentBookmark{
				{Title: evilScript, URL: evilURL, Source: evilAttr, SavedAt: at, DocumentID: evilAttr, DocumentState: evilQuotes},
				{URL: evilURL, Source: "chrome", SavedAt: at},
			}},
		},
		PageSearch: Search{
			Layout: layout(NavSearch),
			Query:  evilAttr,
			Err:    panelErr,
			Results: &SearchResults{Degraded: true, Warnings: []string{evilScript}, TookMS: 12, Hits: []SearchHit{{
				DocumentID: evilAttr, Title: evilScript, URL: evilURL, Score: 0.03,
				Matches: []Match{
					{Segments: Highlight("before <em>" + evilScript + "</em> after " + evilAttr), BM25: new(1.5)},
					{Segments: []Segment{{Text: Excerpt(evilQuotes+evilScript, 300)}}, Vector: new(0.7)},
				},
			}, {DocumentID: "doc", URL: "https://example.com/" + evilQuotes}}},
		},
		PageLibrary: Library{
			Layout:  layout(NavLibrary),
			Filters: LibraryFilters{State: evilAttr, ContentType: evilScript, Host: evilQuotes, Folder: evilURL, Limit: 7},
			Rows: []LibraryRow{{DocumentID: evilAttr, Title: evilScript, URL: evilURL, State: evilQuotes,
				ContentType: evilAttr, UpdatedAt: at, LastError: evilScript}},
			NextCursor: evilAttr,
		},
		PageDocument: Document{
			Layout: layout(NavLibrary),
			Meta: DocumentMeta{ID: evilAttr, Title: evilScript, URL: evilURL, CanonicalURL: evilURL,
				ContentType: evilQuotes, State: evilAttr, Author: evilScript, Language: evilQuotes, PublishedAt: at,
				WordCount: 42, CreatedAt: at, UpdatedAt: at, MarkdownPath: evilScript},
			Extraction: &Extraction{Fetcher: evilScript, Via: "jina", Status: evilAttr, ErrorMessage: evilQuotes,
				FetchedAt: at},
			LastError: evilScript,
			Text: TextPanel{State: TextShown, Text: text, Truncated: true, MarkdownPath: evilAttr,
				OfferImages: true},
			Related: RelatedPanel{Docs: []RelatedDoc{{DocumentID: evilAttr, Title: evilScript, URL: evilURL, Score: 0.9}}},
			Bookmarks: BookmarksPanel{Bookmarks: []DocumentBookmark{{Source: evilScript, Folder: evilAttr,
				Title: evilQuotes, Tags: []string{evilScript, evilURL}, SavedAt: at}}},
		},
		PageInterests: Interests{
			Layout:    layout(NavInterests),
			Run:       &InterestRun{ComputedAt: at, Algo: evilScript, Documents: 40, Noise: 3},
			Interests: []Interest{interest, {ID: "unlabeled", Size: 1}},
		},
		PageInterest: InterestPage{Layout: layout(NavInterests), Interest: interest},
		PageError: ErrorPage{Layout: layout(NavNone), Status: http.StatusBadRequest, Title: evilScript,
			Message: evilAttr, RequestID: evilQuotes, Retry: NavLibrary},
		PageStarting: Starting{Layout: layout(NavNone), Phase: evilScript, Migrating: true, Applied: 1, Total: 6},
	}
}

// partialSamples are the view models of the partials every page set
// shares.
var partialSamples = map[string]any{
	"panel-error": &PanelError{Message: evilScript, RequestID: evilAttr},
}

// TestEveryTemplateRenders executes every template each page's set
// defines, the page and the partials alike, with a typed sample whose
// strings are hostile, and checks that each output is inert. A template
// without a sample fails: a new one needs one here.
func TestEveryTemplateRenders(t *testing.T) {
	r := newRenderer(t)
	pageSamples := samples(t, r)
	require.ElementsMatch(t, pageNames, keys(pageSamples), "a sample for every page")
	for _, page := range pageNames {
		set := r.pages[page]
		for _, tmpl := range set.Templates() {
			name := tmpl.Name()
			t.Run(page+"/"+name, func(t *testing.T) {
				data, ok := partialSamples[name]
				if !ok {
					data, ok = pageSamples[page], pageTemplate(page, name)
				}
				require.True(t, ok, "template %q of the %s page has no sample", name, page)
				var buf bytes.Buffer
				require.NoError(t, set.ExecuteTemplate(&buf, name, data))
				out := buf.String()
				uitest.AssertInert(t, out)
				assert.NotContains(t, out, evilScript, "escaped")
				assert.NotContains(t, out, `onerror="alert`, "escaped")
			})
		}
	}
}

// pageTemplate reports whether name is one of page's own templates: the
// layout and its blocks, which take the page's view model, and the
// template of the page's file, which holds its defines.
func pageTemplate(page, name string) bool {
	switch name {
	case "layout", "head", "content", "document-actions", "layout.html", page + ".html":
		return true
	}
	return false
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPage_WritesNothingOnFailure: a template that fails part way writes
// nothing, not a status and half a page.
func TestPage_WritesNothingOnFailure(t *testing.T) {
	r := newRenderer(t)
	w := httptest.NewRecorder()
	// The overview's template reads fields a Search doesn't have.
	err := r.Page(w, http.StatusOK, PageOverview, Search{Layout: Layout{Title: "t"}}, CSP)
	require.Error(t, err)
	assert.False(t, w.Flushed)
	assert.Zero(t, w.Body.Len(), "nothing written")
	assert.Empty(t, w.Header(), "no headers set")

	require.Error(t, r.Page(w, http.StatusOK, "no-such-page", nil, CSP))
}

// TestPage_Headers: a page is HTML with the CSP it was given and its
// length.
func TestPage_Headers(t *testing.T) {
	r := newRenderer(t)
	w := httptest.NewRecorder()
	page := samples(t, r)[PageError]
	require.NoError(t, r.Page(w, http.StatusNotFound, PageError, page, CSPWithImages))
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, CSPWithImages, w.Header().Get("Content-Security-Policy"))
	assert.Equal(t, strconv.Itoa(w.Body.Len()), w.Header().Get("Content-Length"))
}

// TestPages_Layout: every page has the viewport meta and loads the
// stylesheet, which themes for dark mode.
func TestPages_Layout(t *testing.T) {
	r := newRenderer(t)
	for page, data := range samples(t, r) {
		out := render(t, r, page, data)
		assert.Contains(t, out, `<meta name="viewport" content="width=device-width, initial-scale=1">`, page)
		assert.Contains(t, out, `<nav aria-label="Main">`, page)
		assert.Contains(t, out, "curio-daemon ", page)
	}
	css, ok := r.assets.files[r.assets.hashed["app.css"]]
	require.True(t, ok)
	assert.Contains(t, string(css.body), "@media (prefers-color-scheme: dark)")
	assert.Contains(t, string(css.body), "color-scheme: light dark")
}

func render(t *testing.T, r *Renderer, page string, data any) string {
	t.Helper()
	w := httptest.NewRecorder()
	require.NoError(t, r.Page(w, http.StatusOK, page, data, CSP))
	return w.Body.String()
}

// assetRefRE finds the asset URLs a page references.
var assetRefRE = regexp.MustCompile(`(?:href|src)="(/ui/static/[^"]+)"`)

// TestAssets: every asset a page references is served, with its type and
// cached for good; a name that isn't an asset's, a stale hash included,
// is not.
func TestAssets(t *testing.T) {
	r := newRenderer(t)
	var refs []string
	for page, data := range samples(t, r) {
		for _, m := range assetRefRE.FindAllStringSubmatch(render(t, r, page, data), -1) {
			if !slices.Contains(refs, m[1]) {
				refs = append(refs, m[1])
			}
		}
	}
	require.Len(t, refs, 2, "the stylesheet and htmx")
	types := map[string]string{".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8"}
	for _, ref := range refs {
		file := strings.TrimPrefix(ref, AssetPrefix)
		assert.Regexp(t, `^[a-z0-9.-]+\.[0-9a-f]{16}\.(css|js)$`, file)
		w := httptest.NewRecorder()
		require.True(t, r.ServeAsset(w, file), ref)
		resp := w.Result()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		sum := sha256.Sum256(body)
		assert.Contains(t, file, hex.EncodeToString(sum[:])[:16], "named after its content")
		assert.Equal(t, types[file[strings.LastIndexByte(file, '.'):]], resp.Header.Get("Content-Type"))
		assert.Equal(t, "public, max-age=31536000, immutable", resp.Header.Get("Cache-Control"))
	}
	for _, name := range []string{"app.css", "app.0000000000000000.css", "nope.js", ""} {
		assert.False(t, r.ServeAsset(httptest.NewRecorder(), name), name)
	}
}
