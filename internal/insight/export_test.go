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
