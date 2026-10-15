package understand

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/search"
)

// fakeModel is a JSONGenerator and Warmer: a model that is loaded or not,
// loads as told, answers the fitness check's probes as a fit model does,
// and every other question with plan.
type fakeModel struct {
	mu        sync.Mutex
	loaded    bool
	loadedErr error
	asked     func(ctx context.Context) (bool, error) // when set, answers Loaded in place of loaded and loadedErr
	warmErr   error
	warmGate  chan struct{} // when set, a warm-up waits for it to close, or for its context
	probes    func(question string) (json.RawMessage, error)
	plan      func(ctx context.Context, question string) (json.RawMessage, error)

	unloadErr error
	pingErr   error // what asking the provider's listing for the model answers

	warms, plans, probesAsked, unloads, pings int
	keepAlives                                []time.Duration
	prompts                                   []string
	opts                                      []generator.Options
}

// fitAnswers are a fit model's answers to the probes.
func fitAnswers(question string) (json.RawMessage, error) {
	switch question {
	case "postgres vacuum tuning":
		return json.RawMessage(`{"kind":"keywords","names":[],"rewrites":[],"filters":{}}`), nil
	case "videos about rust ownership I saved in October 2017":
		return json.RawMessage(`{"kind":"list","names":[],"rewrites":["rust borrow checker"],"filters":` +
			`{"saved_after":"2017-10-01","saved_before":"2017-11-01","content_type":["video"]}}`), nil
	case "the paper from Google that introduced the transformer architecture":
		return json.RawMessage(`{"kind":"one","names":["Attention Is All You Need"],"rewrites":[],"filters":{}}`), nil
	}
	return nil, fmt.Errorf("not a probe: %q", question)
}

func (*fakeModel) Generate(context.Context, string, generator.Options) (string, error) {
	return "", errors.New("the planner asks for JSON")
}
func (*fakeModel) Model() string { return "gemma4:26b" }
func (m *fakeModel) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pings++
	return m.pingErr
}

func (m *fakeModel) GenerateJSON(ctx context.Context, prompt string, schema json.RawMessage,
	opts generator.Options) (json.RawMessage, error) {
	if string(schema) != answerSchema {
		return nil, errors.New("not the answer's schema")
	}
	_, question, _ := strings.Cut(prompt, "\nQuestion: ")
	question = strings.TrimSuffix(question, "\n")
	m.mu.Lock()
	m.prompts, m.opts = append(m.prompts, prompt), append(m.opts, opts)
	probes := m.probes
	isProbe := false
	for _, p := range probeQuestions() {
		isProbe = isProbe || p == question
	}
	if isProbe {
		m.probesAsked++
	} else {
		m.plans++
	}
	plan := m.plan
	m.mu.Unlock()
	if isProbe {
		if probes == nil {
			probes = fitAnswers
		}
		return probes(question)
	}
	return plan(ctx, question)
}

func probeQuestions() []string {
	out := make([]string, len(probes))
	for i, p := range probes {
		out[i] = p.question
	}
	return out
}

func (m *fakeModel) Loaded(ctx context.Context) (bool, error) {
	m.mu.Lock()
	asked, loaded, err := m.asked, m.loaded, m.loadedErr
	m.mu.Unlock()
	if asked != nil {
		return asked(ctx)
	}
	return loaded, err
}

func (m *fakeModel) Warm(ctx context.Context, keepAlive time.Duration) error {
	m.mu.Lock()
	m.warms++
	m.keepAlives = append(m.keepAlives, keepAlive)
	gate, err := m.warmGate, m.warmErr
	m.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.loaded = true
	m.mu.Unlock()
	return nil
}

func (m *fakeModel) Unload(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unloads++
	if m.unloadErr != nil {
		return m.unloadErr
	}
	m.loaded = false
	return nil
}

// setLoaded says whether Ollama holds the model now.
func (m *fakeModel) setLoaded(loaded bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loaded = loaded
}

func (m *fakeModel) unloadCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unloads
}

func (m *fakeModel) counts() (warms, probes, plans int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.warms, m.probesAsked, m.plans
}

// fakeClock is a clock tests move by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// rig is a planner over a fake model, on a clock that reads Thursday
// 2025-04-17, 3 p.m. in New York, logging every record into logs.
type rig struct {
	p      *Planner
	model  *fakeModel
	clock  *fakeClock
	logs   *bytes.Buffer
	cancel context.CancelFunc
}

func newRig(t *testing.T, model *fakeModel) *rig {
	t.Helper()
	return newRigWith(t, model, func(*Options) {})
}

// newRigWith is newRig with its options changed by change.
func newRigWith(t *testing.T, model *fakeModel, change func(*Options)) *rig {
	t.Helper()
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	clock := &fakeClock{t: time.Date(2025, 4, 17, 15, 0, 0, 0, ny)}
	logs := &bytes.Buffer{}
	lifetime, cancel := context.WithCancel(context.Background())
	opts := Options{On: true, Timeout: time.Minute, Model: config.ModelSpec{Provider: "ollama",
		Model: "gemma4:26b"}, Now: clock.Now, Location: ny,
		Log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	change(&opts)
	p := New(lifetime, model, opts)
	r := &rig{p: p, model: model, clock: clock, logs: logs, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		p.Wait()
	})
	return r
}

// plan plans question, failing the test on an error.
func (r *rig) plan(t *testing.T, question string) Outcome {
	t.Helper()
	out, err := r.p.Plan(context.Background(), question)
	require.NoError(t, err)
	return out
}

// records are the log's records, after the background work is done.
func (r *rig) records(t *testing.T) []map[string]any {
	t.Helper()
	r.p.Wait()
	var out []map[string]any
	for line := range strings.Lines(r.logs.String()) {
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		out = append(out, rec)
	}
	return out
}

// answer is a model's plan that answers every question with raw.
func answerWith(raw string) func(context.Context, string) (json.RawMessage, error) {
	return func(context.Context, string) (json.RawMessage, error) { return json.RawMessage(raw), nil }
}

// fitRig is a rig whose loaded model has passed the check.
func fitRig(t *testing.T, plan func(context.Context, string) (json.RawMessage, error)) *rig {
	t.Helper()
	r := newRig(t, &fakeModel{loaded: true, plan: plan})
	require.Equal(t, StatusChecking, r.plan(t, "warm-up question").Status)
	r.p.Wait()
	return r
}

const fullPlan = `{"kind":"list","names":["Okapi Field Guide"],` +
	`"rewrites":["striped horses","The zebra stripes VIDEOS  I saved in summer 2018"],` +
	`"filters":{"saved_after":"2018-06-01","saved_before":"2018-09-23","content_type":["video","pdf"],` +
	`"source":["safari"]}}`

// TestPlanner_Planned: a fit model's answer is a plan: its texts
// normalized, its filters grounded in the question's words and converted
// to local days, what the guards dropped listed. The request is greedy,
// capped at 256 tokens and keeps the model loaded 30 minutes, and the
// prompt says today in the planner's location.
func TestPlanner_Planned(t *testing.T) {
	r := fitRig(t, answerWith(fullPlan))
	out := r.plan(t, "the zebra stripes videos I saved in summer 2018")
	assert.Equal(t, StatusPlanned, out.Status)
	assert.Empty(t, out.Reason)
	assert.Equal(t, "gemma4:26b", out.Model)
	assert.Positive(t, out.Took)
	require.NotNil(t, out.Plan)
	assert.Equal(t, Plan{Kind: KindList, Names: []string{"Okapi Field Guide"}, Rewrites: []string{"striped horses"},
		Filters: search.FilterRequest{SavedAfter: "2018-06-01T00:00:00-04:00", SavedBefore: "2018-09-23T00:00:00-04:00",
			ContentType: []string{"video"}}}, *out.Plan, "the rewrite that is the question is dropped")
	assert.Equal(t, []Dropped{
		{"content_type", `the question doesn't name the type "pdf"`},
		{"source", `the question doesn't name the browser "safari"`},
	}, out.Dropped)

	r.model.mu.Lock()
	defer r.model.mu.Unlock()
	last := len(r.model.prompts) - 1
	assert.Contains(t, r.model.prompts[last], "Today is Thursday, 2025-04-17.\nQuestion: the zebra stripes videos")
	assert.Equal(t, generator.Options{Temperature: new(0.0), MaxTokens: 256, KeepAlive: 30 * time.Minute},
		r.model.opts[last])
}

// TestPlanner_AnotherLanguage: the guards read English words, so a
// question in another language keeps its rewrites and names, and loses
// its filters.
func TestPlanner_AnotherLanguage(t *testing.T) {
	r := fitRig(t, answerWith(`{"kind":"list","names":["The Rust Programming Language"],`+
		`"rewrites":["rust ownership borrowing"],"filters":{"saved_after":"2025-03-01","saved_before":"2025-04-01",`+
		`"content_type":["video"]}}`))
	out := r.plan(t, "vídeos sobre Rust que guardé el mes pasado")
	require.Equal(t, StatusPlanned, out.Status)
	assert.Equal(t, []string{"rust ownership borrowing"}, out.Plan.Rewrites)
	assert.Equal(t, []string{"The Rust Programming Language"}, out.Plan.Names)
	assert.Equal(t, search.FilterRequest{}, out.Plan.Filters)
	assert.Len(t, out.Dropped, 2)
}

// TestPlanner_Off: off, or over a provider that can't answer in JSON,
// every plan is StatusOff and nothing is asked; the latter is logged once,
// at ERROR.
func TestPlanner_Off(t *testing.T) {
	model := &fakeModel{loaded: true}
	off := New(context.Background(), model, Options{Model: config.ModelSpec{Provider: "ollama", Model: "m"}})
	out, err := off.Plan(context.Background(), "q")
	require.NoError(t, err)
	assert.Equal(t, Outcome{Status: StatusOff, Reason: OffReason, Model: "m", Took: out.Took}, out)
	assert.Equal(t, "search understanding is off (search.understand: false)", OffReason)

	var logs bytes.Buffer
	prose := New(context.Background(), proseOnly{}, Options{On: true, Model: config.ModelSpec{Provider: "acme",
		Model: "m"}, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	for range 2 {
		out, err = prose.Plan(context.Background(), "q")
		require.NoError(t, err)
		assert.Equal(t, StatusOff, out.Status)
		assert.Equal(t, "acme has no structured output", out.Reason)
	}
	assert.Equal(t, 1, strings.Count(logs.String(), "level=ERROR"))
	_, probesAsked, plans := model.counts()
	assert.Zero(t, probesAsked+plans)
}

// proseOnly is a generator without structured output.
type proseOnly struct{ generator.Generator }

// TestPlanner_CallerDone: a caller whose context has ended gets its error,
// and nothing is asked or logged.
func TestPlanner_CallerDone(t *testing.T) {
	r := newRig(t, &fakeModel{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.p.Plan(ctx, "q")
	require.ErrorIs(t, err, context.Canceled)
	warms, probesAsked, plans := r.model.counts()
	assert.Zero(t, warms+probesAsked+plans)
	assert.Empty(t, r.records(t))
}

// TestPlanner_Cold: a question that finds the model unloaded is searched
// by its words at once, as cold, while the model loads in the background,
// once however many questions find it so, then is checked; the next
// question is planned. Loading logs at INFO once.
func TestPlanner_Cold(t *testing.T) {
	gate := make(chan struct{})
	r := newRig(t, &fakeModel{warmGate: gate, plan: answerWith(`{"kind":"one"}`)})
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			out, err := r.p.Plan(context.Background(), "q")
			assert.NoError(t, err)
			assert.Equal(t, StatusCold, out.Status)
			assert.Equal(t, "gemma4:26b is loading", out.Reason)
		})
	}
	wg.Wait()
	close(gate)
	r.p.Wait()
	warms, probesAsked, plans := r.model.counts()
	assert.Equal(t, 1, warms, "one warm-up for every cold question")
	assert.Equal(t, []time.Duration{30 * time.Minute}, r.model.keepAlives)
	assert.Equal(t, len(probes), probesAsked, "then the check")
	assert.Zero(t, plans)

	assert.Equal(t, StatusPlanned, r.plan(t, "q").Status)
	var loading int
	for _, rec := range r.records(t) {
		if strings.Contains(rec["msg"].(string), "loading the planner's model") {
			loading++
			assert.Equal(t, "INFO", rec["level"])
		}
	}
	assert.Equal(t, 1, loading)
}

// TestPlanner_WarmFailure: a warm-up that failed answers every question
// for a minute, then the next cold question tries again.
func TestPlanner_WarmFailure(t *testing.T) {
	r := newRig(t, &fakeModel{warmErr: fmt.Errorf("ollama load: %w", ollama.ErrUnreachable)})
	assert.Equal(t, StatusCold, r.plan(t, "q").Status)
	r.p.Wait()
	out := r.plan(t, "q")
	assert.Equal(t, StatusUnavailable, out.Status)
	assert.Contains(t, out.Reason, "loading gemma4:26b failed")
	assert.Contains(t, out.Reason, "ollama unreachable")

	r.clock.advance(59 * time.Second)
	assert.Equal(t, StatusUnavailable, r.plan(t, "q").Status)
	r.clock.advance(2 * time.Second)
	assert.Equal(t, StatusCold, r.plan(t, "q").Status)
	r.p.Wait()
	warms, _, _ := r.model.counts()
	assert.Equal(t, 2, warms)
}

// TestPlanner_Checking: a loaded model nobody has checked is checked in the
// background, the question meanwhile searched by its words.
func TestPlanner_Checking(t *testing.T) {
	r := newRig(t, &fakeModel{loaded: true, plan: answerWith(`{"kind":"one"}`)})
	out := r.plan(t, "q")
	assert.Equal(t, StatusChecking, out.Status)
	assert.Equal(t, "curio is checking gemma4:26b", out.Reason)
	r.p.Wait()
	assert.Equal(t, StatusPlanned, r.plan(t, "q").Status)
	warms, probesAsked, plans := r.model.counts()
	assert.Zero(t, warms, "a loaded model isn't loaded again")
	assert.Equal(t, len(probes), probesAsked)
	assert.Equal(t, 1, plans)
	var passed bool
	for _, rec := range r.records(t) {
		passed = passed || (rec["msg"] == "search understanding: the planner's model passed its check" &&
			rec["level"] == "INFO")
	}
	assert.True(t, passed)
}

// TestPlanner_Unfit: a model that answers a probe wrong, or that Ollama
// refuses outright, is unfit for the life of the process: no plan is asked
// of it again, and the verdict is logged once, at WARN, naming the probe
// and the model.
func TestPlanner_Unfit(t *testing.T) {
	everyFilter := `{"kind":"list","names":[],"rewrites":[],"filters":{"saved_after":"2025-04-10",` +
		`"content_type":["video"],"source":["chrome"]}}`
	for _, tc := range []struct {
		name   string
		probes func(string) (json.RawMessage, error)
		probe  string
	}{
		{"sets a filter nobody stated", func(string) (json.RawMessage, error) {
			return json.RawMessage(everyFilter), nil
		}, "no filter"},
		{"misreads the month", func(q string) (json.RawMessage, error) {
			if strings.Contains(q, "October 2017") {
				return json.RawMessage(`{"kind":"list","names":[],"rewrites":[],"filters":{"saved_after":"2017-10-01",` +
					`"saved_before":"2017-10-31","content_type":["video"]}}`), nil
			}
			return fitAnswers(q)
		}, "a month and a type"},
		{"can't name the paper", func(q string) (json.RawMessage, error) {
			if strings.Contains(q, "transformer") {
				return json.RawMessage(`{"kind":"one","names":["Google"],"rewrites":[],"filters":{}}`), nil
			}
			return fitAnswers(q)
		}, "a paper by description"},
		{"answers what isn't a plan", func(string) (json.RawMessage, error) {
			return json.RawMessage(`{"kind":"summary"}`), nil
		}, "no filter"},
		{"its provider refuses the request", func(string) (json.RawMessage, error) {
			return nil, fmt.Errorf("ollama generate: %w: %w", generator.ErrBadRequest,
				&ollama.StatusError{Code: http.StatusBadRequest, Body: `{"error":"model does not support format"}`})
		}, "no filter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, &fakeModel{loaded: true, probes: tc.probes, plan: answerWith(`{"kind":"one"}`)})
			assert.Equal(t, StatusChecking, r.plan(t, "q").Status)
			r.p.Wait()
			for range 2 {
				out := r.plan(t, "q")
				assert.Equal(t, StatusUnfit, out.Status)
				assert.Contains(t, out.Reason, fmt.Sprintf("gemma4:26b didn't pass the check %q", tc.probe))
				assert.True(t, strings.HasSuffix(out.Reason, "; set generation.roles.planner.model to a model "+
					"that passes, or search.understand: false"), "the fix: %s", out.Reason)
			}
			_, _, plans := r.model.counts()
			assert.Zero(t, plans, "no plan is asked of an unfit model")
			var warns int
			for _, rec := range r.records(t) {
				if rec["level"] == "WARN" {
					warns++
					assert.Equal(t, tc.probe, rec["probe"])
					assert.Equal(t, "gemma4:26b", rec["model"])
				}
			}
			assert.Equal(t, 1, warns)
		})
	}
}

// TestPlanner_CheckUnreached: a check that couldn't reach the model says
// nothing of it: the verdict stays unknown, the failure answers for a
// minute, and the next question starts the check again.
func TestPlanner_CheckUnreached(t *testing.T) {
	for name, err := range map[string]error{
		"no answer":           fmt.Errorf("ollama generate: %w: connection refused", generator.ErrUnreachable),
		"model not pulled":    fmt.Errorf("ollama generate: %w", ollama.ErrModelNotLoaded),
		"a 404":               &ollama.StatusError{Code: http.StatusNotFound},
		"rate limited":        fmt.Errorf("HTTP 429: %w", generator.ErrUnavailable),
		"a server error":      fmt.Errorf("HTTP 500: boom: %w", generator.ErrUnavailable),
		"a key refused":       fmt.Errorf("HTTP 401: %w", generator.ErrUnauthorized),
		"out of credit":       fmt.Errorf("HTTP 402: %w", generator.ErrUnauthorized),
		"a failure unknown":   errors.New("a provider's error curio has no name for"),
		"a 400 unclassified":  &ollama.StatusError{Code: http.StatusBadRequest},
		"the probe timed out": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, &fakeModel{loaded: true, plan: answerWith(`{"kind":"one"}`),
				probes: func(string) (json.RawMessage, error) { return nil, err }})
			assert.Equal(t, StatusChecking, r.plan(t, "q").Status)
			r.p.Wait()
			out := r.plan(t, "q")
			assert.Equal(t, StatusUnavailable, out.Status)
			assert.Contains(t, out.Reason, "checking gemma4:26b failed")
			r.clock.advance(61 * time.Second)
			assert.Equal(t, StatusChecking, r.plan(t, "q").Status)
			r.p.Wait()
			_, probesAsked, _ := r.model.counts()
			assert.Equal(t, 2, probesAsked, "the first probe, twice")
		})
	}
}

// TestPlanner_CheckUnreachedWarnsOnce: a check that can't reach the model,
// retried a minute apart, warns once, then logs at DEBUG.
func TestPlanner_CheckUnreachedWarnsOnce(t *testing.T) {
	r := newRig(t, &fakeModel{loaded: true, probes: func(string) (json.RawMessage, error) {
		return nil, fmt.Errorf("ollama generate: %w", generator.ErrUnreachable)
	}})
	for range 4 {
		assert.Equal(t, StatusChecking, r.plan(t, "q").Status)
		r.p.Wait()
		r.clock.advance(retryAfter)
	}
	var levels []string
	for _, rec := range r.records(t) {
		if rec["cause"] == "unavailable:check" {
			levels = append(levels, rec["level"].(string))
			assert.Equal(t, "no filter", rec["probe"])
		}
	}
	assert.Equal(t, []string{"WARN", "DEBUG", "DEBUG", "DEBUG"}, levels)
}

// TestPlanner_Failures: once the model is fit, a plan it doesn't give is
// searched by the question's words, its status saying why.
func TestPlanner_Failures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(context.Context, string) (json.RawMessage, error)
		want   Status
		reason string
	}{
		{"its own deadline", func(ctx context.Context, _ string) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, fmt.Errorf("ollama generate: %w", ctx.Err())
		}, StatusTimeout, "understanding took longer than 20ms"},
		{"the model gone", func(context.Context, string) (json.RawMessage, error) {
			return nil, fmt.Errorf("ollama generate: %w", generator.ErrModelMissing)
		}, StatusUnavailable, "gemma4:26b isn't pulled — run curio up"},
		{"Ollama down", func(context.Context, string) (json.RawMessage, error) {
			return nil, fmt.Errorf("ollama generate: %w: connection refused", ollama.ErrUnreachable)
		}, StatusUnavailable, "connection refused"},
		{"a server error", func(context.Context, string) (json.RawMessage, error) {
			return nil, &ollama.StatusError{Code: http.StatusInternalServerError, Body: "boom"}
		}, StatusUnavailable, "HTTP 500"},
		{"not JSON", func(context.Context, string) (json.RawMessage, error) {
			return nil, fmt.Errorf("ollama generate: %w: %q", generator.ErrNotJSON, "Sure!")
		}, StatusInvalid, "the answer isn't JSON"},
		{"cut off", func(context.Context, string) (json.RawMessage, error) {
			return nil, fmt.Errorf("ollama generate: %w", generator.ErrTruncated)
		}, StatusInvalid, "the answer ran past 256 tokens"},
		{"not a plan", answerWith(`{"kind":"one","site":"example.com"}`), StatusInvalid, "it isn't a plan's JSON"},
		{"too many names", answerWith(`{"kind":"one","names":["a","b","c","d","e"]}`), StatusInvalid, "5 names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fitRig(t, tc.answer)
			r.p.timeout = 20 * time.Millisecond
			out := r.plan(t, "q")
			assert.Equal(t, tc.want, out.Status)
			assert.Contains(t, out.Reason, tc.reason)
			assert.Nil(t, out.Plan)
		})
	}
	t.Run("the model isn't asked whether it's loaded", func(t *testing.T) {
		r := fitRig(t, answerWith(`{"kind":"one"}`))
		r.model.mu.Lock()
		r.model.loadedErr = fmt.Errorf("%w: /api/ps: HTTP 500", ollama.ErrUnreachable)
		r.model.mu.Unlock()
		out := r.plan(t, "q")
		assert.Equal(t, StatusUnavailable, out.Status)
		assert.Contains(t, out.Reason, "can't tell whether gemma4:26b is loaded")
	})
}

// TestPlanner_CallerCancelledMidPlan: the caller's context ending while
// the model writes is the caller's error, not a timeout, and isn't logged.
func TestPlanner_CallerCancelledMidPlan(t *testing.T) {
	asked := make(chan struct{})
	r := fitRig(t, func(ctx context.Context, _ string) (json.RawMessage, error) {
		close(asked)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	before := len(r.records(t))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-asked
		cancel()
	}()
	_, err := r.p.Plan(ctx, "q")
	require.ErrorIs(t, err, context.Canceled)
	assert.Len(t, r.records(t), before, "nothing logged")
}

// TestPlanner_CallerCancelledAskingLoaded: the caller's context ending
// while the planner asks Ollama whether the model is loaded is the
// caller's error and logs nothing: a dashboard keystroke cancels requests
// by design, and the first WARN of an outage is kept for a real one.
func TestPlanner_CallerCancelledAskingLoaded(t *testing.T) {
	asking := make(chan struct{})
	r := newRig(t, &fakeModel{asked: func(ctx context.Context) (bool, error) {
		close(asking)
		<-ctx.Done()
		return false, fmt.Errorf("ollama ps: %w: %w", ollama.ErrUnreachable, ctx.Err())
	}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-asking
		cancel()
	}()
	_, err := r.p.Plan(ctx, "q")
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, r.records(t), "nothing logged")

	r.model.mu.Lock()
	r.model.asked, r.model.loadedErr = nil, fmt.Errorf("ollama ps: %w: connection refused", ollama.ErrUnreachable)
	r.model.mu.Unlock()
	assert.Equal(t, StatusUnavailable, r.plan(t, "q").Status)
	records := r.records(t)
	require.Len(t, records, 2, "the outage's first record, and the question's")
	assert.Equal(t, "WARN", records[0]["level"], "the outage's first record")
	assert.Equal(t, "search understanding: a question", records[1]["msg"])
}

// TestPlanner_Logs: each cause of a plan's failure is logged at WARN the
// first time and at DEBUG after, with the model; no record carries the
// question, a rewrite or a name.
func TestPlanner_Logs(t *testing.T) {
	const q, rw, nm = "zqxjq question words", "zqxjq rewrite words", "Zqxjq Name"
	var calls int
	var mu sync.Mutex
	r := fitRig(t, func(context.Context, string) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch calls {
		case 1, 2:
			return json.RawMessage(`{"kind":"list","names":["` + nm + `"],"rewrites":["` + rw + `"],"filters":{}}`), nil
		case 3, 4:
			return nil, fmt.Errorf("ollama generate: %w: %q", generator.ErrNotJSON, q+" "+rw)
		default:
			return nil, fmt.Errorf("ollama generate: %w", ollama.ErrUnreachable)
		}
	})
	for range 6 {
		r.plan(t, q)
	}
	levels := map[string][]string{}
	for _, rec := range r.records(t) {
		line, err := json.Marshal(rec)
		require.NoError(t, err)
		for _, secret := range []string{"zqxjq", "Zqxjq"} {
			assert.NotContains(t, string(line), secret)
		}
		if cause, ok := rec["cause"].(string); ok {
			levels[cause] = append(levels[cause], rec["level"].(string))
			assert.Equal(t, "gemma4:26b", rec["model"])
		}
	}
	assert.Equal(t, map[string][]string{"invalid:json": {"WARN", "DEBUG"}, "unavailable:generate": {"WARN", "DEBUG"}},
		levels)
}

// TestPlanner_WaitAfterLifetime: background work runs on the lifetime
// context and stops with it: a warm-up in flight is cancelled, and Wait
// returns.
func TestPlanner_WaitAfterLifetime(t *testing.T) {
	r := newRig(t, &fakeModel{warmGate: make(chan struct{})})
	assert.Equal(t, StatusCold, r.plan(t, "q").Status)
	r.cancel()
	done := make(chan struct{})
	go func() {
		r.p.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait didn't return after the lifetime ended")
	}
	_, probesAsked, _ := r.model.counts()
	assert.Zero(t, probesAsked, "no check after a cancelled warm-up")
}

// TestPlanner_NoWorkAfterLifetime: once the lifetime has ended, a question
// that finds the model cold starts no background work, so none can begin
// while the daemon waits for it to stop.
func TestPlanner_NoWorkAfterLifetime(t *testing.T) {
	r := newRig(t, &fakeModel{})
	r.cancel()
	assert.Equal(t, StatusCold, r.plan(t, "q").Status)
	r.p.Wait()
	warms, probesAsked, _ := r.model.counts()
	assert.Zero(t, warms+probesAsked)
}

// TestPlanner_CheckNow: Check runs the fitness check on the caller's
// context and keeps its verdict for Plan: the probe a model failed, none
// for a fit one, an error when a probe couldn't reach it.
func TestPlanner_CheckNow(t *testing.T) {
	fit := newRig(t, &fakeModel{loaded: true, plan: answerWith(`{"kind":"one"}`)})
	failed, err := fit.p.Check(context.Background())
	require.NoError(t, err)
	assert.Empty(t, failed)
	assert.Equal(t, StatusPlanned, fit.plan(t, "q").Status, "checked already, so planned at once")

	unfit := newRig(t, &fakeModel{loaded: true, plan: answerWith(`{"kind":"one"}`),
		probes: func(string) (json.RawMessage, error) {
			return json.RawMessage(`{"kind":"list","filters":{"source":["chrome"]}}`), nil
		}})
	failed, err = unfit.p.Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "no filter", failed)
	assert.Equal(t, StatusUnfit, unfit.plan(t, "q").Status)

	down := newRig(t, &fakeModel{loaded: true, probes: func(string) (json.RawMessage, error) {
		return nil, fmt.Errorf("ollama generate: %w", ollama.ErrUnreachable)
	}})
	failed, err = down.p.Check(context.Background())
	require.ErrorIs(t, err, ollama.ErrUnreachable)
	assert.Empty(t, failed)

	_, err = New(context.Background(), proseOnly{}, Options{On: true, Model: config.ModelSpec{Provider: "acme"},
		Log: slog.New(slog.DiscardHandler)}).Check(context.Background())
	require.EqualError(t, err, "acme has no structured output")
}

// TestPlanner_Ask: Ask plans whatever the check found and whether the
// model is loaded, for measuring a model; an off planner's is off.
func TestPlanner_Ask(t *testing.T) {
	r := newRig(t, &fakeModel{plan: answerWith(`{"kind":"keywords","names":["Okapi"]}`),
		probes: func(string) (json.RawMessage, error) { return json.RawMessage(`{"kind":"summary"}`), nil }})
	failed, err := r.p.Check(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, failed, "unfit")
	out, err := r.p.Ask(context.Background(), "okapi")
	require.NoError(t, err)
	assert.Equal(t, StatusPlanned, out.Status)
	assert.Equal(t, []string{"Okapi"}, out.Plan.Names)
	assert.Equal(t, "gemma4:26b", out.Model)
	warms, _, _ := r.model.counts()
	assert.Zero(t, warms, "Ask loads nothing in the background")

	out, err = New(context.Background(), r.model, Options{Model: config.ModelSpec{Model: "m"}}).Ask(
		context.Background(), "q")
	require.NoError(t, err)
	assert.Equal(t, StatusOff, out.Status)
}

// TestPlanner_NotPulled: a model its provider doesn't have is said to be
// missing, with how to get it, from the first question on, and never
// pulled or loaded; the listing is asked again on each question, so the
// first one after a pull loads the model.
func TestPlanner_NotPulled(t *testing.T) {
	missing := fmt.Errorf("model not loaded: gemma4:26b: %w", generator.ErrModelMissing)
	r := newRig(t, &fakeModel{pingErr: missing, plan: answerWith(`{"kind":"one"}`)})
	for range 3 {
		out := r.plan(t, "q")
		assert.Equal(t, StatusUnavailable, out.Status)
		assert.Equal(t, "gemma4:26b isn't pulled — run curio up", out.Reason)
	}
	r.p.Wait()
	warms, probesAsked, _ := r.model.counts()
	assert.Zero(t, warms+probesAsked, "nothing loaded or asked")
	assert.Equal(t, "unavailable:missing", r.p.State().Failure.Cause)

	r.model.mu.Lock()
	r.model.pingErr = nil
	r.model.mu.Unlock()
	assert.Equal(t, StatusCold, r.plan(t, "q").Status, "pulled since: loading")
	r.p.Wait()
	assert.Equal(t, StatusPlanned, r.plan(t, "q").Status)

	var warns int
	for _, rec := range r.records(t) {
		if rec["cause"] == "unavailable:missing" && rec["level"] == "WARN" {
			warns++
			assert.Contains(t, rec["msg"], "run curio up")
		}
	}
	assert.Equal(t, 1, warns, "once for the episode")
}

// TestPlanner_LoadFindsItMissing: a model the listing names but whose load
// says is missing is said to be missing, and loaded again a minute later,
// not on every question.
func TestPlanner_LoadFindsItMissing(t *testing.T) {
	missing := fmt.Errorf("ollama load gemma4:26b: %w", generator.ErrModelMissing)
	r := newRig(t, &fakeModel{warmErr: missing, plan: answerWith(`{"kind":"one"}`)})
	assert.Equal(t, StatusCold, r.plan(t, "q").Status)
	r.p.Wait()
	for range 2 {
		out := r.plan(t, "q")
		assert.Equal(t, StatusUnavailable, out.Status)
		assert.Equal(t, "gemma4:26b isn't pulled — run curio up", out.Reason)
	}
	r.p.Wait()
	warms, _, _ := r.model.counts()
	assert.Equal(t, 1, warms, "no retry within the minute")

	r.model.mu.Lock()
	r.model.warmErr = nil
	r.model.mu.Unlock()
	r.clock.advance(retryAfter)
	assert.Equal(t, "gemma4:26b isn't pulled — run curio up", r.plan(t, "q").Reason, "what is known while it retries")
	r.p.Wait()
	assert.Equal(t, StatusPlanned, r.plan(t, "q").Status)
}

// TestPlanner_ListingUnanswered: a listing that fails for another reason
// says nothing: the model is loaded as before.
func TestPlanner_ListingUnanswered(t *testing.T) {
	r := newRig(t, &fakeModel{pingErr: errors.New("HTTP 500: boom"), plan: answerWith(`{"kind":"one"}`)})
	assert.Equal(t, StatusCold, r.plan(t, "q").Status)
	r.p.Wait()
	warms, _, _ := r.model.counts()
	assert.Equal(t, 1, warms)
}

// TestPlanner_LoadedAsksNoListing: a loaded model's questions don't ask
// the listing.
func TestPlanner_LoadedAsksNoListing(t *testing.T) {
	r := fitRig(t, answerWith(`{"kind":"one"}`))
	r.plan(t, "q")
	r.model.mu.Lock()
	defer r.model.mu.Unlock()
	assert.Zero(t, r.model.pings)
}

// TestPlanner_UnfitIsUnloaded: an unfit model's memory is freed after the
// check, unless another role writes with it; a check made for measuring
// frees nothing.
func TestPlanner_UnfitIsUnloaded(t *testing.T) {
	wrong := func(string) (json.RawMessage, error) {
		return json.RawMessage(`{"kind":"list","names":[],"rewrites":[],"filters":{"source":["chrome"]}}`), nil
	}
	alone := newRig(t, &fakeModel{loaded: true, probes: wrong})
	assert.Equal(t, StatusChecking, alone.plan(t, "q").Status)
	alone.p.Wait()
	assert.Equal(t, 1, alone.model.unloadCount())
	assert.Equal(t, StatusUnfit, alone.plan(t, "q").Status)
	alone.p.Wait()
	assert.Equal(t, 1, alone.model.unloadCount(), "once")

	shared := newRigWith(t, &fakeModel{loaded: true, probes: wrong}, func(o *Options) { o.Shared = true })
	assert.Equal(t, StatusChecking, shared.plan(t, "q").Status)
	shared.p.Wait()
	assert.Zero(t, shared.model.unloadCount(), "the labels write with it")

	fails := newRig(t, &fakeModel{loaded: true, probes: wrong, unloadErr: errors.New("boom")})
	assert.Equal(t, StatusChecking, fails.plan(t, "q").Status)
	fails.p.Wait()
	assert.Equal(t, StatusUnfit, fails.plan(t, "q").Status, "unfit whatever the unload did")
	var warned bool
	for _, rec := range fails.records(t) {
		warned = warned || (rec["msg"] == "search understanding: couldn't unload the unfit planner's model" &&
			rec["level"] == "WARN")
	}
	assert.True(t, warned)

	measured := newRig(t, &fakeModel{loaded: true, probes: wrong})
	failed, err := measured.p.Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "no filter", failed)
	assert.Zero(t, measured.model.unloadCount())
}

// timesOut is a model's plan that takes longer than any timeout.
func timesOut(ctx context.Context, _ string) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, fmt.Errorf("ollama generate: %w", ctx.Err())
}

// TestPlanner_TimeoutHold: after 3 plans in a row time out, questions
// answer timeout at once, asking no model, for 2 minutes, then 4, 8 and
// at most 15 while plans keep timing out; a plan in time ends the run.
func TestPlanner_TimeoutHold(t *testing.T) {
	var slow atomic.Bool
	slow.Store(true)
	r := fitRig(t, func(ctx context.Context, q string) (json.RawMessage, error) {
		if slow.Load() {
			return timesOut(ctx, q)
		}
		return json.RawMessage(`{"kind":"one"}`), nil
	})
	r.p.timeout = 20 * time.Millisecond
	_, _, before := r.model.counts()
	for range 3 {
		assert.Equal(t, "understanding took longer than 20ms", r.plan(t, "q").Reason)
	}
	asked := func() int {
		_, _, plans := r.model.counts()
		return plans - before
	}
	held := r.plan(t, "q")
	assert.Equal(t, StatusTimeout, held.Status)
	assert.Equal(t, "3 plans in a row took longer than 20ms; understanding resumes at 15:02", held.Reason)
	assert.Equal(t, 3, asked(), "a held question asks no model")
	assert.Equal(t, r.clock.Now().Add(2*time.Minute), r.p.State().TimeoutHoldUntil)

	for _, hold := range []time.Duration{4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
		r.clock.advance(time.Minute)
		assert.Contains(t, r.plan(t, "q").Reason, "in a row", "still held")
		r.clock.advance(time.Hour)
		n := asked()
		assert.Equal(t, "understanding took longer than 20ms", r.plan(t, "q").Reason, "asked once the hold ends")
		assert.Equal(t, n+1, asked())
		assert.Equal(t, r.clock.Now().Add(hold), r.p.State().TimeoutHoldUntil, "held again, longer")
	}

	r.clock.advance(time.Hour)
	slow.Store(false)
	assert.Equal(t, StatusPlanned, r.plan(t, "q").Status)
	assert.Zero(t, r.p.State().TimeoutHoldUntil)
	slow.Store(true)
	for range 3 {
		r.plan(t, "q")
	}
	assert.Equal(t, r.clock.Now().Add(2*time.Minute), r.p.State().TimeoutHoldUntil, "a new run starts at 2 minutes")

	levels := map[string][]string{}
	for _, rec := range r.records(t) {
		if cause, ok := rec["cause"].(string); ok && cause == "timeout:held" {
			levels[cause] = append(levels[cause], rec["level"].(string))
		}
	}
	assert.Equal(t, []string{"WARN", "DEBUG", "DEBUG", "DEBUG", "DEBUG", "WARN"}, levels["timeout:held"],
		"once a run, and again after a plan")
}

// TestPlanner_ConcurrentTimeouts: plans in flight together that all time
// out start one hold of 2 minutes, not one escalated per plan.
func TestPlanner_ConcurrentTimeouts(t *testing.T) {
	r := fitRig(t, timesOut)
	r.p.timeout = 50 * time.Millisecond
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			out, err := r.p.Plan(context.Background(), "q")
			assert.NoError(t, err)
			assert.Equal(t, StatusTimeout, out.Status)
		})
	}
	wg.Wait()
	assert.Equal(t, r.clock.Now().Add(2*time.Minute), r.p.State().TimeoutHoldUntil)
	r.clock.advance(3 * time.Minute)
	assert.Contains(t, r.plan(t, "q").Reason, "understanding took longer than", "asked once the hold ends")
	assert.Equal(t, r.clock.Now().Add(4*time.Minute), r.p.State().TimeoutHoldUntil, "then doubled")
}

// TestPlanner_InvalidEndsATimeoutRun: an answer in time, even one that
// can't be used, ends a run of timeouts.
func TestPlanner_InvalidEndsATimeoutRun(t *testing.T) {
	var calls atomic.Int32
	r := fitRig(t, func(ctx context.Context, q string) (json.RawMessage, error) {
		if calls.Add(1) == 3 {
			return json.RawMessage(`{"kind":"summary"}`), nil
		}
		return timesOut(ctx, q)
	})
	r.p.timeout = 20 * time.Millisecond
	for _, want := range []Status{StatusTimeout, StatusTimeout, StatusInvalid, StatusTimeout, StatusTimeout} {
		assert.Equal(t, want, r.plan(t, "q").Status)
	}
	assert.Zero(t, r.p.State().TimeoutHoldUntil, "two in a row since the answer")
}

// TestPlanner_Evictions: a model found unloaded within its keep-alive of
// the last answer was evicted; the second time within the hour, no
// warm-up starts for 15 minutes, and questions say so, unless the model
// is loaded meanwhile. A model found unloaded after its keep-alive ran
// out wasn't evicted, and ends the count.
func TestPlanner_Evictions(t *testing.T) {
	r := fitRig(t, answerWith(`{"kind":"one"}`))
	require.Equal(t, StatusPlanned, r.plan(t, "q").Status)

	evict := func() Outcome {
		r.model.setLoaded(false)
		r.clock.advance(time.Minute)
		out := r.plan(t, "q")
		r.p.Wait()
		return out
	}
	assert.Equal(t, StatusCold, evict().Status, "the first: loaded again")
	warms, _, _ := r.model.counts()
	assert.Equal(t, 1, warms)

	out := evict()
	assert.Equal(t, StatusUnavailable, out.Status)
	assert.Equal(t, "Ollama unloaded gemma4:26b within its keep-alive 2 times, for other models; curio loads it "+
		"again from 15:17", out.Reason)
	r.clock.advance(14 * time.Minute)
	assert.Equal(t, out.Reason, r.plan(t, "q").Reason)
	r.p.Wait()
	warms, _, _ = r.model.counts()
	assert.Equal(t, 1, warms, "no warm-up while held")
	assert.Equal(t, r.clock.Now().Add(time.Minute), r.p.State().EvictionHoldUntil)
	assert.Equal(t, "evicted", r.p.State().Failure.Cause)

	r.model.setLoaded(true)
	assert.Equal(t, StatusPlanned, r.plan(t, "q").Status, "loaded by another: planned")
	r.model.setLoaded(false)
	assert.Equal(t, StatusUnavailable, r.plan(t, "q").Status, "still held")

	r.clock.advance(2 * time.Minute)
	assert.Equal(t, StatusCold, r.plan(t, "q").Status, "the hold over")
	r.p.Wait()
	assert.Zero(t, r.p.State().EvictionHoldUntil)

	r.clock.advance(31 * time.Minute)
	r.model.setLoaded(false)
	assert.Equal(t, StatusCold, r.plan(t, "q").Status, "expired, not evicted")
	r.p.Wait()
	assert.Equal(t, StatusCold, evict().Status, "the count started again")

	r.clock.advance(2 * time.Hour)
	r.model.setLoaded(true)
	require.Equal(t, StatusPlanned, r.plan(t, "q").Status)
	assert.Equal(t, StatusCold, evict().Status, "an hour past the first eviction starts a new count")
}

// TestPlanner_SharedKeepAlive: with another role on the model, whose
// requests set Ollama's 5-minute default, a model found unloaded more than
// 5 minutes after the last answer wasn't evicted.
func TestPlanner_SharedKeepAlive(t *testing.T) {
	r := newRigWith(t, &fakeModel{loaded: true, plan: answerWith(`{"kind":"one"}`)},
		func(o *Options) { o.Shared = true })
	r.plan(t, "q")
	r.p.Wait()
	for range 3 {
		require.Equal(t, StatusPlanned, r.plan(t, "q").Status)
		r.model.setLoaded(false)
		r.clock.advance(6 * time.Minute)
		assert.Equal(t, StatusCold, r.plan(t, "q").Status)
		r.p.Wait()
	}
	assert.Zero(t, r.p.State().EvictionHoldUntil)
}

// TestPlanner_KeepAlive: the keep-alive Options give is the one every plan
// and warm-up sends.
func TestPlanner_KeepAlive(t *testing.T) {
	r := newRigWith(t, &fakeModel{plan: answerWith(`{"kind":"one"}`)},
		func(o *Options) { o.KeepAlive = 7 * time.Minute })
	assert.Equal(t, StatusCold, r.plan(t, "q").Status)
	r.p.Wait()
	assert.Equal(t, StatusPlanned, r.plan(t, "q").Status)
	r.model.mu.Lock()
	defer r.model.mu.Unlock()
	assert.Equal(t, []time.Duration{7 * time.Minute}, r.model.keepAlives)
	for _, o := range r.model.opts {
		assert.Equal(t, 7*time.Minute, o.KeepAlive)
	}
}

// TestPlanner_TooLong: a question longer than the planner takes is
// searched by its words, its own status saying so, and never sent.
func TestPlanner_TooLong(t *testing.T) {
	r := fitRig(t, answerWith(`{"kind":"one"}`))
	_, _, before := r.model.counts()
	out := r.plan(t, strings.Repeat("ü", 257))
	assert.Equal(t, StatusTooLong, out.Status)
	assert.Equal(t, "the question is 514 bytes; curio understands questions of at most 512", out.Reason)
	assert.Equal(t, StatusPlanned, r.plan(t, strings.Repeat("x", 512)).Status)
	asked, err := r.p.Ask(context.Background(), strings.Repeat("x", 513))
	require.NoError(t, err)
	assert.Equal(t, StatusTooLong, asked.Status, "measuring holds questions to the cap too")
	_, _, plans := r.model.counts()
	assert.Equal(t, before+1, plans, "only the one at the cap was sent")
}

// TestPlanner_Withheld: a planner whose questions would go where its role
// doesn't name sends none: every question is unavailable with why, and
// the check refuses, all asking nothing; it says so once, at WARN.
func TestPlanner_Withheld(t *testing.T) {
	const why = "questions would leave this machine"
	r := newRigWith(t, &fakeModel{loaded: true}, func(o *Options) { o.Withheld = why })
	for range 2 {
		out := r.plan(t, "q")
		assert.Equal(t, StatusUnavailable, out.Status)
		assert.Equal(t, why, out.Reason)
	}
	out, err := r.p.Ask(context.Background(), "q")
	require.NoError(t, err)
	assert.Equal(t, StatusUnavailable, out.Status)
	_, err = r.p.Check(context.Background())
	require.EqualError(t, err, why)
	warms, probesAsked, plans := r.model.counts()
	assert.Zero(t, warms+probesAsked+plans)
	assert.Equal(t, why, r.p.State().Reason)
	var warns int
	for _, rec := range r.records(t) {
		if rec["level"] == "WARN" {
			warns++
			assert.Equal(t, why, rec["reason"])
		}
	}
	assert.Equal(t, 1, warns)
}

// TestPlanner_WarnsAgainAfterAPlan: a cause logged at WARN is at WARN
// again once a plan was made since, so an outage a week later is seen.
func TestPlanner_WarnsAgainAfterAPlan(t *testing.T) {
	var down atomic.Bool
	r := fitRig(t, func(context.Context, string) (json.RawMessage, error) {
		if down.Load() {
			return nil, fmt.Errorf("ollama generate: %w", generator.ErrUnreachable)
		}
		return json.RawMessage(`{"kind":"one"}`), nil
	})
	for _, d := range []bool{true, true, false, true, true} {
		down.Store(d)
		r.plan(t, "q")
	}
	var levels []string
	for _, rec := range r.records(t) {
		if rec["cause"] == "unavailable:generate" {
			levels = append(levels, rec["level"].(string))
		}
	}
	assert.Equal(t, []string{"WARN", "DEBUG", "WARN", "DEBUG"}, levels)
}

// TestPlanner_UnavailableCauses: asking whether the model is loaded and
// asking it for a plan fail as causes of their own, each warned of once.
func TestPlanner_UnavailableCauses(t *testing.T) {
	r := fitRig(t, func(context.Context, string) (json.RawMessage, error) {
		return nil, fmt.Errorf("ollama generate: %w", generator.ErrUnreachable)
	})
	r.plan(t, "q")
	r.model.mu.Lock()
	r.model.loadedErr = fmt.Errorf("ollama ps: %w", generator.ErrUnreachable)
	r.model.mu.Unlock()
	r.plan(t, "q")
	causes := map[string]string{}
	for _, rec := range r.records(t) {
		if cause, ok := rec["cause"].(string); ok {
			causes[cause] = rec["level"].(string)
		}
	}
	assert.Equal(t, map[string]string{"unavailable:generate": "WARN", "unavailable:loaded": "WARN"}, causes)
}

// TestPlanner_DebugLine: every question is logged at DEBUG with its
// status, time, model and prompt version, and nothing of its text.
func TestPlanner_DebugLine(t *testing.T) {
	r := fitRig(t, answerWith(`{"kind":"one","names":["Zqxjq"]}`))
	r.plan(t, "zqxjq words")
	var lines int
	for _, rec := range r.records(t) {
		if rec["msg"] != "search understanding: a question" {
			continue
		}
		lines++
		assert.Equal(t, "DEBUG", rec["level"])
		assert.Equal(t, PromptVersion, rec["prompt_version"])
		assert.Equal(t, "gemma4:26b", rec["model"])
		assert.Contains(t, rec, "took_ms")
		line, err := json.Marshal(rec)
		require.NoError(t, err)
		assert.NotContains(t, strings.ToLower(string(line)), "zqxjq")
	}
	assert.Equal(t, 2, lines, "the question that started the check, and the planned one")
	assert.Equal(t, StatusPlanned, r.p.State().Last)
}

// TestPlanner_State: State reads the planner's memory: on, its model, the
// check's verdict and when, the last question's status and the last
// failure.
func TestPlanner_State(t *testing.T) {
	r := newRig(t, &fakeModel{loaded: true, plan: answerWith(`{"kind":"one"}`)})
	assert.Equal(t, State{On: true, Model: "gemma4:26b", Check: CheckUnchecked}, r.p.State())
	r.plan(t, "q")
	r.p.Wait()
	s := r.p.State()
	assert.Equal(t, CheckFit, s.Check)
	assert.Equal(t, r.clock.Now(), s.CheckedAt)
	assert.Equal(t, StatusChecking, s.Last)
	assert.Equal(t, r.clock.Now(), s.LastAt)

	unfit := newRig(t, &fakeModel{loaded: true, probes: func(string) (json.RawMessage, error) {
		return json.RawMessage(`{"kind":"summary"}`), nil
	}})
	unfit.plan(t, "q")
	unfit.p.Wait()
	s = unfit.p.State()
	assert.Equal(t, CheckUnfit, s.Check)
	assert.Contains(t, s.Unfit, `didn't pass the check "no filter"`)
	assert.Equal(t, "unfit", s.Failure.Cause)
	assert.Equal(t, unfit.clock.Now(), s.Failure.At)

	off := New(context.Background(), &fakeModel{}, Options{Model: config.ModelSpec{Model: "m"}})
	assert.Equal(t, State{Model: "m", Reason: OffReason, Check: CheckUnchecked}, off.State())
}
