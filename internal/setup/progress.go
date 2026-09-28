package setup

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// redrawInterval caps a terminal's progress line at about 10 redraws a
// second: a pull reports progress far more often than that, and each
// redraw is a write to the terminal.
const redrawInterval = 100 * time.Millisecond

// progressLine is a Progress on a writer: on a terminal one line redrawn
// in place, elsewhere a line at each 10%, which a screen reader or a log
// can follow. It never shows less done than it showed before, and ends at
// 100% when the work is done.
type progressLine struct {
	out    io.Writer
	title  string
	redraw bool
	now    func() time.Time

	mu               sync.Mutex
	completed, total int64
	drawnAt          time.Time
	step             int // the last tenth printed, without redraw
	done             bool
}

func newProgress(out io.Writer, title string, redraw bool, now func() time.Time) *progressLine {
	return &progressLine{out: out, title: title, redraw: redraw, now: now}
}

func (p *progressLine) Update(completed, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	p.completed = max(p.completed, completed)
	p.total = max(p.total, total, p.completed)
	if p.redraw {
		if at := p.now(); at.Sub(p.drawnAt) >= redrawInterval {
			p.drawnAt = at
			fmt.Fprintf(p.out, "\r\x1b[K%s", p.line())
		}
		return
	}
	if step := percent(p.completed, p.total) / 10; step > p.step {
		p.step = step
		fmt.Fprintln(p.out, p.line())
	}
}

func (p *progressLine) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	p.done = true
	p.total = max(p.total, p.completed)
	p.completed = p.total
	switch {
	case p.redraw:
		fmt.Fprintf(p.out, "\r\x1b[K%s\n", p.line())
	case p.step < 10:
		fmt.Fprintln(p.out, p.line())
	}
}

func (p *progressLine) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	p.done = true
	if p.redraw && !p.drawnAt.IsZero() {
		fmt.Fprintln(p.out) // end the line redrawn in place
	}
}

// line is the progress as one line: "pulling gemma4:26b: 40% (7.6 GB of
// 19 GB)".
func (p *progressLine) line() string {
	pct := percent(p.completed, p.total)
	if p.total == 0 {
		pct = 100 // done, with no size ever reported
		if !p.done {
			pct = 0
		}
	}
	s := fmt.Sprintf("%s: %d%%", p.title, pct)
	if p.total > 0 {
		s += fmt.Sprintf(" (%s of %s)", FormatSize(byteCount(p.completed)), FormatSize(byteCount(p.total)))
	}
	return s
}

// byteCount is n bytes as a size, a negative count as none.
func byteCount(n int64) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// percent is completed out of total as a whole percentage, at most 100.
func percent(completed, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(min(100, completed*100/total))
}

// FormatSize writes a size in bytes the way Ollama's library does, in
// decimal units: "639 MB", "2.5 GB", "19 GB".
func FormatSize(n uint64) string {
	const (
		kb = 1000
		mb = 1000 * kb
		gb = 1000 * mb
	)
	switch {
	case n >= gb:
		return trimZero(float64(n)/gb) + " GB"
	case n >= mb:
		return strconv.FormatUint((n+mb/2)/mb, 10) + " MB"
	case n >= kb:
		return strconv.FormatUint((n+kb/2)/kb, 10) + " kB"
	default:
		return strconv.FormatUint(n, 10) + " B"
	}
}

// trimZero writes f to one decimal, without a trailing ".0".
func trimZero(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0")
}
