package insight

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/samsar/curio/internal/insight/layout"
	"github.com/samsar/curio/internal/insight/louvain"
	"github.com/samsar/curio/internal/store"
)

// The interest map: two views of each regrouping, drawn by BuildMap when
// the regrouping is built. The document map places every document (package
// layout's DocMap, on the grouping's own neighbour lists); the zoom view
// places the grouping, each interest a circle of its documents inside its
// area's (layout's Zoom). Each area and interest also gets its label's
// anchor on the document map. See docs/decisions.md, "Interest map: two
// views of each regrouping, drawn when it is built".

// MapSeed seeds the map the engine draws: the layouts' starts and the
// descent. The cluster report draws with other seeds to measure how much
// the seed moves the map.
const MapSeed = 1

// mapVersion changes when BuildMap changes what it draws for the same
// layouts: the anchors, the views' inputs.
const mapVersion = 1

// SimilarInterests is how many of its most similar interests each interest
// of a run lists.
const SimilarInterests = 3

// MapParams is the JSON a map records: every constant of the layouts and
// of BuildMap, the seed and whether the vectors were centred. A prior map
// with other params never starts a map warm. Canonical: encoding/json sorts
// map keys.
func MapParams(center bool, seed uint64) ([]byte, error) {
	raw, err := json.Marshal(map[string]any{
		"layout": layout.Params(), "map_version": mapVersion, "seed": seed, "center": center,
		"similar_interests": SimilarInterests,
	})
	if err != nil {
		return nil, fmt.Errorf("encode the map's params: %w", err)
	}
	return raw, nil
}

// MapInput is what BuildMap draws: a grouping as the engine holds it after
// the merge, the strays and the carry-over.
type MapInput struct {
	// Points are the prepared points the grouping grouped.
	Points []Point
	// Grouping is the grouping after MergeNearDuplicates; its Neighbours,
	// when set, are the document map's, else BuildMap finds them.
	Grouping Grouping
	// Centroids are Centroids(Points, Grouping.Interest), and Fits
	// AssignStrays'.
	Centroids [][]float32
	Fits      []Fit
	// AreaKeys and InterestKeys name each area and interest, by the
	// grouping's numbering: they find a group's place in Prior. The
	// engine's are identity IDs.
	AreaKeys, InterestKeys []string
	// AreaStarts and InterestStarts name, for a new group, the prior group
	// whose place it starts at (its lineage row's old identity with the
	// most shared members), "" for none; nil for none at all.
	AreaStarts, InterestStarts []string
	// Prior is the previous run's built map, nil for none. The document
	// map is aligned to it, and the zoom view starts from it when its
	// params and shape are this map's.
	Prior *PriorMap
	// WarmDocs lets the document map start from Prior when its params are
	// this map's: the engine's rule, which a re-embedding or a drift
	// forbids.
	WarmDocs bool
	// Unchanged says no document changed since Prior's run: with WarmDocs,
	// and the same documents, the document map is Prior's verbatim, and so
	// is the zoom view when the grouping is unchanged too.
	Unchanged bool
	// Center and Seed are recorded in the params; Seed seeds the layouts.
	Center bool
	Seed   uint64
}

// PriorMap is a previous run's built map, as a new one reads it.
type PriorMap struct {
	Params    []byte
	Shape     Shape
	DotRadius float64
	Unsorted  store.Circle
	// Docs are the run's assigned documents by ID, with their places;
	// documents placed since are left out, their places approximations.
	Docs map[string]PriorDoc
	// Groups are the run's areas and interests by identity.
	Groups map[string]PriorGroup
}

// PriorDoc is a document of a prior map: where the run put it, and its
// place.
type PriorDoc struct {
	Fit        store.InterestFit
	InterestID string
	AreaID     string
	Map        store.MapPosition
}

// PriorGroup is a group of a prior map.
type PriorGroup struct {
	Level    store.InterestLevel
	ParentID string
	Map      store.GroupMap
}

// NewPriorMap is run's map as the next map reads it, from the run's groups
// and assignments; nil unless the run's map is built.
func NewPriorMap(run *store.InterestRun, groups []store.InterestGroup, assignments []store.InterestAssignment) *PriorMap {
	if run == nil || run.Map == nil || run.Map.Status != store.MapBuilt {
		return nil
	}
	p := &PriorMap{Params: run.Map.Params, Shape: Shape(run.Shape), DotRadius: run.Map.DotRadius,
		Unsorted: run.Map.Unsorted, Docs: make(map[string]PriorDoc, len(assignments)),
		Groups: make(map[string]PriorGroup, len(groups))}
	for _, a := range assignments {
		if a.Map != nil {
			p.Docs[a.DocumentID] = PriorDoc{Fit: a.Fit, InterestID: a.InterestID, AreaID: a.AreaID, Map: *a.Map}
		}
	}
	for _, g := range groups {
		if g.Map != nil {
			p.Groups[g.ID] = PriorGroup{Level: g.Level, ParentID: g.ParentID, Map: *g.Map}
		}
	}
	return p
}

// Map is a grouping's map: the zoom view's dot radius and Unsorted's disc,
// a place per document (index-aligned with MapInput.Points), and per area
// and interest (by the grouping's numbering) its circle and anchor.
type Map struct {
	// Kind is warm when the document map started from the prior's, or is
	// it; fresh otherwise, a map allowed to start warm from a prior that
	// shares none of its documents included.
	Kind      store.RunKind
	Params    []byte
	DotRadius float64
	Unsorted  store.Circle
	Docs      []store.MapPosition
	Areas     []store.GroupMap
	Interests []store.GroupMap
}

// BuildMap draws the map of in. The document map starts warm from the
// prior when in.WarmDocs and the params match, and is aligned to the
// prior's either way; the zoom view starts warm when the params and the
// shape match. A map of an unchanged library is the prior's, as described
// at MapInput.Unchanged. Positions never depend on the keys' values, only
// on which are equal: a new group's key is a fresh identity.
func BuildMap(ctx context.Context, in MapInput) (*Map, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	params, err := MapParams(in.Center, in.Seed)
	if err != nil {
		return nil, err
	}
	b := mapBuild{ctx: ctx, in: in, params: params}
	return b.build()
}

// check validates what BuildMap takes beyond what the layouts check.
func (in MapInput) check() error {
	n, areas, interests := len(in.Points), numLabels(in.Grouping.Area), numLabels(in.Grouping.Interest)
	switch {
	case len(in.Fits) != n:
		return fmt.Errorf("insight: %d fits for %d points", len(in.Fits), n)
	case len(in.Centroids) != interests || len(in.InterestKeys) != interests || len(in.AreaKeys) != areas:
		return fmt.Errorf("insight: %d centroids and %d keys for %d interests, %d keys for %d areas",
			len(in.Centroids), len(in.InterestKeys), interests, len(in.AreaKeys), areas)
	case in.AreaStarts != nil && len(in.AreaStarts) != areas, in.InterestStarts != nil && len(in.InterestStarts) != interests:
		return fmt.Errorf("insight: %d area starts and %d interest starts for %d areas and %d interests",
			len(in.AreaStarts), len(in.InterestStarts), areas, interests)
	}
	return in.Grouping.Validate(n)
}

// mapBuild is one BuildMap call's work.
type mapBuild struct {
	ctx    context.Context
	in     MapInput
	params []byte
}

func (b mapBuild) build() (*Map, error) {
	in := b.in
	sameParams := in.Prior != nil && bytes.Equal(in.Prior.Params, b.params)
	warmDocs := in.WarmDocs && sameParams
	warmZoom := sameParams && in.Prior.Shape == in.Grouping.Shape
	m := &Map{Kind: store.RunKindFresh, Params: b.params, Docs: make([]store.MapPosition, len(in.Points))}
	if warmDocs && in.Unchanged && b.samePoints() {
		m.Kind = store.RunKindWarm
		if warmZoom && b.sameGrouping() {
			return b.priorMap(m), nil
		}
		for i, p := range in.Points {
			prior := in.Prior.Docs[p.ID].Map
			m.Docs[i].MapX, m.Docs[i].MapY = prior.MapX, prior.MapY
		}
	} else if err := b.docMap(m, warmDocs); err != nil {
		return nil, err
	}
	if err := b.zoom(m, warmZoom); err != nil {
		return nil, err
	}
	b.anchors(m)
	return m, nil
}

// samePoints reports whether the points are the prior's documents.
func (b mapBuild) samePoints() bool {
	if len(b.in.Points) != len(b.in.Prior.Docs) {
		return false
	}
	for _, p := range b.in.Points {
		if _, ok := b.in.Prior.Docs[p.ID]; !ok {
			return false
		}
	}
	return true
}

// sameGrouping reports whether the grouping is the prior's: the same
// identities with the same parents, every document in the same interest
// and area with the same fit.
func (b mapBuild) sameGrouping() bool {
	in, prior := b.in, b.in.Prior
	if len(in.AreaKeys)+len(in.InterestKeys) != len(prior.Groups) {
		return false
	}
	parents := b.parents()
	for l, key := range in.InterestKeys {
		g, ok := prior.Groups[key]
		if !ok || g.Level != store.InterestLevelInterest || g.ParentID != parents[l] {
			return false
		}
	}
	for _, key := range in.AreaKeys {
		if g, ok := prior.Groups[key]; !ok || g.Level != store.InterestLevelArea {
			return false
		}
	}
	for i, p := range in.Points {
		d, f := prior.Docs[p.ID], in.Fits[i]
		interest, area := "", ""
		if f.Kind != FitUnsorted {
			interest = in.InterestKeys[f.Interest]
		}
		if a := in.Grouping.Area[i]; a != NoiseLabel {
			area = in.AreaKeys[a]
		}
		if d.Fit != store.InterestFit(f.Kind) || d.InterestID != interest || d.AreaID != area {
			return false
		}
	}
	return true
}

// parents are each interest's area key, "" in the flat shape.
func (b mapBuild) parents() []string {
	out := make([]string, len(b.in.InterestKeys))
	for i, f := range b.in.Fits {
		if f.Kind == FitMember {
			if a := b.in.Grouping.Area[i]; a != NoiseLabel {
				out[f.Interest] = b.in.AreaKeys[a]
			}
		}
	}
	return out
}

// priorMap fills m with the prior's map, verbatim.
func (b mapBuild) priorMap(m *Map) *Map {
	prior := b.in.Prior
	m.DotRadius, m.Unsorted = prior.DotRadius, prior.Unsorted
	for i, p := range b.in.Points {
		m.Docs[i] = prior.Docs[p.ID].Map
	}
	m.Areas = make([]store.GroupMap, len(b.in.AreaKeys))
	for a, key := range b.in.AreaKeys {
		m.Areas[a] = prior.Groups[key].Map
	}
	m.Interests = make([]store.GroupMap, len(b.in.InterestKeys))
	for l, key := range b.in.InterestKeys {
		m.Interests[l] = prior.Groups[key].Map
	}
	return m
}

// docMap draws the document map into m: from the grouping's neighbour
// lists, or one pass of its own. m is warm when the layout started from
// the prior, which a warm one does only when the prior shares a document.
func (b mapBuild) docMap(m *Map, warm bool) error {
	in := b.in
	lists := in.Grouping.Neighbours
	if lists == nil {
		var err error
		if lists, err = nearestNeighbours(b.ctx, vectorsOf(in.Points)); err != nil {
			return fmt.Errorf("insight: the map's nearest neighbours: %w", err)
		}
	}
	dm := layout.DocMapInput{Keys: idsOf(in.Points), Vectors: vectorsOf(in.Points), Neighbours: layoutNeighbours(lists),
		Warm: warm, Seed: in.Seed}
	if in.Prior != nil {
		dm.Prior = make(map[string]layout.XY, len(in.Prior.Docs))
		for id, d := range in.Prior.Docs {
			dm.Prior[id] = layout.XY{X: d.Map.MapX, Y: d.Map.MapY}
		}
	}
	l, err := layout.DocMap(b.ctx, dm)
	if err != nil {
		return fmt.Errorf("insight: the document map: %w", err)
	}
	if l.Warm {
		m.Kind = store.RunKindWarm
	}
	for i, p := range l.Points {
		m.Docs[i].MapX, m.Docs[i].MapY = p.X, p.Y
	}
	return nil
}

// layoutNeighbours are the neighbour lists as package layout takes them.
func layoutNeighbours(lists [][]louvain.Edge) [][]layout.Neighbour {
	out := make([][]layout.Neighbour, len(lists))
	for i, es := range lists {
		out[i] = make([]layout.Neighbour, len(es))
		for k, e := range es {
			out[i][k] = layout.Neighbour{Index: e.To, Similarity: e.Weight}
		}
	}
	return out
}

// zoom draws the zoom view into m, from the prior's when warm.
func (b mapBuild) zoom(m *Map, warm bool) error {
	in := b.in
	z := layout.ZoomInput{Keys: idsOf(in.Points), Vectors: vectorsOf(in.Points), Interest: make([]int, len(in.Points)),
		Nearest: make([]int, len(in.Points)), Similarity: make([]float64, len(in.Points)), Seed: in.Seed}
	for i, f := range in.Fits {
		z.Interest[i], z.Nearest[i], z.Similarity[i] = f.Interest, -1, f.Similarity
		if f.Kind == FitUnsorted {
			z.Interest[i], z.Nearest[i] = -1, f.Interest
		}
	}
	areaOf := b.areaOf()
	for l, key := range in.InterestKeys {
		z.Interests = append(z.Interests, layout.Group{Key: key, Start: startOf(in.InterestStarts, l), Area: areaOf[l],
			Centroid: in.Centroids[l]})
	}
	areaCentroids, err := b.areaCentroids(areaOf)
	if err != nil {
		return err
	}
	for a, c := range areaCentroids {
		z.Areas = append(z.Areas, layout.Group{Key: in.AreaKeys[a], Start: startOf(in.AreaStarts, a), Area: -1,
			Centroid: c})
	}
	if warm {
		z.Prior = &layout.ZoomPrior{DotRadius: in.Prior.DotRadius, Places: make(map[string]layout.Circle, len(in.Prior.Groups))}
		for key, g := range in.Prior.Groups {
			z.Prior.Places[key] = layout.Circle{X: g.Map.ZoomX, Y: g.Map.ZoomY, R: g.Map.ZoomR}
		}
	}
	view, err := layout.Zoom(b.ctx, z)
	if err != nil {
		return fmt.Errorf("insight: the zoom view: %w", err)
	}
	m.DotRadius = view.DotRadius
	m.Unsorted = store.Circle{X: view.Unsorted.X, Y: view.Unsorted.Y, R: view.Unsorted.R}
	for i, p := range view.Docs {
		m.Docs[i].ZoomX, m.Docs[i].ZoomY = p.X, p.Y
	}
	m.Areas = make([]store.GroupMap, len(view.Areas))
	for a, c := range view.Areas {
		m.Areas[a] = store.GroupMap{ZoomX: c.X, ZoomY: c.Y, ZoomR: c.R}
	}
	m.Interests = make([]store.GroupMap, len(view.Interests))
	for l, c := range view.Interests {
		m.Interests[l] = store.GroupMap{ZoomX: c.X, ZoomY: c.Y, ZoomR: c.R}
	}
	return nil
}

func startOf(starts []string, i int) string {
	if starts == nil {
		return ""
	}
	return starts[i]
}

// areaOf is each interest's area, -1 in the flat shape: its members' area.
func (b mapBuild) areaOf() []int {
	out := make([]int, len(b.in.InterestKeys))
	for l := range out {
		out[l] = -1
	}
	if b.in.Grouping.Shape != ShapeAreas {
		return out
	}
	for i, f := range b.in.Fits {
		if f.Kind == FitMember {
			out[f.Interest] = b.in.Grouping.Area[i]
		}
	}
	return out
}

// areaCentroids are each area's centroid: the unit mean of its interests'
// members.
func (b mapBuild) areaCentroids(areaOf []int) ([][]float32, error) {
	labels := make([]int, len(b.in.Points))
	for i, f := range b.in.Fits {
		labels[i] = NoiseLabel
		if f.Kind == FitMember && areaOf[f.Interest] >= 0 {
			labels[i] = areaOf[f.Interest]
		}
	}
	cents, err := Centroids(b.in.Points, labels)
	if err != nil {
		return nil, fmt.Errorf("insight: area centroids: %w", err)
	}
	if len(cents) != len(b.in.AreaKeys) {
		return nil, fmt.Errorf("insight: %d of %d areas hold an interest's member", len(cents), len(b.in.AreaKeys))
	}
	return cents, nil
}

// anchors gives each area and interest its label's anchor on the document
// map: the coordinate-wise median of its members' places (an area's, its
// interests' members'), snapped to the member nearest it, ties to the
// lower document ID, so a label sits on its documents even when the median
// falls between two islands.
func (b mapBuild) anchors(m *Map) {
	in := b.in
	areaOf := b.areaOf()
	interestMembers := make([][]int, len(in.InterestKeys))
	areaMembers := make([][]int, len(in.AreaKeys))
	for i, f := range in.Fits {
		if f.Kind != FitMember {
			continue
		}
		interestMembers[f.Interest] = append(interestMembers[f.Interest], i)
		if a := areaOf[f.Interest]; a >= 0 {
			areaMembers[a] = append(areaMembers[a], i)
		}
	}
	for l, members := range interestMembers {
		m.Interests[l].AnchorX, m.Interests[l].AnchorY = b.anchor(m, members)
	}
	for a, members := range areaMembers {
		m.Areas[a].AnchorX, m.Areas[a].AnchorY = b.anchor(m, members)
	}
}

func (b mapBuild) anchor(m *Map, members []int) (float64, float64) {
	if len(members) == 0 {
		return layout.Extent / 2, layout.Extent / 2
	}
	xs, ys := make([]float64, len(members)), make([]float64, len(members))
	for k, i := range members {
		xs[k], ys[k] = m.Docs[i].MapX, m.Docs[i].MapY
	}
	slices.Sort(xs)
	slices.Sort(ys)
	mx, my := xs[len(xs)/2], ys[len(ys)/2]
	byID := slices.Clone(members)
	slices.SortFunc(byID, func(a, c int) int { return strings.Compare(b.in.Points[a].ID, b.in.Points[c].ID) })
	best, bestD := byID[0], math.Inf(1)
	for _, i := range byID {
		if d := math.Hypot(m.Docs[i].MapX-mx, m.Docs[i].MapY-my); d < bestD {
			best, bestD = i, d
		}
	}
	return m.Docs[best].MapX, m.Docs[best].MapY
}

// Validate checks m is a whole map of numDocs documents, numAreas areas
// and numInterests interests: every place on the map, every circle of a
// positive radius.
func (m *Map) Validate(numDocs, numAreas, numInterests int) error {
	switch {
	case m == nil:
		return errors.New("insight: no map")
	case len(m.Docs) != numDocs || len(m.Areas) != numAreas || len(m.Interests) != numInterests:
		return fmt.Errorf("insight: a map of %d documents, %d areas and %d interests for %d, %d and %d",
			len(m.Docs), len(m.Areas), len(m.Interests), numDocs, numAreas, numInterests)
	case !m.Kind.Valid():
		return fmt.Errorf("insight: a map of kind %q", m.Kind)
	case !positive(m.DotRadius) || !positive(m.Unsorted.R) || !onMap(m.Unsorted.X, m.Unsorted.Y, m.Unsorted.R):
		return fmt.Errorf("insight: the map's dot radius %g or Unsorted's disc %+v is not on the map", m.DotRadius, m.Unsorted)
	}
	for i, d := range m.Docs {
		if !onMap(d.MapX, d.MapY, 0) || !onMap(d.ZoomX, d.ZoomY, 0) {
			return fmt.Errorf("insight: document %d's place %+v is not on the map", i, d)
		}
	}
	for i, g := range slices.Concat(m.Areas, m.Interests) {
		if !positive(g.ZoomR) || !onMap(g.ZoomX, g.ZoomY, g.ZoomR) || !onMap(g.AnchorX, g.AnchorY, 0) {
			return fmt.Errorf("insight: group %d's place %+v is not on the map", i, g)
		}
	}
	return nil
}

func positive(x float64) bool { return x > 0 && !math.IsInf(x, 1) }

// onMap reports whether a circle (a point when r is 0) lies inside the
// map.
func onMap(x, y, r float64) bool {
	ok := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	return ok(x) && ok(y) && ok(r) && x-r >= 0 && x+r <= store.MapExtent && y-r >= 0 && y+r <= store.MapExtent
}

// similarTo returns each interest's SimilarInterests most similar other
// interests by the cosine of their centroids, ties to the lower number.
func similarTo(centroids [][]float32) [][]similarity {
	out := make([][]similarity, len(centroids))
	for l, c := range centroids {
		others := make([]similarity, 0, len(centroids)-1)
		for j, d := range centroids {
			if j != l {
				others = append(others, similarity{interest: j, cosine: dot(c, d)})
			}
		}
		slices.SortStableFunc(others, func(a, b similarity) int { return cmp.Compare(b.cosine, a.cosine) })
		out[l] = others[:min(SimilarInterests, len(others))]
	}
	return out
}

// similarity is another interest and its cosine.
type similarity struct {
	interest int
	cosine   float64
}
