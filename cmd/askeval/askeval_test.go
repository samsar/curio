package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/generator"
)

const questionsYAML = `questions:
  - id: q1
    kind: one
    question: How long should bread cool?
    sources: [https://Example.com/bread#top]
    facts: &cooling [an hour]
  - &shared
    id: q2
    kind: unanswerable
    question: What is the boiling point of mercury?
    facts: [none of the pages say]
    note: made up
  - id: q4
    kind: one
    question: When can I slice the loaf?
    facts: *cooling
  - id: q3
    kind: library
    mode: interests
    question: How have my interests changed?
    facts: [a, b]
`

// TestParseQuestions: a question file reads strictly, its URLs
// normalized, its aliases resolved; a mistake names the question.
func TestParseQuestions(t *testing.T) {
	qs, err := parseQuestions([]byte(questionsYAML), "q.yaml")
	require.NoError(t, err)
	require.Len(t, qs, 4)
	assert.Equal(t, []string{"https://example.com/bread"}, qs[0].Sources)
	assert.Equal(t, []string{"an hour"}, qs[2].Facts, "an alias of another question's facts")
	assert.Equal(t, "interests", qs[3].wantRoute())
	assert.Equal(t, "answer", qs[0].wantRoute())
	assert.False(t, qs[1].answerable())

	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"an unknown field", "questions:\n  - {id: a, kind: one, question: q, facts: [f], tag: x}\n",
			`unknown key "tag"`},
		{"a repeated key", "questions:\n  - {id: a, id: b, kind: one, question: q, facts: [f]}\n", "already defined"},
		{"no id", "questions:\n  - {kind: one, question: q, facts: [f]}\n", "id is required"},
		{"a kind outside the set", "questions:\n  - {id: a, kind: essay, question: q, facts: [f]}\n", `kind "essay"`},
		{"five facts", "questions:\n  - {id: a, kind: one, question: q, facts: [a, b, c, d, e]}\n", "5 facts"},
		{"a mode outside the set", "questions:\n  - {id: a, kind: one, question: q, facts: [f], mode: essay}\n",
			`mode "essay"`},
		{"sources for an unanswerable", "questions:\n  - {id: a, kind: unanswerable, question: q, facts: [f], " +
			"sources: [https://example.com/]}\n", "lists no sources"},
		{"a repeated id", "questions:\n  - {id: a, kind: one, question: q, facts: [f]}\n" +
			"  - {id: a, kind: one, question: r, facts: [f]}\n", `the same id "a"`},
		{"an unknown top key", "query: []\n", "field query not found"},
		{"two documents", questionsYAML + "---\nquestions: []\n", "a second YAML document"},
		{"a question aliased whole repeats its id", questionsYAML + "  - *shared\n", `the same id "q2"`},
		{"an alias to nothing", "questions:\n  - {id: a, kind: one, question: q, facts: *none}\n",
			"unknown anchor"},
		{"none", "questions: []\n", "no question"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseQuestions([]byte(tc.yaml), "q.yaml")
			require.ErrorContains(t, err, tc.want)
			assert.Contains(t, err.Error(), "q.yaml")
		})
	}
}

// TestSentences: an answer splits into sentences, citations kept with the
// sentence they close, list markers dropped.
func TestSentences(t *testing.T) {
	assert.Equal(t, []string{"Bread cools for an hour [1].", "It sets [2][3]!", "Why?", "Because."},
		sentences("Bread cools for an hour [1]. It sets [2][3]! Why? Because."))
	assert.Equal(t, []string{"First point [1].", "Second point [2].", "A last line"},
		sentences("- First point [1].\n2. Second point [2].\n\nA last line"))
	assert.Empty(t, sentences("  \n"))
}

// TestDecodeJudgment: every sentence and fact judged once, in range, of
// the schema's keys alone.
func TestDecodeJudgment(t *testing.T) {
	ok := `{"sentences":[{"n":1,"claim":true,"support":"supported"},{"n":2,"claim":false,"support":"none"}],` +
		`"facts":[{"n":1,"stated":true}]}`
	j, err := decodeJudgment([]byte(ok), 2, 1)
	require.NoError(t, err)
	assert.Len(t, j.Sentences, 2)
	for _, bad := range []string{
		`{"sentences":[{"n":1,"claim":true,"support":"supported"}],"facts":[{"n":1,"stated":true}]}`,
		`{"sentences":[{"n":1,"claim":true,"support":"supported"},{"n":1,"claim":true,"support":"not"}],` +
			`"facts":[{"n":1,"stated":true}]}`,
		`{"sentences":[{"n":1,"claim":true,"support":"supported"},{"n":3,"claim":true,"support":"not"}],` +
			`"facts":[{"n":1,"stated":true}]}`,
		`{"sentences":[{"n":1,"claim":true,"support":"supported","Claim":false},{"n":2,"claim":false,` +
			`"support":"none"}],"facts":[{"n":1,"stated":true}]}`,
		`{"sentences":[],"facts":[],"verdict":"fine"}`,
	} {
		_, err := decodeJudgment([]byte(bad), 2, 1)
		require.ErrorIs(t, err, errBadJudgment, bad)
	}
}

// TestMeasure: the metrics by their definitions, on made-up answers.
func TestMeasure(t *testing.T) {
	one := question{ID: "a", Kind: "one", Sources: []string{"https://example.com/a"}, Facts: []string{"f1", "f2"}}
	none := question{ID: "b", Kind: "unanswerable", Facts: []string{"f"}}
	listed := question{ID: "c", Kind: "list", Mode: "list", Facts: []string{"f"}}
	as := []answered{
		{q: one, route: "answer", cited: []int{1}, invalid: 1, citedURLs: []string{"https://example.com/a"},
			sents: []string{"s1", "s2", "s3"}, sentCited: []bool{true, true, false}, judged: true,
			verdicts: judgment{Sentences: []sentenceVerdict{{1, true, "supported"}, {2, true, "partly"},
				{3, true, "uncited"}}, Facts: []factVerdict{{1, true}, {2, false}}},
			stages: stageMS{FirstToken: 2000, Total: 10000}},
		{q: none, route: "answer", abstained: true, judged: true, sents: []string{"x"}, sentCited: []bool{false},
			verdicts: judgment{Sentences: []sentenceVerdict{{1, false, "none"}}, Facts: []factVerdict{{1, true}}},
			stages:   stageMS{FirstToken: 4000, Total: 20000}},
		{q: listed, route: "answer", judged: false, stages: stageMS{Total: 30000}, timingsReplayed: true},
	}
	m := measure(as)
	assert.Equal(t, 3, m.Questions)
	assert.InDelta(t, 0.5, m.CitationValidity, 1e-9, "one valid of two")
	assert.InDelta(t, 0.75, m.CitationSupport, 1e-9, "supported and partly, of two cited claims")
	assert.InDelta(t, 2.0/3, m.ClaimsCited, 1e-9)
	assert.InDelta(t, 1, m.SourceRecall, 1e-9)
	assert.InDelta(t, 2.0/3, m.FactCoverage, 1e-9)
	assert.InDelta(t, 1, m.Abstention, 1e-9)
	assert.InDelta(t, 0, m.FalseAbstention, 1e-9)
	assert.InDelta(t, 2.0/3, m.Routing, 1e-9, "the list question took the answer route")
	assert.Equal(t, int64(4000), m.FirstTokenP95MS)
	assert.Equal(t, int64(20000), m.TotalP50MS)
	assert.Equal(t, 1, m.TimingsReplayed)
	assert.Equal(t, -1.0, measure(nil).CitationValidity, "nothing to count")
}

// TestStreamBook: a recorded call is replayed asking no model; a call the
// book hasn't fails, naming the question, unless the book records, when
// the model is asked and its reply kept for the next.
func TestStreamBook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.jsonl")
	rec, err := openBook("", path)
	require.NoError(t, err)
	live := &countingStream{reply: "It cools [1]."}
	sb := &streamBook{model: "m", version: "v", book: rec, live: live}
	sb.asking("q1")
	var got strings.Builder
	_, err = sb.stream(context.Background(), "prompt", generator.Options{MaxTokens: 64}, func(p string) error {
		got.WriteString(p)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "It cools [1].", got.String())
	require.NoError(t, rec.addAsk(askRecord{Variant: "ask", ID: "q1", Order: []string{"d1"}}))
	require.NoError(t, rec.close())

	replay, err := openBook(path, "")
	require.NoError(t, err)
	sb = &streamBook{model: "m", version: "v", book: replay}
	got.Reset()
	_, err = sb.stream(context.Background(), "prompt", generator.Options{MaxTokens: 64}, func(p string) error {
		got.WriteString(p)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "It cools [1].", got.String())
	prompt, _, fromBook := sb.answered()
	assert.Equal(t, "prompt", prompt)
	assert.True(t, fromBook, "the book answered it")
	a, ok := replay.askOf("ask", "q1")
	require.True(t, ok)
	assert.Equal(t, []string{"d1"}, a.Order)

	sb.asking("q2")
	_, err = sb.stream(context.Background(), "another prompt", generator.Options{MaxTokens: 64},
		func(string) error { return nil })
	require.ErrorIs(t, err, errNotRecorded)
	assert.Contains(t, err.Error(), "question q2")
	assert.Equal(t, 1, live.calls)
}

// countingStream streams reply, counting its calls.
type countingStream struct {
	reply string
	calls int
}

func (*countingStream) Generate(context.Context, string, generator.Options) (string, error) {
	return "", errors.New("unused")
}
func (*countingStream) Model() string              { return "m" }
func (*countingStream) Ping(context.Context) error { return nil }
func (c *countingStream) GenerateStream(_ context.Context, _ string, _ generator.Options,
	emit func(string) error) (generator.StreamEnd, error) {
	c.calls++
	return generator.StreamEnd{}, emit(c.reply)
}

func (c *countingStream) GenerateJSON(context.Context, string, json.RawMessage, generator.Options) (
	json.RawMessage, error) {
	c.calls++
	return json.RawMessage(c.reply), nil
}

// TestJudgePrompt: the judge sees the writer's material as it was given,
// the sentences and the facts numbered; an answer citing rows or groups
// gets the markers template.
func TestJudgePrompt(t *testing.T) {
	p := judgePrompt("<source n=\"1\">\ntitle: A\n</source>", false, []string{"It is [1]."}, []string{"f"})
	assert.Contains(t, p, "Sources:\n<source n=\"1\">\ntitle: A\n</source>\n\nAnswer sentences:\n1. It is [1].\n\n"+
		"Facts:\n1. f\n")
	p = judgePrompt("[T1] area: Cooking | total 3 | 1 2", true, []string{"Cooking grew [T1]."}, []string{"f"})
	assert.Contains(t, p, "Material:\n[T1] area: Cooking | total 3 | 1 2\n\nAnswer sentences:\n1. Cooking grew [T1].")
	assert.Contains(t, judgePrompt("", false, []string{"s"}, nil), "Sources:\n(none)\n")
}

var update = flag.Bool("update", false, "record the testdata/*.golden files")

// TestJudgeGolden: the judge's prompts, for made-up material, sentences
// and facts, and its schema at 3 sentences and 2 facts, are the ones
// recorded: a change of either changes every verdict's key, and the
// judgment books are recorded again (-update records them).
func TestJudgeGolden(t *testing.T) {
	sents := []string{"Bread rises in an hour [1].", "Rye takes longer [2].", "Enjoy."}
	facts := []string{"an hour", "rye is slower"}
	material := "<source n=\"1\">\ntitle: Bread\npassage: Bread rises in an hour.\n</source>\n\n" +
		"<source n=\"2\">\ntitle: Rye\npassage: Rye rises slower.\n</source>"
	markers := "Interests as of the grouping of 2025-04-01, counted by year.\nPeriods, oldest first: 2023 | 2024\n" +
		"[T1] area: Cooking | total 3 | 1 2\n[T2] undated: 1 save on no known day, in no period"
	files := map[string]string{
		"judge.golden":         judgePrompt(material, false, sents, facts),
		"judge_markers.golden": judgePrompt(markers, true, []string{"Cooking grew [T1].", "One is undated [T2]."}, facts),
		"judge_schema.golden":  judgeSchema(3, 2),
	}
	for file, got := range files {
		path := filepath.Join("testdata", file)
		if *update {
			require.NoError(t, os.MkdirAll("testdata", 0o755))
			require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
		}
		want, err := os.ReadFile(path)
		require.NoError(t, err, "record %s with -update", path)
		assert.Equal(t, string(want), got, "%s changed: record the judgment books again, then -update", file)
	}
}

// TestJudgeVersion is the SHA-256 of the judge's templates and its schema.
func TestJudgeVersion(t *testing.T) {
	sum := sha256.Sum256([]byte(judgeTemplate + judgeMarkersTemplate + judgeSchemaFormat))
	assert.Equal(t, hex.EncodeToString(sum[:]), judgeVersion)
}

// TestJudge_NotRecorded: a verdict a judge's book lacks, while it records
// nothing, names that judge's own record flag.
func TestJudge_NotRecorded(t *testing.T) {
	for _, flagName := range []string{"-judgments-out", "-judgments2-out"} {
		b, err := openBook("", "")
		require.NoError(t, err)
		j := &judge{model: "m", book: b, recordFlag: flagName}
		_, err = j.judgeAnswer(context.Background(), "material", false, []string{"s"}, []string{"f"})
		require.ErrorIs(t, err, errJudgeNotRecorded)
		assert.Contains(t, err.Error(), flagName+" isn't set to ask the judge")
	}
}

// TestStreamBook_JSON: a structured call (the evidence judgment) is
// recorded and replayed by its key, its schema among it; a call the book
// hasn't fails while nothing records.
func TestStreamBook_JSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.jsonl")
	rec, err := openBook("", path)
	require.NoError(t, err)
	live := &countingStream{reply: `{"judgments":[]}`}
	sb := &streamBook{model: "m", version: "v", book: rec, live: live}
	raw, err := sb.json(context.Background(), "judge these", json.RawMessage(`{"type":"object"}`),
		generator.Options{MaxTokens: 768})
	require.NoError(t, err)
	assert.JSONEq(t, `{"judgments":[]}`, string(raw))
	require.NoError(t, rec.close())

	replay, err := openBook(path, "")
	require.NoError(t, err)
	sb = &streamBook{model: "m", version: "v", book: replay}
	raw, err = sb.json(context.Background(), "judge these", json.RawMessage(`{"type":"object"}`),
		generator.Options{MaxTokens: 768})
	require.NoError(t, err)
	assert.JSONEq(t, `{"judgments":[]}`, string(raw), "replayed, asking no model")
	_, err = sb.json(context.Background(), "judge these", json.RawMessage(`{"type":"array"}`),
		generator.Options{MaxTokens: 768})
	require.ErrorIs(t, err, errNotRecorded, "another schema is another call")
	assert.Equal(t, 1, live.calls)
}

// TestMeasure_Stages: each stage's p50 and p95 over the asks it ran in,
// with its n; the answer call's tokens over those that reported them.
func TestMeasure_Stages(t *testing.T) {
	q := question{ID: "a", Kind: "one", Facts: []string{"f"}}
	as := []answered{
		{q: q, route: "answer", stages: stageMS{Plan: 1000, Judge: 0, FirstToken: 3000, Total: 5000},
			promptTokens: 2000, outputTokens: 50},
		{q: q, route: "answer", stages: stageMS{Plan: 3000, Judge: 900, FirstToken: 4000, Total: 9000},
			promptTokens: 3000, outputTokens: 150},
		{q: q, route: "answer", stages: stageMS{Total: 100}},
	}
	m := measure(as)
	byName := map[string]stageStat{}
	for _, st := range m.Stages {
		byName[st.Stage] = st
	}
	assert.Equal(t, stageNames, func() []string {
		names := make([]string, 0, len(m.Stages))
		for _, st := range m.Stages {
			names = append(names, st.Stage)
		}
		return names
	}())
	assert.Equal(t, stageStat{Stage: "plan", N: 2, P50MS: 1000, P95MS: 3000}, byName["plan"])
	assert.Equal(t, stageStat{Stage: "judge", N: 1, P50MS: 900, P95MS: 900}, byName["judge"], "ran once")
	assert.Equal(t, stageStat{Stage: "sources", N: 0}, byName["sources"], "never ran")
	assert.Equal(t, stageStat{Stage: "total", N: 3, P50MS: 5000, P95MS: 9000}, byName["total"])
	assert.Equal(t, tokenStat{N: 2, P50: 2000, P95: 3000}, m.PromptTokens)
	assert.Equal(t, tokenStat{N: 2, P50: 50, P95: 150}, m.OutputTokens)
}

// TestMeasure_Routes: a table's or a list's markers count as citations;
// on the list route, the pages of the groups cited count toward recall;
// routing compares the route taken with the question's mode.
func TestMeasure_Routes(t *testing.T) {
	library := question{ID: "l", Kind: "library", Mode: "interests", Facts: []string{"f"}}
	listed := question{ID: "c", Kind: "list", Mode: "list", Sources: []string{"https://example.com/b"},
		Facts: []string{"f"}}
	claim := question{ID: "e", Kind: "evidence", Mode: "evidence", Sources: []string{"https://example.com/e"},
		Facts: []string{"f"}}
	m := measure([]answered{
		{q: library, route: "interests", markers: []string{"T1", "T2"}, invalid: 1},
		{q: listed, route: "list", markers: []string{"G1"}, citedURLs: []string{"https://example.com/b"}},
		{q: claim, route: "answer", cited: []int{1}, citedURLs: []string{"https://example.com/x"}},
	})
	assert.InDelta(t, 4.0/5, m.CitationValidity, 1e-9, "four valid of five")
	assert.InDelta(t, 0.5, m.SourceRecall, 1e-9, "the list's group holds its page; the claim missed")
	assert.InDelta(t, 2.0/3, m.Routing, 1e-9, "the claim took the answer route")
}

// TestCitedURLs: the list route's recall reads every page of each group
// cited; the others, the sources cited.
func TestCitedURLs(t *testing.T) {
	got := &collected{done: ask.Done{Markers: []string{"G2"}, Citations: []int{1}},
		retrieved: ask.Retrieved{Sources: []ask.Source{{N: 1, URL: "https://Example.com/s#x"}}},
		groups: &ask.Groups{Groups: []ask.Group{
			{Marker: "G1", Documents: []ask.GroupDocument{{URL: "https://example.com/not"}}},
			{Marker: "G2", Documents: []ask.GroupDocument{{URL: "https://example.com/a"},
				{URL: "https://example.com/b"}}}}}}
	assert.Equal(t, []string{"https://example.com/a", "https://example.com/b", "https://example.com/s"},
		citedURLs(got))
}

// TestRequest: each variant's request: ask leaves the rerank to
// ask.rerank, norerank turns it off, literal understanding too, and
// recorded asks as ask; a run asks models live unless it only replays.
func TestRequest(t *testing.T) {
	q := question{Question: "q"}
	assert.Equal(t, ask.Request{Question: "q", Understand: true}, request("ask", q))
	assert.Equal(t, ask.Request{Question: "q", Understand: true}, request(variantRecorded, q))
	assert.Equal(t, ask.Request{Question: "q", Understand: true, Rerank: new(false)}, request("norerank", q))
	assert.Equal(t, ask.Request{Question: "q", Rerank: new(false)}, request("literal", q))

	assert.True(t, options{}.live())
	assert.True(t, options{answers: "a", answersOut: "b"}.live(), "a replay that records asks what it lacks")
	assert.False(t, options{answers: "a"}.live())
}

// TestAgree: raw agreement and Cohen's kappa by their definitions; kappa
// is undefined when both judges gave every item one label.
func TestAgree(t *testing.T) {
	a := agree([][2]string{{"yes", "yes"}, {"yes", "no"}, {"no", "no"}, {"no", "no"}, {"yes", "yes"}})
	assert.Equal(t, 5, a.Items)
	assert.InDelta(t, 0.8, a.Raw, 1e-9)
	require.NotNil(t, a.Kappa)
	// Chance: 3/5·2/5 + 2/5·3/5 = 0.48, so kappa = (0.8 − 0.48) / 0.52.
	assert.InDelta(t, 0.32/0.52, *a.Kappa, 1e-9)

	a = agree([][2]string{{"yes", "no"}, {"no", "yes"}})
	require.NotNil(t, a.Kappa)
	assert.InDelta(t, -1, *a.Kappa, 1e-9, "every item swapped")

	a = agree([][2]string{{"none", "none"}, {"none", "none"}})
	assert.InDelta(t, 1, a.Raw, 1e-9)
	assert.Nil(t, a.Kappa, "one label throughout: chance agrees fully")

	assert.Equal(t, agreement{Raw: -1}, agree(nil))
}

// TestAgreeJudges: sentences compare by label (no claim is none), the
// support of cited sentences both call a claim, and facts; an answer one
// judge left unjudged counts in none.
func TestAgreeJudges(t *testing.T) {
	q := question{ID: "a", Kind: "one", Facts: []string{"f1", "f2"}}
	both := answered{q: q, sents: []string{"s1", "s2", "s3"}, sentCited: []bool{true, true, false},
		judged: true, judged2: true,
		verdicts: judgment{Sentences: []sentenceVerdict{{1, true, "supported"}, {2, true, "partly"},
			{3, false, "none"}}, Facts: []factVerdict{{1, true}, {2, false}}},
		// The second judge numbers in another order, and calls sentence 2
		// no claim and sentence 3 uncited.
		verdicts2: judgment{Sentences: []sentenceVerdict{{3, true, "uncited"}, {1, true, "supported"},
			{2, false, "none"}}, Facts: []factVerdict{{2, false}, {1, true}}}}
	once := both
	once.judged2 = false
	got := agreeJudges([]answered{both, once})
	assert.Equal(t, 3, got.Sentences.Items)
	assert.InDelta(t, 1.0/3, got.Sentences.Raw, 1e-9)
	assert.Equal(t, 1, got.Support.Items, "sentence 1 alone is cited and a claim to both")
	assert.InDelta(t, 1, got.Support.Raw, 1e-9)
	assert.Nil(t, got.Support.Kappa)
	assert.Equal(t, 2, got.Facts.Items)
	assert.InDelta(t, 1, got.Facts.Raw, 1e-9)
	require.NotNil(t, got.Facts.Kappa)
	assert.InDelta(t, 1, *got.Facts.Kappa, 1e-9)

	m := measure(byJudge2([]answered{both}))
	assert.InDelta(t, 1, m.CitationSupport, 1e-9, "the second judge's support: one cited claim, supported")
	assert.InDelta(t, 0.5, m.ClaimsCited, 1e-9, "the second judge's claims: 1 and 3, one of them cited")
}

// TestParseFlags_Judge2: a second judge is compared with a first, so it
// needs one, and another model.
func TestParseFlags_Judge2(t *testing.T) {
	base := []string{"-db", "c.db", "-questions", "q.yaml"}
	var out bytes.Buffer
	opts, err := parseFlags(append(base, "-judge2", "qwen3:4b-instruct"), &out)
	require.NoError(t, err)
	assert.Equal(t, "gemma4:12b", opts.judgeModel)
	assert.Equal(t, "qwen3:4b-instruct", opts.judge2Model)
	for _, args := range [][]string{
		{"-judge", "none", "-judge2", "qwen3:4b-instruct"},
		{"-judge2", "gemma4:12b"},
	} {
		out.Reset()
		_, err := parseFlags(append(base, args...), &out)
		require.Error(t, err, args)
		assert.Contains(t, out.String(), "-judge2 is compared with -judge", args)
	}
}
