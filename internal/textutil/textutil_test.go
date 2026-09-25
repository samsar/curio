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
