package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/store"
)

// fakeModel is a scripted JSONGenerator and Warmer: each call is answered
// by answer, from the prompt; it records the prompts and options.
type fakeModel struct {
	mu        sync.Mutex
	answer    func(prompt string) (string, error)
	prompts   []string
	options   []generator.Options
	loaded    bool
	loadedErr error
	warmErr   error
	pingErr   error
	warms     int
	warmedFor []time.Duration // each load's keep-alive
	unloads   int
}

func (*fakeModel) Generate(context.Context, string, generator.Options) (string, error) {
	return "", errors.New("prose is never asked for")
}

func (f *fakeModel) GenerateJSON(ctx context.Context, prompt string, _ json.RawMessage,
	opts generator.Options) (json.RawMessage, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.options = append(f.options, opts)
	answer := f.answer
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reply, err := answer(prompt)
	if opts.Usage != nil {
		*opts.Usage = generator.Usage{PromptTokens: len(prompt) / 4, OutputTokens: len(reply) / 4}
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(reply), nil
}

func (*fakeModel) Model() string                { return "gemma4:26b" }
func (f *fakeModel) Ping(context.Context) error { return f.pingErr }

func (f *fakeModel) Loaded(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loaded, f.loadedErr
}

func (f *fakeModel) Warm(_ context.Context, keepAlive time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warms++
	f.warmedFor = append(f.warmedFor, keepAlive)
	if f.warmErr == nil {
		f.loaded = true
	}
	return f.warmErr
}

func (f *fakeModel) Unload(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unloads++
	f.loaded = false
	return nil
}

func (f *fakeModel) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

// fit answers each probe as a fit model does, and every other page with
// page.
func fit(page string) func(string) (string, error) {
	return func(prompt string) (string, error) {
		switch {
		case strings.Contains(prompt, "Kestrel K2"):
			return `{"kind":"article","site":"","author":"Marisol Fenwick-Oduya","about":"A crawler ships.",` +
				`"entities":["Tidewater Robotics","Kestrel K2","Ignatius Varga-Lindqvist"],"claims":[],"descriptions":[]}`, nil
		case strings.Contains(prompt, "Title: Sign in\n"):
			return `{"kind":"not_the_page","site":"","author":"","about":"","entities":[],"claims":[],"descriptions":[]}`, nil
		case strings.Contains(prompt, "Castellbo"):
			return `{"kind":"profile","site":"","author":"Oriane Castellbo-Ferrant, a glaciologist","about":"A bio.",` +
				`"entities":[],"claims":[],"descriptions":[]}`, nil
		}
		return page, nil
	}
}

const goodAnswer = `{"kind":"article","site":" Example Times, a made-up newspaper ","author":"Bo Example",` +
	`"about":"Why queues back up.","entities":["Zephyrine","zephyrine","Kafka","Mars Base Nine",""],` +
	`"claims":["Backpressure beats buffering.","backpressure  beats buffering."],"descriptions":["a queues piece"]}`

func page() Input {
	return Input{DocumentID: "doc", Title: "Queues back up", URL: "https://example.com/queues", Site: "Example Times",
		ContentType: "article", Text: "Zephyrine wrote that Kafka consumers fall behind."}
}

func newWriter(t *testing.T, model *fakeModel, opts Options) (*Writer, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	opts.On = true
	opts.Model = config.ModelSpec{Provider: "ollama", Model: "gemma4:26b"}
	opts.Log = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return New(model, opts), &logs
}

// TestWrite: a fit model's summary is decoded, normalized and grounded:
// texts trimmed, repeats and empties dropped, an entity the input doesn't
// hold left out and counted; the call is greedy, capped, with the
// provider's keep-alive, and reports its usage.
func TestWrite(t *testing.T) {
	model := &fakeModel{loaded: true, answer: fit(goodAnswer)}
	w, _ := newWriter(t, model, Options{})
	got, err := w.Write(context.Background(), page())
	require.NoError(t, err)
	assert.Equal(t, store.Summary{Kind: store.SummaryKindArticle, Site: "Example Times, a made-up newspaper",
		Author: "Bo Example", About: "Why queues back up.", Entities: []string{"Zephyrine", "Kafka"},
		Claims: []string{"Backpressure beats buffering."}, Descriptions: []string{"a queues piece"},
		SummaryWriter: store.SummaryWriter{Model: "gemma4:26b", PromptVersion: PromptVersion},
		InputHash:     page().Hash()}, got.Summary)
	assert.Equal(t, 1, got.Dropped, "Mars Base Nine isn't in the input")
	assert.Positive(t, got.Usage.PromptTokens)
	assert.Equal(t, 4, model.calls(), "three probes, then the page")
	opts := model.options[3]
	assert.Equal(t, 0.0, *opts.Temperature)
	assert.Equal(t, maxAnswerTokens, opts.MaxTokens)
	assert.Zero(t, opts.KeepAlive, "none set: the provider's keep-alive")
	assert.Equal(t, CheckFit, w.State().Check)
	_, err = w.Write(context.Background(), page())
	require.NoError(t, err)
	assert.Equal(t, 5, model.calls(), "the verdict lasts: no probe again")
}

// TestWrite_NotThePage: a page that isn't the one saved keeps no text,
// whatever the model wrote.
func TestWrite_NotThePage(t *testing.T) {
	model := &fakeModel{loaded: true, answer: fit(`{"kind":"not_the_page","site":"Example","author":"x",` +
		`"about":"A sign-in form.","entities":["Kafka"],"claims":["c"],"descriptions":["d"]}`)}
	w, _ := newWriter(t, model, Options{})
	got, err := w.Write(context.Background(), page())
	require.NoError(t, err)
	assert.Equal(t, store.Summary{Kind: store.SummaryKindNotThePage,
		SummaryWriter: w.Identity(), InputHash: page().Hash()}, got.Summary)
}

// TestGrounded: an entity is grounded in what the model was sent as a
// whole word where it begins or ends with a letter of a spaced script or a
// digit, and anywhere at an edge of a script whose words aren't spaced.
func TestGrounded(t *testing.T) {
	for _, tc := range []struct {
		entity, text string
		want         bool
	}{
		{"AI", "She said it twice.", false},
		{"AI", "AI is the subject.", true},
		{"Go", "A good read.", false},
		{"Go", "Written in Go, mostly.", true},
		{"Kafka", "Kafka's consumers.", true},
		{"Fenwick-Oduya", "by Marisol Fenwick Oduya", true},
		{"Mars Base", "the marsbase blog", false},
		{"K2", "the Kestrel K2 crawler", true},
		{"2", "the Kestrel K2 crawler", false},
		{"C++", "notes on C++ templates", true},
		{"Example", "https://example.com/queues", true},
		{"Привет", "Приветствие", false},
		{"東京", "東京都の地図", true},
		{"—", "a dash — here", false},
	} {
		got := grounded(source(Input{Text: tc.text}), tc.entity)
		assert.Equal(t, tc.want, got, "%q in %q", tc.entity, tc.text)
	}
}

// TestDecodeAnswer_Invalid: every answer the strict decoding refuses is an
// *InvalidAnswer naming its rule, and quoting none of the answer.
func TestDecodeAnswer_Invalid(t *testing.T) {
	base := map[string]any{"kind": "article", "site": "", "author": "", "about": "", "entities": []string{},
		"claims": []string{}, "descriptions": []string{}}
	with := func(key string, v any) string {
		m := maps.Clone(base)
		m[key] = v
		b, err := json.Marshal(m)
		require.NoError(t, err)
		return string(b)
	}
	many := func(n, size int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strings.Repeat("x", size)
		}
		return out
	}
	for _, tc := range []struct {
		name, raw, rule string
	}{
		{"not JSON", `secret words`, "shape"},
		{"two objects", with("kind", "article") + `{}`, "shape"},
		{"an unknown key", `{"kind":"article","secret":"words"}`, "shape"},
		{"a wrong type", with("entities", "secret words"), "shape"},
		{"a kind outside the set", with("kind", "essay"), "kind"},
		{"too many entities", with("entities", many(maxEntities+1, 3)), "entities"},
		{"too many claims", with("claims", many(maxClaims+1, 3)), "claims"},
		{"too many descriptions", with("descriptions", many(maxDescriptions+1, 3)), "descriptions"},
		{"an about over its bytes", with("about", strings.Repeat("x", maxBytes(maxAboutChars)+1)), "about"},
		{"an entity over its bytes", with("entities", many(1, maxBytes(maxEntityChars)+1)), "entities"},
		{"a control character", with("site", "secret\u0007words"), "site"},
		{"a format character", with("author", "secret\u202ewords"), "author"},
		{"a description with a line break", with("descriptions", []string{"secret\nwords"}), "descriptions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeAnswer([]byte(tc.raw))
			ia, ok := errors.AsType[*InvalidAnswer](err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, tc.rule, ia.Rule)
			assert.NotContains(t, err.Error(), "secret")
		})
	}
	_, err := decodeAnswer([]byte(with("entities", many(maxEntities, maxBytes(maxEntityChars)))))
	require.NoError(t, err, "at every cap")
}

// TestCheck: the verdicts. A wrong answer, an invalid one or a request the
// provider refuses makes the model unfit for the process, freeing it
// unless another role shares it; a failure that says nothing of the model
// leaves the verdict unknown, to be checked again.
func TestCheck(t *testing.T) {
	wrongWall := func(prompt string) (string, error) {
		if strings.Contains(prompt, "Title: Sign in\n") {
			return `{"kind":"article","site":"","author":"","about":"A form.","entities":[],"claims":[],"descriptions":[]}`, nil
		}
		return fit(goodAnswer)(prompt)
	}
	invented := func(prompt string) (string, error) {
		if strings.Contains(prompt, "Kestrel K2") {
			return `{"kind":"article","site":"","author":"","about":"x","entities":["Kestrel K2","Atlantis Corp"],` +
				`"claims":[],"descriptions":[]}`, nil
		}
		return fit(goodAnswer)(prompt)
	}
	for _, tc := range []struct {
		name      string
		answer    func(string) (string, error)
		shared    bool
		wantErr   error
		check     string
		unloads   int
		outage    bool
		failCause string
	}{
		{"a wall read as an article", wrongWall, false, ErrNotWriting, CheckUnfit, 1, false, "unfit"},
		{"an entity the page doesn't name", invented, false, ErrNotWriting, CheckUnfit, 1, false, "unfit"},
		{"unfit and shared: kept loaded", wrongWall, true, ErrNotWriting, CheckUnfit, 0, false, "unfit"},
		{"an invalid answer", func(string) (string, error) { return `{"kind":"essay"}`, nil }, false, ErrNotWriting,
			CheckUnfit, 1, false, "unfit"},
		{"a refused request", func(string) (string, error) { return "", generator.ErrBadRequest }, false,
			ErrNotWriting, CheckUnfit, 1, false, "unfit"},
		{"no answer", func(string) (string, error) { return "", generator.ErrUnreachable }, false, ErrNotChecked,
			CheckUnchecked, 0, true, "unreachable"},
		{"a provider that can't serve now", func(string) (string, error) { return "", generator.ErrUnavailable },
			false, ErrNotChecked, CheckUnchecked, 0, false, "check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{loaded: true, answer: tc.answer}
			w, _ := newWriter(t, model, Options{Shared: tc.shared})
			_, err := w.Write(context.Background(), page())
			require.ErrorIs(t, err, tc.wantErr)
			st := w.State()
			assert.Equal(t, tc.check, st.Check)
			assert.Equal(t, tc.unloads, model.unloads)
			assert.Equal(t, tc.outage, !st.Outage.Since.IsZero())
			assert.Equal(t, tc.failCause, st.Failure.Cause)
			if tc.check == CheckUnfit {
				assert.Contains(t, st.Unfit, UnfitFix)
				assert.Contains(t, w.Refusal(), UnfitFix)
				assert.Equal(t, FeedNone, w.Feed())
				calls := model.calls()
				_, err := w.Write(context.Background(), page())
				require.ErrorIs(t, err, ErrNotWriting)
				assert.Equal(t, calls, model.calls(), "an unfit model is never asked again")
			}
		})
	}
}

// TestCheck_WaitsAfterNoVerdict: a check the model answers with an error,
// or too slowly while it answers a listing, reaches no verdict and holds
// the next for a minute, twice as long after each further one, at most 15
// minutes: a write meanwhile asks nothing, and the pass is fed one job.
// The episode is one WARN, and a verdict ends it.
func TestCheck_WaitsAfterNoVerdict(t *testing.T) {
	slow := fmt.Errorf("ollama generate: %w: %w", generator.ErrUnreachable, context.DeadlineExceeded)
	for name, failure := range map[string]error{"an error": generator.ErrUnavailable, "too slow": slow} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
			model := &fakeModel{loaded: true, answer: func(string) (string, error) { return "", failure }}
			w, logs := newWriter(t, model, Options{Now: func() time.Time { return now }})
			waits := make([]time.Duration, 0, 7)
			for range 7 {
				_, err := w.Write(context.Background(), page())
				we, ok := errors.AsType[*WaitError](err)
				require.True(t, ok, "%v", err)
				require.ErrorIs(t, err, ErrNotChecked)
				assert.True(t, we.Probed, "this write ran the check")
				waits = append(waits, we.Until.Sub(now))
				calls := model.calls()
				_, err = w.Write(context.Background(), page())
				we, ok = errors.AsType[*WaitError](err)
				require.True(t, ok, "%v", err)
				assert.False(t, we.Probed)
				assert.Equal(t, calls, model.calls(), "a write within the wait asks nothing")
				st := w.State()
				assert.Equal(t, CheckUnchecked, st.Check)
				assert.Equal(t, we.Until, st.CheckRetryAt)
				assert.Equal(t, we.Until, w.CheckRetryAt(), "the feed's job runs no sooner")
				assert.Equal(t, "check", st.Failure.Cause)
				assert.True(t, st.Outage.Since.IsZero(), "no outage: the model answers")
				assert.Equal(t, FeedOne, w.Feed(), "one job while the check waits")
				warms := model.warms
				err = w.Ready(context.Background())
				we, ok = errors.AsType[*WaitError](err)
				require.True(t, ok, "%v", err)
				assert.False(t, we.Probed)
				assert.Equal(t, warms, model.warms, "nor does a load within the wait")
				now = we.Until
				assert.True(t, w.State().CheckRetryAt.IsZero(), "past its time, a job kept behind a pause runs it")
				assert.True(t, w.CheckRetryAt().IsZero())
				assert.Equal(t, FeedOne, w.Feed(), "one job until a verdict")
			}
			assert.Equal(t, []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
				15 * time.Minute, 15 * time.Minute, 15 * time.Minute}, waits)
			assert.Equal(t, 7, model.calls(), "one probe a wait")
			assert.Equal(t, 1, strings.Count(logs.String(), `"level":"WARN"`), "one WARN an episode")

			model.answer = fit(goodAnswer)
			_, err := w.Write(context.Background(), page())
			require.NoError(t, err)
			st := w.State()
			assert.Equal(t, CheckFit, st.Check)
			assert.True(t, st.CheckRetryAt.IsZero(), "a verdict ends the wait")
			assert.Equal(t, FeedAll, w.Feed())
		})
	}
	t.Run("a timeout while the listing doesn't answer is an outage", func(t *testing.T) {
		model := &fakeModel{loaded: true, pingErr: generator.ErrUnreachable,
			answer: func(string) (string, error) { return "", slow }}
		w, _ := newWriter(t, model, Options{})
		_, err := w.Write(context.Background(), page())
		require.ErrorIs(t, err, ErrNotChecked)
		_, isWait := errors.AsType[*WaitError](err)
		assert.False(t, isWait)
		st := w.State()
		assert.False(t, st.Outage.Since.IsZero())
		assert.True(t, st.CheckRetryAt.IsZero(), "an outage's probe is its one job's deferrals")
		assert.Equal(t, FeedOne, w.Feed())
	})
}

// TestMeasure: a writer that measures never checks before a write, and
// writes on after Check finds the model unfit, which it still reports.
func TestMeasure(t *testing.T) {
	model := &fakeModel{loaded: true, answer: func(prompt string) (string, error) {
		if strings.Contains(prompt, "Title: Sign in\n") {
			return `{"kind":"article","site":"","author":"","about":"A form.","entities":[],"claims":[],"descriptions":[]}`, nil
		}
		return fit(goodAnswer)(prompt)
	}}
	w, _ := newWriter(t, model, Options{Measure: true})
	_, err := w.Write(context.Background(), page())
	require.NoError(t, err)
	assert.Equal(t, 1, model.calls(), "no check before the write")

	failed, err := w.Check(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, failed, "the check's verdict is reported")
	assert.Equal(t, CheckUnfit, w.State().Check)
	assert.Empty(t, w.Refusal(), "and stops no write")
	_, err = w.Write(context.Background(), page())
	require.NoError(t, err)
	assert.Zero(t, model.unloads, "nothing unloaded on a measured verdict")
}

// TestWrite_Failures: what a call's failure makes of the writer and the
// error it returns.
func TestWrite_Failures(t *testing.T) {
	timeout := fmt.Errorf("ollama generate: %w: %w", generator.ErrUnreachable, context.DeadlineExceeded)
	for _, tc := range []struct {
		name    string
		err     error
		pingErr error
		is      error
		outage  bool
		cause   string
	}{
		{"no answer", generator.ErrUnreachable, nil, generator.ErrUnreachable, true, "unreachable"},
		{"a missing model", generator.ErrModelMissing, nil, generator.ErrModelMissing, true, "missing"},
		{"a refused caller", generator.ErrUnauthorized, nil, generator.ErrUnauthorized, true, "unauthorized"},
		{"a timeout while it answers", timeout, nil, ErrSlow, false, "timeout"},
		{"a timeout while it doesn't", timeout, generator.ErrUnreachable, generator.ErrUnreachable, true,
			"unreachable"},
		{"a provider that can't serve now", generator.ErrUnavailable, nil, generator.ErrUnavailable, false,
			"unavailable"},
		{"cut off", generator.ErrTruncated, nil, generator.ErrTruncated, false, "invalid:truncated"},
		{"not JSON", fmt.Errorf("ollama generate: %w: %q", generator.ErrNotJSON, "secret words"), nil,
			generator.ErrNotJSON, false, "invalid:json"},
		{"a refused request", generator.ErrBadRequest, nil, generator.ErrBadRequest, false, "bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{loaded: true, pingErr: tc.pingErr}
			model.answer = fit(goodAnswer)
			w, _ := newWriter(t, model, Options{})
			require.NoError(t, w.ensureChecked(context.Background()))
			model.answer = func(string) (string, error) { return "", tc.err }
			_, err := w.Write(context.Background(), page())
			require.ErrorIs(t, err, tc.is)
			assert.NotContains(t, err.Error(), "secret", "the reply is never quoted")
			st := w.State()
			assert.NotContains(t, st.Failure.Err, "secret")
			assert.Equal(t, tc.outage, !st.Outage.Since.IsZero())
			switch {
			case tc.cause == OutageMissing:
				assert.Equal(t, FeedNone, w.Feed(), "a model not pulled: no job until a pull")
			case tc.outage:
				assert.Equal(t, FeedOne, w.Feed(), "an outage: one job, its probe")
			default:
				assert.Equal(t, FeedAll, w.Feed())
			}
			assert.Equal(t, tc.cause, st.Failure.Cause)
			assert.Equal(t, "doc", st.Failure.DocumentID)

			model.answer = fit(goodAnswer)
			_, err = w.Write(context.Background(), page())
			require.NoError(t, err)
			assert.True(t, w.State().Outage.Since.IsZero(), "an answer ends an outage")
		})
	}
	t.Run("the caller's context", func(t *testing.T) {
		model := &fakeModel{loaded: true, answer: fit(goodAnswer)}
		w, _ := newWriter(t, model, Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := w.Write(ctx, page())
		require.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, w.State().Failure.Cause, "nothing recorded")
		assert.Equal(t, CheckUnchecked, w.State().Check)
	})
}

// TestReady: a loaded model is left as it is; one that isn't is loaded;
// a provider that doesn't answer starts an outage.
func TestReady(t *testing.T) {
	model := &fakeModel{loaded: true}
	w, _ := newWriter(t, model, Options{})
	require.NoError(t, w.Ready(context.Background()))
	assert.Zero(t, model.warms)

	model.loaded = false
	require.NoError(t, w.Ready(context.Background()))
	assert.Equal(t, 1, model.warms)

	model.loaded, model.warmErr = false, generator.ErrModelMissing
	err := w.Ready(context.Background())
	require.ErrorIs(t, err, generator.ErrModelMissing)
	assert.Contains(t, err.Error(), "gemma4:26b isn't pulled — run curio up")
	assert.Equal(t, "missing", w.State().Failure.Cause)
	assert.Equal(t, FeedNone, w.Feed(), "no job while the model isn't pulled")

	model.warmErr, model.loadedErr = nil, generator.ErrUnreachable
	require.ErrorIs(t, w.Ready(context.Background()), generator.ErrUnreachable)
	model.loadedErr = nil
	require.NoError(t, w.Ready(context.Background()))
	assert.Equal(t, FeedAll, w.Feed(), "a load that answers ends the outage")

	model.loadedErr = generator.ErrUnreachable
	require.ErrorIs(t, w.Ready(context.Background()), generator.ErrUnreachable)
	model.loadedErr, model.loaded = nil, true
	require.NoError(t, w.Ready(context.Background()))
	assert.Equal(t, FeedAll, w.Feed(), "a listing that says the model is loaded ends the outage")
	assert.True(t, w.State().Outage.Since.IsZero())
}

// TestReady_LoadFailureWaits: a load the provider couldn't serve (a 5xx)
// while the model is unchecked is the check reaching no verdict, which
// can't run without it: it waits as a failed check does, so no page is
// charged for a model that can't be loaded, and a load within the wait
// asks nothing. Once the model is fit, a load that fails so is the page's
// job's failure (decisions.md, Known limits).
func TestReady_LoadFailureWaits(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	model := &fakeModel{warmErr: fmt.Errorf("ollama generate: %w: HTTP 500: not enough memory",
		generator.ErrUnavailable), answer: fit(goodAnswer)}
	w, logs := newWriter(t, model, Options{Now: func() time.Time { return now }})
	err := w.Ready(context.Background())
	we, ok := errors.AsType[*WaitError](err)
	require.True(t, ok, "%v", err)
	require.ErrorIs(t, err, ErrNotChecked)
	require.ErrorIs(t, err, generator.ErrUnavailable)
	assert.True(t, we.Probed, "this load asked")
	assert.Equal(t, now.Add(time.Minute), we.Until)
	st := w.State()
	assert.Equal(t, CheckUnchecked, st.Check)
	assert.Equal(t, we.Until, st.CheckRetryAt)
	assert.Equal(t, "check", st.Failure.Cause)
	assert.Contains(t, st.Failure.Err, "load gemma4:26b")
	assert.True(t, st.Outage.Since.IsZero(), "Ollama answered")
	assert.Equal(t, FeedOne, w.Feed())

	err = w.Ready(context.Background())
	we, ok = errors.AsType[*WaitError](err)
	require.True(t, ok, "%v", err)
	assert.False(t, we.Probed)
	assert.Equal(t, 1, model.warms, "a load within the wait asks nothing")
	now = now.Add(time.Minute)
	err = w.Ready(context.Background())
	we, ok = errors.AsType[*WaitError](err)
	require.True(t, ok, "%v", err)
	assert.True(t, we.Probed)
	assert.Equal(t, now.Add(2*time.Minute), we.Until, "twice as long")
	assert.Equal(t, 1, strings.Count(logs.String(), `"level":"WARN"`), "one WARN an episode")

	now = now.Add(2 * time.Minute)
	model.warmErr = nil
	require.NoError(t, w.Ready(context.Background()))
	_, err = w.Write(context.Background(), page())
	require.NoError(t, err)
	assert.Equal(t, CheckFit, w.State().Check)
	assert.Equal(t, FeedAll, w.Feed())

	model.loaded, model.warmErr = false, generator.ErrUnavailable
	err = w.Ready(context.Background())
	require.ErrorIs(t, err, generator.ErrUnavailable)
	_, ok = errors.AsType[*WaitError](err)
	assert.False(t, ok, "a fit model's load that fails is the page's job's")
	assert.Equal(t, "unavailable:load", w.State().Failure.Cause)
}

// TestKeepAlive: summary calls, the check's probes and a load send the
// writer's keep-alive, which is the planner's while the planner is in use
// and writes with the same model, and none otherwise.
func TestKeepAlive(t *testing.T) {
	model := &fakeModel{answer: fit(goodAnswer)}
	w, _ := newWriter(t, model, Options{KeepAlive: 30 * time.Minute})
	require.NoError(t, w.Ready(context.Background()))
	_, err := w.Write(context.Background(), page())
	require.NoError(t, err)
	assert.Equal(t, []time.Duration{30 * time.Minute}, model.warmedFor)
	require.Len(t, model.options, 4, "three probes, then the page")
	for _, opts := range model.options {
		assert.Equal(t, 30*time.Minute, opts.KeepAlive)
	}

	cfg := config.Default()
	cfg.Search.UnderstandKeepAliveMinutes = 45
	assert.Equal(t, 45*time.Minute, OptionsFrom(cfg).KeepAlive, "the planner's, on its model")
	cfg.Search.Understand = false
	assert.Zero(t, OptionsFrom(cfg).KeepAlive, "no planner in use: Ollama's default")
	cfg = config.Default()
	cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleSummaries: {Model: "gemma4:26b"}}
	assert.Zero(t, OptionsFrom(cfg).KeepAlive, "another model than the planner's")
	cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RolePlanner: {BaseURL: "http://127.0.0.1:11435"}}
	assert.Zero(t, OptionsFrom(cfg).KeepAlive, "the same model on another server")
}

// TestProbe: while the provider doesn't have the model, the pass is fed
// nothing, and a probe asks the provider's listing whether it is pulled
// now, ending the outage once it is; with any other outage, or none, a
// probe asks nothing.
func TestProbe(t *testing.T) {
	model := &fakeModel{loaded: true, answer: fit(goodAnswer)}
	w, _ := newWriter(t, model, Options{})
	pings := 0
	w.gen = countingPings{model, &pings}
	w.Probe(context.Background())
	assert.Zero(t, pings, "no outage: nothing asked")

	model.answer = func(string) (string, error) { return "", generator.ErrModelMissing }
	_, err := w.Write(context.Background(), page())
	require.ErrorIs(t, err, generator.ErrModelMissing)
	assert.Equal(t, OutageMissing, w.State().Outage.Cause)
	assert.Equal(t, FeedNone, w.Feed())

	model.pingErr = generator.ErrModelMissing
	w.Probe(context.Background())
	assert.Equal(t, 1, pings)
	assert.Equal(t, FeedNone, w.Feed(), "still not pulled")

	model.pingErr = nil
	w.Probe(context.Background())
	assert.Equal(t, 2, pings)
	assert.True(t, w.State().Outage.Since.IsZero(), "the listing holds it: the outage is over")
	assert.Equal(t, FeedAll, w.Feed())

	model.answer = func(string) (string, error) { return "", generator.ErrUnreachable }
	_, err = w.Write(context.Background(), page())
	require.ErrorIs(t, err, generator.ErrUnreachable)
	w.Probe(context.Background())
	assert.Equal(t, 2, pings, "an outage of another cause: its job's deferrals probe it")
	assert.Equal(t, FeedOne, w.Feed())
}

// countingPings is a model whose pings are counted.
type countingPings struct {
	*fakeModel
	pings *int
}

func (c countingPings) Ping(ctx context.Context) error {
	*c.pings++
	return c.fakeModel.Ping(ctx)
}

// TestReady_EvictionHold: a model found unloaded within the keep-alive of
// the writer's last load counts as evicted; the second within an hour
// holds the pass 15 minutes, asking nothing and feeding none, with one
// WARN, twice as long when it holds again within an hour of the last
// hold's end; a model found unloaded after its keep-alive is no eviction.
func TestReady_EvictionHold(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	model := &fakeModel{}
	w, logs := newWriter(t, model, Options{Now: func() time.Time { return now }})
	evict := func(after time.Duration) error {
		now = now.Add(after)
		model.mu.Lock()
		model.loaded = false
		model.mu.Unlock()
		return w.Ready(context.Background())
	}
	require.NoError(t, w.Ready(context.Background()), "a first load")
	require.NoError(t, evict(time.Minute), "one eviction loads it again")
	assert.Equal(t, 2, model.warms)

	err := evict(time.Minute)
	require.ErrorIs(t, err, ErrHeld, "the second within an hour")
	assert.Equal(t, 2, model.warms, "a hold loads nothing")
	st := w.State()
	assert.Equal(t, now.Add(15*time.Minute), st.EvictionHoldUntil)
	assert.Equal(t, "evicted", st.Failure.Cause)
	assert.Equal(t, FeedNone, w.Feed())
	assert.Equal(t, 1, strings.Count(logs.String(), "Ollama keeps unloading"))

	now = now.Add(10 * time.Minute)
	require.ErrorIs(t, w.Ready(context.Background()), ErrHeld, "until the hold ends")
	assert.Equal(t, 2, model.warms)

	now = now.Add(5 * time.Minute)
	assert.Equal(t, FeedAll, w.Feed(), "the hold is over")
	assert.True(t, w.State().EvictionHoldUntil.IsZero())
	require.NoError(t, w.Ready(context.Background()), "loaded again, no eviction counted")
	assert.Equal(t, 3, model.warms)
	require.NoError(t, evict(time.Minute))
	require.ErrorIs(t, evict(time.Minute), ErrHeld)
	assert.Equal(t, now.Add(30*time.Minute), w.State().EvictionHoldUntil, "again within an hour: twice as long")

	now = now.Add(30 * time.Minute)
	require.NoError(t, w.Ready(context.Background()))
	require.NoError(t, evict(6*time.Minute), "after its keep-alive: no eviction")
	require.NoError(t, evict(time.Minute), "the count began again")
	assert.True(t, w.State().EvictionHoldUntil.IsZero())
}

// TestEnds_OnTheWallClock: the check's wait and the eviction hold end at a
// wall time, with no monotonic reading, so they are judged on the clock
// the jobs table keeps and healthz reports, never on Go's monotonic clock,
// which stops while a Mac sleeps.
func TestEnds_OnTheWallClock(t *testing.T) {
	model := &fakeModel{loaded: true, answer: func(string) (string, error) { return "", generator.ErrUnavailable }}
	w, _ := newWriter(t, model, Options{})
	_, err := w.Write(context.Background(), page())
	_, ok := errors.AsType[*WaitError](err)
	require.True(t, ok, "%v", err)
	for _, end := range []time.Time{w.State().CheckRetryAt, w.CheckRetryAt()} {
		assert.NotContains(t, end.String(), "m=")
		assert.InDelta(t, time.Minute.Seconds(), time.Until(end).Seconds(), 5)
	}

	model = &fakeModel{}
	w, _ = newWriter(t, model, Options{})
	for range 2 {
		require.NoError(t, w.Ready(context.Background()))
		model.mu.Lock()
		model.loaded = false
		model.mu.Unlock()
	}
	require.ErrorIs(t, w.Ready(context.Background()), ErrHeld)
	end := w.State().EvictionHoldUntil
	assert.NotContains(t, end.String(), "m=")
	assert.InDelta(t, (15 * time.Minute).Seconds(), time.Until(end).Seconds(), 5)
}

// TestReady_KeptForTheShortestKeepAlive: an unload counts as an eviction
// only within the shortest keep-alive a request for the model may leave:
// the writer's own when shorter than Ollama's default.
func TestReady_KeptForTheShortestKeepAlive(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	model := &fakeModel{answer: fit(goodAnswer)}
	w, _ := newWriter(t, model, Options{KeepAlive: time.Minute, Now: func() time.Time { return now }})
	require.NoError(t, w.Ready(context.Background()))
	for range 3 {
		_, err := w.Write(context.Background(), page())
		require.NoError(t, err)
		now = now.Add(2 * time.Minute)
		model.mu.Lock()
		model.loaded = false
		model.mu.Unlock()
		require.NoError(t, w.Ready(context.Background()), "unloaded after the writer's keep-alive of a minute")
	}
	assert.True(t, w.State().EvictionHoldUntil.IsZero())
}

// plain is a generator without structured output.
type plain struct{}

func (plain) Generate(context.Context, string, generator.Options) (string, error) { return "", nil }
func (plain) Model() string                                                       { return "m" }
func (plain) Ping(context.Context) error                                          { return nil }

// TestNew_WritesNothing: off, without structured output, or withheld, the
// writer sends nothing and says why; the second is logged at ERROR.
func TestNew_WritesNothing(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	noJSON := New(plain{}, Options{On: true, Model: config.ModelSpec{Provider: "acme", Model: "m"}, Log: log})
	assert.Equal(t, "acme has no structured output", noJSON.Refusal())
	assert.Contains(t, logs.String(), `"level":"ERROR"`)

	off := New(&fakeModel{}, Options{Log: log})
	assert.Equal(t, OffReason, off.Refusal())
	assert.False(t, off.State().On)

	model := &fakeModel{loaded: true, answer: fit(goodAnswer)}
	withheld := New(model, Options{On: true, Withheld: "pages would leave this machine", Log: log})
	for _, w := range []*Writer{noJSON, off, withheld} {
		_, err := w.Write(context.Background(), page())
		require.ErrorIs(t, err, ErrNotWriting)
		require.ErrorIs(t, w.Ready(context.Background()), ErrNotWriting)
		_, err = w.Check(context.Background())
		require.ErrorIs(t, err, ErrNotWriting)
		assert.Equal(t, FeedNone, w.Feed())
	}
	assert.Zero(t, model.calls(), "a withheld writer sends nothing")
	assert.Zero(t, model.warms)
	assert.Equal(t, "pages would leave this machine", withheld.State().Reason)
}

// TestOptionsFrom_OffByDefault: summaries.enabled left out turns the
// writer off for every model, saying so and how to turn them on, logging
// nothing; true turns it on, and false off, saying it is config.yaml's.
func TestOptionsFrom_OffByDefault(t *testing.T) {
	for _, model := range []string{"qwen3:4b-instruct", "gemma4:26b"} {
		cfg := config.Default()
		cfg.Generation.Model = model
		opts := OptionsFrom(cfg)
		assert.False(t, opts.On, model)
		assert.True(t, opts.OffByDefault, model)
		var logs bytes.Buffer
		opts.Log = slog.New(slog.NewJSONHandler(&logs, nil))
		w := New(&fakeModel{}, opts)
		st := w.State()
		assert.False(t, st.On)
		assert.True(t, st.OffByDefault)
		assert.Equal(t, "document summaries are off by default; "+OnFix, st.Reason)
		assert.Equal(t, st.Reason, w.Refusal())
		assert.Empty(t, logs.String(), "nothing logged")
		_, err := w.Write(context.Background(), page())
		require.ErrorIs(t, err, ErrNotWriting)

		cfg.Summaries.Enabled = new(true)
		assert.True(t, OptionsFrom(cfg).On, "true turns them on")
		assert.False(t, OptionsFrom(cfg).OffByDefault)
		cfg.Summaries.Enabled = new(false)
		opts = OptionsFrom(cfg)
		assert.False(t, opts.On)
		assert.False(t, opts.OffByDefault, "off by config.yaml")
		assert.False(t, New(&fakeModel{}, opts).State().OffByDefault)
		assert.Equal(t, OffReason, New(&fakeModel{}, opts).Refusal())
	}
}

// TestWithheld: pages leave this machine, or go to another provider, only
// where the summaries role names it.
func TestWithheld(t *testing.T) {
	cfg := config.Default()
	cfg.Summaries.Enabled = new(true)
	assert.Empty(t, OptionsFrom(cfg).Withheld)
	assert.True(t, OptionsFrom(cfg).On)
	assert.True(t, OptionsFrom(cfg).Shared, "the labels and the planner write with generation.model")
	cfg.Generation.BaseURL = "http://gpu-box.lan:11434"
	assert.Equal(t, "pages would leave this machine for http://gpu-box.lan:11434, generation.base_url, which the "+
		"summaries role doesn't name; set generation.roles.summaries.base_url to send them there, or "+
		"summaries.enabled: false", OptionsFrom(cfg).Withheld)
	cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleSummaries: {BaseURL: cfg.Generation.BaseURL}}
	assert.Empty(t, OptionsFrom(cfg).Withheld)
	cfg = config.Default()
	cfg.Generation.Provider = "openai"
	assert.Contains(t, OptionsFrom(cfg).Withheld, "pages would go to the provider openai, generation.provider")
}

// TestWrite_LogsNoPageText: no log record of a write, its check or its
// failures carries a word of the page, the prompt or the summary.
func TestWrite_LogsNoPageText(t *testing.T) {
	secret := Input{DocumentID: "doc", Title: "Quillfeather", URL: "https://example.com/xq", Site: "Brambleworth",
		Text: "Quillfeather and Brambleworth met at Thistledown."}
	answer := `{"kind":"article","site":"Brambleworth","author":"Thistledown","about":"Quillfeather.",` +
		`"entities":["Quillfeather"],"claims":["Thistledown"],"descriptions":["Brambleworth"]}`
	model := &fakeModel{loaded: true, answer: fit(answer)}
	w, logs := newWriter(t, model, Options{})
	_, err := w.Write(context.Background(), secret)
	require.NoError(t, err)
	model.answer = func(string) (string, error) { return `{"kind":"Quillfeather"}`, nil }
	_, err = w.Write(context.Background(), secret)
	require.Error(t, err)
	model.answer = func(string) (string, error) { return "", generator.ErrUnavailable }
	_, err = w.Write(context.Background(), secret)
	require.Error(t, err)
	require.NotEmpty(t, logs.String())
	for _, word := range []string{"Quillfeather", "Brambleworth", "Thistledown"} {
		assert.NotContains(t, logs.String(), word)
	}
	assert.Contains(t, logs.String(), `"document_id":"doc"`)
}
