package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"

	"github.com/samsar/curio/internal/ui/uitest"
)

// renderLimit is a generous bound on rendering MaxRenderedMarkdown bytes of
// anything, race detector included. What the budgets stop took from
// seconds to minutes at that size.
const renderLimit = 10 * time.Second

// fill is head, then unit repeated, then tail: MaxRenderedMarkdown bytes,
// or as close to it as whole units come.
func fill(head, unit, tail string) []byte {
	n := (MaxRenderedMarkdown - len(head) - len(tail)) / len(unit)
	return []byte(head + strings.Repeat(unit, n) + tail)
}

// TestMarkdown_OverBudget: each shape that makes goldmark's work grow
// faster than the text, at the render cap, is over a budget, and its page
// shows it unformatted, fast.
func TestMarkdown_OverBudget(t *testing.T) {
	wideTable := strings.Repeat("|a", 1024) + "|\n" + strings.Repeat("|-", 1024) + "|\n"
	longRef := "[x]: https://a.example/" + strings.Repeat("a", 16<<10) + "\n\n"
	cases := []struct {
		name string
		src  []byte
		want *budgetError
	}{
		// Container markers on one line: goldmark's work is their square.
		{"> on one line", fill("", ">", ""), errDeepNesting},
		{">tab on one line", fill("", ">\t", ""), errDeepNesting},
		{"- then x", fill("", "- ", "x"), errDeepNesting},
		{"+ then x", fill("", "+ ", "x"), errDeepNesting},
		{"> - then x", fill("", "> - ", "x"), errDeepNesting},
		{"1. then x", fill("", "1. ", "x"), errDeepNesting},
		// A container per line, each with its own tags.
		{"16 > a line", fill("", strings.Repeat(">", 16)+"x\n\n", ""), errManyContainers},
		// Markup that scans to the end of its paragraph, or is compared
		// with every other delimiter in it.
		{"[a](", fill("", "[a](", ""), errDenseMarkup},
		{"[a](b", fill("", "[a](b", ""), errDenseMarkup},
		{"![a](", fill("", "![a](", ""), errDenseMarkup},
		{"[a](<", fill("", "[a](< ", ""), errDenseMarkup},
		{"*a", fill("", "*a", ""), errDenseMarkup},
		{"_a", fill("", "_a", ""), errDenseMarkup},
		{"~a", fill("", "~a", ""), errDenseMarkup},
		{"a*", fill("", "a*", ""), errDenseMarkup},
		{"**a", fill("", "**a", ""), errDenseMarkup},
		{"`a", fill("", "`a", ""), errDenseMarkup},
		{"[a] lines", fill("", "[a]\n", ""), errDenseMarkup},
		{"[a]: b lines", fill("", "[a]: b\n", ""), errDenseMarkup},
		// A wide header padding every short line after it.
		{"wide table", fill(wideTable, "x\n", ""), errTableCells},
		// A long reference definition repeated by every link to it.
		{"repeated reference", fill(longRef, "[x]\n\n", ""), errLinkBytes},
	}
	r := newRenderer(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.LessOrEqual(t, len(tc.src), MaxRenderedMarkdown)
			for _, images := range []bool{false, true} {
				start := time.Now()
				text, err := r.RenderMarkdown(tc.src, docURL, images)
				require.NoError(t, err)
				assert.Less(t, time.Since(start), renderLimit)
				assert.Equal(t, tc.want.reason, text.Unformatted)
				assert.Equal(t, string(tc.src), text.Source)
				assert.Empty(t, text.HTML)
				assert.Zero(t, text.RemoteImages)
			}
			_, _, err := r.markdown.format(tc.src, docURL, false)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// TestMarkdown_WithinBudget: long documents as they are stored, articles,
// READMEs and flattened wiki tables, are formatted.
func TestMarkdown_WithinBudget(t *testing.T) {
	cases := map[string]struct {
		src  []byte
		want []string
	}{
		"article": {article(), []string{"<h2>Section 1</h2>", "<em>emphasis</em>", "<strong>strong</strong>",
			"<code>code</code>", `<a href="https://example.com/1"`, `<a href="https://example.com/ref"`,
			"<blockquote>", "<pre><code", "<table>", "<td>1</td>", "[image: figure 1]"}},
		"a list of links": {listOfLinks(), []string{`<li><a href="https://github.com/o/project-1"`,
			"<em>short</em>"}},
		"a paragraph of links": {paragraphOfLinks(), []string{`<a href="https://en.wikipedia.org/wiki/Name_1"`,
			`title="Name 1"`}},
		"nested lists": {nestedLists(), []string{"<ul>\n<li>level 9"}},
		"a long table": {longTable(), []string{"<td>row 999</td>"}},
		"long lines":   {fill("", strings.Repeat("word ", 199)+"end\n", ""), []string{"<p>word word"}},
	}
	r := newRenderer(t)
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, checkShape(tc.src))
			start := time.Now()
			text, err := r.RenderMarkdown(tc.src, docURL, false)
			require.NoError(t, err)
			assert.Less(t, time.Since(start), renderLimit)
			assert.Empty(t, text.Unformatted)
			html := string(text.HTML)
			for _, want := range tc.want {
				assert.Contains(t, html, want)
			}
		})
	}
}

// article is an article of about MaxRenderedMarkdown bytes: sections of
// prose with links, emphasis and code, a list, a quote, code, a table and
// a figure, and reference links to a definition at its end.
func article() []byte {
	var b bytes.Buffer
	for i := 1; b.Len() < MaxRenderedMarkdown-4096; i++ {
		fmt.Fprintf(&b, "## Section %d\n\n", i)
		for range 3 {
			fmt.Fprintf(&b, "A paragraph of prose, with a [link](https://example.com/%d), *emphasis*, "+
				"**strong** words and `code`, and a [reference][ref]. %s\n\n", i, strings.Repeat("More prose. ", 30))
		}
		for j := range 8 {
			fmt.Fprintf(&b, "- an item with a [link](https://example.com/%d/%d) and _emphasis_\n", i, j)
		}
		b.WriteString("\n> A quote, with *emphasis*.\n\n```go\nfunc main() { fmt.Println(\"[a](\") }\n```\n\n")
		b.WriteString("| a | b | c |\n|---|:-:|--:|\n")
		for j := range 5 {
			fmt.Fprintf(&b, "| %d | [x](https://example.com/t/%d) | `y` |\n", j+1, j)
		}
		fmt.Fprintf(&b, "\n![figure %d](https://img.example/%d.png)\n\n", i, i)
	}
	b.WriteString("[ref]: https://example.com/ref \"The reference\"\n")
	return b.Bytes()
}

// listOfLinks is an awesome-list README: one tight list of about
// MaxRenderedMarkdown bytes of links.
func listOfLinks() []byte {
	var b bytes.Buffer
	b.WriteString("# Awesome things\n\n")
	for i := 1; b.Len() < MaxRenderedMarkdown-200; i++ {
		fmt.Fprintf(&b, "- [Project %d](https://github.com/o/project-%d) - A *short* description, `code` "+
			"and ~~old~~ news.\n", i, i)
	}
	return b.Bytes()
}

// paragraphOfLinks is a wiki table flattened into one line of prose,
// links and citations, as Jina returns one: 100 KiB of it.
func paragraphOfLinks() []byte {
	var b bytes.Buffer
	for i := 1; b.Len() < 100<<10; i++ {
		fmt.Fprintf(&b, "[Name %d](https://en.wikipedia.org/wiki/Name_%d \"Name %d\")[\\[%d\\]](#cite_note-%d) "+
			"born 1900, died 1950, canonized 2000. ", i, i, i, i, i)
	}
	b.WriteByte('\n')
	return b.Bytes()
}

// nestedLists is lists nested ten deep, over and over.
func nestedLists() []byte {
	var b bytes.Buffer
	for b.Len() < MaxRenderedMarkdown-4096 {
		for level := range 10 {
			fmt.Fprintf(&b, "%s- level %d with *emphasis*\n", strings.Repeat("  ", level), level)
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// longTable is a table of ten columns and 1,000 rows.
func longTable() []byte {
	var b bytes.Buffer
	b.WriteString("| a | b | c | d | e | f | g | h | i | j |\n" + strings.Repeat("|---", 10) + "|\n")
	for i := range 1000 {
		fmt.Fprintf(&b, "| row %d | [x](https://example.com/%d) | *c* | d | e | f | g | h | i | j |\n", i, i)
	}
	return b.Bytes()
}

func TestContainerMarkers(t *testing.T) {
	cases := []struct {
		line string
		n    int
		rest string
	}{
		{"plain text\n", 0, "plain text\n"},
		{"> quote\n", 1, "quote\n"},
		{">> > quote\n", 3, "quote\n"},
		{"- item\n", 1, "item\n"},
		{"  * + - item\n", 3, "item\n"},
		{"1. one\n", 1, "one\n"},
		{"> 12) - item\n", 3, "item\n"},
		{"-\n", 1, "\n"},
		{"- -\n", 2, "\n"},
		{">\t>\tx", 2, "x"},
		{"*emphasis*\n", 0, "*emphasis*\n"},
		{"-1 degrees\n", 0, "-1 degrees\n"},
		{"1.5 metres\n", 0, "1.5 metres\n"},
		{"1234567890. ten digits\n", 0, "1234567890. ten digits\n"},
		{"|---|---|\n", 0, "|---|---|\n"},
	}
	for _, tc := range cases {
		n, rest := containerMarkers([]byte(tc.line))
		assert.Equal(t, tc.n, n, "%q", tc.line)
		assert.Equal(t, tc.rest, string(rest), "%q", tc.line)
	}
}

func TestStartsBlock(t *testing.T) {
	starts := []string{"- item\n", "* item", "+\titem", "   - item", "> - item", ">  - item", "> > * item",
		"   >   - item", "# heading", "###### heading\n", "#\n", "> ## heading"}
	for _, line := range starts {
		assert.True(t, startsBlock([]byte(line)), "%q", line)
	}
	// An empty item can't interrupt a paragraph, and four spaces in is a
	// continuation of it: charging them to the paragraph before is safe.
	continues := []string{"text", "-\n", "- \n", "-item", "    - item", "\t- item", ">\t- item", "    > - item",
		"####### seven", "#hashtag", "1. an ordered item", "2. an ordered item", "[a](b)", ""}
	for _, line := range continues {
		assert.False(t, startsBlock([]byte(line)), "%q", line)
	}
}

func TestDelimiterRowCells(t *testing.T) {
	cases := map[string]int{
		"|---|---|\n":         2,
		"--- | :-: | --:":     3,
		"|-|-|-|-|":           4,
		":-\r\n":              1,
		"---":                 1,
		"-||-":                2,
		"| a | b |":           0,
		"|---|-x-|":           0,
		"":                    0,
		"| | |":               0,
		"|---|---|---|---|--": 5,
	}
	for line, want := range cases {
		assert.Equal(t, want, delimiterRowCells([]byte(line)), "%q", line)
	}
}

// TestCheckShape_Tables: a table is charged its header's width for every
// line after its delimiter row, whichever delimiter row goldmark takes.
func TestCheckShape_Tables(t *testing.T) {
	var s shape
	for _, line := range []string{"a | b\n", "|-|\n", "|a|a|a|\n", "|-|-|-|\n", "x\n", "x\n"} {
		require.NoError(t, s.add([]byte(line)))
	}
	// The one-column delimiter row: its header, and itself and the line
	// after it as rows. The three-column one: its header, and itself and
	// the two lines after it as rows.
	assert.Equal(t, 1+2*1+3+3*3, s.cells)
	require.NoError(t, s.add([]byte("\n")))
	require.NoError(t, s.add([]byte("x\n")))
	assert.Equal(t, 15, s.cells, "a blank line ends the table")
}

// TestCheckShape_Paragraphs: a paragraph's markup is charged its length,
// up to a blank line or a line that starts a list item or a heading.
func TestCheckShape_Paragraphs(t *testing.T) {
	var s shape
	for _, line := range []string{"*a* b\n", "[c]\n", "\n", "- *d*\n", "# e_\n"} {
		require.NoError(t, s.add([]byte(line)))
	}
	require.NoError(t, s.endParagraph())
	assert.Equal(t, int64(4*10+2*6+1*5), s.work)
}

// TestMarkup_CoversInlineTriggers: every character newMarkdown's inline
// parsers start at is charged as markup, a space aside.
func TestMarkup_CoversInlineTriggers(t *testing.T) {
	charged := func(p parser.InlineParser) {
		for _, c := range p.Trigger() {
			assert.True(t, c == ' ' || markup[c], "%T starts at %q", p, c)
		}
	}
	for _, p := range parser.DefaultInlineParsers() {
		charged(p.Value.(parser.InlineParser))
	}
	// extension.GFM's.
	for _, p := range []parser.InlineParser{extension.NewLinkifyParser(), extension.NewStrikethroughParser(),
		extension.NewTaskCheckBoxParser()} {
		charged(p)
	}
}

// TestMarkdown_OverBudgetPageIsInert: the page of a document over a
// budget shows its text escaped.
func TestMarkdown_OverBudgetPageIsInert(t *testing.T) {
	r := newRenderer(t)
	src := strings.Repeat(">", maxLineNesting+1) + " <script>alert(1)</script> <img src=x onerror=alert(1)>\n"
	text, err := r.RenderMarkdown([]byte(src), docURL, false)
	require.NoError(t, err)
	page := samples(t, r)[PageDocument].(Document)
	page.Text = TextPanel{State: TextShown, Text: text}
	out := render(t, r, PageDocument, page)
	uitest.AssertInert(t, out)
	assert.Contains(t, out, "Shown as stored, unformatted: "+errDeepNesting.reason+" to format quickly.")
	assert.Contains(t, out, `<pre class="source">`+strings.Repeat("&gt;", maxLineNesting+1)+
		" &lt;script&gt;alert(1)&lt;/script&gt; &lt;img src=x onerror=alert(1)&gt;\n</pre>")
}
