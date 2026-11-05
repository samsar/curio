package cli

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// TestRerankWords: the reasons and fixes doctor reads from the daemon's
// reranker are the ones it writes.
func TestRerankWords(t *testing.T) {
	assert.Equal(t, rerank.OffReason, rerankOffReason)
	assert.Equal(t, rerank.OnFix, rerankOnFix)
	assert.Equal(t, rerank.SummariesFix, rerankSummariesFix)
	assert.Equal(t, rerank.UnfitFix, rerankUnfitFix)
	assert.Equal(t, rerank.NoLogprobsFix, rerankLogprobsFix)
}

// TestDoctorAndStatus_Reranking: while search reranking is on and reorders
// no search, status says why in one warning line, and doctor warns with
// the fix: questions and pages withheld, a model that failed its check,
// one Ollama keeps unloading, reranks that keep timing out, and a check
// that keeps failing to reach the model. A fit or unchecked model, or
// reranking off, warns of nothing; off by default says how to turn it on.
func TestDoctorAndStatus_Reranking(t *testing.T) {
	at := time.Date(2026, 10, 14, 14, 2, 0, 0, time.UTC)
	until := time.Now().Add(10 * time.Minute)
	const model = "gemma4:26b"
	for _, tc := range []struct {
		name         string
		state        rerank.State
		status       string // the status line, "" for none
		mark, doctor string
		hint         string
	}{
		{"off", rerank.State{Reason: rerank.OffReason}, "", "✓", "off (search.rerank: false)", ""},
		{"off by default", rerank.State{Model: "qwen3:4b-instruct", Reason: "search reranking is off by default " +
			"for qwen3:4b-instruct, which isn't measured as the reranker; " + rerank.OnFix}, "",
			"✓", "off by default for qwen3:4b-instruct, which isn't measured as the reranker", rerank.OnFix},
		{"off without the summaries", rerank.State{Model: model, Reason: "search reranking is off by default for " +
			model + " without the summaries, which it was measured with; " + rerank.SummariesFix}, "",
			"✓", "off by default for " + model + " without the summaries, which it was measured with",
			rerank.SummariesFix},
		{"on by default", rerank.State{On: true, ByDefault: true, Model: model, Check: rerank.CheckFit, CheckedAt: at},
			"", "✓", "on by default (measured as the reranker): " + model + " passed its check (" +
				localTime(at) + ")", ""},
		{"withheld", rerank.State{On: true, Model: model, Check: rerank.CheckUnchecked,
			Reason: "questions and pages would leave this machine for http://gpu-box.lan:11434"},
			"warning: search reranking keeps every first ranking: questions and pages would leave this machine " +
				"for http://gpu-box.lan:11434; run `curio doctor`\n",
			"!", "every search keeps its first ranking: questions and pages would leave this machine for " +
				"http://gpu-box.lan:11434", ""},
		{"unfit", rerank.State{On: true, Model: model, Check: rerank.CheckUnfit, CheckedAt: at,
			Unfit: model + ` didn't pass the check "a page remembered"; ` + rerank.UnfitFix},
			"warning: search reranking keeps every first ranking: " + model + " failed its check; run " +
				"`curio doctor`\n",
			"!", model + ` didn't pass the check "a page remembered" (checked ` + localTime(at) + "); every search " +
				"keeps its first ranking", rerankUnfitFix},
		{"no logprobs", rerank.State{On: true, Model: model, Check: rerank.CheckUnfit, CheckedAt: at,
			Unfit: model + ` didn't pass the check "relevant, irrelevant and asking for a yes": no token ` +
				"probabilities; " + rerank.NoLogprobsFix},
			"warning: search reranking keeps every first ranking: " + model + " failed its check; run " +
				"`curio doctor`\n",
			"!", model + ` didn't pass the check "relevant, irrelevant and asking for a yes": no token ` +
				"probabilities (checked " + localTime(at) + "); every search keeps its first ranking",
			rerankLogprobsFix},
		{"evicted", rerank.State{On: true, Model: model, Check: rerank.CheckFit, CheckedAt: at,
			EvictionHoldUntil: until},
			"warning: search reranking keeps first rankings until " + localTime(until) + ": Ollama keeps " +
				"unloading " + model + " for other models; run `curio doctor`\n",
			"!", "Ollama unloaded " + model + " within its keep-alive twice, for other models; curio doesn't load " +
				"it again until " + localTime(until) + ", and searches keep their first ranking meanwhile",
			rerankEvictFix},
		{"timing out", rerank.State{On: true, Model: model, Check: rerank.CheckFit, CheckedAt: at,
			TimeoutHoldUntil: until},
			"warning: search reranking keeps first rankings until " + localTime(until) + ": reranks with " + model +
				" keep timing out; run `curio doctor`\n",
			"!", "reranks with " + model + " kept taking longer than the timeout; searches keep their first " +
				"ranking until " + localTime(until), rerankSlowFix},
		{"a check that ran out of time", rerank.State{On: true, Model: model, Check: rerank.CheckUnchecked,
			TimeoutHoldUntil: until, Failure: rerank.Failure{Cause: "timeout:check", At: at,
				Err: "checking " + model + `, the probe "a page remembered" took longer than 14s`}},
			"warning: search reranking keeps first rankings until " + localTime(until) + ": checking " + model +
				" ran out of time; run `curio doctor`\n",
			"!", "checking " + model + `, the probe "a page remembered" took longer than 14s; curio checks it ` +
				"again at " + localTime(until) + ", and searches keep their first ranking meanwhile", rerankSlowFix},
		{"a check that keeps failing", rerank.State{On: true, Model: model, Check: rerank.CheckUnchecked,
			Failure: rerank.Failure{Cause: "unavailable:check", At: at, Err: `probe "a page remembered": HTTP 500`}},
			"", "!", model + ` couldn't be checked: probe "a page remembered": HTTP 500; searches keep their first ` +
				"ranking until it is", rerankCheckFix},
		{"fit", rerank.State{On: true, Model: model, Check: rerank.CheckFit, CheckedAt: at}, "",
			"✓", model + " passed its check (" + localTime(at) + ")", ""},
		{"unchecked", rerank.State{On: true, Model: model, Check: rerank.CheckUnchecked}, "",
			"✓", model + " not checked yet: the first reranked search loads and checks it", ""},
		{"understanding off", rerank.State{On: true, Model: model, Check: rerank.CheckUnchecked,
			Reason: "curio reranks only understood searches, and search.understand is off; set search.understand: " +
				"true, or search.rerank: false"},
			"warning: search reranking keeps every first ranking: curio reranks only understood searches, and " +
				"search.understand is off; set search.understand: true, or search.rerank: false; run `curio doctor`\n",
			"!", "every search keeps its first ranking: curio reranks only understood searches, and " +
				"search.understand is off; set search.understand: true, or search.rerank: false", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := apitest.NewReranker(20)
			r.SetState(tc.state)
			w := upWorld(t, apitest.WithReranker(r))
			out, err := w.run(t, "status")
			require.NoError(t, err)
			if tc.status == "" {
				assert.NotContains(t, out, "search reranking")
			} else {
				assert.Contains(t, out, tc.status)
			}
			out, err = w.run(t, "doctor")
			require.NoError(t, err, out)
			line, hint := doctorLine(t, out, "reranking")
			assert.Equal(t, fmt.Sprintf("%s %-22s %s", tc.mark, "reranking", tc.doctor), line)
			assert.Equal(t, tc.hint, hint)
			assert.Empty(t, r.Questions(), "status and doctor ask the reranker nothing")
		})
	}
}

// TestDoctor_RerankingWaitsForThePlanner: the shipped routes rerank only
// understood searches, so while the planner can understand no question
// (its model unfit, held for timeouts or evictions, or every question held
// back), doctor's reranking check says so, whatever the reranker's own
// model is, and points at the understanding check; a fit planner, or a
// reranker with its own trouble, says nothing of it.
func TestDoctor_RerankingWaitsForThePlanner(t *testing.T) {
	at := time.Date(2026, 10, 14, 14, 2, 0, 0, time.UTC)
	until := time.Now().Add(10 * time.Minute)
	const model = "gemma4:26b"
	fit := rerank.State{On: true, Model: model, Check: rerank.CheckFit, CheckedAt: at}
	const waits = "searches keep their first ranking: curio reranks only understood searches, and none is for now: "
	for _, tc := range []struct {
		name    string
		planner understand.State
		rr      rerank.State
		doctor  string
		hint    string
	}{
		{"planner unfit", understand.State{On: true, Model: model, Check: understand.CheckUnfit, CheckedAt: at,
			Unfit: "no"}, fit, waits + model + " failed the planner's check", rerankUnplannedFix},
		{"plans timing out", understand.State{On: true, Model: model, Check: understand.CheckFit,
			TimeoutHoldUntil: until}, rerank.State{On: true, Model: model, Check: rerank.CheckUnchecked},
			waits + "plans with " + model + " keep timing out, and searches use their words until " +
				localTime(until), rerankUnplannedFix},
		{"planner evicted", understand.State{On: true, Model: model, Check: understand.CheckFit,
			EvictionHoldUntil: until}, fit, waits + "Ollama keeps unloading " + model + ", which curio doesn't " +
			"load again until " + localTime(until), rerankUnplannedFix},
		{"every question held back", understand.State{On: true, Model: model, Reason: "ollama has no structured " +
			"output"}, fit, waits + "ollama has no structured output", rerankUnplannedFix},
		{"planner fit", understand.State{On: true, Model: model, Check: understand.CheckFit, CheckedAt: at}, fit,
			model + " passed its check (" + localTime(at) + ")", ""},
		{"the reranker's own trouble first", understand.State{On: true, Model: model, Check: understand.CheckUnfit,
			Unfit: "no"}, rerank.State{On: true, Model: model, Check: rerank.CheckUnfit, CheckedAt: at,
			Unfit: model + ` didn't pass the check "a page remembered"; ` + rerank.UnfitFix},
			model + ` didn't pass the check "a page remembered" (checked ` + localTime(at) + "); every search " +
				"keeps its first ranking", rerankUnfitFix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := apitest.NewReranker(20)
			r.SetState(tc.rr)
			planner := apitest.NewPlanner(understand.Outcome{})
			planner.SetState(tc.planner)
			w := upWorld(t, apitest.WithReranker(r), apitest.WithPlanner(planner))
			out, err := w.run(t, "doctor")
			require.NoError(t, err, out)
			line, hint := doctorLine(t, out, "reranking")
			mark := "!"
			if tc.hint == "" {
				mark = "✓"
			}
			assert.Equal(t, fmt.Sprintf("%s %-22s %s", mark, "reranking", tc.doctor), line)
			assert.Equal(t, tc.hint, hint)
		})
	}
}

// TestSearch_Reranks: curio search asks for reranking when it understands,
// and says it reranked after the understood line; --no-rerank and
// --literal don't ask.
func TestSearch_Reranks(t *testing.T) {
	r := apitest.NewReranker(20)
	srv := apitest.Start(t, apitest.DocumentLists, apitest.WithReranker(r))
	srv.AddBookmark(t, "https://example.com/a", store.SourceChrome, "Sourdough a", "")
	srv.AddBookmark(t, "https://example.com/b", store.SourceChrome, "Sourdough b", "")

	out := mustRun(t, srv, "search", "sourdough")
	assert.Contains(t, out, "reranked (m:1, listwise, top 2, ")
	assert.Equal(t, []string{"sourdough"}, r.Questions())
	mustRun(t, srv, "search", "--no-rerank", "sourdough")
	mustRun(t, srv, "search", "--literal", "sourdough")
	assert.Len(t, r.Questions(), 1, "neither asks for a rerank")
}
