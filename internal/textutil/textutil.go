// Package textutil holds small string helpers that must respect UTF-8 rune
// boundaries. Slicing a Go string by byte offset (s[:n]) cuts multi-byte
// runes in half and yields invalid UTF-8, which corrupts chunk text sent to
// the embedder, persisted labels, and terminal output alike.
package textutil

import "unicode/utf8"

// TruncateBytes returns the longest prefix of s that is at most n bytes and
// ends on a rune boundary. It returns "" when n is smaller than the first
// rune, and s itself when s already fits.
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
