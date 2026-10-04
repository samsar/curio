package layout

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
)

// The zoom view's constants, in dot radii: every dot is a circle of radius 1
// until the view is fit to the map.
const (
	// minUnsortedSlots is the fewest documents Unsorted's disc has room
	// for, so documents placed after the regrouping find room.
	minUnsortedSlots = 24
	// The gaps between circles' edges: interests in an area, or at the top
	// in the flat shape; areas; and the content and Unsorted's disc. An
	// area's rim stands areaPadding off its interests.
	interestGap = 3.0
	areaGap     = 8.0
	areaPadding = 3.0
	unsortedGap = 12.0
	// orientingSimilar is how many of an interest's most similar interests
	// its documents turn toward.
	orientingSimilar = 6
	// maxDotShare caps a dot's radius at this share of Extent.
	maxDotShare = 0.01
	// A new group of a warm layout starts at the weighted mean of its
	// startNeighbours most similar placed groups.
	startNeighbours = 3
	// minDistance is the distance between centroids under which two groups
	// count as one place when the layout is scaled to the circles.
	minDistance = 1e-6
	// maxFitScale holds a warm level's fitted scale within this factor of
	// the touching scale, either way. Two placed groups at nearly one
	// place but drawn apart would otherwise inflate the fit without bound,
	// and a warm view never compacts. On the owner's library the fit is
	// 1.1 to 1.9 times the touching scale.
	maxFitScale = 4.0
)

// Group is an area or an interest of the grouping the zoom view draws.
type Group struct {
	// Key names the group across groupings: it finds the group's place in
	// a previous view, and nothing else.
	Key string
	// Start is the key of the previous group whose place a new group
	// starts at, "" for none.
	Start string
	// Area is an interest's area, an index of ZoomInput.Areas; -1 in the
	// flat shape. Ignored for an area.
	Area int
	// Centroid is the group's centroid, a unit vector of the documents'
	// width.
	Centroid []float32
}

// ZoomPrior is a previous zoom view to start from.
type ZoomPrior struct {
	// DotRadius is its dots' radius on its map.
	DotRadius float64
	// Places are its groups' circles by key, on its map.
	Places map[string]Circle
}

// ZoomInput is what Zoom draws: documents, the interests they are in, and
// the areas holding the interests (none in the flat shape).
type ZoomInput struct {
	// Keys name the documents, uniquely.
	Keys []string
	// Vectors are the documents' vectors, of one width.
	Vectors [][]float32
	// Interest is each document's interest, an index of Interests: a
	// member or a loose fit of it; -1 for a document in none (Unsorted).
	Interest []int
	// Nearest is an Unsorted document's nearest interest, -1 for none;
	// ignored for the others.
	Nearest []int
	// Similarity is an Unsorted document's cosine to Nearest: the most
	// similar sit nearest the rim. Ignored for the others.
	Similarity []float64
	// Interests and Areas are the groups. Every interest holds a document,
	// and every area an interest.
	Interests []Group
	Areas     []Group
	// Prior is the previous view of the same shape to start from, nil for
	// none.
	Prior *ZoomPrior
	// Seed seeds the principal axes' starts.
	Seed uint64
}

// ZoomLayout is the zoom view, on the map: the dots' radius, a circle per
// interest and per area (index-aligned with the input's), Unsorted's disc,
// and a dot per document (index-aligned with ZoomInput.Keys).
type ZoomLayout struct {
	DotRadius float64
	Interests []Circle
	Areas     []Circle
	Unsorted  Circle
	Docs      []XY
}

// Zoom draws the zoom view of in.
func Zoom(ctx context.Context, in ZoomInput) (ZoomLayout, error) {
	if err := ctx.Err(); err != nil {
		return ZoomLayout{}, fmt.Errorf("layout: zoom view: %w", err)
	}
	z, err := newZoomer(ctx, in)
	if err != nil {
		return ZoomLayout{}, err
	}
	return z.draw()
}

// zoomGroup is a group in the zoomer's order.
type zoomGroup struct {
	key, start string
	centroid   []float32
	orig       int   // the input's index
	area       int   // an interest's area, the zoomer's index; -1 for none
	docs       []int // an interest's documents, ascending
	interests  []int // an area's interests, in order
	radius     float64
	slots      int
}

// zoomer is a Zoom call's work: the documents in key order, the groups in
// the order of their contents (more documents first, then the one whose
// smallest document key sorts first), and their places in dot radii.
type zoomer struct {
	ctx       context.Context
	keys      []string
	vectors   [][]float32
	origDoc   []int     // a document's index in the input
	nearest   []int     // an Unsorted document's nearest interest, -1 for none
	sim       []float64 // an Unsorted document's similarity to it
	interests []zoomGroup
	areas     []zoomGroup
	unsorted  []int
	prior     *ZoomPrior
	seed      uint64
	lat       *lattice
}

func newZoomer(ctx context.Context, in ZoomInput) (*zoomer, error) {
	order, err := in.check()
	if err != nil {
		return nil, err
	}
	n := len(order)
	z := &zoomer{ctx: ctx, keys: make([]string, n), vectors: make([][]float32, n), origDoc: order,
		nearest: make([]int, n), sim: make([]float64, n), prior: in.Prior, seed: in.Seed}
	interestOf := make([]int, n)
	for k, i := range order {
		z.keys[k], z.vectors[k], z.sim[k] = in.Keys[i], in.Vectors[i], in.Similarity[i]
		interestOf[k] = in.Interest[i]
	}
	byInput := make([][]int, len(in.Interests))
	for k, l := range interestOf {
		if l >= 0 {
			byInput[l] = append(byInput[l], k)
		}
	}
	z.interests = groupsInOrder(in.Interests, byInput)
	rank := make([]int, len(in.Interests)) // an input interest's index in z.interests
	for x, g := range z.interests {
		rank[g.orig] = x
	}
	for k, i := range order {
		z.nearest[k] = -1
		switch {
		case interestOf[k] >= 0:
		case in.Nearest[i] >= 0:
			z.nearest[k] = rank[in.Nearest[i]]
			z.unsorted = append(z.unsorted, k)
		default:
			z.unsorted = append(z.unsorted, k)
		}
	}
	if len(in.Areas) > 0 {
		docsOf := make([][]int, len(in.Areas))
		for _, g := range z.interests {
			a := in.Interests[g.orig].Area
			docsOf[a] = append(docsOf[a], g.docs...)
		}
		for a := range docsOf {
			slices.Sort(docsOf[a])
		}
		z.areas = groupsInOrder(in.Areas, docsOf)
		areaRank := make([]int, len(in.Areas))
		for x, g := range z.areas {
			areaRank[g.orig] = x
		}
		for x := range z.interests {
			a := areaRank[in.Interests[z.interests[x].orig].Area]
			z.interests[x].area = a
			z.areas[a].interests = append(z.areas[a].interests, x)
		}
	}
	most := max(len(z.unsorted), minUnsortedSlots)
	for _, g := range z.interests {
		most = max(most, len(g.docs))
	}
	z.lat = newLattice(slotsFor(most))
	for x := range z.interests {
		z.interests[x].radius, z.interests[x].slots = z.lat.circleFor(len(z.interests[x].docs))
	}
	return z, nil
}

// groupsInOrder returns the groups, each with its documents docs[i]
// (ascending, at least one), more documents first, then the group whose
// first document comes first.
func groupsInOrder(groups []Group, docs [][]int) []zoomGroup {
	out := make([]zoomGroup, len(groups))
	for i, g := range groups {
		out[i] = zoomGroup{key: g.Key, start: g.Start, centroid: g.Centroid, orig: i, area: -1, docs: docs[i]}
	}
	slices.SortFunc(out, func(a, b zoomGroup) int {
		return cmp.Or(cmp.Compare(len(b.docs), len(a.docs)), cmp.Compare(a.docs[0], b.docs[0]))
	})
	return out
}

// check validates the input and returns the documents' key order.
func (in ZoomInput) check() ([]int, error) {
	n := len(in.Keys)
	if len(in.Interest) != n || len(in.Nearest) != n || len(in.Similarity) != n {
		return nil, inputError("%d interests, %d nearest and %d similarities for %d documents",
			len(in.Interest), len(in.Nearest), len(in.Similarity), n)
	}
	if err := checkVectors(in.Vectors, n); err != nil {
		return nil, err
	}
	order, err := keyOrder(in.Keys)
	if err != nil {
		return nil, err
	}
	held := make([]int, len(in.Interests))
	for i := range n {
		switch l, near := in.Interest[i], in.Nearest[i]; {
		case l < -1 || l >= len(in.Interests):
			return nil, inputError("document %d is in interest %d of %d", i, l, len(in.Interests))
		case l == -1 && (near < -1 || near >= len(in.Interests)):
			return nil, inputError("document %d is nearest interest %d of %d", i, near, len(in.Interests))
		case l >= 0:
			held[l]++
		}
		if !finite(in.Similarity[i]) {
			return nil, inputError("document %d has a non-finite similarity", i)
		}
	}
	if err := checkGroups(in, held); err != nil {
		return nil, err
	}
	return order, in.Prior.check()
}

// checkGroups validates the groups: keys unique and not empty, centroids of
// the documents' width, every interest holding a document and in an area
// when there are areas, and every area holding an interest.
func checkGroups(in ZoomInput, held []int) error {
	keys := make(map[string]bool, len(in.Interests)+len(in.Areas))
	inArea := make([]int, len(in.Areas))
	dim := -1
	if len(in.Vectors) > 0 {
		dim = len(in.Vectors[0])
	}
	for level, groups := range [][]Group{in.Interests, in.Areas} {
		for i, g := range groups {
			switch {
			case g.Key == "" || keys[g.Key]:
				return inputError("group key %q is empty or appears twice", g.Key)
			case dim >= 0 && len(g.Centroid) != dim || dim < 0 && len(g.Centroid) == 0:
				return inputError("group %q has a centroid of dim %d", g.Key, len(g.Centroid))
			}
			keys[g.Key] = true
			for _, x := range g.Centroid {
				if !finite(float64(x)) {
					return inputError("group %q has a non-finite centroid", g.Key)
				}
			}
			if level == 1 {
				continue
			}
			switch {
			case held[i] == 0:
				return inputError("interest %d holds no document", i)
			case len(in.Areas) == 0 && g.Area != -1, len(in.Areas) > 0 && (g.Area < 0 || g.Area >= len(in.Areas)):
				return inputError("interest %d is in area %d of %d", i, g.Area, len(in.Areas))
			case len(in.Areas) > 0:
				inArea[g.Area]++
			}
		}
	}
	if a := slices.Index(inArea, 0); a >= 0 {
		return inputError("area %d holds no interest", a)
	}
	return nil
}

func (p *ZoomPrior) check() error {
	if p == nil {
		return nil
	}
	if !finite(p.DotRadius) || p.DotRadius <= 0 {
		return inputError("the prior's dot radius %g is not positive", p.DotRadius)
	}
	for key, c := range p.Places {
		if !finite(c.X, c.Y, c.R) || c.R <= 0 {
			return inputError("the prior place of %q is not a circle", key)
		}
	}
	return nil
}

// draw lays the view out and fits it to the map.
func (z *zoomer) draw() (ZoomLayout, error) {
	interestAt, areaAt, areaR, err := z.placeGroups()
	if err != nil {
		return ZoomLayout{}, err
	}
	content := emptyBox()
	if len(z.areas) > 0 {
		for a, at := range areaAt {
			content.add(at.X, at.Y, areaR[a])
		}
	} else {
		for i, at := range interestAt {
			content.add(at.X, at.Y, z.interests[i].radius)
		}
	}
	unsortedR, unsortedSlots := z.lat.circleFor(max(len(z.unsorted), minUnsortedSlots))
	unsorted := Circle{R: unsortedR}
	if !content.empty() {
		unsorted.X, unsorted.Y = content.x1+unsortedGap+unsortedR, (content.y0+content.y1)/2
	}
	docs, err := z.placeDocs(interestAt, unsorted, unsortedSlots)
	if err != nil {
		return ZoomLayout{}, err
	}
	whole := content
	whole.add(unsorted.X, unsorted.Y, unsorted.R)
	f := fitBox(whole, maxDotShare*Extent)
	out := ZoomLayout{DotRadius: f.dotRadius(), Interests: make([]Circle, len(z.interests)),
		Areas: make([]Circle, len(z.areas)), Unsorted: f.circle(unsorted), Docs: make([]XY, len(docs))}
	for i, at := range interestAt {
		out.Interests[z.interests[i].orig] = f.circle(Circle{at.X, at.Y, z.interests[i].radius})
	}
	for a, at := range areaAt {
		out.Areas[z.areas[a].orig] = f.circle(Circle{at.X, at.Y, areaR[a]})
	}
	for k, p := range docs {
		out.Docs[z.origDoc[k]] = f.point(p)
	}
	if err := out.check(); err != nil {
		return ZoomLayout{}, err
	}
	if err := z.checkPacked(out); err != nil {
		return ZoomLayout{}, err
	}
	return out, nil
}

// check fails unless every circle and dot of the view is on the map.
func (l ZoomLayout) check() error {
	if !finite(l.DotRadius) || l.DotRadius <= 0 {
		return fmt.Errorf("layout: the dot radius %g is not positive", l.DotRadius)
	}
	if err := checkCircle("Unsorted's disc", l.Unsorted); err != nil {
		return err
	}
	for i, c := range l.Interests {
		if err := checkCircle(fmt.Sprintf("interest %d", i), c); err != nil {
			return err
		}
	}
	for a, c := range l.Areas {
		if err := checkCircle(fmt.Sprintf("area %d", a), c); err != nil {
			return err
		}
	}
	for k, p := range l.Docs {
		if err := checkPoint(fmt.Sprintf("document %d", k), p); err != nil {
			return err
		}
	}
	return nil
}

// roundingSlack is the most that rounding to 0.01 takes from the room
// between two circles, or between a circle and a circle or dot inside it:
// each centre moves up to 0.005·√2 and each radius up to 0.01.
const roundingSlack = 0.035

// checkPacked fails unless the view keeps what the packing and the lattice
// guarantee, up to rounding: an area's interests inside it and apart, the
// top-level circles and Unsorted's disc apart, and every dot inside its
// circle. A view that broke one would draw overlapping circles, so its map
// fails instead.
func (z *zoomer) checkPacked(l ZoomLayout) error {
	top := l.Interests
	if len(z.areas) > 0 {
		top = l.Areas
	}
	if i, j, ok := apart(slices.Concat(top, []Circle{l.Unsorted})); !ok {
		return fmt.Errorf("layout: top-level circles %d and %d overlap (%d is Unsorted's)", i, j, len(top))
	}
	for _, area := range z.areas {
		inner := make([]Circle, len(area.interests))
		for x, i := range area.interests {
			inner[x] = l.Interests[z.interests[i].orig]
			if !within(inner[x], l.Areas[area.orig]) {
				return fmt.Errorf("layout: interest %d lies outside area %d", z.interests[i].orig, area.orig)
			}
		}
		if x, y, ok := apart(inner); !ok {
			return fmt.Errorf("layout: interests %d and %d of area %d overlap",
				z.interests[area.interests[x]].orig, z.interests[area.interests[y]].orig, area.orig)
		}
	}
	for _, g := range z.interests {
		if err := z.dotsWithin(l, g.docs, l.Interests[g.orig]); err != nil {
			return err
		}
	}
	return z.dotsWithin(l, z.unsorted, l.Unsorted)
}

// apart reports whether no two circles overlap, and, when two do, which.
func apart(cs []Circle) (int, int, bool) {
	for i, a := range cs {
		for j := i + 1; j < len(cs); j++ {
			if b := cs[j]; math.Hypot(a.X-b.X, a.Y-b.Y) < a.R+b.R-roundingSlack {
				return i, j, false
			}
		}
	}
	return 0, 0, true
}

// within reports whether inner lies inside outer.
func within(inner, outer Circle) bool {
	return math.Hypot(inner.X-outer.X, inner.Y-outer.Y)+inner.R <= outer.R+roundingSlack
}

// dotsWithin fails unless every one of docs has its dot inside c.
func (z *zoomer) dotsWithin(l ZoomLayout, docs []int, c Circle) error {
	for _, k := range docs {
		p := l.Docs[z.origDoc[k]]
		if !within(Circle{p.X, p.Y, l.DotRadius}, c) {
			return fmt.Errorf("layout: document %d lies outside its circle", z.origDoc[k])
		}
	}
	return nil
}

// placeGroups places the groups, in dot radii: each interest's centre, and
// each area's centre and radius. In the areas shape, each area's interests
// are laid out inside it first, then the areas; in the flat shape the
// interests are the top level.
func (z *zoomer) placeGroups() (interestAt, areaAt []XY, areaR []float64, err error) {
	if len(z.areas) == 0 {
		interestAt, err = z.arrange(z.interests, nil, false, interestGap)
		return interestAt, nil, nil, err
	}
	local := make([]XY, len(z.interests))
	areaR = make([]float64, len(z.areas))
	for a := range z.areas {
		at, r, err := z.areaInterior(a)
		if err != nil {
			return nil, nil, nil, err
		}
		for x, i := range z.areas[a].interests {
			local[i] = at[x]
		}
		areaR[a] = r
		z.areas[a].radius = r
	}
	if areaAt, err = z.arrange(z.areas, nil, false, areaGap); err != nil {
		return nil, nil, nil, err
	}
	interestAt = make([]XY, len(z.interests))
	for i, g := range z.interests {
		interestAt[i] = XY{areaAt[g.area].X + local[i].X, areaAt[g.area].Y + local[i].Y}
	}
	return interestAt, areaAt, areaR, nil
}

// areaInterior lays out area a's interests around its centre and returns
// their centres and the area's radius.
func (z *zoomer) areaInterior(a int) ([]XY, float64, error) {
	members := make([]zoomGroup, len(z.areas[a].interests))
	for x, i := range z.areas[a].interests {
		members[x] = z.interests[i]
	}
	// An interest starts at its previous place only inside the area's
	// previous circle: one that moved areas starts anew.
	var frame *Circle
	if c, ok := z.priorPlace(z.areas[a]); ok {
		frame = &c
	}
	at, err := z.arrange(members, frame, true, interestGap)
	if err != nil {
		return nil, 0, err
	}
	box := emptyBox()
	for x, p := range at {
		box.add(p.X, p.Y, members[x].radius)
	}
	c := XY{(box.x0 + box.x1) / 2, (box.y0 + box.y1) / 2}
	var r float64
	for x := range at {
		at[x] = XY{at[x].X - c.X, at[x].Y - c.Y}
		r = math.Max(r, math.Hypot(at[x].X, at[x].Y)+members[x].radius)
	}
	return at, r + areaPadding, nil
}

// priorPlace is g's circle in the prior, in dot radii: its own place, or
// its start's.
func (z *zoomer) priorPlace(g zoomGroup) (Circle, bool) {
	if z.prior == nil {
		return Circle{}, false
	}
	for _, key := range []string{g.key, g.start} {
		if c, ok := z.prior.Places[key]; ok && key != "" {
			d := z.prior.DotRadius
			return Circle{c.X / d, c.Y / d, c.R / d}, true
		}
	}
	return Circle{}, false
}

// arrange places circles for groups gs, gap apart: the top level, or the
// interests inside an area (interior), whose previous area is frame, nil
// when it has none. Cold, it places their centroids by MDS (at the top,
// metric MDS: stress majorization seeded by classical; inside an area,
// classical alone, the centroids' principal plane), scales that to the
// circles, packs and compacts them.
// Warm (a prior holding one of them), each starts at its previous place, an
// interior one relative to frame's centre and only when it lay inside
// frame, a new one beside its most similar placed groups and then by MDS
// against the others, which stay put; the circles are packed, uncompacted,
// the whole turned and moved (never scaled: radii are absolute) back onto
// the previous places, and packed once more, so the packing, not the
// transform, has the last word on overlaps. Refining the carried places
// toward MDS's distances as well moved the interests' centres eight times
// as far.
func (z *zoomer) arrange(gs []zoomGroup, frame *Circle, interior bool, gap float64) ([]XY, error) {
	n := len(gs)
	if n == 0 {
		return nil, nil
	}
	r := make([]float64, n)
	for i, g := range gs {
		r[i] = g.radius
	}
	dm, err := cosineDistances(z.ctx, gs)
	if err != nil {
		return nil, err
	}
	starts, placed := z.starts(gs, frame, interior)
	if !slices.Contains(placed, true) {
		return z.arrangeCold(gs, dm, r, gap, !interior)
	}
	return z.arrangeWarm(dm, r, gap, starts, placed)
}

// starts are the groups' previous places, an interior group's relative to
// frame's centre, and which of them have one.
func (z *zoomer) starts(gs []zoomGroup, frame *Circle, interior bool) ([]XY, []bool) {
	starts := make([]XY, len(gs))
	placed := make([]bool, len(gs))
	if z.prior == nil || interior && frame == nil {
		return starts, placed
	}
	for i, g := range gs {
		c, ok := z.priorPlace(g)
		if !ok {
			continue
		}
		if interior {
			if math.Hypot(c.X-frame.X, c.Y-frame.Y)+c.R > frame.R {
				continue
			}
			c.X, c.Y = c.X-frame.X, c.Y-frame.Y
		}
		starts[i], placed[i] = XY{c.X, c.Y}, true
	}
	return starts, placed
}

func (z *zoomer) arrangeCold(gs []zoomGroup, dm [][]float64, r []float64, gap float64, metric bool) ([]XY, error) {
	n := len(gs)
	if n == 1 {
		return []XY{{}}, nil
	}
	centroids := make([][]float32, n)
	for i, g := range gs {
		centroids[i] = g.centroid
	}
	pos, err := principalPlane(z.ctx, centroids, z.seed)
	if err != nil {
		return nil, err
	}
	target := dm
	if metric {
		if pos, err = stressMajorize(z.ctx, dm, pos, nil, stressIterations); err != nil {
			return nil, err
		}
	} else {
		target = chords(dm)
	}
	s := touchingScale(target, r, gap)
	for i := range pos {
		pos[i] = XY{pos[i].X * s, pos[i].Y * s}
	}
	if pos, err = separate(z.ctx, pos, r, gap); err != nil {
		return nil, err
	}
	return pos, compact(z.ctx, pos, r, gap)
}

// chords are the distances |a − b| between unit vectors a cosine distance
// apart, √(2·dm): what classical MDS lays out.
func chords(dm [][]float64) [][]float64 {
	out := make([][]float64, len(dm))
	for i := range dm {
		out[i] = make([]float64, len(dm))
		for j, d := range dm[i] {
			out[i][j] = math.Sqrt(2 * d)
		}
	}
	return out
}

func (z *zoomer) arrangeWarm(dm [][]float64, r []float64, gap float64, starts []XY, placed []bool) ([]XY, error) {
	n := len(r)
	pos := slices.Clone(starts)
	mobile := make([]bool, n)
	for i := range n {
		if placed[i] {
			continue
		}
		if err := z.ctx.Err(); err != nil {
			return nil, fmt.Errorf("layout: starting new groups: %w", err)
		}
		pos[i], mobile[i] = besidePlaced(dm, starts, placed, i), true
	}
	if slices.Contains(mobile, true) {
		var err error
		if pos, err = stressMajorize(z.ctx, scaledDistances(dm, starts, placed, r, gap), pos, mobile, stressIterations); err != nil {
			return nil, err
		}
	}
	pos, err := separate(z.ctx, pos, r, gap)
	if err != nil {
		return nil, err
	}
	var src, dst []XY
	for i := range n {
		if placed[i] {
			src, dst = append(src, pos[i]), append(dst, starts[i])
		}
	}
	t := procrustes(src, dst, false)
	for i := range pos {
		pos[i] = t.apply(pos[i])
	}
	// The alignment is rigid, so this moves nothing that rounding didn't
	// bring within the gap; it is here so that no overlap ever rests on
	// the transform being exact.
	return separate(z.ctx, pos, r, gap)
}

// besidePlaced is the similarity-weighted mean of the starts of group i's
// startNeighbours most similar placed groups, ties to the lower index.
func besidePlaced(dm [][]float64, starts []XY, placed []bool, i int) XY {
	var near []int
	for j := range starts {
		if placed[j] {
			near = append(near, j)
		}
	}
	slices.SortStableFunc(near, func(a, b int) int { return cmp.Compare(dm[i][a], dm[i][b]) })
	var sum XY
	var weights float64
	for _, j := range near[:min(startNeighbours, len(near))] {
		w := math.Max(1-dm[i][j], 0.01)
		sum.X += w * starts[j].X
		sum.Y += w * starts[j].Y
		weights += w
	}
	return XY{sum.X / weights, sum.Y / weights}
}

// scaledDistances are dm in dot radii: scaled by the least-squares fit of
// the placed groups' distances to theirs, held within maxFitScale of the
// scale at which similar circles about touch, or, with fewer than two
// placed groups apart, by that scale.
func scaledDistances(dm [][]float64, starts []XY, placed []bool, r []float64, gap float64) [][]float64 {
	var num, den float64
	for i := range starts {
		for j := i + 1; j < len(starts); j++ {
			if placed[i] && placed[j] {
				num += dist(starts[i], starts[j]) * dm[i][j]
				den += dm[i][j] * dm[i][j]
			}
		}
	}
	touching := touchingScale(dm, r, gap)
	s := touching
	if den > 0 && num > 0 {
		s = min(max(num/den, touching/maxFitScale), touching*maxFitScale)
	}
	out := make([][]float64, len(dm))
	for i := range dm {
		out[i] = make([]float64, len(dm))
		for j := range dm[i] {
			out[i][j] = s * dm[i][j]
		}
	}
	return out
}

// touchingScale is the factor that brings the median pair of circles, at
// the distances dm, to touch, gap apart. Pairs closer than minDistance are
// one place, left to the packing; with none farther, it is 1.
func touchingScale(dm [][]float64, r []float64, gap float64) float64 {
	var ratios []float64
	for i := range r {
		for j := i + 1; j < len(r); j++ {
			if d := dm[i][j]; d >= minDistance {
				ratios = append(ratios, (r[i]+r[j]+gap)/d)
			}
		}
	}
	if len(ratios) == 0 {
		return 1
	}
	slices.Sort(ratios)
	return ratios[len(ratios)/2]
}

// cosineDistances are 1 − cosine between the groups' centroids, never
// below 0.
func cosineDistances(ctx context.Context, gs []zoomGroup) ([][]float64, error) {
	dm := make([][]float64, len(gs))
	for i := range gs {
		dm[i] = make([]float64, len(gs))
	}
	for i := range gs {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("layout: the groups' distances: %w", err)
		}
		for j := i + 1; j < len(gs); j++ {
			d := math.Max(0, 1-cosine(gs[i].centroid, gs[j].centroid))
			dm[i][j], dm[j][i] = d, d
		}
	}
	return dm, nil
}

// placeDocs gives every document its dot: an interest's inside its circle,
// an Unsorted one's inside the disc.
func (z *zoomer) placeDocs(interestAt []XY, unsorted Circle, unsortedSlots int) ([]XY, error) {
	out := make([]XY, len(z.keys))
	for i, g := range z.interests {
		targets, err := z.interestTargets(i, interestAt)
		if err != nil {
			return nil, err
		}
		if err := z.assign(out, g.docs, interestAt[i], g.radius, g.slots, targets); err != nil {
			return nil, err
		}
	}
	at := XY{unsorted.X, unsorted.Y}
	if err := z.assign(out, z.unsorted, at, unsorted.R, unsortedSlots, z.unsortedTargets(interestAt, at)); err != nil {
		return nil, err
	}
	return out, nil
}

// target is where a document would like its dot: an angle and a rank, the
// lower ranks nearer the centre.
type target struct {
	doc   int
	angle float64
	key   float64 // what the rank orders by, ascending
}

// assign ranks the targets (by key, then document), spreads the ranks over
// the circle's radius so it fills evenly, and gives each, innermost first,
// the free slot nearest where it would like its dot.
func (z *zoomer) assign(out []XY, docs []int, c XY, radius float64, slots int, targets []target) error {
	if len(docs) == 0 {
		return nil
	}
	slices.SortFunc(targets, func(a, b target) int { return cmp.Or(cmp.Compare(a.key, b.key), cmp.Compare(a.doc, b.doc)) })
	inner := radius - 1
	want := make([]XY, len(targets))
	for rank, t := range targets {
		rr := inner * math.Sqrt((float64(rank)+0.5)/float64(len(targets)))
		want[rank] = XY{c.X + rr*math.Cos(t.angle), c.Y + rr*math.Sin(t.angle)}
	}
	got, err := z.lat.assignSlots(z.ctx, c, slots, want)
	if err != nil {
		return err
	}
	for rank, t := range targets {
		out[t.doc] = got[rank]
	}
	return nil
}

// interestTargets are where interest i's documents would like their dots:
// their own first two principal components, turned (or reflected) to lean
// toward the circles of the interest's most similar interests, the radius
// ranking them.
func (z *zoomer) interestTargets(i int, interestAt []XY) ([]target, error) {
	g := z.interests[i]
	set := newRowSet(z.vectors, g.docs)
	axes, _, err := principalAxes(z.ctx, set, 2, z.seed)
	if err != nil {
		return nil, err
	}
	coords := make([]XY, len(g.docs))
	for x := range g.docs {
		coords[x] = planar(set.project(x, axes))
	}
	r := z.towardSimilar(i, set, axes, interestAt)
	out := make([]target, len(g.docs))
	for x, k := range g.docs {
		c := XY{coords[x].X*r[0][0] + coords[x].Y*r[1][0], coords[x].X*r[0][1] + coords[x].Y*r[1][1]}
		out[x] = target{doc: k, angle: math.Atan2(c.Y, c.X), key: math.Hypot(c.X, c.Y)}
	}
	return out, nil
}

// towardSimilar is the rotation or reflection that best takes the
// directions of interest i's orientingSimilar most similar interests, as
// its documents' principal axes see their centroids, onto their circles'
// directions from its own, weighted by similarity.
func (z *zoomer) towardSimilar(i int, set rowSet, axes [][]float64, interestAt []XY) [2][2]float64 {
	g := z.interests[i]
	others := make([]int, 0, len(z.interests)-1)
	sims := make([]float64, len(z.interests))
	for j, h := range z.interests {
		if j != i {
			others = append(others, j)
			sims[j] = cosine(g.centroid, h.centroid)
		}
	}
	slices.SortStableFunc(others, func(a, b int) int { return cmp.Compare(sims[b], sims[a]) })
	var m [2][2]float64
	for _, j := range others[:min(orientingSimilar, len(others))] {
		c := make([]float64, len(set.mean))
		for d, x := range z.interests[j].centroid {
			c[d] = float64(x) - set.mean[d]
		}
		p := XY{}
		if len(axes) > 0 {
			p.X = dotF(c, axes[0])
		}
		if len(axes) > 1 {
			p.Y = dotF(c, axes[1])
		}
		q := XY{interestAt[j].X - interestAt[i].X, interestAt[j].Y - interestAt[i].Y}
		pl, ql := math.Hypot(p.X, p.Y), math.Hypot(q.X, q.Y)
		if pl == 0 || ql == 0 {
			continue
		}
		w := math.Max(sims[j], 0.01)
		m[0][0] += w * p.X / pl * q.X / ql
		m[0][1] += w * p.X / pl * q.Y / ql
		m[1][0] += w * p.Y / pl * q.X / ql
		m[1][1] += w * p.Y / pl * q.Y / ql
	}
	r, _ := polar(m)
	return r
}

// planar is the first two of a point's principal coordinates.
func planar(c []float64) XY {
	var p XY
	if len(c) > 0 {
		p.X = c[0]
	}
	if len(c) > 1 {
		p.Y = c[1]
	}
	return p
}

// unsortedTargets are where Unsorted's documents would like their dots:
// toward their nearest interest's circle, the more similar nearer the rim;
// one without a nearest interest at the golden angle of its rank.
func (z *zoomer) unsortedTargets(interestAt []XY, c XY) []target {
	out := make([]target, len(z.unsorted))
	for x, k := range z.unsorted {
		out[x] = target{doc: k, key: z.sim[k], angle: math.NaN()}
		if near := z.nearest[k]; near >= 0 {
			out[x].angle = math.Atan2(interestAt[near].Y-c.Y, interestAt[near].X-c.X)
		}
	}
	slices.SortFunc(out, func(a, b target) int { return cmp.Or(cmp.Compare(a.key, b.key), cmp.Compare(a.doc, b.doc)) })
	for rank := range out {
		if math.IsNaN(out[rank].angle) {
			out[rank].angle = goldenAngle * float64(rank)
		}
	}
	return out
}
