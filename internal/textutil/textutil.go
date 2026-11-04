// Package textutil holds small string helpers: truncation that respects
// UTF-8 rune boundaries, the check of a short text a model wrote, and
// quoting for a POSIX shell.
package textutil

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TruncateBytes returns the longest prefix of s that is at most n bytes and
// ends on a rune boundary. Slicing a Go string by byte offset (s[:n]) cuts
// multi-byte runes in half and yields invalid UTF-8, which corrupts chunk
// text sent to the embedder, persisted labels, and terminal output alike.
// It returns "" when n is smaller than the first rune, and s itself when s
// already fits.
func TruncateBytes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	// Back up to the first byte of the rune straddling the cut. Valid UTF-8
	// needs at most UTFMax-1 steps; the bound keeps a long run of stray
	// continuation bytes in invalid input from turning this into a scan.
	cut := n
	for i := 0; i < utf8.UTFMax-1 && cut > 0 && !utf8.RuneStart(s[cut]); i++ {
		cut--
	}
	return s[:cut]
}

// TruncateRunes returns the prefix of s holding at most n runes.
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// TextProblem says what keeps t, a short text a model wrote (a plan's
// rewrite or name, a summary's field), from being used, "" when nothing
// does: more than maxBytes once trimmed, a control character, which no
// such text needs and JSON writes six bytes long, or a format character
// (Unicode's Cf: bidirectional overrides and isolates, zero-width spaces,
// soft hyphens), which none needs either and which can make a line read
// other than it is. The zero-width non-joiner and joiner are words' own
// letters' business in Persian, Hindi and emoji sequences, so they stay.
func TextProblem(t string, maxBytes int) string {
	t = strings.TrimSpace(t)
	switch {
	case len(t) > maxBytes:
		return fmt.Sprintf("one of %d bytes, at most %d", len(t), maxBytes)
	case strings.ContainsFunc(t, unicode.IsControl):
		return "one with a control character"
	case strings.ContainsFunc(t, formatCharacter):
		return "one with a format character"
	}
	return ""
}

// formatCharacter reports whether r is a format character TextProblem
// refuses: Unicode's Cf but the zero-width non-joiner and joiner.
func formatCharacter(r rune) bool {
	return unicode.Is(unicode.Cf, r) && r != zeroWidthNonJoiner && r != zeroWidthJoiner
}

// The zero-width non-joiner and joiner, which shape Persian and Indic
// words and join emoji.
const (
	zeroWidthNonJoiner = '\u200c'
	zeroWidthJoiner    = '\u200d'
)

// shellSafe matches words a POSIX shell reads as themselves.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ShellQuote quotes s for a POSIX shell, leaving plain words as they are,
// so a command printed for the user can be pasted as it is.
func ShellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellList is commands as a list for a sentence, each a quoted command
// line in backticks: "`brew install ollama` and `brew services start
// ollama`".
func ShellList(commands ...[]string) string {
	quoted := make([]string, len(commands))
	for i, argv := range commands {
		quoted[i] = "`" + ShellJoin(argv) + "`"
	}
	return strings.Join(quoted, " and ")
}

// ShellJoin is argv as one command line, each word quoted by ShellQuote.
func ShellJoin(argv []string) string {
	words := make([]string, len(argv))
	for i, w := range argv {
		words[i] = ShellQuote(w)
	}
	return strings.Join(words, " ")
}
