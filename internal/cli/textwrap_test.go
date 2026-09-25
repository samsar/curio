package cli

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"fits", "short", 10, "short"},
		{"ascii cut", "abcdef", 3, "abc..."},
		{"cjk cut on a rune", "日本語のタイトル日本語のタイトル", 13, "日本語のタイトル日本語のタ..."},
		{"cjk fits in runes", "日本語", 3, "日本語"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.n)
			assert.Equal(t, tc.want, got)
			assert.True(t, utf8.ValidString(got))
		})
	}
}

func TestWrapLines(t *testing.T) {
	t.Run("breaks on spaces", func(t *testing.T) {
		got := wrapLines("alpha beta gamma delta", 11)
		assert.Equal(t, []string{"alpha beta", "gamma delta"}, got)
	})
	t.Run("hard-cuts a long token", func(t *testing.T) {
		got := wrapLines(strings.Repeat("x", 25), 10)
		assert.Equal(t, []string{"xxxxxxxxxx", "xxxxxxxxxx", "xxxxx"}, got)
	})
	t.Run("empty", func(t *testing.T) {
		assert.Nil(t, wrapLines("   ", 10))
	})
	t.Run("cjk lines stay valid UTF-8", func(t *testing.T) {
		snippet := strings.Repeat("機械学習の入門と深層学習。", 30)
		got := wrapLines(snippet, 100)
		assert.Greater(t, len(got), 1)
		var rejoined strings.Builder
		for i, line := range got {
			assert.True(t, utf8.ValidString(line), "line %d is not valid UTF-8", i)
			assert.LessOrEqual(t, utf8.RuneCountInString(line), 100)
			rejoined.WriteString(line)
		}
		assert.Equal(t, snippet, rejoined.String(), "wrapping loses nothing")
	})
}
