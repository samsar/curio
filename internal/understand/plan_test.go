package understand

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/search"
)

// TestDecodeAnswer: an answer is one JSON object of the schema's shape;
// anything else, or more names or rewrites than it allows, or one over its
// cap once trimmed or holding a control or format character, is invalid,
// by the rule it broke.
func TestDecodeAnswer(t *testing.T) {
	long := func(n int) string { return `"` + strings.Repeat("x", n) + `"` }
	for _, tc := range []struct {
		name, raw, rule string
	}{
		{"a plan", `{"kind":"list","names":["Okapi"],"rewrites":["a b"],"filters":{"content_type":["video"]}}`, ""},
		{"keys left out", `{"kind":"one"}`, ""},
		{"a name at its cap once trimmed", `{"kind":"one","names":["  ` + strings.Repeat("x", 100) + `  "]}`, ""},
		{"an unknown key", `{"kind":"one","names":[],"rewrites":[],"filters":{},"site":"example.com"}`, "shape"},
		{"an unknown filter", `{"kind":"one","filters":{"site":["example.com"]}}`, "shape"},
		{"a wrong type", `{"kind":"one","names":"Okapi"}`, "shape"},
		{"not JSON", `kind: one`, "shape"},
		{"two values", `{"kind":"one"} {"kind":"list"}`, "shape"},
		{"a kind outside the set", `{"kind":"summary"}`, "kind"},
		{"no kind", `{"names":[]}`, "kind"},
		{"five names", `{"kind":"one","names":["a","b","c","d","e"]}`, "names"},
		{"four rewrites", `{"kind":"one","rewrites":["a","b","c","d"]}`, "rewrites"},
		{"a name over its cap", `{"kind":"one","names":[` + long(101) + `]}`, "names"},
		{"a rewrite over its cap", `{"kind":"one","rewrites":[` + long(161) + `]}`, "rewrites"},
		{"a name around a line break", `{"kind":"one","names":["\n Okapi\t"]}`, ""},
		{"a name holding a NUL", `{"kind":"one","names":["Oka\u0000pi"]}`, "names"},
		{"a rewrite holding a line break", `{"kind":"one","rewrites":["striped\nhorses"]}`, "rewrites"},
		{"a name holding a right-to-left override", `{"kind":"one","names":["Okapi \u202egnp.exe"]}`, "names"},
		{"a rewrite holding a zero-width space", `{"kind":"one","rewrites":["striped\u200bhorses"]}`, "rewrites"},
		{"a name of letters beyond ASCII", `{"kind":"one","names":["Café Zürich 東京"]}`, ""},
		{"a Persian rewrite with a zero-width non-joiner", `{"kind":"list","rewrites":["کتاب\u200cهای برنامه\u200cنویسی"]}`,
			""},
		{"a Hindi rewrite with a zero-width joiner", `{"kind":"list","rewrites":["क्\u200dष मशीन लर्निंग"]}`, ""},
		{"a name of an emoji sequence", `{"kind":"one","names":["👩\u200d💻 Developer Survey"]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeAnswer([]byte(tc.raw))
			if tc.rule == "" {
				require.NoError(t, err)
				return
			}
			ia, ok := errors.AsType[*invalidAnswer](err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, tc.rule, ia.rule)
		})
	}
}

// TestValidate_JoinersStay: a client's plan whose texts hold the zero-width
// non-joiner or joiner, as Persian words and emoji sequences do, is taken.
func TestValidate_JoinersStay(t *testing.T) {
	require.NoError(t, Validate(Plan{Kind: KindList, Rewrites: []string{"کتاب\u200cهای برنامه\u200cنویسی"},
		Names: []string{"👩\u200d💻 Developer Survey"}}))
}

// TestNormalize: names and rewrites are trimmed, their empties and their
// repeats (case aside) dropped, a rewrite that is the question dropped,
// and a keywords plan keeps no rewrite.
func TestNormalize(t *testing.T) {
	got := normalize(answer{Kind: KindList,
		Names:    []string{" Okapi ", "", "okapi", "Grevy's zebra", "  "},
		Rewrites: []string{"Zebra  STRIPES", "striped horses", "", "Striped Horses", " camouflage "},
	}, "zebra stripes")
	assert.Equal(t, []string{"Okapi", "Grevy's zebra"}, got.Names)
	assert.Equal(t, []string{"striped horses", "camouflage"}, got.Rewrites)

	got = normalize(answer{Kind: KindKeywords, Names: []string{"Okapi"}, Rewrites: []string{"striped horses"}},
		"zebra stripes")
	assert.Equal(t, []string{"Okapi"}, got.Names)
	assert.Empty(t, got.Rewrites)
}

// TestValidate: a client's plan is held to the structure, its filters to
// Resolve, with every dimension open to it; a refusal names the field as
// the API does.
func TestValidate(t *testing.T) {
	ok := Plan{Kind: KindList, Rewrites: []string{"striped horses"}, Names: []string{"Okapi"},
		Filters: search.FilterRequest{Site: []string{"example.com"}, Folder: []string{"/Reading"},
			Host: []string{"www.example.com"}, SavedAfter: "2024-03-01T00:00:00+01:00", IncludeUndated: true}}
	require.NoError(t, Validate(ok))
	require.NoError(t, Validate(Plan{Kind: KindKeywords}))

	for _, tc := range []struct {
		name   string
		change func(*Plan)
		field  string
	}{
		{"a kind outside the set", func(p *Plan) { p.Kind = "summary" }, "plan.kind"},
		{"no kind", func(p *Plan) { p.Kind = "" }, "plan.kind"},
		{"four rewrites", func(p *Plan) { p.Rewrites = []string{"a", "b", "c", "d"} }, "plan.rewrites"},
		{"an empty rewrite", func(p *Plan) { p.Rewrites = []string{" "} }, "plan.rewrites"},
		{"five names", func(p *Plan) { p.Names = []string{"a", "b", "c", "d", "e"} }, "plan.names"},
		{"a name over its cap", func(p *Plan) { p.Names = []string{strings.Repeat("x", 101)} }, "plan.names"},
		{"a name holding an escape", func(p *Plan) { p.Names = []string{"Okapi\x1b[2J"} }, "plan.names"},
		{"a rewrite holding a line break", func(p *Plan) { p.Rewrites = []string{"striped\nhorses"} }, "plan.rewrites"},
		{"a name holding a bidirectional isolate", func(p *Plan) { p.Names = []string{"Okapi\u2066x\u2069"} },
			"plan.names"},
		{"a rewrite holding a soft hyphen", func(p *Plan) { p.Rewrites = []string{"striped\u00adhorses"} },
			"plan.rewrites"},
		{"a name holding a zero-width space", func(p *Plan) { p.Names = []string{"Okapi\u200bGuide"} }, "plan.names"},
		{"a bad date", func(p *Plan) { p.Filters.SavedAfter = "last week" }, "plan.filters.saved_after"},
		{"a bad type", func(p *Plan) { p.Filters.ContentType = []string{"podcast"} }, "plan.filters.content_type"},
		{"undated without a date", func(p *Plan) { p.Filters.SavedAfter = "" }, "plan.filters.include_undated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ok
			tc.change(&p)
			fe, isField := errors.AsType[*FieldError](Validate(p))
			require.True(t, isField)
			assert.Equal(t, tc.field, fe.Field)
			assert.NotEmpty(t, fe.Reason)
		})
	}
}
