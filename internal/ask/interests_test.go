package ask

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/retrieve"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// at is noon UTC on a day.
func at(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

// finishedAt is when the made-up rebuilds finished.
var finishedAt = time.Date(2025, 4, 1, 9, 0, 0, 0, time.UTC)

// grouped gives r's library a rebuild of two areas of made-up interests,
// saved over two years, one save undated.
func (r *rig) grouped() *store.InterestRun {
	run := &store.InterestRun{ID: "run-1", Shape: store.InterestShapeAreas, FinishedAt: &finishedAt}
	r.interests.runs = []*store.InterestRun{run}
	r.interests.saves = map[string][]store.RunSave{"run-1": {
		{DocumentID: "d1", InterestID: "in-bread", AreaID: "area-cook", SavedAt: at(2023, 2, 1)},
		{DocumentID: "d2", InterestID: "in-bread", AreaID: "area-cook", SavedAt: at(2024, 2, 1)},
		{DocumentID: "d3", InterestID: "in-soup", AreaID: "area-cook", SavedAt: at(2024, 3, 1)},
		{DocumentID: "d4", InterestID: "in-chains", AreaID: "area-bike", SavedAt: at(2023, 6, 1)},
		{DocumentID: "d5", SavedAt: at(2024, 7, 1)},
		{DocumentID: "d6", InterestID: "in-soup", AreaID: "area-cook"},
	}}
	r.interests.labels = map[string]string{"area-cook": "Cooking", "area-bike": "Cycling", "in-bread": "Bread",
		"in-soup": "Soup", "in-chains": "Chains"}
	return run
}

// tableOf is the Table event.
func tableOf(t *testing.T, events []Event) Table {
	t.Helper()
	for _, e := range events {
		if tb, ok := e.(Table); ok {
			return tb
		}
	}
	t.Fatal("no Table event")
	return Table{}
}

// goldenInterestsPrompt is the interests prompt for a made-up table.
func goldenInterestsPrompt() string {
	saves := []store.RunSave{
		{DocumentID: "d1", InterestID: "in-bread", AreaID: "area-cook", SavedAt: at(2023, 2, 1)},
		{DocumentID: "d2", InterestID: "in-soup", AreaID: "area-cook", SavedAt: at(2024, 2, 1)},
		{DocumentID: "d3", InterestID: "in-chains", AreaID: "area-bike", SavedAt: at(2024, 6, 1)},
		{DocumentID: "d4", SavedAt: at(2024, 7, 1)},
		{DocumentID: "d5", InterestID: "in-soup", AreaID: "area-cook"},
	}
	labels := map[string]string{"area-cook": "Cooking", "area-bike": "Cycling", "in-bread": "Bread",
		"in-soup": "Soup", "in-chains": "Chains"}
	tb := Table{FinishedAt: finishedAt, Timeline: insight.Timeline(saves, labels, insight.Window{}, insight.PeriodYear,
		time.UTC)}
	material := tb.mark(budget("how have my interests changed", interestsInstructionTokens, 768), time.UTC)
	return fill(interestsTemplate, material, "how have my interests changed", goldenDay, time.UTC)
}

// TestInterests: the table of the latest rebuild, every row marked in
// its order and the undated count last, reported after the retrieval
// event, which has no sources; the answer cites rows; nothing is
// searched or reranked; the events in their order.
func TestInterests(t *testing.T) {
	r := newRig(t, nil)
	r.grouped()
	r.writer.answer = answers("Bread came first [T2], then soup [T3]; 1 save had no date [T7], and [T99] is none.")
	events := r.ask(t, Request{Question: "how have my interests changed"})
	assert.Equal(t, []string{"progress retrieving", "retrieval", "table", "progress writing", "delta", "done"},
		order(events))
	assert.Empty(t, r.ret.queries, "nothing searched")
	require.Len(t, r.ret.understood, 1)
	assert.False(t, r.ret.understood[0].Rerank)
	got := retrievedOf(t, events)
	assert.Equal(t, RouteInterests, got.Route)
	assert.Empty(t, got.Sources)

	tb := tableOf(t, events)
	assert.Equal(t, "run-1", tb.RunID)
	assert.Equal(t, finishedAt, tb.FinishedAt)
	assert.Equal(t, insight.PeriodQuarter, tb.Timeline.Unit, "seven quarters of saves")
	kinds := make([]insight.RowKind, len(tb.Timeline.Rows))
	for i, row := range tb.Timeline.Rows {
		kinds[i] = row.Kind
	}
	assert.Equal(t, []insight.RowKind{insight.RowArea, insight.RowInterest, insight.RowInterest, insight.RowArea,
		insight.RowInterest, insight.RowUnsorted}, kinds)
	assert.Equal(t, []string{"T1", "T2", "T3", "T4", "T5", "T6"}, tb.Markers)
	assert.Equal(t, "T7", tb.UndatedMarker)
	assert.Equal(t, 7, tb.Shown)
	assert.Equal(t, 1, tb.Timeline.Undated)

	asks := r.writer.asks()
	require.Len(t, asks, 1)
	assert.Contains(t, asks[0], "Interests as of the grouping of 2025-04-01")
	assert.Contains(t, asks[0], "[T1] area: Cooking | total 3 |")
	assert.Contains(t, asks[0], "[T7] undated: 1 save on no known day, in no period")

	d := doneOf(t, events)
	assert.Equal(t, StatusAnswered, d.Status)
	assert.Equal(t, RouteInterests, d.Route)
	assert.Equal(t, []string{"T2", "T3", "T7"}, d.Markers)
	assert.Equal(t, []string{"T99"}, d.InvalidCitations)
	assert.Empty(t, d.Citations)
	assert.False(t, d.Uncited)
	assert.Positive(t, d.Timings.Sources)
}

// TestInterests_NoRun: a library not grouped yet says so before the
// writer is awaited: no plan, no model call.
func TestInterests_NoRun(t *testing.T) {
	r := newRig(t, nil)
	r.writer.loaded = false
	events := r.ask(t, Request{Question: "what was I into in 2021"})
	require.Len(t, events, 1)
	d := doneOf(t, events)
	assert.Equal(t, StatusNoSources, d.Status)
	assert.Equal(t, "the library hasn't been grouped yet", d.Reason)
	assert.Equal(t, RouteInterests, d.Route)
	assert.Empty(t, r.ret.understood)
	assert.Empty(t, r.writer.prompts)
	assert.Zero(t, r.writer.warms)
}

// TestInterests_Window: the plan's saved window bounds the table, every
// other dimension it set dropped, saying why; the request's own window
// wins over the plan's.
func TestInterests_Window(t *testing.T) {
	r := newRig(t, nil)
	r.grouped()
	plan := &understand.Plan{Kind: understand.KindList, Filters: search.FilterRequest{SavedAfter: "2024-01-01",
		SavedBefore: "2025-01-01", Host: []string{"example.com"}}}
	r.ret.got.Plan = plan
	r.ret.got.Understanding = &retrieve.Understanding{Status: understand.StatusPlanned}
	events := r.ask(t, Request{Question: "what did I save most last year", Understand: true})
	tb := tableOf(t, events)
	assert.Equal(t, at(2024, 1, 1).Truncate(24*time.Hour), tb.Window.After)
	assert.Equal(t, 3, tb.Timeline.Dated, "2024's saves alone")
	u := retrievedOf(t, events).Understanding
	require.NotNil(t, u)
	require.Len(t, u.Dropped, 1)
	assert.Equal(t, understand.Dropped{Field: "host", Reason: "the interests table counts by saved date alone"},
		u.Dropped[0])
	assert.Empty(t, u.Plan.Filters.Host, "the plan as applied")
	assert.Equal(t, "2024-01-01", u.Plan.Filters.SavedAfter)

	events = r.ask(t, Request{Question: "what did I save most last year", Understand: true,
		Filters: search.FilterRequest{SavedAfter: "2023-01-01", SavedBefore: "2024-01-01"}})
	tb = tableOf(t, events)
	assert.Equal(t, 2, tb.Timeline.Dated, "the request's window wins: 2023's saves")
}

// TestInterests_RowNotShownIsInvalid: rows the prompt couldn't hold have
// no marker, and a citation past the shown rows is invalid.
func TestInterests_RowNotShownIsInvalid(t *testing.T) {
	r := newRig(t, func(o *Options) { o.MaxTokens = config.MaxAskTokens })
	r.grouped()
	saves := r.interests.saves["run-1"]
	for a := range 14 {
		area := fmt.Sprintf("area-%02d", a)
		r.interests.labels[area] = hostile(200)
		for i := range 5 {
			interest := fmt.Sprintf("%s-in-%d", area, i)
			r.interests.labels[interest] = hostile(200)
			for d := range 3 {
				saves = append(saves, store.RunSave{DocumentID: fmt.Sprintf("%s-%d", interest, d),
					InterestID: interest, AreaID: area, SavedAt: at(1995+(a+i+d)%30, time.Month(1+d), 1)})
			}
		}
	}
	r.interests.saves["run-1"] = saves
	r.writer.answer = answers("Many areas [T1], and a row left out [T60].")
	events := r.ask(t, Request{Question: "how have my interests changed", Mode: RouteInterests})
	tb := tableOf(t, events)
	marked := 0
	for _, m := range tb.Markers {
		if m != "" {
			marked++
		}
	}
	assert.Less(t, marked, len(tb.Timeline.Rows), "some rows left out")
	assert.Equal(t, tb.Shown-1, marked, "the marked rows, and the undated count")
	assert.Less(t, tb.Shown, 60)
	assert.Equal(t, "T"+strconv.Itoa(tb.Shown), tb.UndatedMarker, "the undated count is always shown, last")
	d := doneOf(t, events)
	assert.Equal(t, []string{"T1"}, d.Markers)
	assert.Equal(t, []string{"T60"}, d.InvalidCitations)
}

// TestInterests_RunChangesMidRead: a rebuild that commits between the
// run read and the saves read is caught, the newer run's table answered.
func TestInterests_RunChangesMidRead(t *testing.T) {
	r := newRig(t, nil)
	run := r.grouped()
	newer := &store.InterestRun{ID: "run-2", Shape: store.InterestShapeFlat, FinishedAt: &finishedAt}
	r.interests.runs = []*store.InterestRun{run, run, newer}
	r.interests.saves["run-2"] = []store.RunSave{{DocumentID: "d9", InterestID: "in-bread", SavedAt: at(2024, 1, 1)}}
	events := r.ask(t, Request{Question: "how have my interests changed"})
	tb := tableOf(t, events)
	assert.Equal(t, "run-2", tb.RunID)
	assert.Equal(t, 1, tb.Timeline.Dated)
}

// TestInterestsTable_Fits: the largest table a library can make (25
// periods, 12 areas and the rest, 3 interests and the rest each, Unsorted)
// of 200-byte hostile labels fits the context at the longest answer and
// the longest question, the undated count shown.
func TestInterestsTable_Fits(t *testing.T) {
	rows := make([]insight.TimelineRow, 0, 62)
	counts := make([]int, 25)
	for i := range counts {
		counts[i] = 99999
	}
	for a := range 12 {
		rows = append(rows, insight.TimelineRow{Kind: insight.RowArea, ID: fmt.Sprint("a", a), Label: hostile(200),
			Counts: counts, Total: 999999})
		for i := range 3 {
			rows = append(rows, insight.TimelineRow{Kind: insight.RowInterest, ID: fmt.Sprint("i", a, i),
				Label: hostile(200), Counts: counts, Total: 999999})
		}
		rows = append(rows, insight.TimelineRow{Kind: insight.RowOtherInterests, Counts: counts, Total: 999999,
			Merged: 99})
	}
	rows = append(rows, insight.TimelineRow{Kind: insight.RowOtherAreas, Counts: counts, Total: 999999, Merged: 99},
		insight.TimelineRow{Kind: insight.RowUnsorted, Counts: counts, Total: 999999})
	periods := make([]string, 25)
	for i := range periods {
		periods[i] = fmt.Sprintf("%d Q%d", 2000+i, i%4+1)
	}
	question := strings.Repeat("9", MaxQuestionBytes)
	tb := Table{FinishedAt: finishedAt, Timeline: insight.TimelineTable{Unit: insight.PeriodQuarter,
		Periods: periods, Rows: rows, Dated: 999999, Undated: 999999}}
	material := tb.mark(budget(question, interestsInstructionTokens, config.MaxAskTokens), time.UTC)
	prompt := fill(interestsTemplate, material, question, goldenDay, time.UTC)
	fits(t, interestsTemplate, prompt, interestsInstructionTokens, config.MaxAskTokens, "interests")
	assert.Contains(t, material, "["+tb.UndatedMarker+"] undated: 999999 saves")
	assert.Positive(t, tb.Shown-1, "some rows shown")
	assert.NotEmpty(t, tb.Markers[0], "the largest area kept first")
}

// TestInterestsTable_AllUndated: a table whose every save is undated names
// no period: no empty periods line, the undated count marked as ever.
func TestInterestsTable_AllUndated(t *testing.T) {
	saves := []store.RunSave{{DocumentID: "d1", InterestID: "in-bread", AreaID: "area-cook"},
		{DocumentID: "d2"}}
	tb := Table{FinishedAt: finishedAt, Timeline: insight.Timeline(saves, map[string]string{"area-cook": "Cooking",
		"in-bread": "Bread"}, insight.Window{}, insight.PeriodAuto, time.UTC)}
	require.Empty(t, tb.Timeline.Periods)
	material := tb.mark(budget("how have my interests changed", interestsInstructionTokens, 768), time.UTC)
	assert.NotContains(t, material, "Periods")
	assert.True(t, strings.HasPrefix(material, "Interests as of the grouping of 2025-04-01, counted by "), material)
	assert.Contains(t, material, "\n[T"+strconv.Itoa(tb.Shown)+"] undated: 2 saves on no known day, in no period")
}

// TestInterests_LogsNoContent: no record of an interests ask carries the
// question, a label or the answer.
func TestInterests_LogsNoContent(t *testing.T) {
	r := newRig(t, nil)
	r.grouped()
	r.writer.answer = answers("Bread rose [T2].")
	r.ask(t, Request{Question: "how have my interests changed"})
	logs := r.logs.String()
	for _, secret := range []string{"interests changed", "Cooking", "Bread"} {
		assert.NotContains(t, logs, secret)
	}
}
