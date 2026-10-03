package insight

import (
	"errors"
	"fmt"
	"math"
)

// RunSpace is a grouping's space as a run stores it: the mean its documents
// were centered on (empty when they weren't) and its interests' centroids
// (Centroids). Place puts a document indexed since into it the way
// AssignStrays judges a stray.
type RunSpace struct {
	mean      []float64 // nil when the run wasn't centered
	centroids [][]float32
	dim       int // 0 when neither the mean nor a centroid fixes it
}

// NewRunSpace checks that the mean and centroids share one width above
// zero and are finite. An empty mean, nil or not (as an empty stored value
// may decode), is a run that wasn't centered. The RunSpace keeps both, so
// the caller must not change them afterwards.
func NewRunSpace(mean []float64, centroids [][]float32) (*RunSpace, error) {
	if len(mean) == 0 {
		mean = nil
	}
	for _, m := range mean {
		if math.IsNaN(m) || math.IsInf(m, 0) {
			return nil, errors.New("insight: the mean has a NaN or infinite component")
		}
	}
	dim := len(mean)
	if dim == 0 && len(centroids) > 0 {
		if dim = len(centroids[0]); dim == 0 {
			return nil, errors.New("insight: centroid 0 has no components")
		}
	}
	if err := checkCentroids(centroids, dim); err != nil {
		return nil, err
	}
	return &RunSpace{mean: mean, centroids: centroids, dim: dim}, nil
}

// Placement is where Place put a document.
type Placement struct {
	// Interest is the nearest interest, ties to the lower label; -1 when
	// the run has none.
	Interest int
	// Similarity is the cosine to Interest's centroid; 0 without one.
	Similarity float64
	// Joined reports Similarity at LooseFitThreshold or above: the
	// document joins Interest until the next grouping, or else waits in
	// Unsorted.
	Joined bool
}

// Place centers vec, a document's mean vector, on the run's mean,
// normalizes it as PreparePoints does, and finds its nearest interest. A
// vector of another width or with a NaN or infinite component is an error;
// a zero residual comes back unjoined with similarity 0.
func (s *RunSpace) Place(vec []float32) (Placement, error) {
	if len(vec) == 0 || (s.dim > 0 && len(vec) != s.dim) {
		return Placement{}, fmt.Errorf("insight: a %d-dimensional vector for a %d-dimensional run", len(vec), s.dim)
	}
	for _, x := range vec {
		if f := float64(x); math.IsNaN(f) || math.IsInf(f, 0) {
			return Placement{}, errors.New("insight: the vector has a NaN or infinite component")
		}
	}
	best, sim := nearestCentroid(unitResidual(vec, s.mean), s.centroids)
	return Placement{Interest: best, Similarity: sim, Joined: best >= 0 && sim >= LooseFitThreshold}, nil
}
