package insight

import (
	"github.com/samsar/curio/internal/store"
)

// commit is the grouping as CommitRun writes it for run, built from prior
// (nil for none), with each group's label as labels give it, index-aligned
// with gr.groups, each interest's most similar interests, and the map
// drawn. It is pure: what it writes follows from its inputs.
func (gr *grouped) commit(run *store.InterestRun, prior *previous, labels []groupLabel, drawn drawnMap) store.RunCommit {
	c := store.RunCommit{RunID: run.ID, TenantID: run.TenantID, Outcome: gr.outcome()}
	c.Outcome.Map = drawn.runMap()
	if prior != nil {
		c.PriorRunID = prior.run.ID
	}
	similar := similarTo(gr.centroids)
	for k, g := range gr.groups {
		lab := labels[k]
		identity := store.Interest{ID: g.id, Level: g.level, Label: lab.label.Name, Summary: lab.label.Summary,
			LabelSource: lab.source, LabeledAt: lab.at}
		switch {
		case g.carried == nil:
			c.NewIdentities = append(c.NewIdentities, identity)
		case lab.relabeled:
			c.Relabels = append(c.Relabels, identity)
		}
		group := store.InterestGroup{Interest: store.Interest{ID: g.id}, Size: g.size(), Loose: g.loose,
			Cohesion: g.cohesion, Centroid: g.centroid}
		if g.parent >= 0 {
			group.ParentID = gr.groups[g.parent].id
		}
		if l := k - gr.numAreas; l >= 0 {
			group.Similar = make([]store.SimilarInterest, len(similar[l]))
			for x, s := range similar[l] {
				group.Similar[x] = store.SimilarInterest{ID: gr.interestID(s.interest), Cosine: s.cosine}
			}
		}
		if m := drawn.m; m != nil && k < gr.numAreas {
			group.Map = &m.Areas[k]
		} else if m != nil {
			group.Map = &m.Interests[k-gr.numAreas]
		}
		c.Groups = append(c.Groups, group)
	}
	c.Assignments = make([]store.InterestAssignment, len(gr.ids))
	for i, f := range gr.fits {
		a := store.InterestAssignment{DocumentID: gr.ids[i], Fit: store.InterestFit(f.Kind), Similarity: f.Similarity,
			AreaSeed: gr.g.Seeds[i].Area, InterestSeed: gr.g.Seeds[i].Interest}
		if area := gr.g.Area[i]; area != NoiseLabel {
			a.AreaID = gr.groups[area].id
		}
		switch {
		case f.Kind != FitUnsorted:
			a.InterestID = gr.interestID(f.Interest)
		case f.Interest >= 0:
			a.NearestID = gr.interestID(f.Interest)
		}
		if drawn.m != nil {
			a.Map = &drawn.m.Docs[i]
		}
		c.Assignments[i] = a
	}
	c.Lineage = append(gr.lineage(gr.areas, 0), gr.lineage(gr.interests, gr.numAreas)...)
	return c
}

// lineage is a level's lineage rows, the new groups named by their
// identities: the level's groups start at offset in gr.groups.
func (gr *grouped) lineage(c Carried, offset int) []store.LineageRow {
	out := make([]store.LineageRow, 0, len(c.Lineage))
	for _, l := range c.Lineage {
		out = append(out, store.LineageRow{OldID: l.OldID, NewID: gr.groups[offset+l.New].id,
			Event: store.LineageEvent(l.Event), Shared: l.Shared})
	}
	return out
}

// outcome is the run's outcome: what it found, and what it did to the
// prior's interest identities.
func (gr *grouped) outcome() store.RunOutcome {
	o := store.RunOutcome{
		Kind: gr.kind, SplitCheck: gr.split, Shape: store.InterestShape(gr.g.Shape), Mean: float32s(gr.mean),
		NumDocuments: len(gr.ids), NumAreas: gr.numAreas, NumInterests: len(gr.groups) - gr.numAreas,
		ChangedDocuments: gr.changed, ChangesSinceSplit: gr.changesSinceSplit,
	}
	for _, f := range gr.fits {
		switch f.Kind {
		case FitLoose:
			o.NumLoose++
		case FitUnsorted:
			o.NumUnsorted++
		case FitMember:
		}
	}
	n := gr.interests.Counts
	o.Kept, o.Created, o.Split, o.Merged, o.Moved, o.Dissolved = n.Kept, n.Created, n.Split, n.Merged, n.Moved, n.Dissolved
	return o
}

// float32s is v as the float32s a run stores, nil for none.
func float32s(v []float64) []float32 {
	if len(v) == 0 {
		return nil
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(x)
	}
	return out
}
