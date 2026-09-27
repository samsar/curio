package fetcher

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/time/rate"

	"github.com/samsar/curio/internal/store"
)

// Native is the Go-native fetcher that replaces the Node `web2md` tool as
// the v1 default. Port of github.com/samsar/web-to-markdown to Go using:
//
//   - net/http for fetch
//   - codeberg.org/readeck/go-readability/v2 for article extraction
//   - github.com/JohannesKaufmann/html-to-markdown/v2 for HTML→Markdown
//
// Every page that comes back, from the origin or from the Jina Reader
// fallback, goes through the same page verdicts (judgePage) before it is
// stored.
type Native struct {
	rt                roundTripper
	userAgent         string
	secChUA           string
	jinaFallback      bool
	jinaBaseURL       string // override for tests
	jinaAPIKey        string
	jinaLimiter       rateLimiter
	jinaCooldown      cooldown
	deadLinkDetection bool
	log               *slog.Logger
	hostCache         *hostFailureCache
	originSlots       *hostGate
	clock             clock
}

// NativeOptions configures Native. Zero-value fields use defaults.
type NativeOptions struct {
	Timeout time.Duration
	// UserAgent overrides the User-Agent the Chrome profile implies. It is
	// sent as is; one that names a different Chrome version than the
	// profile logs a warning, since bot checks compare the two.
	UserAgent    string
	JinaFallback bool
	JinaBaseURL  string // default https://r.jina.ai/
	// JinaAPIKey is sent as a bearer token and raises Jina's rate limit.
	// Empty falls back to the CURIO_JINA_API_KEY environment variable.
	JinaAPIKey string
	// DeadLinkDetection classifies hard 404/410 and detected soft 404s
	// as permanent dead links (never retried, never sent to Jina).
	// Off by default here like JinaFallback — the daemon passes the
	// config value, which defaults to true.
	DeadLinkDetection bool
	Log               *slog.Logger
	// HostFailureTTL is how long a cached host-wide failure is honored
	// before we try the host again. Default 15 minutes — long enough
	// to drain an import without re-trying hopeless hosts, short
	// enough that a transient outage doesn't permanently blacklist
	// the site for a long-running daemon.
	HostFailureTTL time.Duration
	// Backend selects the HTTP transport. "chrome" (default) parrots a real
	// Chrome TLS+HTTP/2 fingerprint via uTLS to clear JA3/Akamai bot checks;
	// "stock" uses Go's net/http (recognizable Go fingerprint, no extra
	// network behavior to reason about). "chrome_120"/"chrome_124"/
	// "chrome_131"/"chrome_133" pin a specific profile, and with it the
	// User-Agent and sec-ch-ua sent. See transport.go.
	Backend string
}

const (
	// Jina's published limits are 20 requests a minute without an API key
	// and 500 with a free one. The keyed rate stays well under the latter.
	jinaRequestsPerMinute      = 20
	jinaKeyedRequestsPerMinute = 200
	// maxInlineJinaWait is the longest Jina cooldown a fetch sits out.
	// Longer ones fail the fetch retryably and leave the wait to the job
	// queue's backoff.
	maxInlineJinaWait = 30 * time.Second
	// jinaAttempts is how many times one fetch calls Jina for transient
	// failures (5xx, 429, transport errors).
	jinaAttempts = 4
	// originRequestsPerHost bounds concurrent origin requests to one host.
	originRequestsPerHost = 2
)

func NewNative(opts NativeOptions) *Native {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.JinaBaseURL == "" {
		opts.JinaBaseURL = "https://r.jina.ai/"
	}
	if opts.JinaAPIKey == "" {
		opts.JinaAPIKey = os.Getenv("CURIO_JINA_API_KEY")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	// Every fetch worker shares one Native, so this one limiter paces all
	// of their Jina calls.
	jinaPerMinute := jinaRequestsPerMinute
	if opts.JinaAPIKey != "" {
		jinaPerMinute = jinaKeyedRequestsPerMinute
	}
	jinaLimiter := rate.NewLimiter(rate.Every(time.Minute/time.Duration(jinaPerMinute)), 1)

	// The stock backend has no Chrome fingerprint, but its headers still
	// come from the latest profile.
	prof, known := chromeProfile(opts.Backend)
	if !known && !isStockBackend(opts.Backend) {
		opts.Log.Warn("unknown chrome profile, using latest", "requested", opts.Backend, "using", prof.name)
	}
	if opts.UserAgent == "" {
		opts.UserAgent = prof.userAgent
	} else if !strings.Contains(opts.UserAgent, fmt.Sprintf("Chrome/%d.", prof.major)) {
		opts.Log.Warn("user_agent doesn't name the chrome profile's version; bot checks compare the two",
			"profile", prof.name)
	}

	rt, err := newRoundTripper(opts.Backend, prof, opts.Timeout)
	if err != nil {
		// A fingerprint backend that won't initialize shouldn't take the
		// fetcher down — degrade to stock net/http and carry on.
		opts.Log.Warn("fetcher transport init failed, falling back to stock net/http",
			"backend", opts.Backend, "err", err)
		rt = newStockRT(opts.Timeout)
	}
	rt = limitBodies(rt, maxResponseBytes)
	opts.Log.Info("native fetcher transport", "backend", rt.name())
	return &Native{
		rt:                rt,
		userAgent:         opts.UserAgent,
		secChUA:           prof.secChUA,
		jinaFallback:      opts.JinaFallback,
		jinaBaseURL:       opts.JinaBaseURL,
		jinaAPIKey:        opts.JinaAPIKey,
		jinaLimiter:       jinaLimiter,
		deadLinkDetection: opts.DeadLinkDetection,
		log:               opts.Log,
		hostCache:         newHostFailureCache(opts.HostFailureTTL),
		originSlots:       newHostGate(originRequestsPerHost),
		clock:             realClock,
	}
}

func (*Native) Name() string { return "native" }

func (n *Native) Fetch(ctx context.Context, target string) (*Result, error) {
	if strings.TrimSpace(target) == "" {
		return nil, errors.New("native: url is empty")
	}

	// Fast-fail by domain: if this host had a host-wide failure recently,
	// short-circuit instead of burning the full retry/Jina budget.
	// Three reasons this matters in practice:
	//   1. ~17% of import failures concentrate in <15 hosts (LinkedIn,
	//      NYT, Inc.com, dribbble, etc.) — same fail signature every time.
	//   2. Each origin failure costs ~30s of HTTP timeout + retries;
	//      Jina fallback adds another ~30s of its own retries.
	//   3. Hammering Jina with hopeless lookups gets us 429'd on the
	//      cases where it would have helped.
	// Cache is in-memory only — survives goroutines, not daemon restarts.
	// That's fine: it re-warms within minutes of resuming.
	host := hostOf(target)
	if err := n.cachedFailure(target, host); err != nil {
		return nil, err
	}

	release, err := n.originSlots.acquire(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("native: wait for a request slot on %s: %w", host, err)
	}
	defer release()
	// A verdict cached while this fetch queued for the host applies to it
	// too; don't send the request that verdict says is hopeless.
	if err := n.cachedFailure(target, host); err != nil {
		return nil, err
	}

	// Pass 1: direct fetch + Readability (or local PDF extraction).
	res, originErr := n.tryReadability(ctx, target)
	if originErr == nil {
		return res, nil
	}
	// Dead links, oversized or unsupported bodies, deterministic statuses:
	// final, and nothing Jina can fix.
	var pe *PermanentError
	if errors.As(originErr, &pe) {
		return nil, originErr
	}
	// A redirect can end on a host whose verdict is already cached. That
	// verdict is as final here as for the host's own URLs; the checks above
	// only cover the requested host, so without this one every retry would
	// repeat the origin and Jina calls.
	if _, answering, ok := hostVerdict(originErr, host); ok && answering != host {
		if err := n.cachedFailure(target, answering); err != nil {
			return nil, err
		}
	}
	if !n.jinaFallback || !jinaCanHelp(originErr) {
		// Settled while still holding the slot, so fetches queued for this
		// host see any verdict it caches.
		return nil, n.settle(host, originErr, originErr)
	}
	// The origin's answer is in; never hold its slot while waiting on or
	// calling Jina.
	release()

	// Pass 2: Jina.
	n.log.Info("native fetch needs help, falling back to jina",
		"url", target, "err", originErr.Error())
	res, jinaErr := n.tryJina(ctx, target)
	if jinaErr == nil {
		if errors.Is(originErr, errPDFUnreadable) {
			res.ContentType = store.ContentTypePDF // Jina reports every page as an article
		}
		return res, nil
	}
	// Jina's failure leads the chain: it is the path a retry now depends
	// on, so errors.As finds its status and Retry-After first. errors.Is
	// still matches the origin's sentinel.
	err = fmt.Errorf("%w (after %w)", jinaErr, originErr)
	switch {
	case errors.Is(jinaErr, ErrTooLarge), errors.Is(jinaErr, ErrDeadLink):
		// Final, and about this URL alone: never cached.
		return nil, &PermanentError{Err: err}
	case !jinaAnswered(jinaErr):
		// Jina's own trouble, or trouble the target has for now, says
		// nothing lasting about the target: never cache it, and let the job
		// retry.
		return nil, err
	}
	return nil, n.settle(host, originErr, err)
}

// jinaCanHelp reports whether an origin failure is one Jina might get past:
//
//   - ErrLoginWall: the page came back but was paywalled or thin
//   - ErrAntiBot: 403/503 from the origin, likely a WAF block
//   - errPDFUnreadable: a PDF the local extractor couldn't read; Jina
//     renders PDFs itself
//
// Everything else (404, other statuses, DNS failures, timeouts) goes
// without Jina: it can't conjure a page that doesn't exist, and spending
// its rate limit on dead links gets us 429'd on the calls that would
// benefit.
func jinaCanHelp(err error) bool {
	return errors.Is(err, ErrLoginWall) || errors.Is(err, ErrAntiBot) || errors.Is(err, errPDFUnreadable)
}

// jinaAnswered reports whether a failed Jina call is a verdict about the
// target: Jina fetched it and its answer is not the page, or Jina refused it
// with a deterministic 4xx. Rate limits, outages, timeouts and transport
// errors are trouble on Jina's side, 401/402 are about our account, and a
// transient status the target gave Jina is the target's trouble for now;
// none of them is a verdict.
func jinaAnswered(err error) bool {
	if errors.Is(err, errJinaRejected) {
		return true
	}
	var se *HTTPStatusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.StatusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired:
		return false
	}
	return se.StatusCode >= 400 && se.StatusCode < 500 && !retryableStatus(se.StatusCode)
}

// settle decides what an origin failure that no extraction path could
// rescue becomes; err is what Fetch returns for it.
//
//   - A host-wide verdict is cached under the host that gave it, and err
//     stays retryable: the first failure for a host gets one more real
//     attempt, later URLs on the host hit the cache.
//   - A page-level verdict Jina could have helped with is final. Every
//     extraction path has answered, so a retry would only repeat the origin
//     and Jina calls (up to four Jina requests each), which is the budget
//     the fallback policy protects.
//   - Anything else (a transient status, a transport error) is returned as
//     is.
//
// Only originErr is judged. Jina's verdicts are about the one page it was
// asked for and never write the cache.
func (n *Native) settle(requestedHost string, originErr, err error) error {
	if kind, host, ok := hostVerdict(originErr, requestedHost); ok {
		n.hostCache.Put(host, kind, err.Error())
		return err
	}
	if jinaCanHelp(originErr) {
		return &PermanentError{Err: err}
	}
	return err
}

// hostVerdict reports whether an origin failure speaks for a whole host, and
// for which one: the host that gave the verdict, which after a redirect is
// not the host requested. Caching a redirect target's verdict under the
// requested host would fail every healthy URL on, say, a link shortener.
// Host-wide means:
//
//   - unreachable: the name doesn't exist, or the host refuses connections
//     or has no route
//   - anti-bot: a 403/503 answer, from the host that sent it
//   - login wall: a redirect onto the requested site's own login page
//
// Everything else is about one page (thin content, a cross-site redirect, a
// bot challenge recognized in a page's content) or transient, and caching it
// would fail healthy URLs without a request.
func hostVerdict(err error, requestedHost string) (kind HostFailureKind, host string, ok bool) {
	switch {
	case errors.Is(err, ErrHostUnreachable):
		var ue *url.Error
		if errors.As(err, &ue) {
			return HostFailUnreachable, orHost(hostOf(ue.URL), requestedHost), true
		}
		return HostFailUnreachable, requestedHost, true
	case errors.Is(err, ErrAntiBot):
		var se *HTTPStatusError
		if !errors.As(err, &se) {
			return 0, "", false
		}
		return HostFailAntiBot, orHost(hostOf(se.URL), requestedHost), true
	case errors.Is(err, errSiteLoginWall):
		return HostFailLoginWall, requestedHost, true
	}
	return 0, "", false
}

func orHost(host, fallback string) string {
	if host == "" {
		return fallback
	}
	return host
}

// cachedFailure returns the fresh host-cache verdict for host as a
// PermanentError, or nil. The verdict cannot change inside the TTL, so
// letting the worker back off and retry (60s → 120s → 240s → 480s, ~15 min
// per URL) would only re-read the cache four more times. Recovery once the
// host is healthy is `curio refetch --all --state=failed`. The sentinel is
// kept so callers can still errors.Is the failure kind, and the
// "(cached: …)" suffix survives into last_error for diagnosis.
func (n *Native) cachedFailure(target, host string) error {
	cached, ok := n.hostCache.Get(host)
	if !ok {
		return nil
	}
	n.log.Info("fast-fail from host cache",
		"url", target, "host", host, "kind", cached.kind.String(),
		"age_seconds", int(time.Since(cached.seenAt).Seconds()))
	return &PermanentError{Err: fmt.Errorf("native: %w (cached: %s)", cached.kind.sentinel(), cached.originalErr)}
}

// tryReadability does pass 1: fetch HTML, run Readability, render to
// markdown. A page that isn't the article fails with judgePage's verdict,
// so callers can tell "page is paywalled or a challenge" from "page failed
// to fetch."
func (n *Native) tryReadability(ctx context.Context, target string) (*Result, error) {
	// Mimic Chrome more thoroughly than just the UA string. CDNs like
	// Cloudflare cross-check several headers (Sec-Fetch-*, Sec-Ch-Ua,
	// Upgrade-Insecure-Requests) against the UA; mismatches trigger
	// 403/503 even with a plausible UA. With the chrome backend the TLS
	// (JA3) and HTTP/2 fingerprints match too, and these headers are sent
	// in Chrome's order. Won't beat sophisticated JS challenges, but
	// removes the cheap blocks. Header order below is Chrome's navigation
	// order; the chrome backend reproduces it on the wire.
	headers := []header{
		{"sec-ch-ua", n.secChUA},
		{"sec-ch-ua-mobile", "?0"},
		{"sec-ch-ua-platform", `"macOS"`},
		{"upgrade-insecure-requests", "1"},
		{"user-agent", n.userAgent},
		{"accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		{"sec-fetch-site", "none"},
		{"sec-fetch-mode", "navigate"},
		{"sec-fetch-user", "?1"},
		{"sec-fetch-dest", "document"},
		{"accept-encoding", "gzip, deflate, br, zstd"},
		{"accept-language", "en-US,en;q=0.9"},
	}

	resp, err := n.rt.do(ctx, target, headers)
	if err != nil {
		// A certificate that fails verification fails the same way on every
		// retry, and Jina would fetch past the check. Not host-cached: see
		// ErrTLSCertificate.
		if errors.Is(err, ErrTLSCertificate) {
			return nil, &PermanentError{Err: fmt.Errorf("native: fetch: %w", err)}
		}
		// Distinguish dead-host (DNS, connection refused) from generic
		// transport errors. Dead hosts shouldn't trigger Jina fallback
		// (Jina can't reach a host that doesn't exist either) and they're
		// the right thing to cache for the longest — they're not coming
		// back in the next 15 minutes.
		if isHostUnreachable(err) {
			return nil, fmt.Errorf("native: fetch: %w: %w", ErrHostUnreachable, err)
		}
		return nil, fmt.Errorf("native: fetch: %w", err)
	}
	defer resp.body.Close()

	if resp.statusCode < 200 || resp.statusCode >= 300 {
		discardErrorBody(resp.body)
		return nil, n.statusFailure(resp)
	}

	// PDFs: extract locally (pure-Go); Fetch falls back to Jina. Detected
	// by Content-Type, or a .pdf URL when the server is vague about the type.
	if isPDFResponse(resp.contentType, target) {
		return n.extractPDF(target, resp.body)
	}

	// Other non-HTML content (images, octet-stream, …) can't be read as
	// HTML. Feeding its bytes to the parser blows x/net/html's nesting limit
	// ("open stack of elements exceeds 512 nodes"), and the failure is
	// deterministic — so fail permanently (no retry) with a clear reason
	// instead of re-downloading the file each attempt. Closing the body
	// unread avoids pulling down the whole file.
	if !isReadableContentType(resp.contentType) {
		return nil, &PermanentError{Err: fmt.Errorf(
			"native: unsupported content type %q (not HTML); URL: %s", resp.contentType, target)}
	}

	finalURL := resp.finalURL
	article, err := readability.FromReader(resp.body, finalURL)
	if tooLarge := overflow(resp.body); tooLarge != nil {
		return nil, &PermanentError{Err: fmt.Errorf("native: %s: %w", target, tooLarge)}
	}
	if err != nil {
		return nil, fmt.Errorf("native: readability: %w", err)
	}

	page, err := articleView(article, finalURL)
	if err != nil {
		return nil, fmt.Errorf("native: %w", err)
	}
	if err := n.judgePage(target, page); err != nil {
		if errors.Is(err, ErrDeadLink) {
			return nil, &PermanentError{Err: fmt.Errorf("native: %w", err)}
		}
		return nil, fmt.Errorf("native: %w", err)
	}

	// Render the cleaned-up HTML and convert to markdown.
	var htmlBuf bytes.Buffer
	if err := article.RenderHTML(&htmlBuf); err != nil {
		return nil, fmt.Errorf("native: render html: %w", err)
	}
	md, err := htmltomarkdown.ConvertString(htmlBuf.String())
	if err != nil {
		return nil, fmt.Errorf("native: html->md: %w", err)
	}
	md = strings.TrimSpace(md)
	if md == "" {
		return nil, fmt.Errorf("native: %w (empty markdown after conversion)", ErrLoginWall)
	}

	r := &Result{
		Markdown:    md,
		FinalURL:    finalURL.String(),
		ContentType: store.ContentTypeArticle,
		Title:       article.Title(),
		Author:      article.Byline(),
		Language:    article.Language(),
		Meta: map[string]any{
			"via":       "readability",
			"site":      article.SiteName(),
			"transport": n.rt.name(),
		},
	}
	if pt, err := article.PublishedTime(); err == nil && !pt.IsZero() {
		r.PublishedAt = &pt
	}
	return r, nil
}

// statusFailure classifies a non-2xx origin answer. Two statuses get the
// fetch policy's special handling before the generic retry rule applies:
//
//   - 403 and 503 are commonly Cloudflare / WAF bot blocks rather than
//     genuine "forbidden" or "server down" answers, and Jina's
//     infrastructure often gets through where we don't. Tagged ErrAntiBot
//     so Fetch falls back instead of giving up.
//   - 404 and 410 are deterministic "page is gone" answers: permanent, so
//     the doc fails on attempt 1 instead of burning the retry budget, and
//     never sent to Jina. With dead-link detection off they stay
//     retryable, which is what the kill switch restores.
func (n *Native) statusFailure(resp *fetchResponse) error {
	se := &HTTPStatusError{StatusCode: resp.statusCode, URL: resp.finalURL.String()}
	se.RetryAfter, _ = parseRetryAfter(resp.header, n.clock.now())
	switch resp.statusCode {
	case http.StatusForbidden, http.StatusServiceUnavailable:
		return fmt.Errorf("native: %w: %w", se, ErrAntiBot)
	case http.StatusNotFound, http.StatusGone:
		if n.deadLinkDetection {
			return &PermanentError{Err: fmt.Errorf("native: dead link (%w): %w", se, ErrDeadLink)}
		}
		return fmt.Errorf("native: %w", se)
	}
	return statusError(se.StatusCode, fmt.Errorf("native: %w", se))
}

// errorBodyDrain is how much of an error answer's body is read before it
// is closed. An error page that fits is read to its end, which lets the
// transport reuse the connection; a bigger one isn't worth downloading.
const errorBodyDrain = 4 << 10

// discardErrorBody reads and drops up to errorBodyDrain bytes of an error
// answer's body. Only connection reuse depends on it, so a failed read
// changes nothing and isn't reported.
func discardErrorBody(body io.Reader) {
	_, _ = io.CopyN(io.Discard, body, errorBodyDrain)
}

// errSiteLoginWall is the login wall a whole site sits behind: the request
// was redirected onto the site's own login page. It wraps ErrLoginWall, so
// it gets the same Jina fallback, but unlike a thin page it is host-wide.
var errSiteLoginWall = fmt.Errorf("site-wide %w", ErrLoginWall)

// pageView is what the page verdicts look at: a page's title and text,
// whether an article was found in it, and the URL the request settled on.
// finalURL is nil when the answer doesn't say where the request ended up,
// as with Jina's.
type pageView struct {
	title    string
	text     string
	found    bool
	finalURL *url.URL
}

// articleView is the page verdicts' view of a Readability result. Its text
// is empty when no article was found.
func articleView(article readability.Article, finalURL *url.URL) (pageView, error) {
	p := pageView{title: article.Title(), found: article.Node != nil, finalURL: finalURL}
	if p.found {
		var text strings.Builder
		if err := article.RenderText(&text); err != nil {
			return pageView{}, fmt.Errorf("render text: %w", err)
		}
		p.text = text.String()
	}
	return p, nil
}

// judgePage decides whether a page that came back is the page asked for.
// Every page goes through it, from the origin or from Jina, and the checks
// run in this order:
//
//  1. Dead link, with dead-link detection on: the request settled on the
//     site's homepage, or the title reads like a not-found page. First,
//     because a tombstone page is usually thin: the later checks would call
//     it a login wall and send it to Jina, which can't help with a page that
//     no longer exists.
//  2. Bot challenge: a challenge or block interstitial served with a 2xx.
//  3. Login wall by redirect: onto another site (this page only), or onto a
//     login path (the whole site, when the redirect stays on it).
//  4. No article found.
//  5. Thin: less than minArticleBytes of text.
//  6. A login-page title.
//
// It returns nil for a page that passes, and otherwise an error wrapping
// ErrDeadLink, ErrAntiBot, errSiteLoginWall or ErrLoginWall. A dead link is
// final; the caller makes it a PermanentError.
func (n *Native) judgePage(target string, p pageView) error {
	if n.deadLinkDetection {
		if reason := looksLikeSoft404(p, target); reason != "" {
			return fmt.Errorf("dead link (%s): %w", reason, ErrDeadLink)
		}
	}
	if reason := looksLikeChallenge(p); reason != "" {
		return fmt.Errorf("%w (bot challenge: %s)", ErrAntiBot, reason)
	}
	if reason, siteWide := looksLikeLoginWall(p, target); reason != "" {
		sentinel := ErrLoginWall
		if siteWide {
			sentinel = errSiteLoginWall
		}
		return fmt.Errorf("%w (%s)", sentinel, reason)
	}
	return nil
}

// looksLikeChallenge detects a bot-challenge or block page served with a
// 2xx status, by its title or, on a short page, by what it says. Returns
// the empty string when nothing looks like one; otherwise a short reason
// string for diagnostics. Only the page is judged: whatever served it may
// serve the next page normally, so the verdict is never host-wide.
func looksLikeChallenge(p pageView) string {
	if challengeTitleRE.MatchString(p.title) {
		return "title " + strconv.Quote(p.title)
	}
	if trimmedByteLen(p.text) > maxChallengeBytes {
		return ""
	}
	text := strings.ReplaceAll(strings.ToLower(p.text), "’", "'")
	for _, phrase := range challengePhrases {
		if strings.Contains(text, phrase) {
			return "page says " + strconv.Quote(phrase)
		}
	}
	return ""
}

// maxChallengeBytes bounds the pages challenge phrases are looked for in.
// Challenge and block pages are short; an article about bot checks can
// quote any of the phrases.
const maxChallengeBytes = 2 << 10

var (
	// challengeTitleRE matches the titles of challenge and block pages
	// (Cloudflare, Akamai, PerimeterX, Imperva, DDoS-Guard, Vercel, and the
	// "Are you a robot?" pages). Anchored at both ends, so an article whose
	// title starts the same way ("Just a moment of silence", "Access Denied:
	// A History of …") doesn't match.
	challengeTitleRE = regexp.MustCompile(`(?i)^\s*(?:` +
		`just a moment(?:\.\.\.|…)` +
		`|attention required! \| cloudflare` +
		`|access denied(?: \| .+ used cloudflare to restrict access)?` +
		`|please wait\.\.\. \| cloudflare` +
		`|access to this page has been denied\.?` +
		`|pardon our interruption` +
		`|ddos-guard` +
		`|vercel security checkpoint` +
		`|(?:.*[|:·•–—-]\s*)?are you a robot\?` +
		`)\s*$`)

	// challengePhrases are what challenge and block pages say, lowercased
	// and with straight apostrophes.
	challengePhrases = []string{
		"enable javascript and cookies to continue",
		"checking your browser before accessing",
		"needs to review the security of your connection before proceeding",
		"performing security verification",
		"verify you are human",
		"verifies you are not a bot",
		"please complete the security check to access",
		"why have i been blocked?",
		"you've been blocked by network security",
		"press & hold to confirm you are a human",
		"request unsuccessful. incapsula incident id",
		"please enable js and disable any ad blocker",
	}
)

// looksLikeLoginWall detects a login wall, or a page too thin to be the
// article:
//   - redirect to a login path segment (/login, /authwall, /signin, /signup)
//   - redirect to a different site
//   - missing article entirely
//   - extracted text shorter than minArticleBytes
//   - a login-page title (loginTitleRE)
//
// Returns the empty string when nothing looks suspicious; otherwise a
// short reason string for diagnostics. siteWide is set for a redirect onto
// the requested site's own login page, the one verdict here that speaks for
// every page on the host. The redirect checks run first so a thin login
// page still counts as the site-wide wall it is; they need p.finalURL.
func looksLikeLoginWall(p pageView, sourceURL string) (reason string, siteWide bool) {
	if source, err := url.Parse(sourceURL); err == nil && p.finalURL != nil {
		final := p.finalURL
		if final.Hostname() != "" && source.Hostname() != "" &&
			!sameSiteHost(final.Hostname(), source.Hostname()) {
			return "redirected to a different host: " + final.Hostname(), false
		}
		if loginPathRE.MatchString(final.Path) {
			return "redirected to a login/auth path: " + final.Path, final.Path != source.Path
		}
	}
	if !p.found {
		return "no article extracted", false
	}
	if trimmedByteLen(p.text) < minArticleBytes {
		return fmt.Sprintf("extracted text < %d bytes", minArticleBytes), false
	}
	if loginTitleRE.MatchString(p.title) {
		return "title looks like a login wall", false
	}
	return "", false
}

// sameSiteHost reports whether two hostnames name the same site, ignoring
// case and one leading "www.": example.com redirecting to www.example.com
// (or back) is canonicalization, not a login wall or a new site.
func sameSiteHost(a, b string) bool {
	norm := func(h string) string { return strings.TrimPrefix(strings.ToLower(h), "www.") }
	return norm(a) == norm(b)
}

var (
	// loginTitleRE matches login-page titles: the verb first, then the end
	// of the title, a separator, or "to"/"or" ("Log in", "Login | Help
	// Center", "Sign in to continue"); or the verb last, after a separator
	// ("Google Docs: Sign-in"). A title about logging in ("Sign In With
	// Apple: …", "How to log in to …") is neither.
	loginTitleRE = regexp.MustCompile(`(?i)` +
		`^\s*(?:sign[\s-]?in|log[\s-]?in|join\s+now|join\s+linkedin)\b(?:\s*$|\s*[|:·•–—-]|\s+(?:to|or)\s)` +
		`|[|:·•–—-]\s*(?:sign[\s-]?in|log[\s-]?in)\s*$`)
	// loginPathRE matches a whole login path segment, with or without a
	// file extension (/login, /uas/login, /Login.aspx), not a slug that
	// starts with one (/questions/1/login-with-oauth).
	loginPathRE = regexp.MustCompile(`(?i)/(?:login|authwall|signin|signup)(?:\.[a-z0-9]+)?(?:/|$)`)
)

// looksLikeSoft404 detects "soft 404s": pages that answer HTTP 200 but
// whose content says the resource is gone (CMS not-found templates,
// deleted articles redirecting to the site root). Two signals:
//
//   - the extracted title reads like a not-found page
//   - the request for a specific path settled on the site's homepage
//     (same site, www. or not; cross-site redirects are login-wall
//     territory). Needs p.finalURL.
//
// Returns the empty string when nothing looks dead; otherwise a short
// reason string for diagnostics.
func looksLikeSoft404(p pageView, sourceURL string) string {
	source, err := url.Parse(sourceURL)
	if final := p.finalURL; err == nil && final != nil &&
		sameSiteHost(source.Hostname(), final.Hostname()) &&
		strings.Trim(source.Path, "/") != "" &&
		strings.Trim(final.Path, "/") == "" &&
		final.RawQuery == "" {
		return "redirected to homepage"
	}

	if p.found && soft404TitleRE.MatchString(p.title) {
		return "title looks like a not-found page: " + p.title
	}
	return ""
}

// soft404TitleRE matches titles of common not-found templates: "404 …",
// "Error 404", a bare "Not Found", "Page not found", "This page doesn't
// exist", "… page has been removed", etc. Curly and straight apostrophes
// both appear in the wild.
var soft404TitleRE = regexp.MustCompile(`(?i)(` +
	`^\s*(error\s*)?404\b` +
	`|^\s*not found\s*$` +
	`|\b404\s+(error|not\s+found)\b` +
	`|page\s+(not\s+found|doesn[’']?t\s+exist|does\s+not\s+exist|can[’']?t\s+be\s+found|cannot\s+be\s+found|could\s+not\s+be\s+found|no\s+longer\s+(exists|available)|is\s+missing)` +
	`|page\b.{0,30}\b(has\s+been|was)\s+(removed|deleted)\b` +
	`|couldn[’']?t\s+find\s+(this|that|the)\s+page` +
	`)`)

// mediaType returns the lowercased media type from a Content-Type header,
// dropping any "; charset=..." parameters.
func mediaType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// isReadableContentType reports whether a Content-Type is something the HTML
// Readability path can handle. Empty/missing is allowed (many servers omit
// or mislabel it, and genuine HTML still parses); text/* and XHTML are
// allowed; everything explicit and non-text (image/*, octet-stream, …) is
// rejected so binary bodies never reach the HTML parser. text/event-stream
// is rejected too: it never ends, so reading it only ends at the size cap.
// (PDFs are handled separately, before this is consulted.)
func isReadableContentType(ct string) bool {
	mt := mediaType(ct)
	if mt == "text/event-stream" {
		return false
	}
	return mt == "" || strings.HasPrefix(mt, "text/") || mt == "application/xhtml+xml"
}

// isPDFResponse reports whether a response should be treated as a PDF: it
// either declares application/pdf, or the URL path ends in .pdf and the
// server was vague about the type (octet-stream or none).
func isPDFResponse(ct, rawURL string) bool {
	mt := mediaType(ct)
	if mt == "application/pdf" {
		return true
	}
	if mt == "" || mt == "application/octet-stream" {
		if u, err := url.Parse(rawURL); err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".pdf") {
			return true
		}
	}
	return false
}

// isHostUnreachable reports whether a transport error means the host
// itself is gone: its name doesn't exist, or it refuses connections or has
// no route. A resolver timeout or temporary DNS failure is transient, and
// "network is unreachable" (ENETUNREACH) describes our own connectivity, not
// the host; both stay retryable and are never cached. errno matching via
// errors.Is works through *url.Error → *net.OpError → *os.SyscallError on
// both backends.
func isHostUnreachable(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH)
}

// minArticleBytes is the thin-content threshold. It is measured in UTF-8
// bytes on purpose: about 500 Latin characters or about 170 CJK ones,
// which tracks how much a page says better than a rune count would.
// Counting runes would make short CJK pages three times as likely to be
// judged thin and sent to Jina's rate-limited budget.
const minArticleBytes = 500

// trimmedByteLen returns the UTF-8 byte length of s after trimming
// whitespace at both ends.
func trimmedByteLen(s string) int {
	return len(strings.TrimSpace(s))
}

// minPDFChars is the floor below which local extraction is treated as a
// miss (empty / garbled output) and we fall back to Jina.
const minPDFChars = 200

// errPDFUnreadable marks a PDF the local extractor couldn't read (too large,
// malformed, or garbled into too little text). Jina renders PDFs itself, so
// Fetch falls back to it.
var errPDFUnreadable = errors.New("pdf not extractable locally")

// extractPDF is tier 1 of PDF handling: pure-Go local extraction, no system
// dependency. body is the already-open response body for target; a PDF over
// the body cap is not read any further.
func (n *Native) extractPDF(target string, body io.Reader) (*Result, error) {
	data, err := io.ReadAll(body)
	switch {
	case errors.Is(err, ErrTooLarge):
		return nil, fmt.Errorf("native: %w: %w", errPDFUnreadable, err)
	case err != nil:
		return nil, fmt.Errorf("native: read pdf: %w", err)
	}
	text, err := extractPDFText(data)
	switch {
	case err != nil:
		return nil, fmt.Errorf("native: %w: %w", errPDFUnreadable, err)
	case len(text) < minPDFChars:
		return nil, fmt.Errorf("native: %w: only %d chars of text", errPDFUnreadable, len(text))
	}
	return &Result{
		Markdown:    text,
		FinalURL:    target,
		ContentType: store.ContentTypePDF,
		Meta:        map[string]any{"via": "pdf-local", "transport": n.rt.name()},
	}, nil
}

var (
	// errJinaRejected marks a Jina answer that is not the page: the target's
	// error status, Jina's CAPTCHA warning, or a page verdict (a challenge,
	// a login page, too little text). It is Jina's verdict about the target.
	errJinaRejected = errors.New("answer is not the page")

	// errJinaTargetTrouble marks a Jina answer carrying a target status that
	// a later attempt could change: a timeout, a rate limit, a server error,
	// or 404/410 with dead-link detection off. It is no verdict about the
	// target, and calling Jina again at once would only spend its budget,
	// so the job's backoff retries it.
	errJinaTargetTrouble = errors.New("target failed for now")
)

// targetStatusError is the status the target answered Jina with, as Jina's
// "Target URL returned error" warning reports it. It is deliberately not an
// *HTTPStatusError: that is Jina's own answer, whose 429 sets Jina's
// cooldown and whose Retry-After the job honors.
type targetStatusError struct {
	code int
	text string
}

func (e *targetStatusError) Error() string {
	if text := cmp.Or(e.text, http.StatusText(e.code)); text != "" {
		return fmt.Sprintf("target answered HTTP %d %s", e.code, text)
	}
	return fmt.Sprintf("target answered HTTP %d", e.code)
}

// tryJina is pass 2: fetch r.jina.ai/<url>. Every call goes through the
// shared limiter and cooldown (awaitJina). Transient failures are retried up
// to jinaAttempts times: 5xx and transport errors after a 2/4/8s backoff,
// 429s after the cooldown they set.
func (n *Native) tryJina(ctx context.Context, target string) (*Result, error) {
	var lastErr error
	for attempt := range jinaAttempts {
		if attempt > 0 && !isRateLimited(lastErr) {
			if err := n.clock.sleep(ctx, jinaBackoff(attempt)); err != nil {
				return nil, fmt.Errorf("jina: %w", err)
			}
		}
		if err := n.awaitJina(ctx); err != nil {
			return nil, err
		}

		res, err := n.jinaOnce(ctx, target)
		if err == nil {
			return res, nil
		}
		lastErr = err
		// Jina limits per client, so a 429 pauses every caller, not just
		// this one.
		var se *HTTPStatusError
		if errors.As(err, &se) && se.StatusCode == http.StatusTooManyRequests {
			n.jinaCooldown.extend(n.clock.now(), cmp.Or(se.RetryAfter, jinaBackoff(attempt+1)))
		}
		if !jinaRetryable(err) || ctx.Err() != nil {
			return nil, err
		}
		n.log.Info("jina retry", "err", err.Error(), "attempt", attempt+1)
	}
	return nil, lastErr
}

// jinaBackoff is the wait before retry number attempt (1-based): 2, 4, 8s.
func jinaBackoff(attempt int) time.Duration {
	return time.Duration(1<<attempt) * time.Second
}

// awaitJina paces a Jina call through the shared limiter and cooldown (see
// pace), sitting out a cooldown a 429 left when it ends within
// maxInlineJinaWait. A longer one fails at once, without a request, with a
// retryable 429 carrying the time left.
func (n *Native) awaitJina(ctx context.Context) error {
	left, err := pace(ctx, n.jinaLimiter, &n.jinaCooldown, n.clock, maxInlineJinaWait)
	if err != nil {
		return fmt.Errorf("jina: %w", err)
	}
	if left > 0 {
		se := &HTTPStatusError{StatusCode: http.StatusTooManyRequests, URL: n.jinaBaseURL, RetryAfter: left}
		return fmt.Errorf("jina: not sent, rate-limit cooldown has %s left: %w", left.Round(time.Second), se)
	}
	return nil
}

// jinaOnce makes one Jina request and parses the answer.
func (n *Native) jinaOnce(ctx context.Context, target string) (*Result, error) {
	headers := []header{
		{"user-agent", n.userAgent},
		{"accept", "text/plain"},
	}
	if n.jinaAPIKey != "" {
		headers = append(headers, header{"authorization", "Bearer " + n.jinaAPIKey})
	}
	resp, err := n.rt.do(ctx, n.jinaBaseURL+target, headers)
	if err != nil {
		return nil, fmt.Errorf("jina: %w", err)
	}
	defer resp.body.Close()

	if resp.statusCode < 200 || resp.statusCode >= 300 {
		discardErrorBody(resp.body)
		se := &HTTPStatusError{StatusCode: resp.statusCode, URL: resp.finalURL.String()}
		se.RetryAfter, _ = parseRetryAfter(resp.header, n.clock.now())
		return nil, fmt.Errorf("jina: %w", se)
	}
	body, err := io.ReadAll(resp.body)
	if err != nil {
		// A body cut off mid-transfer is a transport failure, never a short
		// article. Past the size cap it is ErrTooLarge.
		return nil, fmt.Errorf("jina: read body: %w", err)
	}

	parsed := parseJina(string(body))
	if err := n.judgeJinaAnswer(target, parsed); err != nil {
		return nil, fmt.Errorf("jina: %w", err)
	}
	result := &Result{
		Markdown: parsed.body,
		// Not URL Source: Jina echoes the request there, re-encoded, even
		// after a redirect, so it names no URL the request settled on.
		FinalURL:    target,
		ContentType: store.ContentTypeArticle,
		Title:       parsed.title,
		Meta:        map[string]any{"via": "jina"},
	}
	if parsed.published != "" {
		if pt, err := time.Parse(time.RFC3339, parsed.published); err == nil {
			result.PublishedAt = &pt
		}
	}
	return result, nil
}

// judgeJinaAnswer decides whether a 2xx Jina answer is the page. Jina
// answers 200 whatever the target served and reports trouble only in
// Warning lines, so the answer is judged in three steps:
//
//  1. The status the target gave Jina, from a "Target URL returned error"
//     warning (targetStatusVerdict).
//  2. Jina's CAPTCHA warning: an anti-bot rejection.
//  3. The page verdicts every page gets (judgePage), without a final URL.
//
// Jina's other warnings (iframes, shadow DOM, a cached snapshot, a page
// maybe not fully loaded) are informational. A dead link is returned as
// is; every other verdict wraps errJinaRejected.
func (n *Native) judgeJinaAnswer(target string, a jinaParsed) error {
	for _, w := range a.warnings {
		if m := jinaTargetErrorRE.FindStringSubmatch(w); m != nil {
			code, _ := strconv.Atoi(m[1]) // three digits, by the pattern
			return n.targetStatusVerdict(&targetStatusError{code: code, text: m[2]})
		}
	}
	if slices.ContainsFunc(a.warnings, jinaCaptchaRE.MatchString) {
		return fmt.Errorf("%w: %w (bot challenge: jina reports a CAPTCHA)", errJinaRejected, ErrAntiBot)
	}
	err := n.judgePage(target, pageView{title: a.title, text: a.body, found: true})
	if err == nil || errors.Is(err, ErrDeadLink) {
		return err
	}
	return fmt.Errorf("%w: %w", errJinaRejected, err)
}

// targetStatusVerdict classifies the status the target answered Jina with,
// the way statusFailure classifies the origin's own:
//
//   - 404/410: a dead link, with dead-link detection on.
//   - 403/503: an anti-bot rejection.
//   - A status a retry could change (408, 429, 5xx other than 503), and
//     404/410 with detection off: errJinaTargetTrouble.
//   - Any other status: a rejection.
func (n *Native) targetStatusVerdict(se *targetStatusError) error {
	switch se.code {
	case http.StatusNotFound, http.StatusGone:
		if n.deadLinkDetection {
			return fmt.Errorf("dead link (%w): %w", se, ErrDeadLink)
		}
		return fmt.Errorf("%w: %w", errJinaTargetTrouble, se)
	case http.StatusForbidden, http.StatusServiceUnavailable:
		return fmt.Errorf("%w: %w: %w", errJinaRejected, se, ErrAntiBot)
	}
	if retryableStatus(se.code) {
		return fmt.Errorf("%w: %w", errJinaTargetTrouble, se)
	}
	return fmt.Errorf("%w: %w", errJinaRejected, se)
}

var (
	// jinaTargetErrorRE reads the target's status from a Jina warning such
	// as "Target URL returned error 404: Not Found".
	jinaTargetErrorRE = regexp.MustCompile(`(?i)target url returned error (\d{3})\b(?::\s*(.*))?`)
	// jinaCaptchaRE matches Jina's warning that the target asked for a
	// CAPTCHA: "This page maybe requiring CAPTCHA, please make sure you are
	// authorized to access this page."
	jinaCaptchaRE = regexp.MustCompile(`(?i)\bcaptcha\b`)
)

// jinaRetryable reports whether a failed Jina call is worth another attempt
// within the same fetch: transport errors and Jina's retryable statuses are.
// A judged answer is not (another call would bring the same page back, and
// a target that failed for now is left to the job's backoff), nor are an
// oversized body and Jina's deterministic statuses.
func jinaRetryable(err error) bool {
	if errors.Is(err, errJinaRejected) || errors.Is(err, errJinaTargetTrouble) ||
		errors.Is(err, ErrDeadLink) || errors.Is(err, ErrTooLarge) {
		return false
	}
	var se *HTTPStatusError
	if errors.As(err, &se) {
		return retryableStatus(se.StatusCode)
	}
	return true
}

func isRateLimited(err error) bool {
	var se *HTTPStatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusTooManyRequests
}

// jinaHeaderRE matches one "Name: value" line of a Jina Reader header block.
var jinaHeaderRE = regexp.MustCompile(`^([A-Z][A-Za-z ]+):\s*(.*)$`)

// jinaParsed is what curio reads from a Jina Reader answer.
type jinaParsed struct {
	title     string
	published string
	warnings  []string // every Warning line, in order
	body      string
}

// parseJina extracts the title, published time, warnings and body from a
// Jina Reader answer. Format:
//
//	Title: ...
//
//	URL Source: ...
//	Published Time: ...
//
//	Warning: ...
//	Warning: ...
//
//	Markdown Content:
//	<body>
//
// URL Source is not read: it echoes the request (see jinaOnce). When the
// "Markdown Content:" marker is missing, the header ends at its first line
// that is neither a header line nor blank.
func parseJina(text string) jinaParsed {
	var (
		out       jinaParsed
		sawHeader bool
	)
	lines := strings.Split(text, "\n")
	i := 0
header:
	for ; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "Markdown Content:" {
			i++
			break
		}
		switch m := jinaHeaderRE.FindStringSubmatch(lines[i]); {
		case len(m) == 3:
			value := strings.TrimSpace(m[2])
			switch strings.TrimSpace(m[1]) {
			case "Title":
				out.title = value
			case "Published Time":
				out.published = value
			case "Warning":
				out.warnings = append(out.warnings, value)
			}
			sawHeader = true
		case line == "":
			// A blank line inside the header is fine.
		case sawHeader:
			// The header ended without a "Markdown Content:" marker.
			break header
		}
	}
	out.body = strings.TrimSpace(strings.Join(lines[i:], "\n"))
	return out
}
