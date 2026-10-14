package indexer

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
)

// update records a golden file that doesn't exist yet. It never
// overwrites one: a golden pins what every home's stored chunks were made
// by, so a change to it is a deliberate new file, never a refresh.
var update = flag.Bool("update", false, "record golden files that don't exist yet")

// corpusDocument is one markdown document of the synthetic corpus the
// chunker's goldens are recorded over. A generated one records its chunks
// by their SHA-256 alone.
type corpusDocument struct {
	name      string
	markdown  string
	generated bool
}

// optionSet is a name and the options a corpus document is chunked with.
type optionSet struct {
	name string
	opts ChunkOptions
}

// The option sets: the daemon's, the same without overlap, a small size
// and cap that cut a short document at every rule, and a medium one for
// the generated documents, which the small one would cut into thousands
// of chunks.
var (
	defaultOptions = optionSet{"default", ChunkOptions{SizeTokens: 384, OverlapTokens: 48}}
	noOverlap      = optionSet{"no overlap", ChunkOptions{SizeTokens: 384, OverlapTokens: 0}}
	smallOptions   = optionSet{"small", ChunkOptions{SizeTokens: 12, OverlapTokens: 3, SizeChars: 120}}
	mediumOptions  = optionSet{"medium", ChunkOptions{SizeTokens: 64, OverlapTokens: 8, SizeChars: 600}}
)

// optionSets are the sets doc is chunked with.
func (doc corpusDocument) optionSets() []optionSet {
	if doc.generated {
		return []optionSet{defaultOptions, noOverlap, mediumOptions}
	}
	return []optionSet{defaultOptions, noOverlap, smallOptions}
}

// corpus is the synthetic library the chunker's goldens cover: every
// heading rule and every boundary rule, by hand, then seeded generated
// documents that mix them at random.
func corpus() []corpusDocument {
	handWritten := []corpusDocument{
		{name: "empty", markdown: ""},
		{name: "blank", markdown: " \n\t\n  \n"},
		{name: "atx and setext", markdown: "# Queues\n\nWhy queues fail, and what backpressure does about it.\n\n" +
			"## Consumers ##\n### Groups\n\nConsumer groups scale reads across brokers.\n\n" +
			"Retention\n=========\n\nLogs keep messages for a while.\n\n" +
			"Compaction\n----------\n\nKeys keep their latest value.\n\n" +
			"#### Level four\n##### Level five\n###### Level six\n####### Seven is text\n\nThe deepest section.\n"},
		{name: "consecutive and trailing headings", markdown: "Intro words before any heading.\n\n" +
			"# Part one\n## Chapter one\n### Section one\n\nBody of section one.\n\n" +
			"## Chapter two\n\nBody of chapter two.\n\n# Appendix\n## Notes\n"},
		{name: "heading under text", markdown: "A paragraph that runs straight\n# Into a heading\n" +
			"with more text under it\nand a line after.\n\n## Below\nno blank line between\n"},
		{name: "fences and code", markdown: "# Build\n\nRun these.\n\n```sh\n# install the tools\nmake tools\n```\n\n" +
			"~~~python\n## not a heading\nprint(\"hi\")\n~~~\n\nAfter the fences.\n\n" +
			"    # indented code, not a heading\n    echo done\n\n" +
			"````\n```\n# still fenced\n````\n\n## Last\n\n```\n# an unclosed fence\nruns to the end\n"},
		{name: "hashtags", markdown: "#hashtag lines are not headings\n#include <stdio.h>\n\n# Real heading\n\n" +
			"Text with a #hashtag inside.\n#another\nTitle under a hashtag\n---\n"},
		{name: "lists tables quotes", markdown: "## Lists\n\n- one\n- two\n---\n\n* three\n\n1. first\n2) second\n---\n\n" +
			"> a quote\n---\n\n| a | b |\n|---|---|\n| 1 | 2 |\n---\n\nTwo lines\nof paragraph\n---\n\n" +
			"Single line\n---\nunder a setext heading.\n\n   Indented three\n===\n\n    Indented four\n===\n"},
		{name: "crlf", markdown: "# Title\r\n\r\nLine one\r\nLine two\r\n\r\nSub\r\n---\r\n\r\nBody\r\n"},
		{name: "data uri images", markdown: "# Diagram ![logo](data:image/png;base64,AAAA)\n\n" +
			"See ![chart](data:image/png;base64,iVBORw0KGgo=) here.\n\ndata:image/png;base64,QUJD stray\n"},
		{name: "reduction", markdown: "# [Getting started](https://example.com/start) with `curio` ¶\n\none\n\n" +
			"## ![badge](https://example.com/b.svg) **Install** the _daemon_ #\n\ntwo\n\n" +
			"### <a id=\"x\"></a>Configure <https://example.com> [a ref][r] §\n\nthree\n\n" +
			"#### \\*Escaped\\* \\# chars 🔗\n\nfour\n\n##### | Table-ish |\n\nfive\n\n" +
			"###### Café über  naïve\t日本語\n\nsix\n\n# ### \n\nseven\n\n" +
			"# [![Build](https://example.com/ci.svg)](https://example.com/ci)\n\neight\n\n" +
			"# " + strings.Repeat("Long heading words ", 20) + "\n\nnine\n\n" +
			"# " + strings.Repeat("日本語", 40) + "\n\nten\n"},
		{name: "cjk", markdown: "# 机器学习\n\n" + strings.Repeat("机器学习", 1000) + "\n\n## 尾\n\n结束。\n"},
		{name: "long paragraph", markdown: "# Long\n## Paragraph\n\n" + words("w", 500) + "\n\n## After\n\nshort tail\n"},
		{name: "oversized word", markdown: "# Blob\n\npreamble words " + strings.Repeat("z", 8000) + " trailing words\n\n" +
			"## Next\n\n" + strings.Repeat("y", 300) + " end\n"},
		{name: "cap crosses sections", markdown: "# First\n\n" + urls("first", 40) + "\n\n# Second\n\n" +
			urls("second", 40) + "\n"},
		{name: "overlap crosses a heading", markdown: "# A\n\n" + words("a", 380) + "\n\n# B\n\n" + words("b", 300) +
			"\n\n## C\n\n" + words("c", 200) + "\n"},
	}
	docs := make([]corpusDocument, 0, len(handWritten)+8)
	docs = append(docs, handWritten...)
	for i := range 8 {
		docs = append(docs, corpusDocument{name: fmt.Sprintf("generated %d", i), markdown: generated(uint64(i)),
			generated: true})
	}
	return docs
}

// words is n distinct words: prefix and their index.
func words(prefix string, n int) string {
	w := make([]string, n)
	for i := range w {
		w[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return strings.Join(w, " ")
}

// urls is n distinct URLs of about 60 bytes, dense enough that a chunk of
// them passes the byte cap long before the word count.
func urls(path string, n int) string {
	u := make([]string, n)
	for i := range u {
		u[i] = fmt.Sprintf("https://example.com/%s/a-long-path-segment/%04d?ref=corpus", path, i)
	}
	return strings.Join(u, " ")
}

// vocabulary is what generated paragraphs are written in.
var vocabulary = strings.Fields("queue broker consumer producer offset retention compaction partition replica " +
	"leader follower log segment index cache eviction latency throughput backpressure retry timeout #tag " +
	"C# **bold** _under_ `code` [link](https://example.com/x) ![img](https://example.com/i.png) \\# ¶ 🔗 日本語 café")

// generated is a seeded document of random blocks: ATX and setext
// headings, paragraphs short and long, fences holding '#' lines, lists,
// tables, quotes, CJK runs, data URIs, words past the byte cap, and CRLF
// line ends.
func generated(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x6375726963686e6b))
	pick := func(list []string) string { return list[r.IntN(len(list))] }
	paragraph := func(n int) string {
		w := make([]string, n)
		for i := range w {
			w[i] = pick(vocabulary)
		}
		return strings.Join(w, " ")
	}
	var b strings.Builder
	for range 16 + r.IntN(24) {
		switch r.IntN(14) {
		case 0, 1:
			fmt.Fprintf(&b, "%s %s\n", strings.Repeat("#", 1+r.IntN(7)), paragraph(1+r.IntN(5)))
		case 2:
			fmt.Fprintf(&b, "%s\n%s\n", paragraph(1+r.IntN(4)), strings.Repeat(pick([]string{"=", "-"}), 1+r.IntN(8)))
		case 3:
			fmt.Fprintf(&b, "%s\n# fenced %s\n%s\n", pick([]string{"```", "~~~", "````"}), paragraph(2),
				pick([]string{"```", "~~~", "````", "not a close"}))
		case 4:
			fmt.Fprintf(&b, "- %s\n- %s\n", paragraph(3), paragraph(4))
		case 5:
			fmt.Fprintf(&b, "| %s | %s |\n|---|---|\n", paragraph(1), paragraph(1))
		case 6:
			fmt.Fprintf(&b, "> %s\n", paragraph(6))
		case 7:
			b.WriteString(strings.Repeat("机器学习", 50+r.IntN(400)) + "\n")
		case 8:
			fmt.Fprintf(&b, "![x](data:image/png;base64,%s) %s\n", strings.Repeat("QUFB", r.IntN(500)), paragraph(3))
		case 9:
			fmt.Fprintf(&b, "%s %s %s\n", paragraph(2), strings.Repeat("x", 3000+r.IntN(5000)), paragraph(2))
		case 10:
			b.WriteString(paragraph(300+r.IntN(300)) + "\n")
		case 11:
			b.WriteString(urls(fmt.Sprintf("g%d", seed), 10+r.IntN(80)) + "\n")
		default:
			b.WriteString(paragraph(5+r.IntN(120)) + "\n")
		}
		b.WriteString(pick([]string{"\n", "\n", "", "\r\n"}))
	}
	return b.String()
}
