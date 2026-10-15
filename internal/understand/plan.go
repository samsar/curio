// Package understand turns a search question into a plan: what kind of
// answer it wants, the filters it states, a few rewrites and the names it
// points at by description. One structured call to the planner role's
// model writes the plan; deterministic code decides what of it to trust:
// a strict decoding, filters grounded in the question's own words, and a
// fitness check the model must pass before any of its plans is used. See
// docs/decisions.md "Understanding the question".
//
// understand reads search's filter shape and never the reverse: search
// stays free of language models, and ranks whatever plan it is given.
package understand

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/samsar/curio/internal/search"
)

// Kind is what a question wants back.
type Kind string

// The kinds, as the prompt defines them.
const (
	KindOne      Kind = "one"      // a single page they remember
	KindList     Kind = "list"     // several pages on a topic or of a sort
	KindEvidence Kind = "evidence" // pages that answer a question or back a claim
	KindAnalysis Kind = "analysis" // a pattern across many pages
	KindKeywords Kind = "keywords" // the question is already search words
)

// Kinds lists every kind, in the prompt's order.
func Kinds() []Kind { return []Kind{KindOne, KindList, KindEvidence, KindAnalysis, KindKeywords} }

// Plan is what a search should do for a question: its kind, the filters
// it states, rewrites searched with it, and names searched in what
// documents were saved as.
type Plan struct {
	Kind     Kind
	Filters  search.FilterRequest
	Rewrites []string
	Names    []string
}

// Status is how a question came to be searched: by a plan, or why by its
// own words. It is a closed set.
type Status string

// The statuses.
const (
	StatusPlanned     Status = "planned"     // the planner's plan
	StatusSupplied    Status = "supplied"    // the client's plan, a page of an understood search
	StatusOff         Status = "off"         // no planner: search.understand is off, or its model can't answer in JSON
	StatusCold        Status = "cold"        // the planner's model is loading
	StatusChecking    Status = "checking"    // the planner's model is being checked
	StatusUnfit       Status = "unfit"       // the planner's model failed the check
	StatusTimeout     Status = "timeout"     // the plan took longer than search.understand_timeout_seconds
	StatusUnavailable Status = "unavailable" // the planner's model couldn't be reached or run
	StatusInvalid     Status = "invalid"     // the model's answer couldn't be used
	StatusTooLong     Status = "too_long"    // the question is longer than the planner takes, and wasn't sent
)

// Statuses lists every status.
func Statuses() []Status {
	return []Status{StatusPlanned, StatusSupplied, StatusOff, StatusCold, StatusChecking, StatusUnfit, StatusTimeout,
		StatusUnavailable, StatusInvalid, StatusTooLong}
}

// Dropped is a filter of a plan left out of the search: Field names its
// dimension (content_type, source, saved, published, ...), Reason why.
type Dropped struct {
	Field  string
	Reason string
}

// Outcome is what understanding a question came to: its status, why there
// is no plan when there is none, the model asked and how long it took, and
// the plan with the filters its guards dropped.
type Outcome struct {
	Status  Status
	Reason  string
	Model   string
	Took    time.Duration
	Plan    *Plan
	Dropped []Dropped
}

// FieldError is a plan Validate refuses: Field names it as the API does
// (plan.kind, plan.filters.saved_after), and Reason says what is wrong.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Reason }

// Validate checks a plan a client supplied, as a page of a search it
// understood or with a filter taken out: a kind of the set, at most
// search.MaxRewrites rewrites and search.MaxNames names, none empty, over
// its cap or holding a control or format character, and filters
// search.FilterRequest.Resolve takes. The client is trusted, so no guard
// reads the question, and every filter dimension is open to it, site,
// folder, host and include_undated included. A refusal is a *FieldError.
func Validate(p Plan) error {
	if !slices.Contains(Kinds(), p.Kind) {
		return &FieldError{"plan.kind", fmt.Sprintf("%q must be one of: %s", p.Kind, joinKinds())}
	}
	if err := checkTexts("plan.rewrites", p.Rewrites, search.MaxRewrites, maxRewriteBytes); err != nil {
		return err
	}
	if err := checkTexts("plan.names", p.Names, search.MaxNames, maxNameBytes); err != nil {
		return err
	}
	if _, err := p.Filters.Resolve(); err != nil {
		var fe *search.FilterError
		if errors.As(err, &fe) {
			return &FieldError{"plan.filters." + fe.Field, fe.Reason}
		}
		return fmt.Errorf("plan.filters: %w", err)
	}
	return nil
}

// checkTexts holds a plan's rewrites or names to most of them, each
// non-empty and a text textProblem takes.
func checkTexts(field string, texts []string, most, maxBytes int) error {
	if len(texts) > most {
		return &FieldError{field, fmt.Sprintf("%d of them, at most %d", len(texts), most)}
	}
	for _, t := range texts {
		if strings.TrimSpace(t) == "" {
			return &FieldError{field, "an empty one"}
		}
		if problem := textProblem(t, maxBytes); problem != "" {
			return &FieldError{field, problem}
		}
	}
	return nil
}

// textProblem says what keeps t from being one of a plan's rewrites or
// names, "" when nothing does: more than maxBytes once trimmed, a control
// character, which no search needs and JSON writes six bytes long, or a
// format character (Unicode's Cf: bidirectional overrides and isolates,
// zero-width spaces, soft hyphens), which no search needs either and which
// can make a chip or the understood line read other than it is. The
// zero-width non-joiner and joiner are words' own letters' business in
// Persian, Hindi and emoji sequences, so they stay. A plan held to these
// is a dashboard ?plan token of bounded size (internal/api's
// maxPlanParam).
func textProblem(t string, maxBytes int) string {
	t = strings.TrimSpace(t)
	switch {
	case len(t) > maxBytes:
		return fmt.Sprintf("one of %d bytes, at most %d", len(t), maxBytes)
	case strings.ContainsFunc(t, unicode.IsControl):
		return "one with a control character"
	case strings.ContainsFunc(t, formatCharacter):
		return "one with a format character"
	}
	return ""
}

// formatCharacter reports whether r is a format character a plan's text
// refuses: Unicode's Cf but the zero-width non-joiner and joiner.
func formatCharacter(r rune) bool {
	return unicode.Is(unicode.Cf, r) && r != zeroWidthNonJoiner && r != zeroWidthJoiner
}

// The zero-width non-joiner and joiner, which shape Persian and Indic
// words and join emoji.
const (
	zeroWidthNonJoiner = '\u200c'
	zeroWidthJoiner    = '\u200d'
)

func joinKinds() string {
	out := make([]string, 0, len(Kinds()))
	for _, k := range Kinds() {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

// answer is the model's reply as the schema shapes it, before any guard.
type answer struct {
	Kind     Kind          `json:"kind"`
	Names    []string      `json:"names"`
	Rewrites []string      `json:"rewrites"`
	Filters  answerFilters `json:"filters"`
}

// answerFilters are the filters the schema lets the model set, every value
// as it wrote it.
type answerFilters struct {
	SavedAfter      string   `json:"saved_after"`
	SavedBefore     string   `json:"saved_before"`
	PublishedAfter  string   `json:"published_after"`
	PublishedBefore string   `json:"published_before"`
	ContentType     []string `json:"content_type"`
	Source          []string `json:"source"`
}

func (f answerFilters) empty() bool { return f.equal(answerFilters{}) }

// equal compares f and o as sets of values, nil and empty alike.
func (f answerFilters) equal(o answerFilters) bool {
	return f.SavedAfter == o.SavedAfter && f.SavedBefore == o.SavedBefore && f.PublishedAfter == o.PublishedAfter &&
		f.PublishedBefore == o.PublishedBefore && sameSet(f.ContentType, o.ContentType) && sameSet(f.Source, o.Source)
}

func sameSet(a, b []string) bool {
	a, b = slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b))
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// invalidAnswer is a reply the planner can't use: rule names the check it
// failed, for logs and tests, and reason says it for a person. Neither
// quotes the reply, which can hold the question's words.
type invalidAnswer struct {
	rule   string
	reason string
}

func (e *invalidAnswer) Error() string { return "the model's answer couldn't be used: " + e.reason }

// decodeAnswer reads raw strictly: one JSON object of the schema's shape,
// no key it doesn't have, a kind of the set, and at most the schema's
// names and rewrites, each a text textProblem takes. The schema holds
// Ollama's decoding to most of this; a provider or a model that strays
// is caught here. A failure is an *invalidAnswer.
func decodeAnswer(raw []byte) (answer, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var a answer
	if err := dec.Decode(&a); err != nil {
		return answer{}, &invalidAnswer{"shape", "it isn't a plan's JSON (" + err.Error() + ")"}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return answer{}, &invalidAnswer{"shape", "it holds more than one JSON value"}
	}
	if !slices.Contains(Kinds(), a.Kind) {
		return answer{}, &invalidAnswer{"kind", "its kind isn't one of: " + joinKinds()}
	}
	for _, l := range []struct {
		rule     string
		texts    []string
		most     int
		maxBytes int
	}{
		{"names", a.Names, search.MaxNames, maxNameBytes},
		{"rewrites", a.Rewrites, search.MaxRewrites, maxRewriteBytes},
	} {
		if len(l.texts) > l.most {
			return answer{}, &invalidAnswer{l.rule, fmt.Sprintf("%d %s, at most %d", len(l.texts), l.rule, l.most)}
		}
		for _, t := range l.texts {
			if problem := textProblem(t, l.maxBytes); problem != "" {
				return answer{}, &invalidAnswer{l.rule, "its " + l.rule + " have " + problem}
			}
		}
	}
	return a, nil
}

// normalize tidies a decoded answer's texts for question: each trimmed,
// the empty ones and the case-insensitive repeats dropped, a rewrite that
// is the question itself (case and spaces aside) dropped, and a keywords
// plan's rewrites dropped, since its question already is search words.
func normalize(a answer, question string) answer {
	a.Names = distinct(a.Names, nil)
	if a.Kind == KindKeywords {
		a.Rewrites = nil
	} else {
		a.Rewrites = distinct(a.Rewrites, []string{fold(question)})
	}
	return a
}

// distinct trims texts and keeps each first one whose fold is new and not
// in skip.
func distinct(texts, skip []string) []string {
	var out []string
	seen := slices.Clone(skip)
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if f := fold(t); t != "" && !slices.Contains(seen, f) {
			seen = append(seen, f)
			out = append(out, t)
		}
	}
	return out
}

// fold is s compared case and spacing aside.
func fold(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
