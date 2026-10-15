package ui

import (
	"strconv"
	"strings"
	"time"

	"github.com/samsar/curio/internal/planlabel"
)

// Understood is how a search's query was understood, as its page shows it.
// With a plan (Status planned or supplied), each of the plan's filters is a
// chip that searches without it, the documents its dates couldn't judge
// can be kept or left out, and what it searched besides the words is
// said; without one, a note says why the query's words were searched.
type Understood struct {
	// Status is POST /v1/search's understanding.status: planned, supplied,
	// off, cold, checking, unfit, timeout, unavailable, invalid or
	// too_long.
	Status string
	Model  string // the planner's model, when one was asked
	// Timeout is how long a plan may take, which a timeout's note names
	// rather than what understanding took: that counts asking Ollama
	// whether the model is loaded, too.
	Timeout time.Duration
	// Filters are the plan's filters as applied, a dimension each.
	Filters  []PlanFilter
	Rewrites []string
	Names    []string
	// Undated is how many documents the plan's dates couldn't judge, nil
	// without a date filter; UndatedKept whether the plan keeps them
	// (include_undated), and UndatedToken is the plan with that switched.
	Undated      *int
	UndatedKept  bool
	UndatedToken string
}

// PlanFilter is one dimension of a plan's filters, with its values as the
// plan has them, and the token of the plan without it (PlanParam).
type PlanFilter struct {
	// Field names the dimension: content_type, source, site, host, folder,
	// saved or published.
	Field         string
	Values        []string // a list dimension's
	After, Before string   // a window's bounds: a day, or an RFC 3339 time
	Token         string
}

// Chip is a plan's filter as the understood line shows it: what it keeps,
// and the search without it.
type Chip struct {
	Label string
	Href  string
}

// UndatedLine says how many documents the plan's dates couldn't judge and
// what became of them, and leads to the search that does the other.
type UndatedLine struct {
	Text   string // "12 undated left out"
	Action string // "include them"
	Href   string
}

// planned reports whether the results were searched by a plan.
func (u *Understood) planned() bool {
	return u != nil && (u.Status == "planned" || u.Status == "supplied")
}

// UnderstoodLine reports whether the page says what the query was
// understood as: for a plan that adds something to its words.
func (s Search) UnderstoodLine() bool {
	u := s.Understood
	return u.planned() && (len(u.Filters) > 0 || len(u.Rewrites) > 0 || len(u.Names) > 0)
}

// Chips are the plan's filters, each leading to the first page of the
// search without it.
func (s Search) Chips() []Chip {
	if !s.UnderstoodLine() {
		return nil
	}
	out := make([]Chip, 0, len(s.Understood.Filters))
	for _, f := range s.Understood.Filters {
		out = append(out, Chip{Label: planlabel.Filter(f.Field, f.Values, f.After, f.Before, time.Local),
			Href: searchHref(s.Query, s.Type, 1, f.Token)})
	}
	return out
}

// UndatedLine is, for a plan with a date filter that couldn't judge some
// documents, how many and the search that keeps them or leaves them out;
// nil otherwise.
func (s Search) UndatedLine() *UndatedLine {
	u := s.Understood
	if !s.UnderstoodLine() || u.Undated == nil || *u.Undated <= 0 || !hasDateFilter(u.Filters) {
		return nil
	}
	line := &UndatedLine{Text: num(*u.Undated) + " undated left out", Action: "include them",
		Href: searchHref(s.Query, s.Type, 1, u.UndatedToken)}
	if u.UndatedKept {
		line.Text, line.Action = num(*u.Undated)+" undated kept", "leave them out"
	}
	return line
}

func hasDateFilter(filters []PlanFilter) bool {
	for _, f := range filters {
		if f.Field == "saved" || f.Field == "published" {
			return true
		}
	}
	return false
}

// WordsHref is the search of the query's own words, limited to the type
// shown: what the page searched before it was understood.
func (s Search) WordsHref() string { return searchHref(s.Query, s.Type, 1, "") }

// AlsoSearched is the line that says what rewrites the plan searched with
// the words; "" for none.
func (u Understood) AlsoSearched() string {
	if len(u.Rewrites) == 0 {
		return ""
	}
	return "Also searched: " + strings.Join(u.Rewrites, "; ")
}

// LookedFor is the line that says what names the plan looked for among
// what the documents were saved as; "" for none.
func (u Understood) LookedFor() string {
	if len(u.Names) == 0 {
		return ""
	}
	return "Looked for: " + strings.Join(u.Names, ", ")
}

// Note says why a query that asked to be understood was searched by its
// words, from the status alone, never the daemon's reason, and what to do
// when something must be done; "" when it was planned, or understanding
// is off.
func (u Understood) Note() string {
	const words = "Searched your words: "
	model := u.Model
	if model == "" {
		model = "the model that understands questions"
	}
	switch u.Status {
	case "cold":
		return words + "the model that understands questions (" + model + ") is loading; " +
			"press Enter again in a few seconds."
	case "checking":
		return words + "curio is checking " + model + "; press Enter again in a few seconds."
	case "unfit":
		return words + model + " didn't pass curio's check for understanding questions; set " +
			"generation.roles.planner.model to a model that passes, or search.understand: false."
	case "timeout":
		return words + "understanding took longer than " + strconv.FormatFloat(u.Timeout.Seconds(), 'f', -1, 64) +
			" s; curio doctor says what helps when it keeps doing so."
	case "unavailable":
		return words + model + " isn't available; curio doctor says why."
	case "invalid":
		return words + "the model's answer couldn't be used."
	case "too_long":
		return words + "the question is too long to understand; a shorter one can be."
	}
	return ""
}
