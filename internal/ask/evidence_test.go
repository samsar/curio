package ask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/textutil"
)

// goldenJudged are made-up passages put to a judgment.
func goldenJudged() []judgedPassage {
	return []judgedPassage{
		{source: Source{DocID: "a", Title: "Static membership", Site: "example.org"},
			passage: Passage{Text: "A static member rejoins as itself, so no rebalance follows its restart."}},
		{source: Source{DocID: "b", Title: "Rebalance storms", Site: "example.com", SavedOn: "2024-03-05"},
			passage: Passage{Text: "Static membership still rebalances when a member leaves for good.",
				Section: "Caveats"}},
	}
}

// goldenEvidencePrompt is the evidence answer's prompt for made-up
// judgments, one on each side.
func goldenEvidencePrompt() string {
	ev := Evidence{}
	sources := sided(goldenJudged(), []verdict{
		{N: 2, Verdict: sideContradicts, Quote: "still rebalances when a member leaves"},
		{N: 1, Verdict: sideSupports, Quote: "no rebalance follows its restart"}}, &ev)
	return fill(evidenceTemplate, renderSources("is it true that static membership stops rebalances", nil, sources,
		evidenceInstructionTokens, 768), "is it true that static membership stops rebalances", goldenDay, time.UTC)
}

// judgeWriter is a fake writer that streams answers and judges passages,
// as judge says, recording the judgment's prompts and options.
type judgeWriter struct {
	*fakeWriter
	mu      sync.Mutex
	judge   func(ctx context.Context, prompt string) (json.RawMessage, error)
	judged  []string
	schemas []string
	opts    []generator.Options
}

func (w *judgeWriter) GenerateJSON(ctx context.Context, prompt string, schema json.RawMessage,
	opts generator.Options) (json.RawMessage, error) {
	w.mu.Lock()
	w.judged = append(w.judged, prompt)
	w.schemas = append(w.schemas, string(schema))
	w.opts = append(w.opts, opts)
	judge := w.judge
	w.mu.Unlock()
	return judge(ctx, prompt)
}

// replies is a judgment of verdicts.
func replies(verdicts ...verdict) func(context.Context, string) (json.RawMessage, error) {
	return func(context.Context, string) (json.RawMessage, error) {
		raw, err := json.Marshal(judgment{Judgments: verdicts})
		return raw, err
	}
}

// evidenceHits are four fetched pages on a claim, and one dead.
func evidenceHits() []search.Hit {
	return []search.Hit{
		hit("for1", "https://example.com/for1", "For one", store.DocStateFetched,
			"Static members rejoin as themselves, so a restart causes no rebalance."),
		hit("dead", "https://example.com/dead", "Dead", store.DocStateDead),
		hit("against", "https://example.org/against", "Against", store.DocStateFetched,
			"Static membership still rebalances when a member leaves for good."),
		hit("aside", "https://example.net/aside", "Aside", store.DocStateFetched, "Kafka is a log."),
		hit("for2", "https://example.com/for2", "For two", store.DocStateFetched,
			"With a group.instance.id set, a bounced consumer keeps its partitions."),
	}
}

// newEvidenceRig is a rig whose writer judges as judge says.
func newEvidenceRig(t *testing.T, judge func(context.Context, string) (json.RawMessage, error)) (*rig, *judgeWriter) {
	t.Helper()
	r := newRig(t, nil)
	jw := &judgeWriter{fakeWriter: r.writer, judge: judge}
	lifetime, cancel := context.WithCancel(context.Background())
	r.a = New(lifetime, jw, r.ret, Stores{Library: r.stores, Fields: r.stores, Summaries: r.stores,
		Bookmarks: r.stores, Chunks: r.stores, Interests: r.interests, TenantID: "local"}, r.a.opts)
	t.Cleanup(func() {
		cancel()
		r.a.Wait()
	})
	r.ret.got.Result.Items = evidenceHits()
	return r, jw
}

// evidenceOf is the Evidence event.
func evidenceOf(t *testing.T, events []Event) Evidence {
	t.Helper()
	for _, e := range events {
		if ev, ok := e.(Evidence); ok {
			return ev
		}
	}
	t.Fatal("no Evidence event")
	return Evidence{}
}

// order is the events' kinds in their order, progress by stage, the
// waits for a model's load and check aside.
func order(events []Event) []string {
	var out []string
	for _, e := range events {
		switch e := e.(type) {
		case Progress:
			if e.Stage != StageLoading && e.Stage != StageChecking {
				out = append(out, "progress "+string(e.Stage))
			}
		case Retrieved:
			out = append(out, "retrieval")
		case Evidence:
			out = append(out, "evidence")
		case Table:
			out = append(out, "table")
		case Groups:
			out = append(out, "groups")
		case Delta:
			if len(out) == 0 || out[len(out)-1] != "delta" {
				out = append(out, "delta")
			}
		case Done:
			out = append(out, "done")
		}
	}
	return out
}

// TestEvidence_BothSides: the claim's readable pages are judged, one
// passage each; the sources are those that take a side, supports first,
// each with its quote and its passage; the answer is written from them,
// citing both; the events in their order.
func TestEvidence_BothSides(t *testing.T) {
	r, jw := newEvidenceRig(t, replies(
		verdict{N: 2, Verdict: sideContradicts, Quote: "still rebalances when a member leaves"},
		verdict{N: 1, Verdict: sideSupports, Quote: "so a restart causes no rebalance"},
		verdict{N: 4, Verdict: sideSupports, Quote: "a bounced consumer keeps its partitions"}))
	r.writer.answer = answers("Static members keep their partitions [1][2], but leaving for good rebalances [3].")
	events := r.ask(t, Request{Question: "is it true that static membership stops rebalances", Mode: RouteEvidence})

	assert.Equal(t, []string{"progress retrieving", "progress judging", "retrieval", "evidence",
		"progress writing", "delta", "done"}, order(events))
	ev := evidenceOf(t, events)
	assert.Equal(t, EvidenceJudged, ev.Status)
	assert.Empty(t, ev.Reason)
	assert.Equal(t, 4, ev.Judged, "the dead page judged by nobody")
	assert.Equal(t, []int{1, 2}, ev.Supports)
	assert.Equal(t, []int{3}, ev.Contradicts)
	assert.Zero(t, ev.DroppedQuotes)

	got := retrievedOf(t, events)
	require.Len(t, got.Sources, 3)
	assert.Equal(t, []string{"for1", "for2", "against"},
		[]string{got.Sources[0].DocID, got.Sources[1].DocID, got.Sources[2].DocID})
	assert.Equal(t, "so a restart causes no rebalance", got.Sources[0].Quote)
	assert.Equal(t, []Passage{{Text: "Static members rejoin as themselves, so a restart causes no rebalance."}},
		got.Sources[0].Passages)
	require.Len(t, got.Unreadable, 1)

	require.Len(t, jw.judged, 1)
	assert.Contains(t, jw.judged[0], "<passage n=\"4\">\nWith a group.instance.id set")
	assert.NotContains(t, jw.judged[0], "<passage n=\"5\">")
	assert.Equal(t, judgeMaxTokens, jw.opts[0].MaxTokens)
	assert.Equal(t, new(0.0), jw.opts[0].Temperature)
	assert.True(t, jw.opts[0].RefuseLongPrompt, "a prompt past the context is refused, never cut")
	assert.Contains(t, jw.schemas[0], `"maxItems": 4`)
	asks := r.writer.asks()
	require.Len(t, asks, 1)
	assert.Contains(t, asks[0], "side: supports\nquote: so a restart causes no rebalance\n")
	assert.Contains(t, asks[0], "side: contradicts\nquote: still rebalances when a member leaves\n")

	d := doneOf(t, events)
	assert.Equal(t, StatusAnswered, d.Status)
	assert.Equal(t, RouteEvidence, d.Route)
	assert.Equal(t, []int{1, 2, 3}, d.Citations)
	assert.Positive(t, d.Timings.Judge)
	assert.Positive(t, d.Timings.Sources)
}

// TestEvidence_OneSide: a claim the pages only support is answered from
// those, the contradicting side empty.
func TestEvidence_OneSide(t *testing.T) {
	r, _ := newEvidenceRig(t, replies(verdict{N: 1, Verdict: sideSupports, Quote: "a restart causes no rebalance"}))
	events := r.ask(t, Request{Question: "evidence that static membership stops rebalances"})
	ev := evidenceOf(t, events)
	assert.Equal(t, EvidenceJudged, ev.Status)
	assert.Equal(t, []int{1}, ev.Supports)
	assert.Empty(t, ev.Contradicts)
	assert.Len(t, retrievedOf(t, events).Sources, 1)
	assert.Equal(t, RouteEvidence, doneOf(t, events).Route, "routed by its words")
}

// TestEvidence_None: no judgment left once the quotes are checked answers
// no_sources, the evidence event and done saying how many pages were
// read, and asks the writer nothing more.
func TestEvidence_None(t *testing.T) {
	r, _ := newEvidenceRig(t, replies(verdict{N: 1, Verdict: sideSupports, Quote: "a paraphrase of no passage"}))
	events := r.ask(t, Request{Question: "q", Mode: RouteEvidence})
	ev := evidenceOf(t, events)
	assert.Equal(t, EvidenceNone, ev.Status)
	assert.Equal(t, 4, ev.Judged)
	assert.Equal(t, 1, ev.DroppedQuotes)
	assert.Equal(t, "none of the 4 pages read supports or contradicts it", ev.Reason)
	assert.Empty(t, retrievedOf(t, events).Sources)
	d := doneOf(t, events)
	assert.Equal(t, StatusNoSources, d.Status)
	assert.Equal(t, ev.Reason, d.Reason)
	assert.Empty(t, r.writer.asks())
}

// TestEvidence_NothingToJudge: an evidence ask whose pages are all
// unreadable answers no_sources at once, saying so, without waiting for
// the writer's slot another ask's answer holds, or asking any model.
func TestEvidence_NothingToJudge(t *testing.T) {
	r, jw := newEvidenceRig(t, replies())
	r.a.opts.Timeout = 2 * time.Second
	r.ret.got.Result.Items = []search.Hit{hit("dead", "https://example.com/dead", "Dead", store.DocStateDead)}
	r.a.slot <- struct{}{}
	defer func() { <-r.a.slot }()
	events := r.ask(t, Request{Question: "is it true that tides follow the moon", Mode: RouteEvidence})
	assert.Equal(t, []string{"progress retrieving", "retrieval", "evidence", "done"}, order(events), "no wait")
	ev := evidenceOf(t, events)
	assert.Equal(t, EvidenceNone, ev.Status)
	assert.Equal(t, "the pages the search found are ones curio couldn't read", ev.Reason)
	d := doneOf(t, events)
	assert.Equal(t, StatusNoSources, d.Status)
	assert.Equal(t, ev.Reason, d.Reason)
	assert.Len(t, retrievedOf(t, events).Unreadable, 1)
	assert.Empty(t, jw.judged)
}

// TestEvidence_Fallbacks: a judgment that can't be used, cut at its cap,
// past its bound, or unreachable, and a writer that can't judge, answer
// from the answer route's sources, the evidence event saying why and
// counting no passage judged.
func TestEvidence_Fallbacks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		judge  func(context.Context, string) (json.RawMessage, error)
		status EvidenceStatus
		reason string
	}{
		{"invalid", func(context.Context, string) (json.RawMessage, error) {
			return json.RawMessage(`{"judgments":[{"n":9,"verdict":"supports","quote":"x"}]}`), nil
		}, EvidenceInvalid, "a passage numbered 9 of 4"},
		{"a repeated key", func(context.Context, string) (json.RawMessage, error) {
			return json.RawMessage(`{"judgments":[],"judgments":[]}`), nil
		}, EvidenceInvalid, "couldn't be used"},
		{"judged twice", replies(verdict{N: 1, Verdict: sideSupports, Quote: "a"},
			verdict{N: 1, Verdict: sideContradicts, Quote: "b"}), EvidenceInvalid, "passage 1 judged twice"},
		{"truncated", func(context.Context, string) (json.RawMessage, error) {
			return nil, fmt.Errorf("ollama: %w", generator.ErrTruncated)
		}, EvidenceInvalid, "cut off"},
		{"not JSON", func(context.Context, string) (json.RawMessage, error) {
			return nil, generator.ErrNotJSON
		}, EvidenceInvalid, "isn't JSON"},
		{"timeout", func(ctx context.Context, _ string) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, EvidenceTimeout, "half of what was left"},
		{"unavailable", func(context.Context, string) (json.RawMessage, error) {
			return nil, fmt.Errorf("ollama: %w", generator.ErrUnavailable)
		}, EvidenceUnavailable, "ollama"},
		{"past the context", func(context.Context, string) (json.RawMessage, error) {
			return nil, fmt.Errorf("ollama: request (9001 tokens) exceeds the available context size: %w",
				generator.ErrPromptTooLong)
		}, EvidenceUnavailable, "the passages to judge ran past the answer writer's context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newEvidenceRig(t, tc.judge)
			if tc.status == EvidenceTimeout {
				r.a.opts.Timeout = 400 * time.Millisecond
			}
			events := r.ask(t, Request{Question: "is it true that static membership stops rebalances",
				Mode: RouteEvidence})
			ev := evidenceOf(t, events)
			assert.Equal(t, tc.status, ev.Status)
			assert.Contains(t, ev.Reason, tc.reason)
			assert.Zero(t, ev.Judged)
			got := retrievedOf(t, events)
			assert.Len(t, got.Sources, 4, "the answer route's sources")
			assert.Empty(t, got.Sources[0].Quote)
			d := doneOf(t, events)
			assert.Equal(t, StatusAnswered, d.Status)
			assert.Equal(t, RouteEvidence, d.Route)
			assert.Equal(t, []string{"progress retrieving", "progress judging", "retrieval", "evidence",
				"progress writing", "delta", "done"}, order(events))
			assert.Zero(t, r.a.gate.State().TimeoutHoldUntil, "no hold counted")
		})
	}

	r := newRig(t, nil)
	r.ret.got.Result.Items = evidenceHits()
	events := r.ask(t, Request{Question: "q", Mode: RouteEvidence})
	ev := evidenceOf(t, events)
	assert.Equal(t, EvidenceUnsupported, ev.Status)
	assert.Equal(t, "ollama can't judge passages", ev.Reason)
	assert.Equal(t, StatusAnswered, doneOf(t, events).Status)
}

// TestEvidence_UnavailableNotedOnce: an unreachable judgment is noted at
// WARN once.
func TestEvidence_UnavailableNotedOnce(t *testing.T) {
	r, _ := newEvidenceRig(t, func(context.Context, string) (json.RawMessage, error) {
		return nil, errors.New("connection refused")
	})
	// No answer succeeds between the two, which would let it be noted again.
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		return nil, generator.StreamEnd{}, generator.ErrUnavailable
	}
	r.ask(t, Request{Question: "q", Mode: RouteEvidence})
	r.ask(t, Request{Question: "q", Mode: RouteEvidence})
	warns := 0
	for _, rec := range r.records(t) {
		if rec["level"] == "WARN" && strings.Contains(fmt.Sprint(rec["msg"]), "couldn't judge") {
			warns++
		}
	}
	assert.Equal(t, 1, warns)
}

// TestGround: a quote is kept when, folded, its passage holds it:
// whitespace, case, curly quotes and dashes aside; a paraphrase, a quote
// of another passage and an empty quote are dropped.
func TestGround(t *testing.T) {
	passages := []judgedPassage{
		{passage: Passage{Text: "The cluster's leader — elected by \"Raft\" — steps down after 3 missed beats."}},
		{passage: Passage{Text: "Followers vote once per term."}},
		{passage: Passage{Text: strings.Repeat("<b>", 20) + " a term ends at the last tag"}},
	}
	for _, tc := range []struct {
		quote string
		kept  bool
	}{
		{"leader — elected by \"Raft\"", true},
		{"leader   —\nelected", true},
		{"THE CLUSTER'S LEADER", true},
		{"the cluster’s leader", true},
		{"“Raft”", true},
		{"leader - elected", true},
		{"leader – elected", true},
		{"the leader resigns after three missed beats", false},
		{"Followers vote once per term", false},
		{"", false},
		{"   ", false},
	} {
		kept, dropped := ground([]verdict{{N: 1, Verdict: sideSupports, Quote: tc.quote}}, passages)
		assert.Equal(t, tc.kept, len(kept) == 1, "%q", tc.quote)
		assert.Equal(t, !tc.kept, dropped == 1, "%q", tc.quote)
	}
	kept, _ := ground([]verdict{{N: 3, Verdict: sideSupports, Quote: "a term ends at the last tag"}}, passages)
	assert.Len(t, kept, 1, "a quote from the end of a passage of brackets, whose neutralized form is longer")
}

// TestDecodeJudgment: what makes a judgment invalid.
func TestDecodeJudgment(t *testing.T) {
	for raw, ok := range map[string]bool{
		`{"judgments":[{"n":1,"verdict":"supports","quote":"a"}]}`: true,
		`{"judgments":[]}`: true,
		`{"judgments":[{"n":1,"verdict":"supports","quote":"a","extra":1}]}`: false,
		`{"Judgments":[]}`: false,
		`{"judgments":[{"n":0,"verdict":"supports","quote":"a"}]}`:                                          false,
		`{"judgments":[{"n":3,"verdict":"supports","quote":"a"}]}`:                                          false,
		`{"judgments":[{"n":1,"verdict":"maybe","quote":"a"}]}`:                                             false,
		`{"judgments":[{"n":1,"verdict":"supports","quote":"a"}],"judgments":[]}`:                           false,
		`{"judgments":[{"n":2,"verdict":"supports","quote":"a"},{"n":2,"verdict":"supports","quote":"b"}]}`: false,
		`not json`: false,
	} {
		_, err := decodeJudgment([]byte(raw), 2)
		if ok {
			assert.NoError(t, err, raw)
			continue
		}
		assert.ErrorIs(t, err, errInvalidJudgment, raw)
	}
}

// TestRenderJudgment_Fits: twenty passages of the densest text and the
// longest question fit the context with the judgment's cap.
func TestRenderJudgment_Fits(t *testing.T) {
	r := newRig(t, nil)
	hits := make([]search.Hit, 0, evidenceDepth+3)
	for i := range evidenceDepth + 3 {
		hits = append(hits, hit(fmt.Sprint("d", i), "https://example.com/"+strconv.Itoa(i), hostile(200),
			store.DocStateFetched, hostile(3500)))
	}
	read, err := r.a.read(context.Background(), nil, hits)
	require.NoError(t, err)
	question := strings.Repeat("9", MaxQuestionBytes)
	passages := r.a.judgedPassages(read, question, nil)
	require.Len(t, passages, evidenceDepth)
	prompt := renderJudgment(question, passages)
	bare := renderJudgment("", nil)
	rest := textutil.TokenQuarters(prompt) - textutil.TokenQuarters(bare)
	assert.LessOrEqual(t, (rest+3)/4+judgeInstructionTokens+judgeMaxTokens+ctxMargin, numCtx)
}

// TestEvidenceAnswer_Fits: as many sides as pages are judged, every one
// of the densest text, fit the evidence answer's prompt at the longest
// answer.
func TestEvidenceAnswer_Fits(t *testing.T) {
	passages := make([]judgedPassage, 0, evidenceDepth)
	verdicts := make([]verdict, 0, evidenceDepth)
	for i := range evidenceDepth {
		s := denseSource(i + 1)
		passages = append(passages, judgedPassage{source: s, passage: Passage{Text: hostile(3500),
			Section: hostile(120)}})
		verdicts = append(verdicts, verdict{N: i + 1, Verdict: sideSupports, Quote: hostile(200)})
	}
	sources := sided(passages, verdicts, &Evidence{})
	question := strings.Repeat("9", MaxQuestionBytes)
	prompt := fill(evidenceTemplate, renderSources(question, nil, sources, evidenceInstructionTokens,
		config.MaxAskTokens), question, goldenDay, time.UTC)
	fits(t, evidenceTemplate, prompt, evidenceInstructionTokens, config.MaxAskTokens, "evidence")
}

// TestEvidence_LogsNoContent: no record of an evidence ask carries the
// question, a passage, a quote or the answer.
func TestEvidence_LogsNoContent(t *testing.T) {
	r, _ := newEvidenceRig(t, replies(verdict{N: 1, Verdict: sideSupports, Quote: "so a restart causes no rebalance"}))
	r.writer.answer = answers("Static members keep their partitions [1].")
	r.ask(t, Request{Question: "is it true that static membership stops rebalances", Mode: RouteEvidence})
	logs := r.logs.String()
	for _, secret := range []string{"static membership", "restart causes", "keep their partitions"} {
		assert.NotContains(t, strings.ToLower(logs), strings.ToLower(secret))
	}
	assert.Contains(t, logs, `"judge_ms"`)
}
