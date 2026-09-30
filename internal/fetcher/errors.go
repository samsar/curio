package fetcher

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// PermanentError signals the job system not to retry: the failure is a
// verdict about the URL that another attempt can't change. Every other
// error a fetcher returns is retried with backoff.
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// DeferError is a fetch held back without a verdict. Either curio chose not
// to make a call, because a hold it keeps itself (a cooldown every caller
// of an upstream shares, a site's turn, a host-cache entry it waits out)
// outlasts what a fetch sits out inline; or an upstream answered that it
// will serve again at a time it names (Jina Reader's block of a site),
// which decides nothing about the page. Nothing was learned about the URL,
// and the fetch should run again at Until. It is never a PermanentError,
// and no fetcher wraps it in one. Until is never zero. Reason says what the
// call waits for, for a person, worded to follow "waiting for" ("GitHub's
// API rate limit to reset"). Err is the error the fetch fails with once the
// job may wait no longer: its text and chain (a 429 and its time left, the
// block) are what an ordinary retryable failure would carry.
type DeferError struct {
	Until  time.Time
	Reason string
	Err    error
}

func (e *DeferError) Error() string { return e.Err.Error() }
func (e *DeferError) Unwrap() error { return e.Err }

// heldBack is the error of a call cooldown c holds past the caller's inline
// cap: a *DeferError until the cooldown ends, for reason, around err.
func heldBack(c *cooldown, reason string, err error) *DeferError {
	return &DeferError{Until: c.deadline(), Reason: reason, Err: err}
}

// Sentinels callers branch on with errors.Is. They say why a fetch failed;
// whether it is retried is decided separately (PermanentError).
var (
	// ErrLoginWall marks a response that came back but looks like a
	// login/paywall placeholder or thin content rather than the article.
	// Jina may get through, except past a redirect onto another site's
	// login page, whatever that page answered: Jina follows the same
	// redirect, so that one is final.
	ErrLoginWall = errors.New("login wall or thin content")

	// ErrAntiBot marks an answer that suggests bot detection rather than a
	// missing or auth-required page: HTTP 403 or 503, or a challenge, block
	// or 403/503 error page served with a 2xx (or by Jina, without the
	// target's status). Jina may get through. Distinct from ErrLoginWall so
	// the two log separately. Only a 403/503 from the origin is host-wide,
	// and cached: the host's other pages then go to Jina without asking the
	// origin, for a while. A challenge or error page is about that page. A
	// 403/503 answered after a redirect onto another site's login path, or
	// with dead-link detection on onto the homepage or another site's
	// landing page, is no ErrAntiBot: the redirect is judged instead
	// (Native's statusFailure).
	ErrAntiBot = errors.New("origin blocked the request (likely anti-bot)")

	// ErrDeadLink marks a URL whose content is gone: a hard 404/410 (from
	// the origin, or reported by Jina for the target), or a "soft 404": HTTP
	// 200 carrying a not-found page, or a redirect that settled on the
	// site's homepage or on another site's landing page, whatever that page
	// answered: 2xx, 403 or 503. Always wrapped in a PermanentError; a dead
	// link the origin reports is never routed to Jina. Never host-cached: a
	// dead path says nothing about the rest of the host, nor a landing page
	// about the site that redirected there or the site it belongs to.
	ErrDeadLink = errors.New("dead link (content is gone)")

	// ErrHostUnreachable marks a host that doesn't resolve or refuses
	// connections. Jina is skipped: it can't reach the host either.
	ErrHostUnreachable = errors.New("host unreachable")

	// ErrTooLarge marks a response body over maxResponseBytes (after
	// decompression). Permanent: the same URL will be just as big next
	// time. The one exception is a PDF over the cap, which goes to Jina
	// (errPDFUnreadable): when Jina fails for its own reasons or reports a
	// transient target status, the error still matches ErrTooLarge but
	// stays retryable, like any Jina failure that is no verdict.
	ErrTooLarge = errors.New("response too large")

	// ErrTLSCertificate marks a server certificate that failed
	// verification: expired, not yet valid, issued for another name, or
	// from an authority the system doesn't trust. From the origin it is
	// always wrapped in a PermanentError (no retry inside the backoff
	// window renews a certificate) and the document goes failed, not dead:
	// certificates get fixed. Never sent to Jina, which would fetch past
	// the check curio refuses to skip. Never host-cached, so a refetch after
	// the fix goes straight out. Other TLS failures (alerts, resets
	// mid-handshake, a non-TLS answer) stay retryable.
	ErrTLSCertificate = errors.New("invalid TLS certificate")

	// ErrUnsupported marks a URL or a response no fetcher reads: a YouTube
	// URL that names no video (a channel page), a GitHub URL of a page the
	// GitHub fetcher doesn't read (a profile, a repository's actions), a
	// response that is neither HTML nor a PDF. Always wrapped in a
	// PermanentError: the same URL is as unreadable next time.
	ErrUnsupported = errors.New("unsupported")
)

// HTTPStatusError is a non-2xx answer from an upstream. URL is the URL that
// answered (after redirects), so a verdict is attributed to the host that
// gave it. RetryAfter is the server's back-off hint; zero when it gave none.
type HTTPStatusError struct {
	StatusCode int
	URL        string
	RetryAfter time.Duration
}

func (e *HTTPStatusError) Error() string {
	if text := http.StatusText(e.StatusCode); text != "" {
		return fmt.Sprintf("HTTP %d %s", e.StatusCode, text)
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

// retryableStatus reports whether another attempt could get a different
// answer: request timeouts, 421/425, rate limits and server errors. 501 and
// 505 are server errors that describe the request itself, so they are as
// deterministic as the 4xx family.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusMisdirectedRequest, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	case http.StatusNotImplemented, http.StatusHTTPVersionNotSupported:
		return false
	}
	return code >= 500 && code <= 599
}

// statusError returns err, an HTTP failure with status code, as is when the
// status is worth retrying and as a *PermanentError otherwise.
func statusError(code int, err error) error {
	if retryableStatus(code) {
		return err
	}
	return &PermanentError{Err: err}
}

// maxRetryAfter bounds a Retry-After hint. Longer hints are clamped rather
// than trusted: a hint becomes a cooldown that defers every fetch it holds
// (DeferError), and the job queue lets a job wait a day at most.
const maxRetryAfter = 24 * time.Hour

// parseRetryAfter reads a Retry-After header given as delta-seconds or as an
// HTTP-date relative to now. ok is false when the header is absent or
// malformed. The result is never negative: a date in the past means "now".
func parseRetryAfter(h http.Header, now time.Time) (d time.Duration, ok bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(min(secs, int64(maxRetryAfter/time.Second))) * time.Second, true
	}
	when, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	return min(max(when.Sub(now), 0), maxRetryAfter), true
}

// maxErrorBody caps how much of a response body an error message quotes.
// Error text lands in jobs.last_error; a whole HTML error page doesn't
// belong there.
const maxErrorBody = 512

// snippet returns at most maxErrorBody bytes of body for an error message,
// cut on a rune boundary.
func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) <= maxErrorBody {
		return s
	}
	cut := maxErrorBody
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// isNotFound reports whether err is an HTTP 404 answer.
func isNotFound(err error) bool {
	var se *HTTPStatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}
