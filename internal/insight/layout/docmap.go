package layout

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
)

// The doc map's constants: UMAP's (McInnes, Healy and Melville, 2018), at
// umap-learn's defaults where it has them.
const (
	// mapNeighbours is how many of a point's nearest neighbours its fuzzy
	// set reads (umap-learn's n_neighbors).
	mapNeighbours = 15
	// curveA and curveB shape the low-dimensional similarity 1/(1 +
	// a·d^2b): umap-learn's fit for min_dist 0.1 and spread 1.
	curveA = 1.577
	curveB = 0.895
	// negativeRate is the negative samples per positive one.
	negativeRate = 5
	// gradientClip bounds each gradient component.
	gradientClip = 4.0
	// A cold map runs coldEpochs, coldEpochsLarge above largeLibrary
	// points (umap-learn's schedule), from learning rate coldAlpha.
	coldEpochs      = 500
	coldEpochsLarge = 200
	largeLibrary    = 10000
	coldAlpha       = 1.0
	// A warm map starts from the previous map and moves less: fewer
	// epochs from a lower rate. The previous map is scaled so the median
	// length of the graph's edges between its points is warmEdgeLength,
	// the median a cold descent ends at (measured 0.31 to 0.38 for 300 to
	// 5,000 points), so the descent starts where it would settle rather
	// than expanding a map squeezed into the cold start's box, which moved
	// the shared points ten times as far. A rate of 0.1 rather than the
	// prototype's 0.25 moved the owner's documents a quarter less, its
	// neighbourhoods kept as well (docs/decisions.md, "Interest map").
	warmEpochs     = 200
	warmAlpha      = 0.1
	warmEdgeLength = 0.3
	// The start is scaled into [0, initExtent] with initNoise of seeded
	// noise, as umap-learn does.
	initExtent = 10.0
	initNoise  = 1e-4
	// Points beyond the pullQuantile radius from the median are pulled in,
	// softly, to within pullSoftness of that radius more.
	pullQuantile = 0.97
	pullSoftness = 0.25
	// minAligned is the fewest points shared with a previous map that it
	// is aligned to; an aligned map keeps the previous map's frame while
	// it fits the square and spans minFrame of it.
	minAligned = 3
	minFrame   = 0.9
)

// The fuzzy set's search for each point's bandwidth σ: bisection until the
// memberships sum to log2 of its neighbours within sigmaTolerance, σ at least
// minSigmaShare of the point's mean distance (umap-learn's MIN_K_DIST_SCALE).
const (
	sigmaBisections = 64
	sigmaTolerance  = 1e-5
	minSigmaShare   = 1e-3
)

// The PCG streams of the doc map's random numbers.
const (
	initStream = 0x1a1
	sgdStream  = 0x5d9
)

// DocMapInput is what DocMap lays out: one point per key.
type DocMapInput struct {
	// Keys name the points, uniquely; they match Prior and order the work.
	Keys []string
	// Vectors are the points' vectors, of one width: the cold start is
	// their first two principal components.
	Vectors [][]float32
	// Neighbours are each point's nearest neighbours, best first; the
	// first mapNeighbours of each are read.
	Neighbours [][]Neighbour
	// Prior holds a previous map's positions by key, nil for none. A map
	// sharing at least minAligned points with it is aligned to it.
	Prior map[string]XY
	// Warm starts the layout from Prior: a point without a position there
	// starts beside its neighbours that have one.
	Warm bool
	// Seed seeds the start's noise and the descent's negative samples.
	Seed uint64
}

// DocMapLayout is the document map: a point per key, index-aligned with
// DocMapInput.Keys, and whether the layout started from the prior, which
// a warm input does only when the prior holds one of its points.
type DocMapLayout struct {
	Points []XY
	Warm   bool
}

// DocMap lays out the points of in on the map.
func DocMap(ctx context.Context, in DocMapInput) (DocMapLayout, error) {
	if err := ctx.Err(); err != nil {
		return DocMapLayout{}, fmt.Errorf("layout: doc map: %w", err)
	}
	order, err := in.check()
	if err != nil {
		return DocMapLayout{}, err
	}
	n := len(order)
	if n == 0 {
		return DocMapLayout{Points: []XY{}}, nil
	}
	p := newDocProblem(in, order)
	pos, warm, aligned, err := p.layout(ctx)
	if err != nil {
		return DocMapLayout{}, err
	}
	f := fitBox(boxOf(pos), 0)
	if aligned && keepsFrame(boxOf(pos)) {
		f = fit{scale: 1}
	}
	out := DocMapLayout{Points: make([]XY, n), Warm: warm}
	for k, i := range order {
		out.Points[i] = f.point(pos[k])
		if err := checkPoint("point "+in.Keys[i], out.Points[i]); err != nil {
			return DocMapLayout{}, err
		}
	}
	return out, nil
}

// check validates the input and returns the keys' sorted order.
func (in DocMapInput) check() ([]int, error) {
	n := len(in.Keys)
	if len(in.Neighbours) != n {
		return nil, inputError("%d neighbour lists for %d keys", len(in.Neighbours), n)
	}
	if err := checkVectors(in.Vectors, n); err != nil {
		return nil, err
	}
	order, err := keyOrder(in.Keys)
	if err != nil {
		return nil, err
	}
	seen := make([]int, n) // the list that last named each point, +1
	for i, list := range in.Neighbours {
		for _, nb := range list {
			switch {
			case nb.Index < 0 || nb.Index >= n:
				return nil, inputError("point %d has neighbour %d of %d points", i, nb.Index, n)
			case nb.Index == i:
				return nil, inputError("point %d is its own neighbour", i)
			case !finite(nb.Similarity):
				return nil, inputError("point %d has a non-finite similarity to %d", i, nb.Index)
			case seen[nb.Index] == i+1:
				return nil, inputError("point %d lists neighbour %d twice", i, nb.Index)
			}
			seen[nb.Index] = i + 1
		}
	}
	for key, p := range in.Prior {
		if !finite(p.X, p.Y) {
			return nil, inputError("the prior position of %q is not finite", key)
		}
	}
	return order, nil
}

// docProblem is the input in key order: point k is the input's order[k].
type docProblem struct {
	keys    []string
	vectors [][]float32
	lists   [][]Neighbour // remapped, best first, ties to the lower index
	prior   map[string]XY
	warm    bool
	seed    uint64
}

func newDocProblem(in DocMapInput, order []int) docProblem {
	n := len(order)
	rank := make([]int, n)
	for k, i := range order {
		rank[i] = k
	}
	p := docProblem{keys: make([]string, n), vectors: make([][]float32, n), lists: make([][]Neighbour, n),
		prior: in.Prior, warm: in.Warm, seed: in.Seed}
	for k, i := range order {
		p.keys[k], p.vectors[k] = in.Keys[i], in.Vectors[i]
		list := make([]Neighbour, len(in.Neighbours[i]))
		for x, nb := range in.Neighbours[i] {
			list[x] = Neighbour{Index: rank[nb.Index], Similarity: nb.Similarity}
		}
		slices.SortFunc(list, func(a, b Neighbour) int {
			return cmp.Or(cmp.Compare(b.Similarity, a.Similarity), cmp.Compare(a.Index, b.Index))
		})
		p.lists[k] = list
	}
	return p
}

// layout lays the points out, before the fit to the map, and reports
// whether it started from the prior and whether it aligned them to it.
func (p docProblem) layout(ctx context.Context) (pos []XY, warm, aligned bool, err error) {
	n := len(p.keys)
	if n == 1 {
		return []XY{{}}, false, false, nil
	}
	edges := fuzzyGraph(p.lists)
	start, warm, err := p.start(ctx, edges)
	if err != nil {
		return nil, false, false, err
	}
	epochs, alpha := coldEpochs, coldAlpha
	switch {
	case warm:
		epochs, alpha = warmEpochs, warmAlpha
	case n > largeLibrary:
		epochs = coldEpochsLarge
	}
	if err := descend(ctx, start, edges, epochs, alpha, p.seed); err != nil {
		return nil, false, false, err
	}
	pullIn(start)
	return start, warm, p.orient(start), nil
}

// keepsFrame reports whether a map aligned to the previous one may stay in
// the previous map's frame rather than be fit anew: it lies inside the
// square and its longer side spans at least minFrame of it. Fitting an
// aligned map anew moved the owner's documents twice as far as the
// alignment had.
func keepsFrame(b bbox) bool {
	return b.x0 >= 0 && b.y0 >= 0 && b.x1 <= Extent && b.y1 <= Extent && math.Max(b.x1-b.x0, b.y1-b.y0) >= minFrame*Extent
}

// start is where the descent starts: from the prior when the map is warm
// and shares a point with it, else the points' first two principal
// components. It reports whether it started warm.
func (p docProblem) start(ctx context.Context, edges []edge) ([]XY, bool, error) {
	rng := rand.New(rand.NewPCG(p.seed, initStream)) //nolint:gosec // G404: seeded noise, not a secret
	if p.warm {
		if pos, ok := p.warmStart(edges, rng); ok {
			return pos, true, nil
		}
	}
	pos, err := p.coldStart(ctx, rng)
	return pos, false, err
}

// coldStart is the points' first two principal components, each axis's
// sign fixed so its third moment is not negative, scaled into [0,
// initExtent] axis by axis, with initNoise of noise.
func (p docProblem) coldStart(ctx context.Context, rng *rand.Rand) ([]XY, error) {
	set := newRowSet(p.vectors, nil)
	axes, _, err := principalAxes(ctx, set, 2, p.seed)
	if err != nil {
		return nil, err
	}
	n := len(p.vectors)
	coords := [2][]float64{make([]float64, n), make([]float64, n)}
	for k := range n {
		c := set.project(k, axes)
		for a := range c {
			coords[a][k] = c[a]
		}
	}
	pos := make([]XY, n)
	for a, cs := range coords {
		var third float64
		for _, c := range cs {
			third += c * c * c
		}
		if third < 0 {
			for k := range cs {
				cs[k] = -cs[k]
			}
		}
		lo, hi := slices.Min(cs), slices.Max(cs)
		for k, c := range cs {
			v := initExtent / 2
			if hi > lo {
				v = initExtent * (c - lo) / (hi - lo)
			}
			v += initNoise * (2*rng.Float64() - 1)
			if a == 0 {
				pos[k].X = v
			} else {
				pos[k].Y = v
			}
		}
	}
	return pos, nil
}

// warmStart is the prior's positions, scaled uniformly so the graph's
// edges between them have the median length warmEdgeLength (into [0,
// initExtent] when no edge joins two of them); a point without one starts
// at the similarity-weighted mean of its neighbours that have a start,
// found in passes, and a point no pass reaches at a seeded spot near the
// centre. ok is false when the prior holds none of the points.
func (p docProblem) warmStart(edges []edge, rng *rand.Rand) ([]XY, bool) {
	n := len(p.keys)
	pos := make([]XY, n)
	placed := make([]bool, n)
	box := emptyBox()
	for k, key := range p.keys {
		if q, ok := p.prior[key]; ok {
			pos[k], placed[k] = q, true
			box.add(q.X, q.Y, 0)
		}
	}
	if box.empty() {
		return nil, false
	}
	scale := 1.0
	if side := math.Max(box.x1-box.x0, box.y1-box.y0); side > 0 {
		scale = initExtent / side
	}
	var lengths []float64
	for _, e := range edges {
		if placed[e.lo] && placed[e.hi] {
			lengths = append(lengths, dist(pos[e.lo], pos[e.hi]))
		}
	}
	if len(lengths) > 0 {
		slices.Sort(lengths)
		if median := lengths[len(lengths)/2]; median > 0 {
			scale = warmEdgeLength / median
		}
	}
	for k := range pos {
		if placed[k] {
			pos[k] = XY{(pos[k].X - box.x0) * scale, (pos[k].Y - box.y0) * scale}
		}
	}
	for {
		var reached []int
		next := slices.Clone(pos)
		for k := range pos {
			if placed[k] {
				continue
			}
			if q, ok := p.besideNeighbours(k, pos, placed); ok {
				next[k] = q
				reached = append(reached, k)
			}
		}
		if len(reached) == 0 {
			break
		}
		pos = next
		for _, k := range reached {
			placed[k] = true
			pos[k] = XY{pos[k].X + initNoise*(2*rng.Float64()-1), pos[k].Y + initNoise*(2*rng.Float64()-1)}
		}
	}
	centre := XY{(box.x1 - box.x0) * scale / 2, (box.y1 - box.y0) * scale / 2}
	for k := range pos {
		if !placed[k] {
			pos[k] = XY{centre.X + (rng.Float64() - 0.5), centre.Y + (rng.Float64() - 0.5)}
		}
	}
	return pos, true
}

// besideNeighbours is the similarity-weighted mean of the starts of point
// k's neighbours that have one.
func (p docProblem) besideNeighbours(k int, pos []XY, placed []bool) (XY, bool) {
	var sum XY
	var weights float64
	for _, nb := range p.lists[k][:min(mapNeighbours, len(p.lists[k]))] {
		if !placed[nb.Index] {
			continue
		}
		w := math.Max(nb.Similarity, 1e-6)
		sum.X += w * pos[nb.Index].X
		sum.Y += w * pos[nb.Index].Y
		weights += w
	}
	if weights == 0 {
		return XY{}, false
	}
	return XY{sum.X / weights, sum.Y / weights}, true
}

// edge is an undirected edge of the fuzzy graph, lo < hi.
type edge struct {
	lo, hi int
	w      float64
}

// fuzzyGraph is UMAP's fuzzy simplicial set over the neighbour lists, in
// key order: each point's memberships to its first mapNeighbours
// neighbours, joined by the fuzzy union a + b − ab, edges sorted by their
// ends.
func fuzzyGraph(lists [][]Neighbour) []edge {
	type directed struct {
		lo, hi int
		w      float64
		fromLo bool
	}
	var all []directed
	for i, list := range lists {
		list = list[:min(mapNeighbours, len(list))]
		for x, w := range memberships(list) {
			j := list[x].Index
			all = append(all, directed{lo: min(i, j), hi: max(i, j), w: w, fromLo: i < j})
		}
	}
	slices.SortFunc(all, func(a, b directed) int {
		return cmp.Or(cmp.Compare(a.lo, b.lo), cmp.Compare(a.hi, b.hi))
	})
	var edges []edge
	for x := 0; x < len(all); {
		var a, b float64
		lo, hi := all[x].lo, all[x].hi
		for ; x < len(all) && all[x].lo == lo && all[x].hi == hi; x++ {
			if all[x].fromLo {
				a = all[x].w
			} else {
				b = all[x].w
			}
		}
		if w := a + b - a*b; w > 0 {
			edges = append(edges, edge{lo: lo, hi: hi, w: w})
		}
	}
	return edges
}

// memberships are a point's fuzzy memberships to the neighbours of list:
// with d = max(0, 1 − similarity) and ρ the smallest positive d,
// exp(−max(0, d − ρ)/σ), σ found so they sum to log2 of the list's length.
// A list of one neighbour or none gets weight 1.
func memberships(list []Neighbour) []float64 {
	w := make([]float64, len(list))
	if len(list) <= 1 {
		for x := range w {
			w[x] = 1
		}
		return w
	}
	d := make([]float64, len(list))
	var rho, mean float64
	for x, nb := range list {
		d[x] = math.Max(0, 1-nb.Similarity)
		if rho == 0 && d[x] > 0 {
			rho = d[x]
		}
		mean += d[x] / float64(len(list))
	}
	sum := func(sigma float64) float64 {
		var s float64
		for _, v := range d {
			if v > rho {
				s += math.Exp(-(v - rho) / sigma)
			} else {
				s++
			}
		}
		return s
	}
	target := math.Log2(float64(len(list)))
	lo, hi, sigma := 0.0, math.Inf(1), 1.0
	for range sigmaBisections {
		s := sum(sigma)
		if math.Abs(s-target) < sigmaTolerance {
			break
		}
		if s > target {
			hi = sigma
			sigma = (lo + hi) / 2
		} else {
			lo = sigma
			if math.IsInf(hi, 1) {
				sigma *= 2
			} else {
				sigma = (lo + hi) / 2
			}
		}
	}
	sigma = math.Max(sigma, minSigmaShare*mean)
	for x, v := range d {
		w[x] = 1
		if v > rho {
			w[x] = math.Exp(-(v - rho) / sigma)
		}
	}
	return w
}

// descend runs UMAP's stochastic gradient descent
// (optimize_layout_euclidean) on the graph from pos, in place: each edge, in
// both directions, sampled in proportion to its weight with epochs at least
// max weight / epochs, attracts its ends; each sample repels its head from
// negativeRate random points; the rate falls linearly from alpha to 0. One
// goroutine, one seeded generator, edges in key order.
func descend(ctx context.Context, pos []XY, edges []edge, epochs int, alpha float64, seed uint64) error {
	var maxW float64
	for _, e := range edges {
		maxW = math.Max(maxW, e.w)
	}
	// A sample is an edge in one direction: its head moves toward its tail
	// every every epochs, and away from random points perNeg apart.
	type sample struct {
		head, tail    int
		every, perNeg float64
		next, nextNeg float64 // the epoch of its next sample, and of its next negative one
	}
	var samples []sample
	for _, e := range edges {
		if e.w < maxW/float64(epochs) {
			continue
		}
		every := maxW / e.w
		samples = append(samples, sample{head: e.lo, tail: e.hi, every: every}, sample{head: e.hi, tail: e.lo, every: every})
	}
	slices.SortFunc(samples, func(a, b sample) int { return cmp.Or(cmp.Compare(a.head, b.head), cmp.Compare(a.tail, b.tail)) })
	for x := range samples {
		s := &samples[x]
		s.perNeg = s.every / negativeRate
		s.next, s.nextNeg = s.every, s.perNeg
	}
	rng := rand.New(rand.NewPCG(seed, sgdStream)) //nolint:gosec // G404: seeded negative samples, not a secret
	n := len(pos)
	for epoch := range epochs {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("layout: doc map descent: %w", err)
		}
		rate := alpha * (1 - float64(epoch)/float64(epochs))
		e := float64(epoch)
		for x := range samples {
			s := &samples[x]
			if s.next > e {
				continue
			}
			// The head moves through its negative samples in locals,
			// written back once.
			cx, cy := pos[s.head].X, pos[s.head].Y
			if dx, dy := cx-pos[s.tail].X, cy-pos[s.tail].Y; dx*dx+dy*dy > 0 {
				d2 := dx*dx + dy*dy
				pb := power(d2)
				g := -2 * curveA * curveB * (pb / d2) / (curveA*pb + 1)
				gx, gy := clip(g*dx)*rate, clip(g*dy)*rate
				cx, cy = cx+gx, cy+gy
				pos[s.tail] = XY{pos[s.tail].X - gx, pos[s.tail].Y - gy}
			}
			s.next += s.every
			negatives := int((e - s.nextNeg) / s.perNeg)
			for range negatives {
				k := rng.IntN(n)
				if k == s.head {
					continue
				}
				dx, dy := cx-pos[k].X, cy-pos[k].Y
				d2 := dx*dx + dy*dy
				if d2 <= 0 {
					continue
				}
				g := 2 * curveB / ((0.001 + d2) * (curveA*power(d2) + 1))
				cx += clip(g*dx) * rate
				cy += clip(g*dy) * rate
			}
			pos[s.head] = XY{cx, cy}
			s.nextNeg += float64(negatives) * s.perNeg
		}
	}
	return nil
}

// clip bounds a gradient component to ±gradientClip. Written out: it runs
// for every sample, where math.Min and math.Max's care for NaN and signed
// zeros costs a sixth of the descent.
func clip(v float64) float64 {
	switch {
	case v > gradientClip:
		return gradientClip
	case v < -gradientClip:
		return -gradientClip
	}
	return v
}

// power is d2^curveB for d2 > 0, as 2^(curveB·log₂ d2): the logarithm by
// the atanh series on the float's mantissa taken into [√½, √2), the power of
// two by a Taylor series about the nearest integer, within 1e-12 relative of
// math.Pow. The descent needs it for every sample, a hundred million times
// for a library of 5,000 points, where math.Pow's logarithm alone costs half
// the descent. A value outside the normal range takes math.Pow.
func power(d2 float64) float64 {
	bits := math.Float64bits(d2)
	e := int(bits>>52&0x7ff) - 1023
	if e <= -1022 || e >= 1022 {
		return math.Pow(d2, curveB)
	}
	m := math.Float64frombits(bits&(1<<52-1) | 1023<<52) // in [1, 2)
	if m > math.Sqrt2 {
		m, e = m/2, e+1
	}
	t := (m - 1) / (m + 1) // |t| < 0.172
	t2 := t * t
	atanh := t * (1 + t2*(1.0/3+t2*(1.0/5+t2*(1.0/7+t2*(1.0/9+t2*(1.0/11+t2*(1.0/13+t2/15)))))))
	y := curveB * (float64(e) + atanh*(2/math.Ln2))
	n := math.Round(y)
	x := (y - n) * math.Ln2 // |x| ≤ ln2/2
	exp := 1 + x*(1+x*(1.0/2+x*(1.0/6+x*(1.0/24+x*(1.0/120+x*(1.0/720+x*(1.0/5040+x*(1.0/40320+
		x*(1.0/362880+x*(1.0/3628800))))))))))
	return exp * math.Float64frombits(uint64(n+1023)<<52)
}

// pullIn pulls the points beyond the pullQuantile radius from the
// coordinate-wise median in along their own direction, softly: a point at
// r past that radius r₉₇ goes to r₉₇ + s·(1 − e^{−(r − r₉₇)/s}), s =
// pullSoftness·r₉₇. UMAP's distances between islands mean little, and a few
// far points would otherwise shrink everything else to fit the map.
func pullIn(pos []XY) {
	n := len(pos)
	xs, ys := make([]float64, n), make([]float64, n)
	for k, q := range pos {
		xs[k], ys[k] = q.X, q.Y
	}
	slices.Sort(xs)
	slices.Sort(ys)
	med := XY{xs[n/2], ys[n/2]}
	rad := make([]float64, n)
	for k, q := range pos {
		rad[k] = dist(q, med)
	}
	sorted := slices.Sorted(slices.Values(rad))
	r97 := sorted[int(math.Round(pullQuantile*float64(n-1)))]
	if r97 <= 0 {
		return
	}
	s := pullSoftness * r97
	for k, r := range rad {
		if r <= r97 {
			continue
		}
		f := (r97 + s*(1-math.Exp(-(r-r97)/s))) / r
		pos[k] = XY{med.X + (pos[k].X-med.X)*f, med.Y + (pos[k].Y-med.Y)*f}
	}
}

// orient turns the map: onto the prior, by the least-squares similarity
// transform of the points they share, when they share minAligned; else its
// principal axis horizontal, each axis's sign such that its third moment is
// not negative. It reports whether it aligned the map to the prior.
func (p docProblem) orient(pos []XY) bool {
	var src, dst []XY
	for k, key := range p.keys {
		if q, ok := p.prior[key]; ok {
			src, dst = append(src, pos[k]), append(dst, q)
		}
	}
	if len(src) >= minAligned {
		t := procrustes(src, dst, true)
		for k := range pos {
			pos[k] = t.apply(pos[k])
		}
		return true
	}
	levelAxes(pos)
	return false
}

// levelAxes rotates pos about its centroid so its principal axis is
// horizontal, then flips each axis whose third moment is negative.
func levelAxes(pos []XY) {
	c := centroid(pos)
	var sxx, sxy, syy float64
	for _, q := range pos {
		dx, dy := q.X-c.X, q.Y-c.Y
		sxx += dx * dx
		sxy += dx * dy
		syy += dy * dy
	}
	_, vecs := symEigen([][]float64{{sxx, sxy}, {sxy, syy}})
	ux, uy := vecs[0][0], vecs[0][1]
	var tx, ty float64
	for k, q := range pos {
		dx, dy := q.X-c.X, q.Y-c.Y
		pos[k] = XY{dx*ux + dy*uy, -dx*uy + dy*ux}
		tx += pos[k].X * pos[k].X * pos[k].X
		ty += pos[k].Y * pos[k].Y * pos[k].Y
	}
	for k := range pos {
		if tx < 0 {
			pos[k].X = -pos[k].X
		}
		if ty < 0 {
			pos[k].Y = -pos[k].Y
		}
	}
}

func boxOf(pos []XY) bbox {
	b := emptyBox()
	for _, q := range pos {
		b.add(q.X, q.Y, 0)
	}
	return b
}
