package understand

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/search"
)

// Grounding: a filter the model sets is kept only when the question's own
// words state its dimension. A weak model sets every filter on every
// question (measured: llama3.2, applied as given, took nDCG@10 from 0.655
// to 0.27); the guards brought it back to 0.648, and cost a strong model
// nothing. They read English words, so a question in another language
// keeps its rewrites and names and loses its filters.
var (
	// timeWords are the words that state a time, besides a year.
	timeWords = wordSet("january february march april may june july august september october november december " +
		"jan feb mar apr jun jul aug sep sept oct nov dec " +
		"today yesterday tonight week weeks weekend month months year years decade ago " +
		"spring summer fall autumn winter recently lately")
	// publishWords make a stated time a publication's.
	publishWords = wordSet("publish published publication written wrote posted released release")
	// typeWords are the words that name each content type the schema has.
	typeWords = map[string]map[string]bool{
		"video":  wordSet("video videos talk talks youtube lecture lectures clip clips watch watched webinar"),
		"repo":   wordSet("repo repos repository repositories github gitlab codebase"),
		"pdf":    wordSet("pdf pdfs"),
		"thread": wordSet("thread threads tweet tweets"),
	}
	// sourceWords name each browser.
	sourceWords = map[string]map[string]bool{
		"chrome":  wordSet("chrome"),
		"safari":  wordSet("safari"),
		"firefox": wordSet("firefox"),
	}
	yearRE = regexp.MustCompile(`^(19|20)[0-9]{2}$`)
	wordRE = regexp.MustCompile(`[a-z0-9]+`)
)

func wordSet(words string) map[string]bool {
	out := map[string]bool{}
	for w := range strings.FieldsSeq(words) {
		out[w] = true
	}
	return out
}

// words are the question's lowercase runs of ASCII letters and digits:
// what the guards read.
type words map[string]bool

func wordsOf(question string) words {
	out := words{}
	for _, w := range wordRE.FindAllString(strings.ToLower(question), -1) {
		out[w] = true
	}
	return out
}

// statesTime reports whether the words state a time: a year 19xx or 20xx,
// or a word of timeWords.
func (w words) statesTime() bool {
	for word := range w {
		if timeWords[word] || yearRE.MatchString(word) {
			return true
		}
	}
	return false
}

// any reports whether the words hold one of set.
func (w words) any(set map[string]bool) bool {
	for word := range w {
		if set[word] {
			return true
		}
	}
	return false
}

// earliestSave is the day no saved window may end on or before, in loc.
func earliestSave(loc *time.Location) time.Time {
	return time.Date(earliestSaveYear, time.January, 1, 0, 0, 0, 0, loc)
}

// groundFilters turns the model's filters for question into the search's,
// as of now in loc: each dimension the question's words don't state is
// dropped, saved days become local midnights (the day the user means
// starts where they are), published days stay days (stored at UTC
// midnight), a saved window starting after tomorrow or ending by 1990 is
// dropped, and each dimension left must resolve alone, a window as a
// pair. What is dropped is listed, with why. IncludeUndated is never set:
// the undated documents flooded a window (measured: temporal nDCG@10 0.932
// without them, 0.546 with).
func groundFilters(f answerFilters, question string, now time.Time, loc *time.Location) (search.FilterRequest,
	[]Dropped) {
	w := wordsOf(question)
	var (
		out     search.FilterRequest
		dropped []Dropped
	)
	drop := func(field, reason string) { dropped = append(dropped, Dropped{field, reason}) }

	if f.SavedAfter != "" || f.SavedBefore != "" {
		after, before, err := savedWindow(f.SavedAfter, f.SavedBefore, w, now, loc)
		if err != nil {
			drop("saved", err.Error())
		} else {
			out.SavedAfter, out.SavedBefore = after, before
		}
	}
	if f.PublishedAfter != "" || f.PublishedBefore != "" {
		switch {
		case !w.statesTime() || !w.any(publishWords):
			drop("published", "the question doesn't ask when the pages were published")
		default:
			if reason := resolves(search.FilterRequest{PublishedAfter: f.PublishedAfter,
				PublishedBefore: f.PublishedBefore}); reason != "" {
				drop("published", reason)
			} else {
				out.PublishedAfter, out.PublishedBefore = f.PublishedAfter, f.PublishedBefore
			}
		}
	}
	var reason string
	out.ContentType, reason = groundList(f.ContentType, w, typeWords, "type",
		func(v []string) search.FilterRequest { return search.FilterRequest{ContentType: v} })
	if reason != "" {
		drop("content_type", reason)
	}
	out.Source, reason = groundList(f.Source, w, sourceWords, "browser",
		func(v []string) search.FilterRequest { return search.FilterRequest{Source: v} })
	if reason != "" {
		drop("source", reason)
	}
	return out, dropped
}

// savedWindow grounds and converts a saved window: stated by the question,
// each day the local midnight it starts at, starting no later than
// tomorrow, ending after 1990, and resolving as a pair.
func savedWindow(afterDay, beforeDay string, w words, now time.Time, loc *time.Location) (string, string, error) {
	if !w.statesTime() {
		return "", "", errors.New("the question names no time: no year, month, season or word like week or ago")
	}
	after, err := localMidnight(afterDay, loc)
	if err != nil {
		return "", "", err
	}
	before, err := localMidnight(beforeDay, loc)
	if err != nil {
		return "", "", err
	}
	y, m, d := now.In(loc).Date()
	if tomorrow := time.Date(y, m, d+1, 0, 0, 0, 0, loc); !after.IsZero() && after.After(tomorrow) {
		return "", "", fmt.Errorf("the window starts after tomorrow, %s", tomorrow.Format(time.DateOnly))
	}
	if floor := earliestSave(loc); !before.IsZero() && !before.After(floor) {
		return "", "", fmt.Errorf("the window ends by %s, before anything was saved", floor.Format(time.DateOnly))
	}
	r := search.FilterRequest{SavedAfter: rfc3339(after), SavedBefore: rfc3339(before)}
	if reason := resolves(r); reason != "" {
		return "", "", errors.New(reason)
	}
	return r.SavedAfter, r.SavedBefore, nil
}

// localMidnight is the start of day in loc: zero for "", an error for a
// day that isn't one.
func localMidnight(day string, loc *time.Location) (time.Time, error) {
	if day == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation(time.DateOnly, day, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a day YYYY-MM-DD", day)
	}
	return t, nil
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// groundList keeps the values of a list dimension the question names a
// word of, in their order, and the reason for the others; the kept values
// must resolve (as the dimension req builds), or the dimension is dropped
// whole. what names a value in the reason ("type", "browser").
func groundList(values []string, w words, lexicon map[string]map[string]bool, what string,
	req func([]string) search.FilterRequest) ([]string, string) {
	var kept, unstated []string
	for _, v := range values {
		if w.any(lexicon[v]) {
			if !slices.Contains(kept, v) {
				kept = append(kept, v)
			}
			continue
		}
		unstated = append(unstated, fmt.Sprintf("%q", v))
	}
	var reasons []string
	if len(unstated) > 0 {
		reasons = append(reasons, fmt.Sprintf("the question doesn't name the %s %s", what, strings.Join(unstated, ", ")))
	}
	if len(kept) > 0 {
		if reason := resolves(req(kept)); reason != "" {
			kept, reasons = nil, append(reasons, reason)
		}
	}
	return kept, strings.Join(reasons, "; ")
}

// resolves is why r doesn't resolve, the FilterError's reason, or "" when
// it does.
func resolves(r search.FilterRequest) string {
	_, err := r.Resolve()
	if err == nil {
		return ""
	}
	if fe, ok := errors.AsType[*search.FilterError](err); ok {
		return fe.Reason
	}
	return err.Error()
}
