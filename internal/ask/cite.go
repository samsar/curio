package ask

import (
	"slices"
	"strconv"
	"strings"
)

// Abstention is the one sentence an answer gives when its sources don't
// answer the question, and nothing else: fixed, and in English whatever
// the question's language, so code can tell an abstention from an answer.
const Abstention = "None of your saved pages answers this."

// Citations is what an answer's citations came to, read after it was
// written: the sources it cites, in the order of their first citation, or
// on the interests and list routes the table rows' and groups' markers it
// cites ("T3", "G1"), likewise; the citations of what it wasn't given, as
// written; whether it is the abstention; and whether it cites nothing
// without abstaining.
type Citations struct {
	Cited    []int
	Markers  []string
	Invalid  []string
	Abstains bool
	Uncited  bool
}

// cite reads answer's citations of k sources (see citationMarks for the
// grammar). A number in [1, k] is a citation of that source; any other
// is an invalid citation, kept as written.
func cite(answer string, k int) Citations {
	var c Citations
	for _, m := range citationMarks(answer, "") {
		switch {
		case m.n >= 1 && m.n <= k:
			if !slices.Contains(c.Cited, m.n) {
				c.Cited = append(c.Cited, m.n)
			}
		case !slices.Contains(c.Invalid, m.text):
			c.Invalid = append(c.Invalid, m.text)
		}
	}
	c.Abstains = abstains(answer)
	c.Uncited = len(c.Cited) == 0 && !c.Abstains
	return c
}

// markerPrefix is the prefix of the citations a route's answer writes: T
// for the interests table's rows, G for the list's groups, none for
// sources.
func markerPrefix(route Route) string {
	switch route {
	case RouteInterests:
		return "T"
	case RouteList:
		return "G"
	case RouteAnswer, RouteEvidence:
	}
	return ""
}

// citeMarkers reads answer's citations of the n markers prefix numbers
// (see citationMarks for the grammar): prefix and a number in [1, n] is a
// citation of that marker; any other number with prefix is an invalid
// citation, kept as written.
func citeMarkers(answer, prefix string, n int) Citations {
	var c Citations
	for _, m := range citationMarks(answer, prefix) {
		switch marker := prefix + strconv.Itoa(m.n); {
		case m.n >= 1 && m.n <= n:
			if !slices.Contains(c.Markers, marker) {
				c.Markers = append(c.Markers, marker)
			}
		case !slices.Contains(c.Invalid, m.text):
			c.Invalid = append(c.Invalid, m.text)
		}
	}
	c.Abstains = abstains(answer)
	c.Uncited = len(c.Markers) == 0 && !c.Abstains
	return c
}

// abstains reports whether answer holds the abstention, compared with
// case, whitespace and the final full stop aside.
func abstains(answer string) bool {
	fold := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	return strings.Contains(fold(answer), strings.TrimSuffix(fold(Abstention), "."))
}

// mark is one number a citation group names: its value, and how it was
// written, prefix and digits ("09" for [09]); a number a range only
// implies is written as its decimal.
type mark struct {
	n    int
	text string
}

// citationMarks are the numbers of every citation group in text whose
// numbers carry prefix (none for a source's), in order, repeats kept,
// ranges expanded. A group is '[', then a list of numbers separated by
// ',' with optional spaces, or a range n-m or n–m with n < m, then ']'.
// A number is prefix and one or two ASCII digits. A group followed by
// '(' is a markdown link's text, and one that begins a line and is
// followed by ':' a link reference's label: neither cites. Anything else
// in brackets ([^1], [123], [3-1], [see 2]) is text.
func citationMarks(text, prefix string) []mark {
	var out []mark
	for i := 0; i < len(text); i++ {
		if text[i] != '[' {
			continue
		}
		end := strings.IndexByte(text[i+1:], ']')
		if end < 0 {
			break
		}
		end += i + 1
		marks, ok := parseGroup(text[i+1:end], prefix)
		if !ok {
			continue
		}
		after := ""
		if end+1 < len(text) {
			after = text[end+1 : end+2]
		}
		lineStart := i == 0 || text[i-1] == '\n'
		if after == "(" || (lineStart && after == ":") {
			i = end
			continue
		}
		out = append(out, marks...)
		i = end
	}
	return out
}

// parseGroup reads the inside of a citation group: a list or a range of
// numbers with prefix.
func parseGroup(inner, prefix string) ([]mark, bool) {
	for _, dash := range []string{"-", "–"} {
		if a, b, ok := strings.Cut(inner, dash); ok {
			lo, okLo := parseNumber(a, prefix)
			hi, okHi := parseNumber(b, prefix)
			if !okLo || !okHi || lo >= hi {
				return nil, false
			}
			out := []mark{{lo, a}}
			for n := lo + 1; n < hi; n++ {
				out = append(out, mark{n, prefix + strconv.Itoa(n)})
			}
			return append(out, mark{hi, b}), true
		}
	}
	var out []mark
	for part := range strings.SplitSeq(inner, ",") {
		part = strings.TrimSpace(part)
		n, ok := parseNumber(part, prefix)
		if !ok {
			return nil, false
		}
		out = append(out, mark{n, part})
	}
	return out, len(out) > 0
}

// parseNumber reads prefix and one or two ASCII digits, nothing else.
func parseNumber(s, prefix string) (int, bool) {
	digits, ok := strings.CutPrefix(s, prefix)
	if !ok || len(digits) < 1 || len(digits) > 2 {
		return 0, false
	}
	n := 0
	for i := range len(digits) {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = 10*n + int(c-'0')
	}
	return n, true
}

// CitationsOf reads answer's citations as an ask on route reads them once
// its answer is written: of n sources on the answer and evidence routes,
// of n table rows on the interests route and of n groups on the list
// route. The developer's evaluation scores answers sentence by sentence
// with it.
func CitationsOf(answer string, route Route, n int) Citations {
	if prefix := markerPrefix(route); prefix != "" {
		return citeMarkers(answer, prefix, n)
	}
	return cite(answer, n)
}
