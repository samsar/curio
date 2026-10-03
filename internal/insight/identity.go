package insight

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// The carry-over thresholds, compared in integers: a new group inherits an
// old group's identity when it holds more than KeepFraction of the old
// group's members still in the library (10·shared > 7·size), and takes a
// part of it when it holds at least SplitFraction (4·shared ≥ size).
const (
	KeepFraction  = 0.70
	SplitFraction = 0.25
)

// Event is what a grouping did to an old group, as a lineage row records
// it. The values are the ones storage records.
type Event string

const (
	// EventKept: the heir sits where the old group did.
	EventKept Event = "kept"
	// EventMoved: the heir sits in another area.
	EventMoved Event = "moved"
	// EventSplit: a part of the old group went to a new group of its own.
	EventSplit Event = "split"
	// EventMerged: a part of an old group without an heir went to a new
	// group that took parts of other old groups too.
	EventMerged Event = "merged"
)

// OldGroup is a group of the previous grouping at one level.
type OldGroup struct {
	ID string
	// Members are the group's member document IDs: never its loose fits
	// or the documents placed into it since.
	Members []string
	// Parent is an interest's area ID, "" for an area or a flat interest.
	Parent string
}

// NewGroup is a group of the new grouping at one level, named by its index.
type NewGroup struct {
	Members []string
	// Parent is an interest's area index in the area level's NewGroups,
	// -1 for an area or a flat interest.
	Parent int
}

// CarryInput is one level's carry-over: areas first, then interests with
// the area level's result.
type CarryInput struct {
	Old []OldGroup
	New []NewGroup
	// Library is every document of the new grouping, members or not.
	Library []string
	// Areas is the area level's Carry, for an interest level whose new
	// groups have parents; nil otherwise.
	Areas *Carried
}

// Carried is a level's carry-over.
type Carried struct {
	// Predecessor holds, per new group, the ID it inherits, or "" when it
	// is new.
	Predecessor []string
	// Fates holds, per old group in input order, what became of it.
	Fates []Fate
	// Lineage holds a row per old group and successor, ordered by old ID
	// and then as Fate.Successors orders them.
	Lineage []LineageRow
	// Counts sums the fates. An old group can count as both split and
	// merged, and Kept + Created is the number of new groups.
	Counts CarryCounts
}

// Fate is what a grouping did to one old group. The flags aren't exclusive:
// an heir and a split-off part make Kept and Split, and without an heir a
// part merged into a shared group and another into a group of its own make
// Merged and Split.
type Fate struct {
	ID                                    string
	Kept, Moved, Split, Merged, Dissolved bool
	// Successors are the new groups holding at least SplitFraction of
	// it, most shared first, ties to the lower index.
	Successors []Successor
}

// Successor is a new group that took a part of an old one.
type Successor struct {
	New    int
	Shared int
}

// LineageRow is one old group's event towards one of its successors.
type LineageRow struct {
	OldID  string
	New    int
	Event  Event
	Shared int
}

// CarryCounts are a level's events. Kept includes moved.
type CarryCounts struct {
	Kept, Moved, Created, Split, Merged, Dissolved int
}

// Carry decides which new groups inherit an old group's identity, and what
// happened to each old group. An old group's size counts its members still
// in the library. Its heir is the new group holding more than KeepFraction
// of that: one heir per old group and one predecessor per new group,
// candidate pairs taken most shared first, ties to the lower old ID, then
// the lower new index. Its successors are the new groups holding at least
// SplitFraction of it. The heir's lineage row is "moved" when both
// groupings have areas and the heir's area descends from another area than
// the old group's (or from none), "kept" otherwise; another successor's row
// is "merged" when the old group has no heir and the successor is a
// successor of other old groups too, "split" otherwise. An old group without
// an heir whose only successor is its own group, or without any successor,
// is dissolved and has no row. Areas are matched over the area community
// membership (Grouping.Area), so a run has to keep enough to rebuild it.
// Carry mints no IDs and doesn't depend on map order.
func Carry(in CarryInput) (Carried, error) {
	lib, err := in.check()
	if err != nil {
		return Carried{}, err
	}
	newOf := make(map[string]int)
	for j, g := range in.New {
		for _, m := range g.Members {
			newOf[m] = j
		}
	}

	// Each old group's size, and what it shares with each new group.
	size := make([]int, len(in.Old))
	shared := make([][]Successor, len(in.Old))
	for o, g := range in.Old {
		counts := make(map[int]int)
		for _, m := range g.Members {
			if !lib[m] {
				continue
			}
			size[o]++
			if j, ok := newOf[m]; ok {
				counts[j]++
			}
		}
		for j, n := range counts {
			shared[o] = append(shared[o], Successor{New: j, Shared: n})
		}
		slices.SortFunc(shared[o], func(a, b Successor) int {
			return cmp.Or(cmp.Compare(b.Shared, a.Shared), cmp.Compare(a.New, b.New))
		})
	}

	out := Carried{Predecessor: make([]string, len(in.New)), Fates: make([]Fate, len(in.Old))}
	heir := inherit(in.Old, size, shared, out.Predecessor)

	// A new group's count of old groups it is a successor of.
	successorOf := make([]int, len(in.New))
	for o := range in.Old {
		out.Fates[o] = Fate{ID: in.Old[o].ID}
		for _, s := range shared[o] {
			if 4*s.Shared >= size[o] {
				out.Fates[o].Successors = append(out.Fates[o].Successors, s)
				successorOf[s.New]++
			}
		}
	}

	for o, g := range in.Old {
		f := &out.Fates[o]
		if heir[o] < 0 && (len(f.Successors) == 0 || (len(f.Successors) == 1 && successorOf[f.Successors[0].New] == 1)) {
			f.Dissolved = true
			continue
		}
		for _, s := range f.Successors {
			row := LineageRow{OldID: g.ID, New: s.New, Shared: s.Shared}
			switch {
			case s.New == heir[o]:
				row.Event = EventKept
				f.Kept = true
				if in.moved(g, s.New) {
					row.Event = EventMoved
					f.Moved = true
				}
			case heir[o] < 0 && successorOf[s.New] > 1:
				row.Event = EventMerged
				f.Merged = true
			default:
				row.Event = EventSplit
				f.Split = true
			}
			out.Lineage = append(out.Lineage, row)
		}
	}
	slices.SortStableFunc(out.Lineage, func(a, b LineageRow) int { return strings.Compare(a.OldID, b.OldID) })
	out.Counts = countFates(out)
	return out, nil
}

// inherit pairs old groups with their heirs: the new group holding more
// than KeepFraction of an old group's size, pairs taken most shared first,
// ties to the lower old ID and then the lower new index, each old and each
// new group in one pair at most. It fills predecessor and returns each old
// group's heir, -1 for none.
func inherit(old []OldGroup, size []int, shared [][]Successor, predecessor []string) []int {
	type pair struct{ old, new, shared int }
	var pairs []pair
	for o, ss := range shared {
		for _, s := range ss {
			if 10*s.Shared > 7*size[o] {
				pairs = append(pairs, pair{o, s.New, s.Shared})
			}
		}
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		return cmp.Or(cmp.Compare(b.shared, a.shared), strings.Compare(old[a.old].ID, old[b.old].ID), cmp.Compare(a.new, b.new))
	})
	heir := make([]int, len(old))
	for o := range heir {
		heir[o] = -1
	}
	for _, p := range pairs {
		if heir[p.old] < 0 && predecessor[p.new] == "" {
			heir[p.old] = p.new
			predecessor[p.new] = old[p.old].ID
		}
	}
	return heir
}

// moved reports whether old group g's heir j sits in an area that descends
// from another area than g's: only when both groupings have areas.
func (in CarryInput) moved(g OldGroup, j int) bool {
	parent := in.New[j].Parent
	if g.Parent == "" || parent < 0 || in.Areas == nil {
		return false
	}
	return in.Areas.Predecessor[parent] != g.Parent
}

func countFates(c Carried) CarryCounts {
	var n CarryCounts
	for _, f := range c.Fates {
		n.Kept += b2i(f.Kept)
		n.Moved += b2i(f.Moved)
		n.Split += b2i(f.Split)
		n.Merged += b2i(f.Merged)
		n.Dissolved += b2i(f.Dissolved)
	}
	for _, p := range c.Predecessor {
		n.Created += b2i(p == "")
	}
	return n
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// check validates the input and returns the library as a set: old IDs
// unique and not empty (an empty predecessor means a new group), no
// document in two groups of a level, new groups' members in the library,
// and parents the area level knows.
func (in CarryInput) check() (map[string]bool, error) {
	lib := make(map[string]bool, len(in.Library))
	for _, id := range in.Library {
		lib[id] = true
	}
	ids := make(map[string]bool, len(in.Old))
	seen := make(map[string]bool)
	for o, g := range in.Old {
		if g.ID == "" {
			return nil, fmt.Errorf("insight: old group %d has no ID", o)
		}
		if ids[g.ID] {
			return nil, fmt.Errorf("insight: old group %s appears twice", g.ID)
		}
		ids[g.ID] = true
		for _, m := range g.Members {
			if seen[m] {
				return nil, fmt.Errorf("insight: document %s is in two old groups", m)
			}
			seen[m] = true
		}
	}
	clear(seen)
	for j, g := range in.New {
		if g.Parent >= 0 && (in.Areas == nil || g.Parent >= len(in.Areas.Predecessor)) {
			return nil, fmt.Errorf("insight: new group %d has parent %d, which the area level lacks", j, g.Parent)
		}
		for _, m := range g.Members {
			if seen[m] {
				return nil, fmt.Errorf("insight: document %s is in two new groups", m)
			}
			if !lib[m] {
				return nil, fmt.Errorf("insight: document %s of new group %d is not in the library", m, j)
			}
			seen[m] = true
		}
	}
	return lib, nil
}
