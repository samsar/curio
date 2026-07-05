package fetcher

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeArticleHTML returns a reasonably article-shaped page so Readability
// will accept it as content. ~600+ chars of body.
func makeArticleHTML(title, body string) string {
	if body == "" {
		body = strings.Repeat("This is a paragraph of an article. ", 30)
	}
	return `<!DOCTYPE html>
<html><head>
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	body := strings.Repeat("Some text here. ", 50) // > 500 chars to bypass length check
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
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
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><p>nope</p></body></html>`))
	}))
	defer source.Close()

	// Fake Jina: emit a header block + body.
	jinaBody := strings.Repeat("This is the article body from Jina. ", 20)
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false})
	_, err := n.Fetch(context.Background(), srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}

func TestParseJina(t *testing.T) {
	in := `Title: My Article
URL Source: https://example.com/x
Published Time: 2024-01-15T10:00:00Z

Markdown Content:
# Heading

This is the body of the article.
`
	got := parseJina(in)
	assert.Equal(t, "My Article", got.title)
	assert.Equal(t, "https://example.com/x", got.urlSource)
	assert.Equal(t, "2024-01-15T10:00:00Z", got.published)
	assert.Contains(t, got.body, "Heading")
	assert.Contains(t, got.body, "body of the article")
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
	assert.Equal(t, "pdf", res.ContentType)
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

// TestNative_Soft404_TitleDetected: HTTP 200 carrying a not-found page
// (long enough to dodge the thin-content check) is a dead link, not a
// login wall — so it must NOT fall back to Jina.
func TestNative_Soft404_TitleDetected(t *testing.T) {
	body := strings.Repeat("The page you are looking for may have moved. Try the search box or browse our sitemap. ", 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>404 - Page Not Found | Example Site</title></head>
			<body><article><h1>Page not found</h1><p>` + body + `</p></article></body></html>`))
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
	assert.ErrorAs(t, err, &pe)
	assert.ErrorIs(t, err, ErrDeadLink)
	assert.Contains(t, err.Error(), "not-found page")
	assert.Equal(t, 0, jinaCalls)
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
	dead := []string{
		"404 Not Found",
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
	}
	for _, title := range dead {
		assert.True(t, soft404TitleRE.MatchString(title), "should match %q", title)
	}

	alive := []string{
		"Understanding HTTP 404s and how to avoid them",
		"How we redesigned our 404 experience", // "our 404 experience" — no boundary hit
		"Finding lost cities: places not found on any map",
		"The Signal and the Noise",
		"Go 1.25 release notes",
	}
	for _, title := range alive {
		assert.False(t, soft404TitleRE.MatchString(title), "should NOT match %q", title)
	}
}
