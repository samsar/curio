package textutil

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTruncateBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"ascii fits", "hello", 5, "hello"},
		{"ascii cut", "hello", 3, "hel"},
		{"zero", "hello", 0, ""},
		{"negative", "hello", -1, ""},
		{"empty", "", 3, ""},
		{"2-byte rune kept whole", "aé", 3, "aé"},
		{"2-byte rune not split", "aé", 2, "a"},
		{"3-byte runes on boundary", "€€", 3, "€"},
		{"3-byte runes mid-rune", "€€", 5, "€"},
		{"4-byte rune mid-rune", "a😀b", 4, "a"},
		{"4-byte rune whole", "a😀b", 5, "a😀"},
		{"n smaller than first rune", "😀", 3, ""},
		{"cjk", "機械学習", 7, "機械"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateBytes(tc.in, tc.n)
			assert.Equal(t, tc.want, got)
			assert.True(t, utf8.ValidString(got))
			assert.LessOrEqual(t, len(got), max(tc.n, 0))
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"ascii fits", "hello", 10, "hello"},
		{"ascii exact", "hello", 5, "hello"},
		{"ascii cut", "hello", 2, "he"},
		{"zero", "hello", 0, ""},
		{"multibyte", "日本語のタイトル", 3, "日本語"},
		{"mixed widths", "aé€😀z", 4, "aé€😀"},
		{"empty", "", 2, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, TruncateRunes(tc.in, tc.n))
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
		"":                        "''",
	}
	for in, want := range cases {
		assert.Equal(t, want, ShellQuote(in), in)
	}
}

func TestShellList(t *testing.T) {
	assert.Equal(t, "`brew install ollama` and `brew services start ollama`",
		ShellList([]string{"brew", "install", "ollama"}, []string{"brew", "services", "start", "ollama"}))
}

func TestShellJoin(t *testing.T) {
	assert.Equal(t, "/opt/homebrew/bin/brew install ollama", ShellJoin([]string{"/opt/homebrew/bin/brew", "install", "ollama"}))
	assert.Equal(t, "/usr/bin/open -a 'Ollama App'", ShellJoin([]string{"/usr/bin/open", "-a", "Ollama App"}))
}
