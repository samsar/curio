// Package indexer turns extracted markdown into chunks ready for embedding,
// and orchestrates the chunk → embed → store pipeline.
//
// This file contains only the chunker — pure logic with no external deps —
// so it's trivially testable. The orchestration lives in indexer.go.
package indexer

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/samsar/curio/internal/textutil"
)

// Chunk is the chunker's output unit. Indexer.Index converts these into
// store.ChunkInput by attaching embeddings.
type Chunk struct {
	Text       string
	TokenCount int // whitespace-separated words, an approximation of model tokens
	// Headings are the markdown headings open where the chunk's own text
	// begins, outermost first, each reduced to plain text (reduceHeading):
	// those of the paragraph holding its first new word, the first word
	// no earlier chunk holds. A chunk opens with words carried over from
	// the one before it, often of the section before, and its headings
	// name the section of what it adds. None before a document's first
	// heading.
	Headings []string
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
	// on URL- or code-dense text, and never more than one token per byte
	// for a byte-level BPE tokenizer like Qwen3-Embedding's. So 3500 bytes
	// is at most 3500 tokens, well inside the embedder's num_ctx of 8192,
	// whatever the content (see decisions.md). Default 3500.
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
		// 3500 bytes ≈ 875 BPE tokens on plain English prose, ≈ 1500-1800
		// on URL/code-dense markdown (URLs tokenize at ~30 tokens each),
		// and at most 3500 for a byte-level tokenizer.
		maxChars = 3500
	}

	paragraphs := splitParagraphs(markdown)
	drafts, starts := pack(paragraphs, size, overlap)
	// Safety net: split anything still over the char cap.
	return finish(enforceCharLimit(drafts, maxChars), paragraphs, starts)
}

// draft is a chunk being cut: its words, which are the document's words
// from start on, counted across its paragraphs, so a chunk knows which
// paragraphs its words came from. A piece of a word longer than the byte
// cap is a draft of one word, that word's piece.
type draft struct {
	words []string
	start int
}

// end is the offset just past d's words.
func (d draft) end() int { return d.start + len(d.words) }

// bytes is the length of d's text: its words joined by single spaces.
func (d draft) bytes() int {
	n := len(d.words) - 1
	for _, w := range d.words {
		n += len(w)
	}
	return n
}

// pack packs paragraphs' words into drafts of at most size words. A
// paragraph stays whole when it fits, and each draft's last overlap words
// open the next. A paragraph longer than size is split into word-runs of
// size with overlap between them; the overlap from the previous (smaller)
// paragraph isn't carried into those, since once a big paragraph is being
// split, it isn't meaningful continuity. starts[i] is the offset of
// paragraph i's first word.
func pack(paragraphs []paragraph, size, overlap int) (drafts []draft, starts []int) {
	var cur draft // being filled
	flush := func() {
		if len(cur.words) == 0 {
			return
		}
		drafts = append(drafts, cur)
		// Keep the last overlap words as the seed for the next draft.
		if overlap > 0 && len(cur.words) > overlap {
			cur = draft{words: slices.Clone(cur.words[len(cur.words)-overlap:]), start: cur.end() - overlap}
		} else {
			cur = draft{}
		}
	}

	pos := 0
	for _, p := range paragraphs {
		starts = append(starts, pos)
		words := strings.Fields(p.text)
		switch {
		case len(words) > size:
			// The draft in progress goes out without leaving overlap
			// behind, so no stray overlap-only draft follows.
			if len(cur.words) > 0 {
				drafts = append(drafts, cur)
				cur = draft{}
			}
			for i := 0; i < len(words); i += size - overlap {
				end := min(i+size, len(words))
				drafts = append(drafts, draft{words: words[i:end], start: pos + i})
				if end == len(words) {
					break
				}
			}
		case len(words) > 0:
			// Would adding this paragraph exceed the size budget? If so,
			// flush first so the paragraph stays whole.
			if len(cur.words)+len(words) > size && len(cur.words) > 0 {
				flush()
			}
			if len(cur.words) == 0 {
				cur.start = pos
			}
			cur.words = append(cur.words, words...)
		}
		pos += len(words)
	}
	flush()
	return drafts, starts
}

// finish makes the chunks of drafts, giving each the headings of the
// paragraph holding its first new word: the first word at or past the end
// of every draft before it, or its own last word when it adds none (a
// draft of carried words alone, or a draft the byte cap cut from carried
// words). A later piece of a word longer than the cap has that word's
// paragraph.
func finish(drafts []draft, paragraphs []paragraph, starts []int) []Chunk {
	chunks := make([]Chunk, len(drafts))
	held := 0 // the end of the words the drafts so far hold
	for i, d := range drafts {
		first := min(max(d.start, held), d.end()-1)
		held = max(held, d.end())
		p := sort.Search(len(starts), func(j int) bool { return starts[j] > first }) - 1
		chunks[i] = Chunk{Text: strings.Join(d.words, " "), TokenCount: len(d.words),
			Headings: slices.Clone(paragraphs[p].headings)}
	}
	return chunks
}

// WordCount is how many words markdown holds, in the chunker's own unit:
// whitespace-separated words of the text it chunks, inline data URLs
// left out (sanitizeForEmbedding). It is 0 for blank markdown.
func WordCount(markdown string) int {
	return len(strings.Fields(sanitizeForEmbedding(markdown)))
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
// PNG → ~30K tokens, far past the embedder's 8192-token num_ctx) AND
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

// enforceCharLimit splits drafts whose text exceeds maxChars into smaller
// drafts at word boundaries. Sub-drafts share a small overlap (maxChars/16)
// so semantic continuity is preserved across the split.
//
// Backstop: a single "word" (no whitespace) longer than maxChars — long URL,
// base64 string that escaped sanitization, unbroken identifier, or a CJK
// paragraph, which has no spaces at all — is split into consecutive pieces
// by splitToken. Without this, oversized tokens silently bypass the byte
// budget and trigger embedder failures downstream.
func enforceCharLimit(in []draft, maxChars int) []draft {
	if maxChars <= 0 {
		return in
	}
	overlapChars := max(maxChars/16, 100)

	var out []draft
	for _, d := range in {
		if d.bytes() <= maxChars {
			out = append(out, d)
			continue
		}
		out = cutToCap(out, d, maxChars, overlapChars)
	}
	return out
}

// cutToCap appends d's pieces of at most maxChars bytes to out, walking
// its words and packing them into a piece, d.words[lo:hi] of size bytes
// joined, until the next word would pass the cap. Each piece but one cut
// before an oversized word seeds the next with its tail of at least
// overlapChars bytes.
func cutToCap(out []draft, d draft, maxChars, overlapChars int) []draft {
	lo, hi, size := 0, 0, 0
	piece := func() draft { return draft{words: d.words[lo:hi], start: d.start + lo} }
	for i, w := range d.words {
		// Backstop: any single word longer than maxChars is emitted as
		// its own run of pieces and skips the normal packing path
		// entirely. Folding it into the piece doesn't work because even
		// after flushing, the overlap seed leaves bytes in it that would
		// make piece + oversized word > maxChars.
		if len(w) > maxChars {
			if hi > lo {
				out = append(out, piece())
			}
			for _, part := range splitToken(w, maxChars) {
				out = append(out, draft{words: []string{part}, start: d.start + i})
			}
			lo, hi, size = i+1, i+1, 0
			continue
		}
		if hi > lo && size+1+len(w) > maxChars {
			out = append(out, piece())
			// Seed the next piece with the tail of this one for
			// continuity.
			from, seed := lo, 0
			for lo = hi; lo > from && seed < overlapChars; lo-- {
				seed += len(d.words[lo-1]) + 1
			}
			size = max(seed-1, 0)
		}
		// Even after flushing, the overlap seed leaves the tail of the
		// previous piece in this one. If that seed plus the next word
		// would itself blow past maxChars, drop the seed entirely —
		// preserving overlap is a nice-to-have, not worth producing an
		// oversized chunk that breaks the embedder. Happens when the
		// previous piece's tail word(s) were near-maxChars and the next
		// word is also large (e.g. two long URLs in a row).
		if size+1+len(w) > maxChars {
			lo, size = hi, 0
		}
		if hi > lo {
			size++
		}
		size += len(w)
		hi = i + 1
	}
	if hi > lo {
		out = append(out, piece())
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

// paragraph is a paragraph as the packer reads it, and the headings open
// once its own heading lines are read: the heading lines it opens with,
// and the setext heading it is. They are shared with the paragraphs under
// the same headings, and never changed.
type paragraph struct {
	text     string
	headings []string
}

// splitParagraphs splits on blank lines. A line starting with '#' ends the
// paragraph before it and is prefixed to the next paragraph, so the packer
// can never leave a heading at the tail of the previous chunk, apart from the
// section it names. Consecutive such lines all join that next paragraph; any
// left at the end of the input stand alone.
//
// Which headings are open is read beside those boundaries, which it never
// changes, by markdown's rules (outline): not every '#' line is a heading,
// and a setext heading starts with no '#'. A heading opens its section
// from its own line on, so it never relabels the paragraph before it.
func splitParagraphs(s string) []paragraph {
	s = strings.ReplaceAll(s, "\r\n", "\n")

	var (
		out      []paragraph
		prefixes []string // '#' lines waiting for the paragraph they introduce
		buf      strings.Builder
		lines    int // in buf
		o        outline
	)
	flush := func() {
		t := strings.TrimSpace(buf.String())
		buf.Reset()
		lines = 0
		if t == "" {
			return
		}
		out = append(out, paragraph{text: strings.Join(append(prefixes, t), " "), headings: o.headings})
		prefixes = nil
	}

	for line := range strings.SplitSeq(s, "\n") {
		// Read before the paragraph before it is flushed, and applied
		// after: a setext heading's text is the paragraph so far.
		h, isHeading := o.read(line, lines == 1, buf.String())
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flush()
		case strings.HasPrefix(trimmed, "#"):
			flush()
			prefixes = append(prefixes, trimmed)
		default:
			if buf.Len() > 0 {
				buf.WriteByte(' ')
			}
			buf.WriteString(trimmed)
			lines++
		}
		if isHeading {
			o.push(h)
		}
	}
	flush()
	if len(prefixes) > 0 {
		out = append(out, paragraph{text: strings.Join(prefixes, " "), headings: o.headings})
	}
	return out
}

// maxHeadingBytes caps a heading's reduced text: a heading is a line's
// title, and longer is a paragraph set as one.
const maxHeadingBytes = 256

// outline follows the headings open as a document's lines are read, by
// markdown's rules, narrowed where the web's markdown fools them:
//
//   - A fence (up to 3 spaces of indentation, then 3 or more backticks or
//     tildes) runs to a line of only its character, at least as many, or
//     to the end; nothing in it is a heading.
//   - An ATX heading is up to 3 spaces of indentation, 1 to 6 '#', then a
//     space, a tab or the line's end: its level is the count, its text the
//     rest without a closing run of '#' after a space or tab. "#hashtag",
//     "#include" and a '#' line indented as code are none.
//   - A setext heading is a line of only '=' (level 1) or only '-' (level
//     2), up to 3 spaces of indentation and trailing spaces or tabs
//     allowed, directly under a text line that is its paragraph's only
//     line so far, which is its text. Under a longer paragraph such a line
//     is a thematic break as often as not, so it is none; nor under a list
//     item, a quote or a table row.
//
// A heading of level L closes every open heading of level L or deeper,
// then opens, unless its text reduces to nothing (reduceHeading).
type outline struct {
	stack    []heading
	headings []string // stack's texts; a new slice whenever they change
	fence    string   // the open fence's marker run; "" outside a fence
	prevText bool     // the line read last is a text line
}

// heading is an open heading: its level and reduced text.
type heading struct {
	level int
	text  string
}

// read reads line, the next line, and returns the heading it is, for push
// once the paragraph boundary it makes is drawn. single says the
// paragraph so far is one line, para, the text a setext underline under
// it would make a heading of.
func (o *outline) read(line string, single bool, para string) (heading, bool) {
	underText := o.prevText
	o.prevText = false
	if o.fence != "" {
		if closesFence(line, o.fence) {
			o.fence = ""
		}
		return heading{}, false
	}
	body, ok := cutIndent(line)
	if !ok {
		return heading{}, false // indented code
	}
	if marker := fenceMarker(body); marker != "" {
		o.fence = marker
		return heading{}, false
	}
	if level, text, ok := atxHeading(body); ok {
		return heading{level: level, text: reduceHeading(text)}, true
	}
	if level := setextLevel(body); level > 0 && underText && single {
		return heading{level: level, text: reduceHeading(para)}, true
	}
	o.prevText = isTextLine(body)
	return heading{}, false
}

// push opens h: it closes every open heading of h's level or deeper, then
// opens h unless its text is empty.
func (o *outline) push(h heading) {
	n := len(o.stack)
	for n > 0 && o.stack[n-1].level >= h.level {
		n--
	}
	o.stack = o.stack[:n]
	if h.text != "" {
		o.stack = append(o.stack, h)
	}
	o.headings = nil
	for _, open := range o.stack {
		o.headings = append(o.headings, open.text)
	}
}

// cutIndent is line without its indentation of up to 3 spaces; ok is
// false for a line indented further, by a fourth space or a tab, which
// markdown reads as code.
func cutIndent(line string) (body string, ok bool) {
	n := 0
	for n < 3 && n < len(line) && line[n] == ' ' {
		n++
	}
	body = line[n:]
	return body, !strings.HasPrefix(body, " ") && !strings.HasPrefix(body, "\t")
}

// fenceMarker is the run of 3 or more backticks or tildes body opens with,
// a fence's opening, or "".
func fenceMarker(body string) string {
	for _, c := range "`~" {
		run := len(body) - len(strings.TrimLeft(body, string(c)))
		if run >= 3 {
			return body[:run]
		}
	}
	return ""
}

// closesFence reports whether line closes the fence opened by marker: it
// holds only marker's character, at least as many times, between spaces.
func closesFence(line, marker string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, marker) && strings.Trim(t, marker[:1]) == ""
}

// atxHeading reads body as an ATX heading: its level, 1 to 6 '#', and its
// text, without the closing run of '#' a space or tab precedes.
func atxHeading(body string) (level int, text string, ok bool) {
	level = len(body) - len(strings.TrimLeft(body, "#"))
	if level == 0 || level > 6 {
		return 0, "", false
	}
	text = body[level:]
	if text != "" && text[0] != ' ' && text[0] != '\t' {
		return 0, "", false
	}
	text = strings.TrimRight(text, " \t")
	if t := strings.TrimRight(text, "#"); t == "" || strings.HasSuffix(t, " ") || strings.HasSuffix(t, "\t") {
		text = t
	}
	return level, text, true
}

// setextLevel is 1 for a setext underline of '=', 2 for one of '-', and 0
// for any other line.
func setextLevel(body string) int {
	u := strings.TrimRight(body, " \t")
	switch {
	case u == "":
		return 0
	case strings.Trim(u, "=") == "":
		return 1
	case strings.Trim(u, "-") == "":
		return 2
	}
	return 0
}

// isTextLine reports whether body, a line without its indentation, can be
// a setext heading's text: it isn't blank, a list item, a quote or a table
// row.
func isTextLine(body string) bool {
	return strings.TrimSpace(body) != "" && !isListItem(body) && !strings.HasPrefix(body, ">") &&
		!strings.HasPrefix(body, "|")
}

// isListItem reports whether body opens a list item: '-', '*' or '+', or
// 1 to 9 digits and '.' or ')', then whitespace or the line's end.
func isListItem(body string) bool {
	var rest string
	switch {
	case body == "":
		return false
	case strings.ContainsRune("-*+", rune(body[0])):
		rest = body[1:]
	default:
		digits := len(body) - len(strings.TrimLeft(body, "0123456789"))
		if digits == 0 || digits > 9 || digits == len(body) || (body[digits] != '.' && body[digits] != ')') {
			return false
		}
		rest = body[digits+1:]
	}
	return rest == "" || rest[0] == ' ' || rest[0] == '\t'
}

// The markup reduceHeading drops or reduces to its text.
var (
	headingImageRE    = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	headingLinkRE     = regexp.MustCompile(`\[([^\]]*)\](?:\([^)]*\)|\[[^\]]*\])`)
	headingAutolinkRE = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9+.-]{1,31}:[^<>\s]*>`)
	headingTagRE      = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	headingEscapeRE   = regexp.MustCompile(`\\([!-/:-@\[-` + "`" + `{-~])`)
	headingEmphasis   = strings.NewReplacer("**", "", "__", "", "*", "", "`", "")
)

// headingTrim is what reduceHeading trims from a heading's ends: the marks
// and anchors sites put beside a heading's text.
const headingTrim = " #¶§🔗|"

// reduceHeading is a heading's markdown as plain text, in order: images
// dropped; links and reference links reduced to their text; autolinks and
// HTML tags dropped; "**", "__", "*" and backticks dropped (a single "_"
// is kept); a backslash before ASCII punctuation dropped; whitespace
// collapsed; the marks of headingTrim trimmed from both ends; and the
// rest cut to maxHeadingBytes on a rune boundary.
func reduceHeading(s string) string {
	s = headingImageRE.ReplaceAllString(s, "")
	s = headingLinkRE.ReplaceAllString(s, "$1")
	s = headingAutolinkRE.ReplaceAllString(s, "")
	s = headingTagRE.ReplaceAllString(s, "")
	s = headingEmphasis.Replace(s)
	s = headingEscapeRE.ReplaceAllString(s, "$1")
	s = strings.Trim(strings.Join(strings.Fields(s), " "), headingTrim)
	return strings.TrimRight(textutil.TruncateBytes(s, maxHeadingBytes), " ")
}
