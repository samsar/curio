package insight

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docs returns the document IDs prefix00 .. prefix(n-1).
func docs(prefix string, from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprintf("%s%02d", prefix, i))
	}
	return out
}

// flat makes a level of new groups without parents.
func flat(groups ...[]string) []NewGroup {
	out := make([]NewGroup, len(groups))
	for j, g := range groups {
		out[j] = NewGroup{Members: g, Parent: -1}
	}
	return out
}

func carried(t *testing.T, in CarryInput) Carried {
	t.Helper()
	if in.Library == nil {
		for _, g := range in.Old {
			in.Library = append(in.Library, g.Members...)
		}
		for _, g := range in.New {
			in.Library = append(in.Library, g.Members...)
		}
		slices.Sort(in.Library)
		in.Library = slices.Compact(in.Library)
	}
	c, err := Carry(in)
	require.NoError(t, err)
	again, err := Carry(in)
	require.NoError(t, err)
	require.Equal(t, c, again, "never depends on map order")
	return c
}

func TestCarry(t *testing.T) {
	o := docs("o", 0, 25)
	p := docs("p", 0, 25)
	cases := []struct {
		name        string
		in          CarryInput
		predecessor []string
		lineage     []LineageRow
		fates       []Fate
		counts      CarryCounts
	}{
		{
			name:        "70% is no heir",
			in:          CarryInput{Old: []OldGroup{{ID: "O", Members: o[:10]}}, New: flat(o[:7])},
			predecessor: []string{""},
			fates:       []Fate{{ID: "O", Dissolved: true, Successors: []Successor{{New: 0, Shared: 7}}}},
			counts:      CarryCounts{Created: 1, Dissolved: 1},
		},
		{
			name:        "80% is the heir",
			in:          CarryInput{Old: []OldGroup{{ID: "O", Members: o[:10]}}, New: flat(o[:8])},
			predecessor: []string{"O"},
			lineage:     []LineageRow{{OldID: "O", New: 0, Event: EventKept, Shared: 8}},
			fates:       []Fate{{ID: "O", Kept: true, Successors: []Successor{{New: 0, Shared: 8}}}},
			counts:      CarryCounts{Kept: 1},
		},
		{
			name:        "25% is a part",
			in:          CarryInput{Old: []OldGroup{{ID: "O", Members: o[:8]}}, New: flat(o[:6], o[6:8])},
			predecessor: []string{"O", ""},
			lineage:     []LineageRow{{OldID: "O", New: 0, Event: EventKept, Shared: 6}, {OldID: "O", New: 1, Event: EventSplit, Shared: 2}},
			fates:       []Fate{{ID: "O", Kept: true, Split: true, Successors: []Successor{{New: 0, Shared: 6}, {New: 1, Shared: 2}}}},
			counts:      CarryCounts{Kept: 1, Created: 1, Split: 1},
		},
		{
			name:        "12.5% is not",
			in:          CarryInput{Old: []OldGroup{{ID: "O", Members: o[:8]}}, New: flat(o[:7], o[7:8])},
			predecessor: []string{"O", ""},
			lineage:     []LineageRow{{OldID: "O", New: 0, Event: EventKept, Shared: 7}},
			fates:       []Fate{{ID: "O", Kept: true, Successors: []Successor{{New: 0, Shared: 7}}}},
			counts:      CarryCounts{Kept: 1, Created: 1},
		},
		{
			name: "equal overlaps go to the lower old ID",
			in: CarryInput{
				Old: []OldGroup{{ID: "b", Members: p[:4]}, {ID: "a", Members: o[:4]}},
				New: flat(slices.Concat(o[:4], p[:4])),
			},
			predecessor: []string{"a"},
			lineage: []LineageRow{
				{OldID: "a", New: 0, Event: EventKept, Shared: 4},
				{OldID: "b", New: 0, Event: EventMerged, Shared: 4},
			},
			fates: []Fate{
				{ID: "b", Merged: true, Successors: []Successor{{New: 0, Shared: 4}}},
				{ID: "a", Kept: true, Successors: []Successor{{New: 0, Shared: 4}}},
			},
			counts: CarryCounts{Kept: 1, Merged: 1},
		},
		{
			name: "two whole groups merge: the larger overlap keeps its ID",
			in: CarryInput{
				Old: []OldGroup{{ID: "a", Members: o[:4]}, {ID: "b", Members: p[:6]}},
				New: flat(slices.Concat(o[:4], p[:6])),
			},
			predecessor: []string{"b"},
			lineage: []LineageRow{
				{OldID: "a", New: 0, Event: EventMerged, Shared: 4},
				{OldID: "b", New: 0, Event: EventKept, Shared: 6},
			},
			fates: []Fate{
				{ID: "a", Merged: true, Successors: []Successor{{New: 0, Shared: 4}}},
				{ID: "b", Kept: true, Successors: []Successor{{New: 0, Shared: 6}}},
			},
			counts: CarryCounts{Kept: 1, Merged: 1},
		},
		{
			name:        "a 72/28 split keeps its ID in the larger part",
			in:          CarryInput{Old: []OldGroup{{ID: "O", Members: o}}, New: flat(o[7:], o[:7])},
			predecessor: []string{"O", ""},
			lineage:     []LineageRow{{OldID: "O", New: 0, Event: EventKept, Shared: 18}, {OldID: "O", New: 1, Event: EventSplit, Shared: 7}},
			fates:       []Fate{{ID: "O", Kept: true, Split: true, Successors: []Successor{{New: 0, Shared: 18}, {New: 1, Shared: 7}}}},
			counts:      CarryCounts{Kept: 1, Created: 1, Split: 1},
		},
		{
			name:        "a 50/50 split has no heir",
			in:          CarryInput{Old: []OldGroup{{ID: "O", Members: o[:10]}}, New: flat(o[5:10], o[:5])},
			predecessor: []string{"", ""},
			lineage:     []LineageRow{{OldID: "O", New: 0, Event: EventSplit, Shared: 5}, {OldID: "O", New: 1, Event: EventSplit, Shared: 5}},
			fates:       []Fate{{ID: "O", Split: true, Successors: []Successor{{New: 0, Shared: 5}, {New: 1, Shared: 5}}}},
			counts:      CarryCounts{Created: 2, Split: 1},
		},
		{
			name: "a merge into a new group",
			in: CarryInput{
				Old: []OldGroup{{ID: "a", Members: o[:10]}, {ID: "b", Members: p[:10]}},
				New: flat(slices.Concat(o[:4], p[:4])),
			},
			predecessor: []string{""},
			lineage: []LineageRow{
				{OldID: "a", New: 0, Event: EventMerged, Shared: 4},
				{OldID: "b", New: 0, Event: EventMerged, Shared: 4},
			},
			fates: []Fate{
				{ID: "a", Merged: true, Successors: []Successor{{New: 0, Shared: 4}}},
				{ID: "b", Merged: true, Successors: []Successor{{New: 0, Shared: 4}}},
			},
			counts: CarryCounts{Created: 1, Merged: 2},
		},
		{
			name: "merged and split at once",
			in: CarryInput{
				Old: []OldGroup{{ID: "a", Members: o[:10]}, {ID: "b", Members: p[:4]}},
				New: flat(slices.Concat(o[:4], p[:4]), o[4:10]),
			},
			predecessor: []string{"b", ""},
			lineage: []LineageRow{
				{OldID: "a", New: 1, Event: EventSplit, Shared: 6},
				{OldID: "a", New: 0, Event: EventMerged, Shared: 4},
				{OldID: "b", New: 0, Event: EventKept, Shared: 4},
			},
			fates: []Fate{
				{ID: "a", Split: true, Merged: true, Successors: []Successor{{New: 1, Shared: 6}, {New: 0, Shared: 4}}},
				{ID: "b", Kept: true, Successors: []Successor{{New: 0, Shared: 4}}},
			},
			counts: CarryCounts{Kept: 1, Created: 1, Split: 1, Merged: 1},
		},
		{
			name:        "dissolved: 40% to a group of its own, the rest strays",
			in:          CarryInput{Old: []OldGroup{{ID: "O", Members: o[:10]}}, New: flat(slices.Concat(o[:4], p[:8]))},
			predecessor: []string{""},
			fates:       []Fate{{ID: "O", Dissolved: true, Successors: []Successor{{New: 0, Shared: 4}}}},
			counts:      CarryCounts{Created: 1, Dissolved: 1},
		},
		{
			name: "every member gone",
			in: CarryInput{
				Old: []OldGroup{{ID: "O", Members: o[:10]}}, New: flat(p[:5]), Library: p[:5],
			},
			predecessor: []string{""},
			fates:       []Fate{{ID: "O", Dissolved: true}},
			counts:      CarryCounts{Created: 1, Dissolved: 1},
		},
		{
			name: "size counts the members still in the library",
			in: CarryInput{
				Old: []OldGroup{{ID: "O", Members: o[:10]}}, New: flat(o[:4]), Library: o[:5],
			},
			predecessor: []string{"O"},
			lineage:     []LineageRow{{OldID: "O", New: 0, Event: EventKept, Shared: 4}},
			fates:       []Fate{{ID: "O", Kept: true, Successors: []Successor{{New: 0, Shared: 4}}}},
			counts:      CarryCounts{Kept: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := carried(t, tc.in)
			assert.Equal(t, tc.predecessor, c.Predecessor)
			assert.Equal(t, tc.lineage, c.Lineage)
			assert.Equal(t, tc.fates, c.Fates)
			assert.Equal(t, tc.counts, c.Counts)
			assert.Equal(t, len(tc.in.New), c.Counts.Kept+c.Counts.Created)
		})
	}
}

// TestCarry_MovedOrKept: an interest's heir has moved when its area
// descends from another area than the old interest's, by the area level's
// carry-over; across a change of shape nothing moves.
func TestCarry_MovedOrKept(t *testing.T) {
	a1, a2, a3 := docs("a", 0, 20), docs("b", 0, 20), docs("c", 0, 20)
	areas := carried(t, CarryInput{
		Old: []OldGroup{{ID: "A1", Members: a1}, {ID: "A2", Members: a2}},
		New: flat(a2, a1, a3), // area 0 inherits A2, area 1 A1, area 2 is new
	})
	require.Equal(t, []string{"A2", "A1", ""}, areas.Predecessor)

	interests := carried(t, CarryInput{
		Old: []OldGroup{
			{ID: "I1", Members: a1[:10], Parent: "A1"},
			{ID: "I2", Members: a1[10:], Parent: "A1"},
			{ID: "I3", Members: a2[:10], Parent: "A2"},
		},
		New: []NewGroup{
			{Members: a1[:10], Parent: 1}, // in A1's heir: kept
			{Members: a1[10:], Parent: 0}, // in A2's heir: moved
			{Members: a2[:10], Parent: 2}, // in a new area: moved
		},
		Library: slices.Concat(a1, a2, a3),
		Areas:   &areas,
	})
	assert.Equal(t, []LineageRow{
		{OldID: "I1", New: 0, Event: EventKept, Shared: 10},
		{OldID: "I2", New: 1, Event: EventMoved, Shared: 10},
		{OldID: "I3", New: 2, Event: EventMoved, Shared: 10},
	}, interests.Lineage)
	assert.Equal(t, CarryCounts{Kept: 3, Moved: 2}, interests.Counts)
	assert.True(t, interests.Fates[1].Moved && interests.Fates[1].Kept)

	// From flat to areas and back, an heir is only ever kept.
	toAreas := carried(t, CarryInput{
		Old: []OldGroup{{ID: "I1", Members: a1[:10]}}, New: []NewGroup{{Members: a1[:10], Parent: 0}},
		Library: a1, Areas: &Carried{Predecessor: []string{""}},
	})
	assert.Equal(t, EventKept, toAreas.Lineage[0].Event)
	toFlat := carried(t, CarryInput{
		Old: []OldGroup{{ID: "I1", Members: a1[:10], Parent: "A1"}}, New: flat(a1[:10]), Library: a1,
	})
	assert.Equal(t, EventKept, toFlat.Lineage[0].Event)
}

func TestCarry_RejectsBadInput(t *testing.T) {
	o := docs("o", 0, 10)
	cases := []struct {
		name string
		in   CarryInput
		want string
	}{
		{"duplicate old ID", CarryInput{Old: []OldGroup{{ID: "O"}, {ID: "O"}}}, "old group O appears twice"},
		{"document in two old groups", CarryInput{Old: []OldGroup{{ID: "a", Members: o[:2]}, {ID: "b", Members: o[1:3]}}},
			"document o01 is in two old groups"},
		{"document in two new groups", CarryInput{New: flat(o[:2], o[1:3]), Library: o}, "document o01 is in two new groups"},
		{"member outside the library", CarryInput{New: flat(o[:2]), Library: o[1:]}, "document o00 of new group 0 is not in the library"},
		{"parent without an area level", CarryInput{New: []NewGroup{{Members: o[:2], Parent: 0}}, Library: o},
			"new group 0 has parent 0, which the area level lacks"},
		{"parent out of range", CarryInput{New: []NewGroup{{Members: o[:2], Parent: 1}}, Library: o, Areas: &Carried{Predecessor: []string{"A"}}},
			"new group 0 has parent 1, which the area level lacks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Carry(tc.in)
			require.EqualError(t, err, "insight: "+tc.want)
		})
	}
}
