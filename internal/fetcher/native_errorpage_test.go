package fetcher

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Error pages the library stored through Jina before its answers were
// judged. Their sites answered 403 or 503 (or Cloudflare 526); without
// Jina's target-status warning, only the page itself tells.
const (
	ibmNoticeBody = `My IBM Log in

 503

Oops — that's not right!
## Oops — that's not right!

The page you requested cannot be displayed.503: Service Unavailable Suggested actions If you typed the address, please make sure that the spelling is correct. Note: Most addresses are case sensitive.For information on IBM offerings, start from the [IBM homepage](http://www.ibm.com/).[Search the IBM Web site](https://www.ibm.com/search).

Get assistance This option lets you send an information request and tell us about a broken link. You will receive an e-mail from us to help you find what you need. Report the issue

Overview Annual report Corporate social responsibility Inclusion@IBM Financing Investor Newsroom Security, privacy & trust Senior leadership Careers with IBM Website Blog Publications Automotive Banking Consumer Goods Energy Government Healthcare Insurance Life Sciences Manufacturing Retail Telecommunications Travel Our strategic partners Find a partner Become a partner - Partner Plus Partner Plus log in IBM TechXChange Community LinkedIn X Instagram YouTube Subscription Center Participate in user experience research Podcasts United States — English Contact IBM Privacy Terms of use Accessibility`

	stanfordForbiddenBody = `[Skip navigation](http://www.stanford.edu/class/e140/e140a/handouts/ProductMgmt.txt#content)

[![Image 1: Stanford](http://www.stanford.edu/su-identity/images/stanford-white@2x.png)](http://www.stanford.edu/)

# Access forbidden

The page you requested is not accessible. This may either be due to the server not having permission to access this directory, or a server error. If you are the administrator for this site, please check the permissions on this directory and [file a HelpSU](http://helpsu.stanford.edu/) if you need help. If you are a user, please contact the administrators for this site, or HelpSU if you think the error is due to a server problem.

[![Image 2: Stanford University](http://www.stanford.edu/su-identity/images/footer-stanford-logo@2x.png)](http://www.stanford.edu/)

*   [SU Home](http://www.stanford.edu/)
*   [Maps & Directions](http://visit.stanford.edu/plan/maps.html)
*   [Search Stanford](http://www.stanford.edu/search/)
*   [Terms of Use](http://www.stanford.edu/site/terms.html)
*   [Copyright Complaints](http://www.stanford.edu/site/copyright.html)

© Stanford University, Stanford, California 94305`

	cfInvalidSSLBody = `## What can I do?

### If you're a visitor of this website:

Please try again in a few minutes.

### If you're the owner of this website:

The SSL certificate presented by the server did not pass validation. This could indicate an expired SSL certificate or a certificate that does not include the requested domain name. Please contact your hosting provider to ensure that an up-to-date and valid SSL certificate issued by a Certificate Authority is configured for this domain name on the origin server.[Additional troubleshooting information here.](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-526/)`

	// gcsAccessDeniedBody is untitled and, at 588 bytes, long enough to
	// pass as an article.
	gcsAccessDeniedBody = "`AccessDenied`Access denied.\n\n" +
		"Anonymous caller does not have storage.objects.get access to the Google Cloud Storage object. " +
		"Permission 'storage.objects.get' denied on resource (or it may not exist).\n\n" +
		"This XML file does not appear to have any style information associated with it. The document tree is shown below.\n\n" +
		"<Error>\n\n<Code>AccessDenied</Code>\n\n<Message>Access denied.</Message>\n\n" +
		"<Details>Anonymous caller does not have storage.objects.get access to the Google Cloud Storage object. " +
		"Permission 'storage.objects.get' denied on resource (or it may not exist).</Details>\n\n...\n\n</Error>"

	s3AccessDeniedBody = `*   Code: AccessDenied
*   Message: Access Denied
*   AccountId: 216416913500
*   RequestId: JKBEVD29QRJXACJX
*   HostId: TAUUPbKNkRpDwTCV9GqcyAE673UTwoxZxiOb361z0fH9ys6JGl1PkFazGMJzkfauD1EIcRm2p54=

* * *`
)

// aptanaForbiddenBody is a 403 page buried in a cookie-consent banner, far
// past the 2 KiB phrases are looked for in: only its title gives it away.
var aptanaForbiddenBody = "We value your privacy\n\nThis website uses cookies to enhance your browsing experience, " +
	"serve personalized content/ads, and analyze traffic. By clicking “Accept All” you agree to use of cookies " +
	"for an optimal web experience. You can also customize your cookie settings here.\n\n" +
	strings.Repeat("Necessary cookies help make a website usable by enabling basic functions like page "+
		"navigation and access to secure areas of the website. ", 20)

func TestErrorPageTitleRE(t *testing.T) {
	errorPages := []struct {
		title string
		code  int
	}{
		{"IBM notice: The page you requested cannot be displayed", 503},
		{"Access forbidden : Stanford University", 403},
		{"Access forbidden", 403},
		{"403 | Forbidden | Axway", 403},
		{"403 | Forbidden", 403},
		{"403 Forbidden", 403},
		{"Error 403 - Forbidden", 403},
		{"Forbidden", 403},
		{"503 Service Unavailable", 503},
		{"Service Unavailable", 503},
		{"Error 503", 503},
		{"500 Internal Server Error", 500},
		{"502 Bad Gateway", 502},
		{"504 Gateway Time-out", 504},
		{"appdesignvault.com | 526: Invalid SSL certificate", 526},
		{"example.com | 522: Connection timed out", 522},
		{"example.com | 503: Service unavailable", 503},
		// No code: the phrase alone names the status.
		{"Internal Server Error", 500},
		{"Bad Gateway", 502},
		{"Gateway Timeout", 504},
		{"Gateway Time-out", 504},
		{"Service Temporarily Unavailable", 503},
	}
	for _, tc := range errorPages {
		m := errorPageTitleRE.FindStringSubmatch(tc.title)
		if assert.NotNil(t, m, "should match %q", tc.title) {
			code := errorPageStatus(m)
			assert.Equal(t, tc.code, code, tc.title)
			// The status decides the verdict: anti-bot, which goes to Jina,
			// or a server error, retried without it.
			verdict := errorPageVerdict(code, tc.title)
			antiBot := tc.code == http.StatusForbidden || tc.code == http.StatusServiceUnavailable
			assert.Equal(t, antiBot, errors.Is(verdict, ErrAntiBot), "anti-bot: %s", tc.title)
			assert.Equal(t, !antiBot, errors.Is(verdict, errServerErrorPage), "server error: %s", tc.title)
		}
	}

	articles := []string{
		"Access Forbidden: A History of Web Censorship",
		"How to fix a 403 Forbidden error",
		"Understanding Cloudflare Error 526",
		"403 Forbidden Error: What It Is and How to Fix It",
		"Forbidden Planet (1956)",
		"The Forbidden City",
		"Service Unavailable: lessons from our outage",
		"Why the page you requested cannot be displayed in Internet Explorer",
		"The 500 Internal Server Error, demystified",
		"Cloudflare | 526: what it means",
		"Error 503 explained: causes and fixes",
		// The near misses of the challenge, not-found and login title rules.
		"Just a moment of silence",
		"Attention required! Why focus matters",
		"Access Denied: A History of Web Censorship",
		"Are you a robot? The Turing test at 75",
		"Pardon our dust: new site coming",
		"Understanding HTTP 404s and how to avoid them",
		"How we redesigned our 404 experience",
		"Finding lost cities: places not found on any map",
		"The Signal and the Noise",
		"Go 1.25 release notes",
		"Login cognito using with scope openId using id_token or access_token don't working",
		"How to log in to Grafana with SSO",
		"Sign In With Apple: A Developer's Guide",
		"Logging in with OAuth 2.0: a primer",
		"Why your login page leaks usernames",
		"Designing a better sign-in",
	}
	for _, title := range articles {
		assert.False(t, errorPageTitleRE.MatchString(title), "should NOT match %q", title)
	}
}

// TestLooksLikeErrorPage recognizes the library's stored error pages by
// their titles, and an untitled S3 or GCS error by its body when the page
// is short.
func TestLooksLikeErrorPage(t *testing.T) {
	cases := []struct {
		name, title, text string
		code              int
	}{
		{"ibm notice", "IBM notice: The page you requested cannot be displayed", ibmNoticeBody, 503},
		{"stanford", "Access forbidden : Stanford University", stanfordForbiddenBody, 403},
		{"aptana", "403 | Forbidden | Axway", aptanaForbiddenBody, 403},
		{"aptana, stored title", "403 | Forbidden", aptanaForbiddenBody, 403},
		{"cloudflare 526", "appdesignvault.com | 526: Invalid SSL certificate", cfInvalidSSLBody, 526},
		{"google cloud storage", "", gcsAccessDeniedBody, 403},
		{"an access-denied body past 2 KiB", "Serving private files from GCS",
			strings.Repeat("A paragraph about bucket permissions. ", 60) + gcsAccessDeniedBody, 0},
		{"an article", "A real article", longArticleBody, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, reason := looksLikeErrorPage(pageView{title: tc.title, text: tc.text, found: true})
			assert.Equal(t, tc.code, code)
			assert.Equal(t, tc.code != 0, reason != "", "reason %q", reason)
		})
	}
}

// TestNative_ErrorPageFromOrigin: an error page an origin serves with a
// 200 is judged like the status it names, and never cached. A 403 page is
// anti-bot, about that page alone: Jina is asked once when it is on, and
// the fetch then fails for good. A 502 page is the server's trouble for
// now: retryable, and not worth a Jina request.
func TestNative_ErrorPageFromOrigin(t *testing.T) {
	serve := func(t *testing.T, page string) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, page)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	stanford := makeArticleHTML("Access forbidden : Stanford University", strings.Repeat(
		"The page you requested is not accessible. This may either be due to the server not having permission to access this directory, or a server error. ", 3))
	badGateway := makeArticleHTML("502 Bad Gateway", "")

	for _, tc := range []struct {
		mode      jinaMode
		jinaCalls int32
	}{{jinaOff, 0}, {jinaThin, 1}} {
		t.Run("403 page/jina "+string(tc.mode), func(t *testing.T) {
			srv := serve(t, stanford)
			n, jinaCalls := newNativeWithJina(t, tc.mode)
			_, err := n.Fetch(context.Background(), srv.URL+"/class/handouts/ProductMgmt.txt")
			require.ErrorIs(t, err, ErrAntiBot)
			assert.Contains(t, err.Error(), "error page")
			var pe *PermanentError
			assert.ErrorAs(t, err, &pe, "every extraction path answered")
			assert.Equal(t, tc.jinaCalls, jinaCalls())
			assert.False(t, hostCached(n, hostOf(srv.URL)))
		})
	}

	t.Run("502 page", func(t *testing.T) {
		srv := serve(t, badGateway)
		n, jinaCalls := newNativeWithJina(t, jinaThin)
		_, err := n.Fetch(context.Background(), srv.URL+"/post")
		require.ErrorIs(t, err, errServerErrorPage)
		var pe *PermanentError
		assert.False(t, errors.As(err, &pe), "a server error is retried: %v", err)
		assert.Zero(t, jinaCalls())
		assert.False(t, hostCached(n, hostOf(srv.URL)))
	})
}

// TestNative_JinaServerErrorPageIsRetryable: a Jina answer that is a
// Cloudflare 5xx page, with no warning naming the status, is the target's
// trouble for now: retryable after one Jina request, and nothing cached.
func TestNative_JinaServerErrorPageIsRetryable(t *testing.T) {
	h := newJinaHarness(t, http.StatusOK, true,
		jinaReply("appdesignvault.com | 526: Invalid SSL certificate", nil, cfInvalidSSLBody))
	_, err := h.n.Fetch(context.Background(), h.origin.URL+"/portfolio/fitpulse/")
	require.ErrorIs(t, err, errServerErrorPage)
	assert.ErrorIs(t, err, errJinaTargetTrouble)
	var pe *PermanentError
	assert.False(t, errors.As(err, &pe), "must stay retryable: %v", err)
	assert.Equal(t, int32(1), h.jinaHits.Load())
	assert.False(t, h.originCached())
}
