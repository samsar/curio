package insight

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"

	"github.com/samsar/curio/internal/insight/louvain"
)

// Shape is how a grouping is organized. The values are the ones storage
// records.
type Shape string

const (
	// ShapeFlat is one level of interests.
	ShapeFlat Shape = "flat"
	// ShapeAreas is broad areas, each holding interests.
	ShapeAreas Shape = "areas"
)

// The smallest groups a Grouping holds: a smaller community is left out.
const (
	MinAreaSize     = 10
	MinInterestSize = 3
)

// Seed is a point's place in a grouping as the next grouping's warm start
// reads it: its area and interest communities before the size cuts, opaque
// numbers, -1 for none.
type Seed struct{ Area, Interest int }

// noSeed is a point with no place.
var noSeed = Seed{Area: -1, Interest: -1}

// GroupInput is what a grouping starts from.
type GroupInput struct {
	// Points are unit (or zero) vectors in the grouping's centered space,
	// one per document ID.
	Points []Point
	// Shape is the shape of the grouping Prior came from, ShapeFlat when
	// there is none: the state the gate between the shapes moves from.
	Shape Shape
	// Prior holds the previous grouping's seeds by document ID. Entries
	// for IDs not in Points are ignored, and a point without one starts
	// beside the neighbours it is most strongly connected to (see package
	// louvain). A Prior with a seed for none of the points (nil or empty
	// included) makes a fresh pass: started from nothing, every point
	// would join its first neighbours, coarser than a fresh pass groups.
	Prior map[string]Seed
	// Split runs the check that parts a community grown into two topics.
	// It is ignored on a fresh pass (no seed for any point, or a change of
	// shape), which is its own split check.
	Split bool
}

// Grouping is a Grouper's partition of GroupInput.Points; every slice is
// index-aligned with them. Areas are numbered largest first, ties to the
// area whose smallest member ID sorts first; interests area by area in
// area order, within an area (or the whole grouping, in ShapeFlat) largest
// first with the same tie-break.
type Grouping struct {
	// Shape is the shape produced. When it differs from GroupInput.Shape
	// the grouping was computed fresh, Prior and Split ignored.
	Shape Shape
	// Area is each point's area, or NoiseLabel; always NoiseLabel in
	// ShapeFlat.
	Area []int
	// Interest is each point's interest, or NoiseLabel. In ShapeAreas
	// every point of an interest is in one area.
	Interest []int
	// Seeds is what the next grouping's Prior holds for each point.
	Seeds []Seed
	// Splits counts the communities the split check divided, at every
	// level.
	Splits int
}

// Validate checks g's invariants for numPoints points: aligned slices,
// labels numbered 0..k-1 with every number used, ShapeFlat without areas,
// in ShapeAreas every interest inside one area, and no area or interest
// below its minimum size.
func (g Grouping) Validate(numPoints int) error {
	if len(g.Area) != numPoints || len(g.Interest) != numPoints || len(g.Seeds) != numPoints {
		return fmt.Errorf("insight: grouping of %d areas, %d interests and %d seeds for %d points",
			len(g.Area), len(g.Interest), len(g.Seeds), numPoints)
	}
	areaSizes, err := labelSizes("area", g.Area)
	if err != nil {
		return err
	}
	interestSizes, err := labelSizes("interest", g.Interest)
	if err != nil {
		return err
	}
	switch g.Shape {
	case ShapeFlat:
		for i := range g.Area {
			if g.Area[i] != NoiseLabel || g.Seeds[i].Area != -1 {
				return fmt.Errorf("insight: point %d has area %d (seed %d) in the flat shape", i, g.Area[i], g.Seeds[i].Area)
			}
		}
	case ShapeAreas:
		areaOf := make([]int, len(interestSizes))
		for c := range areaOf {
			areaOf[c] = NoiseLabel
		}
		for i, c := range g.Interest {
			if c == NoiseLabel {
				continue
			}
			switch a := g.Area[i]; {
			case a == NoiseLabel:
				return fmt.Errorf("insight: point %d is in interest %d but in no area", i, c)
			case areaOf[c] == NoiseLabel:
				areaOf[c] = a
			case areaOf[c] != a:
				return fmt.Errorf("insight: interest %d spans areas %d and %d", c, areaOf[c], a)
			}
		}
	default:
		return fmt.Errorf("insight: unknown shape %q", g.Shape)
	}
	for a, n := range areaSizes {
		if n < MinAreaSize {
			return fmt.Errorf("insight: area %d has %d members, want at least %d", a, n, MinAreaSize)
		}
	}
	for c, n := range interestSizes {
		if n < MinInterestSize {
			return fmt.Errorf("insight: interest %d has %d members, want at least %d", c, n, MinInterestSize)
		}
	}
	return nil
}

// labelSizes returns the size of each label 0..k-1 of labels, failing
// unless every label is NoiseLabel or one of 0..k-1 and each is used.
func labelSizes(what string, labels []int) ([]int, error) {
	if err := checkLabels(what, labels); err != nil {
		return nil, err
	}
	sizes := make([]int, numLabels(labels))
	for _, l := range labels {
		if l != NoiseLabel {
			sizes[l]++
		}
	}
	if gap := slices.Index(sizes, 0); gap >= 0 {
		return nil, fmt.Errorf("insight: %s %d is unused among 0..%d", what, gap, len(sizes)-1)
	}
	return sizes, nil
}

// checkLabels fails unless every label is NoiseLabel or names a group of
// the points: with every label used, a grouping of n points has at most n
// groups, so a larger label is wrong, and sizing anything by it could
// exhaust memory.
func checkLabels(what string, labels []int) error {
	for i, l := range labels {
		if l < NoiseLabel || l >= len(labels) {
			return fmt.Errorf("insight: point %d has %s %d", i, what, l)
		}
	}
	return nil
}

// Grouper partitions a library's prepared vectors into areas and interests.
// It must be deterministic: the same points (in any order), Shape, Prior and
// Split give the same Grouping, per document ID.
type Grouper interface {
	Group(ctx context.Context, in GroupInput) (Grouping, error)
	// Name identifies the algorithm; it is stored on the run.
	Name() string
	// Params returns every constant that changes a grouping; it is stored
	// on the run, and a change makes the next rebuild fresh.
	Params() map[string]any
}

// checkInput validates what every Grouper takes: a known shape, unique
// IDs, and vectors of one width, each unit length or zero.
func checkInput(in GroupInput) error {
	switch in.Shape {
	case ShapeFlat, ShapeAreas:
	default:
		return fmt.Errorf("insight: unknown shape %q", in.Shape)
	}
	if len(in.Points) == 0 {
		return nil
	}
	ids := make([]string, len(in.Points))
	for i, p := range in.Points {
		ids[i] = p.ID
	}
	slices.Sort(ids)
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			return fmt.Errorf("insight: point %s appears twice", ids[i])
		}
	}
	return checkUnitVectors(in.Points)
}

// emptyGrouping is the grouping of no points.
func emptyGrouping() Grouping {
	return Grouping{Shape: ShapeFlat, Area: []int{}, Interest: []int{}, Seeds: []Seed{}}
}

// louvainConstants are LouvainGrouper's constants beside the graph's
// (graphK, areaK, areaMinSimilarity); Params records every one.
type louvainConstants struct {
	areaResolution     float64
	interestResolution float64
	minArea            int
	minInterest        int
	// The flat shape's resolution is r(n) = max(1, flatScale·√(n/flatReference)).
	flatScale     float64
	flatReference float64
	// The gate between the shapes: from flat to areas at areasFromDocs
	// points and areasFromCoverage of them in an area, back to flat below
	// flatBelowDocs points or flatBelowCoverage.
	areasFromDocs     int
	areasFromCoverage float64
	flatBelowDocs     int
	flatBelowCoverage float64
	seed              uint64
	// Louvain's caps; zero takes package louvain's defaults.
	maxPasses   int
	maxRestarts int
}

// algorithmVersion changes when the grouping code changes what it produces
// for the same constants.
const algorithmVersion = 1

// LouvainGrouper groups with Louvain on the kNN graph (package louvain).
//
// Below the gate it makes one level of interests: Louvain on the graph of
// every point's 20 nearest neighbours, at a resolution that grows with the
// library, r(n) = max(1, 8·√(n/5254)), so a small library gets a few
// interests and the owner's 5,254 documents the resolution measured for
// them. From the gate on it makes areas, Louvain at resolution 1 on the
// graph of every point's 10 nearest neighbours at cosine 0.40 or above
// (communities of 10 or more), each holding interests found by Louvain at
// resolution 1 inside the area's subgraph (communities of 3 or more). An
// area with no community of 3 becomes one interest. The gate opens at 1,000
// points with 80% of them in an area and closes below 900 or 70%, so a
// library near either threshold doesn't flip shapes on every rebuild.
//
// A warm pass starts both levels from the previous grouping's seeds and,
// when asked, runs the split check at each level and Louvain again from its
// result. Points are worked on in ID order, so the input order never
// matters. It holds no mutable state and is safe for concurrent use.
type LouvainGrouper struct {
	log        *slog.Logger
	c          louvainConstants
	neighbours func(ctx context.Context, vecs [][]float32) (neighbourLists, error)
}

// NewLouvainGrouper returns the grouper; a nil log logs to slog.Default().
func NewLouvainGrouper(log *slog.Logger) *LouvainGrouper {
	if log == nil {
		log = slog.Default()
	}
	return &LouvainGrouper{
		log: log,
		c: louvainConstants{
			areaResolution:     1,
			interestResolution: 1,
			minArea:            MinAreaSize,
			minInterest:        MinInterestSize,
			flatScale:          8,
			flatReference:      5254,
			areasFromDocs:      1000,
			areasFromCoverage:  0.80,
			flatBelowDocs:      900,
			flatBelowCoverage:  0.70,
			seed:               1,
		},
		neighbours: nearestNeighbours,
	}
}

// Name implements Grouper.
func (*LouvainGrouper) Name() string { return "louvain" }

// Params implements Grouper.
func (lg *LouvainGrouper) Params() map[string]any {
	c := lg.c
	return map[string]any{
		"algorithm_version":    algorithmVersion,
		"graph_k":              graphK,
		"area_k":               areaK,
		"area_min_similarity":  areaMinSimilarity,
		"area_resolution":      c.areaResolution,
		"interest_resolution":  c.interestResolution,
		"min_area_size":        c.minArea,
		"min_interest_size":    c.minInterest,
		"flat_resolution":      map[string]any{"scale": c.flatScale, "reference_documents": c.flatReference},
		"areas_from_documents": c.areasFromDocs,
		"areas_from_coverage":  c.areasFromCoverage,
		"flat_below_documents": c.flatBelowDocs,
		"flat_below_coverage":  c.flatBelowCoverage,
		"seed":                 c.seed,
		"visit_order":          "seeded-permutation",
		"new_documents":        "beside-strongest-neighbours",
	}
}

// flatResolution is r(n).
func (c louvainConstants) flatResolution(n int) float64 {
	return max(1, c.flatScale*math.Sqrt(float64(n)/c.flatReference))
}

// shapeAfter is the gate: the shape a grouping of n points takes from the
// shape cur, coverage being the share of them in an area of at least
// minArea.
func (c louvainConstants) shapeAfter(cur Shape, n int, coverage float64) Shape {
	if cur == ShapeAreas {
		if n < c.flatBelowDocs || coverage < c.flatBelowCoverage {
			return ShapeFlat
		}
		return ShapeAreas
	}
	if n >= c.areasFromDocs && coverage >= c.areasFromCoverage {
		return ShapeAreas
	}
	return ShapeFlat
}

// needsCoverage reports whether the gate's answer for n points from cur
// depends on coverage, so that below it the area pass is skipped.
func (c louvainConstants) needsCoverage(cur Shape, n int) bool {
	if cur == ShapeAreas {
		return n >= c.flatBelowDocs
	}
	return n >= c.areasFromDocs
}

// Group implements Grouper.
func (lg *LouvainGrouper) Group(ctx context.Context, in GroupInput) (Grouping, error) {
	if err := checkInput(in); err != nil {
		return Grouping{}, err
	}
	if len(in.Points) == 0 {
		return emptyGrouping(), nil
	}
	order, sorted := byID(in.Points)
	lists, err := lg.neighbours(ctx, vectorsOf(sorted))
	if err != nil {
		return Grouping{}, fmt.Errorf("insight: nearest neighbours: %w", err)
	}
	ids := idsOf(sorted)
	p := &groupPass{ctx: ctx, c: lg.c, points: sorted, ids: ids, lists: lists, prior: seedsFor(in.Prior, ids), split: in.Split}
	g, err := p.group(in.Shape)
	if err != nil {
		return Grouping{}, err
	}
	if p.caps.hits > 0 {
		lg.log.Warn("interests: louvain stopped at a cap, so the grouping may not be optimal",
			"pass", p.caps.pass, "nodes", p.caps.nodes, "cap", p.caps.cap, "limit", p.caps.limit, "hits", p.caps.hits)
	}
	return g.unsorted(order), nil
}

// groupPass is one Group call's work, on points sorted by ID.
type groupPass struct {
	ctx    context.Context
	c      louvainConstants
	points []Point
	ids    []string
	lists  neighbourLists
	prior  map[string]Seed
	split  bool
	splits int
	caps   capReport
}

// capReport remembers the first Louvain cap a Group call hit, and counts
// them: the pass (area, interest or flat), its node count, the cap and its
// limit.
type capReport struct {
	pass  string
	nodes int
	cap   string
	limit int
	hits  int
}

// group decides the shape and groups at it.
func (p *groupPass) group(cur Shape) (Grouping, error) {
	n := len(p.points)
	if !p.c.needsCoverage(cur, n) {
		return p.flat(cur == ShapeFlat)
	}
	areaGraph, err := louvain.NewGraph(p.lists.areaGraph())
	if err != nil {
		return Grouping{}, fmt.Errorf("insight: area graph: %w", err)
	}
	sameShape := cur == ShapeAreas
	areaComm, err := p.areas(areaGraph, sameShape)
	if err != nil {
		return Grouping{}, err
	}
	areas := numberGroups(areaComm, p.ids, p.c.minArea)
	covered := 0
	for _, a := range areas {
		if a != NoiseLabel {
			covered++
		}
	}
	if p.c.shapeAfter(cur, n, float64(covered)/float64(n)) == ShapeFlat {
		return p.flat(cur == ShapeFlat)
	}
	// From flat, the area pass above was fresh: a flat grouping has no
	// areas to start from.
	return p.interests(areaGraph, areaComm, areas, sameShape)
}

// seedsFor returns prior when it holds a seed for one of ids, nil when it
// holds none: the pass is then fresh.
func seedsFor(prior map[string]Seed, ids []string) map[string]Seed {
	if slices.ContainsFunc(ids, func(id string) bool { _, ok := prior[id]; return ok }) {
		return prior
	}
	return nil
}

// warmFrom reports whether a pass at a shape the previous grouping also had
// starts from its seeds.
func (p *groupPass) warmFrom(sameShape bool) bool { return sameShape && p.prior != nil }

// areas finds the area communities, warm from the seeds when the previous
// grouping had areas too.
func (p *groupPass) areas(g *louvain.Graph, sameShape bool) ([]int, error) {
	var init []int
	warm := p.warmFrom(sameShape)
	if warm {
		init = p.seedsOf(func(s Seed) int { return s.Area }, nil)
	}
	return p.louvain("area", g, init, p.c.areaResolution, warm)
}

// interests finds each area's interests and assembles the areas grouping.
func (p *groupPass) interests(areaGraph *louvain.Graph, areaComm, areas []int, sameShape bool) (Grouping, error) {
	n := len(p.points)
	members := make([][]int, numLabels(areas))
	for i, a := range areas {
		if a != NoiseLabel {
			members[a] = append(members[a], i)
		}
	}
	g := Grouping{Shape: ShapeAreas, Area: areas, Interest: make([]int, n), Seeds: make([]Seed, n)}
	for i := range g.Interest {
		g.Interest[i] = NoiseLabel
		g.Seeds[i] = Seed{Area: areaComm[i], Interest: -1}
	}
	warm := p.warmFrom(sameShape)
	nextLabel, nextSeed := 0, 0
	for a, ms := range members {
		if err := p.ctx.Err(); err != nil {
			return Grouping{}, err
		}
		sub, err := areaGraph.Induced(ms)
		if err != nil {
			return Grouping{}, fmt.Errorf("insight: area %d: %w", a, err)
		}
		var init []int
		if warm {
			init = p.seedsOf(func(s Seed) int { return s.Interest }, ms)
		}
		comm, err := p.louvain("interest", sub, init, p.c.interestResolution, warm)
		if err != nil {
			return Grouping{}, err
		}
		local := numberGroups(comm, pick(p.ids, ms), p.c.minInterest)
		if !slices.ContainsFunc(local, func(l int) bool { return l != NoiseLabel }) {
			// No community of minInterest: the area is one interest.
			for k := range local {
				local[k] = 0
			}
		}
		top, topSeed := -1, -1
		for k, i := range ms {
			if local[k] != NoiseLabel {
				g.Interest[i] = nextLabel + local[k]
				top = max(top, local[k])
			}
			g.Seeds[i].Interest = nextSeed + comm[k]
			topSeed = max(topSeed, comm[k])
		}
		nextLabel += top + 1
		nextSeed += topSeed + 1
	}
	g.Splits = p.splits
	return g, nil
}

// flat groups into one level of interests at r(n).
func (p *groupPass) flat(sameShape bool) (Grouping, error) {
	n := len(p.points)
	g, err := louvain.NewGraph(p.lists.flatGraph())
	if err != nil {
		return Grouping{}, fmt.Errorf("insight: flat graph: %w", err)
	}
	p.splits = 0
	var init []int
	warm := p.warmFrom(sameShape)
	if warm {
		init = p.seedsOf(func(s Seed) int { return s.Interest }, nil)
	}
	comm, err := p.louvain("flat", g, init, p.c.flatResolution(n), warm)
	if err != nil {
		return Grouping{}, err
	}
	out := Grouping{
		Shape: ShapeFlat, Area: make([]int, n), Interest: numberGroups(comm, p.ids, p.c.minInterest),
		Seeds: make([]Seed, n), Splits: p.splits,
	}
	for i := range out.Area {
		out.Area[i] = NoiseLabel
		out.Seeds[i] = Seed{Area: -1, Interest: comm[i]}
	}
	return out, nil
}

// louvain runs Louvain on g from init at resolution res and, when the pass
// is warm and the split check was asked for, the split check and Louvain
// again from its result. pass names the pass (area, interest or flat) in
// errors and the cap warning.
func (p *groupPass) louvain(pass string, g *louvain.Graph, init []int, res float64, warm bool) ([]int, error) {
	opts := louvain.Options{Resolution: res, Seed: p.c.seed, MaxPasses: p.c.maxPasses, MaxRestarts: p.c.maxRestarts}
	r, err := louvain.Run(p.ctx, g, init, opts)
	if err != nil {
		return nil, fmt.Errorf("insight: %s louvain: %w", pass, err)
	}
	p.note(pass, g.Len(), r.Stats, opts)
	if !warm || !p.split {
		return r.Communities, nil
	}
	s, err := louvain.RefineSplit(p.ctx, g, r.Communities, opts)
	if err != nil {
		return nil, fmt.Errorf("insight: %s split check: %w", pass, err)
	}
	p.note(pass, g.Len(), s.Stats, opts)
	p.splits += s.Divided
	if s.Divided == 0 {
		return r.Communities, nil
	}
	if r, err = louvain.Run(p.ctx, g, s.Communities, opts); err != nil {
		return nil, fmt.Errorf("insight: %s louvain after the split check: %w", pass, err)
	}
	p.note(pass, g.Len(), r.Stats, opts)
	return r.Communities, nil
}

// note records a cap a Louvain run hit.
func (p *groupPass) note(pass string, nodes int, st louvain.Stats, opts louvain.Options) {
	if !st.PassCapHit && !st.RestartCapHit {
		return
	}
	p.caps.hits++
	if p.caps.hits > 1 {
		return
	}
	p.caps.pass, p.caps.nodes = pass, nodes
	if st.PassCapHit {
		p.caps.cap, p.caps.limit = "passes", cmp.Or(opts.MaxPasses, louvain.DefaultMaxPasses)
	} else {
		p.caps.cap, p.caps.limit = "restarts", cmp.Or(opts.MaxRestarts, louvain.DefaultMaxRestarts)
	}
}

// seedsOf returns the starting community of each point (of the points at
// idx, or all of them when idx is nil): its seed from the previous
// grouping, or -1 for a point it didn't know.
func (p *groupPass) seedsOf(level func(Seed) int, idx []int) []int {
	if idx == nil {
		idx = identityIndexes(len(p.points))
	}
	init := make([]int, len(idx))
	for k, i := range idx {
		init[k] = -1
		if s, ok := p.prior[p.ids[i]]; ok {
			init[k] = level(s)
		}
	}
	return init
}

func idsOf(points []Point) []string {
	ids := make([]string, len(points))
	for i, p := range points {
		ids[i] = p.ID
	}
	return ids
}

// numberGroups numbers the groups of comm (equal values together, a
// negative value in none) with at least minSize members 0..k-1, largest
// first, ties to the group whose smallest member ID sorts first; every
// other point is NoiseLabel.
func numberGroups(comm []int, ids []string, minSize int) []int {
	type group struct {
		members []int
		first   string
	}
	byComm := make(map[int]*group)
	var groups []*group
	for i, c := range comm {
		if c < 0 {
			continue
		}
		g, ok := byComm[c]
		if !ok {
			g = &group{first: ids[i]}
			byComm[c] = g
			groups = append(groups, g)
		}
		g.members = append(g.members, i)
		g.first = min(g.first, ids[i])
	}
	groups = slices.DeleteFunc(groups, func(g *group) bool { return len(g.members) < minSize })
	slices.SortFunc(groups, func(a, b *group) int {
		return cmp.Or(cmp.Compare(len(b.members), len(a.members)), strings.Compare(a.first, b.first))
	})
	out := make([]int, len(comm))
	for i := range out {
		out[i] = NoiseLabel
	}
	for label, g := range groups {
		for _, i := range g.members {
			out[i] = label
		}
	}
	return out
}

// byID returns the permutation that sorts points by ID, and the sorted
// points. IDs are unique (checkInput), so the order is total.
func byID(points []Point) ([]int, []Point) {
	order := identityIndexes(len(points))
	slices.SortFunc(order, func(a, b int) int { return strings.Compare(points[a].ID, points[b].ID) })
	sorted := make([]Point, len(points))
	for i, idx := range order {
		sorted[i] = points[idx]
	}
	return order, sorted
}

// unsorted maps a grouping of the ID-sorted points back to the input order.
func (g Grouping) unsorted(order []int) Grouping {
	out := Grouping{
		Shape: g.Shape, Area: make([]int, len(order)), Interest: make([]int, len(order)),
		Seeds: make([]Seed, len(order)), Splits: g.Splits,
	}
	for i, idx := range order {
		out.Area[idx], out.Interest[idx], out.Seeds[idx] = g.Area[i], g.Interest[i], g.Seeds[i]
	}
	return out
}

func identityIndexes(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func pick[T any](xs []T, idx []int) []T {
	out := make([]T, len(idx))
	for k, i := range idx {
		out[k] = xs[i]
	}
	return out
}

// FlatGrouper adapts a Clusterer to Grouper: every grouping is ShapeFlat,
// its interests c's clusters, renumbered as a Grouping numbers them (with
// a cluster below MinInterestSize left out), and Prior and Split are
// ignored, so every pass is fresh while identities still carry over by
// overlap. Name and Params are c's.
func FlatGrouper(c Clusterer) Grouper { return flatGrouper{c: c} }

type flatGrouper struct{ c Clusterer }

func (f flatGrouper) Name() string           { return f.c.Name() }
func (f flatGrouper) Params() map[string]any { return f.c.Params() }

func (f flatGrouper) Group(ctx context.Context, in GroupInput) (Grouping, error) {
	if err := checkInput(in); err != nil {
		return Grouping{}, err
	}
	n := len(in.Points)
	if n == 0 {
		return emptyGrouping(), nil
	}
	labels, err := f.c.Cluster(ctx, in.Points)
	if err != nil {
		return Grouping{}, fmt.Errorf("insight: %s: %w", f.c.Name(), err)
	}
	if len(labels) != n {
		return Grouping{}, fmt.Errorf("insight: %s returned %d labels for %d points", f.c.Name(), len(labels), n)
	}
	g := Grouping{
		Shape: ShapeFlat, Area: make([]int, n), Interest: numberGroups(labels, idsOf(in.Points), MinInterestSize),
		Seeds: make([]Seed, n),
	}
	for i := range g.Area {
		g.Area[i], g.Seeds[i] = NoiseLabel, noSeed
	}
	return g, nil
}
