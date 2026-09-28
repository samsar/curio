package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
