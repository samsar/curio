package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/quality"
	"github.com/samsar/curio/internal/store"
)

// storedOf describes the stored run: its row, its labels and their
// duplicates, and how it agrees with full, the report's fresh grouping of
// the whole library, which the grouper named grouper made with params.
func storedOf(s *storedRun, full *grouping, grouper string, params map[string]any) (storedReport, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return storedReport{}, fmt.Errorf("encode the grouper's params: %w", err)
	}
	r, o := s.run, s.run.RunOutcome
	rep := storedReport{
		RunID: r.ID, Trigger: r.Trigger, Kind: o.Kind, SplitCheck: o.SplitCheck, Shape: o.Shape,
		FinishedAt: r.FinishedAt, SameParams: r.Grouper == grouper && bytes.Equal(r.Params, raw),
		Counts: runCounts{Documents: o.NumDocuments, Areas: o.NumAreas, Interests: o.NumInterests, Loose: o.NumLoose,
			Unsorted: o.NumUnsorted, Changed: o.ChangedDocuments, Kept: o.Kept, Created: o.Created, Split: o.Split,
			Merged: o.Merged, Moved: o.Moved, Dissolved: o.Dissolved},
		LabelSources: map[string]int{},
		Labels:       labelTable(s.groups),
		Duplicates:   labelDuplicatesOf(s.groups),
		Agreement:    agreementOf(s, full),
	}
	for _, g := range s.groups {
		source := string(g.LabelSource)
		if source == "" {
			source = "none"
		}
		rep.LabelSources[source]++
	}
	return rep, nil
}

// labelTable is the run's groups as the Interests page lists them: areas
// largest first, each with its interests largest first, or the interests
// of a flat run. groups are in that order.
func labelTable(groups []store.InterestGroup) []labelRow {
	var out []labelRow
	index := map[string]int{}
	for _, g := range groups {
		if g.Level == store.InterestLevelArea {
			index[g.ID] = len(out)
			out = append(out, rowOf(g))
		}
	}
	for _, g := range groups {
		if g.Level != store.InterestLevelInterest {
			continue
		}
		if a, ok := index[g.ParentID]; ok {
			out[a].Interests = append(out[a].Interests, rowOf(g))
		} else {
			out = append(out, rowOf(g))
		}
	}
	return out
}

func rowOf(g store.InterestGroup) labelRow {
	return labelRow{ID: g.ID, Label: g.Label, LabelSource: g.LabelSource, Size: g.Size}
}

// labelDuplicatesOf finds the labels that repeat: in each scope labels
// must be unique in (an area's interests and the areas, or every interest
// of a flat run), and among all interests.
func labelDuplicatesOf(groups []store.InterestGroup) labelDuplicates {
	var d labelDuplicates
	var areas, interests []string
	byArea := map[string][]string{}
	var areaOrder []store.InterestGroup
	for _, g := range groups {
		switch g.Level {
		case store.InterestLevelArea:
			areas = append(areas, g.Label)
			areaOrder = append(areaOrder, g)
		case store.InterestLevelInterest:
			interests = append(interests, g.Label)
			byArea[g.ParentID] = append(byArea[g.ParentID], g.Label)
		}
	}
	add := func(scope string, labels []string) {
		dup := quality.DuplicateLabels(labels)
		d.WithinScopes.Exact += dup.Exact
		d.WithinScopes.Near += dup.Near
		if dup.Exact+dup.Near > 0 {
			d.Scopes = append(d.Scopes, scopeDuplicates{Scope: scope, LabelDuplicates: dup})
		}
	}
	if len(areaOrder) == 0 {
		add("interests", interests)
	} else {
		for _, a := range areaOrder {
			add(a.Label, byArea[a.ID])
		}
		add("areas", areas)
	}
	d.AllInterests = quality.DuplicateLabels(interests)
	return d
}

// agreementOf compares the stored run with full over the documents both
// hold: the ARI of each level, and the documents whose fit is the same (a
// member or loose fit of corresponding interests, or unsorted in both).
func agreementOf(s *storedRun, full *grouping) agreement {
	areaIndex, interestIndex := map[string]int{}, map[string]int{}
	for _, g := range s.groups {
		switch g.Level {
		case store.InterestLevelArea:
			areaIndex[g.ID] = len(areaIndex)
		case store.InterestLevelInterest:
			interestIndex[g.ID] = len(interestIndex)
		}
	}
	byDoc := make(map[string]store.InterestAssignment, len(s.assignments))
	for _, a := range s.assignments {
		byDoc[a.DocumentID] = a
	}
	var ag agreement
	var shared []int // full's indexes of the shared documents
	var storedArea, storedInterest, toolArea, toolInterest []int
	for i, id := range full.ids {
		a, ok := byDoc[id]
		if !ok {
			ag.OnlyFresh++
			continue
		}
		shared = append(shared, i)
		storedArea = append(storedArea, labelOrNoise(areaIndex, a.AreaID))
		interest := insight.NoiseLabel
		if a.Fit == store.InterestFitMember {
			interest = labelOrNoise(interestIndex, a.InterestID)
		}
		storedInterest = append(storedInterest, interest)
		toolArea = append(toolArea, full.g.Area[i])
		toolInterest = append(toolInterest, full.g.Interest[i])
	}
	ag.Shared = len(shared)
	ag.OnlyStored = len(byDoc) - ag.Shared
	if ag.Shared == 0 {
		return ag
	}
	ag.InterestsARI = new(quality.Compare(storedInterest, toolInterest).ARI)
	if len(areaIndex) > 0 || full.g.Shape == insight.ShapeAreas {
		ag.AreasARI = new(quality.Compare(storedArea, toolArea).ARI)
	}
	ag.SameFits = sameFits(byDoc, interestIndex, full, shared, storedInterest)
	ag.Identical = ag.OnlyStored == 0 && ag.OnlyFresh == 0 && string(s.run.Shape) == string(full.g.Shape) &&
		*ag.InterestsARI == 1 && (ag.AreasARI == nil || *ag.AreasARI == 1) && ag.SameFits == ag.Shared
	return ag
}

// sameFits counts the shared documents (full's indexes, with the stored
// run's interest labels storedInterest for them) whose fit is the same in
// both: unsorted in both, or a member or loose fit of interests that
// correspond, an interest corresponding to the other run's holding its
// first member.
func sameFits(byDoc map[string]store.InterestAssignment, interestIndex map[string]int, full *grouping,
	shared, storedInterest []int) int {
	corresponds := map[int]int{}
	for k, i := range shared {
		if l := storedInterest[k]; l != insight.NoiseLabel {
			if _, ok := corresponds[l]; !ok {
				corresponds[l] = full.g.Interest[i]
			}
		}
	}
	same := 0
	for _, i := range shared {
		a, f := byDoc[full.ids[i]], full.fits[i]
		if string(a.Fit) != string(f.Kind) {
			continue
		}
		if f.Kind == insight.FitUnsorted {
			same++
			continue
		}
		if l, ok := corresponds[labelOrNoise(interestIndex, a.InterestID)]; ok && l == f.Interest {
			same++
		}
	}
	return same
}

func labelOrNoise(index map[string]int, id string) int {
	if l, ok := index[id]; ok {
		return l
	}
	return insight.NoiseLabel
}
