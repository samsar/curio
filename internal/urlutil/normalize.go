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
	"strconv"
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

	// YouTube canonicalization: youtu.be/ID → youtube.com/watch?v=ID,
	// and strip YouTube-specific tracking params.
	if id, ok := YouTubeVideoID(u); ok {
		u.Host = "www.youtube.com"
		u.Path = "/watch"
		u.RawQuery = "v=" + id
		u.Fragment = ""
		u.RawFragment = ""
		return u.String(), nil
	}

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

var youtubeHosts = map[string]bool{
	"youtube.com":     true,
	"www.youtube.com": true,
	"m.youtube.com":   true,
	"youtu.be":        true,
}

// YouTubeVideoID extracts the video ID from a parsed YouTube URL.
// Returns ("", false) for non-YouTube URLs or playlist-only URLs.
func YouTubeVideoID(u *url.URL) (string, bool) {
	host := strings.ToLower(u.Hostname())
	if !youtubeHosts[host] {
		return "", false
	}

	// youtu.be/ID
	if host == "youtu.be" {
		id := strings.TrimPrefix(u.Path, "/")
		id = strings.SplitN(id, "/", 2)[0]
		if id != "" {
			return id, true
		}
		return "", false
	}

	// youtube.com/watch?v=ID
	if v := u.Query().Get("v"); v != "" {
		return v, true
	}

	// youtube.com/shorts/ID, /live/ID, /embed/ID, /v/ID
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) == 2 {
		switch parts[0] {
		case "shorts", "live", "embed", "v":
			if parts[1] != "" {
				return parts[1], true
			}
		}
	}

	return "", false
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
