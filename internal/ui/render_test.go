package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"iter"
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

	"github.com/samsar/curio/internal/store"
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
	layout := func(nav Nav) Layout {
		return Layout{Title: evilScript, Nav: nav, Version: evilQuotes, Listen: sampleListen}
	}
	panelErr := &PanelError{Message: evilScript, RequestID: evilAttr}
	text, err := r.RenderMarkdown([]byte("# "+evilScript+"\n\n<img src=x onerror=alert(1)> [x]("+evilURL+
		") ![alt "+evilAttr+"](https://img.example/a.png)\n"), "https://example.com/post", false)
	require.NoError(t, err)
	member := Member{DocumentID: evilAttr, Title: evilScript, URL: evilURL, Similarity: 0.8}
	interest := Interest{ID: evilAttr, Label: evilScript, Summary: evilQuotes, Size: 7, Cohesion: 0.7,
		Members: []Member{member, {DocumentID: "doc", URL: "https://example.com/" + evilQuotes}}}

	upstream := func(name, state string, enabled bool) Upstream {
		return Upstream{Name: name, Enabled: enabled, State: state, LastSuccess: at, LastFailure: at,
			LastFailureClass: evilQuotes, CooldownUntil: at, Window: 15 * time.Minute, Calls: 20, Failed: 6}
	}

	return map[string]any{
		PageStatus: Status{
			Layout: layout(NavStatus),
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
				Upstreams: []Upstream{upstream(evilScript, "failing", true), upstream("jina", "degraded", true),
					upstream(evilAttr, "paused", true), upstream("jina", "failing", false),
					upstream(evilQuotes, evilAttr, true), {Name: "jina", Enabled: true, State: "failing"}}},
			Failures: FailuresPanel{Total: 17, Causes: []Count{{Name: evilScript, Count: 9},
				{Name: string(store.FailureCauseAntiBot), Count: 5}, {Name: string(store.FailureCauseDeadLink), Count: 3}}},
		},
		PageSearch: Search{
			Layout: layout(NavSearch),
			Query:  evilAttr,
			Type:   evilAttr,
			Err:    panelErr,
			Results: &SearchResults{Degraded: true, Warnings: []string{evilScript}, TookMS: 12, Hits: []SearchHit{{
				DocumentID: evilAttr, Title: evilScript, URL: evilURL, ContentType: evilAttr, Score: 0.03,
				Matches: []Match{
					{Segments: Highlight("before <em>" + evilScript + "</em> after " + evilAttr), BM25: new(1.5)},
					{Segments: []Segment{{Text: Excerpt(evilQuotes+evilScript, 300)}}, Vector: new(0.7)},
					{Segments: Highlight(evilScript + " <em>third</em>"), BM25: new(0.5), Vector: new(0.2)},
				},
			}, {DocumentID: "doc", URL: "https://example.com/" + evilQuotes + "/a/b?q=" + evilScript,
				ContentType: "video"}}},
		},
		PageLibrary: Library{
			Layout: layout(NavLibrary),
			Filters: LibraryFilters{State: evilAttr, ContentType: evilScript, Host: evilQuotes, Folder: evilURL,
				Cause: evilScript, Limit: 7},
			Counts: &LibraryCounts{Documents: 3, Bookmarks: 4, ByState: map[string]int{evilAttr: 1}},
			Shown:  50,
			Rows: []LibraryRow{
				{DocumentID: evilAttr, Title: evilScript, URL: evilURL, State: evilQuotes, ContentType: evilAttr,
					UpdatedAt: at, LastError: evilScript, FailureCause: evilAttr},
				{DocumentID: "doc", URL: "https://example.com/" + evilQuotes, State: "failed", ContentType: "pdf",
					UpdatedAt: at, LastError: "permanent failure: native: " + evilScript, FailureCause: "anti_bot"},
			},
			NextCursor: evilAttr,
			PageSize:   7,
		},
		PageDocument: Document{
			Layout: layout(NavLibrary),
			Meta: DocumentMeta{ID: evilAttr, Title: evilScript, URL: evilURL, CanonicalURL: evilURL,
				ContentType: evilQuotes, State: evilAttr, Author: evilScript, Language: evilQuotes, PublishedAt: at,
				WordCount: 42, CreatedAt: at, UpdatedAt: at, MarkdownPath: evilScript},
			Extraction: &Extraction{Fetcher: evilScript, Via: "jina", Status: evilAttr, ErrorMessage: evilQuotes,
				FetchedAt: at},
			LastError:    evilScript,
			FailureCause: evilAttr,
			Text: TextPanel{State: TextShown, Text: text, Truncated: true, MarkdownPath: evilAttr,
				OfferImages: true},
			Related: RelatedPanel{Docs: []RelatedDoc{{DocumentID: evilAttr, Title: evilScript, URL: evilURL, Score: 0.9}}},
			Bookmarks: BookmarksPanel{Bookmarks: []DocumentBookmark{{Source: evilScript, Folder: evilAttr,
				Title: evilQuotes, Tags: []string{evilScript, evilURL}, SavedAt: at}}},
		},
		PageInterests: Interests{
			Layout:    layout(NavInterests),
			Run:       &InterestRun{ComputedAt: at, Algo: evilScript, Documents: 40, Noise: 3, Interests: 9},
			Interests: []Interest{interest, {ID: "unlabeled", Size: 1}},
		},
		PageInterest: InterestPage{Layout: layout(NavInterests), Interest: interest},
		PageError: ErrorPage{Layout: layout(NavNone), Status: http.StatusBadRequest, Title: evilScript,
			Message: evilAttr, RequestID: evilQuotes, Retry: NavLibrary},
		PageStarting: Starting{Layout: layout(NavNone), Phase: evilScript, Migrating: true, Applied: 1, Total: 6},
	}
}

// sampleVariants are more samples of the pages with branches their sample
// can't take: the search home, with interests and without, Status with
// every panel failed, and the Library with counts for its tabs and without
// counts.
func sampleVariants(t testing.TB) map[string][]any {
	t.Helper()
	at := time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)
	layout := func(nav Nav) Layout {
		return Layout{Title: evilScript, Nav: nav, Version: evilQuotes, Listen: sampleListen}
	}
	panelErr := &PanelError{Message: evilScript, RequestID: evilAttr}
	row := LibraryRow{DocumentID: evilAttr, Title: evilScript, URL: evilURL, State: evilQuotes, ContentType: evilAttr,
		UpdatedAt: at}
	counts := &LibraryCounts{Documents: 7467, Bookmarks: 7497, ByState: map[string]int{"fetched": 4498, evilAttr: 2}}
	return map[string][]any{
		PageSearch: {
			Search{Layout: layout(NavSearch), Type: "pdf", Home: &SearchHome{Searchable: 4498, AllInterests: 222,
				Interests: []Interest{
					{ID: evilAttr, Label: evilScript, Size: 90},
					{ID: "long", Label: strings.Repeat("W", 200), Size: 80},
					{ID: "unlabeled", Size: 70},
					{ID: "cut", Label: "Mobile Ecosystems and " + evilScript, Size: 60},
					{ID: "whole", Label: evilQuotes + " and " + evilAttr, Size: 50},
				}}},
			Search{Layout: layout(NavSearch), Query: " ", Home: &SearchHome{}},
		},
		PageStatus: {Status{Layout: layout(NavStatus), Counts: CountsPanel{Err: panelErr},
			Queue: QueuePanel{Err: panelErr}, Progress: ProgressPanel{Err: panelErr}, Health: HealthPanel{Err: panelErr},
			Failures: FailuresPanel{Err: panelErr}}},
		PageLibrary: {
			Library{Layout: layout(NavLibrary), Filters: LibraryFilters{State: evilAttr, Limit: 7}, Counts: counts,
				Rows: []LibraryRow{row}, NextCursor: evilScript, PageSize: 7, Shown: 3},
			Library{Layout: layout(NavLibrary), Filters: LibraryFilters{State: "fetched"}, Counts: counts,
				Rows: []LibraryRow{row}, PageSize: 50},
			Library{Layout: layout(NavLibrary), Rows: []LibraryRow{row}, NextCursor: evilAttr, PageSize: 50},
		},
	}
}

// allSamples are every page's samples: its sample, then its variants.
func allSamples(t testing.TB, r *Renderer) map[string][]any {
	t.Helper()
	all := map[string][]any{}
	for page, sample := range samples(t, r) {
		all[page] = []any{sample}
	}
	for page, variants := range sampleVariants(t) {
		all[page] = append(all[page], variants...)
	}
	return all
}

// sampleListen is the samples' daemon address.
const sampleListen = "127.0.0.1:8765"

// partialSamples are the view models of the partials the page sets hold,
// each with every shape of data it takes: every icon, a known one or not,
// and hostile values in every string.
func partialSamples(t testing.TB) map[string][]any {
	t.Helper()
	at := time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)
	names := append(iconNames(t), "no-such-icon", evilScript)
	icons := make([]any, 0, len(names))
	for _, name := range names {
		icons = append(icons, name)
	}
	return map[string][]any{
		"panel-error": {&PanelError{Message: evilScript, RequestID: evilAttr}},
		"icon":        icons,
		"logo":        {nil},
		"icons.html":  {nil},
		"reltime":     {time.Time{}, at},
		"doc-title": {DocRef{ID: evilAttr, Title: evilScript, URL: evilURL}, DocRef{ID: evilAttr, URL: evilURL},
			DocRef{Title: evilScript, URL: evilURL}},
		"doc-cell": {DocCell{Ref: DocRef{ID: evilAttr, Title: evilScript, URL: evilURL}, Where: evilQuotes,
			Source: evilScript, ContentType: evilAttr, When: at, FailureCause: evilScript, LastError: evilAttr},
			DocCell{Ref: DocRef{URL: evilURL}}},
		"state-badge": {evilScript, "dead"},
		"stackbar": {stateBar([]Count{{Name: "fetched", Count: 2}, {Name: "failed", Count: 1}}),
			[]BarSegment{{Class: evilAttr, X: evilScript, Width: evilQuotes}}},
		"hbar": {causeBar("dead_link", 819, 926), []BarSegment(nil),
			[]BarSegment{{Class: evilAttr, X: evilScript, Width: evilQuotes}}},
		"upstream-failure": {Upstream{Name: evilScript, LastFailure: at, LastFailureClass: evilScript}, Upstream{}},
		"full-error":       {evilScript, ""},
		"match":            {Match{Segments: Highlight(evilScript + " <em>" + evilAttr + "</em>"), BM25: new(1.5)}},
		"cohesion":         {0.5},
	}
}

// TestEveryTemplateRenders executes every template each page's set
// defines, the page and the partials alike, with a typed sample whose
// strings are hostile, and checks that each output is inert. A template
// without a sample fails: a new one needs one here.
func TestEveryTemplateRenders(t *testing.T) {
	r := newRenderer(t)
	pageSamples := allSamples(t, r)
	partials := partialSamples(t)
	require.ElementsMatch(t, pageNames, keys(samples(t, r)), "a sample for every page")
	for _, page := range pageNames {
		set := r.pages[page]
		for _, tmpl := range set.Templates() {
			name := tmpl.Name()
			t.Run(page+"/"+name, func(t *testing.T) {
				data, ok := partials[name]
				if !ok && pageTemplate(page, name) {
					data, ok = pageSamples[page], true
				}
				require.True(t, ok, "template %q of the %s page has no sample", name, page)
				for _, d := range data {
					var buf bytes.Buffer
					require.NoError(t, set.ExecuteTemplate(&buf, name, d))
					out := buf.String()
					uitest.AssertInert(t, out)
					assert.NotContains(t, out, evilScript, "escaped")
					assert.NotContains(t, out, `onerror="alert`, "escaped")
				}
			})
		}
	}
}

// pageTemplate reports whether name is one of page's own templates: the
// layout and its blocks, which take the page's view model, and the
// template of the page's file, which holds its defines.
func pageTemplate(page, name string) bool {
	switch name {
	case "layout", "head", "header-search", "content", "document-actions", "layout.html", page + ".html":
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
	// Status's template reads fields a Search doesn't have.
	err := r.Page(w, http.StatusOK, PageStatus, Search{Layout: Layout{Title: "t"}}, CSP)
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

// TestPages_Layout: every page has the viewport meta, a skip link to its
// content, the navigation, the header's search box unless the page can't
// use one, and a footer naming the daemon and where it listens; it loads
// the stylesheet, which themes for dark mode.
func TestPages_Layout(t *testing.T) {
	r := newRenderer(t)
	for page, data := range eachSample(t, r) {
		out := render(t, r, page, data)
		assert.Contains(t, out, `<meta name="viewport" content="width=device-width, initial-scale=1">`, page)
		assert.Contains(t, out, `<a class="skip-link" href="#main">Skip to content</a>`, page)
		assert.Contains(t, out, `<main id="main" class="page">`, page)
		assert.Contains(t, out, `<nav aria-label="Main">`, page)
		assert.Contains(t, out, `<a class="brand" href="/ui/" aria-label="curio: search">`, page)
		const headerSearch = `<form class="header-search" action="/ui/" method="get" role="search">`
		if page != PageSearch && page != PageStarting {
			assert.Equal(t, 1, strings.Count(out, headerSearch), "%s: the header's search box", page)
			assert.Equal(t, 1, strings.Count(out, ` name="q"`), "%s: the header's search box sends only q", page)
		} else {
			assert.NotContains(t, out, `class="header-search"`, page)
		}
		assert.Contains(t, out, `<footer class="site"><div class="container"><span>curio-daemon &#39;&#34;&lt;&gt;&amp;</span>`+
			`<span>`+sampleListen+` · this Mac only</span></div></footer>`, page)
	}

	unknown := samples(t, r)[PageError].(ErrorPage)
	unknown.Layout.Listen = ""
	assert.Contains(t, render(t, r, PageError, unknown),
		`<footer class="site"><div class="container"><span>curio-daemon &#39;&#34;&lt;&gt;&amp;</span></div></footer>`,
		"the footer without an address")

	css, ok := r.assets.files[r.assets.hashed["app.css"]]
	require.True(t, ok)
	assert.Contains(t, string(css.body), "@media (prefers-color-scheme: dark)")
	assert.Contains(t, string(css.body), "color-scheme: light dark")
}

// TestPages_Navigation: the navigation links to every page, in its order,
// the current one marked, and to no page when none is current.
func TestPages_Navigation(t *testing.T) {
	r := newRenderer(t)
	navRE := regexp.MustCompile(`<a href="([^"]+)"( aria-current="page")?><svg class="icon"`)
	for page, data := range eachSample(t, r) {
		var hrefs, current []string
		for _, m := range navRE.FindAllStringSubmatch(render(t, r, page, data), -1) {
			hrefs = append(hrefs, m[1])
			if m[2] != "" {
				current = append(current, m[1])
			}
		}
		assert.Equal(t, []string{"/ui/", "/ui/library", "/ui/interests", "/ui/status"}, hrefs, page)
		switch page {
		case PageSearch:
			assert.Equal(t, []string{"/ui/"}, current, page)
		case PageStatus:
			assert.Equal(t, []string{"/ui/status"}, current, page)
		case PageLibrary, PageDocument:
			assert.Equal(t, []string{"/ui/library"}, current, page)
		case PageInterests, PageInterest:
			assert.Equal(t, []string{"/ui/interests"}, current, page)
		default:
			assert.Empty(t, current, page)
		}
	}
}

// eachSample yields every sample of every page, variants included.
func eachSample(t *testing.T, r *Renderer) iter.Seq2[string, any] {
	t.Helper()
	all := allSamples(t, r)
	return func(yield func(string, any) bool) {
		for page, samples := range all {
			for _, data := range samples {
				if !yield(page, data) {
					return
				}
			}
		}
	}
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
	for page, data := range eachSample(t, r) {
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
