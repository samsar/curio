package insight

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// on is noon UTC on a day.
func on(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

// save is a document saved at t, in interest of area ("" for none).
func save(doc, interest, area string, t time.Time) store.RunSave {
	return store.RunSave{DocumentID: doc, InterestID: interest, AreaID: area, SavedAt: t}
}

var timelineLabels = map[string]string{"area-a": "Cooking", "area-b": "Cycling", "in-bread": "Bread",
	"in-soup": "Soup", "in-chains": "Chains", "retired": "Retired long ago"}

// TestTimeline_AreasByQuarter: each document counted once, under its
// interest (a member, a loose fit or placed since alike) and that
// interest's area, by the quarter it was first saved; undated documents
// counted apart and in no period; Unsorted last; an identity the run
// doesn't hold absent.
func TestTimeline_AreasByQuarter(t *testing.T) {
	saves := []store.RunSave{
		save("d1", "in-bread", "area-a", on(2024, 1, 10)),
		save("d2", "in-bread", "area-a", on(2024, 5, 1)),
		save("d3", "in-soup", "area-a", on(2024, 5, 2)),
		save("d4", "in-chains", "area-b", on(2024, 2, 1)),
		save("d5", "", "", on(2024, 8, 1)),
		{DocumentID: "d6", InterestID: "in-soup", AreaID: "area-a"},
		{DocumentID: "d7"},
	}
	got := Timeline(saves, timelineLabels, Window{}, PeriodAuto, time.UTC)
	assert.Equal(t, PeriodQuarter, got.Unit)
	assert.Equal(t, []string{"2024 Q1", "2024 Q2", "2024 Q3"}, got.Periods)
	assert.Equal(t, 5, got.Dated)
	assert.Equal(t, 2, got.Undated)
	assert.Equal(t, []TimelineRow{
		{Kind: RowArea, ID: "area-a", Label: "Cooking", Counts: []int{1, 2, 0}, Total: 3},
		{Kind: RowInterest, ID: "in-bread", AreaID: "area-a", Label: "Bread", Counts: []int{1, 1, 0}, Total: 2},
		{Kind: RowInterest, ID: "in-soup", AreaID: "area-a", Label: "Soup", Counts: []int{0, 1, 0}, Total: 1},
		{Kind: RowArea, ID: "area-b", Label: "Cycling", Counts: []int{1, 0, 0}, Total: 1},
		{Kind: RowInterest, ID: "in-chains", AreaID: "area-b", Label: "Chains", Counts: []int{1, 0, 0}, Total: 1},
		{Kind: RowUnsorted, Counts: []int{0, 0, 1}, Total: 1},
	}, got.Rows)
}

// TestTimeline_Window: only saves in [after, before) count, and the
// periods are theirs; undated documents count whatever the window.
func TestTimeline_Window(t *testing.T) {
	saves := []store.RunSave{
		save("d1", "in-bread", "area-a", on(2022, 6, 1)),
		save("d2", "in-bread", "area-a", on(2023, 1, 1)),
		save("d3", "in-bread", "area-a", on(2023, 12, 31)),
		save("d4", "in-bread", "area-a", on(2024, 1, 1)),
		{DocumentID: "d5", InterestID: "in-bread", AreaID: "area-a"},
	}
	w := Window{After: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC), Before: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	got := Timeline(saves, timelineLabels, w, PeriodYear, time.UTC)
	assert.Equal(t, []string{"2023"}, got.Periods)
	assert.Equal(t, 2, got.Dated)
	assert.Equal(t, 1, got.Undated)
	require.NotEmpty(t, got.Rows)
	assert.Equal(t, 2, got.Rows[0].Total)
}

// TestTimeline_Unit: auto counts by quarter while the saves span at most
// 12 quarters, and by year from 13; year and quarter force the unit.
func TestTimeline_Unit(t *testing.T) {
	twelve := make([]store.RunSave, 0, 3)
	twelve = append(twelve, save("d1", "in-bread", "area-a", on(2022, 1, 5)),
		save("d2", "in-bread", "area-a", on(2024, 12, 5)))
	got := Timeline(twelve, timelineLabels, Window{}, PeriodAuto, time.UTC)
	assert.Equal(t, PeriodQuarter, got.Unit)
	assert.Len(t, got.Periods, 12)
	assert.Equal(t, "2022 Q1", got.Periods[0])
	assert.Equal(t, "2024 Q4", got.Periods[11])

	thirteen := append(slices.Clone(twelve), save("d3", "in-bread", "area-a", on(2021, 12, 5)))
	got = Timeline(thirteen, timelineLabels, Window{}, PeriodAuto, time.UTC)
	assert.Equal(t, PeriodYear, got.Unit)
	assert.Equal(t, []string{"2021", "2022", "2023", "2024"}, got.Periods)
	assert.Equal(t, []int{1, 1, 0, 1}, got.Rows[0].Counts, "zeros included")

	got = Timeline(twelve, timelineLabels, Window{}, PeriodYear, time.UTC)
	assert.Equal(t, []string{"2022", "2023", "2024"}, got.Periods)
	got = Timeline(thirteen, timelineLabels, Window{}, PeriodQuarter, time.UTC)
	assert.Len(t, got.Periods, 13)
}

// TestTimeline_MergesPast25: past 25 periods, the oldest are one "before"
// period, the 24 newest apart.
func TestTimeline_MergesPast25(t *testing.T) {
	var saves []store.RunSave
	for y := 1990; y <= 2025; y++ {
		saves = append(saves, save(fmt.Sprintf("d%d", y), "in-bread", "area-a", on(y, 6, 1)))
	}
	got := Timeline(saves, timelineLabels, Window{}, PeriodAuto, time.UTC)
	assert.Equal(t, PeriodYear, got.Unit)
	require.Len(t, got.Periods, 25)
	assert.Equal(t, "before 2002", got.Periods[0])
	assert.Equal(t, "2002", got.Periods[1])
	assert.Equal(t, "2025", got.Periods[24])
	assert.Equal(t, 12, got.Rows[0].Counts[0], "1990 to 2001")
	assert.Equal(t, 1, got.Rows[0].Counts[1])
	assert.Equal(t, 36, got.Rows[0].Total)
}

// TestTimeline_LocalYear: a save at 23:30 on 31 December in the person's
// zone counts in that year, though it is the next year in UTC.
func TestTimeline_LocalYear(t *testing.T) {
	zone := time.FixedZone("UTC-5", -5*60*60)
	late := time.Date(2023, 12, 31, 23, 30, 0, 0, zone)
	saves := []store.RunSave{save("d1", "in-bread", "area-a", late.UTC()),
		save("d2", "in-bread", "area-a", on(2021, 3, 1))}
	got := Timeline(saves, timelineLabels, Window{}, PeriodYear, zone)
	assert.Equal(t, []string{"2021", "2022", "2023"}, got.Periods)
	assert.Equal(t, []int{1, 0, 1}, got.Rows[0].Counts)
}

// TestTimeline_Caps: the 12 largest areas apart, the rest one row that
// says how many it merged; each area's 3 largest interests apart, the
// rest one row.
func TestTimeline_Caps(t *testing.T) {
	var saves []store.RunSave
	labels := map[string]string{}
	for a := range 14 {
		area := fmt.Sprintf("area-%02d", a)
		labels[area] = area
		for i := range 5 {
			interest := fmt.Sprintf("%s-in-%d", area, i)
			labels[interest] = interest
			for d := range 20 - a + i {
				saves = append(saves, save(fmt.Sprintf("%s-%d", interest, d), interest, area, on(2024, 3, 1)))
			}
		}
	}
	got := Timeline(saves, labels, Window{}, PeriodAuto, time.UTC)
	var areas, others, otherInterests int
	for _, r := range got.Rows {
		switch r.Kind {
		case RowArea:
			areas++
		case RowOtherAreas:
			others++
			assert.Equal(t, 2, r.Merged)
		case RowOtherInterests:
			otherInterests++
			assert.Equal(t, 2, r.Merged)
		case RowInterest, RowUnsorted:
		}
	}
	assert.Equal(t, 12, areas)
	assert.Equal(t, 1, others)
	assert.Equal(t, 12, otherInterests)
	assert.Equal(t, RowArea, got.Rows[0].Kind)
	assert.Equal(t, "area-00", got.Rows[0].ID, "the largest first")
	assert.Equal(t, "area-00-in-4", got.Rows[1].ID, "its largest interest next")
	assert.Equal(t, RowOtherAreas, got.Rows[len(got.Rows)-1].Kind)
}

// TestTimeline_Flat: a run whose saves name no area lists its 15 largest
// interests, then the rest as one row, then Unsorted.
func TestTimeline_Flat(t *testing.T) {
	var saves []store.RunSave
	for i := range 17 {
		interest := fmt.Sprintf("in-%02d", i)
		for d := range 20 - i {
			saves = append(saves, save(fmt.Sprintf("%s-%d", interest, d), interest, "", on(2024, 3, 1)))
		}
	}
	saves = append(saves, save("loose-end", "", "", on(2024, 3, 2)))
	got := Timeline(saves, nil, Window{}, PeriodAuto, time.UTC)
	require.Len(t, got.Rows, 17)
	for _, r := range got.Rows[:15] {
		assert.Equal(t, RowInterest, r.Kind)
		assert.Empty(t, r.AreaID)
	}
	assert.Equal(t, "in-00", got.Rows[0].ID)
	assert.Equal(t, TimelineRow{Kind: RowOtherInterests, Counts: []int{9}, Total: 9, Merged: 2}, got.Rows[15])
	assert.Equal(t, RowUnsorted, got.Rows[16].Kind)
}

// TestTimeline_Empty: no dated save is no period and no row.
func TestTimeline_Empty(t *testing.T) {
	got := Timeline([]store.RunSave{{DocumentID: "d1", InterestID: "in-bread"}}, timelineLabels, Window{}, PeriodAuto,
		time.UTC)
	assert.Equal(t, TimelineTable{Unit: PeriodYear, Undated: 1}, got)
}

// timelineStore is a run's saves and labels, a hook between the reads.
type timelineStore struct {
	runs   []*store.InterestRun // the latest run, read in turn; the last stays
	reads  int
	saves  map[string][]store.RunSave
	labels []store.Interest
}

func (s *timelineStore) LatestRun(context.Context, string, store.InterestRunStatus) (*store.InterestRun, error) {
	if len(s.runs) == 0 {
		return nil, store.ErrNotFound
	}
	run := s.runs[min(s.reads, len(s.runs)-1)]
	s.reads++
	return run, nil
}

func (s *timelineStore) RunSaves(_ context.Context, _, runID string) ([]store.RunSave, error) {
	return s.saves[runID], nil
}

func (s *timelineStore) GetInterests(context.Context, string, []string) ([]store.Interest, error) {
	return s.labels, nil
}

// TestReadTimeline: the latest done run's table, with its labels; none
// is ErrNoRun; a rebuild that commits between the run read and the rows
// read is caught, and the newer run's table answered.
func TestReadTimeline(t *testing.T) {
	ctx := context.Background()
	_, _, err := ReadTimeline(ctx, &timelineStore{}, "local", Window{}, PeriodAuto, time.UTC)
	require.ErrorIs(t, err, ErrNoRun)

	older, newer := &store.InterestRun{ID: "older"}, &store.InterestRun{ID: "newer"}
	st := &timelineStore{runs: []*store.InterestRun{older, newer},
		saves: map[string][]store.RunSave{
			"older": {save("d1", "in-bread", "area-a", on(2024, 1, 1))},
			"newer": {save("d1", "in-soup", "area-a", on(2024, 1, 1)), save("d2", "in-soup", "area-a", on(2024, 1, 2))},
		},
		labels: []store.Interest{{ID: "area-a", Label: "Cooking"}, {ID: "in-soup", Label: "Soup"}}}
	run, table, err := ReadTimeline(ctx, st, "local", Window{}, PeriodAuto, time.UTC)
	require.NoError(t, err)
	assert.Equal(t, "newer", run.ID)
	assert.Equal(t, 2, table.Dated)
	require.Len(t, table.Rows, 2)
	assert.Equal(t, "Cooking", table.Rows[0].Label)
	assert.Equal(t, "Soup", table.Rows[1].Label)
	assert.Equal(t, 4, st.reads, "the run read, checked, read again and checked")
}
