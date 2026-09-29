package ui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHighlight(t *testing.T) {
	cases := []struct {
		name    string
		snippet string
		want    []Segment
	}{
		{"no match", "plain text", []Segment{{Text: "plain text"}}},
		{"one term", "a <em>kafka</em> b", []Segment{{Text: "a "}, {Text: "kafka", Mark: true}, {Text: " b"}}},
		{"terms back to back", "<em>a</em><em>b</em>", []Segment{{Text: "a", Mark: true}, {Text: "b", Mark: true}}},
		{"hostile text stays text", "<em><script>alert(1)</script></em> <img src=x onerror=alert(1)>",
			[]Segment{{Text: "<script>alert(1)</script>", Mark: true}, {Text: " <img src=x onerror=alert(1)>"}}},
		{"an unclosed marker marks the rest", "a <em>b", []Segment{{Text: "a "}, {Text: "b", Mark: true}}},
		{"a stray close is text", "a </em> b", []Segment{{Text: "a </em> b"}}},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Highlight(tc.snippet))
		})
	}
}

func TestExcerpt(t *testing.T) {
	assert.Equal(t, "short text", Excerpt("  short\n\ttext ", 20))
	assert.Equal(t, "one two…", Excerpt("one two three", 10), "cut back to a word")
	assert.Equal(t, "abcdefghij…", Excerpt("abcdefghijklmnop", 10), "one long word is cut inside it")
	token := strings.Repeat("x", 200)
	assert.Equal(t, "see "+token[:96]+"…", Excerpt("see "+token+" end", 100),
		"a word boundary further back than excerptWordSlack is passed over")
	long := strings.Repeat("é", 400)
	got := Excerpt(long, 300)
	assert.Equal(t, strings.Repeat("é", 300)+"…", got, "counted in runes, never split")
}
