package indexer

import (
	"cmp"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChunkText_Empty(t *testing.T) {
	assert.Empty(t, ChunkText("", ChunkOptions{}))
	assert.Empty(t, ChunkText("    \n\n  \n", ChunkOptions{}))
}

// TestWordCount: a document's word count is the chunker's unit over the
// text it chunks: without overlap, and with no chunk cut by the byte
// cap, the words of its chunks add up to it.
func TestWordCount(t *testing.T) {
	assert.Zero(t, WordCount(""))
	assert.Zero(t, WordCount(" \n\t "))
	assert.Equal(t, 3, WordCount("an ![chart](data:image/png;base64,iVBORw0KGgo=) image"),
		"an inline image counts as one word, its bytes as none")

	md := "# Queues\n\nWhy queues fail, and what backpressure does about it.\n\n" +
		"## Consumers\n### Groups\n\n" + strings.Repeat("Consumer groups scale reads across brokers. ", 60) +
		"\n\nA closing paragraph.\n\n# Trailing heading"
	chunks := ChunkText(md, ChunkOptions{SizeTokens: 50, OverlapTokens: 0})
	require.Greater(t, len(chunks), 3, "several chunks")
	sum := 0
	for _, c := range chunks {
		sum += c.TokenCount
	}
	assert.Equal(t, sum, WordCount(md))
}

func TestChunkText_FitsInOneChunk(t *testing.T) {
	got := ChunkText("hello world goodbye", ChunkOptions{SizeTokens: 100, OverlapTokens: 10})
	assert.Len(t, got, 1)
	assert.Equal(t, "hello world goodbye", got[0].Text)
	assert.Equal(t, 3, got[0].TokenCount)
}

func TestChunkText_RespectsParagraphBoundary(t *testing.T) {
	// Two paragraphs of 5 words each, chunk size 6 → each paragraph
	// stays whole even though both together would fit.
	md := "alpha beta gamma delta epsilon\n\nzeta eta theta iota kappa"
	got := ChunkText(md, ChunkOptions{SizeTokens: 6, OverlapTokens: 0})
	assert.Len(t, got, 2)
	assert.Equal(t, "alpha beta gamma delta epsilon", got[0].Text)
	assert.Equal(t, "zeta eta theta iota kappa", got[1].Text)
}

func TestChunkText_BigParagraphSplitsByWord(t *testing.T) {
	// 20-word paragraph, size 8, overlap 2 → multiple chunks
	words := strings.Fields(strings.Repeat("word ", 20))
	got := ChunkText(strings.Join(words, " "), ChunkOptions{SizeTokens: 8, OverlapTokens: 2})
	assert.GreaterOrEqual(t, len(got), 3)
	for _, c := range got {
		assert.LessOrEqual(t, c.TokenCount, 8)
	}
}

func TestChunkText_OverlapWithinBigParagraph(t *testing.T) {
	// A single big paragraph split into sub-chunks; consecutive sub-chunks
	// share `overlap` words for continuity. (Cross-paragraph overlap is
	// deliberately not implemented — once paragraphs break, each is a
	// new logical unit.)
	words := strings.Fields(strings.Repeat("w ", 30))
	// Use distinct words so we can verify the overlap.
	for i := range words {
		words[i] = "w" + string(rune('a'+(i%26)))
	}
	md := strings.Join(words, " ")
	got := ChunkText(md, ChunkOptions{SizeTokens: 10, OverlapTokens: 3})
	require.GreaterOrEqual(t, len(got), 3)
	// Last 3 words of chunk 0 should be first 3 words of chunk 1.
	c0 := strings.Fields(got[0].Text)
	c1 := strings.Fields(got[1].Text)
	assert.Equal(t, c0[len(c0)-3:], c1[:3])
}

func TestChunkText_HeadingsStayWithNextParagraph(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want []string
	}{
		{
			// The heading would fit at the tail of the first chunk; it must
			// open the chunk holding its section instead.
			name: "heading does not trail the previous chunk",
			md:   "a b c d e f g h\n\n# Title\n\nx y z w",
			want: []string{"a b c d e f g h", "# Title x y z w"},
		},
		{
			name: "each heading opens its own section",
			md:   "# Title\n\nFirst paragraph words here.\n\n## Subhead\n\nSecond paragraph words here.",
			want: []string{"# Title First paragraph words here.", "## Subhead Second paragraph words here."},
		},
		{
			name: "consecutive headings join the next paragraph",
			md:   "a b c d e f g h\n\n# Part\n## Chapter\n\nx y",
			want: []string{"a b c d e f g h", "# Part ## Chapter x y"},
		},
		{
			name: "heading at end of input stands alone",
			md:   "a b c d e f g h i\n\n# Appendix",
			want: []string{"a b c d e f g h i", "# Appendix"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChunkText(tc.md, ChunkOptions{SizeTokens: 10, OverlapTokens: 0})
			assert.Equal(t, tc.want, chunkTexts(got))
		})
	}
}

func TestChunkText_DefaultsApplyOnZeroOpts(t *testing.T) {
	words := strings.Fields(strings.Repeat("w ", 1000))
	got := ChunkText(strings.Join(words, " "), ChunkOptions{})
	require.GreaterOrEqual(t, len(got), 2, "1000 words should need >1 chunk at default size")
	assert.Equal(t, 384, got[0].TokenCount, "default size_tokens is 384")
}

func TestChunkText_OverlapClampedIfLargerThanSize(t *testing.T) {
	// overlap >= size is nonsensical; the impl should clamp rather than
	// loop forever.
	words := strings.Fields(strings.Repeat("w ", 30))
	got := ChunkText(strings.Join(words, " "), ChunkOptions{SizeTokens: 10, OverlapTokens: 100})
	assert.Greater(t, len(got), 1)
}

func TestChunkText_StripsBase64ImageDataURL(t *testing.T) {
	// A 50KB base64 blob would normally survive into one chunk and
	// blow up the embedder. After sanitization, the chunk should be
	// small and contain the placeholder.
	bigBlob := strings.Repeat("A", 50000)
	md := "Some intro text here, plenty of words to form a real paragraph.\n\n" +
		"![diagram](data:image/png;base64," + bigBlob + ")\n\n" +
		"More body after the image."

	chunks := ChunkText(md, ChunkOptions{SizeChars: 3500})
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c.Text), 3500, "chunk exceeded char cap: len=%d", len(c.Text))
		assert.NotContains(t, c.Text, bigBlob[:200], "base64 content leaked into chunk")
	}
	combined := strings.Join(func() []string {
		out := make([]string, len(chunks))
		for i, c := range chunks {
			out[i] = c.Text
		}
		return out
	}(), "\n")
	assert.Contains(t, combined, "diagram", "alt text should be preserved")
	assert.Contains(t, combined, "intro text", "surrounding prose preserved")
}

func TestChunkText_SplitsOversizedSingleWordWithoutLoss(t *testing.T) {
	// A pathological case the sanitizer can't catch — e.g. a hex blob
	// or unbroken identifier. The char-limit backstop must split it into
	// pieces under the cap without dropping any of it.
	monster := strings.Repeat("z", 20000)
	md := "preamble word word word " + monster + " trailing words"

	chunks := ChunkText(md, ChunkOptions{SizeChars: 1000})
	zs := 0
	for _, c := range chunks {
		assert.LessOrEqual(t, len(c.Text), 1000,
			"chunk exceeded char cap: len=%d", len(c.Text))
		zs += strings.Count(c.Text, "z")
	}
	assert.Equal(t, len(monster), zs, "every byte of the oversized word is indexed")
}

func TestChunkText_CJKParagraphWithoutSpaces(t *testing.T) {
	// CJK text has no spaces, so a whole paragraph is one "word". It must be
	// split on rune boundaries: valid UTF-8, under the cap, nothing lost.
	para := strings.Repeat("机器学习", 750) // 3000 runes, 9000 bytes
	chunks := ChunkText(para, ChunkOptions{})

	runes := 0
	for i, c := range chunks {
		assert.True(t, utf8.ValidString(c.Text), "chunk[%d] is not valid UTF-8", i)
		assert.LessOrEqual(t, len(c.Text), 3500, "chunk[%d] exceeded cap", i)
		runes += utf8.RuneCountInString(c.Text)
	}
	assert.Equal(t, utf8.RuneCountInString(para), runes)
}

func TestSplitToken_CapSmallerThanRune(t *testing.T) {
	// Progress is guaranteed even when no whole rune fits the cap.
	assert.Equal(t, []string{"😀", "😀"}, splitToken("😀😀", 2))
	assert.Equal(t, []string{"abc", "€", "€"}, splitToken("abc€€", 4))
}

func TestSanitizeForEmbedding_PreservesAltText(t *testing.T) {
	in := "before ![important caption](data:image/png;base64,AAAA) after"
	out := sanitizeForEmbedding(in)
	assert.Contains(t, out, "important caption")
	assert.NotContains(t, out, "base64")
	assert.NotContains(t, out, "AAAA")
}

func TestSanitizeForEmbedding_NoOpWithoutDataURLs(t *testing.T) {
	in := "Plain markdown with [a link](https://example.com/page) and no images."
	assert.Equal(t, in, sanitizeForEmbedding(in))
}

func TestChunkText_AdjacentLargeWordsStayUnderCap(t *testing.T) {
	// Two near-cap "words" (e.g. tracking pixel URLs ~2KB each) packed
	// in the same paragraph. Both fit under the per-word truncation
	// threshold but together exceed maxChars; the overlap seed used to
	// keep the first word in the buffer when the second arrived,
	// producing a single 4KB+ chunk that overflowed the embedder.
	w1 := strings.Repeat("a", 2100)
	w2 := strings.Repeat("b", 2100)
	md := "intro " + w1 + " " + w2 + " outro"

	chunks := ChunkText(md, ChunkOptions{SizeChars: 3500})
	for i, c := range chunks {
		assert.LessOrEqual(t, len(c.Text), 3500,
			"chunk[%d] exceeded cap: len=%d", i, len(c.Text))
	}
}

// TestChunkText_BoundariesGolden: what the chunker makes of the corpus,
// under each option set, is chunk for chunk what testdata/chunks.golden
// recorded: the same texts and word counts. Every home's stored chunks
// were cut by these rules, and a re-index matches a document's new chunks
// to its stored ones by position, so a change here rewrites the chunks of
// every document it touches.
func TestChunkText_BoundariesGolden(t *testing.T) {
	golden(t, filepath.Join("testdata", "chunks.golden"), renderBoundaries(),
		"the chunk boundaries changed: every re-index would cut the documents it touches anew")
}

// renderBoundaries writes every corpus document's chunks under each
// option set: a chunk's word count, then its text, or its length and
// SHA-256 for a generated document's chunk or a long one.
func renderBoundaries() string {
	var b strings.Builder
	b.WriteString("chunker boundaries\n")
	for _, set := range []optionSet{defaultOptions, noOverlap, smallOptions, mediumOptions} {
		fmt.Fprintf(&b, "options %q size %d overlap %d chars %d\n", set.name, set.opts.SizeTokens,
			set.opts.OverlapTokens, set.opts.SizeChars)
	}
	for _, doc := range corpus() {
		for _, set := range doc.optionSets() {
			fmt.Fprintf(&b, "\ndocument %q options %q\n", doc.name, set.name)
			for i, c := range ChunkText(doc.markdown, set.opts) {
				fmt.Fprintf(&b, "  %d words %d %s\n", i, c.TokenCount, goldenText(c.Text, doc.generated))
			}
		}
	}
	return b.String()
}

// goldenText is how a golden records a chunk's text: quoted, unless it is
// a generated document's or longer than a line holds, when its length and
// SHA-256 pin it.
func goldenText(text string, generated bool) string {
	if !generated && len(text) <= 240 {
		return fmt.Sprintf("%q", text)
	}
	return fmt.Sprintf("bytes %d sha256 %x", len(text), sha256.Sum256([]byte(text)))
}

// golden holds got to the golden file at path, which -update records when
// it doesn't exist yet; changed says what a difference means.
func golden(t *testing.T, path, got, changed string) {
	t.Helper()
	if *update {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
		}
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "record %s with -update", path)
	assert.Equal(t, string(want), got, changed)
}

// headingsAt is the headings of the first paragraph of md holding word.
func headingsAt(t *testing.T, md, word string) []string {
	t.Helper()
	for _, p := range splitParagraphs(md) {
		if slices.Contains(strings.Fields(p.text), word) {
			return p.headings
		}
	}
	t.Fatalf("no paragraph holds %q", word)
	return nil
}

// TestSplitParagraphs_Headings: each paragraph's headings are those open
// once its own heading lines are read, by the outline's rules.
func TestSplitParagraphs_Headings(t *testing.T) {
	for name, tc := range map[string]struct {
		md   string
		want map[string][]string // a word, and the headings of its paragraph
	}{
		"ATX levels 1 to 6, closing runs dropped": {
			md: "# One #\nw1\n\n## Two ##\nw2\n\n### Three ###\t \nw3\n\n#### Four\nw4\n\n##### Five\nw5\n\n" +
				"###### Six\nw6\n\n####### Seven is text\nw7",
			want: map[string][]string{"w1": {"One"}, "w2": {"One", "Two"}, "w3": {"One", "Two", "Three"},
				"w4": {"One", "Two", "Three", "Four"}, "w5": {"One", "Two", "Three", "Four", "Five"},
				"w6": {"One", "Two", "Three", "Four", "Five", "Six"}, "w7": {"One", "Two", "Three", "Four", "Five", "Six"}},
		},
		"a closing run after no space is text": {
			md:   "# Title #tag\nw1",
			want: map[string][]string{"w1": {"Title #tag"}},
		},
		"hashtags and includes": {
			md:   "#hashtag\nw1\n\n#include <stdio.h>\nw2\n\n# Real\n\n#another\nw3",
			want: map[string][]string{"w1": nil, "w2": nil, "w3": {"Real"}},
		},
		"indentation": {
			md:   "    # Indented as code\nw1\n\n   # Three spaces\nw2\n\n\t# A tab\nw3",
			want: map[string][]string{"w1": nil, "w2": {"Three spaces"}, "w3": {"Three spaces"}},
		},
		"fences": {
			md: "# Before\n\n```sh\n# no\nw1\n```\n\nw2\n\n~~~\n## no\n~~~\n\nw3\n\n````\n```\n# no\n````\n\nw4\n\n" +
				"```\nTitle\n---\nw5\n```\n\nw6\n\n```\n# an unclosed fence\nw7\n\n## runs to the end\nw8",
			want: map[string][]string{"w1": {"Before"}, "w2": {"Before"}, "w3": {"Before"}, "w4": {"Before"},
				"w5": {"Before"}, "w6": {"Before"}, "w7": {"Before"}, "w8": {"Before"}},
		},
		"setext": {
			md:   "Title\n=====\nw1\n\nSub\n---  \nw2\n\n   Indented\n   ===\nw3",
			want: map[string][]string{"w1": {"Title"}, "w2": {"Title", "Sub"}, "w3": {"Indented"}},
		},
		"setext only under a paragraph's only line": {
			md: "# A\n\ntwo\nlines\n---\nw1\n\n- item\n---\nw2\n\n* item\n===\nw3\n\n1. item\n---\nw4\n\n" +
				"2) item\n---\nw5\n\n> quote\n---\nw6\n\n| a | b |\n---\nw7\n\n    code\n---\nw8\n\n---\nw9",
			want: map[string][]string{"w1": {"A"}, "w2": {"A"}, "w3": {"A"}, "w4": {"A"}, "w5": {"A"}, "w6": {"A"},
				"w7": {"A"}, "w8": {"A"}, "w9": {"A"}},
		},
		"nesting": {
			md:   "# A\nw1\n\n## B\nw2\n\n### C\nw3\n\n## D\nw4\n\n# E\nw5",
			want: map[string][]string{"w1": {"A"}, "w2": {"A", "B"}, "w3": {"A", "B", "C"}, "w4": {"A", "D"}, "w5": {"E"}},
		},
		"an empty heading closes and opens nothing": {
			md:   "# A\n\n## B\nw1\n\n## ![badge](https://example.com/b.svg)\nw2\n\n#\nw3",
			want: map[string][]string{"w1": {"A", "B"}, "w2": {"A"}, "w3": nil},
		},
		"a heading never relabels the text above it": {
			md:   "w1 runs straight\n# Into\nw2\n\nw3 and a setext\nHeading\n---\nw4",
			want: map[string][]string{"w1": nil, "w2": {"Into"}, "w3": {"Into"}, "w4": {"Into"}},
		},
		"a setext heading is its own paragraph's": {
			md:   "# A\n\nTitle\n---\nw1\n\nw2",
			want: map[string][]string{"w1": {"A", "Title"}, "w2": {"A", "Title"}},
		},
		"trailing headings stand alone under themselves": {
			md:   "w1\n\n# End\n## Notes",
			want: map[string][]string{"w1": nil, "Notes": {"End", "Notes"}},
		},
		"CRLF": {
			md:   "# A\r\n\r\nw1\r\nSub\r\n---\r\n\r\nTitle\r\n---\r\nw2\r\n",
			want: map[string][]string{"w1": {"A"}, "w2": {"A", "Title"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			for word, want := range tc.want {
				assert.Equal(t, want, headingsAt(t, tc.md, word), word)
			}
		})
	}
}

// TestReduceHeading: a heading's markdown reduces to its plain text.
func TestReduceHeading(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("word ", 100))
	for in, want := range map[string]string{
		"[Getting started](https://example.com/start) with `curio` ¶":                      "Getting started with curio",
		"![badge](https://example.com/b.svg) **Install** the _daemon_ #":                   "Install the _daemon_",
		`<a id="x"></a>Configure <https://example.com> <ftp://example.com/f> [a ref][r] §`: "Configure a ref",
		`Escaped \[brackets\] and \_under\_ \# kept`:                                       "Escaped [brackets] and _under_ # kept",
		"**bold**, *em*, __strong__ and `code`":                                            "bold, em, strong and code",
		"| Table-ish |":                                                                    "Table-ish",
		"🔗 Anchored §":                                                                     "Anchored",
		"Café über  naïve\t日本語":                                                            "Café über naïve 日本語",
		"[![Build](https://example.com/ci.svg)](https://example.com/ci)":                   "",
		" # ":       "",
		"":          "",
		"C# basics": "C# basics",
		long:        long[:maxHeadingBytes],
		"x" + long:  "x" + long[:maxHeadingBytes-2], // the cut lands after a space
	} {
		assert.Equal(t, want, reduceHeading(in), in)
	}
	cut := reduceHeading(strings.Repeat("日本語", 40))
	assert.True(t, utf8.ValidString(cut))
	assert.Equal(t, strings.Repeat("日本語", 28)+"日", cut, "cut on a rune boundary within 256 bytes")
}

// TestChunkText_HeadingsOfTheFirstNewWord: a chunk's headings are those of
// the paragraph holding the first word no earlier chunk holds.
func TestChunkText_HeadingsOfTheFirstNewWord(t *testing.T) {
	t.Run("the overlap crosses a heading", func(t *testing.T) {
		md := "# A\n\n" + words("a", 20) + "\n\n# B\n\n" + words("b", 10)
		chunks := ChunkText(md, ChunkOptions{SizeTokens: 24, OverlapTokens: 6})
		require.Len(t, chunks, 2)
		assert.Equal(t, []string{"A"}, chunks[0].Headings)
		assert.True(t, strings.HasPrefix(chunks[1].Text, "a14 "), "opens with words carried from A: %s", chunks[1].Text)
		assert.Equal(t, []string{"B"}, chunks[1].Headings, "named by what it adds")
	})
	t.Run("a paragraph split into windows", func(t *testing.T) {
		md := "# A\n\nx\n\n## B\n\n" + words("b", 30) + "\n\n# C\n\ny"
		chunks := ChunkText(md, ChunkOptions{SizeTokens: 10, OverlapTokens: 3})
		require.Greater(t, len(chunks), 4)
		assert.Equal(t, []string{"A"}, chunks[0].Headings)
		for _, c := range chunks[1 : len(chunks)-1] {
			assert.Equal(t, []string{"A", "B"}, c.Headings, c.Text)
		}
		assert.Equal(t, []string{"C"}, chunks[len(chunks)-1].Headings)
	})
	t.Run("a chunk cut by the byte cap across sections", func(t *testing.T) {
		md := "# First\n\n" + urls("first", 6) + "\n\n# Second\n\n" + urls("second", 6)
		chunks := ChunkText(md, ChunkOptions{SizeTokens: 384, OverlapTokens: 48, SizeChars: 200})
		require.Greater(t, len(chunks), 3)
		var sections []string
		for _, c := range chunks {
			require.Len(t, c.Headings, 1, c.Text)
			if !slices.Contains(sections, c.Headings[0]) {
				sections = append(sections, c.Headings[0])
			}
			if strings.Contains(c.Text, "/second/") && !strings.Contains(c.Text, "/first/") {
				assert.Equal(t, []string{"Second"}, c.Headings, c.Text)
			}
		}
		assert.Equal(t, []string{"First", "Second"}, sections)
	})
	t.Run("the pieces of a word longer than the cap", func(t *testing.T) {
		md := "# A\n\nshort\n\n# B\n\n" + strings.Repeat("z", 500) + "\n\n# C\n\ntail"
		chunks := ChunkText(md, ChunkOptions{SizeTokens: 384, OverlapTokens: 48, SizeChars: 200})
		require.Len(t, chunks, 5)
		assert.Equal(t, "# A short # B", chunks[0].Text)
		assert.Equal(t, []string{"A"}, chunks[0].Headings)
		for _, piece := range chunks[1:4] {
			assert.Equal(t, []string{"B"}, piece.Headings, "every piece is its word's")
		}
		assert.Equal(t, []string{"C"}, chunks[4].Headings)
	})
}

func chunkTexts(chunks []Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Text
	}
	return out
}

// FuzzChunkText checks the chunker's safety properties: it terminates,
// respects the byte cap, emits valid UTF-8 for valid input, gives each
// chunk at most six one-line headings of at most maxHeadingBytes, and never
// drops content (overlap may repeat words, so output can only have more).
func FuzzChunkText(f *testing.F) {
	f.Add("# Title\n\nSome words here.", uint16(0), uint16(0), uint16(0))
	f.Add(strings.Repeat("机器学习", 400), uint16(100), uint16(8), uint16(2))
	f.Add("a "+strings.Repeat("😀", 50)+" b", uint16(4), uint16(3), uint16(1))
	f.Add("x\r\n\r\n## h\n## i\n\nbody ![a](data:image/png;base64,AAAA) end", uint16(16), uint16(2), uint16(5))
	f.Add("Title\n---\n```\n# no\n```\n### [a](b) **c** ¶ #\n"+strings.Repeat("日本語", 100), uint16(64), uint16(4), uint16(1))
	f.Fuzz(func(t *testing.T, md string, sizeChars, sizeTokens, overlap uint16) {
		// With overlap close to the chunk size, each chunk advances by only
		// a word or two, so the output grows as words × size and the fuzzer
		// spends its time on output volume instead of new inputs. Half the
		// size still exercises overlap.
		size := cmp.Or(int(sizeTokens), 384)
		overlap %= uint16(size/2 + 1)
		chunks := ChunkText(md, ChunkOptions{
			SizeTokens:    int(sizeTokens),
			OverlapTokens: int(overlap),
			SizeChars:     int(sizeChars),
		})
		limit := int(sizeChars)
		if limit == 0 {
			limit = 3500
		}
		valid := utf8.ValidString(md)
		for i, c := range chunks {
			if limit >= utf8.UTFMax && len(c.Text) > limit {
				t.Fatalf("chunk[%d] is %d bytes, cap %d", i, len(c.Text), limit)
			}
			checkHeadings(t, i, c.Headings, valid)
		}
		if !valid {
			return
		}
		var out int
		for i, c := range chunks {
			if !utf8.ValidString(c.Text) {
				t.Fatalf("chunk[%d] is not valid UTF-8: %q", i, c.Text)
			}
			out += nonSpaceRunes(c.Text)
		}
		if in := nonSpaceRunes(sanitizeForEmbedding(md)); out < in {
			t.Fatalf("content lost: %d non-space runes in, %d out", in, out)
		}
	})
}

// checkHeadings fails unless chunk i's headings are at most six, each a
// line of at most maxHeadingBytes, not empty, with no space at either end,
// and valid UTF-8 when the markdown is (valid).
func checkHeadings(t *testing.T, i int, headings []string, valid bool) {
	t.Helper()
	if len(headings) > 6 {
		t.Fatalf("chunk[%d] has %d headings: %q", i, len(headings), headings)
	}
	for _, h := range headings {
		if (valid && !utf8.ValidString(h)) || len(h) > maxHeadingBytes || strings.ContainsAny(h, "\r\n") ||
			strings.TrimSpace(h) != h || h == "" {
			t.Fatalf("chunk[%d] has the heading %q", i, h)
		}
	}
}

func nonSpaceRunes(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}
