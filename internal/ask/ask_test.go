package ask

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/modelgate"
	"github.com/samsar/curio/internal/retrieve"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
	"github.com/samsar/curio/internal/understand"
)

// TestAsk_Answered: a cold model is loaded and checked, the question
// retrieved as a search, the answer streamed piece by piece from the
// sources, and its citations read: the events in their order.
func TestAsk_Answered(t *testing.T) {
	r := newRig(t, nil)
	r.writer.loaded = false
	r.writer.answer = answers("Partitions ", "are shared [1][3], ", "and moved [9].")
	events := r.ask(t, Request{Question: "how do consumer groups share partitions", Understand: true,
		Rerank: new(true)})

	var stages []Stage
	for _, e := range events {
		// The fake's check answers at once, so the wait may find it done
		// before it sees it under way: checking is reported, if at all,
		// between the load and the retrieval (the gate's own tests hold
		// Await to reporting it).
		if p, ok := e.(Progress); ok && p.Stage != StageChecking {
			stages = append(stages, p.Stage)
		}
	}
	assert.Equal(t, []Stage{StageLoading, StageRetrieving, StageWriting}, stages)
	if i := slices.IndexFunc(events, func(e Event) bool { p, ok := e.(Progress); return ok && p.Stage == StageChecking }); i >= 0 {
		assert.IsType(t, Progress{}, events[i-1], "checking follows the load")
	}
	got := retrievedOf(t, events)
	require.Len(t, got.Sources, 3)
	assert.Equal(t, Source{N: 1, DocID: "d1", URL: "https://example.com/kafka", Title: "Kafka consumer groups",
		Site:     "example.com",
		Passages: []Passage{{Text: "A consumer group shares a topic's partitions among its members."}},
		OwnTitle: "Kafka consumer groups"},
		got.Sources[0])
	assert.Equal(t, "Partitions are shared [1][3], and moved [9].", text(events))

	d := doneOf(t, events)
	assert.Equal(t, StatusAnswered, d.Status)
	assert.Empty(t, d.Reason)
	assert.Equal(t, []int{1, 3}, d.Citations)
	assert.Equal(t, []CitedAt{{Start: 23, End: 24, N: 1}, {Start: 26, End: 27, N: 3}}, d.CitedAt,
		"where in the delta texts' concatenation")
	assert.Equal(t, []string{"9"}, d.InvalidCitations)
	assert.False(t, d.Uncited)
	assert.False(t, d.Partial)
	assert.Equal(t, "gemma4:26b", d.Model)
	assert.Equal(t, PromptVersion, d.PromptVersion)
	assert.Equal(t, RouteAnswer, d.Route)
	assert.Positive(t, d.Timings.FirstToken)
	assert.GreaterOrEqual(t, d.Timings.Total, d.Timings.FirstToken)

	require.Len(t, r.ret.queries, 1)
	assert.Equal(t, retrieve.Query{Question: "how do consumer groups share partitions", K: readDepth,
		Understand: true, Rerank: true}, r.ret.queries[0], "retrieved as a search, the reranker's head deep")
	asks := r.writer.asks()
	require.Len(t, asks, 1)
	assert.Contains(t, asks[0], "Today is Thursday, 2025-04-17.")
	assert.True(t, strings.HasSuffix(asks[0], "Question: how do consumer groups share partitions\n"))
	opts := r.writer.opts[len(r.writer.opts)-1]
	assert.NotNil(t, opts.Usage, "the answer's cost asked for, its prompt's reading timed")
	opts.Usage = nil
	assert.Equal(t, generator.Options{Temperature: new(0.0), MaxTokens: 768, KeepAlive: 30 * time.Minute,
		Idle: streamIdle}, opts)
	assert.Equal(t, modelgate.CheckFit, r.a.State().Check)
	assert.Equal(t, StatusAnswered, r.a.State().Last)
}

// TestAsk_AbstainsAndUncited: the abstention is an answer that cites
// nothing, and isn't uncited; an answer citing nothing otherwise is.
func TestAsk_AbstainsAndUncited(t *testing.T) {
	r := newRig(t, nil)
	r.writer.answer = answers(Abstention)
	d := doneOf(t, r.ask(t, Request{Question: "what is the capital of Peru"}))
	assert.Equal(t, StatusAnswered, d.Status)
	assert.True(t, d.Abstained)
	assert.False(t, d.Uncited)

	r.writer.answer = answers("Partitions are shared.")
	d = doneOf(t, r.ask(t, Request{Question: "how are partitions shared"}))
	assert.Equal(t, StatusAnswered, d.Status)
	assert.True(t, d.Uncited)
	assert.Empty(t, d.Citations)
}

// TestAsk_Truncated: an answer cut at its token cap ends truncated, its
// pieces sent and its citations read.
func TestAsk_Truncated(t *testing.T) {
	r := newRig(t, nil)
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		return []string{"Partitions [2] are"}, generator.StreamEnd{Truncated: true}, nil
	}
	events := r.ask(t, Request{Question: "how are partitions shared"})
	d := doneOf(t, events)
	assert.Equal(t, StatusTruncated, d.Status)
	assert.Equal(t, "the answer ran past 768 tokens (ask.max_tokens)", d.Reason)
	assert.Equal(t, []int{2}, d.Citations)
	assert.Equal(t, "Partitions [2] are", text(events))
}

// TestAsk_NoSources: an empty library, a search that finds nothing, and
// a search whose every page is unreadable ask no model, and say why.
func TestAsk_NoSources(t *testing.T) {
	r := newRig(t, nil)
	r.stores.counts = map[store.DocState]int{store.DocStatePending: 4}
	events := r.ask(t, Request{Question: "anything"})
	require.Len(t, events, 1, "nothing retrieved")
	d := doneOf(t, events)
	assert.Equal(t, StatusNoSources, d.Status)
	assert.Equal(t, "the library has no page curio has read yet", d.Reason)
	assert.Empty(t, r.ret.queries)

	r.stores.counts = map[store.DocState]int{store.DocStateFetched: 2}
	r.ret.got.Result.Items = nil
	d = doneOf(t, r.ask(t, Request{Question: "anything"}))
	assert.Equal(t, StatusNoSources, d.Status)
	assert.Equal(t, "the search found no saved page to answer from", d.Reason)

	r.ret.got.Result.Items = []search.Hit{
		hit("dead", "https://example.com/gone", "Gone", store.DocStateDead),
		hit("failed", "https://example.org/walled", "Walled", store.DocStateFailed),
		hit("bare", "https://example.net/bare", "Bare", store.DocStateFetched),
	}
	events = r.ask(t, Request{Question: "anything"})
	d = doneOf(t, events)
	assert.Equal(t, StatusNoSources, d.Status)
	assert.Equal(t, "the pages the search found are ones curio couldn't read", d.Reason)
	got := retrievedOf(t, events)
	assert.Empty(t, got.Sources, "a page with neither a passage nor a summary is passed over")
	assert.Equal(t, []Unreadable{
		{DocID: "dead", URL: "https://example.com/gone", Title: "Gone", State: store.DocStateDead,
			Reason: "the page is no longer available", OwnTitle: "Gone"},
		{DocID: "failed", URL: "https://example.org/walled", Title: "Walled", State: store.DocStateFailed,
			Reason: "the page couldn't be read", OwnTitle: "Walled"},
	}, got.Unreadable)
	assert.Empty(t, r.writer.asks(), "no model asked")
}

// TestAsk_Sources: sources are the ranking's readable pages in order,
// up to the request's count; each is named by its fields, and as the
// dashboard names it, by its own title or else its newest save's, with its
// summary's about line, its first known save and its two best passages;
// unreadable pages are listed apart, at most three.
func TestAsk_Sources(t *testing.T) {
	r := newRig(t, nil)
	r.ret.got.Result.Items = []search.Hit{
		hit("dead1", "https://example.com/1", "One", store.DocStateDead),
		hit("a", "https://example.com/a", "", store.DocStateFetched, "first passage", "second passage", "third"),
		hit("dead2", "https://example.com/2", "Two", store.DocStateDead),
		hit("dead3", "https://example.com/3", "Three", store.DocStateFailed),
		hit("dead4", "https://example.com/4", "Four", store.DocStateDead),
		hit("summed", "https://example.org/b", "Summed", store.DocStateFetched),
		hit("c", "https://example.net/c", "Third", store.DocStateFetched, "c passage"),
	}
	approx := time.Date(2023, 5, 6, 23, 30, 0, 0, time.UTC)
	exact := time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC)
	r.stores.fields = map[string]store.DocumentFields{
		"a": {Host: "example.com", SiteName: "Example News", BookmarkTitles: "Saved A\nSaved again"},
	}
	r.stores.summaries = map[string]*store.Summary{"summed": {About: "A page about b."}}
	r.stores.bookmarks = map[string][]*store.Bookmark{
		"a": {{SavedAt: exact, SavedDate: store.SavedDateExact, Title: new(" Newest save ")},
			{SavedAt: approx, SavedDate: store.SavedDateApprox, Title: new("Older save")},
			{SavedAt: approx.AddDate(-5, 0, 0), SavedDate: store.SavedDateUnknown}},
		"c": {{SavedAt: exact, SavedDate: store.SavedDateExact}},
	}
	got := retrievedOf(t, r.ask(t, Request{Question: "q", Sources: 2}))
	require.Len(t, got.Sources, 2)
	assert.Equal(t, Source{N: 1, DocID: "a", URL: "https://example.com/a", Title: "Saved A",
		SavedAs: []string{"Saved again"}, Site: "Example News", SavedOn: "about 2023-05-06",
		Passages:   []Passage{{Text: "first passage"}, {Text: "second passage"}, {Text: "third"}},
		SavedTitle: "Newest save"}, got.Sources[0])
	assert.Equal(t, Source{N: 2, DocID: "summed", URL: "https://example.org/b", Title: "Summed",
		Site: "example.org", About: "A page about b.", OwnTitle: "Summed"}, got.Sources[1])
	require.Len(t, got.Unreadable, 3)
	assert.Equal(t, []string{"dead1", "dead2", "dead3"},
		[]string{got.Unreadable[0].DocID, got.Unreadable[1].DocID, got.Unreadable[2].DocID})
	assert.Equal(t, 1, r.stores.bookmarkReads, "the names come from the one read of the bookmarks")
}

// TestAsk_Off: Ask off, off by default for an unmeasured model, or a
// provider that can't stream, answers off with its reason, which says how
// to turn it on, asking nothing and retrieving nothing.
func TestAsk_Off(t *testing.T) {
	r := newRig(t, func(o *Options) { o.On = false })
	d := doneOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, StatusOff, d.Status)
	assert.Equal(t, OffReason, d.Reason)
	assert.Empty(t, r.ret.queries)
	assert.Empty(t, r.writer.prompts)

	byDefault := OptionsFrom(config.Default())
	r = newRig(t, func(o *Options) { o.On, o.OffReason = false, byDefault.OffReason })
	d = doneOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, StatusOff, d.Status)
	assert.Equal(t, byDefault.OffReason, d.Reason)
	assert.Contains(t, d.Reason, "ask.enabled: true turns it on")
	st := r.a.State()
	assert.False(t, st.On)
	assert.False(t, st.ByDefault)
	assert.False(t, st.OffByDefault, "the options didn't say so")
	assert.Equal(t, byDefault.OffReason, st.Reason, "healthz says it too")
	assert.Empty(t, r.ret.queries)

	a := New(t.Context(), noStream{}, r.ret, Stores{Library: r.stores}, Options{On: true, Timeout: time.Minute,
		Model: r.a.opts.Model, Log: r.a.log})
	var events []Event
	require.NoError(t, a.Ask(context.Background(), Request{Question: "q"}, func(e Event) error {
		events = append(events, e)
		return nil
	}))
	d = doneOf(t, events)
	assert.Equal(t, StatusOff, d.Status)
	assert.Equal(t, "ollama can't stream answers", d.Reason)
	st = a.State()
	assert.True(t, st.On)
	assert.True(t, st.NoStream, "said as a fact, for a client that words it itself")
	assert.False(t, st.Withheld)
	assert.Equal(t, "ollama can't stream answers", st.Reason, "healthz says it too")
	assert.False(t, r.a.State().NoStream, "off, its provider unasked")
	assert.False(t, newRig(t, nil).a.State().NoStream, "a provider that streams")
}

// noStream is a generator without a stream.
type noStream struct{}

func (noStream) Generate(context.Context, string, generator.Options) (string, error) { return "", nil }
func (noStream) Model() string                                                       { return "m" }
func (noStream) Ping(context.Context) error                                          { return nil }

// TestAsk_Withheld: a withheld ask sends nothing anywhere: no retrieval,
// no model call, the reason naming the role's key; logged once at WARN.
func TestAsk_Withheld(t *testing.T) {
	why := "questions and pages would leave this machine for http://gpu.example:11434, generation.base_url, " +
		"which the answers role doesn't name; set generation.roles.answers.base_url to send them there, or " +
		"ask.enabled: false"
	r := newRig(t, func(o *Options) { o.Withheld = why })
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		t.Error("the model was asked")
		return nil, generator.StreamEnd{}, nil
	}
	d := doneOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, StatusWithheld, d.Status)
	assert.Equal(t, why, d.Reason)
	assert.Empty(t, r.ret.queries)
	assert.Empty(t, r.writer.prompts)
	assert.Equal(t, why, r.a.State().Reason)
	assert.True(t, r.a.State().Withheld)
	assert.False(t, r.a.State().OffByDefault)
	warns := 0
	for _, rec := range r.records(t) {
		if rec["level"] == "WARN" {
			warns++
		}
	}
	assert.Equal(t, 1, warns)
}

// TestAsk_Unfit: a model that fails its check answers nothing, now or
// later, and isn't asked again.
func TestAsk_Unfit(t *testing.T) {
	r := newRig(t, nil)
	r.writer.probe = func(string) string { return "AARDVARK [1]" }
	d := doneOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, StatusUnfit, d.Status)
	assert.Contains(t, d.Reason, `gemma4:26b didn't pass the check "one source answers"`)
	assert.Contains(t, d.Reason, UnfitFix)
	probes := len(r.writer.prompts)

	d = doneOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, StatusUnfit, d.Status)
	assert.Len(t, r.writer.prompts, probes, "not asked again")
	assert.Len(t, r.ret.queries, 0, "nothing retrieved for an unfit model")
}

// TestAsk_UnavailableMidAnswer: a stream cut mid-answer keeps the pieces
// sent, marks the answer partial and says why.
func TestAsk_UnavailableMidAnswer(t *testing.T) {
	r := newRig(t, nil)
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		return []string{"Partitions are "}, generator.StreamEnd{},
			fmt.Errorf("ollama generate: %w: nothing for 30s", generator.ErrStalled)
	}
	events := r.ask(t, Request{Question: "q"})
	d := doneOf(t, events)
	assert.Equal(t, StatusUnavailable, d.Status)
	assert.Contains(t, d.Reason, "stopped mid-reply")
	assert.True(t, d.Partial)
	assert.Equal(t, "Partitions are ", text(events))

	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		return nil, generator.StreamEnd{}, fmt.Errorf("ollama generate: %w", generator.ErrModelMissing)
	}
	d = doneOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, StatusUnavailable, d.Status)
	assert.Equal(t, "gemma4:26b isn't pulled — run curio up", d.Reason)
	assert.False(t, d.Partial)
}

// tooLong is a provider's refusal of a prompt past the model's context.
func tooLong() error {
	return fmt.Errorf("ollama generate: HTTP 400: request (9000 tokens) exceeds the available context size "+
		"(8192 tokens): %w", generator.ErrPromptTooLong)
}

// TestAsk_PromptTooLong: a prompt the provider refuses as past the
// writer's context is asked once more without its last source, every
// other source's block as it was; refused again, the ask ends
// unavailable, quoting the provider's counts and naming no setting to
// change (Ollama counts the prompt alone, which fills the context by
// construction). The retry and the end are each noted at WARN once under
// its own cause until an answer, at DEBUG after.
func TestAsk_PromptTooLong(t *testing.T) {
	r := newRig(t, nil)
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		return nil, generator.StreamEnd{}, tooLong()
	}
	events := r.ask(t, Request{Question: "q"})
	d := doneOf(t, events)
	assert.Equal(t, StatusUnavailable, d.Status)
	assert.Contains(t, d.Reason, "the pages' text ran past the answer writer's context (ollama generate: HTTP 400: "+
		"request (9000 tokens) exceeds the available context size (8192 tokens)")
	assert.True(t, strings.HasSuffix(d.Reason, "): it takes more tokens than curio counts for any text, which no "+
		"setting changes"), d.Reason)
	asks := r.writer.asks()
	require.Len(t, asks, 2, "asked again once")
	n := len(retrievedOf(t, events).Sources)
	require.Greater(t, n, 1)
	last := fmt.Sprintf("\n\n<source n=\"%d\">", n)
	require.Contains(t, asks[0], last)
	i, j := strings.Index(asks[0], last), strings.Index(asks[0], "</source>\n\nQuestion: ")
	assert.Equal(t, asks[0][:i]+asks[0][j+len("</source>"):], asks[1], "the same prompt, less its last block")

	r.ask(t, Request{Question: "q"})
	causes := map[string][]string{}
	for _, rec := range r.records(t) {
		if strings.Contains(fmt.Sprint(rec["msg"]), "past the answer writer's context") {
			cause := fmt.Sprint(rec["cause"])
			causes[cause] = append(causes[cause], fmt.Sprint(rec["level"]))
		}
	}
	assert.Equal(t, []string{"WARN", "DEBUG"}, causes["unavailable:too_long"], "the ask's end noted once")
	assert.Equal(t, []string{"WARN", "DEBUG"}, causes["retry:too_long"], "the retry noted once, under its own cause")
	assert.Len(t, causes, 2)

	// An answer clears the causes noted: the next refusal is warned of again.
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		return []string{"Partitions are shared [1]."}, generator.StreamEnd{}, nil
	}
	assert.Equal(t, StatusAnswered, doneOf(t, r.ask(t, Request{Question: "q"})).Status)
	var refused bool
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		if !refused {
			refused = true
			return nil, generator.StreamEnd{}, tooLong()
		}
		return []string{"Partitions are shared [1]."}, generator.StreamEnd{}, nil
	}
	assert.Equal(t, StatusAnswered, doneOf(t, r.ask(t, Request{Question: "q"})).Status)
	var retries []string
	for _, rec := range r.records(t) {
		if rec["cause"] == "retry:too_long" {
			retries = append(retries, fmt.Sprint(rec["level"]))
		}
	}
	assert.Equal(t, []string{"WARN", "DEBUG", "WARN"}, retries)
}

// TestAsk_PromptTooLongThenAnswered: refused, then answered without the
// last source: the answer cites what the shorter prompt numbered, and a
// citation of the source left out is invalid.
func TestAsk_PromptTooLongThenAnswered(t *testing.T) {
	r := newRig(t, nil)
	var refused bool
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		if !refused {
			refused = true
			return nil, generator.StreamEnd{}, tooLong()
		}
		return []string{"Partitions are shared [1]", " and moved [9]."}, generator.StreamEnd{}, nil
	}
	events := r.ask(t, Request{Question: "q"})
	d := doneOf(t, events)
	assert.Equal(t, StatusAnswered, d.Status)
	assert.Equal(t, []int{1}, d.Citations)
	assert.Equal(t, []string{"9"}, d.InvalidCitations)
	assert.Len(t, r.writer.asks(), 2)
	assert.Equal(t, "Partitions are shared [1] and moved [9].", text(events))
}

// TestAsk_PromptTooLongOneSource: a prompt of one source refused is not
// asked again.
func TestAsk_PromptTooLongOneSource(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Sources = 1 })
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		return nil, generator.StreamEnd{}, tooLong()
	}
	assert.Equal(t, StatusUnavailable, doneOf(t, r.ask(t, Request{Question: "q"})).Status)
	assert.Len(t, r.writer.asks(), 1)
}

// TestAsk_Timings: the answer route times its reading of the sources and
// the writer's reading of the prompt, and judges nothing.
func TestAsk_Timings(t *testing.T) {
	r := newRig(t, nil)
	r.writer.usage = generator.Usage{PromptEval: 1500 * time.Millisecond}
	d := doneOf(t, r.ask(t, Request{Question: "q"}))
	assert.Positive(t, d.Timings.Sources)
	assert.Equal(t, 1500*time.Millisecond, d.Timings.PromptEval)
	assert.Zero(t, d.Timings.Judge)
}

// TestAsk_KindDoesNotRoute: the planner's kind never picks the route: a
// question the words send to the answer route stays there whatever kind
// its plan has.
func TestAsk_KindDoesNotRoute(t *testing.T) {
	for _, kind := range []understand.Kind{understand.KindEvidence, understand.KindList, understand.KindAnalysis} {
		r := newRig(t, nil)
		r.ret.got.Plan = &understand.Plan{Kind: kind}
		r.ret.got.Understanding = &retrieve.Understanding{Status: understand.StatusPlanned}
		events := r.ask(t, Request{Question: "how does raft elect a leader", Understand: true})
		assert.Equal(t, RouteAnswer, retrievedOf(t, events).Route, kind)
		assert.Equal(t, RouteAnswer, doneOf(t, events).Route, kind)
	}
}

// TestAsk_RerankFromOptions: a request that leaves the rerank to the
// daemon reranks as ask.rerank says; one that says reranks so.
func TestAsk_RerankFromOptions(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Rerank = false })
	r.ask(t, Request{Question: "q"})
	r.ask(t, Request{Question: "q", Rerank: new(true)})
	require.Len(t, r.ret.queries, 2)
	assert.False(t, r.ret.queries[0].Rerank, "ask.rerank: false")
	assert.True(t, r.ret.queries[1].Rerank, "the request's own")
}

// TestAsk_LoadFails: a model that won't load ends the ask unavailable,
// saying why, with nothing retrieved or written.
func TestAsk_LoadFails(t *testing.T) {
	r := newRig(t, nil)
	r.writer.loaded = false
	r.writer.warmErr = errors.New("model requires more memory than is available")
	events := r.ask(t, Request{Question: "q"})
	d := doneOf(t, events)
	assert.Equal(t, StatusUnavailable, d.Status)
	assert.Equal(t, "loading gemma4:26b failed: model requires more memory than is available", d.Reason)
	assert.Equal(t, Progress{Stage: StageLoading, Model: "gemma4:26b"}, events[0], "the load was waited for")
	assert.Empty(t, r.writer.prompts, "nothing written, not even a probe")
	assert.Empty(t, r.ret.queries, "nothing retrieved")
}

// TestAsk_EvictionHold: a model Ollama unloads within its keep-alive
// twice in an hour holds Ask: the next ask ends unavailable, saying so,
// with nothing retrieved or written, and healthz's state names the hold.
func TestAsk_EvictionHold(t *testing.T) {
	r := newRig(t, func(o *Options) { o.KeepAlive = 30 * time.Minute })
	r.fit(t)
	r.writer.unload()
	require.Equal(t, StatusAnswered, doneOf(t, r.ask(t, Request{Question: "q"})).Status,
		"the first eviction loads the model again")
	asked, retrieved := len(r.writer.asks()), len(r.ret.queries)

	r.writer.unload()
	d := doneOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, StatusUnavailable, d.Status)
	assert.Equal(t, "Ollama unloaded gemma4:26b within its keep-alive 2 times, for other models; curio loads it "+
		"again from 15:15", d.Reason)
	assert.Len(t, r.writer.asks(), asked, "held: no prompt sent")
	assert.Len(t, r.ret.queries, retrieved, "held: nothing retrieved")
	assert.Equal(t, time.Date(2025, 4, 17, 15, 15, 0, 0, time.UTC), r.a.State().EvictionHoldUntil)
}

// blockUntilDone answers with one piece, then waits for its context.
func blockUntilDone(ctx context.Context, _ string) ([]string, generator.StreamEnd, error) {
	<-ctx.Done()
	return []string{"Partly "}, generator.StreamEnd{}, fmt.Errorf("ollama generate: %w", ctx.Err())
}

// TestAsk_Timeout: an answer the ask's deadline cuts ends timeout, its
// pieces kept; three in a row hold Ask, which then answers timeout at
// once.
func TestAsk_Timeout(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Timeout = 50 * time.Millisecond })
	r.fit(t)
	r.writer.answer = blockUntilDone
	for range 3 {
		events := r.ask(t, Request{Question: "q"})
		d := doneOf(t, events)
		assert.Equal(t, StatusTimeout, d.Status)
		assert.Equal(t, "the ask took longer than 50ms (ask.timeout_seconds)", d.Reason)
		assert.True(t, d.Partial)
		assert.Equal(t, "Partly ", text(events))
	}
	asked := len(r.writer.asks())
	d := doneOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, StatusTimeout, d.Status)
	assert.Contains(t, d.Reason, "3 answers in a row took longer than 50ms")
	assert.Len(t, r.writer.asks(), asked, "held: not asked")
	assert.False(t, r.a.State().TimeoutHoldUntil.IsZero())
}

// TestAsk_TimeoutAfterAWaitHoldsNothing: a deadline met by an ask that
// spent part of its time waiting for another answer says nothing of the
// model's speed: no run of them holds Ask.
func TestAsk_TimeoutAfterAWaitHoldsNothing(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Timeout = 300 * time.Millisecond })
	r.fit(t)
	for range 4 {
		release, holding := make(chan struct{}), make(chan struct{})
		r.writer.answer = func(ctx context.Context, p string) ([]string, generator.StreamEnd, error) {
			if strings.HasSuffix(p, "Question: first\n") {
				close(holding)
				<-release
				return []string{"ok [1]"}, generator.StreamEnd{}, nil
			}
			return blockUntilDone(ctx, p)
		}
		first := make(chan error)
		go func() {
			_, err := r.askErr(context.Background(), Request{Question: "first"})
			first <- err
		}()
		<-holding
		var events []Event
		require.NoError(t, r.a.Ask(context.Background(), Request{Question: "second"}, func(e Event) error {
			events = append(events, e)
			if p, ok := e.(Progress); ok && p.Stage == StageWaiting {
				close(release)
			}
			return nil
		}))
		require.NoError(t, <-first)
		assert.Equal(t, StatusTimeout, doneOf(t, events).Status)
	}
	assert.True(t, r.a.State().TimeoutHoldUntil.IsZero(), "no hold")
}

// TestAsk_TimeoutWhileShared: an answer is a long call of its model, which
// another feature reads while it is written (a plan asked meanwhile waits
// for it in Ollama's queue); and an answer the ask's deadline cuts while
// another feature called the model shared it, which says nothing certain
// of the model's speed: no run of them holds Ask.
func TestAsk_TimeoutWhileShared(t *testing.T) {
	traffic := &modelgate.Traffic{}
	server := "http://localhost:11434"
	r := newRig(t, func(o *Options) {
		o.Timeout, o.Traffic, o.Model.BaseURL = 50*time.Millisecond, traffic, server
	})
	r.fit(t)
	planner := modelgate.New(context.Background(), nil, modelgate.Options{Model: "gemma4:26b", Server: server,
		Traffic: traffic})
	r.writer.answer = func(ctx context.Context, p string) ([]string, generator.StreamEnd, error) {
		assert.True(t, planner.HeldSince(time.Now()), "the answer holds the model")
		end := planner.Use()
		defer end()
		return blockUntilDone(ctx, p)
	}
	for range 4 {
		assert.Equal(t, StatusTimeout, doneOf(t, r.ask(t, Request{Question: "q"})).Status)
	}
	assert.Zero(t, r.a.State().TimeoutHoldUntil, "no hold")
}

// TestAsk_DaemonStops: the daemon's stop mid-answer cancels the
// generation and ends the ask unavailable, saying so.
func TestAsk_DaemonStops(t *testing.T) {
	r := newRig(t, nil)
	started := make(chan struct{})
	r.writer.answer = func(ctx context.Context, p string) ([]string, generator.StreamEnd, error) {
		close(started)
		return blockUntilDone(ctx, p)
	}
	go func() {
		<-started
		r.cancel()
	}()
	d := doneOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, StatusUnavailable, d.Status)
	assert.Equal(t, "the daemon is stopping; ask again once it has restarted", d.Reason)
	assert.True(t, d.Partial)
}

// TestAsk_CallerLeaves: the caller's context ending is its error, and the
// generation is cancelled.
func TestAsk_CallerLeaves(t *testing.T) {
	r := newRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan struct{})
	r.writer.answer = func(c context.Context, _ string) ([]string, generator.StreamEnd, error) {
		cancel()
		<-c.Done()
		close(cancelled)
		return nil, generator.StreamEnd{}, c.Err()
	}
	_, err := r.askErr(ctx, Request{Question: "q"})
	require.ErrorIs(t, err, context.Canceled)
	<-cancelled
}

// TestAsk_EmitFails: an emit that fails, a client gone, stops the ask with
// its error.
func TestAsk_EmitFails(t *testing.T) {
	r := newRig(t, nil)
	gone := errors.New("client gone")
	err := r.a.Ask(context.Background(), Request{Question: "q"}, func(e Event) error {
		if _, ok := e.(Delta); ok {
			return gone
		}
		return nil
	})
	require.ErrorIs(t, err, gone)
}

// TestAsk_StoreFails: a store that fails is the ask's error.
func TestAsk_StoreFails(t *testing.T) {
	r := newRig(t, nil)
	r.stores.err = errBoom
	_, err := r.askErr(context.Background(), Request{Question: "q"})
	require.ErrorIs(t, err, errBoom)
}

// TestAsk_OneAnswerAtATime: an ask waits for another's answer to finish,
// saying so, and counts no timeout for the wait.
func TestAsk_OneAnswerAtATime(t *testing.T) {
	r := newRig(t, nil)
	release := make(chan struct{})
	first := make(chan struct{})
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		select {
		case <-first:
		default:
			close(first)
			<-release
		}
		return []string{"ok [1]"}, generator.StreamEnd{}, nil
	}
	done := make(chan []Event)
	go func() {
		events, _ := r.askErr(context.Background(), Request{Question: "one"})
		done <- events
	}()
	<-first
	waiting := make(chan struct{})
	second := make(chan []Event)
	go func() {
		var events []Event
		_ = r.a.Ask(context.Background(), Request{Question: "two"}, func(e Event) error {
			events = append(events, e)
			if p, ok := e.(Progress); ok && p.Stage == StageWaiting {
				close(waiting)
			}
			return nil
		})
		second <- events
	}()
	<-waiting
	close(release)
	assert.Equal(t, StatusAnswered, doneOf(t, <-done).Status)
	assert.Equal(t, StatusAnswered, doneOf(t, <-second).Status)
}

// TestAsk_LogsNoContent: no record carries the question, a source's text
// or the answer.
func TestAsk_LogsNoContent(t *testing.T) {
	r := newRig(t, nil)
	r.writer.loaded = false
	r.ret.got.Result.Items = []search.Hit{hit("d1", "https://example.com/z", "Quokka habits", store.DocStateFetched,
		"The quokka naps through the afternoon.")}
	r.writer.answer = answers("The quokka naps [1].")
	r.ask(t, Request{Question: "when does a quokka nap"})
	r.writer.answer = func(context.Context, string) ([]string, generator.StreamEnd, error) {
		return []string{"The quokka "}, generator.StreamEnd{}, fmt.Errorf("%w: quokka", generator.ErrUnavailable)
	}
	r.ask(t, Request{Question: "when does a quokka nap"})
	r.a.Wait()
	records := r.records(t)
	require.NotEmpty(t, records)
	var debug bool
	for _, rec := range records {
		debug = debug || rec["msg"] == "ask: an answer"
		for k, v := range rec {
			if k == "err" {
				continue // the provider's message, not the ask's content
			}
			assert.NotContains(t, strings.ToLower(fmt.Sprint(v)), "quokka", "%s in %v", k, rec)
		}
	}
	assert.True(t, debug, "one DEBUG line an ask")
}

// TestValidate: what a request may hold.
func TestValidate(t *testing.T) {
	plan := &understand.Plan{Kind: understand.KindList, Filters: search.FilterRequest{SavedAfter: "2024-01-01"}}
	for _, tc := range []struct {
		name string
		req  Request
		want string
	}{
		{"a question", Request{Question: "q"}, ""},
		{"empty", Request{Question: "  "}, "question is required"},
		{"too long", Request{Question: strings.Repeat("x", 513)}, "question is 513 bytes, at most 512"},
		{"sources at the bounds", Request{Question: "q", Sources: 12}, ""},
		{"too many sources", Request{Question: "q", Sources: 13}, "sources must be between 1 and 12"},
		{"negative sources", Request{Question: "q", Sources: -1}, "sources must be between 1 and 12"},
		{"a bad filter", Request{Question: "q", Filters: search.FilterRequest{SavedAfter: "soon"}},
			"filters.saved_after"},
		{"a plan and understand", Request{Question: "q", Plan: plan, Understand: true},
			"send understand or plan, not both"},
		{"a plan refused", Request{Question: "q", Plan: &understand.Plan{Kind: "summary"}}, "plan.kind"},
		{"a plan's filters that won't merge", Request{Question: "q", Plan: plan,
			Filters: search.FilterRequest{IncludeUndated: true}}, "filters.include_undated"},
		{"an order and rerank", Request{Question: "q", Rerank: new(true), RerankOrder: []string{"a"}},
			"send rerank or rerank_order, not both"},
		{"an order, rerank left to the daemon", Request{Question: "q", RerankOrder: []string{"a"}}, ""},
		{"the answer mode", Request{Question: "q", Mode: RouteAnswer}, ""},
		{"the evidence mode", Request{Question: "q", Mode: RouteEvidence}, ""},
		{"the interests mode", Request{Question: "q", Mode: RouteInterests}, ""},
		{"the list mode", Request{Question: "q", Mode: RouteList}, ""},
		{"the interests mode with its window", Request{Question: "q", Mode: RouteInterests,
			Filters: search.FilterRequest{SavedAfter: "2024-01-01", SavedBefore: "2025-01-01"}}, ""},
		{"the interests mode with a host", Request{Question: "q", Mode: RouteInterests,
			Filters: search.FilterRequest{Host: []string{"example.com"}}},
			"mode interests counts saves by date alone: filters.host isn't taken"},
		{"the interests mode with include_undated", Request{Question: "q", Mode: RouteInterests,
			Filters: search.FilterRequest{SavedAfter: "2024-01-01", IncludeUndated: true}},
			"filters.include_undated isn't taken"},
		{"the interests mode with an order", Request{Question: "q", Mode: RouteInterests, Rerank: new(false),
			RerankOrder: []string{"a"}}, "mode interests reads no ranking: rerank_order isn't taken"},
		{"no route", Request{Question: "q", Mode: "essay"},
			`mode: "essay" isn't a route; the routes are: answer, evidence, interests, list`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
			_, ok := errors.AsType[*RequestError](err)
			assert.True(t, ok)
		})
	}
}

// TestState_OffFacts: State says off by default and withheld as facts:
// off by default for ask.enabled left out and an unmeasured model; never
// for ask.enabled: false, or Ask on; withheld for an asker on whose
// questions may not leave, never for one that is off.
func TestState_OffFacts(t *testing.T) {
	measured := config.Default()
	measured.Generation.Model = "gemma4:26b"
	off := config.Default()
	off.Ask.Enabled = new(false)
	withheld := measured
	withheld.Generation.BaseURL = "http://gpu.example:11434"
	offWithheld := withheld
	offWithheld.Ask.Enabled = new(false)
	for _, tc := range []struct {
		name                   string
		cfg                    config.Config
		offByDefault, withheld bool
	}{
		{"ask.enabled left out, an unmeasured model", config.Default(), true, false},
		{"ask.enabled: false", off, false, false},
		{"on by default", measured, false, false},
		{"withheld", withheld, false, true},
		{"off and withheld", offWithheld, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := OptionsFrom(tc.cfg)
			r := newRig(t, func(o *Options) {
				o.On, o.ByDefault, o.OffByDefault, o.OffReason, o.Withheld = opts.On, opts.ByDefault,
					opts.OffByDefault, opts.OffReason, opts.Withheld
			})
			st := r.a.State()
			assert.Equal(t, tc.offByDefault, st.OffByDefault)
			assert.Equal(t, tc.withheld, st.Withheld)
			assert.Equal(t, cmp.Or(opts.OffReason, opts.Withheld), st.Reason)
		})
	}
}

// TestSavedTitle: an untitled document's saved title is its newest
// titled bookmark's, ties to the higher ID, as the store lists them,
// trimmed; a title blank of ASCII whitespace is passed over, one of other
// spaces chosen and trimmed to nothing, as the search page shows it; a
// document with a title of its own has none.
func TestSavedTitle(t *testing.T) {
	at := time.Date(2025, 4, 17, 15, 0, 0, 0, time.UTC)
	bm := func(id, title string, saved time.Time) *store.Bookmark {
		return &store.Bookmark{ID: id, Title: &title, SavedAt: saved}
	}
	for _, tc := range []struct {
		name      string
		own       string
		bookmarks []*store.Bookmark
		want      string
	}{
		{"the newest saved", "", []*store.Bookmark{bm("b1", "Older", at.Add(-time.Hour)), bm("b2", " Newer\n", at)},
			"Newer"},
		{"ties to the higher ID", "", []*store.Bookmark{bm("b2", "Higher", at), bm("b1", "Lower", at)}, "Higher"},
		{"ties whatever the order", "", []*store.Bookmark{bm("b1", "Lower", at), bm("b2", "Higher", at)}, "Higher"},
		{"blank of ASCII whitespace passed over", "",
			[]*store.Bookmark{bm("b1", "Real", at.Add(-time.Hour)), bm("b2", " \t\n\v\f\r", at)}, "Real"},
		{"other spaces chosen", "",
			[]*store.Bookmark{bm("b1", "Real", at.Add(-time.Hour)), bm("b2", "\u00a0", at)}, ""},
		{"no title", "", []*store.Bookmark{{ID: "b1", SavedAt: at}}, ""},
		{"no bookmark", "", nil, ""},
		{"a title of its own", "Own", []*store.Bookmark{bm("b1", "Saved", at)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, savedTitle(tc.own, tc.bookmarks))
		})
	}
}

// TestAsk_NamesAsTheStoreNamesThem: an answer's pages are named as every
// list of the dashboard names them, by their own title or the bookmark
// the store names an untitled document by (GetByIDsWithLastError's, the
// search page's and the Library's), through the real store's bookmarks as
// ListByDocuments returns them: the newest save, ties at one saved time
// to the higher ID, a title blank of ASCII whitespace passed over and one
// of other spaces chosen, then trimmed as the search page trims it.
func TestAsk_NamesAsTheStoreNamesThem(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.NewDB(t)
	docs, bookmarks := sqlite.NewDocuments(db), sqlite.NewBookmarks(db)
	at := time.Date(2025, 4, 17, 15, 0, 0, 0, time.UTC)
	type save struct {
		id    string
		title *string
		at    time.Time
	}
	pages := []struct {
		own   string
		saves []save
		want  string
	}{
		{"", []save{{"b1", new("Older"), at.Add(-time.Hour)}, {"b2", new(" Newer\n"), at}}, "Newer"},
		{"", []save{{"b4", new("Higher"), at}, {"b3", new("Lower"), at}}, "Higher"},
		{"", []save{{"b5", new("Real"), at.Add(-time.Hour)}, {"b6", new(" \t\n\v\f\r"), at}}, "Real"},
		{"", []save{{"b7", new("Real"), at.Add(-time.Hour)}, {"b8", new(" "), at}}, ""},
		{"", []save{{"b9", nil, at}}, ""},
		{"Its own", []save{{"b10", new("Saved"), at}}, ""},
	}
	hits := make([]search.Hit, 0, len(pages))
	ids := make([]string, 0, len(pages))
	for i, p := range pages {
		doc := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/%d", i),
			State: store.DocStateFetched}
		require.NoError(t, docs.Create(ctx, doc))
		_, err := db.ExecContext(ctx, `UPDATE documents SET title = ? WHERE id = ?`, p.own, doc.ID)
		require.NoError(t, err)
		for j, s := range p.saves {
			require.NoError(t, bookmarks.Create(ctx, &store.Bookmark{ID: s.id, TenantID: "local", DocumentID: &doc.ID,
				URL: doc.URL, Title: s.title, SavedAt: s.at, Source: []string{"chrome", "safari"}[j]}))
		}
		hits = append(hits, hit(doc.ID, doc.URL, p.own, store.DocStateFetched, "a passage"))
		ids = append(ids, doc.ID)
	}
	named, err := docs.GetByIDsWithLastError(ctx, "local", ids)
	require.NoError(t, err)
	storeNames := map[string]string{}
	for _, d := range named {
		storeNames[d.ID] = strings.TrimSpace(d.BookmarkTitle)
	}

	r := newRigReading(t, nil, bookmarks)
	r.ret.got.Result.Items = hits
	got := retrievedOf(t, r.ask(t, Request{Question: "q", Sources: len(pages)}))
	require.Len(t, got.Sources, len(pages))
	for i, s := range got.Sources {
		assert.Equal(t, pages[i].want, s.SavedTitle, "page %d", i)
		assert.Equal(t, storeNames[s.DocID], s.SavedTitle, "page %d: as the store names it", i)
		assert.Equal(t, pages[i].own, s.OwnTitle, "page %d", i)
	}
}
