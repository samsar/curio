package fetcher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// ErrBlockedAddress marks a URL whose host resolves to an address curio
// will not fetch: loopback, a private or link-local network, a multicast
// or documentation address, or another range that can reach the machine
// itself or a local network. A fetch that meets it is permanent: retrying
// the same name only reaches the same kind of address.
var ErrBlockedAddress = errors.New("private network address blocked")

// blockedPrefixes are the IPv4 and IPv6 ranges a bookmark may not reach.
// The net.IP methods cover loopback, private, link-local, multicast and
// unspecified addresses; these add the ranges they leave out.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmark
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("3fff::/20"),       // documentation (RFC 9637)
	netip.MustParsePrefix("fec0::/10"),       // deprecated site-local
	netip.MustParsePrefix("::/96"),           // IPv4-compatible (deprecated)
	netip.MustParsePrefix("2001:10::/28"),    // ORCHID
	netip.MustParsePrefix("2001:20::/28"),    // ORCHIDv2
	netip.MustParsePrefix("2002::/16"),       // 6to4
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("100::/64"),        // discard-only
}

// blockedIP reports whether ip is one a bookmark may not reach.
func blockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// guardDialer is the net.Dialer every fetcher transport uses. It resolves
// the name itself, refuses the connection when any address is blocked,
// and dials the checked address rather than the name, so a DNS answer
// cannot change between the check and the connection (DNS rebinding).
type guardDialer struct {
	d            net.Dialer
	allowPrivate bool
}

func newGuardDialer(allowPrivate bool) *guardDialer {
	return &guardDialer{allowPrivate: allowPrivate}
}

func (g *guardDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if g.allowPrivate {
		return g.d.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed address %q", ErrBlockedAddress, addr)
	}
	if host == "" {
		return nil, fmt.Errorf("%w: address %q names no host", ErrBlockedAddress, addr)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("dial %s: no addresses", host)
	}
	for _, ip := range ips {
		if blockedIP(ip.IP) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedAddress, host, ip.IP)
		}
	}
	var lastErr error
	for _, ip := range ips {
		conn, err := g.d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
