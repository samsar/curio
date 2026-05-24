// Package indexer turns extracted markdown into chunks ready for embedding,
// and orchestrates the chunk → embed → store pipeline.
//
// This file contains only the chunker — pure logic with no external deps —
// so it's trivially testable. The orchestration lives in indexer.go.
package indexer

import (
	"strings"
)

// Chunk is the chunker's output unit. Indexer.Index converts these into
// store.ChunkInput by attaching embeddings.
type Chunk struct {
	Text       string
	TokenCount int // approximate; word-count for M0
}

// ChunkOptions controls splitting behavior.
type ChunkOptions struct {
	SizeTokens    int // target chunk size in whitespace-words; default 384
	OverlapTokens int // overlap between consecutive chunks; default 48
	// SizeChars is a hard upper bound on chunk length in bytes,
	// applied AFTER word-count splitting. Acts as a safety net for
	// dense markdown (long URLs, code blocks, tables) where BPE
	// tokens-per-word is high and a "word-correct" chunk still
	// overflows the embedder's context window.
	//
	// Rule of thumb: ~4 chars per BPE token for English. nomic-embed-text
	// v1 caps at 2048 tokens regardless of what Ollama's modelfile sets,
	// so 6000 chars ≈ 1500 tokens leaves a comfortable margin.
	// Default 6000.
	SizeChars int
}

// Chunk splits markdown into chunks. The split is paragraph-aware: a single
// paragraph stays whole when possible, only falling back to word-level
// splitting when one paragraph exceeds SizeTokens.
//
// Token counting is approximate (whitespace-separated words). This is close
// enough for embedder budget planning at M0; we can swap in a real
// tokenizer later without changing the interface.
func ChunkText(markdown string, opts ChunkOptions) []Chunk {
	if strings.TrimSpace(markdown) == "" {
		return nil
	}
	size := opts.SizeTokens
	if size <= 0 {
		size = 384
	}
	overlap := opts.OverlapTokens
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= size {
		overlap = size / 8
	}
	maxChars := opts.SizeChars
	if maxChars <= 0 {
		// 3500 chars ≈ 875 BPE tokens on plain English prose, ≈ 1500-1800
		// on URL/code-dense markdown (URLs tokenize at ~30 tokens each).
		// Both well under nomic-embed-text's 2048-token hard ceiling.
		maxChars = 3500
	}

	paragraphs := splitParagraphs(markdown)

	var (
		chunks      []Chunk
		curWords    []string
		curTokens   int
	)
	flush := func() {
		if curTokens == 0 {
			return
		}
		chunks = append(chunks, Chunk{
			Text:       strings.Join(curWords, " "),
			TokenCount: curTokens,
		})
		// Keep last `overlap` words as the seed for the next chunk.
		if overlap > 0 && len(curWords) > overlap {
			curWords = append([]string{}, curWords[len(curWords)-overlap:]...)
			curTokens = len(curWords)
		} else {
			curWords = nil
			curTokens = 0
		}
	}

	for _, p := range paragraphs {
		words := strings.Fields(p)
		if len(words) == 0 {
			continue
		}

		// If a single paragraph dwarfs the chunk size, split it into
		// word-runs of `size` with overlap between sub-chunks. We don't
		// preserve cross-paragraph overlap here — once we know we're
		// splitting a big paragraph, the overlap from the previous
		// (smaller) paragraph isn't meaningful continuity.
		if len(words) > size {
			// Flush any in-progress chunk (without leaving overlap behind,
			// to avoid emitting a stray overlap-only chunk later).
			if curTokens > 0 {
				chunks = append(chunks, Chunk{
					Text:       strings.Join(curWords, " "),
					TokenCount: curTokens,
				})
				curWords = nil
				curTokens = 0
			}
			for i := 0; i < len(words); i += size - overlap {
				end := i + size
				if end > len(words) {
					end = len(words)
				}
				chunks = append(chunks, Chunk{
					Text:       strings.Join(words[i:end], " "),
					TokenCount: end - i,
				})
				if end == len(words) {
					break
				}
			}
			continue
		}

		// Would adding this paragraph exceed the size budget? If so,
		// flush first so the paragraph stays whole.
		if curTokens+len(words) > size && curTokens > 0 {
			flush()
		}
		curWords = append(curWords, words...)
		curTokens += len(words)
	}
	flush()

	// Safety net: split anything still over the char cap.
	return enforceCharLimit(chunks, maxChars)
}

// enforceCharLimit splits chunks whose Text exceeds maxChars into smaller
// chunks at word boundaries. Sub-chunks share a small overlap (maxChars/16)
// so semantic continuity is preserved across the split.
func enforceCharLimit(in []Chunk, maxChars int) []Chunk {
	if maxChars <= 0 {
		return in
	}
	overlapChars := maxChars / 16
	if overlapChars < 100 {
		overlapChars = 100
	}

	var out []Chunk
	for _, c := range in {
		if len(c.Text) <= maxChars {
			out = append(out, c)
			continue
		}
		// Walk word boundaries, packing into byte-budgeted sub-chunks.
		words := strings.Fields(c.Text)
		var buf strings.Builder
		var bufWords []string
		flush := func() {
			if buf.Len() == 0 {
				return
			}
			out = append(out, Chunk{Text: buf.String(), TokenCount: len(bufWords)})
			// Seed next chunk with the tail of this one for continuity.
			buf.Reset()
			seedBytes := 0
			start := len(bufWords)
			for ; start > 0 && seedBytes < overlapChars; start-- {
				seedBytes += len(bufWords[start-1]) + 1
			}
			seed := bufWords[start:]
			bufWords = append([]string{}, seed...)
			for i, w := range seed {
				if i > 0 {
					buf.WriteByte(' ')
				}
				buf.WriteString(w)
			}
		}
		for _, w := range words {
			projected := buf.Len()
			if projected > 0 {
				projected++ // space
			}
			projected += len(w)
			if projected > maxChars && buf.Len() > 0 {
				flush()
			}
			if buf.Len() > 0 {
				buf.WriteByte(' ')
			}
			buf.WriteString(w)
			bufWords = append(bufWords, w)
		}
		if buf.Len() > 0 {
			out = append(out, Chunk{Text: buf.String(), TokenCount: len(bufWords)})
		}
	}
	return out
}

// splitParagraphs splits on blank lines but preserves leading markdown
// heading lines as their own "paragraph" so headings travel with whichever
// chunk follows them.
func splitParagraphs(s string) []string {
	// Normalize line endings.
	s = strings.ReplaceAll(s, "\r\n", "\n")

	var (
		out []string
		buf strings.Builder
	)
	flush := func() {
		t := strings.TrimSpace(buf.String())
		if t != "" {
			out = append(out, t)
		}
		buf.Reset()
	}

	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			// Heading line: flush whatever came before so the heading
			// joins the next chunk rather than gluing onto the previous.
			flush()
			out = append(out, trimmed)
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte(' ')
		}
		buf.WriteString(trimmed)
	}
	flush()

	return out
}
