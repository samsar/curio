package urlutil

import (
	"net/url"
	"testing"
)

// FuzzNormalize checks the two properties dedup relies on: anything
// Normalize accepts is a fetchable http(s) URL with a host, and
// normalizing it again changes nothing (clients normalize before the
// daemon normalizes again). The seeds cover every table case, including
// the inputs earlier fuzzing found.
func FuzzNormalize(f *testing.F) {
	for _, seed := range []string{
		"https://example.com/article",
		"HTTPS://Example.COM:443/Article#section1",
		"https://example.com/x?utm_source=a&id=1",
		"https://example.com/x?b=2&a=1",
		"https://example.com/x?a=1;b=2",
		"https://example.com/x?ref=main&x=1",
		"https://example.com/x?flag",
		"https://example.com",
		"https://example.com?x=1",
		"https://example.com/x?q=%zz&utm_source=a",
		"https://example.com/x?q=a%20b",
		"https://user:pass@example.com/x",
		"https://[::1]:443/x",
		"https://[::1]:8443/x",
		"https://0000000::",
		"https://youtu.be/dQw4w9WgXcQ?si=abc",
		"https://www.youtube.com/watch?v=abc%26list%3Dx",
		"https://www.youtube.com/watch?v=ab%20cd",
		"http://Youtu.Be/0&0",
		"http://Youtu.Be/&",
		"http://0?%\x82\x82\x82\x82 #",
		"http://0?00&0&0&0&0&0&0&0&0&0&0&0&0&0&0&\x9a0%",
		"https:example.com/x",
		"https:///x",
		"javascript:alert(1)",
		"file:///etc/passwd",
	} {
		f.Add(seed)
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
