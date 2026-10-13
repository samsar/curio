package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/eval"
)

// baselineRun runs the fixture with qs and extra, writing its JSON report
// to dir/name, and returns the report's path.
func baselineRun(t *testing.T, fx *fixture, name, qs string, extra ...string) string {
	t.Helper()
	out := filepath.Join(fx.dir, name)
	res := runEval(t, fx.args(qs, append(extra, "-json", out)...)...)
	require.Equal(t, exitOK, res.code, res.stderr)
	return out
}

// deltaRow matches a group's line of a comparison whose every delta is
// +0.000.
func deltaRow(label string) *regexp.Regexp {
	return regexp.MustCompile(`\n` + regexp.QuoteMeta(label) + `(\s+\+0\.000){8}\n`)
}

// TestBaseline_SameRun: a run compared with one just like it matches its
// queries by index, differs in nothing, and moves nothing; its own report
// names the baseline and holds no comparison, so it can be the next one's.
func TestBaseline_SameRun(t *testing.T) {
	fx := newFixture(t)
	first := baselineRun(t, fx, "first.json", queries, "-variants", "hybrid,vector")
	out := filepath.Join(fx.dir, "second.json")
	res := runEval(t, fx.args(queries, "-variants", "hybrid,vector", "-baseline", first, "-json", out)...)
	require.Equal(t, exitOK, res.code, res.stderr)

	assert.Contains(t, res.stdout, "Baseline: "+first)
	assert.Contains(t, res.stdout, "queries matched by index")
	assert.Contains(t, res.stdout, "The same database, schema, embedding model, query prefix, search settings and curio "+
		"version as the baseline")
	for _, variant := range []string{"hybrid", "vector"} {
		assert.Contains(t, res.stdout, "\nAgainst the baseline's "+variant+":\n")
	}
	for _, label := range []string{"all", "kind topic", "kind keyword", "tag owner", "tag recent"} {
		assert.Len(t, deltaRow(label).FindAllString(res.stdout, -1), 2, "%s, in both variants", label)
	}
	assert.Equal(t, 2, strings.Count(res.stdout, "nDCG@10: none moved by 0.05 or more"))
	assert.NotContains(t, res.stdout, "Variants only")

	rep := readReport(t, out)
	assert.Equal(t, first, rep.Provenance.Baseline)
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "+0.000")
	assert.NotContains(t, string(raw), "Against")
}

// TestBaseline_ExactDeltas: halving the BM25 weight swaps delta's first two
// results, and nothing else. delta's hybrid ranking at weight 1 is 18
// (1/61 + 1/63 = 0.032266) before 16 (2/62 = 0.032258); at 0.5, 16
// (0.5/62 + 1/62 = 0.024194) before 18 (0.5/61 + 1/63 = 0.024070). Every
// other query keeps its order: at 0.5, alpha's 3 and 5 (0.024590, 0.023690),
// beta's 20 and 12 (0.024458, 0.022690) and gamma's 7 (0.022903) still
// lead every vector-only document (1/61 at most). So delta's grade-2
// document 16 moves from 2nd to 1st: its RR from 1/2 to 1, its S@1 from 0
// to 1, its nDCG@10 from (3/l2(3) + 1/l2(11)) / idealDelta to (3 +
// 1/l2(11)) / idealDelta; the groups holding delta (all, of 4 queries, and
// recent, of 2) move by those over their size.
func TestBaseline_ExactDeltas(t *testing.T) {
	fx := newFixture(t)
	first := baselineRun(t, fx, "first.json", queries)
	fx.config = writeConfig(t, fx.dir, fx.ollama.url, "search:\n  bm25_weight: 0.5\n")
	second := baselineRun(t, fx, "second.json", queries, "-baseline", first)

	oldNDCG := (3/l2(3) + 1/l2(11)) / idealDelta
	newNDCG := (3 + 1/l2(11)) / idealDelta
	moved := newNDCG - oldNDCG // 0.268027
	c := compareVariants(readReport(t, second).Variants[0], readReport(t, first).Variants[0], false)
	// R@10, R@100, R@10hi, MRR, nDCG@10, S@1, S@5, S@10.
	wantDeltas := map[string][8]float64{
		"all":          {0, 0, 0, 0.5 / 4, moved / 4, 1.0 / 4, 0, 0},
		"kind topic":   {},
		"kind keyword": {},
		"tag owner":    {},
		"tag recent":   {0, 0, 0, 0.5 / 2, moved / 2, 1.0 / 2, 0, 0},
	}
	require.Len(t, c.groups, len(wantDeltas))
	for _, g := range c.groups {
		want, ok := wantDeltas[g.label]
		require.True(t, ok, g.label)
		require.Empty(t, g.only, g.label)
		for i, d := range g.deltas {
			require.NotNil(t, d, "%s %s", g.label, groupMetrics[i].name)
			assert.InDelta(t, want[i], *d, 1e-9, "%s %s", g.label, groupMetrics[i].name)
		}
	}
	require.Len(t, c.moved, 1)
	assert.Equal(t, "q-delta", c.moved[0].label)
	assert.InDelta(t, oldNDCG, c.moved[0].old, 1e-9)
	assert.InDelta(t, newNDCG, c.moved[0].new, 1e-9)

	res := runEval(t, fx.args(queries, "-baseline", first)...)
	require.Equal(t, exitOK, res.code, res.stderr)
	assert.Contains(t, res.stdout, "Differs from the baseline in: search settings")
	assert.Regexp(t, `\nall +\+0\.000 +\+0\.000 +\+0\.000 +\+0\.125 +\+0\.067 +\+0\.250 +\+0\.000 +\+0\.000\n`, res.stdout)
	assert.Regexp(t, `\ntag recent +\+0\.000 +\+0\.000 +\+0\.000 +\+0\.250 +\+0\.134 +\+0\.500 +\+0\.000 +\+0\.000\n`,
		res.stdout)
	assert.Contains(t, res.stdout, "nDCG@10 moved by 0.05 or more:\n  q-delta  0.528 → 0.796\n")
}

// TestBaseline_MatchedByID: after a query's judgments change, its ids kept,
// the queries are matched by id, and only that query moves: gamma's
// grade-1 judgment 9 (10th) becomes 1 (2nd), lifting its nDCG@10 from
// (3/l2(4) + 1/l2(11)) / idealGamma to (3/l2(4) + 1/l2(3)) / idealGamma.
func TestBaseline_MatchedByID(t *testing.T) {
	fx := newFixture(t)
	first := baselineRun(t, fx, "first.json", queries)
	data, err := os.ReadFile(queries)
	require.NoError(t, err)
	edited := filepath.Join(fx.dir, "edited.yaml")
	require.Equal(t, 1, strings.Count(string(data), docURL(9)))
	require.NoError(t, os.WriteFile(edited, []byte(strings.Replace(string(data), docURL(9), docURL(1), 1)), 0o600))

	oldNDCG := (3/l2(4) + 1/l2(11)) / idealGamma
	newNDCG := (3/l2(4) + 1/l2(3)) / idealGamma
	second := baselineRun(t, fx, "second.json", edited)
	c := compareVariants(readReport(t, second).Variants[0], readReport(t, first).Variants[0], true)
	require.Len(t, c.moved, 1, "only the edited query moves")
	assert.Equal(t, "q-gamma", c.moved[0].label)
	assert.InDelta(t, oldNDCG, c.moved[0].old, 1e-9)
	assert.InDelta(t, newNDCG, c.moved[0].new, 1e-9)

	res := runEval(t, fx.args(edited, "-baseline", first)...)
	require.Equal(t, exitOK, res.code, res.stderr)
	assert.Contains(t, res.stdout, "queries matched by id: the query file changed since the baseline")
	assert.Contains(t, res.stdout, fmt.Sprintf("nDCG@10 moved by 0.05 or more:\n  q-gamma  %.3f → %.3f\n",
		oldNDCG, newNDCG))
}

// TestBaseline_Refused: a baseline whose queries can't be matched, or that
// isn't a report at all, is refused before the copy is opened.
func TestBaseline_Refused(t *testing.T) {
	fx := newFixture(t)
	first := baselineRun(t, fx, "first.json", queries)
	write := func(name, content string) string {
		path := filepath.Join(fx.dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}
	sha := readReport(t, first).Provenance.QueriesSHA256
	for _, tc := range []struct {
		name     string
		qs, base string
		says     []string
	}{
		{"another file without ids", withoutIDs(t, fx.dir), first,
			[]string{"another query file", "sha256 " + sha, "not every query has an id", "q-alpha (the baseline's)"}},
		{"JSON that doesn't parse", queries, write("broken.json", "{not json"), []string{"is not a searcheval report"}},
		{"another document", queries, write("other.json", `{"queries": []}`),
			[]string{"is not a searcheval report of format 1 (it has format 0)"}},
		{"a report without queries", queries, write("empty.json", `{"format": 1, "variants": []}`),
			[]string{"it holds no queries"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A home's own database, which the run would refuse in turn.
			require.NoError(t, os.WriteFile(filepath.Join(fx.dir, "daemon.pid"), []byte("4242\n"), 0o600))
			t.Cleanup(func() { require.NoError(t, os.Remove(filepath.Join(fx.dir, "daemon.pid"))) })
			res := runEval(t, fx.args(tc.qs, "-baseline", tc.base)...)
			assert.Equal(t, exitFailure, res.code)
			assert.Empty(t, res.stdout)
			for _, s := range tc.says {
				assert.Contains(t, res.stderr, s)
			}
			assert.Contains(t, res.stderr, tc.base)
			assert.NotContains(t, res.stderr, "daemon.pid", "refused before the copy is opened")
		})
	}
}

// TestBaseline_VariantsOnOneSide: variants only one run ran are named, and
// those both ran are compared.
func TestBaseline_VariantsOnOneSide(t *testing.T) {
	fx := newFixture(t)
	first := baselineRun(t, fx, "first.json", queries, "-variants", "hybrid,bm25")
	res := runEval(t, fx.args(queries, "-variants", "hybrid,vector", "-baseline", first)...)
	require.Equal(t, exitOK, res.code, res.stderr)
	assert.Contains(t, res.stdout, "Variants only in the baseline: bm25\n")
	assert.Contains(t, res.stdout, "Variants only in this run: vector\n")
	assert.Contains(t, res.stdout, "Against the baseline's hybrid:")
	assert.NotContains(t, res.stdout, "Against the baseline's vector:")
}

// TestMatchQueries_NamesTenIDs: a refusal names at most ten ids found on
// one side only, and how many more there are.
func TestMatchQueries_NamesTenIDs(t *testing.T) {
	base := &report{Provenance: provenance{QueriesSHA256: "aaa"}, Variants: []variantReport{{}}}
	set := &eval.QuerySet{}
	for i := range 8 {
		base.Variants[0].Queries = append(base.Variants[0].Queries, queryReport{ID: fmt.Sprintf("old-%d", i)})
		set.Queries = append(set.Queries, eval.Query{ID: fmt.Sprintf("new-%d", i)})
	}
	_, err := matchQueries(base, queryFile{sha256: "bbb", set: set})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256 aaa; this one is bbb")
	assert.Contains(t, err.Error(), "old-7 (the baseline's), new-0 (this file's), new-1 (this file's) and 6 more")
	assert.NotContains(t, err.Error(), "new-2")

	same := &eval.QuerySet{Queries: []eval.Query{{ID: "new-1"}, {ID: "new-0"}}}
	base.Variants[0].Queries = []queryReport{{ID: "new-0"}, {ID: "new-1"}}
	byID, err := matchQueries(base, queryFile{sha256: "bbb", set: same})
	require.NoError(t, err)
	assert.True(t, byID, "the same ids, in another order")
}

// TestReport_NoComparisonInJSON: the report's JSON has exactly the keys
// a baseline is read back from.
func TestReport_NoComparisonInJSON(t *testing.T) {
	fx := newFixture(t)
	out := baselineRun(t, fx, "r.json", queries)
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &top))
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"format", "generated_at", "provenance", "library", "variants"}, keys)
}

// TestCompareVariants_GroupsOnOneSide: a group only one side has is named
// with its side, never compared, and the groups both have still are.
func TestCompareVariants_GroupsOnOneSide(t *testing.T) {
	group := func(typ eval.GroupType, name string, ndcg float64) groupReport {
		return groupReport{GroupScore: eval.GroupScore{Type: typ, Name: name, NDCGAt10: ndcg}}
	}
	cur := variantReport{Groups: []groupReport{group(eval.GroupAll, "", 0.6), group(eval.GroupKind, "lookup", 0.5)}}
	base := variantReport{Groups: []groupReport{group(eval.GroupAll, "", 0.5), group(eval.GroupKind, "keyword", 0.9)}}
	c := compareVariants(cur, base, false)
	require.Len(t, c.groups, 3)
	assert.Equal(t, "all", c.groups[0].label)
	assert.Empty(t, c.groups[0].only)
	assert.InDelta(t, 0.1, *c.groups[0].deltas[4], 1e-9, "nDCG@10's delta")
	assert.Equal(t, groupDelta{label: "kind lookup", only: "this run"}, c.groups[1])
	assert.Equal(t, groupDelta{label: "kind keyword", only: "the baseline"}, c.groups[2])
}
