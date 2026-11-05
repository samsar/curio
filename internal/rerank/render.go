package rerank

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/samsar/curio/internal/textutil"
)

// A listwise prompt holds every candidate, and must fit the model's
// context by construction: Ollama truncates an over-long prompt silently,
// dropping its head, the instructions (measured on 0.40.14), and has no
// endpoint that counts a prompt's tokens. So each candidate gets an even
// share of what the context leaves once the instructions, the question at
// the densest rates (textutil.TokenQuarters), the answer and a margin are
// counted, and its fields are filled in a fixed order, each to its own
// cap, while they fit the share.
const (
	// numCtx is the context size every writing model's generator asks for
	// (generator.NewOllama's default).
	numCtx = 8192
	// ctxMargin is the context left unused: the chat template's tokens,
	// and rounding.
	ctxMargin = 128
	// listwiseInstructionTokens is the most the listwise prompt costs
	// without its question and candidates, with margin: measured with an
	// empty question and no candidates (prompt_eval_count, Ollama 0.40.14)
	// at 252 tokens on gemma4:26b, its chat template included, and 230 on
	// a Qwen3-Reranker build's tokenizer, sent raw.
	listwiseInstructionTokens = 400
	// maxOrderTokens caps a listwise answer (num_predict): an order of
	// 30 numbers is about 100 tokens.
	maxOrderTokens = 256
	// maxQuestionBytes is the longest question reranked; a longer one is
	// a paste, not a question, and keeps its first ranking without asking
	// the model, as the planner does.
	maxQuestionBytes = 512
	// pointwiseBytes caps a pointwise candidate, every field together: one
	// candidate a call, so the context isn't what bounds it, the call's
	// time is. Measured at this cap on the owner's library (p50 over 4,195
	// calls each): gemma4:26b 0.35 s a call, so 30 candidates outlast the
	// 7 s timeout; a Qwen3-Reranker 4B build 0.17 s.
	pointwiseBytes = 1200
)

// The fields' caps, in bytes, before a listwise share or the pointwise
// cap cuts them further. Lists are joined by " | ".
const (
	titleBytes        = 200
	savedAsBytes      = 200
	siteBytes         = 80
	authorBytes       = 80
	noteBytes         = 60
	kindBytes         = 20
	aboutBytes        = 300
	passageBytes      = 600
	descriptionsBytes = 300
	entitiesBytes     = 200
	claimsBytes       = 300
	foldersBytes      = 120
	listSeparator     = " | "
)

// neutralize is s as a candidate's field or a question is sent: valid
// UTF-8, its invalid bytes dropped; control characters and Unicode's
// format characters dropped (but the zero-width non-joiner and joiner,
// which words need); '<' and '>' replaced by '‹' and '›', so no text can
// write a chat template's tokens (Ollama parses "<|im_end|>" inside a
// prompt as the special token); every run of whitespace one space,
// trimmed; at most maxBytes, cut on a rune boundary. One pass, stopping
// once maxBytes are written, so a page of any length costs at most its
// share.
func neutralize(s string, maxBytes int) string {
	var b strings.Builder
	b.Grow(min(len(s), maxBytes))
	space := false
	for _, r := range s {
		switch {
		case r == utf8.RuneError: // an invalid byte
			continue
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d':
			continue
		case r == '<':
			r = '‹'
		case r == '>':
			r = '›'
		}
		n := utf8.RuneLen(r)
		if space {
			n++
		}
		if b.Len()+n > maxBytes {
			break
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// list is items neutralized and joined, at most maxBytes, whole items
// first: an item that doesn't fit is cut only when it is the first.
func list(items []string, maxBytes int) string {
	var b strings.Builder
	for _, it := range items {
		it = neutralize(it, maxBytes)
		if it == "" {
			continue
		}
		sep := 0
		if b.Len() > 0 {
			sep = len(listSeparator)
		}
		if b.Len()+sep+len(it) > maxBytes {
			if b.Len() == 0 {
				b.WriteString(textutil.TruncateBytes(it, maxBytes))
			}
			break
		}
		if sep > 0 {
			b.WriteString(listSeparator)
		}
		b.WriteString(it)
	}
	return b.String()
}

// field is one of a candidate's fields as a prompt shows it: its key, its
// label in a pointwise block, and its text, neutralized and cut to its cap.
type field struct {
	key, label, text string
}

// fields are c's fields as in shows them, in the order they are filled:
// what it is, why its page can't be read, its summary's kind and about,
// its passage, the summary's descriptions and names, then its claims and
// folders when shown.
func fields(c Candidate, in Inputs) []field {
	out := []field{
		{"title", "title", neutralize(c.Title, titleBytes)},
		{"saved_as", "saved as", list(c.SavedAs, savedAsBytes)},
		{"site", "site", neutralize(c.Site, siteBytes)},
		{"author", "author", neutralize(c.Author, authorBytes)},
		{"note", "note", neutralize(c.note(), noteBytes)},
	}
	summary := c.Summary
	if in.Input == InputPassage {
		summary = nil
	}
	if summary != nil {
		out = append(out, field{"kind", "kind", neutralize(summary.Kind, kindBytes)},
			field{"about", "about", neutralize(summary.About, aboutBytes)})
	}
	if in.Input != InputSummary {
		out = append(out, field{"passage", "passage", neutralize(c.Passage, passageBytes)})
	}
	if summary != nil {
		out = append(out, field{"descriptions", "descriptions", list(summary.Descriptions, descriptionsBytes)},
			field{"entities", "names", list(summary.Entities, entitiesBytes)})
		if in.Claims {
			out = append(out, field{"claims", "claims", list(summary.Claims, claimsBytes)})
		}
	}
	if in.Folders {
		out = append(out, field{"folders", "folders", list(c.Folders, foldersBytes)})
	}
	return out
}

// listwiseShare is how many quarter-tokens each of n candidates' lines may
// cost in a listwise prompt for question, the newline after it apart.
func listwiseShare(question string, n int) int {
	n = max(n, 1)
	left := 4*(numCtx-ctxMargin-listwiseInstructionTokens-maxOrderTokens) - textutil.TokenQuarters(question) -
		n*textutil.ByteQuarters
	return max(left/n, 0)
}

// listwiseLine is c, numbered n, as one line of JSON holding its fields in
// order while their cost stays within share quarter-tokens, the last one
// that would pass it cut to fit and the rest left out. Keys are fixed, and
// the text is neutralized before it is encoded, so no text can break the
// line, the object or the numbering.
func listwiseLine(n int, c Candidate, in Inputs, share int) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, `{"n":%d`, n)
	left := share - textutil.TokenQuarters(b.String()) - textutil.TokenQuarters("}")
	for _, f := range fields(c, in) {
		if f.text == "" {
			continue
		}
		key := `,"` + f.key + `":""`
		text := fitJSON(f.text, left-textutil.TokenQuarters(key))
		if text == "" {
			break
		}
		b.WriteString(`,"` + f.key + `":`)
		encodeString(&b, text)
		left -= textutil.TokenQuarters(key) + jsonQuarters(text)
	}
	b.WriteString("}")
	return b.String()
}

// encodeString writes s as a JSON string, without HTML escaping: s is
// neutralized, so only '"' and '\' are escaped.
func encodeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		panic(fmt.Sprintf("rerank: encode a string: %v", err)) // a string always encodes
	}
	b.Truncate(b.Len() - 1) // Encode's newline
}

// jsonQuarters is what s costs inside a JSON string, its escapes counted.
func jsonQuarters(s string) int {
	n := textutil.TokenQuarters(s)
	n += textutil.ByteQuarters * (strings.Count(s, `"`) + strings.Count(s, `\`))
	return n
}

// fitJSON is the longest prefix of s, cut on a rune boundary, whose cost
// inside a JSON string is at most quarters.
func fitJSON(s string, quarters int) string {
	if jsonQuarters(s) <= quarters {
		return s
	}
	cost := 0
	for i, r := range s {
		c := jsonQuarters(string(r))
		if cost+c > quarters {
			return strings.TrimSpace(s[:i])
		}
		cost += c
	}
	return s
}

// pointwiseBlock is c as a pointwise prompt shows it: one labeled line a
// field, in order, every field together at most maxBytes, the last one
// that would pass it cut to fit and the rest left out.
func pointwiseBlock(c Candidate, in Inputs, maxBytes int) string {
	var b strings.Builder
	for _, f := range fields(c, in) {
		if f.text == "" {
			continue
		}
		label := f.label + ": "
		left := maxBytes - b.Len() - len(label) - 1
		if left <= 0 {
			break
		}
		text := strings.TrimSpace(textutil.TruncateBytes(f.text, left))
		if text == "" {
			break
		}
		b.WriteString(label + text + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
