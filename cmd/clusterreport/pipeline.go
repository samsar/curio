package main

import (
	"context"
	"fmt"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
)

// grouping is one rebuild's grouping of a document set, as the engine
// holds it before carry-over: every slice index-aligned with the points.
type grouping struct {
	ids       []string
	points    []insight.Point
	raw       insight.Grouping // Group's, before the merge
	g         insight.Grouping // after the merge
	merged    int
	centroids [][]float32
	fits      []insight.Fit
}

// regroup groups dvs as a rebuild does, in Engine.group's and
// newGrouped's order: PreparePoints, centered as the daemon's default
// center_vectors does, then Group from shape (the previous grouping's, or
// ShapeFlat for a first grouping) with prior's seeds and the split check
// when asked, MergeNearDuplicates, Centroids and AssignStrays. The engine
// equivalence test holds it to Engine.Rebuild.
func regroup(ctx context.Context, gr insight.Grouper, dvs []store.DocVector, shape insight.Shape,
	prior map[string]insight.Seed, split bool) (*grouping, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	points, _, err := insight.PreparePoints(dvs, true)
	if err != nil {
		return nil, err
	}
	raw, err := gr.Group(ctx, insight.GroupInput{Points: points, Shape: shape, Prior: prior, Split: split})
	if err != nil {
		return nil, fmt.Errorf("group: %w", err)
	}
	merged, n, err := insight.MergeNearDuplicates(points, raw, insight.MergeThreshold)
	if err != nil {
		return nil, fmt.Errorf("merge near-duplicate interests: %w", err)
	}
	cents, err := insight.Centroids(points, merged.Interest)
	if err != nil {
		return nil, fmt.Errorf("interest centroids: %w", err)
	}
	fits, err := insight.AssignStrays(points, merged, cents, insight.LooseFitThreshold)
	if err != nil {
		return nil, fmt.Errorf("place strays: %w", err)
	}
	ids := make([]string, len(points))
	for i, p := range points {
		ids[i] = p.ID
	}
	return &grouping{ids: ids, points: points, raw: raw, g: merged, merged: n, centroids: cents, fits: fits}, nil
}

// seeds is the Prior a warm rebuild from this grouping reads: what the
// engine stores as each assignment's seeds.
func (gp *grouping) seeds() map[string]insight.Seed {
	out := make(map[string]insight.Seed, len(gp.ids))
	for i, id := range gp.ids {
		out[id] = gp.g.Seeds[i]
	}
	return out
}

// vectors are the grouping's prepared points, which quality measures in
// the space the grouping saw.
func (gp *grouping) vectors() [][]float32 {
	out := make([][]float32, len(gp.points))
	for i, p := range gp.points {
		out[i] = p.Vector
	}
	return out
}

// counts are the grouping's areas and interests, and its loose fits and
// unsorted documents.
func (gp *grouping) counts() (areas, interests, loose, unsorted int) {
	areas, interests = numGroups(gp.g.Area), numGroups(gp.g.Interest)
	for _, f := range gp.fits {
		switch f.Kind {
		case insight.FitLoose:
			loose++
		case insight.FitUnsorted:
			unsorted++
		case insight.FitMember:
		}
	}
	return areas, interests, loose, unsorted
}

// numGroups is k for labels numbered 0..k-1, NoiseLabel for none.
func numGroups(labels []int) int {
	k := 0
	for _, l := range labels {
		k = max(k, l+1)
	}
	return k
}

// kept is the share of a level's old groups whose identity carries over,
// nil when the level had none.
type kept struct {
	Interests *float64 `json:"interests"`
	Areas     *float64 `json:"areas"`
}

// namesKept is the share of prev's identities that carry into next.
func namesKept(prev, next *grouping) (kept, error) {
	areas, interests, err := carryOver(prev, next)
	if err != nil {
		return kept{}, err
	}
	return kept{Interests: keptShare(interests), Areas: keptShare(areas)}, nil
}

// carryOver carries prev's identities into next by insight.Carry, built as
// the engine builds it (previous.oldGroups, grouped.carry): areas matched
// over area membership, then interests over their members, a new
// interest's parent its area when next has areas. Old groups are named by
// their zero-padded labels.
func carryOver(prev, next *grouping) (areas, interests insight.Carried, err error) {
	areas, err = insight.Carry(insight.CarryInput{
		Old:     oldGroups(prev, prev.g.Area, nil),
		New:     newGroups(next, next.g.Area, false),
		Library: next.ids,
	})
	if err != nil {
		return insight.Carried{}, insight.Carried{}, fmt.Errorf("carry areas over: %w", err)
	}
	in := insight.CarryInput{
		Old:     oldGroups(prev, prev.g.Interest, prev.g.Area),
		New:     newGroups(next, next.g.Interest, next.g.Shape == insight.ShapeAreas),
		Library: next.ids,
	}
	if next.g.Shape == insight.ShapeAreas {
		in.Areas = &areas
	}
	if interests, err = insight.Carry(in); err != nil {
		return insight.Carried{}, insight.Carried{}, fmt.Errorf("carry interests over: %w", err)
	}
	return areas, interests, nil
}

// oldGroups are a grouping's groups at the level labels gives, each with
// its members (for interests, the members alone: labels is after the
// merge, which loose fits aren't in) and, when parents is given, its area.
func oldGroups(gp *grouping, labels, parents []int) []insight.OldGroup {
	out := make([]insight.OldGroup, numGroups(labels))
	for l := range out {
		out[l].ID = groupID(l)
	}
	for i, l := range labels {
		if l == insight.NoiseLabel {
			continue
		}
		out[l].Members = append(out[l].Members, gp.ids[i])
		if parents != nil && parents[i] != insight.NoiseLabel {
			out[l].Parent = groupID(parents[i])
		}
	}
	return out
}

// newGroups are a grouping's groups at the level labels gives, each an
// interest's parent its area when withParents.
func newGroups(gp *grouping, labels []int, withParents bool) []insight.NewGroup {
	out := make([]insight.NewGroup, numGroups(labels))
	for l := range out {
		out[l].Parent = -1
	}
	for i, l := range labels {
		if l == insight.NoiseLabel {
			continue
		}
		out[l].Members = append(out[l].Members, gp.ids[i])
		if withParents {
			out[l].Parent = gp.g.Area[i]
		}
	}
	return out
}

func groupID(label int) string { return fmt.Sprintf("%06d", label) }

// keptShare is Counts.Kept over the old groups, nil without any.
func keptShare(c insight.Carried) *float64 {
	if len(c.Fates) == 0 {
		return nil
	}
	share := float64(c.Counts.Kept) / float64(len(c.Fates))
	return &share
}
