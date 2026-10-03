package insight

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/textutil"
)

// ClusterInfo is the labeler's view of a group: the titles of its most
// representative (most central) member documents, its size, and the names
// it must be told apart from.
type ClusterInfo struct {
	Titles []string
	Size   int
	// Siblings are the names already given in the group's scope: the
	// other interests of its area, every other interest in the flat
	// shape, or the other areas.
	Siblings []string
	// Area is an interest's area's name, when the area has one.
	Area string
	// Children are an area's interests, largest first; set only for an
	// area, which is named from them.
	Children []ChildInfo
	// Taken is a name the labeler gave before that was already taken in
	// the group's scope, for a second try.
	Taken string
}

// ChildInfo is one of an area's interests, as the area's prompt cites it.
type ChildInfo struct {
	Name string
	Size int
}

// Label is a group's human-facing name + one-line summary.
type Label struct {
	Name    string
	Summary string
}

// Labeler turns a group's representative titles into a Label. Implementations
// range from deterministic (term frequency) to generative (an LLM).
type Labeler interface {
	Label(ctx context.Context, info ClusterInfo) (Label, error)
	Name() string
}

// LabelKey is the form two labels are compared in: lowercased, the runs of
// letters and digits joined by one space, so "AI agents" and "AI-Agents!"
// share one. Labels in one scope have distinct keys, and
// quality.DuplicateLabels counts equal keys as exact duplicates.
func LabelKey(name string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// TermLabeler is the deterministic fallback: it names a group after the most
// frequent salient terms across its member titles. Always succeeds (no external
// dependency), so the insight layer works even without a generation model.
type TermLabeler struct {
	maxTerms int
}

// NewTermLabeler constructs a TermLabeler.
func NewTermLabeler() *TermLabeler { return &TermLabeler{maxTerms: 4} }

func (*TermLabeler) Name() string { return "terms" }

// Label implements Labeler; it never fails.
func (l *TermLabeler) Label(_ context.Context, info ClusterInfo) (Label, error) {
	return l.label(info), nil
}

// label names the group after its titles' most frequent salient terms,
// skipping every word a sibling's name uses, so it reads apart from them.
// With nothing left the name is empty.
func (l *TermLabeler) label(info ClusterInfo) Label {
	taken := make(map[string]bool)
	for _, name := range info.Siblings {
		for _, tok := range tokenize(name) {
			taken[tok] = true
		}
	}
	counts := make(map[string]int)
	order := make(map[string]int) // first-seen position, for stable tie-breaking
	seen := 0
	for _, title := range info.Titles {
		for _, tok := range tokenize(title) {
			if utf8.RuneCountInString(tok) < 3 || stopwords[tok] || taken[tok] {
				continue
			}
			if _, ok := counts[tok]; !ok {
				order[tok] = seen
				seen++
			}
			counts[tok]++
		}
	}
	if len(counts) == 0 {
		return Label{}
	}
	type term struct {
		word  string
		count int
	}
	terms := make([]term, 0, len(counts))
	for w, c := range counts {
		terms = append(terms, term{w, c})
	}
	slices.SortFunc(terms, func(a, b term) int {
		if c := cmp.Compare(b.count, a.count); c != 0 {
			return c
		}
		return cmp.Compare(order[a.word], order[b.word]) // earlier-seen first
	})
	if len(terms) > l.maxTerms {
		terms = terms[:l.maxTerms]
	}
	words := make([]string, len(terms))
	for i, t := range terms {
		words[i] = titleCase(t.word)
	}
	return Label{Name: strings.Join(words, " ")}
}

// ErrUnparseableLabel reports a model reply with no usable NAME field. It is
// a per-group failure: the engine term-labels that group but keeps asking
// the model about the others, unlike a transport or HTTP error.
var ErrUnparseableLabel = errors.New("unparseable label reply")

// maxLabelNameWords rejects a NAME that is really a sentence; the prompt asks
// for 2 to 4 words.
const maxLabelNameWords = 6

// maxReplyExcerptRunes bounds how much of a rejected reply goes into the error.
const maxReplyExcerptRunes = 120

// LLMLabeler names a group with a local generation model. On any error the
// engine falls back to the deterministic TermLabeler.
type LLMLabeler struct {
	gen       generator.Generator
	maxTitles int
}

// NewLLMLabeler constructs an LLMLabeler over gen.
func NewLLMLabeler(gen generator.Generator) *LLMLabeler {
	return &LLMLabeler{gen: gen, maxTitles: 12}
}

func (l *LLMLabeler) Name() string { return "llm:" + l.gen.Model() }

const labelSystemPrompt = "You name topic clusters for a personal reading library. " +
	"Be concise and specific. Never invent topics not supported by the titles."

// labelFormat ends every prompt.
const labelFormat = `Respond in exactly this format and nothing else:
NAME: <topic name>
SUMMARY: <one sentence>`

func (l *LLMLabeler) Label(ctx context.Context, info ClusterInfo) (Label, error) {
	prompt := l.interestPrompt(info)
	if info.Children != nil {
		prompt = l.areaPrompt(info)
	}
	out, err := l.gen.Generate(ctx, prompt, generator.Options{
		Temperature: 0.2,
		MaxTokens:   120,
		System:      labelSystemPrompt,
	})
	if err != nil {
		return Label{}, err
	}
	lab, err := parseLabel(out)
	if err != nil {
		return Label{}, fmt.Errorf("llm labeler: %w in reply %q", err,
			textutil.TruncateRunes(out, maxReplyExcerptRunes))
	}
	return lab, nil
}

// interestPrompt asks for an interest's name from its titles, apart from
// its siblings' names, inside its area when the area has a name.
func (l *LLMLabeler) interestPrompt(info ClusterInfo) string {
	var b strings.Builder
	b.WriteString("These document titles were grouped together because they are about a similar topic:\n\n")
	writeList(&b, info.Titles[:min(len(info.Titles), l.maxTitles)])
	if info.Area != "" {
		fmt.Fprintf(&b, "They belong to the broader area %q.", info.Area)
		if len(info.Siblings) > 0 {
			b.WriteString(" Its other topics are already named:\n")
		} else {
			b.WriteString("\n")
		}
	} else if len(info.Siblings) > 0 {
		b.WriteString("Other topics are already named:\n")
	}
	writeList(&b, info.Siblings)
	b.WriteString("Give this group a short topic name (2 to 4 words)")
	if len(info.Siblings) > 0 {
		b.WriteString(" that is different from those names")
	}
	b.WriteString(", and a single-sentence summary of what it's about.\n")
	writeTaken(&b, info.Taken)
	b.WriteString(labelFormat)
	return b.String()
}

// areaPrompt asks for an area's name from its interests' names and sizes
// and their most representative titles, apart from the other areas' names.
func (l *LLMLabeler) areaPrompt(info ClusterInfo) string {
	var b strings.Builder
	b.WriteString("These topics form one broad area of a personal reading library:\n")
	for _, c := range info.Children {
		name := c.Name
		if name == "" {
			name = "an unnamed topic"
		}
		fmt.Fprintf(&b, "- %s (%d documents)\n", name, c.Size)
	}
	b.WriteString("Their most representative titles:\n")
	writeList(&b, info.Titles[:min(len(info.Titles), l.maxTitles)])
	if len(info.Siblings) > 0 {
		fmt.Fprintf(&b, "Other areas are already named: %s.\n", strings.Join(info.Siblings, "; "))
	}
	b.WriteString("Give this area a short name (1 to 4 words) that covers all its topics")
	if len(info.Siblings) > 0 {
		b.WriteString(" and differs from the other areas' names")
	}
	b.WriteString(", and a single-sentence summary.\n")
	writeTaken(&b, info.Taken)
	b.WriteString(labelFormat)
	return b.String()
}

// writeList writes items as a bulleted list, one a line.
func writeList(b *strings.Builder, items []string) {
	for _, it := range items {
		b.WriteString("- ")
		b.WriteString(it)
		b.WriteString("\n")
	}
}

// writeTaken asks again, after a name that was already taken.
func writeTaken(b *strings.Builder, taken string) {
	if taken != "" {
		fmt.Fprintf(b, "The name %q is already taken; choose another.\n", taken)
	}
}

// parseLabel extracts NAME:/SUMMARY: fields from a model response, tolerating
// leading markdown/bullets and ":", "-", "=", en/em-dash separators (small
// local models are inconsistent about the exact format). A reply without an
// explicit NAME field, or whose NAME reads like a sentence, is rejected with
// ErrUnparseableLabel: any other line (a preamble, the summary, a bare name)
// can't be told apart from junk, and a wrong interest name is worse than a
// deterministic term label.
func parseLabel(s string) (Label, error) {
	var lab Label
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "#*->•· \t")
		if v, ok := fieldValue(line, "NAME"); ok {
			lab.Name = cleanLabel(v)
		} else if v, ok := fieldValue(line, "SUMMARY"); ok {
			lab.Summary = cleanLabel(v)
		}
	}
	if lab.Name == "" {
		return Label{}, fmt.Errorf("%w: no NAME field", ErrUnparseableLabel)
	}
	if n := len(strings.Fields(lab.Name)); n > maxLabelNameWords {
		return Label{}, fmt.Errorf("%w: NAME has %d words", ErrUnparseableLabel, n)
	}
	return lab, nil
}

// fieldValue reports whether line begins with field followed by a separator
// (":", "-", "=", en/em-dash), case-insensitively, and returns the trimmed
// value after it.
func fieldValue(line, field string) (string, bool) {
	if len(line) < len(field) || !strings.EqualFold(line[:len(field)], field) {
		return "", false
	}
	rest := strings.TrimLeft(line[len(field):], " \t")
	for _, sep := range []string{":", "-", "=", "–", "—"} {
		if strings.HasPrefix(rest, sep) {
			return strings.TrimSpace(rest[len(sep):]), true
		}
	}
	return "", false
}

// maxLabelRunes caps a parsed label field, so a model that ignores the
// format can't persist a paragraph as an interest name or summary.
const maxLabelRunes = 200

// cleanLabel trims whitespace, surrounding quotes/markdown, and caps length.
func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'*`_")
	s = strings.TrimSpace(s)
	return strings.TrimSpace(textutil.TruncateRunes(s, maxLabelRunes))
}

// tokenize lowercases s and splits it into words of letters and digits in any
// script. Marks count as word characters too, so a decomposed accent or an
// Indic vowel sign doesn't split a word.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	})
}

func titleCase(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToTitle(r)) + s[size:]
}

// stopwords is a small English + web stopword set for term labeling. Kept local
// so the insight package doesn't depend on internal/search.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"this": true, "that": true, "how": true, "why": true, "what": true,
	"your": true, "you": true, "are": true, "was": true, "were": true,
	"has": true, "have": true, "had": true, "not": true, "but": true,
	"can": true, "will": true, "our": true, "out": true, "get": true,
	"guide": true, "introduction": true, "intro": true, "tutorial": true,
	"using": true, "use": true, "part": true, "com": true, "www": true,
	"html": true, "http": true, "https": true, "org": true, "net": true,
	"about": true, "into": true, "over": true, "when": true, "where": true,
	"which": true, "who": true, "its": true, "his": true, "her": true,
}
