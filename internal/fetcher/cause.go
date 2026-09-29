package fetcher

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/samsar/curio/internal/store"
)

// FailureCause is the cause a document records when its fetch gives up with
// err: what the site did, read from err's chain. It is "" for nil, and one
// of store.FailureCauses otherwise, dead_link exactly when err is a dead
// link (errors.Is ErrDeadLink), the rule that makes a document dead.
//
// A dead link anywhere in the chain wins: the content is gone, whoever said
// so. Otherwise, after a Jina fallback (jinaFallbackError), one of the two
// paths speaks for the target: Jina when its failure is a verdict about the
// target (it answered with something that isn't the page, refused the
// target, reported a status the target gave it, or sent a body over the
// cap), and the origin when it is Jina's own trouble (its rate limit, an
// outage, its CDN challenging curio, our account), which says nothing about
// the site. A document is grouped by what the site did, as the fallback
// policy reads it.
//
// The error that speaks is classified by the first of these it matches:
//
//   - errJinaRefused: jina_refused
//   - ErrTLSCertificate: tls
//   - ErrHostUnreachable: unreachable
//   - ErrTooLarge: too_large
//   - ErrAntiBot: anti_bot
//   - ErrLoginWall: login_wall
//   - ErrUnsupported, ErrFetcherNotFound or errPDFUnreadable: unsupported
//   - errRateLimited, or a 429 status: rate_limited
//   - any other status, the origin's or one the target gave Jina, or an
//     error page naming one (errServerErrorPage): http_error
//   - a deadline or a network timeout: timeout
//   - any other transport failure (a reset, a TLS alert, a redirect loop,
//     a body cut short): network
//   - anything else: other
//
// A new sentinel or failure path belongs in this list.
func FailureCause(err error) store.FailureCause {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrDeadLink):
		return store.FailureCauseDeadLink
	}
	return classify(speaker(err))
}

// speaker is the part of err that speaks for the target: after a Jina
// fallback, Jina's failure when it is a verdict about the target and the
// origin's otherwise; err itself when Jina was never asked.
func speaker(err error) error {
	var fb *jinaFallbackError
	if !errors.As(err, &fb) {
		return err
	}
	if jinaAnswered(fb.jina) || errors.Is(fb.jina, errJinaTargetTrouble) || errors.Is(fb.jina, ErrTooLarge) {
		return fb.jina
	}
	return fb.origin
}

// classify is FailureCause's list, over the error that speaks for the
// target.
func classify(err error) store.FailureCause {
	switch code := statusCode(err); {
	case errors.Is(err, errJinaRefused):
		return store.FailureCauseJinaRefused
	case errors.Is(err, ErrTLSCertificate):
		return store.FailureCauseTLS
	case errors.Is(err, ErrHostUnreachable):
		return store.FailureCauseUnreachable
	case errors.Is(err, ErrTooLarge):
		return store.FailureCauseTooLarge
	case errors.Is(err, ErrAntiBot):
		return store.FailureCauseAntiBot
	case errors.Is(err, ErrLoginWall):
		return store.FailureCauseLoginWall
	case errors.Is(err, ErrUnsupported), errors.Is(err, ErrFetcherNotFound), errors.Is(err, errPDFUnreadable):
		return store.FailureCauseUnsupported
	case errors.Is(err, errRateLimited), code == http.StatusTooManyRequests:
		return store.FailureCauseRateLimited
	case code != 0, errors.Is(err, errServerErrorPage):
		return store.FailureCauseHTTPError
	case timedOut(err):
		return store.FailureCauseTimeout
	case transportFailure(err):
		return store.FailureCauseNetwork
	}
	return store.FailureCauseOther
}

// statusCode is the HTTP status in err's chain: an upstream's own answer,
// or the status the target gave Jina. 0 when there is none.
func statusCode(err error) int {
	if se, ok := errors.AsType[*HTTPStatusError](err); ok {
		return se.StatusCode
	}
	if te, ok := errors.AsType[*targetStatusError](err); ok {
		return te.code
	}
	return 0
}

// timedOut reports whether err is a deadline that passed: a request's or a
// tool's, or a network operation's.
func timedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}

// transportFailure reports whether err is a request that got no whole
// answer: it failed in the transport, or its body was cut short.
func transportFailure(err error) bool {
	if _, ok := errors.AsType[*url.Error](err); ok {
		return true
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}
