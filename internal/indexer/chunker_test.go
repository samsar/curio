package indexer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
	require := assert.New(t)
	require.GreaterOrEqual(len(got), 3)
	// Last 3 words of chunk 0 should be first 3 words of chunk 1.
	c0 := strings.Fields(got[0].Text)
	c1 := strings.Fields(got[1].Text)
	require.Equal(c0[len(c0)-3:], c1[:3])
}

func TestChunkText_HeadingsStayWithNextParagraph(t *testing.T) {
	md := "# Title\n\nFirst paragraph words here.\n\n## Subhead\n\nSecond paragraph words here."
	got := ChunkText(md, ChunkOptions{SizeTokens: 10, OverlapTokens: 0})
	require := assert.New(t)
	require.NotEmpty(got)
	// First chunk should contain the title (heading was emitted as its
	// own paragraph and travels with the next words).
	require.Contains(got[0].Text, "Title")
}

func TestChunkText_DefaultsApplyOnZeroOpts(t *testing.T) {
	// 1000 words, default size = 512.
	words := strings.Fields(strings.Repeat("w ", 1000))
	got := ChunkText(strings.Join(words, " "), ChunkOptions{})
	require := assert.New(t)
	require.GreaterOrEqual(len(got), 2, "1000 words should need >1 chunk at default size")
	require.LessOrEqual(got[0].TokenCount, 512)
}

func TestChunkText_OverlapClampedIfLargerThanSize(t *testing.T) {
	// overlap >= size is nonsensical; the impl should clamp rather than
	// loop forever.
	words := strings.Fields(strings.Repeat("w ", 30))
	got := ChunkText(strings.Join(words, " "), ChunkOptions{SizeTokens: 10, OverlapTokens: 100})
	assert.Greater(t, len(got), 1)
}
