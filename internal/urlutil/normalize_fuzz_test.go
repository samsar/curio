package urlutil

import (
	"net/url"
	"testing"
)

// FuzzNormalize checks the two properties dedup relies on: anything
// Normalize accepts is a fetchable http(s) URL with a host, and
// normalizing it again changes nothing (clients normalize before the
// daemon normalizes again). The seeds are every input of the Normalize
// case tables, plus inputs earlier fuzzing found that no table has
// verbatim.
func FuzzNormalize(f *testing.F) {
	for _, tc := range normalizeCases {
		f.Add(tc.in)
	}
	for _, tc := range youTubeCases {
		f.Add(tc.in)
	}
	for _, tc := range invalidYouTubeIDCases {
		f.Add(tc.in)
	}
	for _, tc := range invalidURLCases {
		f.Add(tc.in)
	}
	for _, raw := range []string{
		"http://0?%\x82\x82\x82\x82 #",
		"http://0?00&0&0&0&0&0&0&0&0&0&0&0&0&0&0&\x9a0%",
	} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		out, err := Normalize(raw)
		if err != nil {
			return
		}
		u, err := url.Parse(out)
		if err != nil {
			t.Fatalf("Normalize(%q) = %q, which doesn't parse: %v", raw, out, err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			t.Fatalf("Normalize(%q) = %q, not an http(s) URL with a host", raw, out)
		}
		again, err := Normalize(out)
		if err != nil {
			t.Fatalf("Normalize(%q) = %q, which Normalize rejects: %v", raw, out, err)
		}
		if again != out {
			t.Fatalf("not idempotent: %q → %q → %q", raw, out, again)
		}
	})
}
