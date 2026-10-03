package insight

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
)

// MergeThreshold is the centroid cosine at or above which two interests of
// one scope (an area, or the whole grouping in ShapeFlat) read as one
// interest cut in two: the median cosine between the two halves of an
// interest cut at random, on the owner's library.
const MergeThreshold = 0.85

// LooseFitThreshold is the cosine to its nearest interest's centroid at or
// above which a point in no interest is a loose fit of that interest.
const LooseFitThreshold = 0.45

// Centroids returns each interest's centroid, interests being the labels
// 0..k-1 of labels (NoiseLabel for none, k at most the number of points):
// the unit mean of its members' vectors, summed in float64 and returned as
// float32, the form a run stores. An unused label, or a zero mean, gets a
// zero centroid.
func Centroids(points []Point, labels []int) ([][]float32, error) {
	if len(labels) != len(points) {
		return nil, fmt.Errorf("insight: %d labels for %d points", len(labels), len(points))
	}
	if err := checkLabels("interest", labels); err != nil {
		return nil, err
	}
	k := numLabels(labels)
	if k == 0 {
		return [][]float32{}, nil
	}
	dim := len(points[0].Vector)
	sums := make([][]float64, k)
	for i, l := range labels {
		if l == NoiseLabel {
			continue
		}
		if len(points[i].Vector) != dim {
			return nil, fmt.Errorf("insight: point %s has dim %d, want %d", points[i].ID, len(points[i].Vector), dim)
		}
		if sums[l] == nil {
			sums[l] = make([]float64, dim)
		}
		for d, v := range points[i].Vector {
			sums[l][d] += float64(v)
		}
	}
	out := make([][]float32, k)
	for l, sum := range sums {
		out[l] = make([]float32, dim)
		if sum == nil {
			continue
		}
		var sq float64
		for _, v := range sum {
			sq += v * v
		}
		if sq == 0 {
			continue
		}
		inv := 1 / math.Sqrt(sq)
		for d, v := range sum {
			out[l][d] = float32(v * inv)
		}
	}
	return out, nil
}

// MergeNearDuplicates joins the interests of one scope (an area in
// ShapeAreas, the whole grouping in ShapeFlat) whose member centroids reach
// threshold, transitively, and repeats with the joined interests' centroids
// until a round joins nothing: joining two interests moves their centroid,
// which can bring a third within reach. It returns g with its interests
// renumbered as a Grouping numbers them, and how many interests were merged
// away. Area and Seeds are left as they were, so a warm start from the seeds
// re-derives the merge and an unchanged library stays identical. Neither
// input is modified.
func MergeNearDuplicates(points []Point, g Grouping, threshold float64) (Grouping, int, error) {
	if err := checkStep(points, g, threshold); err != nil {
		return Grouping{}, 0, err
	}
	interest := slices.Clone(g.Interest)
	before := numLabels(interest)
	k := before
	for k > 1 {
		cents, err := Centroids(points, interest)
		if err != nil {
			return Grouping{}, 0, err
		}
		root := mergeRound(cents, scopesOf(g, interest, k), threshold)
		if root == nil {
			break
		}
		for i, l := range interest {
			if l != NoiseLabel {
				interest[i] = root[l]
			}
		}
		interest = numberInterests(interest, g.Area, idsOf(points))
		k = numLabels(interest)
	}
	out := Grouping{
		Shape: g.Shape, Area: slices.Clone(g.Area), Interest: interest,
		Seeds: slices.Clone(g.Seeds), Splits: g.Splits,
	}
	return out, before - k, nil
}

// checkStep validates what the steps after a grouping take: a valid
// grouping of the points, unit or zero vectors, and a finite threshold (a
// NaN would compare false with every cosine, a NaN vector likewise).
func checkStep(points []Point, g Grouping, threshold float64) error {
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return fmt.Errorf("insight: threshold %g, want a finite value", threshold)
	}
	if err := g.Validate(len(points)); err != nil {
		return err
	}
	if len(points) == 0 {
		return nil
	}
	return checkUnitVectors(points)
}

// numLabels is k for labels numbered 0..k-1, NoiseLabel for none.
func numLabels(labels []int) int {
	k := 0
	for _, l := range labels {
		k = max(k, l+1)
	}
	return k
}

// scopesOf returns each interest's scope: its area in ShapeAreas, 0 in
// ShapeFlat.
func scopesOf(g Grouping, interest []int, k int) []int {
	scope := make([]int, k)
	if g.Shape == ShapeAreas {
		for i, l := range interest {
			if l != NoiseLabel {
				scope[l] = g.Area[i]
			}
		}
	}
	return scope
}

// mergeRound joins, with union-find, every pair of interests of one scope
// whose centroids reach threshold, the lowest label of each joined set
// naming it, and returns each interest's set; nil when nothing joined.
func mergeRound(cents [][]float32, scope []int, threshold float64) []int {
	root := identityIndexes(len(cents))
	var find func(int) int
	find = func(x int) int {
		if root[x] != x {
			root[x] = find(root[x])
		}
		return root[x]
	}
	joined := false
	for a := range cents {
		for b := a + 1; b < len(cents); b++ {
			if scope[a] != scope[b] || dot(cents[a], cents[b]) < threshold {
				continue
			}
			if ra, rb := find(a), find(b); ra != rb {
				root[max(ra, rb)] = min(ra, rb)
				joined = true
			}
		}
	}
	if !joined {
		return nil
	}
	for l := range root {
		root[l] = find(l)
	}
	return root
}

// numberInterests renumbers interests as a Grouping numbers them: area by
// area in area order (area being NoiseLabel throughout in ShapeFlat), within
// an area largest first, ties to the interest whose smallest member ID
// sorts first.
func numberInterests(interest, area []int, ids []string) []int {
	type group struct {
		label, area, size int
		first             string
	}
	byLabel := make(map[int]*group)
	var groups []*group
	for i, l := range interest {
		if l == NoiseLabel {
			continue
		}
		g, ok := byLabel[l]
		if !ok {
			g = &group{label: l, area: area[i], first: ids[i]}
			byLabel[l] = g
			groups = append(groups, g)
		}
		g.size++
		g.first = min(g.first, ids[i])
	}
	slices.SortFunc(groups, func(a, b *group) int {
		return cmp.Or(cmp.Compare(a.area, b.area), cmp.Compare(b.size, a.size), strings.Compare(a.first, b.first))
	})
	next := make(map[int]int, len(groups))
	for n, g := range groups {
		next[g.label] = n
	}
	out := make([]int, len(interest))
	for i, l := range interest {
		out[i] = NoiseLabel
		if l != NoiseLabel {
			out[i] = next[l]
		}
	}
	return out
}

// FitKind is how a point fits its interest. The values are the ones storage
// records.
type FitKind string

const (
	// FitMember is a point the grouping put in an interest.
	FitMember FitKind = "member"
	// FitLoose is a point in no interest whose nearest interest centroid
	// is at LooseFitThreshold or above.
	FitLoose FitKind = "loose"
	// FitUnsorted is every other point.
	FitUnsorted FitKind = "unsorted"
)

// Fit is where one point sits after the grouping.
type Fit struct {
	Kind FitKind
	// Interest is a member's or loose fit's interest; for an unsorted
	// point the nearest one, or -1 when there is none.
	Interest int
	// Similarity is the cosine to Interest's centroid; 0 without one.
	Similarity float64
}

// AssignStrays gives every point of g its Fit: a member its own interest
// with its cosine to that interest's centroid, any other point its nearest
// centroid (ties to the lower label), as a loose fit when the cosine is at
// threshold or above and unsorted otherwise. centroids must be
// Centroids(points, g.Interest). Inputs are not modified.
func AssignStrays(points []Point, g Grouping, centroids [][]float32, threshold float64) ([]Fit, error) {
	if err := checkStep(points, g, threshold); err != nil {
		return nil, err
	}
	if len(points) == 0 {
		return []Fit{}, nil
	}
	k := numLabels(g.Interest)
	if len(centroids) != k {
		return nil, fmt.Errorf("insight: %d centroids for %d interests", len(centroids), k)
	}
	if err := checkCentroids(centroids, len(points[0].Vector)); err != nil {
		return nil, err
	}
	fits := make([]Fit, len(points))
	for i, p := range points {
		if l := g.Interest[i]; l != NoiseLabel {
			fits[i] = Fit{Kind: FitMember, Interest: l, Similarity: dot(p.Vector, centroids[l])}
			continue
		}
		best, sim := nearestCentroid(p.Vector, centroids)
		kind := FitUnsorted
		if best >= 0 && sim >= threshold {
			kind = FitLoose
		}
		fits[i] = Fit{Kind: kind, Interest: best, Similarity: sim}
	}
	return fits, nil
}

// checkCentroids checks every centroid has dim finite components.
func checkCentroids(centroids [][]float32, dim int) error {
	for l, c := range centroids {
		if len(c) != dim {
			return fmt.Errorf("insight: centroid %d has dim %d, want %d", l, len(c), dim)
		}
		for _, x := range c {
			if f := float64(x); math.IsNaN(f) || math.IsInf(f, 0) {
				return fmt.Errorf("insight: centroid %d has a NaN or infinite component", l)
			}
		}
	}
	return nil
}

// nearestCentroid returns the centroid closest to v by cosine (v and the
// centroids being unit or zero), the lower label winning ties, and that
// cosine; -1 and 0 without centroids. AssignStrays and RunSpace share it,
// so a stray and a placed document are judged alike.
func nearestCentroid(v []float32, centroids [][]float32) (int, float64) {
	best, bestSim := -1, 0.0
	for l, c := range centroids {
		if s := dot(v, c); best < 0 || s > bestSim {
			best, bestSim = l, s
		}
	}
	return best, bestSim
}
