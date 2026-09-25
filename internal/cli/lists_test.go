package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveFilter(t *testing.T) {
	cases := []struct {
		name        string
		explicit    string
		failed, all bool
		want        string
	}{
		{name: "happy path by default", want: "fetched"},
		{name: "--all is no filter", all: true, want: ""},
		{name: "--failed", failed: true, want: "failed"},
		{name: "--failed beats --all", failed: true, all: true, want: "failed"},
		{name: "explicit wins", explicit: "dead", failed: true, all: true, want: "dead"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveFilter(tc.explicit, tc.failed, tc.all, "fetched"))
		})
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"eyJ0IjoiMjAyNCJ9":        "eyJ0IjoiMjAyNCJ9",
		"/Users/me/.curio":        "/Users/me/.curio",
		"/Users/me/My Curio":      "'/Users/me/My Curio'",
		"it's":                    `'it'\''s'`,
		"http://127.0.0.1:8765/x": "http://127.0.0.1:8765/x",
	}
	for in, want := range cases {
		assert.Equal(t, want, shellQuote(in), in)
	}
}
