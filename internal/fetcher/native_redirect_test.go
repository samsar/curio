package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLooksLikeLandingPage pins the landing-page rule on redirects seen in
// the library. Only a page that names something, sent to another site's
// homepage or section without a trace of what it named, is a landing page.
func TestLooksLikeLandingPage(t *testing.T) {
	const googleLogin, driveLogin, atlassianLogin = "Sign in - Google Accounts", "Google Drive: Sign-in", "Log in with Atlassian account"
	cases := []struct {
		source, final, title string
		landing              bool
	}{
		// Landing pages: the destination names no page and kept nothing of
		// the source.
		{"http://java.sun.com/developer/technicalArticles/Intl/IntlIntro/", "https://www.oracle.com/java/technologies/", "", true},
		{"http://java.sun.com/docs/hotspot/gc1.4.2/", "https://www.oracle.com/java/technologies/", "", true},
		{"http://www.ibm.com/developerworks/opensource/library/os-nodejs/", "https://developer.ibm.com/technologies/", "", true},
		{"https://forums.aws.amazon.com/message.jspa?messageID=284911", "https://repost.aws/forums?newRedirect=1&origin=/message.jspa&messageID=284911", "", true},
		{"http://www.thebookoflife.org/the-great-philosophers-aristotle/", "https://www.theschooloflife.com/articles/", "", true},
		{"http://www.bwater.com/home/culture--principles.aspx", "https://www.bridgewater.com/", "", true},
		{"http://radar.oreilly.com/2011/07/what-is-node.html", "https://www.oreilly.com/radar/", "", true},
		{"http://www.ibm.com/developerworks/library/x-ioschat/", "https://developer.ibm.com/technologies/web-development/", "", true},
		{"http://www.appdesignvault.com/portfolio/fitpulse/", "https://roadtolpg.com/", "", true},

		// Moves: the destination kept a word of the source, in its path,
		// query or hostname.
		{"http://www.farnamstreetblog.com/2013/06/the-work-required-to-have-an-opinion/", "https://fs.blog/the-work-required-to-have-an-opinion/", "", false},
		{"http://code.google.com/webtoolkit/doc/latest/tutorial/create.html", "https://www.gwtproject.org/doc/latest/tutorial/create", "", false},
		{"https://twitter.com/jack/status/20", "https://x.com/jack/status/20", "", false},
		{"http://www.collaborativefund.com/blog/useful-hacks/", "https://collabfund.com/blog/useful-hacks/", "", false},
		{"https://youtu.be/dQw4w9WgXcQ?si=AbCdEf", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&feature=youtu.be", "", false},
		{"http://c2.com/cgi/wiki?BurnOut=", "http://wiki.c2.com/?BurnOut", "", false},
		{"https://m.youtube.com/watch?v=GLFnfqgkIxM", "https://www.youtube.com/watch?app=desktop&v=GLFnfqgkIxM", "", false},
		{"http://content.time.com/time/magazine/article/0,9171,1126746,00.html", "https://time.com/archive/6596659/ambition-why-some-people-are-most-likely-to-succeed/", "", false},
		{"https://computing.llnl.gov/tutorials/pthreads/", "https://hpc-tutorials.llnl.gov/posix/", "", false},
		{"http://clients.njoyn.com/cl2/xweb/XWeb.asp?BRID=95934&Jobid=J0315-0701", "https://validate.perfdrive.com/?ssa=a4b9&ssc=http%3A%2F%2Fclients.njoyn.com%2Fcl2%2Fxweb%2FXWeb.asp%3FBRID%3D95934", "", false},

		// The destination names a page: as many segments as the source, the
		// last one more than a section name.
		{"http://www.useit.com/alertbox/20000319.html", "https://www.nngroup.com/articles/why-you-only-need-to-test-with-5-users/", "", false},
		{"https://evernote.com/webclipper/guide/", "https://help.evernote.com/hc/articles/209125877", "", false},

		// The source names no page: shorteners, homepages, index documents.
		{"https://youtu.be/dQw4w9WgXcQ", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&feature=youtu.be", "", false},
		{"https://bit.ly/3xYzAb", "https://example.com/", "", false},
		{"https://www.firebase.com/index.html", "https://firebase.google.com/", "", false},
		{"https://www.homejoy.com/?zipcode=L6C2W1", "https://www.homeaglow.com/", "", false},
		{"http://vectorpoem.com/news/?p=74", "https://jplebreton.com/?p=74", "", false},

		// Login pages are login walls, never landing pages.
		{"https://docs.google.com/document/d/1AbC/edit", "https://accounts.google.com/ServiceLogin?continue=https://docs.google.com/document/d/1AbC/edit", googleLogin, false},
		{"https://drive.google.com/drive/folders/0BydlE3MlPeyD", "https://accounts.google.com/v3/signin/identifier?continue=x", driveLogin, false},
		{"https://steadystate.atlassian.net/wiki/spaces/FM/pages/166494273/Project+Overview", "https://id.atlassian.com/login?continue=x", atlassianLogin, false},

		// The same site: the homepage rule's business, not this one's.
		{"https://example.com/blog/a-post/", "https://www.example.com/", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.source+" → "+tc.final, func(t *testing.T) {
			source, err := url.Parse(tc.source)
			require.NoError(t, err)
			final, err := url.Parse(tc.final)
			require.NoError(t, err)
			assert.Equal(t, tc.landing, looksLikeLandingPage(source, final, tc.title))
		})
	}
}

// newCrossSiteRedirect serves two sites from one server: every request to
// it as 127.0.0.1 is redirected to dest (a path and query) on localhost,
// which serves page. It returns the 127.0.0.1 base URL.
func newCrossSiteRedirect(t *testing.T, dest, page string) string {
	t.Helper()
	return redirectToLocalhost(t, dest, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, page)
	})
}

// newBlockedCrossSiteRedirect is newCrossSiteRedirect with a destination
// that answers status with a block page.
func newBlockedCrossSiteRedirect(t *testing.T, dest string, status int) string {
	t.Helper()
	return redirectToLocalhost(t, dest, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, blockPageHTML)
	})
}

// redirectToLocalhost redirects every request to its server as 127.0.0.1
// to dest on localhost, where answer serves it. It returns the 127.0.0.1
// base URL.
func redirectToLocalhost(t *testing.T, dest string, answer http.HandlerFunc) string {
	t.Helper()
	var destBase string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "localhost:") {
			answer(w, r)
			return
		}
		http.Redirect(w, r, destBase+dest, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	destBase = localhostURL(t, srv.URL)
	return srv.URL
}

// newBlockedSameSiteRedirect redirects every request to dest (a path and
// query) on the same host, which answers status with a block page. It
// returns the base URL.
func newBlockedSameSiteRedirect(t *testing.T, dest string, status int) string {
	t.Helper()
	destURL, err := url.Parse(dest)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == destURL.Path {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, blockPageHTML)
			return
		}
		http.Redirect(w, r, dest, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// blockPageHTML is the body of a blocked answer: Cloudflare's block page,
// which curio drains unread.
const blockPageHTML = `<html><head><title>Attention Required! | Cloudflare</title></head>` +
	`<body><h1>Sorry, you have been blocked</h1></body></html>`

// assertUncached asserts that neither side of a cross-site redirect got a
// host-cache entry.
func assertUncached(t *testing.T, n *Native) {
	t.Helper()
	for _, host := range []string{"127.0.0.1", "localhost"} {
		_, cached := n.hostCache.Get(host)
		assert.False(t, cached, "%s must not be cached", host)
	}
}

// crossSiteNative is a Native whose Jina answers too little text, counting
// its requests, with dead-link detection as given.
func crossSiteNative(t *testing.T, detection bool) (*Native, func() int32) {
	t.Helper()
	n, jinaCalls := newNativeWithJina(t, jinaThin)
	n.deadLinkDetection = detection
	return n, jinaCalls
}

// landingReply is what Jina brings back after following a redirect onto a
// landing page: a page that passes every verdict, so a fetch that reached
// Jina would store it.
var landingReply = jinaReply("Java Technologies | Oracle", nil, longArticleBody)

// landingJinaNative is a Native on backend, with dead-link detection as
// given, whose Jina answers landingReply, counting its requests. With jina
// false the fallback is off and the count stays zero.
func landingJinaNative(t *testing.T, backend string, jina, detection bool) (*Native, func() int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, landingReply)
	}))
	t.Cleanup(srv.Close)
	n := NewNative(NativeOptions{
		Timeout: 5 * time.Second, Backend: backend, DeadLinkDetection: detection,
		JinaFallback: jina, JinaBaseURL: srv.URL + "/",
	})
	return unpaced(n, newFakeClock()), hits.Load
}

// TestNative_CrossSiteLandingPageIsDead: a redirect that settles on another
// site's landing page is a dead link, final at once: no Jina request, and
// neither host cached.
func TestNative_CrossSiteLandingPageIsDead(t *testing.T) {
	cases := []struct{ name, source, dest string }{
		{"a site's section", "/developer/technicalArticles/Intl/IntlIntro/", "/java/technologies/"},
		{"a section as deep as the source", "/the-great-philosophers-aristotle/", "/articles/"},
		{"a query recording the source", "/message.jspa?messageID=284911", "/forums?newRedirect=1&origin=/message.jspa&messageID=284911"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := newCrossSiteRedirect(t, tc.dest, makeArticleHTML("Java Technologies", ""))
			n, jinaCalls := crossSiteNative(t, true)

			_, err := n.Fetch(context.Background(), src+tc.source)
			var pe *PermanentError
			require.ErrorAs(t, err, &pe)
			assert.ErrorIs(t, err, ErrDeadLink)
			assert.Contains(t, err.Error(), "redirected to another site's landing page: localhost:")
			assert.Zero(t, jinaCalls())
			assertUncached(t, n)
		})
	}
}

// TestNative_CrossSiteRedirectIsJudgedLikeAnyPage: a redirect onto another
// site that is neither a landing nor a login page is judged like any page:
// an article is stored as the origin served it, and a thin one is a
// page-level login wall that goes to Jina. Neither is cached.
func TestNative_CrossSiteRedirectIsJudgedLikeAnyPage(t *testing.T) {
	t.Run("article", func(t *testing.T) {
		src := newCrossSiteRedirect(t, "/blog/useful-hacks/", makeArticleHTML("Useful Hacks", ""))
		n, jinaCalls := crossSiteNative(t, true)

		res, err := n.Fetch(context.Background(), src+"/blog/useful-hacks/")
		require.NoError(t, err)
		assert.Equal(t, "readability", res.Meta["via"])
		assert.Equal(t, localhostURL(t, src)+"/blog/useful-hacks/", res.FinalURL)
		assert.Zero(t, jinaCalls())
		assertUncached(t, n)
	})

	t.Run("thin", func(t *testing.T) {
		src := newCrossSiteRedirect(t, "/blog/useful-hacks/", thinPage)
		n, jinaCalls := crossSiteNative(t, true)

		_, err := n.Fetch(context.Background(), src+"/blog/useful-hacks/")
		require.ErrorIs(t, err, ErrLoginWall)
		assert.NotErrorIs(t, err, errOffsiteLoginWall)
		assert.NotErrorIs(t, err, errSiteLoginWall)
		assert.Contains(t, err.Error(), "extracted text < 500 bytes")
		assert.Equal(t, int32(1), jinaCalls())
		assertUncached(t, n)
	})
}

// TestNative_CrossSiteLoginIsFinal: a redirect onto another site's login
// page, by its path or its title, fails for good without Jina, which has
// no session and follows the same redirect. The document goes failed, not
// dead, and neither host is cached: the requested one only sent a link to
// an account host.
func TestNative_CrossSiteLoginIsFinal(t *testing.T) {
	cases := []struct{ name, dest, page string }{
		{"login path", "/login?continue=https://example.com/doc", makeArticleHTML("Atlassian", "")},
		{"login title", "/ServiceLogin?continue=https://example.com/doc", makeArticleHTML("Sign in - Google Accounts", "")},
	}
	for _, tc := range cases {
		for _, detection := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/detection=%v", tc.name, detection), func(t *testing.T) {
				src := newCrossSiteRedirect(t, tc.dest, tc.page)
				n, jinaCalls := crossSiteNative(t, detection)

				_, err := n.Fetch(context.Background(), src+"/document/d/1AbC/edit")
				var pe *PermanentError
				require.ErrorAs(t, err, &pe)
				assert.ErrorIs(t, err, ErrLoginWall)
				assert.ErrorIs(t, err, errOffsiteLoginWall)
				assert.NotErrorIs(t, err, errSiteLoginWall)
				assert.NotErrorIs(t, err, ErrDeadLink)
				assert.Contains(t, err.Error(), "redirected onto another site's login page: localhost:")
				assert.Zero(t, jinaCalls())
				assertUncached(t, n)
			})
		}
	}
}

// TestNative_LandingRuleNeedsDeadLinkDetection: with dead-link detection
// off, a redirect onto a landing page is judged like any page and stored,
// still without asking Jina.
func TestNative_LandingRuleNeedsDeadLinkDetection(t *testing.T) {
	src := newCrossSiteRedirect(t, "/java/technologies/", makeArticleHTML("Java Technologies", ""))
	n, jinaCalls := crossSiteNative(t, false)

	res, err := n.Fetch(context.Background(), src+"/developer/technicalArticles/Intl/IntlIntro/")
	require.NoError(t, err)
	assert.Equal(t, "readability", res.Meta["via"])
	assert.Zero(t, jinaCalls())
	assertUncached(t, n)
}

// TestNative_IndexDocumentRedirectIsNoDeadLink: a bookmarked index document
// that redirects to the site root is stored as the homepage it is, not
// judged a page redirected to the homepage.
func TestNative_IndexDocumentRedirectIsNoDeadLink(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/index.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, makeArticleHTML("MIT OpenCourseWare", ""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	n, jinaCalls := crossSiteNative(t, true)

	res, err := n.Fetch(context.Background(), srv.URL+"/index.html")
	require.NoError(t, err)
	assert.Equal(t, "readability", res.Meta["via"])
	assert.Equal(t, srv.URL+"/", res.FinalURL)
	assert.Zero(t, jinaCalls())
}

// TestNative_SameSiteLoginRedirectIsSiteWide: a redirect onto the requested
// site's own login page is still host-wide: cached under the site, and the
// first failure stays retryable after asking Jina.
func TestNative_SameSiteLoginRedirectIsSiteWide(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, makeArticleHTML("Log in", ""))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	n, jinaCalls := crossSiteNative(t, true)

	_, err := n.Fetch(context.Background(), srv.URL+"/post")
	require.ErrorIs(t, err, errSiteLoginWall)
	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "the first failure stays retryable: %v", err)
	assert.Equal(t, int32(1), jinaCalls())
	_, cached := n.hostCache.Get("127.0.0.1")
	assert.True(t, cached)
}

// TestNative_BlockedLandingPageIsDead: a redirect onto another site's
// landing page that answers 403 or 503 is the dead link it is on a 2xx, not
// anti-bot. It is final at once, without Jina, which would follow the same
// redirect and store the landing page; it caches neither host, and wins
// over a fresh entry for the destination.
func TestNative_BlockedLandingPageIsDead(t *testing.T) {
	const source, dest = "/developer/technicalArticles/Intl/IntlIntro/", "/java/technologies/"
	for _, backend := range []string{"chrome", "stock"} {
		for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
			for _, jina := range []bool{true, false} {
				for _, destCached := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%d/jina=%v/destination cached=%v", backend, status, jina, destCached), func(t *testing.T) {
						src := newBlockedCrossSiteRedirect(t, dest, status)
						n, jinaCalls := landingJinaNative(t, backend, jina, true)
						if destCached {
							n.hostCache.Put("localhost", HostFailAntiBot, "native: HTTP 403 Forbidden: "+ErrAntiBot.Error())
						}

						_, err := n.Fetch(context.Background(), src+source)
						var pe *PermanentError
						require.ErrorAs(t, err, &pe)
						assert.ErrorIs(t, err, ErrDeadLink)
						assert.NotErrorIs(t, err, ErrAntiBot)
						var se *HTTPStatusError
						require.ErrorAs(t, err, &se, "the origin's status stays in the chain")
						assert.Equal(t, status, se.StatusCode)
						assert.Equal(t, "localhost", hostOf(se.URL))
						assert.Regexp(t, fmt.Sprintf(`^native: HTTP %d %s: dead link \(redirected to another site's landing page: localhost:\d+%s\): %s$`,
							status, http.StatusText(status), dest, regexp.QuoteMeta(ErrDeadLink.Error())), err.Error())
						assert.Zero(t, jinaCalls())

						_, cached := n.hostCache.Get("127.0.0.1")
						assert.False(t, cached, "the requested host is not cached")
						_, cached = n.hostCache.Get("localhost")
						assert.Equal(t, destCached, cached, "the destination is cached only as seeded")
					})
				}
			}
		}
	}
}

// TestNative_BlockedHomepageIsDead: a redirect onto the site's homepage that
// answers 403 or 503 is the dead link it is on a 2xx: final, without Jina,
// which would store the homepage, and uncached.
func TestNative_BlockedHomepageIsDead(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			src := newBlockedSameSiteRedirect(t, "/", status)
			n, jinaCalls := landingJinaNative(t, "", true, true)

			_, err := n.Fetch(context.Background(), src+"/2019/05/a-deleted-post")
			var pe *PermanentError
			require.ErrorAs(t, err, &pe)
			assert.ErrorIs(t, err, ErrDeadLink)
			assert.NotErrorIs(t, err, ErrAntiBot)
			assert.Contains(t, err.Error(), fmt.Sprintf("HTTP %d", status))
			assert.Contains(t, err.Error(), "dead link (redirected to homepage)")
			assert.Zero(t, jinaCalls())
			_, cached := n.hostCache.Get("127.0.0.1")
			assert.False(t, cached)
		})
	}
}

// TestNative_BlockedOffsiteLoginIsFinal: a redirect onto another site's
// login page that answers 403 or 503 is the final login wall it is on a
// 2xx, whatever dead-link detection says: no Jina request, which would
// follow the same redirect, and neither host cached.
func TestNative_BlockedOffsiteLoginIsFinal(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
		for _, detection := range []bool{true, false} {
			t.Run(fmt.Sprintf("%d/detection=%v", status, detection), func(t *testing.T) {
				src := newBlockedCrossSiteRedirect(t, "/login?continue=https://example.com/doc", status)
				n, jinaCalls := landingJinaNative(t, "", true, detection)

				_, err := n.Fetch(context.Background(), src+"/document/d/1AbC/edit")
				var pe *PermanentError
				require.ErrorAs(t, err, &pe)
				assert.ErrorIs(t, err, errOffsiteLoginWall)
				assert.ErrorIs(t, err, ErrLoginWall)
				assert.NotErrorIs(t, err, ErrAntiBot)
				assert.NotErrorIs(t, err, ErrDeadLink)
				var se *HTTPStatusError
				require.ErrorAs(t, err, &se)
				assert.Equal(t, status, se.StatusCode)
				assert.Contains(t, err.Error(), "redirected onto another site's login page: localhost:")
				assert.Zero(t, jinaCalls())
				assertUncached(t, n)
			})
		}
	}
}

// TestNative_BlockedPageIsAntiBotWithoutARedirectVerdict: a 403 from a page
// no redirect rule judges (another site's page that kept the source's
// words, the site's own login page) is anti-bot as before: host-wide,
// cached under the host that answered once Jina gave its own verdict, the
// first failure retryable, and stored through Jina when Jina passes.
func TestNative_BlockedPageIsAntiBotWithoutARedirectVerdict(t *testing.T) {
	cases := []struct {
		name, source string
		redirect     func(t *testing.T) string
		answering    string // the host cached
	}{
		{"another site's page", "/blog/useful-hacks/", func(t *testing.T) string {
			return newBlockedCrossSiteRedirect(t, "/blog/useful-hacks/", http.StatusForbidden)
		}, "localhost"},
		{"the site's own login page", "/post", func(t *testing.T) string {
			return newBlockedSameSiteRedirect(t, "/login?next=/post", http.StatusForbidden)
		}, "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/jina thin", func(t *testing.T) {
			src := tc.redirect(t)
			n, jinaCalls := crossSiteNative(t, true)

			_, err := n.Fetch(context.Background(), src+tc.source)
			require.ErrorIs(t, err, ErrAntiBot)
			assert.NotErrorIs(t, err, ErrDeadLink)
			assert.NotErrorIs(t, err, errOffsiteLoginWall)
			var pe *PermanentError
			assert.False(t, errors.As(err, &pe), "the first failure stays retryable: %v", err)
			assert.Equal(t, int32(1), jinaCalls())
			for _, host := range []string{"127.0.0.1", "localhost"} {
				_, cached := n.hostCache.Get(host)
				assert.Equal(t, host == tc.answering, cached, "%s cached", host)
			}
		})
		t.Run(tc.name+"/jina passes", func(t *testing.T) {
			src := tc.redirect(t)
			n, jinaCalls := landingJinaNative(t, "", true, true)

			res, err := n.Fetch(context.Background(), src+tc.source)
			require.NoError(t, err)
			assert.Equal(t, "jina", res.Meta["via"])
			assert.Equal(t, int32(1), jinaCalls())
		})
	}
}

// TestNative_BlockedLandingPageNeedsDeadLinkDetection: with dead-link
// detection off, a redirect onto a landing page that answers 403 is
// anti-bot, and goes to Jina, as before.
func TestNative_BlockedLandingPageNeedsDeadLinkDetection(t *testing.T) {
	src := newBlockedCrossSiteRedirect(t, "/java/technologies/", http.StatusForbidden)
	n, jinaCalls := crossSiteNative(t, false)

	_, err := n.Fetch(context.Background(), src+"/developer/technicalArticles/Intl/IntlIntro/")
	require.ErrorIs(t, err, ErrAntiBot)
	assert.NotErrorIs(t, err, ErrDeadLink)
	assert.Equal(t, int32(1), jinaCalls())
}
