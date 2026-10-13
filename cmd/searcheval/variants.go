package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// variant is a way of ranking the library for a query: the production
// pipeline, or a change to it measured against it.
type variant struct {
	name        string
	description string
	build       func(env) (ranker, error)
}

// env is what a variant builds its ranker from: the copy's stores, the
// query embedder, the loaded config and the log.
type env struct {
	docs     store.DocumentStore
	chunks   store.ChunkStore
	index    store.DocumentIndex
	embedder search.Embedder
	cfg      config.Config
	log      *slog.Logger
}

// ranker ranks the library's documents for a query.
type ranker interface {
	rank(ctx context.Context, query string) (ranking, error)
}

// ranking is a query's documents, best first, and how long each stage of
// ranking them took, in the order the stages ran.
type ranking struct {
	docs   []rankedDoc
	stages []stage
}

// rankedDoc is a ranked document.
type rankedDoc struct {
	id  string
	url string
}

// stage is a named step of a ranking and how long it took.
type stage struct {
	name string
	took time.Duration
}

// urls are the ranking's document URLs, which judgments are made by.
func (r ranking) urls() []string {
	out := make([]string, len(r.docs))
	for i, d := range r.docs {
		out[i] = d.url
	}
	return out
}

// variants are the registered variants, in the order -variants lists them.
var variants = []variant{
	{
		name:        "hybrid",
		description: "the daemon's search: BM25 and vector search fused by RRF, as config.yaml sets them",
		build:       func(e env) (ranker, error) { return newEngineRanker(e, search.ConfigFrom(e.cfg)), nil },
	},
	{
		name:        "bm25",
		description: "BM25 alone: the engine with the vector weight 0, which never embeds the query",
		build: func(e env) (ranker, error) {
			cfg := search.ConfigFrom(e.cfg)
			cfg.BM25Weight, cfg.VectorWeight = 1, 0
			return newEngineRanker(e, cfg), nil
		},
	},
	{
		name:        "vector",
		description: "vector search alone: the engine with the BM25 weight 0",
		build: func(e env) (ranker, error) {
			cfg := search.ConfigFrom(e.cfg)
			cfg.BM25Weight, cfg.VectorWeight = 0, 1
			return newEngineRanker(e, cfg), nil
		},
	},
}

// variantNames are the registered variants' names, in order.
func variantNames() []string {
	out := make([]string, len(variants))
	for i, v := range variants {
		out[i] = v.name
	}
	return out
}

// variantNamed is the registered variant called name.
func variantNamed(name string) (variant, bool) {
	for _, v := range variants {
		if v.name == name {
			return v, true
		}
	}
	return variant{}, false
}

// engineRanker ranks with the search engine, whole: the ranking a search
// for the first 100 results returns.
type engineRanker struct{ engine *search.Engine }

func newEngineRanker(e env, cfg search.Config) engineRanker {
	cfg.Log = e.log
	return engineRanker{engine: search.New(e.chunks, e.docs, e.index, e.embedder, cfg)}
}

// errDegraded is a search that came back keyword-only: scoring it would
// report BM25 alone as the pipeline being measured.
var errDegraded = errors.New("the search came back keyword-only")

func (r engineRanker) rank(ctx context.Context, query string) (ranking, error) {
	res, err := r.engine.Search(ctx, search.Request{TenantID: store.LocalTenantID, Query: query, K: store.MaxSearchK})
	if err != nil {
		return ranking{}, err
	}
	if res.Degraded {
		return ranking{}, fmt.Errorf("%w: %s", errDegraded, strings.Join(res.Warnings, "; "))
	}
	out := ranking{docs: make([]rankedDoc, len(res.Items)), stages: []stage{
		{"embed", res.Timings.Embed},
		{"bm25", res.Timings.BM25},
		{"vector", res.Timings.Vector},
		{"fuse", res.Timings.Fuse},
		{"hydrate", res.Timings.Hydrate},
	}}
	for i, hit := range res.Items {
		out.docs[i] = rankedDoc{id: hit.Document.ID, url: hit.Document.URL}
	}
	return out, nil
}
