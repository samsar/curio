package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// InterestsTimeline mirrors the openapi InterestsTimeline schema: the
// latest rebuild's interests by when their documents were first saved, as
// insight.Timeline counts them. An ask's table event carries the same
// object, each row the prompt showed marked.
type InterestsTimeline struct {
	Run     TimelineRun     `json:"run"`
	Period  string          `json:"period"`
	Periods []string        `json:"periods"`
	Rows    []TimelineRow   `json:"rows"`
	Dated   int             `json:"dated"`
	Undated int             `json:"undated"`
	Window  *TimelineWindow `json:"window,omitempty"`
}

// TimelineRun is the rebuild a timeline counts by.
type TimelineRun struct {
	ID         string    `json:"id"`
	FinishedAt time.Time `json:"finished_at"`
	Shape      string    `json:"shape"`
}

// TimelineRow mirrors insight.TimelineRow; Marker is the row's citation
// mark in an ask's table event ("T3"), on the rows the prompt showed.
type TimelineRow struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	AreaID string `json:"area_id,omitempty"`
	Label  string `json:"label"`
	Counts []int  `json:"counts"`
	Total  int    `json:"total"`
	Merged int    `json:"merged,omitempty"`
	Marker string `json:"marker,omitempty"`
}

// TimelineWindow is the saved window a timeline counted, as applied: a
// bound left open is absent.
type TimelineWindow struct {
	SavedAfter  *time.Time `json:"saved_after,omitempty"`
	SavedBefore *time.Time `json:"saved_before,omitempty"`
}

// InterestsProblemType is the problem type of interests asked for before
// any rebuild of them is done.
const InterestsProblemType = "urn:curio:problem:interests-unavailable"

// interestsNoRun is the reason of InterestsProblemType: no rebuild is done.
const interestsNoRun = "no_run"

// InterestsUnavailable is the 404 problem of interests not grouped yet.
type InterestsUnavailable struct {
	Problem
	Reason string `json:"reason"`
}

// interestsUnavailableError is no rebuild of the interests being done.
type interestsUnavailableError struct{ body InterestsUnavailable }

func (e *interestsUnavailableError) Error() string { return e.body.Detail }

func (*interestsUnavailableError) problem() (int, string, string) {
	return http.StatusNotFound, "interests unavailable", InterestsProblemType
}

func (e *interestsUnavailableError) withProblem(p Problem) any {
	b := e.body
	b.Problem = p
	return b
}

// handleInterestsTimeline answers GET /v1/interests/timeline.
func (d Deps) handleInterestsTimeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	resp, err := d.interestsTimeline(r.Context(), q.Get("saved_after"), q.Get("saved_before"), q.Get("period"))
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// interestsTimeline is the latest rebuild's interests, their saves counted
// by period: savedAfter and savedBefore bound the saves counted, read as
// POST /v1/search reads its filters, and period is auto, year or quarter
// ("" auto). A bound or a period it can't take is a requestError naming
// its query parameter, and no rebuild done an *interestsUnavailableError.
func (d Deps) interestsTimeline(ctx context.Context, savedAfter, savedBefore, period string) (InterestsTimeline,
	error) {
	resolved, err := search.FilterRequest{SavedAfter: savedAfter, SavedBefore: savedBefore}.Resolve()
	if fe, ok := errors.AsType[*search.FilterError](err); ok {
		// The parameters are named as the search's filters' fields are,
		// but stand alone: no "filters." before them.
		return InterestsTimeline{}, badRequest("%s: %s", fe.Field, fe.Reason)
	}
	if err != nil {
		return InterestsTimeline{}, &requestError{err: err}
	}
	unit := insight.Period(cmp.Or(period, string(insight.PeriodAuto)))
	if !slices.Contains(insight.Periods(), unit) {
		return InterestsTimeline{}, badRequest("period must be auto, year or quarter, got %q", period)
	}
	window := insight.Window{After: resolved.SavedAfter, Before: resolved.SavedBefore}
	run, table, err := insight.ReadTimeline(ctx, d.Insights, d.TenantID, window, unit, d.location())
	if errors.Is(err, insight.ErrNoRun) {
		return InterestsTimeline{}, &interestsUnavailableError{body: InterestsUnavailable{Reason: interestsNoRun,
			Problem: Problem{Detail: "no rebuild of the interests is done yet: the first one groups the library"}}}
	}
	if err != nil {
		return InterestsTimeline{}, err
	}
	return timelineOf(run, table, window)
}

// timelineOf is run's table in the wire shape, its window as applied. A
// done run without a finish time is an inconsistency.
func timelineOf(run *store.InterestRun, table insight.TimelineTable, window insight.Window) (InterestsTimeline,
	error) {
	if run.FinishedAt == nil {
		return InterestsTimeline{}, fmt.Errorf("done run %s has no finish time", run.ID)
	}
	return timelineView(run.ID, *run.FinishedAt, run.Shape, table, window), nil
}

// timelineView is a table in the wire shape: the run it counts by, its
// periods and rows, and its window as applied.
func timelineView(runID string, finishedAt time.Time, shape store.InterestShape, table insight.TimelineTable,
	window insight.Window) InterestsTimeline {
	out := InterestsTimeline{Run: TimelineRun{ID: runID, FinishedAt: finishedAt.UTC(), Shape: string(shape)},
		Period: string(table.Unit), Periods: table.Periods, Rows: make([]TimelineRow, 0, len(table.Rows)),
		Dated: table.Dated, Undated: table.Undated}
	if out.Periods == nil {
		out.Periods = []string{}
	}
	for _, row := range table.Rows {
		out.Rows = append(out.Rows, TimelineRow{Kind: string(row.Kind), ID: row.ID, AreaID: row.AreaID,
			Label: row.Label, Counts: row.Counts, Total: row.Total, Merged: row.Merged})
	}
	if !window.After.IsZero() || !window.Before.IsZero() {
		out.Window = &TimelineWindow{}
		if !window.After.IsZero() {
			after := window.After.UTC()
			out.Window.SavedAfter = &after
		}
		if !window.Before.IsZero() {
			before := window.Before.UTC()
			out.Window.SavedBefore = &before
		}
	}
	return out
}

// location is the person's zone, which the interests' timeline counts
// days in.
func (d Deps) location() *time.Location {
	if d.Location != nil {
		return d.Location
	}
	return time.Local
}
