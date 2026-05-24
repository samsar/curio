package search

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/samsar/curio/internal/store"
)

// Engine runs hybrid search.
//
//  1. BM25 over chunks_fts
//  2. Vector ANN over chunks_vec
//  3. RRF fuse the two ranked chunk lists
//  4. Collapse chunks → documents (best chunk per doc, configurable)
//  5. Return top-K documents with their best chunk snippets
//
// The Embedder dependency is used only on the query side — to vectorize
// the user's query before VectorSearch. Documents are embedded by the
// indexer at write time.
type Engine struct {
	chunks    store.ChunkStore
	docs      store.DocumentStore
	embedder  Embedder
	bm25W     float64
	vectorW   float64
	rrfK      int
	collapse  CollapseStrategy
	preFanout int // how many chunks to pull from each retriever before fusion
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
	BM25Weight   float64
	VectorWeight float64
	RRFK         int
	Collapse     CollapseStrategy
	PreFanout    int
}

func New(chunks store.ChunkStore, docs store.DocumentStore, embedder Embedder, cfg Config) *Engine {
	if cfg.BM25Weight == 0 {
		cfg.BM25Weight = 1.0
	}
	if cfg.VectorWeight == 0 {
		cfg.VectorWeight = 1.0
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
	return &Engine{
		chunks:    chunks,
		docs:      docs,
		embedder:  embedder,
		bm25W:     cfg.BM25Weight,
		vectorW:   cfg.VectorWeight,
		rrfK:      cfg.RRFK,
		collapse:  cfg.Collapse,
		preFanout: cfg.PreFanout,
	}
}

// Request is the inbound search request shape.
type Request struct {
	TenantID string
	Query    string
	K        int // results to return after fusion + collapse
}

// Hit is a single document-level result with its best chunk snippets attached.
type Hit struct {
	Document *store.Document
	Score    float64
	Chunks   []ChunkMatch // up to top 3 per doc, in score order
}

// ChunkMatch carries one matching chunk with its retriever-specific scores.
// Exposing both BM25 and vector scores lets API consumers tune the search
// without server-side guessing.
type ChunkMatch struct {
	ChunkID     string
	Text        string
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
}

// Search runs the hybrid pipeline end-to-end.
func (e *Engine) Search(ctx context.Context, req Request) (*Result, error) {
	if req.Query == "" {
		return nil, errors.New("search: query is required")
	}
	if req.TenantID == "" {
		return nil, errors.New("search: tenant_id is required")
	}
	if req.K <= 0 {
		req.K = 10
	}

	// FTS5 has its own MATCH grammar — bare punctuation (commas, slashes,
	// etc.) is a syntax error, and tokens like AND/OR/NOT/NEAR are reserved.
	// Real user queries are natural language ("articles about X, Y, and Z"),
	// so we tokenize down to safe terms before handing to FTS5. The vector
	// path keeps the original text since the embedder handles language fine.
	// Empty after sanitization (e.g. query was all punctuation) means BM25
	// contributes nothing; vector search still runs.
	var bm25Hits []store.ChunkHit
	if ftsQuery := sanitizeBM25Query(req.Query); ftsQuery != "" {
		hits, err := e.chunks.BM25Search(ctx, req.TenantID, ftsQuery, e.preFanout)
		if err != nil {
			return nil, fmt.Errorf("bm25: %w", err)
		}
		bm25Hits = hits
	}

	queryVec, err := e.embedder.Embed(ctx, []string{req.Query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(queryVec) == 0 {
		return nil, errors.New("search: embedder returned no vectors")
	}
	vecHits, err := e.chunks.VectorSearch(ctx, req.TenantID, queryVec[0], e.preFanout)
	if err != nil {
		return nil, fmt.Errorf("vector: %w", err)
	}

	bm25Ranked := toRanked(bm25Hits)
	vecRanked := toRanked(vecHits)

	fused := Fuse(
		[][]RankedItem{bm25Ranked, vecRanked},
		[]float64{e.bm25W, e.vectorW},
		e.rrfK,
	)

	// Map chunk_id -> (bm25 score, vector score, snippet, document_id)
	bm25ByID := make(map[string]store.ChunkHit, len(bm25Hits))
	for _, h := range bm25Hits {
		bm25ByID[h.ChunkID] = h
	}
	vecByID := make(map[string]store.ChunkHit, len(vecHits))
	for _, h := range vecHits {
		vecByID[h.ChunkID] = h
	}

	// Collapse fused chunks → documents.
	type docAgg struct {
		chunkIDs   []string
		chunkScore map[string]float64 // chunk_id -> fused score (post-RRF)
	}
	byDoc := make(map[string]*docAgg)

	for _, fc := range fused {
		var docID string
		if h, ok := bm25ByID[fc.ID]; ok {
			docID = h.DocumentID
		} else if h, ok := vecByID[fc.ID]; ok {
			docID = h.DocumentID
		} else {
			continue // shouldn't happen
		}
		agg, ok := byDoc[docID]
		if !ok {
			agg = &docAgg{chunkScore: map[string]float64{}}
			byDoc[docID] = agg
		}
		agg.chunkIDs = append(agg.chunkIDs, fc.ID)
		agg.chunkScore[fc.ID] = fc.Score
	}

	// Apply collapse strategy.
	type docScore struct {
		documentID string
		score      float64
		chunkIDs   []string
		chunkScore map[string]float64
	}
	docList := make([]docScore, 0, len(byDoc))
	for docID, agg := range byDoc {
		s := collapseScore(agg.chunkScore, agg.chunkIDs, e.collapse)
		docList = append(docList, docScore{
			documentID: docID,
			score:      s,
			chunkIDs:   agg.chunkIDs,
			chunkScore: agg.chunkScore,
		})
	}
	sort.Slice(docList, func(i, j int) bool {
		if docList[i].score != docList[j].score {
			return docList[i].score > docList[j].score
		}
		return docList[i].documentID < docList[j].documentID
	})

	if len(docList) > req.K {
		docList = docList[:req.K]
	}

	// Hydrate documents + top chunks per doc.
	out := &Result{
		Query:      req.Query,
		BM25Hits:   len(bm25Hits),
		VectorHits: len(vecHits),
	}
	for _, d := range docList {
		doc, err := e.docs.GetByID(ctx, d.documentID)
		if err != nil {
			return nil, fmt.Errorf("hydrate document %s: %w", d.documentID, err)
		}
		top := topChunkIDs(d.chunkScore, 3)
		hit := Hit{Document: doc, Score: d.score}
		if len(top) > 0 {
			chunks, err := e.chunks.GetByIDs(ctx, top)
			if err == nil { // missing chunks shouldn't fail the whole result
				byID := map[string]*store.Chunk{}
				for _, c := range chunks {
					byID[c.ID] = c
				}
				for _, cid := range top {
					c, ok := byID[cid]
					if !ok {
						continue
					}
					m := ChunkMatch{ChunkID: c.ID, Text: c.Text}
					if h, ok := bm25ByID[cid]; ok {
						s := h.Score
						m.BM25Score = &s
						m.Snippet = h.Snippet
					}
					if h, ok := vecByID[cid]; ok {
						s := h.Score
						m.VectorScore = &s
					}
					hit.Chunks = append(hit.Chunks, m)
				}
			}
		}
		out.Items = append(out.Items, hit)
	}
	return out, nil
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
		sortFloatsDesc(scores)
		n := 3
		if len(scores) < n {
			n = len(scores)
		}
		if n == 0 {
			return 0
		}
		var s float64
		for i := 0; i < n; i++ {
			s += scores[i]
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
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].id < list[j].id
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

func sortFloatsDesc(s []float64) {
	sort.Sort(sort.Reverse(sort.Float64Slice(s)))
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
	tokens := wordTokenRE.FindAllString(q, -1)
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tokens))
	for _, t := range tokens {
		// Strip any embedded double quotes — FTS5 has no escape for `"`
		// inside a quoted phrase, the only safe move is to drop them.
		t = strings.ReplaceAll(t, `"`, "")
		if t == "" {
			continue
		}
		if _, stop := bm25Stopwords[strings.ToLower(t)]; stop {
			continue
		}
		parts = append(parts, `"`+t+`"`)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " OR ")
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
