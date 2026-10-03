package insight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/samsar/curio/internal/store"
)

// Labeling modes.
const (
	LabelingLLM   = "llm"   // try the LLM labeler, fall back to terms
	LabelingTerms = "terms" // deterministic term-frequency labels only
	LabelingOff   = "off"   // no labels (groups are still found and sized)
)

// RetiredRetention is how long a retired identity is kept, with its
// lineage, so an old link still finds what became of it.
const RetiredRetention = 180 * 24 * time.Hour

// Config tunes the engine (not the grouper, which carries its own params).
type Config struct {
	// Labeling selects how groups are named: LabelingLLM | LabelingTerms |
	// LabelingOff.
	Labeling string
	// TitlesPerCluster caps how many representative titles feed the labeler
	// and are fetched per group. Default 12.
	TitlesPerCluster int
	// LabelingTimeout bounds the total time one run spends waiting on the LLM
	// labeler, retries included. Groups left when it runs out get term
	// labels. Default 15m.
	LabelingTimeout time.Duration
	// Center subtracts the corpus mean vector before grouping. Many
	// embedding models are anisotropic (their vectors sit in a narrow
	// cone), so raw cosines are uniformly high and everything collapses
	// into one giant group; centering removes that shared component so the
	// residual topical structure drives the graph. Cohesion and member
	// similarity are computed in the same space, so they describe the actual
	// grouping. No default is applied here — the config layer owns it
	// (default true).
	Center bool
}

// Engine rebuilds a tenant's interests: read the document vectors → prepare
// (center, normalize) → group, from the previous grouping when it can →
// merge near-duplicates → place strays → carry identities over → label the
// groups that need it → commit the run in one transaction → prune.
type Engine struct {
	docs        store.DocumentStore
	chunks      store.ChunkStore
	insights    store.InsightStore
	grouper     Grouper
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
	grouper Grouper,
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
		grouper:     grouper,
		llmLabeler:  llmLabeler,
		termLabeler: NewTermLabeler(),
		cfg:         cfg,
		log:         log,
	}
}

// Rebuild regroups the tenant's library and returns the run's ID.
//
// It starts from the tenant's latest done run, the prior: warm, from the
// prior's seeds, when the prior was made by this grouper with these params,
// fresh otherwise; identities carry over from the prior either way. The
// split check runs once the changes absorbed since the last one reach four
// times the change threshold (see plan).
//
// Once grouping starts, the attempt is an interest run that ends done or
// failed (a failed run's ID comes back with the error). Two paths create no
// run: with no vectors to group and a prior, the prior's ID is returned, so
// a library momentarily without vectors (a refetch of every document, say)
// retires nothing; and a failure before the run is created (reading the
// prior or the vectors) returns "" and the error.
func (e *Engine) Rebuild(ctx context.Context, tenantID string, trigger store.RunTrigger) (string, error) {
	priorRun, err := e.insights.LatestRun(ctx, tenantID, store.InterestRunDone)
	switch {
	case errors.Is(err, store.ErrNotFound): // the tenant's first rebuild
	case err != nil:
		return "", fmt.Errorf("read the latest done run: %w", err)
	}
	readAt := time.Now().UTC()
	dvs, err := e.chunks.DocumentVectors(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("read document vectors: %w", err)
	}
	t := timings{read: time.Since(readAt)}
	dvs = e.dropNonFinite(tenantID, dvs)
	if len(dvs) == 0 && priorRun != nil {
		e.log.Info("interests: no document vectors; keeping the last run", "tenant", tenantID, "run", priorRun.ID)
		return priorRun.ID, nil
	}
	params, err := e.runParams()
	if err != nil {
		return "", err
	}
	var prior *previous
	if priorRun != nil {
		if prior, err = e.readPrior(ctx, priorRun); err != nil {
			return "", err
		}
	}

	p := e.plan(prior, params, dvs)
	run := &store.InterestRun{TenantID: tenantID, Trigger: trigger, Grouper: e.grouper.Name(), Params: params,
		VectorsReadAt: &readAt, RunOutcome: store.RunOutcome{Kind: p.kind(), SplitCheck: p.split, Shape: p.shape}}
	if err := e.insights.CreateRun(ctx, run); err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}
	if err := e.run(ctx, run, prior, p, dvs, t); err != nil {
		e.recordFailure(ctx, tenantID, run.ID, len(dvs), err)
		return run.ID, err
	}
	return run.ID, nil
}

// readPrior reads what a rebuild starts from of the prior run: its
// assignments and groups.
func (e *Engine) readPrior(ctx context.Context, run *store.InterestRun) (*previous, error) {
	assignments, err := e.insights.RunAssignments(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("read run %s: %w", run.ID, err)
	}
	groups, err := e.insights.RunGroups(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("read run %s: %w", run.ID, err)
	}
	return newPrevious(run, assignments, groups), nil
}

// plan is what a rebuild sets out to do, decided before it groups.
type plan struct {
	// warm: the prior was made by this grouper with these params, so the
	// grouping can start from its seeds.
	warm  bool
	shape store.InterestShape // the prior's shape, or flat
	// changed counts the documents added or gone since the prior.
	changed int
	split   bool
}

// kind is the run's kind as planned; the grouping can still make it fresh.
func (p plan) kind() store.RunKind {
	if p.warm {
		return store.RunKindWarm
	}
	return store.RunKindFresh
}

// The split check's cadence: it runs once the changes absorbed since the
// last one reach splitEvery times the change threshold, max(minChanges,
// ⌈changeShare of the prior's documents⌉). At automatic rebuilds every
// threshold's worth of changes, that is every fourth rebuild, and a manual
// one moves it closer only by the changes it absorbed.
const (
	changeShare = 0.05
	minChanges  = 5
	splitEvery  = 4
)

func (e *Engine) plan(prior *previous, params []byte, dvs []store.DocVector) plan {
	if prior == nil {
		return plan{shape: store.InterestShapeFlat}
	}
	p := plan{
		warm:    prior.run.Grouper == e.grouper.Name() && bytes.Equal(prior.run.Params, params),
		shape:   prior.run.Shape,
		changed: prior.changed(dvs),
	}
	threshold := max(minChanges, int(math.Ceil(changeShare*float64(prior.run.NumDocuments))))
	p.split = p.warm && prior.run.ChangesSinceSplit+p.changed >= splitEvery*threshold
	return p
}

// timings are how long each part of a rebuild took, for its log line.
type timings struct {
	read, group, label, persist time.Duration
}

// run does the work of one rebuild after its run is created: group, label
// and commit, then prune.
func (e *Engine) run(ctx context.Context, run *store.InterestRun, prior *previous, p plan, dvs []store.DocVector, t timings) error {
	start := time.Now()
	gr, err := e.group(ctx, prior, p, dvs)
	if err != nil {
		return err
	}
	t.group = time.Since(start)

	start = time.Now()
	labels, stats, err := e.label(ctx, gr)
	if err != nil {
		return err
	}
	t.label = time.Since(start)

	start = time.Now()
	c := gr.commit(run, prior, labels)
	if err := e.insights.CommitRun(ctx, c); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	e.prune(ctx, run.TenantID, run.ID)
	t.persist = time.Since(start)
	e.logRebuilt(run, c, gr, stats, t)
	return nil
}

// group runs the grouping steps: Group, from the prior's seeds when the
// plan is warm, then the merge of near-duplicates, the strays, and the
// carry-over of identities.
func (e *Engine) group(ctx context.Context, prior *previous, p plan, dvs []store.DocVector) (*grouped, error) {
	points, mean, err := PreparePoints(dvs, e.cfg.Center)
	if err != nil {
		return nil, err
	}
	in := GroupInput{Points: points, Shape: Shape(p.shape), Split: p.split}
	if p.warm {
		in.Prior = prior.seeds()
	}
	g, err := e.grouper.Group(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("group: %w", err)
	}
	return newGrouped(points, mean, in, g, prior, p)
}

// prune drops the runs before this one and what they alone held: the
// lineage the runs since have told again, and identities retired longer
// ago than RetiredRetention. Each step is best effort.
func (e *Engine) prune(ctx context.Context, tenantID, runID string) {
	if err := e.insights.PruneRunsExcept(ctx, tenantID, runID); err != nil {
		e.log.Warn("interests: prune old runs failed", "tenant", tenantID, "err", err)
	}
	if err := e.insights.TrimLineage(ctx, tenantID, runID); err != nil {
		e.log.Warn("interests: trim lineage failed", "tenant", tenantID, "err", err)
	}
	if _, err := e.insights.PruneRetired(ctx, tenantID, time.Now().Add(-RetiredRetention)); err != nil {
		e.log.Warn("interests: prune retired interests failed", "tenant", tenantID, "err", err)
	}
}

// logRebuilt writes the rebuild's one INFO line.
func (e *Engine) logRebuilt(run *store.InterestRun, c store.RunCommit, gr *grouped, stats labelStats, t timings) {
	o, areas := c.Outcome, gr.areas.Counts
	e.log.Info("interests rebuilt",
		"tenant", run.TenantID, "run", run.ID, "trigger", run.Trigger, "kind", o.Kind, "split_check", o.SplitCheck,
		"shape", o.Shape, "documents", o.NumDocuments, "areas", o.NumAreas, "interests", o.NumInterests,
		"kept", o.Kept, "created", o.Created, "split", o.Split, "merged", o.Merged, "moved", o.Moved,
		"dissolved", o.Dissolved, "areas_kept", areas.Kept, "areas_created", areas.Created,
		"areas_dissolved", areas.Dissolved, "loose", o.NumLoose, "unsorted", o.NumUnsorted,
		"changed", o.ChangedDocuments, "read_ms", t.read.Milliseconds(), "group_ms", t.group.Milliseconds(),
		"label_ms", t.label.Milliseconds(), "labels_llm", stats.llm, "labels_terms", stats.terms,
		"persist_ms", t.persist.Milliseconds())
}

// maxLoggedIDs bounds how many document IDs one warning lists.
const maxLoggedIDs = 10

// dropNonFinite removes document vectors with a NaN or infinite component,
// warning once with their count and first IDs. One such vector would make
// the corpus mean NaN and fail every run, blaming whichever healthy
// document the unit-length check met first. Skipping it groups the rest,
// the way a run already tolerates an all-zero vector (it ends up unsorted)
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
	e.log.Warn("interests: skipping documents whose vectors have NaN or infinite values; "+
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
// good run's interests and this failure.
func (e *Engine) recordFailure(ctx context.Context, tenantID, runID string, numDocuments int, cause error) {
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()
	if err := e.insights.FailRun(bctx, runID, numDocuments, cause.Error()); err != nil {
		e.log.Warn("interests: mark the run failed", "run", runID, "err", err)
	}
	e.pruneStaleRuns(bctx, tenantID, runID)
}

// runParams is the JSON recorded on a run: the grouper's parameters plus the
// engine's own vector preparation. A change to either makes the next rebuild
// fresh, so it is canonical JSON: encoding/json sorts map keys.
func (e *Engine) runParams() ([]byte, error) {
	params := maps.Clone(e.grouper.Params())
	if params == nil {
		params = map[string]any{}
	}
	params["center"] = e.cfg.Center
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode grouper params: %w", err)
	}
	return raw, nil
}

// pruneStaleRuns drops every run for the tenant except the latest done run
// and failedRunID, the run that just failed. The done run's interests survive
// later failures, and the failure stays the newest run, which the Interests
// page reports, until another run replaces it: at most two rows, so a
// persistently failing rebuild can't accumulate them. If the latest done run
// can't be read, it prunes nothing: this is best-effort cleanup, and deleting
// on a guess could take the last good interests with it.
func (e *Engine) pruneStaleRuns(ctx context.Context, tenantID, failedRunID string) {
	keep := []string{failedRunID}
	switch done, err := e.insights.LatestRun(ctx, tenantID, store.InterestRunDone); {
	case err == nil:
		keep = append(keep, done.ID)
	case !errors.Is(err, store.ErrNotFound):
		e.log.Warn("interests: skip pruning runs: can't read the last completed run",
			"tenant", tenantID, "err", err)
		return
	}
	if err := e.insights.PruneRunsExcept(ctx, tenantID, keep...); err != nil {
		e.log.Warn("interests: prune stale runs failed", "tenant", tenantID, "err", err)
	}
}

// PreparePoints turns document vectors into the unit vectors every
// grouping step works on, mean-centering them first when center is set (see
// Config.Center), and returns the mean they were centered on: nil when not
// centering or with fewer than two vectors. A grouping keeps the mean so a
// document indexed later can be placed in the same space. Preparing once
// keeps a single n×d copy of the corpus in memory. A vector of another
// width, an empty one, or one with a NaN or infinite component is an error
// naming its document: a non-finite component would make the mean NaN and
// blame whichever healthy document a later check met first.
func PreparePoints(dvs []store.DocVector, center bool) ([]Point, []float64, error) {
	if len(dvs) == 0 {
		return []Point{}, nil, nil
	}
	dim := len(dvs[0].Vector)
	if dim == 0 {
		return nil, nil, fmt.Errorf("document %s has an empty vector", dvs[0].DocumentID)
	}
	for _, dv := range dvs {
		if len(dv.Vector) != dim {
			return nil, nil, fmt.Errorf("document %s has a %d-dimensional vector, want %d",
				dv.DocumentID, len(dv.Vector), dim)
		}
		if nonFinite(dv) {
			return nil, nil, fmt.Errorf("document %s has a NaN or infinite component", dv.DocumentID)
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
	return points, mean, nil
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
// not empty (it then has v's width). A zero residual yields a zero vector, so
// the point gets no edges and ends up in no group.
func unitResidual(v []float32, mean []float64) []float32 {
	residual := make([]float64, len(v))
	var sum float64
	for i, x := range v {
		r := float64(x)
		if len(mean) > 0 {
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
