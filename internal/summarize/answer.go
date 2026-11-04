package summarize

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/textutil"
)

// answer is the model's reply as the schema shapes it, before any guard.
type answer struct {
	Kind         store.SummaryKind `json:"kind"`
	Site         string            `json:"site"`
	Author       string            `json:"author"`
	About        string            `json:"about"`
	Entities     []string          `json:"entities"`
	Claims       []string          `json:"claims"`
	Descriptions []string          `json:"descriptions"`
}

// InvalidAnswer is a reply the writer can't use: Rule names the check it
// failed, for logs, tests and the sample's report, and Reason says it for
// a person. Neither quotes the reply, which holds the page's words.
type InvalidAnswer struct {
	Rule   string
	Reason string
}

func (e *InvalidAnswer) Error() string { return "the model's summary couldn't be used: " + e.Reason }

// decodeAnswer reads raw strictly: one JSON object of the schema's shape,
// no key it doesn't have, a kind of the set, and at most the schema's
// items in each list, each text within its cap in bytes and a text
// textutil.TextProblem takes. The schema holds Ollama's decoding to most
// of this; a provider or a model that strays is caught here. A failure is
// an *InvalidAnswer.
func decodeAnswer(raw []byte) (answer, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var a answer
	// The decoder's error can quote the answer (an unknown key), so it is
	// left out.
	if err := dec.Decode(&a); err != nil {
		return answer{}, &InvalidAnswer{"shape", "it isn't a JSON object of a summary's keys and types"}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return answer{}, &InvalidAnswer{"shape", "it holds more than one JSON value"}
	}
	if !a.Kind.Valid() {
		return answer{}, &InvalidAnswer{"kind", "its kind isn't one of the summary kinds"}
	}
	for _, f := range []struct {
		rule  string
		text  string
		chars int
	}{{"site", a.Site, maxSiteChars}, {"author", a.Author, maxAuthorChars}, {"about", a.About, maxAboutChars}} {
		if problem := textutil.TextProblem(f.text, maxBytes(f.chars)); problem != "" {
			return answer{}, &InvalidAnswer{f.rule, "its " + f.rule + " is " + problem}
		}
	}
	for _, l := range []struct {
		rule  string
		items []string
		most  int
		chars int
	}{
		{"entities", a.Entities, maxEntities, maxEntityChars},
		{"claims", a.Claims, maxClaims, maxClaimChars},
		{"descriptions", a.Descriptions, maxDescriptions, maxDescriptionChars},
	} {
		if len(l.items) > l.most {
			return answer{}, &InvalidAnswer{l.rule, fmt.Sprintf("%d %s, at most %d", len(l.items), l.rule, l.most)}
		}
		for _, it := range l.items {
			if problem := textutil.TextProblem(it, maxBytes(l.chars)); problem != "" {
				return answer{}, &InvalidAnswer{l.rule, "its " + l.rule + " have " + problem}
			}
		}
	}
	return a, nil
}

// normalize tidies a decoded answer for the input it was written from:
// texts trimmed, empty items and case-insensitive repeats dropped, and
// entities kept only when they occur in what the model was sent (grounded
// in source), so a name the model made up never becomes a document's
// keyword; dropped counts those left out. A page that isn't the one saved
// keeps no text: what it says is the wall's, not the document's.
func normalize(a answer, in Input) (out answer, dropped int) {
	if a.Kind == store.SummaryKindNotThePage {
		return answer{Kind: a.Kind}, 0
	}
	out = answer{Kind: a.Kind, Site: strings.TrimSpace(a.Site), Author: strings.TrimSpace(a.Author),
		About: strings.TrimSpace(a.About), Claims: distinct(a.Claims), Descriptions: distinct(a.Descriptions)}
	src := source(in)
	for _, e := range distinct(a.Entities) {
		if grounded(src, e) {
			out.Entities = append(out.Entities, e)
			continue
		}
		dropped++
	}
	return out, dropped
}

// source is what an entity is grounded in: everything the model was sent
// of the page (its title, URL, site and text), folded.
func source(in Input) string {
	return fold(strings.Join([]string{in.Title, in.URL, in.Site, in.Text}, "\n"))
}

// grounded reports whether entity occurs in src, a source: compared as
// fold compares, and as a whole word at an edge that is a letter or a
// digit of a script that spaces its words (spacedEdge), so "AI" isn't
// found in "said" nor "Go" in "good"; at any other edge (Chinese,
// Japanese), anywhere, since their words aren't spaced.
func grounded(src, entity string) bool {
	e := fold(entity)
	if e == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(e)
	last, _ := utf8.DecodeLastRuneInString(e)
	for from := 0; ; {
		i := strings.Index(src[from:], e)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(e)
		before, _ := utf8.DecodeLastRuneInString(src[:start])
		after, _ := utf8.DecodeRuneInString(src[end:])
		if (!spacedEdge(first) || !wordRune(before)) && (!spacedEdge(last) || !wordRune(after)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(src[start:])
		from = start + size
	}
}

// spacedEdge reports whether r, an entity's first or last rune, must begin
// or end a word: a digit, or a letter of a script that spaces its words.
func spacedEdge(r rune) bool {
	return unicode.IsDigit(r) || unicode.In(r, unicode.Latin, unicode.Greek, unicode.Cyrillic)
}

// wordRune reports whether r continues a word: a letter or a digit.
// utf8.RuneError, the rune past either end of a text, doesn't.
func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// distinct trims texts and keeps each first one whose fold is new.
func distinct(texts []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if f := fold(t); t != "" && !seen[f] {
			seen[f] = true
			out = append(out, t)
		}
	}
	return out
}

// fold is s compared case, spacing and dashes aside: a name the page
// hyphenates is the same name spelled with a space.
func fold(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.Is(unicode.Pd, r)
	}), " ")
}
