package ui

import "bytes"

// Formatting budgets: how much a document's markdown may hold of what
// makes goldmark's work grow faster than the text, and still be formatted.
// A stored page can hold any of it, by accident or by design (a 1 MiB line
// of > took four and a half minutes), and goldmark can't be stopped part
// way. A document over a budget is shown as the plain text it is stored
// as, which costs what its length does. docs/decisions.md "Dashboard:
// formatting budgets for stored markdown" has the measurements.
const (
	// maxLineNesting is how many blockquotes and list items one line may
	// open: goldmark's work on a line grows with their square.
	maxLineNesting = 32
	// maxContainers bounds the blockquote and list markers of the whole
	// text, each a container goldmark builds and writes tags for.
	maxContainers = 1 << 17
	// maxInlineWork bounds goldmark's inline parsing: the sum, over the
	// text's paragraphs, of their markup characters times their length.
	// Each can start a scan to the end of its paragraph (an unclosed [, (
	// or `), or be compared with every other delimiter in it.
	maxInlineWork = 1 << 31
	// maxTableCells bounds the cells of the text's tables: goldmark pads
	// every row to its header's width, so a wide header over many short
	// lines makes cells out of nothing.
	maxTableCells = 1 << 18
	// maxLinkBytes bounds the destinations and titles of the text's links
	// and images, counted once per link: every link to a reference
	// definition repeats its URL.
	maxLinkBytes = 8 << 20
)

// budgetError is a formatting budget a text is over, and why its page
// shows it unformatted.
type budgetError struct{ reason string }

func (e *budgetError) Error() string { return "over a formatting budget: " + e.reason }

var (
	errDeepNesting    = &budgetError{"a line opens too many blockquotes and lists"}
	errManyContainers = &budgetError{"it has too many blockquotes and list items"}
	errDenseMarkup    = &budgetError{"its paragraphs hold too much markup"}
	errTableCells     = &budgetError{"its tables have too many cells"}
	errLinkBytes      = &budgetError{"its links repeat too much text"}
)

// markup marks the characters goldmark's inline parsers start at, spaces
// aside: code spans, links and images, emphasis and strikethrough, raw
// HTML and autolinks, task list items, and the linkify extension's
// triggers. A space starts linkify too, but its scan stops at the next
// space. TestMarkup_CoversInlineTriggers keeps the list in step with the
// parsers newMarkdown uses.
var markup = [256]bool{'!': true, '[': true, ']': true, '`': true, '*': true, '_': true, '~': true,
	'<': true, '(': true}

// checkShape reports the first formatting budget src is over, reading it
// once. It overestimates goldmark's work, never underestimates it: that
// is what makes the budgets bounds.
func checkShape(src []byte) error {
	var s shape
	for line := range bytes.Lines(src) {
		if err := s.add(line); err != nil {
			return err
		}
	}
	return s.endParagraph()
}

// shape is what checkShape has read so far.
//
// Its paragraphs are runs of lines between blank lines, split before a
// line that starts a list item or a heading. Every paragraph, heading and
// table cell goldmark parses inline lies within one of them, so charging
// each of them its markup times its length charges goldmark's inline work
// at least once.
type shape struct {
	containers int
	cells      int
	work       int64 // the inline work of the paragraphs before this one
	// This paragraph's markup characters and bytes, and the width of its
	// widest delimiter row: goldmark makes a table of a paragraph with a
	// delimiter row, whose rows are every line after it.
	markup, size int64
	cols         int
}

func (s *shape) add(line []byte) error {
	opened, rest := containerMarkers(line)
	if opened > maxLineNesting {
		return errDeepNesting
	}
	if s.containers += opened; s.containers > maxContainers {
		return errManyContainers
	}
	if isBlank(line) {
		return s.endParagraph()
	}
	if startsBlock(line) {
		if err := s.endParagraph(); err != nil {
			return err
		}
	}
	if cols := delimiterRowCells(rest); cols > s.cols {
		s.cells += cols // its header row's
		s.cols = cols
	}
	if s.cells += s.cols; s.cells > maxTableCells {
		return errTableCells
	}
	for _, c := range line {
		if markup[c] {
			s.markup++
		}
	}
	s.size += int64(len(line))
	return nil
}

func (s *shape) endParagraph() error {
	s.work += s.markup * s.size
	s.markup, s.size, s.cols = 0, 0, 0
	if s.work > maxInlineWork {
		return errDenseMarkup
	}
	return nil
}

// containerMarkers counts the blockquote and list markers line starts
// with, each a container the line can open, and returns the rest of it.
func containerMarkers(line []byte) (int, []byte) {
	n := 0
	for i := 0; i < len(line); {
		switch c := line[i]; {
		case c == ' ' || c == '\t':
			i++
			continue
		case c == '>':
			i++
		case c == '-' || c == '+' || c == '*':
			if !markerEnds(line, i+1) {
				return n, line[i:]
			}
			i++
		case isDigit(c):
			j := i + 1
			for j < len(line) && j-i < 9 && isDigit(line[j]) {
				j++
			}
			if j == len(line) || (line[j] != '.' && line[j] != ')') || !markerEnds(line, j+1) {
				return n, line[i:]
			}
			i = j + 1
		default:
			return n, line[i:]
		}
		n++
	}
	return n, nil
}

// markerEnds reports whether a list marker ending before line[i] is one:
// followed by a space, a tab or the end of the line.
func markerEnds(line []byte, i int) bool {
	return i == len(line) || isSpace(line[i])
}

// startsBlock reports whether line starts a list item with content or an
// ATX heading, at most three spaces in after any blockquote markers. Either
// interrupts a paragraph, whatever contains it, so goldmark never parses
// the lines before and after it inline together.
func startsBlock(line []byte) bool {
	i := 0
	for {
		j := i
		for j < len(line) && j-i < 3 && line[j] == ' ' {
			j++
		}
		if j == len(line) || line[j] != '>' {
			i = j
			break
		}
		i = j + 1
		if i < len(line) && line[i] == ' ' {
			i++
		}
	}
	rest := line[i:]
	switch {
	case len(rest) == 0:
		return false
	case rest[0] == '-' || rest[0] == '+' || rest[0] == '*':
		return len(rest) > 1 && (rest[1] == ' ' || rest[1] == '\t') && !isBlank(rest[2:])
	case rest[0] == '#':
		n := 1
		for n < len(rest) && rest[n] == '#' {
			n++
		}
		return n <= 6 && markerEnds(rest, n)
	}
	return false
}

// delimiterRowCells is how many cells line has when it can be a table's
// delimiter row, only hyphens, colons, pipes and spaces: its cells with a
// hyphen. It is 0 for any other line.
func delimiterRowCells(line []byte) int {
	cells, hyphen := 0, false
	for _, c := range line {
		switch {
		case c == '-':
			hyphen = true
		case c == '|':
			if hyphen {
				cells++
			}
			hyphen = false
		case c != ':' && !isSpace(c):
			return 0
		}
	}
	if hyphen {
		cells++
	}
	return cells
}

// isBlank reports whether line has only spaces, as goldmark counts them:
// spaces, tabs, carriage returns and newlines.
func isBlank(line []byte) bool {
	for _, c := range line {
		if !isSpace(c) {
			return false
		}
	}
	return true
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
