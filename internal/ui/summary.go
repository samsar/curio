package ui

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// summaryLeadRE matches the stock opening a writing model gives most of
// the descriptions it writes for an area or an interest, which says only
// that a group of documents is about something: "This group focuses on",
// "This collection explores the", "A collection of resources covering",
// "Technical documentation for", "An analysis of". What follows it is the
// description's matter. The opening is read case-insensitively, the
// fillers after it ("the", "various") in lower case, at the start only.
var summaryLeadRE = regexp.MustCompile(`^(?i:` +
	// "This group covers", "These documents explore", "This collection is about".
	`(?:this|these|the) (?:group|collection|set|cluster|area|interest|documents|pages|resources|articles|guides|courses|links|sites|tools|posts)(?: of \w+)? ` +
	`(?:focuses on|focus on|is focused on|are focused on|centers on|centres on|center on|covers|cover|` +
	`explores|explore|examines|examine|consists of|consist of|contains|contain|includes|include|` +
	`brings together|gathers|collects|is about|are about|deals with|deal with|discusses|discuss|` +
	`addresses|address|documents|document|features|feature|highlights|highlight|` +
	`provides|provide|offers|offer|presents|present|analyzes|analyses|analyze|investigates|` +
	`compiles|showcases|spans|span|encompasses|encompass|explains|explain|describes|describe|outlines|outline|` +
	`tracks|track|details|detail|introduces|introduce|surveys|survey|reviews|review)` +
	`|` +
	// "A collection of resources covering", "A set of tools focused on".
	`an? (?:collection|set|selection|compilation|group) of (?:resources|documents|pages|articles|materials|content|links) ` +
	`(?:covering|focused on|focusing on|about|on|for|exploring|examining|related to|regarding|dedicated to|centered on|centred on|discussing|addressing)` +
	`|` +
	// "A collection of tools, data, and insights focused on": the matter
	// starts right after "of".
	`an? (?:collection|set|selection|compilation|group) of` +
	`|` +
	// "Resources covering", "Technical documentation for".
	`(?:resources|documents|articles|materials|content|(?:technical )?documentation) ` +
	`(?:covering|focused on|focusing on|about|on|for|related to|regarding|dedicated to|exploring|examining|discussing)` +
	`|` +
	// "An analysis of", "An overview of", "An exploration of".
	`an? (?:analysis|examination|exploration|overview|survey|look|study|discussion|review|investigation) (?:of|at|into)` +
	`)` +
	// What may follow the opening and says nothing either, in lower case
	// only: "The" in "covers The Beatles' albums" is a name's.
	`(?: (?:the|various|a range of|a variety of|a wide range of|a broad range of|topics (?:like|such as|in|on)|` +
	`(?:the )?(?:topic|subject) of|resources (?:on|about|for)|content (?:on|about)))*` +
	`[\s:,]+`)

// minSummaryRest is the fewest characters a description may keep once its
// opening is gone: anything shorter is shown whole.
const minSummaryRest = 12

// summaryText is an area's or an interest's description as the pages show
// it: without the stock opening a writing model gives most of them
// (summaryLeadRE), what is left starting with a capital. A description
// with no such opening, or with too little after it, is shown as stored.
// The stored description is never changed: this is display only, and the
// interest map, which draws descriptions in its own script, shows them as
// stored.
func summaryText(s string) string {
	s = strings.TrimSpace(s)
	loc := summaryLeadRE.FindStringIndex(s)
	if loc == nil {
		return s
	}
	rest := strings.TrimSpace(s[loc[1]:])
	if utf8.RuneCountInString(rest) < minSummaryRest {
		return s
	}
	return capitalizeFirst(rest)
}

// capitalizeFirst gives s a capital first letter when its first word is
// all lower case ("stock markets" → "Stock markets"); a word that already
// mixes cases, such as "iOS" or "eBPF", is a name and stays as it is.
func capitalizeFirst(s string) string {
	first, size := utf8.DecodeRuneInString(s)
	if !unicode.IsLower(first) {
		return s
	}
	end := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	if end < 0 {
		end = len(s)
	}
	for _, r := range s[:end] {
		if unicode.IsUpper(r) {
			return s
		}
	}
	return string(unicode.ToUpper(first)) + s[size:]
}
