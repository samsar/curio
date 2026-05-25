package urlutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
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
		{"strip ref", "https://example.com/x?ref=hn", "https://example.com/x"},
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.out, got)
		})
	}
}

func TestNormalize_YouTube(t *testing.T) {
	canonical := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	cases := []struct {
		name string
		in   string
		out  string
	}{
		{"standard", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", canonical},
		{"no www", "https://youtube.com/watch?v=dQw4w9WgXcQ", canonical},
		{"mobile", "https://m.youtube.com/watch?v=dQw4w9WgXcQ", canonical},
		{"short link", "https://youtu.be/dQw4w9WgXcQ", canonical},
		{"with tracking", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&si=abc&list=PL123&t=42&pp=xyz", canonical},
		{"with feature", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&feature=youtu.be", canonical},
		{"shorts", "https://www.youtube.com/shorts/dQw4w9WgXcQ", canonical},
		{"live", "https://www.youtube.com/live/dQw4w9WgXcQ", canonical},
		{"embed", "https://www.youtube.com/embed/dQw4w9WgXcQ", canonical},
		{"short link with tracking", "https://youtu.be/dQw4w9WgXcQ?si=abc123", canonical},
		{"http scheme", "http://www.youtube.com/watch?v=dQw4w9WgXcQ", "http://www.youtube.com/watch?v=dQw4w9WgXcQ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.out, got)
		})
	}
}

func TestNormalize_YouTube_PlaylistOnly(t *testing.T) {
	// Playlist-only URLs don't have a video ID — should pass through
	// without YouTube canonicalization.
	got, err := Normalize("https://www.youtube.com/playlist?list=PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf")
	require.NoError(t, err)
	assert.Equal(t, "https://www.youtube.com/playlist?list=PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf", got)
}

func TestNormalize_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"no scheme", "example.com/article"},
		{"control chars", "ht\x00tp://example.com"},
	}
	for _, tc := range cases {
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

func TestHostname(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://Example.COM/article", "example.com"},
		{"https://example.com:8443/x", "example.com"},
		{"http://sub.example.com/x", "sub.example.com"},
		{"https://example.com", "example.com"},
	}
	for _, tc := range cases {
		got, err := Hostname(tc.in)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}
}
