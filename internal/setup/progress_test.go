package setup

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// clock is a time a test moves by hand.
type clock struct{ now time.Time }

func newClock() *clock { return &clock{now: time.Date(2026, 9, 27, 22, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// lines splits output into its lines.
func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

// redraws splits a terminal's progress output into what each redraw
// showed.
func redraws(s string) []string { return strings.Split(s, "\r\x1b[K")[1:] }

// TestProgress_Terminal: one line redrawn in place at most every 100ms,
// never showing less done than before, and ending at 100% on a line of
// its own.
func TestProgress_Terminal(t *testing.T) {
	var out bytes.Buffer
	c := newClock()
	p := newProgress(&out, "pulling gemma4:26b", true, c.Now)

	p.Update(1e9, 19e9)
	c.Advance(50 * time.Millisecond)
	p.Update(2e9, 19e9) // too soon: not drawn
	c.Advance(60 * time.Millisecond)
	p.Update(1e9, 19e9) // less done than reported before: still 2 GB
	c.Advance(200 * time.Millisecond)
	p.Update(9.5e9, 19e9)
	p.Done()

	assert.Equal(t, []string{
		"pulling gemma4:26b: 5% (1 GB of 19 GB)",
		"pulling gemma4:26b: 10% (2 GB of 19 GB)",
		"pulling gemma4:26b: 50% (9.5 GB of 19 GB)",
		"pulling gemma4:26b: 100% (19 GB of 19 GB)\n",
	}, redraws(out.String()))
}

// TestProgress_Lines: without a terminal, a line at each 10% reached,
// and 100% at the end, once.
func TestProgress_Lines(t *testing.T) {
	var out bytes.Buffer
	p := newProgress(&out, "pulling qwen3-embedding:0.6b", false, newClock().Now)
	for _, done := range []int64{50, 99, 100, 150, 340, 300, 1000} {
		p.Update(done, 1000)
	}
	p.Done()
	p.Done()
	assert.Equal(t, []string{
		"pulling qwen3-embedding:0.6b: 10% (100 B of 1 kB)",
		"pulling qwen3-embedding:0.6b: 34% (340 B of 1 kB)",
		"pulling qwen3-embedding:0.6b: 100% (1 kB of 1 kB)",
	}, lines(out.String()))
}

// TestProgress_Stop: a report cut short ends its line where it stands.
func TestProgress_Stop(t *testing.T) {
	var out bytes.Buffer
	p := newProgress(&out, "pulling gemma4:26b", true, newClock().Now)
	p.Update(1e9, 19e9)
	p.Stop()
	p.Update(5e9, 19e9)
	p.Done()
	assert.Equal(t, "\r\x1b[Kpulling gemma4:26b: 5% (1 GB of 19 GB)\n", out.String())
}

func TestFormatSize(t *testing.T) {
	cases := map[uint64]string{
		0: "0 B", 999: "999 B", 1000: "1 kB", 639e6: "639 MB", 2.5e9: "2.5 GB",
		7.6e9: "7.6 GB", 16e9: "16 GB", 19e9: "19 GB", 285e9: "285 GB",
	}
	for n, want := range cases {
		assert.Equal(t, want, FormatSize(n), "%d", n)
	}
}
