package insight

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/samsar/curio/internal/store"
)

// Placement's time bounds.
const (
	// placeTimeout bounds placing one document: a few reads and a write,
	// in milliseconds when the database isn't busy.
	placeTimeout = 10 * time.Second
	// sweepTimeout bounds a sweep, which reads each unplaced document's
	// vectors: a few thousand documents indexed while a rebuild ran fit
	// in it.
	sweepTimeout = 2 * time.Minute
)

// errNoVectors is a document with no chunk vector: nothing to place.
var errNoVectors = errors.New("the document has no vectors")

// Placer puts documents indexed between rebuilds into the tenant's
// current grouping, the latest done run: each into its nearest interest
// when the cosine to its centroid reaches LooseFitThreshold, into Unsorted
// otherwise, in the space the run grouped in (RunSpace). The run's space
// is cached until a newer run is done. Placement is held while the
// embeddings drifted or a re-embedding's fresh rebuild is owed: the run's
// centroids came from vectors the library no longer holds. It is safe for
// concurrent use.
type Placer struct {
	insights store.InsightStore
	chunks   store.ChunkStore
	drift    func() string
	log      *slog.Logger

	mu     sync.Mutex
	cached *runPlacement // the latest run's space, nil before the first placement
	warned bool          // a failure was warned about, for the run failed names
	failed string        // that run, "" for a failure before one was read
}

// NewPlacer returns a Placer over the stores; drift says how the
// embeddings drifted, "" while they haven't (nil reports no drift).
func NewPlacer(insights store.InsightStore, chunks store.ChunkStore, drift func() string, log *slog.Logger) *Placer {
	if log == nil {
		log = slog.Default()
	}
	return &Placer{insights: insights, chunks: chunks, drift: drift, log: log}
}

// Place places a document just indexed into the tenant's current
// grouping, unless placement is held, there is no done run, or the run
// assigned the document (a refetch or reindex of one it grouped keeps its
// assignment until the next rebuild). A document placed already is placed
// anew. It is best effort and never fails its caller: a failure, a panic
// included, is a warning once a run and a debug line after, and it gives
// up after placeTimeout. A document without vectors, or a cancelled
// context, writes nothing and logs at debug.
func (p *Placer) Place(ctx context.Context, tenantID, documentID string) {
	pctx, cancel := context.WithTimeout(ctx, placeTimeout)
	defer cancel()
	runID, err := p.place(pctx, tenantID, documentID)
	switch {
	case err == nil:
	case errors.Is(err, errNoVectors):
		p.log.Debug("interests: nothing to place", "document", documentID, "err", err)
	case ctx.Err() != nil:
		p.log.Debug("interests: placement cancelled", "document", documentID, "err", err)
	default:
		p.failure(runID, "document", documentID, "err", err)
	}
}

// place is Place, returning the run it placed into, or would have, and
// why it didn't.
func (p *Placer) place(ctx context.Context, tenantID, documentID string) (runID string, err error) {
	err = recovered(func() error {
		run, ok, err := p.target(ctx, tenantID)
		if err != nil || !ok {
			return err
		}
		runID = run.ID
		if assigned, err := p.insights.Assigned(ctx, run.ID, documentID); err != nil || assigned {
			return err
		}
		rp, err := p.space(ctx, run)
		if err != nil {
			return err
		}
		vec, err := p.vector(ctx, documentID)
		if err != nil {
			return err
		}
		pl, err := rp.place(documentID, vec)
		if err != nil {
			return err
		}
		// The run may have been replaced since it was read: the write
		// checks.
		_, err = p.insights.PlaceDocument(ctx, tenantID, pl)
		return err
	})
	return runID, err
}

// Sweep places, under the same holds as Place, every fetched document
// indexed since the current run read its vectors that the run neither
// assigned nor placed: those indexed while a rebuild ran, and any whose
// placement failed. A document placed meanwhile keeps that placement. It
// returns how many it placed. A document whose vectors can't be placed
// (another width, a non-finite value) is left out with a warning, so one
// can't stall the rest; any other failure, a panic included, is its error.
func (p *Placer) Sweep(ctx context.Context, tenantID string) (placed int, err error) {
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	err = recovered(func() error {
		placed, err = p.sweep(ctx, tenantID)
		return err
	})
	return placed, err
}

func (p *Placer) sweep(ctx context.Context, tenantID string) (int, error) {
	run, ok, err := p.target(ctx, tenantID)
	if err != nil || !ok {
		return 0, err
	}
	since := run.StartedAt
	if run.VectorsReadAt != nil {
		since = *run.VectorsReadAt
	}
	ids, err := p.insights.Unplaced(ctx, tenantID, run.ID, since)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	rp, err := p.space(ctx, run)
	if err != nil {
		return 0, err
	}
	ps := make([]store.Placement, 0, len(ids))
	var skipped []string
	var skipErr error
	for _, id := range ids {
		vec, err := p.vector(ctx, id)
		switch {
		case errors.Is(err, errNoVectors):
			continue
		case err != nil:
			return 0, err
		}
		pl, err := rp.place(id, vec)
		if err != nil {
			skipped, skipErr = append(skipped, id), cmp.Or(skipErr, err)
			continue
		}
		ps = append(ps, pl)
	}
	if len(skipped) > 0 {
		p.log.Warn("interests: the sweep left out documents it can't place", "run", run.ID, "count", len(skipped),
			"document_ids", skipped[:min(len(skipped), maxLoggedIDs)], "err", skipErr)
	}
	return p.insights.PlaceMany(ctx, tenantID, run.ID, ps)
}

// target is the run to place into: the tenant's latest done run, unless
// placement is held (ok false).
func (p *Placer) target(ctx context.Context, tenantID string) (run *store.InterestRun, ok bool, err error) {
	if p.drift != nil && p.drift() != "" {
		return nil, false, nil
	}
	run, err = p.insights.LatestRun(ctx, tenantID, store.InterestRunDone)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("read the latest done run: %w", err)
	}
	st, err := p.insights.State(ctx, tenantID)
	if err != nil {
		return nil, false, err
	}
	return run, st.FreshOwed != store.FreshReindex, nil
}

// space is run's space, read once per run.
func (p *Placer) space(ctx context.Context, run *store.InterestRun) (*runPlacement, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != nil && p.cached.runID == run.ID {
		return p.cached, nil
	}
	groups, err := p.insights.RunGroups(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	rp, err := newRunPlacement(run, groups)
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", run.ID, err)
	}
	p.cached = rp
	return rp, nil
}

// vector is a document's mean chunk vector, the one DocumentVectors gives
// a rebuild; errNoVectors without one.
func (p *Placer) vector(ctx context.Context, documentID string) ([]float32, error) {
	embs, err := p.chunks.EmbeddingsForDocument(ctx, documentID)
	if err != nil {
		return nil, err
	}
	var mean store.MeanVector
	for _, e := range embs {
		if err := mean.Add(e.Embedding); err != nil {
			return nil, fmt.Errorf("document %s: chunk %s: %w", documentID, e.ChunkID, err)
		}
	}
	vec := mean.Mean()
	if vec == nil {
		return nil, errNoVectors
	}
	return vec, nil
}

// failure logs a placement that failed into runID: a warning for the
// first of a run, debug for the rest, so a broken store doesn't log a
// warning per document indexed.
func (p *Placer) failure(runID string, args ...any) {
	p.mu.Lock()
	first := p.failed != runID || !p.warned
	p.failed, p.warned = runID, true
	p.mu.Unlock()
	level := slog.LevelDebug
	if first {
		level = slog.LevelWarn
	}
	p.log.Log(context.Background(), level, "interests: placement failed", append([]any{"run", runID}, args...)...)
}

// runPlacement is a run's space as the Placer caches it: the interests'
// centroids, by interest ID, around the run's mean.
type runPlacement struct {
	runID     string
	space     *RunSpace
	interests []string // a centroid's interest, by index
}

func newRunPlacement(run *store.InterestRun, groups []store.InterestGroup) (*runPlacement, error) {
	interests := slices.DeleteFunc(slices.Clone(groups), func(g store.InterestGroup) bool {
		return g.Level != store.InterestLevelInterest
	})
	// By ID, so a tie between two centroids goes the same way every time.
	slices.SortFunc(interests, func(a, b store.InterestGroup) int { return cmp.Compare(a.ID, b.ID) })
	rp := &runPlacement{runID: run.ID, interests: make([]string, len(interests))}
	centroids := make([][]float32, len(interests))
	for i, g := range interests {
		rp.interests[i], centroids[i] = g.ID, g.Centroid
	}
	mean := make([]float64, len(run.Mean))
	for i, x := range run.Mean {
		mean[i] = float64(x)
	}
	space, err := NewRunSpace(mean, centroids)
	if err != nil {
		return nil, err
	}
	rp.space = space
	return rp, nil
}

// place is where vec, documentID's vector, goes: an interest it joins, or
// Unsorted with its cosine to the nearest.
func (rp *runPlacement) place(documentID string, vec []float32) (store.Placement, error) {
	pl, err := rp.space.Place(vec)
	if err != nil {
		return store.Placement{}, fmt.Errorf("document %s: %w", documentID, err)
	}
	out := store.Placement{RunID: rp.runID, DocumentID: documentID, Similarity: pl.Similarity}
	if pl.Joined {
		out.InterestID = rp.interests[pl.Interest]
	}
	return out, nil
}

// recovered runs fn and returns its error, a panic turned into one, with
// the stack: placement runs in the index worker, whose recovery would fail
// the job, and at the daemon's start, where nothing recovers.
func recovered(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return fn()
}
