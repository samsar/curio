package quality

import (
	"slices"
	"strings"
	"unicode"
)

// NearLabelJaccard is the token-set Jaccard index at or above which two
// different labels count as near-duplicates.
const NearLabelJaccard = 0.5

// LabelDuplicates counts labels that repeat within one run.
type LabelDuplicates struct {
	// Exact counts pairs of interests whose labels are equal ignoring case,
	// punctuation and spacing.
	Exact int `json:"exact_pairs"`
	// Near counts the other pairs whose label tokens overlap by at least
	// NearLabelJaccard (Jaccard over lowercased words without stop words, a
	// plural "s" dropped: "AWS Lambda Functions" and "AWS Lambda" share 2
	// of 3).
	Near int `json:"near_pairs"`
	// Interests is how many interests are in at least one such pair.
	Interests int `json:"interests"`
	// Pairs lists them, as label pairs.
	Pairs [][2]string `json:"pairs,omitempty"`
}

// DuplicateLabels finds exact and near-duplicate pairs among labels (one per
// interest; an empty label is skipped).
func DuplicateLabels(labels []string) LabelDuplicates {
	var d LabelDuplicates
	norm := make([]string, len(labels))
	toks := make([][]string, len(labels))
	for i, l := range labels {
		toks[i] = labelTokens(l)
		norm[i] = strings.Join(strings.FieldsFunc(strings.ToLower(l), notWord), " ")
	}
	in := make([]bool, len(labels))
	for i := range labels {
		if norm[i] == "" {
			continue
		}
		for j := i + 1; j < len(labels); j++ {
			if norm[j] == "" {
				continue
			}
			switch {
			case norm[i] == norm[j]:
				d.Exact++
			case jaccard(toks[i], toks[j]) >= NearLabelJaccard:
				d.Near++
			default:
				continue
			}
			in[i], in[j] = true, true
			d.Pairs = append(d.Pairs, [2]string{labels[i], labels[j]})
		}
	}
	for _, b := range in {
		if b {
			d.Interests++
		}
	}
	return d
}

func notWord(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

var labelStopwords = map[string]bool{
	"and": true, "the": true, "of": true, "for": true, "in": true, "on": true,
	"to": true, "a": true, "an": true, "with": true, "vs": true, "by": true,
}

// labelTokens returns the distinct content words of a label, sorted.
func labelTokens(label string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(label), notWord) {
		if labelStopwords[w] {
			continue
		}
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			w = strings.TrimSuffix(w, "s")
		}
		out = append(out, w)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for _, w := range a {
		if _, ok := slices.BinarySearch(b, w); ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}
