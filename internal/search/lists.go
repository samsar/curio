package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/samsar/curio/internal/store"
)

// MaxRewrites and MaxNames bound a request's rewrites and names: each
// rewrite reads five lists, and each name three.
const (
	MaxRewrites = 3
	MaxNames    = 4
)

// maxConcurrentReads bounds the store reads one search runs at once. A
// planned search makes up to 33 reads (32 lists, and the count of what a
// date filter couldn't judge), from a pool of 32 connections the workers
// share; measured on the owner's library, 6 at once ranked a planned
// search no slower than no bound (decisions.md "Understanding the
// question").
const maxConcurrentReads = 6

// query is one text a search ranks documents by: the question, a rewrite
// or a name, at weight, which multiplies each list's own. The question and
// its rewrites read every list; a name only the document lists, which
// match what a document was saved as.
type query struct {
	text   string
	weight float64
	chunks bool   // the chunk lists read it
	fts    string // sanitizeBM25Query(text): the BM25 and fields lists' MATCH
	title  string // titleQuery(text): the title list's MATCH, "" to skip it
	vector int    // its vector's index in the embedded batch; -1 for none
}

// queries are the texts req is ranked by, in the order their lists fuse:
// the question at weight 1, then each rewrite, then each name, the latter
// two left out when weighted 0. A query gets a vector when a semantic list
// it reads is weighted.
func (e *Engine) queries(req Request) []query {
	out := []query{newQuery(req.Query, 1, true)}
	if e.rewriteW > 0 {
		for _, r := range req.Rewrites {
			out = append(out, newQuery(r, e.rewriteW, true))
		}
	}
	if e.nameW > 0 {
		for _, n := range req.Names {
			out = append(out, newQuery(n, e.nameW, false))
		}
	}
	vectors := 0
	for i := range out {
		if q := &out[i]; e.cardsW > 0 || (q.chunks && e.vectorW > 0) {
			q.vector = vectors
			vectors++
		}
	}
	return out
}

func newQuery(text string, weight float64, chunks bool) query {
	return query{text: text, weight: weight, chunks: chunks, fts: sanitizeBM25Query(text), title: titleQuery(text),
		vector: -1}
}

// checkQueries refuses more rewrites or names than a search reads, and an
// empty one, which would read nothing.
func checkQueries(req Request) error {
	for _, l := range []struct {
		field string
		texts []string
		most  int
	}{{"rewrites", req.Rewrites, MaxRewrites}, {"names", req.Names, MaxNames}} {
		if len(l.texts) > l.most {
			return fmt.Errorf("search: %d %s, at most %d", len(l.texts), l.field, l.most)
		}
		for _, t := range l.texts {
			if strings.TrimSpace(t) == "" {
				return fmt.Errorf("search: an empty string in %s", l.field)
			}
		}
	}
	return nil
}

// queryHits are one query's lists, and how long each read took. Each read
// writes only its own fields, read after the reads are done.
type queryHits struct {
	bm25, vector          []store.ChunkHit
	fields, cards, titles []store.DocumentHit
	took                  Timings // BM25, Fields, Titles, Vector and Cards
}

// listHits are every list a search read: each query's, the documents a
// date filter couldn't judge, and why the semantic lists failed, when they
// did, which drops them all.
type listHits struct {
	queries     []queryHits
	undated     *int
	semanticErr error
	embed       time.Duration
	undatedTook time.Duration
}

// readSlots bounds the store reads of one search (maxConcurrentReads). A
// read takes a slot around the call itself, never around a goroutine that
// starts others, which could deadlock.
type readSlots chan struct{}

// read runs fn in a slot, waiting for one until ctx ends, and records how
// long fn itself took in took.
func (s readSlots) read(ctx context.Context, took *time.Duration, fn func() error) error {
	select {
	case s <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s }()
	start := time.Now()
	err := fn()
	*took = time.Since(start)
	return err
}

// readLists reads every list of qs concurrently: the keyword lists of each
// query, the count of what a date filter couldn't judge, and, from one
// embedding of every query that needs a vector, the semantic lists. A
// keyword read that fails fails the search; the semantic lists fail
// together, reported in listHits.semanticErr.
func (e *Engine) readLists(ctx context.Context, req Request, qs []query) (listHits, error) {
	fanout := e.chunkFanout(store.MaxSearchK)
	slots := make(readSlots, maxConcurrentReads)
	out := listHits{queries: make([]queryHits, len(qs))}
	g, gctx := errgroup.WithContext(ctx)
	for i, q := range qs {
		h := &out.queries[i]
		// FTS5 has its own MATCH grammar, so the keyword lists read the
		// sanitized query (sanitizeBM25Query); empty after sanitizing (all
		// punctuation or stopwords), they contribute nothing. BM25 runs
		// whatever its weight: its results are the fallback when the
		// semantic lists fail.
		if q.chunks && q.fts != "" {
			g.Go(func() error {
				return wrap("bm25", slots.read(gctx, &h.took.BM25, func() (err error) {
					h.bm25, err = e.chunks.BM25Search(gctx, req.TenantID, q.fts, fanout, req.Filters)
					return err
				}))
			})
		}
		if e.fieldsW > 0 && q.fts != "" {
			g.Go(func() error {
				return wrap("fields", slots.read(gctx, &h.took.Fields, func() (err error) {
					h.fields, err = e.index.SearchFields(gctx, req.TenantID, q.fts, documentListDepth, req.Filters)
					return err
				}))
			})
		}
		// The title list reads only a query of at least two terms, every one
		// of which a document's titles must hold.
		if e.titleW > 0 && q.title != "" {
			g.Go(func() error {
				return wrap("titles", slots.read(gctx, &h.took.Titles, func() (err error) {
					h.titles, err = e.index.SearchTitles(gctx, req.TenantID, q.title, documentListDepth, req.Filters)
					return err
				}))
			})
		}
	}
	// The count of what a date filter couldn't judge is local SQLite, as
	// the keyword lists are, and fails the search like them.
	if req.Filters.HasDates() {
		g.Go(func() error {
			return wrap("undated", slots.read(gctx, &out.undatedTook, func() error {
				n, err := e.index.CountUndated(gctx, req.TenantID, req.Filters)
				if err != nil {
					return err
				}
				out.undated = &n
				return nil
			}))
		})
	}
	// Never fatal: returning nil keeps a semantic failure from canceling the
	// keyword lists.
	if hasVectors(qs) {
		g.Go(func() error {
			out.semanticErr = e.semanticLists(gctx, req, qs, out.queries, slots, fanout, &out.embed)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return listHits{}, err
	}
	return out, nil
}

// wrap prefixes a list's error with its name; nil stays nil.
func wrap(list string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", list, err)
	}
	return nil
}

// hasVectors reports whether any query needs a vector: whether the search
// waits on the embedding model at all.
func hasVectors(qs []query) bool {
	for _, q := range qs {
		if q.vector >= 0 {
			return true
		}
	}
	return false
}

// semanticLists embeds every query that needs a vector in one batch, under
// its own deadline, so a hung embedding model costs at most embedTimeout,
// then reads each query's vector search over the chunks and over the
// cards, each weighted, concurrently. A failure of any read fails them
// all. It records how long the embed took in embedTook, and each read's
// in its query's hits.
func (e *Engine) semanticLists(ctx context.Context, req Request, qs []query, hits []queryHits, slots readSlots,
	fanout int, embedTook *time.Duration) error {
	var texts []string
	for _, q := range qs {
		if q.vector >= 0 {
			texts = append(texts, q.text)
		}
	}
	vecs, err := e.embedQueries(ctx, texts, embedTook)
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	for i, q := range qs {
		if q.vector < 0 {
			continue
		}
		h, vec := &hits[i], vecs[q.vector]
		if q.chunks && e.vectorW > 0 {
			g.Go(func() error {
				return wrap("vector search", slots.read(gctx, &h.took.Vector, func() (err error) {
					h.vector, err = e.chunks.VectorSearch(gctx, req.TenantID, vec, fanout, req.Filters)
					return err
				}))
			})
		}
		if e.cardsW > 0 {
			g.Go(func() error {
				return wrap("card search", slots.read(gctx, &h.took.Cards, func() (err error) {
					h.cards, err = e.index.SearchCards(gctx, req.TenantID, vec, documentListDepth, req.Filters)
					return err
				}))
			})
		}
	}
	return g.Wait()
}

// embedQueries embeds texts, each behind the query prefix, in one call
// under its own deadline, recording how long it took in took.
func (e *Engine) embedQueries(ctx context.Context, texts []string, took *time.Duration) ([][]float32, error) {
	ectx, cancel := context.WithTimeout(ctx, e.embedTimeout)
	defer cancel()
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = e.queryPrefix + t
	}
	start := time.Now()
	vecs, err := e.embedder.Embed(ectx, prefixed)
	*took = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("embed query: got %d vectors, want %d", len(vecs), len(texts))
	}
	return vecs, nil
}

// timings are the list stages of hits: each the longest read of its
// stage, the embed the batch's.
func (h listHits) timings() Timings {
	t := Timings{Embed: h.embed, Undated: h.undatedTook}
	for _, q := range h.queries {
		t.BM25 = max(t.BM25, q.took.BM25)
		t.Fields = max(t.Fields, q.took.Fields)
		t.Titles = max(t.Titles, q.took.Titles)
		t.Vector = max(t.Vector, q.took.Vector)
		t.Cards = max(t.Cards, q.took.Cards)
	}
	return t
}
