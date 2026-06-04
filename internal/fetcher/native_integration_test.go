//go:build integration

package fetcher

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNative_LiveSites_RenderMarkdown fetches real sites end-to-end and
// asserts we get readable markdown, not undecoded bytes. Requires network;
// runs only under `make test-integration` (build tag `integration`).
//
// The MDN case is the one that caught the HTTP/3 + brotli regression: MDN's
// CDN advertises h3, the chrome backend negotiates it, and that transport
// path does not auto-decompress — so a missing defensive decompress shows up
// here as binary garbage where markdown should be.
func TestNative_LiveSites_RenderMarkdown(t *testing.T) {
	cases := []struct {
		name        string
		url         string
		mustContain string
	}{
		{"mdn_h3_brotli", "https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/User-Agent", "User-Agent"},
		{"wikipedia_h2_gzip", "https://en.wikipedia.org/wiki/Transport_Layer_Security", "Transport Layer Security"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Backend defaults to chrome; Jina off so we test the direct fetch.
			n := NewNative(NativeOptions{Timeout: 20 * time.Second, JinaFallback: false})
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()

			res, err := n.Fetch(ctx, tc.url)
			require.NoError(t, err)
			assertReadableMarkdown(t, res.Markdown)
			assert.Contains(t, res.Markdown, tc.mustContain)
			assert.Greater(t, len(res.Markdown), 500, "suspiciously short extraction")
		})
	}
}
