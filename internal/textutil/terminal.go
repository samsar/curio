package textutil

import (
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TerminalSafe makes s safe to write to a terminal: every C0 control but
// newline and tab, every C1 control, and Unicode's format characters
// (bidi overrides and isolates, zero-width spaces, soft hyphens) are shown
// as their escape, so a page title or snippet cannot drive the terminal.
// Newline and tab stay, for multi-line error text, page content and
// tables. The zero-width non-joiner and joiner stay: they are words' own
// letters in Persian, Hindi and emoji sequences. Invalid UTF-8 is replaced
// with U+FFFD rather than passed through as raw bytes.
func TerminalSafe(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	if !strings.ContainsFunc(s, terminalUnsafe) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		writeTerminalRune(&b, r)
	}
	return b.String()
}

// TerminalLine makes s safe to write as one terminal line: TerminalSafe,
// and newline and tab escaped too, so a stored title cannot forge another
// line of output.
func TerminalLine(s string) string {
	s = TerminalSafe(s)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}

// terminalUnsafe reports whether r may drive a terminal: a C0 control but
// newline and tab, DEL, a C1 control, or a format character other than
// the zero-width non-joiner and joiner.
func terminalUnsafe(r rune) bool {
	switch r {
	case '\n', '\t', zeroWidthNonJoiner, zeroWidthJoiner:
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || unicode.Is(unicode.Cf, r)
}

// writeTerminalRune writes r to b, escaped when a terminal would act on
// it.
func writeTerminalRune(b *strings.Builder, r rune) {
	switch {
	case !terminalUnsafe(r):
		b.WriteRune(r)
	case r < 0x20 || r == 0x7f:
		fmt.Fprintf(b, `\x%02x`, r)
	default:
		fmt.Fprintf(b, `\u%04x`, r)
	}
}

// terminalWriter is the writer Run puts in front of stdout: it keeps its
// lines and columns, while a stored ESC, backspace, carriage return, C1
// control or bidi override cannot reach the terminal as a control. It
// carries an incomplete UTF-8 rune across writes, so a control split
// between two writes is still escaped and a split rune is not corrupted.
type terminalWriter struct {
	w     io.Writer
	carry []byte
}

// NewTerminalWriter returns w with terminal control characters neutralized.
func NewTerminalWriter(w io.Writer) io.Writer { return &terminalWriter{w: w} }

func (t *terminalWriter) Write(p []byte) (int, error) {
	data := make([]byte, 0, len(t.carry)+len(p))
	data = append(data, t.carry...)
	data = append(data, p...)
	t.carry = nil
	var b strings.Builder
	b.Grow(len(data))
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 && !utf8.FullRune(data) {
			// A valid rune split across writes: keep its bytes for the next
			// Write rather than replacing them with U+FFFD.
			t.carry = append(t.carry, data...)
			break
		}
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&b, `\x%02x`, data[0])
			data = data[1:]
			continue
		}
		writeTerminalRune(&b, r)
		data = data[size:]
	}
	if _, err := io.WriteString(t.w, b.String()); err != nil {
		return 0, err
	}
	return len(p), nil
}
