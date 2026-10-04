package insight

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"runtime/debug"
	"slices"
	"strings"
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
	// sweepBatch is how many placements a sweep writes at once, each batch
	// in a transaction of its own: about 2.8 s of neighbour searches at the
	// owner's 44 ms each, against a write of a few milliseconds, so the
	// write lock is never held across a search, and a sweep cut short
	// keeps all but its last batch.
	sweepBatch = 64
)

// errNoVectors is a document with no chunk vector: nothing to place.
var errNoVectors = errors.New("the document has no vectors")

// A placed document's place on a built map: near its most similar mapped
// documents, found by a vector search of its chunks' (VectorSearch, 40 ms
// on the owner's library, where the rest of a placement takes 2 ms).
const (
	// neighbourChunks is how many chunks the search reads: about 24
	// documents on the owner's library.
	neighbourChunks = 50
	// placeAnchors is how many of the most similar mapped documents a
	// place blends, on each view.
	placeAnchors = 5
	// placeTemperature: a neighbour's weight falls by e for every
	// placeTemperature its nearness (minus half its squared distance to the
	// document's vector) is below the nearest's, so the nearest dominates
	// and a document identical to a mapped one sits on it.
	placeTemperature = 0.02
	// placeOffset is how far, as a share of the map, a placed dot sits from
	// where its neighbours put it, at its ID's hash angle, so it never sits
	// exactly on another.
	placeOffset = 0.004
	// sweepNeighbourBudget bounds the time a sweep spends searching: past
	// it, the documents left take the fallback place, and the placement
	// itself never waits on it.
	sweepNeighbourBudget = 60 * time.Second
	// neighbourTimeout bounds one placement's search and read of its
	// neighbours: a search that stalls takes the fallback place and leaves
	// the rest of placeTimeout to the placement's write.
	neighbourTimeout = 2 * time.Second
)

// Placer puts documents indexed between rebuilds into the tenant's
// current grouping, the latest done run: each into its nearest interest
// when the cosine to its centroid reaches LooseFitThreshold, into Unsorted
// otherwise, in the space the run grouped in (RunSpace), with a place on
// the run's map when it is built and the map is on. The run's space is
// cached until a newer run is done. Placement is held while the
// embeddings drifted or a re-embedding's fresh rebuild is owed: the run's
// centroids came from vectors the library no longer holds. It is safe for
// concurrent use.
type Placer struct {
	insights store.InsightStore
	chunks   store.ChunkStore
	drift    func() string
	log      *slog.Logger
	mapOff   bool
	// neighbourBudget is a sweep's sweepNeighbourBudget, and
	// neighbourTimeout a placement's.
	neighbourBudget  time.Duration
	neighbourTimeout time.Duration
	batch            int // a sweep's sweepBatch

	mu        sync.Mutex
	cached    *runPlacement // the latest run's space, nil before the first placement
	failures  runWarnings   // placements that failed
	fallbacks runWarnings   // places that fell back for a failure
}

// runWarnings tells the first of a kind of failure in a run, to warn about,
// from the rest, logged at debug. Its owner's mutex guards it.
type runWarnings struct {
	run    string // the run the last was in, "" for one before a run was read
	warned bool
}

// first notes a failure in runID and reports whether it is the run's first.
func (w *runWarnings) first(runID string) bool {
	first := w.run != runID || !w.warned
	w.run, w.warned = runID, true
	return first
}

// PlacerOptions are what a Placer works with besides its stores.
type PlacerOptions struct {
	// Drift says how the embeddings drifted, "" while they haven't: it
	// holds placement. nil reports no drift.
	Drift func() string
	// MapOff is insight.map: false. A placement then gets no place on the
	// map, even on a run whose map is built, and searches for no
	// neighbours; the sweep repairs it once the map is on again.
	MapOff bool
	Log    *slog.Logger // default slog.Default()
}

// NewPlacer returns a Placer over the stores.
func NewPlacer(insights store.InsightStore, chunks store.ChunkStore, opts PlacerOptions) *Placer {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Placer{insights: insights, chunks: chunks, drift: opts.Drift, log: log, mapOff: opts.MapOff,
		neighbourBudget: sweepNeighbourBudget, neighbourTimeout: neighbourTimeout, batch: sweepBatch}
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
		p.position(ctx, tenantID, rp, &pl, true)
		// The run may have been replaced since it was read: the write
		// checks.
		_, err = p.insights.PlaceDocument(ctx, tenantID, pl.Placement)
		return err
	})
	return runID, err
}

// Sweep places, under the same holds as Place, every fetched document
// indexed since the current run read its vectors that the run neither
// assigned nor placed: those indexed while a rebuild ran, and any whose
// placement failed. On a built map it places again, with a place, those
// placed off it (InsightStore.Unplaced): with no place, or outside their
// circle, as placing with the map off and 2.5.x leave them; with the map
// off, it can't, and keeps them. A document placed meanwhile on the map
// keeps that placement. A document whose vectors can't be placed (another
// width, a non-finite value) is left out with a warning, so one can't
// stall the rest. Searching for neighbours on the map stops once it has
// taken the sweep's neighbour budget: the documents left take the
// fallback place. It writes as it goes, sweepBatch placements at a time,
// and returns how many it placed: on any other failure, a panic or its
// context ending included, those with the error, the rest left for the
// next sweep.
func (p *Placer) Sweep(ctx context.Context, tenantID string) (placed int, err error) {
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	err = recovered(func() error { return p.sweep(ctx, tenantID, &placed) })
	return placed, err
}

// sweep is Sweep, counting what it writes in placed as it goes.
func (p *Placer) sweep(ctx context.Context, tenantID string, placed *int) error {
	run, ok, err := p.target(ctx, tenantID)
	if err != nil || !ok {
		return err
	}
	since := run.StartedAt
	if run.VectorsReadAt != nil {
		since = *run.VectorsReadAt
	}
	ids, err := p.insights.Unplaced(ctx, tenantID, run.ID, since)
	if err != nil || len(ids) == 0 {
		return err
	}
	rp, err := p.space(ctx, run)
	if err != nil {
		return err
	}
	batch := make([]store.Placement, 0, min(len(ids), p.batch))
	write := func() error {
		n, err := p.insights.PlaceMany(ctx, tenantID, run.ID, batch)
		*placed += n
		batch = batch[:0]
		return err
	}
	var skipped []string
	var skipErr error
	defer func() {
		if len(skipped) > 0 {
			p.log.Warn("interests: the sweep left out documents it can't place", "run", run.ID, "count", len(skipped),
				"document_ids", skipped[:min(len(skipped), maxLoggedIDs)], "err", skipErr)
		}
	}()
	var searched time.Duration
	for _, id := range ids {
		vec, err := p.vector(ctx, id)
		switch {
		case errors.Is(err, errNoVectors):
			continue
		case err != nil:
			return err
		}
		pl, err := rp.place(id, vec)
		if err != nil {
			skipped, skipErr = append(skipped, id), cmp.Or(skipErr, err)
			continue
		}
		start := time.Now()
		p.position(ctx, tenantID, rp, &pl, searched < p.neighbourBudget)
		searched += time.Since(start)
		if batch = append(batch, pl.Placement); len(batch) == p.batch {
			if err := write(); err != nil {
				return err
			}
		}
	}
	return write()
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
	rp, err := newRunPlacement(run, groups, !p.mapOff)
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
	p.logOnce(&p.failures, runID, "interests: placement failed", args...)
}

// logOnce logs msg about runID: a warning for the first of w's kind in the
// run, debug for the rest.
func (p *Placer) logOnce(w *runWarnings, runID, msg string, args ...any) {
	p.mu.Lock()
	first := w.first(runID)
	p.mu.Unlock()
	level := slog.LevelDebug
	if first {
		level = slog.LevelWarn
	}
	p.log.Log(context.Background(), level, msg, append([]any{"run", runID}, args...)...)
}

// runPlacement is a run's space as the Placer caches it: the interests'
// centroids, by interest ID, around the run's mean; and, when the run's
// map is built and placements go on it, each interest's circle and
// anchor, Unsorted's disc and the dots' radius.
type runPlacement struct {
	runID     string
	space     *RunSpace
	interests []string // a centroid's interest, by index
	mapped    bool
	places    map[string]store.GroupMap // by interest ID
	unsorted  store.Circle
	dotRadius float64
}

// newRunPlacement is run's space, with its groups; with mapped, its map
// too, when it is built.
func newRunPlacement(run *store.InterestRun, groups []store.InterestGroup, mapped bool) (*runPlacement, error) {
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
	if m := run.Map; mapped && m != nil && m.Status == store.MapBuilt {
		rp.mapped, rp.unsorted, rp.dotRadius = true, m.Unsorted, m.DotRadius
		rp.places = make(map[string]store.GroupMap, len(interests))
		for _, g := range interests {
			if g.Map == nil {
				return nil, fmt.Errorf("interest %s has no place on the run's built map", g.ID)
			}
			rp.places[g.ID] = *g.Map
		}
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

// placed is a placement and what its place on the map starts from: the
// document's vector and its nearest interest, "" for none.
type placed struct {
	store.Placement
	vec     []float32
	nearest string
}

// place is where vec, documentID's vector, goes: an interest it joins, or
// Unsorted with its cosine to the nearest.
func (rp *runPlacement) place(documentID string, vec []float32) (placed, error) {
	pl, err := rp.space.Place(vec)
	if err != nil {
		return placed{}, fmt.Errorf("document %s: %w", documentID, err)
	}
	out := placed{Placement: store.Placement{RunID: rp.runID, DocumentID: documentID, Similarity: pl.Similarity},
		vec: vec}
	if pl.Interest >= 0 {
		out.nearest = rp.interests[pl.Interest]
	}
	if pl.Joined {
		out.InterestID = out.nearest
	}
	return out, nil
}

// position gives pl its place on the run's map, when the map is built and
// placements go on it (runPlacement.mapped): near its most similar mapped
// documents when search is set and finds some within neighbourTimeout,
// else the fallback place. A failed search falls back: a warning for the
// run's first, debug after, and debug alone when ctx ended, which the
// placement reports itself.
func (p *Placer) position(ctx context.Context, tenantID string, rp *runPlacement, pl *placed, search bool) {
	if !rp.mapped {
		return
	}
	var near []mapNeighbour
	if search {
		err := recovered(func() error {
			sctx, cancel := context.WithTimeout(ctx, p.neighbourTimeout)
			defer cancel()
			var err error
			near, err = p.neighbours(sctx, tenantID, rp.runID, pl)
			return err
		})
		const msg = "interests: a placement's place on the map fell back"
		switch {
		case err == nil:
		case ctx.Err() != nil:
			p.log.Debug(msg, "run", rp.runID, "document", pl.DocumentID, "err", err)
		default:
			p.logOnce(&p.fallbacks, rp.runID, msg, "document", pl.DocumentID, "err", err)
		}
	}
	pl.Map = rp.position(pl, near)
}

// mapNeighbour is a mapped document near a placed one: its nearness (minus
// half its squared distance to the placed document's vector) and its place.
type mapNeighbour struct {
	nearness float64
	place    store.MapPlace
}

// neighbours are the mapped documents nearest pl's vector, nearest first,
// ties to the lower ID: its vector search's documents, each at its nearest
// chunk, that have a place on the run's map.
func (p *Placer) neighbours(ctx context.Context, tenantID, runID string, pl *placed) ([]mapNeighbour, error) {
	hits, err := p.chunks.VectorSearch(ctx, tenantID, pl.vec, neighbourChunks,
		store.SearchFilters{ExcludeDocumentID: pl.DocumentID})
	if err != nil {
		return nil, fmt.Errorf("search the document's neighbours: %w", err)
	}
	nearness := map[string]float64{}
	var ids []string
	for _, h := range hits {
		if h.Score <= 0 {
			continue
		}
		d := 1/h.Score - 1 // VectorSearch scores a distance d as 1/(1+d)
		if prev, ok := nearness[h.DocumentID]; !ok {
			ids = append(ids, h.DocumentID)
			nearness[h.DocumentID] = -d * d / 2
		} else {
			nearness[h.DocumentID] = math.Max(prev, -d*d/2)
		}
	}
	places, err := p.insights.MapPositions(ctx, runID, ids)
	if err != nil {
		return nil, fmt.Errorf("read the neighbours' places: %w", err)
	}
	out := make([]mapNeighbour, len(places))
	for i, place := range places {
		out[i] = mapNeighbour{nearness: nearness[place.DocumentID], place: place}
	}
	slices.SortFunc(out, func(a, b mapNeighbour) int {
		return cmp.Or(cmp.Compare(b.nearness, a.nearness), strings.Compare(a.place.DocumentID, b.place.DocumentID))
	})
	return out, nil
}

// position is pl's place on the map. On the document map: the weighted
// mean of its placeAnchors nearest neighbours' places, else the anchor of
// the interest it joined (its nearest, in Unsorted; the map's centre with
// none). In the zoom view: the weighted mean of its nearest neighbours in
// its own circle (its interest's, or Unsorted's disc), else a point of its
// own inside the circle: halfway out at its hash angle in an interest's,
// seven tenths out toward its nearest interest in Unsorted's. Each is
// moved placeOffset (a dot, in the zoom view) along its hash angle so it
// never sits exactly on another dot, kept inside its circle and the map,
// and rounded as the layouts round.
func (rp *runPlacement) position(pl *placed, near []mapNeighbour) *store.MapPosition {
	ux, uy := hashDirection(pl.DocumentID)
	var mapX, mapY float64
	if x, y, ok := blend(near, func(store.MapPlace) bool { return true }, func(p store.MapPosition) (float64, float64) {
		return p.MapX, p.MapY
	}); ok {
		mapX, mapY = x, y
	} else if g, ok := rp.places[cmp.Or(pl.InterestID, pl.nearest)]; ok {
		mapX, mapY = g.AnchorX, g.AnchorY
	} else {
		mapX, mapY = store.MapExtent/2, store.MapExtent/2
	}
	shift := placeOffset * store.MapExtent
	mapX, mapY = clampToMap(mapX+shift*ux), clampToMap(mapY+shift*uy)

	circle := rp.unsorted
	if g, ok := rp.places[pl.InterestID]; ok && pl.InterestID != "" {
		circle = store.Circle{X: g.ZoomX, Y: g.ZoomY, R: g.ZoomR}
	}
	room := math.Max(circle.R-rp.dotRadius-0.01, 0) // a whole dot inside, whatever rounding does
	sameCircle := func(m store.MapPlace) bool { return m.InterestID == pl.InterestID }
	zoomX, zoomY, ok := blend(near, sameCircle, func(p store.MapPosition) (float64, float64) { return p.ZoomX, p.ZoomY })
	switch {
	case ok:
		zoomX, zoomY = zoomX+rp.dotRadius*ux, zoomY+rp.dotRadius*uy
	case pl.InterestID != "":
		zoomX, zoomY = circle.X+room/2*ux, circle.Y+room/2*uy
	default:
		dx, dy := ux, uy
		if g, ok := rp.places[pl.nearest]; ok && (g.ZoomX != circle.X || g.ZoomY != circle.Y) {
			d := math.Hypot(g.ZoomX-circle.X, g.ZoomY-circle.Y)
			dx, dy = (g.ZoomX-circle.X)/d, (g.ZoomY-circle.Y)/d
		}
		zoomX, zoomY = circle.X+0.7*room*dx, circle.Y+0.7*room*dy
	}
	if d := math.Hypot(zoomX-circle.X, zoomY-circle.Y); d > room {
		zoomX, zoomY = circle.X+(zoomX-circle.X)*room/d, circle.Y+(zoomY-circle.Y)*room/d
	}
	return &store.MapPosition{MapX: roundPlace(mapX), MapY: roundPlace(mapY), ZoomX: roundPlace(clampToMap(zoomX)),
		ZoomY: roundPlace(clampToMap(zoomY))}
}

// blend is the weighted mean, by at, of the places of the placeAnchors
// nearest of near that keep holds; ok is false for none. The nearest
// weighs 1, and each other e^((nearness − nearest's)/placeTemperature).
func blend(near []mapNeighbour, keep func(store.MapPlace) bool, at func(store.MapPosition) (float64, float64)) (x, y float64, ok bool) {
	var sum, best float64
	used := 0
	for _, n := range near {
		if !keep(n.place) {
			continue
		}
		if used == 0 {
			best = n.nearness
		}
		w := math.Exp((n.nearness - best) / placeTemperature)
		px, py := at(n.place.Map)
		x, y, sum = x+w*px, y+w*py, sum+w
		if used++; used == placeAnchors {
			break
		}
	}
	if used == 0 {
		return 0, 0, false
	}
	return x / sum, y / sum, true
}

// hashDirection is the unit vector at the angle of id's FNV-1a hash.
func hashDirection(id string) (float64, float64) {
	h := fnv.New64a()
	h.Write([]byte(id)) // a hash.Hash's Write never fails
	a := 2 * math.Pi * float64(h.Sum64()>>11) / (1 << 53)
	return math.Cos(a), math.Sin(a)
}

func clampToMap(v float64) float64 { return math.Max(0, math.Min(store.MapExtent, v)) }

// roundPlace rounds to 0.01, as the layouts do.
func roundPlace(v float64) float64 { return math.Round(v*100) / 100 }

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
