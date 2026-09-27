package fetcher

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	utls "github.com/bogdanfinn/utls"

	"github.com/samsar/curio/internal/urlutil"
)

// roundTripper is the HTTP backend the Native fetcher issues GETs through.
//
// Two implementations exist:
//
//   - stockRT — Go's net/http. Correct and dependency-free, but its TLS
//     ClientHello (JA3/JA4) and HTTP/2 SETTINGS frame are the well-known
//     "Go" fingerprint that Cloudflare/Akamai/DataDome blocklist on sight,
//     no matter how browser-like the headers are.
//   - chromeRT — uTLS + a forked HTTP/2 stack (bogdanfinn/tls-client) that
//     parrots a real Chrome handshake. Defeats the JA3 and Akamai-h2
//     fingerprint layers; header order (a weaker JA4H signal) is matched
//     too.
//
// Swapping at this boundary keeps tryReadability/tryJina identical
// regardless of which backend is active, and lets a misbehaving fingerprint
// backend degrade to stock without touching call sites.
type roundTripper interface {
	// name identifies the backend for logs and document_extractions.
	name() string
	// do issues a GET to target with the given headers (order preserved)
	// and returns a normalized response. The body must be closed by the
	// caller. Redirects are followed; finalURL is the settled URL. A server
	// certificate that fails verification is reported as ErrTLSCertificate
	// by both backends, whichever TLS stack they use.
	do(ctx context.Context, target string, headers []header) (*fetchResponse, error)
}

// header is one outbound request header. The slice order is the wire order
// the chrome backend reproduces; the stock backend ignores it (net/http
// canonicalizes and sorts anyway).
type header struct{ key, value string }

// fetchResponse is the backend-agnostic slice of an HTTP response the
// fetcher actually consumes.
type fetchResponse struct {
	statusCode  int
	header      http.Header
	body        io.ReadCloser
	finalURL    *url.URL // never nil
	contentType string   // raw Content-Type header (may include "; charset=...")
}

// maxResponseBytes caps every response body a fetcher reads, measured
// after decompression. Nothing a bookmark points at needs more. Without a
// cap, a gzip bomb or a never-ending text/* stream is read into memory
// until the client timeout, once per fetch worker.
const maxResponseBytes = 32 << 20 // 32 MiB

// limitedBody is a body that fails with ErrTooLarge once more than max
// bytes have been read, rather than quietly ending there: a cut-off body
// must never pass for a complete one.
type limitedBody struct {
	io.ReadCloser
	max      int64
	left     int64
	exceeded bool
}

func newLimitedBody(rc io.ReadCloser, maxBytes int64) *limitedBody {
	return &limitedBody{ReadCloser: rc, max: maxBytes, left: maxBytes}
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.exceeded {
		return 0, b.tooLarge()
	}
	if b.left <= 0 {
		// At the cap, one more byte tells a body of exactly max bytes from
		// a longer one.
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			b.exceeded = true
			return 0, b.tooLarge()
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) tooLarge() error {
	return fmt.Errorf("%w (limit %d bytes)", ErrTooLarge, b.max)
}

// overflow returns the size error when body is a limitedBody that hit its
// cap. For consumers that flatten read errors into strings: go-readability
// wraps them with %v, so errors.Is can't see ErrTooLarge through it.
func overflow(body io.Reader) error {
	if b, ok := body.(*limitedBody); ok && b.exceeded {
		return b.tooLarge()
	}
	return nil
}

// readLimited reads all of r, failing with ErrTooLarge past maxBytes.
func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	return io.ReadAll(newLimitedBody(io.NopCloser(r), maxBytes))
}

// bodyLimitRT caps every body its roundTripper returns at max bytes. Both
// backends hand back decompressed streams, so the cap applies to the
// decoded bytes.
type bodyLimitRT struct {
	roundTripper
	max int64
}

func limitBodies(rt roundTripper, maxBytes int64) roundTripper {
	return bodyLimitRT{roundTripper: rt, max: maxBytes}
}

func (l bodyLimitRT) do(ctx context.Context, target string, headers []header) (*fetchResponse, error) {
	resp, err := l.roundTripper.do(ctx, target, headers)
	if err != nil {
		return nil, err
	}
	resp.body = newLimitedBody(resp.body, l.max)
	return resp, nil
}

// newRoundTripper builds the backend named by backend: stockRT for a stock
// name (see isStockBackend), otherwise chromeRT with prof's fingerprint.
//
// Returns an error only when a chrome backend was requested and tls-client
// init failed; callers may then fall back to stock.
func newRoundTripper(backend string, prof chromeProfileSpec, timeout time.Duration) (roundTripper, error) {
	if isStockBackend(backend) {
		return newStockRT(timeout), nil
	}
	return newChromeRT(timeout, prof)
}

// isStockBackend reports whether backend names Go's net/http transport:
// "stock", "go" or "net/http".
func isStockBackend(backend string) bool {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "stock", "go", "net/http":
		return true
	}
	return false
}

// stockRT is the net/http backend.
type stockRT struct{ client *http.Client }

func newStockRT(timeout time.Duration) *stockRT {
	// Follow redirects (net/http does up to 10 by default) so finalURL is
	// what the server settled on.
	return &stockRT{client: &http.Client{Timeout: timeout}}
}

func (*stockRT) name() string { return "stock" }

func (s *stockRT) do(ctx context.Context, target string, headers []header) (*fetchResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	for _, h := range headers {
		// Skip Accept-Encoding: setting it by hand disables net/http's
		// transparent gzip (it only decompresses when the transport itself
		// added the header). Letting net/http negotiate keeps the body
		// readable — fidelity isn't this backend's job anyway.
		if strings.EqualFold(h.key, "accept-encoding") {
			continue
		}
		req.Header.Set(h.key, h.value)
	}
	resp, err := s.client.Do(req) //nolint:bodyclose // the body escapes in fetchResponse.body, which the caller closes
	if err != nil {
		var cve *tls.CertificateVerificationError
		if errors.As(err, &cve) {
			return nil, fmt.Errorf("%w: %w", ErrTLSCertificate, err)
		}
		return nil, err
	}
	return &fetchResponse{
		statusCode:  resp.StatusCode,
		header:      resp.Header,
		body:        resp.Body,
		finalURL:    resp.Request.URL,
		contentType: resp.Header.Get("Content-Type"),
	}, nil
}

// chromeRT is the uTLS + HTTP/2 fingerprint backend.
type chromeRT struct {
	client  tlsclient.HttpClient
	profile string
}

// newChromeRT builds the chrome backend with prof's fingerprint. extra is
// applied after curio's own options; production passes none, tests route
// dials and trust their own roots through it.
func newChromeRT(timeout time.Duration, prof chromeProfileSpec, extra ...tlsclient.HttpClientOption) (*chromeRT, error) {
	secs := int(timeout / time.Second)
	if secs <= 0 {
		secs = 30
	}
	opts := append([]tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(prof.tls),
		tlsclient.WithTimeoutSeconds(secs),
		// Redirects are followed, so finalURL is the settled URL.
		tlsclient.WithCustomRedirectFunc(chromeCheckRedirect),
	}, extra...)
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), opts...)
	if err != nil {
		return nil, fmt.Errorf("tls-client init (profile %s): %w", prof.name, err)
	}
	return &chromeRT{client: client, profile: prof.name}, nil
}

func (c *chromeRT) name() string { return "chrome:" + c.profile }

func (c *chromeRT) do(ctx context.Context, target string, headers []header) (*fetchResponse, error) {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	pinPlainHTTPPort(req)
	// fhttp's Header.Add records each key into HeaderOrderKey as it goes, so
	// adding in Chrome's order reproduces Chrome's header order on the wire.
	// Pseudo-header order and H2 SETTINGS come from the client profile.
	for _, h := range headers {
		req.Header.Add(h.key, h.value)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, chromeError(err)
	}
	// fhttp auto-decompresses by Content-Encoding on its HTTP/2 path (and
	// gzip on HTTP/1.1), but NOT on HTTP/3 — and the Chrome profile
	// negotiates h3 (QUIC) with CDNs that advertise it, so a faithful
	// "gzip, deflate, br, zstd" can arrive raw. Decompress defensively when
	// the transport didn't: Uncompressed stays false and a Content-Encoding
	// remains. On h2/h1-gzip Uncompressed is already true, so no
	// double-decompress.
	if !resp.Uncompressed && resp.Header.Get("Content-Encoding") != "" {
		resp.Body = fhttp.DecompressBody(resp)
	}
	return &fetchResponse{
		statusCode:  resp.StatusCode,
		header:      http.Header(resp.Header), // same map[string][]string shape
		body:        resp.Body,
		finalURL:    urlutil.StripDefaultPort(resp.Request.URL),
		contentType: resp.Header.Get("Content-Type"),
	}, nil
}

// pinPlainHTTPPort gives a portless http:// request an explicit :80,
// leaving its Host header port-free.
//
// tls-client caches one transport per host:port and, for a URL without a
// port, uses host:443 whatever the scheme (roundtripper.go,
// getDialTLSAddr). http://h/ and https://h/ would then share one cached
// transport: whichever scheme reaches a host second fails with "http2:
// unsupported scheme" or "protocol negotiated", and the dial path writes
// that cache without its lock. An explicit port gives each scheme its own
// entry. It can go once tls-client keys transports by scheme; see
// docs/decisions.md, "Chrome backend: plain http carries an explicit :80".
//
// A URL without a host is left for fhttp to refuse: pinned, it would name
// ":80", which Go dials on the local machine.
func pinPlainHTTPPort(req *fhttp.Request) {
	if !strings.EqualFold(req.URL.Scheme, "http") || req.URL.Port() != "" || req.URL.Hostname() == "" {
		return
	}
	if req.Host == "" {
		req.Host = req.URL.Host
	}
	req.URL.Host = net.JoinHostPort(req.URL.Hostname(), "80")
}

// maxRedirects is the limit of fhttp's default redirect policy, which
// chromeCheckRedirect replaces.
const maxRedirects = 10

// errNoHost is the error fhttp and net/http give a URL without a host.
var errNoHost = errors.New("http: no Host in request URL")

// chromeCheckRedirect is the chrome backend's redirect policy: fhttp's
// default limit, and the :80 pin on every hop (see pinPlainHTTPPort).
// fhttp calls it once the next hop is built and before it is sent. By then
// it has set the hop's Referer from the previous hop's URL, which may carry
// the pin. It has also carried the previous hop's Host over to a Location
// without a scheme, taking a Host that differs from the URL for one the
// caller chose; the pin makes them differ, and a scheme-relative Location
// (//www.example.com/post) names another host. curio never chooses a Host,
// so every hop takes its own from its URL.
//
// A hop without a host (Location: http:///x or https:///x) is refused here
// with the stock backend's error. fhttp refuses one itself only after
// tls-client has dialed an https hop's address, ":443" for no host, which
// Go dials on the local machine.
func chromeCheckRedirect(req *fhttp.Request, via []*fhttp.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Host == "" {
		return errNoHost
	}
	req.Host = urlutil.StripDefaultPort(req.URL).Host
	pinPlainHTTPPort(req)
	if ref := req.Header.Get("Referer"); ref != "" {
		req.Header.Set("Referer", withoutDefaultPort(ref))
	}
	return nil
}

// chromeError normalizes a failed chrome request: the URL a *url.Error
// names loses the :80 pin, and a certificate that failed verification is
// tagged ErrTLSCertificate. uTLS reports that with its own
// CertificateVerificationError type, which crypto/tls's doesn't match.
func chromeError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = withoutDefaultPort(ue.URL)
	}
	var cve *utls.CertificateVerificationError
	if errors.As(err, &cve) {
		return fmt.Errorf("%w: %w", ErrTLSCertificate, err)
	}
	return err
}

// withoutDefaultPort returns rawURL without an explicit default port. A
// URL without one, or one that doesn't parse, is returned as is.
func withoutDefaultPort(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if stripped := urlutil.StripDefaultPort(u); stripped.Host != u.Host {
		return stripped.String()
	}
	return rawURL
}

// chromeProfileSpec is one Chrome version curio can impersonate. The
// TLS/HTTP2 fingerprint, the User-Agent and sec-ch-ua all come from the
// same entry: a fingerprint that says one version next to headers that say
// another is itself a mismatch bot checks flag.
type chromeProfileSpec struct {
	name      string // the fetcher.native.backend value, e.g. "chrome_133"
	tls       profiles.ClientProfile
	major     int
	userAgent string
	// secChUA is the header as real Chrome of this major version sends it.
	// The GREASE brand and the brand order change with the version, so
	// these are copied, not generated.
	secChUA string
}

// chromeProfiles lists the supported profiles, latest first.
var chromeProfiles = []chromeProfileSpec{
	{"chrome_133", profiles.Chrome_133, 133, chromeUA(133), `"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"`},
	{"chrome_131", profiles.Chrome_131, 131, chromeUA(131), `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`},
	{"chrome_124", profiles.Chrome_124, 124, chromeUA(124), `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`},
	{"chrome_120", profiles.Chrome_120, 120, chromeUA(120), `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`},
}

// chromeUA is desktop Chrome's User-Agent on macOS. Chrome reports only
// the major version; the rest is frozen at 0.0.0.
func chromeUA(major int) string {
	return fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
		"(KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", major)
}

// chromeProfile maps a backend name to its profile. The latest profile is
// the answer for "", "chrome", "chrome_latest", and any unrecognized name
// (ok=false signals the fallback so the caller can log it).
func chromeProfile(name string) (prof chromeProfileSpec, ok bool) {
	switch n := strings.ToLower(strings.TrimSpace(name)); n {
	case "", "chrome", "chrome_latest":
		return chromeProfiles[0], true
	default:
		for _, p := range chromeProfiles {
			if p.name == n {
				return p, true
			}
		}
		return chromeProfiles[0], false
	}
}
