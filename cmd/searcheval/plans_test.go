package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/understand"
)

// plannedQueries is testdata/planned.yaml's path.
var plannedQueries = filepath.Join("testdata", "planned.yaml")

// plannedVariants are every planned variant, after the hybrid.
const plannedVariants = "hybrid,planned,planned-filters,planned-rewrites,planned-names"

// writePlannedConfig writes the planned tests' config.yaml: the planner
// at the fake, BM25 and the fields list alone (nothing embedded at query
// time), rewrites at 0.5 and names at 1.
func writePlannedConfig(t *testing.T, dir, baseURL string) string {
	t.Helper()
	path := filepath.Join(dir, "planned.yaml")
	cfg := fmt.Sprintf("embedding:\n  model: %s\n  dim: %d\n  base_url: %s\n  query_prefix: \"\"\n"+
		"generation:\n  model: %s\n  base_url: %s\n"+
		"search:\n  vector_weight: 0\n  fields_weight: 1\n  cards_weight: 0\n  title_weight: 0\n"+
		"  rewrite_weight: 0.5\n  name_weight: 1\n", testModel, testDim, baseURL, planModel, baseURL)
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o600))
	return path
}

// fitProbes are a fit planner's answers to the fitness check.
var fitProbes = map[string]string{
	"postgres vacuum tuning": `{"kind":"keywords","names":[],"rewrites":[],"filters":{}}`,
	"videos about rust ownership I saved in October 2017": `{"kind":"list","names":[],"rewrites":[],"filters":` +
		`{"saved_after":"2017-10-01","saved_before":"2017-11-01","content_type":["video"]}}`,
	"the paper from Google that introduced the transformer architecture": `{"kind":"one",` +
		`"names":["Attention Is All You Need"],"rewrites":[],"filters":{}}`,
}

// plannedAnswers are the fake planner's answers: a fit model's to the
// probes, and to each query of testdata/planned.yaml the plan its note
// describes; "last week" as of the prompt's today.
func plannedAnswers(question, today string) string {
	if a, ok := fitProbes[question]; ok {
		return a
	}
	switch question {
	case "epsilon":
		return `{"kind":"list","names":[],"rewrites":["alpha"],"filters":{}}`
	case "alpha videos":
		return `{"kind":"list","names":[],"rewrites":[],"filters":{"content_type":["video"]}}`
	case "the archived beta page":
		return `{"kind":"one","names":["Beta archive"],"rewrites":[],"filters":{}}`
	case "zebra saved last week":
		day, err := time.Parse(time.DateOnly, today)
		if err != nil {
			return `{"kind":"one"}`
		}
		return fmt.Sprintf(`{"kind":"list","names":[],"rewrites":[],"filters":{"saved_after":%q,"saved_before":%q}}`,
			day.AddDate(0, 0, -7).Format(time.DateOnly), day.AddDate(0, 0, 1).Format(time.DateOnly))
	}
	return `{"kind":"summary"}` // q-fallback's, and any other: no plan
}

// plannedFixture is the synthetic library with the planner's config, and
// a fake planner that answers plannedAnswers.
func plannedFixture(t *testing.T) *fixture {
	t.Helper()
	fx := newFixture(t)
	fx.ollama.withModels(testModel, planModel)
	fx.ollama.answer = plannedAnswers
	fx.config = writePlannedConfig(t, fx.dir, fx.ollama.url)
	return fx
}

// The planned rankings, worked out from the library (library_test.go) and
// each query's plan, with BM25 and the fields list at weight 1, a rewrite
// at 0.5 and a name at 1, rrf_k 60:
//
// q-rewrite, "epsilon": its words match nothing. The rewrite "alpha"'s
// BM25 ranks 3, 5 at 0.5: 3 (0.5/61), 5 (0.5/62). Judged: 5 (grade 2), 3.
//
// q-filter, "alpha videos": BM25 ranks 3, 5. The plan's video type holds
// no document. Judged: 5 (grade 2).
//
// q-name, "the archived beta page": BM25 ranks 12, 20 (22 failed, left
// out); the fields list finds 22 by its bookmark title, "Beta archive":
// 12 (1/61) and 22 (1/61) tie, in ID order, then 20 (1/62). The name
// "Beta archive" adds 1/61 to 22: 22, 12, 20. Judged: 22 (grade 2), 20.
//
// q-fallback, "beta": the planner's answer is no plan; the words alone, as
// q-name's: 12, 22, 20. Judged: 20 (grade 2), 12.
//
// q-week, "zebra saved last week": nothing matches, and the window holds
// none of the library. Judged: 0.
var (
	// idealTwo is the ideal DCG@10 of a grade-2 and a grade-1 judgment,
	// q-rewrite's, q-name's and q-fallback's.
	idealTwo = 3 + 1/l2(3)

	literalNDCG = []float64{0, (3 / l2(3)) / 3, (3/l2(3) + 1/l2(4)) / idealTwo, (1 + 3/l2(4)) / idealTwo, 0}
	rewriteNDCG = (1 + 3/l2(3)) / idealTwo
	nameNDCG    = (3 + 1/l2(4)) / idealTwo
)

// wantPlanned are each planned variant's nDCG@10 by query: the literal
// search's, but where the part of the plan the variant searches by
// changes it.
var wantPlanned = map[string][]float64{
	"hybrid":           literalNDCG,
	"planned":          {rewriteNDCG, 0, nameNDCG, literalNDCG[3], 0},
	"planned-filters":  {0, 0, literalNDCG[2], literalNDCG[3], 0},
	"planned-rewrites": {rewriteNDCG, literalNDCG[1], literalNDCG[2], literalNDCG[3], 0},
	"planned-names":    {0, literalNDCG[1], nameNDCG, literalNDCG[3], 0},
}

// TestRun_Planned: each planned variant searches by its part of the
// queries' plans, planned once for all four, and scores as worked out; the
// report records the planner, its passed check, each query's plan, status
// and time, the statuses counted, and how the applied filters compare
// with the file's.
func TestRun_Planned(t *testing.T) {
	fx := plannedFixture(t)
	out := filepath.Join(fx.dir, "report.json")
	res := runEval(t, fx.args(plannedQueries, "-json", out, "-variants", plannedVariants, "-today", "2025-04-17")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	rep := readReport(t, out)

	require.NotNil(t, rep.Planner)
	assert.Equal(t, planModel, rep.Planner.Model)
	assert.Equal(t, understand.PromptVersion, rep.Planner.PromptVersion)
	assert.Equal(t, "2025-04-17", rep.Planner.Today)
	assert.Equal(t, "passed", rep.Planner.Check)
	assert.Equal(t, 0.5, rep.Planner.RewriteWeight)
	assert.Equal(t, 1, fx.ollama.loads, "the model loaded once, before the check")
	asked := fx.ollama.questions()
	assert.Len(t, asked, 3+5, "the three probes, then each query once for all four variants")

	require.Len(t, rep.Variants, 5)
	for _, v := range rep.Variants {
		for i, q := range v.Queries {
			assert.InDelta(t, wantPlanned[v.Name][i], q.NDCGAt10, 1e-9, "%s %s nDCG@10", v.Name, q.ID)
		}
		if v.Name == "hybrid" {
			assert.Empty(t, v.PlanStatuses)
			assert.NotContains(t, v.LatencyMS, "plan")
			assert.Nil(t, v.Queries[0].Plan)
			continue
		}
		assert.Equal(t, map[string]int{"planned": 4, "invalid": 1}, v.PlanStatuses, v.Name)
		assert.Contains(t, v.LatencyMS, "plan", v.Name)
		assert.GreaterOrEqual(t, v.LatencyMS["total"].P50, v.LatencyMS["plan"].P50, "%s: the total holds the plan",
			v.Name)
		rewrite := v.Queries[0]
		assert.Equal(t, "planned", rewrite.PlanStatus)
		require.NotNil(t, rewrite.Plan, "%s: the whole plan, whichever part the variant searched by", v.Name)
		assert.Equal(t, []string{"alpha"}, rewrite.Plan.Rewrites)
		require.NotNil(t, rewrite.PlanMS)
		assert.Equal(t, "invalid", v.Queries[3].PlanStatus)
		assert.Nil(t, v.Queries[3].Plan)
	}
	byName := func(name string) variantReport {
		for _, v := range rep.Variants {
			if v.Name == name {
				return v
			}
		}
		t.Fatalf("no variant %s", name)
		return variantReport{}
	}
	// q-filter's type and q-week's window are what the planned search and
	// its filters apply; the rewrites and the names apply neither.
	planned, rewrites := byName("planned"), byName("planned-rewrites")
	assert.Equal(t, map[string]verdict{"content_type": agreeExtra}, planned.Queries[1].FilterAgreement)
	assert.Equal(t, map[string]verdict{"saved": agreeSame}, planned.Queries[4].FilterAgreement)
	assert.Equal(t, map[verdict]int{agreeSame: 1, agreeExtra: 1}, planned.FilterAgreement)
	assert.Equal(t, map[string]verdict{"saved": agreeMissing}, rewrites.Queries[4].FilterAgreement)
	assert.Equal(t, byName("planned-filters").FilterAgreement, planned.FilterAgreement)

	assert.Contains(t, res.stdout, "Planner check: PASSED\n")
	assert.Contains(t, res.stdout, "plans: planned 4, invalid 1\n")
	assert.Contains(t, res.stdout, "filters against the query file's: same 1, narrower 0, wider 0, wrong 0, "+
		"missing 0, extra 1\n  extra: q-filter (content_type)\n")
}

// TestRun_PlannedTextKeepsPlansPrivate: the text report names queries by
// id, and counts: never a query's text, a rewrite, a name or a filter's
// value, which come from the private query set.
func TestRun_PlannedTextKeepsPlansPrivate(t *testing.T) {
	fx := plannedFixture(t)
	res := runEval(t, fx.args(plannedQueries, "-variants", plannedVariants, "-today", "2025-04-17")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	for _, private := range []string{"epsilon", "alpha", "video", "Beta archive", "archived", "zebra", "2025-04-10",
		"2025-04-18"} {
		assert.NotContains(t, res.stdout, private)
		assert.NotContains(t, res.stderr, private, "nor the log")
	}
}

// TestRun_PlannedReplay: a report's plans replayed with -plans rank every
// query the same, asking no model: the plan stage reads 0, and the report
// says where the plans came from.
func TestRun_PlannedReplay(t *testing.T) {
	fx := plannedFixture(t)
	first := filepath.Join(fx.dir, "first.json")
	res := runEval(t, fx.args(plannedQueries, "-json", first, "-variants", plannedVariants, "-today", "2025-04-17")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	asked := len(fx.ollama.questions())

	again := filepath.Join(fx.dir, "again.json")
	res = runEval(t, fx.args(plannedQueries, "-json", again, "-variants", "planned,planned-names", "-plans", first)...)
	require.Equal(t, exitOK, res.code, res.stderr)
	assert.Len(t, fx.ollama.questions(), asked, "a replay asks no model")
	assert.Contains(t, res.stdout, "Plans: replayed from "+first)

	was, rep := readReport(t, first), readReport(t, again)
	assert.Equal(t, first, rep.Provenance.PlansFrom)
	assert.Equal(t, understand.PromptVersion, rep.Provenance.PlansPromptVersion)
	assert.Nil(t, rep.Planner)
	for _, v := range rep.Variants {
		old := was.Variants[slicesIndex(was.Variants, v.Name)]
		for i, q := range v.Queries {
			assert.Equal(t, old.Queries[i].Ranking, q.Ranking, "%s %s", v.Name, q.ID)
			assert.Equal(t, old.Queries[i].PlanStatus, q.PlanStatus)
			assert.Equal(t, 0.0, *q.PlanMS, "replayed, not planned")
		}
		assert.Zero(t, v.LatencyMS["plan"].P95)
	}
}

func slicesIndex(vs []variantReport, name string) int {
	for i, v := range vs {
		if v.Name == name {
			return i
		}
	}
	return -1
}

// TestRun_PlannedToday: -today is the day the planner is told is today:
// a week before another day is another window, which no longer agrees
// with the file's.
func TestRun_PlannedToday(t *testing.T) {
	fx := plannedFixture(t)
	out := filepath.Join(fx.dir, "report.json")
	res := runEval(t, fx.args(plannedQueries, "-json", out, "-variants", "planned", "-today", "2025-04-24")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	rep := readReport(t, out)
	assert.Equal(t, "2025-04-24", rep.Planner.Today)
	week := rep.Variants[0].Queries[4]
	require.NotNil(t, week.Plan)
	after, err := time.Parse(time.RFC3339, week.Plan.Filters.SavedAfter)
	require.NoError(t, err)
	assert.Equal(t, "2025-04-17", after.In(time.Local).Format(time.DateOnly), "a local midnight a week before")
	assert.Equal(t, map[string]verdict{"saved": agreeWrong}, week.FilterAgreement)
}

// TestRun_PlannedCheckFailed: a planner that fails the check is reported
// so, prominently, and measured all the same.
func TestRun_PlannedCheckFailed(t *testing.T) {
	fx := plannedFixture(t)
	fx.ollama.answer = func(question, today string) string {
		if question == "postgres vacuum tuning" {
			return `{"kind":"keywords","names":[],"rewrites":[],"filters":{"source":["chrome"]}}`
		}
		return plannedAnswers(question, today)
	}
	out := filepath.Join(fx.dir, "report.json")
	res := runEval(t, fx.args(plannedQueries, "-json", out, "-variants", "planned", "-today", "2025-04-17")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	assert.Equal(t, "failed:no filter", readReport(t, out).Planner.Check)
	assert.Contains(t, res.stdout, "Planner check: NOT PASSED (failed:no filter): the daemon would search every "+
		"question by its words alone\n")
	assert.Equal(t, map[string]int{"planned": 4, "invalid": 1}, readReport(t, out).Variants[0].PlanStatuses)
}

// TestRun_PlannedRefusals: a planner model Ollama doesn't have is an
// error, never a pull; plans from another query file without ids are
// refused before the copy is opened.
func TestRun_PlannedRefusals(t *testing.T) {
	fx := plannedFixture(t)
	fx.ollama.withModels(testModel)
	res := runEval(t, fx.args(plannedQueries, "-variants", "planned")...)
	assert.Equal(t, exitFailure, res.code)
	assert.Contains(t, res.stderr, "doesn't have the planner's model "+planModel)
	assert.Contains(t, res.stderr, "never pulls")

	other := filepath.Join(fx.dir, "other.yaml")
	require.NoError(t, os.WriteFile(other, []byte("queries:\n  - query: epsilon\n    relevant: [https://example.com/doc/03]\n"),
		0o600))
	plans := filepath.Join(fx.dir, "plans.json")
	fx.ollama.withModels(testModel, planModel)
	res = runEval(t, fx.args(plannedQueries, "-json", plans, "-variants", "planned")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	res = runEval(t, append(fx.args(other, "-variants", "planned", "-plans", plans), "-db", filepath.Join(fx.dir,
		"never-opened.db"))...)
	assert.Equal(t, exitFailure, res.code)
	assert.Contains(t, res.stderr, "can't be matched by id: not every query has an id")
	assert.NoFileExists(t, filepath.Join(fx.dir, "never-opened.db"))

	res = runEval(t, fx.args(plannedQueries, "-variants", "planned", "-plans", filepath.Join("testdata",
		"pr1-report.json"))...)
	assert.Equal(t, exitFailure, res.code)
	assert.Contains(t, res.stderr, "ran no planned variant")
}

// TestRun_PlannedAgainstAnOlderBaseline: a report from before plans, of
// format 1 still, is a baseline for a run with planned variants.
func TestRun_PlannedAgainstAnOlderBaseline(t *testing.T) {
	fx := plannedFixture(t)
	out := filepath.Join(fx.dir, "report.json")
	res := runEval(t, fx.args(queries, "-json", out, "-variants", "hybrid,planned", "-today", "2025-04-17",
		"-baseline", filepath.Join("testdata", "pr1-report.json"))...)
	require.Equal(t, exitOK, res.code, res.stderr)
	assert.Equal(t, reportFormat, readReport(t, out).Format)
	assert.Equal(t, 1, reportFormat)
	assert.Contains(t, res.stdout, "Variants only in this run: planned")
}

// TestDropQueryText: the log loses a query's text and a plan's rewrites
// and names, at the top level, and keeps the rest.
func TestDropQueryText(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: dropQueryText}))
	log.Info("planned", "query", "private words", "rewrites", []string{"secret"}, "names", "Secret Name",
		"query_id", "q-1", "model", planModel)
	assert.NotContains(t, buf.String(), "private")
	assert.NotContains(t, buf.String(), "ecret")
	assert.True(t, strings.Contains(buf.String(), "query_id=q-1") && strings.Contains(buf.String(), planModel))
}
