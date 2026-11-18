package textutil

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Neutralize is s as a page's text or a question is sent to a writing
// model inside a prompt: valid UTF-8, its invalid bytes dropped; control
// characters and Unicode's format characters dropped (but the zero-width
// non-joiner and joiner, which words need); '<' and '>' replaced by '‹' and '›', so no text can
// write a chat template's tokens (Ollama parses "<|im_end|>" inside a
// prompt as the special token); every run of whitespace one space,
// trimmed; at most maxBytes, cut on a rune boundary. One pass, stopping
// once maxBytes are written, so a page of any length costs at most its
// share.
func Neutralize(s string, maxBytes int) string {
	return neutralize(s, maxBytes)
}

// NeutralizeWhole is s neutralized as Neutralize does, never cut: a text
// whose fit is decided by its neutralized form. '<' and '>' grow to three
// bytes each ('‹', '›'), so a cap at s's own length would cut a text that
// holds them; three times it bounds any.
func NeutralizeWhole(s string) string { return neutralize(s, 3*len(s)) }

func neutralize(s string, maxBytes int) string {
	var b strings.Builder
	b.Grow(min(len(s), maxBytes))
	space := false
	for _, r := range s {
		switch {
		case r == utf8.RuneError: // an invalid byte
			continue
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d':
			continue
		case r == '<':
			r = '‹'
		case r == '>':
			r = '›'
		}
		n := utf8.RuneLen(r)
		if space {
			n++
		}
		if b.Len()+n > maxBytes {
			break
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}
