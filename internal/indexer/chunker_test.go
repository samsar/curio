package indexer

import (
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

func chunkTexts(chunks []Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Text
	}
	return out
}

// FuzzChunkText checks the chunker's safety properties: it terminates,
// respects the byte cap, emits valid UTF-8 for valid input, and never drops
// content (overlap may repeat words, so output can only have more).
func FuzzChunkText(f *testing.F) {
	f.Add("# Title\n\nSome words here.", uint16(0), uint16(0), uint16(0))
	f.Add(strings.Repeat("机器学习", 400), uint16(100), uint16(8), uint16(2))
	f.Add("a "+strings.Repeat("😀", 50)+" b", uint16(4), uint16(3), uint16(1))
	f.Add("x\r\n\r\n## h\n## i\n\nbody ![a](data:image/png;base64,AAAA) end", uint16(16), uint16(2), uint16(5))
	f.Fuzz(func(t *testing.T, md string, sizeChars, sizeTokens, overlap uint16) {
		chunks := ChunkText(md, ChunkOptions{
			SizeTokens:    int(sizeTokens),
			OverlapTokens: int(overlap),
			SizeChars:     int(sizeChars),
		})
		limit := int(sizeChars)
		if limit == 0 {
			limit = 3500
		}
		for i, c := range chunks {
			if limit >= utf8.UTFMax && len(c.Text) > limit {
				t.Fatalf("chunk[%d] is %d bytes, cap %d", i, len(c.Text), limit)
			}
		}
		if !utf8.ValidString(md) {
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

func nonSpaceRunes(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}
