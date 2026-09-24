package fetcher

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChromeProfiles_Coherent: every profile's User-Agent and sec-ch-ua
// name the profile's own Chrome version.
func TestChromeProfiles_Coherent(t *testing.T) {
	uaVersionRE := regexp.MustCompile(`Chrome/(\d+)\.`)
	brandRE := regexp.MustCompile(`"(Google Chrome|Chromium)";v="(\d+)"`)
	for _, p := range chromeProfiles {
		t.Run(p.name, func(t *testing.T) {
			major := strconv.Itoa(p.major)
			assert.Equal(t, "chrome_"+major, p.name)

			m := uaVersionRE.FindStringSubmatch(p.userAgent)
			require.Len(t, m, 2, p.userAgent)
			assert.Equal(t, major, m[1])

			brands := brandRE.FindAllStringSubmatch(p.secChUA, -1)
			require.Len(t, brands, 2, "sec-ch-ua must name Google Chrome and Chromium: %s", p.secChUA)
			for _, b := range brands {
				assert.Equal(t, major, b[2], b[1])
			}
		})
	}
}

// TestNative_HeadersFollowProfile: without a user_agent override, the
// User-Agent and sec-ch-ua sent match the selected profile; the stock
// backend uses the latest one.
func TestNative_HeadersFollowProfile(t *testing.T) {
	cases := []struct{ backend, major string }{
		{"", "133"},
		{"chrome_120", "120"},
		{"chrome_124", "124"},
		{"stock", "133"},
	}
	for _, tc := range cases {
		t.Run("backend="+tc.backend, func(t *testing.T) {
			var ua, chUA string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ua, chUA = r.Header.Get("User-Agent"), r.Header.Get("Sec-Ch-Ua")
				_, _ = w.Write([]byte(makeArticleHTML("Headers", "")))
			}))
			defer srv.Close()

			n := NewNative(NativeOptions{Timeout: 5 * time.Second, Backend: tc.backend})
			_, err := n.Fetch(context.Background(), srv.URL)
			require.NoError(t, err)
			assert.Contains(t, ua, "Chrome/"+tc.major+".0.0.0")
			assert.Contains(t, chUA, `"Google Chrome";v="`+tc.major+`"`)
			assert.Contains(t, chUA, `"Chromium";v="`+tc.major+`"`)
		})
	}
}

// TestNewNative_WarnsOnMismatchedUserAgent: an override naming a different
// Chrome version than the profile is honored but logged once.
func TestNewNative_WarnsOnMismatchedUserAgent(t *testing.T) {
	cases := []struct {
		ua       string
		warnings int
	}{
		{chromeUA(133), 1},
		{chromeUA(120), 0},
		{"curio-test/1.0", 1},
	}
	for _, tc := range cases {
		t.Run(tc.ua, func(t *testing.T) {
			var logs bytes.Buffer
			n := NewNative(NativeOptions{Backend: "chrome_120", UserAgent: tc.ua, Log: slog.New(slog.NewTextHandler(&logs, nil))})
			assert.Equal(t, tc.ua, n.userAgent, "an override is sent as is")
			assert.Equal(t, tc.warnings, strings.Count(logs.String(), "user_agent doesn't name"))
		})
	}
}
