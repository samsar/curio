package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/eval"
)

// filteredQueries is testdata/queries.yaml with two more queries: beta
// and delta again, filtered to the documents saved since August 2026,
// the failed 22 and the dead 23 alone, which only the document lists can
// return.
func filteredQueries(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(queries)
	require.NoError(t, err)
	extra := `
  - id: q-beta-saved
    query: beta
    filters: {saved_after: 2026-08-01, site: [www.example.com]}
    relevant: [https://example.com/doc/22]

  - id: q-delta-saved
    query: delta
    filters: {saved_after: 2026-08-01}
    relevant: [https://example.com/doc/23]
`
	path := filepath.Join(dir, "filtered.yaml")
	require.NoError(t, os.WriteFile(path, append(data, extra...), 0o600))
	return path
}

// TestRun_Filters: a query's filters reach every variant's search, the
// report records them and marks the query, and the copy's saved dates are
// counted.
func TestRun_Filters(t *testing.T) {
	fx := newFixture(t)
	out := filepath.Join(fx.dir, "report.json")
	res := runEval(t, fx.args(filteredQueries(t, fx.dir), "-json", out, "-variants", "hybrid,bm25,vector,fields,cards")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	assert.Contains(t, res.stdout, "\nq-beta-saved [filtered] ")
	assert.Regexp(t, `\nq-beta +keyword `, res.stdout, "an unfiltered query isn't marked")

	rep := readReport(t, out)
	assert.Equal(t, savedDatesReport{Exact: 2}, rep.Library.SavedDates)
	for _, v := range rep.Variants {
		beta, filtered := v.Queries[1], v.Queries[4]
		require.Equal(t, "q-beta-saved", filtered.ID)
		assert.Nil(t, beta.Filters)
		assert.Equal(t, &eval.Filters{SavedAfter: "2026-08-01", Site: []string{"www.example.com"}}, filtered.Filters)
		assert.NotEmpty(t, beta.Ranking, v.Name)
		for _, q := range v.Queries[4:] {
			for _, r := range q.Ranking {
				assert.True(t, slices.Contains([]string{"doc-22", "doc-23"}, r.DocumentID),
					"%s, %s: %s was saved before the window", v.Name, q.ID, r.DocumentID)
			}
		}
		assert.Contains(t, v.LatencyMS, "undated", v.Name)
	}
	assert.Empty(t, rep.Variants[0].Queries[4].Ranking, "the chunk lists never return a failed page")
	fields := rep.Variants[3]
	require.Equal(t, "fields", fields.Name)
	assert.Equal(t, []rankedReport{{Rank: 1, DocumentID: "doc-22", URL: "https://example.com/doc/22"}},
		fields.Queries[4].Ranking, "the failed page by its bookmark")
}

// TestRun_RefusesABadFilter: a filter search would refuse fails the run
// before anything is searched or embedded, naming the query by its id.
func TestRun_RefusesABadFilter(t *testing.T) {
	fx := newFixture(t)
	path := filepath.Join(fx.dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`queries:
  - id: q-bad
    query: a secret query text
    filters: {site: ["https://example.com"]}
    relevant: [https://example.com/doc/01]
`), 0o600))
	res := runEval(t, fx.args(path)...)
	require.Equal(t, exitFailure, res.code)
	assert.Contains(t, res.stderr, "query q-bad: filters.site: ")
	assert.NotContains(t, res.stderr, "secret")
	assert.Empty(t, fx.ollama.sent(), "nothing was embedded")
}

// TestCompareVariants_Rankings: the comparison counts the matched queries
// that rank the same documents in the same order as the baseline, and
// names the others.
func TestCompareVariants_Rankings(t *testing.T) {
	ranked := func(ids ...string) []rankedReport {
		out := make([]rankedReport, len(ids))
		for i, id := range ids {
			out[i] = rankedReport{Rank: i + 1, DocumentID: id}
		}
		return out
	}
	query := func(i int, id string, ranking []rankedReport) queryReport {
		return queryReport{Index: i, ID: id, Ranking: ranking}
	}
	base := variantReport{Queries: []queryReport{
		query(0, "same", ranked("a", "b")), query(1, "swapped", ranked("a", "b")),
		query(2, "shorter", ranked("a", "b")), query(3, "gone", ranked("a")),
	}}
	cur := variantReport{Queries: []queryReport{
		query(0, "same", ranked("a", "b")), query(1, "swapped", ranked("b", "a")),
		query(2, "shorter", ranked("a")), query(3, "new", ranked("a")),
	}}
	c := compareVariants(cur, base, true)
	assert.Equal(t, 3, c.matched, "new has no baseline")
	assert.Equal(t, []string{"swapped", "shorter"}, c.reorder)

	c = compareVariants(cur, base, false)
	assert.Equal(t, 4, c.matched, "by index")
	assert.Equal(t, []string{"swapped", "shorter"}, c.reorder)

	base.Name, cur.Name = "hybrid", "hybrid"
	var out bytes.Buffer
	(&baseline{report: &report{Variants: []variantReport{base}}, byID: true}).compareVariant(&out, cur)
	assert.Contains(t, out.String(), "\nrankings: 1 of 3 matched queries rank the same documents in the same order "+
		"as the baseline\n  differ: swapped, shorter\n")
}
