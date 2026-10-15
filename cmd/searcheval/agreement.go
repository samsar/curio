package main

import (
	"slices"
	"time"

	"github.com/samsar/curio/internal/eval"
	"github.com/samsar/curio/internal/search"
)

// verdict is how a plan's filter of one dimension compares with the
// query file's, the oracle's.
type verdict string

const (
	agreeSame     verdict = "same"     // the same days, or the same values
	agreeNarrower verdict = "narrower" // inside the file's
	agreeWider    verdict = "wider"    // holding the file's, and more
	agreeWrong    verdict = "wrong"    // neither: overlapping in part, or apart
	agreeMissing  verdict = "missing"  // the file sets the dimension, the plan doesn't
	agreeExtra    verdict = "extra"    // the plan sets it, the file doesn't
)

// verdicts are every verdict, in the order the report lists them.
var verdicts = []verdict{agreeSame, agreeNarrower, agreeWider, agreeWrong, agreeMissing, agreeExtra}

// agreementDims are the dimensions compared, in the report's order.
var agreementDims = []string{"saved", "published", "content_type", "site", "folder", "source"}

// agreement compares a plan's filters with a query's file filters (nil
// for none), dimension by dimension, for each dimension either sets. A
// window is compared in calendar days: the plan's bounds as days in loc,
// the planner's local midnights; the file's as written.
func agreement(file *eval.Filters, plan search.FilterRequest, loc *time.Location) map[string]verdict {
	var f eval.Filters
	if file != nil {
		f = *file
	}
	out := map[string]verdict{}
	for _, c := range []struct {
		dim string
		fn  func() (verdict, bool)
	}{
		{"saved", func() (verdict, bool) {
			return compareWindows(window{f.SavedAfter, f.SavedBefore, nil}, window{plan.SavedAfter, plan.SavedBefore, loc})
		}},
		{"published", func() (verdict, bool) {
			return compareWindows(window{f.PublishedAfter, f.PublishedBefore, nil},
				window{plan.PublishedAfter, plan.PublishedBefore, loc})
		}},
		// A query file names no type or browser: the plan's are extra.
		{"content_type", func() (verdict, bool) { return compareSets(nil, plan.ContentType) }},
		{"site", func() (verdict, bool) { return compareSets(f.Site, plan.Site) }},
		{"folder", func() (verdict, bool) { return compareSets(f.Folder, plan.Folder) }},
		{"source", func() (verdict, bool) { return compareSets(nil, plan.Source) }},
	} {
		if v, ok := c.fn(); ok {
			out[c.dim] = v
		}
	}
	return out
}

// window is a [after, before) window's bounds and the zone a time's day is
// read in; nil reads a time's day as written.
type window struct {
	after, before string
	loc           *time.Location
}

func (w window) set() bool { return w.after != "" || w.before != "" }

// days are the window's bounds as days, an open bound at the end of time
// it is open to.
func (w window) days() (from, until time.Time) {
	from, until = time.Time{}, time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	if d, ok := boundDay(w.after, w.loc); ok {
		from = d
	}
	if d, ok := boundDay(w.before, w.loc); ok {
		until = d
	}
	return from, until
}

// boundDay is a bound's calendar day, as midnight UTC: a day as written,
// or an RFC 3339 time's day in loc, or as written when loc is nil.
func boundDay(bound string, loc *time.Location) (time.Time, bool) {
	if t, err := time.Parse(time.DateOnly, bound); err == nil {
		return t, true
	}
	t, err := time.Parse(time.RFC3339, bound)
	if err != nil {
		return time.Time{}, false
	}
	if loc != nil {
		t = t.In(loc)
	}
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), true
}

// compareWindows is the plan's window against the file's; false when
// neither sets one.
func compareWindows(file, plan window) (verdict, bool) {
	switch {
	case !file.set() && !plan.set():
		return "", false
	case !plan.set():
		return agreeMissing, true
	case !file.set():
		return agreeExtra, true
	}
	ff, fu := file.days()
	pf, pu := plan.days()
	switch {
	case ff.Equal(pf) && fu.Equal(pu):
		return agreeSame, true
	case !pf.Before(ff) && !pu.After(fu):
		return agreeNarrower, true
	case !pf.After(ff) && !pu.Before(fu):
		return agreeWider, true
	}
	return agreeWrong, true
}

// compareSets is the plan's values of a list dimension against the
// file's; false when neither sets any.
func compareSets(file, plan []string) (verdict, bool) {
	switch {
	case len(file) == 0 && len(plan) == 0:
		return "", false
	case len(plan) == 0:
		return agreeMissing, true
	case len(file) == 0:
		return agreeExtra, true
	}
	inFile := func(v string) bool { return slices.Contains(file, v) }
	inPlan := func(v string) bool { return slices.Contains(plan, v) }
	planInFile, fileInPlan := allOf(plan, inFile), allOf(file, inPlan)
	switch {
	case planInFile && fileInPlan:
		return agreeSame, true
	case planInFile:
		return agreeNarrower, true
	case fileInPlan:
		return agreeWider, true
	}
	return agreeWrong, true
}

func allOf(values []string, ok func(string) bool) bool {
	for _, v := range values {
		if !ok(v) {
			return false
		}
	}
	return true
}
