package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/eval"
	"github.com/samsar/curio/internal/store"
)

// l2 is log2, the discount nDCG divides rank r's gain by as l2(r+1).
var l2 = math.Log2

// The ideal DCG@10 of each query of testdata/queries.yaml: its judgments'
// gains (3 for grade 2, 1 for grade 1) sorted, missing and unsearchable
// ones included, each over l2(rank+1).
var (
	idealAlpha = 3 + 1/l2(3) + 1/l2(4)           // grades 2, 1, 1
	idealBeta  = 3 + 3/l2(3) + 1/l2(4) + 1/l2(5) // grades 2, 2, 1, 1
	idealGamma = 3 + 1/l2(3)                     // grades 2, 1
	idealDelta = 3 + 1/l2(3) + 1/l2(4)           // grades 2, 1, 1
)

// want are the scores a query's ranking should get.
type want struct {
	r10, r100 float64
	hi        *float64 // recall@10 of the grade-2 judgments; nil without one
	ndcg      float64
	rr        float64
	s1, s5    float64
	s10       float64
	first     int
}

// The rankings, worked out from the library's construction (library_test.go).
// A query's vector ranks the searchable documents (0 to 20) by their distance
// on the line; its keyword ranks the documents holding it, and the hybrid
// fuses the two by RRF, 1/(60+rank) each.
//
// alpha, at 3.25. Vector: 3, 4, 2, 5, 1, 6, 0, 7, ... BM25: 3, 5.
// Hybrid: 3 (2/61), 5 (1/62 + 1/64 = 0.031754), then the vector order, 4
// first (1/62). Judged: 5 (grade 2), 4, and a URL the copy lacks.
//
// beta, at 20.25. Vector: 20, 19, 18, ..., 13 (8th), 12 (9th). BM25: 12, 20
// (22 failed, left out). Hybrid: 20 (1/62 + 1/61 = 0.032522), 12 (1/61 +
// 1/69 = 0.030886), 19, 18, 17, 16, 15, 14, 13 (9th). Judged: 20 and the
// failed 22 at grade 2, the chunkless 21 and 13 at grade 1.
//
// gamma, at 0.75. Vector: 1, 0, 2, 3, 4, 5, 6, 7, 8, 9 (10th). BM25: 7.
// Hybrid: 7 (1/61 + 1/68), then the vector order without it: 1, 0 (3rd),
// ..., 9 (10th). Judged: 0 (grade 2) and 9.
//
// delta, at 16.6. Vector: 17, 16, 18, 15, 19, 14, 20, 13, 12, 11 (10th).
// BM25: 18 (twice), 16. Hybrid: 18 (1/61 + 1/63 = 0.032266), 16 (2/62 =
// 0.032258), 17, 15, 19, 14, 20, 13, 12, 11 (10th). Judged: 16 (grade 2),
// the dead 23, and 11.
var wants = map[string][]want{
	"hybrid": {
		// alpha: 5 at 2, 4 at 3; the missing URL never.
		{r10: 2.0 / 3, r100: 2.0 / 3, hi: new(1.0), ndcg: (3/l2(3) + 1/l2(4)) / idealAlpha, rr: 0.5, s5: 1, s10: 1, first: 2},
		// beta: 20 at 1, 13 at 9; 22 and 21 never.
		{r10: 0.5, r100: 0.5, hi: new(0.5), ndcg: (3/l2(2) + 1/l2(10)) / idealBeta, rr: 1, s1: 1, s5: 1, s10: 1, first: 1},
		// gamma: 0 at 3, 9 at 10.
		{r10: 1, r100: 1, hi: new(1.0), ndcg: (3/l2(4) + 1/l2(11)) / idealGamma, rr: 1.0 / 3, s5: 1, s10: 1, first: 3},
		// delta: 16 at 2, 11 at 10; 23 never.
		{r10: 2.0 / 3, r100: 2.0 / 3, hi: new(1.0), ndcg: (3/l2(3) + 1/l2(11)) / idealDelta, rr: 0.5, s5: 1, s10: 1, first: 2},
	},
	// BM25 alone ranks the keyword's documents and nothing else.
	"bm25": {
		// alpha: 3, 5: 5 at 2.
		{r10: 1.0 / 3, r100: 1.0 / 3, hi: new(1.0), ndcg: (3 / l2(3)) / idealAlpha, rr: 0.5, s5: 1, s10: 1, first: 2},
		// beta: 12, 20: 20 at 2.
		{r10: 0.25, r100: 0.25, hi: new(0.5), ndcg: (3 / l2(3)) / idealBeta, rr: 0.5, s5: 1, s10: 1, first: 2},
		// gamma: 7 alone, judged by no one.
		{hi: new(0.0)},
		// delta: 18, 16: 16 at 2.
		{r10: 1.0 / 3, r100: 1.0 / 3, hi: new(1.0), ndcg: (3 / l2(3)) / idealDelta, rr: 0.5, s5: 1, s10: 1, first: 2},
	},
	// The vector leg alone ranks every searchable document by distance.
	"vector": {
		// alpha: 4 at 2, 5 at 4.
		{r10: 2.0 / 3, r100: 2.0 / 3, hi: new(1.0), ndcg: (1/l2(3) + 3/l2(5)) / idealAlpha, rr: 0.5, s5: 1, s10: 1, first: 2},
		// beta: 20 at 1, 13 at 8.
		{r10: 0.5, r100: 0.5, hi: new(0.5), ndcg: (3/l2(2) + 1/l2(9)) / idealBeta, rr: 1, s1: 1, s5: 1, s10: 1, first: 1},
		// gamma: 0 at 2, 9 at 10.
		{r10: 1, r100: 1, hi: new(1.0), ndcg: (3/l2(3) + 1/l2(11)) / idealGamma, rr: 0.5, s5: 1, s10: 1, first: 2},
		// delta: 16 at 2, 11 at 10.
		{r10: 2.0 / 3, r100: 2.0 / 3, hi: new(1.0), ndcg: (3/l2(3) + 1/l2(11)) / idealDelta, rr: 0.5, s5: 1, s10: 1, first: 2},
	},
}

// Each query's judgments search can't return: alpha's URL the copy lacks;
// beta's failed 22 and chunkless 21; delta's dead 23.
var (
	wantMissing      = []int{1, 0, 0, 0}
	wantUnsearchable = []int{0, 2, 0, 1}
)

// The groups of testdata/queries.yaml, by query index: alpha and gamma
// are topic, beta keyword, delta has no kind; alpha and beta are tagged
// owner, beta and delta recent.
var wantGroups = []struct {
	typ                   eval.GroupType
	name                  string
	queries               []int
	missing, unsearchable int
}{
	{eval.GroupAll, "", []int{0, 1, 2, 3}, 1, 3},
	{eval.GroupKind, "topic", []int{0, 2}, 1, 0},
	{eval.GroupKind, "keyword", []int{1}, 0, 2},
	{eval.GroupTag, "owner", []int{0, 1}, 1, 2},
	{eval.GroupTag, "recent", []int{1, 3}, 0, 3},
}

// mean is the mean of of over the queries.
func mean(ws []want, queries []int, of func(want) float64) float64 {
	var sum float64
	for _, i := range queries {
		sum += of(ws[i])
	}
	return sum / float64(len(queries))
}

// TestRun_ScoresTheLibrary: every variant's per-query and per-group scores
// are the hand-derived ones, and so are the counts of judgments search
// can't return.
func TestRun_ScoresTheLibrary(t *testing.T) {
	fx := newFixture(t)
	out := filepath.Join(fx.dir, "report.json")
	res := runEval(t, fx.args(queries, "-json", out, "-variants", "hybrid,bm25,vector")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	rep := readReport(t, out)

	assert.Equal(t, libraryReport{Judgments: 12, Missing: 1, Unsearchable: 3}, rep.Library)
	require.Len(t, rep.Variants, 3)
	for _, v := range rep.Variants {
		ws := wants[v.Name]
		require.Len(t, v.Queries, 4, v.Name)
		for i, q := range v.Queries {
			w := ws[i]
			assert.InDelta(t, w.r10, q.RecallAt10, 1e-9, "%s %s R@10", v.Name, q.ID)
			assert.InDelta(t, w.r100, q.RecallAt100, 1e-9, "%s %s R@100", v.Name, q.ID)
			require.NotNil(t, q.HighRecallAt10, "%s %s", v.Name, q.ID)
			assert.InDelta(t, *w.hi, *q.HighRecallAt10, 1e-9, "%s %s R@10hi", v.Name, q.ID)
			assert.InDelta(t, w.ndcg, q.NDCGAt10, 1e-9, "%s %s nDCG@10", v.Name, q.ID)
			assert.InDelta(t, w.rr, q.RR, 1e-9, "%s %s RR", v.Name, q.ID)
			assert.Equal(t, []float64{w.s1, w.s5, w.s10}, []float64{q.SuccessAt1, q.SuccessAt5, q.SuccessAt10},
				"%s %s S@1, S@5, S@10", v.Name, q.ID)
			assert.Equal(t, w.first, q.FirstRank, "%s %s first rank", v.Name, q.ID)
			statuses := map[judgmentStatus]int{}
			for _, j := range q.Judgments {
				statuses[j.Status]++
			}
			assert.Equal(t, wantMissing[i], statuses[missing], "%s %s missing", v.Name, q.ID)
			assert.Equal(t, wantUnsearchable[i], statuses[unsearchable], "%s %s unsearchable", v.Name, q.ID)
		}
		require.Len(t, v.Groups, len(wantGroups), v.Name)
		for i, g := range v.Groups {
			wg := wantGroups[i]
			label := v.Name + " " + groupLabel(wg.typ, wg.name)
			assert.Equal(t, wg.typ, g.Type, label)
			assert.Equal(t, wg.name, g.Name, label)
			assert.Equal(t, len(wg.queries), g.N, label)
			assert.Equal(t, len(wg.queries), g.NHigh, "%s: every query has a grade-2 judgment", label)
			assert.InDelta(t, mean(ws, wg.queries, func(w want) float64 { return w.r10 }), g.RecallAt10, 1e-9, label)
			assert.InDelta(t, mean(ws, wg.queries, func(w want) float64 { return w.r100 }), g.RecallAt100, 1e-9, label)
			require.NotNil(t, g.HighRecallAt10, label)
			assert.InDelta(t, mean(ws, wg.queries, func(w want) float64 { return *w.hi }), *g.HighRecallAt10, 1e-9, label)
			assert.InDelta(t, mean(ws, wg.queries, func(w want) float64 { return w.ndcg }), g.NDCGAt10, 1e-9, label)
			assert.InDelta(t, mean(ws, wg.queries, func(w want) float64 { return w.rr }), g.MRR, 1e-9, label)
			assert.InDelta(t, mean(ws, wg.queries, func(w want) float64 { return w.s1 }), g.SuccessAt1, 1e-9, label)
			assert.InDelta(t, mean(ws, wg.queries, func(w want) float64 { return w.s5 }), g.SuccessAt5, 1e-9, label)
			assert.InDelta(t, mean(ws, wg.queries, func(w want) float64 { return w.s10 }), g.SuccessAt10, 1e-9, label)
			assert.Equal(t, wg.missing, g.Missing, label)
			assert.Equal(t, wg.unsearchable, g.Unsearchable, label)
		}
	}
}

// TestRun_TheBM25VariantRanksOnlyItsMatches: with the vector weight 0 the
// engine ranks the keyword's documents alone, never the vector leg's at
// score 0.
func TestRun_TheBM25VariantRanksOnlyItsMatches(t *testing.T) {
	fx := newFixture(t)
	out := filepath.Join(fx.dir, "report.json")
	res := runEval(t, fx.args(queries, "-json", out, "-variants", "bm25")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	qs := readReport(t, out).Variants[0].Queries
	ranked := make([][]string, 0, len(qs))
	for _, q := range qs {
		var ids []string
		for _, r := range q.Ranking {
			ids = append(ids, r.DocumentID)
		}
		ranked = append(ranked, ids)
	}
	assert.Equal(t, [][]string{{"doc-03", "doc-05"}, {"doc-12", "doc-20"}, {"doc-07"}, {"doc-18", "doc-16"}}, ranked)
}

// TestRun_Judgments: each judgment says what it is in the copy: missing,
// unsearchable (failed, dead, or without chunks) or searchable (a pending
// document with chunks too), and its rank, or null.
func TestRun_Judgments(t *testing.T) {
	fx := newFixture(t)
	out := filepath.Join(fx.dir, "report.json")
	require.Equal(t, exitOK, runEval(t, fx.args(queries, "-json", out)...).code)
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var rep struct {
		Variants []struct {
			Queries []struct {
				Judgments []map[string]any `json:"judgments"`
				Ranking   []map[string]any `json:"ranking"`
			} `json:"queries"`
		} `json:"variants"`
	}
	require.NoError(t, json.Unmarshal(raw, &rep))
	beta := rep.Variants[0].Queries[1]
	assert.Equal(t, []map[string]any{
		{"url": docURL(20), "document_id": "doc-20", "grade": 2.0, "status": "searchable", "rank": 1.0},
		{"url": docURL(22), "document_id": "doc-22", "grade": 2.0, "status": "unsearchable", "rank": nil},
		{"url": docURL(21), "document_id": "doc-21", "grade": 1.0, "status": "unsearchable", "rank": nil},
		{"url": docURL(13), "document_id": "doc-13", "grade": 1.0, "status": "searchable", "rank": 9.0},
	}, beta.Judgments)
	assert.Len(t, beta.Ranking, 21, "every searchable document: 0 to 20")
	assert.Equal(t, map[string]any{"rank": 1.0, "document_id": "doc-20", "url": docURL(20)}, beta.Ranking[0])
	alpha := rep.Variants[0].Queries[0]
	assert.Equal(t, map[string]any{"url": "https://example.com/doc/missing", "document_id": "", "grade": 1.0,
		"status": "missing", "rank": nil}, alpha.Judgments[2])
}

// TestRun_TextReport: the text names the copy, the query file's hash and
// the counts, and each variant's groups, queries by id with their grade-2
// ranks, and stages; never a query's text.
func TestRun_TextReport(t *testing.T) {
	fx := newFixture(t)
	res := runEval(t, fx.args(queries, "-variants", "hybrid,bm25")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	out := res.stdout
	assert.Contains(t, out, "Search evaluation of "+fx.db)
	assert.Contains(t, out, "4 queries, 12 judgments: 1 missing, 3 unsearchable")
	assert.Contains(t, out, "variants hybrid, bm25")
	assert.Contains(t, out, "\nhybrid: the daemon's search")
	assert.Contains(t, out, "\nbm25: BM25 alone")
	for _, label := range []string{"all", "kind topic", "kind keyword", "tag owner", "tag recent"} {
		assert.Contains(t, out, "\n"+label+" ", label)
	}
	for _, stage := range []string{"embed", "bm25", "vector", "fuse", "hydrate", "total"} {
		assert.Regexp(t, `\n`+stage+` +\d+\.\d +\d+\.\d\n`, out, stage)
	}
	// The grade-2 ranks: hybrid's, then BM25's, where gamma's grade-2
	// document is searchable but unranked (-), and beta's failed one (×).
	assert.Regexp(t, `\nq-beta +keyword +0\.500 +1\.000 +0\.\d{3} +1 ×\n`, out)
	assert.Regexp(t, `\nq-gamma +topic +0\.000 +0\.000 +0\.000 +-\n`, out)
	assert.Regexp(t, `\nq-delta +- +`, out, "a query without a kind")
	for _, text := range []string{"alpha\n", "beta\n", "gamma\n", "delta\n"} {
		assert.NotContains(t, out, " "+text, "no query text")
	}
	assert.Equal(t, 2, strings.Count(out, "wall time "))
}

// TestRun_QueriesWithoutIDs: a query without an id is named by its index.
func TestRun_QueriesWithoutIDs(t *testing.T) {
	fx := newFixture(t)
	res := runEval(t, fx.args(withoutIDs(t, fx.dir))...)
	require.Equal(t, exitOK, res.code, res.stderr)
	assert.Regexp(t, `\n#0 +topic +`, res.stdout)
	assert.Regexp(t, `\n#3 +- +`, res.stdout)
}

// withoutIDs writes testdata/queries.yaml without its ids into dir and
// returns its path.
func withoutIDs(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(queries)
	require.NoError(t, err)
	stripped := regexp.MustCompile(`- id: \S+\n {4}`).ReplaceAllString(string(data), "- ")
	require.NotContains(t, stripped, "id:")
	path := filepath.Join(dir, "no-ids.yaml")
	require.NoError(t, os.WriteFile(path, []byte(stripped), 0o600))
	return path
}

// TestRun_Deterministic: two runs on one copy report the same, but for
// when they ran and how long they took.
func TestRun_Deterministic(t *testing.T) {
	fx := newFixture(t)
	reports := make([]map[string]any, 0, 2)
	for _, name := range []string{"a.json", "b.json"} {
		out := filepath.Join(fx.dir, name)
		require.Equal(t, exitOK, runEval(t, fx.args(queries, "-json", out, "-variants", "hybrid,bm25,vector")...).code)
		raw, err := os.ReadFile(out)
		require.NoError(t, err)
		var rep map[string]any
		require.NoError(t, json.Unmarshal(raw, &rep))
		delete(rep, "generated_at")
		for _, v := range rep["variants"].([]any) {
			variant := v.(map[string]any)
			delete(variant, "wall_ms")
			delete(variant, "latency_ms")
			for _, q := range variant["queries"].([]any) {
				delete(q.(map[string]any), "timings_ms")
			}
		}
		reports = append(reports, rep)
	}
	assert.Equal(t, reports[0], reports[1])
}

// TestRun_Provenance: the JSON records what was measured.
func TestRun_Provenance(t *testing.T) {
	fx := newFixture(t)
	out := filepath.Join(fx.dir, "report.json")
	require.Equal(t, exitOK, runEval(t, fx.args(queries, "-json", out, "-variants", "vector,hybrid")...).code)
	rep := readReport(t, out)
	assert.Equal(t, reportFormat, rep.Format)
	assert.False(t, rep.GeneratedAt.IsZero())
	p := rep.Provenance
	assert.Equal(t, fx.db, p.Database)
	assert.Positive(t, p.Schema)
	assert.Equal(t, testDim, p.Width)
	assert.Equal(t, queries, p.Queries)
	assert.Len(t, p.QueriesSHA256, 64)
	assert.Equal(t, fx.config, p.Config)
	assert.Equal(t, embeddingProvenance{Provider: "ollama", Model: testModel, Dim: testDim, BaseURL: fx.ollama.url},
		p.Embedding)
	assert.Equal(t, searchProvenance{BM25Weight: 1, VectorWeight: 1, RRFK: 60, Collapse: "max", EmbedTimeoutSeconds: 10},
		p.Search)
	assert.Equal(t, store.MaxSearchK, p.K)
	assert.Equal(t, []string{"vector", "hybrid"}, p.Variants, "in the order given")
	assert.Empty(t, p.Baseline)
	for _, v := range rep.Variants {
		for _, stage := range []string{"embed", "bm25", "vector", "fuse", "hydrate", "total"} {
			assert.Contains(t, v.LatencyMS, stage, v.Name)
		}
	}
}

// TestRun_EmbedsTheQueries: the queries are embedded as they are, after
// one warm-up, and nothing is pulled (the fake fails a test that pulls).
func TestRun_EmbedsTheQueries(t *testing.T) {
	fx := newFixture(t)
	require.Equal(t, exitOK, runEval(t, fx.args(queries)...).code)
	assert.Equal(t, []string{warmUpText, "alpha", "beta", "gamma", "delta"}, fx.ollama.embeds)
}
