package ui

import "strings"

// Segment is a run of text in a search match: a term the search matched
// when Mark is set. Templates put marked segments in <mark>, escaping the
// text of both as usual.
type Segment struct {
	Text string
	Mark bool
}

// Snippet markers: the BM25 snippet wraps each matched term in them.
const (
	markOpen  = "<em>"
	markClose = "</em>"
)

// Highlight splits a BM25 snippet at its <em> and </em> markers. The
// snippet is the stored chunk's text, which is hostile, so it is never
// treated as HTML: every segment is plain text, and a marker the chunk
// itself contained just marks text too.
func Highlight(snippet string) []Segment {
	var out []Segment
	mark := false
	for snippet != "" {
		sep := markOpen
		if mark {
			sep = markClose
		}
		before, after, found := strings.Cut(snippet, sep)
		if before != "" {
			out = append(out, Segment{Text: before, Mark: mark})
		}
		if !found {
			break
		}
		snippet, mark = after, !mark
	}
	return out
}

// Excerpt is the start of text for a match without a snippet: its first
// maxRunes runes, cut back to a word boundary with an ellipsis when it is
// longer, and its whitespace collapsed.
func Excerpt(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	head := string(runes[:maxRunes])
	if i := strings.LastIndexByte(head, ' '); i > 0 {
		head = head[:i]
	}
	return head + "…"
}
