package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/insight/quality"
	"github.com/samsar/curio/internal/store"
)

// report is every number the report holds. A value that doesn't apply (a
// level a shape lacks, a share of no groups) is nil, so the JSON says null,
// never NaN.
type report struct {
	Database         string         `json:"database"`
	Documents        int            `json:"documents"`
	DroppedNonFinite int            `json:"dropped_non_finite"`
	Grouper          string         `json:"grouper"`
	Params           map[string]any `json:"params"`
	Draws            uint           `json:"draws"`
	Seed             uint64         `json:"seed"`
	Fresh            freshReport    `json:"fresh"`
	Baseline         baselineReport `json:"baseline"`
	Warm             warmReport     `json:"warm"`
	FreshRebuild     keptReport     `json:"fresh_rebuild"`
	Chain            chainReport    `json:"chain"`
	StoredRun        *storedReport  `json:"stored_run"`
	TimingsMS        timings        `json:"timings_ms"`
}

// level measures one level of a grouping. Cohesion is the mean over its
// groups; it and the silhouette are nil without the groups they need.
type level struct {
	Groups     int           `json:"groups"`
	Coverage   float64       `json:"coverage"`
	Sizes      quality.Sizes `json:"sizes"`
	Cohesion   *float64      `json:"cohesion"`
	Silhouette *float64      `json:"silhouette"`
}

// freshReport is a fresh grouping of the whole library.
type freshReport struct {
	Shape                insight.Shape  `json:"shape"`
	Areas                *level         `json:"areas"`
	Interests            level          `json:"interests"`
	InterestsBeforeMerge int            `json:"interests_before_merge"`
	Merged               int            `json:"merged"`
	Loose                int            `json:"loose"`
	Unsorted             int            `json:"unsorted"`
	NearDuplicates       nearDuplicates `json:"near_duplicates"`
}

// nearDuplicates counts pairs of interests whose centroids reach a
// threshold; WithinAnArea is nil in the flat shape.
type nearDuplicates struct {
	Threshold        float64 `json:"threshold"`
	WithinAnArea     *int    `json:"within_an_area"`
	Anywhere         int     `json:"anywhere"`
	LenientThreshold float64 `json:"lenient_threshold"`
	AnywhereLenient  int     `json:"anywhere_lenient"`
}

// baselineReport is the baseline clusterer's grouping.
type baselineReport struct {
	Clusterer string         `json:"clusterer"`
	Params    map[string]any `json:"params"`
	Interests level          `json:"interests"`
}

// keptDraw is the names one draw's rebuild kept.
type keptDraw struct {
	Draw uint   `json:"draw"`
	Seed uint64 `json:"seed"`
	kept
}

// keptReport is the names kept over the draws.
type keptReport struct {
	Draws []keptDraw `json:"draws"`
	Mean  kept       `json:"mean"`
	Min   kept       `json:"min"`
}

// warmReport is the warm rebuilds after each kind of 5% change.
type warmReport struct {
	Added keptReport `json:"added"`
	Mixed keptReport `json:"mixed"`
}

// chainStep is one step of a chain draw against a fresh grouping of the
// same library; Kept is nil at step 0.
type chainStep struct {
	Draw           uint     `json:"draw"`
	Step           int      `json:"step"`
	LibraryPercent int      `json:"library_percent"`
	Documents      int      `json:"documents"`
	SplitCheck     bool     `json:"split_check"`
	Areas          int      `json:"areas"`
	Interests      int      `json:"interests"`
	FreshAreas     int      `json:"fresh_areas"`
	FreshInterests int      `json:"fresh_interests"`
	Cohesion       *float64 `json:"cohesion"`
	FreshCohesion  *float64 `json:"fresh_cohesion"`
	Gap            *float64 `json:"gap"`
	Kept           kept     `json:"kept"`
}

// chainEnd is a chain draw's last step.
type chainEnd struct {
	Draw      uint     `json:"draw"`
	Areas     int      `json:"areas"`
	Interests int      `json:"interests"`
	Cohesion  *float64 `json:"cohesion"`
	Gap       *float64 `json:"gap"`
}

// worstGap is the step whose cohesion is furthest from a fresh grouping's.
type worstGap struct {
	Draw uint    `json:"draw"`
	Step int     `json:"step"`
	Gap  float64 `json:"gap"`
}

// keptStats is the mean and minimum names kept over some steps.
type keptStats struct {
	Mean kept `json:"mean"`
	Min  kept `json:"min"`
}

// chainReport is the chain draws and their summary.
type chainReport struct {
	Steps []chainStep `json:"steps"`
	// Fresh is the fresh grouping of the whole library the chains end
	// against.
	Fresh struct {
		Areas     int      `json:"areas"`
		Interests int      `json:"interests"`
		Cohesion  *float64 `json:"cohesion"`
	} `json:"fresh"`
	End                     []chainEnd `json:"end"`
	MeanEndAreas            float64    `json:"mean_end_areas"`
	MeanEndInterests        float64    `json:"mean_end_interests"`
	EndInterestsDiffPercent *float64   `json:"end_interests_diff_percent"`
	MeanEndCohesion         *float64   `json:"mean_end_cohesion"`
	MeanEndGap              *float64   `json:"mean_end_gap"`
	WorstGap                *worstGap  `json:"worst_gap"`
	SplitSteps              keptStats  `json:"split_check_steps"`
	OtherSteps              keptStats  `json:"other_steps"`
}

// storedReport is the copy's latest done run.
type storedReport struct {
	RunID      string              `json:"run_id"`
	Trigger    store.RunTrigger    `json:"trigger"`
	Kind       store.RunKind       `json:"kind"`
	SplitCheck bool                `json:"split_check"`
	Shape      store.InterestShape `json:"shape"`
	FinishedAt *time.Time          `json:"finished_at"`
	// SameParams: the run's grouper and params are the report's, so a
	// fresh run of the same documents groups as the report does.
	SameParams   bool            `json:"same_params"`
	Counts       runCounts       `json:"counts"`
	LabelSources map[string]int  `json:"label_sources"`
	Labels       []labelRow      `json:"labels"`
	Duplicates   labelDuplicates `json:"label_duplicates"`
	Agreement    agreement       `json:"agreement"`
}

// runCounts are a run row's counts.
type runCounts struct {
	Documents int `json:"documents"`
	Areas     int `json:"areas"`
	Interests int `json:"interests"`
	Loose     int `json:"loose"`
	Unsorted  int `json:"unsorted"`
	Changed   int `json:"changed"`
	Kept      int `json:"kept"`
	Created   int `json:"created"`
	Split     int `json:"split"`
	Merged    int `json:"merged"`
	Moved     int `json:"moved"`
	Dissolved int `json:"dissolved"`
}

// labelRow is an area with its interests, or an interest.
type labelRow struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	LabelSource store.LabelSource `json:"label_source"`
	Size        int               `json:"size"`
	Interests   []labelRow        `json:"interests,omitempty"`
}

// labelDuplicates are the labels that repeat: per scope labels must be
// unique in (the scopes with a pair), summed, and among all interests.
type labelDuplicates struct {
	Scopes       []scopeDuplicates `json:"scopes"`
	WithinScopes struct {
		Exact int `json:"exact_pairs"`
		Near  int `json:"near_pairs"`
	} `json:"within_scopes"`
	AllInterests quality.LabelDuplicates `json:"all_interests"`
}

// scopeDuplicates are one scope's repeated labels; Scope is its area's
// label, "areas", or "interests" for a flat run's.
type scopeDuplicates struct {
	Scope string `json:"scope"`
	quality.LabelDuplicates
}

// agreement compares the stored run with the report's fresh grouping.
type agreement struct {
	Shared       int      `json:"shared"`
	OnlyStored   int      `json:"only_stored"`
	OnlyFresh    int      `json:"only_fresh"`
	AreasARI     *float64 `json:"areas_ari"`
	InterestsARI *float64 `json:"interests_ari"`
	SameFits     int      `json:"same_fits"`
	// Identical: the same documents, shape, partitions and fits.
	Identical bool `json:"identical"`
}

// timings are how long each part of the report took.
type timings struct {
	Read         int64 `json:"read"`
	Fresh        int64 `json:"fresh"`
	Baseline     int64 `json:"baseline"`
	Warm         int64 `json:"warm"`
	FreshRebuild int64 `json:"fresh_rebuild"`
	Chain        int64 `json:"chain"`
	StoredRun    int64 `json:"stored_run"`
	Total        int64 `json:"total"`
}

// writeJSON writes rep to path atomically: a temporary file beside it,
// renamed over it once written. os.CreateTemp makes it 0600.
func writeJSON(path string, rep *report) error {
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the JSON report: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("write the JSON report: %w", err)
	}
	_, werr := f.Write(append(raw, '\n'))
	if err := errors.Join(werr, f.Close()); err != nil {
		return errors.Join(fmt.Errorf("write the JSON report: %w", err), os.Remove(f.Name()))
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return errors.Join(fmt.Errorf("write the JSON report: %w", err), os.Remove(f.Name()))
	}
	return nil
}

// writeText renders the report and writes it to w in one write.
func (r *report) writeText(w io.Writer) error {
	var b bytes.Buffer
	r.text(&b)
	_, err := w.Write(b.Bytes())
	return err
}

func (r *report) text(b *bytes.Buffer) {
	fmt.Fprintf(b, "Cluster report of %s\n", r.Database)
	fmt.Fprintf(b, "%d documents with a usable vector (%d dropped: NaN or infinite), grouper %s, %d draws, seed %d\n",
		r.Documents, r.DroppedNonFinite, r.Grouper, r.Draws, r.Seed)

	f := r.Fresh
	fmt.Fprintf(b, "\nFresh grouping of the whole library: %s shape\n", f.Shape)
	levelHeader(b)
	if f.Areas != nil {
		levelLine(b, "areas", *f.Areas)
	}
	levelLine(b, "interests", f.Interests)
	fmt.Fprintf(b, "  %d interests before the merge (%d merged), %d loose fits, %d unsorted\n",
		f.InterestsBeforeMerge, f.Merged, f.Loose, f.Unsorted)
	d := f.NearDuplicates
	within := "n/a"
	if d.WithinAnArea != nil {
		within = strconv.Itoa(*d.WithinAnArea)
	}
	fmt.Fprintf(b, "  near-duplicate interest pairs: at ≥%.2f %s within an area and %d anywhere; at ≥%.2f %d anywhere\n",
		d.Threshold, within, d.Anywhere, d.LenientThreshold, d.AnywhereLenient)

	fmt.Fprintf(b, "\nBaseline: %s (%s), its clusters as stored, no merge, no strays\n",
		r.Baseline.Clusterer, paramsText(r.Baseline.Params))
	levelHeader(b)
	levelLine(b, "interests", r.Baseline.Interests)

	fmt.Fprintf(b, "\nWarm rebuild after a 5%% change, names kept (interests · areas)\n")
	keptBlock(b, "added", r.Warm.Added)
	keptBlock(b, "mixed", r.Warm.Mixed)
	fmt.Fprintf(b, "\nFresh rebuild of the whole library after 5%% added, names kept (interests · areas)\n")
	keptBlock(b, "added", r.FreshRebuild)

	r.Chain.text(b)
	if r.StoredRun == nil {
		fmt.Fprintf(b, "\nStored run: none (the copy has no done interests run)\n")
	} else {
		r.StoredRun.text(b)
	}

	t := r.TimingsMS
	fmt.Fprintf(b, "\nTimings: read %s, fresh %s, baseline %s, warm %s, fresh rebuild %s, chain %s, stored run %s; total %s\n",
		seconds(t.Read), seconds(t.Fresh), seconds(t.Baseline), seconds(t.Warm), seconds(t.FreshRebuild),
		seconds(t.Chain), seconds(t.StoredRun), seconds(t.Total))
}

func levelHeader(b *bytes.Buffer) {
	fmt.Fprintf(b, "  %-10s %6s %9s %7s %7s %5s %6s %9s %10s\n",
		"level", "groups", "coverage", "median", "p90", "max", "small", "cohesion", "silhouette")
}

func levelLine(b *bytes.Buffer, name string, l level) {
	fmt.Fprintf(b, "  %-10s %6d %9s %7.1f %7.1f %5d %6s %9s %10s\n", name, l.Groups, percent(l.Coverage),
		l.Sizes.Median, l.Sizes.P90, l.Sizes.Max, percent(l.Sizes.SmallShare), fmtFloat(l.Cohesion, "%.4f"),
		fmtFloat(l.Silhouette, "%.4f"))
}

func keptBlock(b *bytes.Buffer, kind string, k keptReport) {
	for i, d := range k.Draws {
		name := ""
		if i == 0 {
			name = kind
		}
		keptLine(b, name, fmt.Sprintf("draw %d (seed %d)", d.Draw, d.Seed), d.kept)
	}
	keptLine(b, "", "mean", k.Mean)
	keptLine(b, "", "min", k.Min)
}

func keptLine(b *bytes.Buffer, kind, what string, k kept) {
	fmt.Fprintf(b, "  %-6s %-20s %s\n", kind, what, keptText(k))
}

func (c *chainReport) text(b *bytes.Buffer) {
	fmt.Fprintf(b, "\nChain: 60%% to 100%% of the library in 5%% steps, warm, the split check every %dth\n", splitEvery)
	for i, s := range c.Steps {
		if i == 0 || s.Draw != c.Steps[i-1].Draw {
			fmt.Fprintf(b, "  draw %d\n", s.Draw)
			fmt.Fprintf(b, "    %4s %7s %6s %5s %5s %9s %13s %8s %8s %8s  %s\n", "step", "library", "docs", "split",
				"areas", "interests", "fresh", "cohesion", "fresh", "gap", "kept (interests · areas)")
		}
		split := ""
		if s.SplitCheck {
			split = "yes"
		}
		keptCol := "n/a"
		if s.Step > 0 {
			keptCol = keptText(s.Kept)
		}
		fmt.Fprintf(b, "    %4d %6d%% %6d %5s %5d %9d %13s %8s %8s %8s  %s\n", s.Step, s.LibraryPercent, s.Documents,
			split, s.Areas, s.Interests, fmt.Sprintf("%d/%d", s.FreshAreas, s.FreshInterests),
			fmtFloat(s.Cohesion, "%.4f"), fmtFloat(s.FreshCohesion, "%.4f"), fmtFloat(s.Gap, "%+.4f"), keptCol)
	}
	interests, areas, cohesions := make([]string, 0, len(c.End)), make([]string, 0, len(c.End)), make([]string, 0, len(c.End))
	for _, e := range c.End {
		interests = append(interests, strconv.Itoa(e.Interests))
		areas = append(areas, strconv.Itoa(e.Areas))
		cohesions = append(cohesions, fmtFloat(e.Cohesion, "%.4f"))
	}
	fmt.Fprintf(b, "  end: %s interests (mean %.1f) against %d fresh (%s); %s areas (mean %.1f) against %d fresh\n",
		strings.Join(interests, ", "), c.MeanEndInterests, c.Fresh.Interests, fmtFloat(c.EndInterestsDiffPercent, "%+.1f%%"),
		strings.Join(areas, ", "), c.MeanEndAreas, c.Fresh.Areas)
	fmt.Fprintf(b, "  end cohesion: %s (mean %s) against %s fresh, gap %s\n", strings.Join(cohesions, ", "),
		fmtFloat(c.MeanEndCohesion, "%.4f"), fmtFloat(c.Fresh.Cohesion, "%.4f"), fmtFloat(c.MeanEndGap, "%+.4f"))
	if c.WorstGap != nil {
		fmt.Fprintf(b, "  worst gap: %+.4f (draw %d, step %d)\n", c.WorstGap.Gap, c.WorstGap.Draw, c.WorstGap.Step)
	}
	fmt.Fprintf(b, "  names kept at the split-check steps: mean %s, min %s\n", keptText(c.SplitSteps.Mean),
		keptText(c.SplitSteps.Min))
	fmt.Fprintf(b, "  names kept at the other steps: mean %s, min %s\n", keptText(c.OtherSteps.Mean),
		keptText(c.OtherSteps.Min))
}

func (s *storedReport) text(b *bytes.Buffer) {
	finished := "n/a"
	if s.FinishedAt != nil {
		finished = s.FinishedAt.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(b, "\nStored run %s: trigger %s, %s, %s shape, split check %t, finished %s\n",
		s.RunID, s.Trigger, s.Kind, s.Shape, s.SplitCheck, finished)
	c := s.Counts
	fmt.Fprintf(b, "  %d documents, %d areas, %d interests, %d loose fits, %d unsorted; %d changed\n",
		c.Documents, c.Areas, c.Interests, c.Loose, c.Unsorted, c.Changed)
	fmt.Fprintf(b, "  interests kept %d, created %d, split %d, merged %d, moved %d, dissolved %d\n",
		c.Kept, c.Created, c.Split, c.Merged, c.Moved, c.Dissolved)
	params := "differ from"
	if s.SameParams {
		params = "are"
	}
	fmt.Fprintf(b, "  its grouper and params %s this report's\n", params)
	sources := make([]string, 0, len(s.LabelSources))
	for _, k := range slices.Sorted(maps.Keys(s.LabelSources)) {
		sources = append(sources, fmt.Sprintf("%s %d", k, s.LabelSources[k]))
	}
	fmt.Fprintf(b, "  label sources: %s\n", strings.Join(sources, ", "))

	a := s.Agreement
	fmt.Fprintf(b, "  against the fresh grouping above, over the %d documents both hold (%d only in the run, %d only in the grouping):\n",
		a.Shared, a.OnlyStored, a.OnlyFresh)
	fmt.Fprintf(b, "    ARI %s (interests), %s (areas); %d with the same fit; identical: %t\n",
		fmtFloat(a.InterestsARI, "%.4f"), fmtFloat(a.AreasARI, "%.4f"), a.SameFits, a.Identical)

	d := s.Duplicates
	fmt.Fprintf(b, "  label duplicates within scopes: %d exact, %d near; among all interests: %d exact, %d near\n",
		d.WithinScopes.Exact, d.WithinScopes.Near, d.AllInterests.Exact, d.AllInterests.Near)
	for _, sc := range d.Scopes {
		for _, p := range sc.Pairs {
			fmt.Fprintf(b, "    in %q: %q · %q\n", sc.Scope, p[0], p[1])
		}
	}

	fmt.Fprintf(b, "  labels:\n")
	for _, row := range s.Labels {
		fmt.Fprintf(b, "    %s (%d, %s)\n", row.Label, row.Size, row.LabelSource)
		for _, in := range row.Interests {
			fmt.Fprintf(b, "      %s (%d, %s)\n", in.Label, in.Size, in.LabelSource)
		}
	}
}

// paramsText is params as "key value" pairs by key.
func paramsText(params map[string]any) string {
	out := make([]string, 0, len(params))
	for _, k := range slices.Sorted(maps.Keys(params)) {
		out = append(out, fmt.Sprintf("%s %v", k, params[k]))
	}
	return strings.Join(out, ", ")
}

func keptText(k kept) string { return fmtShare(k.Interests) + " · " + fmtShare(k.Areas) }

func fmtShare(v *float64) string { return fmtFloat(scale(v, 100), "%.1f%%") }

func scale(v *float64, by float64) *float64 {
	if v == nil {
		return nil
	}
	return new(*v * by)
}

func percent(v float64) string { return fmt.Sprintf("%.1f%%", 100*v) }

// fmtFloat formats v, or says n/a for nil.
func fmtFloat(v *float64, format string) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf(format, *v)
}

func seconds(ms int64) string { return fmt.Sprintf("%.1f s", float64(ms)/1000) }
