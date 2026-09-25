// Package indexer turns extracted markdown into chunks ready for embedding,
// and orchestrates the chunk → embed → store pipeline.
//
// This file contains only the chunker — pure logic with no external deps —
// so it's trivially testable. The orchestration lives in indexer.go.
package indexer

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/samsar/curio/internal/textutil"
)

// Chunk is the chunker's output unit. Indexer.Index converts these into
// store.ChunkInput by attaching embeddings.
type Chunk struct {
	Text       string
	TokenCount int // whitespace-separated words, an approximation of model tokens
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
	// Rule of thumb: ~4 chars per BPE token for English prose, far fewer
	// on URL- or code-dense text. nomic-embed-text v1 caps at 2048 tokens
	// regardless of what Ollama's modelfile sets, and 3500 bytes stays
	// under that even for URL-heavy markdown (see decisions.md).
	// Default 3500.
	SizeChars int
}

// Chunk splits markdown into chunks. The split is paragraph-aware: a single
// paragraph stays whole when possible, only falling back to word-level
// splitting when one paragraph exceeds SizeTokens.
//
// Token counting is approximate: whitespace-separated words. That is close
// enough to size chunks, because the byte cap (SizeChars), not the word
// count, is what keeps a chunk inside the embedder's context window.
func ChunkText(markdown string, opts ChunkOptions) []Chunk {
	if strings.TrimSpace(markdown) == "" {
		return nil
	}
	// Strip embed-poisoning content (inline base64 image data URLs) before
	// splitting. The on-disk markdown is untouched — this only affects what
	// goes to the embedder. See sanitizeForEmbedding for rationale.
	markdown = sanitizeForEmbedding(markdown)
	size := opts.SizeTokens
	if size <= 0 {
		size = 384
	}
	overlap := max(opts.OverlapTokens, 0)
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
		chunks    []Chunk
		curWords  []string
		curTokens int
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
				end := min(i+size, len(words))
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

// sanitizeForEmbedding removes content that's useless to the embedder and
// dangerous to chunk: inline base64 image data URLs.
//
//   - `![alt](data:image/...;base64,...)` becomes `![alt](image)` — we keep
//     the alt text (often meaningful) and a placeholder so the chunk still
//     reads as "this paragraph had an image here," but drop the bytes.
//   - Bare `data:image/...;base64,...` outside markdown image syntax (rare,
//     happens when readability mangles HTML) is dropped.
//
// We only touch the input to the chunker — the on-disk markdown keeps the
// original data URLs, so if a future feature wants the image bytes (vision
// embeddings, OCR, image search), they're still recoverable from disk.
//
// Embedding base64 bytes is pure noise: the BPE tokenizer treats random
// base64 as high-entropy garbage, blowing up token counts (a 100KB inline
// PNG → ~30K tokens, vs. nomic-embed-text's 2048-token hard ceiling) AND
// poisoning the semantic vector with content that has zero retrieval value.
func sanitizeForEmbedding(s string) string {
	if !strings.Contains(s, "data:") {
		return s
	}
	s = markdownDataImageRE.ReplaceAllString(s, "![${1}](image)")
	s = bareDataURLRE.ReplaceAllString(s, "")
	return s
}

var (
	// markdownDataImageRE matches `![alt](data:...)` capturing the alt text.
	// Uses [^)]* for the URL body because data URLs never contain ')'
	// unencoded.
	markdownDataImageRE = regexp.MustCompile(`!\[([^\]]*)\]\(data:[^)]*\)`)

	// bareDataURLRE matches a data URL not enclosed in markdown image syntax.
	// Stops at whitespace or markdown punctuation that wouldn't appear in
	// base64. Conservative: it's better to leave a stray data URL fragment
	// than to over-strip legitimate text containing the literal "data:".
	bareDataURLRE = regexp.MustCompile(`data:[a-zA-Z0-9/+.-]+;base64,[A-Za-z0-9+/=]+`)
)

// enforceCharLimit splits chunks whose Text exceeds maxChars into smaller
// chunks at word boundaries. Sub-chunks share a small overlap (maxChars/16)
// so semantic continuity is preserved across the split.
//
// Backstop: a single "word" (no whitespace) longer than maxChars — long URL,
// base64 string that escaped sanitization, unbroken identifier, or a CJK
// paragraph, which has no spaces at all — is split into consecutive pieces
// by splitToken. Without this, oversized tokens silently bypass the byte
// budget and trigger embedder failures downstream.
func enforceCharLimit(in []Chunk, maxChars int) []Chunk {
	if maxChars <= 0 {
		return in
	}
	overlapChars := max(maxChars/16, 100)

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
			// Backstop: any single word longer than maxChars is emitted as
			// its own run of pieces and skips the normal packing path
			// entirely. Folding it into `buf` doesn't work because even
			// after flushing, the overlap-seed leaves bytes in `buf` that
			// would make `buf + oversized_word > maxChars`.
			if len(w) > maxChars {
				if buf.Len() > 0 {
					out = append(out, Chunk{Text: buf.String(), TokenCount: len(bufWords)})
					buf.Reset()
					bufWords = nil
				}
				for _, piece := range splitToken(w, maxChars) {
					out = append(out, Chunk{Text: piece, TokenCount: 1})
				}
				continue
			}
			projected := buf.Len()
			if projected > 0 {
				projected++ // space
			}
			projected += len(w)
			if projected > maxChars && buf.Len() > 0 {
				flush()
			}
			// Even after flushing, the overlap-seed leaves the tail of the
			// previous chunk in `buf`. If that seed plus the next word
			// would itself blow past maxChars, drop the seed entirely —
			// preserving overlap is a nice-to-have, not worth producing
			// an oversized chunk that breaks the embedder. Happens when
			// the previous chunk's tail word(s) were near-maxChars and
			// the next word is also large (e.g. two long URLs in a row).
			if buf.Len()+1+len(w) > maxChars {
				buf.Reset()
				bufWords = nil
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

// splitToken cuts a whitespace-free token into consecutive pieces of at most
// maxChars bytes, each ending on a rune boundary, so no content is dropped
// and no piece is invalid UTF-8. A cap smaller than one rune still advances
// by one whole rune per piece.
func splitToken(w string, maxChars int) []string {
	var pieces []string
	for w != "" {
		piece := textutil.TruncateBytes(w, maxChars)
		if piece == "" {
			_, size := utf8.DecodeRuneInString(w)
			piece = w[:size]
		}
		pieces = append(pieces, piece)
		w = w[len(piece):]
	}
	return pieces
}

// splitParagraphs splits on blank lines. A markdown heading line ends the
// paragraph before it and is prefixed to the next paragraph, so the packer
// can never leave a heading at the tail of the previous chunk, apart from the
// section it names. Consecutive headings all join that next paragraph; any
// left at the end of the input stand alone.
func splitParagraphs(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")

	var (
		out      []string
		headings []string // waiting for the paragraph they introduce
		buf      strings.Builder
	)
	flush := func() {
		t := strings.TrimSpace(buf.String())
		buf.Reset()
		if t == "" {
			return
		}
		out = append(out, strings.Join(append(headings, t), " "))
		headings = nil
	}

	for line := range strings.SplitSeq(s, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flush()
		case strings.HasPrefix(trimmed, "#"):
			flush()
			headings = append(headings, trimmed)
		default:
			if buf.Len() > 0 {
				buf.WriteByte(' ')
			}
			buf.WriteString(trimmed)
		}
	}
	flush()
	if len(headings) > 0 {
		out = append(out, strings.Join(headings, " "))
	}
	return out
}
