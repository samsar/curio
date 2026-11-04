package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/summarize"
	"github.com/samsar/curio/internal/understand"
)

// summariesOn is a summaries' writer that writes.
func summariesOn() *writerAt {
	return &writerAt{state: summarize.State{On: true, Model: "gemma4:26b", PromptVersion: "p1",
		Check: summarize.CheckFit}}
}

// TestHealth_SummariesReadNothing: healthz reports the summaries from the
// writer's, the pass's and the person's state alone, reading no store, so
// a summary store that fails every call doesn't fail it; an outage and the
// brake are reported, and neither changes the status.
func TestHealth_SummariesReadNothing(t *testing.T) {
	writer := summariesOn()
	writer.state.Outage = summarize.Outage{Since: time.Now().Add(-time.Minute), Err: "ollama unreachable"}
	until := time.Now().Add(15 * time.Minute)
	s := newTestServer(t, func(d *Deps) {
		// Any call through it panics: nothing implements it.
		d.Summaries = struct{ store.SummaryStore }{}
		d.SummaryWriter = writer
		d.SummaryFeed = &summaryFeedAt{progress: jobs.SummaryProgress{Brake: jobs.Brake{Until: until, Failures: 3,
			Documents: []string{"d1"}, Err: "HTTP 500"}}}
		d.Foreground = jobs.NewForeground(time.Hour)
	})
	end := s.deps.Foreground.Begin()
	defer end()
	var h Health
	s.getJSON(t, "/v1/healthz", &h)
	assert.Equal(t, "ok", h.Status, "summaries are not health")
	assert.True(t, h.Summaries.On)
	assert.Equal(t, "gemma4:26b", h.Summaries.Model)
	require.NotNil(t, h.Summaries.Outage)
	assert.Equal(t, "ollama unreachable", h.Summaries.Outage.Error)
	require.NotNil(t, h.Summaries.Brake)
	assert.Equal(t, 3, h.Summaries.Brake.Failures)
	assert.False(t, h.Summaries.YieldingUntil.IsZero(), "a use in flight: the pass yields")
}

// failingSummaryCount is a summary store whose counts fail.
type failingSummaryCount struct{ store.SummaryStore }

func (failingSummaryCount) CountSummaries(context.Context, string, store.SummaryWriter) (store.SummaryCounts,
	error) {
	return store.SummaryCounts{}, errInjected
}

// TestStats_SummaryCountFailure: a summary count the store can't produce
// fails /v1/stats, as every other count does, while summaries are on.
func TestStats_SummaryCountFailure(t *testing.T) {
	s := newTestServer(t, func(d *Deps) {
		d.Summaries = failingSummaryCount{d.Summaries}
		d.SummaryWriter = summariesOn()
	})
	assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/stats"}), http.StatusInternalServerError)
}

// countingSummaries counts the summary counts read through it.
type countingSummaries struct {
	store.SummaryStore
	counts atomic.Int32
}

func (c *countingSummaries) CountSummaries(ctx context.Context, tenantID string, w store.SummaryWriter) (
	store.SummaryCounts, error) {
	c.counts.Add(1)
	return c.SummaryStore.CountSummaries(ctx, tenantID, w)
}

// feedWriterAt is a writer the pass feeds, in writerAt's state.
type feedWriterAt struct{ *writerAt }

func (feedWriterAt) Refusal() string         { return "" }
func (feedWriterAt) Feed() summarize.Feed    { return summarize.FeedAll }
func (feedWriterAt) CheckRetryAt() time.Time { return time.Time{} }
func (feedWriterAt) Probe(context.Context)   {}

// TestStats_SummaryCountsThroughThePass: /v1/stats reads the summary
// counts through the pass, which counts the library again only after a
// change it hears of, or once they are past their freshness, and says when
// they were counted: the dashboard's Status page asks every 2 s.
func TestStats_SummaryCountsThroughThePass(t *testing.T) {
	var counting *countingSummaries
	var feed *jobs.Summaries
	s := newTestServer(t, func(d *Deps) {
		counting = &countingSummaries{SummaryStore: d.Summaries}
		writer := summariesOn()
		feed = jobs.NewSummaries(jobs.SummariesOptions{Store: counting, Writer: feedWriterAt{writer},
			TenantID: d.TenantID, CountsFresh: time.Hour})
		d.Summaries, d.SummaryWriter, d.SummaryFeed = counting, writer, feed
	})
	var first, second Stats
	s.getJSON(t, "/v1/stats", &first)
	s.getJSON(t, "/v1/stats", &second)
	require.NotNil(t, first.Summaries)
	assert.Equal(t, int32(1), counting.counts.Load(), "fresh counts served again")
	assert.Equal(t, first.Summaries.CountedAt, second.Summaries.CountedAt)
	feed.Kick()
	s.getJSON(t, "/v1/stats", &second)
	assert.Equal(t, int32(2), counting.counts.Load(), "a change heard of: counted again")
}

// TestSummarizeAll: summarize-all owes the summaries its scope names and
// kicks the pass, which writes them; with summaries off it is a 409 that
// owes nothing and kicks nothing.
func TestSummarizeAll(t *testing.T) {
	feed := &summaryFeedAt{}
	s := newTestServer(t, func(d *Deps) {
		d.SummaryWriter = summariesOn()
		d.SummaryFeed = feed
	})
	resp := s.do(t, request{method: http.MethodPost, path: "/v1/documents/summarize-all?scope=all"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	var got map[string]int
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, map[string]int{"summaries_owed": 0}, got)
	assert.Equal(t, 1, feed.kicked(), "the pass is kicked")

	off := &summaryFeedAt{}
	s = newTestServer(t, func(d *Deps) {
		d.SummaryWriter = &writerAt{state: summarize.State{Reason: summarize.OffReason}}
		d.SummaryFeed = off
	})
	assertProblem(t, s.do(t, request{method: http.MethodPost, path: "/v1/documents/summarize-all"}),
		http.StatusConflict)
	assert.Zero(t, off.kicked())

	s = newTestServer(t, func(d *Deps) {
		d.SummaryWriter = &writerAt{state: summarize.State{Model: "gemma4:26b", Reason: summarize.OffByDefaultReason,
			OffByDefault: true}}
		d.SummaryFeed = off
	})
	resp = s.do(t, request{method: http.MethodPost, path: "/v1/documents/summarize-all"})
	assertProblem(t, resp, http.StatusConflict)
	assert.Contains(t, resp.body, "off by default", "the 409 says why they are off")
	assert.Contains(t, resp.body, "summaries.enabled: true", "and how to turn them on")
	var h Health
	s.getJSON(t, "/v1/healthz", &h)
	assert.False(t, h.Summaries.On)
	assert.True(t, h.Summaries.OffByDefault)
	assert.Equal(t, summarize.OffByDefaultReason, h.Summaries.Reason)
}

// inUse reports whether a use of the models is in flight on f.
func inUse(f *jobs.Foreground) bool {
	until, quiet := f.Quiet(time.Now())
	return !quiet && until.IsZero()
}

// useRecorder records, at each step of a search, whether a use of the
// models was in flight.
type useRecorder struct {
	f     *jobs.Foreground
	mu    sync.Mutex
	steps map[string]bool
}

func (r *useRecorder) record(step string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps[step] = inUse(r.f)
}

// recordingExtractions records the use while a search's hits are hydrated.
type recordingExtractions struct {
	store.ExtractionStore
	r *useRecorder
}

func (e recordingExtractions) GetByID(ctx context.Context, id string) (*store.DocumentExtraction, error) {
	e.r.record("hydrate")
	return e.ExtractionStore.GetByID(ctx, id)
}

// TestSearch_MarksOneUse: a search, its plan, its query's embed and the
// hydration of its hits, is one use of the models, begun before the plan
// and ended once the answer is built, so the summary pass yields to all of
// it; nothing is left in flight after.
func TestSearch_MarksOneUse(t *testing.T) {
	fg := jobs.NewForeground(time.Nanosecond)
	rec := &useRecorder{f: fg, steps: map[string]bool{}}
	emb := embedFunc(func(context.Context, []string) ([][]float32, error) {
		rec.record("embed")
		return [][]float32{unitVec()}, nil
	})
	s := newSearchServer(t, emb, search.Config{BM25Weight: 1, VectorWeight: 1}, func(d *Deps) {
		d.Foreground = fg
		d.Extractions = recordingExtractions{d.Extractions, rec}
		d.Planner = plannerFunc(func(context.Context, string) (understand.Outcome, error) {
			rec.record("plan")
			return understand.Outcome{Status: understand.StatusTimeout}, nil
		})
	})
	begun := fg.Begun()
	resp := s.search(t, `{"query":"kafka","understand":true}`)
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.Equal(t, map[string]bool{"plan": true, "embed": true, "hydrate": true}, rec.steps)
	select {
	case <-begun:
	default:
		t.Fatal("the search began no use")
	}
	_, quiet := fg.Quiet(time.Now().Add(time.Second))
	assert.True(t, quiet, "the use ended with the answer")
	assert.False(t, inUse(fg))
}

// TestSearch_RefusedMarksNoUse: a search refused before it reads anything
// marks no use: the pass doesn't yield to a request that asks no model.
func TestSearch_RefusedMarksNoUse(t *testing.T) {
	fg := jobs.NewForeground(time.Hour)
	s := newTestServer(t, func(d *Deps) {
		d.Foreground = fg
		d.Search = search.New(d.Chunks, d.Documents, d.Index, okEmbedder(),
			search.Config{Log: slog.New(slog.DiscardHandler)})
	})
	assertProblem(t, s.search(t, `{"query":""}`), http.StatusBadRequest)
	_, quiet := fg.Quiet(time.Now())
	assert.True(t, quiet)
}
