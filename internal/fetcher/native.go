package fetcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	client       *http.Client
	userAgent    string
	jinaFallback bool
	jinaBaseURL  string // override for tests
	log          *slog.Logger
}

// NativeOptions configures Native. Zero-value fields use defaults.
type NativeOptions struct {
	Timeout      time.Duration
	UserAgent    string
	JinaFallback bool
	JinaBaseURL  string // default https://r.jina.ai/
	Log          *slog.Logger
}

const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/124.0 Safari/537.36"

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
	return &Native{
		client: &http.Client{
			Timeout: opts.Timeout,
			// Follow redirects so finalURL is what the server settled on.
			// http.Client follows up to 10 by default; that's fine here.
		},
		userAgent:    opts.UserAgent,
		jinaFallback: opts.JinaFallback,
		jinaBaseURL:  opts.JinaBaseURL,
		log:          opts.Log,
	}
}

func (n *Native) Name() string { return "native" }

func (n *Native) Fetch(ctx context.Context, target string) (*Result, error) {
	if strings.TrimSpace(target) == "" {
		return nil, errors.New("native: url is empty")
	}

	// Pass 1: direct fetch + Readability.
	direct, directErr := n.tryReadability(ctx, target)
	if directErr == nil {
		return direct, nil
	}

	if !n.jinaFallback {
		return nil, directErr
	}

	n.log.Info("readability failed, falling back to jina",
		"url", target, "err", directErr.Error())

	jina, err := n.tryJina(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("both readability and jina failed (readability: %v) (jina: %w)",
			directErr, err)
	}
	return jina, nil
}

// tryReadability does pass 1: fetch HTML, run Readability, render to
// markdown. Returns ErrLoginWall (wrapped) on any of the login-wall
// heuristics so callers can distinguish "page is paywalled" from "page
// failed to fetch."
func (n *Native) tryReadability(ctx context.Context, target string) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("native: build request: %w", err)
	}
	req.Header.Set("User-Agent", n.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := n.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("native: fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Drain a bit so the connection can be reused.
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		return nil, fmt.Errorf("native: HTTP %d", resp.StatusCode)
	}

	finalURL := resp.Request.URL
	article, err := readability.FromReader(resp.Body, finalURL)
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
			"via":  "readability",
			"site": article.SiteName(),
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

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.jinaBaseURL+target, nil)
		if err != nil {
			return nil, fmt.Errorf("jina: build request: %w", err)
		}
		req.Header.Set("User-Agent", n.userAgent)
		req.Header.Set("Accept", "text/plain")

		resp, err := n.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("jina: %w", err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
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

		lastErr = fmt.Errorf("jina: HTTP %d", resp.StatusCode)
		if _, ok := retryable[resp.StatusCode]; !ok {
			break
		}
		n.log.Info("jina retry", "status", resp.StatusCode, "attempt", attempt+1)
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
