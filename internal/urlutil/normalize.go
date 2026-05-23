// Package urlutil normalizes URLs to a canonical form used as a dedup key.
//
// The same logical resource can be referenced by many syntactic URLs:
//
//	HTTPS://Example.COM:443/Article#section1
//	https://example.com/Article?utm_source=twitter
//	https://example.com/Article
//
// Normalize collapses these to a single canonical string by:
//   - Lowercasing scheme and host (paths stay case-sensitive)
//   - Stripping default ports (:80 for http, :443 for https)
//   - Removing fragments (#...)
//   - Removing common tracking query parameters (utm_*, fbclid, gclid, mc_*, ...)
//   - Sorting remaining query parameters alphabetically by key
//
// Paths, trailing slashes, and userinfo are preserved verbatim — many servers
// distinguish those, and being too aggressive risks collapsing distinct
// resources.
package urlutil

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrInvalidURL is returned for empty, unparseable, or schemeless URLs.
var ErrInvalidURL = errors.New("invalid url")

// trackingParams is the exact-match list of query parameters to strip.
// Prefix-based families (utm_*, mc_*, vero_*, _hs*) are handled separately.
var trackingParams = map[string]struct{}{
	"fbclid":         {},
	"gclid":          {},
	"msclkid":        {},
	"dclid":          {},
	"yclid":          {},
	"ttclid":         {},
	"twclid":         {},
	"igshid":         {},
	"_ga":            {},
	"_gl":            {},
	"_gid":           {},
	"ref":            {},
	"ref_src":        {},
	"ref_url":        {},
	"oly_anon_id":    {},
	"oly_enc_id":     {},
	"vero_id":        {},
	"hsctatracking":  {},
	"_hsenc":         {},
	"_hsmi":          {},
	"sb_referer":     {},
	"trk":            {},
	"trk_contact":    {},
	"trk_msg":        {},
	"trk_module":     {},
	"s_cid":          {},
	"mkt_tok":        {},
	"pk_campaign":    {},
	"pk_kwd":         {},
	"pk_keyword":     {},
	"piwik_campaign": {},
	"piwik_kwd":      {},
}

// Normalize returns the canonical form of raw. It returns ErrInvalidURL if
// the input is empty, malformed, or missing a scheme.
func Normalize(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidURL)
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if u.Scheme == "" {
		return "", fmt.Errorf("%w: missing scheme: %s", ErrInvalidURL, raw)
	}

	// Lowercase scheme.
	u.Scheme = strings.ToLower(u.Scheme)

	// Lowercase host portion (not userinfo). url.URL.Host is "host:port" or "host".
	if u.Host != "" {
		hostname := strings.ToLower(u.Hostname())
		port := u.Port()
		if isDefaultPort(u.Scheme, port) {
			port = ""
		}
		if port == "" {
			u.Host = hostname
		} else {
			u.Host = hostname + ":" + port
		}
	}

	// Drop fragment.
	u.Fragment = ""
	u.RawFragment = ""

	// Filter and rebuild query (url.Values.Encode sorts keys alphabetically).
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if isTracking(k) {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode()
	}

	return u.String(), nil
}

// MustNormalize is Normalize that panics on error. Test helper.
func MustNormalize(raw string) string {
	out, err := Normalize(raw)
	if err != nil {
		panic(err)
	}
	return out
}

// Hostname returns the lowercased hostname of raw (no port). Returns the
// empty string if the URL has no host. Used by fetcher rules and filters.
func Hostname(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	return strings.ToLower(u.Hostname()), nil
}

func isDefaultPort(scheme, port string) bool {
	switch {
	case scheme == "http" && port == "80":
		return true
	case scheme == "https" && port == "443":
		return true
	}
	return false
}

func isTracking(key string) bool {
	lower := strings.ToLower(key)
	if _, ok := trackingParams[lower]; ok {
		return true
	}
	for _, prefix := range []string{"utm_", "mc_", "vero_", "_hs"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
