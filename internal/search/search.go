package search

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/samsar/curio/internal/store"
)

// Engine runs hybrid search.
//
//  1. Five lists, concurrently, each reading the same pool whatever the
//     request asks for: BM25 over chunks_fts and the fields list over the
//     documents' fields, from the same sanitized query; the title list
//     over the documents' titles and bookmark titles alone, from the AND
//     of the query's terms (titleQuery); and, from one embedding of the
//     query, vector ANN over chunks_vec and over the documents' card
//     vectors. A list weighted 0 is not read, and the query is embedded
//     only when a semantic list is weighted. A request's rewrites read the
//     five lists too, at RewriteWeight times each list's weight, and its
//     names the document lists alone, at NameWeight times theirs; every
//     text that needs a vector is embedded in the one call.
//  2. RRF fuse the ranked chunk lists
//  3. Collapse chunks → documents (best chunk per doc, configurable), and
//     rank them
//  4. Add each document list's reciprocal ranks to its documents, fields,
//     cards, then titles, query by query, a document only they found
//     joining without chunks, and rank them again
//  5. Return the requested window of that ranking, [Offset, Offset+K), with
//     each document's best chunks and the BM25 snippets of those alone
//
// The chunk lists never return failed or dead documents (their chunks are
// of a page that is gone); the document lists return documents of every
// state, by what they were saved as.
//
// The Embedder dependency is used only on the query side — to vectorize
// the user's query before the semantic lists, never with both weighted 0.
// Documents and their cards are embedded by indexing jobs.
type Engine struct {
	chunks       store.ChunkStore
	docs         store.DocumentStore
	index        store.DocumentIndex
	embedder     Embedder
	bm25W        float64
	vectorW      float64
	fieldsW      float64
	cardsW       float64
	titleW       float64
	rewriteW     float64
	nameW        float64
	rrfK         int
	collapse     CollapseStrategy
	preFanout    int    // minimum chunks to pull from each retriever before fusion
	queryPrefix  string // instruction prepended to the query before embedding
	defaultK     int
	embedTimeout time.Duration
	log          *slog.Logger
	// degraded is set while searches are degraded: the first of an outage
	// warns, the others are at debug, and a search that isn't ends it.
	degraded atomic.Bool
}

// Embedder is the slice of the embedder package this engine needs.
// Duplicated here as a tiny interface so search has no dep on the embedder
// package — easier to test with a fake.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// CollapseStrategy decides how multiple chunk hits per document combine
// into a single document score.
type CollapseStrategy string

const (
	CollapseMax     CollapseStrategy = "max"
	CollapseSum     CollapseStrategy = "sum"
	CollapseTop3Avg CollapseStrategy = "top3_avg"
)

// Config wires an Engine. Zero-value fields use sensible defaults.
type Config struct {
	// BM25Weight, VectorWeight, FieldsWeight, CardsWeight and TitleWeight
	// weigh each list in RRF. All five zero means unset: 1.0 for each
	// chunk list, and the document lists off. A zero switches that list's
	// contribution off, and a zero document weight leaves its list unread.
	// With VectorWeight and CardsWeight both zero the query is never
	// embedded, so a down or hung embedding model costs the search nothing
	// and never degrades it. A zero BM25Weight still runs BM25, whose
	// results are the fallback when the semantic lists fail.
	BM25Weight   float64
	VectorWeight float64
	FieldsWeight float64
	CardsWeight  float64
	TitleWeight  float64
	// RewriteWeight weighs a request's rewrites in every list, and
	// NameWeight its names in the document lists, as a multiple of each
	// list's weight; the query itself is at 1. A zero leaves them unread.
	RewriteWeight float64
	NameWeight    float64
	RRFK          int
	Collapse      CollapseStrategy
	// PreFanout is the fewest chunks a retriever returns before fusion:
	// Search reads max(PreFanout, 8·store.MaxSearchK) for every request,
	// Related max(PreFanout, 8·K). Default 50.
	PreFanout int
	// DefaultK is the number of results when a request leaves K at 0.
	// Default 10.
	DefaultK int
	// EmbedTimeout bounds embedding the query. When it runs out (or the
	// embedder fails) Search returns keyword-only results rather than an
	// error. Default 10s.
	EmbedTimeout time.Duration
	// Log receives the warning for a degraded search. Default slog.Default().
	Log *slog.Logger
	// QueryPrefix is prepended to the query text before embedding: the
	// query half of the embedding model's prompt scheme, whose document half
	// the indexer applies (by default Qwen3-Embedding's instruction here and
	// nothing on documents). Empty = no prefix. Must correspond to the
	// indexer's document prefix or vector search quality degrades.
	QueryPrefix string
}

// New builds an engine over the stores. index must not be nil; with every
// document weight zero it is never read.
func New(chunks store.ChunkStore, docs store.DocumentStore, index store.DocumentIndex, embedder Embedder,
	cfg Config) *Engine {
	if cfg.BM25Weight == 0 && cfg.VectorWeight == 0 && cfg.FieldsWeight == 0 && cfg.CardsWeight == 0 &&
		cfg.TitleWeight == 0 {
		cfg.BM25Weight, cfg.VectorWeight = 1.0, 1.0
	}
	if cfg.RRFK == 0 {
		cfg.RRFK = 60
	}
	if cfg.Collapse == "" {
		cfg.Collapse = CollapseMax
	}
	if cfg.PreFanout == 0 {
		cfg.PreFanout = 50
	}
	if cfg.DefaultK <= 0 {
		cfg.DefaultK = 10
	}
	if cfg.EmbedTimeout <= 0 {
		cfg.EmbedTimeout = 10 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Engine{
		chunks:       chunks,
		docs:         docs,
		index:        index,
		embedder:     embedder,
		bm25W:        cfg.BM25Weight,
		vectorW:      cfg.VectorWeight,
		fieldsW:      cfg.FieldsWeight,
		cardsW:       cfg.CardsWeight,
		titleW:       cfg.TitleWeight,
		rewriteW:     cfg.RewriteWeight,
		nameW:        cfg.NameWeight,
		rrfK:         cfg.RRFK,
		collapse:     cfg.Collapse,
		preFanout:    cfg.PreFanout,
		queryPrefix:  cfg.QueryPrefix,
		defaultK:     cfg.DefaultK,
		embedTimeout: cfg.EmbedTimeout,
		log:          cfg.Log,
	}
}

// Request is the inbound search request shape.
type Request struct {
	TenantID string
	Query    string
	// Rewrites are other wordings of Query, searched with it in one
	// ranking: at most MaxRewrites, none empty.
	Rewrites []string
	// Names are what Query points at by description, searched in what
	// documents were saved as: at most MaxNames, none empty.
	Names []string
	K     int // results to return after fusion + collapse; 0 = Config.DefaultK, at most store.MaxSearchK
	// Offset is where in the ranking the results start: Search returns the
	// documents ranked [Offset, Offset+K). Every search ranks the same pool
	// whatever K and Offset, so the windows of one query tile one ranking.
	// 0 <= Offset <= store.MaxSearchK-K.
	Offset int
	// Filters scope the search, as FilterRequest.Resolve gives them; empty
	// is no filter.
	Filters store.SearchFilters
}

// Hit is a single document-level result with its best chunk snippets attached.
type Hit struct {
	Document *store.Document
	Score    float64
	// Chunks are up to 3 of the document's matching chunks, in score
	// order; none for a document only the document lists found, by what
	// it was saved as.
	Chunks []ChunkMatch
}

// ChunkMatch carries one matching chunk with its retriever-specific scores.
// Exposing both BM25 and vector scores lets API consumers tune the search
// without server-side guessing.
type ChunkMatch struct {
	ChunkID string
	Text    string
	// Snippet is the passage of Text around the query's terms, each between
	// <em> and </em>, for a chunk BM25 returned; empty otherwise.
	Snippet     string
	BM25Score   *float64 // nil if BM25 didn't surface this chunk
	VectorScore *float64
}

// Result is the search response payload.
type Result struct {
	Query      string
	BM25Hits   int
	VectorHits int
	Items      []Hit
	// Total is how many documents Search ranked, at most store.MaxSearchK,
	// and Capped is set when more matched than it ranks. Related sets
	// neither.
	Total  int
	Capped bool
	// Degraded is set when the semantic lists failed and Items are
	// keyword-only (BM25, and the fields and title lists when they are
	// weighted); Warnings then says why.
	Degraded bool
	Warnings []string
	// Undated is, when the filters set a date, how many of the library's
	// documents their dates couldn't judge (DocumentIndex.CountUndated):
	// left out, or with IncludeUndated kept. Nil without a date filter.
	Undated *int
	// Timings are how long Search's stages took; Related sets none.
	Timings Timings
}

// Timings are how long each stage of a search took, for measuring a
// ranking change against its latency; the API never reports them. The
// keyword lists run alongside the embed and the semantic lists, and those
// alongside each other, so the stages add up to more than Total. A stage
// read for several queries (a request's rewrites and names) is its longest
// read. A stage that didn't run is 0, and one that failed counts up to its
// failure.
type Timings struct {
	Embed   time.Duration // embedding the query, its rewrites and names in one batch
	BM25    time.Duration // the keyword search
	Fields  time.Duration // the fields list's search
	Titles  time.Duration // the title list's search
	Vector  time.Duration // the vector search
	Cards   time.Duration // the card vectors' search
	Undated time.Duration // counting the documents a date filter couldn't judge
	Fuse    time.Duration // RRF, resolving the chunks to their documents, and ranking those with the document lists
	Hydrate time.Duration // reading the window's documents, their chunks and the snippets
	Total   time.Duration // the whole search
}

// Search runs the hybrid pipeline end-to-end.
//
// Every search reads the same pool, chunkFanout(store.MaxSearchK) chunks
// from each chunk list and documentListDepth documents from each document
// list, and ranks the documents they come from; the request picks its
// window of that ranking. A pool that grew with K would rank a page asked
// as "k=20, keep 11-20" from more chunks than page 1, and a document could
// then show on both pages or on neither. See docs/decisions.md "Search
// pages by offset within a fixed-depth pool".
//
// The lists fail asymmetrically. BM25, the fields list and the title list
// are local SQLite and always available, so a failure of any is a bug or
// corruption and fails the search. The semantic lists depend on an
// embedding model in another process (Ollama), which may be down, still
// starting, or hung — legitimately optional at query time — so a failure
// of any drops them all, for every query, and returns the keyword results
// marked Degraded instead of discarding them. If the caller's own context
// ends, Search returns that error, never a degraded success.
func (e *Engine) Search(ctx context.Context, req Request) (*Result, error) {
	began := time.Now()
	if req.Query == "" {
		return nil, errors.New("search: query is required")
	}
	if req.TenantID == "" {
		return nil, errors.New("search: tenant_id is required")
	}
	switch {
	case req.K == 0:
		req.K = e.defaultK
	case req.K < 0 || req.K > store.MaxSearchK:
		return nil, fmt.Errorf("search: k must be between 1 and %d, got %d", store.MaxSearchK, req.K)
	}
	// Checked as a difference: Offset+K overflows for an Offset near MaxInt.
	if req.Offset < 0 || req.Offset > store.MaxSearchK-req.K {
		return nil, fmt.Errorf("search: offset must be between 0 and %d (offset + k at most %d), got %d",
			store.MaxSearchK-req.K, store.MaxSearchK, req.Offset)
	}
	if err := checkQueries(req); err != nil {
		return nil, err
	}
	if err := req.Filters.Validate(); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	qs := e.queries(req)
	read, err := e.readLists(ctx, req, qs)
	if cerr := ctx.Err(); cerr != nil {
		return nil, fmt.Errorf("search: %w", cerr)
	}
	if err != nil {
		return nil, err
	}
	timings := read.timings()

	res := &Result{Query: req.Query, Undated: read.undated}
	degraded := read.semanticErr != nil
	if degraded {
		res.Degraded = true
		res.Warnings = []string{e.degradedWarning(read.semanticErr)}
	}
	e.logDegraded(len(req.Query), read.semanticErr)
	fuseStart := time.Now()
	lists, weights := e.chunkLists(qs, read.queries, degraded)
	fused := Fuse(lists, weights, e.rrfK)
	bm25ByID, vecByID := chunkHits(read.queries, degraded)

	// Resolve each fused chunk to its parent document.
	scored := make([]scoredChunk, 0, len(fused))
	for _, fc := range fused {
		var docID string
		if h, ok := bm25ByID[fc.ID]; ok {
			docID = h.DocumentID
		} else if h, ok := vecByID[fc.ID]; ok {
			docID = h.DocumentID
		} else {
			continue // shouldn't happen
		}
		scored = append(scored, scoredChunk{chunkID: fc.ID, documentID: docID, score: fc.Score})
	}

	ranked := addDocumentLists(rankDocuments(scored, e.collapse), e.documentLists(qs, read.queries, degraded), e.rrfK)
	timings.Fuse = time.Since(fuseStart)
	res.Total, res.Capped = min(len(ranked), store.MaxSearchK), len(ranked) > store.MaxSearchK
	hydrateStart := time.Now()
	items, err := e.hydrate(ctx, ranked, req.Offset, req.K, bm25ByID, vecByID)
	if err != nil {
		return nil, err
	}
	if err := e.attachSnippets(ctx, snippetQuery(qs), items); err != nil {
		return nil, err
	}
	timings.Hydrate = time.Since(hydrateStart)
	res.BM25Hits = len(bm25ByID)
	res.VectorHits = len(vecByID)
	res.Items = items
	timings.Total = time.Since(began)
	res.Timings = timings
	return res, nil
}

// logDegraded logs a search whose semantic lists failed with err: at WARN
// the first of an outage, at DEBUG the others, so a down embedder costs
// one line however many searches it degrades. A search that wasn't
// degraded (nil err) ends the outage. The record carries the query's
// length, never the query: it is the user's own words.
func (e *Engine) logDegraded(queryBytes int, err error) {
	if err == nil {
		e.degraded.Store(false)
		return
	}
	level := slog.LevelDebug
	if e.degraded.CompareAndSwap(false, true) {
		level = slog.LevelWarn
	}
	e.log.Log(context.Background(), level, "semantic search unavailable; returning keyword-only results",
		"query_bytes", queryBytes, "err", err)
}

// chunkLists are the chunk lists to fuse and their weights, in a fixed
// order: each chunk query's BM25 list, then its vector list, the question
// first, each at the list's weight times the query's. Degraded, only the
// BM25 lists are left, each at its query's weight alone: BM25's own weight
// only balanced it against the vector list, and a zero would erase the
// ranking.
func (e *Engine) chunkLists(qs []query, hits []queryHits, degraded bool) ([][]RankedItem, []float64) {
	var (
		lists   [][]RankedItem
		weights []float64
	)
	for i, q := range qs {
		if !q.chunks {
			continue
		}
		if degraded {
			lists, weights = append(lists, toRanked(hits[i].bm25)), append(weights, q.weight)
			continue
		}
		lists = append(lists, toRanked(hits[i].bm25), toRanked(hits[i].vector))
		weights = append(weights, e.bm25W*q.weight, e.vectorW*q.weight)
	}
	return lists, weights
}

// documentLists are the document lists to add to the chunks' ranking, in
// a fixed order: each query's fields, cards and titles, the question
// first, then its rewrites, then its names, each at the list's weight
// times the query's. Degraded, the cards are left out with the other
// semantic lists.
func (e *Engine) documentLists(qs []query, hits []queryHits, degraded bool) []documentList {
	out := make([]documentList, 0, 3*len(qs))
	for i, q := range qs {
		out = append(out, documentList{weight: e.fieldsW * q.weight, hits: hits[i].fields})
		if !degraded {
			out = append(out, documentList{weight: e.cardsW * q.weight, hits: hits[i].cards})
		}
		out = append(out, documentList{weight: e.titleW * q.weight, hits: hits[i].titles})
	}
	return out
}

// chunkHits are the chunks the BM25 lists and the vector lists found, by
// ID, each with its score from the first list that found it, the
// question's before its rewrites'. Degraded, the vector lists found none.
func chunkHits(hits []queryHits, degraded bool) (bm25ByID, vecByID map[string]store.ChunkHit) {
	bm25ByID, vecByID = map[string]store.ChunkHit{}, map[string]store.ChunkHit{}
	for _, h := range hits {
		for _, c := range h.bm25 {
			if _, ok := bm25ByID[c.ChunkID]; !ok {
				bm25ByID[c.ChunkID] = c
			}
		}
		if degraded {
			continue
		}
		for _, c := range h.vector {
			if _, ok := vecByID[c.ChunkID]; !ok {
				vecByID[c.ChunkID] = c
			}
		}
	}
	return bm25ByID, vecByID
}

// snippetQuery is what the window's snippets mark: the OR of every chunk
// query's terms, so a chunk BM25 found through a rewrite shows that
// rewrite's words. For a request without rewrites it is the question's
// sanitized query.
func snippetQuery(qs []query) string {
	var texts []string
	for _, q := range qs {
		if q.chunks {
			texts = append(texts, q.text)
		}
	}
	return sanitizeBM25Query(strings.Join(texts, " "))
}

// degradedWarning is what a degraded search tells its caller: the semantic
// lists failed for err, and which lists ranked it.
func (e *Engine) degradedWarning(err error) string {
	w := fmt.Sprintf("semantic search unavailable (%v); keyword-only results", err)
	if e.cardsW > 0 {
		w += ", without the document cards"
	}
	return w
}

// DefaultK is how many results a request that leaves K at 0 gets.
func (e *Engine) DefaultK() int { return e.defaultK }

// chunkFanout is how many chunks each retriever returns before fusion for
// k documents: Search asks it for store.MaxSearchK, the most any window
// reaches, Related for its K. Hits are chunk-level, and one long document
// can fill dozens of the nearest slots, so a pool of about k chunks could
// collapse to fewer than k distinct documents (and never yield more
// documents than its size). ~8 chunk slots per document, floored at
// preFanout.
func (e *Engine) chunkFanout(k int) int {
	return max(e.preFanout, k*8)
}

// RelatedRequest asks for documents similar to an existing document.
type RelatedRequest struct {
	TenantID   string
	DocumentID string
	K          int // results to return; default 10
}

// maxRelatedChunks caps how many of a document's chunk vectors are
// mean-pooled into the similarity query. Chunk count is unbounded (a
// book-length page can have hundreds); the leading chunks carry the
// document's topic well enough and this bounds read cost.
const maxRelatedChunks = 64

// Related finds documents similar to the given one by embedding
// similarity over its stored chunk vectors — no query text, no embedder
// call. The document's chunk vectors are mean-pooled into a single query
// vector, one ANN search runs with the source document excluded, and
// chunk hits collapse to documents exactly like Search.
//
// A document with no indexed chunks (not indexed yet) returns an empty
// result rather than an error — the document exists, it just has no
// vectors to be similar to anything yet. Failed and dead documents are
// never among the results (see ChunkStore.VectorSearch).
func (e *Engine) Related(ctx context.Context, req RelatedRequest) (*Result, error) {
	if req.DocumentID == "" {
		return nil, errors.New("related: document_id is required")
	}
	if req.TenantID == "" {
		return nil, errors.New("related: tenant_id is required")
	}
	if req.K <= 0 {
		req.K = 10
	}

	// Surface store.ErrNotFound (wrapped) for unknown IDs so the API layer
	// can map it to a 404.
	if _, err := e.docs.GetByID(ctx, req.DocumentID); err != nil {
		return nil, fmt.Errorf("related: load document: %w", err)
	}

	embs, err := e.chunks.EmbeddingsForDocument(ctx, req.DocumentID)
	if err != nil {
		return nil, fmt.Errorf("related: read embeddings: %w", err)
	}
	out := &Result{Query: "related:" + req.DocumentID}
	if len(embs) == 0 {
		return out, nil
	}
	if len(embs) > maxRelatedChunks {
		embs = embs[:maxRelatedChunks]
	}

	mean := meanVector(embs)
	fanout := e.chunkFanout(req.K)
	// The exclusion relies on VectorSearch's over-fetch: sqlite-vec applies
	// non-MATCH predicates after the k cutoff.
	vecHits, err := e.chunks.VectorSearch(ctx, req.TenantID, mean, fanout,
		store.SearchFilters{ExcludeDocumentID: req.DocumentID})
	if err != nil {
		return nil, fmt.Errorf("related: vector: %w", err)
	}
	out.VectorHits = len(vecHits)

	vecByID := make(map[string]store.ChunkHit, len(vecHits))
	scored := make([]scoredChunk, 0, len(vecHits))
	for _, h := range vecHits {
		vecByID[h.ChunkID] = h
		scored = append(scored, scoredChunk{chunkID: h.ChunkID, documentID: h.DocumentID, score: h.Score})
	}

	items, err := e.hydrate(ctx, rankDocuments(scored, e.collapse), 0, req.K, nil, vecByID)
	if err != nil {
		return nil, err
	}
	out.Items = items
	return out, nil
}

// meanVector mean-pools chunk embeddings in float64 to limit rounding
// drift, then converts back to float32 for the ANN query.
func meanVector(embs []store.ChunkEmbedding) []float32 {
	dim := len(embs[0].Embedding)
	acc := make([]float64, dim)
	for _, e := range embs {
		for i, v := range e.Embedding {
			acc[i] += float64(v)
		}
	}
	mean := make([]float32, dim)
	for i, v := range acc {
		mean[i] = float32(v / float64(len(embs)))
	}
	return mean
}

// scoredChunk is one chunk with its final score (RRF-fused for Search,
// raw vector similarity for Related), resolved to its parent document.
type scoredChunk struct {
	chunkID    string
	documentID string
	score      float64
}

// docScore is one document of a ranking: its collapsed score, and the
// scores of its chunks the retrievers returned.
type docScore struct {
	documentID string
	score      float64
	chunkScore map[string]float64
}

// rankDocuments collapses scored chunks into documents, by strategy, and
// sorts them best first. Equal scores are ordered by document ID, so a
// ranking recomputed for every page puts its ties in the same place.
func rankDocuments(scored []scoredChunk, strategy CollapseStrategy) []docScore {
	type docAgg struct {
		chunkIDs   []string
		chunkScore map[string]float64
	}
	byDoc := make(map[string]*docAgg)
	for _, sc := range scored {
		agg, ok := byDoc[sc.documentID]
		if !ok {
			agg = &docAgg{chunkScore: map[string]float64{}}
			byDoc[sc.documentID] = agg
		}
		agg.chunkIDs = append(agg.chunkIDs, sc.chunkID)
		agg.chunkScore[sc.chunkID] = sc.score
	}

	ranked := make([]docScore, 0, len(byDoc))
	for docID, agg := range byDoc {
		ranked = append(ranked, docScore{
			documentID: docID,
			score:      collapseScore(agg.chunkScore, agg.chunkIDs, strategy),
			chunkScore: agg.chunkScore,
		})
	}
	slices.SortFunc(ranked, func(a, b docScore) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return strings.Compare(a.documentID, b.documentID)
	})
	return ranked
}

// hydrate turns the ranked documents from offset on into k hits: each
// document's row and up to 3 of its top chunks, reading nothing for the
// documents before offset or after the k-th. A document deleted since the
// retrievers read its chunks is skipped, not an error: a concurrent delete
// shouldn't fail the query (or make Related report its source missing), and
// the next-ranked document takes its place. bm25ByID/vecByID annotate the
// per-chunk retriever scores; either may be nil.
func (e *Engine) hydrate(ctx context.Context, ranked []docScore, offset, k int, bm25ByID, vecByID map[string]store.ChunkHit) ([]Hit, error) {
	window := ranked[min(offset, len(ranked)):]
	items := make([]Hit, 0, min(k, len(window)))
	for _, d := range window {
		if len(items) == k {
			break
		}
		doc, err := e.docs.GetByID(ctx, d.documentID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("hydrate document %s: %w", d.documentID, err)
		}
		items = append(items, Hit{Document: doc, Score: d.score,
			Chunks: e.matches(ctx, d, bm25ByID, vecByID)})
	}
	return items, nil
}

// matches loads document d's top 3 chunks with each retriever's score. The
// matching chunks only decorate the hit, and the document still matched,
// so a failed read leaves the hit without them, logged.
func (e *Engine) matches(ctx context.Context, d docScore, bm25ByID, vecByID map[string]store.ChunkHit) []ChunkMatch {
	top := topChunkIDs(d.chunkScore, 3)
	if len(top) == 0 {
		return nil
	}
	chunks, err := e.chunks.GetByIDs(ctx, top)
	if err != nil {
		e.log.Warn("search: load matching chunks", "document", d.documentID, "err", err)
		return nil
	}
	byID := make(map[string]*store.Chunk, len(chunks))
	for _, c := range chunks {
		byID[c.ID] = c
	}
	var out []ChunkMatch
	for _, cid := range top {
		c, ok := byID[cid]
		if !ok {
			continue
		}
		m := ChunkMatch{ChunkID: c.ID, Text: c.Text}
		if h, ok := bm25ByID[cid]; ok {
			s := h.Score
			m.BM25Score = &s
		}
		if h, ok := vecByID[cid]; ok {
			s := h.Score
			m.VectorScore = &s
		}
		out = append(out, m)
	}
	return out
}

// attachSnippets gives every match in items that BM25 returned its snippet
// for ftsQuery, read in one query for the window's chunks alone: the pool
// holds up to 800 BM25 chunks, and FTS5 making a snippet of each cost more
// than the rest of the keyword search. A failed read leaves the matches
// without snippets, logged, as a failed chunk read leaves a hit without
// its matches; the caller's context ending is an error, never a degraded
// success.
func (e *Engine) attachSnippets(ctx context.Context, ftsQuery string, items []Hit) error {
	var ids []string
	for _, it := range items {
		for _, m := range it.Chunks {
			if m.BM25Score != nil {
				ids = append(ids, m.ChunkID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	snippets, err := e.chunks.Snippets(ctx, ftsQuery, ids)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("search: %w", cerr)
		}
		e.log.Warn("search: load snippets", "err", err)
		return nil
	}
	for i := range items {
		for j := range items[i].Chunks {
			m := &items[i].Chunks[j]
			m.Snippet = snippets[m.ChunkID]
		}
	}
	return nil
}

func toRanked(hits []store.ChunkHit) []RankedItem {
	out := make([]RankedItem, len(hits))
	for i, h := range hits {
		out[i] = RankedItem{ID: h.ChunkID, Rank: i + 1}
	}
	return out
}

func collapseScore(perChunk map[string]float64, ids []string, strat CollapseStrategy) float64 {
	switch strat {
	case CollapseSum:
		var s float64
		for _, id := range ids {
			s += perChunk[id]
		}
		return s
	case CollapseTop3Avg:
		scores := make([]float64, 0, len(ids))
		for _, id := range ids {
			scores = append(scores, perChunk[id])
		}
		// Sort desc and average top 3.
		slices.SortFunc(scores, func(a, b float64) int { return cmp.Compare(b, a) })
		n := min(3, len(scores))
		if n == 0 {
			return 0
		}
		var s float64
		for _, score := range scores[:n] {
			s += score
		}
		return s / float64(n)
	case CollapseMax:
		fallthrough
	default:
		var m float64
		for _, id := range ids {
			if v := perChunk[id]; v > m {
				m = v
			}
		}
		return m
	}
}

func topChunkIDs(perChunk map[string]float64, n int) []string {
	type kv struct {
		id    string
		score float64
	}
	list := make([]kv, 0, len(perChunk))
	for k, v := range perChunk {
		list = append(list, kv{k, v})
	}
	slices.SortFunc(list, func(a, b kv) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	if len(list) > n {
		list = list[:n]
	}
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.id
	}
	return out
}

// sanitizeBM25Query turns an arbitrary user query into something safe AND
// useful to pass to FTS5's MATCH operator:
//
//   - Extracts word-like tokens (letters/digits/apostrophes/hyphens).
//   - Drops common English stopwords ("the", "and", "is", ...). Without this,
//     a natural-language query like "Find me articles about X" would AND
//     every token together — no real chunk has all 13 words, BM25 returns
//     zero, and the lexical leg of hybrid search dies on every long query.
//   - Wraps each remaining token in double quotes so FTS5 treats it as a
//     literal phrase, escaping any reserved punctuation or keywords.
//   - Joins with OR so any content-bearing term can contribute. BM25's role
//     in hybrid retrieval is to catch exact lexical matches that vector
//     misses (names, identifiers, jargon) — OR semantics maximize that.
//
// Returns empty string when no tokens survive (e.g. query was all
// punctuation or all stopwords); callers should skip BM25 in that case,
// vector search still works.
//
// We don't try to support advanced FTS5 syntax (operators, prefix wildcards,
// column filters) here — if the search surface ever exposes that, it
// should be a separate code path that the caller opts into.
//
// See docs/decisions.md "BM25 query sanitization: OR + stopwords" for the
// design rationale and the natural-language-search work we may revisit.
func sanitizeBM25Query(q string) string {
	return phrases(keywordTerms(q), " OR ")
}

// titleListMinTerms is the fewest distinct terms a query needs for the
// title list to read it. With one term the AND is no more precise than the
// fields list's OR held to the titles, and at the default weights eight
// times as heavy: measured on one-word queries, it filled half the first
// page with documents whose titles hold the word. See docs/decisions.md
// "Metadata search: a fields index and a card per document".
const titleListMinTerms = 2

// titleQuery is the title list's MATCH expression for q: the AND of its
// distinct keyword terms, compared case-insensitively, each quoted as
// sanitizeBM25Query quotes it. A term with no letter or digit is dropped:
// FTS5 tokenizes it to an empty phrase, which makes an AND match nothing.
// "" when fewer than titleListMinTerms terms remain.
func titleQuery(q string) string {
	terms := keywordTerms(q)
	seen := make(map[string]bool, len(terms))
	distinct := terms[:0]
	for _, t := range terms {
		key := strings.ToLower(t)
		if seen[key] || !strings.ContainsFunc(t, isLetterOrDigit) {
			continue
		}
		seen[key] = true
		distinct = append(distinct, t)
	}
	if len(distinct) < titleListMinTerms {
		return ""
	}
	return phrases(distinct, " AND ")
}

func isLetterOrDigit(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

// keywordTerms are the terms the keyword lists read a query by: its
// word-like tokens (wordTokenRE), in order, less the stopwords. No term
// can hold `"`, which wordTokenRE doesn't match, so a quote inside a word
// splits it; that is what makes quoting each term as an FTS5 phrase safe,
// FTS5 having no escape for `"` inside a quoted phrase.
func keywordTerms(q string) []string {
	tokens := wordTokenRE.FindAllString(q, -1)
	terms := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, stop := bm25Stopwords[strings.ToLower(t)]; stop {
			continue
		}
		terms = append(terms, t)
	}
	return terms
}

// phrases joins terms, each quoted as an FTS5 phrase, with op: "" for
// none.
func phrases(terms []string, op string) string {
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	return strings.Join(quoted, op)
}

// wordTokenRE matches runs of letters, digits, apostrophes, and hyphens —
// the latter two so words like "don't" and "state-of-the-art" stay whole.
// Everything else (whitespace, punctuation, symbols) acts as a separator.
var wordTokenRE = regexp.MustCompile(`[\p{L}\p{N}'\-]+`)

// bm25Stopwords is a small English stopword list. Intentionally short:
// covers the high-frequency function words that flood natural-language
// queries ("Find me articles about ...") without trying to be exhaustive.
// We err on the side of keeping words — over-aggressive stopwording hurts
// recall on technical terms that happen to overlap with English words
// ("set", "map", "list" in CS contexts).
var bm25Stopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {},
	"and": {}, "or": {}, "but": {}, "nor": {}, "so": {}, "yet": {},
	"is": {}, "are": {}, "was": {}, "were": {}, "be": {}, "been": {}, "being": {},
	"am": {}, "do": {}, "does": {}, "did": {}, "doing": {},
	"have": {}, "has": {}, "had": {}, "having": {},
	"of": {}, "in": {}, "on": {}, "at": {}, "to": {}, "from": {}, "by": {},
	"for": {}, "with": {}, "about": {}, "against": {}, "between": {}, "into": {},
	"through": {}, "during": {}, "before": {}, "after": {}, "above": {},
	"below": {}, "up": {}, "down": {}, "out": {}, "off": {}, "over": {},
	"under": {}, "again": {}, "further": {}, "then": {}, "once": {},
	"i": {}, "me": {}, "my": {}, "we": {}, "us": {}, "our": {},
	"you": {}, "your": {}, "he": {}, "him": {}, "his": {}, "she": {},
	"her": {}, "it": {}, "its": {}, "they": {}, "them": {}, "their": {},
	"this": {}, "that": {}, "these": {}, "those": {},
	"what": {}, "which": {}, "who": {}, "whom": {}, "whose": {},
	"all": {}, "any": {}, "some": {}, "no": {}, "not": {}, "only": {},
	"own": {}, "same": {}, "than": {}, "too": {}, "very": {}, "just": {},
	"can": {}, "will": {}, "would": {}, "should": {}, "could": {},
	"may": {}, "might": {}, "must": {}, "shall": {},
	"find": {}, "show": {}, "give": {}, "tell": {}, "want": {}, "need": {},
	"please": {}, "best": {}, "good": {}, "bad": {},
	"how": {}, "why": {}, "when": {}, "where": {},
}
