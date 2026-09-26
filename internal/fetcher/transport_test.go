package fetcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChromeProfiles_Coherent: every profile's User-Agent and sec-ch-ua
// name the profile's own Chrome version.
func TestChromeProfiles_Coherent(t *testing.T) {
	uaVersionRE := regexp.MustCompile(`Chrome/(\d+)\.`)
	brandRE := regexp.MustCompile(`"(Google Chrome|Chromium)";v="(\d+)"`)
	for _, p := range chromeProfiles {
		t.Run(p.name, func(t *testing.T) {
			major := strconv.Itoa(p.major)
			assert.Equal(t, "chrome_"+major, p.name)

			m := uaVersionRE.FindStringSubmatch(p.userAgent)
			require.Len(t, m, 2, p.userAgent)
			assert.Equal(t, major, m[1])

			brands := brandRE.FindAllStringSubmatch(p.secChUA, -1)
			require.Len(t, brands, 2, "sec-ch-ua must name Google Chrome and Chromium: %s", p.secChUA)
			for _, b := range brands {
				assert.Equal(t, major, b[2], b[1])
			}
		})
	}
}

// TestNative_HeadersFollowProfile: without a user_agent override, the
// User-Agent and sec-ch-ua sent match the selected profile; the stock
// backend uses the latest one.
func TestNative_HeadersFollowProfile(t *testing.T) {
	cases := []struct{ backend, major string }{
		{"", "133"},
		{"chrome_120", "120"},
		{"chrome_124", "124"},
		{"stock", "133"},
	}
	for _, tc := range cases {
		t.Run("backend="+tc.backend, func(t *testing.T) {
			var ua, chUA string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ua, chUA = r.Header.Get("User-Agent"), r.Header.Get("Sec-Ch-Ua")
				_, _ = w.Write([]byte(makeArticleHTML("Headers", "")))
			}))
			defer srv.Close()

			n := NewNative(NativeOptions{Timeout: 5 * time.Second, Backend: tc.backend})
			_, err := n.Fetch(context.Background(), srv.URL)
			require.NoError(t, err)
			assert.Contains(t, ua, "Chrome/"+tc.major+".0.0.0")
			assert.Contains(t, chUA, `"Google Chrome";v="`+tc.major+`"`)
			assert.Contains(t, chUA, `"Chromium";v="`+tc.major+`"`)
		})
	}
}

// TestNewNative_WarnsOnMismatchedUserAgent: an override naming a different
// Chrome version than the profile is honored but logged once.
func TestNewNative_WarnsOnMismatchedUserAgent(t *testing.T) {
	cases := []struct {
		ua       string
		warnings int
	}{
		{chromeUA(133), 1},
		{chromeUA(120), 0},
		{"curio-test/1.0", 1},
	}
	for _, tc := range cases {
		t.Run(tc.ua, func(t *testing.T) {
			var logs bytes.Buffer
			n := NewNative(NativeOptions{Backend: "chrome_120", UserAgent: tc.ua, Log: slog.New(slog.NewTextHandler(&logs, nil))})
			assert.Equal(t, tc.ua, n.userAgent, "an override is sent as is")
			assert.Equal(t, tc.warnings, strings.Count(logs.String(), "user_agent doesn't name"))
		})
	}
}

// plainHTTPSite serves one site, example.com, as a browser would reach it:
// https (HTTP/2, a certificate from ca) and plain http, both answering
// with handler. It returns the routes a chrome backend dials it by.
func plainHTTPSite(t *testing.T, ca *testCA, hosts []string, handler http.Handler) map[string]string {
	t.Helper()
	secure := newTLSServer(t, ca.validLeaf(t, hosts...), handler)
	plain := httptest.NewServer(handler)
	t.Cleanup(plain.Close)
	routes := map[string]string{}
	for _, h := range hosts {
		routes[net.JoinHostPort(h, "443")] = secure.Listener.Addr().String()
		routes[net.JoinHostPort(h, "80")] = plain.Listener.Addr().String()
	}
	return routes
}

// seenRequest is what a test site saw of one request.
type seenRequest struct {
	secure                   bool
	proto, host, path, refer string
}

// requestLog records the requests a test site sees.
type requestLog struct {
	mu   sync.Mutex
	seen []seenRequest
}

func (l *requestLog) record(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, seenRequest{r.TLS != nil, r.Proto, r.Host, r.URL.Path, r.Referer()})
}

func (l *requestLog) all() []seenRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seen)
}

// redirectingSite answers every path with 200, except:
//
//	/redir → https://<host>/final   (a same-host upgrade to https)
//	/down  → http://<host>/plain    (a downgrade)
//	/rel   → /plain2                (relative)
//	/loop  → /loop
func redirectingSite(log *requestLog) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		switch r.URL.Path {
		case "/redir":
			http.Redirect(w, r, "https://"+r.Host+"/final", http.StatusMovedPermanently)
		case "/down":
			http.Redirect(w, r, "http://"+r.Host+"/plain", http.StatusMovedPermanently)
		case "/rel":
			http.Redirect(w, r, "/plain2", http.StatusMovedPermanently)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusMovedPermanently)
		default:
			_, _ = io.WriteString(w, "ok")
		}
	})
}

// chromeGet fetches target through rt and returns the settled URL.
func chromeGet(t *testing.T, rt roundTripper, target string) string {
	t.Helper()
	resp, err := rt.do(t.Context(), target, nil)
	require.NoError(t, err, target)
	defer resp.body.Close()
	_, err = io.Copy(io.Discard, resp.body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.statusCode, target)
	return resp.finalURL.String()
}

// TestChromeRT_PlainAndSecureShareAHost: http:// and https:// URLs on one
// host work in any order and across redirects either way. tls-client keys
// its cached transports by host:443 whatever the scheme, so without the :80
// pin the second scheme to reach a host failed with "http2: unsupported
// scheme" or "protocol negotiated". The pin never shows: the site sees a
// port-free Host and Referer, and the settled URL has no :80.
func TestChromeRT_PlainAndSecureShareAHost(t *testing.T) {
	ca := newTestCA(t)
	cases := []struct {
		name    string
		targets []string
		final   []string
	}{
		{
			name:    "https, http, https",
			targets: []string{"https://example.com/", "http://example.com/", "https://example.com/"},
			final:   []string{"https://example.com/", "http://example.com/", "https://example.com/"},
		},
		{
			name:    "http, https, http",
			targets: []string{"http://example.com/", "https://example.com/", "http://example.com/"},
			final:   []string{"http://example.com/", "https://example.com/", "http://example.com/"},
		},
		{
			name:    "upgrade redirect, twice",
			targets: []string{"http://example.com/redir", "http://example.com/redir"},
			final:   []string{"https://example.com/final", "https://example.com/final"},
		},
		{
			name:    "downgrade redirect",
			targets: []string{"https://example.com/down"},
			final:   []string{"http://example.com/plain"},
		},
		{
			name:    "relative redirect",
			targets: []string{"http://example.com/rel"},
			final:   []string{"http://example.com/plain2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var log requestLog
			rt := newRoutedChromeRT(t, ca.pool, plainHTTPSite(t, ca, []string{"example.com"}, redirectingSite(&log)))
			for i, target := range tc.targets {
				assert.Equal(t, tc.final[i], chromeGet(t, rt, target))
			}
			for _, r := range log.all() {
				assert.Equal(t, "example.com", r.host, "Host of %s", r.path)
				assert.NotContains(t, r.refer, ":80", "Referer of %s", r.path)
				if r.secure {
					assert.Equal(t, "HTTP/2.0", r.proto, r.path)
				}
			}
		})
	}
}

// TestChromeRT_UpgradeRefererIsPortFree: the https hop after a pinned http
// hop names that hop, without the pin, as its Referer.
func TestChromeRT_UpgradeRefererIsPortFree(t *testing.T) {
	ca := newTestCA(t)
	var log requestLog
	rt := newRoutedChromeRT(t, ca.pool, plainHTTPSite(t, ca, []string{"example.com"}, redirectingSite(&log)))
	chromeGet(t, rt, "http://example.com/redir")

	seen := log.all()
	require.Len(t, seen, 2)
	assert.Equal(t, "/final", seen[1].path)
	assert.Equal(t, "http://example.com/redir", seen[1].refer)
}

// TestChromeRT_RedirectLimit: the redirect policy the pin rides on keeps
// fhttp's own limit.
func TestChromeRT_RedirectLimit(t *testing.T) {
	ca := newTestCA(t)
	var log requestLog
	rt := newRoutedChromeRT(t, ca.pool, plainHTTPSite(t, ca, []string{"example.com"}, redirectingSite(&log)))
	_, err := rt.do(t.Context(), "http://example.com/loop", nil)
	require.ErrorContains(t, err, "stopped after 10 redirects")
	assert.Len(t, log.all(), maxRedirects)
}

// TestChromeRT_ErrorNamesPortFreeURL: a transport error on a pinned
// request names the URL as requested, and is still classified by what
// went wrong.
func TestChromeRT_ErrorNamesPortFreeURL(t *testing.T) {
	rt := newRoutedChromeRT(t, nil, map[string]string{"example.com:80": closedAddr(t)})
	_, err := rt.do(t.Context(), "http://example.com/x", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"http://example.com/x"`)
	assert.NotContains(t, err.Error(), ":80")
	var ue *url.Error
	require.ErrorAs(t, err, &ue)
	assert.Equal(t, "http://example.com/x", ue.URL)
	assert.True(t, isHostUnreachable(err))
	kind, host, ok := hostVerdict(fmt.Errorf("%w: %w", ErrHostUnreachable, err), "example.com")
	assert.True(t, ok)
	assert.Equal(t, HostFailUnreachable, kind)
	assert.Equal(t, "example.com", host)
}

// TestChromeRT_ExplicitPortsUntouched: a URL that names its port is sent
// as is, whatever the port.
func TestChromeRT_ExplicitPortsUntouched(t *testing.T) {
	var log requestLog
	srv := httptest.NewServer(redirectingSite(&log))
	defer srv.Close()
	rt, err := newChromeRT(5*time.Second, chromeProfiles[0])
	require.NoError(t, err)

	assert.Equal(t, srv.URL+"/plain2", chromeGet(t, rt, srv.URL+"/rel"))
	for _, r := range log.all() {
		assert.Equal(t, srv.Listener.Addr().String(), r.host)
	}
}

// TestChromeRT_ConcurrentUpgradeRedirects: many fetches at once, each an
// http → https redirect, across several hosts on one client. Without the
// :80 pin tls-client writes its transport cache unlocked on this path; the
// race detector reports it, and outside it the runtime can abort with a
// concurrent map write.
func TestChromeRT_ConcurrentUpgradeRedirects(t *testing.T) {
	ca := newTestCA(t)
	hosts := []string{"a.example", "b.example", "c.example", "d.example", "e.example"}
	var log requestLog
	rt := newRoutedChromeRT(t, ca.pool, plainHTTPSite(t, ca, hosts, redirectingSite(&log)))

	var wg sync.WaitGroup
	for i := range 40 {
		host := hosts[i%len(hosts)]
		wg.Go(func() {
			resp, err := rt.do(context.Background(), "http://"+host+"/redir", nil)
			if !assert.NoError(t, err, host) {
				return
			}
			defer resp.body.Close()
			assert.Equal(t, http.StatusOK, resp.statusCode)
			assert.Equal(t, "https://"+host+"/final", resp.finalURL.String())
		})
	}
	wg.Wait()
}
