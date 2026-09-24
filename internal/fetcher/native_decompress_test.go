package fetcher

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/andybalholm/brotli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests guard against the class of regression where a compressed
// response is stored/returned WITHOUT being decompressed — i.e. the fetcher
// hands Readability raw gzip/brotli bytes and we persist binary garbage
// instead of markdown.
//
// NOTE: these run over httptest, which is HTTP/1.1. fhttp's h1 (and h2) paths
// already auto-decompress gzip AND brotli, so these pass even without the
// defensive decompress in chromeRT.do. The actual production regression was
// HTTP/3-specific (fhttp's h3 path does NOT auto-decompress) and is caught by
// the live test in native_integration_test.go, which fetches a real h3 site.
// These remain as a fast, deterministic floor on decompression behavior.

func mustGzip(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write(b)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func mustBrotli(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	_, err := w.Write(b)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// serveEncoded returns a server that always responds with the given
// Content-Encoding and pre-encoded body, ignoring Accept-Encoding. That lets
// a test force a specific encoding onto a specific backend.
func serveEncoded(t *testing.T, encoding string, encoded []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Encoding", encoding)
		_, _ = w.Write(encoded)
	}))
}

// assertReadableMarkdown fails if md looks like undecoded binary rather than
// text: invalid UTF-8, or a high proportion of control/replacement runes.
func assertReadableMarkdown(t *testing.T, md string) {
	t.Helper()
	require.NotEmpty(t, md, "markdown is empty")
	require.True(t, utf8.ValidString(md),
		"markdown is not valid UTF-8 — looks like undecoded compressed bytes")
	var bad, total int
	for _, r := range md {
		total++
		if r == utf8.RuneError || (unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r') {
			bad++
		}
	}
	require.Positive(t, total)
	ratio := float64(bad) / float64(total)
	require.Less(t, ratio, 0.05,
		"markdown is %.1f%% non-text runes — looks like binary/compressed garbage", ratio*100)
}

// TestNative_RendersMarkdown_Brotli asserts a brotli response comes back as
// readable markdown over the chrome backend. (h1/h2 decompress brotli
// natively in fhttp; the h3 gap is covered by the live integration test.)
func TestNative_RendersMarkdown_Brotli(t *testing.T) {
	html := makeArticleHTML("Brotli Article", "")
	srv := serveEncoded(t, "br", mustBrotli(t, []byte(html)))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, Backend: "chrome", JinaFallback: false})
	res, err := n.Fetch(context.Background(), srv.URL)
	require.NoError(t, err)
	assertReadableMarkdown(t, res.Markdown)
	assert.Contains(t, res.Markdown, "paragraph of an article")
	assert.Equal(t, "Brotli Article", res.Title)
}

// TestNative_RendersMarkdown_Gzip checks gzip decompression on both backends.
func TestNative_RendersMarkdown_Gzip(t *testing.T) {
	for _, backend := range []string{"chrome", "stock"} {
		t.Run(backend, func(t *testing.T) {
			html := makeArticleHTML("Gzip Article", "")
			srv := serveEncoded(t, "gzip", mustGzip(t, []byte(html)))
			defer srv.Close()

			n := NewNative(NativeOptions{Timeout: 5 * time.Second, Backend: backend, JinaFallback: false})
			res, err := n.Fetch(context.Background(), srv.URL)
			require.NoError(t, err)
			assertReadableMarkdown(t, res.Markdown)
			assert.Contains(t, res.Markdown, "paragraph of an article")
		})
	}
}

// testBodyLimit is the body cap the size tests inject: small enough that
// hitting it takes milliseconds, far below the client timeout.
const testBodyLimit = 64 << 10

// TestNative_EndlessHTMLHitsBodyCap: a page that never stops streaming
// fails permanently at the body cap on both backends, well before the
// client timeout. It is not sent to Jina and not host-cached.
func TestNative_EndlessHTMLHitsBodyCap(t *testing.T) {
	for _, backend := range []string{"chrome", "stock"} {
		t.Run(backend, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html><body><article><p>"))
				chunk := []byte(strings.Repeat("endless words ", 1024))
				for written := 0; written < 64<<20 && r.Context().Err() == nil; written += len(chunk) {
					if _, err := w.Write(chunk); err != nil {
						return
					}
				}
			}))
			defer srv.Close()

			var jinaHits atomic.Int32
			jina := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { jinaHits.Add(1) }))
			defer jina.Close()

			n := NewNative(NativeOptions{Timeout: 30 * time.Second, Backend: backend, JinaFallback: true, JinaBaseURL: jina.URL + "/"})
			n.rt = limitBodies(n.rt, testBodyLimit)
			start := time.Now()
			_, err := n.Fetch(context.Background(), srv.URL+"/a")
			assert.Less(t, time.Since(start), 10*time.Second)

			var pe *PermanentError
			require.ErrorAs(t, err, &pe)
			assert.ErrorIs(t, err, ErrTooLarge)
			assert.Zero(t, jinaHits.Load(), "an oversized page must not go to Jina")

			_, ok := n.hostCache.Get(hostOf(srv.URL))
			assert.False(t, ok, "an oversized page says nothing about the host")
		})
	}
}

// TestNative_DecompressionBombHitsBodyCap: the cap applies to decoded
// bytes, so a small gzip body that inflates past it is rejected.
func TestNative_DecompressionBombHitsBodyCap(t *testing.T) {
	bomb := mustGzip(t, []byte("<html><body><p>"+strings.Repeat("a", 8<<20)+"</p></body></html>"))
	require.Less(t, len(bomb), testBodyLimit, "the compressed body itself must fit under the cap")
	for _, backend := range []string{"chrome", "stock"} {
		t.Run(backend, func(t *testing.T) {
			srv := serveEncoded(t, "gzip", bomb)
			defer srv.Close()

			n := NewNative(NativeOptions{Timeout: 30 * time.Second, Backend: backend})
			n.rt = limitBodies(n.rt, testBodyLimit)
			_, err := n.Fetch(context.Background(), srv.URL)
			var pe *PermanentError
			require.ErrorAs(t, err, &pe)
			assert.ErrorIs(t, err, ErrTooLarge)
		})
	}
}
