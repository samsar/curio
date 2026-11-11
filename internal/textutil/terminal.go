package textutil

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

// TerminalSafe makes s safe to write to a terminal: every C0 control but
// newline and tab, every C1 control, and Unicode's format characters
// (bidi overrides and isolates, zero-width spaces, soft hyphens) are shown
// as their escape, so a page title or snippet cannot drive the terminal.
// Newline and tab stay, for multi-line error text, page content and
// tables. The zero-width non-joiner and joiner stay: they are words' own
// letters in Persian, Hindi and emoji sequences.
func TerminalSafe(s string) string {
	if !strings.ContainsFunc(s, terminalUnsafe) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case !terminalUnsafe(r):
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
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

// terminalWriter replaces the C0 controls a terminal would act on (every
// one but newline and tab) with their escape, and passes everything else
// through, including valid UTF-8. It is the writer Run puts in front of
// stdout: command output keeps its lines and columns, while a stored ESC,
// backspace or carriage return cannot reach the terminal as a control.
type terminalWriter struct{ w io.Writer }

// NewTerminalWriter returns w with terminal control characters neutralized.
func NewTerminalWriter(w io.Writer) io.Writer { return terminalWriter{w: w} }

func (t terminalWriter) Write(p []byte) (int, error) {
	var b strings.Builder
	b.Grow(len(p))
	for _, c := range p {
		if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			fmt.Fprintf(&b, `\x%02x`, c)
			continue
		}
		b.WriteByte(c)
	}
	if _, err := io.WriteString(t.w, b.String()); err != nil {
		return 0, err
	}
	return len(p), nil
}
