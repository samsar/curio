package fetcher

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
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
	"unicode"
	"unicode/utf8"

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
	jinaSites         *sitePacer
	jinaHealth        *healthTracker
	deadLinkDetection bool
	log               *slog.Logger
	hostCache         *hostFailureCache
	originSlots       *hostGate
	clock             clock
}

// NativeOptions configures Native. Zero-value fields use defaults.
type NativeOptions struct {
	Timeout time.Duration
	// UserAgent overrides the User-Agent the Chrome profile implies, on
	// origin requests only: Jina requests identify as curio (jinaUserAgent).
	// It is sent as is; one that names a different Chrome version than the
	// profile logs a warning, since bot checks compare the two.
	UserAgent    string
	JinaFallback bool
	JinaBaseURL  string // default https://r.jina.ai/
	// JinaAPIKey is sent as a bearer token and raises Jina's rate limit.
	// Empty falls back to the CURIO_JINA_API_KEY environment variable.
	JinaAPIKey string
	// JinaSiteRequestsPerMinute caps the Jina requests for pages of one
	// site (siteOf) a minute, key or not: Jina's abuse check blocks a
	// domain's keyless reads after a burst. Zero or less means
	// DefaultJinaSiteRequestsPerMinute.
	JinaSiteRequestsPerMinute int
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
	// Longer ones defer the fetch until the cooldown ends (awaitJina).
	maxInlineJinaWait = 30 * time.Second
	// jinaHoldReason is what a fetch deferred for Jina waits for.
	jinaHoldReason = "the pause on Jina Reader calls to end"
	// jinaSiteBlockDefault is how long Jina Reader's block of a site is
	// taken to last when its answer gives no end curio can read: the blocks
	// observed ended about an hour after they began.
	jinaSiteBlockDefault = time.Hour
	// minJinaSiteBlock and maxJinaSiteBlock bound the pause a block sets. A
	// block always pauses its site a minute at least, since Jina has just
	// refused it, so an end already past (a slow clock) can't send the
	// site's pages straight back; and a day at most, the longest a job
	// waits (store.DeferralBudget): one block named 2039.
	minJinaSiteBlock = time.Minute
	maxJinaSiteBlock = store.DeferralBudget
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
	perSite := opts.JinaSiteRequestsPerMinute
	if perSite <= 0 {
		perSite = DefaultJinaSiteRequestsPerMinute
	}

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
		jinaSites:         newSitePacer(time.Minute / time.Duration(perSite)),
		jinaHealth:        newHealthTracker("jina", opts.Log),
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

	// A host whose failure spoke for all its pages (settle) is cached, and
	// its other pages skip the origin request whose answer is known
	// (pastCachedHost). Three reasons this matters in practice:
	//   1. ~17% of import failures concentrate in <15 hosts (LinkedIn,
	//      NYT, Inc.com, dribbble, etc.) — same fail signature every time.
	//   2. Each origin failure costs ~30s of HTTP timeout + retries.
	//   3. A host that just blocked curio blocks its next request too:
	//      asking again for every page spends time and invites a longer
	//      block.
	// Cache is in-memory only — survives goroutines, not daemon restarts.
	// That's fine: it re-warms within minutes of resuming.
	host := hostOf(target)
	if hit, ok := n.hostCache.Get(host, n.clock.now()); ok {
		return n.pastCachedHost(ctx, target, host, hit, hit.err())
	}

	release, err := n.originSlots.acquire(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("native: wait for a request slot on %s: %w", host, err)
	}
	defer release()
	// A verdict cached while this fetch queued for the host applies to it
	// too; don't send the request that verdict says is hopeless, nor hold
	// the slot past it.
	if hit, ok := n.hostCache.Get(host, n.clock.now()); ok {
		release()
		return n.pastCachedHost(ctx, target, host, hit, hit.err())
	}

	// Pass 1: direct fetch + Readability (or local PDF extraction).
	res, originErr := n.tryReadability(ctx, target)
	if originErr == nil {
		return res, nil
	}
	// Dead links, another site's login page, oversized or unsupported
	// bodies, deterministic statuses: final, and nothing Jina can fix.
	var pe *PermanentError
	if errors.As(originErr, &pe) {
		return nil, originErr
	}
	// A redirect can end on a host whose verdict is already cached. Its
	// entry holds this page as it holds the host's own (the checks above
	// only cover the requested host): without this one, every retry would
	// ask the origin again and write the entry anew.
	if _, answering, ok := hostVerdict(originErr, host); ok && answering != host {
		if hit, ok := n.hostCache.Get(answering, n.clock.now()); ok {
			release()
			return n.pastCachedHost(ctx, target, answering, hit, originErr)
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
	err = &jinaFallbackError{jina: jinaErr, origin: originErr}
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

// jinaFallbackError is a fetch that failed on the origin, then on Jina.
// Jina's failure leads the chain: it is the path a retry now depends on, so
// errors.As finds its status and Retry-After first, while errors.Is still
// matches the origin's sentinel. Kept apart, the two tell FailureCause
// which of them spoke for the target.
type jinaFallbackError struct {
	jina, origin error
}

func (e *jinaFallbackError) Error() string {
	return e.jina.Error() + " (after " + e.origin.Error() + ")"
}

func (e *jinaFallbackError) Unwrap() []error { return []error{e.jina, e.origin} }

// jinaCanHelp reports whether an origin failure is one Jina might get past:
//
//   - ErrLoginWall: the page came back but was paywalled or thin
//   - ErrAntiBot: 403/503 from the origin, likely a WAF block, or a
//     challenge or 403/503 error page served with a 2xx. A 403/503 from the
//     homepage or another site's landing page a redirect settled on is a
//     dead link instead, with dead-link detection on (statusFailure).
//   - errPDFUnreadable: a PDF the local extractor couldn't read; Jina
//     renders PDFs itself
//
// Everything else (404, other statuses, DNS failures, timeouts, an error
// page naming a server error) goes without Jina: it can't conjure a page
// that doesn't exist, and spending its rate limit on dead links gets us
// 429'd on the calls that would benefit. Neither does a redirect onto
// another site's login page, although it is an ErrLoginWall, whether that
// page answered 2xx, 403 or 503: it comes as a PermanentError, which Fetch
// returns before asking.
func jinaCanHelp(err error) bool {
	return errors.Is(err, ErrLoginWall) || errors.Is(err, ErrAntiBot) || errors.Is(err, errPDFUnreadable)
}

// jinaAnswered reports whether a failed Jina call is a verdict about the
// target: Jina fetched it and its answer is not the page (errJinaRejected),
// or Jina refused it (errJinaRefused). Rate limits, outages, timeouts and
// transport errors are trouble on Jina's side, 401/402 and a 403 that names
// no target are about our client (our account, or r.jina.ai refusing
// curio), Jina's block of the target's site (errJinaSiteBlocked) ends at a
// time it names, and a transient status the target gave Jina is the
// target's trouble for now; none of them is a verdict. The target's own 403
// comes in a warning, as a targetStatusError.
func jinaAnswered(err error) bool {
	return errors.Is(err, errJinaRejected) || errors.Is(err, errJinaRefused)
}

// settle decides what an origin failure that no extraction path could
// rescue becomes; err is what Fetch returns for it.
//
//   - A host-wide verdict is cached under the host that gave it, quoting
//     the origin's own answer, and err stays retryable: the first failure
//     for a host gets one more real attempt, and later URLs on the host
//     skip the origin while the entry lasts (pastCachedHost).
//   - A page-level verdict Jina could have helped with is final. Every
//     extraction path has answered, so a retry would only repeat the origin
//     and Jina calls (up to four Jina requests each), which is the budget
//     the fallback policy protects.
//   - Anything else (a transient status, a transport error) is returned as
//     is.
//
// Only originErr is judged, and only its text is cached: Jina's verdicts
// are about the one page it was asked for, and never write the cache nor
// reach the host's other pages.
func (n *Native) settle(requestedHost string, originErr, err error) error {
	if kind, host, ok := hostVerdict(originErr, requestedHost); ok {
		n.hostCache.Put(host, kind, originErr.Error(), n.clock.now())
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
// Everything else is about one page (thin content, a redirect onto another
// site's login or landing page, a bot challenge or an error page recognized
// in a page's content) or transient, and caching it would fail healthy URLs
// without a request.
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

// pastCachedHost fetches target, a page on host or redirected onto it,
// past host's fresh cache entry hit. origin is the page's own origin
// failure, or hit's error when the origin wasn't asked. The entry says what
// the origin would answer, so:
//
//   - An unreachable host fails the page for good, from the entry: Jina
//     can't reach the host either, and a name that doesn't resolve isn't
//     back within the entry's life.
//   - With Jina off, an anti-bot or login-wall host defers the page until
//     the entry expires, when the origin is asked again. Nothing was learned
//     about the page.
//   - Otherwise Jina is asked, and its answer decides the page alone, as a
//     fallback's does: a verdict about the target (a rejection, a refusal,
//     a dead link, a body over the cap) is final, and Jina's own trouble,
//     the target's for now, or a hold stays retryable or deferred. Nothing
//     is cached or refreshed: the entry spoke for the host once, and Jina
//     speaks for one page.
//
// The caller holds no origin slot.
func (n *Native) pastCachedHost(ctx context.Context, target, host string, hit hostCacheEntry, origin error) (*Result, error) {
	cached := hit.err()
	switch {
	case !jinaCanHelp(cached):
		n.log.Info("fast-fail from host cache", "url", target, "host", host, "kind", hit.kind.String(),
			"expires_in", hit.until.Sub(n.clock.now()).Round(time.Second).String())
		return nil, &PermanentError{Err: cached}
	case !n.jinaFallback:
		return nil, &DeferError{Until: hit.until, Reason: hit.kind.waitReason(host), Err: cached}
	}

	n.log.Info("host cache: asking jina without the origin", "url", target, "host", host,
		"kind", hit.kind.String(), "expires_in", hit.until.Sub(n.clock.now()).Round(time.Second).String())
	res, jinaErr := n.tryJina(ctx, target)
	if jinaErr == nil {
		res.Meta["host_cache"] = hit.kind.String()
		return res, nil
	}
	err := &jinaFallbackError{jina: jinaErr, origin: origin}
	if jinaAnswered(jinaErr) || errors.Is(jinaErr, ErrDeadLink) || errors.Is(jinaErr, ErrTooLarge) {
		return nil, &PermanentError{Err: err}
	}
	return nil, err
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
		return nil, n.statusFailure(target, resp)
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
			"native: %w content type %q (not HTML); URL: %s", ErrUnsupported, resp.contentType, target)}
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
		// Final, and nothing Jina can fix: it follows the same redirect.
		if errors.Is(err, ErrDeadLink) || errors.Is(err, errOffsiteLoginWall) {
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

// statusFailure classifies a non-2xx origin answer to a request for target.
// Two statuses get the fetch policy's special handling before the generic
// retry rule applies:
//
//   - 403 and 503 are commonly Cloudflare / WAF bot blocks rather than
//     genuine "forbidden" or "server down" answers, and Jina's
//     infrastructure often gets through where we don't. Tagged ErrAntiBot
//     so Fetch falls back instead of giving up. A redirect onto the
//     homepage or another site's landing or login page is judged first
//     (judgeRedirect): its verdict is final, since Jina would follow the
//     same redirect to the same page, and caches neither host, since it is
//     about this URL alone. It keeps the status in its chain.
//   - 404 and 410 are deterministic "page is gone" answers: permanent, so
//     the doc fails on attempt 1 instead of burning the retry budget, and
//     never sent to Jina. With dead-link detection off they stay
//     retryable, which is what the kill switch restores.
func (n *Native) statusFailure(target string, resp *fetchResponse) error {
	se := &HTTPStatusError{StatusCode: resp.statusCode, URL: resp.finalURL.String()}
	se.RetryAfter, _ = parseRetryAfter(resp.header, n.clock.now())
	switch resp.statusCode {
	case http.StatusForbidden, http.StatusServiceUnavailable:
		if err := n.judgeRedirect(target, resp.finalURL); err != nil {
			return &PermanentError{Err: fmt.Errorf("native: %w: %w", se, err)}
		}
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

var (
	// errSiteLoginWall is the login wall a whole site sits behind: the
	// request was redirected onto the site's own login page. It wraps
	// ErrLoginWall, so it gets the same Jina fallback, but unlike a thin page
	// it is host-wide.
	errSiteLoginWall = fmt.Errorf("site-wide %w", ErrLoginWall)

	// errOffsiteLoginWall is a redirect onto another site's login page, an
	// account or SSO host (accounts.google.com, id.atlassian.com). It wraps
	// ErrLoginWall but is final: Jina has no session either and follows the
	// same redirect. It says nothing about the rest of either host, so it is
	// never cached.
	errOffsiteLoginWall = fmt.Errorf("offsite %w", ErrLoginWall)
)

// pageView is what the page verdicts look at: a page's title and text,
// whether an article was found in it, and the URL the request settled on.
// The text is the article's plain text from the origin (articleView), and
// the markdown body of a Jina answer, link URLs and all. finalURL is nil
// when the answer doesn't say where the request ended up, as with Jina's.
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
//     site's homepage or on another site's landing page, the title reads
//     like a not-found page, or the page's opening holds a not-found notice
//     or a parked domain's (pageText). First, because a tombstone page is
//     usually thin: the later checks would call it a login wall and send it
//     to Jina, which can't help with a page that no longer exists.
//  2. Bot challenge: a challenge or block interstitial served with a 2xx.
//  3. Error page: an error page served with a 2xx, judged like the status
//     it names.
//  4. Login wall by redirect: onto another site's login page (final for
//     this URL), or onto the site's own login path (the whole site, when the
//     redirect changes the path).
//  5. No article found.
//  6. Thin: less than minArticleBytes of text.
//  7. A login-page title.
//  8. A sign-in form: a short page whose text opens with a password field
//     and a sign-in line.
//
// A redirect onto another site that none of these flags is judged like any
// page, and stored when it passes.
//
// It returns nil for a page that passes, and otherwise an error wrapping
// ErrDeadLink, ErrAntiBot, errServerErrorPage, errOffsiteLoginWall,
// errSiteLoginWall or ErrLoginWall. A dead link and an offsite login wall
// are final; the caller makes them a PermanentError.
func (n *Native) judgePage(target string, p pageView) error {
	text := readPageText(p.text)
	if n.deadLinkDetection {
		if reason := looksLikeSoft404(p, target); reason != "" {
			return deadLink(reason)
		}
		if reason := text.notFoundNotice(); reason != "" {
			return deadLink(reason)
		}
		if reason := text.parkedDomain(target); reason != "" {
			return deadLink(reason)
		}
	}
	if reason := looksLikeChallenge(p); reason != "" {
		return fmt.Errorf("%w (bot challenge: %s)", ErrAntiBot, reason)
	}
	if code, reason := looksLikeErrorPage(p); reason != "" {
		return errorPageVerdict(code, reason)
	}
	if reason, scope := looksLikeLoginWall(p, target); reason != "" {
		return loginWall(reason, scope)
	}
	if reason := text.signInForm(); reason != "" {
		return loginWall(reason, loginWallPage)
	}
	return nil
}

// judgeRedirect gives judgePage's redirect verdicts on where a request for
// target settled, for an answer whose page is not read: with no title and
// no article, only the URL rules can hold. In judgePage's order:
//
//  1. Dead link, with dead-link detection on: the request settled on the
//     site's homepage or on another site's landing page.
//  2. A login page on another site. The other login-wall verdicts are left
//     to the status: a page-level one is about content this answer lacks,
//     and a login path on the requested site stays anti-bot, host-wide and
//     Jina-eligible like the site-wide wall it is on a 2xx.
//
// It returns nil when neither holds, as for a request that wasn't
// redirected.
func (n *Native) judgeRedirect(target string, final *url.URL) error {
	p := pageView{finalURL: final}
	if n.deadLinkDetection {
		if reason := looksLikeSoft404(p, target); reason != "" {
			return deadLink(reason)
		}
	}
	if reason, scope := looksLikeLoginWall(p, target); scope == loginWallOffsite {
		return loginWall(reason, scope)
	}
	return nil
}

// deadLink is the dead-link verdict, for the reason a soft-404 rule gives.
func deadLink(reason string) error {
	return fmt.Errorf("dead link (%s): %w", reason, ErrDeadLink)
}

// loginWall is the login-wall verdict of scope, for the reason
// looksLikeLoginWall gives.
func loginWall(reason string, scope loginWallScope) error {
	return fmt.Errorf("%w (%s)", scope.sentinel(), reason)
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
		// Google's "unusual traffic" page (/sorry/), titled with the URL asked for.
		"our systems have detected unusual traffic from your computer network",
		// Fastly's "Client Challenge" page.
		"a required part of this site couldn't load",
	}
)

// looksLikeErrorPage detects an error page served with a 2xx, or whose
// status the answer doesn't carry (Jina's, without its target-status
// warning): by an anchored title (errorPageTitleRE) or, on a short page, by
// an error body (errorPagePhrases). It returns the HTTP status the page
// names and a short reason string for diagnostics, or the empty string when
// nothing looks like one.
func looksLikeErrorPage(p pageView) (code int, reason string) {
	if m := errorPageTitleRE.FindStringSubmatch(p.title); m != nil {
		return errorPageStatus(m), "title " + strconv.Quote(p.title)
	}
	if trimmedByteLen(p.text) > maxChallengeBytes {
		return 0, ""
	}
	text := strings.ToLower(p.text)
	for _, e := range errorPagePhrases {
		if strings.Contains(text, e.phrase) {
			return e.code, "page says " + strconv.Quote(e.phrase)
		}
	}
	return 0, ""
}

// errServerErrorPage marks an error page naming a server error other than
// 503: any other 5xx a title names (500, 502, 504, Cloudflare's 52x and
// 530). Like the status it names, it is the server's trouble for now:
// retried with the job's backoff, never cached, and never sent to Jina,
// which would reach the same failing server.
var errServerErrorPage = errors.New("server error page")

// errorPageVerdict is what an error page means, by the status it names, the
// way statusFailure reads a real status: 403 and 503 are anti-bot (page-level
// here, since no status came with the page), any other a server error.
func errorPageVerdict(code int, reason string) error {
	if code == http.StatusForbidden || code == http.StatusServiceUnavailable {
		return fmt.Errorf("%w (error page: %s)", ErrAntiBot, reason)
	}
	return fmt.Errorf("%w (%s)", errServerErrorPage, reason)
}

// errorPageStatus is the status an errorPageTitleRE match names: the code
// the title gives, or else the one its phrase means.
func errorPageStatus(m []string) int {
	for _, name := range []string{"code", "errorCode", "cfCode"} {
		if s := m[errorPageTitleRE.SubexpIndex(name)]; s != "" {
			code, _ := strconv.Atoi(s) // three digits, by the pattern
			return code
		}
	}
	switch phrase := strings.ToLower(m[errorPageTitleRE.SubexpIndex("phrase")]); {
	case strings.HasSuffix(phrase, "forbidden"):
		return http.StatusForbidden
	case phrase == "internal server error":
		return http.StatusInternalServerError
	case phrase == "bad gateway":
		return http.StatusBadGateway
	case strings.HasPrefix(phrase, "gateway"):
		return http.StatusGatewayTimeout
	}
	// "Service (temporarily) unavailable", or no phrase: IBM's "The page you
	// requested cannot be displayed", its 503 notice.
	return http.StatusServiceUnavailable
}

var (
	// errorPageTitleRE matches the titles of server and CDN error pages.
	// Anchored at both ends: the error opens the title, optionally after
	// "Error" and a status code, and either ends it or is followed by a
	// spaced separator and a site name ("Access forbidden : Stanford
	// University", "403 | Forbidden | Axway"). An article about an error
	// ("How to fix a 403 Forbidden error", "Service Unavailable: lessons from
	// our outage") doesn't match. A 404 or 410 title is soft404TitleRE's.
	errorPageTitleRE = regexp.MustCompile(`(?i)^\s*(?:` +
		`(?:(?:error\s*)?(?P<code>403|50[0234])\s*(?:[|:–—-]\s*)?)?` +
		`(?P<phrase>forbidden|access forbidden|internal server error|bad gateway|service (?:temporarily )?unavailable|gateway time-?out)` +
		`|error\s*(?P<errorCode>403|50[0234])` +
		// Cloudflare: "example.com | 526: Invalid SSL certificate".
		`|[a-z0-9-]+(?:\.[a-z0-9-]+)+\s*\|\s*(?P<cfCode>5\d\d):\s+[^|]+` +
		// IBM: "IBM notice: The page you requested cannot be displayed".
		`|(?:[^|:]{1,30}:\s*)?(?:the )?page you requested cannot be displayed\.?` +
		`)(?:\s+[|:·•–—-]\s+[^|:]{1,60})?\s*$`)

	// errorPagePhrases are error bodies recognized on short pages, lowercased,
	// with the status they mean: S3's and Google Cloud Storage's XML error.
	errorPagePhrases = []struct {
		phrase string
		code   int
	}{
		{"<code>accessdenied</code>", http.StatusForbidden},
	}
)

// loginWallScope is how far a login-wall verdict reaches.
type loginWallScope int

const (
	// loginWallPage is about this page alone: no article, too little text,
	// a login-page title. Jina may get past it.
	loginWallPage loginWallScope = iota
	// loginWallSite is a redirect onto the requested site's own login page,
	// the one verdict here that speaks for every page on the host.
	loginWallSite
	// loginWallOffsite is a redirect onto another site's login page. Final
	// for this URL, and about neither host (errOffsiteLoginWall).
	loginWallOffsite
)

// sentinel is the error a login wall of scope s wraps.
func (s loginWallScope) sentinel() error {
	switch s {
	case loginWallPage:
		return ErrLoginWall
	case loginWallSite:
		return errSiteLoginWall
	case loginWallOffsite:
		return errOffsiteLoginWall
	}
	return fmt.Errorf("unknown login wall scope %d: %w", int(s), ErrLoginWall)
}

// looksLikeLoginWall detects a login wall, or a page too thin to be the
// article:
//   - redirect onto another site's login page: a login path segment
//     (loginPathRE) or a login-page title (loginTitleRE)
//   - redirect onto a login path segment on the same site (/login,
//     /authwall, /signin, /signup)
//   - missing article entirely
//   - extracted text shorter than minArticleBytes
//   - a login-page title
//
// Returns the empty string when nothing looks suspicious; otherwise a
// short reason string for diagnostics and the verdict's scope. The redirect
// checks run first, so a thin login page still counts as the wall it is;
// they need p.finalURL. A redirect onto another site that is no login page
// is none of this function's business: it is judged like any page.
func looksLikeLoginWall(p pageView, sourceURL string) (reason string, scope loginWallScope) {
	if source, err := url.Parse(sourceURL); err == nil && p.finalURL != nil {
		final := p.finalURL
		loginPath := loginPathRE.MatchString(final.Path)
		switch {
		case crossSite(source, final):
			// Never site-wide: that would cache the requested host for
			// sending its links to an SSO host.
			if loginPath || loginTitleRE.MatchString(p.title) {
				return "redirected onto another site's login page: " + final.Host + final.Path, loginWallOffsite
			}
		case loginPath:
			scope = loginWallPage
			if final.Path != source.Path {
				scope = loginWallSite
			}
			return "redirected to a login/auth path: " + final.Path, scope
		}
	}
	if !p.found {
		return "no article extracted", loginWallPage
	}
	if trimmedByteLen(p.text) < minArticleBytes {
		return fmt.Sprintf("extracted text < %d bytes", minArticleBytes), loginWallPage
	}
	if loginTitleRE.MatchString(p.title) {
		return "title looks like a login wall", loginWallPage
	}
	return "", loginWallPage
}

// sameSiteHost reports whether two hostnames name the same site, ignoring
// case and one leading "www.": example.com redirecting to www.example.com
// (or back) is canonicalization, not a login wall or a new site.
func sameSiteHost(a, b string) bool {
	norm := func(h string) string { return strings.TrimPrefix(strings.ToLower(h), "www.") }
	return norm(a) == norm(b)
}

// crossSite reports whether a request for source settled on another site.
func crossSite(source, final *url.URL) bool {
	return source.Hostname() != "" && final.Hostname() != "" &&
		!sameSiteHost(source.Hostname(), final.Hostname())
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
// deleted articles redirecting to the site root, retired sites sending
// every old URL to their successor's front door). It reads the URLs and
// the title; judgePage then reads the page's text for a not-found notice
// or a parked domain (pageText), which judgeRedirect, with no page to
// read, never does. Three signals:
//
//   - the request for a page settled on the site's homepage (same site,
//     www. or not). A source path that is only an index document names no
//     page: /index.htm → / is the homepage, canonicalized. Needs
//     p.finalURL.
//   - the request settled on another site's landing page
//     (looksLikeLandingPage). Needs p.finalURL.
//   - the title is a not-found template (soft404TitleRE), whether or not
//     an article was found: a not-found page whose body Readability can't
//     extract, such as an app shell, is still dead, not a login wall for
//     Jina to try. Its spaces are made plain first: the rule's \s is
//     ASCII, and some sites pad their separators with no-break spaces.
//
// Returns the empty string when nothing looks dead; otherwise a short
// reason string for diagnostics.
func looksLikeSoft404(p pageView, sourceURL string) string {
	if source, err := url.Parse(sourceURL); err == nil && p.finalURL != nil {
		final := p.finalURL
		if sameSiteHost(source.Hostname(), final.Hostname()) &&
			len(pageSegments(source.Path)) > 0 &&
			strings.Trim(final.Path, "/") == "" &&
			final.RawQuery == "" {
			return "redirected to homepage"
		}
		if looksLikeLandingPage(source, final, p.title) {
			return "redirected to another site's landing page: " + final.Host + final.Path
		}
	}

	if soft404TitleRE.MatchString(strings.Join(strings.Fields(p.title), " ")) {
		return "title looks like a not-found page: " + p.title
	}
	return ""
}

// looksLikeLandingPage reports whether a request for source settled on
// another site's landing page, a homepage or a section such as /articles/,
// instead of on a page of its own: how a retired site sends every old URL
// to its successor. All of these must hold:
//
//   - final is on another site, and is no login page (that is a login wall,
//     not a dead link).
//   - source names a page: its path, less an index document, holds a word,
//     and its path and query hold two. A shortener or profile link
//     (bit.ly/x, youtu.be/x, twitter.com/user) never counts, so a redirect
//     to a homepage can't mark it dead.
//   - final kept nothing of source: no word of source's path or query is a
//     word of final's hostname, path or query. A move keeps its slug, ID or
//     name somewhere (twitter.com/jack/status/20 → x.com/jack/status/20,
//     youtu.be/ID → youtube.com/watch?v=ID, computing.llnl.gov/tutorials/
//     pthreads/ → hpc-tutorials.llnl.gov/posix/). A query that only
//     records the path the request came from (repost.aws/forums?origin=
//     /message.jspa&messageID=…) is bookkeeping, not identity, and ignored.
//   - final names no page: no path, fewer path segments than source, a last
//     segment of letters alone (/articles/, /forums, /technologies/; a
//     numeric ID is a page), or a bookkeeping query.
//
// A word is a run of two or more letters or digits, lowercased.
func looksLikeLandingPage(source, final *url.URL, title string) bool {
	if !crossSite(source, final) || loginPathRE.MatchString(final.Path) || loginTitleRE.MatchString(title) {
		return false
	}
	sourceSegs := pageSegments(source.Path)
	pathWords := urlWords(strings.Join(sourceSegs, "/"))
	sourceWords := slices.Concat(pathWords, queryWords(source.Query()))
	if len(pathWords) == 0 || len(sourceWords) < 2 {
		return false
	}

	bookkeeping := recordsPath(final.Query(), source.Path)
	finalWords := slices.Concat(urlWords(final.Hostname()), urlWords(final.Path))
	if !bookkeeping {
		finalWords = append(finalWords, queryWords(final.Query())...)
	}
	if slices.ContainsFunc(sourceWords, func(w string) bool { return slices.Contains(finalWords, w) }) {
		return false
	}

	finalSegs := pathSegments(final.Path)
	return len(finalSegs) == 0 || len(finalSegs) < len(sourceSegs) || bookkeeping ||
		isLetters(finalSegs[len(finalSegs)-1])
}

// recordsPath reports whether a value of query q is path: a redirect
// noting where the request came from.
func recordsPath(q url.Values, path string) bool {
	for _, values := range q {
		if slices.Contains(values, path) {
			return true
		}
	}
	return false
}

// indexDocumentRE matches a directory's index document (index.html,
// default.aspx), which names no page of its own.
var indexDocumentRE = regexp.MustCompile(`(?i)^(?:index|default)\.[a-z0-9]+$`)

// pathSegments returns the non-empty segments of a URL path.
func pathSegments(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
}

// pageSegments returns the segments of a URL path that name a page: all of
// them, less a trailing index document.
func pageSegments(path string) []string {
	segs := pathSegments(path)
	if n := len(segs); n > 0 && indexDocumentRE.MatchString(segs[n-1]) {
		segs = segs[:n-1]
	}
	return segs
}

// urlWords returns the words of s: runs of two or more letters or digits,
// lowercased.
func urlWords(s string) []string {
	runs := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return slices.DeleteFunc(runs, func(w string) bool { return utf8.RuneCountInString(w) < 2 })
}

// queryWords returns the words of a query's keys and values.
func queryWords(q url.Values) []string {
	var words []string
	for key, values := range q {
		words = append(words, urlWords(key)...)
		for _, v := range values {
			words = append(words, urlWords(v)...)
		}
	}
	return words
}

// isLetters reports whether s is all letters: a section name, not an ID or
// a slug.
func isLetters(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
}

// soft404TitleRE matches the title of a not-found page: the whole title is
// a not-found template, optionally opened by "Oops!" or "Sorry," and closed
// by "(404)". A template is a status ("404", "Error 410", "410 Deleted by
// author"), something gone ("Page not found", "This post has been
// deleted", "The requested page could not be found"), a bare "Not found",
// or "We can't find the page you're looking for". Up to two site names may
// stand before it, each followed by a spaced separator, or by a colon when
// the name is one word ("Palantir | Careers | Page Not Found", "reddit.com:
// page not found"), and up to two after it, each after a spaced separator
// ("Page not found | Free local classifieds - Kijiji"). A spaced separator
// is one of | / · • – — - with whitespace on both sides.
//
// Anchored at both ends, because a dead verdict is sticky: a headline that
// discusses a missing page ("How to fix 404 Not Found errors in Nginx",
// "404 Media", "Product not found: lessons from a failed launch",
// "Lessons from a failed launch: Product not found") is an article's. The
// one rule anchored at the start only is the sentence "The page you're
// looking for doesn't exist", which a Readability title can carry whole,
// with whatever the page says next.
//
// The word lists are explicit: a word added to one needs a test row.
//
// notFoundLineRE is the same templates less the sentence, so anchored at
// both ends: the text rules read a line of a page with it (pagetext.go),
// where a sentence may go on to say something else, and notFoundSentenceRE
// asks for the sentence's end. statusCodeLineRE matches a template that is
// a status code alone, site names around it ("404", "Votes: 404", "404 ·
// Followers"): on a line of a page's text, that is as often a count as a
// notice.
var soft404TitleRE, notFoundLineRE, statusCodeLineRE = notFoundTemplates()

// notFoundTemplates builds soft404TitleRE, notFoundLineRE and
// statusCodeLineRE from one set of words.
func notFoundTemplates() (title, line, statusCode *regexp.Regexp) {
	const (
		// site is a site's name beside the template.
		site = `[^|]{1,60}?`
		// sep separates a site's name from the template, with whitespace
		// on both sides. A colon is none: after a template, it opens a
		// headline ("Not Found: The Search for Amelia Earhart").
		sep = `\s+[|/·•–—-]\s+`
		// prefix is a site's name before the template: any name before a
		// spaced separator, or one word before a colon ("reddit.com: page
		// not found"). Several words before a colon open a headline
		// ("Lessons from a failed launch: Product not found").
		prefix = `(?:` + site + sep + `|[^\s|:]{1,60}\s*:\s+)`
		// code is a not-found status code: 404 Not Found or 410 Gone.
		code = `(?:404|410)`
		// status is the code, as a title gives it.
		status = `(?:http\s+)?(?:error\s*)?` + code
		// statusWords may follow a status, up to twice ("404 Error: Page
		// Not Found").
		statusWords = `(?:not\s+found|\(\s*not\s+found\s*\)|error|gone|page\s+not\s+found` +
			`|deleted(?:\s+by\s+(?:the\s+)?author)?)`
		// parenStatus closes a template with its status.
		parenStatus = `(?:\s*\(\s*(?:error\s+)?` + code + `\s*\))?`
		// template is the title less the site names around it.
		template = notFoundLead + `(?:` +
			status + `\.?(?:\s*[|:–—-]?\s*` + statusWords + `){0,2}` +
			`|(?:(?:this|that|the)\s+)?(?:requested\s+)?` + notFoundThing + notFoundYouWanted + `\s+` + notFoundGone +
			`|not\s+found` +
			`|` + notFoundCantFind + `\s+(?:this|that|the)\s+(?:requested\s+)?` + notFoundThing + notFoundYouWanted +
			`)` + parenStatus
		// sentence is the one template anchored at the start only: the
		// title may go on after it.
		sentence = notFoundLead + `(?:the|this)\s+page\s+you(?:` + notFoundApos + `re|\s+are|\s+were)\s+looking\s+for\s+` +
			notFoundGone + `\b`
	)
	// whole is a template with the site names around it, the whole of a
	// title or a line.
	whole := func(inner string) string {
		return `^\s*` + prefix + `{0,2}` + inner + `[.!]*(?:` + sep + site + `){0,2}\s*$`
	}
	return regexp.MustCompile(`(?i)` + whole(template) + `|^\s*` + sentence),
		regexp.MustCompile(`(?i)` + whole(template)),
		regexp.MustCompile(`(?i)` + whole(code))
}

// The words a not-found notice is made of, shared by the title rule
// (soft404TitleRE) and the text rules (notFoundLineRE, notFoundSentenceRE).
// The lists are explicit: a word added to one needs a row in each rule's
// test (TestSoft404TitleRE, TestNotFoundSentenceRE).
const (
	// notFoundApos is an apostrophe, straight or curly.
	notFoundApos = `[’']`
	// notFoundLead is an interjection opening a notice.
	notFoundLead = `(?:(?:oops|whoops|sorry|uh[\s-]?oh)[!.,:…]*\s+)?`
	// notFoundThing is what a site says is gone.
	notFoundThing = `(?:page|content|post|story|article|video|track|product|group|profile|user|item|listing)`
	// notFoundYouWanted says the thing was asked for.
	notFoundYouWanted = `(?:\s+you(?:` + notFoundApos + `re|\s+are|\s+were)?` +
		`\s+(?:looking\s+for|requested|tried\s+to\s+(?:access|reach|visit)))?`
	// notFoundGone says the thing is gone.
	notFoundGone = `(?:(?:was\s+|is\s+)?not\s+found` +
		`|(?:could\s+not|couldn` + notFoundApos + `?t|can` + notFoundApos + `?t|cannot)\s+be\s+found` +
		`|(?:does\s+not|doesn` + notFoundApos + `?t)\s+exist` +
		`|no\s+longer\s+(?:exists|available)` +
		`|(?:is\s+no\s+longer|is\s+not|isn` + notFoundApos + `?t)\s+available` +
		`|is\s+missing` +
		`|(?:has\s+been|was)\s+(?:removed|deleted)(?:\s+by\s+(?:the|its)\s+(?:author|owner|user))?)`
	// notFoundCantFind is the site saying it can't find the thing.
	notFoundCantFind = `(?:we\s+)?(?:couldn` + notFoundApos + `?t|could\s+not|can` + notFoundApos + `?t|cannot)\s+find`
)

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
	// an error page, a login page, too little text). It is Jina's verdict
	// about the target.
	errJinaRejected = errors.New("answer is not the page")

	// errJinaTargetTrouble marks a Jina answer carrying a target status that
	// a later attempt could change: a timeout, a rate limit, a server error
	// (reported in a warning, or an error page naming one), or 404/410 with
	// dead-link detection off. It is no verdict about the target, and
	// calling Jina again at once would only spend its budget, so the job's
	// backoff retries it.
	errJinaTargetTrouble = errors.New("target failed for now")

	// errJinaChallenged marks Jina's own 403 carrying Cloudflare's
	// "cf-mitigated: challenge": r.jina.ai's CDN took curio for a bot. It
	// says nothing about the target, and every Jina call would get the same
	// answer, so it pauses them all (jinaChallengeCooldown).
	errJinaChallenged = errors.New("r.jina.ai's CDN challenged the request")

	// errJinaRefused marks Jina's own answer refusing the target
	// (jinaRefusesTarget): a deterministic 4xx about the request, such as a
	// publisher's opt-out (451), or a 403 whose reason names the target's
	// host and is no site block. It is Jina's verdict about the target, like
	// errJinaRejected, and costs one Jina request.
	errJinaRefused = errors.New("refused the target")

	// errJinaSiteBlocked marks Jina Reader's block of a site's keyless
	// reads for now (an AbuseAlleviationError, jinaSiteBlock), and the calls
	// curio holds back while it lasts (siteBlockedError). It says nothing
	// about the target: the site's pages wait for the block to end.
	errJinaSiteBlocked = errors.New("blocks the site for now")
)

// jinaSiteBlock is Jina Reader's answer that it blocks keyless reads of a
// domain until a time it names, after a burst of them: "AbuseAlleviationError:
// Anonymous access to domain mobile.twitter.com blocked until Mon Sep 28
// 2026 18:48:50 GMT+0000 (Coordinated Universal Time) due to previous abuse
// found on …". err is the answer's error, errJinaSiteBlocked around Jina's
// *HTTPStatusError, quoting its reason.
type jinaSiteBlock struct {
	domain string    // the domain the answer names; empty when it names none
	until  time.Time // when the answer says the block ends; zero when it doesn't say readably
	reason string    // Jina's reason (jinaReason)
	err    error
}

func (e *jinaSiteBlock) Error() string { return e.err.Error() }
func (e *jinaSiteBlock) Unwrap() error { return e.err }

// siteBlockedError is a Jina call for a page of site that curio didn't
// send, because Jina Reader blocks the site until until for reason.
type siteBlockedError struct {
	site   string
	until  time.Time
	reason string
}

func (e *siteBlockedError) Error() string {
	return fmt.Sprintf("jina: not sent, Jina Reader blocks %s until %s: %s",
		e.site, e.until.UTC().Format(time.RFC3339), e.reason)
}

func (*siteBlockedError) Unwrap() error { return errJinaSiteBlocked }

// targetStatusError is the status the target answered Jina with, as Jina's
// "Target URL returned error" warning reports it. It is deliberately not an
// *HTTPStatusError: that is Jina's own answer, whose 429 extends Jina's
// cooldown by its Retry-After, which a status the target gave Jina must not.
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

// tryJina is pass 2: fetch r.jina.ai/<url>. Every call takes its site's
// turn and goes through the shared limiter and cooldown (awaitJina), and
// every request's outcome counts toward Jina's health (jinaHealth).
// Transient failures are retried up to jinaAttempts times: 5xx and
// transport errors after a 2/4/8s backoff, 429s after the cooldown they
// set. Jina's block of the site defers the fetch at once (siteBlocked).
func (n *Native) tryJina(ctx context.Context, target string) (*Result, error) {
	var lastErr error
	for attempt := range jinaAttempts {
		if attempt > 0 && !isRateLimited(lastErr) {
			if err := n.clock.sleep(ctx, jinaBackoff(attempt)); err != nil {
				return nil, fmt.Errorf("jina: %w", err)
			}
		}
		if err := n.awaitJina(ctx, target); err != nil {
			return nil, err
		}

		res, err := n.jinaOnce(ctx, target)
		// A request our own cancellation (shutdown) cut short says nothing
		// about Jina.
		if err == nil || ctx.Err() == nil {
			n.jinaHealth.record(n.clock.now(), hostOf(target), jinaCallClass(err), err)
		}
		if err == nil {
			return res, nil
		}
		lastErr = err
		// A site's block holds that site's pages alone, whatever its
		// status: never the shared cooldown, and never a retry.
		if sb, ok := errors.AsType[*jinaSiteBlock](err); ok {
			return nil, n.siteBlocked(target, sb)
		}
		n.extendJinaCooldown(err, attempt)
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

// jinaChallengeCooldown is how long Jina calls pause after r.jina.ai's CDN
// challenged one, when its answer gave no Retry-After. Every call would be
// challenged the same way, so a pause saves each fetch a request. The
// fetches that need Jina during it are deferred until it ends, without
// using up an attempt.
const jinaChallengeCooldown = 10 * time.Minute

// extendJinaCooldown extends the cooldown every Jina call shares when a
// failed call, attempt (0-based) of its fetch, says Jina would refuse the
// next ones too. Jina limits per client, so a 429 pauses every caller, not
// just this one; a CDN challenge pauses them for longer. A block of one
// site, even one answered with a 429, pauses that site alone and never
// comes here (siteBlocked).
//
// A challenge warns once per pause: the calls already in flight when the
// CDN began challenging come back challenged too, and only extend the pause
// the first one started. One that meets the short cooldown of a 429 extends
// it silently; healthz and doctor still show the pause and the challenged
// call.
func (n *Native) extendJinaCooldown(err error, attempt int) {
	var se *HTTPStatusError
	if !errors.As(err, &se) {
		return
	}
	switch {
	case errors.Is(err, errJinaChallenged):
		pause := cmp.Or(se.RetryAfter, jinaChallengeCooldown)
		if n.jinaCooldown.extend(n.clock.now(), pause) {
			n.log.Warn("r.jina.ai's CDN challenged curio, pausing Jina calls", "pause", pause)
		}
	case se.StatusCode == http.StatusTooManyRequests:
		n.jinaCooldown.extend(n.clock.now(), cmp.Or(se.RetryAfter, jinaBackoff(attempt+1)))
	}
}

// awaitJina clears a Jina call for target, in this order:
//
//  1. The shared cooldown, when it already outlasts maxInlineJinaWait:
//     before the site's turn, which it would otherwise spend.
//  2. The turn of target's site (siteOf), which Jina Reader's block of the
//     site also holds back (sitePacer.take).
//  3. The shared limiter and cooldown (pace).
//  4. The send-time guard, which spaces the site's requests again after
//     the limiter's queue (sitePacer.spaceSend).
//  5. The site's block, once more, right before the request goes.
//
// Waits up to maxInlineJinaWait are sat out; the limiter's and the guard's
// always are, as steady paces. A longer hold returns at once, without a
// request, a *DeferError until it ends: the cooldown's around a 429
// carrying the time left, whether or not this fetch has called Jina
// already. The fetch then runs again whole: the origin's answer is not
// kept, since a block may have lifted by then, and a memo of it would be
// state a restart loses.
func (n *Native) awaitJina(ctx context.Context, target string) error {
	site := siteOf(hostOf(target))
	if left := n.jinaCooldown.remaining(n.clock.now()); left > maxInlineJinaWait {
		return n.cooldownHold(left)
	}
	turn := n.jinaSites.take(site, n.clock.now(), maxInlineJinaWait)
	if !turn.until.IsZero() {
		return n.siteHold(site, turn)
	}
	if err := n.sleepFor(ctx, turn.wait); err != nil {
		return err
	}
	left, err := pace(ctx, n.jinaLimiter, &n.jinaCooldown, n.clock, maxInlineJinaWait)
	if err != nil {
		return fmt.Errorf("jina: %w", err)
	}
	if left > 0 {
		return n.cooldownHold(left)
	}
	if err := n.sleepFor(ctx, n.jinaSites.spaceSend(site, n.clock.now())); err != nil {
		return err
	}
	if turn, held := n.jinaSites.holdForBlock(site, n.clock.now()); held {
		return n.siteHold(site, turn)
	}
	return nil
}

// sleepFor sits out a wait of d before a Jina call, on the Native's clock.
func (n *Native) sleepFor(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if err := n.clock.sleep(ctx, d); err != nil {
		return fmt.Errorf("jina: %w", err)
	}
	return nil
}

// cooldownHold is the deferral of a Jina call the shared cooldown holds,
// with left of it to go.
func (n *Native) cooldownHold(left time.Duration) *DeferError {
	se := &HTTPStatusError{StatusCode: http.StatusTooManyRequests, URL: n.jinaBaseURL, RetryAfter: left}
	return heldBack(&n.jinaCooldown, jinaHoldReason,
		fmt.Errorf("jina: not sent, cooldown has %s left: %w", left.Round(time.Second), se))
}

// siteHold is the deferral of a Jina call for a page of site that the
// site pacer holds past the inline cap, for the site's turn or its block.
// Its error is retryable, and says nothing about the page.
func (n *Native) siteHold(site string, turn siteTurn) *DeferError {
	if turn.block != nil {
		return &DeferError{Until: turn.until, Reason: siteBlockReason(site),
			Err: &siteBlockedError{site: site, until: turn.block.until, reason: turn.block.reason}}
	}
	return &DeferError{Until: turn.until, Reason: "a turn at Jina Reader for " + site,
		Err: fmt.Errorf("jina: not sent, %s's turn comes at %s (%s between its requests)",
			site, turn.until.UTC().Format(time.RFC3339), n.jinaSites.interval)}
}

// siteBlockReason is what the pages of site wait for while Jina Reader
// blocks it, worded to follow "waiting for".
func siteBlockReason(site string) string { return "Jina Reader's block of " + site + " to lift" }

// siteBlocked records Jina Reader's block of a site, from sb, its answer
// to a request for target, and returns the deferral of target's fetch: it
// waits for the block's end, spread out with the site's other pages (see
// sitePacer). The site is the one the answer names, or target's when it
// names none. The block lasts until the time the answer gives, or
// jinaSiteBlockDefault without one, within [minJinaSiteBlock,
// maxJinaSiteBlock]. It warns when it starts a block; answers that extend
// one say nothing more.
func (n *Native) siteBlocked(target string, sb *jinaSiteBlock) *DeferError {
	now := n.clock.now()
	site := siteOf(cmp.Or(sb.domain, hostOf(target)))
	end := siteBlockEnd(sb.until, now)
	turn, started := n.jinaSites.block(site, end, sb.reason, now)
	if started {
		n.log.Warn("Jina Reader blocks keyless reads of a site, holding its pages",
			"site", site, "domain", sb.domain, "until", end)
	}
	return &DeferError{Until: turn.until, Reason: siteBlockReason(site), Err: sb}
}

// siteBlockEnd is when a block that Jina Reader said, at now, lasts until
// said (zero when it didn't say readably) is taken to end.
func siteBlockEnd(said, now time.Time) time.Time {
	pause := jinaSiteBlockDefault
	if !said.IsZero() {
		pause = said.Sub(now)
	}
	return now.Add(min(max(pause, minJinaSiteBlock), maxJinaSiteBlock))
}

// jinaUserAgent is the User-Agent of every Jina request. r.jina.ai sits
// behind Cloudflare, which answers a browser's User-Agent from a client that
// runs no JavaScript with a managed challenge (403), while it lets an API
// client that says what it is through. The browser headers are for origins.
const jinaUserAgent = "curio (+https://github.com/samsar/curio)"

// jinaOnce makes one Jina request and parses the answer.
func (n *Native) jinaOnce(ctx context.Context, target string) (*Result, error) {
	headers := []header{
		{"user-agent", jinaUserAgent},
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
		// One bounded read gives the reason and lets the connection be
		// reused (errorBodyDrain). A failed read only loses the reason, and
		// errs on the safe side: the status still classifies the answer, but
		// a 403 whose reason is lost names no target and stays no verdict.
		head, _ := io.ReadAll(io.LimitReader(resp.body, errorBodyDrain))
		if n.jinaAPIKey != "" {
			// The reason is quoted into last_error and the logs; a key Jina
			// echoes back must not go with it.
			head = bytes.ReplaceAll(head, []byte(n.jinaAPIKey), []byte("[redacted]"))
		}
		se := &HTTPStatusError{StatusCode: resp.statusCode, URL: resp.finalURL.String()}
		se.RetryAfter, _ = parseRetryAfter(resp.header, n.clock.now())
		return nil, jinaStatusError(target, se, resp.header, jinaReason(resp.contentType, head))
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

// jinaStatusError classifies Jina's own non-2xx answer se to a request for
// target, whose header came with it and whose body gave reason (jinaReason;
// empty when it gave none):
//
//   - A 403 carrying Cloudflare's "cf-mitigated: challenge":
//     errJinaChallenged. Its body is the challenge page, never quoted.
//   - An AbuseAlleviationError, whatever its status (403 or 429): Jina's
//     block of a site for now, a *jinaSiteBlock (errJinaSiteBlocked).
//   - A refusal of the target (jinaRefusesTarget): errJinaRefused.
//   - Anything else is Jina's trouble, or curio's with Jina, and no verdict.
//
// The reason is quoted after the status, so last_error says why.
func jinaStatusError(target string, se *HTTPStatusError, header http.Header, reason string) error {
	var err error = se
	switch {
	case se.StatusCode == http.StatusForbidden && strings.EqualFold(header.Get("Cf-Mitigated"), "challenge"):
		return fmt.Errorf("jina: %w: %w", errJinaChallenged, se)
	case strings.HasPrefix(reason, jinaAbuseErrorName+": "):
		return parseSiteBlock(reason, fmt.Errorf("jina: %w: %w: %s", errJinaSiteBlocked, se, reason))
	case jinaRefusesTarget(se.StatusCode, reason, hostOf(target)):
		err = fmt.Errorf("%w: %w", errJinaRefused, se)
	}
	if reason == "" {
		return fmt.Errorf("jina: %w", err)
	}
	return fmt.Errorf("jina: %w: %s", err, reason)
}

// jinaRefusesTarget reports whether Jina's answer with status code, whose
// body gave reason, refuses the target on host: a deterministic 4xx about
// the request (400, 404, 410, 422, 451, …), or a 403 whose reason names
// host. Jina's abuse blocks name the domain too, but end, and are told
// apart before (jinaSiteBlock). A refusal of curio itself, such as
// Cloudflare's block page or an IP ban, names no target, so a 403 that
// names none stays no verdict rather than failing every document for good.
func jinaRefusesTarget(code int, reason, host string) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusPaymentRequired:
		return false
	case http.StatusForbidden:
		return namesHost(reason, host)
	}
	return code >= 400 && code < 500 && !retryableStatus(code)
}

// namesHost reports whether text names host (lowercase, as hostOf gives
// it): whether one of its words, split at every character a hostname can't
// hold and less a sentence's closing dot, is host. A whole name must match,
// so a reason about mobile.twitter.com doesn't name twitter.com. A host
// inside a URL doesn't count: an error that echoes the requested URL says
// nothing about what was refused, and reading it as a refusal would fail
// every document for good while Jina itself is down.
func namesHost(text, host string) bool {
	if host == "" {
		return false
	}
	text = urlInTextRE.ReplaceAllString(text, " ")
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '-'
	})
	return slices.ContainsFunc(words, func(w string) bool { return strings.TrimRight(w, ".") == host })
}

// urlInTextRE matches a URL in running text, scheme to the next space.
var urlInTextRE = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S*`)

// jinaErrorLineRE matches the first line of a text answer in which Jina
// names its error: "AbuseAlleviationError: Anonymous access to …".
var jinaErrorLineRE = regexp.MustCompile(`^[A-Z][A-Za-z]*Error: `)

// jinaAbuseErrorName is the name Jina Reader gives its block of a domain's
// keyless reads, which opens the error's reason (jinaReason).
const jinaAbuseErrorName = "AbuseAlleviationError"

var (
	// jinaSiteBlockRE reads the domain and the end of a block from an
	// AbuseAlleviationError's reason: "Anonymous access to domain <domain>
	// blocked until <date> due to …". A sentence's closing dot after the
	// domain is not part of it.
	jinaSiteBlockRE = regexp.MustCompile(`(?i)\baccess to domain (\S+?)\.? blocked until (.+?)(?:\s+due to\b|$)`)
	// jinaZoneNameRE matches the time zone's name that closes a JavaScript
	// date: " (Coordinated Universal Time)".
	jinaZoneNameRE = regexp.MustCompile(`\s*\([^()]*\)$`)
	// jinaDateLayouts are the forms a block's end is read in: a JavaScript
	// Date's toString, as Jina writes it (a 1- or 2-digit day, any GMT
	// offset), then the HTTP and ISO forms, should it switch.
	jinaDateLayouts = []string{"Mon Jan 2 2006 15:04:05 GMT-0700", time.RFC1123, time.RFC1123Z, time.RFC3339Nano}
)

// parseSiteBlock reads a *jinaSiteBlock around err from reason, an
// AbuseAlleviationError's. A reason that names no domain, or no end curio
// can read, leaves that part empty; the block still holds the target's
// site (siteBlocked).
func parseSiteBlock(reason string, err error) *jinaSiteBlock {
	sb := &jinaSiteBlock{reason: reason, err: err}
	m := jinaSiteBlockRE.FindStringSubmatch(reason)
	if m == nil {
		return sb
	}
	sb.domain = strings.ToLower(m[1])
	date := jinaZoneNameRE.ReplaceAllString(m[2], "")
	for _, layout := range jinaDateLayouts {
		if t, perr := time.Parse(layout, date); perr == nil {
			sb.until = t
			break
		}
	}
	return sb
}

// jinaReason is what the head of Jina's error answer says went wrong: a
// JSON answer's message, after its name when it gives one, or the first line
// of any other answer when it names one of Jina's errors (jinaErrorLineRE).
// Anything else, a Cloudflare challenge or block page among them, gives
// none. It is capped like any quoted body (snippet).
func jinaReason(contentType string, head []byte) string {
	if mt := mediaType(contentType); mt == "application/json" || strings.HasSuffix(mt, "+json") {
		var answer struct {
			Name    string `json:"name"`
			Message string `json:"message"`
		}
		if json.Unmarshal(head, &answer) != nil || answer.Message == "" {
			return ""
		}
		if answer.Name != "" {
			return snippet([]byte(answer.Name + ": " + answer.Message))
		}
		return snippet([]byte(answer.Message))
	}
	line, _, _ := bytes.Cut(head, []byte("\n"))
	if !jinaErrorLineRE.Match(line) {
		return ""
	}
	return snippet(line)
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
// is, and a server-error page is errJinaTargetTrouble, like the status it
// names; every other verdict wraps errJinaRejected.
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
	switch {
	case err == nil, errors.Is(err, ErrDeadLink):
		return err
	case errors.Is(err, errServerErrorPage):
		return fmt.Errorf("%w: %w", errJinaTargetTrouble, err)
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
	// authorized to access this page." It is anchored to that warning's
	// opening, so another warning that merely mentions a CAPTCHA stays
	// informational.
	jinaCaptchaRE = regexp.MustCompile(`(?i)^this page maybe requiring captcha\b`)
)

// jinaRetryable reports whether a failed Jina call is worth another attempt
// within the same fetch: transport errors and Jina's retryable statuses are.
// A judged answer is not (another call would bring the same page back, and
// a target that failed for now is left to the job's backoff), nor are an
// oversized body, Jina's deterministic statuses, and its block of a site,
// even with a 429: the block's end is when to ask again.
func jinaRetryable(err error) bool {
	if errors.Is(err, errJinaRejected) || errors.Is(err, errJinaTargetTrouble) ||
		errors.Is(err, ErrDeadLink) || errors.Is(err, ErrTooLarge) || errors.Is(err, errJinaSiteBlocked) {
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

// jinaCallClass is how a Jina request went, for Jina's health, from the
// error jinaOnce returned for it.
func jinaCallClass(err error) CallClass {
	var se *HTTPStatusError
	switch {
	case err == nil:
		return CallOK
	case errors.Is(err, errJinaSiteBlocked):
		// Jina limiting curio's reads of a site, whatever the status.
		return CallRateLimited
	case errors.Is(err, errJinaRefused):
		return CallRefused
	case errors.Is(err, errJinaChallenged):
		return CallChallenged
	case errors.Is(err, errJinaRejected), errors.Is(err, errJinaTargetTrouble),
		errors.Is(err, ErrDeadLink), errors.Is(err, ErrTooLarge):
		// A 2xx whose answer is not the page.
		return CallJudged
	case !errors.As(err, &se):
		// No answer, or not all of one.
		return CallNetwork
	}
	switch se.StatusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired:
		return CallAuth
	case http.StatusForbidden:
		return CallForbidden
	case http.StatusTooManyRequests:
		return CallRateLimited
	}
	// Every other 4xx refuses the target, which leaves 5xx, 408, 421, 425,
	// and statuses that are no answer (1xx, 3xx).
	return CallServerError
}

// JinaHealth is the health of the Jina fallback: how its recent requests
// went (healthTracker), the pause in effect, if any, and the sites Jina
// Reader blocks for now, which leave its state alone: a block is about one
// site's reads, not the service.
func (n *Native) JinaHealth() UpstreamHealth {
	now := n.clock.now()
	h := n.jinaHealth.snapshot(now, n.jinaFallback, n.jinaCooldown.deadline())
	h.SitePauses = n.jinaSites.pauses(now)
	return h
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
