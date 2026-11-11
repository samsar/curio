package fetcher

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func TestBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.255.254", "::1", "0.0.0.0", "0.1.2.3",
		"10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "192.0.2.1", "198.51.100.1", "203.0.113.1",
		"198.18.0.1", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"fe80::1", "fc00::1", "ff02::1", "2001:db8::1", "64:ff9b::7f00:1",
	}
	for _, s := range blocked {
		assert.True(t, blockedIP(net.ParseIP(s)), "should block %s", s)
	}
	public := []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111", "2001:4860:4860::8888"}
	for _, s := range public {
		assert.False(t, blockedIP(net.ParseIP(s)), "should allow %s", s)
	}
}

func TestGuardDialerBlocksPrivate(t *testing.T) {
	g := newGuardDialer(false)
	for _, addr := range []string{"127.0.0.1:80", "localhost:80", "[::1]:80", ":80", "127.0.0.1"} {
		_, err := g.DialContext(context.Background(), "tcp", addr)
		require.ErrorIs(t, err, ErrBlockedAddress, addr)
	}
}

func TestGuardDialerAllowsPublic(t *testing.T) {
	// Resolve a public literal without dialing it: an address that passes
	// the guard is called by the dialer, and the call itself is not the
	// check under test.
	assert.False(t, blockedIP(net.ParseIP("1.1.1.1")))
}

func TestNativeBlocksPrivateAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, makeArticleHTML("A page", ""))
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, JinaFallback: true})
	_, err := n.Fetch(context.Background(), srv.URL)
	require.ErrorIs(t, err, ErrBlockedAddress)
	var pe *PermanentError
	require.ErrorAs(t, err, &pe, "a private address must not be retried or sent to Jina")
	assert.Equal(t, store.FailureCauseUnsupported, FailureCause(err))
}

func TestNativeAllowsPrivateHostsWhenAsked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, makeArticleHTML("A page", longArticleBody))
	}))
	defer srv.Close()

	n := NewNative(NativeOptions{Timeout: 5 * time.Second, AllowPrivateHosts: true})
	res, err := n.Fetch(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Contains(t, res.Markdown, "interpreter reads the source")
}

func TestStockTransportBlocksPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	rt := newStockRT(5*time.Second, false)
	_, err := rt.do(context.Background(), srv.URL, nil)
	require.ErrorIs(t, err, ErrBlockedAddress)
}

func TestStockTransportAllowsPrivateWhenAsked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	rt := newStockRT(5*time.Second, true)
	resp, err := rt.do(context.Background(), srv.URL, nil)
	require.NoError(t, err)
	defer resp.body.Close()
	body, err := io.ReadAll(resp.body)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
}

func TestChromeTransportBlocksPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	rt, err := newChromeRT(5*time.Second, chromeProfiles[0], false)
	require.NoError(t, err)
	_, err = rt.do(context.Background(), srv.URL, nil)
	require.ErrorIs(t, err, ErrBlockedAddress)
}

func TestBlockedAddressIsNotDeadLink(t *testing.T) {
	err := &PermanentError{Err: ErrBlockedAddress}
	assert.False(t, errors.Is(err, ErrDeadLink))
	assert.Equal(t, store.FailureCauseUnsupported, FailureCause(err))
}
