package summarize

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/samsar/curio/internal/textutil"
)

// What a page's prompt holds, and how it is held to the model's context.
// Ollama shares one context size (num_ctx) between the planner, the labels
// and the writer: another would load the model again (measured 5.1 s), so
// the writer asks for the generator's, numCtx. A prompt longer than it is
// truncated silently, keeping its end and dropping the instructions at its
// head (measured on Ollama 0.40.14), so the page is cut to guarantee fit:
// the instructions, the page's fields, its text and maxAnswerTokens of
// answer stay ctxMargin under numCtx, every byte counted at the densest
// rate measured (decisions.md "Summaries: one structured call per
// document, stored for the reranker").
const (
	// numCtx is the context size every writing model's generator asks for
	// (generator.NewOllama's default).
	numCtx = 8192
	// ctxMargin is the context left unused: the chat template's tokens,
	// and rounding.
	ctxMargin = 128
	// instructionTokens is the most the prompt without a page costs, on
	// the models measured (qwen3:4b-instruct and gemma4:26b), with margin.
	instructionTokens = 700
	// maxAnswerTokens caps an answer (num_predict): one at every cap of the
	// schema, at 3 characters a token, with its JSON, fits.
	maxAnswerTokens = 1024
	// scanWindow is how much of a page's markdown is read at most: far
	// more than the text kept, so a page whose opening is all menus and
	// images still yields its text, and far less than the 32 MiB a page
	// may hold.
	scanWindow = 256 << 10
	// maxPageBytes is the most of a page's reduced text the prompt holds:
	// the largest of 8, 12 and 16 KB whose prose fits the context at the
	// densest rates measured (pageTokens).
	maxPageBytes = 8 << 10
	// The page's fields, each cut on a rune boundary.
	maxTitleBytes = 300
	maxURLBytes   = 300
	maxSiteBytes  = 200
)

// The densest rates measured, in quarter-tokens a byte (on Ollama 0.40.14,
// from prompt_eval_count over synthetic text and the operator's sample): a
// token covers at least one byte of any text (a run of digits, Korean
// syllables: one byte a token), and at least 1.34 bytes of a run of ASCII
// letters (random base64, the densest letter-heavy text). Counting each
// byte at its rate over-counts prose, at about 6 bytes a token, five
// times over, and never under-counts a page.
const (
	letterQuarters = 3
	byteQuarters   = 4
)

// Input is exactly what a page's summary is written from, as the prompt
// sends it: the document's own title (never a bookmark's), its URL, its
// display site name (else its host), its content type, and its reduced
// text, cut to fit. DocumentID is for the writer's state and log alone,
// never sent.
type Input struct {
	DocumentID  string
	Title       string
	URL         string
	Site        string
	ContentType string
	Text        string
}

// Document is what BuildInput reads of a document besides its markdown.
type Document struct {
	ID          string
	Title       string
	URL         string
	Site        string
	ContentType string
}

// BuildInput reads a page's markdown, at most scanWindow bytes of it,
// reduces it (reduce) and cuts it to fit the context with the document's
// fields (cut). A read that fails is an error; markdown that reduces to
// nothing is an input with no text, which the model is asked about like
// any other (a page with nothing to read is not_the_page).
func BuildInput(d Document, markdown io.Reader) (Input, error) {
	raw, err := io.ReadAll(io.LimitReader(markdown, scanWindow))
	if err != nil {
		return Input{}, fmt.Errorf("read the page's markdown: %w", err)
	}
	in := Input{
		DocumentID:  d.ID,
		Title:       field(d.Title, maxTitleBytes),
		URL:         field(d.URL, maxURLBytes),
		Site:        field(d.Site, maxSiteBytes),
		ContentType: field(d.ContentType, maxSiteBytes),
	}
	in.Text = cut(reduce(string(raw)), pageTokens(in))
	return in, nil
}

// Hash is the SHA-256 of PromptVersion and the prompt in makes: a summary
// read from an equal hash was written from exactly this input.
func (in Input) Hash() string {
	sum := sha256.Sum256([]byte(PromptVersion + "\n" + renderPrompt(in)))
	return hex.EncodeToString(sum[:])
}

// field is a page's field as the prompt holds it: valid UTF-8, its
// whitespace collapsed, no delimiter, cut to limit bytes on a rune
// boundary.
func field(s string, limit int) string {
	s = strings.Join(strings.Fields(withoutDelimiters(strings.ToValidUTF8(s, ""))), " ")
	return strings.TrimSpace(textutil.TruncateBytes(s, limit))
}

// pageTokens is how many tokens in's text may cost: what the context
// leaves once the instructions, in's fields at the densest rates, the
// answer and the margin are counted.
func pageTokens(in Input) int {
	fields := (estimateQuarters(in.Title+in.URL+in.Site+in.ContentType) + 3) / 4
	return numCtx - ctxMargin - instructionTokens - maxAnswerTokens - fields
}

// estimateQuarters is how many quarter-tokens s costs at most, at the
// densest rates measured.
func estimateQuarters(s string) int {
	n := 0
	for i := range len(s) {
		if c := s[i]; ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') {
			n += letterQuarters
		} else {
			n += byteQuarters
		}
	}
	return n
}

// The markdown a page's text drops: images (spans), data URLs, autolinks
// and link reference definitions; links keep their text (spans). A Go
// regexp's search is linear in what it reads, but ReplaceAllString
// searches again after each match, and a link's search can read on to the
// line's end before it settles on a match (behind an earlier "[x](" left
// open): a search that long for each match is quadratic, so links and
// images are found in one pass. Each of these regexps' searches ends where
// its match does, or is anchored to the line's start or end, so it reads a
// line once (TestBuildInput_HostilePagesCostTheirLength).
var (
	dataURLRE = regexp.MustCompile(`data:[\w.+-]+/[\w.+-]+(?:;[\w=.+-]+)*,[A-Za-z0-9+/=%._-]*`)
	autoRE    = regexp.MustCompile(`<(?:https?|ftp|mailto):[^>\s]*>`)
	refDefRE  = regexp.MustCompile(`^\s{0,3}\[[^\]\n]+\]:\s*\S`)
	headingRE = regexp.MustCompile(`^\s{0,3}#{1,6}(?:\s+|$)`)
	closingRE = regexp.MustCompile(`\s+#+\s*$`)
)

// reduce is a page's markdown as the prompt holds it: valid UTF-8; images
// and data URLs dropped; a line of links alone (a menu, a list of related
// pages) dropped; every other link reduced to its text, autolinks and
// reference definitions dropped; a heading kept as its text; runs of
// blank lines one; and no page delimiter left.
func reduce(markdown string) string {
	markdown = strings.ReplaceAll(strings.ToValidUTF8(markdown, ""), "\r\n", "\n")
	lines := strings.Split(markdown, "\n")
	out := make([]string, 0, len(lines))
	blank := true // no blank line leads
	for _, line := range lines {
		line, keep := reduceLine(line)
		if !keep {
			continue
		}
		if strings.TrimSpace(line) == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return withoutDelimiters(strings.TrimRight(strings.Join(out, "\n"), "\n"))
}

// reduceLine is one line of a page's markdown as reduce keeps it, and
// whether it keeps it at all: a line that held only images, links or a
// reference definition is gone, not blank.
func reduceLine(line string) (string, bool) {
	if refDefRE.MatchString(line) {
		return "", false
	}
	reduced := dataURLRE.ReplaceAllString(replaceSpans(line, spans(line, true), false), "")
	links := spans(reduced, false)
	linked := len(links) > 0 || autoRE.MatchString(reduced)
	if linked && !hasWords(autoRE.ReplaceAllString(replaceSpans(reduced, links, false), "")) {
		return "", false
	}
	if strings.TrimSpace(reduced) == "" && strings.TrimSpace(line) != "" {
		return "", false
	}
	reduced = autoRE.ReplaceAllString(replaceSpans(reduced, links, true), "")
	if headingRE.MatchString(reduced) {
		reduced = closingRE.ReplaceAllString(headingRE.ReplaceAllString(reduced, ""), "")
	}
	return strings.TrimRightFunc(reduced, unicode.IsSpace), true
}

// hasWords reports whether s holds a letter or a digit: the words of a
// line, once its links are gone.
func hasWords(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
}

// span is a link's or an image's place in a line: s[start:end] the whole
// of it, s[text:textEnd] its text.
type span struct{ start, end, text, textEnd int }

// spans are the links in s, or with image its images, as a regexp's
// ReplaceAllString finds them: the leftmost, then the leftmost after its
// end. A link is "[", a text with no "]", "]", then "(", a URL with no ")"
// and ")", or "[", a reference with no "]" and "]"; no part crosses a
// line's end, and an image is a link after "!". Each part ends at the first
// of its closer or a newline, so the end of a link's text decides whether
// it is one, and where it ends: a start that isn't one rules out every
// start before its text's end, which the next search begins after, and the
// URL's and the reference's closers are found by cursors that never move
// back, so s is read a bounded number of times.
func spans(s string, image bool) []span {
	lead := "["
	if image {
		lead = "!["
	}
	url, ref := closer{s: s, set: ")\n", found: -1}, closer{s: s, set: "]\n", found: -1}
	var out []span
	for i := 0; i < len(s); {
		at := strings.Index(s[i:], lead)
		if at < 0 {
			break
		}
		start := i + at
		open := start + len(lead) // the text's first byte
		shut := stop(s, open, "]\n")
		end := -1
		if shut+1 < len(s) && s[shut] == ']' {
			switch s[shut+1] {
			case '(':
				if k := url.next(shut + 2); k < len(s) && s[k] == ')' {
					end = k + 1
				}
			case '[':
				if k := ref.next(shut + 2); k < len(s) && s[k] == ']' {
					end = k + 1
				}
			}
		}
		if end < 0 {
			i = shut + 1
			continue
		}
		out = append(out, span{start: start, end: end, text: open, textEnd: shut})
		i = end
	}
	return out
}

// closer finds the first byte of set at or after a position in s. Asked
// at positions that never decrease, as spans asks it, it reads each byte
// of s at most once: an answer stands for every position from the one
// asked up to it.
type closer struct {
	s, set string
	asked  int // the last position asked
	found  int // the answer to it, with no byte of set from asked up to it
}

func (f *closer) next(from int) int {
	if from < f.asked || from > f.found {
		f.found = stop(f.s, from, f.set)
	}
	f.asked = from
	return f.found
}

// stop is the index of the first byte of set at or after from in s, else
// len(s).
func stop(s string, from int, set string) int {
	if k := strings.IndexAny(s[from:], set); k >= 0 {
		return from + k
	}
	return len(s)
}

// replaceSpans is s with each of its spans dropped, or reduced to its text
// when keepText is set.
func replaceSpans(s string, spans []span, keepText bool) string {
	if len(spans) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp.start])
		if keepText {
			b.WriteString(s[sp.text:sp.textEnd])
		}
		last = sp.end
	}
	b.WriteString(s[last:])
	return b.String()
}

// The states of reading a page delimiter, `(?i)<\s*/?\s*page\s*>` (\s
// being a space, a tab, a newline, a form feed or a carriage return), in
// the text since its last "<": how much of a delimiter it is.
const (
	delimNone  uint8 = iota // none: no "<", or a byte no delimiter holds there
	delimOpen               // "<" and spaces
	delimSlash              // "<", spaces, "/" and spaces
	delimP                  // then "p", in either case
	delimPA                 // "pa"
	delimPAG                // "pag"
	delimName               // "page" and spaces, which a ">" closes
)

// withoutDelimiters is s with every page delimiter removed, the halves of
// one that removing another joins included, in one pass: the text kept is
// a stack, each byte pushed with the state its reading reached, and a ">"
// that closes a delimiter pops it back to its "<", where the reading
// resumes from the state below. Delimiters never overlap (each runs from
// a "<" to the first ">" after it), so every order of removing them ends in
// the same text, this one's; removing them pass by pass until none is left
// costs a pass per level of nesting, quadratic on a page that nests them.
// The bytes a delimiter holds are ASCII, and no other rune folds to its
// letters, so reading bytes reads it as the regexp does.
func withoutDelimiters(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	out := make([]byte, 0, len(s))
	states := make([]uint8, 0, len(s))
	state := delimNone
	for i := range len(s) {
		c := s[i]
		if c == '>' && state == delimName {
			// The delimiter's own bytes are read back once, as they go.
			open := bytes.LastIndexByte(out, '<')
			out, states = out[:open], states[:open]
			state = delimNone
			if open > 0 {
				state = states[open-1]
			}
			continue
		}
		state = nextDelimState(state, c)
		out = append(out, c)
		states = append(states, state)
	}
	return string(out)
}

// nextDelimState is the state of a delimiter's reading after c.
func nextDelimState(state uint8, c byte) uint8 {
	space := c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
	lower := c | 0x20 // a letter's lower case; no other byte becomes one of p, a, g, e
	switch {
	case c == '<':
		return delimOpen
	case space && (state == delimOpen || state == delimSlash || state == delimName):
		return state
	case c == '/' && state == delimOpen:
		return delimSlash
	case lower == 'p' && (state == delimOpen || state == delimSlash):
		return delimP
	case lower == 'a' && state == delimP:
		return delimPA
	case lower == 'g' && state == delimPA:
		return delimPAG
	case lower == 'e' && state == delimPAG:
		return delimName
	}
	return delimNone
}

// cut is text cut to at most maxPageBytes and tokens at the densest rates:
// after the last whole line that fits, when the lines that fit keep at
// least half of either limit; else after the last rune that fits, so a
// short line followed by one longer than the limits (a heading over a
// paragraph with no break) still sends most of what the limits allow.
func cut(text string, tokens int) string {
	budget := 4 * max(tokens, 0)
	if len(text) <= maxPageBytes && estimateQuarters(text) <= budget {
		return text
	}
	end, quarters := 0, 0
	for end < len(text) {
		next := strings.IndexByte(text[end:], '\n')
		if next < 0 {
			next = len(text) - end
		} else {
			next++
		}
		cost := estimateQuarters(text[end : end+next])
		if end+next > maxPageBytes || quarters+cost > budget {
			break
		}
		end, quarters = end+next, quarters+cost
	}
	if 2*end >= maxPageBytes || 2*quarters >= budget {
		return strings.TrimRight(text[:end], "\n")
	}
	for end < len(text) {
		_, size := utf8.DecodeRuneInString(text[end:])
		cost := estimateQuarters(text[end : end+size])
		if end+size > maxPageBytes || quarters+cost > budget {
			break
		}
		end, quarters = end+size, quarters+cost
	}
	return text[:end]
}
