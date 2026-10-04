// Package layout draws the two views of a grouped library that the interest
// map shows, with the standard library alone.
//
// DocMap places every document as a point, so that documents near each other
// in the embedding space are near each other on the map: UMAP's fuzzy graph
// over each point's nearest neighbours, laid out by UMAP's stochastic gradient
// descent from the points' first two principal components, or from a previous
// map's positions.
//
// Zoom places the grouping itself: each interest a circle just large enough
// for its documents, packed by similarity inside its area's circle, the areas
// (or, in the flat shape, the interests) packed by similarity at the top, and
// one more circle for the documents in no interest. Every document is a dot on
// a hexagonal lattice inside its circle, turned toward the most similar
// interests, so no two dots overlap.
//
// Coordinates: both views fit a square of side Extent with one uniform scale,
// centred, every point and every circle's x±r and y±r inside [0, Extent], and
// every value rounded to 0.01 (a circle's radius up and a dot's down, so a
// dot inside a circle stays inside it). The longer side of a view spans fill
// of the Extent, except that a zoom view's dots are never wider than 1% of
// it, which keeps a tiny library small and centred, and that a document map
// aligned to a previous one keeps that map's frame while it lies inside the
// square and spans at least 90% of it, so the documents it shares stay put.
//
// Determinism: a layout depends only on its input, never on the input's order,
// map order, GOMAXPROCS or the clock. Documents are worked on in key order,
// groups in the order of their contents (size, then smallest document key),
// never by their keys: a new group's key is a fresh identity each time. Parallel
// steps use a fixed partition and sum their parts in order; the descent is one
// goroutine with one seeded generator. The same input gives the same output on
// one platform; arm64 fuses multiply-adds, so another architecture may differ
// in the last bits.
//
// Every function validates its input and returns an error rather than
// panicking, checks its context in every loop that can run long, and checks
// its output before returning it.
package layout

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

// Extent is the side of the square both views fit.
const Extent = 1000.0

// algorithmVersion changes when the code changes what it draws for the same
// constants.
const algorithmVersion = 1

// fill is the share of Extent a view's longer side spans.
const fill = 0.95

// XY is a point.
type XY struct{ X, Y float64 }

// Circle is a circle: its centre and radius.
type Circle struct{ X, Y, R float64 }

// Neighbour is one of a point's nearest neighbours: its index among the
// points and their cosine similarity.
type Neighbour struct {
	Index      int
	Similarity float64
}

// Params returns every constant that changes what the views draw, for the
// caller to record with a map; a change of any should make the next map
// start cold. Only the golden angle, a mathematical constant, the lattice's
// spacing, latticeGap's, and the slack a zoom view's check allows rounding
// are left out (TestParams_NameEveryConstant).
func Params() map[string]any {
	return map[string]any{
		"algorithm_version": algorithmVersion,
		"extent":            Extent,
		"fill":              fill,
		"reflection_margin": reflectionMargin,
		"doc_map": map[string]any{
			"neighbours":         mapNeighbours,
			"curve_a":            curveA,
			"curve_b":            curveB,
			"negative_rate":      negativeRate,
			"gradient_clip":      gradientClip,
			"cold_epochs":        coldEpochs,
			"cold_epochs_large":  coldEpochsLarge,
			"large_library":      largeLibrary,
			"cold_learning_rate": coldAlpha,
			"warm_epochs":        warmEpochs,
			"warm_learning_rate": warmAlpha,
			"warm_edge_length":   warmEdgeLength,
			"init":               "pca",
			"init_extent":        initExtent,
			"init_noise":         initNoise,
			"pull_in_quantile":   pullQuantile,
			"pull_in_softness":   pullSoftness,
			"min_aligned":        minAligned,
			"min_frame":          minFrame,
			"sigma_bisections":   sigmaBisections,
			"sigma_tolerance":    sigmaTolerance,
			"min_sigma_share":    minSigmaShare,
			"init_stream":        initStream,
			"descent_stream":     sgdStream,
		},
		"zoom": map[string]any{
			"lattice_gap":        latticeGap,
			"spare_slots":        spareSlots,
			"min_unsorted_slots": minUnsortedSlots,
			"interest_gap":       interestGap,
			"area_gap":           areaGap,
			"area_padding":       areaPadding,
			"unsorted_gap":       unsortedGap,
			"orienting_similar":  orientingSimilar,
			"max_dot_share":      maxDotShare,
			"start_neighbours":   startNeighbours,
			"min_distance":       minDistance,
			"max_fit_scale":      maxFitScale,
			"stress_iterations":  stressIterations,
			"stress_tolerance":   stressTolerance,
			"separation_sweeps":  separationSweeps,
			"separation_slack":   separationSlack,
			"overlap_tolerance":  overlapTolerance,
			"coincident":         coincident,
			"spread_factor":      spreadFactor,
			"compaction_rounds":  compactionRounds,
		},
		"pca": map[string]any{"block": pcaBlock, "max_iterations": pcaMaxIterations, "tolerance": pcaTolerance,
			"chunk": pcaChunk, "stream": pcaStream, "jacobi_sweeps": jacobiSweeps},
	}
}

// errInput wraps every refusal of an input.
var errInput = errors.New("layout: invalid input")

func inputError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInput, fmt.Sprintf(format, args...))
}

// checkVectors checks that the vectors are n, of one width above zero, and
// finite.
func checkVectors(vectors [][]float32, n int) error {
	if len(vectors) != n {
		return inputError("%d vectors for %d keys", len(vectors), n)
	}
	if n == 0 {
		return nil
	}
	dim := len(vectors[0])
	if dim == 0 {
		return inputError("vector 0 is empty")
	}
	for i, v := range vectors {
		if len(v) != dim {
			return inputError("vector %d has dim %d, want %d", i, len(v), dim)
		}
		for _, x := range v {
			if f := float64(x); math.IsNaN(f) || math.IsInf(f, 0) {
				return inputError("vector %d has a NaN or infinite component", i)
			}
		}
	}
	return nil
}

// keyOrder checks keys are unique and not empty, and returns the permutation
// that sorts them: order[k] is the index of the k-th key.
func keyOrder(keys []string) ([]int, error) {
	order := make([]int, len(keys))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		switch {
		case keys[a] < keys[b]:
			return -1
		case keys[a] > keys[b]:
			return 1
		}
		return 0
	})
	for k, i := range order {
		if keys[i] == "" {
			return nil, inputError("key %d is empty", i)
		}
		if k > 0 && keys[order[k-1]] == keys[i] {
			return nil, inputError("key %q appears twice", keys[i])
		}
	}
	return order, nil
}

// finite reports whether every value is a number.
func finite(xs ...float64) bool {
	for _, x := range xs {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
	}
	return true
}

// bbox is a bounding box.
type bbox struct{ x0, y0, x1, y1 float64 }

func emptyBox() bbox { return bbox{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)} }

func (b *bbox) add(x, y, r float64) {
	b.x0, b.y0 = math.Min(b.x0, x-r), math.Min(b.y0, y-r)
	b.x1, b.y1 = math.Max(b.x1, x+r), math.Max(b.y1, y+r)
}

func (b bbox) empty() bool { return b.x0 > b.x1 }

// fit is the uniform scale and translation that puts a box of content into
// the Extent square, centred, its longer side spanning fill of it, a scale
// no larger than maxScale (0 for none).
type fit struct{ scale, dx, dy float64 }

func fitBox(b bbox, maxScale float64) fit {
	if b.empty() {
		return fit{scale: 1, dx: Extent / 2, dy: Extent / 2}
	}
	side := math.Max(b.x1-b.x0, b.y1-b.y0)
	scale := 1.0
	if side > 0 {
		scale = fill * Extent / side
	}
	if maxScale > 0 {
		scale = math.Min(scale, maxScale)
	}
	cx, cy := (b.x0+b.x1)/2, (b.y0+b.y1)/2
	return fit{scale: scale, dx: Extent/2 - scale*cx, dy: Extent/2 - scale*cy}
}

func (f fit) point(p XY) XY { return XY{round(f.scale*p.X + f.dx), round(f.scale*p.Y + f.dy)} }

// circle is c on the map, its radius rounded up: with the dots' radius
// rounded down (dotRadius), a dot inside a circle stays inside it whatever
// rounding does to their centres.
func (f fit) circle(c Circle) Circle {
	return Circle{X: round(f.scale*c.X + f.dx), Y: round(f.scale*c.Y + f.dy), R: math.Ceil(f.scale*c.R*100) / 100}
}

// dotRadius is a dot's radius on the map, rounded down.
func (f fit) dotRadius() float64 { return math.Floor(f.scale*100) / 100 }

// round rounds to 0.01.
func round(x float64) float64 { return math.Round(x*100) / 100 }

// checkPoint fails unless p is finite and inside the square.
func checkPoint(what string, p XY) error {
	if !finite(p.X, p.Y) || p.X < 0 || p.X > Extent || p.Y < 0 || p.Y > Extent {
		return fmt.Errorf("layout: %s at (%g, %g) is outside the map", what, p.X, p.Y)
	}
	return nil
}

// checkCircle fails unless c has a positive radius and lies inside the
// square.
func checkCircle(what string, c Circle) error {
	if !finite(c.X, c.Y, c.R) || c.R <= 0 || c.X-c.R < 0 || c.X+c.R > Extent || c.Y-c.R < 0 || c.Y+c.R > Extent {
		return fmt.Errorf("layout: %s (%g, %g) radius %g is outside the map", what, c.X, c.Y, c.R)
	}
	return nil
}

func dist(a, b XY) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }
