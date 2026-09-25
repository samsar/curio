package insight

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

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
	// LabelingTimeout bounds the total time one run spends waiting on the LLM
	// labeler. Clusters left when it runs out get term labels. Default 15m.
	LabelingTimeout time.Duration
	// Center subtracts the corpus mean vector before clustering. Embedding
	// models like nomic-embed-text are anisotropic (their vectors sit in a
	// narrow cone), so raw cosines are uniformly high and everything collapses
	// into one giant cluster; centering removes that shared component so the
	// residual topical structure drives the graph. Cohesion and member
	// similarity are computed in the same space, so they describe the actual
	// clustering. No default is applied here — the config layer owns it
	// (default true).
	Center bool
}

// Engine runs the clustering pipeline: read document vectors → prepare (center,
// normalize) → cluster → summarize (centroid + cohesion + member similarities)
// → label → persist.
type Engine struct {
	docs        store.DocumentStore
	chunks      store.ChunkStore
	insights    store.InsightStore
	clusterer   Clusterer
	llmLabeler  Labeler // may be nil (no generation model configured)
	termLabeler *TermLabeler
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
	if cfg.LabelingTimeout <= 0 {
		cfg.LabelingTimeout = 15 * time.Minute
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

// Rebuild recomputes the tenant's clusters from scratch and returns the run's
// ID. Once clustering starts, the attempt is a cluster_runs row that ends done
// or failed (a failed run's ID comes back with the error), so callers/UI can
// report freshness. Two paths create no row and leave existing runs untouched:
// with nothing to cluster and a prior done run, that run's ID is returned; a
// failure before clustering starts (reading vectors, looking up the prior run,
// encoding the params) returns "" and the error.
func (e *Engine) Rebuild(ctx context.Context, tenantID string) (string, error) {
	dvs, err := e.chunks.DocumentVectors(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("read document vectors: %w", err)
	}
	dvs = e.dropNonFinite(tenantID, dvs)

	// Nothing to cluster (a fresh corpus, or every doc temporarily `pending`
	// during a `refetch --all` window): don't clobber a prior successful run's
	// interests with an empty one. Only record an empty run when there is
	// definitely nothing worth keeping; a store error is not "no prior run".
	if len(dvs) == 0 {
		prior, err := e.insights.LatestRun(ctx, tenantID, store.ClusterRunDone)
		switch {
		case err == nil:
			e.log.Info("clustering: no document vectors; keeping prior run",
				"tenant", tenantID, "run", prior.ID)
			return prior.ID, nil
		case !errors.Is(err, store.ErrNotFound):
			return "", fmt.Errorf("look up the last completed run: %w", err)
		}
	}

	params, err := e.runParams()
	if err != nil {
		return "", err
	}
	run := &store.ClusterRun{TenantID: tenantID, Algo: e.clusterer.Name(), Params: params}
	if err := e.insights.CreateRun(ctx, run); err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}

	if err := e.run(ctx, tenantID, run.ID, dvs); err != nil {
		e.recordFailure(ctx, tenantID, run.ID, len(dvs), err)
		return run.ID, err
	}
	return run.ID, nil
}

// maxLoggedIDs bounds how many document IDs one warning lists.
const maxLoggedIDs = 10

// dropNonFinite removes document vectors with a NaN or infinite component,
// warning once with their count and first IDs. One such vector would make
// the corpus mean NaN and fail every run, blaming whichever healthy
// document the unit-length check met first. Skipping it clusters the rest,
// the way a run already tolerates an all-zero vector (it falls out as noise)
// and a document deleted mid-run.
func (e *Engine) dropNonFinite(tenantID string, dvs []store.DocVector) []store.DocVector {
	if !slices.ContainsFunc(dvs, nonFinite) {
		return dvs
	}
	kept := make([]store.DocVector, 0, len(dvs))
	var skipped []string
	for _, dv := range dvs {
		if nonFinite(dv) {
			skipped = append(skipped, dv.DocumentID)
			continue
		}
		kept = append(kept, dv)
	}
	e.log.Warn("clustering: skipping documents whose vectors have NaN or infinite values; "+
		"re-embed them with `curio reindex <id>`",
		"tenant", tenantID, "count", len(skipped), "document_ids", skipped[:min(len(skipped), maxLoggedIDs)])
	return kept
}

func nonFinite(dv store.DocVector) bool {
	return slices.ContainsFunc(dv.Vector, func(x float32) bool {
		f := float64(x)
		return math.IsNaN(f) || math.IsInf(f, 0)
	})
}

// bookkeepingTimeout bounds recording a failed run. It runs detached from the
// run's context, which is often why the run failed (daemon shutdown), so the
// row doesn't stay "running" forever.
const bookkeepingTimeout = 10 * time.Second

// recordFailure marks the run failed and prunes stale runs, keeping the last
// good run's interests.
func (e *Engine) recordFailure(ctx context.Context, tenantID, runID string, numDocuments int, cause error) {
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()
	msg := cause.Error()
	res := store.RunResult{Status: store.ClusterRunFailed, NumDocuments: numDocuments, Error: &msg}
	if err := e.insights.FinishRun(bctx, runID, res); err != nil {
		e.log.Warn("mark cluster run failed", "run", runID, "err", err)
	}
	e.pruneStaleRuns(bctx, tenantID, runID)
}

// runParams is the JSON recorded on a run: the clusterer's parameters plus the
// engine's own vector preparation.
func (e *Engine) runParams() ([]byte, error) {
	params := maps.Clone(e.clusterer.Params())
	if params == nil {
		params = map[string]any{}
	}
	params["center"] = e.cfg.Center
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode clusterer params: %w", err)
	}
	return raw, nil
}

// pruneStaleRuns drops every run for the tenant except the latest done run, so
// a successful run's interests survive later failures. If there is definitely
// no done run yet, it keeps fallbackKeepID so a persistently-failing first run
// can't accumulate rows without bound. If the latest done run can't be read,
// it prunes nothing: this is best-effort cleanup, and deleting on a guess
// could take the last good interests with it.
func (e *Engine) pruneStaleRuns(ctx context.Context, tenantID, fallbackKeepID string) {
	keep := fallbackKeepID
	switch done, err := e.insights.LatestRun(ctx, tenantID, store.ClusterRunDone); {
	case err == nil:
		keep = done.ID
	case !errors.Is(err, store.ErrNotFound):
		e.log.Warn("skip pruning cluster runs: can't read the last completed run",
			"tenant", tenantID, "err", err)
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
	var clusterDur, labelDur time.Duration

	if n > 0 {
		points, err := preparePoints(dvs, e.cfg.Center)
		if err != nil {
			return err
		}

		start := time.Now()
		labels, err := e.clusterer.Cluster(ctx, points)
		if err != nil {
			return fmt.Errorf("cluster: %w", err)
		}
		clusterDur = time.Since(start)

		var groups [][]int
		groups, numNoise = clusterGroups(labels)
		start = time.Now()
		if cws, err = e.describe(ctx, tenantID, points, groups); err != nil {
			return err
		}
		labelDur = time.Since(start)
	}

	start := time.Now()
	if err := e.insights.ReplaceClusters(ctx, runID, cws); err != nil {
		return fmt.Errorf("write clusters: %w", err)
	}
	res := store.RunResult{Status: store.ClusterRunDone, NumDocuments: n, NumClusters: len(cws), NumNoise: numNoise}
	if err := e.insights.FinishRun(ctx, runID, res); err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	// Keep only the just-completed run: older runs and their clusters are
	// dropped to bound storage, since nothing reads them.
	if err := e.insights.PruneRunsExcept(ctx, tenantID, runID); err != nil {
		e.log.Warn("prune old cluster runs failed", "err", err)
	}
	e.log.Info("clustering done",
		"tenant", tenantID, "documents", n, "clusters", len(cws), "noise", numNoise,
		"cluster_ms", clusterDur.Milliseconds(), "label_ms", labelDur.Milliseconds(),
		"persist_ms", time.Since(start).Milliseconds())
	return nil
}

// clusterGroups collects the member indexes of each cluster, largest cluster
// first (ties: smallest label), whatever numbering the clusterer used, and
// counts the noise points.
func clusterGroups(labels []int) (groups [][]int, noise int) {
	byLabel := make(map[int][]int)
	for i, l := range labels {
		if l == NoiseLabel {
			noise++
			continue
		}
		byLabel[l] = append(byLabel[l], i)
	}
	keys := slices.Sorted(maps.Keys(byLabel))
	slices.SortStableFunc(keys, func(a, b int) int { return cmp.Compare(len(byLabel[b]), len(byLabel[a])) })
	for _, l := range keys {
		groups = append(groups, byLabel[l])
	}
	return groups, noise
}

// describe summarizes and names each cluster, keeping the order of groups.
func (e *Engine) describe(ctx context.Context, tenantID string, points []Point, groups [][]int) ([]store.ClusterWithMembers, error) {
	cws := make([]store.ClusterWithMembers, len(groups))
	infos := make([]ClusterInfo, len(groups))
	for i, g := range groups {
		members, cohesion := summarize(points, g)
		titles, err := e.titlesFor(ctx, members)
		if err != nil {
			return nil, err
		}
		cws[i] = store.ClusterWithMembers{
			Cluster: store.Cluster{TenantID: tenantID, Size: len(members), Cohesion: cohesion},
			Members: members,
		}
		infos[i] = ClusterInfo{Titles: titles, Size: len(members)}
	}
	labels, err := e.labelAll(ctx, infos)
	if err != nil {
		return nil, err
	}
	for i, lab := range labels {
		cws[i].Cluster.Label = optional(lab.Name)
		cws[i].Cluster.Summary = optional(lab.Summary)
	}
	return cws, nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// preparePoints turns document vectors into the unit vectors that both the
// clusterer and summarize work on, mean-centering them first when center is
// set (see Config.Center). Preparing once keeps a single n×d copy of the
// corpus in memory.
func preparePoints(dvs []store.DocVector, center bool) ([]Point, error) {
	dim := len(dvs[0].Vector)
	if dim == 0 {
		return nil, fmt.Errorf("document %s has an empty vector", dvs[0].DocumentID)
	}
	for _, dv := range dvs {
		if len(dv.Vector) != dim {
			return nil, fmt.Errorf("document %s has a %d-dimensional vector, want %d",
				dv.DocumentID, len(dv.Vector), dim)
		}
	}
	var mean []float64
	if center {
		mean = corpusMean(dvs, dim)
	}
	points := make([]Point, len(dvs))
	for i, dv := range dvs {
		points[i] = Point{ID: dv.DocumentID, Vector: unitResidual(dv.Vector, mean)}
	}
	return points, nil
}

// corpusMean returns the element-wise mean of all vectors (dim-length), or nil
// for fewer than two (nothing to center against).
func corpusMean(dvs []store.DocVector, dim int) []float64 {
	if len(dvs) < 2 {
		return nil
	}
	mean := make([]float64, dim)
	for _, dv := range dvs {
		for d, v := range dv.Vector {
			mean[d] += float64(v)
		}
	}
	for d := range mean {
		mean[d] /= float64(len(dvs))
	}
	return mean
}

// unitResidual returns the unit vector of v, first subtracting mean when it is
// non-nil. A zero residual yields a zero vector, so the point gets no edges and
// falls out as noise.
func unitResidual(v []float32, mean []float64) []float32 {
	residual := make([]float64, len(v))
	var sum float64
	for i, x := range v {
		r := float64(x)
		if mean != nil {
			r -= mean[i]
		}
		residual[i] = r
		sum += r * r
	}
	out := make([]float32, len(v))
	if sum == 0 {
		return out
	}
	inv := 1.0 / math.Sqrt(sum)
	for i, r := range residual {
		out[i] = float32(r * inv)
	}
	return out
}

// summarize computes the centroid of the members' prepared (unit) vectors,
// each member's cosine similarity to it, and the cluster cohesion (mean member
// similarity). Members are returned ordered by similarity descending (most
// representative first), with a stable tie-break on doc ID.
func summarize(points []Point, idxs []int) ([]store.ClusterMember, float64) {
	centroid := make([]float64, len(points[idxs[0]].Vector))
	for _, idx := range idxs {
		for d, v := range points[idx].Vector {
			centroid[d] += float64(v)
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
		for d, v := range points[idx].Vector {
			s += float64(v) * centroid[d]
		}
		s = max(s, 0)
		members[k] = store.ClusterMember{DocumentID: points[idx].ID, Similarity: s}
		total += s
	}
	cohesion := total / float64(len(idxs))

	slices.SortStableFunc(members, func(a, b store.ClusterMember) int {
		if c := cmp.Compare(b.Similarity, a.Similarity); c != 0 {
			return c
		}
		return strings.Compare(a.DocumentID, b.DocumentID)
	})
	return members, cohesion
}

// titlesFor fetches the titles of the most representative members (already
// sorted most-central-first), up to the configured cap. A document with no
// title falls back to its URL. A document deleted since its vector was read is
// skipped; any other store error fails the run.
func (e *Engine) titlesFor(ctx context.Context, members []store.ClusterMember) ([]string, error) {
	var titles []string
	for _, m := range members {
		if len(titles) >= e.cfg.TitlesPerCluster {
			break
		}
		d, err := e.docs.GetByID(ctx, m.DocumentID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load title of document %s: %w", m.DocumentID, err)
		}
		switch {
		case d.Title != nil && *d.Title != "":
			titles = append(titles, *d.Title)
		default:
			titles = append(titles, d.URL)
		}
	}
	return titles, nil
}

// labelAll names the clusters in order, honoring cfg.Labeling with a graceful
// fallback to deterministic term labels.
//
// The LLM is asked cluster by cluster until it fails in a way that would
// repeat — unreachable, an HTTP error, a timeout, or the run's labeling budget
// running out — and then isn't called again this run: every remaining cluster
// gets a term label at once instead of waiting out the same failure N times.
// An unparseable reply costs only that cluster its LLM label. Labeling stays
// sequential: a local Ollama serializes generation anyway and competes with
// index embeddings, and the budget plus largest-first order bound the wait and
// spend it where it matters most. If ctx itself ends, the run fails rather
// than completing with fallback labels.
func (e *Engine) labelAll(ctx context.Context, infos []ClusterInfo) ([]Label, error) {
	labels := make([]Label, len(infos))
	if e.cfg.Labeling == LabelingOff {
		return labels, nil
	}
	var llm Labeler
	if e.cfg.Labeling == LabelingLLM {
		llm = e.llmLabeler
	}
	// Every term label counts as a fallback when LLM labels were wanted,
	// including the clusters never offered to the model once it was switched
	// off, so the warning reports how many interests lack an LLM name.
	wantLLM := llm != nil
	budget, cancel := context.WithTimeout(ctx, e.cfg.LabelingTimeout)
	defer cancel()

	var fellBack int
	var reason error
	for i, info := range infos {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("label clusters: %w", err)
		}
		if llm != nil {
			lab, err := llm.Label(budget, info)
			if err == nil && lab.Name == "" {
				err = fmt.Errorf("%w: empty name", ErrUnparseableLabel)
			}
			switch {
			case err == nil:
				labels[i] = lab
				continue
			case ctx.Err() != nil:
				return nil, fmt.Errorf("label clusters: %w", ctx.Err())
			case errors.Is(err, ErrUnparseableLabel):
				// Only this reply was unusable; keep asking the model.
			default:
				llm = nil
			}
			reason = err
		}
		labels[i] = e.termLabeler.label(info)
		if wantLLM {
			fellBack++
		}
	}
	if fellBack > 0 {
		e.log.Warn("llm labeling fell back to term labels",
			"clusters", fellBack, "of", len(infos), "reason", reason)
	}
	return labels, nil
}
