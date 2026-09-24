// Package urlutil normalizes URLs to a canonical form used as a dedup key.
//
// The same logical resource can be referenced by many syntactic URLs:
//
//	HTTPS://Example.COM:443/Article#section1
//	https://example.com/Article?utm_source=twitter
//	https://example.com/Article
//
// The normalized URL is also the URL curio fetches, so every step must
// keep it pointing at the same resource, and normalizing twice must change
// nothing (clients normalize before the daemon does it again). Normalize:
//
//   - accepts only absolute http(s) URLs with a host
//   - lowercases scheme and host; IPv6 hosts keep their brackets
//   - drops the default port (:80 for http, :443 for https)
//   - turns an empty path into "/"
//   - drops the fragment (#...)
//   - rewrites YouTube video URLs to https://www.youtube.com/watch?v=<ID>
//   - splits the query on '&' only, then drops empty pairs and tracking
//     parameters (utm_*, fbclid, gclid, mc_*, ...), re-encodes well-formed
//     pairs the way url.Values.Encode does, keeps pairs that don't decode
//     or contain ';' as they are (only spaces and non-ASCII bytes get
//     percent-encoded, as a browser would send them), keeps a key without
//     '=' without one, and sorts pairs by decoded key (stable, so repeated
//     keys keep their order)
//
// Paths, trailing slashes, and userinfo are preserved verbatim — many servers
// distinguish those, and being too aggressive risks collapsing distinct
// resources.
package urlutil

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrInvalidURL is returned for URLs curio can't fetch: empty, unparseable,
// not http(s), or without a host.
var ErrInvalidURL = errors.New("invalid url")

// trackingParams is the exact-match list of query parameters to strip.
// Prefix-based families (utm_*, mc_*, vero_*, _hs*) are handled separately.
// Generic names that sites also use for content (ref: a branch, a version)
// don't belong here: stripping them changes what is fetched.
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

// Normalize returns the canonical form of raw. It returns ErrInvalidURL
// unless raw is an absolute http or https URL with a host.
func Normalize(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidURL)
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%w: not an http(s) url: %s", ErrInvalidURL, raw)
	}
	// "https:example.com/x" parses as an opaque URL and "https:///x" with an
	// empty host; neither names a host to fetch from.
	if u.Opaque != "" || u.Hostname() == "" {
		return "", fmt.Errorf("%w: missing host: %s", ErrInvalidURL, raw)
	}
	host, err := canonicalHost(u)
	if err != nil {
		return "", err
	}
	u.Host = host

	if u.Path == "" {
		u.Path = "/"
	}
	u.Fragment = ""
	u.RawFragment = ""

	// YouTube canonicalization: youtu.be/ID → youtube.com/watch?v=ID,
	// dropping every other parameter.
	if id, ok := YouTubeVideoID(u); ok {
		u.Host = "www.youtube.com"
		u.Path = "/watch"
		u.RawPath = ""
		u.RawQuery = url.Values{"v": {id}}.Encode()
		return u.String(), nil
	}

	u.RawQuery = canonicalQuery(u.RawQuery)
	return u.String(), nil
}

// canonicalHost returns u's host lowercased, without a default port, and
// with IPv6 literals bracketed. A host containing ':' must be an IP
// literal: url.Parse accepts "0000000::" as a host with an empty port.
func canonicalHost(u *url.URL) (string, error) {
	hostname := strings.ToLower(u.Hostname())
	if strings.Contains(hostname, ":") && net.ParseIP(hostname) == nil {
		return "", fmt.Errorf("%w: bad host %q", ErrInvalidURL, u.Host)
	}
	port := u.Port()
	if isDefaultPort(u.Scheme, port) {
		port = ""
	}
	if port != "" {
		return net.JoinHostPort(hostname, port), nil
	}
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]", nil
	}
	return hostname, nil
}

// queryPair is one '&'-separated query element: text is what goes into the
// normalized URL, key what it sorts by.
type queryPair struct {
	key  string
	text string
}

// canonicalQuery rebuilds a raw query per the package rules. Well-formed
// pairs come out exactly as url.Values.Encode writes them, so dedup keys of
// ordinary URLs are unchanged; anything it can't decode it keeps verbatim
// rather than dropping it, since the query is part of what gets fetched.
func canonicalQuery(raw string) string {
	var pairs []queryPair
	for part := range strings.SplitSeq(raw, "&") {
		if part == "" {
			continue
		}
		if p, ok := canonicalPair(part); ok {
			pairs = append(pairs, p)
		}
	}
	slices.SortStableFunc(pairs, func(a, b queryPair) int { return strings.Compare(a.key, b.key) })
	texts := make([]string, len(pairs))
	for i, p := range pairs {
		texts[i] = p.text
	}
	return strings.Join(texts, "&")
}

// canonicalPair normalizes one query element; ok is false for a tracking
// parameter, which is dropped. Spaces and non-ASCII bytes are escaped
// first, so a pair kept as is sorts the same way on the next pass.
func canonicalPair(part string) (queryPair, bool) {
	part = escapeLoose(part)
	rawKey, rawValue, hasValue := strings.Cut(part, "=")
	key, keyErr := url.QueryUnescape(rawKey)
	if keyErr != nil {
		return queryPair{key: rawKey, text: part}, true
	}
	// Servers that split on ';' see more than one parameter here; keep
	// them all, tracking key or not.
	if strings.Contains(part, ";") {
		return queryPair{key: key, text: part}, true
	}
	if isTracking(key) {
		return queryPair{}, false
	}
	value, valueErr := url.QueryUnescape(rawValue)
	switch {
	case valueErr != nil:
		return queryPair{key: key, text: part}, true
	case !hasValue:
		return queryPair{key: key, text: url.QueryEscape(key)}, true
	}
	return queryPair{key: key, text: url.QueryEscape(key) + "=" + url.QueryEscape(value)}, true
}

// escapeLoose percent-encodes the spaces and non-ASCII bytes of a query
// pair, the way a browser sends them, and leaves everything else (malformed
// '%' sequences included) untouched. It changes nothing a decoder sees, and
// a raw space at the end of the URL would otherwise be trimmed by the next
// Normalize.
func escapeLoose(s string) string {
	var b strings.Builder
	for i := range len(s) {
		if c := s[i]; c == ' ' || c >= utf8.RuneSelf {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

var youtubeHosts = map[string]bool{
	"youtube.com":     true,
	"www.youtube.com": true,
	"m.youtube.com":   true,
	"youtu.be":        true,
}

// videoIDRE is the alphabet of YouTube video IDs. Anything else in the ID
// position is not a video URL, and canonicalizing it would paste
// unescaped text into the query.
var videoIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// YouTubeVideoID extracts the video ID from a parsed YouTube URL.
// Returns ("", false) for non-YouTube URLs, playlist-only URLs, and IDs
// outside the video-ID alphabet.
func YouTubeVideoID(u *url.URL) (string, bool) {
	host := strings.ToLower(u.Hostname())
	if !youtubeHosts[host] {
		return "", false
	}
	id := youtubeIDCandidate(host, u)
	return id, videoIDRE.MatchString(id)
}

// youtubeIDCandidate returns what sits in the video-ID position of a
// YouTube URL, or "" when nothing does.
func youtubeIDCandidate(host string, u *url.URL) string {
	// youtu.be/ID
	if host == "youtu.be" {
		id, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
		return id
	}

	// youtube.com/watch?v=ID
	if v := u.Query().Get("v"); v != "" {
		return v
	}

	// youtube.com/shorts/ID, /live/ID, /embed/ID, /v/ID
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) == 2 {
		switch parts[0] {
		case "shorts", "live", "embed", "v":
			return parts[1]
		}
	}
	return ""
}

// GitHubURLInfo describes the components of a parsed GitHub URL.
type GitHubURLInfo struct {
	Owner  string // e.g. "kubernetes"
	Repo   string // e.g. "kubernetes"
	Type   string // "repo", "file", "issue", "pull", "wiki", "other"
	Ref    string // branch or tag for file/tree URLs
	Path   string // file path within repo, or wiki page name
	Number int    // issue or PR number for issue/pull URLs
}

// ParseGitHubURL extracts structured info from a GitHub URL.
// Returns false for non-GitHub URLs or URLs that don't match a
// recognized pattern (e.g. github.com/settings).
func ParseGitHubURL(u *url.URL) (GitHubURLInfo, bool) {
	host := strings.ToLower(u.Hostname())
	if host != "github.com" {
		return GitHubURLInfo{}, false
	}

	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	// Filter empty parts from trailing slashes
	var clean []string
	for _, p := range parts {
		if p != "" {
			clean = append(clean, p)
		}
	}
	parts = clean

	if len(parts) < 2 {
		return GitHubURLInfo{}, false
	}

	owner, repo := parts[0], parts[1]
	// Skip non-repo paths like /settings, /marketplace, /explore
	if owner == "settings" || owner == "marketplace" || owner == "explore" ||
		owner == "topics" || owner == "trending" || owner == "login" || owner == "signup" {
		return GitHubURLInfo{}, false
	}

	info := GitHubURLInfo{Owner: owner, Repo: repo, Type: "repo"}

	if len(parts) >= 4 {
		switch parts[2] {
		case "blob":
			info.Type = "file"
			info.Ref = parts[3]
			if len(parts) > 4 {
				info.Path = strings.Join(parts[4:], "/")
			}
		case "tree":
			info.Type = "repo"
			info.Ref = parts[3]
		case "issues":
			// /owner/repo/issues/123 — but /issues/new and similar
			// non-numeric paths aren't fetchable issues.
			if n, err := strconv.Atoi(parts[3]); err == nil && n > 0 {
				info.Type = "issue"
				info.Number = n
			} else {
				info.Type = "other"
			}
		case "pull":
			// /owner/repo/pull/456 (web URL is singular; the REST API
			// path is /pulls/456). Sub-pages like /pull/456/files still
			// identify the PR.
			if n, err := strconv.Atoi(parts[3]); err == nil && n > 0 {
				info.Type = "pull"
				info.Number = n
			} else {
				info.Type = "other"
			}
		case "wiki":
			info.Type = "wiki"
			info.Path = strings.Join(parts[3:], "/")
		default:
			info.Type = "other"
		}
	} else if len(parts) == 3 {
		switch parts[2] {
		case "wiki":
			// /owner/repo/wiki — the wiki home page.
			info.Type = "wiki"
			info.Path = "Home"
		case "issues", "pulls", "actions", "releases", "tags":
			info.Type = "other"
		}
	}

	return info, true
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
