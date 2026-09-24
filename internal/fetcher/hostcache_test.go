package fetcher

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jinaMode is how a fake Jina answers.
type jinaMode string

const (
	jinaOff         jinaMode = "off"
	jinaThin        jinaMode = "thin 2xx"
	jinaRateLimited jinaMode = "429"
	jinaDown        jinaMode = "500"
	jinaUnreachable jinaMode = "unreachable"
)

var allJinaModes = []jinaMode{jinaOff, jinaThin, jinaRateLimited, jinaDown, jinaUnreachable}

// newNativeWithJina builds a Native whose Jina fallback behaves per mode,
// on a fake clock so Jina's retry backoff doesn't sleep. It returns the
// number of Jina requests made so far.
func newNativeWithJina(t *testing.T, mode jinaMode) (*Native, func() int32) {
	t.Helper()
	var hits atomic.Int32
	opts := NativeOptions{Timeout: 5 * time.Second}
	switch mode {
	case jinaOff:
	case jinaUnreachable:
		opts.JinaFallback, opts.JinaBaseURL = true, "http://"+closedAddr(t)+"/"
	default:
		jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			switch mode {
			case jinaThin:
				_, _ = w.Write([]byte("Title: x\n\nMarkdown Content:\ntoo short"))
			case jinaRateLimited:
				w.WriteHeader(http.StatusTooManyRequests)
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		t.Cleanup(jina.Close)
		opts.JinaFallback, opts.JinaBaseURL = true, jina.URL+"/"
	}
	return unpaced(NewNative(opts), newFakeClock()), hits.Load
}

// closedAddr returns a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// localhostURL rewrites an httptest URL onto "localhost", a different
// host-cache key than "127.0.0.1" for the same server.
func localhostURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	u.Host = "localhost:" + u.Port()
	return u.String()
}

// TestNative_PageLevelVerdictsAreNotHostCached: a verdict about one page
// never fails the rest of its host, whether Jina is off or on and whatever
// Jina answers.
func TestNative_PageLevelVerdictsAreNotHostCached(t *testing.T) {
	article := makeArticleHTML("A real article", "")
	pages := map[string]func(w http.ResponseWriter, r *http.Request, other string){
		"thin text": func(w http.ResponseWriter, _ *http.Request, _ string) {
			_, _ = w.Write([]byte(thinPage))
		},
		"no article": func(w http.ResponseWriter, _ *http.Request, _ string) {
			_, _ = w.Write([]byte(`<html><head><title>x</title></head><body></body></html>`))
		},
		"login-like title": func(w http.ResponseWriter, _ *http.Request, _ string) {
			_, _ = w.Write([]byte(`<html><head><title>Sign in to read this</title></head><body><article><h1>Sign in to read this</h1><p>` +
				strings.Repeat("Some text here. ", 50) + `</p></article></body></html>`))
		},
		"cross-host redirect": func(w http.ResponseWriter, r *http.Request, other string) {
			http.Redirect(w, r, other+"/elsewhere", http.StatusFound)
		},
	}
	for pageName, page := range pages {
		for _, mode := range allJinaModes {
			t.Run(pageName+"/jina "+string(mode), func(t *testing.T) {
				var originHits atomic.Int32
				var other string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					originHits.Add(1)
					switch r.URL.Path {
					case "/a":
						page(w, r, other)
					default:
						_, _ = w.Write([]byte(article))
					}
				}))
				defer srv.Close()
				other = localhostURL(t, srv.URL)

				n, _ := newNativeWithJina(t, mode)
				_, err := n.Fetch(context.Background(), srv.URL+"/a")
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrLoginWall)

				res, err := n.Fetch(context.Background(), srv.URL+"/b")
				require.NoError(t, err, "a page-level verdict must not fail the rest of the host")
				assert.Equal(t, "readability", res.Meta["via"])
			})
		}
	}
}

// TestNative_JinaSideFailuresDoNotCache: when Jina itself fails (rate
// limit, outage, unreachable), an origin 403 is not cached and the error
// stays retryable; the next URL on the host still reaches the origin.
func TestNative_JinaSideFailuresDoNotCache(t *testing.T) {
	for _, mode := range []jinaMode{jinaRateLimited, jinaDown, jinaUnreachable} {
		t.Run(string(mode), func(t *testing.T) {
			var originHits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				originHits.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer srv.Close()

			n, _ := newNativeWithJina(t, mode)
			for i, path := range []string{"/a", "/b"} {
				_, err := n.Fetch(context.Background(), srv.URL+path)
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrAntiBot)
				var pe *PermanentError
				assert.False(t, errors.As(err, &pe), "Jina's trouble must leave the error retryable: %v", err)
				assert.NotContains(t, err.Error(), "(cached:")
				assert.Equal(t, int32(i+1), originHits.Load())
			}
		})
	}
}

// TestNative_AntiBotCachedWhenNoPathLeft: an origin 403 is cached when Jina
// is off or itself answers with too little content. The first failure is
// retryable; the next URL on the host fails permanently from the cache.
func TestNative_AntiBotCachedWhenNoPathLeft(t *testing.T) {
	for _, mode := range []jinaMode{jinaOff, jinaThin} {
		t.Run(string(mode), func(t *testing.T) {
			var originHits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				originHits.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer srv.Close()

			n, _ := newNativeWithJina(t, mode)
			_, err := n.Fetch(context.Background(), srv.URL+"/a")
			require.ErrorIs(t, err, ErrAntiBot)
			var pe *PermanentError
			assert.False(t, errors.As(err, &pe), "first failure must stay retryable: %v", err)

			_, err = n.Fetch(context.Background(), srv.URL+"/b")
			require.ErrorAs(t, err, &pe)
			assert.ErrorIs(t, err, ErrAntiBot)
			assert.Contains(t, err.Error(), "(cached:")
			assert.Equal(t, int32(1), originHits.Load())
		})
	}
}

// TestNative_HostCacheKeyedByAnsweringHost: a verdict is cached under the
// host that gave it. A redirect from S to a D that blocks us, or that is
// down, caches D and leaves S alone, so a link shortener isn't failed for
// one bad destination.
func TestNative_HostCacheKeyedByAnsweringHost(t *testing.T) {
	for _, backend := range []string{"chrome", "stock"} {
		t.Run(backend+"/destination blocks", func(t *testing.T) {
			dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			}))
			defer dest.Close()
			destURL := localhostURL(t, dest.URL)
			src := newRedirectingServer(t, destURL+"/x")

			n := NewNative(NativeOptions{Timeout: 5 * time.Second, Backend: backend})
			_, err := n.Fetch(context.Background(), src.URL+"/a")
			require.ErrorIs(t, err, ErrAntiBot)

			res, err := n.Fetch(context.Background(), src.URL+"/b")
			require.NoError(t, err, "the redirecting host must not be cached")
			assert.Equal(t, "readability", res.Meta["via"])

			_, err = n.Fetch(context.Background(), destURL+"/y")
			var pe *PermanentError
			require.ErrorAs(t, err, &pe)
			assert.ErrorIs(t, err, ErrAntiBot)
			assert.Contains(t, err.Error(), "(cached:")
		})
		t.Run(backend+"/destination down", func(t *testing.T) {
			destURL := "http://localhost:" + strings.Split(closedAddr(t), ":")[1]
			src := newRedirectingServer(t, destURL+"/x")

			n := NewNative(NativeOptions{Timeout: 5 * time.Second, Backend: backend})
			_, err := n.Fetch(context.Background(), src.URL+"/a")
			require.ErrorIs(t, err, ErrHostUnreachable)
			var ue *url.Error
			assert.ErrorAs(t, err, &ue, "the transport error must stay reachable")

			_, err = n.Fetch(context.Background(), src.URL+"/b")
			require.NoError(t, err, "the redirecting host must not be cached")

			_, err = n.Fetch(context.Background(), destURL+"/y")
			var pe *PermanentError
			require.ErrorAs(t, err, &pe)
			assert.ErrorIs(t, err, ErrHostUnreachable)
			assert.Contains(t, err.Error(), "(cached:")
		})
	}
}

// newRedirectingServer serves an article everywhere except /a, which
// redirects to target.
func newRedirectingServer(t *testing.T, target string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a" {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(makeArticleHTML("Healthy", "")))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestNative_PageLevelLoginWallIsFinal: once every configured extraction
// path has answered, a page-level login wall fails permanently instead of
// repeating origin and Jina calls on every retry. Jina's own trouble keeps
// it retryable.
func TestNative_PageLevelLoginWallIsFinal(t *testing.T) {
	cases := []struct {
		mode      jinaMode
		permanent bool
		jinaCalls int32
	}{
		{jinaOff, true, 0},
		{jinaThin, true, 1},
		{jinaRateLimited, false, 4},
		{jinaDown, false, 4},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			srv := serveThinPage(t)
			defer srv.Close()

			n, jinaCalls := newNativeWithJina(t, tc.mode)
			_, err := n.Fetch(context.Background(), srv.URL)
			require.ErrorIs(t, err, ErrLoginWall)
			var pe *PermanentError
			assert.Equal(t, tc.permanent, errors.As(err, &pe), "permanent: %v", err)
			assert.Equal(t, tc.jinaCalls, jinaCalls())
		})
	}
}

// TestNative_CombinedErrorKeepsBothChains: when origin and Jina both fail,
// the returned error matches the origin's sentinel and Jina's status.
func TestNative_CombinedErrorKeepsBothChains(t *testing.T) {
	srv := serveThinPage(t)
	defer srv.Close()

	n, _ := newNativeWithJina(t, jinaRateLimited)
	_, err := n.Fetch(context.Background(), srv.URL)
	assert.ErrorIs(t, err, ErrLoginWall)
	var se *HTTPStatusError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, http.StatusTooManyRequests, se.StatusCode)
}

// TestNative_UnreachableClassification drives Fetch with constructed
// transport errors: only a host that doesn't exist or refuses connections
// is cached as unreachable. Resolver hiccups and our own network being
// down stay retryable and uncached.
func TestNative_UnreachableClassification(t *testing.T) {
	dial := func(err error) error {
		return &url.Error{Op: "Get", URL: "https://dead.example/x", Err: &net.OpError{Op: "dial", Net: "tcp", Err: err}}
	}
	cases := []struct {
		name        string
		err         error
		unreachable bool
		dns         bool
	}{
		{"nxdomain", dial(&net.DNSError{Err: "no such host", Name: "dead.example", IsNotFound: true}), true, true},
		{"resolver timeout", dial(&net.DNSError{Err: "i/o timeout", Name: "dead.example", IsTimeout: true}), false, true},
		{"temporary dns failure", dial(&net.DNSError{Err: "server misbehaving", Name: "dead.example", IsTemporary: true}), false, true},
		{"connection refused", dial(os.NewSyscallError("connect", syscall.ECONNREFUSED)), true, false},
		{"no route to host", dial(os.NewSyscallError("connect", syscall.EHOSTUNREACH)), true, false},
		{"network unreachable", dial(os.NewSyscallError("connect", syscall.ENETUNREACH)), false, false},
		{"reset", dial(os.NewSyscallError("read", syscall.ECONNRESET)), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.unreachable, isHostUnreachable(tc.err))

			n := NewNative(NativeOptions{Timeout: 5 * time.Second})
			n.rt = fakeRT(func(string) (*fetchResponse, error) { return nil, tc.err })
			_, err := n.Fetch(context.Background(), "https://dead.example/x")
			require.Error(t, err)
			assert.Equal(t, tc.unreachable, errors.Is(err, ErrHostUnreachable))
			var ue *url.Error
			assert.ErrorAs(t, err, &ue)
			var dnsErr *net.DNSError
			assert.Equal(t, tc.dns, errors.As(err, &dnsErr), "the resolver error must stay reachable")

			_, cached := n.hostCache.Get("dead.example")
			assert.Equal(t, tc.unreachable, cached)
		})
	}
}

// TestNative_DeadLinkNeverCached: a hard 404 is permanent, never goes to
// Jina and says nothing about the rest of the host.
func TestNative_DeadLinkNeverCached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gone" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(makeArticleHTML("Alive", "")))
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, DeadLinkDetection: true})
	_, err := n.Fetch(context.Background(), srv.URL+"/gone")
	require.ErrorIs(t, err, ErrDeadLink)
	_, err = n.Fetch(context.Background(), srv.URL+"/alive")
	require.NoError(t, err)
}

func TestHostOf(t *testing.T) {
	assert.Equal(t, "example.com", hostOf("https://Example.COM:8443/x"))
	assert.Equal(t, "::1", hostOf("http://[::1]:80/"))
	assert.Empty(t, hostOf("://bad"))
}

func TestSameSiteHost(t *testing.T) {
	assert.True(t, sameSiteHost("example.com", "www.example.com"))
	assert.True(t, sameSiteHost("WWW.Example.com", "example.com"))
	assert.False(t, sameSiteHost("example.com", "login.example.com"))
	assert.False(t, sameSiteHost("127.0.0.1", "localhost"))
}

// parseArticle runs Readability over html as if served from pageURL.
func parseArticle(t *testing.T, html, pageURL string) readability.Article {
	t.Helper()
	u, err := url.Parse(pageURL)
	require.NoError(t, err)
	article, err := readability.FromReader(strings.NewReader(html), u)
	require.NoError(t, err)
	return article
}

// TestLooksLikeLoginWall_WWWIsSameSite: an apex↔www redirect is
// canonicalization, not a login wall.
func TestLooksLikeLoginWall_WWWIsSameSite(t *testing.T) {
	cases := []struct{ source, final string }{
		{"https://example.com/p", "https://www.example.com/p"},
		{"https://www.example.com/p", "https://example.com/p"},
	}
	for _, tc := range cases {
		article := parseArticle(t, makeArticleHTML("Full article", ""), tc.final)
		final, err := url.Parse(tc.final)
		require.NoError(t, err)
		reason, siteWide := looksLikeLoginWall(article, final, tc.source)
		assert.Empty(t, reason, "%s → %s", tc.source, tc.final)
		assert.False(t, siteWide)
	}
}

// TestLooksLikeLoginWall_Redirects: a redirect onto the requested site's
// login page is site-wide; a redirect to another site is flagged but is
// only about this page.
func TestLooksLikeLoginWall_Redirects(t *testing.T) {
	article := parseArticle(t, makeArticleHTML("Full article", ""), "https://example.com/login")
	cases := []struct {
		source, final string
		siteWide      bool
	}{
		{"https://example.com/post", "https://example.com/login", true},
		{"https://example.com/post", "https://www.example.com/authwall", true},
		{"https://example.com/login", "https://example.com/login", false}, // bookmarked login page, no redirect
		{"https://example.com/post", "https://other.example/post", false},
	}
	for _, tc := range cases {
		final, err := url.Parse(tc.final)
		require.NoError(t, err)
		reason, siteWide := looksLikeLoginWall(article, final, tc.source)
		assert.NotEmpty(t, reason, "%s → %s", tc.source, tc.final)
		assert.Equal(t, tc.siteWide, siteWide, "%s → %s", tc.source, tc.final)
	}
}

// TestLooksLikeSoft404_WWWHomepage: a deleted article that settles on the
// www homepage is a dead link, not a cross-site redirect.
func TestLooksLikeSoft404_WWWHomepage(t *testing.T) {
	final, err := url.Parse("https://www.example.com/")
	require.NoError(t, err)
	assert.Equal(t, "redirected to homepage",
		looksLikeSoft404(readability.Article{}, final, "https://example.com/deleted-post"))
}

// TestNative_CrossHostRedirectStillFlagged: the documented cross-site
// heuristic is kept (127.0.0.1 → localhost), and never host-cached.
func TestNative_CrossHostRedirectStillFlagged(t *testing.T) {
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(makeArticleHTML("Moved", "")))
	}))
	defer dest.Close()
	src := newRedirectingServer(t, localhostURL(t, dest.URL)+"/x")

	n := NewNative(NativeOptions{Timeout: 5 * time.Second})
	_, err := n.Fetch(context.Background(), src.URL+"/a")
	require.ErrorIs(t, err, ErrLoginWall)
	assert.Contains(t, err.Error(), "redirected to a different host")
	_, cached := n.hostCache.Get(hostOf(src.URL))
	assert.False(t, cached)
	_, cached = n.hostCache.Get("localhost")
	assert.False(t, cached)
}
