package fetcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
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
	rt           roundTripper
	userAgent    string
	jinaFallback bool
	jinaBaseURL  string // override for tests
	log          *slog.Logger
	hostCache    *hostFailureCache
}

// NativeOptions configures Native. Zero-value fields use defaults.
type NativeOptions struct {
	Timeout      time.Duration
	UserAgent    string
	JinaFallback bool
	JinaBaseURL  string // default https://r.jina.ai/
	Log          *slog.Logger
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
	// "chrome_131"/"chrome_133" pin a specific profile. See transport.go.
	Backend string
}

// defaultUA must stay coherent with the default chrome profile (Chrome_133):
// a JA3 that says Chrome 133 paired with a UA that says something else is a
// mismatch some bot checks flag. Override Backend and UserAgent together.
const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

func NewNative(opts NativeOptions) *Native {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUA
	}
	if opts.JinaBaseURL == "" {
		opts.JinaBaseURL = "https://r.jina.ai/"
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	rt, err := newRoundTripper(opts.Backend, opts.Timeout, opts.Log)
	if err != nil {
		// A fingerprint backend that won't initialize shouldn't take the
		// fetcher down — degrade to stock net/http and carry on.
		opts.Log.Warn("fetcher transport init failed, falling back to stock net/http",
			"backend", opts.Backend, "err", err)
		rt = newStockRT(opts.Timeout)
	}
	opts.Log.Info("native fetcher transport", "backend", rt.name())
	return &Native{
		rt:           rt,
		userAgent:    opts.UserAgent,
		jinaFallback: opts.JinaFallback,
		jinaBaseURL:  opts.JinaBaseURL,
		log:          opts.Log,
		hostCache:    newHostFailureCache(opts.HostFailureTTL),
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
	if cached, ok := n.hostCache.Get(host); ok {
		n.log.Info("fast-fail from host cache",
			"url", target, "host", host, "kind", cached.kind.String(),
			"age_seconds", int(time.Since(cached.seenAt).Seconds()))
		switch cached.kind {
		case HostFailUnreachable:
			return nil, fmt.Errorf("native: %w (cached: %s)", ErrHostUnreachable, cached.originalErr)
		case HostFailAntiBot:
			return nil, fmt.Errorf("native: %w (cached: %s)", ErrAntiBot, cached.originalErr)
		case HostFailLoginWall:
			return nil, fmt.Errorf("native: %w (cached: %s)", ErrLoginWall, cached.originalErr)
		}
	}

	// Pass 1: direct fetch + Readability.
	direct, directErr := n.tryReadability(ctx, target)
	if directErr == nil {
		return direct, nil
	}

	// Unreachable hosts don't get Jina — Jina hits origin too and will
	// fail the same way, just slower.
	if errors.Is(directErr, ErrHostUnreachable) {
		n.recordHostFailure(host, directErr)
		return nil, directErr
	}

	if !n.jinaFallback {
		n.recordHostFailure(host, directErr)
		return nil, directErr
	}

	// Fall back to Jina only for cases it can plausibly help with:
	//   ErrLoginWall — page came back but was paywalled/thin
	//   ErrAntiBot   — 403/503 from origin, likely a WAF block
	// Skip for 404, 5xx-other, and timeouts: Jina can't conjure a page
	// that doesn't exist, and wasting its rate limit on dead links gets
	// us 429'd on the calls that *would* benefit.
	if !errors.Is(directErr, ErrLoginWall) && !errors.Is(directErr, ErrAntiBot) {
		return nil, directErr
	}

	n.log.Info("native fetch needs help, falling back to jina",
		"url", target, "err", directErr.Error())

	jina, err := n.tryJina(ctx, target)
	if err != nil {
		// Both origin AND Jina failed for this host — strong signal it's
		// a host-wide block. Cache so the next N jobs for this host
		// short-circuit.
		n.recordHostFailure(host, directErr)
		return nil, fmt.Errorf("both readability and jina failed (readability: %v) (jina: %w)",
			directErr, err)
	}
	return jina, nil
}

// recordHostFailure stores the failure in the host cache if its kind is
// host-wide. Path-specific errors (404, plain 500s) are ignored — they
// don't predict the next path on the same host.
func (n *Native) recordHostFailure(host string, err error) {
	if host == "" || err == nil {
		return
	}
	kind, ok := hostFailureFromError(err)
	if !ok {
		return
	}
	n.hostCache.Put(host, kind, err.Error())
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
		{"sec-ch-ua", `"Chromium";v="133", "Google Chrome";v="133", "Not(A:Brand";v="99"`},
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
			return nil, fmt.Errorf("native: fetch: %w: %v", ErrHostUnreachable, err)
		}
		return nil, fmt.Errorf("native: fetch: %w", err)
	}
	defer resp.body.Close()

	if resp.statusCode < 200 || resp.statusCode >= 300 {
		// Drain a bit so the connection can be reused.
		_, _ = io.CopyN(io.Discard, resp.body, 1024)
		// 403 and 503 are commonly Cloudflare / WAF bot blocks rather
		// than genuine "page missing" or "server down" — Jina's
		// infrastructure often gets through where we don't. Tag with
		// ErrAntiBot so Fetch falls back instead of giving up.
		if resp.statusCode == http.StatusForbidden || resp.statusCode == http.StatusServiceUnavailable {
			return nil, fmt.Errorf("native: HTTP %d: %w", resp.statusCode, ErrAntiBot)
		}
		return nil, fmt.Errorf("native: HTTP %d", resp.statusCode)
	}

	finalURL := resp.finalURL
	if finalURL == nil {
		finalURL, _ = url.Parse(target)
	}
	article, err := readability.FromReader(resp.body, finalURL)
	if err != nil {
		return nil, fmt.Errorf("native: readability: %w", err)
	}

	if reason := looksLikeLoginWall(article, finalURL, target); reason != "" {
		return nil, fmt.Errorf("native: %w (%s)", ErrLoginWall, reason)
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
		ContentType: "article",
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

// ErrLoginWall is wrapped by tryReadability when the heuristic suggests
// the response is a login/paywall placeholder rather than article content.
// Exported so tests can match it.
var ErrLoginWall = errors.New("login wall or thin content")

// ErrAntiBot is wrapped by tryReadability when the origin returned an HTTP
// status that suggests bot detection rather than a missing or
// auth-required page. Used by Fetch to decide whether Jina might succeed
// where direct fetch failed. Distinct from ErrLoginWall so callers can
// log the two cases separately.
var ErrAntiBot = errors.New("origin blocked the request (likely anti-bot)")

// looksLikeLoginWall mirrors the JS impl's heuristics in samsar/web-to-markdown:
//   - missing article entirely
//   - extracted text < 500 characters
//   - title starts with "sign in"/"log in"/"join now"/"join linkedin"
//   - redirect to a different host
//   - redirect to a /login, /authwall, /signin, /signup path
//
// Returns the empty string when nothing looks suspicious; otherwise a
// short reason string for diagnostics.
func looksLikeLoginWall(article readability.Article, finalURL *url.URL, sourceURL string) string {
	if article.Node == nil {
		return "no article extracted"
	}

	// Length check: render the text body and count runes.
	var txtBuf bytes.Buffer
	_ = article.RenderText(&txtBuf)
	if utf8Trimmed(txtBuf.String()) < 500 {
		return "extracted text < 500 chars"
	}

	if loginTitleRE.MatchString(article.Title()) {
		return "title looks like a login wall"
	}

	source, err := url.Parse(sourceURL)
	if err == nil && finalURL != nil {
		if finalURL.Hostname() != "" && source.Hostname() != "" &&
			finalURL.Hostname() != source.Hostname() {
			return "redirected to a different host: " + finalURL.Hostname()
		}
		if loginPathRE.MatchString(finalURL.Path) {
			return "redirected to a login/auth path: " + finalURL.Path
		}
	}
	return ""
}

var (
	loginTitleRE = regexp.MustCompile(`(?i)^(sign in|log in|join now|join linkedin)`)
	loginPathRE  = regexp.MustCompile(`(?i)/(login|authwall|signin|signup)\b`)
)

// isHostUnreachable returns true when err represents the host being
// genuinely unreachable (DNS lookup failed, connection refused, no route).
// Anything else — TLS errors, timeouts, EOFs mid-body — is treated as
// generic transient so retries get a chance.
func isHostUnreachable(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// Connection refused / no route shows up here as Op="dial" with
		// the underlying syscall error in opErr.Err. Match by message
		// because syscall.Errno values are platform-specific.
		msg := opErr.Err.Error()
		if strings.Contains(msg, "connection refused") ||
			strings.Contains(msg, "no route to host") ||
			strings.Contains(msg, "network is unreachable") {
			return true
		}
	}
	return false
}

// hostFailureFromError classifies a Fetch error into a HostFailureKind
// suitable for caching, or returns false if the error isn't host-wide
// (e.g. 404, generic timeout, 5xx that might recover quickly).
func hostFailureFromError(err error) (HostFailureKind, bool) {
	switch {
	case errors.Is(err, ErrHostUnreachable):
		return HostFailUnreachable, true
	case errors.Is(err, ErrAntiBot):
		return HostFailAntiBot, true
	case errors.Is(err, ErrLoginWall):
		return HostFailLoginWall, true
	}
	return 0, false
}

// utf8Trimmed returns the character count after trimming whitespace at
// both ends. The JS impl uses .trim() + .length; this mirrors that.
func utf8Trimmed(s string) int {
	return len(strings.TrimSpace(s))
}

// tryJina is pass 2: hit r.jina.ai/<url> with retries. Returns parsed
// markdown + extracted metadata in the Result.
func (n *Native) tryJina(ctx context.Context, target string) (*Result, error) {
	retryable := map[int]struct{}{
		http.StatusTooManyRequests:     {},
		http.StatusInternalServerError: {},
		http.StatusBadGateway:          {},
		http.StatusServiceUnavailable:  {},
		http.StatusGatewayTimeout:      {},
	}

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<attempt) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		resp, err := n.rt.do(ctx, n.jinaBaseURL+target, []header{
			{"user-agent", n.userAgent},
			{"accept", "text/plain"},
		})
		if err != nil {
			lastErr = fmt.Errorf("jina: %w", err)
			continue
		}
		body, _ := io.ReadAll(resp.body)
		_ = resp.body.Close()

		if resp.statusCode >= 200 && resp.statusCode < 300 {
			parsed := parseJina(string(body))
			if len(parsed.body) < 200 {
				return nil, fmt.Errorf("jina returned too little content (%d chars)", len(parsed.body))
			}
			result := &Result{
				Markdown:    parsed.body,
				FinalURL:    parsed.urlSource,
				ContentType: "article",
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

		lastErr = fmt.Errorf("jina: HTTP %d", resp.statusCode)
		if _, ok := retryable[resp.statusCode]; !ok {
			break
		}
		n.log.Info("jina retry", "status", resp.statusCode, "attempt", attempt+1)
	}
	if lastErr == nil {
		lastErr = errors.New("jina: unknown error")
	}
	return nil, lastErr
}

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
	headerRE := regexp.MustCompile(`^([A-Z][A-Za-z ]+):\s*(.*)$`)
	meta := map[string]string{}
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "Markdown Content:" {
			i++
			break
		}
		m := headerRE.FindStringSubmatch(line)
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
