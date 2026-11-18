package ask

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCite: the citation grammar, against eight sources.
func TestCite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string
		cited   []int
		invalid []string
	}{
		{"one", "Bread rises [1].", []int{1}, nil},
		{"adjacent", "It rises [1][3].", []int{1, 3}, nil},
		{"a list with a space", "It rises [1, 3].", []int{1, 3}, nil},
		{"a list without", "It rises [1,3].", []int{1, 3}, nil},
		{"a range", "See [1-3].", []int{1, 2, 3}, nil},
		{"a range with an en dash", "See [1–3].", []int{1, 2, 3}, nil},
		{"a backward range is text", "See [3-1].", nil, nil},
		{"out of range", "See [0] and [13].", nil, []string{"0", "13"}},
		{"three digits are text", "See [123].", nil, nil},
		{"a footnote is text", "See [^1].", nil, nil},
		{"a markdown link is no citation", "See [1](https://example.com/x).", nil, nil},
		{"a link reference is no citation", "[1]: https://example.com/\nIt rises [2].", []int{2}, nil},
		{"a reference mid-line cites", "It rises [1]: see there.", []int{1}, nil},
		{"nested brackets", "It rises [[2]].", []int{2}, nil},
		{"a repeat counted once", "It rises [2]. It falls [2][1].", []int{2, 1}, nil},
		{"none", "It rises.", nil, nil},
		{"words in brackets are text", "It rises [see 2].", nil, nil},
		{"an empty group", "It rises [].", nil, nil},
		{"a range past the sources", "See [7-9].", []int{7, 8}, []string{"9"}},
		{"invalid as written", "See [09], [9] and [07-10].", []int{7, 8}, []string{"09", "9", "10"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cite(tc.answer, 8)
			assert.Equal(t, tc.cited, c.Cited)
			assert.Equal(t, tc.invalid, c.Invalid)
			assert.Equal(t, len(tc.cited) == 0, c.Uncited)
			assert.False(t, c.Abstains)
		})
	}
}

// TestCite_Abstains: the abstention is found with case, whitespace and the
// final full stop aside, and an answer that abstains isn't uncited.
func TestCite_Abstains(t *testing.T) {
	for _, answer := range []string{
		Abstention,
		"none of your saved   pages\nanswers this",
		"  NONE OF YOUR SAVED PAGES ANSWERS THIS.  ",
	} {
		c := cite(answer, 8)
		assert.True(t, c.Abstains, "%q", answer)
		assert.False(t, c.Uncited, "%q", answer)
	}
	assert.False(t, cite("None of your saved pages answers it", 8).Abstains)
}

// TestCitationMarks_Prefix: markers of a prefix read as numbers do, and
// other prefixes and plain numbers are text.
func TestCitationMarks_Prefix(t *testing.T) {
	assert.Equal(t, []mark{{1, "A1"}, {3, "A3"}},
		citationMarks("Grew [A1][A3], then [A2, I4], [I1] and [2].", "A"), "a list mixing prefixes is text")
	assert.Equal(t, []mark{{1, "A01"}, {2, "A2"}, {3, "A3"}}, citationMarks("[A01-A3]", "A"))
	assert.Equal(t, []mark{{4, "I4"}}, citationMarks("[I4]", "I"))
}

// TestCitationsOf_Routes: the answer and evidence routes cite sources by
// number, the interests route table rows as [Tn] and the list route groups
// as [Gn]; a marker past those shown, or of another prefix's grammar, is
// invalid as written, and an answer citing no valid marker without
// abstaining is uncited.
func TestCitationsOf_Routes(t *testing.T) {
	answer := "Bread grew [T2][T3], soup faded [T3], see [T0] and [T9]; groups [G1]; sources [2]."
	c := CitationsOf(answer, RouteInterests, 4)
	assert.Equal(t, []string{"T2", "T3"}, c.Markers)
	assert.Equal(t, []string{"T0", "T9"}, c.Invalid)
	assert.Empty(t, c.Cited)
	assert.False(t, c.Uncited)

	c = CitationsOf(answer, RouteList, 4)
	assert.Equal(t, []string{"G1"}, c.Markers)
	assert.Empty(t, c.Invalid)

	for _, route := range []Route{RouteAnswer, RouteEvidence} {
		c = CitationsOf(answer, route, 4)
		assert.Equal(t, []int{2}, c.Cited, route)
		assert.Empty(t, c.Markers, route)
	}

	c = CitationsOf("Nothing cited [7].", RouteInterests, 4)
	assert.True(t, c.Uncited)
	assert.False(t, CitationsOf(Abstention, RouteList, 4).Uncited)
}
