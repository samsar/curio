package textutil

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTerminalSafe(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello", "hello"},
		{"newline and tab stay", "one\ntwo\tthree", "one\ntwo\tthree"},
		{"escape", "a\x1b[2Jb", `a\x1b[2Jb`},
		{"osc 52 and bell", "\x1b]52;c;Zm9v\x07", `\x1b]52;c;Zm9v\x07`},
		{"carriage return", "a\rb", `a\x0db`},
		{"backspace", "a\bb", `a\x08b`},
		{"c1 csi", "\u009b2J", `\u009b2J`},
		{"bidi override", "a\u202eb", `a\u202eb`},
		{"zero width joiner stays", "a\u200db", "a\u200db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TerminalSafe(tc.in)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "\x1b")
			assert.NotContains(t, got, "\x07")
		})
	}
}

func TestTerminalLine(t *testing.T) {
	assert.Equal(t, `one\ntwo\tthree`, TerminalLine("one\ntwo\tthree"))
	assert.Equal(t, `a\x1b[2Jb`, TerminalLine("a\x1b[2Jb"))
}

func TestTerminalWriterEscapesC1AndFormat(t *testing.T) {
	var b bytes.Buffer
	w := NewTerminalWriter(&b)
	_, _ = w.Write([]byte("a\u009bb\u202ec"))
	assert.Equal(t, `a\u009bb\u202ec`, b.String())
}

func TestTerminalWriterKeepsSplitRune(t *testing.T) {
	var b bytes.Buffer
	w := NewTerminalWriter(&b)
	_, _ = w.Write([]byte{0xc2})
	assert.Empty(t, b.String(), "an incomplete rune waits for its next byte")
	_, _ = w.Write([]byte{0x9b})
	assert.Equal(t, `\u009b`, b.String())
}

func TestTerminalWriter(t *testing.T) {
	var b bytes.Buffer
	w := NewTerminalWriter(&b)
	p := []byte("ok\x1b[2J\r\nfine\x07")
	n, err := w.Write(p)
	assert.NoError(t, err)
	assert.Equal(t, len(p), n)
	assert.Equal(t, `ok\x1b[2J\x0d
fine\x07`, b.String())
	assert.False(t, strings.Contains(b.String(), "\x1b"))
}
