package insight

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
)

// previous is the run a rebuild starts from, the tenant's latest done run,
// with its assignments and its groups by identity ID.
type previous struct {
	run         *store.InterestRun
	assignments []store.InterestAssignment
	groups      map[string]store.InterestGroup
}

func newPrevious(run *store.InterestRun, assignments []store.InterestAssignment, groups []store.InterestGroup) *previous {
	p := &previous{run: run, assignments: assignments, groups: make(map[string]store.InterestGroup, len(groups))}
	for _, g := range groups {
		p.groups[g.ID] = g
	}
	return p
}

// seeds is the Prior a warm grouping starts from: each grouped document's
// seeds.
func (p *previous) seeds() map[string]Seed {
	out := make(map[string]Seed, len(p.assignments))
	for _, a := range p.assignments {
		out[a.DocumentID] = Seed{Area: a.AreaSeed, Interest: a.InterestSeed}
	}
	return out
}

// changed counts the documents added or gone since the run: those read now
// that it didn't group, and those it grouped that aren't read now, failed,
// pending again or deleted. A deleted document's rows went with it, so the
// run's document count tells those apart from the rows left.
func (p *previous) changed(dvs []store.DocVector) int {
	grouped := make(map[string]bool, len(p.assignments))
	for _, a := range p.assignments {
		grouped[a.DocumentID] = true
	}
	added, still := 0, 0
	for _, dv := range dvs {
		if grouped[dv.DocumentID] {
			still++
		} else {
			added++
		}
	}
	return added + p.run.NumDocuments - still
}

// oldGroups returns the run's areas and interests as carry-over matches
// them, each sorted by ID: an area's members are every document its
// community held, whatever its fit; an interest's are the documents it
// grouped, never its loose fits, and its parent is its area. Without a
// prior there are none.
func (p *previous) oldGroups() (areas, interests []OldGroup) {
	if p == nil {
		return nil, nil
	}
	areaMembers := make(map[string][]string)
	members := make(map[string][]string)
	for _, a := range p.assignments {
		if a.AreaID != "" {
			areaMembers[a.AreaID] = append(areaMembers[a.AreaID], a.DocumentID)
		}
		if a.Fit == store.InterestFitMember {
			members[a.InterestID] = append(members[a.InterestID], a.DocumentID)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(p.groups)) {
		switch g := p.groups[id]; g.Level {
		case store.InterestLevelArea:
			areas = append(areas, OldGroup{ID: id, Members: areaMembers[id]})
		case store.InterestLevelInterest:
			interests = append(interests, OldGroup{ID: id, Members: members[id], Parent: g.ParentID})
		}
	}
	return areas, interests
}

// newGroup is an area or an interest of a rebuild's grouping, as labels and
// the commit see it.
type newGroup struct {
	level store.InterestLevel
	// parent is an interest's area, its index in grouped.groups; -1 for an
	// area or a flat interest.
	parent int
	// members are the point indexes of an interest's members, most similar
	// first, then by document ID; an area's are its interests' members.
	members  []int
	loose    int
	cohesion float64
	centroid []float32 // an interest's; nil for an area
	// id is the identity the group has: its predecessor's, or a new one.
	id string
	// carried is the predecessor as the prior run had it, nil for a new
	// group.
	carried *store.InterestGroup
}

func (g newGroup) size() int { return len(g.members) }

// grouped is a rebuild's grouping after every step but labels: Group, the
// merge of near-duplicates, the strays and the carry-over at each level.
type grouped struct {
	ids       []string // the points' document IDs
	mean      []float64
	g         Grouping // after the merge
	merged    int
	centroids [][]float32
	fits      []Fit
	// groups are the areas, numbered as g.Area numbers them, then the
	// interests, numbered as g.Interest does, offset by numAreas.
	groups   []newGroup
	numAreas int
	// areas and interests are the levels' carry-overs.
	areas, interests Carried

	kind  store.RunKind
	split bool
	// changed and changesSinceSplit are the outcome's counts.
	changed, changesSinceSplit int
}

// newGrouped runs the steps after Group: g is the grouping of in. The run
// is fresh unless the plan was warm and the grouping took the prior's
// seeds: it kept the prior's shape, and the prior seeded one of its points.
func newGrouped(points []Point, mean []float64, in GroupInput, g Grouping, prior *previous, p plan) (*grouped, error) {
	merged, n, err := MergeNearDuplicates(points, g, MergeThreshold)
	if err != nil {
		return nil, fmt.Errorf("merge near-duplicate interests: %w", err)
	}
	cents, err := Centroids(points, merged.Interest)
	if err != nil {
		return nil, fmt.Errorf("interest centroids: %w", err)
	}
	fits, err := AssignStrays(points, merged, cents, LooseFitThreshold)
	if err != nil {
		return nil, fmt.Errorf("place strays: %w", err)
	}
	gr := &grouped{ids: idsOf(points), mean: mean, g: merged, merged: n, centroids: cents, fits: fits,
		kind: store.RunKindFresh, changed: p.changed}
	if p.warm && g.Shape == in.Shape && seedsFor(in.Prior, gr.ids) != nil {
		gr.kind = store.RunKindWarm
	}
	gr.split = p.split && gr.kind == store.RunKindWarm
	if gr.kind == store.RunKindWarm && !gr.split {
		gr.changesSinceSplit = prior.run.ChangesSinceSplit + p.changed
	}
	gr.buildGroups(points)
	if err := gr.carry(prior); err != nil {
		return nil, err
	}
	return gr, nil
}

// buildGroups sizes and summarizes the grouping's areas and interests.
func (gr *grouped) buildGroups(points []Point) {
	gr.numAreas = numLabels(gr.g.Area)
	numInterests := numLabels(gr.g.Interest)
	gr.groups = make([]newGroup, gr.numAreas+numInterests)
	for a := range gr.numAreas {
		gr.groups[a] = newGroup{level: store.InterestLevelArea, parent: -1}
	}
	for l := range numInterests {
		gr.groups[gr.numAreas+l] = newGroup{level: store.InterestLevelInterest, parent: -1,
			centroid: gr.centroids[l]}
	}
	for i, f := range gr.fits {
		switch f.Kind {
		case FitMember:
			in := &gr.groups[gr.numAreas+f.Interest]
			in.members = append(in.members, i)
			if a := gr.g.Area[i]; a != NoiseLabel {
				in.parent = a
			}
		case FitLoose:
			gr.groups[gr.numAreas+f.Interest].loose++
		case FitUnsorted:
		}
	}
	for l := range numInterests {
		in := &gr.groups[gr.numAreas+l]
		slices.SortFunc(in.members, func(a, b int) int {
			return cmp.Or(cmp.Compare(gr.fits[b].Similarity, gr.fits[a].Similarity), strings.Compare(gr.ids[a], gr.ids[b]))
		})
		var sum float64
		for _, i := range in.members {
			sum += gr.fits[i].Similarity
		}
		in.cohesion = sum / float64(len(in.members))
		if in.parent >= 0 {
			area := &gr.groups[in.parent]
			area.members = append(area.members, in.members...)
			area.loose += in.loose
		}
	}
	for a := range gr.numAreas {
		gr.groups[a].cohesion = cohesion(points, gr.groups[a].members)
	}
}

// cohesion is the mean cosine of the members to their unit mean.
func cohesion(points []Point, members []int) float64 {
	if len(members) == 0 {
		return 0
	}
	centroid := make([]float64, len(points[members[0]].Vector))
	for _, i := range members {
		for d, v := range points[i].Vector {
			centroid[d] += float64(v)
		}
	}
	var norm float64
	for _, v := range centroid {
		norm += v * v
	}
	if norm == 0 {
		return 0
	}
	norm = math.Sqrt(norm)
	var total float64
	for _, i := range members {
		var s float64
		for d, v := range points[i].Vector {
			s += float64(v) * centroid[d]
		}
		total += s / norm
	}
	return total / float64(len(members))
}

// carry carries the prior's identities over, areas first, matched over the
// area communities, then interests, matched over their members, and gives
// each group its identity: its predecessor's, or a new one.
func (gr *grouped) carry(prior *previous) error {
	oldAreas, oldInterests := prior.oldGroups()
	newAreas := make([]NewGroup, gr.numAreas)
	for a := range newAreas {
		newAreas[a].Parent = -1
	}
	for i, a := range gr.g.Area {
		if a != NoiseLabel {
			newAreas[a].Members = append(newAreas[a].Members, gr.ids[i])
		}
	}
	var err error
	if gr.areas, err = Carry(CarryInput{Old: oldAreas, New: newAreas, Library: gr.ids}); err != nil {
		return fmt.Errorf("carry areas over: %w", err)
	}
	in := CarryInput{Old: oldInterests, New: make([]NewGroup, len(gr.groups)-gr.numAreas), Library: gr.ids}
	if gr.g.Shape == ShapeAreas {
		in.Areas = &gr.areas
	}
	for l := range in.New {
		g := gr.groups[gr.numAreas+l]
		in.New[l] = NewGroup{Members: pick(gr.ids, g.members), Parent: g.parent}
	}
	if gr.interests, err = Carry(in); err != nil {
		return fmt.Errorf("carry interests over: %w", err)
	}
	predecessors := slices.Concat(gr.areas.Predecessor, gr.interests.Predecessor)
	for k, id := range predecessors {
		if id == "" {
			gr.groups[k].id = uuid.NewString()
			continue
		}
		carried := prior.groups[id]
		gr.groups[k].id, gr.groups[k].carried = id, &carried
	}
	return nil
}

// interestID is the identity of the grouping's interest l.
func (gr *grouped) interestID(l int) string { return gr.groups[gr.numAreas+l].id }
