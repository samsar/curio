package ui

import (
	"bytes"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"

	"github.com/samsar/curio/internal/ui/uitest"
)

// renderLimit and renderAlloc are generous bounds on rendering
// MaxRenderedMarkdown bytes of anything, race detector included: what the
// budgets stop took from seconds to minutes at that size, or allocated
// gigabytes. shownAlloc bounds showing a text over a budget, which the
// budgets refuse before goldmark parses it. linkAlloc bounds a text over
// the link budget, which costs what goldmark's parse of its links does.
const (
	renderLimit = 10 * time.Second
	renderAlloc = 1 << 30
	shownAlloc  = 16 << 20
	linkAlloc   = 512 << 20
)

// newBudgetRenderer is a Renderer whose time limit is out of the way, so a
// test of the budgets tests them alone: under the race detector, the
// costliest texts within them take longer than maxFormatTime.
func newBudgetRenderer(t testing.TB) *Renderer {
	t.Helper()
	r := newRenderer(t)
	r.markdown.timeLimit = time.Minute
	return r
}

// allocated is how many bytes fn allocates.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

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
		// Lists a blank line keeps open, each visited again on every one.
		{"an item over blank lines", fill("- a\n", "\n", "b\n"), errBlockVisits},
		{"a nested item over blank lines", fill(strings.Repeat("- ", 32)+"a\n", "\n", "b\n"), errBlockVisits},
		{"a ramp over blank lines", append(markerRamp(16<<10), strings.Repeat("\n", 32000)+"b\n"...), errBlockVisits},
		// Lists nested a little deeper on every line, each line indented
		// past them all, and read again by every one.
		{"a ramp", markerRamp(MaxRenderedMarkdown), errVisitBytes},
		{"deep lists, spaces", deepLists(false), errVisitBytes},
		{"deep lists, tabs", deepLists(true), errVisitBytes},
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
		// Emphasis runs that pair with none, within the markup budget.
		{"a**b then c*", []byte("a**b" + strings.Repeat("c* ", 20000) + "\n"), errEmphasis},
		// A wide header padding every short line after it.
		{"wide table", fill(wideTable, "x\n", ""), errTableCells},
		// A long reference definition repeated by every link to it.
		{"repeated reference", fill(longRef, "[x]\n\n", ""), errLinkBytes},
	}
	r := newBudgetRenderer(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.LessOrEqual(t, len(tc.src), MaxRenderedMarkdown)
			// The links are counted as goldmark parses them, so that text
			// costs what the parse does.
			limit := uint64(shownAlloc)
			if tc.want == errLinkBytes {
				limit = linkAlloc
			}
			for _, images := range []bool{false, true} {
				var text Text
				var err error
				start := time.Now()
				alloc := allocated(func() { text, err = r.RenderMarkdown(tc.src, docURL, images) })
				require.NoError(t, err)
				assert.Less(t, time.Since(start), renderLimit)
				assert.Less(t, alloc, limit)
				assertStored(t, text, tc.src, tc.want)
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
		"nested lists":       {nestedLists(), []string{"<ul>\n<li>level 9"}},
		"loose nested lists": {looseNestedLists(), []string{"<ul>\n<li>\n<p>level 3", "<li>level 4"}},
		"a loose list":       {fill("", "- an item\n\n", ""), []string{"<li>\n<p>an item</p>\n</li>"}},
		"a long table":       {longTable(), []string{"<td>row 999</td>"}},
		"long lines":         {fill("", strings.Repeat("word ", 199)+"end\n", ""), []string{"<p>word word"}},
		"a spaced thematic break": {[]byte("a\n\n" + strings.Repeat("- ", 64) + "\n\nb\n"),
			[]string{"<p>a</p>\n<hr>\n<p>b</p>"}},
	}
	r := newBudgetRenderer(t)
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

// looseNestedLists is lists nested five deep with a blank line after every
// item, as a README's outline can be, over and over.
func looseNestedLists() []byte {
	var b bytes.Buffer
	for b.Len() < MaxRenderedMarkdown-1024 {
		for level := range 5 {
			fmt.Fprintf(&b, "%s- level %d, with a [link](https://example.com/%d)\n\n", strings.Repeat("  ", level),
				level, level)
		}
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

// linesUpTo is line(0), line(1) and so on, as many as fit in size bytes.
func linesUpTo(size int, line func(i int) string) []byte {
	var b bytes.Buffer
	for i := 0; ; i++ {
		l := line(i)
		if b.Len()+len(l) > size {
			return b.Bytes()
		}
		b.WriteString(l)
	}
}

// markerRamp is lists 32 deeper on every line, each line indented past
// all of them with tabs: size bytes at most.
func markerRamp(size int) []byte {
	return linesUpTo(size, func(k int) string {
		return strings.Repeat("\t", 16*k) + strings.Repeat("- ", 32) + "a\n"
	})
}

// listIndent is 2·i columns, of spaces or of tabs: the indentation of the
// ith item of cmark's deeply nested lists.
func listIndent(i int, tabs bool) string {
	if tabs {
		return strings.Repeat("\t", i/2) + strings.Repeat(" ", 2*(i%2))
	}
	return strings.Repeat(" ", 2*i)
}

// deepLists is cmark's deeply nested lists, MaxRenderedMarkdown bytes at
// most: each item indented two columns past the one before.
func deepLists(tabs bool) []byte {
	return linesUpTo(MaxRenderedMarkdown, func(i int) string { return listIndent(i, tabs) + "- a\n" })
}

// textUnderLists is levels of deepLists indented with tabs, then lines of
// text in the deepest item: MaxRenderedMarkdown bytes at most.
func textUnderLists(levels int, text string) []byte {
	return linesUpTo(MaxRenderedMarkdown, func(i int) string {
		if i < levels {
			return listIndent(i, true) + "- a\n"
		}
		return listIndent(levels, true) + text + "\n"
	})
}

// underBudget is the most of src's first lines within the formatting
// budgets.
func underBudget(src []byte) []byte {
	var s shape
	n := 0
	for line := range bytes.Lines(src) {
		if s.add(line) != nil {
			break
		}
		if end := s; end.endParagraph() != nil {
			break
		}
		n += len(line)
	}
	return src[:n]
}

// TestMarkdown_JustUnderBudget: the costliest shapes found, cut to the most
// of them within the block and emphasis budgets, are formatted within the
// render bounds.
func TestMarkdown_JustUnderBudget(t *testing.T) {
	cases := map[string][]byte{
		"an item over blank lines":       fill("- a\n", "\n", "b\n"),
		"a nested item over blank lines": fill(strings.Repeat("- ", 32)+"a\n", "\n", "b\n"),
		"deep lists, tabs":               deepLists(true),
		"text under deep lists":          textUnderLists(128, strings.Repeat("x", 64)),
		"a**b then c*":                   fill("a**b\n", "c*\n", ""),
	}
	r := newBudgetRenderer(t)
	for name, full := range cases {
		t.Run(name, func(t *testing.T) {
			src := underBudget(full)
			require.Less(t, len(src), len(full), "the whole shape is within the budgets")
			require.NoError(t, checkShape(src))
			var text Text
			var err error
			start := time.Now()
			alloc := allocated(func() { text, err = r.RenderMarkdown(src, docURL, false) })
			require.NoError(t, err)
			assert.Empty(t, text.Unformatted)
			assert.Less(t, time.Since(start), renderLimit)
			assert.Less(t, alloc, uint64(renderAlloc))
		})
	}
}

// TestMarkdown_CostsCheckShapeDoesntSee: texts within checkShape's budgets
// whose cost lies in what it doesn't read, each held by what bounds it to
// its outcome and to a time and an allocation bound per render, race
// detector included, with the time limit out of the way.
func TestMarkdown_CostsCheckShapeDoesntSee(t *testing.T) {
	const kib16, kib128 = 16 << 10, 128 << 10
	noLinks := func(t *testing.T, text Text, _ bool) {
		assert.Contains(t, string(text.HTML), "<p>")
		assert.NotContains(t, string(text.HTML), "<a")
	}
	cases := []struct {
		name string
		src  []byte
		over *budgetError // the budget it is over; nil when it is formatted
		// shows checks a formatted text, with images or not.
		shows func(t *testing.T, text Text, images bool)
		took  time.Duration
		alloc uint64
	}{
		// Links a page can't keep, each charged before it is resolved:
		// resolving one copies its reference's destination. These take
		// 1-2 s under -race, nearly all of it goldmark's own parse, so the
		// time bound is the generous one: the allocation bound (the old
		// code allocated 3.9-11.2 GiB) is what catches a regression.
		{"ftp references", references("ftp://a.example/"+strings.Repeat("a", kib16), "[x]"),
			errLinkBytes, nil, renderLimit, linkAlloc},
		{"ftp references, a long destination", references("ftp://a.example/"+strings.Repeat("a", kib128), "[x]"),
			errLinkBytes, nil, renderLimit, linkAlloc},
		{"javascript references", references("javascript:"+strings.Repeat("a", kib16), "[x]"),
			errLinkBytes, nil, renderLimit, linkAlloc},
		{"javascript image references", references("javascript:"+strings.Repeat("a", kib16), "![x]"),
			errLinkBytes, nil, renderLimit, linkAlloc},
		{"ftp image references", references("ftp://a.example/"+strings.Repeat("a", kib16), "![x]"),
			errLinkBytes, nil, renderLimit, linkAlloc},
		// Addresses written out without link markup, which Linkify's
		// regexps would scan the line for again from every trigger: an
		// email address dropped for the _ after it, and URLs closed at
		// every ).
		{"an email address after runs of ~", fill("x"+strings.Repeat("~~~a", 680)+"@",
			strings.Repeat("a", 62)+".", "a_\n"), nil, noLinks, 2 * time.Second, 64 << 20},
		{"URLs in parentheses", []byte(strings.Repeat("(http://a.b/"+strings.Repeat(")", 4000), 256) + "\n"),
			nil, noLinks, 2 * time.Second, 64 << 20},
		// Links within the link budget whose URLs escaping makes five
		// times longer: the HTML cap.
		{"escaped references", escapedReferences(), errHTMLBytes, nil, 2 * time.Second, 256 << 20},
		// Images inside images: only the outer one is labelled, with all
		// the text inside it.
		{"nested images", nestedImages(4300), nil, showsOuterImage, 2 * time.Second, 64 << 20},
	}
	r := newBudgetRenderer(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.LessOrEqual(t, len(tc.src), MaxRenderedMarkdown)
			require.NoError(t, checkShape(tc.src), "within the shape budgets")
			for _, images := range []bool{false, true} {
				var text Text
				var err error
				start := time.Now()
				alloc := allocated(func() { text, err = r.RenderMarkdown(tc.src, docURL, images) })
				require.NoError(t, err)
				assert.Less(t, time.Since(start), tc.took, "images %v", images)
				assert.Less(t, alloc, tc.alloc, "images %v", images)
				if tc.over != nil {
					assertStored(t, text, tc.src, tc.over)
					continue
				}
				require.Empty(t, text.Unformatted, "images %v", images)
				uitest.AssertInert(t, string(text.HTML))
				tc.shows(t, text, images)
			}
		})
	}
}

// references is a reference definition to dest, then ref, a use of it,
// in paragraphs of their own: MaxRenderedMarkdown bytes.
func references(dest, ref string) []byte {
	return fill("[x]: "+dest+"\n\n", ref+"\n\n", "")
}

// escapedReferences is a reference definition whose destination is
// 16 KiB of &, which goldmark writes as &amp;, used by 500 links: within
// the link budget, and 41 MB of HTML.
func escapedReferences() []byte {
	return []byte("[x]: https://a.example/?" + strings.Repeat("&", 16<<10) + "\n\n" + strings.Repeat("[x]\n\n", 500))
}

// nestedImages is n images, each inside the one before.
func nestedImages(n int) []byte {
	return []byte(strings.Repeat("![", n) + "a" + strings.Repeat("](https://i.example/x.png)", n) + "\n")
}

// showsOuterImage checks nestedImages(n) for n over 999, goldmark's limit
// on open brackets: the innermost images are text inside the rest, and
// the outer image is its alt text, all of that text. Only the outer image
// is counted.
func showsOuterImage(t *testing.T, text Text, images bool) {
	t.Helper()
	html := string(text.HTML)
	assert.Equal(t, 1, text.RemoteImages)
	if images {
		// The sanitizer drops an alt with a : in it, as it drops any.
		assert.Equal(t, `<p><img src="https://i.example/x.png" loading="lazy"></p>`+"\n", html)
		return
	}
	assert.True(t, strings.HasPrefix(html, `<p><a href="https://i.example/x.png" rel="nofollow noreferrer noopener" `+
		`target="_blank">[image: ![![`), "the outer image's label")
	assert.Contains(t, html, "![a](https://i.example/x.png)](")
	assert.True(t, strings.HasSuffix(html, "](https://i.example/x.png)]</a></p>\n"), "the outer image's label")
}

// assertStored checks that text is src shown as stored, over the budget
// or limit over.
func assertStored(t *testing.T, text Text, src []byte, over *budgetError) {
	t.Helper()
	assert.Equal(t, over.reason, text.Unformatted)
	assert.Equal(t, string(src), text.Source)
	assert.Empty(t, text.HTML)
	assert.Zero(t, text.RemoteImages)
}

// mostOnOneLine is unit repeated on one line, as many times as the budgets
// allow.
func mostOnOneLine(unit string) []byte {
	line := func(n int) []byte { return []byte(strings.Repeat(unit, n) + "\n") }
	return line(sort.Search(MaxRenderedMarkdown/len(unit), func(n int) bool { return checkShape(line(n+1)) != nil }))
}

// TestMarkdown_TimeLimit: a text within the budgets that takes longer to
// format than the time limit is stopped there, in the block parse or the
// inline one, and shown as stored.
func TestMarkdown_TimeLimit(t *testing.T) {
	cases := map[string][]byte{
		"deep lists, tabs": underBudget(deepLists(true)),
		"[a](":             mostOnOneLine("[a]("),
	}
	r := newRenderer(t)
	r.markdown.timeLimit = 10 * time.Millisecond
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, checkShape(src))
			for _, images := range []bool{false, true} {
				start := time.Now()
				text, err := r.RenderMarkdown(src, docURL, images)
				require.NoError(t, err)
				assert.Less(t, time.Since(start), 250*time.Millisecond, "images %v", images)
				assertStored(t, text, src, errFormatTime)
			}
		})
	}
}

// TestMarkdown_AllocLimit: a text within the budgets that allocates more
// than the allocation limit is stopped once it has, and shown as stored.
func TestMarkdown_AllocLimit(t *testing.T) {
	src := underBudget(textUnderLists(128, strings.Repeat("x", 64)))
	require.NoError(t, checkShape(src))
	r := newBudgetRenderer(t)
	r.markdown.allocLimit = 32 << 20
	var text Text
	var err error
	alloc := allocated(func() { text, err = r.RenderMarkdown(src, docURL, false) })
	require.NoError(t, err)
	assertStored(t, text, src, errFormatAlloc)
	assert.Less(t, alloc, uint64(64<<20))
}

// TestMarkdown_FormatsAfterAStop: a render stopped part way leaves nothing
// behind: the same Renderer then formats a text exactly as a fresh one
// does.
func TestMarkdown_FormatsAfterAStop(t *testing.T) {
	stops := []struct {
		name      string
		src       []byte
		timeLimit time.Duration
		over      *budgetError
	}{
		{"by the time limit", mostOnOneLine("[a]("), 10 * time.Millisecond, errFormatTime},
		{"by the HTML cap", escapedReferences(), time.Minute, errHTMLBytes},
	}
	fresh, r := newBudgetRenderer(t), newBudgetRenderer(t)
	src := article()
	for _, stop := range stops {
		t.Run(stop.name, func(t *testing.T) {
			r.markdown.timeLimit = stop.timeLimit
			stopped, err := r.RenderMarkdown(stop.src, docURL, false)
			require.NoError(t, err)
			require.Equal(t, stop.over.reason, stopped.Unformatted)
			r.markdown.timeLimit = time.Minute
			for _, images := range []bool{false, true} {
				want, err := fresh.RenderMarkdown(src, docURL, images)
				require.NoError(t, err)
				require.NotEmpty(t, want.HTML)
				got, err := r.RenderMarkdown(src, docURL, images)
				require.NoError(t, err)
				assert.True(t, got == want, "the same text, images %v", images)
			}
		})
	}
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
		// A thematic break is one block, not markers, even after some.
		{"- - -\n", 0, "- - -\n"},
		{"* * * *\n", 0, "* * * *\n"},
		{"> - - -\n", 1, "- - -\n"},
		{"* - - -\n", 1, "- - -\n"},
		{"- - -x\n", 2, "-x\n"},
		{"+ + +\n", 3, "\n"},
		// Counting stops past maxLineNesting.
		{strings.Repeat(">", 40), maxLineNesting + 1, strings.Repeat(">", 40-maxLineNesting-1)},
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
	for _, line := range []string{"*a* b\n", "[c]\n", "\n", "- *d*\n", "# e_\n", "\n", "***a___b~ ~\n"} {
		require.NoError(t, s.add([]byte(line)))
	}
	require.NoError(t, s.endParagraph())
	assert.Equal(t, int64(4*10+2*6+1*5+8*12), s.work)
	// Runs of *, _ and ~, squared: a run of one character is one run.
	assert.Equal(t, int64(2*2+2*2+1*1+4*4), s.emphasis)
}

// TestCheckShape_Blocks: each line is charged the open blocks goldmark can
// visit at it, and counts the blocks it can leave open.
func TestCheckShape_Blocks(t *testing.T) {
	cases := []struct {
		name   string
		lines  []string
		visits int64
		open   int
	}{
		{"a marker opens two blocks, a line one more", []string{"- - a\n"}, 0, 5},
		{"a blank line visits every block and keeps them open", []string{"- - a\n", "\n", "\n"}, 10, 5},
		{"a lazy line keeps them open", []string{"- - a\n", "b\n", "\n"}, 1 + 5, 5},
		{"a line after a blank one closes what it doesn't continue", []string{"- - a\n", "\n", "b\n"}, 5 + 1, 1},
		{"a column of indentation continues a block", []string{"- - a\n", "\n", "   b\n"}, 5 + 4, 4},
		{"a tab is four columns", []string{"- - a\n", "\n", "\tb\n"}, 5 + 5, 5},
		{"a marker continues a block", []string{"> > a\n", "\n", "> b\n"}, 5 + 3, 5},
		{"a line of markers can visit every block", []string{"- - a\n", "\n", ">\n"}, 5 + 5, 5},
		{"a thematic break opens one block", []string{"- - - -\n"}, 0, 1},
		{"each visit reads the line", []string{"- - a\n", "\n", "\t" + strings.Repeat("x", 100) + "\n"}, 5 + 5, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s shape
			var visitBytes int64
			for _, line := range tc.lines {
				before := s.visits
				require.NoError(t, s.add([]byte(line)))
				visitBytes += (s.visits - before) * int64(len(line))
			}
			assert.Equal(t, tc.visits, s.visits)
			assert.Equal(t, visitBytes, s.visitBytes)
			assert.Equal(t, tc.open, s.open)
		})
	}
}

// TestMarkup_CoversInlineTriggers: every character newMarkdown's inline
// parsers start at is charged as markup, and the stop parser starts at
// each of them.
func TestMarkup_CoversInlineTriggers(t *testing.T) {
	// goldmark's, and those of the extensions newMarkdown uses (a table
	// is parsed inline cell by cell, with these).
	parsers := make([]parser.InlineParser, 0, len(parser.DefaultInlineParsers())+2)
	for _, p := range parser.DefaultInlineParsers() {
		parsers = append(parsers, p.Value.(parser.InlineParser))
	}
	parsers = append(parsers, extension.NewStrikethroughParser(), extension.NewTaskCheckBoxParser())
	var triggers []byte
	for _, p := range parsers {
		for _, c := range p.Trigger() {
			assert.True(t, markup[c], "%T starts at %q", p, c)
			if !slices.Contains(triggers, c) {
				triggers = append(triggers, c)
			}
		}
	}
	assert.ElementsMatch(t, triggers, stopParser{}.Trigger())
}

// TestMarkdown_StoppedPageIsInert: the page of a document whose render the
// time limit stopped shows its text escaped.
func TestMarkdown_StoppedPageIsInert(t *testing.T) {
	r := newRenderer(t)
	r.markdown.timeLimit = 10 * time.Millisecond
	hostile := "<script>alert(1)</script> <img src=x onerror=alert(1)>\n\n"
	src := underBudget(append([]byte(hostile), deepLists(true)...))
	text, err := r.RenderMarkdown(src, docURL, false)
	require.NoError(t, err)
	require.Equal(t, errFormatTime.reason, text.Unformatted)
	page := samples(t, newRenderer(t))[PageDocument].(Document)
	page.Text = TextPanel{State: TextShown, Text: text}
	out := render(t, r, PageDocument, page)
	uitest.AssertInert(t, out)
	assert.Contains(t, out, "Shown as stored, unformatted: "+errFormatTime.reason+" to format quickly.")
	assert.Contains(t, out, `<pre class="source">&lt;script&gt;alert(1)&lt;/script&gt; &lt;img src=x onerror=alert(1)&gt;`+
		"\n\n- a\n")
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
