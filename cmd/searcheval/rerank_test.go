package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/eval"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/rerank"
)

// overlap is how many of question's words, by their first five letters, a
// page's text shares, the fitness check's pleas aside: the fake reranker's
// judgement, which passes the check.
func overlap(question, text string) int {
	stems := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r < 'a' || r > 'z' }) {
			if len(w) >= 3 {
				out[w[:min(5, len(w))]] = true
			}
		}
		return out
	}
	q, n := stems(question), 0
	for w := range stems(text) {
		if q[w] && w != "rank" && w != "answe" && w != "first" {
			n++
		}
	}
	return n
}

// rerankAnswer is the fake Ollama's /api/generate reply to a reranker's
// call: a listwise order by overlap, ties in order, or first-token
// probabilities likelier yes the more the page shares.
func rerankAnswer(t *testing.T, prompt string, logprobs bool) string {
	_, rest, _ := strings.Cut(prompt, "\nQuestion: ")
	if logprobs {
		question, page, _ := strings.Cut(rest, "\n\nPage:\n")
		p := 1 / (1 + math.Exp(2-3*float64(overlap(question, page))))
		return fmt.Sprintf(`{"response":"yes","done":true,"logprobs":[{"token":"yes","logprob":%g,`+
			`"top_logprobs":[{"token":"yes","logprob":%g},{"token":"no","logprob":%g}]}]}`, math.Log(p), math.Log(p),
			math.Log(1-p))
	}
	question, list, _ := strings.Cut(rest, "\n\nCandidates:\n")
	type scored struct{ n, overlap int }
	var all []scored
	for line := range strings.SplitSeq(strings.TrimSpace(list), "\n") {
		var c map[string]any
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Errorf("a candidate line isn't JSON: %v", err)
			return `{"error":"bad candidate"}`
		}
		all = append(all, scored{int(c["n"].(float64)), overlap(question, fmt.Sprint(c["title"], " ", c["about"], " ",
			c["passage"], " ", c["saved_as"]))})
	}
	slices.SortStableFunc(all, func(a, b scored) int { return b.overlap - a.overlap })
	order := make([]int, len(all))
	for i, s := range all {
		order[i] = s.n
	}
	reply, err := json.Marshal(map[string][]int{"order": order})
	if err != nil {
		t.Errorf("encode an order: %v", err)
	}
	quoted, err := json.Marshal(string(reply))
	if err != nil {
		t.Errorf("encode a reply: %v", err)
	}
	return fmt.Sprintf(`{"response":%s,"done":true,"done_reason":"stop"}`, quoted)
}

// TestRerankOptions_TheDaemons: without -rerank-with the reranked
// variants rerank as the daemon would, on whatever search.rerank and
// search.understand say; each key changes its setting alone.
func TestRerankOptions_TheDaemons(t *testing.T) {
	cfg := config.Default()
	want := rerank.OptionsFrom(cfg)
	want.On, want.OffReason = true, ""
	assert.Equal(t, want, rerankOptions(cfg, rerankWith{}))
	cfg.Search.Understand = false
	require.NotEmpty(t, rerank.OptionsFrom(cfg).Unplanned)
	assert.Empty(t, rerankOptions(cfg, rerankWith{}).Held(), "the tool plans the queries itself")
	cfg = config.Default()

	w, err := parseRerankWith("method=listwise,model=example/reranker:q8,depth=30,blend=rrf,input=summary," +
		"textless=held,folders=true,claims=true,timeout=3s")
	require.NoError(t, err)
	got := rerankOptions(cfg, w)
	assert.Equal(t, "example/reranker:q8", got.Model.Model)
	assert.Equal(t, rerank.MethodListwise, got.Settings.Force)
	assert.Equal(t, 30, got.Settings.Depth)
	assert.Equal(t, rerank.BlendRRF, got.Settings.Blend)
	assert.Equal(t, rerank.InputSummary, got.Settings.Input)
	assert.Equal(t, rerank.TextlessHeld, got.Settings.Textless)
	assert.True(t, got.Settings.Folders)
	assert.True(t, got.Settings.Claims)
	assert.Equal(t, 3*time.Second, got.Timeout)
	assert.Nil(t, cfg.Generation.Roles, "the config itself is left as it was")
}

// TestParseRerankWith: the keys and values are read strictly.
func TestParseRerankWith(t *testing.T) {
	for in, want := range map[string]string{
		"depth":            `"depth" is not key=value`,
		"depth=":           `"depth=" is not key=value`,
		"method=skip":      `"skip" is not one of listwise, pointwise`,
		"textless=dropped": `"dropped" is not one of read, held`,
		"folders=yes":      `"yes" is not true or false`,
		"timeout=-1s":      `"-1s" is not a positive duration`,
		"depth=1":          `"1" is not a depth in [2, 30]`,
		"speed=1":          "unknown key",
	} {
		_, err := parseRerankWith(in)
		require.ErrorContains(t, err, want, in)
	}
	w, err := parseRerankWith("")
	require.NoError(t, err)
	assert.Equal(t, rerankWith{}, w)
}

// TestCallKey: a call's key is the same for the same call, and another
// for a change of the model, the method, the options or the prompt.
func TestCallKey(t *testing.T) {
	base := callKey("m:1", rerank.MethodListwise, "temperature 0", "prompt")
	assert.Equal(t, base, callKey("m:1", rerank.MethodListwise, "temperature 0", "prompt"))
	assert.Len(t, base, 64)
	for _, other := range []string{
		callKey("m:2", rerank.MethodListwise, "temperature 0", "prompt"),
		callKey("m:1", rerank.MethodPointwise, "temperature 0", "prompt"),
		callKey("m:1", rerank.MethodListwise, "temperature 0.2", "prompt"),
		callKey("m:1", rerank.MethodListwise, "temperature 0", "prompt."),
	} {
		assert.NotEqual(t, base, other)
	}
	keepAlive := generator.Options{Temperature: new(0.0), MaxTokens: 256, KeepAlive: time.Minute}
	assert.Equal(t, optionsKey(generator.Options{Temperature: new(0.0), MaxTokens: 256}), optionsKey(keepAlive),
		"the keep-alive changes no reply")
}

// countingModel is a model whose calls answer as given and count.
type countingModel struct {
	generator.Generator
	calls int
}

func (m *countingModel) GenerateJSON(context.Context, string, json.RawMessage, generator.Options) (
	json.RawMessage, error) {
	m.calls++
	return json.RawMessage(`{"order":[2,1]}`), nil
}

func (m *countingModel) FirstTokens(context.Context, string, bool, int, generator.Options) (
	[]generator.TokenChoice, error) {
	m.calls++
	return []generator.TokenChoice{{Token: "yes", Logprob: -0.1}}, nil
}

// TestAnswerBook: a call the model answers is recorded, its time counted
// as measured and recorded; a replayed one is answered from the file,
// counting its recorded time and none measured, asking no model; a call
// a replay has no answer to is errNotRecorded, marked missed.
func TestAnswerBook(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "answers.jsonl")
	model := &countingModel{}
	book, err := newAnswerBook(model, "m:1", "", out)
	require.NoError(t, err)
	opts := generator.Options{Temperature: new(0.0), MaxTokens: 256}
	order, err := book.GenerateJSON(context.Background(), "p1", json.RawMessage(`{}`), opts)
	require.NoError(t, err)
	assert.JSONEq(t, `{"order":[2,1]}`, string(order))
	_, err = book.FirstTokens(context.Background(), "p2", true, 20, generator.Options{})
	require.NoError(t, err)
	recorded, measured, missed := book.take()
	assert.Equal(t, recorded, measured)
	assert.False(t, missed)
	require.NoError(t, book.Close())
	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(raw), "\n"))
	assert.NotContains(t, string(raw), "p1", "no prompt is kept")

	replay, err := newAnswerBook(nil, "m:1", out, "")
	require.NoError(t, err)
	order, err = replay.GenerateJSON(context.Background(), "p1", json.RawMessage(`{}`), opts)
	require.NoError(t, err)
	assert.JSONEq(t, `{"order":[2,1]}`, string(order))
	recorded, measured, missed = replay.take()
	assert.Zero(t, measured, "replayed: no call made")
	assert.False(t, missed)
	assert.GreaterOrEqual(t, recorded, time.Duration(0))
	_, err = replay.GenerateJSON(context.Background(), "another prompt", json.RawMessage(`{}`), opts)
	require.ErrorIs(t, err, errNotRecorded)
	_, _, missed = replay.take()
	assert.True(t, missed)
	assert.Equal(t, 2, model.calls, "the replay asked no model")
}

// TestUnjudgedEntering: the documents a rerank brought into the top 10
// that no judgment names.
func TestUnjudgedEntering(t *testing.T) {
	docs := func(ids ...string) []rankedDoc {
		out := make([]rankedDoc, len(ids))
		for i, id := range ids {
			out[i] = rankedDoc{id: id, url: "https://example.com/" + id}
		}
		return out
	}
	q := eval.Query{Relevant: []eval.Judgment{{URL: "https://example.com/j", Grade: 1}}}
	before := docs("a", "b", "c", "d", "e", "f", "g", "h", "i", "k", "j", "x", "y")
	after := docs("x", "j", "a", "b", "c", "d", "e", "f", "g", "h", "i", "k", "y")
	assert.Equal(t, []string{"x"}, unjudgedEntering(q, before, after), "j is judged; a to h were there")
	assert.Empty(t, unjudgedEntering(q, before, before))
}

// TestPlanKindGroups: queries are grouped by their plan's kind, in first
// appearance, none for a query without a plan.
func TestPlanKindGroups(t *testing.T) {
	queries := []queryReport{{Plan: &planJSON{Kind: "list"}}, {}, {Plan: &planJSON{Kind: "one"}},
		{Plan: &planJSON{Kind: "list"}}}
	assert.Equal(t, []eval.Group{{Type: groupPlanKind, Name: "list", Queries: []int{0, 3}},
		{Type: groupPlanKind, Name: "none", Queries: []int{1}}, {Type: groupPlanKind, Name: "one", Queries: []int{2}}},
		planKindGroups(queries))
}

// TestRun_Reranked: the reranked variants rerank their base ranking's
// head as the daemon would (the order's documents first, by
// rerank.Apply, the rest as they were), recording every answer; a replay
// of them ranks every query the same asking no model; a point whose
// prompts change fails the replay naming the query; and a recorded rerank
// past the timeout is scored as the timeout the daemon would have
// answered, the first ranking kept, at the timeout's cost.
func TestRun_Reranked(t *testing.T) {
	fx := plannedFixture(t)
	first := filepath.Join(fx.dir, "first.json")
	answers := filepath.Join(fx.dir, "answers.jsonl")
	variants := "hybrid,hybrid-reranked,planned,planned-reranked"
	res := runEval(t, fx.args(plannedQueries, "-json", first, "-variants", variants, "-today", "2025-04-17",
		"-reranks-out", answers)...)
	require.Equal(t, exitOK, res.code, res.stderr)
	rep := readReport(t, first)
	asked := fx.ollama.reranks
	assert.Positive(t, asked)

	base := map[string]variantReport{}
	for _, v := range rep.Variants {
		base[v.Name] = v
	}
	for _, name := range []string{"hybrid-reranked", "planned-reranked"} {
		v := base[name]
		require.NotNil(t, v.Rerank, name)
		assert.Equal(t, "passed", v.Rerank.Check, "%s: the fake passes the check", name)
		assert.Equal(t, rerank.PromptVersion, v.Rerank.PromptVersion)
		assert.Contains(t, v.LatencyMS, "rerank")
		assert.Contains(t, v.LatencyMS, "total")
		assert.True(t, slices.ContainsFunc(v.Groups, func(g groupReport) bool { return g.Type == groupPlanKind }),
			"%s: grouped by plan kind", name)
		from := base[strings.TrimSuffix(name, "-reranked")]
		for i, q := range v.Queries {
			require.NotNil(t, q.Rerank, "%s %s", name, q.ID)
			was := ids(from.Queries[i].Ranking)
			if q.Rerank.Status != rerank.StatusReranked {
				assert.Equal(t, was, ids(q.Ranking), "%s %s: kept", name, q.ID)
				continue
			}
			applied, _ := rerank.Apply(was, q.Rerank.Order)
			assert.Equal(t, applied, ids(q.Ranking), "%s %s: the API's rule", name, q.ID)
		}
	}
	assert.Contains(t, res.stdout, "reranks: reranked ")

	again := filepath.Join(fx.dir, "again.json")
	res = runEval(t, fx.args(plannedQueries, "-json", again, "-variants", variants, "-today", "2025-04-17",
		"-reranks", answers)...)
	require.Equal(t, exitOK, res.code, res.stderr)
	assert.Equal(t, asked, fx.ollama.reranks, "a replay asks no model")
	replayed := readReport(t, again)
	for vi, v := range replayed.Variants {
		for i, q := range v.Queries {
			assert.Equal(t, ids(rep.Variants[vi].Queries[i].Ranking), ids(q.Ranking), "%s %s", v.Name, q.ID)
		}
		if v.Rerank != nil {
			assert.Equal(t, "replayed", v.Rerank.Check)
		}
	}

	res = runEval(t, fx.args(plannedQueries, "-variants", "hybrid-reranked", "-reranks", answers, "-rerank-with",
		"method=listwise")...)
	assert.Equal(t, exitFailure, res.code)
	assert.Contains(t, res.stderr, "variant hybrid-reranked, query q-")
	assert.Contains(t, res.stderr, errNotRecorded.Error())

	late := filepath.Join(fx.dir, "late.json")
	res = runEval(t, fx.args(plannedQueries, "-json", late, "-variants", "planned,planned-reranked", "-today",
		"2025-04-17", "-reranks", answers, "-rerank-with", "timeout=1ns")...)
	require.Equal(t, exitOK, res.code, res.stderr)
	slow := readReport(t, late)
	timedOut := 0
	for i, q := range slow.Variants[1].Queries {
		if q.Rerank.TookMS == 0 {
			continue // nothing asked
		}
		timedOut++
		assert.True(t, q.Rerank.TimedOutByBudget, q.ID)
		assert.Equal(t, rerank.StatusTimeout, q.Rerank.Status)
		assert.Equal(t, ids(slow.Variants[0].Queries[i].Ranking), ids(q.Ranking), "%s: the first ranking", q.ID)
	}
	assert.Positive(t, timedOut, "a recorded rerank to score")
}

// TestRun_RerankCheckTimedApart: the fitness check's calls are timed apart
// from the queries'. With every answer recorded at a known time, each
// reranked query's rerank time is its own call's, whether the check's
// answers are replayed, asked of the model, or not needed (a replay runs
// no check), and the three runs rank and time every query alike.
func TestRun_RerankCheckTimedApart(t *testing.T) {
	fx := plannedFixture(t)
	runReport := func(name string, flags ...string) variantReport {
		t.Helper()
		out := filepath.Join(fx.dir, name+".json")
		// Listwise for every query: one call a reranked query, after the
		// check's.
		res := runEval(t, fx.args(plannedQueries, append([]string{"-json", out, "-variants", "hybrid-reranked",
			"-rerank-with", "method=listwise"}, flags...)...)...)
		require.Equal(t, exitOK, res.code, res.stderr)
		return readReport(t, out).Variants[0]
	}
	recorded := filepath.Join(fx.dir, "recorded.jsonl")
	reranked := 0
	for _, q := range runReport("recorded", "-reranks-out", recorded).Queries {
		require.Contains(t, []rerank.Status{rerank.StatusReranked, rerank.StatusSkipped}, q.Rerank.Status, q.ID)
		if q.Rerank.Status == rerank.StatusReranked {
			reranked++
		}
	}
	answers := readAnswers(t, recorded)
	probes := len(answers) - reranked
	require.Positive(t, reranked)
	require.Positive(t, probes, "the check's answers, recorded first")
	const callMS = 250.0
	for i := range answers {
		answers[i].TookMS = callMS
	}
	all, queriesOnly := filepath.Join(fx.dir, "all.jsonl"), filepath.Join(fx.dir, "queries.jsonl")
	writeAnswers(t, all, answers)
	writeAnswers(t, queriesOnly, answers[probes:])

	asked := fx.ollama.rerankCalls()
	var want []queryReport
	for _, run := range []struct {
		name    string
		flags   []string
		check   string
		checkMS float64 // -1: asked of the model, its time unknown
	}{
		{"no-check", []string{"-reranks", all}, "replayed", 0},
		{"check-replayed", []string{"-reranks", all, "-reranks-out", filepath.Join(fx.dir, "more.jsonl")}, "passed",
			float64(probes) * callMS},
		{"check-asked", []string{"-reranks", queriesOnly, "-reranks-out", filepath.Join(fx.dir, "probes.jsonl")},
			"passed", -1},
	} {
		v := runReport(run.name, run.flags...)
		assert.Equal(t, run.check, v.Rerank.Check, run.name)
		if run.checkMS >= 0 {
			assert.InDelta(t, run.checkMS, v.Rerank.CheckMS, 1e-9, run.name)
		}
		for i, q := range v.Queries {
			own := 0.0 // a skipped query asks nothing
			if q.Rerank.Status == rerank.StatusReranked {
				own = callMS
			}
			assert.InDelta(t, own, q.Rerank.TookMS, 1e-9, "%s %s: its own calls' time", run.name, q.ID)
			if want != nil {
				assert.Equal(t, want[i].Rerank, q.Rerank, "%s %s", run.name, q.ID)
				assert.Equal(t, ids(want[i].Ranking), ids(q.Ranking), "%s %s", run.name, q.ID)
			}
		}
		if want == nil {
			want = v.Queries
		}
	}
	assert.Equal(t, asked+probes, fx.ollama.rerankCalls(), "the check alone asked the model, once")
}

// TestRun_RerankReportsWhatOllamaHeld: a reranked variant reports what
// Ollama holds after it, and the models it held before that it doesn't
// now, evicted while the reranker ran; the run unloads the reranker's
// model when it loaded it, and leaves it loaded when it found it so.
func TestRun_RerankReportsWhatOllamaHeld(t *testing.T) {
	fx := plannedFixture(t)
	runReport := func(name string) (*rerankReport, string) {
		t.Helper()
		out := filepath.Join(fx.dir, name+".json")
		res := runEval(t, fx.args(plannedQueries, "-json", out, "-variants", "hybrid-reranked", "-rerank-with",
			"method=listwise")...)
		require.Equal(t, exitOK, res.code, res.stderr)
		return readReport(t, out).Variants[0].Rerank, res.stdout
	}

	fx.ollama.holding(testModel, testModel) // the embedder alone, evicted at the first rerank
	rr, stdout := runReport("loaded")
	assert.Equal(t, []string{planModel}, rr.ResidentAfter, "the reranker's model, which the run loaded")
	assert.Equal(t, []string{testModel}, rr.Evicted)
	assert.Contains(t, stdout, "evicted while it ran: "+testModel)
	assert.Equal(t, []string{planModel}, fx.ollama.unloaded(), "the run unloads the model it loaded")

	fx.ollama.holding("", testModel, planModel)
	rr, stdout = runReport("found")
	assert.Equal(t, []string{testModel, planModel}, rr.ResidentAfter)
	assert.Empty(t, rr.Evicted)
	assert.NotContains(t, stdout, "evicted while it ran")
	assert.Empty(t, fx.ollama.unloaded(), "a model the run found loaded stays loaded")
}

// readAnswers reads a -reranks file.
func readAnswers(t *testing.T, path string) []answer {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var out []answer
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		var a answer
		require.NoError(t, json.Unmarshal([]byte(line), &a))
		out = append(out, a)
	}
	return out
}

// writeAnswers writes answers to path as a -reranks file.
func writeAnswers(t *testing.T, path string, answers []answer) {
	t.Helper()
	var b strings.Builder
	for _, a := range answers {
		line, err := json.Marshal(a)
		require.NoError(t, err)
		b.Write(line)
		b.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
}

func ids(r []rankedReport) []string {
	out := make([]string, len(r))
	for i, d := range r {
		out[i] = d.DocumentID
	}
	return out
}

// TestDropQueryText_RerankKeys: no record carries a question, candidates,
// a prompt or a reply.
func TestDropQueryText_RerankKeys(t *testing.T) {
	for _, key := range []string{"question", "candidates", "prompt", "reply", "query"} {
		assert.Empty(t, dropQueryText(nil, slog.String(key, "private")).Key, key)
	}
	assert.Equal(t, "query_id", dropQueryText(nil, slog.String("query_id", "q-1")).Key)
}
