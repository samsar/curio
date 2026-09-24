package urlutil

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Normalize case tables are package-level so FuzzNormalize can seed
// its corpus from them.

var normalizeCases = []struct {
	name string
	in   string
	out  string
}{
	// Identity / no-op
	{"plain https", "https://example.com/article", "https://example.com/article"},
	{"plain http", "http://example.com/article", "http://example.com/article"},
	{"trailing slash preserved", "https://example.com/", "https://example.com/"},
	{"path case preserved", "https://example.com/Article", "https://example.com/Article"},

	// Scheme + host case folding
	{"scheme uppercased", "HTTPS://example.com/x", "https://example.com/x"},
	{"host uppercased", "https://EXAMPLE.COM/x", "https://example.com/x"},
	{"mixed scheme + host", "HTTPS://Example.COM/Article", "https://example.com/Article"},

	// Default port stripping
	{"strip https:443", "https://example.com:443/x", "https://example.com/x"},
	{"strip http:80", "http://example.com:80/x", "http://example.com/x"},
	{"non-default port preserved", "https://example.com:8443/x", "https://example.com:8443/x"},
	{"http on 443 preserved", "http://example.com:443/x", "http://example.com:443/x"},
	{"https on 80 preserved", "https://example.com:80/x", "https://example.com:80/x"},

	// Fragment stripping
	{"strip fragment", "https://example.com/x#section", "https://example.com/x"},
	{"strip empty fragment", "https://example.com/x#", "https://example.com/x"},
	{"strip fragment with query", "https://example.com/x?a=1#section", "https://example.com/x?a=1"},

	// Query tracking removal
	{"strip utm_source", "https://example.com/x?utm_source=twitter", "https://example.com/x"},
	{"strip all utm_*", "https://example.com/x?utm_source=a&utm_medium=b&utm_campaign=c", "https://example.com/x"},
	{"strip gclid", "https://example.com/x?gclid=abc", "https://example.com/x"},
	{"strip fbclid", "https://example.com/x?fbclid=xyz", "https://example.com/x"},
	{"strip msclkid", "https://example.com/x?msclkid=1", "https://example.com/x"},
	{"strip mc_*", "https://example.com/x?mc_eid=1&mc_cid=2", "https://example.com/x"},
	{"strip _hsenc", "https://example.com/x?_hsenc=p2A&_hsmi=2", "https://example.com/x"},
	{"strip vero_*", "https://example.com/x?vero_id=abc", "https://example.com/x"},
	{"strip case-insensitively", "https://example.com/x?UTM_Source=foo", "https://example.com/x"},

	// Mixed: keep legit params, strip tracking
	{"keep id strip utm", "https://example.com/x?id=42&utm_source=t", "https://example.com/x?id=42"},
	{"sort remaining keys", "https://example.com/x?b=2&a=1", "https://example.com/x?a=1&b=2"},
	{"sort + strip", "https://example.com/x?z=9&utm_source=t&a=1", "https://example.com/x?a=1&z=9"},

	// Query empty after stripping
	{"all tracking removed leaves no ?", "https://example.com/x?utm_source=a&fbclid=b", "https://example.com/x"},

	// Userinfo preserved
	{"userinfo preserved", "https://user:pass@example.com/x", "https://user:pass@example.com/x"},

	// Path quirks
	{"path with encoded chars", "https://example.com/article%20one", "https://example.com/article%20one"},
	{"path with spaces (raw)", "https://example.com/a%20b", "https://example.com/a%20b"},
	{"empty path becomes /", "https://example.com", "https://example.com/"},
	{"empty path with query", "https://example.com?x=1", "https://example.com/?x=1"},

	// Query data is never lost: the normalized URL is what gets fetched.
	{"semicolon pair kept verbatim", "https://example.com/x?a=1;b=2", "https://example.com/x?a=1;b=2"},
	{"ref is not tracking", "https://example.com/x?ref=main&x=1", "https://example.com/x?ref=main&x=1"},
	{"valueless key stays valueless", "https://example.com/x?flag", "https://example.com/x?flag"},
	{"undecodable pair kept, tracking dropped", "https://example.com/x?q=%zz&utm_source=a", "https://example.com/x?q=%zz"},
	{"re-encoded like url.Values", "https://example.com/x?q=a%20b", "https://example.com/x?q=a+b"},
	{"empty pairs dropped", "https://example.com/x?&a=1&&b=2&", "https://example.com/x?a=1&b=2"},
	{"kept pair gets spaces and non-ascii escaped", "http://0?%\x82 #", "http://0/?%%82%20"},
	{"repeated keys keep their order", "https://example.com/x?b=2&a=z&a=y", "https://example.com/x?a=z&a=y&b=2"},

	// IPv6 literals
	{"ipv6 default port stripped", "https://[::1]:443/x", "https://[::1]/x"},
	{"ipv6 other port kept", "https://[::1]:8443/x", "https://[::1]:8443/x"},
	{"ipv6 lowercased", "http://[FE80::1]/x", "http://[fe80::1]/x"},
}

func TestNormalize(t *testing.T) {
	for _, tc := range normalizeCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.out, got)
		})
	}
}

const canonicalWatch = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

var youTubeCases = []struct {
	name string
	in   string
	out  string
}{
	{"standard", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", canonicalWatch},
	{"no www", "https://youtube.com/watch?v=dQw4w9WgXcQ", canonicalWatch},
	{"mobile", "https://m.youtube.com/watch?v=dQw4w9WgXcQ", canonicalWatch},
	{"short link", "https://youtu.be/dQw4w9WgXcQ", canonicalWatch},
	{"with tracking", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&si=abc&list=PL123&t=42&pp=xyz", canonicalWatch},
	{"with feature", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&feature=youtu.be", canonicalWatch},
	{"shorts", "https://www.youtube.com/shorts/dQw4w9WgXcQ", canonicalWatch},
	{"live", "https://www.youtube.com/live/dQw4w9WgXcQ", canonicalWatch},
	{"embed", "https://www.youtube.com/embed/dQw4w9WgXcQ", canonicalWatch},
	{"short link with tracking", "https://youtu.be/dQw4w9WgXcQ?si=abc123", canonicalWatch},
	{"http scheme", "http://www.youtube.com/watch?v=dQw4w9WgXcQ", "http://www.youtube.com/watch?v=dQw4w9WgXcQ"},
	{"underscore and dash", "https://youtu.be/test_id-1", "https://www.youtube.com/watch?v=test_id-1"},
}

func TestNormalize_YouTube(t *testing.T) {
	for _, tc := range youTubeCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.out, got)
		})
	}
}

var invalidYouTubeIDCases = []struct{ in, out string }{
	{"https://www.youtube.com/watch?v=abc%26list%3Dx", "https://www.youtube.com/watch?v=abc%26list%3Dx"},
	{"https://www.youtube.com/watch?v=ab%20cd", "https://www.youtube.com/watch?v=ab+cd"},
	{"http://Youtu.Be/0&0", "http://youtu.be/0&0"},
	{"http://Youtu.Be/&", "http://youtu.be/&"},
}

// TestNormalize_YouTube_InvalidIDs: text in the video-ID position that
// isn't a video ID is left alone rather than pasted unescaped into a
// canonical watch URL, so no raw '&' or space leaks into the query and a
// second pass changes nothing.
func TestNormalize_YouTube_InvalidIDs(t *testing.T) {
	for _, tc := range invalidYouTubeIDCases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := Normalize(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.out, got)
			u, err := url.Parse(got)
			require.NoError(t, err)
			assert.NotContains(t, u.RawQuery, " ")
			if strings.Contains(u.Path, "watch") {
				assert.NotContains(t, u.RawQuery, "&")
			}
			again, err := Normalize(got)
			require.NoError(t, err)
			assert.Equal(t, got, again)
		})
	}
}

func TestYouTubeVideoID_Alphabet(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", true},
		{"https://youtu.be/test_id", true},
		{"https://www.youtube.com/shorts/a-b_C9", true},
		{"https://www.youtube.com/watch?v=abc%26list%3Dx", false},
		{"https://www.youtube.com/watch?v=ab%20cd", false},
		{"https://youtu.be/0&0", false},
		{"https://www.youtube.com/embed/x%2Fy", false},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.url)
		require.NoError(t, err)
		_, ok := YouTubeVideoID(u)
		assert.Equal(t, tc.ok, ok, tc.url)
	}
}

func TestNormalize_YouTube_PlaylistOnly(t *testing.T) {
	// Playlist-only URLs don't have a video ID — should pass through
	// without YouTube canonicalization.
	got, err := Normalize("https://www.youtube.com/playlist?list=PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf")
	require.NoError(t, err)
	assert.Equal(t, "https://www.youtube.com/playlist?list=PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf", got)
}

func TestParseGitHubURL(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		ok     bool
		typ    string
		owner  string
		repo   string
		ref    string
		path   string
		number int
	}{
		{"repo", "https://github.com/kubernetes/kubernetes", true, "repo", "kubernetes", "kubernetes", "", "", 0},
		{"repo trailing slash", "https://github.com/kubernetes/kubernetes/", true, "repo", "kubernetes", "kubernetes", "", "", 0},
		{"file", "https://github.com/owner/repo/blob/main/docs/arch.md", true, "file", "owner", "repo", "main", "docs/arch.md", 0},
		{"file root", "https://github.com/owner/repo/blob/main/README.md", true, "file", "owner", "repo", "main", "README.md", 0},
		{"tree", "https://github.com/owner/repo/tree/main/src", true, "repo", "owner", "repo", "main", "", 0},
		{"issue", "https://github.com/owner/repo/issues/123", true, "issue", "owner", "repo", "", "", 123},
		{"issue new", "https://github.com/owner/repo/issues/new", true, "other", "owner", "repo", "", "", 0},
		{"pull", "https://github.com/owner/repo/pull/456", true, "pull", "owner", "repo", "", "", 456},
		{"pull files view", "https://github.com/owner/repo/pull/456/files", true, "pull", "owner", "repo", "", "", 456},
		{"issues list", "https://github.com/owner/repo/issues", true, "other", "owner", "repo", "", "", 0},
		{"wiki home", "https://github.com/owner/repo/wiki", true, "wiki", "owner", "repo", "", "Home", 0},
		{"wiki page", "https://github.com/owner/repo/wiki/Getting-Started", true, "wiki", "owner", "repo", "", "Getting-Started", 0},
		{"not github", "https://example.com/owner/repo", false, "", "", "", "", "", 0},
		{"settings page", "https://github.com/settings/profile", false, "", "", "", "", "", 0},
		{"owner only", "https://github.com/kubernetes", false, "", "", "", "", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			require.NoError(t, err)
			info, ok := ParseGitHubURL(u)
			assert.Equal(t, tc.ok, ok)
			if ok {
				assert.Equal(t, tc.typ, info.Type)
				assert.Equal(t, tc.owner, info.Owner)
				assert.Equal(t, tc.repo, info.Repo)
				assert.Equal(t, tc.ref, info.Ref)
				assert.Equal(t, tc.path, info.Path)
				assert.Equal(t, tc.number, info.Number)
			}
		})
	}
}

var invalidURLCases = []struct {
	name string
	in   string
}{
	{"empty", ""},
	{"whitespace only", "   "},
	{"no scheme", "example.com/article"},
	{"control chars", "ht\x00tp://example.com"},
	{"javascript", "javascript:alert(1)"},
	{"file", "file:///etc/passwd"},
	{"mailto", "mailto:someone@example.com"},
	{"ftp", "ftp://example.com/file"},
	{"opaque https", "https:example.com/x"},
	{"empty host", "https:///x"},
	{"port only", "https://:443/x"},
	{"colon in a non-ip host", "https://0000000::"},
}

func TestNormalize_Errors(t *testing.T) {
	for _, tc := range invalidURLCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Normalize(tc.in)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidURL)
		})
	}
}

func TestNormalize_Idempotent(t *testing.T) {
	// Normalizing twice should yield the same result.
	urls := []string{
		"https://example.com/x",
		"HTTPS://EXAMPLE.COM:443/X?utm_source=a&id=1#frag",
		"http://a.example.com/article?b=2&a=1",
	}
	for _, raw := range urls {
		t.Run(raw, func(t *testing.T) {
			first, err := Normalize(raw)
			require.NoError(t, err)
			second, err := Normalize(first)
			require.NoError(t, err)
			assert.Equal(t, first, second, "Normalize should be idempotent")
		})
	}
}

func TestNormalize_DistinctURLsStayDistinct(t *testing.T) {
	// Pairs that should NOT collapse — verifies we're not over-normalizing.
	pairs := [][2]string{
		// Trailing slash matters on many servers
		{"https://example.com/x", "https://example.com/x/"},
		// Path case is preserved
		{"https://example.com/Article", "https://example.com/article"},
		// Different non-tracking query values
		{"https://example.com/x?id=1", "https://example.com/x?id=2"},
		// Different ports
		{"https://example.com:8080/x", "https://example.com:9090/x"},
		// Different schemes (http vs https — same site, but we don't assume equivalence)
		{"http://example.com/x", "https://example.com/x"},
	}
	for _, p := range pairs {
		a, err := Normalize(p[0])
		require.NoError(t, err)
		b, err := Normalize(p[1])
		require.NoError(t, err)
		assert.NotEqual(t, a, b, "%q and %q should not collapse", p[0], p[1])
	}
}
