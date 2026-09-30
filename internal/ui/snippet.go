package ui

import (
	"slices"
	"strings"
	"unicode"
)

// passageRunes is how much of a chunk's text a passage without a snippet
// shows.
const passageRunes = 300

// Passage is what a search result shows of a matching chunk: its BM25
// snippet with the matched terms marked, or, for a chunk only the vector
// search found, the start of its text. Chunks are stored markdown, whose
// markup reads as noise in a line of text (a chunk holds no newlines, so
// even a heading is inline), so the markup is taken out and the text it
// wraps kept: a link's text without its destination, an image's alt text,
// emphasis without its asterisks. A snippet the markup leaves nothing of
// falls back to the text. The segments are plain text, which the template
// escapes; the document's page shows the whole text as it is formatted.
func Passage(snippet, text string) []Segment {
	if segs := newPassage(Highlight(snippet)).clean(); visible(segs) {
		return segs
	}
	var b strings.Builder
	for _, s := range newPassage([]Segment{{Text: text}}).clean() {
		b.WriteString(s.Text)
	}
	if b.Len() == 0 {
		return nil
	}
	return []Segment{{Text: Excerpt(b.String(), passageRunes)}}
}

// visible reports whether segs show anything but FTS5's ellipses.
func visible(segs []Segment) bool {
	return slices.ContainsFunc(segs, func(s Segment) bool { return strings.Trim(s.Text, "… ") != "" })
}

// ellipsis is how FTS5 marks where it cut a snippet's text.
const ellipsis = '…'

// passage is a text being cleaned, a rune at a time: what the search
// marked, what an escape made literal, and what the cleaning drops. Each
// rule is one pass over it, which never goes back over what it has read,
// so cleaning takes time in proportion to the text, however it is built.
type passage struct {
	r       []rune
	mark    []bool
	literal []bool // escaped: taken as itself, never read as markup
	drop    []bool
	start   int // where the text starts, after FTS5's leading ellipsis
}

func newPassage(segs []Segment) *passage {
	p := &passage{}
	for _, s := range segs {
		for _, c := range s.Text {
			p.r = append(p.r, c)
			p.mark = append(p.mark, s.Mark)
		}
	}
	p.literal = make([]bool, len(p.r))
	p.drop = make([]bool, len(p.r))
	if len(p.r) > 0 && p.r[0] == ellipsis {
		p.start = 1
	}
	return p
}

// clean drops the markup and returns what is left, its whitespace
// collapsed and its marks merged.
func (p *passage) clean() []Segment {
	p.escapes()
	p.leadingDestination()
	p.links()
	p.emphasis()
	p.headings()
	p.quotes()
	p.htmlTags()
	p.tableRules()
	return p.segments()
}

// markup reports whether the rune at i can be markup: it is not dropped
// already, and no escape made it literal.
func (p *passage) markup(i int) bool { return !p.drop[i] && !p.literal[i] }

// dropRange drops the runes [from, to).
func (p *passage) dropRange(from, to int) {
	for i := from; i < to; i++ {
		p.drop[i] = true
	}
}

// escapes drops the backslash of each escape, a backslash before ASCII
// punctuation, and makes the punctuation literal.
func (p *passage) escapes() {
	for i := 0; i+1 < len(p.r); i++ {
		if p.r[i] == '\\' && isASCIIPunct(p.r[i+1]) {
			p.drop[i], p.literal[i+1] = true, true
			i++
		}
	}
}

// leadingDestination drops the end of a link's destination a snippet
// starts inside: its leading run of non-whitespace, up to the first ')'
// that closes no '(' in it. A ')' followed by ']' is the end of a link's
// text instead, and a bracket ends the run: what comes before a "](" is
// text, which links keeps.
func (p *passage) leadingDestination() {
	depth := 0
	for i := p.start; i < len(p.r); i++ {
		c := p.r[i]
		if unicode.IsSpace(c) || (p.markup(i) && (c == '[' || c == ']')) {
			return
		}
		if !p.markup(i) {
			continue
		}
		switch {
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		case c == ')':
			if i > p.start && (i+1 == len(p.r) || p.r[i+1] != ']') {
				p.dropRange(p.start, i+1)
			}
			return
		}
	}
}

// links drops the markup of links and images: [text](destination) and
// [text][label] keep their text, ![alt](source) its alt text, whatever the
// destination's scheme. A snippet cuts them anywhere: a "](destination)"
// whose '[' is before the snippet keeps the text before it, a destination
// cut at the end is dropped to the end, and a '[' never closed is dropped,
// its text kept.
func (p *passage) links() {
	type open struct {
		at    int
		image bool
	}
	var opens []open
	n := len(p.r)
	for i := p.start; i < n; i++ {
		if !p.markup(i) {
			continue
		}
		switch p.r[i] {
		case '[':
			opens = append(opens, open{at: i, image: i > 0 && p.r[i-1] == '!' && p.markup(i-1)})
		case ']':
			var end int
			switch {
			case i+1 < n && p.r[i+1] == '(' && p.markup(i+1):
				end = p.closing(i+2, '(', ')')
			case i+1 < n && p.r[i+1] == '[' && p.markup(i+1):
				end = p.closing(i+2, '[', ']')
			default:
				// A bracket without a destination is text, and so is its '['.
				if len(opens) > 0 {
					opens = opens[:len(opens)-1]
				}
				continue
			}
			p.dropRange(i, min(end+1, n))
			if end == n && p.r[n-1] == ellipsis {
				p.drop[n-1] = false
			}
			if len(opens) > 0 {
				p.dropOpen(opens[len(opens)-1].at, opens[len(opens)-1].image)
				opens = opens[:len(opens)-1]
			}
			i = end
		}
	}
	for _, o := range opens {
		p.dropOpen(o.at, o.image)
	}
}

// dropOpen drops a link's '[' at i, and an image's '!' before it.
func (p *passage) dropOpen(i int, image bool) {
	p.drop[i] = true
	if image {
		p.drop[i-1] = true
	}
}

// closing is the index of the closer that ends a bracket opened just
// before from, counting the nested pairs of opener and closer in between,
// or len(r) when the text ends first.
func (p *passage) closing(from int, opener, closer rune) int {
	depth := 0
	for i := from; i < len(p.r); i++ {
		if p.literal[i] {
			continue
		}
		switch p.r[i] {
		case opener:
			depth++
		case closer:
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return len(p.r)
}

// emphasis drops the markup of emphasis and code: runs of '*' and '_',
// except between two letters or digits (snake_case, 5*3), runs of two or
// more '~', and runs of '`'.
func (p *passage) emphasis() {
	n := len(p.r)
	for i := 0; i < n; {
		c := p.r[i]
		if !p.markup(i) || !strings.ContainsRune("*_~`", c) {
			i++
			continue
		}
		end := i
		for end < n && p.r[end] == c && p.markup(end) {
			end++
		}
		var before, after rune
		if i > 0 {
			before = p.r[i-1]
		}
		if end < n {
			after = p.r[end]
		}
		if c == '`' || (c == '~' && end-i >= 2) || ((c == '*' || c == '_') && (!isWord(before) || !isWord(after))) {
			p.dropRange(i, end)
		}
		i = end
	}
}

// headings drops a heading's marker, flattened into the line: one to six
// '#' followed by a space, at the start or after whitespace.
func (p *passage) headings() {
	n := len(p.r)
	for i := p.start; i < n; {
		if p.r[i] != '#' || !p.markup(i) || (i > p.start && !unicode.IsSpace(p.r[i-1])) {
			i++
			continue
		}
		end := i
		for end < n && p.r[end] == '#' && p.markup(end) {
			end++
		}
		if end-i <= 6 && end < n && unicode.IsSpace(p.r[end]) {
			p.dropRange(i, end)
		}
		i = end
	}
}

// quotes drops a blockquote's markers, flattened into the line: a run of
// '>' ("> >" for a nested quote) followed by a space, at the start, or
// after whitespace that follows no letter or digit. So "x > 5" and "->"
// keep theirs.
func (p *passage) quotes() {
	n := len(p.r)
	var last rune // the last rune kept, whitespace aside; 0 before any
	for i := 0; i < n; {
		if p.r[i] == '>' && p.markup(i) && (i == p.start || (i > 0 && unicode.IsSpace(p.r[i-1]))) {
			end := i
			for end < n && ((p.r[end] == '>' && p.markup(end)) ||
				(p.r[end] == ' ' && end+1 < n && p.r[end+1] == '>' && p.markup(end+1))) {
				end++
			}
			if end < n && unicode.IsSpace(p.r[end]) && !isWord(last) {
				p.dropRange(i, end)
			} else {
				last = '>'
			}
			i = end
			continue
		}
		if !p.drop[i] && !unicode.IsSpace(p.r[i]) {
			last = p.r[i]
		}
		i++
	}
}

// htmlElements are the elements whose tags htmlTags drops, keeping what
// they wrap. Any other angle-bracketed text (List<String>, <script>) stays
// text, which the page escapes.
var htmlElements = map[string]bool{
	"a": true, "br": true, "img": true, "div": true, "span": true, "p": true, "b": true, "i": true, "u": true,
	"s": true, "strong": true, "em": true, "code": true, "kbd": true, "pre": true, "sub": true, "sup": true,
	"small": true, "mark": true, "details": true, "summary": true, "ul": true, "ol": true, "li": true,
	"table": true, "thead": true, "tbody": true, "tr": true, "td": true, "th": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "hr": true, "picture": true, "source": true,
	"figure": true, "figcaption": true, "video": true, "audio": true, "iframe": true, "center": true,
	"font": true,
}

// htmlTags drops the tags of htmlElements, opening and closing.
func (p *passage) htmlTags() {
	for i := 0; i < len(p.r); i++ {
		if p.r[i] != '<' || !p.markup(i) {
			continue
		}
		if end, ok := p.htmlTag(i); ok {
			p.dropRange(i, end+1)
			i = end
		}
	}
}

// htmlTag reports whether a tag of one of htmlElements starts at the '<'
// at i, and the index of its '>'. The tag ends at the first '>', and a '<'
// before it means it wasn't one.
func (p *passage) htmlTag(i int) (int, bool) {
	n := len(p.r)
	j := i + 1
	if j < n && p.r[j] == '/' {
		j++
	}
	name := j
	for j < n && p.r[j] < unicode.MaxASCII && (unicode.IsLetter(p.r[j]) || unicode.IsDigit(p.r[j])) {
		j++
	}
	if !htmlElements[strings.ToLower(string(p.r[name:j]))] ||
		(j < n && !unicode.IsSpace(p.r[j]) && p.r[j] != '/' && p.r[j] != '>') {
		return 0, false
	}
	for ; j < n; j++ {
		switch p.r[j] {
		case '>':
			return j, true
		case '<':
			return 0, false
		}
	}
	return 0, false
}

// tableRules drops a table's markup, flattened into the line: each word
// made of '|', '-' and ':' alone with a '|' in it ("|", "|---|:--|").
func (p *passage) tableRules() {
	n := len(p.r)
	for i := 0; i < n; {
		if unicode.IsSpace(p.r[i]) {
			i++
			continue
		}
		end, pipe, other := i, false, false
		for ; end < n && !unicode.IsSpace(p.r[end]); end++ {
			switch c := p.r[end]; {
			case p.drop[end]:
			case p.literal[end] || (c != '|' && c != '-' && c != ':'):
				other = true
			case c == '|':
				pipe = true
			}
		}
		if pipe && !other {
			p.dropRange(i, end)
		}
		i = end
	}
}

// char is a rune of a cleaned passage, and whether the search marked it.
type char struct {
	r    rune
	mark bool
}

// segments is what is left: its whitespace collapsed, its marks merged,
// as runs of marked and unmarked text.
func (p *passage) segments() []Segment {
	chars := p.kept()
	mergeMarks(chars)
	var segs []Segment
	var b strings.Builder
	for i, c := range chars {
		b.WriteRune(c.r)
		if i+1 == len(chars) || chars[i+1].mark != c.mark {
			segs = append(segs, Segment{Text: b.String(), Mark: c.mark})
			b.Reset()
		}
	}
	return segs
}

// kept is the runes not dropped, whitespace collapsed to a space and
// trimmed, with no space between FTS5's ellipses and the text they cut.
func (p *passage) kept() []char {
	chars := make([]char, 0, len(p.r))
	space := true // leading whitespace goes, as after a space
	for i, c := range p.r {
		switch {
		case p.drop[i]:
			continue
		case unicode.IsSpace(c):
			if space {
				continue
			}
			c, space = ' ', true
		default:
			space = false
		}
		chars = append(chars, char{c, p.mark[i]})
	}
	if n := len(chars); n > 0 && chars[n-1].r == ' ' {
		chars = chars[:n-1]
	}
	if len(chars) > 1 && chars[0].r == ellipsis && chars[1].r == ' ' {
		chars = slices.Delete(chars, 1, 2)
	}
	if n := len(chars); n > 1 && chars[n-1].r == ellipsis && chars[n-2].r == ' ' {
		chars = slices.Delete(chars, n-2, n-1)
	}
	return chars
}

// maxMarkGap is the longest gap mergeMarks closes between two marked runs.
const maxMarkGap = 3

// mergeMarks marks each gap between two marked runs that is at most
// maxMarkGap runes of spaces and hyphens or dashes, so "single-table
// design" and "Vendor Lock-in" read as one match rather than three.
func mergeMarks(chars []char) {
	for i := 0; i < len(chars); {
		if chars[i].mark {
			i++
			continue
		}
		end := i
		for end < len(chars) && !chars[end].mark {
			end++
		}
		if i > 0 && end < len(chars) && end-i <= maxMarkGap && joins(chars[i:end]) {
			for j := i; j < end; j++ {
				chars[j].mark = true
			}
		}
		i = end
	}
}

// joins reports whether gap is spaces and hyphens or dashes alone.
func joins(gap []char) bool {
	for _, c := range gap {
		if !strings.ContainsRune(" -‐–", c.r) {
			return false
		}
	}
	return true
}

// isWord reports whether c is a letter or a digit.
func isWord(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }

// isASCIIPunct reports whether c is ASCII punctuation, which a backslash
// escapes in markdown.
func isASCIIPunct(c rune) bool {
	return c < unicode.MaxASCII && strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c)
}
