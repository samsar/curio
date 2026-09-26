package fetcher

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tlsclient "github.com/bogdanfinn/tls-client"
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
