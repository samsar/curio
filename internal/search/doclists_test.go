package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// listChunks is a chunk store whose lists are given: BM25Search and
// VectorSearch return them as they are, and every chunk is its ID's text.
type listChunks struct {
	store.ChunkStore
	bm25, vector []store.ChunkHit
	vectorErr    error
}

func (c listChunks) BM25Search(context.Context, string, string, int, store.SearchFilters) ([]store.ChunkHit, error) {
	return c.bm25, nil
}

func (c listChunks) VectorSearch(context.Context, string, []float32, int, store.SearchFilters) ([]store.ChunkHit, error) {
	return c.vector, c.vectorErr
}

func (listChunks) GetByIDs(_ context.Context, ids []string) ([]*store.Chunk, error) {
	out := make([]*store.Chunk, len(ids))
	for i, id := range ids {
		out[i] = &store.Chunk{ID: id, Text: "text of " + id}
	}
	return out, nil
}

func (listChunks) Snippets(context.Context, string, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

// idDocs is a document store whose every document is its ID.
type idDocs struct{ store.DocumentStore }

func (idDocs) GetByID(_ context.Context, id string) (*store.Document, error) {
	return &store.Document{ID: id, TenantID: "local", URL: "https://example.com/" + id}, nil
}

// listIndex is a document index whose lists are given; it records the
// limit each list was read at and fails as told.
type listIndex struct {
	store.DocumentIndex
	fields, cards, titles          []store.DocumentHit
	fieldsErr, cardsErr, titlesErr error

	mu     sync.Mutex
	limits map[string][]int
}

func (x *listIndex) record(list string, limit int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.limits == nil {
		x.limits = map[string][]int{}
	}
	x.limits[list] = append(x.limits[list], limit)
}

func (x *listIndex) SearchFields(_ context.Context, _, _ string, limit int, _ store.SearchFilters) ([]store.DocumentHit, error) {
	x.record("fields", limit)
	return x.fields, x.fieldsErr
}

func (x *listIndex) SearchCards(_ context.Context, _ string, _ []float32, limit int, _ store.SearchFilters) ([]store.DocumentHit, error) {
	x.record("cards", limit)
	return x.cards, x.cardsErr
}

func (x *listIndex) SearchTitles(_ context.Context, _, _ string, limit int, _ store.SearchFilters) ([]store.DocumentHit, error) {
	x.record("titles", limit)
	return x.titles, x.titlesErr
}

// countingEmbedder embeds every text as a 1-wide vector, counting the
// calls.
type countingEmbedder struct{ calls atomic.Int32 }

func (e *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls.Add(1)
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1}
	}
	return out, nil
}

func chunkHit(chunk, doc string) store.ChunkHit {
	return store.ChunkHit{ChunkID: chunk, DocumentID: doc}
}

func docHits(ids ...string) []store.DocumentHit {
	out := make([]store.DocumentHit, len(ids))
	for i, id := range ids {
		out[i] = store.DocumentHit{DocumentID: id}
	}
	return out
}

// rr is a reciprocal rank as the engine adds it, weight/(rrf_k + rank)
// with rrf_k 60, in float64 at run time, so sums of them taken in the
// engine's order are its scores to the bit.
func rr(weight float64, rank int) float64 { return weight / float64(60+rank) }

// TestEngine_DocumentListsArithmetic: each document list adds
// weight/(rrf_k + rank) to the documents the chunk lists ranked, after
// their chunks collapsed, fields, cards, then titles; a document only a
// list found joins with no chunks.
func TestEngine_DocumentListsArithmetic(t *testing.T) {
	chunks := listChunks{
		bm25:   []store.ChunkHit{chunkHit("c1", "A"), chunkHit("c2", "B")},
		vector: []store.ChunkHit{chunkHit("c3", "B")},
	}
	index := &listIndex{fields: docHits("C", "A"), cards: docHits("B", "D"), titles: docHits("A", "E")}
	engine := New(chunks, idDocs{}, index, &countingEmbedder{}, Config{BM25Weight: 1, VectorWeight: 1,
		FieldsWeight: 0.5, CardsWeight: 2, TitleWeight: 3})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zebra stripes", K: 10})
	require.NoError(t, err)
	// With rrf_k 60, the chunks fuse to c1 1/61 (BM25's first), c2 1/62
	// (its second) and c3 1/61 (the vector list's first), and collapse by
	// max to A 1/61 and B 1/61. The fields list adds 0.5/61 to C, its
	// first, and 0.5/62 to A; the cards list 2/61 to B and 2/62 to D; the
	// title list 3/61 to A and 3/62 to E:
	//
	//	A = 1/61 + 0.5/62 + 3/61 ≈ 0.0737
	//	B = 1/61 + 2/61          ≈ 0.0492
	//	E = 3/62                 ≈ 0.0484
	//	D = 2/62                 ≈ 0.0323
	//	C = 0.5/61               ≈ 0.0082
	want := []struct {
		id    string
		score float64
	}{
		{"A", rr(1, 1) + rr(0.5, 2) + rr(3, 1)}, {"B", rr(1, 1) + rr(2, 1)}, {"E", rr(3, 2)}, {"D", rr(2, 2)},
		{"C", rr(0.5, 1)},
	}
	require.Len(t, res.Items, len(want))
	for i, w := range want {
		assert.Equal(t, w.id, res.Items[i].Document.ID, "rank %d", i+1)
		assert.Equal(t, w.score, res.Items[i].Score, w.id)
	}
	assert.Len(t, res.Items[0].Chunks, 1, "A's chunk")
	assert.Len(t, res.Items[1].Chunks, 2, "B's two chunks")
	assert.Empty(t, res.Items[2].Chunks, "E only the titles found")
	assert.Empty(t, res.Items[3].Chunks, "D only the cards found")
	assert.Empty(t, res.Items[4].Chunks, "C only the fields found")
	assert.Equal(t, 5, res.Total)
	assert.False(t, res.Capped)
}

// TestEngine_DocumentListsReadAtAFixedDepth: every document list is read
// 200 deep whatever the request's window, so a query's pages are cut from
// one ranking.
func TestEngine_DocumentListsReadAtAFixedDepth(t *testing.T) {
	index := &listIndex{}
	engine := New(listChunks{}, idDocs{}, index, &countingEmbedder{}, Config{FieldsWeight: 1, CardsWeight: 1,
		TitleWeight: 1})
	for _, req := range []Request{{K: 1}, {K: 100}, {K: 10, Offset: 90}} {
		req.TenantID, req.Query = "local", "zebra stripes"
		_, err := engine.Search(context.Background(), req)
		require.NoError(t, err)
	}
	assert.Equal(t, map[string][]int{"fields": {200, 200, 200}, "cards": {200, 200, 200}, "titles": {200, 200, 200}},
		index.limits)
}

// TestNew_NoWeightIsUnset: a Config that weighs no list ranks by the chunk
// lists at weight 1; one that weighs the title list alone ranks by it
// alone, and embeds nothing.
func TestNew_NoWeightIsUnset(t *testing.T) {
	chunks := listChunks{bm25: []store.ChunkHit{chunkHit("c1", "A")}, vector: []store.ChunkHit{chunkHit("c2", "B")}}
	for _, tc := range []struct {
		cfg    Config
		want   []string
		embeds int32
	}{
		{Config{}, []string{"A", "B"}, 1},
		{Config{TitleWeight: 1}, []string{"T"}, 0},
	} {
		emb := &countingEmbedder{}
		engine := New(chunks, idDocs{}, &listIndex{titles: docHits("T")}, emb, tc.cfg)
		res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zebra stripes"})
		require.NoError(t, err)
		assert.Equal(t, tc.want, hitIDs(res.Items), "%+v", tc.cfg)
		assert.Equal(t, tc.embeds, emb.calls.Load(), "%+v", tc.cfg)
	}
}

// TestEngine_TitleListReadsTwoTermsOrMore: the title list reads a query
// with two distinct terms or more, and nothing else; a weight of 0 leaves
// it unread.
func TestEngine_TitleListReadsTwoTermsOrMore(t *testing.T) {
	for _, tc := range []struct {
		query  string
		weight float64
		reads  bool
	}{
		{"zebra stripes", 1, true},
		{"Find me the zebra stripes", 1, true},
		{"zebra", 1, false},
		{"zebra ZEBRA Zebra", 1, false},
		{"the and of for", 1, false},
		{",,, --- ???", 1, false},
		{"zebra -- ''", 1, false},
		{"zebra stripes", 0, false},
	} {
		index := &listIndex{}
		engine := New(listChunks{}, idDocs{}, index, &countingEmbedder{}, Config{BM25Weight: 1, TitleWeight: tc.weight})
		_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: tc.query})
		require.NoError(t, err, tc.query)
		_, read := index.limits["titles"]
		assert.Equal(t, tc.reads, read, "%q at weight %g", tc.query, tc.weight)
	}
}

// TestEngine_OneEmbedPerSearch: the query is embedded once for both
// semantic lists, and not at all when neither is weighted.
func TestEngine_OneEmbedPerSearch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    Config
		embeds int32
	}{
		{"every list", Config{BM25Weight: 1, VectorWeight: 1, FieldsWeight: 1, CardsWeight: 1}, 1},
		{"cards alone", Config{CardsWeight: 1}, 1},
		{"keyword lists alone", Config{BM25Weight: 1, FieldsWeight: 1}, 0},
	} {
		emb := &countingEmbedder{}
		engine := New(listChunks{}, idDocs{}, &listIndex{}, emb, tc.cfg)
		_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "query"})
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.embeds, emb.calls.Load(), tc.name)
	}
}

// TestEngine_SemanticFailureDropsBothLists: a failure of either semantic
// list degrades the search: both are dropped, BM25 ranks at weight 1 and
// the fields and title lists at their own, and the warning says the cards
// are out.
func TestEngine_SemanticFailureDropsBothLists(t *testing.T) {
	errDown := errors.New("connection refused")
	for _, tc := range []struct {
		name   string
		chunks listChunks
		index  *listIndex
	}{
		{"chunk vectors", listChunks{bm25: []store.ChunkHit{chunkHit("c1", "A")}, vectorErr: errDown},
			&listIndex{fields: docHits("C"), cards: docHits("B"), titles: docHits("D")}},
		{"card vectors", listChunks{bm25: []store.ChunkHit{chunkHit("c1", "A")}, vector: []store.ChunkHit{chunkHit("c2", "B")}},
			&listIndex{fields: docHits("C"), cards: docHits("B"), titles: docHits("D"), cardsErr: errDown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := New(tc.chunks, idDocs{}, tc.index, &countingEmbedder{}, Config{BM25Weight: 0.2, VectorWeight: 1,
				FieldsWeight: 0.5, CardsWeight: 1, TitleWeight: 2, Log: slog.New(slog.DiscardHandler)})
			res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zebra stripes"})
			require.NoError(t, err)
			assert.True(t, res.Degraded)
			require.Len(t, res.Warnings, 1)
			assert.Regexp(t, `^semantic search unavailable \(.*connection refused\); keyword-only results, `+
				`without the document cards$`, res.Warnings[0])
			require.Len(t, res.Items, 3, "B, which the semantic lists alone found, is gone")
			assert.Equal(t, []string{"D", "A", "C"}, hitIDs(res.Items))
			assert.Equal(t, rr(2, 1), res.Items[0].Score, "the titles at their own")
			assert.Equal(t, rr(1, 1), res.Items[1].Score, "BM25 at weight 1")
			assert.Equal(t, rr(0.5, 1), res.Items[2].Score, "the fields at their own")
			assert.Zero(t, res.VectorHits)
		})
	}

	engine := New(listChunks{vectorErr: errDown}, idDocs{}, &listIndex{}, &countingEmbedder{},
		Config{BM25Weight: 1, VectorWeight: 1, Log: slog.New(slog.DiscardHandler)})
	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "query"})
	require.NoError(t, err)
	assert.Equal(t, []string{"semantic search unavailable (vector search: connection refused); keyword-only results"},
		res.Warnings, "no cards to be without")
}

// TestEngine_KeywordDocumentListFailureIsFatal: the fields and title lists
// are local SQLite, like BM25, so a failure of either fails the search.
func TestEngine_KeywordDocumentListFailureIsFatal(t *testing.T) {
	errDisk := errors.New("disk I/O error")
	for list, index := range map[string]*listIndex{
		"fields": {fieldsErr: errDisk},
		"titles": {titlesErr: errDisk},
	} {
		engine := New(listChunks{}, idDocs{}, index, &countingEmbedder{},
			Config{BM25Weight: 1, VectorWeight: 1, FieldsWeight: 1, CardsWeight: 1, TitleWeight: 1})
		_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zebra stripes"})
		require.ErrorIs(t, err, errDisk, list)
		assert.ErrorContains(t, err, list+": ")
	}
}

// TestEngine_DocumentOnlyHitsCount: documents only the document lists
// found count in the ranking's total, and in whether it was capped.
func TestEngine_DocumentOnlyHitsCount(t *testing.T) {
	many := make([]string, 0, 150)
	for i := range 150 {
		many = append(many, fmt.Sprintf("d%03d", i))
	}
	engine := New(listChunks{}, idDocs{}, &listIndex{fields: docHits(many...)}, &countingEmbedder{},
		Config{BM25Weight: 1, FieldsWeight: 1})
	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "query", K: 5})
	require.NoError(t, err)
	assert.Equal(t, store.MaxSearchK, res.Total)
	assert.True(t, res.Capped)
	assert.Equal(t, many[:5], hitIDs(res.Items))
}

// slowIndex takes at least fields for the fields list, cards for the
// cards list and titles for the title list.
type slowIndex struct {
	listIndex
	fields, cards, titles time.Duration
}

func (x *slowIndex) SearchFields(ctx context.Context, tenantID, query string, limit int,
	f store.SearchFilters) ([]store.DocumentHit, error) {
	time.Sleep(x.fields)
	return x.listIndex.SearchFields(ctx, tenantID, query, limit, f)
}

func (x *slowIndex) SearchCards(ctx context.Context, tenantID string, v []float32, limit int,
	f store.SearchFilters) ([]store.DocumentHit, error) {
	time.Sleep(x.cards)
	return x.listIndex.SearchCards(ctx, tenantID, v, limit, f)
}

func (x *slowIndex) SearchTitles(ctx context.Context, tenantID, query string, limit int,
	f store.SearchFilters) ([]store.DocumentHit, error) {
	time.Sleep(x.titles)
	return x.listIndex.SearchTitles(ctx, tenantID, query, limit, f)
}

// TestEngine_DocumentListTimings: each document list reports at least the
// time its search took, and 0 when it didn't run.
func TestEngine_DocumentListTimings(t *testing.T) {
	const minFields, minCards, minTitles = 3 * time.Millisecond, 4 * time.Millisecond, 5 * time.Millisecond
	index := &slowIndex{fields: minFields, cards: minCards, titles: minTitles}
	on := New(listChunks{}, idDocs{}, index, &countingEmbedder{}, Config{BM25Weight: 1, FieldsWeight: 1, CardsWeight: 1,
		TitleWeight: 1})
	res, err := on.Search(context.Background(), Request{TenantID: "local", Query: "zebra stripes"})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, res.Timings.Fields, minFields)
	assert.GreaterOrEqual(t, res.Timings.Cards, minCards)
	assert.GreaterOrEqual(t, res.Timings.Titles, minTitles)
	assert.GreaterOrEqual(t, res.Timings.Total, max(res.Timings.Fields, res.Timings.Titles,
		res.Timings.Embed+res.Timings.Cards))

	off := New(listChunks{}, idDocs{}, index, &countingEmbedder{}, Config{BM25Weight: 1, VectorWeight: 1})
	res, err = off.Search(context.Background(), Request{TenantID: "local", Query: "zebra stripes"})
	require.NoError(t, err)
	assert.Zero(t, res.Timings.Fields)
	assert.Zero(t, res.Timings.Cards)
	assert.Zero(t, res.Timings.Titles)
}

// cardsLibrary is a library over a real store: documents in each state
// with chunks that match "zebra", their fields and their cards, each card
// vector near the query's in the order the documents were made.
type cardsLibrary struct {
	db     *sqlitestore.DB
	docs   *sqlitestore.Documents
	chunks *sqlitestore.Chunks
	index  *sqlitestore.DocumentIndex
	dim    int
}

func newCardsLibrary(t *testing.T) *cardsLibrary {
	t.Helper()
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	return &cardsLibrary{db: db, docs: sqlitestore.NewDocuments(db), chunks: sqlitestore.NewChunks(db, dim),
		index: sqlitestore.NewDocumentIndex(db, dim), dim: dim}
}

// add makes a document of url titled title, in state, with a chunk of
// text whose vector is chunkVec, and its card's vector cardVec.
func (l *cardsLibrary) add(t *testing.T, url, title, text string, state store.DocState, chunkVec, cardVec float32) string {
	t.Helper()
	ctx := context.Background()
	d := &store.Document{TenantID: "local", URL: url, Title: &title, ContentType: store.ContentTypeArticle}
	require.NoError(t, l.docs.Create(ctx, d))
	e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK}
	require.NoError(t, sqlitestore.NewExtractions(l.db).Create(ctx, e))
	require.NoError(t, l.chunks.ReplaceForDocument(ctx, d.ID, e.ID, title, nil,
		[]store.ChunkInput{{Text: text, Embedding: filledVec(l.dim, chunkVec)}}))
	switch state {
	case store.DocStateFailed:
		require.NoError(t, l.docs.MarkFailed(ctx, d.ID, store.FailureCauseOther))
	case store.DocStateDead:
		require.NoError(t, l.docs.MarkFailed(ctx, d.ID, store.FailureCauseDeadLink))
	case store.DocStateFetched:
		require.NoError(t, l.docs.MarkFetched(ctx, d.ID, 3))
	case store.DocStatePending:
	}
	stale, err := l.index.StaleCards(ctx, "local", "", 100)
	require.NoError(t, err)
	for _, c := range stale {
		if c.DocumentID == d.ID {
			_, err := l.index.PutCardVectors(ctx, []store.CardVector{{DocumentID: d.ID, Text: c.Text,
				Embedding: filledVec(l.dim, cardVec)}})
			require.NoError(t, err)
		}
	}
	return d.ID
}

// TestEngine_DocumentListsReachEveryState: failed and dead documents'
// chunks are never searched, but the document lists find them by their
// titles and cards, with no passage; a pending document's chunks still
// match.
func TestEngine_DocumentListsReachEveryState(t *testing.T) {
	l := newCardsLibrary(t)
	dead := l.add(t, "https://example.com/dead", "Zebra herds", "zebra herds migrate", store.DocStateDead, 0.1, 0.1)
	failed := l.add(t, "https://example.com/failed", "Zebra stripes", "zebra stripes confuse", store.DocStateFailed, 0.1, 0.1)
	pending := l.add(t, "https://example.com/pending", "Savanna", "zebra foals walk", store.DocStatePending, 0.1, 0.9)
	emb := embedFunc(func(context.Context, []string) ([][]float32, error) { return [][]float32{filledVec(l.dim, 0.1)}, nil })
	ctx := context.Background()

	chunksOnly := New(l.chunks, l.docs, l.index, emb, Config{BM25Weight: 1, VectorWeight: 1})
	res, err := chunksOnly.Search(ctx, Request{TenantID: "local", Query: "zebra"})
	require.NoError(t, err)
	assert.Equal(t, []string{pending}, hitIDs(res.Items), "the chunk lists never return failed or dead documents")
	assert.NotEmpty(t, res.Items[0].Chunks)

	all := New(l.chunks, l.docs, l.index, emb, Config{BM25Weight: 1, VectorWeight: 1, FieldsWeight: 1, CardsWeight: 1})
	res, err = all.Search(ctx, Request{TenantID: "local", Query: "zebra"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{dead, failed, pending}, hitIDs(res.Items))
	for _, hit := range res.Items {
		if hit.Document.ID == pending {
			assert.NotEmpty(t, hit.Chunks, "the pending document's chunk matched")
			continue
		}
		assert.Empty(t, hit.Chunks, "%s matched by what it was saved as, with no passage", hit.Document.State)
	}
}

// TestEngine_DocumentListTies: documents whose fields read the same, and
// whose cards are the same vector, rank in one order on every search:
// the fields' by the order they were written, the cards' by document ID.
func TestEngine_DocumentListTies(t *testing.T) {
	l := newCardsLibrary(t)
	ids := make([]string, 0, 4)
	for i := range 4 {
		ids = append(ids, l.add(t, fmt.Sprintf("https://example.com/%d", i), "Identical title", fmt.Sprint("body ", i),
			store.DocStateFetched, 0.5, 0.3))
	}
	emb := embedFunc(func(context.Context, []string) ([][]float32, error) { return [][]float32{filledVec(l.dim, 0.3)}, nil })
	ctx := context.Background()

	fields := New(l.chunks, l.docs, l.index, emb, Config{FieldsWeight: 1})
	cards := New(l.chunks, l.docs, l.index, emb, Config{CardsWeight: 1})
	byID := slices.Clone(ids)
	slices.Sort(byID)
	for range 3 {
		res, err := fields.Search(ctx, Request{TenantID: "local", Query: "identical"})
		require.NoError(t, err)
		assert.Equal(t, ids, hitIDs(res.Items), "in the order their fields were written")
		res, err = cards.Search(ctx, Request{TenantID: "local", Query: "identical"})
		require.NoError(t, err)
		assert.Equal(t, byID, hitIDs(res.Items), "by document ID")
	}
}

// TestEngine_DocumentListsPageAndFilter: with all five lists on, the
// pages of a query tile its k=100 ranking, and a filter scopes the
// document lists as it does the chunk lists.
func TestEngine_DocumentListsPageAndFilter(t *testing.T) {
	l := newCardsLibrary(t)
	ctx := context.Background()
	for i := range 120 {
		host := "a.example"
		state := store.DocStateFetched
		if i%3 == 0 {
			host, state = "b.example", store.DocStateDead
		}
		l.add(t, fmt.Sprintf("https://%s/%d", host, i), fmt.Sprintf("Zebra field note %d", i),
			fmt.Sprintf("zebra sighting number %d", i), state, 0.1+float32(i%50)*0.01, 0.1+float32(i%40)*0.01)
	}
	emb := embedFunc(func(context.Context, []string) ([][]float32, error) { return [][]float32{filledVec(l.dim, 0.3)}, nil })
	engine := New(l.chunks, l.docs, l.index, emb, Config{BM25Weight: 1, VectorWeight: 1, FieldsWeight: 0.5, CardsWeight: 1,
		TitleWeight: 2})
	const query = "zebra field"

	whole, err := engine.Search(ctx, Request{TenantID: "local", Query: query, K: 100})
	require.NoError(t, err)
	require.Len(t, whole.Items, 100)
	assert.True(t, whole.Capped)
	var paged []Hit
	for offset := 0; offset <= 90; offset += 10 {
		page, err := engine.Search(ctx, Request{TenantID: "local", Query: query, K: 10, Offset: offset})
		require.NoError(t, err)
		paged = append(paged, page.Items...)
	}
	assert.Equal(t, hitIDs(whole.Items), hitIDs(paged))

	for name, f := range map[string]store.SearchFilters{
		"host":         {Host: []string{"b.example"}},
		"content type": {ContentType: []string{string(store.ContentTypeArticle)}, Host: []string{"b.example"}},
	} {
		res, err := engine.Search(ctx, Request{TenantID: "local", Query: query, K: 100, Filters: f})
		require.NoError(t, err, name)
		assert.Len(t, res.Items, 40, "%s: the dead documents of b.example, through the document lists", name)
		for _, hit := range res.Items {
			assert.Contains(t, hit.Document.URL, "https://b.example/", name)
		}
	}
	res, err := engine.Search(ctx, Request{TenantID: "local", Query: query, Filters: store.SearchFilters{
		ContentType: []string{string(store.ContentTypeVideo)}}})
	require.NoError(t, err)
	assert.Empty(t, res.Items, "no list returns a document the type filter leaves out")
	res, err = engine.Search(ctx, Request{TenantID: "local", Query: query, Filters: store.SearchFilters{
		Source: []string{store.SourceChrome}}})
	require.NoError(t, err)
	assert.Empty(t, res.Items, "nor one saved from no such source")
}

// TestEngine_KnownItemsReachTheFirstPage: a bookmark whose page failed,
// went dead or was never fetched, searched by the title it was saved under
// or by three words of it, is the first hit with the default weights, above
// 30 fetched pages whose text holds every word of the query and which both
// chunk lists rank. The title list is what puts it there: the fields list
// alone leaves it off the first page.
func TestEngine_KnownItemsReachTheFirstPage(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	ctx := context.Background()
	docs, exts, chunks := sqlitestore.NewDocuments(db), sqlitestore.NewExtractions(db), sqlitestore.NewChunks(db, dim)
	bookmarks, index := sqlitestore.NewBookmarks(db), sqlitestore.NewDocumentIndex(db, dim)
	indexChunk := func(docID, text string, vec float32) {
		t.Helper()
		e := &store.DocumentExtraction{DocumentID: docID, Fetcher: "test", Status: store.ExtractionStatusOK}
		require.NoError(t, exts.Create(ctx, e))
		require.NoError(t, chunks.ReplaceForDocument(ctx, docID, e.ID, "", nil,
			[]store.ChunkInput{{Text: text, Embedding: filledVec(dim, vec)}}))
	}

	items := []struct {
		state           store.DocState
		title, fragment string
	}{
		{store.DocStateFailed, "Glacier lake sediment cores explained", "lake sediment cores"},
		{store.DocStateDead, "Byzantine mosaic restoration techniques", "mosaic restoration techniques"},
		{store.DocStatePending, "Tidal turbine blade fatigue testing", "turbine blade fatigue"},
	}
	every := make([]string, 0, len(items))
	for _, it := range items {
		every = append(every, strings.ToLower(it.title))
	}
	for i := range 30 {
		d := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/notes/%02d", i),
			Title: new(fmt.Sprint("Field note ", i))}
		require.NoError(t, docs.Create(ctx, d))
		indexChunk(d.ID, strings.Join(every, ", ")+fmt.Sprint(", note ", i), 0.1+0.01*float32(i))
		require.NoError(t, docs.MarkFetched(ctx, d.ID, 0))
	}
	saved := map[string]string{} // the known items' documents, by title
	for _, it := range items {
		b := &store.Bookmark{TenantID: "local", URL: "https://example.org/" + string(it.state), Title: new(it.title),
			Source: store.SourceChrome, SavedAt: time.Now().UTC()}
		_, err := bookmarks.Ingest(ctx, b)
		require.NoError(t, err)
		if cause := map[store.DocState]store.FailureCause{store.DocStateFailed: store.FailureCauseOther,
			store.DocStateDead: store.FailureCauseDeadLink}[it.state]; cause != "" {
			indexChunk(*b.DocumentID, strings.ToLower(it.title), 0.1)
			require.NoError(t, docs.MarkFailed(ctx, *b.DocumentID, cause))
		}
		saved[it.title] = *b.DocumentID
	}

	emb := embedFunc(func(context.Context, []string) ([][]float32, error) { return [][]float32{filledVec(dim, 0.1)}, nil })
	defaults := ConfigFrom(config.Default())
	defaults.Log = slog.New(slog.DiscardHandler)
	withoutTitles := defaults
	withoutTitles.TitleWeight = 0
	engine, withoutTitleList := New(chunks, docs, index, emb, defaults), New(chunks, docs, index, emb, withoutTitles)
	for _, it := range items {
		want := saved[it.title]
		for _, q := range []string{it.title, it.fragment} {
			fields, err := index.SearchFields(ctx, "local", sanitizeBM25Query(q), documentListDepth, store.SearchFilters{})
			require.NoError(t, err)
			titles, err := index.SearchTitles(ctx, "local", titleQuery(q), documentListDepth, store.SearchFilters{})
			require.NoError(t, err)
			require.Len(t, titles, 1, "%s: the title list finds it alone by %q", it.state, q)
			require.Equal(t, want, titles[0].DocumentID, "%s: the title list finds it alone by %q", it.state, q)
			require.NotEmpty(t, fields)
			require.Equal(t, want, fields[0].DocumentID, "%s: the fields list finds it first by %q", it.state, q)

			res, err := engine.Search(ctx, Request{TenantID: "local", Query: q, K: 10})
			require.NoError(t, err)
			require.NotEmpty(t, res.Items)
			assert.Equal(t, want, res.Items[0].Document.ID, "%s: first by %q", it.state, q)
			assert.Empty(t, res.Items[0].Chunks, "%s: by what it was saved as, with no passage", it.state)

			res, err = withoutTitleList.Search(ctx, Request{TenantID: "local", Query: q, K: 10})
			require.NoError(t, err)
			assert.Len(t, res.Items, 10)
			assert.NotContains(t, hitIDs(res.Items), want, "%s: off the first page by %q without the title list",
				it.state, q)
		}
	}
}
