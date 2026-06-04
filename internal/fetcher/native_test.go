package fetcher

import (
	"context"
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

// TestNative_RejectsNonHTMLContentType guards the PDF-misrouting fix: a URL
// serving application/pdf (or any non-HTML type) must fail with a
// PermanentError — not get its bytes fed to the HTML parser, and not be
// retried. Regression test for "html: open stack of elements exceeds 512
// nodes" on a 6 MB PDF that re-downloaded on every attempt.
func TestNative_RejectsNonHTMLContentType(t *testing.T) {
	for _, ct := range []string{"application/pdf", "image/png", "application/octet-stream"} {
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
