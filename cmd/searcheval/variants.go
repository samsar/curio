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
	"github.com/samsar/curio/internal/understand"
)

// variant is a way of ranking the library for a query: the production
// pipeline, or a change to it measured against it. config is the engine's
// configuration for it, from config.yaml's; its weights also say which
// lists it ranks with, and so which documents it can return (reach). A
// planned variant searches each query by its plan, the part of it mask
// keeps, in place of the query file's filters; nil for the others.
type variant struct {
	name        string
	description string
	config      func(config.Config) search.Config
	mask        func(understand.Plan) understand.Plan
}

// env is what a variant builds its ranker from: the copy's stores, the
// query embedder, the loaded config, the queries' plans when a planned
// variant runs, and the log.
type env struct {
	docs     store.DocumentStore
	chunks   store.ChunkStore
	index    store.DocumentIndex
	embedder search.Embedder
	cfg      config.Config
	plans    []queryPlan
	log      *slog.Logger
}

// ranker ranks the library's documents for query i of the file under its
// filters.
type ranker interface {
	rank(ctx context.Context, i int, query string, filters store.SearchFilters) (ranking, error)
}

// ranking is a query's documents, best first, and how long each stage of
// ranking them took, in the order the stages ran. A planned ranking also
// has the query's plan, made before it, and the filters of it it applied.
type ranking struct {
	docs    []rankedDoc
	stages  []stage
	plan    *queryPlan
	applied search.FilterRequest
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
		description: "the daemon's search: every list config.yaml weights, fused by RRF",
		config:      search.ConfigFrom,
	},
	{
		name:        "bm25",
		description: "BM25 alone: the engine with only the BM25 weight, which never embeds the query",
		config:      only(1, 0, 0, 0, 0),
	},
	{
		name:        "vector",
		description: "vector search alone: the engine with only the vector weight",
		config:      only(0, 1, 0, 0, 0),
	},
	{
		name:        "fields",
		description: "the fields list alone: BM25 over what each document was saved as, which never embeds the query",
		config:      only(0, 0, 1, 0, 0),
	},
	{
		name:        "cards",
		description: "the cards list alone: the engine with only the cards weight",
		config:      only(0, 0, 0, 1, 0),
	},
	{
		name: "titles",
		description: "the title list alone: the documents whose title and bookmark titles hold every word of a " +
			"query of two words or more, which never embeds the query",
		config: only(0, 0, 0, 0, 1),
	},
	{
		name: "planned",
		description: "the daemon's understood search: each query's plan from the planner config.yaml configures, " +
			"its filters, rewrites and names, in place of the file's filters",
		config: search.ConfigFrom,
		mask:   func(p understand.Plan) understand.Plan { return p },
	},
	{
		name:        "planned-filters",
		description: "the planned search with the plans' filters alone",
		config:      search.ConfigFrom,
		mask:        func(p understand.Plan) understand.Plan { return understand.Plan{Kind: p.Kind, Filters: p.Filters} },
	},
	{
		name:        "planned-rewrites",
		description: "the planned search with the plans' rewrites alone",
		config:      search.ConfigFrom,
		mask:        func(p understand.Plan) understand.Plan { return understand.Plan{Kind: p.Kind, Rewrites: p.Rewrites} },
	},
	{
		name:        "planned-names",
		description: "the planned search with the plans' names alone",
		config:      search.ConfigFrom,
		mask:        func(p understand.Plan) understand.Plan { return understand.Plan{Kind: p.Kind, Names: p.Names} },
	},
}

// only is the production configuration with the five lists weighted bm25,
// vector, fields, cards and titles.
func only(bm25, vector, fields, cards, titles float64) func(config.Config) search.Config {
	return func(c config.Config) search.Config {
		cfg := search.ConfigFrom(c)
		cfg.BM25Weight, cfg.VectorWeight, cfg.FieldsWeight, cfg.CardsWeight = bm25, vector, fields, cards
		cfg.TitleWeight = titles
		return cfg
	}
}

// build is v's ranker over e.
func (v variant) build(e env) ranker {
	r := newEngineRanker(e, v.config(e.cfg))
	if v.planned() {
		return plannedRanker{engine: r, plans: e.plans, mask: v.mask}
	}
	return r
}

// reach is which documents v's lists can return.
func (v variant) reach(c config.Config) reach {
	cfg := v.config(c)
	return reach{chunks: cfg.BM25Weight > 0 || cfg.VectorWeight > 0,
		documents: cfg.FieldsWeight > 0 || cfg.CardsWeight > 0 || cfg.TitleWeight > 0}
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

func (r engineRanker) rank(ctx context.Context, _ int, query string, filters store.SearchFilters) (ranking, error) {
	return r.search(ctx, search.Request{TenantID: store.LocalTenantID, Query: query, K: store.MaxSearchK,
		Filters: filters})
}

// search ranks req's first 100 documents.
func (r engineRanker) search(ctx context.Context, req search.Request) (ranking, error) {
	res, err := r.engine.Search(ctx, req)
	if err != nil {
		return ranking{}, err
	}
	if res.Degraded {
		return ranking{}, fmt.Errorf("%w: %s", errDegraded, strings.Join(res.Warnings, "; "))
	}
	out := ranking{docs: make([]rankedDoc, len(res.Items)), stages: []stage{
		{"embed", res.Timings.Embed},
		{"bm25", res.Timings.BM25},
		{"fields", res.Timings.Fields},
		{"titles", res.Timings.Titles},
		{"vector", res.Timings.Vector},
		{"cards", res.Timings.Cards},
		{"undated", res.Timings.Undated},
		{"fuse", res.Timings.Fuse},
		{"hydrate", res.Timings.Hydrate},
	}}
	for i, hit := range res.Items {
		out.docs[i] = rankedDoc{id: hit.Document.ID, url: hit.Document.URL}
	}
	return out, nil
}

// plannedRanker ranks a query by its plan, the part of it mask keeps, as
// the daemon searches an understood question: the plan's filters in place
// of the query file's, its rewrites and names searched with the words. A
// query the planner gave no plan is searched by its words alone, without
// filters, as the daemon falls back.
type plannedRanker struct {
	engine engineRanker
	plans  []queryPlan
	mask   func(understand.Plan) understand.Plan
}

func (r plannedRanker) rank(ctx context.Context, i int, query string, _ store.SearchFilters) (ranking, error) {
	qp := r.plans[i]
	req := search.Request{TenantID: store.LocalTenantID, Query: query, K: store.MaxSearchK}
	if qp.plan != nil {
		p := r.mask(*qp.plan)
		filters, err := p.Filters.Resolve()
		if err != nil {
			return ranking{}, fmt.Errorf("the plan's filters: %w", err)
		}
		req.Filters, req.Rewrites, req.Names = filters, p.Rewrites, p.Names
	}
	out, err := r.engine.search(ctx, req)
	if err != nil {
		return ranking{}, err
	}
	if qp.plan != nil {
		out.applied = r.mask(*qp.plan).Filters
	}
	out.plan = &qp
	out.stages = append([]stage{{"plan", qp.took}}, out.stages...)
	return out, nil
}
