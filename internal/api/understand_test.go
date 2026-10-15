package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// fakePlanner answers every question with its outcome, or with the
// caller's context error once that ends while block is set, recording the
// questions.
type fakePlanner struct {
	mu        sync.Mutex
	outcome   understand.Outcome
	block     bool
	questions []string
}

func (p *fakePlanner) Plan(ctx context.Context, question string) (understand.Outcome, error) {
	p.mu.Lock()
	p.questions = append(p.questions, question)
	out, block := p.outcome, p.block
	p.mu.Unlock()
	if block {
		<-ctx.Done()
		return understand.Outcome{}, ctx.Err()
	}
	return out, nil
}

// plannerFunc adapts a function to Planner.
type plannerFunc func(ctx context.Context, question string) (understand.Outcome, error)

func (f plannerFunc) Plan(ctx context.Context, question string) (understand.Outcome, error) {
	return f(ctx, question)
}

func (plannerFunc) Timeout() time.Duration { return 5 * time.Second }

func (plannerFunc) State() understand.State {
	return understand.State{On: true, Model: "gemma4:26b", Check: understand.CheckUnchecked}
}

func (*fakePlanner) Timeout() time.Duration { return 5 * time.Second }

func (*fakePlanner) State() understand.State {
	return understand.State{On: true, Model: "gemma4:26b", Check: understand.CheckUnchecked}
}

// statePlanner is a Planner whose State is state.
type statePlanner struct {
	Planner
	state understand.State
}

func (p statePlanner) State() understand.State { return p.state }

func (p *fakePlanner) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.questions)
}

// planLibrary is a server whose search ranks by the keyword lists alone,
// rewrites at 0.5 and names at 1, over four documents a plan for "kafka"
// tells apart:
//
//	article  "kafka partitions explained"  found by the question
//	video    "kafka streams talk"          found by the question; a video
//	compact  "event log compaction"        found only by the rewrite "log compaction"
//	book     titled "Designing Data-Intensive Applications", text "chapter notes"
//	                                       found only by the name
type planLibrary struct {
	*testServer
	planner                       *fakePlanner
	article, video, compact, book string
}

func newPlanLibrary(t *testing.T) *planLibrary {
	t.Helper()
	planner := &fakePlanner{}
	s := newTestServer(t, func(d *Deps) {
		d.Search = search.New(d.Chunks, d.Documents, d.Index, everyText{}, search.Config{BM25Weight: 1,
			FieldsWeight: 1, TitleWeight: 1, RewriteWeight: 0.5, NameWeight: 1, Log: slog.New(slog.DiscardHandler)})
		d.Planner = planner
	})
	l := &planLibrary{testServer: s, planner: planner}
	add := func(url, title string, ct store.ContentType, text string) string {
		t.Helper()
		ctx := context.Background()
		doc := &store.Document{TenantID: "local", URL: url, ContentType: ct, State: store.DocStateFetched}
		if title != "" {
			doc.Title = &title
		}
		require.NoError(t, s.deps.Documents.Create(ctx, doc))
		ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK}
		require.NoError(t, s.deps.Extractions.Create(ctx, ext))
		require.NoError(t, s.deps.Documents.SetCurrentExtraction(ctx, doc.ID, ext.ID))
		require.NoError(t, s.deps.Chunks.ReplaceForDocument(ctx, doc.ID, ext.ID, title, nil,
			[]store.ChunkInput{{Text: text, Embedding: unitVec()}}))
		return doc.ID
	}
	l.article = add("https://example.com/article", "", store.ContentTypeArticle, "kafka partitions explained")
	l.video = add("https://example.com/video", "", store.ContentTypeVideo, "kafka streams talk")
	l.compact = add("https://example.com/compact", "", store.ContentTypeArticle, "event log compaction")
	l.book = add("https://example.com/book", "Designing Data-Intensive Applications", store.ContentTypeArticle,
		"chapter notes")
	return l
}

// everyText embeds every text as unitVec.
type everyText struct{}

func (everyText) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = unitVec()
	}
	return out, nil
}

// kafkaPlan is the plan the tests' planner gives "kafka": articles only,
// a rewrite and a name.
func kafkaPlan() *understand.Plan {
	return &understand.Plan{Kind: understand.KindList,
		Filters:  search.FilterRequest{ContentType: []string{string(store.ContentTypeArticle)}},
		Rewrites: []string{"log compaction"}, Names: []string{"Designing Data-Intensive Applications"}}
}

func plannedKafka() understand.Outcome {
	return understand.Outcome{Status: understand.StatusPlanned, Model: "gemma4:26b", Took: 1200 * time.Millisecond,
		Plan: kafkaPlan(), Dropped: []understand.Dropped{{Field: "source", Reason: `the question doesn't name the browser "chrome"`}}}
}

// TestSearch_Understand: understand asks the planner once, and the search
// applies its filters, rewrites and names: the video is filtered out, and
// the documents only the rewrite and the name find are in. Understanding
// reports the plan as applied, the model, how long it took and what the
// guards dropped.
func TestSearch_Understand(t *testing.T) {
	l := newPlanLibrary(t)
	l.planner.outcome = plannedKafka()
	literal := decodeSearch(t, l.search(t, `{"query":"kafka"}`))
	assert.ElementsMatch(t, []string{l.article, l.video}, searchIDs(literal.Items))
	assert.Nil(t, literal.Understanding, "a request without understand or plan has none")
	assert.Zero(t, l.planner.calls())

	resp := l.search(t, `{"query":"kafka","understand":true}`)
	got := decodeSearch(t, resp)
	assert.Equal(t, []string{"kafka"}, l.planner.questions)
	assert.ElementsMatch(t, []string{l.article, l.compact, l.book}, searchIDs(got.Items))
	require.NotNil(t, got.Understanding)
	assert.Equal(t, Understanding{Status: "planned", Model: "gemma4:26b", TookMS: 1200,
		Plan: &PlanJSON{Kind: "list", Filters: Filters{ContentType: []string{"article"}},
			Rewrites: []string{"log compaction"}, Names: []string{"Designing Data-Intensive Applications"}},
		Dropped: []DroppedFilter{{Field: "source", Reason: `the question doesn't name the browser "chrome"`}}},
		*got.Understanding)
	assert.GreaterOrEqual(t, got.TookMS, int64(0))
}

// TestSearch_SuppliedPlan: a plan the request supplies is searched as is,
// with no model asked.
func TestSearch_SuppliedPlan(t *testing.T) {
	l := newPlanLibrary(t)
	plan := `{"kind":"list","filters":{"content_type":["article"]},"rewrites":["log compaction"],` +
		`"names":["Designing Data-Intensive Applications"]}`
	got := decodeSearch(t, l.search(t, `{"query":"kafka","plan":`+plan+`}`))
	assert.Zero(t, l.planner.calls())
	assert.ElementsMatch(t, []string{l.article, l.compact, l.book}, searchIDs(got.Items))
	require.NotNil(t, got.Understanding)
	assert.Equal(t, "supplied", got.Understanding.Status)
	assert.Empty(t, got.Understanding.Reason)
	assert.Empty(t, got.Understanding.Model)
	echoed, err := json.Marshal(got.Understanding.Plan)
	require.NoError(t, err)
	assert.JSONEq(t, plan, string(echoed))
}

// TestSearch_PlanRefused: a request with understand and a plan, or with a
// plan Validate refuses, is a 400 naming what is wrong, and searches and
// plans nothing.
func TestSearch_PlanRefused(t *testing.T) {
	l := newPlanLibrary(t)
	for body, detail := range map[string]string{
		`{"query":"kafka","understand":true,"plan":{"kind":"one"}}`:                  "send understand or plan, not both",
		`{"query":"kafka","plan":{"kind":"summary"}}`:                                "plan.kind: ",
		`{"query":"kafka","plan":{"kind":"one","rewrites":["a","b","c","d"]}}`:       "plan.rewrites: 4 of them, at most 3",
		`{"query":"kafka","plan":{"kind":"one","names":[""]}}`:                       "plan.names: an empty one",
		`{"query":"kafka","plan":{"kind":"one","filters":{"saved_after":"soon"}}}`:   "plan.filters.saved_after: ",
		`{"query":"kafka","plan":{"kind":"one","filters":{"include_undated":true}}}`: "plan.filters.include_undated: ",
		`{"query":"kafka","plan":{"kind":"one","site":"example.com"}}`:               `unknown field "site"`,
	} {
		t.Run(body, func(t *testing.T) {
			p := assertProblem(t, l.search(t, body), http.StatusBadRequest)
			assert.Contains(t, p.Detail, detail)
		})
	}
	assert.Zero(t, l.planner.calls())
}

// TestSearch_ClientFiltersWin: the request's own filter of a dimension
// wins over the plan's, which is reported dropped; the plan's other
// dimensions apply.
func TestSearch_ClientFiltersWin(t *testing.T) {
	l := newPlanLibrary(t)
	l.planner.outcome = plannedKafka()
	got := decodeSearch(t, l.search(t, `{"query":"kafka","understand":true,"filters":{"content_type":["video"]}}`))
	assert.Equal(t, []string{l.video}, searchIDs(got.Items), "the request's videos, not the plan's articles")
	require.NotNil(t, got.Understanding)
	assert.Contains(t, got.Understanding.Dropped, DroppedFilter{Field: "content_type",
		Reason: "the request sets content_type"})
	assert.Empty(t, got.Understanding.Plan.Filters.ContentType, "the plan as applied")
	assert.Equal(t, []string{"log compaction"}, got.Understanding.Plan.Rewrites)
}

// TestSearch_RequestFiltersCheckedAlone: include_undated without a date of
// the request's own is refused, though the plan would set a window.
func TestSearch_RequestFiltersCheckedAlone(t *testing.T) {
	l := newPlanLibrary(t)
	p := assertProblem(t, l.search(t, `{"query":"kafka","filters":{"include_undated":true},`+
		`"plan":{"kind":"one","filters":{"saved_after":"2024-01-01"}}}`), http.StatusBadRequest)
	assert.Contains(t, p.Detail, "filters.include_undated")
}

// TestMergeFilters: each dimension the request sets is its own, the plan's
// of that dimension dropped; a window is one dimension; include_undated
// goes with the request's dates when it sets any.
func TestMergeFilters(t *testing.T) {
	plan := search.FilterRequest{ContentType: []string{"video"}, Source: []string{"safari"},
		SavedAfter: "2024-01-01T00:00:00-05:00", SavedBefore: "2025-01-01T00:00:00-05:00",
		PublishedBefore: "2020-01-01", IncludeUndated: true}
	for _, tc := range []struct {
		name    string
		req     search.FilterRequest
		want    search.FilterRequest
		dropped []string
	}{
		{"nothing set", search.FilterRequest{}, plan, nil},
		{"a type", search.FilterRequest{ContentType: []string{"pdf"}}, search.FilterRequest{
			ContentType: []string{"pdf"}, Source: plan.Source, SavedAfter: plan.SavedAfter,
			SavedBefore: plan.SavedBefore, PublishedBefore: plan.PublishedBefore, IncludeUndated: true},
			[]string{"content_type"}},
		{"one bound of the saved window", search.FilterRequest{SavedBefore: "2023-01-01"}, search.FilterRequest{
			ContentType: plan.ContentType, Source: plan.Source, SavedBefore: "2023-01-01",
			PublishedBefore: plan.PublishedBefore}, []string{"saved", "include_undated"}},
		{"the request's dates, undated kept", search.FilterRequest{PublishedAfter: "2019-01-01",
			IncludeUndated: true}, search.FilterRequest{ContentType: plan.ContentType, Source: plan.Source,
			SavedAfter: plan.SavedAfter, SavedBefore: plan.SavedBefore, PublishedAfter: "2019-01-01",
			IncludeUndated: true}, []string{"published"}},
		{"a site the plan hasn't", search.FilterRequest{Site: []string{"example.com"}}, search.FilterRequest{
			ContentType: plan.ContentType, Source: plan.Source, Site: []string{"example.com"},
			SavedAfter: plan.SavedAfter, SavedBefore: plan.SavedBefore, PublishedBefore: plan.PublishedBefore,
			IncludeUndated: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped := mergeFilters(tc.req, plan)
			assert.Equal(t, tc.want, got)
			var fields []string
			for _, d := range dropped {
				fields = append(fields, d.Field)
			}
			assert.Equal(t, tc.dropped, fields)
			_, err := got.Resolve()
			assert.NoError(t, err)
		})
	}
}

// TestSearch_Fallbacks: every way a question goes without a plan answers
// 200 with the literal search's results, and the status and reason; with
// no planner, understanding is off.
func TestSearch_Fallbacks(t *testing.T) {
	l := newPlanLibrary(t)
	for _, status := range []understand.Status{understand.StatusCold, understand.StatusChecking,
		understand.StatusUnfit, understand.StatusTimeout, understand.StatusUnavailable, understand.StatusInvalid,
		understand.StatusOff} {
		t.Run(string(status), func(t *testing.T) {
			l.planner.outcome = understand.Outcome{Status: status, Reason: "why " + string(status),
				Model: "gemma4:26b"}
			got := decodeSearch(t, l.search(t, `{"query":"kafka","understand":true}`))
			assert.ElementsMatch(t, []string{l.article, l.video}, searchIDs(got.Items))
			require.NotNil(t, got.Understanding)
			assert.Equal(t, string(status), got.Understanding.Status)
			assert.Equal(t, "why "+string(status), got.Understanding.Reason)
			assert.Nil(t, got.Understanding.Plan)
		})
	}

	off := newSearchServer(t, everyText{}, search.Config{})
	got := decodeSearch(t, off.search(t, `{"query":"kafka","understand":true}`))
	assert.Len(t, got.Items, 3)
	assert.Equal(t, &Understanding{Status: "off", Reason: "search understanding is off (search.understand: false)"},
		got.Understanding)
}

// TestSearch_UnderstandCancelled: a request whose context ends while it is
// understood gets the context's error, and nothing is searched.
func TestSearch_UnderstandCancelled(t *testing.T) {
	l := newPlanLibrary(t)
	l.planner.block = true
	d := l.deps
	d.TenantID = store.LocalTenantID
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := d.search(ctx, SearchRequest{Query: "kafka", Understand: true})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestSearch_PlanPagingNoSecondModelCall: an understood search's pages,
// asked with the plan its first page returned, are windows of the plan's
// k=100 ranking, with the model asked once.
func TestSearch_PlanPagingNoSecondModelCall(t *testing.T) {
	l := newPlanLibrary(t)
	l.planner.outcome = plannedKafka()
	ctx := context.Background()
	for i := range 120 {
		doc := l.seedDocument(t, fmt.Sprintf("https://example.com/compaction/%03d", i), store.DocStateFetched)
		ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK}
		require.NoError(t, l.deps.Extractions.Create(ctx, ext))
		require.NoError(t, l.deps.Chunks.ReplaceForDocument(ctx, doc.ID, ext.ID, "", nil, []store.ChunkInput{{
			Text: fmt.Sprintf("kafka log compaction note %d", i), Embedding: unitVec()}}))
		_, err := l.db.Exec(`UPDATE documents SET content_type = 'article' WHERE id = ?`, doc.ID)
		require.NoError(t, err)
	}
	first := decodeSearch(t, l.search(t, `{"query":"kafka","understand":true,"k":10}`))
	require.NotNil(t, first.Understanding.Plan)
	plan, err := json.Marshal(first.Understanding.Plan)
	require.NoError(t, err)

	whole := decodeSearch(t, l.search(t, `{"query":"kafka","k":100,"plan":`+string(plan)+`}`))
	require.Len(t, whole.Items, 100)
	paged := first.Items
	for offset := 10; offset <= 90; offset += 10 {
		page := decodeSearch(t, l.search(t, fmt.Sprintf(`{"query":"kafka","k":10,"offset":%d,"plan":%s}`, offset,
			plan)))
		assert.Equal(t, "supplied", page.Understanding.Status)
		paged = append(paged, page.Items...)
	}
	assert.Equal(t, searchIDs(whole.Items), searchIDs(paged))
	assert.Equal(t, 1, l.planner.calls(), "the model is asked for page 1 alone")
}

// TestSearch_DropAFilter: the echoed plan without one of its filters
// searches as the plan did but for that dimension.
func TestSearch_DropAFilter(t *testing.T) {
	l := newPlanLibrary(t)
	l.planner.outcome = plannedKafka()
	planned := decodeSearch(t, l.search(t, `{"query":"kafka","understand":true}`))
	p := *planned.Understanding.Plan
	p.Filters.ContentType = nil
	plan, err := json.Marshal(p)
	require.NoError(t, err)
	got := decodeSearch(t, l.search(t, `{"query":"kafka","plan":`+string(plan)+`}`))
	assert.ElementsMatch(t, append(searchIDs(planned.Items), l.video), searchIDs(got.Items),
		"the video is back; the rewrite's and the name's documents stay")
	assert.Equal(t, &p, got.Understanding.Plan)
}
