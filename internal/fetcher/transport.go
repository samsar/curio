package fetcher

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
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
	// caller. Redirects are followed; finalURL is the settled URL.
	do(ctx context.Context, target string, headers []header) (*fetchResponse, error)
}

// header is one outbound request header. The slice order is the wire order
// the chrome backend reproduces; the stock backend ignores it (net/http
// canonicalizes and sorts anyway).
type header struct{ key, value string }

// fetchResponse is the backend-agnostic slice of an HTTP response the
// fetcher actually consumes.
type fetchResponse struct {
	statusCode int
	body       io.ReadCloser
	finalURL   *url.URL
}

// newRoundTripper builds the backend named by backend:
//
//   - "stock" / "go" / "net/http" → stockRT
//   - "" / "chrome" / "chrome_<ver>" → chromeRT with that profile
//     (empty and unknown-but-chrome-ish names use the latest known profile)
//
// Returns an error only when a chrome backend was requested and tls-client
// init failed; callers may then fall back to stock.
func newRoundTripper(backend string, timeout time.Duration, log *slog.Logger) (roundTripper, error) {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "stock", "go", "net/http":
		return newStockRT(timeout), nil
	default:
		return newChromeRT(timeout, backend, log)
	}
}

// stockRT is the net/http backend.
type stockRT struct{ client *http.Client }

func newStockRT(timeout time.Duration) *stockRT {
	// Follow redirects (net/http does up to 10 by default) so finalURL is
	// what the server settled on.
	return &stockRT{client: &http.Client{Timeout: timeout}}
}

func (s *stockRT) name() string { return "stock" }

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
	// Body deliberately escapes to the caller via fetchResponse.body, which
	// the caller closes — bodyclose can't see across the abstraction.
	resp, err := s.client.Do(req) //nolint:bodyclose
	if err != nil {
		return nil, err
	}
	return &fetchResponse{
		statusCode: resp.StatusCode,
		body:       resp.Body,
		finalURL:   resp.Request.URL,
	}, nil
}

// chromeRT is the uTLS + HTTP/2 fingerprint backend.
type chromeRT struct {
	client  tlsclient.HttpClient
	profile string
}

func newChromeRT(timeout time.Duration, profileName string, log *slog.Logger) (*chromeRT, error) {
	prof, name, ok := chromeProfile(profileName)
	if !ok {
		if log != nil {
			log.Warn("unknown chrome profile, using latest", "requested", profileName, "using", name)
		}
	}
	secs := int(timeout / time.Second)
	if secs <= 0 {
		secs = 30
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithClientProfile(prof),
		tlsclient.WithTimeoutSeconds(secs),
		// Redirects followed by default; finalURL reflects the settled URL.
	)
	if err != nil {
		return nil, fmt.Errorf("tls-client init (profile %s): %w", name, err)
	}
	return &chromeRT{client: client, profile: name}, nil
}

func (c *chromeRT) name() string { return "chrome:" + c.profile }

func (c *chromeRT) do(ctx context.Context, target string, headers []header) (*fetchResponse, error) {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	// fhttp's Header.Add records each key into HeaderOrderKey as it goes, so
	// adding in Chrome's order reproduces Chrome's header order on the wire.
	// Pseudo-header order and H2 SETTINGS come from the client profile.
	for _, h := range headers {
		req.Header.Add(h.key, h.value)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	// fhttp's HTTP/2 transport always decompresses by Content-Encoding
	// (gzip/deflate/br/zstd), so a faithful Accept-Encoding is safe here.
	return &fetchResponse{
		statusCode: resp.StatusCode,
		body:       resp.Body,
		finalURL:   resp.Request.URL,
	}, nil
}

// chromeProfile maps a config string to a tls-client profile. The latest
// known profile is the default for "", "chrome", and any unrecognized name
// (ok=false signals the fallback so the caller can log it).
func chromeProfile(name string) (profile profiles.ClientProfile, label string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "chrome", "chrome_latest":
		return profiles.Chrome_133, "chrome_133", true
	case "chrome_133":
		return profiles.Chrome_133, "chrome_133", true
	case "chrome_131":
		return profiles.Chrome_131, "chrome_131", true
	case "chrome_124":
		return profiles.Chrome_124, "chrome_124", true
	case "chrome_120":
		return profiles.Chrome_120, "chrome_120", true
	default:
		return profiles.Chrome_133, "chrome_133", false
	}
}
