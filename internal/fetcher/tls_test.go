package fetcher

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCA is a certificate authority made up for one test, so TLS tests
// choose their certificates' names and validity instead of relying on
// httptest's fixed one.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool // trusts this CA only
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "curio test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

// leaf issues a server certificate for dnsNames, valid from notBefore to
// notAfter.
func (ca *testCA) leaf(t *testing.T, notBefore, notAfter time.Time, dnsNames ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// validLeaf issues a certificate for dnsNames that is valid now.
func (ca *testCA) validLeaf(t *testing.T, dnsNames ...string) tls.Certificate {
	t.Helper()
	return ca.leaf(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), dnsNames...)
}

// newTLSServer serves h over HTTPS with cert, offering HTTP/2 as well as
// HTTP/1.1. Handshake failures are expected in these tests and are not
// logged.
func newTLSServer(t *testing.T, cert tls.Certificate, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// routeDialer dials by table, from "host:port" to a local listener's
// address. Any other address fails, so no test dial leaves the machine or
// asks DNS.
func routeDialer(routes map[string]string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		to, ok := routes[addr]
		if !ok {
			return nil, fmt.Errorf("test dialer: no route to %s", addr)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, to)
	}
}

// newRoutedChromeRT builds the production chrome backend (latest profile)
// with its dials routed by routes and roots trusted.
func newRoutedChromeRT(t *testing.T, roots *x509.CertPool, routes map[string]string) *chromeRT {
	t.Helper()
	rt, err := newChromeRT(5*time.Second, chromeProfiles[0],
		tlsclient.WithDialContext(routeDialer(routes)),
		tlsclient.WithTransportOptions(&tlsclient.TransportOptions{RootCAs: roots}),
	)
	require.NoError(t, err)
	return rt
}

// certRoundTrippers builds each backend with its dials routed by routes
// and only roots trusted.
func certRoundTrippers(t *testing.T, roots *x509.CertPool, routes map[string]string) map[string]roundTripper {
	t.Helper()
	return map[string]roundTripper{
		"stock": &stockRT{client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
			DialContext:     routeDialer(routes),
			TLSClientConfig: &tls.Config{RootCAs: roots},
		}}},
		"chrome": newRoutedChromeRT(t, roots, routes),
	}
}

// TestRoundTrippers_CertificateFailures: every way a certificate fails
// verification is ErrTLSCertificate on both backends, with the x509 cause
// and the *url.Error still reachable. The chrome backend's uTLS reports it
// with a type of its own, which crypto/tls's doesn't match.
func TestRoundTrippers_CertificateFailures(t *testing.T) {
	ca := newTestCA(t)
	stranger := newTestCA(t) // an authority nobody here trusts
	now := time.Now()
	isExpired := func(err error) bool {
		var e x509.CertificateInvalidError
		return errors.As(err, &e) && e.Reason == x509.Expired
	}
	cases := []struct {
		name  string
		cert  tls.Certificate
		cause func(error) bool
	}{
		{"expired", ca.leaf(t, now.Add(-2*time.Hour), now.Add(-time.Hour), "example.com"), isExpired},
		{"not yet valid", ca.leaf(t, now.Add(time.Hour), now.Add(2*time.Hour), "example.com"), isExpired},
		{"wrong hostname", ca.validLeaf(t, "other.example"), func(err error) bool {
			var e x509.HostnameError
			return errors.As(err, &e)
		}},
		{"unknown authority", stranger.validLeaf(t, "example.com"), func(err error) bool {
			var e x509.UnknownAuthorityError
			return errors.As(err, &e)
		}},
	}
	for _, tc := range cases {
		srv := newTLSServer(t, tc.cert, http.NotFoundHandler())
		routes := map[string]string{"example.com:443": srv.Listener.Addr().String()}
		for backend, rt := range certRoundTrippers(t, ca.pool, routes) {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				_, err := rt.do(t.Context(), "https://example.com/", nil)
				require.ErrorIs(t, err, ErrTLSCertificate)
				assert.True(t, tc.cause(err), "x509 cause not reachable: %v", err)
				var ue *url.Error
				assert.ErrorAs(t, err, &ue)
			})
		}
	}
}

// TestNative_UntrustedCertificateIsPermanent: an origin whose certificate
// fails verification fails permanently on the first attempt, saying why,
// without Jina and without a host-cache entry: the next URL on the host
// makes its own handshake.
func TestNative_UntrustedCertificateIsPermanent(t *testing.T) {
	for _, backend := range []string{"stock", "chrome"} {
		t.Run(backend, func(t *testing.T) {
			var handshakes atomic.Int32
			origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, makeArticleHTML("Unreachable", ""))
			}))
			origin.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					handshakes.Add(1)
				}
			}
			origin.Config.ErrorLog = log.New(io.Discard, "", 0)
			origin.StartTLS()
			defer origin.Close()
			var jinaHits atomic.Int32
			jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				jinaHits.Add(1)
				_, _ = io.WriteString(w, jinaArticleBody())
			}))
			defer jina.Close()

			n := unpaced(NewNative(NativeOptions{
				Timeout: 5 * time.Second, Backend: backend, JinaFallback: true, JinaBaseURL: jina.URL + "/",
			}), newFakeClock())
			_, err := n.Fetch(t.Context(), origin.URL+"/a")
			var pe *PermanentError
			require.ErrorAs(t, err, &pe)
			assert.ErrorIs(t, err, ErrTLSCertificate)
			assert.Contains(t, err.Error(), "x509:")
			assert.Zero(t, jinaHits.Load(), "Jina must not fetch past a failed certificate check")
			assert.False(t, hostCached(n, hostOf(origin.URL)))

			before := handshakes.Load()
			_, err = n.Fetch(t.Context(), origin.URL+"/b")
			require.ErrorIs(t, err, ErrTLSCertificate)
			assert.NotContains(t, err.Error(), "(cached:")
			assert.Greater(t, handshakes.Load(), before, "the next URL must make its own handshake")
		})
	}
}

// TestNative_OtherTLSFailuresStayRetryable: TLS trouble that says nothing
// about the certificate is not ErrTLSCertificate, and is retried.
func TestNative_OtherTLSFailuresStayRetryable(t *testing.T) {
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	servers := map[string]string{
		"plain http listener":         plain.Listener.Addr().String(),
		"closed during the handshake": hangUpListener(t),
	}
	for name, addr := range servers {
		for _, backend := range []string{"stock", "chrome"} {
			t.Run(backend+"/"+name, func(t *testing.T) {
				n := NewNative(NativeOptions{Timeout: 5 * time.Second, Backend: backend})
				_, err := n.Fetch(t.Context(), "https://"+addr+"/")
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrTLSCertificate)
				var pe *PermanentError
				assert.False(t, errors.As(err, &pe), "must stay retryable: %v", err)
			})
		}
	}
}

// hangUpListener returns the address of a listener that closes each
// connection as soon as the client's first bytes (its ClientHello)
// arrive.
func hangUpListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			_, _ = conn.Read(make([]byte, 1))
			_ = conn.Close()
		}
	}()
	return ln.Addr().String()
}

// TestNative_JinaCertificateFailureIsJinasTrouble: a certificate failure
// talking to Jina says nothing about the target. The fetch stays
// retryable and nothing is host-cached.
func TestNative_JinaCertificateFailureIsJinasTrouble(t *testing.T) {
	origin := serveThinPage(t)
	defer origin.Close()
	jina := httptest.NewUnstartedServer(http.NotFoundHandler())
	jina.Config.ErrorLog = log.New(io.Discard, "", 0)
	jina.StartTLS()
	defer jina.Close()

	for _, backend := range []string{"stock", "chrome"} {
		t.Run(backend, func(t *testing.T) {
			n := unpaced(NewNative(NativeOptions{
				Timeout: 5 * time.Second, Backend: backend, JinaFallback: true, JinaBaseURL: jina.URL + "/",
			}), newFakeClock())
			_, err := n.Fetch(t.Context(), origin.URL)
			require.ErrorIs(t, err, ErrLoginWall)
			assert.ErrorIs(t, err, ErrTLSCertificate)
			var pe *PermanentError
			assert.False(t, errors.As(err, &pe), "Jina's trouble must leave the fetch retryable: %v", err)
			assert.False(t, hostCached(n, hostOf(origin.URL)))
		})
	}
}
