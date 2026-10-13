package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/samsar/curio/internal/eval"
)

// baseline is an earlier run's report, which this run is compared with,
// and how their queries match.
type baseline struct {
	path   string
	report *report
	// byID: the query file changed since the baseline, and its queries
	// are matched by id; otherwise by index, the file being the same.
	byID bool
}

// loadBaseline reads the report at path and checks that its queries can
// be matched with the file's, before the database is opened: comparing
// other queries would report the edit as a change in search quality.
func loadBaseline(path string, qf queryFile) (*baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the -baseline file: %w", err)
	}
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("the baseline %s is not a searcheval report: %w", path, err)
	}
	switch {
	case rep.Format != reportFormat:
		return nil, fmt.Errorf("the baseline %s is not a searcheval report of format %d (it has format %d)",
			path, reportFormat, rep.Format)
	case rep.queryCount() == 0:
		return nil, fmt.Errorf("the baseline %s is not a searcheval report: it holds no queries", path)
	}
	byID, err := matchQueries(&rep, qf)
	if err != nil {
		return nil, fmt.Errorf("the baseline %s: %w", path, err)
	}
	return &baseline{path: path, report: &rep, byID: byID}, nil
}

// maxIDsNamed caps the ids a refusal names.
const maxIDsNamed = 10

// matchQueries says how base's queries match the file's: by index when
// base was made from the same bytes, by id when it wasn't and every query
// on both sides has an id, the same ids on both. Anything else can't be
// compared.
func matchQueries(base *report, qf queryFile) (byID bool, err error) {
	if base.Provenance.QueriesSHA256 == qf.sha256 {
		return false, nil
	}
	var baseIDs, fileIDs []string
	complete := true
	for _, q := range base.Variants[0].Queries {
		baseIDs = append(baseIDs, q.ID)
		complete = complete && q.ID != ""
	}
	for _, q := range qf.set.Queries {
		fileIDs = append(fileIDs, q.ID)
		complete = complete && q.ID != ""
	}
	var oneSide []string
	for _, id := range baseIDs {
		if id != "" && !slices.Contains(fileIDs, id) {
			oneSide = append(oneSide, id+" (the baseline's)")
		}
	}
	for _, id := range fileIDs {
		if id != "" && !slices.Contains(baseIDs, id) {
			oneSide = append(oneSide, id+" (this file's)")
		}
	}
	if complete && len(oneSide) == 0 && len(baseIDs) == len(fileIDs) {
		return true, nil
	}
	why := "the ids differ"
	if !complete {
		why = "not every query has an id"
	}
	if len(oneSide) > 0 {
		why += "; on one side only: " + strings.Join(oneSide[:min(len(oneSide), maxIDsNamed)], ", ")
		if len(oneSide) > maxIDsNamed {
			why += fmt.Sprintf(" and %d more", len(oneSide)-maxIDsNamed)
		}
	}
	return false, fmt.Errorf("it was made from another query file (sha256 %s; this one is %s), and its queries "+
		"can't be matched by id: %s", base.Provenance.QueriesSHA256, qf.sha256, why)
}

// header says what the run is compared with: how the queries match, what
// the runs differ in besides their results, and the variants only one of
// them ran.
func (b *baseline) header(w *bytes.Buffer, cur *report) {
	match := "queries matched by index"
	if b.byID {
		match = "queries matched by id: the query file changed since the baseline"
	}
	fmt.Fprintf(w, "Baseline: %s (generated %s), %s\n", b.path, b.report.GeneratedAt.Format("2006-01-02 15:04 UTC"),
		match)
	if diffs := provenanceDiffs(cur.Provenance, b.report.Provenance); len(diffs) > 0 {
		fmt.Fprintf(w, "Differs from the baseline in: %s\n", strings.Join(diffs, ", "))
	} else {
		w.WriteString("The same database, schema, embedding model, query prefix, search settings and curio version " +
			"as the baseline\n")
	}
	onlyBase, onlyCur := variantsOnOneSide(cur, b.report)
	if len(onlyBase) > 0 {
		fmt.Fprintf(w, "Variants only in the baseline: %s\n", strings.Join(onlyBase, ", "))
	}
	if len(onlyCur) > 0 {
		fmt.Fprintf(w, "Variants only in this run: %s\n", strings.Join(onlyCur, ", "))
	}
}

// provenanceDiffs names what, besides its results, cur's run differs from
// base's in.
func provenanceDiffs(cur, base provenance) []string {
	var out []string
	for _, d := range []struct {
		name    string
		differs bool
	}{
		{"database", cur.Database != base.Database},
		{"schema", cur.Schema != base.Schema},
		{"embedding model", cur.Embedding.Model != base.Embedding.Model},
		{"query prefix", cur.Embedding.QueryPrefix != base.Embedding.QueryPrefix},
		{"search settings", cur.Search != base.Search},
		{"curio version", cur.Version != base.Version || cur.Commit != base.Commit},
	} {
		if d.differs {
			out = append(out, d.name)
		}
	}
	return out
}

// variantsOnOneSide are the variants only base ran, and only cur did.
func variantsOnOneSide(cur, base *report) (onlyBase, onlyCur []string) {
	has := func(r *report, name string) bool {
		return slices.ContainsFunc(r.Variants, func(v variantReport) bool { return v.Name == name })
	}
	for _, v := range base.Variants {
		if !has(cur, v.Name) {
			onlyBase = append(onlyBase, v.Name)
		}
	}
	for _, v := range cur.Variants {
		if !has(base, v.Name) {
			onlyCur = append(onlyCur, v.Name)
		}
	}
	return onlyBase, onlyCur
}

// groupMetrics are the scores a comparison takes the difference of, in
// the order it shows them; nil where a score doesn't apply.
var groupMetrics = []struct {
	name string
	of   func(eval.GroupScore) *float64
}{
	{"R@10", func(g eval.GroupScore) *float64 { return &g.RecallAt10 }},
	{"R@100", func(g eval.GroupScore) *float64 { return &g.RecallAt100 }},
	{"R@10hi", func(g eval.GroupScore) *float64 { return g.HighRecallAt10 }},
	{"MRR", func(g eval.GroupScore) *float64 { return &g.MRR }},
	{"nDCG@10", func(g eval.GroupScore) *float64 { return &g.NDCGAt10 }},
	{"S@1", func(g eval.GroupScore) *float64 { return &g.SuccessAt1 }},
	{"S@5", func(g eval.GroupScore) *float64 { return &g.SuccessAt5 }},
	{"S@10", func(g eval.GroupScore) *float64 { return &g.SuccessAt10 }},
}

// groupDelta is a group's change since the baseline: each metric's
// difference, current minus baseline (nil where a side has none), or the
// side the group is on alone.
type groupDelta struct {
	label  string
	deltas []*float64
	only   string // "", "the baseline" or "this run"
}

// movedQuery is a query whose nDCG@10 moved since the baseline.
type movedQuery struct {
	label    string
	old, new float64
}

// moveThreshold is how far a query's nDCG@10 must move to be listed.
const moveThreshold = 0.05

// variantComparison is a variant's change since the baseline.
type variantComparison struct {
	groups []groupDelta
	moved  []movedQuery
}

// compareVariant shows how cur, a variant of this run, changed since the
// baseline's run of it; nothing when the baseline didn't run it.
func (b *baseline) compareVariant(w *bytes.Buffer, cur variantReport) {
	i := slices.IndexFunc(b.report.Variants, func(v variantReport) bool { return v.Name == cur.Name })
	if i < 0 {
		return
	}
	c := compareVariants(cur, b.report.Variants[i], b.byID)
	fmt.Fprintf(w, "\nAgainst the baseline's %s:\n\n", cur.Name)
	header := []string{"group"}
	for _, m := range groupMetrics {
		header = append(header, m.name)
	}
	rows := [][]string{header}
	for _, g := range c.groups {
		if g.only != "" {
			rows = append(rows, []string{g.label, "only in " + g.only})
			continue
		}
		row := []string{g.label}
		for _, d := range g.deltas {
			row = append(row, signed(d))
		}
		rows = append(rows, row)
	}
	table(w, rows)
	if len(c.moved) == 0 {
		fmt.Fprintf(w, "\nnDCG@10: none moved by %.2f or more\n", moveThreshold)
		return
	}
	fmt.Fprintf(w, "\nnDCG@10 moved by %.2f or more:\n", moveThreshold)
	for _, m := range c.moved {
		fmt.Fprintf(w, "  %s  %.3f → %.3f\n", m.label, m.old, m.new)
	}
}

// signed is a difference to 3 decimals with its sign, "n/a" for none; a
// difference that rounds to zero is +0.000.
func signed(d *float64) string {
	if d == nil {
		return "n/a"
	}
	v := math.Round(*d*1000) / 1000
	if v == 0 {
		v = 0 // no -0.000
	}
	return fmt.Sprintf("%+.3f", v)
}

// compareVariants is cur's change since base: its groups' deltas, matched
// by type and name, current groups first and then the baseline's alone,
// and the queries, matched by id or by index, whose nDCG@10 moved by
// moveThreshold or more, largest fall first, ties in file order.
func compareVariants(cur, base variantReport, byID bool) variantComparison {
	var c variantComparison
	for _, g := range cur.Groups {
		label := groupLabel(g.Type, g.Name)
		j := slices.IndexFunc(base.Groups, func(o groupReport) bool { return o.Type == g.Type && o.Name == g.Name })
		if j < 0 {
			c.groups = append(c.groups, groupDelta{label: label, only: "this run"})
			continue
		}
		d := groupDelta{label: label}
		for _, m := range groupMetrics {
			d.deltas = append(d.deltas, difference(m.of(g.GroupScore), m.of(base.Groups[j].GroupScore)))
		}
		c.groups = append(c.groups, d)
	}
	for _, g := range base.Groups {
		if !slices.ContainsFunc(cur.Groups, func(o groupReport) bool { return o.Type == g.Type && o.Name == g.Name }) {
			c.groups = append(c.groups, groupDelta{label: groupLabel(g.Type, g.Name), only: "the baseline"})
		}
	}
	for _, q := range cur.Queries {
		old, ok := matchingQuery(q, base.Queries, byID)
		if !ok || math.Abs(q.NDCGAt10-old.NDCGAt10) < moveThreshold-1e-9 {
			continue
		}
		c.moved = append(c.moved, movedQuery{label: queryLabel(q.ID, q.Index), old: old.NDCGAt10, new: q.NDCGAt10})
	}
	slices.SortStableFunc(c.moved, func(a, b movedQuery) int { return cmp.Compare(a.new-a.old, b.new-b.old) })
	return c
}

// matchingQuery is q's query in the baseline: the one with its id, or at
// its index.
func matchingQuery(q queryReport, base []queryReport, byID bool) (queryReport, bool) {
	if byID {
		i := slices.IndexFunc(base, func(o queryReport) bool { return o.ID == q.ID })
		if i < 0 {
			return queryReport{}, false
		}
		return base[i], true
	}
	if q.Index >= len(base) {
		return queryReport{}, false
	}
	return base[q.Index], true
}

// difference is cur - base, nil when either is.
func difference(cur, base *float64) *float64 {
	if cur == nil || base == nil {
		return nil
	}
	d := *cur - *base
	return &d
}
