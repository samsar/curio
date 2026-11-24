package ask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/retrieve"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// fakeWriter is a streaming model: loaded or not, it loads when warmed
// (unless warmErr fails the load), and answers each prompt as answer
// says, piece by piece. Its answer to the fitness check's probes is a fit
// model's unless probe says otherwise.
type fakeWriter struct {
	mu      sync.Mutex
	loaded  bool
	warmErr error
	answer  func(ctx context.Context, prompt string) ([]string, generator.StreamEnd, error)
	probe   func(question string) string // a probe's answer; nil answers as a fit model does
	prompts []string
	opts    []generator.Options
	warms   int
	// usage is what an answer reports it cost.
	usage generator.Usage
}

func (*fakeWriter) Generate(context.Context, string, generator.Options) (string, error) {
	return "", nil
}
func (*fakeWriter) Model() string              { return "gemma4:26b" }
func (*fakeWriter) Ping(context.Context) error { return nil }

func (w *fakeWriter) Loaded(context.Context) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.loaded, nil
}

func (w *fakeWriter) Warm(context.Context, time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warms++
	if w.warmErr != nil {
		return w.warmErr
	}
	w.loaded = true
	return nil
}

// unload has Ollama drop the model, as it does for another model's room.
func (w *fakeWriter) unload() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loaded = false
}

func (w *fakeWriter) Unload(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loaded = false
	return nil
}

// fitProbeAnswer answers each probe as a fit model does.
func fitProbeAnswer(question string) string {
	switch {
	case strings.Contains(question, "sourdough"):
		return "Let it cool for at least an hour before slicing [2]."
	case strings.Contains(question, "mercury"):
		return Abstention
	default:
		return "Spring tides come at the new moon and the full moon [3]."
	}
}

// probeQuestion is the probe question prompt asks, "" for none.
func probeQuestion(prompt string) string {
	for _, p := range probes {
		if strings.HasSuffix(prompt, "Question: "+p.question+"\n") {
			return p.question
		}
	}
	return ""
}

func (w *fakeWriter) GenerateStream(ctx context.Context, prompt string, opts generator.Options,
	emit func(string) error) (generator.StreamEnd, error) {
	w.mu.Lock()
	w.prompts = append(w.prompts, prompt)
	w.opts = append(w.opts, opts)
	answer, probe := w.answer, w.probe
	w.mu.Unlock()
	if q := probeQuestion(prompt); q != "" {
		if probe == nil {
			probe = fitProbeAnswer
		}
		return generator.StreamEnd{}, emit(probe(q))
	}
	pieces, end, err := answer(ctx, prompt)
	for _, p := range pieces {
		if eerr := emit(p); eerr != nil {
			return generator.StreamEnd{}, eerr
		}
	}
	if err == nil && opts.Usage != nil {
		w.mu.Lock()
		*opts.Usage = w.usage
		w.mu.Unlock()
	}
	return end, err
}

// asks are the prompts that weren't probes.
func (w *fakeWriter) asks() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, p := range w.prompts {
		if probeQuestion(p) == "" {
			out = append(out, p)
		}
	}
	return out
}

// answers is an answer of pieces, written whole.
func answers(pieces ...string) func(context.Context, string) ([]string, generator.StreamEnd, error) {
	return func(context.Context, string) ([]string, generator.StreamEnd, error) {
		return pieces, generator.StreamEnd{}, nil
	}
}

// fakeRetriever answers every query with its retrieval, and every
// understanding with the retrieval's plan laid under the query's filters,
// recording the queries.
type fakeRetriever struct {
	mu         sync.Mutex
	got        retrieve.Retrieval
	err        error
	queries    []retrieve.Query
	understood []retrieve.Query
}

func (r *fakeRetriever) Retrieve(ctx context.Context, q retrieve.Query) (retrieve.Retrieval, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, q)
	if err := ctx.Err(); err != nil {
		return retrieve.Retrieval{}, err
	}
	return r.got, r.err
}

func (r *fakeRetriever) Understand(ctx context.Context, q retrieve.Query) (retrieve.Understood, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.understood = append(r.understood, q)
	if err := ctx.Err(); err != nil {
		return retrieve.Understood{}, err
	}
	out := retrieve.Understood{Plan: r.got.Plan, Understanding: r.got.Understanding, Filters: q.Filters}
	if p := r.got.Plan; p != nil {
		if q.Filters.SavedAfter == "" && q.Filters.SavedBefore == "" {
			out.Filters.SavedAfter, out.Filters.SavedBefore = p.Filters.SavedAfter, p.Filters.SavedBefore
		}
		if out.Understanding != nil {
			u := *out.Understanding
			applied := *p
			u.Plan = &applied
			out.Understanding = &u
		}
	}
	return out, r.err
}

// fakeStores are a library's counts and its documents' fields, summaries,
// bookmarks and chunks, ranked as ranked says (by document: its chunks,
// nearest first).
type fakeStores struct {
	counts    map[store.DocState]int
	fields    map[string]store.DocumentFields
	summaries map[string]*store.Summary
	bookmarks map[string][]*store.Bookmark
	chunks    map[string]*store.Chunk
	ranked    map[string][]string
	rankErr   error
	rankAsked [][]float32
	err       error
}

func (s *fakeStores) RankChunks(_ context.Context, _ string, vector []float32, ids []string) ([]store.RankedChunk,
	error) {
	s.rankAsked = append(s.rankAsked, vector)
	if s.rankErr != nil {
		return nil, s.rankErr
	}
	var out []store.RankedChunk
	for _, doc := range ids {
		for i, id := range s.ranked[doc] {
			out = append(out, store.RankedChunk{DocumentID: doc, ChunkID: id, Ord: s.chunks[id].Ord,
				Distance: float64(i)})
		}
	}
	return out, nil
}

func (s *fakeStores) GetByIDs(_ context.Context, ids []string) ([]*store.Chunk, error) {
	var out []*store.Chunk
	for _, id := range ids {
		if c, ok := s.chunks[id]; ok {
			out = append(out, c)
		}
	}
	return out, s.err
}

// fakeInterests are the latest rebuild's runs, read in turn (the last
// stays), its saves and places, and its identities' labels.
type fakeInterests struct {
	mu     sync.Mutex
	runs   []*store.InterestRun
	reads  int
	saves  map[string][]store.RunSave
	places map[string]map[string]store.DocumentInterest // by run, then document
	labels map[string]string
	err    error
}

func (f *fakeInterests) LatestRun(context.Context, string, store.InterestRunStatus) (*store.InterestRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if len(f.runs) == 0 {
		return nil, store.ErrNotFound
	}
	run := f.runs[min(f.reads, len(f.runs)-1)]
	f.reads++
	return run, nil
}

func (f *fakeInterests) RunSaves(_ context.Context, _, runID string) ([]store.RunSave, error) {
	return f.saves[runID], nil
}

func (f *fakeInterests) RunInterestsOf(_ context.Context, runID string, ids []string) ([]store.DocumentInterest,
	error) {
	out := make([]store.DocumentInterest, len(ids))
	for i, id := range ids {
		out[i] = f.places[runID][id]
		out[i].DocumentID = id
	}
	return out, nil
}

func (f *fakeInterests) GetInterests(_ context.Context, _ string, ids []string) ([]store.Interest, error) {
	var out []store.Interest
	for _, id := range ids {
		if label, ok := f.labels[id]; ok {
			out = append(out, store.Interest{ID: id, Label: label})
		}
	}
	return out, nil
}

func (s *fakeStores) CountByState(context.Context, string) (map[store.DocState]int, error) {
	return s.counts, s.err
}

func (s *fakeStores) FieldsOf(context.Context, string, []string) (map[string]store.DocumentFields, error) {
	return s.fields, s.err
}

func (s *fakeStores) CurrentSummaries(context.Context, string, []string) (map[string]*store.Summary, error) {
	return s.summaries, s.err
}

func (s *fakeStores) ListByDocuments(context.Context, string, []string) (map[string][]*store.Bookmark, error) {
	return s.bookmarks, s.err
}

// hit is a search hit of a fetched document id at url titled title, with
// passages as its chunks.
func hit(id, url, title string, state store.DocState, passages ...string) search.Hit {
	h := search.Hit{Document: &store.Document{ID: id, URL: url, Title: &title, State: state}}
	for i, p := range passages {
		h.Chunks = append(h.Chunks, search.ChunkMatch{ChunkID: id + "-" + string(rune('a'+i)), Text: p})
	}
	return h
}

// rig is an asker over a fake writer, retriever and stores, logging every
// record into logs.
type rig struct {
	a         *Asker
	writer    *fakeWriter
	ret       *fakeRetriever
	stores    *fakeStores
	interests *fakeInterests
	logs      *bytes.Buffer
	cancel    context.CancelFunc
}

// defaultHits are three fetched pages of a made-up library.
func defaultHits() []search.Hit {
	return []search.Hit{
		hit("d1", "https://example.com/kafka", "Kafka consumer groups", store.DocStateFetched,
			"A consumer group shares a topic's partitions among its members."),
		hit("d2", "https://example.org/rebalance", "Rebalancing", store.DocStateFetched,
			"A rebalance moves partitions when a member joins or leaves."),
		hit("d3", "https://example.net/static", "Static membership", store.DocStateFetched,
			"A static member keeps its partitions across a restart."),
	}
}

func newRig(t *testing.T, change func(*Options)) *rig {
	t.Helper()
	logs := &bytes.Buffer{}
	var mu sync.Mutex
	lifetime, cancel := context.WithCancel(context.Background())
	r := &rig{writer: &fakeWriter{loaded: true, answer: answers("Partitions are shared [1].")},
		ret:       &fakeRetriever{got: retrieve.Retrieval{Result: search.Result{Items: defaultHits()}}},
		stores:    &fakeStores{counts: map[store.DocState]int{store.DocStateFetched: 3}},
		interests: &fakeInterests{}, logs: logs, cancel: cancel}
	opts := Options{On: true, Sources: 8, MaxTokens: 768, Timeout: time.Minute, Rerank: true,
		Model:    config.ModelSpec{Provider: "ollama", Model: "gemma4:26b"},
		Location: time.UTC, Now: func() time.Time { return time.Date(2025, 4, 17, 15, 0, 0, 0, time.UTC) },
		Log: slog.New(slog.NewJSONHandler(lockedWriter{&mu, logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	if change != nil {
		change(&opts)
	}
	r.a = New(lifetime, r.writer, r.ret, Stores{Library: r.stores, Fields: r.stores, Summaries: r.stores,
		Bookmarks: r.stores, Chunks: r.stores, Interests: r.interests, TenantID: "local"}, opts)
	t.Cleanup(func() {
		cancel()
		r.a.Wait()
	})
	return r
}

// fit makes the writer's model checked fit, with one ask answered.
func (r *rig) fit(t *testing.T) {
	t.Helper()
	require.Equal(t, StatusAnswered, doneOf(t, r.ask(t, Request{Question: "warm-up"})).Status)
}

// lockedWriter serializes the log's writes, which the background work
// makes too.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// ask asks question, collecting the events, failing on an error.
func (r *rig) ask(t *testing.T, req Request) []Event {
	t.Helper()
	events, err := r.askErr(context.Background(), req)
	require.NoError(t, err)
	return events
}

func (r *rig) askErr(ctx context.Context, req Request) ([]Event, error) {
	var events []Event
	err := r.a.Ask(ctx, req, func(e Event) error {
		events = append(events, e)
		return nil
	})
	return events, err
}

// doneOf is the last event, a Done.
func doneOf(t *testing.T, events []Event) Done {
	t.Helper()
	require.NotEmpty(t, events)
	d, ok := events[len(events)-1].(Done)
	require.True(t, ok, "the last event is %T", events[len(events)-1])
	return d
}

// retrievedOf is the Retrieved event.
func retrievedOf(t *testing.T, events []Event) Retrieved {
	t.Helper()
	for _, e := range events {
		if r, ok := e.(Retrieved); ok {
			return r
		}
	}
	t.Fatal("no Retrieved event")
	return Retrieved{}
}

// text is the answer the Delta events carry.
func text(events []Event) string {
	var b strings.Builder
	for _, e := range events {
		if d, ok := e.(Delta); ok {
			b.WriteString(d.Text)
		}
	}
	return b.String()
}

// records are the log's records.
func (r *rig) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(r.logs.String()) {
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		out = append(out, rec)
	}
	return out
}

var errBoom = errors.New("boom")
