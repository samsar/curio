package urlutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSite(t *testing.T) {
	cases := map[string]string{
		"mobile.twitter.com": "twitter.com",
		"www.forbes.com":     "forbes.com",
		"forbes.com":         "forbes.com",
		"Forbes.COM.":        "forbes.com",
		"akveo.github.io":    "akveo.github.io", // github.io is a public suffix
		"www.bbc.co.uk":      "bbc.co.uk",
		"github.io":          "github.io",
		"co.uk":              "co.uk",
		"localhost":          "localhost",
		"intranet":           "intranet",
		"127.0.0.1":          "127.0.0.1",
		"::1":                "::1",
		"":                   "",
	}
	for host, want := range cases {
		assert.Equal(t, want, Site(host), host)
	}
}

// TestRegistrableDomain: a host has one exactly when Site finds it a
// registrable domain; its own site otherwise.
func TestRegistrableDomain(t *testing.T) {
	cases := map[string]string{
		"blog.example.co.uk": "example.co.uk",
		"Example.COM.":       "example.com",
		"user.github.io":     "user.github.io",
		"xn--bcher-kva.ch":   "xn--bcher-kva.ch",
	}
	for host, want := range cases {
		got, ok := RegistrableDomain(host)
		assert.True(t, ok, host)
		assert.Equal(t, want, got, host)
	}
	for _, host := range []string{"github.io", "co.uk", "localhost", "127.0.0.1", "::1", ""} {
		got, ok := RegistrableDomain(host)
		assert.False(t, ok, host)
		assert.Empty(t, got, host)
	}
}
