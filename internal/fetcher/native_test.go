package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
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
	body := strings.Repeat("Some text here. ", 50) // > minArticleBytes to bypass length check
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

// TestNative_HostCache_HitIsPermanent: a redirect onto the site's own
// login page is a host-wide verdict. The first failure is a plain retryable
// error (it populates the cache); every fetch on that host within the TTL
// short-circuits as a PermanentError carrying the same sentinel plus a
// "(cached: …)" suffix, and never contacts the origin.
func TestNative_HostCache_HitIsPermanent(t *testing.T) {
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`<html><head><title>Log in</title></head><body><p>Please sign in.</p></body></html>`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false})

	// First attempt: real fetch, retryable login-wall error.
	_, err := n.Fetch(context.Background(), srv.URL+"/first")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLoginWall)
	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "first failure must stay retryable: %v", err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits), "redirect + login page")

	// Second attempt, same host, different path: served from the host
	// cache, permanent, origin not contacted.
	_, err = n.Fetch(context.Background(), srv.URL+"/second")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLoginWall)
	require.True(t, errors.As(err, &pe), "cache hit must be permanent: %v", err)
	assert.Contains(t, err.Error(), "(cached:")
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits), "cache hit must not contact origin")
}

// Same contract for the anti-bot kind (HTTP 403 → ErrAntiBot).
func TestNative_HostCache_AntiBotHitIsPermanent(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: false})

	_, err := n.Fetch(context.Background(), srv.URL+"/a")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAntiBot)
	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "first 403 must stay retryable: %v", err)

	_, err = n.Fetch(context.Background(), srv.URL+"/b")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAntiBot)
	require.True(t, errors.As(err, &pe), "cached 403 must be permanent: %v", err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
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

// unpaced strips n's waits for tests: an unlimited Jina limiter, and fc
// as the clock so backoffs and cooldowns are recorded instead of slept.
func unpaced(n *Native, fc *fakeClock) *Native {
	n.jinaLimiter = rate.NewLimiter(rate.Inf, 1)
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
					body := "Title: Big PDF\n\nMarkdown Content:\n" + strings.Repeat("Rendered PDF text. ", 20)
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
			assert.Equal(t, "pdf", res.ContentType)
		})
	}
}
