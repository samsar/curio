package fetcher

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/iotest"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/samsar/curio/internal/store"
)

// makeArticleHTML returns a reasonably article-shaped page so Readability
// will accept it as content. ~600+ chars of body. The page declares UTF-8:
// Readability guesses the encoding of a page that doesn't.
func makeArticleHTML(title, body string) string {
	if body == "" {
		body = strings.Repeat("This is a paragraph of an article. ", 30)
	}
	return `<!DOCTYPE html>
<html><head>
<meta charset="utf-8">
<title>` + title + `</title>
<meta name="author" content="Test Author">
</head><body>
<article>
<h1>` + title + `</h1>
<p>` + body + `</p>
<p>` + body + `</p>
</article>
</body></html>`
}

func TestNative_ReadabilityHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(makeArticleHTML("Test Article", "")))
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second})
	res, err := n.Fetch(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "native", n.Name())
	assert.Equal(t, "Test Article", res.Title)
	assert.Contains(t, res.Markdown, "paragraph of an article")
	assert.Equal(t, "readability", res.Meta["via"])
}

func TestNative_LoginWall_TooShort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Login</title></head>
			<body><article><p>Please sign in.</p></article></body></html>`))
	}))
	defer srv.Close()

	// No fallback so we see the raw error.
	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false})
	_, err := n.Fetch(context.Background(), srv.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLoginWall)
}

func TestNative_LoginWall_TitlePattern(t *testing.T) {
	body := strings.Repeat("Some text here. ", 50) // > minArticleBytes to bypass length check
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Sign in to read this</title></head>
			<body><article><h1>Sign in to read this</h1><p>` + body + `</p></article></body></html>`))
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false})
	_, err := n.Fetch(context.Background(), srv.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLoginWall)
}

func TestNative_LoginWall_RedirectToLoginPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/article", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		body := strings.Repeat("Please log in to continue. ", 50)
		_, _ = w.Write([]byte(`<html><head><title>Log in</title></head>
			<body><article><h1>Welcome back</h1><p>` + body + `</p></article></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false})
	_, err := n.Fetch(context.Background(), srv.URL+"/article")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLoginWall)
}

func TestNative_JinaFallbackOnLoginWall(t *testing.T) {
	// First server: a thin page that triggers the login-wall heuristic.
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><p>nope</p></body></html>`))
	}))
	defer source.Close()

	// Fake Jina: emit a header block + body.
	jinaBody := strings.Repeat("This is the article body from Jina. ", 20)
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Title: Article via Jina\nURL Source: " + source.URL + "\n\nMarkdown Content:\n" + jinaBody))
	}))
	defer jina.Close()

	n := NewNative(NativeOptions{
		Timeout:      5 * time.Second,
		JinaFallback: true,
		JinaBaseURL:  jina.URL + "/",
	})
	res, err := n.Fetch(context.Background(), source.URL)
	require.NoError(t, err)
	assert.Equal(t, "Article via Jina", res.Title)
	assert.Equal(t, "jina", res.Meta["via"])
	assert.Contains(t, res.Markdown, "article body from Jina")
}

func TestNative_HTTPError_NoFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false})
	_, err := n.Fetch(context.Background(), srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}

// TestParseJina reads Jina's real layout, blank lines between the header
// groups included, and keeps every Warning line in order.
func TestParseJina(t *testing.T) {
	in := `Title: Welcome to Python.org

URL Source: https://www.python.org/this-page-does-not-exist-xyz-123/
Published Time: 2024-01-15T10:00:00Z

Warning: Target URL returned error 404: Not Found
Warning: This page maybe requiring CAPTCHA, please make sure you are authorized to access this page.

Markdown Content:
# Heading

This is the body of the article.
`
	got := parseJina(in)
	assert.Equal(t, "Welcome to Python.org", got.title)
	assert.Equal(t, "2024-01-15T10:00:00Z", got.published)
	assert.Equal(t, []string{
		"Target URL returned error 404: Not Found",
		"This page maybe requiring CAPTCHA, please make sure you are authorized to access this page.",
	}, got.warnings)
	assert.Equal(t, "# Heading\n\nThis is the body of the article.", got.body)
}

func TestParseJina_NoMarker(t *testing.T) {
	// Jina sometimes omits the "Markdown Content:" marker.
	in := `Title: x

Body without marker.
More body.`
	got := parseJina(in)
	assert.Equal(t, "x", got.title)
	assert.Contains(t, got.body, "Body without marker")
}

// TestNative_RejectsNonHTMLContentType guards the binary-misrouting fix: a
// URL serving non-HTML, non-PDF content (images, octet-stream) must fail
// with a PermanentError — not get its bytes fed to the HTML parser, and not
// be retried. Regression test for "html: open stack of elements exceeds 512
// nodes". (PDFs are handled by the PDF tier, covered separately.)
func TestNative_RejectsNonHTMLContentType(t *testing.T) {
	for _, ct := range []string{"image/png", "image/jpeg", "application/octet-stream"} {
		t.Run(ct, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", ct)
				_, _ = w.Write([]byte("%PDF-1.7\n\x00\x01\x02 binary, not html"))
			}))
			defer srv.Close()

			n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false})
			_, err := n.Fetch(context.Background(), srv.URL)
			require.Error(t, err)

			var pe *PermanentError
			assert.ErrorAs(t, err, &pe, "non-HTML content must be a permanent (non-retryable) failure")
			assert.Contains(t, err.Error(), "unsupported content type")
		})
	}
}

func TestIsReadableContentType(t *testing.T) {
	for _, ok := range []string{"", "text/html", "text/html; charset=utf-8", "TEXT/HTML", "application/xhtml+xml", "text/plain"} {
		assert.True(t, isReadableContentType(ok), "should allow %q", ok)
	}
	for _, bad := range []string{"application/pdf", "image/png", "application/octet-stream", "application/json", "video/mp4"} {
		assert.False(t, isReadableContentType(bad), "should reject %q", bad)
	}
}

func TestIsPDFResponse(t *testing.T) {
	yes := []struct{ ct, url string }{
		{"application/pdf", "https://x.com/doc"},
		{"application/pdf; charset=binary", "https://x.com/doc"},
		{"application/octet-stream", "https://x.com/file.pdf"}, // vague type + .pdf suffix
		{"", "https://x.com/file.PDF"},                         // no type + .pdf suffix
	}
	for _, c := range yes {
		assert.True(t, isPDFResponse(c.ct, c.url), "want PDF for %+v", c)
	}
	no := []struct{ ct, url string }{
		{"text/html", "https://x.com/page.pdf"}, // declared HTML wins over .pdf suffix
		{"application/octet-stream", "https://x.com/file.zip"},
		{"image/png", "https://x.com/doc"},
		{"", "https://x.com/page"},
	}
	for _, c := range no {
		assert.False(t, isPDFResponse(c.ct, c.url), "want non-PDF for %+v", c)
	}
}

// TestNative_PDF_FallsBackToJina: a PDF that local (pure-Go) extraction
// can't read must fall through to the Jina tier rather than failing.
func TestNative_PDF_FallsBackToJina(t *testing.T) {
	// Source serves application/pdf bytes that ledongthuc can't extract.
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4\nnot a parseable pdf body"))
	}))
	defer source.Close()

	jinaBody := strings.Repeat("This is the PDF text rendered by Jina. ", 20)
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Title: PDF via Jina\nURL Source: " + source.URL + "\n\nMarkdown Content:\n" + jinaBody))
	}))
	defer jina.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"})
	res, err := n.Fetch(context.Background(), source.URL)
	require.NoError(t, err)
	assert.Equal(t, "jina", res.Meta["via"])
	assert.Contains(t, res.Markdown, "rendered by Jina")
	// Must be a content_type the documents CHECK constraint allows.
	assert.Equal(t, store.ContentTypePDF, res.ContentType)
}

// TestNative_PDF_NoJinaIsPermanent: with Jina disabled, an unreadable PDF is
// a permanent failure (no retries).
func TestNative_PDF_NoJinaIsPermanent(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4\nnope"))
	}))
	defer source.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false})
	_, err := n.Fetch(context.Background(), source.URL)
	require.Error(t, err)
	var pe *PermanentError
	assert.ErrorAs(t, err, &pe)
}

// TestNative_Hard404_DeadLink: 404 and 410 are deterministic "gone"
// answers — permanent (no retries), tagged ErrDeadLink, and never routed
// to Jina even when the fallback is enabled.
func TestNative_Hard404_DeadLink(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "gone", status)
			}))
			defer srv.Close()

			jinaCalls := 0
			jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				jinaCalls++
				_, _ = w.Write([]byte("Title: x\n\nMarkdown Content:\n" + strings.Repeat("body ", 100)))
			}))
			defer jina.Close()

			n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/", DeadLinkDetection: true})
			_, err := n.Fetch(context.Background(), srv.URL)
			require.Error(t, err)

			var pe *PermanentError
			assert.ErrorAs(t, err, &pe, "dead link must be permanent")
			assert.ErrorIs(t, err, ErrDeadLink)
			assert.Equal(t, 0, jinaCalls, "dead links must not burn Jina budget")
		})
	}
}

// TestNative_Soft404_TitleDetected: an origin page whose title is a
// not-found template is a dead link, not a login wall, however
// article-shaped its body: final at once, without Jina. An article whose
// title discusses a missing page is stored.
func TestNative_Soft404_TitleDetected(t *testing.T) {
	cases := []struct {
		name  string
		title string
		dead  bool
	}{
		{"a not-found template", "404 - Page Not Found | Example Site", true},
		// 11175b32 and 714ced09 reached the library through Jina.
		{"home depot", "Product Not Found | The Home Depot Canada", true},
		{"aqr's not-found sentence", aqrNotFoundTitle, true},
		{"an article about a 404", "How to fix 404 Not Found errors in Nginx", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, jinaRequests, err := fetchServed(t, makeArticleHTML(tc.title, ""), true)
			assert.Zero(t, jinaRequests, "a page judged at the origin costs no Jina request")
			if !tc.dead {
				require.NoError(t, err)
				assert.Equal(t, "readability", res.Meta["via"])
				assert.Equal(t, tc.title, res.Title)
				return
			}
			require.ErrorIs(t, err, ErrDeadLink)
			var pe *PermanentError
			assert.ErrorAs(t, err, &pe)
			assert.Contains(t, err.Error(), "not-found page")
		})
	}
}

// TestNative_AppShellTitle: Readability finds no article in an app shell
// but reads its title. A not-found title makes it a dead link, final at
// once and without Jina, Jina on or off: the library's f7b423fe, 9493db96,
// 50676971 and fff881fa each spent a Jina request to be judged dead. An
// ordinary title leaves a login wall, which Jina may get past.
func TestNative_AppShellTitle(t *testing.T) {
	const notFound = "Palantir | Page Not Found"
	cases := []struct {
		name         string
		title        string
		jinaOn       bool
		cause        store.FailureCause // empty for a page stored through Jina
		reason       string
		jinaRequests int32
	}{
		{"not-found title, Jina on", notFound, true, store.FailureCauseDeadLink, "not-found page", 0},
		{"not-found title, Jina off", notFound, false, store.FailureCauseDeadLink, "not-found page", 0},
		{"ordinary title, Jina on", "Palantir Careers", true, "", "", 1},
		{"ordinary title, Jina off", "Palantir Careers", false, store.FailureCauseLoginWall, "no article extracted", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, jinaRequests, err := fetchServed(t, appShell(tc.title), tc.jinaOn)
			assert.Equal(t, tc.jinaRequests, jinaRequests)
			if tc.cause == "" {
				require.NoError(t, err)
				assert.Equal(t, "jina", res.Meta["via"])
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.cause, FailureCause(err), "%v", err)
			assert.Contains(t, err.Error(), tc.reason)
			_, permanent := errors.AsType[*PermanentError](err)
			assert.True(t, permanent, "final once every extraction path answered: %v", err)
		})
	}
}

// appShell is a single-page app's HTML: a title over an empty mount point,
// with no article for Readability to find.
func appShell(title string) string {
	return `<!DOCTYPE html><html><head><meta charset="utf-8"><title>` + title + `</title></head>` +
		`<body><div id="app"></div><script src="/main.js"></script></body></html>`
}

// fetchServed fetches causePage from an origin serving html, with dead-link
// detection on and Jina, when on, answering an article. It returns what the
// fetch returned and the number of Jina requests it made.
func fetchServed(t *testing.T, html string, jinaOn bool) (res *Result, jinaRequests int32, err error) {
	t.Helper()
	var hits atomic.Int32
	var jina *fakeAnswer
	if jinaOn {
		jina = new(*jinaArticlePage)
		jina.hits = &hits
	}
	res, err = fakeNative(t, htmlPage(html), jina, true).Fetch(t.Context(), causePage)
	return res, hits.Load(), err
}

// TestNative_Soft404_RedirectToHomepage: a specific path settling on the
// site root (same host) means the content is gone.
func TestNative_Soft404_RedirectToHomepage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/blog/deleted-article", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(makeArticleHTML("Example Site — all our great content", "")))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false, DeadLinkDetection: true})
	_, err := n.Fetch(context.Background(), srv.URL+"/blog/deleted-article")
	require.Error(t, err)

	var pe *PermanentError
	assert.ErrorAs(t, err, &pe)
	assert.ErrorIs(t, err, ErrDeadLink)
	assert.Contains(t, err.Error(), "redirected to homepage")
}

// TestNative_DeadLinkDetectionDisabled: with the kill switch off
// (fetcher.native.dead_link_detection: false), a 404 is the old plain
// retryable error — no PermanentError, no ErrDeadLink — and a soft-404
// page passes through as content.
func TestNative_DeadLinkDetectionDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false}) // detection off (zero value)
	_, err := n.Fetch(context.Background(), srv.URL)
	require.Error(t, err)

	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "404 must stay retryable with detection off")
	assert.NotErrorIs(t, err, ErrDeadLink)
	assert.Contains(t, err.Error(), "HTTP 404")

	// Soft-404 page: with detection off it's just an article.
	soft := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := strings.Repeat("The page you are looking for may have moved. Try the search box. ", 15)
		_, _ = w.Write([]byte(`<html><head><title>404 - Page Not Found</title></head>
			<body><article><h1>Page not found</h1><p>` + body + `</p></article></body></html>`))
	}))
	defer soft.Close()

	res, err := n.Fetch(context.Background(), soft.URL)
	require.NoError(t, err)
	assert.Contains(t, res.Title, "404")
}

func TestSoft404TitleRE(t *testing.T) {
	notFound := []string{
		// The tombstones the library stored as articles: 7f6fa07b and 10
		// more Medium stories, 11175b32 and 714ced09, 1e29e209, 4bcccb00,
		// 466d4d9e, and b8993401's Readability title.
		"410 Deleted by author — Medium",
		"Product Not Found | The Home Depot Canada",
		"Meetup | Group not found",
		"This track was not found",
		"Content has been deleted - Quora",
		aqrNotFoundTitle,
		// The titles the library's dead documents were judged by.
		"404",
		"404 - Not Found",
		"404 Not Found",
		"404. Page Not Found",
		"Page Not Found - Apple Developer",
		"Page Not Found - Clarity Design System",
		"Page not found - Poynter",
		"Page not found - Practice PPE Exams",
		"Page not found - The School of Life",
		"Page not found | General Motors Careers",
		"Palantir | Page Not Found",
		"RxJS - PAGE NOT FOUND",
		// Common templates, alone or beside a site's name.
		"404 - Page Not Found | Example",
		"Error 404",
		"error 404 – nothing here",
		"Not Found",
		"Page not found — Medium",
		"Oops! That page can’t be found.",
		"This page doesn't exist",
		"Sorry, this page no longer exists",
		"The page you requested has been removed",
		"We couldn't find this page",
		"Page not found | Free local classifieds - Kijiji",
		"404 - Page Not Found | Example Site",
		// Each status, status word and template.
		"410 Gone",
		"Error 410",
		"HTTP 404",
		"404 Error: Page Not Found",
		"Page Not Found (404)",
		"Error 404 - Not Found",
		"Oops! Page not found",
		"Story not found",
		"User not found - Stack Overflow",
		"This post has been deleted",
		"This video has been removed",
		"The requested page could not be found",
		"We can't find the page you're looking for",
		"This content is no longer available",
		// Site names before and after it.
		"reddit.com: page not found",
		"Page not found · GitHub",
		"Page not found / X",
		"Page not found • Example",
		"Palantir | Careers | Page Not Found",
		"Page Not Found | Help Center | Example",
		// Every other word and mark of the rule's lists, so that dropping
		// one fails a row. Interjections:
		"Whoops! Page not found",
		"Uh oh, page not found",
		"Uh-oh! Page not found",
		"Oops. Page not found",
		"Oops… page not found",
		// Status words, and the status in parentheses:
		"404 Error",
		"404 (Not Found)",
		"410 Deleted by the author",
		"410 Deleted",
		"Page not found (Error 404)",
		// The marks between status words, unspaced: spaced, they are a
		// site's separator.
		"404|Page Not Found",
		"404-Not Found",
		"410–Gone",
		"410—Gone",
		// Things gone:
		"Article not found",
		"Profile not found",
		"Item not found",
		"Listing not found",
		// What was asked for:
		"The content you are looking for is no longer available",
		"The post you were looking for has been removed",
		"The page you tried to access is not available",
		"The page you tried to reach does not exist",
		"The page you tried to visit no longer exists",
		// How it is gone:
		"The requested product is not found",
		"Page no longer available",
		"This page couldn't be found",
		"The page cannot be found",
		"This video isn't available",
		"This page is missing",
		"This story was deleted",
		"This video has been removed by the user",
		"This post was deleted by its author",
		"Content has been removed by the owner",
		// Can't find it:
		"We cannot find the requested page",
		"We could not find that page",
		"Can't find that page",
		// Contractions typed without their apostrophe:
		"This page doesnt exist",
		"We cant find that page",
		"We couldnt find this page",
		"This video isnt available",
		// A closing mark:
		"Page not found!",
		// The sentence, with whatever the page says next.
		"The page you're looking for can't be found. Try the search box.",
		"Sorry: the page you were looking for does not exist. It may have moved.",
		"This page you are looking for no longer exists, sorry.",
	}
	for _, title := range notFound {
		assert.True(t, soft404TitleRE.MatchString(title), "should match %q", title)
	}

	articles := []string{
		"", // a page without one, as judgeRedirect judges
		"Understanding HTTP 404s and how to avoid them",
		"How we redesigned our 404 experience",
		"Finding lost cities: places not found on any map",
		"The Signal and the Noise",
		"Go 1.25 release notes",
		// Articles about a missing page. A rule that matches a phrase
		// anywhere in the title, or a 404 at its start, matches each of
		// these, and would judge the article dead for good.
		"How to fix 404 Not Found errors in Nginx",
		"Fix the 404 error on your WordPress site",
		"404 Media",
		"404 Media: The Future of Independent Journalism",
		"404 Error Pages: 30 Creative Examples",
		"Error 404 explained: causes and fixes",
		"Error 404: How to Fix It",
		"Creating a custom page not found handler in Express",
		"Page Not Found Errors: A Guide",
		"Your page has been removed from Google's index: what now?",
		// A template's words inside a longer title.
		"How to return a 410 Gone instead of a 404",
		"Product-market fit not found: lessons from a failed startup",
		"Why my content has been deleted from Instagram",
		"User Not Found errors in Active Directory explained",
		// A template before a colon opens a headline: a site's name after
		// the template follows a spaced separator.
		"410 Gone: Why HTTP Status Codes Matter",
		"Product not found: lessons from a failed launch",
		"Not Found: The Search for Amelia Earhart",
		"Group Not Found: How We Lost Our Meetup",
		// So do several words before a template: a site's name before a
		// colon is one word.
		"Lessons from a failed launch: Product not found",
		"Kubernetes debugging: Error 404",
		"Season 2, Episode 4: Not Found",
		// Error pages, not tombstones: LSAC's sign-in page (570bb74c), whose
		// content exists behind a login, and a help center's error page.
		"403 (access denied) error | The Law School Admission Council",
		"Error | BigCommerce Help Center",
	}
	for _, title := range articles {
		assert.False(t, soft404TitleRE.MatchString(title), "should NOT match %q", title)
	}
}

// TestNative_HostCache_LoginWallHitDefers: a redirect onto the site's own
// login page is a host-wide verdict. The first failure is a plain retryable
// error (it populates the cache). With Jina off, every fetch on that host
// within the TTL waits for the entry to expire without contacting the
// origin, carrying the same sentinel plus a "(cached: …)" suffix that
// quotes the origin's answer; after it, the origin is asked again.
func TestNative_HostCache_LoginWallHitDefers(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`<html><head><title>Log in</title></head><body><p>Please sign in.</p></body></html>`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	fc := newFakeClock()
	n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false}), fc)

	// First attempt: real fetch, retryable login-wall error.
	_, err := n.Fetch(context.Background(), srv.URL+"/first")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLoginWall)
	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "first failure must stay retryable: %v", err)
	assert.Equal(t, int32(2), hits.Load(), "redirect + login page")
	expires := fc.now().Add(15 * time.Minute)

	// Same host, different path, a minute later: held by the host cache
	// until the entry expires, origin not contacted.
	fc.advance(time.Minute)
	_, err = n.Fetch(context.Background(), srv.URL+"/second")
	de := requireDeferred(t, err, expires)
	assert.Equal(t, hostOf(srv.URL)+" to be tried again: it sent curio's last request to its login page", de.Reason)
	assert.ErrorIs(t, err, ErrLoginWall)
	assert.Equal(t, "native: login wall or thin content (cached: native: site-wide login wall or thin content "+
		"(redirected to a login/auth path: /login))", err.Error())
	assert.Equal(t, int32(2), hits.Load(), "cache hit must not contact origin")
	assert.Equal(t, store.FailureCauseLoginWall, FailureCause(err))

	// Once the entry expires, the origin is asked again.
	fc.advance(14 * time.Minute)
	_, err = n.Fetch(context.Background(), srv.URL+"/third")
	require.ErrorIs(t, err, ErrLoginWall)
	assert.False(t, errors.As(err, &pe), "a new first failure: %v", err)
	assert.NotContains(t, err.Error(), "(cached:")
	assert.Equal(t, int32(4), hits.Load())
}

// Same contract for the anti-bot kind (HTTP 403 → ErrAntiBot).
func TestNative_HostCache_AntiBotHitDefers(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	fc := newFakeClock()
	n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false}), fc)

	_, err := n.Fetch(context.Background(), srv.URL+"/a")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAntiBot)
	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "first 403 must stay retryable: %v", err)

	_, err = n.Fetch(context.Background(), srv.URL+"/b")
	requireDeferred(t, err, fc.now().Add(15*time.Minute))
	assert.ErrorIs(t, err, ErrAntiBot)
	assert.Equal(t, "native: origin blocked the request (likely anti-bot) "+
		"(cached: native: HTTP 403 Forbidden: origin blocked the request (likely anti-bot))", err.Error())
	assert.Equal(t, int32(1), hits.Load())
	assert.Equal(t, store.FailureCauseAntiBot, FailureCause(err))
}

// TestNative_StatusMatrix pins how every class of origin status is
// classified: anti-bot statuses are retryable and Jina-eligible, dead links
// are permanent only with detection on, transient statuses are retryable,
// and every other status is a deterministic answer that fails permanently.
func TestNative_StatusMatrix(t *testing.T) {
	cases := []struct {
		status     int
		detection  bool
		permanent  bool
		antiBot    bool
		deadLink   bool
		retryAfter string
		wantAfter  time.Duration
	}{
		{status: http.StatusForbidden, antiBot: true},
		{status: http.StatusServiceUnavailable, antiBot: true, retryAfter: "30", wantAfter: 30 * time.Second},
		{status: http.StatusNotFound, detection: true, permanent: true, deadLink: true},
		{status: http.StatusGone, detection: true, permanent: true, deadLink: true},
		{status: http.StatusNotFound},
		{status: http.StatusGone},
		{status: http.StatusRequestTimeout},
		{status: http.StatusMisdirectedRequest},
		{status: http.StatusTooEarly},
		{status: http.StatusTooManyRequests},
		{status: http.StatusTooManyRequests, retryAfter: "120", wantAfter: 120 * time.Second},
		{status: http.StatusInternalServerError},
		{status: http.StatusBadGateway},
		{status: http.StatusGatewayTimeout},
		{status: 520},
		{status: http.StatusBadRequest, permanent: true},
		{status: http.StatusUnauthorized, permanent: true},
		{status: http.StatusPaymentRequired, permanent: true},
		{status: http.StatusMethodNotAllowed, permanent: true},
		{status: http.StatusUnavailableForLegalReasons, permanent: true},
		{status: http.StatusNotImplemented, permanent: true},
		{status: 999, permanent: true},
	}
	for _, tc := range cases {
		name := fmt.Sprintf("%d detection=%v retry-after=%q", tc.status, tc.detection, tc.retryAfter)
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			n := NewNative(NativeOptions{Timeout: 5 * time.Second, DeadLinkDetection: tc.detection})
			_, err := n.Fetch(context.Background(), srv.URL+"/page")
			require.Error(t, err)

			var pe *PermanentError
			assert.Equal(t, tc.permanent, errors.As(err, &pe), "permanent: %v", err)
			assert.Equal(t, tc.antiBot, errors.Is(err, ErrAntiBot), "anti-bot: %v", err)
			assert.Equal(t, tc.deadLink, errors.Is(err, ErrDeadLink), "dead link: %v", err)
			var se *HTTPStatusError
			require.ErrorAs(t, err, &se)
			assert.Equal(t, tc.status, se.StatusCode)
			assert.Equal(t, srv.URL+"/page", se.URL)
			assert.Equal(t, tc.wantAfter, se.RetryAfter)
		})
	}
}

// TestNative_RetryAfterHTTPDate: an HTTP-date Retry-After is measured
// against the fetcher's clock, not the wall clock.
func TestNative_RetryAfterHTTPDate(t *testing.T) {
	fc := newFakeClock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", fc.now().Add(90*time.Second).Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second})
	n.clock = fc.clock()
	_, err := n.Fetch(context.Background(), srv.URL)
	var se *HTTPStatusError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, 90*time.Second, se.RetryAfter)
}

// TestNative_ErrorAnswerKeepsConnection: a short error page is read to its
// end before the body is closed, so the next request to that server reuses
// the connection; on the origin path with both backends, and on Jina's
// retries.
func TestNative_ErrorAnswerKeepsConnection(t *testing.T) {
	errorPage := strings.Repeat("<p>Internal error.</p>", 50)
	serve := func(t *testing.T, h http.HandlerFunc) (srv *httptest.Server, conns *atomic.Int32) {
		t.Helper()
		conns = new(atomic.Int32)
		srv = httptest.NewUnstartedServer(h)
		srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				conns.Add(1)
			}
		}
		srv.Start()
		t.Cleanup(srv.Close)
		return srv, conns
	}

	for _, backend := range []string{"chrome", "stock"} {
		t.Run("origin "+backend, func(t *testing.T) {
			srv, conns := serve(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(errorPage))
			})
			n := NewNative(NativeOptions{Timeout: 5 * time.Second, Backend: backend})
			for _, path := range []string{"/a", "/b", "/c"} {
				_, err := n.Fetch(context.Background(), srv.URL+path)
				require.Error(t, err)
			}
			assert.Equal(t, int32(1), conns.Load())
		})
	}

	t.Run("jina retries", func(t *testing.T) {
		origin := serveThinPage(t)
		defer origin.Close()
		jina, conns := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(errorPage))
		})
		n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"}), newFakeClock())
		_, err := n.Fetch(context.Background(), origin.URL)
		require.Error(t, err)
		assert.Equal(t, int32(1), conns.Load())
	})
}

// unpaced strips n's waits for tests: an unlimited Jina limiter, no
// spacing between a site's Jina requests (its blocks still hold it), and
// fc as the clock so backoffs and cooldowns are recorded instead of slept.
func unpaced(n *Native, fc *fakeClock) *Native {
	n.jinaLimiter = rate.NewLimiter(rate.Inf, 1)
	n.jinaSites = newSitePacer(0)
	n.clock = fc.clock()
	return n
}

// thinPage is an origin answer the login-wall heuristic rejects as thin,
// so Fetch falls back to Jina.
const thinPage = `<html><body><p>nope</p></body></html>`

func serveThinPage(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(thinPage))
	}))
}

// TestNative_JinaTruncatedBodyIsRetryable: a Jina answer cut off mid-body
// (Content-Length promised more, then the connection closed) is a
// transport failure retried with backoff, never a short article.
func TestNative_JinaTruncatedBodyIsRetryable(t *testing.T) {
	source := serveThinPage(t)
	defer source.Close()

	var jinaHits atomic.Int32
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaHits.Add(1)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if !assert.NoError(t, err) {
			return
		}
		defer conn.Close()
		body := "Title: Cut off\n\nMarkdown Content:\n" + strings.Repeat("partial body text ", 40)
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s", len(body)+10_000, body)
		assert.NoError(t, buf.Flush())
	}))
	defer jina.Close()

	fc := newFakeClock()
	n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"}), fc)
	res, err := n.Fetch(context.Background(), source.URL)
	require.Error(t, err)
	assert.Nil(t, res)
	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "a truncated transfer must be retried: %v", err)
	assert.Equal(t, int32(4), jinaHits.Load())
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}, fc.slept())
}

// TestNative_JinaBodyOverLimitIsPermanent: a Jina answer larger than the
// body cap fails permanently on the first attempt.
func TestNative_JinaBodyOverLimitIsPermanent(t *testing.T) {
	source := serveThinPage(t)
	defer source.Close()

	var jinaHits atomic.Int32
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaHits.Add(1)
		_, _ = w.Write([]byte("Title: Huge\n\nMarkdown Content:\n" + strings.Repeat("x", 2*testBodyLimit)))
	}))
	defer jina.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"})
	n.rt = limitBodies(n.rt, testBodyLimit)
	_, err := n.Fetch(context.Background(), source.URL)
	var pe *PermanentError
	require.ErrorAs(t, err, &pe)
	assert.ErrorIs(t, err, ErrTooLarge)
	assert.Equal(t, int32(1), jinaHits.Load())
}

// TestNative_EventStreamRejectedUpFront: text/event-stream never ends, so
// it is refused on its Content-Type without reading the body.
func TestNative_EventStreamRejectedUpFront(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hello\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 30 * time.Second})
	start := time.Now()
	_, err := n.Fetch(context.Background(), srv.URL)
	assert.Less(t, time.Since(start), 5*time.Second)
	var pe *PermanentError
	require.ErrorAs(t, err, &pe)
	assert.Contains(t, err.Error(), "unsupported content type")
}

// fakeRT is a roundTripper answering from a function, for tests that need
// to see exactly what the fetcher reads.
type fakeRT func(target string) (*fetchResponse, error)

func (fakeRT) name() string { return "fake" }
func (f fakeRT) do(_ context.Context, target string, _ []header) (*fetchResponse, error) {
	return f(target)
}

// countingReader is an endless body that counts the bytes handed out.
type countingReader struct{ n atomic.Int64 }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.n.Add(int64(len(p)))
	return len(p), nil
}

// TestNative_PDFOverLimit: a PDF over the body cap skips local extraction
// after reading at most one byte past the cap, then goes to Jina. With
// Jina off it fails permanently.
func TestNative_PDFOverLimit(t *testing.T) {
	const limit = 4096
	const jinaBase = "https://jina.test/"
	for _, jinaOn := range []bool{true, false} {
		t.Run(fmt.Sprintf("jina=%v", jinaOn), func(t *testing.T) {
			origin := &countingReader{}
			n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: jinaOn, JinaBaseURL: jinaBase})
			n.rt = limitBodies(fakeRT(func(target string) (*fetchResponse, error) {
				u, err := url.Parse(target)
				require.NoError(t, err)
				if strings.HasPrefix(target, jinaBase) {
					body := "Title: Big PDF\n\nMarkdown Content:\n" + strings.Repeat("Rendered PDF text. ", 40)
					return &fetchResponse{statusCode: http.StatusOK, header: http.Header{}, finalURL: u,
						body: io.NopCloser(strings.NewReader(body)), contentType: "text/plain"}, nil
				}
				return &fetchResponse{statusCode: http.StatusOK, header: http.Header{}, finalURL: u,
					body: io.NopCloser(origin), contentType: "application/pdf"}, nil
			}), limit)

			res, err := n.Fetch(context.Background(), "https://example.com/big.pdf")
			assert.LessOrEqual(t, origin.n.Load(), int64(limit+1), "must stop reading at the cap")
			if !jinaOn {
				var pe *PermanentError
				require.ErrorAs(t, err, &pe)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "jina", res.Meta["via"])
			assert.Equal(t, store.ContentTypePDF, res.ContentType)
		})
	}
}

// TestNative_ChromePlainHTTPSettlesOnItsOwnURL: an http:// page fetched
// through the chrome backend settles on the URL requested, so the fetch
// handler records no url_canonical, and its relative links resolve without
// the backend's :80 pin.
func TestNative_ChromePlainHTTPSettlesOnItsOwnURL(t *testing.T) {
	ca := newTestCA(t)
	page := makeArticleHTML("Plain HTTP", strings.Repeat(`A paragraph that links <a href="/other">elsewhere</a>. `, 30))
	site := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, page) })
	n := NewNative(NativeOptions{Timeout: 5 * time.Second})
	n.rt = limitBodies(newRoutedChromeRT(t, ca.pool, plainHTTPSite(t, ca, []string{"example.com"}, site)), maxResponseBytes)

	res, err := n.Fetch(t.Context(), "http://example.com/article")
	require.NoError(t, err)
	assert.Equal(t, "http://example.com/article", res.FinalURL)
	assert.Contains(t, res.Markdown, "(http://example.com/other)")
	assert.NotContains(t, res.Markdown, ":80")
}

// TestNative_ChromeHostlessRedirectCachesNothing: a redirect to a URL
// without a host fails that fetch alone. Dialed, it would reach the local
// machine, here a refused port, and the refusal would be cached against the
// redirecting host, failing its healthy pages without a request.
func TestNative_ChromeHostlessRedirectCachesNothing(t *testing.T) {
	ca := newTestCA(t)
	page := makeArticleHTML("Healthy", strings.Repeat("A paragraph long enough to count as an article. ", 30))
	for _, location := range []string{"http:///x", "https:///x"} {
		t.Run(location, func(t *testing.T) {
			site := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/moved" {
					http.Redirect(w, r, location, http.StatusMovedPermanently)
					return
				}
				_, _ = io.WriteString(w, page)
			})
			routes := plainHTTPSite(t, ca, []string{"example.com"}, site)
			routes[":80"] = closedAddr(t)
			routes[":443"] = closedAddr(t)
			n := NewNative(NativeOptions{Timeout: 5 * time.Second})
			n.rt = limitBodies(newRoutedChromeRT(t, ca.pool, routes), maxResponseBytes)

			_, err := n.Fetch(t.Context(), "http://example.com/moved")
			require.ErrorContains(t, err, errNoHost.Error())
			assert.False(t, hostCached(n, "example.com"))

			res, err := n.Fetch(t.Context(), "http://example.com/healthy")
			require.NoError(t, err)
			assert.Equal(t, "Healthy", res.Title)
		})
	}
}

// jinaReply builds a Jina Reader answer in its real layout. URL Source is
// not the requested URL, as after a re-encoding: nothing may read it.
func jinaReply(title string, warnings []string, body string) string {
	var b strings.Builder
	b.WriteString("Title: " + title + "\n\nURL Source: https://jina-echo.example/requested\n\n")
	for _, w := range warnings {
		b.WriteString("Warning: " + w + "\n")
	}
	if len(warnings) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("Markdown Content:\n" + body)
	return b.String()
}

// Warnings Jina adds to an answer.
const (
	warnTarget403 = "Target URL returned error 403: Forbidden"
	warnCaptcha   = "This page maybe requiring CAPTCHA, please make sure you are authorized to access this page."
)

// Pages that are not the article, as Jina renders them in a 200 answer.
const (
	// cfChallengeBody is Cloudflare's managed challenge, about 200 bytes.
	cfChallengeBody = "example.com\n-----------\n\n## Performing security verification\n\n" +
		"This website uses a security service to protect against malicious bots. " +
		"This page is displayed while the website verifies you are not a bot."
	cfBlockBody = `Sorry, you have been blocked
============================

You are unable to access example.com
------------------------------------

Why have I been blocked?
------------------------

This website is using a security service to protect itself from online attacks. The action you just performed triggered the security solution.

What can I do to resolve this?
------------------------------

You can email the site owner to let them know you were blocked.

Cloudflare Ray ID: **8c5f2b1e9d4a7f30** • Performance & security by Cloudflare`
	redditBlockBody = `You’ve been blocked by network security.

To continue, log in to your Reddit account or use your developer token

If you think you’ve been blocked by mistake, file a ticket below and we’ll look into it.

[Log in](https://www.reddit.com/login/)[File a ticket](https://support.reddithelp.com/hc/en-us/requests/new)`
	akamaiDeniedBody = `Access Denied
=============

You don't have permission to access "http://www.example.com/products/42" on this server.

Reference #18.2f3c1702.1726000000.1a2b3c4d

https://errors.edgesuite.net/18.2f3c1702.1726000000.1a2b3c4d`
	perimeterXBody = `Access to this page has been denied
===================================

Press & Hold to confirm you are a human (and not a bot).

Reference ID 5f2c1a40-7b3e-11ef-9d2a-0242ac120002`
	// parkedDomainBody is a parked domain's page, 340 bytes. Nothing but
	// its length gives it away.
	parkedDomainBody = `example.org
===========

This domain may be for sale!

[Inquire now](https://www.sedo.com/search/details/?domain=example.org&language=us)

Related Searches:

*   [Web Hosting](http://example.org/?q=Web+Hosting)
*   [Domain Registration](http://example.org/?q=Domain+Registration)
*   [Email Marketing](http://example.org/?q=Email+Marketing)`
	tweetBody = `Conversation
============

[Jane Doe](https://x.com/janedoe)

[@janedoe](https://x.com/janedoe)

Shipping a new version of our SQLite extension today: vector search now runs inside the same transaction as your writes, so there is no second store to keep in step. Benchmarks, the migration guide and the full changelog are in the thread below. Thanks to everyone who filed issues and sent patches.

[3:14 PM · Sep 20, 2026](https://x.com/janedoe/status/1837000000000000000)

12.4K Views

[42 Reposts](https://x.com/janedoe/status/1837000000000000000/retweets) [310 Likes](https://x.com/janedoe/status/1837000000000000000/likes)`
)

var (
	// longArticleBody is over 2 KiB, past where challenge phrases are
	// looked for, so only the title and Jina's warnings judge it.
	longArticleBody = strings.Repeat("The interpreter reads the source, compiles it to bytecode and runs it. ", 36)
	// loginPageBody is long enough that only a login page's title gives it
	// away.
	loginPageBody = strings.Repeat("Enter your email address and password to continue to your account. ", 8)
	// kijijiNotFoundBody is a not-found page long enough that only its
	// title gives it away.
	kijijiNotFoundBody = "Oops! We can't seem to find the page you're looking for.\n\n" +
		strings.Repeat("Browse [Cars & Vehicles](https://www.kijiji.ca/b-cars-vehicles/canada/c27l0), "+
			"[Real Estate](https://www.kijiji.ca/b-real-estate/canada/c34l0) and more. ", 5)
)

// Not-found pages the library stored as articles through Jina, as Jina
// rendered them, with no target-status warning. Each is long enough to pass
// as an article under an ordinary title: only its own title gives it away.
// Medium's and Quora's are whole; the others are trimmed of cookie banners,
// navigation and trending lists.
const (
	// mediumTombstoneBody is 7f6fa07b's, titled "410 Deleted by author —
	// Medium": Medium's page for a story its author deleted.
	mediumTombstoneBody = "[Sitemap](https://medium.com/sitemap/sitemap.xml)\n\n" +
		"[Open in app](https://play.google.com/store/apps/details?id=com.medium.reader&referrer=utm_source%3DmobileNavBar&source=---top_nav_layout_nav-------------------------------------------)\n\n" +
		"Sign up\n\n" +
		"[Sign in](https://medium.com/m/signin?operation=login&redirect=https%3A%2F%2Fmedium.com%2Fdesign-explosion%2Fdesign-explosions-mapping-on-ios-ad4ec6ba5c59&source=post_page---top_nav_layout_nav-----------------------global_nav--------------------)\n\n" +
		"[](https://medium.com/?source=---top_nav_layout_nav-------------------------------------------)\n\n" +
		"Get app\n\n" +
		"[Write](https://medium.com/m/signin?operation=register&redirect=https%3A%2F%2Fmedium.com%2Fnew-story&source=---top_nav_layout_nav-----------------------new_post_topnav--------------------)\n\n" +
		"[Search](https://medium.com/search?source=---top_nav_layout_nav-------------------------------------------)\n\n" +
		"Sign up\n\n" +
		"[Sign in](https://medium.com/m/signin?operation=login&redirect=https%3A%2F%2Fmedium.com%2Fdesign-explosion%2Fdesign-explosions-mapping-on-ios-ad4ec6ba5c59&source=post_page---top_nav_layout_nav-----------------------global_nav--------------------)\n\n" +
		"![Image 1: Unknown user](https://miro.medium.com/v2/resize:fill:64:64/1*dmbNkD5D-u45r44go_cf0g.png)\n\n" +
		"Error\n\n" +
		"410\n\n" +
		"The author deleted this Medium story."
	// homeDepotNotFoundBody is 11175b32's, titled "Product Not Found | The
	// Home Depot Canada".
	homeDepotNotFoundBody = "[](http://www.homedepot.ca/)\n\n" +
		"*   [Rental](http://www.homedepot.ca/en/home/tool-and-vehicle-rental.html)\n" +
		"*   [Credit Services](http://www.homedepot.ca/en/home/credit-services.html)\n" +
		"*   [For the Pro](http://www.homedepot.ca/en/home/pro.html)\n" +
		"*   [Order Status](http://www.homedepot.ca/guest-order-details)\n" +
		"*   [Customer Support](http://www.homedepot.ca/en/home/customer-support.html)\n\n" +
		"[Account / Sign In](http://www.homedepot.ca/)\n\n" +
		"[Cart](http://www.homedepot.ca/cart)\n\n" +
		"*   [Shop by Department](http://www.homedepot.ca/)\n" +
		"*   [Shop by Room](http://www.homedepot.ca/en/home/shop-by-room.html)\n" +
		"*   [Ideas & How-to](http://www.homedepot.ca/en/home/ideas-how-to.html)\n\n" +
		"Don’t miss out on our Pro Savings Event. Ends October 7.[Shop Now](http://www.homedepot.ca/en/home/categories/all/events/pro-savings-event.html?intid=HP_scarf_260924_EN_ProSavings)\n\n" +
		"## How We Use Cookies\n\n" +
		"We use cookies and similar technologies (“Cookies”) which are required for our website and app to function. " +
		"We use optional Cookies to understand how people use our website/app, to improve our services, and to personalize offers/ads to you.\n\n" +
		"Accept All\n\n" +
		"Manage Cookie Preferences"
	// meetupNotFoundBody is 1e29e209's, titled "Meetup | Group not found".
	meetupNotFoundBody = "[Skip to content](http://www.meetup.com/leancoffeeto/#main)\n\n" +
		"[](https://www.meetup.com/)\n\n" +
		"Homepage\n\n" +
		"English\n\n" +
		"Log in Sign up\n\n" +
		"![Image 2: searchPurple illustration](https://secure.meetupstatic.com/next/images/illustrations/search-purple.webp?w=384)\n\n" +
		"# Group not found\n\n" +
		"Sorry, the group you're looking for doesn't exist\n\n" +
		".\n\n" +
		"The people platform\n\n" +
		"Create your own Meetup group.\n\n" +
		"[Get Started](https://www.meetup.com/start?origin=groups&eventOrigin=page-footer)\n\n" +
		"Your account\n\n" +
		"*   [Sign up](https://www.meetup.com/register/?returnUri=https%3A%2F%2Fwww.meetup.com%2Fleancoffeeto%2F)\n" +
		"*   [Log in](https://www.meetup.com/login/?returnUri=https%3A%2F%2Fwww.meetup.com%2Fleancoffeeto%2F)\n" +
		"*   [Help](https://help.meetup.com/hc)\n\n" +
		"© 2026 Bending Spoons US Inc."
	// soundCloudNotFoundBody is 4bcccb00's, titled "This track was not found".
	soundCloudNotFoundBody = "[SoundCloud](https://soundcloud.com/ \"Home\")\n\n" +
		"*   [Home](https://soundcloud.com/discover)\n" +
		"*   [Feed](https://soundcloud.com/feed)\n" +
		"*   [Library](https://soundcloud.com/you/library)\n\n" +
		"Search\n\n" +
		"Sign in Create account\n\n" +
		"[Upload](https://soundcloud.com/upload)\n\n" +
		"[Bloomberg Opinion](https://soundcloud.com/bloombergview)\n\n" +
		" This track was not found. Maybe it has been removed [Learn more](https://help.soundcloud.com/hc/articles/115003563948-Can-t-find-a-track-anymore)\n\n" +
		" Trending tracks on SoundCloud \n\n" +
		"*   [2006](https://soundcloud.com/tijan-ebrima/2006a1) [Dragnutz](https://soundcloud.com/tijan-ebrima)\n" +
		"*   [Backwards](https://soundcloud.com/quavoofficial/backwards) [Quavo, T.I.](https://soundcloud.com/quavoofficial)\n\n" +
		"[Legal](https://soundcloud.com/terms-of-use \"Terms of use\")· [Privacy](https://soundcloud.com/pages/privacy \"Privacy policy\")· " +
		"[Cookie Policy](https://soundcloud.com/pages/cookies \"Cookie Policy\")"
	// quoraDeletedBody is 466d4d9e's, titled "Content has been deleted -
	// Quora".
	quoraDeletedBody = "[](https://www.quora.com/)\n\n" +
		"![Image 1: Icon for Consultantsmind](https://qph.cf2.quoracdn.net/main-thumb-ti-133003-100-xcdvpxbiwwtjxfewsbpsmuphiktubgku.jpeg)\n\n" +
		"## [Consultantsmind](https://consultantsmind.quora.com/)\n\n" +
		"## Consulting Blog\n\n" +
		"This post has been deleted.\n\n" +
		"[About](https://www.quora.com/about) · [Careers](https://www.quora.com/careers) · [Privacy](https://www.quora.com/about/privacy) · [Terms](https://www.quora.com/about/tos) · [Contact](https://www.quora.com/contact) · [Languages](https://www.quora.com/about/languages) · [Your Ad Choices](https://www.quora.com/about/your_ad_choices) · [Press](https://www.quora.com/press) · \n" +
		"© Quora, Inc. 2026"
)

// aqrNotFoundTitle is b8993401's title, which the library stored from the
// origin: Readability took the first sentence of aqr.com's not-found page
// for it.
const aqrNotFoundTitle = "The page you are looking for does not exist or has been moved. " +
	"To find what you’re looking for, try one of the following:"

// jinaHarness is a Native whose origin serves the thin page, or answers
// with an error status, and whose fake Jina always answers reply. Both
// count their requests.
type jinaHarness struct {
	n          *Native
	fc         *fakeClock
	origin     *httptest.Server
	originHits atomic.Int32
	jinaHits   atomic.Int32
}

func newJinaHarness(t *testing.T, originStatus int, deadLinkDetection bool, reply string) *jinaHarness {
	t.Helper()
	h := &jinaHarness{fc: newFakeClock()}
	h.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.originHits.Add(1)
		if originStatus != http.StatusOK {
			w.WriteHeader(originStatus)
			return
		}
		_, _ = io.WriteString(w, thinPage)
	}))
	t.Cleanup(h.origin.Close)
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.jinaHits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(jina.Close)
	h.n = unpaced(NewNative(NativeOptions{
		Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/",
		DeadLinkDetection: deadLinkDetection,
	}), h.fc)
	return h
}

// originCached reports whether the origin's host has a host-cache entry.
func (h *jinaHarness) originCached() bool {
	return hostCached(h.n, hostOf(h.origin.URL))
}

// TestNative_JinaTargetStatus pins what the status Jina reports for the
// target turns a fetch into, behind a thin origin page and behind an origin
// 403. Whatever the status, the answer costs one Jina request, extends no
// cooldown, and is never an *HTTPStatusError: the only one in the chain is
// the origin's own. Only the origin's host-wide verdict is ever cached, and
// only once Jina gave a verdict of its own; the host's next URL then asks
// Jina alone.
func TestNative_JinaTargetStatus(t *testing.T) {
	type outcome struct{ permanent, deadLink, antiBot, cached bool }
	cases := []struct {
		status    int
		origin    int // 200 serves the thin page
		detection bool
		want      outcome
	}{
		{http.StatusNotFound, http.StatusOK, true, outcome{permanent: true, deadLink: true}},
		{http.StatusGone, http.StatusOK, true, outcome{permanent: true, deadLink: true}},
		{http.StatusNotFound, http.StatusForbidden, true, outcome{permanent: true, deadLink: true, antiBot: true}},
		{http.StatusGone, http.StatusForbidden, true, outcome{permanent: true, deadLink: true, antiBot: true}},
		{http.StatusForbidden, http.StatusOK, true, outcome{permanent: true, antiBot: true}},
		{http.StatusServiceUnavailable, http.StatusOK, true, outcome{permanent: true, antiBot: true}},
		{http.StatusForbidden, http.StatusForbidden, true, outcome{antiBot: true, cached: true}},
		{http.StatusServiceUnavailable, http.StatusForbidden, true, outcome{antiBot: true, cached: true}},
		{http.StatusRequestTimeout, http.StatusOK, true, outcome{}},
		{http.StatusTooManyRequests, http.StatusOK, true, outcome{}},
		{http.StatusInternalServerError, http.StatusOK, true, outcome{}},
		{http.StatusBadGateway, http.StatusOK, true, outcome{}},
		{520, http.StatusOK, true, outcome{}},
		{526, http.StatusOK, true, outcome{}},
		{http.StatusTooManyRequests, http.StatusForbidden, true, outcome{antiBot: true}},
		{http.StatusBadGateway, http.StatusForbidden, true, outcome{antiBot: true}},
		{http.StatusBadRequest, http.StatusOK, true, outcome{permanent: true}},
		{http.StatusUnauthorized, http.StatusOK, true, outcome{permanent: true}},
		{http.StatusUnavailableForLegalReasons, http.StatusOK, true, outcome{permanent: true}},
		{http.StatusNotImplemented, http.StatusOK, true, outcome{permanent: true}},
		{http.StatusNotFound, http.StatusOK, false, outcome{}},
		{http.StatusGone, http.StatusOK, false, outcome{}},
		{http.StatusNotFound, http.StatusForbidden, false, outcome{antiBot: true}},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("target %d origin %d detection=%v", tc.status, tc.origin, tc.detection), func(t *testing.T) {
			warning := fmt.Sprintf("Target URL returned error %d: %s", tc.status, cmp.Or(http.StatusText(tc.status), "Unknown"))
			h := newJinaHarness(t, tc.origin, tc.detection,
				jinaReply("Welcome to Python.org", []string{warning}, longArticleBody))

			_, err := h.n.Fetch(context.Background(), h.origin.URL+"/a")
			require.Error(t, err)
			var pe *PermanentError
			assert.Equal(t, tc.want.permanent, errors.As(err, &pe), "permanent: %v", err)
			assert.Equal(t, tc.want.deadLink, errors.Is(err, ErrDeadLink), "dead link: %v", err)
			assert.Equal(t, tc.want.antiBot, errors.Is(err, ErrAntiBot), "anti-bot: %v", err)
			var se *HTTPStatusError
			assert.Equal(t, tc.origin == http.StatusForbidden, errors.As(err, &se),
				"the target's status is never an *HTTPStatusError: %v", err)
			assert.Equal(t, int32(1), h.jinaHits.Load(), "one Jina request per fetch")
			assert.Zero(t, h.n.jinaCooldown.remaining(h.fc.now()), "Jina's cooldown is its own 429's")
			assert.Equal(t, tc.want.cached, h.originCached())

			_, err = h.n.Fetch(context.Background(), h.origin.URL+"/b")
			require.Error(t, err)
			if tc.want.cached {
				// The next URL skips the origin; Jina's verdict on it is its
				// own, and final.
				require.ErrorAs(t, err, &pe)
				assert.ErrorIs(t, err, errJinaRejected)
				assert.Contains(t, err.Error(), "(cached:")
				assert.Equal(t, int32(1), h.originHits.Load())
				assert.Equal(t, int32(2), h.jinaHits.Load(), "one Jina request of its own")
				assert.Equal(t, store.FailureCauseAntiBot, FailureCause(err))
			} else {
				assert.Equal(t, int32(2), h.originHits.Load(), "the next URL on the host reaches the origin")
			}
		})
	}
}

// TestNative_JinaTombstoneAfterOrigin403: Jina's answer for a page the
// origin refused is Medium's page for a deleted story, without the
// target-status warning that would say 410 (7f6fa07b and 4 more were stored
// this way). Its title makes it a dead link: final, one Jina request, and
// no host-cache entry, so the host's next page asks the origin again.
func TestNative_JinaTombstoneAfterOrigin403(t *testing.T) {
	h := newJinaHarness(t, http.StatusForbidden, true,
		jinaReply("410 Deleted by author — Medium", nil, mediumTombstoneBody))

	_, err := h.n.Fetch(t.Context(), h.origin.URL+"/a")
	require.ErrorIs(t, err, ErrDeadLink)
	assert.ErrorIs(t, err, ErrAntiBot, "the origin's side of the chain")
	var pe *PermanentError
	assert.ErrorAs(t, err, &pe)
	assert.Contains(t, err.Error(), "not-found page")
	assert.Equal(t, store.FailureCauseDeadLink, FailureCause(err))
	assert.Equal(t, int32(1), h.jinaHits.Load())
	assert.False(t, h.originCached())

	_, err = h.n.Fetch(t.Context(), h.origin.URL+"/b")
	require.ErrorIs(t, err, ErrDeadLink)
	assert.Equal(t, int32(2), h.originHits.Load(), "the next page on the host reaches the origin")
}

// TestNative_JinaRejectsNonArticles: a 2xx Jina answer that is a challenge,
// a block, a 403/503 error page without the target-status warning, a
// not-found or a login page, or too thin, is Jina's verdict: one request,
// never stored, never cached. Behind a thin origin page it fails the fetch
// permanently.
func TestNative_JinaRejectsNonArticles(t *testing.T) {
	cases := []struct {
		name     string
		title    string
		warnings []string
		body     string
		want     error
		reason   string
	}{
		{"cloudflare challenge", "Just a moment...", nil, cfChallengeBody, ErrAntiBot, "bot challenge"},
		{"cloudflare block", "Attention Required! | Cloudflare", nil, cfBlockBody, ErrAntiBot, "bot challenge"},
		{"reddit block", "", nil, redditBlockBody, ErrAntiBot, "network security"},
		{"akamai", "Access Denied", nil, akamaiDeniedBody, ErrAntiBot, "bot challenge"},
		{"perimeterx", "Access to this page has been denied.", nil, perimeterXBody, ErrAntiBot, "bot challenge"},
		{"perimeterx, by its text", "", nil, perimeterXBody, ErrAntiBot, "press & hold"},
		{"captcha warning", "An article", []string{warnCaptcha}, longArticleBody, ErrAntiBot, "CAPTCHA"},
		{"not-found title", "Page not found | Free local classifieds - Kijiji", nil, kijijiNotFoundBody, ErrDeadLink, "not-found page"},
		// The library's tombstones: Jina's answers for them carry no
		// target-status warning, so their titles tell.
		{"medium tombstone", "410 Deleted by author — Medium", nil, mediumTombstoneBody, ErrDeadLink, "not-found page"},
		{"home depot", "Product Not Found | The Home Depot Canada", nil, homeDepotNotFoundBody, ErrDeadLink, "not-found page"},
		{"meetup", "Meetup | Group not found", nil, meetupNotFoundBody, ErrDeadLink, "not-found page"},
		{"soundcloud", "This track was not found", nil, soundCloudNotFoundBody, ErrDeadLink, "not-found page"},
		{"quora", "Content has been deleted - Quora", nil, quoraDeletedBody, ErrDeadLink, "not-found page"},
		{"parked domain", "example.org", nil, parkedDomainBody, ErrLoginWall, "extracted text < 500 bytes"},
		{"a byte under the floor", "A note", nil, strings.Repeat("x", minArticleBytes-1), ErrLoginWall, "extracted text < 500 bytes"},
		{"atlassian", "Log in to continue - Log in with Atlassian account", nil, loginPageBody, ErrLoginWall, "login wall"},
		{"google docs", "Google Docs: Sign-in", nil, loginPageBody, ErrLoginWall, "login wall"},
		{"google sheets", "Google Sheets: Sign-in", nil, loginPageBody, ErrLoginWall, "login wall"},
		{"google drive", "Google Drive: Sign-in", nil, loginPageBody, ErrLoginWall, "login wall"},
		{"google accounts", "Sign in - Google Accounts", nil, loginPageBody, ErrLoginWall, "login wall"},
		{"consensys", "Sign in to Consensys Software Inc.", nil, loginPageBody, ErrLoginWall, "login wall"},
		{"colorcombos", "ColorCombos.com - Login", nil, loginPageBody, ErrLoginWall, "login wall"},
		{"bigcommerce", "Login | BigCommerce Help Center", nil, loginPageBody, ErrLoginWall, "login wall"},
		{"plaid", "Plaid - Dashboard | Signin", nil, loginPageBody, ErrLoginWall, "login wall"},
		{"ibm notice", "IBM notice: The page you requested cannot be displayed", nil, ibmNoticeBody, ErrAntiBot, "error page"},
		{"stanford", "Access forbidden : Stanford University", nil, stanfordForbiddenBody, ErrAntiBot, "error page"},
		{"aptana", "403 | Forbidden | Axway", nil, aptanaForbiddenBody, ErrAntiBot, "error page"},
		{"aptana, stored title", "403 | Forbidden", nil, aptanaForbiddenBody, ErrAntiBot, "error page"},
		{"google cloud storage", "", nil, gcsAccessDeniedBody, ErrAntiBot, "error page"},
		{"visage.co", "403 Forbidden", nil, s3AccessDeniedBody, ErrAntiBot, "error page"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newJinaHarness(t, http.StatusOK, true, jinaReply(tc.title, tc.warnings, tc.body))
			res, err := h.n.Fetch(context.Background(), h.origin.URL+"/a")
			require.Error(t, err)
			assert.Nil(t, res)
			assert.ErrorIs(t, err, tc.want)
			assert.Contains(t, err.Error(), tc.reason)
			assert.Equal(t, !errors.Is(tc.want, ErrDeadLink), errors.Is(err, errJinaRejected),
				"a dead link is final, every other verdict a rejection: %v", err)
			var pe *PermanentError
			assert.ErrorAs(t, err, &pe)
			assert.Equal(t, int32(1), h.jinaHits.Load())
			assert.False(t, h.originCached())
		})
	}
}

// TestNative_JinaAcceptsArticles: informational warnings, articles about
// bot checks and logins, titles that start like a challenge's, a short
// post and a body of exactly minArticleBytes are all stored, settled on the
// URL requested.
func TestNative_JinaAcceptsArticles(t *testing.T) {
	// botEssay quotes challenge phrases in 3 KB of text, past the 2 KiB
	// up to which a page's text is searched for them.
	botEssay := strings.Repeat("Bot checks are everywhere now. ", 20) +
		"The page said just a moment, then Checking your browser before accessing the site, " +
		"then asked me to verify you are human. A 404 would have been kinder. " +
		strings.Repeat("The rest of this essay is about why these checks fail real readers. ", 34)
	article := strings.Repeat("A paragraph of a real article about the subject in its title. ", 10)
	cases := []struct {
		name      string
		title     string
		warnings  []string
		body      string
		detection bool
	}{
		{"informational warnings", "An article", []string{
			"This page contains iframe that are currently hidden, consider enabling iframe processing.",
			"This page contains shadow DOM that are currently hidden, consider enabling shadow DOM processing.",
			"This is a cached snapshot of the original page, consider retry with caching opt-out.",
			"This page maybe not yet fully loaded, consider explicitly specify a timeout.",
		}, article, true},
		{"an essay quoting challenge pages", "Why bot checks fail real readers", nil, botEssay, true},
		{"grafana", "How to log in to Grafana with SSO", nil, article, true},
		{"apple", "Sign In With Apple: A Developer's Guide", nil, article, true},
		{"stack overflow", "Login cognito using with scope openId using id_token or access_token don't working", nil, article, true},
		{"just a moment of", "Just a moment of silence", nil, article, true},
		{"access denied:", "Access Denied: A History of Web Censorship", nil, article, true},
		{"access forbidden:", "Access Forbidden: A History of Web Censorship", nil, article, true},
		{"an article about a 403", "How to fix a 403 Forbidden error", nil, article, true},
		{"an article about a 526", "Understanding Cloudflare Error 526", nil, article, true},
		{"a short post", `Jane Doe on X: "Shipping a new version of our SQLite extension today" / X`, nil, tweetBody, true},
		{"at the floor", "A note", nil, strings.Repeat("x", minArticleBytes), true},
		{"not-found title, detection off", "Page not found | Free local classifieds - Kijiji", nil, kijijiNotFoundBody, false},
		{"tombstone, detection off", "410 Deleted by author — Medium", nil, mediumTombstoneBody, false},
		{"an article about a 404", "How to fix 404 Not Found errors in Nginx", nil, article, true},
		// The library's tombstones under an ordinary title: only their own
		// titles make them dead.
		{"medium tombstone's body", "An article", nil, mediumTombstoneBody, true},
		{"home depot's body", "An article", nil, homeDepotNotFoundBody, true},
		{"meetup's body", "An article", nil, meetupNotFoundBody, true},
		{"soundcloud's body", "An article", nil, soundCloudNotFoundBody, true},
		{"quora's body", "An article", nil, quoraDeletedBody, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newJinaHarness(t, http.StatusOK, tc.detection, jinaReply(tc.title, tc.warnings, tc.body))
			target := h.origin.URL + "/a"
			res, err := h.n.Fetch(context.Background(), target)
			require.NoError(t, err)
			assert.Equal(t, "jina", res.Meta["via"])
			assert.Equal(t, tc.title, res.Title)
			assert.Equal(t, target, res.FinalURL, "URL Source echoes the request; it is no final URL")
			assert.Equal(t, int32(1), h.jinaHits.Load())
		})
	}
}

// TestNative_ChallengePageFallsBackToJina: a bot challenge served with 200
// is anti-bot, not a thin page, and never stored: with Jina off it fails
// permanently, and with Jina on Jina is asked once.
func TestNative_ChallengePageFallsBackToJina(t *testing.T) {
	for name, page := range challengePages {
		for _, mode := range []jinaMode{jinaOff, jinaThin} {
			t.Run(name+"/jina "+string(mode), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, page)
				}))
				defer srv.Close()

				n, jinaCalls := newNativeWithJina(t, mode)
				res, err := n.Fetch(context.Background(), srv.URL+"/a")
				require.ErrorIs(t, err, ErrAntiBot)
				assert.Nil(t, res)
				assert.Contains(t, err.Error(), "bot challenge")
				var pe *PermanentError
				assert.ErrorAs(t, err, &pe)
				if mode == jinaOff {
					assert.Zero(t, jinaCalls())
				} else {
					assert.Equal(t, int32(1), jinaCalls())
				}
			})
		}
	}
}

// challengePages are bot interstitials served with a 200 status.
var challengePages = map[string]string{
	"imperva": `<!DOCTYPE html><html><head><title>Pardon Our Interruption</title></head><body>
<div class="container"><h1>Pardon Our Interruption</h1>
<p>As you were browsing something about your browser made us think you were a bot. There are a few reasons this might happen:</p>
<ul><li>You're a power user moving through this website with super-human speed.</li>
<li>You've disabled cookies in your web browser.</li>
<li>A third-party browser plugin, such as Ghostery or NoScript, is preventing JavaScript from running. Additional information is available in this <a href="/support">support article</a>.</li></ul>
<p>To regain access, please make sure that cookies and JavaScript are enabled before reloading the page.</p>
</div></body></html>`,
	"cloudflare": `<!DOCTYPE html><html lang="en-US"><head><title>Just a moment...</title></head><body>
<div class="main-wrapper" role="main"><div class="main-content">
<h1 class="zone-name-title h1">example.com</h1>
<h2 class="h2" id="challenge-running">Checking if the site connection is secure</h2>
<noscript><div class="h2"><span id="challenge-error-text">Enable JavaScript and cookies to continue</span></div></noscript>
<div id="challenge-body-text" class="core-msg spacer">example.com needs to review the security of your connection before proceeding.</div>
</div></div></body></html>`,
}

// TestJudgePage_Order pins the order of the page verdicts: dead links
// first (another site's landing page included), then challenges, then
// error pages, then the login-wall checks, redirects before content.
func TestJudgePage_Order(t *testing.T) {
	at := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return u
	}
	const target = "https://example.com/blog/a-post-about-caching"
	long := strings.Repeat("A sentence of the article. ", 30)
	challenge := "Checking your browser before accessing example.com."
	cases := []struct {
		name      string
		page      pageView
		detection bool
		want      error
		reason    string
		scope     loginWallScope // of an ErrLoginWall
	}{
		{"not-found title before a challenge", pageView{title: "Page not found", text: challenge, found: true}, true, ErrDeadLink, "not-found page", 0},
		{"homepage before a challenge title", pageView{title: "Just a moment...", found: true, finalURL: at("https://example.com/")}, true, ErrDeadLink, "redirected to homepage", 0},
		{"landing page before a challenge", pageView{title: "Just a moment...", text: challenge, found: true, finalURL: at("https://other.example/articles/")}, true, ErrDeadLink, "another site's landing page", 0},
		{"detection off", pageView{title: "Page not found", text: challenge, found: true}, false, ErrAntiBot, "bot challenge", 0},
		{"not-found title without an article", pageView{title: "Palantir | Page Not Found"}, true, ErrDeadLink, "not-found page", 0},
		{"not-found title without an article, detection off", pageView{title: "Palantir | Page Not Found"}, false, ErrLoginWall, "no article extracted", loginWallPage},
		// Google Cloud's docs pad their separators with no-break spaces; the
		// reason quotes the title as served.
		{"not-found title with no-break spaces", pageView{title: "Page not found  |  Google Cloud Documentation", text: long, found: true}, true, ErrDeadLink, "not-found page: Page not found  |  Google", 0},
		{"challenge before an error page", pageView{title: "403 Forbidden", text: challenge, found: true}, true, ErrAntiBot, "bot challenge", 0},
		{"challenge before a login redirect", pageView{title: "Just a moment...", text: long, found: true, finalURL: at("https://example.com/login")}, true, ErrAntiBot, "bot challenge", 0},
		{"challenge title without an article", pageView{title: "Just a moment..."}, true, ErrAntiBot, "bot challenge", 0},
		{"error page before a login redirect", pageView{title: "403 Forbidden", found: true, finalURL: at("https://example.com/login")}, true, ErrAntiBot, "error page", 0},
		{"server error page before thin", pageView{title: "502 Bad Gateway", text: "short", found: true}, true, errServerErrorPage, "502 Bad Gateway", 0},
		{"a login destination is no landing page", pageView{title: "Welcome", found: true, finalURL: at("https://accounts.other.example/login")}, true, errOffsiteLoginWall, "another site's login page", loginWallOffsite},
		{"cross-site login redirect before no article", pageView{title: "Sign in - Other Accounts", finalURL: at("https://accounts.other.example/ServiceLogin")}, true, errOffsiteLoginWall, "another site's login page", loginWallOffsite},
		{"cross-site login redirect before thin", pageView{text: "short", found: true, finalURL: at("https://id.other.example/login")}, false, errOffsiteLoginWall, "another site's login page", loginWallOffsite},
		{"same-site login redirect before no article", pageView{finalURL: at("https://example.com/login")}, true, ErrLoginWall, "login/auth path", loginWallSite},
		{"cross-site redirect onto a thin page", pageView{text: "short", found: true, finalURL: at("https://other.example/blog/a-post-about-caching")}, true, ErrLoginWall, "extracted text < 500 bytes", loginWallPage},
		{"no article", pageView{title: "Log in"}, true, ErrLoginWall, "no article extracted", loginWallPage},
		{"thin before a login title", pageView{title: "Log in", text: "short", found: true}, true, ErrLoginWall, "extracted text < 500 bytes", loginWallPage},
		{"login title", pageView{title: "Log in", text: long, found: true}, true, ErrLoginWall, "title looks like a login wall", loginWallPage},
		{"article", pageView{title: "A real article", text: long, found: true, finalURL: at(target)}, true, nil, "", 0},
		{"article on another site", pageView{title: "A real article", text: long, found: true, finalURL: at("https://other.example/2019/a-post-about-caching")}, true, nil, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &Native{deadLinkDetection: tc.detection}
			err := n.judgePage(target, tc.page)
			if tc.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.want)
			assert.Contains(t, err.Error(), tc.reason)
			if errors.Is(err, ErrLoginWall) {
				assert.Equal(t, tc.scope == loginWallSite, errors.Is(err, errSiteLoginWall), "site-wide: %v", err)
				assert.Equal(t, tc.scope == loginWallOffsite, errors.Is(err, errOffsiteLoginWall), "offsite: %v", err)
			}
		})
	}
}

// TestJudgeJinaAnswer pins the order of the steps a Jina answer is judged
// in, whatever order its warnings come in: the target's status, then Jina's
// CAPTCHA warning, then the page verdicts. Only those two warnings count.
func TestJudgeJinaAnswer(t *testing.T) {
	const warn404 = "Target URL returned error 404: Not Found"
	cases := []struct {
		name   string
		answer jinaParsed
		want   error
		reason string
	}{
		{"target status before the CAPTCHA warning",
			jinaParsed{title: "Welcome to Python.org", warnings: []string{warnCaptcha, warn404}, body: longArticleBody},
			ErrDeadLink, "HTTP 404"},
		{"target status before the page",
			jinaParsed{title: "Just a moment...", warnings: []string{warn404}, body: cfChallengeBody},
			ErrDeadLink, "HTTP 404"},
		{"target status in any case",
			jinaParsed{title: "Welcome to Python.org", warnings: []string{"target url returned error 404: not found"}, body: longArticleBody},
			ErrDeadLink, "HTTP 404"},
		{"CAPTCHA warning before the page",
			jinaParsed{title: "Page not found", warnings: []string{warnCaptcha}, body: longArticleBody},
			ErrAntiBot, "CAPTCHA"},
		{"page verdicts after informational warnings",
			jinaParsed{title: "Page not found", warnings: []string{"This page maybe not yet fully loaded, consider explicitly specify a timeout."}, body: longArticleBody},
			ErrDeadLink, "not-found page"},
		{"a warning that only mentions a CAPTCHA",
			jinaParsed{title: "An article", warnings: []string{"This page contains a CAPTCHA widget that was left out."}, body: longArticleBody},
			nil, ""},
		{"a 403 error page is a rejection",
			jinaParsed{title: "Access forbidden : Stanford University", body: stanfordForbiddenBody},
			errJinaRejected, "error page"},
		{"a server error page is the target's trouble for now",
			jinaParsed{title: "appdesignvault.com | 526: Invalid SSL certificate", body: cfInvalidSSLBody},
			errJinaTargetTrouble, "server error page"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &Native{deadLinkDetection: true}
			err := n.judgeJinaAnswer("https://example.com/post", tc.answer)
			if tc.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.want)
			assert.Contains(t, err.Error(), tc.reason)
		})
	}
}

func TestChallengeTitleRE(t *testing.T) {
	challenge := []string{
		"Just a moment...",
		"Just a moment…",
		"just a moment...",
		"Attention Required! | Cloudflare",
		"Access Denied",
		"Access denied | www.example.com used Cloudflare to restrict access",
		"Please Wait... | Cloudflare",
		"Access to this page has been denied",
		"Access to this page has been denied.",
		"Pardon Our Interruption",
		"DDoS-Guard",
		"Vercel Security Checkpoint",
		"Are you a robot?",
		"Bloomberg - Are you a robot?",
	}
	for _, title := range challenge {
		assert.True(t, challengeTitleRE.MatchString(title), "should match %q", title)
	}
	article := []string{
		"Just a moment of silence",
		"Attention required! Why focus matters",
		"Access Denied: A History of Web Censorship",
		"Are you a robot? The Turing test at 75",
		"Pardon our dust: new site coming",
	}
	for _, title := range article {
		assert.False(t, challengeTitleRE.MatchString(title), "should NOT match %q", title)
	}
}

// TestLooksLikeChallenge_Phrases: challenge phrases count on short pages
// only, case-insensitively and whatever the apostrophe.
func TestLooksLikeChallenge_Phrases(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"cloudflare", "example.com needs to review the security of your connection before proceeding.", true},
		{"reddit, curly apostrophe", redditBlockBody, true},
		{"incapsula", "Request unsuccessful. Incapsula incident ID: 123000450123456789-12345678901234567", true},
		{"ad blocker", "Please enable JS and disable any ad blocker", true},
		{"short page, no phrase", "A short note about our release.", false},
		{"3 KB article quoting one", strings.Repeat("An essay on bot checks. ", 128) +
			"Enable JavaScript and cookies to continue, the page said.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := looksLikeChallenge(pageView{title: "A title", text: tc.text, found: true})
			assert.Equal(t, tc.want, reason != "", "reason %q", reason)
		})
	}
}

func TestLoginTitleRE(t *testing.T) {
	login := []string{
		"Log in to continue - Log in with Atlassian account",
		"Google Docs: Sign-in",
		"Sign in - Google Accounts",
		"Sign in to Consensys Software Inc.",
		"ColorCombos.com - Login",
		"Login | BigCommerce Help Center",
		"Plaid - Dashboard | Signin",
		"Sign In | Sentry",
		"Login or Sign Up for a Dropbox Account",
		"Sign in to read this",
		"Log in",
		"Join now",
		"Join LinkedIn",
	}
	for _, title := range login {
		assert.True(t, loginTitleRE.MatchString(title), "should match %q", title)
	}
	article := []string{
		"Login cognito using with scope openId using id_token or access_token don't working",
		"How to log in to Grafana with SSO",
		"Sign In With Apple: A Developer's Guide",
		"Logging in with OAuth 2.0: a primer",
		"Why your login page leaks usernames",
		"Designing a better sign-in",
	}
	for _, title := range article {
		assert.False(t, loginTitleRE.MatchString(title), "should NOT match %q", title)
	}
}

func TestLoginPathRE(t *testing.T) {
	login := []string{
		"/login", "/login/", "/uas/login", "//user/login.php", "/s/login/", "/auth/login/",
		"/auth/v3/signin", "/v3/signin/identifier", "/signup/credentials", "/authwall",
		"/m/signin", "/i/flow/login", "/accounts/login/", "/Login.aspx",
	}
	for _, path := range login {
		assert.True(t, loginPathRE.MatchString(path), "should match %q", path)
	}
	notLogin := []string{
		"/questions/63177503/login-cognito-using-with-scope-openid",
		"/blog/login-best-practices",
		"/signup-flow-teardown",
		"/signing-keys",
	}
	for _, path := range notLogin {
		assert.False(t, loginPathRE.MatchString(path), "should NOT match %q", path)
	}
}

// Jina's own error answers, after r.jina.ai's on 2026-09-27.
const (
	// jinaAbuseBlock refuses keyless reads of a domain after a burst of
	// them; {host} stands for the domain it names.
	jinaAbuseBlock = "AbuseAlleviationError: Anonymous access to domain {host} blocked until " +
		"Sun Sep 27 2026 16:40:15 GMT+0000 (Coordinated Universal Time) due to previous abuse found on " +
		"https://{host}/someone: DDoS attack suspected: Too many requests\n"
	// jina451Body declines a domain whose owner opted out of Jina Reader,
	// verbatim.
	jina451Body = `{"code": 451, "status": 45101, "message": "This domain is excluded from Jina Reader at the ` +
		`request of its owner, People Inc.", "detail": "The publisher of this URL has instructed Jina AI to cease ` +
		`automated access to its properties. Jina Reader is complying with that request. For programmatic access ` +
		`to this content, please contact People Inc. directly regarding licensing.", "readme": "https://r.jina.ai/docs"}`
	jina451Reason = "This domain is excluded from Jina Reader at the request of its owner, People Inc."
	// cfBlockHTML is the head of the page Cloudflare's CDN blocks a client
	// with: a 403 without a challenge.
	cfBlockHTML = "<!DOCTYPE html>\n<html lang=\"en-US\"><head><title>Attention Required! | Cloudflare</title></head>\n" +
		"<body><h1>Sorry, you have been blocked</h1><h2>You are unable to access jina.ai</h2></body></html>"
)

// abuseBlock is jinaAbuseBlock naming host.
func abuseBlock(host string) string { return strings.ReplaceAll(jinaAbuseBlock, "{host}", host) }

// jinaHostRefusal is a 403 whose reason names a host, {host}, and is no
// abuse block. Its wording is made up: the rule it tests is for a refusal
// of the target, whatever Jina calls it.
const jinaHostRefusal = "DomainRefusedError: Jina Reader does not read {host}\n"

// hostRefusal is jinaHostRefusal naming host.
func hostRefusal(host string) string { return strings.ReplaceAll(jinaHostRefusal, "{host}", host) }

func TestJinaReason(t *testing.T) {
	long := "RateLimitTriggeredError: " + strings.Repeat("é", 400) // byte 512 falls inside an é
	cases := []struct {
		name        string
		contentType string
		body        string
		want        string
	}{
		{"text error line", "text/plain; charset=utf-8", abuseBlock("mobile.twitter.com") + "more detail\n",
			strings.TrimSpace(abuseBlock("mobile.twitter.com"))},
		{"JSON with a name", "application/json",
			`{"code":401,"name":"AuthenticationRequiredError","message":"Authentication is required to use this endpoint."}`,
			"AuthenticationRequiredError: Authentication is required to use this endpoint."},
		{"JSON without a name", "application/json; charset=utf-8", `{"code":422,"message":"Invalid URL"}`, "Invalid URL"},
		{"451 verbatim", "application/json", jina451Body, jina451Reason},
		{"JSON without a message", "application/json", `{"code":500}`, ""},
		{"JSON cut short", "application/json", `{"code":451,"message":"This domain`, ""},
		{"Cloudflare's challenge", "text/html; charset=UTF-8",
			"<!DOCTYPE html><html lang=\"en-US\"><head><title>Just a moment...</title>", ""},
		{"Cloudflare's block page", "text/html; charset=UTF-8", cfBlockHTML, ""},
		{"plain Forbidden", "text/plain", "Forbidden", ""},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, jinaReason(tc.contentType, []byte(tc.body)))
		})
	}

	t.Run("capped on a rune boundary", func(t *testing.T) {
		got := jinaReason("text/plain", []byte(long))
		assert.True(t, strings.HasPrefix(got, "RateLimitTriggeredError: é"))
		assert.True(t, strings.HasSuffix(got, "é…"), got)
		assert.LessOrEqual(t, len(got), maxErrorBody+len("…"))
		assert.True(t, utf8.ValidString(got))
	})
}

// TestParseSiteBlock: an AbuseAlleviationError names the domain Jina
// blocks and when the block ends, in JavaScript's Date form (any GMT
// offset, a 1- or 2-digit day), or should Jina switch, in the HTTP or ISO
// form. A reason without them leaves them unset.
func TestParseSiteBlock(t *testing.T) {
	blocked := func(domain, until string) string {
		return "AbuseAlleviationError: Anonymous access to domain " + domain + " blocked until " + until +
			" due to previous abuse found on https://" + domain + "/someone: DDoS attack suspected: Too many requests"
	}
	cases := []struct {
		name   string
		reason string
		domain string
		until  time.Time
	}{
		{"mobile.twitter.com, as Jina wrote it", blocked("mobile.twitter.com",
			"Mon Sep 28 2026 18:48:50 GMT+0000 (Coordinated Universal Time)"),
			"mobile.twitter.com", time.Date(2026, 9, 28, 18, 48, 50, 0, time.UTC)},
		{"another offset", blocked("x.com", "Mon Sep 28 2026 11:51:10 GMT-0700 (Pacific Daylight Time)"),
			"x.com", time.Date(2026, 9, 28, 18, 51, 10, 0, time.UTC)},
		{"a one-digit day, no zone name", blocked("x.com", "Mon Oct 5 2026 11:51:10 GMT-0700"),
			"x.com", time.Date(2026, 10, 5, 18, 51, 10, 0, time.UTC)},
		{"an ISO date", blocked("x.com", "2026-09-28T18:51:10.000Z"), "x.com", time.Date(2026, 9, 28, 18, 51, 10, 0, time.UTC)},
		{"an HTTP date", blocked("x.com", "Mon, 28 Sep 2026 18:51:10 GMT"), "x.com", time.Date(2026, 9, 28, 18, 51, 10, 0, time.UTC)},
		{"no reason given after the date", "AbuseAlleviationError: Anonymous access to domain Www.Forbes.com blocked until " +
			"Mon Sep 28 2026 07:33:33 GMT+0000 (Coordinated Universal Time)",
			"www.forbes.com", time.Date(2026, 9, 28, 7, 33, 33, 0, time.UTC)},
		{"a closing dot", "AbuseAlleviationError: Anonymous access to domain x.com. blocked until sometime",
			"x.com", time.Time{}},
		{"an unreadable date", blocked("x.com", "sometime"), "x.com", time.Time{}},
		{"no domain", "AbuseAlleviationError: Too many requests", "", time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := errors.New("the answer")
			sb := parseSiteBlock(tc.reason, err)
			assert.Equal(t, tc.domain, sb.domain)
			assert.True(t, tc.until.Equal(sb.until), "until %v, want %v", sb.until, tc.until)
			assert.Equal(t, tc.reason, sb.reason)
			assert.Same(t, err, errors.Unwrap(sb))
		})
	}
}

// TestSiteBlockEnd: a block lasts until the time Jina gave, or an hour when
// it gave none it could read, and never less than a minute nor more than a
// job waits.
func TestSiteBlockEnd(t *testing.T) {
	now := time.Date(2026, 9, 28, 17, 50, 10, 0, time.UTC)
	cases := []struct {
		name string
		said time.Time
		want time.Duration
	}{
		{"as said", now.Add(58*time.Minute + 40*time.Second), 58*time.Minute + 40*time.Second},
		{"unsaid", time.Time{}, jinaSiteBlockDefault},
		{"already over", now.Add(-time.Minute), minJinaSiteBlock},
		{"in a few seconds", now.Add(5 * time.Second), minJinaSiteBlock},
		{"in 2039", time.Date(2039, 12, 30, 17, 9, 6, 0, time.UTC), store.DeferralBudget},
	}
	for _, tc := range cases {
		assert.Equal(t, now.Add(tc.want), siteBlockEnd(tc.said, now), tc.name)
	}
}

// TestJinaStatusError_SiteBlock: an AbuseAlleviationError is a site block
// whatever its status, text or JSON, quoting Jina's reason; a challenge
// stays a challenge.
func TestJinaStatusError_SiteBlock(t *testing.T) {
	const target = "https://mobile.twitter.com/someone"
	reason := strings.TrimSpace(abuseBlock("mobile.twitter.com"))
	for _, code := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		err := jinaStatusError(target, &HTTPStatusError{StatusCode: code}, nil, reason)
		sb, ok := errors.AsType[*jinaSiteBlock](err)
		require.True(t, ok, "%d: %v", code, err)
		assert.Equal(t, "mobile.twitter.com", sb.domain)
		assert.ErrorIs(t, err, errJinaSiteBlocked)
		assert.NotErrorIs(t, err, errJinaRefused)
		assert.Equal(t, fmt.Sprintf("jina: blocks the site for now: HTTP %d %s: %s", code, http.StatusText(code), reason),
			err.Error())
		var se *HTTPStatusError
		require.ErrorAs(t, err, &se)
		assert.Equal(t, code, se.StatusCode)
		assert.False(t, jinaRetryable(err), "the block's end is when to ask again")
	}

	jsonReason := jinaReason("application/json", []byte(`{"name":"AbuseAlleviationError","message":"Anonymous access to domain `+
		`x.com blocked until Mon Sep 28 2026 18:51:10 GMT+0000 (Coordinated Universal Time)"}`))
	sb, ok := errors.AsType[*jinaSiteBlock](jinaStatusError(target, &HTTPStatusError{StatusCode: http.StatusForbidden}, nil, jsonReason))
	require.True(t, ok, "a JSON answer's name counts too")
	assert.Equal(t, "x.com", sb.domain)

	challenged := jinaStatusError(target, &HTTPStatusError{StatusCode: http.StatusForbidden},
		http.Header{"Cf-Mitigated": {"challenge"}}, reason)
	assert.ErrorIs(t, challenged, errJinaChallenged)
	assert.NotErrorIs(t, challenged, errJinaSiteBlocked)
}

func TestNamesHost(t *testing.T) {
	reason := strings.TrimSpace(abuseBlock("mobile.twitter.com"))
	assert.True(t, namesHost(reason, "mobile.twitter.com"))
	assert.True(t, namesHost(strings.ToUpper(reason), "mobile.twitter.com"), "case doesn't matter")
	assert.True(t, namesHost("Access to www.investing.com.", "www.investing.com"), "a closing dot isn't part of it")
	assert.False(t, namesHost(reason, "twitter.com"), "a parent domain is another host")
	assert.False(t, namesHost(reason, "twitter.co"))
	assert.False(t, namesHost("Anonymous access to domain abc.com blocked", "c.com"))
	assert.False(t, namesHost(reason, ""))
	assert.False(t, namesHost("Forbidden: https://www.example.com/a/b is not allowed", "www.example.com"),
		"a host inside a URL isn't named")
	assert.False(t, namesHost("Blocked: HTTP://www.example.com.", "www.example.com"), "whatever the scheme's case")
}

// fakeAnswer is how a server answers a request, for fakeRT.
type fakeAnswer struct {
	status      int
	header      http.Header
	contentType string
	body        string
	bodyErr     error         // the body fails with it once body is read
	err         error         // the request fails with it instead
	hits        *atomic.Int32 // counts the requests answered, when set
}

// respond is the answer a describes to a request for target.
func (a fakeAnswer) respond(target string) (*fetchResponse, error) {
	if a.hits != nil {
		a.hits.Add(1)
	}
	if a.err != nil {
		return nil, a.err
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	var body io.Reader = strings.NewReader(a.body)
	if a.bodyErr != nil {
		body = io.MultiReader(body, iotest.ErrReader(a.bodyErr))
	}
	header := a.header
	if header == nil {
		header = http.Header{}
	}
	return &fetchResponse{statusCode: a.status, header: header, finalURL: u, body: io.NopCloser(body),
		contentType: a.contentType}, nil
}

// TestJinaCallClass: each way a Jina request can go is classed for Jina's
// health from the error the real jinaOnce returns for it.
func TestJinaCallClass(t *testing.T) {
	const target = "https://news.example/story"
	warned := func(warning string) string { return jinaReply("An article", []string{warning}, longArticleBody) }
	status := func(code int) fakeAnswer { return fakeAnswer{status: code} }
	cases := []struct {
		name  string
		reply fakeAnswer
		want  CallClass
	}{
		{"the page", fakeAnswer{status: http.StatusOK, body: jinaArticleBody()}, CallOK},
		{"a thin answer", fakeAnswer{status: http.StatusOK, body: jinaReply("Short", nil, "too short")}, CallJudged},
		{"the target's 403", fakeAnswer{status: http.StatusOK, body: warned(warnTarget403)}, CallJudged},
		{"the target's 404", fakeAnswer{status: http.StatusOK,
			body: warned("Target URL returned error 404: Not Found")}, CallJudged},
		{"the target's 502", fakeAnswer{status: http.StatusOK,
			body: warned("Target URL returned error 502: Bad Gateway")}, CallJudged},
		{"an answer over the cap", fakeAnswer{status: http.StatusOK,
			body: jinaArticle + strings.Repeat("x", 2*testBodyLimit)}, CallJudged},
		{"a 403 naming the target's host", fakeAnswer{status: http.StatusForbidden, contentType: "text/plain",
			body: hostRefusal("news.example")}, CallRefused},
		{"an abuse block of the target's site", fakeAnswer{status: http.StatusForbidden, contentType: "text/plain",
			body: abuseBlock("news.example")}, CallRateLimited},
		{"an abuse block with a 429", fakeAnswer{status: http.StatusTooManyRequests, contentType: "text/plain",
			body: abuseBlock("news.example")}, CallRateLimited},
		{"an abuse block of another site", fakeAnswer{status: http.StatusForbidden, contentType: "text/plain",
			body: abuseBlock("mobile.twitter.com")}, CallRateLimited},
		{"451", fakeAnswer{status: http.StatusUnavailableForLegalReasons, contentType: "application/json",
			body: jina451Body}, CallRefused},
		{"400", status(http.StatusBadRequest), CallRefused},
		{"404", status(http.StatusNotFound), CallRefused},
		{"a challenge", fakeAnswer{status: http.StatusForbidden, header: http.Header{"Cf-Mitigated": {"challenge"}},
			contentType: "text/html", body: "<!DOCTYPE html><title>Just a moment...</title>"}, CallChallenged},
		{"a bare 403", status(http.StatusForbidden), CallForbidden},
		{"a 403 naming another host", fakeAnswer{status: http.StatusForbidden, contentType: "text/plain",
			body: hostRefusal("mobile.twitter.com")}, CallForbidden},
		{"Cloudflare's block page", fakeAnswer{status: http.StatusForbidden, contentType: "text/html",
			body: cfBlockHTML}, CallForbidden},
		{"401", status(http.StatusUnauthorized), CallAuth},
		{"402", status(http.StatusPaymentRequired), CallAuth},
		{"429", status(http.StatusTooManyRequests), CallRateLimited},
		{"500", status(http.StatusInternalServerError), CallServerError},
		{"502", status(http.StatusBadGateway), CallServerError},
		{"503", status(http.StatusServiceUnavailable), CallServerError},
		{"501", status(http.StatusNotImplemented), CallServerError},
		{"505", status(http.StatusHTTPVersionNotSupported), CallServerError},
		{"408", status(http.StatusRequestTimeout), CallServerError},
		{"421", status(http.StatusMisdirectedRequest), CallServerError},
		{"425", status(http.StatusTooEarly), CallServerError},
		{"304", status(http.StatusNotModified), CallServerError},
		{"a connection reset", fakeAnswer{err: &url.Error{Op: "Get", URL: "https://jina.test/",
			Err: syscall.ECONNRESET}}, CallNetwork},
		{"a timeout", fakeAnswer{err: &url.Error{Op: "Get", URL: "https://jina.test/",
			Err: os.ErrDeadlineExceeded}}, CallNetwork},
		{"a bad certificate", fakeAnswer{err: fmt.Errorf("%w: x509: certificate signed by unknown authority",
			ErrTLSCertificate)}, CallNetwork},
		{"a body cut short", fakeAnswer{status: http.StatusOK, body: "Title: Cut off\n",
			bodyErr: io.ErrUnexpectedEOF}, CallNetwork},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true,
				JinaBaseURL: "https://jina.test/", DeadLinkDetection: true})
			n.rt = limitBodies(fakeRT(tc.reply.respond), testBodyLimit)
			_, err := n.jinaOnce(context.Background(), target)
			assert.Equal(t, tc.want, jinaCallClass(err), "%v", err)
		})
	}
}

// TestJinaOnce_ErrorBodyReadIsBounded: however long Jina's error answer,
// jinaOnce reads no more than errorBodyDrain of it.
func TestJinaOnce_ErrorBodyReadIsBounded(t *testing.T) {
	body := &countingReader{}
	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: "https://jina.test/"})
	n.rt = fakeRT(func(target string) (*fetchResponse, error) {
		u, err := url.Parse(target)
		if err != nil {
			return nil, err
		}
		return &fetchResponse{statusCode: http.StatusBadGateway, header: http.Header{}, finalURL: u,
			body: io.NopCloser(io.LimitReader(body, 1<<20)), contentType: "text/plain"}, nil
	})
	_, err := n.jinaOnce(context.Background(), "https://news.example/story")
	require.Error(t, err)
	assert.LessOrEqual(t, body.n.Load(), int64(errorBodyDrain))
}

// TestNative_JinaRefusal: Jina refusing the target, by a 403 whose reason
// names the target's host or by a 451, is Jina's verdict, quoted in the
// error. Behind a thin page the first fetch fails for good after one Jina
// request; behind an origin 403 the origin's host is cached, and the next
// URL there skips the origin and fails for good on Jina's refusal of it.
// Neither pauses Jina, and Jina's health counts the refusal as a healthy
// answer.
func TestNative_JinaRefusal(t *testing.T) {
	answers := []struct {
		name        string
		status      int
		contentType string
		body        func(host string) string
		reason      string
	}{
		{"a 403 naming the target's host", http.StatusForbidden, "text/plain; charset=utf-8", hostRefusal,
			"HTTP 403 Forbidden: DomainRefusedError: Jina Reader does not read 127.0.0.1"},
		{"owner opted out", http.StatusUnavailableForLegalReasons, "application/json",
			func(string) string { return jina451Body }, "HTTP 451 Unavailable For Legal Reasons: " + jina451Reason},
	}
	for _, a := range answers {
		refusingJina := func(t *testing.T, host string) (base string, hits *atomic.Int32) {
			t.Helper()
			hits = new(atomic.Int32)
			jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", a.contentType)
				w.WriteHeader(a.status)
				_, _ = io.WriteString(w, a.body(host))
			}))
			t.Cleanup(jina.Close)
			return jina.URL + "/", hits
		}

		t.Run(a.name+"/thin page", func(t *testing.T) {
			origin := serveThinPage(t)
			defer origin.Close()
			jina, jinaHits := refusingJina(t, hostOf(origin.URL))
			fc := newFakeClock()
			n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina}), fc)

			_, err := n.Fetch(context.Background(), origin.URL+"/a")
			var pe *PermanentError
			require.ErrorAs(t, err, &pe, "a refusal behind a page-level failure is final")
			assert.ErrorIs(t, err, ErrLoginWall)
			assert.ErrorIs(t, err, errJinaRefused)
			assert.True(t, strings.HasPrefix(err.Error(), "jina: refused the target: "+a.reason), err.Error())
			assert.Contains(t, err.Error(), "(after native: login wall or thin content")
			var se *HTTPStatusError
			require.ErrorAs(t, err, &se)
			assert.Equal(t, a.status, se.StatusCode)
			assert.True(t, strings.HasPrefix(se.URL, jina), "errors.As finds Jina's status first: %s", se.URL)
			assert.Equal(t, int32(1), jinaHits.Load())
			assert.Zero(t, n.jinaCooldown.remaining(fc.now()))
			assert.False(t, hostCached(n, hostOf(origin.URL)))
			h := n.JinaHealth()
			assert.Equal(t, map[CallClass]int{CallRefused: 1}, h.Recent)
			assert.Equal(t, UpstreamOK, h.State)
		})

		t.Run(a.name+"/origin 403", func(t *testing.T) {
			var originHits atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				originHits.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer origin.Close()
			jina, jinaHits := refusingJina(t, hostOf(origin.URL))
			fc := newFakeClock()
			n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina}), fc)

			_, err := n.Fetch(context.Background(), origin.URL+"/a")
			require.ErrorIs(t, err, ErrAntiBot)
			assert.ErrorIs(t, err, errJinaRefused)
			var pe *PermanentError
			assert.False(t, errors.As(err, &pe), "the first failure for a host stays retryable: %v", err)
			assert.True(t, hostCached(n, hostOf(origin.URL)), "the origin's verdict is cached")

			_, err = n.Fetch(context.Background(), origin.URL+"/b")
			require.ErrorAs(t, err, &pe, "the next URL's own refusal is final")
			assert.ErrorIs(t, err, errJinaRefused)
			assert.True(t, strings.HasPrefix(err.Error(), "jina: refused the target: "+a.reason), err.Error())
			assert.Contains(t, err.Error(),
				"(after native: origin blocked the request (likely anti-bot) (cached: native: HTTP 403 Forbidden: ")
			assert.Equal(t, int32(2), jinaHits.Load(), "the next URL asks Jina")
			assert.Equal(t, int32(1), originHits.Load(), "and not the origin")
			assert.Equal(t, store.FailureCauseJinaRefused, FailureCause(err))
			assert.Zero(t, n.jinaCooldown.remaining(fc.now()))
		})
	}
}

// jinaBlockDate writes t as Jina writes the end of a block: a JavaScript
// Date, with the zone's name.
func jinaBlockDate(t time.Time) string {
	return t.UTC().Format("Mon Jan 2 2006 15:04:05 GMT-0700") + " (Coordinated Universal Time)"
}

// siteBlockAnswer is Jina's AbuseAlleviationError, with status, blocking
// keyless reads of domain until until.
func siteBlockAnswer(status int, domain string, until time.Time) fakeAnswer {
	return fakeAnswer{status: status, contentType: "text/plain; charset=utf-8",
		body: "AbuseAlleviationError: Anonymous access to domain " + domain + " blocked until " + jinaBlockDate(until) +
			" due to previous abuse found on https://" + domain + "/someone: DDoS attack suspected: Too many requests\n"}
}

// blockingJina is a Native on fc, without site spacing, logging to logs,
// whose origin answers origin and whose Jina answers pages of news.example
// with block while blocking holds, and every other page with an article.
// It counts Jina's requests by site.
func blockingJina(t *testing.T, fc *fakeClock, logs *logRecorder, origin, block fakeAnswer,
	blocking *atomic.Bool) (n *Native, jinaHits func(site string) int) {
	t.Helper()
	const jinaBase = "https://jina.test/"
	var mu sync.Mutex
	hits := map[string]int{}
	n = unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jinaBase,
		DeadLinkDetection: true, Log: slog.New(logs)}), fc)
	n.rt = fakeRT(func(target string) (*fetchResponse, error) {
		page, isJina := strings.CutPrefix(target, jinaBase)
		if !isJina {
			return origin.respond(target)
		}
		site := siteOf(hostOf(page))
		mu.Lock()
		hits[site]++
		mu.Unlock()
		if site == "news.example" && blocking.Load() {
			return block.respond(target)
		}
		return fakeAnswer{status: http.StatusOK, body: jinaArticleBody()}.respond(target)
	})
	return n, func(site string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[site]
	}
}

// TestNative_JinaSiteBlock: Jina's AbuseAlleviationError, with a 403 or a
// 429, is no verdict. The page that met it waits for the block's end, which
// its answer names, and so does every later page of the site, www. or not,
// without a Jina request, behind a thin page or an origin 403 alike: never
// a permanent failure, never a host-cache entry, never Jina's shared
// cooldown. Health counts the one call; the block logs one warning and
// shows in the upstream's site pauses. Once it ends, Jina is asked again.
func TestNative_JinaSiteBlock(t *testing.T) {
	origins := []struct {
		name     string
		answer   fakeAnswer
		sentinel error
		cause    store.FailureCause // once a day of waiting runs out
	}{
		{"thin page", htmlPage(thinPage), ErrLoginWall, store.FailureCauseRateLimited},
		{"origin 403", answerStatus(http.StatusForbidden), ErrAntiBot, store.FailureCauseRateLimited},
	}
	for _, origin := range origins {
		for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
			t.Run(fmt.Sprintf("%s/%d", origin.name, status), func(t *testing.T) {
				fc := newFakeClock()
				ends := fc.now().Add(50 * time.Minute)
				var blocking atomic.Bool
				blocking.Store(true)
				logs := &logRecorder{}
				n, jinaHits := blockingJina(t, fc, logs, origin.answer, siteBlockAnswer(status, "www.news.example", ends), &blocking)
				const reason = "Jina Reader's block of news.example to lift"

				_, err := n.Fetch(t.Context(), "https://www.news.example/a")
				de := requireDeferred(t, err, ends)
				assert.Equal(t, reason, de.Reason)
				assert.ErrorIs(t, err, errJinaSiteBlocked)
				assert.ErrorIs(t, err, origin.sentinel)
				assert.NotErrorIs(t, err, errJinaRefused)
				assert.True(t, strings.HasPrefix(err.Error(), fmt.Sprintf("jina: blocks the site for now: HTTP %d %s: "+
					"AbuseAlleviationError: Anonymous access to domain www.news.example blocked until", status, http.StatusText(status))),
					err.Error())
				assert.Equal(t, 1, jinaHits("news.example"))
				assert.Equal(t, map[CallClass]int{CallRateLimited: 1}, n.JinaHealth().Recent)
				assert.Zero(t, n.jinaCooldown.remaining(fc.now()), "the shared cooldown is not the site's")
				assert.False(t, hostCached(n, "www.news.example"), "a block writes no host-cache entry")
				assert.Equal(t, origin.cause, FailureCause(fmt.Errorf("fetch failed: %w", err)))
				assert.Equal(t, 1, logs.count(slog.LevelWarn, "Jina Reader blocks keyless reads of a site, holding its pages"))

				// The site's later pages wait for the block without a request.
				for _, page := range []string{"https://news.example/b", "https://m.news.example/c"} {
					_, err = n.Fetch(t.Context(), page)
					de = requireDeferred(t, err, ends)
					assert.Equal(t, reason, de.Reason)
					assert.ErrorIs(t, err, errJinaSiteBlocked)
					assert.Contains(t, err.Error(), "jina: not sent, Jina Reader blocks news.example until "+
						ends.Format(time.RFC3339)+": AbuseAlleviationError: Anonymous access to domain www.news.example")
					assert.Equal(t, origin.cause, FailureCause(fmt.Errorf("fetch failed: %w", err)))
				}
				assert.Equal(t, 1, jinaHits("news.example"))
				h := n.JinaHealth()
				assert.Equal(t, map[CallClass]int{CallRateLimited: 1}, h.Recent, "a held call is no call")
				assert.Equal(t, []SitePause{{Site: "news.example", Until: ends}}, h.SitePauses)
				assert.NotEqual(t, UpstreamPaused, h.State, "one site's block doesn't pause Jina")

				// Other sites are not held.
				res, err := n.Fetch(t.Context(), "https://blog.example/d")
				require.NoError(t, err)
				assert.Equal(t, "jina", res.Meta["via"])

				// Once the block ends, the site's pages ask Jina again.
				fc.advance(50 * time.Minute)
				blocking.Store(false)
				res, err = n.Fetch(t.Context(), "https://news.example/b")
				require.NoError(t, err)
				assert.Equal(t, "jina", res.Meta["via"])
				assert.Equal(t, 2, jinaHits("news.example"))
				assert.Empty(t, n.JinaHealth().SitePauses)
				assert.Equal(t, 1, logs.count(slog.LevelWarn, "Jina Reader blocks keyless reads of a site, holding its pages"))
			})
		}
	}
}

// TestNative_JinaSiteBlockNamesItsSite: a block holds the site Jina's
// answer names, which need not be the target's, and the target's own site
// when the answer names none, for jinaSiteBlockDefault.
func TestNative_JinaSiteBlockNamesItsSite(t *testing.T) {
	t.Run("another site", func(t *testing.T) {
		fc := newFakeClock()
		ends := fc.now().Add(50 * time.Minute)
		var blocking atomic.Bool
		blocking.Store(true)
		n, jinaHits := blockingJina(t, fc, &logRecorder{}, answerStatus(http.StatusForbidden),
			siteBlockAnswer(http.StatusForbidden, "mobile.other.example", ends), &blocking)

		_, err := n.Fetch(t.Context(), "https://www.news.example/a")
		requireDeferred(t, err, ends)
		assert.Equal(t, []SitePause{{Site: "other.example", Until: ends}}, n.JinaHealth().SitePauses)

		_, err = n.Fetch(t.Context(), "https://other.example/b")
		requireDeferred(t, err, ends)
		assert.Zero(t, jinaHits("other.example"), "the named site's pages wait without a request")

		blocking.Store(false)
		res, err := n.Fetch(t.Context(), "https://www.news.example/c")
		require.NoError(t, err, "the target's own site isn't held")
		assert.Equal(t, "jina", res.Meta["via"])
	})
	t.Run("no site named", func(t *testing.T) {
		fc := newFakeClock()
		var blocking atomic.Bool
		blocking.Store(true)
		block := fakeAnswer{status: http.StatusForbidden, contentType: "text/plain; charset=utf-8",
			body: "AbuseAlleviationError: Too many requests\n"}
		n, jinaHits := blockingJina(t, fc, &logRecorder{}, answerStatus(http.StatusForbidden), block, &blocking)
		ends := fc.now().Add(jinaSiteBlockDefault)

		_, err := n.Fetch(t.Context(), "https://www.news.example/a")
		requireDeferred(t, err, ends)
		assert.Equal(t, []SitePause{{Site: "news.example", Until: ends}}, n.JinaHealth().SitePauses)
		_, err = n.Fetch(t.Context(), "https://news.example/b")
		requireDeferred(t, err, ends)
		assert.Equal(t, 1, jinaHits("news.example"))
	})
}

// TestNative_JinaSiteBlockWarnsOncePerBlock: Jina calls for a site in
// flight when Jina blocks it all come back blocked. The first answer starts
// the block and warns; the others extend it, to the latest end, silently.
// A block after that one has ended warns again.
func TestNative_JinaSiteBlockWarnsOncePerBlock(t *testing.T) {
	const inFlight = 2
	fc := newFakeClock()
	first, later := fc.now().Add(40*time.Minute), fc.now().Add(45*time.Minute)
	arrived := make(chan struct{}, inFlight)
	release := make(chan struct{})
	var answered atomic.Int32
	logs := &logRecorder{}
	n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: causeJina,
		Log: slog.New(logs)}), fc)
	n.rt = fakeRT(func(target string) (*fetchResponse, error) {
		if !strings.HasPrefix(target, causeJina) {
			return thinOrigin.respond(target)
		}
		arrived <- struct{}{}
		<-release
		end := first
		if answered.Add(1) > 1 {
			end = later
		}
		return siteBlockAnswer(http.StatusForbidden, "news.example", end).respond(target)
	})
	warnings := func() int {
		return logs.count(slog.LevelWarn, "Jina Reader blocks keyless reads of a site, holding its pages")
	}

	var wg sync.WaitGroup
	errs := make([]error, inFlight)
	for i := range errs {
		wg.Go(func() { _, errs[i] = n.Fetch(t.Context(), fmt.Sprintf("https://news.example/%d", i)) })
	}
	for range inFlight {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatalf("expected %d Jina requests in flight", inFlight)
		}
	}
	close(release)
	wg.Wait()

	for _, err := range errs {
		assert.ErrorIs(t, err, errJinaSiteBlocked)
		de, ok := errors.AsType[*DeferError](err)
		require.True(t, ok, "a deferral: %v", err)
		// Whichever answer is recorded first, a page waits for the block as
		// it stood then.
		assert.True(t, de.Until.Equal(first) || de.Until.Equal(later), "until %v", de.Until)
	}
	assert.Equal(t, 1, warnings(), logs.messages())
	assert.Equal(t, []SitePause{{Site: "news.example", Until: later}}, n.JinaHealth().SitePauses,
		"the block ends at the latest extension")

	fc.advance(later.Sub(fc.now()))
	_, err := n.Fetch(t.Context(), "https://news.example/after")
	require.ErrorIs(t, err, errJinaSiteBlocked)
	assert.Equal(t, 2, warnings(), "a block after the last one ended warns again")
}

// TestNative_JinaHealthFromFetches: Jina's health follows its answers to
// fetches. Jina refusing curio's key on five targets across hosts makes it
// failing, with one warning that keeps the key out; its next page ends
// that, with one recovery line.
func TestNative_JinaHealthFromFetches(t *testing.T) {
	const (
		jinaBase = "https://jina.test/"
		key      = "jina_secret_key_123"
	)
	var serving atomic.Bool
	var logs bytes.Buffer
	n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jinaBase,
		JinaAPIKey: key, Log: slog.New(slog.NewTextHandler(&logs, nil))}), newFakeClock())
	n.rt = fakeRT(func(target string) (*fetchResponse, error) {
		switch {
		case !strings.HasPrefix(target, jinaBase):
			return fakeAnswer{status: http.StatusOK, contentType: "text/html", body: thinPage}.respond(target)
		case serving.Load():
			return fakeAnswer{status: http.StatusOK, body: jinaArticleBody()}.respond(target)
		}
		return fakeAnswer{status: http.StatusUnauthorized, contentType: "application/json",
			body: `{"code":401,"name":"AuthenticationFailedError","message":"Invalid API key"}`}.respond(target)
	})
	count := func(msg string) int { return strings.Count(logs.String(), `msg="`+msg+`"`) }

	for i := range failingStreak {
		_, err := n.Fetch(context.Background(), fmt.Sprintf("https://site%d.example/page", i))
		require.ErrorContains(t, err, "jina: HTTP 401 Unauthorized: AuthenticationFailedError: Invalid API key")
	}
	h := n.JinaHealth()
	assert.Equal(t, UpstreamFailing, h.State)
	assert.Equal(t, CallAuth, h.LastFailureClass)
	assert.Equal(t, map[CallClass]int{CallAuth: failingStreak}, h.Recent)
	assert.Equal(t, 1, count("upstream failing"))
	assert.Contains(t, logs.String(), "upstream=jina")

	serving.Store(true)
	res, err := n.Fetch(context.Background(), "https://site9.example/page")
	require.NoError(t, err)
	assert.Equal(t, "jina", res.Meta["via"])
	assert.Equal(t, UpstreamDegraded, n.JinaHealth().State, "5 of 6 calls in the window failed")
	assert.Equal(t, 1, count("upstream recovered"))
	assert.Equal(t, 1, count("upstream failing"))
	assert.NotContains(t, logs.String(), key)
}

// TestNative_JinaHealthIgnoresCancelledRequests: a Jina request that the
// fetch's own cancellation (shutdown) cut short is no news about Jina.
func TestNative_JinaHealthIgnoresCancelledRequests(t *testing.T) {
	const jinaBase = "https://jina.test/"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jinaBase,
		Log: slog.New(slog.NewTextHandler(&logs, nil))}), newFakeClock())
	n.rt = fakeRT(func(target string) (*fetchResponse, error) {
		if !strings.HasPrefix(target, jinaBase) {
			return fakeAnswer{status: http.StatusOK, contentType: "text/html", body: thinPage}.respond(target)
		}
		cancel()
		return nil, &url.Error{Op: "Get", URL: target, Err: context.Canceled}
	})

	_, err := n.Fetch(ctx, "https://news.example/story")
	require.ErrorIs(t, err, context.Canceled)
	h := n.JinaHealth()
	assert.Empty(t, h.Recent)
	assert.True(t, h.LastFailure.IsZero())
	assert.Equal(t, UpstreamIdle, h.State)
	assert.NotContains(t, logs.String(), "upstream")
}
