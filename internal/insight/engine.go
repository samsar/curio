package insight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"

	"github.com/samsar/curio/internal/store"
)

// Labeling modes.
const (
	LabelingLLM   = "llm"   // try the LLM labeler, fall back to terms
	LabelingTerms = "terms" // deterministic term-frequency labels only
	LabelingOff   = "off"   // no labels (clusters are still grouped/sized)
)

// Config tunes the engine (not the clusterer — that carries its own params).
type Config struct {
	// Labeling selects how clusters are named: LabelingLLM | LabelingTerms |
	// LabelingOff.
	Labeling string
	// TitlesPerCluster caps how many representative titles feed the labeler
	// and are fetched per cluster. Default 12.
	TitlesPerCluster int
}

// Engine runs the clustering pipeline: read document vectors → cluster →
// summarize (medoid + cohesion + member similarities) → label → persist.
type Engine struct {
	docs        store.DocumentStore
	chunks      store.ChunkStore
	insights    store.InsightStore
	clusterer   Clusterer
	llmLabeler  Labeler // may be nil (no generation model configured)
	termLabeler Labeler
	cfg         Config
	log         *slog.Logger
}

// New constructs an Engine. llmLabeler may be nil, in which case labeling
// always uses the deterministic term labeler regardless of cfg.Labeling.
func New(
	docs store.DocumentStore,
	chunks store.ChunkStore,
	insights store.InsightStore,
	clusterer Clusterer,
	llmLabeler Labeler,
	cfg Config,
	log *slog.Logger,
) *Engine {
	if cfg.TitlesPerCluster <= 0 {
		cfg.TitlesPerCluster = 12
	}
	if cfg.Labeling == "" {
		cfg.Labeling = LabelingTerms
	}
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		docs:        docs,
		chunks:      chunks,
		insights:    insights,
		clusterer:   clusterer,
		llmLabeler:  llmLabeler,
		termLabeler: NewTermLabeler(),
		cfg:         cfg,
		log:         log,
	}
}

// Rebuild recomputes the tenant's clusters from scratch and returns the new
// run ID. It records a cluster_runs row for the attempt regardless of outcome
// (status done or failed), so callers/UI can always report freshness.
func (e *Engine) Rebuild(ctx context.Context, tenantID string) (string, error) {
	dvs, err := e.chunks.DocumentVectors(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("read document vectors: %w", err)
	}

	// Nothing to cluster (a fresh corpus, or every doc temporarily `pending`
	// during a `refetch --all` window): don't clobber a prior successful run's
	// interests with an empty one. Only record an empty run when there's
	// nothing worth keeping.
	if len(dvs) == 0 {
		if prior, perr := e.insights.LatestRun(ctx, tenantID, store.ClusterRunDone); perr == nil {
			e.log.Info("clustering: no document vectors; keeping prior run",
				"tenant", tenantID, "run", prior.ID)
			return prior.ID, nil
		}
	}

	params, _ := json.Marshal(e.clusterer.Params())
	run := &store.ClusterRun{TenantID: tenantID, Algo: e.clusterer.Name(), Params: params}
	if err := e.insights.CreateRun(ctx, run); err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}

	if err := e.run(ctx, tenantID, run.ID, dvs); err != nil {
		msg := err.Error()
		if ferr := e.insights.FinishRun(ctx, run.ID, store.ClusterRunFailed, len(dvs), 0, 0, &msg); ferr != nil {
			e.log.Warn("mark cluster run failed", "run", run.ID, "err", ferr)
		}
		// Keep the last good run's interests; drop this failed run and any
		// older/orphaned ones so failed attempts don't accumulate unbounded.
		e.pruneStaleRuns(ctx, tenantID, run.ID)
		return run.ID, err
	}
	return run.ID, nil
}

// pruneStaleRuns drops every run for the tenant except the latest done run, so
// a successful run's interests survive later failures. If there is no done run
// yet, it keeps fallbackKeepID so a persistently-failing first run can't
// accumulate rows without bound.
func (e *Engine) pruneStaleRuns(ctx context.Context, tenantID, fallbackKeepID string) {
	keep := fallbackKeepID
	if done, err := e.insights.LatestRun(ctx, tenantID, store.ClusterRunDone); err == nil {
		keep = done.ID
	}
	if keep == "" {
		return
	}
	if err := e.insights.PruneRunsExcept(ctx, tenantID, keep); err != nil {
		e.log.Warn("prune stale cluster runs failed", "err", err)
	}
}

// run does the work for one clustering pass, finishing the run on success.
func (e *Engine) run(ctx context.Context, tenantID, runID string, dvs []store.DocVector) error {
	n := len(dvs)
	cws := make([]store.ClusterWithMembers, 0)
	numNoise := 0

	if n > 0 {
		points := make([]Point, n)
		for i, dv := range dvs {
			points[i] = Point{ID: dv.DocumentID, Vector: dv.Vector}
		}

		labels, err := e.clusterer.Cluster(ctx, points)
		if err != nil {
			return fmt.Errorf("cluster: %w", err)
		}

		groups := make(map[int][]int)
		for i, l := range labels {
			if l == NoiseLabel {
				numNoise++
				continue
			}
			groups[l] = append(groups[l], i)
		}

		labelKeys := make([]int, 0, len(groups))
		for l := range groups {
			labelKeys = append(labelKeys, l)
		}
		sort.Ints(labelKeys)

		for _, l := range labelKeys {
			members, cohesion := summarize(points, groups[l])
			info := ClusterInfo{Titles: e.titlesFor(ctx, members), Size: len(members)}
			lab := e.label(ctx, info)

			c := store.Cluster{TenantID: tenantID, Size: len(members), Cohesion: cohesion}
			if lab.Name != "" {
				name := lab.Name
				c.Label = &name
			}
			if lab.Summary != "" {
				sum := lab.Summary
				c.Summary = &sum
			}
			cws = append(cws, store.ClusterWithMembers{Cluster: c, Members: members})
		}
	}

	if err := e.insights.ReplaceClusters(ctx, runID, cws); err != nil {
		return fmt.Errorf("write clusters: %w", err)
	}
	if err := e.insights.FinishRun(ctx, runID, store.ClusterRunDone, n, len(cws), numNoise, nil); err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	// Keep only the just-completed run; older runs (and their clusters) are
	// dropped to bound storage. Keeping history for trajectory analysis is a
	// later milestone.
	if err := e.insights.PruneRunsExcept(ctx, tenantID, runID); err != nil {
		e.log.Warn("prune old cluster runs failed", "err", err)
	}
	e.log.Info("clustering done",
		"tenant", tenantID, "documents", n, "clusters", len(cws), "noise", numNoise)
	return nil
}

// summarize computes the cluster centroid, each member's cosine similarity to
// it, and the cluster cohesion (mean member similarity). Members are returned
// ordered by similarity descending (most representative first), with a stable
// tie-break on document ID.
func summarize(points []Point, idxs []int) ([]store.ClusterMember, float64) {
	dim := len(points[idxs[0]].Vector)
	centroid := make([]float64, dim)
	units := make([][]float32, len(idxs))
	for k, idx := range idxs {
		u := unit(points[idx].Vector)
		units[k] = u
		for d := range dim {
			centroid[d] += float64(u[d])
		}
	}
	var cn float64
	for _, v := range centroid {
		cn += v * v
	}
	if cn > 0 {
		cn = math.Sqrt(cn)
		for d := range centroid {
			centroid[d] /= cn
		}
	}

	members := make([]store.ClusterMember, len(idxs))
	var total float64
	for k, idx := range idxs {
		var s float64
		for d := range dim {
			s += float64(units[k][d]) * centroid[d]
		}
		if s < 0 {
			s = 0
		}
		members[k] = store.ClusterMember{DocumentID: points[idx].ID, Similarity: s}
		total += s
	}
	cohesion := total / float64(len(idxs))

	sort.SliceStable(members, func(a, b int) bool {
		if members[a].Similarity != members[b].Similarity {
			return members[a].Similarity > members[b].Similarity
		}
		return members[a].DocumentID < members[b].DocumentID
	})
	return members, cohesion
}

// titlesFor fetches the titles of the most representative members (already
// sorted most-central-first), up to the configured cap. Missing documents are
// skipped; a document with no title falls back to its URL.
func (e *Engine) titlesFor(ctx context.Context, members []store.ClusterMember) []string {
	var titles []string
	for _, m := range members {
		if len(titles) >= e.cfg.TitlesPerCluster {
			break
		}
		d, err := e.docs.GetByID(ctx, m.DocumentID)
		if err != nil {
			continue
		}
		switch {
		case d.Title != nil && *d.Title != "":
			titles = append(titles, *d.Title)
		default:
			titles = append(titles, d.URL)
		}
	}
	return titles
}

// label names a cluster, honoring cfg.Labeling with a graceful fallback: LLM
// first (if configured), then the deterministic term labeler.
func (e *Engine) label(ctx context.Context, info ClusterInfo) Label {
	if e.cfg.Labeling == LabelingOff {
		return Label{}
	}
	if e.cfg.Labeling == LabelingLLM && e.llmLabeler != nil {
		lab, err := e.llmLabeler.Label(ctx, info)
		if err == nil && lab.Name != "" {
			return lab
		}
		if err != nil {
			e.log.Warn("llm labeling failed, using term fallback", "err", err)
		}
	}
	lab, _ := e.termLabeler.Label(ctx, info)
	return lab
}
