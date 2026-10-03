package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
)

// flatStored is a stored flat run of two interests over d0..d6: d0–d2 in
// a, d3 and d4 in b, d5 a loose fit of b, d6 unsorted.
func flatStored() *storedRun {
	member := func(doc, interest string) store.InterestAssignment {
		return store.InterestAssignment{DocumentID: doc, InterestID: interest, Fit: store.InterestFitMember}
	}
	return &storedRun{
		run: &store.InterestRun{ID: "run-1", RunOutcome: store.RunOutcome{Shape: store.InterestShapeFlat}},
		groups: []store.InterestGroup{
			{Interest: store.Interest{ID: "a", Level: store.InterestLevelInterest, Label: "Agent Tooling"}, Size: 3},
			{Interest: store.Interest{ID: "b", Level: store.InterestLevelInterest, Label: "Agent Talks"}, Size: 2},
		},
		assignments: []store.InterestAssignment{
			member("d0", "a"), member("d1", "a"), member("d2", "a"), member("d3", "b"), member("d4", "b"),
			{DocumentID: "d5", InterestID: "b", Fit: store.InterestFitLoose},
			{DocumentID: "d6", Fit: store.InterestFitUnsorted},
		},
	}
}

// toolGrouping is the tool's flat grouping of d0..d6 with the stored run's
// members, d5 a loose fit of the second interest, and d6 given lastFit.
func toolGrouping(lastFit insight.Fit) *grouping {
	return &grouping{
		ids: []string{"d0", "d1", "d2", "d3", "d4", "d5", "d6"},
		g: insight.Grouping{
			Shape:    insight.ShapeFlat,
			Area:     []int{-1, -1, -1, -1, -1, -1, -1},
			Interest: []int{0, 0, 0, 1, 1, -1, -1},
		},
		fits: []insight.Fit{
			{Kind: insight.FitMember, Interest: 0}, {Kind: insight.FitMember, Interest: 0},
			{Kind: insight.FitMember, Interest: 0}, {Kind: insight.FitMember, Interest: 1},
			{Kind: insight.FitMember, Interest: 1}, {Kind: insight.FitLoose, Interest: 1},
			lastFit,
		},
	}
}

// TestAgreementOf: a stored run and the tool's grouping that agree on
// every member but differ on one document's fit are not identical, though
// their partitions agree; the same run against its own grouping is.
func TestAgreementOf(t *testing.T) {
	same := agreementOf(flatStored(), toolGrouping(insight.Fit{Kind: insight.FitUnsorted, Interest: 1}))
	assert.True(t, same.Identical)
	assert.Equal(t, 7, same.SameFits)

	// d6 unsorted in the stored run, a loose fit of b in the tool's.
	differs := agreementOf(flatStored(), toolGrouping(insight.Fit{Kind: insight.FitLoose, Interest: 1}))
	require.NotNil(t, differs.InterestsARI)
	assert.InDelta(t, 1, *differs.InterestsARI, 1e-12, "the members' partitions agree")
	assert.Equal(t, 7, differs.Shared)
	assert.Equal(t, 6, differs.SameFits, "one fit differs")
	assert.False(t, differs.Identical)
}

// TestLabelDuplicatesOf: a label repeated within a scope is found there,
// and among all interests; a flat run's scope is its interests.
func TestLabelDuplicatesOf(t *testing.T) {
	run := flatStored()
	run.groups[1].Label = "agent tooling!" // the same name, cased and punctuated otherwise
	d := labelDuplicatesOf(run.groups)
	assert.Equal(t, 1, d.WithinScopes.Exact)
	require.Len(t, d.Scopes, 1)
	assert.Equal(t, "interests", d.Scopes[0].Scope)
	assert.Equal(t, 1, d.AllInterests.Exact)

	assert.Zero(t, labelDuplicatesOf(flatStored().groups).WithinScopes.Exact)
}
