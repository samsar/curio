package ui

import (
	"bytes"
	"runtime/metrics"
	"sync/atomic"
	"time"
)

// Formatting budgets: how much a document's markdown may hold of what
// makes goldmark's work or memory grow faster than the text, and still be
// formatted. A stored page can hold any of it, by accident or by design (a
// 1 MiB line of > took four and a half minutes). The budgets decide before
// goldmark parses, the same way on every machine; a render they let
// through is still stopped once it passes maxFormatTime or maxFormatAlloc
// (formatLimits), for the shapes they miss. A document over a budget or a
// limit is shown as the plain text it is stored as, which costs what its
// length does. docs/decisions.md "Dashboard: formatting budgets for
// stored markdown" has the measurements.
const (
	// maxLineNesting is how many blockquotes and list items one line may
	// open: goldmark's work on a line grows with their square.
	maxLineNesting = 32
	// maxContainers bounds the blockquote and list markers of the whole
	// text, each a container goldmark builds and writes tags for.
	maxContainers = 1 << 17
	// maxBlockVisits bounds goldmark's block parse: the sum, over the
	// text's lines, of the open blocks it visits at each. A blank line
	// visits every open list, which stays open over any number of them,
	// and every visit is recorded until the top-level block ends.
	maxBlockVisits = 1 << 21
	// maxVisitBytes bounds what those visits read: the sum, over the
	// text's lines, of their visits times their length. Each visit reads
	// the line's indentation again, and copies the rest of the line when a
	// tab is split between two blocks.
	maxVisitBytes = 1 << 28
	// maxInlineWork bounds goldmark's inline parsing: the sum, over the
	// text's paragraphs, of their markup characters times their length.
	// Each can start a scan to the end of its paragraph (an unclosed [, (
	// or `), or be compared with every other delimiter in it.
	maxInlineWork = 1 << 31
	// maxEmphasisWork bounds goldmark's emphasis matching: the sum, over
	// the text's paragraphs, of the square of their runs of *, _ and ~.
	// Each run that can close is compared with every run before it, back
	// to one it pairs with, and a run that pairs with none can stay to be
	// compared with the next.
	maxEmphasisWork = 1 << 27
	// maxTableCells bounds the cells of the text's tables: goldmark pads
	// every row to its header's width, so a wide header over many short
	// lines makes cells out of nothing.
	maxTableCells = 1 << 18
	// maxLinkBytes bounds the destinations and titles of the text's links
	// and images, as stored, counted for every one of them, kept or not,
	// before it is resolved: every link to a reference definition repeats
	// its destination, and resolving one copies it several times over.
	maxLinkBytes = 8 << 20
	// maxHTMLBytes bounds the HTML goldmark writes. Escaping makes a
	// link's URL up to five times longer than maxLinkBytes counts it (&
	// is &amp;), and sanitizing and serving the page cost what the HTML
	// does.
	maxHTMLBytes = 8 * MaxRenderedMarkdown
)

// The limits every render is held to, whatever its text: it stops at the
// first check after maxFormatTime has passed or maxFormatAlloc bytes have
// been allocated since it began. They sit well above the most a text
// within the budgets takes (about a second, and 330 MiB allocated), so the
// budgets stay the decision, and the limits stop only a shape they miss.
// Each needs the other: within two seconds a render can allocate
// gigabytes, and one that allocates little can run on.
const (
	maxFormatTime  = 2 * time.Second
	maxFormatAlloc = 1 << 30
)

// budgetError is a formatting budget or limit a text is over, and why its
// page shows it unformatted.
type budgetError struct{ reason string }

func (e *budgetError) Error() string { return "over a formatting budget: " + e.reason }

var (
	errDeepNesting    = &budgetError{"a line opens too many blockquotes and lists"}
	errManyContainers = &budgetError{"it has too many blockquotes and list items"}
	errBlockVisits    = &budgetError{"its nested lists and blockquotes run over too many lines"}
	errVisitBytes     = &budgetError{"its lines are nested too deep for their length"}
	errDenseMarkup    = &budgetError{"its paragraphs hold too much markup"}
	errEmphasis       = &budgetError{"its paragraphs hold too many runs of *, _ and ~"}
	errTableCells     = &budgetError{"its tables have too many cells"}
	errLinkBytes      = &budgetError{"its links repeat too much text"}
	errHTMLBytes      = &budgetError{"it makes too much HTML"}
	errFormatTime     = &budgetError{"its markup takes too much work"}
	errFormatAlloc    = &budgetError{"its markup takes too much memory"}
)

// allocCheckEvery is how often a render's checks read the process's
// allocations: a read costs about as much as a hundred checks (0.2 µs),
// so reading at every 256th keeps the reads' cost under the checks'.
const allocCheckEvery = 256

// formatLimits holds one render to maxFormatTime and maxFormatAlloc, or
// what a test sets instead. goldmark can't be cancelled, so the render
// checks them from goldmark's extension points (at every inline trigger,
// every line the block parse reads, every link, autolink and image, and
// every write of HTML), and a check that fails unwinds goldmark by
// panicking with its *budgetError, which format recovers. Only the
// render's goroutine checks; timeUp is set from its timer's.
type formatLimits struct {
	timer  *time.Timer
	timeUp atomic.Bool
	checks int
	// The process's heap allocations when the render began, and how many
	// more it may make.
	start, maxAlloc uint64
	allocs          [1]metrics.Sample
}

// startLimits starts a render's limits: its time runs from now, and its
// allocations count from here. stop releases its timer.
func startLimits(maxTime time.Duration, maxAlloc uint64) *formatLimits {
	l := &formatLimits{maxAlloc: maxAlloc}
	l.allocs[0].Name = "/gc/heap/allocs:bytes"
	l.start = l.allocated()
	l.timer = time.AfterFunc(maxTime, func() { l.timeUp.Store(true) })
	return l
}

func (l *formatLimits) stop() { l.timer.Stop() }

// check panics with errFormatTime once the render's time is up, and with
// errFormatAlloc once the process has allocated more than the render may
// since it began. The allocations are the whole process's, so other
// requests can only stop a render sooner.
func (l *formatLimits) check() {
	if l.timeUp.Load() {
		panic(errFormatTime)
	}
	if l.checks++; l.checks%allocCheckEvery == 0 && l.allocated()-l.start > l.maxAlloc {
		panic(errFormatAlloc)
	}
}

// allocated is how many bytes the process has allocated on the heap.
func (l *formatLimits) allocated() uint64 {
	metrics.Read(l.allocs[:])
	return l.allocs[0].Value.Uint64()
}

// markup marks the characters goldmark's inline parsers start at (code
// spans, links and images, emphasis and strikethrough, raw HTML and
// autolinks, task list items) and (, where an inline link's destination
// starts, which the ] before it scans to the end of the line.
// TestMarkup_CoversInlineTriggers keeps the list in step with the parsers
// newMarkdown uses.
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
	containers, cells int
	// The block parse: at most how many blocks are open after the last
	// line, whether that line was blank, and the visits and the bytes they
	// read, charged so far.
	open               int
	blank              bool
	visits, visitBytes int64
	// The inline and emphasis work of the paragraphs before this one.
	work, emphasis int64
	// This paragraph's markup characters, runs of *, _ and ~, and bytes,
	// and the width of its widest delimiter row: goldmark makes a table of
	// a paragraph with a delimiter row, whose rows are every line after it.
	markup, runs, size int64
	cols               int
}

func (s *shape) add(line []byte) error {
	opened, rest := containerMarkers(line)
	if opened > maxLineNesting {
		return errDeepNesting
	}
	if s.containers += opened; s.containers > maxContainers {
		return errManyContainers
	}
	if err := s.visitBlocks(line, rest, opened); err != nil {
		return err
	}
	if s.blank {
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
	var prev byte
	for _, c := range line {
		if markup[c] {
			s.markup++
			if (c == '*' || c == '_' || c == '~') && c != prev {
				s.runs++
			}
		}
		prev = c
	}
	s.size += int64(len(line))
	return nil
}

// visitBlocks charges goldmark's block parse for line, whose leading
// spaces, tabs and container markers (markers of them) end where rest
// starts, and keeps count of the blocks that can be open after it. On
// every line goldmark visits the open blocks in order, up to the first the
// line doesn't continue.
//
// A line continues a blockquote with a > of its own, a list with a marker
// of its own or as much indentation as its item's content, at least two
// columns, and the item with that same indentation. Once what is left of
// the line is blank, it continues every list and list item beneath, as a
// blank line does. So a line with more than spaces, tabs and markers
// continues at most one block per marker and per column of indentation,
// and visits one more; a line with no more than those can visit every open
// block. Each marker opens at most two blocks, a list and its item, and
// the line one more, the paragraph or code it holds; a blank line opens
// none. The blocks a line doesn't continue stay open only when it
// continues a paragraph lazily, which a line after a blank one can't: no
// paragraph is open across a blank line.
func (s *shape) visitBlocks(line, rest []byte, markers int) error {
	blank := isBlank(line)
	reach := min(s.open, columns(line[:len(line)-len(rest)])+markers)
	visits, open := min(s.open, reach+1), reach+2*markers+1
	switch {
	case blank:
		visits, open = s.open, s.open
	case isBlank(rest):
		visits, open = s.open, max(open, s.open)
	case !s.blank:
		open = max(open, s.open)
	}
	s.open, s.blank = open, blank
	s.visits += int64(visits)
	s.visitBytes += int64(visits) * int64(len(line))
	switch {
	case s.visits > maxBlockVisits:
		return errBlockVisits
	case s.visitBytes > maxVisitBytes:
		return errVisitBytes
	}
	return nil
}

func (s *shape) endParagraph() error {
	s.work += s.markup * s.size
	s.emphasis += s.runs * s.runs
	s.markup, s.runs, s.size, s.cols = 0, 0, 0, 0
	switch {
	case s.work > maxInlineWork:
		return errDenseMarkup
	case s.emphasis > maxEmphasisWork:
		return errEmphasis
	}
	return nil
}

// containerMarkers counts the blockquote and list markers line starts
// with, each a container the line can open, and returns the rest of it. It
// stops counting past maxLineNesting. A thematic break isn't markers:
// goldmark takes - - - for one before it tries a list.
func containerMarkers(line []byte) (int, []byte) {
	n, i := 0, 0
	for i < len(line) && n <= maxLineNesting {
		switch c := line[i]; {
		case c == ' ' || c == '\t':
			i++
			continue
		case c == '>':
			i++
		case c == '-' || c == '+' || c == '*':
			if !markerEnds(line, i+1) || isThematicBreak(line[i:]) {
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
	return n, line[i:]
}

// isThematicBreak reports whether line is a thematic break: three or more
// of -, * or _, the same one, and nothing else but spaces.
func isThematicBreak(line []byte) bool {
	if len(line) == 0 || (line[0] != '-' && line[0] != '*' && line[0] != '_') {
		return false
	}
	n := 0
	for _, c := range line {
		switch {
		case c == line[0]:
			n++
		case !isSpace(c):
			return false
		}
	}
	return n >= 3
}

// columns is at most how many columns the spaces and tabs in b fill: a
// tab fills up to four.
func columns(b []byte) int {
	n := 0
	for _, c := range b {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		}
	}
	return n
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
