package urlutil

import (
	"net"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Site is the site a page on host belongs to: its registrable domain, so
// mobile.twitter.com and twitter.com are one site, as www.forbes.com and
// forbes.com are. A host without one is its own site: an IP address, a
// name under no public suffix (localhost, intranet), a public suffix
// itself (github.io), or a name publicsuffix can't read. The host is
// compared as DNS names are, lowercased and without a trailing dot.
func Site(host string) string {
	if site, ok := RegistrableDomain(host); ok {
		return site
	}
	return dnsHost(host)
}

// RegistrableDomain is host's registrable domain, its public suffix and
// the one label before it (blog.example.co.uk's is example.co.uk), and
// whether it has one: Site's hosts that are their own site have none.
func RegistrableDomain(host string) (string, bool) {
	host = dnsHost(host)
	if net.ParseIP(host) != nil {
		return "", false
	}
	site, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", false
	}
	return site, true
}

// dnsHost is host as DNS names compare: lowercased, without a
// trailing dot.
func dnsHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
