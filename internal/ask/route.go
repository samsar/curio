package ask

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/samsar/curio/internal/search"
)

// Route is how a question is answered: from the pages its search finds
// (answer), or by one of the fixed plans for questions about the whole
// library, evidence for a claim, how the person's interests changed, and
// everything they saved about a topic.
type Route string

// The routes.
const (
	RouteAnswer    Route = "answer"
	RouteEvidence  Route = "evidence"
	RouteInterests Route = "interests"
	RouteList      Route = "list"
)

// Routes lists every route, each one a request may name as its mode.
func Routes() []Route { return []Route{RouteAnswer, RouteEvidence, RouteInterests, RouteList} }

// checkMode refuses a mode no route is; "" routes automatically.
func checkMode(mode Route) error {
	if mode == "" || slices.Contains(Routes(), mode) {
		return nil
	}
	return fmt.Errorf("%q isn't a route; the routes are: %s", string(mode), joinRoutes(Routes()))
}

// checkInterestsMode refuses what the interests route can't take: it
// counts saves by their saved date alone, from the interests, and reads no
// ranking, so any other filter and an order are refused, naming them.
func checkInterestsMode(f search.FilterRequest, order []string) error {
	if field := nonWindowFilter(f); field != "" {
		return fmt.Errorf("mode interests counts saves by date alone: filters.%s isn't taken", field)
	}
	if order != nil {
		return errors.New("mode interests reads no ranking: rerank_order isn't taken")
	}
	return nil
}

// nonWindowFilter is the first filter f sets other than its saved window,
// as the request names it; "" when it sets none.
func nonWindowFilter(f search.FilterRequest) string {
	for _, l := range []struct {
		field string
		set   bool
	}{
		{"content_type", len(f.ContentType) > 0},
		{"host", len(f.Host) > 0},
		{"source", len(f.Source) > 0},
		{"site", len(f.Site) > 0},
		{"folder", len(f.Folder) > 0},
		{"published_after", f.PublishedAfter != ""},
		{"published_before", f.PublishedBefore != ""},
		{"include_undated", f.IncludeUndated},
	} {
		if l.set {
			return l.field
		}
	}
	return ""
}

// route is the route a request takes: the mode it names, else RouteFor's,
// except that a question RouteFor sends to the interests, asked with what
// the interests can't take (a filter but the saved window, or an order),
// is answered from its search.
func (r Request) route() Route {
	if r.Mode != "" {
		return r.Mode
	}
	to := RouteFor(r.Question)
	if to == RouteInterests && checkInterestsMode(r.Filters, r.RerankOrder) != nil {
		return RouteAnswer
	}
	return to
}

// RouteFor is the route a question takes when its request names none, by
// rules over its words alone, so routing costs no model call and is the
// same every time. The planner's kind is no guide: it calls any factual
// question evidence and any topical one a list. The rules read English,
// and a question in another language takes the answer route. In order:
//
//  1. interests, when every word but the stopwords is a word about the
//     person, their saving, their interests, change, time or quantity, and
//     one at least is about saving or interests ("how have my interests
//     changed", "what was I into two years ago"); or when the person's
//     saving of the library, or interest in a topic, changes over time,
//     whatever the topic ("how has my saving about cooking changed", "in
//     which year did I save the most about travel", "when did I stop
//     bookmarking recipes", "has my interest in kafka grown"); never for
//     a question about one page ("the article I saved") or one asking
//     advice ("how much should I save", "how do I keep my focus");
//  2. evidence, when a claim cue is among them ("is it true that",
//     "evidence", "debunked", "the case that"), or a yes-or-no question
//     checks a claim ("does fasting actually work", "is the rule real",
//     "is Rust faster than C", "does what I saved show that…");
//  3. list, when a cue for everything on a topic is ("everything", "all
//     my", "what have I saved about", "what have I got on", "anything on",
//     "every article I have", "which pages have I saved", "show me the
//     pages I saved about");
//  4. answer.
//
// A word is a maximal run of ASCII letters and digits, lowercased.
func RouteFor(question string) Route {
	words := routeWords(question)
	switch {
	case aboutInterests(words) || interestsOverTime(words):
		return RouteInterests
	case aboutClaim(words):
		return RouteEvidence
	case asksForAll(words):
		return RouteList
	}
	return RouteAnswer
}

// routeWords are question's words: its maximal runs of ASCII letters and
// digits, lowercased; anything else separates them.
func routeWords(question string) []string {
	return strings.FieldsFunc(strings.ToLower(question), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// aboutInterests reports whether words ask about the library as a whole,
// every word in rule 1's lexicons: the first half of rule 1 of RouteFor.
func aboutInterests(words []string) bool {
	if asksAdvice(words) {
		return false
	}
	subject := false
	for _, w := range words {
		switch {
		case routeStopwords[w] || selfWords[w] || changeWords[w] || timeWords[w] || quantityWords[w] ||
			isNumber(w) || isYear(w):
		case savingWords[w] || interestWords[w]:
			subject = true
		default:
			return false
		}
	}
	return subject
}

// interestsOverTime reports whether words ask how the person's saving of
// the library, or their interest in a topic, changed, whatever the topic:
// the second half of rule 1 of RouteFor. Most of rule 1's words have an
// everyday sense that a topic makes plain ("keep my starter alive", "read
// to my toddler", "post on my blog", "my interest rate", "my area's
// zoning", "save on taxes"), so the person must be named and one of these
// hold:
//
//   - saving of the library started or stopped (savingTurned: "stopped
//     bookmarking", "stop saving things about");
//   - the library saved, on a topic or in its things (savesLibrary: "save
//     the most about", "saved mostly programming stuff"), or an interest
//     in a topic (interestIn: "my interest in", "what was I into"), with a
//     word of change ("changed", "grown", "used to", "gone up") or an
//     amount compared over a span ("the most last year", "how much over
//     the years");
//   - the person's library itself, a word of change right after it
//     (myLibraryChanged: "my bookmarking changed", "my library grew").
//
// A question about one page is about what that page says, and one asking
// advice is answered from pages.
func interestsOverTime(words []string) bool {
	if !hasAny(words, selfWords) || aboutOnePage(words) || asksAdvice(words) {
		return false
	}
	switch {
	case savingTurned(words):
		return true
	case savesLibrary(words) || interestIn(words):
		return hasAny(words, trendWords) || hasCue(words, trendCues) ||
			hasAny(words, amountWords) && spansTime(words)
	}
	return myLibraryChanged(words)
}

// aboutOnePage reports whether words name one saved page: a singular word
// for a page after "the", "that", "this" or "one" ("the article I saved").
func aboutOnePage(words []string) bool {
	for i := 1; i < len(words); i++ {
		if onePageNouns[words[i]] && pointers[words[i-1]] {
			return true
		}
	}
	return false
}

// asksAdvice reports whether words ask what the person should do, which
// pages answer, not their saves: "should" or "ought" ("how much should I
// save each month"), or "how" then "do", "can" or "could" then "I" or "we"
// ("how do I keep my focus", "how can we read more"; "how do my interests
// compare" asks no advice).
func asksAdvice(words []string) bool {
	if hasAny(words, adviceWords) {
		return true
	}
	for i := 0; i+2 < len(words); i++ {
		if words[i] == "how" && adviceAuxiliaries[words[i+1]] && subjects[words[i+2]] {
			return true
		}
	}
	return false
}

// savingTurned reports whether words start or stop saving the library: a
// word of starting or stopping right before a word of bookmarking
// ("started bookmarking"), or before a word of saving the library's
// things or a topic ("stopped saving things about", savedLibraryAt).
func savingTurned(words []string) bool {
	for i := 1; i < len(words); i++ {
		if turnWords[words[i-1]] && (bookmarkWords[words[i]] || savedLibraryAt(words, i)) {
			return true
		}
	}
	return false
}

// savesLibrary reports whether words save the library's things or a
// topic: a word of saving or bookmarking that savedLibraryAt holds, or,
// where a word of saving is, a word for the things a library holds before
// "about" and a topic ("how much stuff about knitting did I save").
func savesLibrary(words []string) bool {
	saves := hasAny(words, libraryVerbs)
	for i := range words {
		if savedLibraryAt(words, i) || saves && libraryThings[words[i]] && topicAt(words, i+1) {
			return true
		}
	}
	return false
}

// savedLibraryAt reports whether words[i] is a word of saving or
// bookmarking followed, within three words, by a word for the things a
// library holds ("saved mostly programming stuff", "bookmarked pages"), or
// by "about", or "of" right after it, and a topic ("save the most about
// travel", "my saving of recipes"; "about 20 percent" and "about as much"
// are amounts, the money's sense). "Save on" is the money's idiom ("save
// on taxes"), and names no topic.
func savedLibraryAt(words []string, i int) bool {
	if !libraryVerbs[words[i]] {
		return false
	}
	for j := i + 1; j < min(i+4, len(words)); j++ {
		if libraryThings[words[j]] {
			return true
		}
		if words[j] == "about" || j == i+1 && words[j] == "of" {
			return j+1 < len(words) && !amountAt(words, j+1)
		}
	}
	return false
}

// topicAt reports whether words[i] is "about" before a topic, not an
// amount.
func topicAt(words []string, i int) bool {
	return i+1 < len(words) && words[i] == "about" && !amountAt(words, i+1)
}

// amountAt reports whether words from i name an amount, which "about"
// before it approximates: a number ("about 20 percent", "about 500
// dollars") or a word for one ("about half", "about a third", "about as
// much"), after "a" or "an" or not.
func amountAt(words []string, i int) bool {
	if i < len(words) && (words[i] == "a" || words[i] == "an") {
		i++
	}
	if i >= len(words) {
		return false
	}
	w := words[i]
	return numberWords[w] || amountNouns[w] || strings.Trim(w, "0123456789") == ""
}

// interestIn reports whether words name an interest in a topic:
// "interest", "interests" or "interested" right before "in" ("my interest
// in kafka", never "my interest rate"), or "into" one or two words after
// "I", "we" or a form of "be" ("what was I into", "I've been really
// into", "was I more into", never "my investment into").
func interestIn(words []string) bool {
	for i := 1; i < len(words); i++ {
		switch words[i] {
		case "in":
			if interestNouns[words[i-1]] {
				return true
			}
		case "into":
			if slices.ContainsFunc(words[max(i-2, 0):i], func(w string) bool { return beWords[w] || subjects[w] }) {
				return true
			}
		}
	}
	return false
}

// myLibraryChanged reports whether words say the person's library itself
// changed: the library named (libraryNamedAt), then right after it a word
// of change ("my bookmarking changed", "our library grew", "what I've
// saved shifted", "my reading list used to").
func myLibraryChanged(words []string) bool {
	for i := range words {
		if end := libraryNamedAt(words, i); end > 0 && end < len(words) &&
			(trendWords[words[end]] || hasCue(words[end:min(end+2, len(words))], trendCues)) {
			return true
		}
	}
	return false
}

// libraryNamedAt is the index right after the person's library named from
// words[i], 0 when none starts there: "my" or "our" then a word of
// bookmarking or saving, "library" or "reading list" ("my bookmarking",
// "my saving", never "since I bookmarked it"); or "what", "I" or "we",
// perhaps "have" or "'ve", then a word of saving or bookmarking ("what I
// save", "what I've bookmarked"). "My saving habits" names money's habits
// as often as the library's, so only a word of change right after the
// name counts.
func libraryNamedAt(words []string, i int) int {
	word := func(j int) string {
		if j < len(words) {
			return words[j]
		}
		return ""
	}
	switch {
	case possessives[word(i)] && (bookmarkWords[word(i+1)] || word(i+1) == "saving" || word(i+1) == "library"):
		return i + 2
	case possessives[word(i)] && word(i+1) == "reading" && word(i+2) == "list":
		return i + 3
	case word(i) == "what" && subjects[word(i+1)]:
		j := i + 2
		if word(j) == "have" || word(j) == "ve" {
			j++
		}
		if libraryVerbs[word(j)] {
			return j + 1
		}
	}
	return 0
}

// spansTime reports whether words name a span or a point of time, or a
// span since something ("since the move").
func spansTime(words []string) bool {
	return slices.ContainsFunc(words, func(w string) bool { return timeWords[w] || isYear(w) || w == "since" })
}

// aboutClaim reports whether words check a claim: rule 2 of RouteFor.
func aboutClaim(words []string) bool {
	if hasCue(words, claimCues) {
		return true
	}
	return yesOrNo(words) && (hasAny(words, claimChecks) || hasCue(words, showCues) || compares(words))
}

// yesOrNo reports whether words open as a yes-or-no question: an
// auxiliary first ("is", "does", "were").
func yesOrNo(words []string) bool { return len(words) > 0 && auxiliaries[words[0]] }

// compares reports whether words compare two things: "than" after a
// comparative ("faster than", "safer than") or after "more", "less",
// "better" or "worse" within the three words before it, and not before a
// number ("more than three" is a count, not a claim).
func compares(words []string) bool {
	for i, w := range words {
		if w != "than" || i == 0 || i+1 < len(words) && isNumber(words[i+1]) {
			continue
		}
		if prev := words[i-1]; strings.HasSuffix(prev, "er") && len(prev) > 3 && !notComparatives[prev] {
			return true
		}
		if slices.ContainsFunc(words[max(i-3, 0):i], func(w string) bool { return comparatives[w] }) {
			return true
		}
	}
	return false
}

// asksForAll reports whether words ask for everything saved on a topic:
// rule 3 of RouteFor.
func asksForAll(words []string) bool {
	return hasCue(words, listCues) || allOfMine(words) || whatIHaveOn(words) || pagesAsked(words)
}

// allOfMine reports whether words ask for all or every one of the
// person's saves: "all" or "every" with the person and their saving or
// having ("every article I have", "all the recipes I've saved"), or with a
// word for pages right after it ("every bookmark", "all bookmarks").
func allOfMine(words []string) bool {
	for i, w := range words {
		if !allWords[w] {
			continue
		}
		if i+1 < len(words) && savingNouns[words[i+1]] {
			return true
		}
		if hasAny(words, selfWords) && (hasAny(words, savingWords) || hasAny(words, havingWords)) {
			return true
		}
	}
	return false
}

// whatIHaveOn reports whether words ask what the person has on a topic:
// "what" or "which", "do", "did" or "have", "I", then one or two words of
// having or saving, then "on" or "about" ("what have I got on", "what do
// I have saved on", "what did I save about").
func whatIHaveOn(words []string) bool {
	for i := 0; i+4 < len(words); i++ {
		if words[i] != "what" && words[i] != "which" || !haveAuxiliaries[words[i+1]] || words[i+2] != "i" {
			continue
		}
		for j := i + 3; j < min(i+5, len(words)); j++ {
			if !havingWords[words[j]] && !savingWords[words[j]] {
				break
			}
			if j+1 < len(words) && topicPrepositions[words[j+1]] {
				return true
			}
		}
	}
	return false
}

// pagesAsked reports whether words ask for the person's pages: "what" or
// "which" right before a plural word for pages, the person named ("which
// pages have I saved about"), or a request to show, give or find that
// names such a word ("show me the pages I saved about").
func pagesAsked(words []string) bool {
	for i := 1; i < len(words); i++ {
		if savingNouns[words[i]] && strings.HasSuffix(words[i], "s") && (words[i-1] == "what" ||
			words[i-1] == "which") && hasAny(words, selfWords) {
			return true
		}
	}
	first := slices.IndexFunc(words, func(w string) bool { return !politeWords[w] })
	return first >= 0 && fetchWords[words[first]] && slices.ContainsFunc(words, func(w string) bool {
		return savingNouns[w] && strings.HasSuffix(w, "s")
	})
}

// isYear reports whether w is a year a library's saves fall in, 1990 to
// 2099: "what was I into in 2021".
func isYear(w string) bool {
	if len(w) != 4 {
		return false
	}
	n, err := strconv.Atoi(w)
	return err == nil && n >= 1990 && n <= 2099
}

// isNumber reports whether w is a count a question about the library
// names, a word for one ("three years ago") or digits up to 99 ("the last
// 3 months").
func isNumber(w string) bool {
	if numberWords[w] {
		return true
	}
	n, err := strconv.Atoi(w)
	return err == nil && len(w) <= 2 && n >= 0
}

// hasCue reports whether words hold one of cues, each a run of words in
// order.
func hasCue(words []string, cues [][]string) bool {
	for i := range words {
		for _, cue := range cues {
			if len(cue) <= len(words)-i && slices.Equal(words[i:i+len(cue)], cue) {
				return true
			}
		}
	}
	return false
}

// hasAny reports whether words hold a word of any of sets.
func hasAny(words []string, sets ...map[string]bool) bool {
	return slices.ContainsFunc(words, func(w string) bool {
		return slices.ContainsFunc(sets, func(s map[string]bool) bool { return s[w] })
	})
}

// set is words as a set.
func set(words ...string) map[string]bool {
	out := make(map[string]bool, len(words))
	for _, w := range words {
		out[w] = true
	}
	return out
}

// The lexicons of rule 1, closed lists written from plain English. A
// question all of whose words but the stopwords are in them asks about
// the library as a whole; one word outside them ("rust", "raft") is a
// topic, which only a search answers, unless the question asks how the
// person's saving of it changed.
var (
	// routeStopwords carry no subject: question words, auxiliaries and
	// their negations, articles, pronouns other than the person's,
	// prepositions and connectives.
	routeStopwords = set("a", "an", "the", "what", "which", "who", "whom", "whose", "when", "where", "why", "how",
		"do", "does", "did", "done", "doing", "have", "has", "had", "having", "is", "are", "was", "were", "be",
		"been", "being", "am", "can", "could", "would", "should", "will", "shall", "may", "might", "must", "it",
		"its", "this", "that", "these", "those", "there", "here", "you", "your", "they", "them", "their", "in",
		"on", "at", "of", "for", "to", "from", "by", "with", "about", "over", "during", "since", "than", "and",
		"or", "but", "so", "up", "out", "as", "any", "some", "very", "show", "tell", "give", "list", "s", "t",
		"ve", "m", "d", "ll", "re", "don", "doesn", "didn", "isn", "aren", "wasn", "weren", "haven", "hasn",
		"hadn", "anything", "something", "around", "like", "judging", "based", "if", "not", "no")
	// selfWords are the person asking.
	selfWords = set("i", "me", "my", "mine", "myself", "we", "us", "our", "ours", "ourselves")
	// savingWords are saving, and the things saved.
	savingWords = set("save", "saved", "saves", "saving", "bookmark", "bookmarks", "bookmarked", "bookmarking",
		"page", "pages", "article", "articles", "link", "links", "post", "posts", "read", "reading", "library",
		"collect", "collected", "collecting", "keep", "kept", "stash", "stashed", "clip", "clipped")
	// interestWords are what the person is interested in.
	interestWords = set("interest", "interests", "interested", "into", "topic", "topics", "theme", "themes",
		"subject", "subjects", "area", "areas", "focus", "focused", "hobby", "hobbies", "passion", "passions",
		"obsession", "obsessed", "curious", "curiosity", "habit", "habits", "mind")
	// changeWords are change over time.
	changeWords = set("change", "changed", "changes", "changing", "shift", "shifted", "shifts", "shifting",
		"evolve", "evolved", "evolving", "grow", "grew", "grown", "growing", "drift", "drifted", "trend",
		"trends", "compare", "compared", "different", "differ", "new", "old", "older", "newer", "start",
		"started", "stop", "stopped", "begin", "began", "become", "became", "still", "anymore", "used", "lately",
		"shrink", "shrank", "shrunk", "shrinking", "drop", "dropped", "dropping", "pick", "picked", "quit",
		"rise", "rose", "rising", "decline", "declined", "fade", "faded", "gone", "went", "down")
	// timeWords are spans and points of time; four-digit years are too
	// (isYear).
	timeWords = set("year", "years", "month", "months", "week", "weeks", "day", "days", "decade", "decades",
		"last", "past", "recent", "recently", "ago", "before", "after", "earlier", "later", "now", "today",
		"then", "time", "times", "early", "late", "back", "season", "seasons", "summer", "winter", "spring",
		"autumn", "fall", "quarter", "quarters", "january", "february", "march", "april", "june", "july",
		"august", "september", "october", "november", "december", "timeline", "nowadays", "currently",
		"current", "right", "first", "half", "period", "periods", "ever", "always", "lately")
	// quantityWords are how much, and how often.
	quantityWords = set("many", "much", "most", "more", "less", "fewer", "least", "number", "count", "total",
		"often", "frequently", "lot", "lots", "top", "biggest", "main", "mostly", "all", "each", "every",
		"fastest", "faster", "slowest")
	// numberWords are counts written as words: "three years ago".
	numberWords = set("one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven",
		"twelve", "twenty", "few", "couple", "several", "dozen")
)

// The words of rule 1's second half, which takes a topic: a change in the
// person's saving or interests (trendWords, trendCues), saving started or
// stopped (turnWords right before a word of saving), or an amount compared
// (amountWords) over a span of time; never a question about one page, nor
// one asking advice (adviceWords, and adviceAuxiliaries between "how" and
// the person). Only unambiguous words name the saving there: saving and
// bookmarking (libraryVerbs) of the things a library holds
// (libraryThings) or about a topic, where an amount after "about"
// (amountNouns, numbers) is money's; the person's bookmarks or library
// (bookmarkWords); an interest in a topic (interestNouns before "in",
// "into" after "I", "we" (subjects) or a form of "be", beWords); "my" or
// "our" before the library (possessives).
var (
	trendWords = set("change", "changed", "changes", "changing", "shift", "shifted", "shifts", "shifting",
		"evolve", "evolved", "evolving", "grow", "grew", "grown", "growing", "shrink", "shrank", "shrunk",
		"shrinking", "trend", "trends", "drift", "drifted", "declined", "faded")
	trendCues = [][]string{{"used", "to"}, {"gone", "up"}, {"gone", "down"}, {"go", "up"}, {"go", "down"},
		{"went", "up"}, {"went", "down"}, {"up", "or", "down"}, {"more", "or", "less"}}
	turnWords   = set("stop", "stopped", "start", "started", "began", "begin", "quit", "quitting")
	amountWords = set("most", "more", "less", "fewer", "least", "mostly", "much", "many")
	// onePageNouns are a saved page in the singular, and pointers the words
	// that point at one ("the article", "that post").
	onePageNouns = set("page", "article", "post", "link", "bookmark", "piece", "guide", "video", "thread",
		"essay", "paper", "site", "blog", "tutorial", "writeup")
	pointers          = set("the", "that", "this", "one")
	adviceWords       = set("should", "shouldn", "ought")
	adviceAuxiliaries = set("do", "can", "could")
	bookmarkWords     = set("bookmark", "bookmarks", "bookmarked", "bookmarking")
	libraryVerbs      = set("save", "saved", "saves", "saving", "bookmark", "bookmarks", "bookmarked",
		"bookmarking")
	libraryThings = set("things", "stuff", "pages", "links", "articles", "bookmarks", "posts", "videos",
		"sites", "material", "content")
	amountNouns = set("half", "third", "quarter", "fifth", "tenth", "percent", "hundred", "thousand",
		"million", "lot", "bit", "twice", "double", "triple", "as", "same", "much", "many", "enough")
	interestNouns = set("interest", "interests", "interested")
	possessives   = set("my", "our")
	subjects      = set("i", "we")
	beWords       = set("am", "m", "is", "s", "are", "re", "was", "were", "been", "be", "being")
)

// claimCues mark a claim to find evidence for or against, rule 2 of
// RouteFor.
var claimCues = [][]string{
	{"evidence"}, {"proof"}, {"prove"}, {"proves"}, {"proven"}, {"disprove"}, {"is", "it", "true"},
	{"true", "that"}, {"claim"}, {"claims"}, {"contradict"}, {"contradicts"}, {"debunk"}, {"debunked"},
	{"myth"}, {"for", "and", "against"}, {"arguments", "for"}, {"arguments", "against"}, {"back", "up"},
	{"support", "the", "idea"}, {"support", "the", "claim"}, {"counterargument"}, {"counterarguments"},
	{"the", "case", "for"}, {"the", "case", "against"}, {"the", "case", "that"},
}

// The words of rule 2's yes-or-no questions: the auxiliaries one opens
// with, the words that check what it asks (claimChecks: "does it actually
// work", "is the rule real"), the cues of a claim shown (showCues), and
// the comparatives of a comparison.
var (
	auxiliaries = set("is", "are", "was", "were", "am", "does", "do", "did", "can", "could", "has", "have",
		"had", "will", "would", "should", "isn", "aren", "wasn", "weren", "doesn", "don", "didn")
	claimChecks = set("actually", "really", "real", "true", "false", "myth", "myths", "folklore", "overrated",
		"overhyped", "hype", "legit", "bogus", "scam")
	showCues = [][]string{{"show", "that"}, {"shows", "that"}, {"suggest", "that"}, {"suggests", "that"},
		{"mean", "that"}, {"means", "that"}}
	comparatives    = set("more", "less", "better", "worse")
	notComparatives = set("other", "rather", "whether", "never", "ever", "over", "under", "after", "later",
		"earlier", "either", "neither", "together", "per")
)

// listCues mark a question for everything the library holds on a topic,
// rule 3 of RouteFor.
var listCues = [][]string{
	{"everything"}, {"all", "my"}, {"all", "the", "pages"}, {"what", "have", "i", "saved", "about"},
	{"what", "have", "i", "saved", "on"}, {"what", "do", "i", "have", "about"}, {"what", "do", "i", "have", "on"},
	{"overview", "of"}, {"anything", "on"}, {"anything", "about"}, {"a", "list", "of"},
}

// The words of rule 3's patterns: "all" and "every"; words for pages,
// which a plural makes a request for several (savingNouns); having;
// what a question asks on (topicPrepositions); the auxiliaries of "what
// do I have"; the requests that fetch pages (fetchWords), after any
// politeness (politeWords).
var (
	allWords    = set("all", "every")
	savingNouns = set("page", "pages", "article", "articles", "bookmark", "bookmarks", "link", "links", "post",
		"posts", "video", "videos", "note", "notes", "site", "sites", "resource", "resources")
	havingWords       = set("have", "got", "own")
	topicPrepositions = set("on", "about", "regarding", "re", "related", "concerning")
	haveAuxiliaries   = set("do", "did", "have")
	fetchWords        = set("show", "give", "list", "find", "pull", "get", "fetch", "collect", "gather", "bring",
		"round")
	politeWords = set("can", "could", "would", "will", "you", "please", "just")
)

func joinRoutes(routes []Route) string {
	out := make([]string, len(routes))
	for i, r := range routes {
		out[i] = string(r)
	}
	return strings.Join(out, ", ")
}
