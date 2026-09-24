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

// Sentinels callers branch on with errors.Is. They say why a fetch failed;
// whether it is retried is decided separately (PermanentError).
var (
	// ErrLoginWall marks a response that came back but looks like a
	// login/paywall placeholder or thin content rather than the article.
	// Jina may get through.
	ErrLoginWall = errors.New("login wall or thin content")

	// ErrAntiBot marks an origin answer that suggests bot detection (HTTP
	// 403 or 503) rather than a missing or auth-required page. Jina may get
	// through. Distinct from ErrLoginWall so the two log separately.
	ErrAntiBot = errors.New("origin blocked the request (likely anti-bot)")

	// ErrDeadLink marks a URL whose content is gone: a hard 404/410, or a
	// "soft 404" (HTTP 200 carrying a not-found page). Always wrapped in a
	// PermanentError and never routed to Jina. Never host-cached: a dead
	// path says nothing about the rest of the host.
	ErrDeadLink = errors.New("dead link (content is gone)")

	// ErrHostUnreachable marks a host that doesn't resolve or refuses
	// connections. Jina is skipped: it can't reach the host either.
	ErrHostUnreachable = errors.New("host unreachable")

	// ErrTooLarge marks a response body over maxResponseBytes (after
	// decompression). Always permanent: the same URL will be just as big
	// next time.
	ErrTooLarge = errors.New("response too large")
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
// than trusted: the job queue's own backoff tops out well below this.
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
