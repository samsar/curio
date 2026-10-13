package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/devtool"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/eval"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/store"
)

// evaluate runs the evaluation opts asks for and writes its report: the
// text to stdout, then the JSON file, written only once everything
// succeeded. What can be checked without searching is checked first, each
// with its own error, before any query runs: the -json path, the config,
// the query file, the baseline (before the database is opened), the copy,
// its vectors' width, and Ollama.
func evaluate(ctx context.Context, opts options, stdout io.Writer, log *slog.Logger) error {
	if opts.json != "" {
		if err := devtool.CheckOutput(opts.json); err != nil {
			return err
		}
	}
	cfg, err := loadConfig(opts.config)
	if err != nil {
		return err
	}
	qf, err := readQueries(opts.queries)
	if err != nil {
		return err
	}
	var base *baseline
	if opts.baseline != "" {
		if base, err = loadBaseline(opts.baseline, qf); err != nil {
			return err
		}
	}
	lib, err := devtool.OpenCopy(ctx, opts.db)
	if err != nil {
		return err
	}
	defer func() {
		if err := lib.Close(); err != nil {
			log.Warn("close the database", "path", opts.db, "err", err)
		}
	}()
	if lib.Width != cfg.Embedding.Dim {
		return fmt.Errorf("the copy's vectors are %d wide, and %s sets embedding.dim %d: search it with its home's config.yaml",
			lib.Width, configName(opts.config), cfg.Embedding.Dim)
	}
	emb, err := connectEmbedder(ctx, cfg)
	if err != nil {
		return err
	}
	judged, err := resolveJudgments(ctx, lib.Documents, lib.Chunks, qf.set)
	if err != nil {
		return err
	}
	rep := newReport(opts, cfg, lib, qf, judged)
	log.Info("judgments resolved", "judgments", rep.Library.Judgments, "missing", rep.Library.Missing,
		"unsearchable", rep.Library.Unsearchable)

	e := env{docs: lib.Documents, chunks: lib.Chunks, embedder: emb, cfg: cfg, log: log}
	for _, v := range opts.variants {
		vr, err := runVariant(ctx, v, e, qf.set, judged)
		if err != nil {
			return err
		}
		log.Info("variant done", "variant", v.name, "queries", len(vr.Queries), "wall_ms", int64(vr.WallMS))
		rep.Variants = append(rep.Variants, vr)
	}
	rep.GeneratedAt = time.Now().UTC()

	if err := rep.writeText(stdout, base); err != nil {
		return fmt.Errorf("write the report: %w", err)
	}
	if opts.json == "" {
		return nil
	}
	if err := devtool.WriteJSON(opts.json, rep); err != nil {
		return err
	}
	log.Info("wrote the JSON report", "path", opts.json)
	return nil
}

// loadConfig is the config.yaml at path, strictly loaded, or the defaults
// without one. A path that doesn't exist is an error: config.Load returns
// the defaults for a missing file, and would measure another search than
// the one asked for.
func loadConfig(path string) (config.Config, error) {
	if path == "" {
		return config.Default(), nil
	}
	if _, err := os.Stat(path); err != nil {
		return config.Config{}, fmt.Errorf("the -config file: %w", err)
	}
	return config.Load(path)
}

// configName names the config searched with in messages.
func configName(path string) string {
	if path == "" {
		return "the default config"
	}
	return path
}

// queryFile is the query file as parsed, and the SHA-256 of the bytes
// parsed, which says whether a baseline's queries are the same ones.
type queryFile struct {
	path   string
	sha256 string
	set    *eval.QuerySet
}

// readQueries reads the query file once, and parses and hashes those
// bytes.
func readQueries(path string) (queryFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return queryFile{}, fmt.Errorf("the query file: %w", err)
	}
	set, err := eval.ParseQuerySet(data, path)
	if err != nil {
		return queryFile{}, err
	}
	sum := sha256.Sum256(data)
	return queryFile{path: path, sha256: hex.EncodeToString(sum[:]), set: set}, nil
}

// pingTimeout bounds asking Ollama whether it has the embedding model.
const pingTimeout = 10 * time.Second

// warmUpText is embedded once, behind the query prefix, before any query
// is, so the first query's embed doesn't pay for loading the model. Its
// time is not reported.
const warmUpText = "a query to warm the model up"

// connectEmbedder builds the query embedder as the daemon does, checks
// that Ollama has its model, and warms the model up. It never pulls one:
// a missing model is an error, which names it.
func connectEmbedder(ctx context.Context, cfg config.Config) (*embedder.Ollama, error) {
	timeout := time.Duration(cfg.Embedding.TimeoutSeconds) * time.Second
	model, at := cfg.Embedding.Model, cfg.Embedding.BaseURL
	emb, err := embedder.NewOllama(embedder.OllamaOptions{BaseURL: at, Model: model, Dim: cfg.Embedding.Dim,
		Timeout: timeout})
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	err = emb.Ping(pctx)
	cancel()
	switch {
	case errors.Is(err, ollama.ErrModelNotLoaded):
		return nil, fmt.Errorf("the Ollama at %s doesn't have %s, and searcheval never pulls a model: "+
			"pull it, or search with the config.yaml of the home the copy is from: %w", at, model, err)
	case errors.Is(err, ollama.ErrUnreachable):
		return nil, fmt.Errorf("no Ollama answers at %s to embed with %s (searcheval never starts one, "+
			"or pulls a model): %w", at, model, err)
	case err != nil:
		return nil, fmt.Errorf("ask the Ollama at %s for %s: %w", at, model, err)
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := emb.Embed(wctx, []string{cfg.Embedding.QueryPrefix + warmUpText}); err != nil {
		return nil, fmt.Errorf("warm %s up at %s: %w", model, at, err)
	}
	return emb, nil
}

// judgmentStatus is whether a search can return a judged document.
type judgmentStatus string

const (
	searchable judgmentStatus = "searchable"
	// missing: no document in the copy has the URL.
	missing judgmentStatus = "missing"
	// unsearchable: the document is failed or dead, or has no chunks, and
	// search never returns it. A pending document with chunks is searched.
	unsearchable judgmentStatus = "unsearchable"
)

// resolved is a judged URL's document in the copy, and whether search can
// return it.
type resolved struct {
	documentID string // empty when missing
	status     judgmentStatus
}

// resolveJudgments looks every judged URL up in the copy, once whatever
// the number of queries that judge it. A missing or unsearchable judgment
// still counts in every score's denominator.
func resolveJudgments(ctx context.Context, docs store.DocumentStore, chunks store.ChunkStore,
	qs *eval.QuerySet) (map[string]resolved, error) {
	out := map[string]resolved{}
	for _, q := range qs.Queries {
		for _, j := range q.Relevant {
			if _, done := out[j.URL]; done {
				continue
			}
			r, err := resolve(ctx, docs, chunks, j.URL)
			if err != nil {
				return nil, err
			}
			out[j.URL] = r
		}
	}
	return out, nil
}

// resolve finds url's document and says whether search can return it.
func resolve(ctx context.Context, docs store.DocumentStore, chunks store.ChunkStore, url string) (resolved, error) {
	doc, err := docs.GetByURL(ctx, store.LocalTenantID, url)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return resolved{status: missing}, nil
	case err != nil:
		return resolved{}, fmt.Errorf("look up the judged %s: %w", url, err)
	}
	r := resolved{documentID: doc.ID, status: searchable}
	if doc.State == store.DocStateFailed || doc.State == store.DocStateDead {
		r.status = unsearchable
		return r, nil
	}
	embs, err := chunks.EmbeddingsForDocument(ctx, doc.ID)
	if err != nil {
		return resolved{}, fmt.Errorf("read the chunks of the judged %s: %w", url, err)
	}
	if len(embs) == 0 {
		r.status = unsearchable
	}
	return r, nil
}

// totalStage is the stage the tool times itself: the whole rank call.
const totalStage = "total"

// runVariant ranks every query once, in file order, with v, and scores
// each ranking. A ranking that fails, a degraded search among them, fails
// the variant, naming the query.
func runVariant(ctx context.Context, v variant, e env, qs *eval.QuerySet, judged map[string]resolved) (variantReport, error) {
	rk, err := v.build(e)
	if err != nil {
		return variantReport{}, fmt.Errorf("variant %s: %w", v.name, err)
	}
	e.log.Info("running a variant", "variant", v.name, "queries", len(qs.Queries))
	began := time.Now()
	vr := variantReport{Name: v.name, Description: v.description, Queries: make([]queryReport, 0, len(qs.Queries))}
	scores := make([]eval.QueryScore, len(qs.Queries))
	took := map[string][]time.Duration{}
	for i, q := range qs.Queries {
		start := time.Now()
		r, err := rk.rank(ctx, q.Query)
		total := time.Since(start)
		if err != nil {
			return variantReport{}, fmt.Errorf("variant %s, query %s: %w", v.name, queryLabel(q.ID, i), err)
		}
		r.stages = append(r.stages, stage{totalStage, total})
		for _, s := range r.stages {
			if !slices.Contains(vr.stages, s.name) {
				vr.stages = append(vr.stages, s.name)
			}
			took[s.name] = append(took[s.name], s.took)
		}
		scores[i] = eval.ScoreQuery(q, r.urls())
		vr.Queries = append(vr.Queries, newQueryReport(i, q, scores[i], r, judged))
	}
	vr.WallMS = ms(time.Since(began))
	vr.LatencyMS = latencies(took)
	for _, g := range eval.Groups(qs) {
		vr.Groups = append(vr.Groups, newGroupReport(g, scores, vr.Queries))
	}
	return vr, nil
}

// queryLabel names a query in the report: its id, or its index, "#3".
func queryLabel(id string, index int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("#%d", index)
}

// latencies are each stage's nearest-rank p50 and p95, in milliseconds,
// over every query, zeros included.
func latencies(took map[string][]time.Duration) map[string]percentiles {
	out := make(map[string]percentiles, len(took))
	for name, ds := range took {
		sorted := slices.Sorted(slices.Values(ds))
		out[name] = percentiles{P50: ms(nearestRank(sorted, 50)), P95: ms(nearestRank(sorted, 95))}
	}
	return out
}

// nearestRank is the pct-th percentile of sorted by the nearest-rank
// method: the value at index ceil(pct·n/100)−1, in integers so a product
// like 95·20/100 lands exactly.
func nearestRank(sorted []time.Duration, pct int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[max((pct*len(sorted)+99)/100-1, 0)]
}

// ms is d in milliseconds.
func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
