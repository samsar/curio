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
// Login-wall heuristics and Jina Reader fallback are ported faithfully —
// the same set of pages that fail in the JS impl fail (and fall back) here.
type Native struct {
	rt                roundTripper
	userAgent         string
	secChUA           string
	jinaFallback      bool
	jinaBaseURL       string // override for tests
	jinaAPIKey        string
	jinaLimiter       *rate.Limiter
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

func (n *Native) Name() string { return "native" }

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
	err = fmt.Errorf("%w; %w", originErr, jinaErr)
	switch {
	case errors.Is(jinaErr, ErrTooLarge):
		return nil, &PermanentError{Err: err}
	case !jinaAnswered(jinaErr):
		// Jina's own trouble says nothing about the target: never cache it,
		// and let the job retry.
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
// target: Jina fetched it and found too little, or refused it with a
// deterministic 4xx. Rate limits, outages, timeouts and transport errors are
// trouble on Jina's side, and 401/402 are about our account; none of them
// says anything about the target.
func jinaAnswered(err error) bool {
	if errors.Is(err, errJinaThin) {
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
//   - anti-bot: a 403/503 answer
//   - login wall: a redirect onto the requested site's own login page
//
// Everything else is about one page (thin content, a cross-site redirect) or
// transient, and caching it would fail healthy URLs without a request.
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
		if errors.As(err, &se) {
			return HostFailAntiBot, orHost(hostOf(se.URL), requestedHost), true
		}
		return HostFailAntiBot, requestedHost, true
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
// markdown. Returns ErrLoginWall (wrapped) on any of the login-wall
// heuristics so callers can distinguish "page is paywalled" from "page
// failed to fetch."
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

	// Soft-404 check must run BEFORE the login-wall heuristics: a
	// tombstone page is usually thin, and the thin-content check would
	// misclassify it as a login wall and route it to Jina — which can't
	// help with a page that no longer exists.
	if n.deadLinkDetection {
		if reason := looksLikeSoft404(article, finalURL, target); reason != "" {
			return nil, &PermanentError{Err: fmt.Errorf("native: dead link (%s): %w", reason, ErrDeadLink)}
		}
	}

	if reason, siteWide := looksLikeLoginWall(article, finalURL, target); reason != "" {
		sentinel := ErrLoginWall
		if siteWide {
			sentinel = errSiteLoginWall
		}
		return nil, fmt.Errorf("native: %w (%s)", sentinel, reason)
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

// errSiteLoginWall is the login wall a whole site sits behind: the request
// was redirected onto the site's own login page. It wraps ErrLoginWall, so
// it gets the same Jina fallback, but unlike a thin page it is host-wide.
var errSiteLoginWall = fmt.Errorf("site-wide %w", ErrLoginWall)

// looksLikeLoginWall mirrors the JS impl's heuristics in samsar/web-to-markdown:
//   - redirect to a /login, /authwall, /signin, /signup path
//   - redirect to a different site
//   - missing article entirely
//   - extracted text shorter than minArticleBytes
//   - title starts with "sign in"/"log in"/"join now"/"join linkedin"
//
// Returns the empty string when nothing looks suspicious; otherwise a
// short reason string for diagnostics. siteWide is set for a redirect onto
// the requested site's own login page, the one verdict here that speaks for
// every page on the host. The redirect checks run first so a thin login
// page still counts as the site-wide wall it is.
func looksLikeLoginWall(article readability.Article, finalURL *url.URL, sourceURL string) (reason string, siteWide bool) {
	if source, err := url.Parse(sourceURL); err == nil {
		if finalURL.Hostname() != "" && source.Hostname() != "" &&
			!sameSiteHost(finalURL.Hostname(), source.Hostname()) {
			return "redirected to a different host: " + finalURL.Hostname(), false
		}
		if loginPathRE.MatchString(finalURL.Path) {
			return "redirected to a login/auth path: " + finalURL.Path, finalURL.Path != source.Path
		}
	}

	if article.Node == nil {
		return "no article extracted", false
	}

	// Length check: render the text body and measure it.
	var txtBuf bytes.Buffer
	if err := article.RenderText(&txtBuf); err != nil {
		return "text rendering failed: " + err.Error(), false
	}
	if trimmedByteLen(txtBuf.String()) < minArticleBytes {
		return fmt.Sprintf("extracted text < %d bytes", minArticleBytes), false
	}

	if loginTitleRE.MatchString(article.Title()) {
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
	loginTitleRE = regexp.MustCompile(`(?i)^(sign in|log in|join now|join linkedin)`)
	loginPathRE  = regexp.MustCompile(`(?i)/(login|authwall|signin|signup)\b`)
)

// looksLikeSoft404 detects "soft 404s": pages that answer HTTP 200 but
// whose content says the resource is gone (CMS not-found templates,
// deleted articles redirecting to the site root). Two signals:
//
//   - the extracted title reads like a not-found page
//   - the request for a specific path settled on the site's homepage
//     (same site, www. or not; cross-site redirects are login-wall
//     territory)
//
// Returns the empty string when nothing looks dead; otherwise a short
// reason string for diagnostics.
func looksLikeSoft404(article readability.Article, finalURL *url.URL, sourceURL string) string {
	source, err := url.Parse(sourceURL)
	if err == nil && finalURL != nil &&
		sameSiteHost(source.Hostname(), finalURL.Hostname()) &&
		strings.Trim(source.Path, "/") != "" &&
		strings.Trim(finalURL.Path, "/") == "" &&
		finalURL.RawQuery == "" {
		return "redirected to homepage"
	}

	if article.Node != nil && soft404TitleRE.MatchString(article.Title()) {
		return "title looks like a not-found page: " + article.Title()
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

// errJinaThin marks a Jina answer with too little content to be the page.
var errJinaThin = errors.New("too little content")

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

// awaitJina paces a Jina call: the shared limiter first, then any cooldown
// a 429 left, sat out inline up to maxInlineJinaWait. A longer cooldown
// fails at once, without a request, with a retryable 429 carrying the time
// left.
func (n *Native) awaitJina(ctx context.Context) error {
	if err := n.jinaLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("jina: rate limiter: %w", err)
	}
	left, err := n.jinaCooldown.wait(ctx, n.clock, maxInlineJinaWait)
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
	if len(parsed.body) < 200 {
		return nil, fmt.Errorf("jina: %w (%d chars)", errJinaThin, len(parsed.body))
	}
	result := &Result{
		Markdown:    parsed.body,
		FinalURL:    parsed.urlSource,
		ContentType: store.ContentTypeArticle,
		Title:       parsed.title,
		Meta:        map[string]any{"via": "jina"},
	}
	if result.FinalURL == "" {
		result.FinalURL = target
	}
	if parsed.published != "" {
		if pt, err := time.Parse(time.RFC3339, parsed.published); err == nil {
			result.PublishedAt = &pt
		}
	}
	return result, nil
}

// jinaRetryable reports whether a failed Jina call is worth another attempt
// within the same fetch: transport errors and retryable statuses are; a
// thin answer, an oversized body and deterministic statuses are not.
func jinaRetryable(err error) bool {
	if errors.Is(err, errJinaThin) || errors.Is(err, ErrTooLarge) {
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

// jinaParsed mirrors the JS impl's parseJina output shape.
type jinaParsed struct {
	title     string
	urlSource string
	published string
	body      string
}

// parseJina extracts the title/source/published/body from a Jina Reader
// response. Format:
//
//	Title: ...
//	URL Source: ...
//	Published Time: ...
//	Description: ...
//
//	Markdown Content:
//	<body>
//
// Mirrors the JS parseJina semantics — uses a single regex per header line.
func parseJina(text string) jinaParsed {
	var (
		out       jinaParsed
		sawHeader bool
	)
	lines := strings.Split(text, "\n")
	i := 0
	meta := map[string]string{}
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "Markdown Content:" {
			i++
			break
		}
		m := jinaHeaderRE.FindStringSubmatch(line)
		if len(m) == 3 {
			meta[strings.TrimSpace(m[1])] = strings.TrimSpace(m[2])
			sawHeader = true
		} else if strings.TrimSpace(line) == "" {
			// blank line in header is fine
		} else if sawHeader {
			// header ended without a "Markdown Content:" marker
			break
		}
		i++
	}
	out.title = meta["Title"]
	out.urlSource = meta["URL Source"]
	out.published = meta["Published Time"]
	out.body = strings.TrimSpace(strings.Join(lines[i:], "\n"))
	return out
}
