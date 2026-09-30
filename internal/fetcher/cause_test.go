package fetcher

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// These tests drive the fetchers to each way a fetch fails, and check the
// cause FailureCause reads from the error they return.

// causeCase is a fetch that fails, and the cause its document records.
type causeCase struct {
	name string
	fail func(t *testing.T) error // runs the fetch, returning its error
	want store.FailureCause
}

const (
	// causePage is the page the Native cases fetch from fakes. Jina's
	// refusals name its host.
	causePage = "https://news.example/2026/story"
	// causeJina is the base URL of the fake Jina the Native cases route to.
	causeJina = "https://jina.test/"
)

// fakeNative is a Native on a fake clock, with bodies capped at
// testBodyLimit, whose origin answers origin and whose Jina answers jina;
// a nil jina turns the fallback off.
func fakeNative(t *testing.T, origin fakeAnswer, jina *fakeAnswer, detection bool) *Native {
	t.Helper()
	opts := NativeOptions{Timeout: 5 * time.Second, DeadLinkDetection: detection}
	if jina != nil {
		opts.JinaFallback, opts.JinaBaseURL = true, causeJina
	}
	n := unpaced(NewNative(opts), newFakeClock())
	n.rt = limitBodies(fakeRT(func(target string) (*fetchResponse, error) {
		if strings.HasPrefix(target, causeJina) {
			return jina.respond(target)
		}
		return origin.respond(target)
	}), testBodyLimit)
	return n
}

// viaFakes fetches causePage from fakeNative, with dead-link detection on.
func viaFakes(origin fakeAnswer, jina *fakeAnswer) func(*testing.T) error {
	return viaFakesDetecting(origin, jina, true)
}

// viaFakesDetecting is viaFakes with dead-link detection as given.
func viaFakesDetecting(origin fakeAnswer, jina *fakeAnswer, detection bool) func(*testing.T) error {
	return func(t *testing.T) error {
		_, err := fakeNative(t, origin, jina, detection).Fetch(t.Context(), causePage)
		return err
	}
}

// thenCached fetches causePage as viaFakes does, then another page on its
// host, and returns that fetch's error: a hit on the host cache.
func thenCached(origin fakeAnswer, jina *fakeAnswer) func(*testing.T) error {
	return func(t *testing.T) error {
		n := fakeNative(t, origin, jina, true)
		_, err := n.Fetch(t.Context(), causePage)
		require.Error(t, err)
		_, err = n.Fetch(t.Context(), "https://news.example/2026/another-story")
		require.ErrorContains(t, err, "(cached: ")
		return err
	}
}

// errOriginAsked is what the origin answers a fetch that should have
// skipped it.
var errOriginAsked = errors.New("the origin was asked past its cache entry")

// cachedHostNative is a fakeNative whose Jina answers jina, with a fresh
// cache entry of kind, quoting originErr, for causePage's host. Its origin
// fails with errOriginAsked.
func cachedHostNative(t *testing.T, kind HostFailureKind, originErr string, jina fakeAnswer) *Native {
	t.Helper()
	n := fakeNative(t, failing(errOriginAsked), &jina, true)
	n.hostCache.Put(hostOf(causePage), kind, originErr, n.clock.now())
	return n
}

// pastCachedHost fetches causePage past a fresh cache entry for its host
// (cachedHostNative), without asking the origin.
func pastCachedHost(kind HostFailureKind, originErr string, jina fakeAnswer) func(*testing.T) error {
	return func(t *testing.T) error {
		_, err := cachedHostNative(t, kind, originErr, jina).Fetch(t.Context(), causePage)
		assert.NotErrorIs(t, err, errOriginAsked)
		return err
	}
}

// jinaArticlePage is Jina's answer rendering the article.
var jinaArticlePage = jinaPage("An article", nil, longArticleBody)

// afterItsTurns paces n's Jina calls at the default rate a site, and hands
// out every turn of causePage's site that starts within the inline cap, so
// its next Jina call is deferred for a turn. It returns n.
func afterItsTurns(n *Native) *Native {
	interval := time.Minute / DefaultJinaSiteRequestsPerMinute
	n.jinaSites = newSitePacer(interval)
	for range int(maxInlineJinaWait/interval) + 1 {
		n.jinaSites.take(siteOf(hostOf(causePage)), n.clock.now(), maxInlineJinaWait)
	}
	return n
}

// Cached origin answers, as settle stores them.
const (
	cachedOrigin403   = "native: HTTP 403 Forbidden: origin blocked the request (likely anti-bot)"
	cachedOriginLogin = "native: site-wide login wall or thin content (redirected to a login/auth path: /login)"
)

// fetchFrom fetches rawURL with a Native on backend, its Jina off and
// dead-link detection on, for the cases a real server answers.
func fetchFrom(t *testing.T, backend, rawURL string) error {
	t.Helper()
	n := NewNative(NativeOptions{Timeout: 5 * time.Second, Backend: backend, DeadLinkDetection: true})
	_, err := n.Fetch(t.Context(), rawURL)
	return err
}

// answerStatus is an answer with status code and no body.
func answerStatus(code int) fakeAnswer { return fakeAnswer{status: code} }

// htmlPage is a 200 answer serving html.
func htmlPage(html string) fakeAnswer {
	return fakeAnswer{status: http.StatusOK, contentType: "text/html", body: html}
}

// thinOrigin serves a page too thin to be the article, which sends the
// fetch to Jina.
var thinOrigin = htmlPage(thinPage)

// jinaPage is Jina's 200 answer rendering a page with title, warnings and
// body.
func jinaPage(title string, warnings []string, body string) *fakeAnswer {
	return &fakeAnswer{status: http.StatusOK, contentType: "text/plain", body: jinaReply(title, warnings, body)}
}

// jinaTarget is Jina's answer reporting that the target answered it code.
func jinaTarget(code int) *fakeAnswer {
	warning := fmt.Sprintf("Target URL returned error %d: %s", code, http.StatusText(code))
	return jinaPage("An article", []string{warning}, longArticleBody)
}

// transportError is a request for target that failed in operation op
// with err, as net/http reports it.
func transportError(target, op string, err error) error {
	return &url.Error{Op: "Get", URL: target, Err: &net.OpError{Op: op, Net: "tcp", Err: err}}
}

// failing is a request that fails with err.
func failing(err error) fakeAnswer { return fakeAnswer{err: err} }

// jinaTrouble are Jina's own failures: none is a verdict about the target.
var jinaTrouble = []struct {
	name   string
	answer fakeAnswer
}{
	{"Jina's 429", answerStatus(http.StatusTooManyRequests)},
	{"Jina's 500", answerStatus(http.StatusInternalServerError)},
	{"Jina's CDN challenge", fakeAnswer{status: http.StatusForbidden,
		header: http.Header{"Cf-Mitigated": {"challenge"}}, contentType: "text/html",
		body: "<!DOCTYPE html><title>Just a moment...</title>"}},
	{"Jina's 401", fakeAnswer{status: http.StatusUnauthorized, contentType: "application/json",
		body: `{"code":401,"name":"AuthenticationRequiredError","message":"Authentication is required to use this endpoint."}`}},
	{"a bare 403 from Jina", answerStatus(http.StatusForbidden)},
	{"Jina unreachable", failing(transportError(causeJina+causePage, "dial",
		os.NewSyscallError("connect", syscall.ECONNREFUSED)))},
}

func nativeCauseCases() []causeCase {
	abuse := fakeAnswer{status: http.StatusForbidden, contentType: "text/plain", body: abuseBlock("news.example")}
	abuse429 := fakeAnswer{status: http.StatusTooManyRequests, contentType: "text/plain", body: abuseBlock("news.example")}
	opted := fakeAnswer{status: http.StatusUnavailableForLegalReasons, contentType: "application/json", body: jina451Body}
	nxdomain := failing(transportError(causePage, "dial",
		&net.DNSError{Err: "no such host", Name: "news.example", IsNotFound: true}))
	refused := failing(transportError(causePage, "dial", os.NewSyscallError("connect", syscall.ECONNREFUSED)))
	return slices.Concat([]causeCase{
		{"origin 404", viaFakes(answerStatus(http.StatusNotFound), nil), store.FailureCauseDeadLink},
		{"origin 410", viaFakes(answerStatus(http.StatusGone), nil), store.FailureCauseDeadLink},
		{"a not-found title", viaFakes(htmlPage(makeArticleHTML("Page not found", "")), nil), store.FailureCauseDeadLink},
		{"a redirect onto the homepage", func(t *testing.T) error {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/" {
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
				_, _ = io.WriteString(w, makeArticleHTML("Front page", ""))
			}))
			t.Cleanup(srv.Close)
			return fetchFrom(t, "", srv.URL+"/2026/story")
		}, store.FailureCauseDeadLink},
		{"a 403 on another site's landing page", func(t *testing.T) error {
			return fetchFrom(t, "", newBlockedCrossSiteRedirect(t, "/", http.StatusForbidden)+"/blog/useful-hacks")
		}, store.FailureCauseDeadLink},

		{"origin 403, Jina off", viaFakes(answerStatus(http.StatusForbidden), nil), store.FailureCauseAntiBot},
		{"origin 403, then its host cached", thenCached(answerStatus(http.StatusForbidden), nil), store.FailureCauseAntiBot},
		{"origin 403, Jina's block of the site", viaFakes(answerStatus(http.StatusForbidden), new(abuse)),
			store.FailureCauseRateLimited},
		{"origin 403, Jina's block of the site with a 429", viaFakes(answerStatus(http.StatusForbidden), new(abuse429)),
			store.FailureCauseRateLimited},
		{"origin 403, Jina's block of the site, then another page", func(t *testing.T) error {
			n := fakeNative(t, answerStatus(http.StatusForbidden), &abuse, true)
			_, err := n.Fetch(t.Context(), causePage)
			require.ErrorIs(t, err, errJinaSiteBlocked)
			_, err = n.Fetch(t.Context(), "https://news.example/2026/another-story")
			require.ErrorContains(t, err, "jina: not sent, Jina Reader blocks news.example until ")
			return err
		}, store.FailureCauseRateLimited},

		{"cached host, Jina's CAPTCHA warning", pastCachedHost(HostFailAntiBot, cachedOrigin403,
			*jinaPage("An article", []string{warnCaptcha}, longArticleBody)), store.FailureCauseAntiBot},
		{"cached host, target 403 through Jina", pastCachedHost(HostFailAntiBot, cachedOrigin403,
			*jinaTarget(http.StatusForbidden)), store.FailureCauseAntiBot},
		{"cached host, target 503 through Jina", pastCachedHost(HostFailAntiBot, cachedOrigin403,
			*jinaTarget(http.StatusServiceUnavailable)), store.FailureCauseAntiBot},
		{"cached host, target 401 through Jina", pastCachedHost(HostFailAntiBot, cachedOrigin403,
			*jinaTarget(http.StatusUnauthorized)), store.FailureCauseHTTPError},
		{"cached host, Jina's 451", pastCachedHost(HostFailAntiBot, cachedOrigin403, opted),
			store.FailureCauseJinaRefused},
		{"cached host, a thin Jina answer", pastCachedHost(HostFailAntiBot, cachedOrigin403,
			*jinaPage("Short", nil, "too short")), store.FailureCauseLoginWall},
		{"cached host, a login page through Jina", pastCachedHost(HostFailAntiBot, cachedOrigin403,
			*jinaPage("Sign in - Google Accounts", nil, loginPageBody)), store.FailureCauseLoginWall},
		{"cached host, target 404 through Jina", pastCachedHost(HostFailAntiBot, cachedOrigin403,
			*jinaTarget(http.StatusNotFound)), store.FailureCauseDeadLink},
		{"cached host, Jina's 500", pastCachedHost(HostFailAntiBot, cachedOrigin403,
			answerStatus(http.StatusInternalServerError)), store.FailureCauseAntiBot},
		{"cached login wall, Jina's 500", pastCachedHost(HostFailLoginWall, cachedOriginLogin,
			answerStatus(http.StatusInternalServerError)), store.FailureCauseLoginWall},
		{"cached host, Jina's block of the site", pastCachedHost(HostFailAntiBot, cachedOrigin403, abuse),
			store.FailureCauseRateLimited},
		{"cached host, a site's turn at Jina", func(t *testing.T) error {
			n := cachedHostNative(t, HostFailAntiBot, cachedOrigin403, *jinaArticlePage)
			_, err := afterItsTurns(n).Fetch(t.Context(), causePage)
			return err
		}, store.FailureCauseAntiBot},

		{"thin page, Jina's block of the site", viaFakes(thinOrigin, new(abuse)), store.FailureCauseRateLimited},
		{"thin page, Jina's 451", viaFakes(thinOrigin, new(opted)), store.FailureCauseJinaRefused},
		{"thin page, a site's turn at Jina", func(t *testing.T) error {
			_, err := afterItsTurns(fakeNative(t, thinOrigin, jinaArticlePage, true)).Fetch(t.Context(), causePage)
			return err
		}, store.FailureCauseLoginWall},
		{"origin 403, a site's turn at Jina", func(t *testing.T) error {
			n := fakeNative(t, answerStatus(http.StatusForbidden), jinaArticlePage, true)
			_, err := afterItsTurns(n).Fetch(t.Context(), causePage)
			return err
		}, store.FailureCauseAntiBot},
		{"thin page, Jina's 422 about a timeout", viaFakes(thinOrigin, new(fakeAnswer{
			status: http.StatusUnprocessableEntity, contentType: "application/json",
			body: `{"code":422,"name":"TimeoutError","message":"page.goto: Timeout 30000ms exceeded"}`})),
			store.FailureCauseJinaRefused},

		{"thin page, a thin Jina answer", viaFakes(thinOrigin, jinaPage("Short", nil, "too short")),
			store.FailureCauseLoginWall},
		{"thin page, Jina's CAPTCHA warning", viaFakes(thinOrigin,
			jinaPage("An article", []string{warnCaptcha}, longArticleBody)), store.FailureCauseAntiBot},
		{"thin page, target 403 through Jina", viaFakes(thinOrigin, jinaTarget(http.StatusForbidden)),
			store.FailureCauseAntiBot},
		{"thin page, target 503 through Jina", viaFakes(thinOrigin, jinaTarget(http.StatusServiceUnavailable)),
			store.FailureCauseAntiBot},
		{"thin page, target 401 through Jina", viaFakes(thinOrigin, jinaTarget(http.StatusUnauthorized)),
			store.FailureCauseHTTPError},
		{"thin page, target 400 through Jina", viaFakes(thinOrigin, jinaTarget(http.StatusBadRequest)),
			store.FailureCauseHTTPError},
		{"thin page, target 500 through Jina", viaFakes(thinOrigin, jinaTarget(http.StatusInternalServerError)),
			store.FailureCauseHTTPError},
		{"thin page, a server error page through Jina", viaFakes(thinOrigin,
			jinaPage("502 Bad Gateway", nil, "nginx")), store.FailureCauseHTTPError},
		{"thin page, target 429 through Jina", viaFakes(thinOrigin, jinaTarget(http.StatusTooManyRequests)),
			store.FailureCauseRateLimited},
		{"thin page, target 404 through Jina", viaFakes(thinOrigin, jinaTarget(http.StatusNotFound)),
			store.FailureCauseDeadLink},
		{"thin page, target 404 through Jina, detection off", viaFakesDetecting(thinOrigin,
			jinaTarget(http.StatusNotFound), false), store.FailureCauseHTTPError},

		{"a redirect onto another site's login page", func(t *testing.T) error {
			src := newCrossSiteRedirect(t, "/login?next=/docs", makeArticleHTML("Your account", ""))
			return fetchFrom(t, "", src+"/docs/quarterly-report")
		}, store.FailureCauseLoginWall},
		{"a redirect onto the site's login page", func(t *testing.T) error {
			return fetchFrom(t, "", siteLoginWall(t)+"/docs/quarterly-report")
		}, store.FailureCauseLoginWall},
		{"a redirect onto the site's login page, then its host cached", func(t *testing.T) error {
			base := siteLoginWall(t)
			n := NewNative(NativeOptions{Timeout: 5 * time.Second, DeadLinkDetection: true})
			_, err := n.Fetch(t.Context(), base+"/docs/quarterly-report")
			require.Error(t, err)
			_, err = n.Fetch(t.Context(), base+"/docs/annual-report")
			require.ErrorContains(t, err, "(cached: ")
			return err
		}, store.FailureCauseLoginWall},

		{"an untrusted certificate", func(t *testing.T) error {
			origin := httptest.NewUnstartedServer(http.NotFoundHandler())
			origin.Config.ErrorLog = log.New(io.Discard, "", 0)
			origin.StartTLS()
			t.Cleanup(origin.Close)
			return fetchFrom(t, "stock", origin.URL+"/a")
		}, store.FailureCauseTLS},

		{"no such host", viaFakes(nxdomain, nil), store.FailureCauseUnreachable},
		{"no such host, then its host cached", thenCached(nxdomain, nil), store.FailureCauseUnreachable},
		{"connection refused", viaFakes(refused, nil), store.FailureCauseUnreachable},
		{"connection refused, then its host cached", thenCached(refused, nil), store.FailureCauseUnreachable},

		{"our network unreachable", viaFakes(failing(transportError(causePage, "dial",
			os.NewSyscallError("connect", syscall.ENETUNREACH))), nil), store.FailureCauseNetwork},
		{"a connection reset", viaFakes(failing(transportError(causePage, "read",
			os.NewSyscallError("read", syscall.ECONNRESET))), nil), store.FailureCauseNetwork},
		{"a TLS alert", viaFakes(failing(&url.Error{Op: "Get", URL: causePage,
			Err: &net.OpError{Op: "remote error", Err: tls.AlertError(40)}}), nil), store.FailureCauseNetwork},
		{"a redirect loop", func(t *testing.T) error {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, r.URL.Path, http.StatusFound)
			}))
			t.Cleanup(srv.Close)
			return fetchFrom(t, "stock", srv.URL+"/2026/story")
		}, store.FailureCauseNetwork},

		{"a client timeout", viaFakes(failing(&url.Error{Op: "Get", URL: causePage, Err: context.DeadlineExceeded}), nil),
			store.FailureCauseTimeout},
		{"a DNS timeout", viaFakes(failing(transportError(causePage, "dial",
			&net.DNSError{Err: "i/o timeout", Name: "news.example", IsTimeout: true})), nil), store.FailureCauseTimeout},

		{"origin 429", viaFakes(answerStatus(http.StatusTooManyRequests), nil), store.FailureCauseRateLimited},

		{"origin 401", viaFakes(answerStatus(http.StatusUnauthorized), nil), store.FailureCauseHTTPError},
		{"origin 409", viaFakes(answerStatus(http.StatusConflict), nil), store.FailureCauseHTTPError},
		{"origin 999", viaFakes(answerStatus(999), nil), store.FailureCauseHTTPError},
		{"origin 500", viaFakes(answerStatus(http.StatusInternalServerError), nil), store.FailureCauseHTTPError},
		{"origin 502", viaFakes(answerStatus(http.StatusBadGateway), nil), store.FailureCauseHTTPError},
		{"a 200 error page naming 502", viaFakes(htmlPage(makeArticleHTML("502 Bad Gateway", "")), nil),
			store.FailureCauseHTTPError},

		{"an origin body over the cap", viaFakes(htmlPage(strings.Repeat("x", 2*testBodyLimit)), nil),
			store.FailureCauseTooLarge},
		{"a Jina answer over the cap", viaFakes(thinOrigin,
			jinaPage("Huge", nil, strings.Repeat("x", 2*testBodyLimit))), store.FailureCauseTooLarge},
		{"a PDF over the cap, Jina off", viaFakes(fakeAnswer{status: http.StatusOK, contentType: "application/pdf",
			body: strings.Repeat("x", 2*testBodyLimit)}, nil), store.FailureCauseTooLarge},

		{"an image", viaFakes(fakeAnswer{status: http.StatusOK, contentType: "image/png", body: "\x89PNG"}, nil),
			store.FailureCauseUnsupported},
		{"an unreadable PDF, Jina off", viaFakes(fakeAnswer{status: http.StatusOK, contentType: "application/pdf",
			body: "not a PDF at all"}, nil), store.FailureCauseUnsupported},
		{"no URL", func(t *testing.T) error {
			_, err := NewNative(NativeOptions{}).Fetch(t.Context(), "")
			return err
		}, store.FailureCauseOther},
	}, jinaTroubleCases())
}

// jinaTroubleCases are the fetches Jina's own trouble fails: the origin's
// failure decides their cause.
func jinaTroubleCases() []causeCase {
	cases := make([]causeCase, 0, 2*len(jinaTrouble))
	for _, trouble := range jinaTrouble {
		cases = append(cases,
			causeCase{"origin 403, " + trouble.name,
				viaFakes(answerStatus(http.StatusForbidden), new(trouble.answer)), store.FailureCauseAntiBot},
			causeCase{"thin page, " + trouble.name,
				viaFakes(thinOrigin, new(trouble.answer)), store.FailureCauseLoginWall})
	}
	return cases
}

// siteLoginWall serves a site that sends every page to its own login page.
// It returns the base URL.
func siteLoginWall(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, makeArticleHTML("Your account", ""))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// gitHubAnswering is a GitHub fetcher whose API answers every request with
// answer.
func gitHubAnswering(t *testing.T, answer http.HandlerFunc) *GitHub {
	t.Helper()
	srv := httptest.NewServer(answer)
	t.Cleanup(srv.Close)
	return newTestGitHub(t, srv)
}

// roundTripFunc is an http.RoundTripper answering from a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const (
	gitHubRepo = "https://github.com/owner/repo"
	videoURL   = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
)

// fetchVideo fetches videoURL with a fake yt-dlp in mode, under timeout.
func fetchVideo(mode string, timeout time.Duration) func(*testing.T) error {
	return func(t *testing.T) error {
		yt := NewYouTube(YouTubeOptions{Bin: fakeTool(t, mode), Timeout: timeout})
		yt.clock = newFakeClock().clock()
		_, err := yt.Fetch(t.Context(), videoURL)
		return err
	}
}

func otherCauseCases() []causeCase {
	// errPermanent stands in for jobs.ErrPermanent, which the fetch handler
	// and the worker wrap their errors in.
	errPermanent := errors.New("permanent failure")
	fetchGitHub := func(rawURL string, answer http.HandlerFunc) func(*testing.T) error {
		return func(t *testing.T) error {
			_, err := gitHubAnswering(t, answer).Fetch(t.Context(), rawURL)
			return err
		}
	}
	answer := func(code int, header http.Header) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			maps.Copy(w.Header(), header)
			w.WriteHeader(code)
		}
	}
	return []causeCase{
		{"github: a profile", fetchGitHub("https://github.com/someone", nil), store.FailureCauseUnsupported},
		{"github: a repository's actions", fetchGitHub(gitHubRepo+"/actions", nil), store.FailureCauseUnsupported},
		{"github: 404", fetchGitHub(gitHubRepo, answer(http.StatusNotFound, nil)), store.FailureCauseHTTPError},
		{"github: 500", fetchGitHub(gitHubRepo, answer(http.StatusInternalServerError, nil)), store.FailureCauseHTTPError},
		{"github: a rate-limit 403", fetchGitHub(gitHubRepo, answer(http.StatusForbidden, http.Header{
			"X-Ratelimit-Remaining": {"0"}, "Retry-After": {"3600"}})), store.FailureCauseRateLimited},
		{"github: 429", fetchGitHub(gitHubRepo, answer(http.StatusTooManyRequests, http.Header{
			"Retry-After": {"3600"}})), store.FailureCauseRateLimited},
		{"github: the cooldown", func(t *testing.T) error {
			g := gitHubAnswering(t, nil)
			g.cooldown.extend(g.clock.now(), time.Hour)
			_, err := g.Fetch(t.Context(), gitHubRepo)
			return err
		}, store.FailureCauseRateLimited},
		{"github: a body over the cap", func(t *testing.T) error {
			g := gitHubAnswering(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, strings.Repeat("x", 2048))
			})
			g.maxBody = 1024
			_, err := g.Fetch(t.Context(), gitHubRepo)
			return err
		}, store.FailureCauseTooLarge},
		{"github: a timeout", func(t *testing.T) error {
			g := gitHubAnswering(t, nil)
			g.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
			})}
			_, err := g.Fetch(t.Context(), gitHubRepo)
			return err
		}, store.FailureCauseTimeout},

		{"youtube: a channel page", func(t *testing.T) error {
			_, err := NewYouTube(YouTubeOptions{}).Fetch(t.Context(), "https://www.youtube.com/@somechannel")
			return err
		}, store.FailureCauseUnsupported},
		{"youtube: the cooldown", func(t *testing.T) error {
			fc := newFakeClock()
			yt := NewYouTube(YouTubeOptions{Bin: "yt-dlp-never-runs"})
			yt.clock = fc.clock()
			yt.cooldown.extend(fc.now(), youtubeRateLimitCooldown)
			_, err := yt.Fetch(t.Context(), videoURL)
			return err
		}, store.FailureCauseRateLimited},
		{"youtube: HTTP Error 429", fetchVideo("yt-dlp-429", 30*time.Second), store.FailureCauseRateLimited},
		{"youtube: a timeout", fetchVideo("yt-dlp-hang", 100*time.Millisecond), store.FailureCauseTimeout},
		{"youtube: a private video", fetchVideo("yt-dlp-private", 30*time.Second), store.FailureCauseOther},
		{"youtube: an unavailable video", fetchVideo("yt-dlp-gone", 30*time.Second), store.FailureCauseOther},

		{"no fetcher for the URL", func(*testing.T) error {
			_, err := (&Single{}).For(causePage)
			return fmt.Errorf("%w: no fetcher for %s: %w", errPermanent, causePage, err)
		}, store.FailureCauseUnsupported},
		{"a plain error", func(*testing.T) error { return errors.New("write markdown: disk full") },
			store.FailureCauseOther},
		{"a handler panic", func(*testing.T) error {
			return fmt.Errorf("panic: %v (%w)", "runtime error: index out of range [3] with length 3", errPermanent)
		}, store.FailureCauseOther},
		{"an orphan out of attempts", func(*testing.T) error {
			return fmt.Errorf("%w: the daemon exited mid-job and no attempts are left", errPermanent)
		}, store.FailureCauseOther},
	}
}

// heldCauseCases are the cases whose call curio held back itself (a shared
// cooldown, a site's turn, a host-cache entry waited out) or Jina Reader's
// block of the site held back, the call that met the block included: they
// alone are deferred (DeferError). Every other case, an upstream's 429 or a
// yt-dlp run that met one included, ended in an answer that decided it.
var heldCauseCases = map[string]bool{
	"github: the cooldown":                                        true,
	"youtube: the cooldown":                                       true,
	"origin 403, then its host cached":                            true,
	"a redirect onto the site's login page, then its host cached": true,
	"origin 403, Jina's block of the site":                        true,
	"origin 403, Jina's block of the site with a 429":             true,
	"origin 403, Jina's block of the site, then another page":     true,
	"cached host, Jina's block of the site":                       true,
	"cached host, a site's turn at Jina":                          true,
	"thin page, Jina's block of the site":                         true,
	"thin page, a site's turn at Jina":                            true,
	"origin 403, a site's turn at Jina":                           true,
}

// TestFailureCause: each failed fetch gets its cause, which is one of the
// store's causes, and a dead link exactly when the error is one, the rule
// that makes a document dead. The fetch handler's wrapping changes
// nothing, and only a held call is deferred (heldCauseCases): its cause is
// the one its job records once a day of waiting runs out.
func TestFailureCause(t *testing.T) {
	assert.Empty(t, FailureCause(nil))
	for _, tc := range slices.Concat(nativeCauseCases(), otherCauseCases()) {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fail(t)
			require.Error(t, err)
			got := FailureCause(err)
			assert.Equal(t, tc.want, got, "%v", err)
			assert.Contains(t, store.FailureCauses(), got)
			assert.Equal(t, errors.Is(err, ErrDeadLink), got == store.FailureCauseDeadLink)
			assert.Equal(t, got, FailureCause(fmt.Errorf("fetch failed: %w", err)))
			_, deferred := errors.AsType[*DeferError](err)
			assert.Equal(t, heldCauseCases[tc.name], deferred, "deferred: %v", err)
		})
	}
}

// TestJinaFallbackError: the error a fetch returns after Jina failed too
// reads as it always has, and still unwraps to Jina's failure first.
func TestJinaFallbackError(t *testing.T) {
	jina := fmt.Errorf("jina: %w: %w: %s", errJinaRefused,
		&HTTPStatusError{StatusCode: http.StatusUnavailableForLegalReasons}, jina451Reason)
	origin := fmt.Errorf("native: %w (extracted text < 500 bytes)", ErrLoginWall)
	err := &jinaFallbackError{jina: jina, origin: origin}

	assert.Equal(t, fmt.Errorf("%w (after %w)", jina, origin).Error(), err.Error())
	assert.True(t, strings.HasPrefix(err.Error(),
		"jina: refused the target: HTTP 451 Unavailable For Legal Reasons: This domain is excluded from Jina Reader"))
	assert.True(t, strings.HasSuffix(err.Error(),
		" (after native: login wall or thin content (extracted text < 500 bytes))"))
	assert.Equal(t, []error{jina, origin}, err.Unwrap())
	assert.ErrorIs(t, err, errJinaRefused)
	assert.ErrorIs(t, err, ErrLoginWall)
}
