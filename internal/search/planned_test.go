package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// planStore is a chunk store and document index whose lists are given per
// query text, and that records the text each read was asked for: a
// keyword list by its MATCH expression's text, a vector list by the text
// whose vector it was given. Its embedder (embed) gives text i of texts
// the 1-wide vector {i}.
type planStore struct {
	store.ChunkStore
	store.DocumentIndex
	texts                 []string
	bm25, vector          map[string][]store.ChunkHit
	fields, cards, titles map[string][]store.DocumentHit

	mu    sync.Mutex
	reads map[string][]string // list → the texts it was read for, in no order
	calls [][]string          // each Embed call's texts
}

// keyword is the text a sanitized MATCH expression was built from, among
// s.texts.
func (s *planStore) keyword(match string) string {
	for _, t := range s.texts {
		if match == sanitizeBM25Query(t) || match == titleQuery(t) {
			return t
		}
	}
	return "unknown MATCH " + match
}

func (s *planStore) record(list, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reads == nil {
		s.reads = map[string][]string{}
	}
	s.reads[list] = append(s.reads[list], text)
}

func (s *planStore) BM25Search(_ context.Context, _, query string, _ int, _ store.SearchFilters) ([]store.ChunkHit, error) {
	t := s.keyword(query)
	s.record("bm25", t)
	return s.bm25[t], nil
}

func (s *planStore) VectorSearch(_ context.Context, _ string, v []float32, _ int, _ store.SearchFilters) ([]store.ChunkHit, error) {
	t := s.texts[int(v[0])]
	s.record("vector", t)
	return s.vector[t], nil
}

func (s *planStore) SearchFields(_ context.Context, _, query string, _ int, _ store.SearchFilters) ([]store.DocumentHit, error) {
	t := s.keyword(query)
	s.record("fields", t)
	return s.fields[t], nil
}

func (s *planStore) SearchCards(_ context.Context, _ string, v []float32, _ int, _ store.SearchFilters) ([]store.DocumentHit, error) {
	t := s.texts[int(v[0])]
	s.record("cards", t)
	return s.cards[t], nil
}

func (s *planStore) SearchTitles(_ context.Context, _, query string, _ int, _ store.SearchFilters) ([]store.DocumentHit, error) {
	t := s.keyword(query)
	s.record("titles", t)
	return s.titles[t], nil
}

func (*planStore) GetByIDs(_ context.Context, ids []string) ([]*store.Chunk, error) {
	return listChunks{}.GetByIDs(context.Background(), ids)
}

func (*planStore) Snippets(context.Context, string, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

// embed embeds each text as {its index in s.texts}, recording the call.
func (s *planStore) embed(_ context.Context, texts []string) ([][]float32, error) {
	s.mu.Lock()
	s.calls = append(s.calls, slices.Clone(texts))
	s.mu.Unlock()
	out := make([][]float32, len(texts))
	for i, t := range texts {
		at := slices.Index(s.texts, t)
		if at < 0 {
			return nil, fmt.Errorf("embedded an unknown text %q", t)
		}
		out[i] = []float32{float32(at)}
	}
	return out, nil
}

// The plan the fusion tests search: a question, a rewrite and a name.
const (
	question = "zebra stripes"
	rewrite  = "equine camouflage"
	name     = "Okapi Field Guide"
)

// newPlanStore is the library of TestEngine_RewriteFusion:
//
//	question  bm25 c1(A)  vector c2(B)  fields A
//	rewrite   bm25 c3(C)  vector c2(B)  fields B  titles C
//	name                                fields D  titles D  cards A
func newPlanStore() *planStore {
	return &planStore{
		texts:  []string{question, rewrite, name},
		bm25:   map[string][]store.ChunkHit{question: {chunkHit("c1", "A")}, rewrite: {chunkHit("c3", "C")}},
		vector: map[string][]store.ChunkHit{question: {chunkHit("c2", "B")}, rewrite: {chunkHit("c2", "B")}},
		fields: map[string][]store.DocumentHit{question: docHits("A"), rewrite: docHits("B"), name: docHits("D")},
		cards:  map[string][]store.DocumentHit{name: docHits("A")},
		titles: map[string][]store.DocumentHit{rewrite: docHits("C"), name: docHits("D")},
	}
}

// defaultWeights are config.yaml's default weights, a plan's included.
var defaultWeights = Config{BM25Weight: 1, VectorWeight: 1, FieldsWeight: 0.25, CardsWeight: 0.5, TitleWeight: 2,
	RewriteWeight: 0.5, NameWeight: 1}

// TestEngine_RewriteFusion: a rewrite reads every list at RewriteWeight
// times the list's weight, and a name the document lists at NameWeight
// times theirs; every chunk list fuses in one RRF and every document list
// adds after it, in a fixed order.
func TestEngine_RewriteFusion(t *testing.T) {
	s := newPlanStore()
	engine := New(s, idDocs{}, s, embedFunc(s.embed), defaultWeights)
	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: question,
		Rewrites: []string{rewrite}, Names: []string{name}})
	require.NoError(t, err)
	// With rrf_k 60, a rewrite at 0.5 and a name at 1:
	//
	//	A = 1/61 (c1, the question's BM25)
	//	    + 0.25/61 (the question's fields) + 0.5/61 (the name's cards)  = 1.75/61
	//	B = 1/61 (c2, the question's vector) + 0.5/61 (c2, the rewrite's)
	//	    + 0.125/61 (the rewrite's fields, 0.25 × 0.5)                   = 1.625/61
	//	C = 0.5/61 (c3, first only in the rewrite's BM25, 1 × 0.5)
	//	    + 1/61 (the rewrite's titles, 2 × 0.5)                          = 1.5/61
	//	D = 0.25/61 (the name's fields) + 2/61 (the name's titles)         = 2.25/61
	want := []struct {
		id    string
		score float64
	}{
		{"D", rr(0.25, 1) + rr(2, 1)},
		{"A", rr(1, 1) + rr(0.25, 1) + rr(0.5, 1)},
		{"B", rr(1, 1) + rr(0.5, 1) + rr(0.125, 1)},
		{"C", rr(0.5, 1) + rr(1, 1)},
	}
	require.Len(t, res.Items, len(want))
	for i, w := range want {
		assert.Equal(t, w.id, res.Items[i].Document.ID, "rank %d", i+1)
		assert.Equal(t, w.score, res.Items[i].Score, w.id)
	}
	assert.Equal(t, 2, res.BM25Hits, "c1 and c3")
	assert.Equal(t, 1, res.VectorHits, "c2, found by two lists, is one chunk")
}

// TestEngine_NamesOnlyDocumentLists: a name never reaches the chunk lists,
// whatever their weights.
func TestEngine_NamesOnlyDocumentLists(t *testing.T) {
	s := newPlanStore()
	engine := New(s, idDocs{}, s, embedFunc(s.embed), defaultWeights)
	_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: question,
		Rewrites: []string{rewrite}, Names: []string{name}})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{question, rewrite}, s.reads["bm25"])
	assert.ElementsMatch(t, []string{question, rewrite}, s.reads["vector"])
	for _, list := range []string{"fields", "titles", "cards"} {
		assert.Contains(t, s.reads[list], name, list)
	}
}

// TestEngine_NamesMatchWhatWasSaved: over a real store, a name lifts the
// document saved under it, through its title, and leaves the one whose
// text alone mentions it as it was.
func TestEngine_NamesMatchWhatWasSaved(t *testing.T) {
	l := newCardsLibrary(t)
	inText := l.add(t, "https://example.com/notes", "Savanna notes", "okapi field guide, as the notes call it",
		store.DocStateFetched, 0.5, 0.5)
	saved := l.add(t, "https://example.com/guide", "Okapi Field Guide", "forest mammals of the congo",
		store.DocStateFetched, 0.5, 0.5)
	cfg := Config{BM25Weight: 1, VectorWeight: 1, FieldsWeight: 0.25, TitleWeight: 2, RewriteWeight: 0.5, NameWeight: 1,
		Log: slog.New(slog.DiscardHandler)}
	engine := New(l.chunks, l.docs, l.index, &fakeEmbedder{dim: l.dim}, cfg)
	score := func(res *Result, id string) float64 {
		for _, h := range res.Items {
			if h.Document.ID == id {
				return h.Score
			}
		}
		t.Fatalf("%s isn't among the hits", id)
		return 0
	}

	req := Request{TenantID: "local", Query: "congo notes"}
	plain, err := engine.Search(context.Background(), req)
	require.NoError(t, err)
	req.Names = []string{"Okapi Field Guide"}
	named, err := engine.Search(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, score(plain, inText), score(named, inText), "a name never reads the chunks")
	assert.Greater(t, score(named, saved), score(plain, saved), "the name matches its title")
	assert.Equal(t, saved, named.Items[0].Document.ID)
}

// TestEngine_PlanEmbedsOnce: every text that needs a vector is embedded in
// one call, the question first: the rewrites always, the names only for
// the cards.
func TestEngine_PlanEmbedsOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want [][]string
	}{
		{"every list", defaultWeights, [][]string{{"q: " + question, "q: " + rewrite, "q: " + name}}},
		{"no cards", Config{BM25Weight: 1, VectorWeight: 1, FieldsWeight: 1, RewriteWeight: 0.5, NameWeight: 1},
			[][]string{{"q: " + question, "q: " + rewrite}}},
		{"cards alone", Config{CardsWeight: 1, RewriteWeight: 0.5, NameWeight: 1},
			[][]string{{"q: " + question, "q: " + rewrite, "q: " + name}}},
		{"no semantic list", Config{BM25Weight: 1, FieldsWeight: 1, RewriteWeight: 0.5, NameWeight: 1}, nil},
		{"rewrites and names weighted 0", Config{BM25Weight: 1, VectorWeight: 1, CardsWeight: 1},
			[][]string{{"q: " + question}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newPlanStore()
			s.texts = []string{"q: " + question, "q: " + rewrite, "q: " + name}
			tc.cfg.QueryPrefix = "q: "
			engine := New(s, idDocs{}, s, embedFunc(s.embed), tc.cfg)
			_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: question,
				Rewrites: []string{rewrite}, Names: []string{name}})
			require.NoError(t, err)
			assert.Equal(t, tc.want, s.calls)
		})
	}
}

// TestEngine_DegradedWithRewrites: when the batch can't be embedded, every
// query's semantic lists are dropped, each BM25 list ranks at its query's
// weight alone, and the other document lists keep theirs.
func TestEngine_DegradedWithRewrites(t *testing.T) {
	s := newPlanStore()
	engine := New(s, idDocs{}, s, failingEmbedder(), Config{BM25Weight: 0.2, VectorWeight: 1, FieldsWeight: 0.25,
		CardsWeight: 0.5, TitleWeight: 2, RewriteWeight: 0.5, NameWeight: 1, Log: slog.New(slog.DiscardHandler)})
	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: question,
		Rewrites: []string{rewrite}, Names: []string{name}})
	require.NoError(t, err)
	assert.True(t, res.Degraded)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "without the document cards")
	// B, which only the vector lists and the rewrite's fields found, keeps
	// its fields; A loses the name's cards:
	//
	//	D = 0.25/61 + 2/61                       (the name's fields and titles)
	//	C = 0.5/61 (c3 at the rewrite's weight) + 1/61 (its titles)
	//	A = 1/61 (c1 at the question's) + 0.25/61 (its fields)
	//	B = 0.125/61 (the rewrite's fields)
	assert.Equal(t, []string{"D", "C", "A", "B"}, hitIDs(res.Items))
	assert.Equal(t, rr(0.5, 1)+rr(1, 1), res.Items[1].Score)
	assert.Equal(t, rr(1, 1)+rr(0.25, 1), res.Items[2].Score)
	assert.Equal(t, rr(0.125, 1), res.Items[3].Score)
	assert.Zero(t, res.VectorHits)
	assert.Empty(t, s.reads["vector"])
	assert.Empty(t, s.reads["cards"])
}

// TestEngine_PlannedPagesTileTheRanking: a planned query's pages, with its
// filters, rewrites and names, tile its k=100 ranking.
func TestEngine_PlannedPagesTileTheRanking(t *testing.T) {
	l := newCardsLibrary(t)
	ctx := context.Background()
	for i := range 120 {
		l.add(t, fmt.Sprintf("https://a.example/%d", i), fmt.Sprintf("Zebra field note %d", i),
			fmt.Sprintf("zebra sighting number %d, stripes and herds", i), store.DocStateFetched,
			0.1+float32(i%50)*0.01, 0.1+float32(i%40)*0.01)
	}
	emb := embedFunc(func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = filledVec(l.dim, 0.2+0.05*float32(i))
		}
		return out, nil
	})
	engine := New(l.chunks, l.docs, l.index, emb, defaultWeights)
	req := Request{TenantID: "local", Query: "zebra field", Rewrites: []string{"striped herds", "sighting notes"},
		Names: []string{"Zebra Field Note 7"}, Filters: store.SearchFilters{
			ContentType: []string{string(store.ContentTypeArticle)}, Host: []string{"a.example"}}}

	req.K = 100
	whole, err := engine.Search(ctx, req)
	require.NoError(t, err)
	require.Len(t, whole.Items, 100)
	var paged []Hit
	for offset := 0; offset <= 90; offset += 10 {
		req.K, req.Offset = 10, offset
		page, err := engine.Search(ctx, req)
		require.NoError(t, err)
		paged = append(paged, page.Items...)
	}
	assert.Equal(t, hitIDs(whole.Items), hitIDs(paged))
}

// TestEngine_SnippetsMarkTheRewrites: a chunk that BM25 found through a
// rewrite alone has a snippet marking the rewrite's words.
func TestEngine_SnippetsMarkTheRewrites(t *testing.T) {
	l := newCardsLibrary(t)
	id := l.add(t, "https://example.com/stripes", "Stripes", "Zebra stripes confuse biting flies.",
		store.DocStateFetched, 0.5, 0.5)
	engine := New(l.chunks, l.docs, l.index, &fakeEmbedder{dim: l.dim}, Config{BM25Weight: 1, RewriteWeight: 0.5})
	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "equine camouflage",
		Rewrites: []string{"zebra stripes"}})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	assert.Equal(t, id, res.Items[0].Document.ID)
	require.NotEmpty(t, res.Items[0].Chunks)
	assert.Contains(t, res.Items[0].Chunks[0].Snippet, "<em>Zebra</em> <em>stripes</em>")
}

// TestEngine_RefusesTooManyQueries: more rewrites or names than a search
// reads, or an empty one, is refused before anything is read.
func TestEngine_RefusesTooManyQueries(t *testing.T) {
	s := newPlanStore()
	engine := New(s, idDocs{}, s, embedFunc(s.embed), defaultWeights)
	for _, tc := range []struct {
		req  Request
		want string
	}{
		{Request{Rewrites: []string{"a b", "c d", "e f", "g h"}}, "search: 4 rewrites, at most 3"},
		{Request{Names: []string{"a", "b", "c", "d", "e"}}, "search: 5 names, at most 4"},
		{Request{Rewrites: []string{"a b", " "}}, "search: an empty string in rewrites"},
		{Request{Names: []string{""}}, "search: an empty string in names"},
	} {
		tc.req.TenantID, tc.req.Query = "local", question
		_, err := engine.Search(context.Background(), tc.req)
		require.EqualError(t, err, tc.want)
	}
	assert.Empty(t, s.reads)
}

// boundedProbe is a chunk store and document index that counts the reads
// in flight. Each read waits until the bound is reached, or every read the
// search makes has started, so that the most in flight at once is the
// bound whenever more reads than it are ready.
type boundedProbe struct {
	planStore
	t     *testing.T
	total int // the reads the search makes

	mu                      sync.Mutex
	inFlight, peak, started int
	wake                    chan struct{} // closed when the reads in flight may finish
}

func (p *boundedProbe) enter() {
	p.mu.Lock()
	p.inFlight++
	p.started++
	p.peak = max(p.peak, p.inFlight)
	wake := p.wake
	if p.inFlight >= maxConcurrentReads || p.started == p.total {
		close(p.wake)
		p.wake = make(chan struct{})
	}
	p.mu.Unlock()
	select {
	case <-wake:
	case <-time.After(legStartBound):
		p.t.Error("a read waited for others that never came")
	}
	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
}

func (p *boundedProbe) BM25Search(ctx context.Context, tenantID, query string, limit int,
	f store.SearchFilters) ([]store.ChunkHit, error) {
	p.enter()
	return p.planStore.BM25Search(ctx, tenantID, query, limit, f)
}

func (p *boundedProbe) VectorSearch(ctx context.Context, tenantID string, v []float32, limit int,
	f store.SearchFilters) ([]store.ChunkHit, error) {
	p.enter()
	return p.planStore.VectorSearch(ctx, tenantID, v, limit, f)
}

func (p *boundedProbe) SearchFields(ctx context.Context, tenantID, query string, limit int,
	f store.SearchFilters) ([]store.DocumentHit, error) {
	p.enter()
	return p.planStore.SearchFields(ctx, tenantID, query, limit, f)
}

func (p *boundedProbe) SearchCards(ctx context.Context, tenantID string, v []float32, limit int,
	f store.SearchFilters) ([]store.DocumentHit, error) {
	p.enter()
	return p.planStore.SearchCards(ctx, tenantID, v, limit, f)
}

func (p *boundedProbe) SearchTitles(ctx context.Context, tenantID, query string, limit int,
	f store.SearchFilters) ([]store.DocumentHit, error) {
	p.enter()
	return p.planStore.SearchTitles(ctx, tenantID, query, limit, f)
}

func (p *boundedProbe) CountUndated(context.Context, string, store.SearchFilters) (int, error) {
	p.enter()
	return 0, nil
}

// TestEngine_ReadsBounded: a plan with 3 rewrites and 4 names makes 33
// store reads, at most maxConcurrentReads of them at once, and the search
// succeeds.
func TestEngine_ReadsBounded(t *testing.T) {
	rewrites := []string{"striped horses", "equine camouflage", "savanna grazers"}
	names := []string{"Okapi Field Guide", "Grevy Zebra", "Plains Zebra", "Mountain Zebra"}
	p := &boundedProbe{t: t, wake: make(chan struct{}),
		planStore: planStore{texts: slices.Concat([]string{question}, rewrites, names)}}
	// The question and each rewrite read five lists, each name three, and
	// the date filter its count of the undated.
	p.total = 4*5 + 4*3 + 1
	engine := New(p, idDocs{}, p, embedFunc(p.embed), defaultWeights)
	_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: question, Rewrites: rewrites,
		Names: names, Filters: store.SearchFilters{SavedAfter: time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC)}})
	require.NoError(t, err)
	assert.Equal(t, p.total, p.started)
	assert.Equal(t, maxConcurrentReads, p.peak)
}

// TestEngine_SemanticReadFailureDropsEveryQuery: one query's card search
// failing drops every query's semantic lists.
func TestEngine_SemanticReadFailureDropsEveryQuery(t *testing.T) {
	s := &failingCards{planStore: *newPlanStore(), failFor: name}
	engine := New(s, idDocs{}, s, embedFunc(s.embed), func() Config {
		c := defaultWeights
		c.Log = slog.New(slog.DiscardHandler)
		return c
	}())
	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: question,
		Rewrites: []string{rewrite}, Names: []string{name}})
	require.NoError(t, err)
	assert.True(t, res.Degraded)
	assert.Contains(t, res.Warnings[0], "card search: disk I/O error")
	assert.Zero(t, res.VectorHits, "the question's and the rewrite's vector lists went with it")
}

// failingCards fails the card search for one text.
type failingCards struct {
	planStore
	failFor string
}

func (f *failingCards) SearchCards(ctx context.Context, tenantID string, v []float32, limit int,
	filters store.SearchFilters) ([]store.DocumentHit, error) {
	if f.texts[int(v[0])] == f.failFor {
		return nil, errors.New("disk I/O error")
	}
	return f.planStore.SearchCards(ctx, tenantID, v, limit, filters)
}

// TestListHits_Timings: a stage read for several queries reports its
// longest read; the embed and the undated count are their one each.
func TestListHits_Timings(t *testing.T) {
	ms := time.Millisecond
	h := listHits{embed: 7 * ms, undatedTook: 2 * ms, queries: []queryHits{
		{took: Timings{BM25: 3 * ms, Fields: 1 * ms, Vector: 4 * ms}},
		{took: Timings{BM25: 5 * ms, Titles: 6 * ms, Vector: 2 * ms, Cards: 8 * ms}},
		{took: Timings{Fields: 9 * ms, Cards: 1 * ms}},
	}}
	assert.Equal(t, Timings{Embed: 7 * ms, Undated: 2 * ms, BM25: 5 * ms, Fields: 9 * ms, Titles: 6 * ms,
		Vector: 4 * ms, Cards: 8 * ms}, h.timings())
}

// TestSnippetQuery: the snippets mark every chunk query's terms, and a
// request without rewrites marks the question's alone, as before plans.
func TestSnippetQuery(t *testing.T) {
	assert.Equal(t, sanitizeBM25Query(question), snippetQuery([]query{{text: question, chunks: true}}))
	assert.Equal(t, `"zebra" OR "stripes" OR "equine" OR "camouflage"`, snippetQuery([]query{
		{text: question, chunks: true}, {text: rewrite, chunks: true}, {text: name}}))
	assert.False(t, strings.Contains(snippetQuery([]query{{text: question, chunks: true}, {text: name}}), "Okapi"),
		"a name reads no chunk")
}
