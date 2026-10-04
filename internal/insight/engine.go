package insight

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"runtime/debug"
	"slices"
	"strings"
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
	// Placer places, once a rebuild commits, the documents indexed while
	// it ran; nil places none.
	Placer *Placer
	// Drift says how the embeddings drifted, "" while they haven't: a run
	// built meanwhile, which only a request makes, says so in its log
	// line. nil reports no drift.
	Drift func() string
	// Indexing reports whether index jobs are pending or running. A run
	// that finds them before or after it reads the vectors, while a
	// re-embedding owes a fresh rebuild, may have read some of the old
	// build's (a rebuild asked for mid-drain), so it leaves that rebuild
	// owed. nil reports none.
	Indexing func(ctx context.Context) (bool, error)
	// MapOff is insight.map: false. A rebuild then draws no map, and its
	// run commits with none, as a run from before maps has.
	MapOff bool
}

// Engine rebuilds a tenant's interests: read the document vectors → prepare
// (center, normalize) → group, from the previous grouping when it can →
// merge near-duplicates → place strays → carry identities over → draw the
// map → label the groups that need it → commit the run in one transaction →
// prune.
type Engine struct {
	docs        store.DocumentStore
	chunks      store.ChunkStore
	insights    store.InsightStore
	grouper     Grouper
	llmLabeler  Labeler // may be nil (no generation model configured)
	termLabeler *TermLabeler
	cfg         Config
	log         *slog.Logger
	now         func() time.Time // when a run reads the vectors
	// buildMap draws a run's map (BuildMap), within mapTimeout.
	buildMap   func(ctx context.Context, in MapInput) (*Map, error)
	mapTimeout time.Duration
}

// mapTimeout bounds drawing a run's map: about ten times what the owner's
// 5,000 documents take cold. A map that runs out of time fails, and the
// run commits without one.
const mapTimeout = 2 * time.Minute

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
		now:         time.Now,
		buildMap:    BuildMap,
		mapTimeout:  mapTimeout,
	}
}

// Rebuild regroups the tenant's library and returns the run's ID.
//
// It starts from the tenant's latest done run, the prior: warm, from the
// prior's seeds, when the prior was made by this grouper with these params
// and no fresh rebuild is owed, fresh otherwise; identities carry over
// from the prior either way. The split check runs once the changes absorbed
// since the last one reach four times the change threshold (see plan).
//
// Once grouping starts, the attempt is an interest run that ends done or
// failed (a failed run's ID comes back with the error). A failure before
// the run is created returns "" and the error: reading the prior, the
// vectors or the tenant's state, or finding no vector to group while a
// prior stands (errNothingToGroup), which keeps the prior, so a library
// momentarily without vectors (a refetch of every document, say) retires
// nothing. Every failure but a cancellation counts as one failed rebuild of
// the tenant's (InsightStore.RecordFailure), which the scheduler backs off
// from.
func (e *Engine) Rebuild(ctx context.Context, tenantID string, trigger store.RunTrigger) (string, error) {
	in, err := e.read(ctx, tenantID)
	if err == nil && len(in.dvs) == 0 && in.prior != nil {
		err = errNothingToGroup
	}
	if err != nil {
		e.recordFailure(ctx, tenantID, "", 0, err)
		return "", err
	}
	p := e.plan(in)
	run := &store.InterestRun{TenantID: tenantID, Trigger: trigger, Grouper: e.grouper.Name(), Params: in.params,
		VectorsReadAt: &in.readAt, RunOutcome: store.RunOutcome{Kind: p.kind(), SplitCheck: p.split, Shape: p.shape}}
	if err := e.insights.CreateRun(ctx, run); err != nil {
		err = fmt.Errorf("create run: %w", err)
		e.recordFailure(ctx, tenantID, "", len(in.dvs), err)
		return "", err
	}
	if err := e.run(ctx, run, in, p); err != nil {
		e.recordFailure(ctx, tenantID, run.ID, len(in.dvs), err)
		return run.ID, err
	}
	return run.ID, nil
}

// errNothingToGroup fails a rebuild that finds no vector to group while a
// done run stands: every fetched document was indexed without a chunk, or
// its vector is NaN or infinite. Committing nothing keeps that run's
// interests, which an empty grouping would retire; counting a failure
// moves the scheduler's next attempt past its backoff. A rebuild that
// succeeded changing nothing would leave the library as due as it was, and
// the scheduler would queue the same rebuild again the moment it finished.
var errNothingToGroup = errors.New("nothing to group: no fetched document has a usable vector; " +
	"the last interests stand")

// input is what a rebuild reads before it plans.
type input struct {
	prior  *previous // the latest done run, nil for none
	readAt time.Time // when the vectors were read
	dvs    []store.DocVector
	// changes are what changed since the prior read its vectors, read
	// after the vectors: what this rebuild absorbs.
	changes store.RunChanges
	state   store.InsightState
	// midReindex: a re-embedding owes a fresh rebuild, and index jobs
	// were left before or after the vectors were read (Config.Indexing).
	midReindex bool
	params     []byte // this rebuild's grouper and engine params
	read       time.Duration
}

// read reads what a rebuild starts from: the prior with its assignments and
// groups, the vectors (non-finite ones dropped), the changes since the
// prior, the tenant's state, whether a re-embedding owed is still
// draining, and the params. The queue is read on both sides of the
// vectors: a drain that ends during their read (seconds, on a large
// library) has jobs left before it, and a re-embedding owed before the
// read but enqueued during it has jobs left after.
func (e *Engine) read(ctx context.Context, tenantID string) (input, error) {
	var in input
	priorRun, err := e.insights.LatestRun(ctx, tenantID, store.InterestRunDone)
	switch {
	case errors.Is(err, store.ErrNotFound): // the tenant's first rebuild
	case err != nil:
		return input{}, fmt.Errorf("read the latest done run: %w", err)
	}
	draining, err := e.indexing(ctx)
	if err != nil {
		return input{}, err
	}
	in.readAt = e.now().UTC()
	start := time.Now()
	dvs, err := e.chunks.DocumentVectors(ctx, tenantID)
	if err != nil {
		return input{}, fmt.Errorf("read document vectors: %w", err)
	}
	in.read = time.Since(start)
	in.dvs = byDocumentID(e.dropNonFinite(tenantID, dvs))
	if priorRun != nil {
		if in.changes, err = e.insights.Changes(ctx, priorRun); err != nil {
			return input{}, err
		}
		if in.prior, err = e.readPrior(ctx, priorRun); err != nil {
			return input{}, err
		}
	}
	if in.state, err = e.insights.State(ctx, tenantID); err != nil {
		return input{}, err
	}
	if in.state.FreshOwed == store.FreshReindex {
		if !draining {
			if draining, err = e.indexing(ctx); err != nil {
				return input{}, err
			}
		}
		in.midReindex = draining
	}
	if in.params, err = e.runParams(); err != nil {
		return input{}, err
	}
	return in, nil
}

// indexing reports whether index jobs are pending or running
// (Config.Indexing).
func (e *Engine) indexing(ctx context.Context) (bool, error) {
	if e.cfg.Indexing == nil {
		return false, nil
	}
	busy, err := e.cfg.Indexing(ctx)
	if err != nil {
		return false, fmt.Errorf("read whether index jobs are left: %w", err)
	}
	return busy, nil
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
	// warm: the prior was made by this grouper with these params, and no
	// fresh rebuild is owed, so the grouping can start from its seeds.
	warm  bool
	shape store.InterestShape // the prior's shape, or flat
	// changed counts the documents changed since the prior.
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

// splitEvery is the split check's cadence: it runs once the changes
// absorbed since the last one reach splitEvery times the change Threshold
// of the prior's documents. A library rebuilt at every threshold's worth
// of changes is split-checked every fourth rebuild; a rebuild sooner moves
// the check closer only by the changes it absorbed.
const splitEvery = 4

func (e *Engine) plan(in input) plan {
	if in.prior == nil {
		return plan{shape: store.InterestShapeFlat}
	}
	prior := in.prior.run
	p := plan{
		warm:    in.state.FreshOwed == "" && !e.paramsChanged(prior, in.params),
		shape:   prior.Shape,
		changed: in.changes.Total(),
	}
	p.split = p.warm && prior.ChangesSinceSplit+p.changed >= splitEvery*Threshold(prior.NumDocuments)
	return p
}

// ParamsChanged reports whether run was grouped by another grouper, or
// with other params, than a rebuild now would be: the next rebuild is then
// fresh, and the scheduler owes it. Params that don't encode report no
// change: the rebuild fails on them, and says why.
func (e *Engine) ParamsChanged(run *store.InterestRun) bool {
	params, err := e.runParams()
	return err == nil && e.paramsChanged(run, params)
}

func (e *Engine) paramsChanged(run *store.InterestRun, params []byte) bool {
	return run.Grouper != e.grouper.Name() || !bytes.Equal(run.Params, params)
}

// byDocumentID is dvs in document ID order, the order the store reads them
// in: a copy when it isn't, so what the map draws never hangs on it.
func byDocumentID(dvs []store.DocVector) []store.DocVector {
	byID := func(a, b store.DocVector) int { return strings.Compare(a.DocumentID, b.DocumentID) }
	if slices.IsSortedFunc(dvs, byID) {
		return dvs
	}
	sorted := slices.Clone(dvs)
	slices.SortFunc(sorted, byID)
	return sorted
}

// timings are how long each part of a rebuild took, for its log line.
type timings struct {
	read, group, label, persist time.Duration
}

// run does the work of one rebuild after its run is created: group, draw
// the map, label and commit, then prune and place the documents indexed
// meanwhile.
func (e *Engine) run(ctx context.Context, run *store.InterestRun, in input, p plan) error {
	t := timings{read: in.read}
	start := time.Now()
	gr, points, err := e.group(ctx, in.prior, p, in.dvs)
	if err != nil {
		return err
	}
	t.group = time.Since(start)

	var drawn drawnMap
	if !e.cfg.MapOff {
		if drawn, err = e.drawMap(ctx, run, in, gr, points); err != nil {
			return err
		}
	}
	// The map was the last reader of the points, which nothing reads past
	// here, and of the neighbour lists: labelling can take minutes without
	// them.
	gr.g.Neighbours = nil

	start = time.Now()
	labels, stats, err := e.label(ctx, gr)
	if err != nil {
		return err
	}
	t.label = time.Since(start)

	start = time.Now()
	c := gr.commit(run, in.prior, labels, drawn)
	c.ReadMidReindex = in.midReindex
	if err := e.insights.CommitRun(ctx, c); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	e.prune(ctx, run.TenantID, run.ID)
	t.persist = time.Since(start)
	e.logRebuilt(run, c, gr, stats, t, e.sweep(ctx, run.TenantID))
	return nil
}

// drawMap draws the run's map within mapTimeout. A map that fails, runs out
// of time, panics or draws something invalid is no failure of the rebuild:
// it comes back failed, with why, after one warning, and the run commits
// without one. Only the rebuild's own cancellation is an error.
func (e *Engine) drawMap(ctx context.Context, run *store.InterestRun, in input, gr *grouped, points []Point) (drawnMap, error) {
	params, err := MapParams(e.cfg.Center, MapSeed)
	if err != nil {
		return drawnMap{}, err
	}
	start := time.Now()
	mctx, cancel := context.WithTimeout(ctx, e.mapTimeout)
	defer cancel()
	m, stack, err := e.callMapper(mctx, e.mapInput(in, gr, points))
	d := drawnMap{m: m, took: time.Since(start), params: params}
	if ctx.Err() != nil {
		return drawnMap{}, fmt.Errorf("draw the map: %w", ctx.Err())
	}
	if err == nil {
		err = m.Validate(len(gr.ids), gr.numAreas, len(gr.groups)-gr.numAreas)
	}
	if err == nil {
		return d, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("the map took longer than %v: %w", e.mapTimeout, err)
	}
	// A failed map always says why: one that said nothing would read as
	// none drawn.
	d.m, d.failed = nil, cmp.Or(oneLine(err.Error(), maxMapError), "the map failed without saying why")
	args := []any{"tenant", run.TenantID, "run", run.ID, "err", d.failed, "map_ms", d.took.Milliseconds()}
	if stack != "" {
		args = append(args, "stack", stack)
	}
	e.log.Warn("interests: map failed", args...)
	return d, nil
}

// maxMapError is the longest a failed map's error is kept.
const maxMapError = 512

// callMapper calls the engine's map builder, turning a panic into an error
// that starts "panic:" and returning its stack apart, for the log.
func (e *Engine) callMapper(ctx context.Context, in MapInput) (m *Map, stack string, err error) {
	defer func() {
		if r := recover(); r != nil {
			m, stack, err = nil, string(debug.Stack()), fmt.Errorf("panic: %v", r)
		}
	}()
	m, err = e.buildMap(ctx, in)
	return m, "", err
}

// oneLine is s on one line, cut to at most limit runes.
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit-1]) + "…"
	}
	return s
}

// mapInput is what the map of grouping gr draws from: the prior's map is
// always the one aligned to, and the document map may start from it unless
// a re-embedding owes a fresh rebuild or the embeddings drifted, since
// either moved the vectors under it.
func (e *Engine) mapInput(in input, gr *grouped, points []Point) MapInput {
	mi := MapInput{Points: points, Grouping: gr.g, Centroids: gr.centroids, Fits: gr.fits,
		AreaKeys: gr.keys(0, gr.numAreas), InterestKeys: gr.keys(gr.numAreas, len(gr.groups)),
		AreaStarts: gr.areas.Starts(), InterestStarts: gr.interests.Starts(), Unchanged: in.changes.Total() == 0,
		Center: e.cfg.Center, Seed: MapSeed}
	if in.prior != nil && in.prior.mapPrior != nil {
		mi.Prior = in.prior.mapPrior
		drifted := e.cfg.Drift != nil && e.cfg.Drift() != ""
		mi.WarmDocs = in.state.FreshOwed != store.FreshReindex && !drifted
	}
	return mi
}

// drawnMap is the map step's outcome: the map, or why there is none, how
// long it took and its params. The zero value is no map drawn, the map
// being off.
type drawnMap struct {
	m      *Map
	failed string
	took   time.Duration
	params []byte
}

// runMap is the run's record of the map: nil when none was drawn.
func (d drawnMap) runMap() *store.RunMap {
	switch {
	case d.m != nil:
		return &store.RunMap{Status: store.MapBuilt, Kind: d.m.Kind, Took: d.took, Params: d.params,
			DotRadius: d.m.DotRadius, Unsorted: d.m.Unsorted}
	case d.failed != "":
		return &store.RunMap{Status: store.MapFailed, Error: d.failed, Took: d.took, Params: d.params}
	}
	return nil
}

// sweep places the documents indexed while the rebuild ran into it, best
// effort, and returns how many it placed.
func (e *Engine) sweep(ctx context.Context, tenantID string) int {
	if e.cfg.Placer == nil {
		return 0
	}
	placed, err := e.cfg.Placer.Sweep(ctx, tenantID)
	switch {
	case err != nil && ctx.Err() != nil:
		e.log.Debug("interests: the placement sweep after the rebuild was cancelled", "tenant", tenantID, "err", err)
	case err != nil:
		e.log.Warn("interests: the placement sweep after the rebuild failed", "tenant", tenantID, "err", err)
	}
	return placed
}

// group runs the grouping steps: Group, from the prior's seeds when the
// plan is warm, then the merge of near-duplicates, the strays, and the
// carry-over of identities. It returns the prepared points too, for the
// map.
func (e *Engine) group(ctx context.Context, prior *previous, p plan, dvs []store.DocVector) (*grouped, []Point, error) {
	points, mean, err := PreparePoints(dvs, e.cfg.Center)
	if err != nil {
		return nil, nil, err
	}
	in := GroupInput{Points: points, Shape: Shape(p.shape), Split: p.split}
	if p.warm {
		in.Prior = prior.seeds()
	}
	g, err := e.grouper.Group(ctx, in)
	if err != nil {
		return nil, nil, fmt.Errorf("group: %w", err)
	}
	gr, err := newGrouped(points, mean, in, g, prior, p)
	return gr, points, err
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

// logRebuilt writes the rebuild's one INFO line; placed is how many
// documents the sweep after it placed.
func (e *Engine) logRebuilt(run *store.InterestRun, c store.RunCommit, gr *grouped, stats labelStats, t timings, placed int) {
	o, areas := c.Outcome, gr.areas.Counts
	args := []any{
		"tenant", run.TenantID, "run", run.ID, "trigger", run.Trigger, "kind", o.Kind, "split_check", o.SplitCheck,
		"shape", o.Shape, "documents", o.NumDocuments, "areas", o.NumAreas, "interests", o.NumInterests,
		"kept", o.Kept, "created", o.Created, "split", o.Split, "merged", o.Merged, "moved", o.Moved,
		"dissolved", o.Dissolved, "areas_kept", areas.Kept, "areas_created", areas.Created,
		"areas_dissolved", areas.Dissolved, "loose", o.NumLoose, "unsorted", o.NumUnsorted,
		"changed", o.ChangedDocuments, "read_ms", t.read.Milliseconds(), "group_ms", t.group.Milliseconds(),
		"label_ms", t.label.Milliseconds(), "labels_llm", stats.llm, "labels_terms", stats.terms,
		"persist_ms", t.persist.Milliseconds(), "placed_after", placed,
	}
	if m := o.Map; m != nil {
		args = append(args, "map", m.Status, "map_kind", m.Kind, "map_ms", m.Took.Milliseconds())
	} else {
		args = append(args, "map", MapOff)
	}
	if e.cfg.Drift != nil && e.cfg.Drift() != "" {
		args = append(args, "embeddings_drifted", true)
	}
	e.log.Info("interests rebuilt", args...)
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

// bookkeepingTimeout bounds recording a failed rebuild. It runs detached
// from the rebuild's context, which is often why it failed (daemon
// shutdown), so a run doesn't stay "running" forever.
const bookkeepingTimeout = 10 * time.Second

// abandonedMessage is the error of a rebuild a daemon left unfinished.
const abandonedMessage = "the daemon stopped during this rebuild (it crashed, was killed, or outran the shutdown grace)"

// recordFailure records a failed rebuild, failing its run (runID, "" when
// it failed before creating one) and pruning stale runs, which keeps the
// last good run's interests and this failure. A failure counts, and is
// warned about with its retry time, unless the rebuild was cancelled: a
// rebuild the daemon's shutdown cut short is requeued and runs again, so it
// failed nothing.
func (e *Engine) recordFailure(ctx context.Context, tenantID, runID string, numDocuments int, cause error) {
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()
	switch {
	case ctx.Err() != nil || errors.Is(cause, context.Canceled):
		e.log.Debug("interests: rebuild cancelled", "tenant", tenantID, "run", runID, "err", cause)
		if runID == "" {
			return
		}
		if err := e.insights.FailRun(bctx, runID, numDocuments, cause.Error()); err != nil {
			e.log.Warn("interests: mark the cancelled run failed", "run", runID, "err", err)
		}
	default:
		st, err := e.insights.RecordFailure(bctx, tenantID, runID, numDocuments, cause.Error())
		if err != nil {
			e.log.Warn("interests: record a failed rebuild", "tenant", tenantID, "run", runID, "err", err,
				"rebuild_err", cause)
		} else {
			e.log.Warn("interests: rebuild failed", "tenant", tenantID, "run", runID, "err", cause,
				"failures", st.Failures, "retry_at", RetryAt(st))
		}
		if runID == "" {
			return
		}
	}
	e.pruneStaleRuns(bctx, tenantID, runID)
}

// Abandoned records a rebuild of the tenant's that a daemon left
// unfinished (it crashed, was killed, or outran the shutdown grace): its
// running runs fail, and it counts as one failed rebuild, so the scheduler
// backs off rather than run what may have killed the daemon again at once.
func (e *Engine) Abandoned(ctx context.Context, tenantID string) error {
	st, err := e.insights.RecordAbandoned(ctx, tenantID, abandonedMessage)
	if err != nil {
		return fmt.Errorf("record the rebuild the daemon left unfinished: %w", err)
	}
	e.log.Warn("interests: rebuild failed", "tenant", tenantID, "err", abandonedMessage, "failures", st.Failures,
		"retry_at", RetryAt(st))
	return nil
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
