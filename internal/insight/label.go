package insight

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/samsar/curio/internal/generator"
)

// ClusterInfo is the labeler's view of a cluster: the titles of its most
// representative (most central) member documents, and the total size.
type ClusterInfo struct {
	Titles []string
	Size   int
}

// Label is a cluster's human-facing name + one-line summary.
type Label struct {
	Name    string
	Summary string
}

// Labeler turns a cluster's representative titles into a Label. Implementations
// range from deterministic (term frequency) to generative (an LLM).
type Labeler interface {
	Label(ctx context.Context, info ClusterInfo) (Label, error)
	Name() string
}

// TermLabeler is the deterministic fallback: it names a cluster after the most
// frequent salient terms across its member titles. Always succeeds (no external
// dependency), so the insight layer works even without a generation model.
type TermLabeler struct {
	maxTerms int
}

// NewTermLabeler constructs a TermLabeler.
func NewTermLabeler() *TermLabeler { return &TermLabeler{maxTerms: 4} }

func (l *TermLabeler) Name() string { return "terms" }

func (l *TermLabeler) Label(_ context.Context, info ClusterInfo) (Label, error) {
	counts := make(map[string]int)
	order := make(map[string]int) // first-seen position, for stable tie-breaking
	seen := 0
	for _, title := range info.Titles {
		for _, tok := range tokenize(title) {
			if len(tok) < 3 || stopwords[tok] {
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
		return Label{}, nil
	}
	type term struct {
		word  string
		count int
	}
	terms := make([]term, 0, len(counts))
	for w, c := range counts {
		terms = append(terms, term{w, c})
	}
	sort.Slice(terms, func(a, b int) bool {
		if terms[a].count != terms[b].count {
			return terms[a].count > terms[b].count
		}
		return order[terms[a].word] < order[terms[b].word] // earlier-seen first
	})
	if len(terms) > l.maxTerms {
		terms = terms[:l.maxTerms]
	}
	words := make([]string, len(terms))
	for i, t := range terms {
		words[i] = titleCase(t.word)
	}
	return Label{Name: strings.Join(words, " ")}, nil
}

// LLMLabeler names a cluster with a local generation model. On any error the
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

const labelPromptTmpl = `These document titles were grouped together because they are about a similar topic:

%s
Give this cluster a short topic name (2 to 4 words) and a single-sentence summary of what it's about.
Respond in exactly this format and nothing else:
NAME: <topic name>
SUMMARY: <one sentence>`

func (l *LLMLabeler) Label(ctx context.Context, info ClusterInfo) (Label, error) {
	titles := info.Titles
	if len(titles) > l.maxTitles {
		titles = titles[:l.maxTitles]
	}
	var b strings.Builder
	for _, t := range titles {
		b.WriteString("- ")
		b.WriteString(t)
		b.WriteString("\n")
	}
	out, err := l.gen.Generate(ctx, fmt.Sprintf(labelPromptTmpl, b.String()), generator.Options{
		Temperature: 0.2,
		MaxTokens:   120,
		System:      labelSystemPrompt,
	})
	if err != nil {
		return Label{}, err
	}
	lab := parseLabel(out)
	if lab.Name == "" {
		return Label{}, fmt.Errorf("llm labeler: empty name in response %q", out)
	}
	return lab, nil
}

// parseLabel extracts NAME:/SUMMARY: fields from a model response, tolerating
// leading markdown/bullets and ":", "-", "=", en/em-dash separators (small
// local models are inconsistent about the exact format). When there's no NAME
// field it falls back to the first non-empty line, but rejects sentence-like
// fallbacks — a preamble the model emitted instead of a name — so the engine
// drops to deterministic term labels rather than persisting junk.
func parseLabel(s string) Label {
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
		for line := range strings.SplitSeq(s, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				lab.Name = cleanLabel(line)
				break
			}
		}
	}
	// A real topic name is a few words (the prompt asks for 2-4); a long
	// fallback is almost always a preamble ("Sure, here is the topic ...").
	// Drop it so the caller uses the term labeler instead.
	if len(strings.Fields(lab.Name)) > 6 {
		lab.Name = ""
	}
	return lab
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

// cleanLabel trims whitespace, surrounding quotes/markdown, and caps length.
func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'*`_")
	s = strings.TrimSpace(s)
	const max = 200
	if len(s) > max {
		s = strings.TrimSpace(s[:max])
	}
	return s
}

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		// Split on any non-alphanumeric rune.
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
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
