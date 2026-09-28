package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
)

func TestIsWebURL(t *testing.T) {
	for arg, want := range map[string]bool{
		"https://github.com/koutto/pi-pwnbox-rogueap": true,
		"http://example.com/a":                        true,
		"HTTPS://Example.com/a":                       true,
		"59fbb552-1b4a-42ae-9421-3f61110df324":        false,
		"github.com/koutto/pi-pwnbox-rogueap":         false, // no scheme: taken as an ID
		"ftp://example.com/a":                         false,
		"https://":                                    false,
	} {
		assert.Equal(t, want, isWebURL(arg), arg)
	}
}

// TestResolveDocumentID_Misses: the lookup's own 404 means the library has
// no such document; a 404 about a document ID "lookup", which a daemon
// from before the lookup answers, means the daemon is too old for it.
func TestResolveDocumentID_Misses(t *testing.T) {
	cases := []struct {
		name, detail, want string
	}{
		{"no document", `document for url "https://example.com/a" not found`,
			"no document for https://example.com/a in the library (curio add https://example.com/a saves it)"},
		{"older daemon", `document "lookup" not found`,
			"the running daemon is older than this curio and can't look documents up by URL: " +
				"upgrade it (brew upgrade curio, then curio up), or pass the document ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"type": "about:blank", "title": "not found", "status": http.StatusNotFound, "detail": tc.detail,
				})
			}))
			defer srv.Close()
			_, err := resolveDocumentID(t.Context(), client.New(srv.URL), "https://example.com/a")
			require.EqualError(t, err, tc.want)
		})
	}
}
