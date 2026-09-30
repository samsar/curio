package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shown is segs as one string, each marked run between ⟦ and ⟧.
func shown(segs []Segment) string {
	var b strings.Builder
	for _, s := range segs {
		if s.Mark {
			b.WriteString("⟦" + s.Text + "⟧")
		} else {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

// TestPassage_Snippets: a snippet's markup is dropped and the text it wraps
// kept, with the matched words marked, adjacent marks merged, and FTS5's
// ellipses kept at the ends. The first cases are real snippets from the
// author's library.
func TestPassage_Snippets(t *testing.T) {
	cases := []struct {
		name, snippet, want string
	}{
		{"a link's destination, cut both ends of a quote",
			`…comes from [Forrest Brazeal's excellent walkthrough on <em>single</em>-<em>table</em> <em>design</em>](https://www.trek10.com/blog/<em>dynamodb</em>-<em>single</em>-<em>table</em>-relational-modeling/): > \[A] well-optimized <em>single</em>-<em>table</em> <em>DynamoDB</em>…`,
			`…comes from Forrest Brazeal's excellent walkthrough on ⟦single-table design⟧: [A] well-optimized ⟦single-table DynamoDB⟧…`},
		{"headings and bold",
			`# Dynobase - <em>Single</em>-<em>Table</em> <em>Designer</em> (for <em>DynamoDB</em>) **Channel:** Dynobase **Published:** 2021-09-23 ## Description`,
			`Dynobase - ⟦Single-Table Designer⟧ (for ⟦DynamoDB⟧) Channel: Dynobase Published: 2021-09-23 Description`},
		{"a link cut at the start, an image link cut at the end",
			`Studio](https://www.sensedeep.com/) that includes a full <em>DynamoDB</em> suite … metrics. [![Image 6: SenseDeep Developer Studio](https…`,
			`Studio that includes a full ⟦DynamoDB⟧ suite … metrics. Image 6: SenseDeep Developer Studio…`},
		{"starts inside a destination, then a table",
			`…2021/<em>dynamodb</em>-singletable-<em>design</em>.html) | | The What and Why of <em>Single</em> <em>Table</em> <em>Design</em> |`,
			`…The What and Why of ⟦Single Table Design⟧`},

		{"a destination with parentheses", `[a](https://en.wikipedia.org/wiki/Kafka_(novel)) rest`, `a rest`},
		{"a destination of any scheme", `[<em>kafka</em>](javascript:alert(1))`, `⟦kafka⟧`},
		{"a destination with a title", `see [the docs](https://example.com/a "The docs") now`, `see the docs now`},
		{"a reference link", `see [the docs][docs] and [more][] now`, `see the docs and more now`},
		{"an image keeps its alt text", `before ![a diagram](https://example.com/d.png) after`, `before a diagram after`},
		{"an image without alt text goes", `before ![](https://example.com/d.png) after`, `before after`},
		{"an image in a link", `[![<em>kafka</em> logo](https://example.com/k.png)](https://kafka.apache.org) text`,
			`⟦kafka⟧ logo text`},
		{"brackets without a destination are text", `an array [0] of [1, 2]`, `an array [0] of [1, 2]`},
		{"an unclosed bracket goes, its text stays", `a [link text never closed`, `a link text never closed`},
		{"an unclosed image goes", `a ![alt text never closed`, `a alt text never closed`},
		{"a destination cut at the end, no ellipsis", `[text](https://example.com/cut`, `text`},
		{"a ) that ends link text is text", `…(tm)](https://example.com) rest`, `…(tm) rest`},
		{"the leading ellipsis survives a dropped destination", `…example.com/<em>kafka</em>) rest`, `…rest`},

		{"escapes are literal", `\*not emphasis\* and \# not a heading, \[not a link\](x)`,
			`*not emphasis* and # not a heading, [not a link](x)`},
		{"an escaped backslash", `a \\ b`, `a \ b`},
		{"snake_case and arithmetic stay", `snake_case_name and 5*3 and 2**8`, `snake_case_name and 5*3 and 2**8`},
		{"emphasis goes", `*one* _two_ **three** __four__ ***five***`, `one two three four five`},
		{"strikethrough goes, a single tilde stays", `~~gone~~ and ~5 minutes`, `gone and ~5 minutes`},
		{"code spans go", "run `curio up` or ```go build```", `run curio up or go build`},
		{"headings of one to six marks", `# a ## b ###### c ####### d #tag C# e`, `a b c ####### d #tag C# e`},
		{"quotes at the start", `> > nested quote`, `nested quote`},
		{"a quote after punctuation", `said: > quoted`, `said: quoted`},
		{"comparisons and arrows stay", `x > 5 and a -> b`, `x > 5 and a -> b`},
		{"a table", `| Name | Age | |---|---| | Ada | 36 |`, `Name Age Ada 36`},
		{"aligned table rules", `|:--|--:|:-:| a|b`, `a|b`},
		{"an escaped pipe stays", `a \| b`, `a | b`},
		{"known HTML tags go, their content stays", `line<br/>next <a href="https://x">link</a> <IMG src=x> <h2>t</h2>`,
			`linenext link t`},
		{"other angle brackets stay", `List<String> and <script>alert(1)</script> and <abbr>x</abbr>`,
			`List<String> and <script>alert(1)</script> and <abbr>x</abbr>`},
		{"a tag cut at the end stays", `text <a href="https://x`, `text <a href="https://x`},

		{"marked hostile text stays one text segment", `<em><script>alert(1)</script></em>`, `⟦<script>alert(1)</script>⟧`},
		{"adjacent marks merge", `<em>a</em><em>b</em>`, `⟦ab⟧`},
		{"marks across a hyphen", `Vendor <em>Lock</em>-<em>in</em> here`, `Vendor ⟦Lock-in⟧ here`},
		{"marks across dashes and spaces", `<em>a</em> – <em>b</em> ‐ <em>c</em>`, `⟦a – b ‐ c⟧`},
		{"marks across a full stop don't merge", `<em>a</em>. <em>b</em>`, `⟦a⟧. ⟦b⟧`},
		{"marks across a slash don't merge", `<em>a</em>/<em>b</em>`, `⟦a⟧/⟦b⟧`},
		{"marks across a word don't merge", `<em>a</em> of <em>b</em>`, `⟦a⟧ of ⟦b⟧`},
		{"marks across a long gap don't merge", `<em>a</em> -- - <em>b</em>`, `⟦a⟧ -- - ⟦b⟧`},
		{"marks across dropped markup merge", `<em>kafka</em>** **<em>streams</em>`, `⟦kafka streams⟧`},
		{"whitespace collapses", "  a \t b  ", `a b`},
		{"no space after a leading ellipsis or before a trailing one", `… ![](y) text ![](z) …`, `…text…`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shown(Passage(tc.snippet, "the chunk's text")))
		})
	}
}

// TestPassage_Text: without a snippet, or with one the cleaning leaves
// nothing of, a passage is the start of its chunk's text, cleaned, as one
// unmarked segment of at most passageRunes runes.
func TestPassage_Text(t *testing.T) {
	assert.Equal(t, []Segment{{Text: "Kafka partitions: a guide"}},
		Passage("", "# Kafka **partitions**: [a guide](https://example.com)"))
	assert.Equal(t, []Segment{{Text: "the text"}}, Passage("…](https://example.com/a)…", "the text"),
		"a snippet of markup alone")
	assert.Equal(t, []Segment{{Text: "the text"}}, Passage("<em></em>", "the text"))

	long := strings.Repeat("word ", 100)
	got := Passage("", "![](https://example.com/l.png) "+long)
	require.Len(t, got, 1)
	assert.False(t, got[0].Mark)
	assert.Equal(t, Excerpt(long, passageRunes), got[0].Text)
	assert.LessOrEqual(t, len([]rune(got[0].Text)), passageRunes+1)

	assert.Nil(t, Passage("", ""))
	assert.Nil(t, Passage("", "![](https://example.com/l.png)"), "nothing to show")
}

// cleanLimit bounds cleaning a text 100 times a chunk's size, twice, under
// the race detector: a cleaning that went back over what it had read would
// take hours.
const cleanLimit = 2 * time.Second

// TestPassage_Adversarial: texts made of one construct's markup over and
// over, the shapes that would make a scan start again at every rune, clean
// to what they should at a chunk's size, and in time in proportion to their
// length at 100 times it.
func TestPassage_Adversarial(t *testing.T) {
	const chunk = 3500
	cases := []struct {
		name, unit string
		want       func(src string) string
	}{
		{"brackets", "[", func(string) string { return "" }},
		{"link ends", "](", func(string) string { return "" }},
		{"image opens", "![", func(string) string { return "" }},
		{"asterisks", "*", func(string) string { return "" }},
		{"backslashes", `\`, func(src string) string { return strings.Repeat(`\`, len(src)/2) }},
		{"backslashes before letters", `\a`, func(src string) string { return src }},
		{"hashes", "#", func(src string) string { return src }},
		{"hashes and spaces", "# ", func(string) string { return "" }},
		{"quotes", ">", func(src string) string { return src }},
		{"quote runs", "> ", func(string) string { return "" }},
		{"pipes", "|", func(string) string { return "" }},
		{"tags opened", "<a ", strings.TrimSpace},
		{"marks", "<em>", func(string) string { return "" }},
		{"marked words", "<em>a</em> ", func(src string) string {
			return "⟦" + strings.TrimSpace(strings.Repeat("a ", strings.Count(src, "<em>"))) + "⟧"
		}},
		{"a destination left open", "[a](((", func(string) string { return "a" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Repeat(tc.unit, chunk/len(tc.unit))
			if got := shown(Passage(src, "")); got != tc.want(src) {
				t.Errorf("cleaned %d runes of %q to %d runes, want %d:\n%.200q", len(src), tc.unit,
					len([]rune(got)), len([]rune(tc.want(src))), got)
			}

			huge := strings.Repeat(tc.unit, 100*chunk/len(tc.unit))
			start := time.Now()
			Passage(huge, huge)
			assert.Less(t, time.Since(start), cleanLimit)
		})
	}
}
