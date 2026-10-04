package insight

import (
	"context"
	"time"
)

// Hooks for the package insight_test tests, which measure groupings with
// package quality (which imports this package).

// NeighbourLists is a grouping's nearest-neighbour pass.
type NeighbourLists = neighbourLists

// NearestNeighbours is the pass LouvainGrouper makes.
var NearestNeighbours = nearestNeighbours

// WithNeighbours returns a copy of lg that makes its nearest-neighbour pass
// with f, to count the passes or reuse one across groupings of a document
// set.
func (lg *LouvainGrouper) WithNeighbours(f func(ctx context.Context, vecs [][]float32) (NeighbourLists, error)) *LouvainGrouper {
	c := *lg
	c.neighbours = f
	return &c
}

// WithClock returns e reading the time its runs read the vectors from now,
// so a test sets the times documents are indexed at against it.
func (e *Engine) WithClock(now func() time.Time) *Engine {
	e.now = now
	return e
}

// WithMapper returns e drawing its runs' maps with f in place of BuildMap.
func (e *Engine) WithMapper(f func(ctx context.Context, in MapInput) (*Map, error)) *Engine {
	e.buildMap = f
	return e
}

// WithMapTimeout returns e giving a map d to draw in place of mapTimeout.
func (e *Engine) WithMapTimeout(d time.Duration) *Engine {
	e.mapTimeout = d
	return e
}

// WithNeighbourBudget returns p giving a sweep's neighbour searches d in
// place of sweepNeighbourBudget.
func (p *Placer) WithNeighbourBudget(d time.Duration) *Placer {
	p.neighbourBudget = d
	return p
}
