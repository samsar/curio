package insight

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/samsar/curio/internal/store"
)

// Period is the unit the interests' timeline counts saves by.
type Period string

// The periods: auto is quarters while the dated saves span at most
// maxQuarters of them, else years.
const (
	PeriodAuto    Period = "auto"
	PeriodYear    Period = "year"
	PeriodQuarter Period = "quarter"
)

// Periods lists every period a request may name.
func Periods() []Period { return []Period{PeriodAuto, PeriodYear, PeriodQuarter} }

// RowKind is what a row of the timeline counts.
type RowKind string

// The row kinds.
const (
	RowArea           RowKind = "area"
	RowOtherAreas     RowKind = "other_areas"
	RowInterest       RowKind = "interest"
	RowOtherInterests RowKind = "other_interests"
	RowUnsorted       RowKind = "unsorted"
)

// RowKinds lists every row kind.
func RowKinds() []RowKind {
	return []RowKind{RowArea, RowOtherAreas, RowInterest, RowOtherInterests, RowUnsorted}
}

// The timeline's bounds, so a library of any age and grouping is a table
// a person, and a model's context, can read: quarters while they are a
// three-year view, at most maxPeriods columns, the largest maxAreas areas
// with their largest interestsPerArea interests each, or in a flat run the
// largest flatInterests interests, the rest of each merged into one row.
const (
	maxQuarters      = 12
	maxPeriods       = 25
	maxAreas         = 12
	interestsPerArea = 3
	flatInterests    = 15
)

// Window is the saved window a timeline counts, [After, Before); a zero
// bound leaves its side open.
type Window struct {
	After, Before time.Time
}

// contains reports whether t is in w.
func (w Window) contains(t time.Time) bool {
	return (w.After.IsZero() || !t.Before(w.After)) && (w.Before.IsZero() || t.Before(w.Before))
}

// TimelineRow is a row of the timeline: an area, an interest (AreaID its
// area, "" in a flat run), the areas or an area's interests past the
// largest (Merged says how many), or Unsorted; its label, its saves in
// each period and in all.
type TimelineRow struct {
	Kind   RowKind
	ID     string
	AreaID string
	Label  string
	Counts []int
	Total  int
	Merged int
}

// TimelineTable is the interests' saves over time: the unit counted by
// (year or quarter), the periods oldest first ("2024 Q1", "2024", or
// "before 2002" for the oldest merged), the rows, and how many documents
// were counted (Dated, in the window) and how many have no known saved
// date (Undated, counted whatever the window).
type TimelineTable struct {
	Unit    Period
	Periods []string
	Rows    []TimelineRow
	Dated   int
	Undated int
}

// Timeline is the table of a run's saves (store.InsightStore.RunSaves),
// labels its identities' labels by ID, counting each document once, on
// the day it was first saved, in loc, the person's zone: a member or a
// loose fit under its interest and that interest's area, a document in
// Unsorted under Unsorted. Only saves in window are counted; a document
// without a known saved date is counted apart, as Undated. Period year or
// quarter forces the unit; auto counts by quarter while the dated saves
// span at most maxQuarters, else by year. Periods run oldest to newest,
// those without a save included, and past maxPeriods the oldest are one
// "before …" period. Rows are the areas by total, largest first (ties by
// label, then ID), the largest maxAreas each followed by its
// interestsPerArea largest interests and one row for the rest, then one
// row for the other areas; a run whose saves name no area is flat, its
// flatInterests largest interests and one row for the rest. Unsorted comes
// last. A row that counts nothing is left out. It is pure: the same saves
// give the same table.
func Timeline(saves []store.RunSave, labels map[string]string, window Window, period Period,
	loc *time.Location) TimelineTable {
	var dated []store.RunSave
	undated := 0
	for _, s := range saves {
		switch {
		case s.SavedAt.IsZero():
			undated++
		case window.contains(s.SavedAt):
			s.SavedAt = s.SavedAt.In(loc)
			dated = append(dated, s)
		}
	}
	cols := periodsOf(dated, period)
	t := TimelineTable{Unit: cols.unit, Periods: cols.labels, Dated: len(dated), Undated: undated}
	t.Rows = rowsOf(dated, labels, cols)
	return t
}

// columns are the timeline's periods: their unit, their labels, and the
// index of the period a save falls in.
type columns struct {
	unit   Period
	labels []string
	// first is the oldest period kept apart, as a quarter's index (4·year
	// + quarter, or 4·year for a year); a save before it is in the merged
	// period, at index 0 when there is one.
	first, last int
	merged      bool
}

// index is the column of a save on t.
func (c columns) index(t time.Time) int {
	q := c.quarter(t)
	switch {
	case q < c.first:
		return 0
	case c.merged:
		return 1 + c.step(q-c.first)
	}
	return c.step(q - c.first)
}

// quarter is t's quarter index, or its year's for a timeline by year.
func (c columns) quarter(t time.Time) int {
	if c.unit == PeriodYear {
		return 4 * t.Year()
	}
	return 4*t.Year() + (int(t.Month())-1)/3
}

// step is how many periods n quarters are.
func (c columns) step(n int) int {
	if c.unit == PeriodYear {
		return n / 4
	}
	return n
}

// periodsOf are the columns that hold dated: by quarter or year as period
// says (auto deciding by their span), oldest to newest, the oldest merged
// into one past maxPeriods.
func periodsOf(dated []store.RunSave, period Period) columns {
	c := columns{unit: period}
	if len(dated) == 0 {
		if c.unit != PeriodQuarter {
			c.unit = PeriodYear
		}
		return c
	}
	lo, hi := dated[0].SavedAt, dated[0].SavedAt
	for _, s := range dated[1:] {
		lo, hi = minTime(lo, s.SavedAt), maxTime(hi, s.SavedAt)
	}
	if c.unit == PeriodAuto {
		c.unit = PeriodYear
		if span := quarterOf(hi) - quarterOf(lo) + 1; span <= maxQuarters {
			c.unit = PeriodQuarter
		}
	}
	c.first, c.last = c.quarter(lo), c.quarter(hi)
	stride := 1
	if c.unit == PeriodYear {
		stride = 4
	}
	n := (c.last-c.first)/stride + 1
	if n > maxPeriods {
		c.first += (n - (maxPeriods - 1)) * stride
		c.merged = true
		c.labels = append(c.labels, "before "+c.label(c.first))
	}
	for q := c.first; q <= c.last; q += stride {
		c.labels = append(c.labels, c.label(q))
	}
	return c
}

// quarterOf is t's quarter index.
func quarterOf(t time.Time) int { return 4*t.Year() + (int(t.Month())-1)/3 }

// label names the period at quarter index q.
func (c columns) label(q int) string {
	if c.unit == PeriodYear {
		return strconv.Itoa(q / 4)
	}
	return fmt.Sprintf("%d Q%d", q/4, q%4+1)
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// counter counts a group's saves by period.
type counter struct {
	id, area string
	counts   []int
	total    int
}

func (c *counter) add(col int) {
	c.counts[col]++
	c.total++
}

// rowsOf are the table's rows over dated.
func rowsOf(dated []store.RunSave, labels map[string]string, cols columns) []TimelineRow {
	n := len(cols.labels)
	newCounter := func(id, area string) *counter { return &counter{id: id, area: area, counts: make([]int, n)} }
	areas, interests := map[string]*counter{}, map[string]*counter{}
	unsorted := newCounter("", "")
	flat := true
	for _, s := range dated {
		col := cols.index(s.SavedAt)
		if s.InterestID == "" {
			unsorted.add(col)
			continue
		}
		in := interests[s.InterestID]
		if in == nil {
			in = newCounter(s.InterestID, s.AreaID)
			interests[s.InterestID] = in
		}
		in.add(col)
		if s.AreaID != "" {
			flat = false
			a := areas[s.AreaID]
			if a == nil {
				a = newCounter(s.AreaID, "")
				areas[s.AreaID] = a
			}
			a.add(col)
		}
	}
	row := func(kind RowKind, c *counter) TimelineRow {
		return TimelineRow{Kind: kind, ID: c.id, AreaID: c.area, Label: labels[c.id], Counts: c.counts,
			Total: c.total}
	}
	var out []TimelineRow
	if flat {
		out = append(out, ranked(sorted(interests, labels), flatInterests, RowInterest, RowOtherInterests, n,
			"", row)...)
	} else {
		byArea := map[string]map[string]*counter{}
		other := newCounter("", "")
		merged := 0
		for _, in := range interests {
			if in.area == "" {
				// An interest outside every area, in a run of areas: its
				// saves count among the other areas.
				for col, k := range in.counts {
					other.counts[col] += k
				}
				other.total += in.total
				continue
			}
			if byArea[in.area] == nil {
				byArea[in.area] = map[string]*counter{}
			}
			byArea[in.area][in.id] = in
		}
		for i, a := range sorted(areas, labels) {
			if i >= maxAreas {
				merged++
				for col, k := range a.counts {
					other.counts[col] += k
				}
				other.total += a.total
				continue
			}
			out = append(out, row(RowArea, a))
			out = append(out, ranked(sorted(byArea[a.id], labels), interestsPerArea, RowInterest,
				RowOtherInterests, n, a.id, row)...)
		}
		if other.total > 0 {
			r := row(RowOtherAreas, other)
			r.Merged = merged
			out = append(out, r)
		}
	}
	if unsorted.total > 0 {
		out = append(out, row(RowUnsorted, unsorted))
	}
	return out
}

// sorted are counters by total, largest first, then by label and ID.
func sorted(of map[string]*counter, labels map[string]string) []*counter {
	out := make([]*counter, 0, len(of))
	for _, c := range of {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b *counter) int {
		return cmp.Or(cmp.Compare(b.total, a.total), cmp.Compare(labels[a.id], labels[b.id]), cmp.Compare(a.id, b.id))
	})
	return out
}

// ranked are the first most of cs as rows of kind, then the rest merged
// into one row of rest, in area.
func ranked(cs []*counter, most int, kind, rest RowKind, n int, area string,
	row func(RowKind, *counter) TimelineRow) []TimelineRow {
	var out []TimelineRow
	other := &counter{area: area, counts: make([]int, n)}
	merged := 0
	for i, c := range cs {
		if i < most {
			out = append(out, row(kind, c))
			continue
		}
		merged++
		for col, k := range c.counts {
			other.counts[col] += k
		}
		other.total += c.total
	}
	if merged > 0 {
		r := row(rest, other)
		r.Merged = merged
		out = append(out, r)
	}
	return out
}

// TimelineStore is what the interests' timeline reads: the latest done
// run, its saves and its identities' labels.
type TimelineStore interface {
	LatestRun(ctx context.Context, tenantID string, status store.InterestRunStatus) (*store.InterestRun, error)
	RunSaves(ctx context.Context, tenantID, runID string) ([]store.RunSave, error)
	GetInterests(ctx context.Context, tenantID string, ids []string) ([]store.Interest, error)
}

// ErrNoRun is a timeline asked for before any rebuild of the interests is
// done: there is nothing to count by.
var ErrNoRun = errors.New("no rebuild of the interests is done yet")

// ReadTimeline is the tenant's latest done run and its timeline
// (Timeline), or ErrNoRun. The run, its saves and its labels are separate
// reads: a rebuild that commits between them prunes the run read
// (Engine's prune), so the latest run is read again after them, and on a
// change everything is read once more from the newer run; a second change
// is answered as read.
func ReadTimeline(ctx context.Context, st TimelineStore, tenantID string, window Window, period Period,
	loc *time.Location) (*store.InterestRun, TimelineTable, error) {
	for attempt := 0; ; attempt++ {
		run, err := st.LatestRun(ctx, tenantID, store.InterestRunDone)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, TimelineTable{}, ErrNoRun
		case err != nil:
			return nil, TimelineTable{}, fmt.Errorf("read the latest rebuild of the interests: %w", err)
		}
		saves, err := st.RunSaves(ctx, tenantID, run.ID)
		if err != nil {
			return nil, TimelineTable{}, err
		}
		labels, err := Labels(ctx, st, tenantID, saves)
		if err != nil {
			return nil, TimelineTable{}, err
		}
		latest, err := st.LatestRun(ctx, tenantID, store.InterestRunDone)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, TimelineTable{}, fmt.Errorf("read the latest rebuild of the interests: %w", err)
		}
		if attempt > 0 || err == nil && latest.ID == run.ID {
			return run, Timeline(saves, labels, window, period, loc), nil
		}
	}
}

// Labels are the labels of the interests and areas saves name, by ID, in
// one read.
func Labels(ctx context.Context, st interface {
	GetInterests(ctx context.Context, tenantID string, ids []string) ([]store.Interest, error)
}, tenantID string, saves []store.RunSave) (map[string]string, error) {
	seen := map[string]bool{}
	var ids []string
	for _, s := range saves {
		for _, id := range []string{s.InterestID, s.AreaID} {
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	got, err := st.GetInterests(ctx, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("read the interests' labels: %w", err)
	}
	out := make(map[string]string, len(got))
	for _, in := range got {
		out[in.ID] = in.Label
	}
	return out, nil
}
