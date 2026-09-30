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
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// jinaMode is how a fake Jina answers.
type jinaMode string

const (
	jinaOff         jinaMode = "off"
	jinaThin        jinaMode = "thin 2xx"
	jinaChallenge   jinaMode = "challenge 2xx"
	jinaRateLimited jinaMode = "429"
	jinaDown        jinaMode = "500"
	jinaUnreachable jinaMode = "unreachable"
)

var allJinaModes = []jinaMode{jinaOff, jinaThin, jinaChallenge, jinaRateLimited, jinaDown, jinaUnreachable}

// newNativeWithJina builds a Native whose Jina fallback behaves per mode,
// on a fake clock so Jina's retry backoff doesn't sleep. It returns the
// number of Jina requests made so far.
func newNativeWithJina(t *testing.T, mode jinaMode) (*Native, func() int32) {
	t.Helper()
	var hits atomic.Int32
	opts := NativeOptions{Timeout: 5 * time.Second}
	serve := func(answer func(http.ResponseWriter)) {
		jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			answer(w)
		}))
		t.Cleanup(jina.Close)
		opts.JinaFallback, opts.JinaBaseURL = true, jina.URL+"/"
	}
	switch mode {
	case jinaOff:
	case jinaUnreachable:
		opts.JinaFallback, opts.JinaBaseURL = true, "http://"+closedAddr(t)+"/"
	case jinaThin:
		serve(func(w http.ResponseWriter) { _, _ = w.Write([]byte("Title: x\n\nMarkdown Content:\ntoo short")) })
	case jinaChallenge:
		serve(func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, jinaReply("Just a moment...", []string{warnTarget403, warnCaptcha}, cfChallengeBody))
		})
	case jinaRateLimited:
		serve(func(w http.ResponseWriter) { w.WriteHeader(http.StatusTooManyRequests) })
	case jinaDown:
		serve(func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })
	default:
		t.Fatalf("unknown jinaMode %q", mode)
	}
	return unpaced(NewNative(opts), newFakeClock()), hits.Load
}

// hostCached reports whether n's host cache holds a fresh entry for host,
// by n's clock.
func hostCached(n *Native, host string) bool {
	_, ok := n.hostCache.Get(host, n.clock.now())
	return ok
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
// never fails the rest of its host, nor the host a redirect reached,
// whether Jina is off or on and whatever Jina answers.
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
		"thin page behind a cross-site redirect": func(w http.ResponseWriter, r *http.Request, other string) {
			http.Redirect(w, r, other+"/thin", http.StatusFound)
		},
	}
	for pageName, page := range pages {
		for _, mode := range allJinaModes {
			t.Run(pageName+"/jina "+string(mode), func(t *testing.T) {
				var aHits, bHits atomic.Int32
				var other string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/a":
						aHits.Add(1)
						page(w, r, other)
					case "/b":
						bHits.Add(1)
						_, _ = w.Write([]byte(article))
					case "/thin":
						_, _ = w.Write([]byte(thinPage))
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
				assert.NotErrorIs(t, err, errSiteLoginWall)
				assert.NotErrorIs(t, err, errOffsiteLoginWall)
				for _, host := range []string{hostOf(srv.URL), hostOf(other)} {
					assert.False(t, hostCached(n, host), "%s must not be cached", host)
				}

				res, err := n.Fetch(context.Background(), srv.URL+"/b")
				require.NoError(t, err, "a page-level verdict must not fail the rest of the host")
				assert.Equal(t, "readability", res.Meta["via"])
				assert.Equal(t, int32(1), aHits.Load())
				assert.Equal(t, int32(1), bHits.Load(), "/b must reach the origin")
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

// TestNative_JinaOwn403IsNoVerdict: a 403 from Jina itself that names no
// target is about curio's client, not the target, whose status comes in a
// warning: a bare 403, one whose reason names another host, Cloudflare's
// block page. Behind a thin page or an origin 403 the fetch stays retryable
// after one Jina request, errors.As finds Jina's status, nothing is cached,
// and Jina's health counts a failure. Only a 403 carrying Cloudflare's
// challenge header pauses Jina calls.
func TestNative_JinaOwn403IsNoVerdict(t *testing.T) {
	const (
		bare       = "jina: HTTP 403 Forbidden"
		challenged = "jina: r.jina.ai's CDN challenged the request: HTTP 403 Forbidden"
	)
	otherHost := hostRefusal("mobile.twitter.com")
	cases := []struct {
		name        string
		origin      int // 200 serves the thin page
		challenged  bool
		contentType string
		body        string
		jinaErr     string // what the error says of Jina's answer
		sentinel    error
		class       CallClass
		pause       time.Duration
	}{
		{name: "thin page", origin: http.StatusOK, jinaErr: bare, sentinel: ErrLoginWall, class: CallForbidden},
		{name: "thin page, challenged", origin: http.StatusOK, challenged: true, jinaErr: challenged,
			sentinel: ErrLoginWall, class: CallChallenged, pause: jinaChallengeCooldown},
		{name: "origin 403", origin: http.StatusForbidden, jinaErr: bare, sentinel: ErrAntiBot, class: CallForbidden},
		{name: "origin 403, challenged", origin: http.StatusForbidden, challenged: true, jinaErr: challenged,
			sentinel: ErrAntiBot, class: CallChallenged, pause: jinaChallengeCooldown},
		{name: "thin page, another host blocked", origin: http.StatusOK, contentType: "text/plain", body: otherHost,
			jinaErr: bare + ": " + strings.TrimSpace(otherHost), sentinel: ErrLoginWall, class: CallForbidden},
		{name: "origin 403, Cloudflare's block page", origin: http.StatusForbidden, contentType: "text/html",
			body: cfBlockHTML, jinaErr: bare, sentinel: ErrAntiBot, class: CallForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.origin != http.StatusOK {
					w.WriteHeader(tc.origin)
					return
				}
				_, _ = io.WriteString(w, thinPage)
			}))
			defer origin.Close()
			var jinaHits atomic.Int32
			jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				jinaHits.Add(1)
				if tc.challenged {
					w.Header().Set("Cf-Mitigated", "challenge")
				}
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer jina.Close()

			fc := newFakeClock()
			n := unpaced(NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/"}), fc)
			_, err := n.Fetch(context.Background(), origin.URL+"/a")
			require.ErrorIs(t, err, tc.sentinel)
			assert.NotErrorIs(t, err, errJinaRefused)
			var pe *PermanentError
			assert.False(t, errors.As(err, &pe), "Jina's own 403 leaves the fetch retryable: %v", err)
			var se *HTTPStatusError
			require.ErrorAs(t, err, &se)
			assert.Equal(t, http.StatusForbidden, se.StatusCode)
			assert.True(t, strings.HasPrefix(se.URL, jina.URL), "errors.As finds Jina's status first: %s", se.URL)
			assert.Equal(t, tc.challenged, errors.Is(err, errJinaChallenged))
			assert.True(t, strings.HasPrefix(err.Error(), tc.jinaErr+" (after "), err.Error())
			assert.Equal(t, int32(1), jinaHits.Load(), "no retry within the fetch")
			assert.NotContains(t, err.Error(), "(cached:")
			assert.False(t, hostCached(n, hostOf(origin.URL)))
			assert.Equal(t, tc.pause, n.jinaCooldown.remaining(fc.now()))
			assert.Equal(t, map[CallClass]int{tc.class: 1}, n.JinaHealth().Recent)
		})
	}
}

// TestJinaAnswered: a Jina failure is a verdict about the target when its
// answer was rejected or Jina refused the target: with a deterministic 4xx,
// or with a 403 whose reason names the target's host and is no abuse block.
// 401, 402 and any other 403 are about curio's client, and rate limits,
// Jina's block of a site, outages and a target's trouble for now are no
// verdict.
func TestJinaAnswered(t *testing.T) {
	const target = "https://www.investing.com/news/1"
	status := func(code int, header http.Header, reason string) error {
		return jinaStatusError(target, &HTTPStatusError{StatusCode: code}, header, reason)
	}
	challenge := http.Header{"Cf-Mitigated": {"challenge"}}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"rejected answer", fmt.Errorf("jina: %w: %w", errJinaRejected, ErrAntiBot), true},
		{"400", status(http.StatusBadRequest, nil, ""), true},
		{"404", status(http.StatusNotFound, nil, ""), true},
		{"451", status(http.StatusUnavailableForLegalReasons, nil, jina451Reason), true},
		{"401", status(http.StatusUnauthorized, nil, ""), false},
		{"402", status(http.StatusPaymentRequired, nil, ""), false},
		{"403", status(http.StatusForbidden, nil, ""), false},
		{"403 naming the target's host", status(http.StatusForbidden, nil, hostRefusal("www.investing.com")), true},
		{"403 naming another host", status(http.StatusForbidden, nil, hostRefusal("investing.com")), false},
		{"403 abuse block of the target's host", status(http.StatusForbidden, nil, abuseBlock("www.investing.com")), false},
		{"429 abuse block of the target's host", status(http.StatusTooManyRequests, nil, abuseBlock("www.investing.com")), false},
		{"403 challenge", status(http.StatusForbidden, challenge, ""), false},
		{"403 challenge naming the target's host", status(http.StatusForbidden, challenge, hostRefusal("www.investing.com")), false},
		{"429", status(http.StatusTooManyRequests, nil, ""), false},
		{"502", status(http.StatusBadGateway, nil, ""), false},
		{"target trouble", fmt.Errorf("jina: %w: %w", errJinaTargetTrouble, errServerErrorPage), false},
		{"transport", errors.New("jina: connection reset"), false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, jinaAnswered(tc.err), tc.name)
	}
}

// TestNative_AntiBotCachedWhenNoPathLeft: an origin 403 is cached when Jina
// is off or itself answers with too little content. The first failure is
// retryable. The next URL on the host skips the origin: with Jina off it
// waits for the entry to expire, and with Jina on it asks Jina once and
// fails for good on Jina's verdict about it.
func TestNative_AntiBotCachedWhenNoPathLeft(t *testing.T) {
	for _, mode := range []jinaMode{jinaOff, jinaThin} {
		t.Run(string(mode), func(t *testing.T) {
			var originHits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				originHits.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer srv.Close()

			n, jinaCalls := newNativeWithJina(t, mode)
			_, err := n.Fetch(context.Background(), srv.URL+"/a")
			require.ErrorIs(t, err, ErrAntiBot)
			var pe *PermanentError
			assert.False(t, errors.As(err, &pe), "first failure must stay retryable: %v", err)
			entry, ok := n.hostCache.Get(hostOf(srv.URL), n.clock.now())
			require.True(t, ok)
			calls := jinaCalls()

			_, err = n.Fetch(context.Background(), srv.URL+"/b")
			assert.ErrorIs(t, err, ErrAntiBot)
			assert.Contains(t, err.Error(), "(cached: native: HTTP 403 Forbidden: origin blocked the request (likely anti-bot))",
				"the entry quotes the origin's own answer")
			assert.Equal(t, int32(1), originHits.Load(), "the origin is not asked again")
			if mode == jinaOff {
				de := requireDeferred(t, err, entry.until)
				assert.Equal(t, hostOf(srv.URL)+" to be tried again: it blocked curio's last request", de.Reason)
				assert.Equal(t, calls, jinaCalls())
				return
			}
			require.ErrorAs(t, err, &pe, "Jina's verdict on the page is final")
			assert.ErrorIs(t, err, errJinaRejected)
			assert.ErrorIs(t, err, ErrLoginWall, "Jina's thin answer")
			assert.Equal(t, calls+1, jinaCalls(), "one Jina request of its own")
			assert.Equal(t, store.FailureCauseLoginWall, FailureCause(err))
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

			fc := newFakeClock()
			n := NewNative(NativeOptions{Timeout: 5 * time.Second, Backend: backend})
			n.clock = fc.clock()
			_, err := n.Fetch(context.Background(), src.URL+"/a")
			require.ErrorIs(t, err, ErrAntiBot)

			res, err := n.Fetch(context.Background(), src.URL+"/b")
			require.NoError(t, err, "the redirecting host must not be cached")
			assert.Equal(t, "readability", res.Meta["via"])

			_, err = n.Fetch(context.Background(), destURL+"/y")
			requireDeferred(t, err, fc.now().Add(15*time.Minute))
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

// TestNative_CachedRedirectTargetIsFinal: a redirect onto a host whose
// verdict is cached is held by that entry, as the host's own URLs are. The
// first failure caches the destination and stays retryable. Its retry
// fails from an unreachable entry at once; past an anti-bot one it asks
// Jina, whose verdict on the page is final, quoting the page's own origin
// answer. Neither writes the cache, and the redirecting host is never
// cached.
func TestNative_CachedRedirectTargetIsFinal(t *testing.T) {
	cases := []struct {
		name      string
		dest      func(t *testing.T) string
		mode      jinaMode
		sentinel  error
		jinaCalls int32 // the first fetch's
		retryJina int32 // the retry's
		fromCache bool  // whether the retry quotes the entry
	}{
		{"destination blocks, jina answers a challenge", func(t *testing.T) string {
			dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			}))
			t.Cleanup(dest.Close)
			return localhostURL(t, dest.URL)
		}, jinaChallenge, ErrAntiBot, 1, 1, false},
		{"destination down, jina off", func(t *testing.T) string {
			return "http://localhost:" + strings.Split(closedAddr(t), ":")[1]
		}, jinaOff, ErrHostUnreachable, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := newRedirectingServer(t, tc.dest(t)+"/x")
			n, jinaCalls := newNativeWithJina(t, tc.mode)

			_, err := n.Fetch(context.Background(), src.URL+"/a")
			require.ErrorIs(t, err, tc.sentinel)
			var pe *PermanentError
			assert.False(t, errors.As(err, &pe), "the first failure stays retryable: %v", err)
			entry, cached := n.hostCache.Get("localhost", n.clock.now())
			assert.True(t, cached, "the destination is cached")
			assert.False(t, hostCached(n, hostOf(src.URL)), "the redirecting host is not")
			assert.Equal(t, tc.jinaCalls, jinaCalls())

			_, err = n.Fetch(context.Background(), src.URL+"/a")
			require.ErrorAs(t, err, &pe, "the retry is final")
			assert.ErrorIs(t, err, tc.sentinel)
			assert.Equal(t, tc.fromCache, strings.Contains(err.Error(), "(cached:"), err.Error())
			assert.Equal(t, tc.jinaCalls+tc.retryJina, jinaCalls())
			again, cached := n.hostCache.Get("localhost", n.clock.now())
			assert.True(t, cached)
			assert.Equal(t, entry, again, "the retry writes no entry")
			assert.False(t, hostCached(n, hostOf(src.URL)), "the redirecting host is still not cached")

			res, err := n.Fetch(context.Background(), src.URL+"/b")
			require.NoError(t, err)
			assert.Equal(t, "readability", res.Meta["via"])
		})
	}
}

// TestNative_CachedRedirectTargetDefersWithoutJina: with Jina off, a
// redirect onto a host with a fresh anti-bot entry waits for the entry to
// expire, and writes nothing.
func TestNative_CachedRedirectTargetDefersWithoutJina(t *testing.T) {
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer dest.Close()
	src := newRedirectingServer(t, localhostURL(t, dest.URL)+"/x")
	n, _ := newNativeWithJina(t, jinaOff)
	n.hostCache.Put("localhost", HostFailAntiBot, "native: HTTP 403 Forbidden: "+ErrAntiBot.Error(), n.clock.now())
	entry, ok := n.hostCache.Get("localhost", n.clock.now())
	require.True(t, ok)

	_, err := n.Fetch(context.Background(), src.URL+"/a")
	de := requireDeferred(t, err, entry.until)
	assert.Equal(t, "localhost to be tried again: it blocked curio's last request", de.Reason)
	assert.Equal(t, store.FailureCauseAntiBot, FailureCause(err))
	assert.False(t, hostCached(n, hostOf(src.URL)))
}

// TestNative_CachedHostJinaOutcomes: past a fresh anti-bot entry, Jina's
// answer decides the page as a fallback's does: a verdict about the target
// is final, and Jina's own trouble, the target's trouble for now, or a
// hold is retried or deferred, never permanent. None asks the origin or
// writes the entry.
func TestNative_CachedHostJinaOutcomes(t *testing.T) {
	limited := answerStatus(http.StatusTooManyRequests)
	limited.header = http.Header{"Retry-After": {"120"}}
	cases := []struct {
		name      string
		jina      fakeAnswer
		permanent bool
		deferred  bool
		sentinel  error
	}{
		{"a CAPTCHA", *jinaPage("An article", []string{warnCaptcha}, longArticleBody), true, false, errJinaRejected},
		{"the target's 403", *jinaTarget(http.StatusForbidden), true, false, errJinaRejected},
		{"a refusal", fakeAnswer{status: http.StatusUnavailableForLegalReasons, contentType: "application/json",
			body: jina451Body}, true, false, errJinaRefused},
		{"the target's 404", *jinaTarget(http.StatusNotFound), true, false, ErrDeadLink},
		{"the target's 502", *jinaTarget(http.StatusBadGateway), false, false, errJinaTargetTrouble},
		{"Jina's 500", answerStatus(http.StatusInternalServerError), false, false, ErrAntiBot},
		{"Jina's long 429", limited, false, true, ErrAntiBot},
		{"Jina's block of the site", fakeAnswer{status: http.StatusForbidden, contentType: "text/plain",
			body: abuseBlock("news.example")}, false, true, errJinaSiteBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := cachedHostNative(t, HostFailAntiBot, cachedOrigin403, tc.jina)
			entry, ok := n.hostCache.Get(hostOf(causePage), n.clock.now())
			require.True(t, ok)

			_, err := n.Fetch(t.Context(), causePage)
			require.ErrorIs(t, err, tc.sentinel)
			assert.NotErrorIs(t, err, errOriginAsked)
			_, permanent := errors.AsType[*PermanentError](err)
			assert.Equal(t, tc.permanent, permanent, "permanent: %v", err)
			_, deferred := errors.AsType[*DeferError](err)
			assert.Equal(t, tc.deferred, deferred, "deferred: %v", err)
			assert.Contains(t, err.Error(), "(after native: origin blocked the request (likely anti-bot) (cached: "+
				cachedOrigin403+"))", "the origin's side is the entry's")
			again, ok := n.hostCache.Get(hostOf(causePage), n.clock.now())
			require.True(t, ok)
			assert.Equal(t, entry, again)
		})
	}
}

// TestNative_ChallengePageIsPageLevel: a bot challenge recognized in a 200
// page's content is about that page. With Jina off, or with Jina answering
// thin, the fetch fails for good, and the next healthy URL on the host
// still goes through.
func TestNative_ChallengePageIsPageLevel(t *testing.T) {
	for _, mode := range []jinaMode{jinaOff, jinaThin} {
		t.Run(string(mode), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/a" {
					_, _ = io.WriteString(w, challengePages["cloudflare"])
					return
				}
				_, _ = io.WriteString(w, makeArticleHTML("Healthy", ""))
			}))
			defer srv.Close()

			n, _ := newNativeWithJina(t, mode)
			_, err := n.Fetch(context.Background(), srv.URL+"/a")
			require.ErrorIs(t, err, ErrAntiBot)
			var pe *PermanentError
			assert.ErrorAs(t, err, &pe)
			assert.False(t, hostCached(n, hostOf(srv.URL)))

			res, err := n.Fetch(context.Background(), srv.URL+"/b")
			require.NoError(t, err)
			assert.Equal(t, "readability", res.Meta["via"])
		})
	}
}

// TestNative_LoginSlugRedirectIsAnArticle: a canonicalizing redirect onto a
// slug that starts with "login-" is no login wall, and caches nothing.
func TestNative_LoginSlugRedirectIsAnArticle(t *testing.T) {
	const title = "Login cognito using with scope openId using id_token or access_token don't working"
	mux := http.NewServeMux()
	mux.HandleFunc("/questions/63177503", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/questions/63177503/login-cognito-using-with-scope-openid", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/questions/63177503/login-cognito-using-with-scope-openid", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, makeArticleHTML(title, ""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second})
	res, err := n.Fetch(context.Background(), srv.URL+"/questions/63177503")
	require.NoError(t, err)
	assert.Equal(t, "readability", res.Meta["via"])
	assert.Equal(t, title, res.Title)
	assert.False(t, hostCached(n, hostOf(srv.URL)))
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
		{jinaChallenge, true, 1},
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
// the returned error matches the origin's sentinel, and errors.As finds
// Jina's status even when the origin answered with a status of its own.
func TestNative_CombinedErrorKeepsBothChains(t *testing.T) {
	thin := serveThinPage(t)
	defer thin.Close()
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer forbidden.Close()

	cases := []struct {
		name     string
		url      string
		sentinel error
	}{
		{"thin page", thin.URL, ErrLoginWall},
		{"anti-bot", forbidden.URL, ErrAntiBot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := newNativeWithJina(t, jinaRateLimited)
			_, err := n.Fetch(context.Background(), tc.url)
			assert.ErrorIs(t, err, tc.sentinel)
			var se *HTTPStatusError
			require.ErrorAs(t, err, &se)
			assert.Equal(t, http.StatusTooManyRequests, se.StatusCode)
		})
	}
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

			assert.Equal(t, tc.unreachable, hostCached(n, "dead.example"))
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

// TestHostFailureCache: an entry is fresh for the TTL from when it was put,
// by the caller's clock, quotes what it was put with, and is swept once it
// has been stale for a TTL.
func TestHostFailureCache(t *testing.T) {
	c := newHostFailureCache(15 * time.Minute)
	t0 := newFakeClock().now()
	c.Put("news.example", HostFailAntiBot, "native: HTTP 403 Forbidden: "+ErrAntiBot.Error(), t0)

	e, ok := c.Get("news.example", t0.Add(15*time.Minute-time.Second))
	require.True(t, ok)
	assert.Equal(t, t0.Add(15*time.Minute), e.until)
	assert.ErrorIs(t, e.err(), ErrAntiBot)
	assert.Equal(t, "native: origin blocked the request (likely anti-bot) "+
		"(cached: native: HTTP 403 Forbidden: origin blocked the request (likely anti-bot))", e.err().Error())
	_, ok = c.Get("news.example", t0.Add(15*time.Minute))
	assert.False(t, ok, "expired")
	_, ok = c.Get("", t0)
	assert.False(t, ok)

	c.Put("blog.example", HostFailUnreachable, "native: fetch: host unreachable", t0.Add(30*time.Minute-time.Second))
	assert.Len(t, c.entries, 2, "a stale entry stays until it is a TTL past")
	c.Put("blog.example", HostFailUnreachable, "native: fetch: host unreachable", t0.Add(30*time.Minute))
	assert.Len(t, c.entries, 1, "swept")
}

// TestHostFailureKind_WaitReason: what a page waits for behind each kind
// of entry, worded to follow "waiting for".
func TestHostFailureKind_WaitReason(t *testing.T) {
	assert.Equal(t, "www.nytimes.com to be tried again: it blocked curio's last request",
		HostFailAntiBot.waitReason("www.nytimes.com"))
	assert.Equal(t, "example.com to be tried again: it sent curio's last request to its login page",
		HostFailLoginWall.waitReason("example.com"))
	assert.Equal(t, "dead.example to be tried again: it couldn't be reached",
		HostFailUnreachable.waitReason("dead.example"))
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

// viewAt is the page verdicts' view of article with the request settled on
// final, as tryReadability builds it.
func viewAt(t *testing.T, article readability.Article, final *url.URL) pageView {
	t.Helper()
	p, err := articleView(article, final)
	require.NoError(t, err)
	return p
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
		reason, _ := looksLikeLoginWall(viewAt(t, article, final), tc.source)
		assert.Empty(t, reason, "%s → %s", tc.source, tc.final)
	}
}

// TestLooksLikeLoginWall_Redirects: a redirect onto the requested site's
// login page is site-wide; one onto another site's login page, by its path
// or its title, is offsite; a redirect to another site's article is no
// login wall at all.
func TestLooksLikeLoginWall_Redirects(t *testing.T) {
	cases := []struct {
		source, final, title string
		scope                loginWallScope
		wall                 bool
	}{
		{"https://example.com/post", "https://example.com/login", "Full article", loginWallSite, true},
		{"https://example.com/post", "https://www.example.com/authwall", "Full article", loginWallSite, true},
		{"https://example.com/login", "https://example.com/login", "Full article", loginWallPage, true}, // bookmarked login page, no redirect
		{"https://example.com/post", "https://accounts.example.net/login?continue=x", "Full article", loginWallOffsite, true},
		{"https://docs.example.com/d/1", "https://accounts.example.net/ServiceLogin", "Sign in - Example Accounts", loginWallOffsite, true},
		{"https://example.com/post", "https://other.example/post", "Full article", 0, false},
	}
	for _, tc := range cases {
		article := parseArticle(t, makeArticleHTML(tc.title, ""), tc.final)
		final, err := url.Parse(tc.final)
		require.NoError(t, err)
		reason, scope := looksLikeLoginWall(viewAt(t, article, final), tc.source)
		if !tc.wall {
			assert.Empty(t, reason, "%s → %s", tc.source, tc.final)
			continue
		}
		assert.NotEmpty(t, reason, "%s → %s", tc.source, tc.final)
		assert.Equal(t, tc.scope, scope, "%s → %s", tc.source, tc.final)
	}
}

// TestLooksLikeSoft404_WWWHomepage: a deleted article that settles on the
// www homepage is a dead link, not a cross-site redirect.
func TestLooksLikeSoft404_WWWHomepage(t *testing.T) {
	final, err := url.Parse("https://www.example.com/")
	require.NoError(t, err)
	assert.Equal(t, "redirected to homepage",
		looksLikeSoft404(pageView{finalURL: final}, "https://example.com/deleted-post"))
}

// TestLooksLikeSoft404_HomepageNeedsAPage: the homepage rule needs a source
// that names a page. A site's index document redirecting to the root is the
// homepage canonicalized, as bookmarks of ocw.mit.edu/index.htm are; an
// index document below the root still names its directory.
func TestLooksLikeSoft404_HomepageNeedsAPage(t *testing.T) {
	cases := []struct{ source, final, reason string }{
		{"http://ocw.mit.edu/index.htm", "https://ocw.mit.edu/", ""},
		{"http://www.infragistics.com/default.aspx", "https://www.infragistics.com/", ""},
		{"https://example.com/index.php", "https://www.example.com/", ""},
		{"https://example.com", "https://www.example.com/", ""},
		{"https://example.com/deleted-post", "https://www.example.com/", "redirected to homepage"},
		{"https://example.com/blog/index.html", "https://example.com/", "redirected to homepage"},
		// An index document is the homepage only as a source.
		{"https://example.com/deleted-post", "https://example.com/index.html", ""},
	}
	for _, tc := range cases {
		t.Run(tc.source+" → "+tc.final, func(t *testing.T) {
			final, err := url.Parse(tc.final)
			require.NoError(t, err)
			assert.Equal(t, tc.reason, looksLikeSoft404(pageView{finalURL: final}, tc.source))
		})
	}
}
