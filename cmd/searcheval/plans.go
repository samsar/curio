package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/eval"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/understand"
)

// queryPlan is a query's plan as the planned variants search by it: the
// planner's outcome, or a replayed report's, and how long the planner took
// to give it (0 when replayed).
type queryPlan struct {
	status  understand.Status
	plan    *understand.Plan
	dropped []understand.Dropped
	took    time.Duration
}

// plannerReport is the planner the planned variants were planned by: its
// model, the prompt's version, the day it was told is today, how long
// warming it up took, and what its fitness check found: passed,
// failed:<probe>, or error:<why it couldn't be asked>. The variants run
// whatever the check says: the tool measures the planner. With the weights
// its plans are searched at.
type plannerReport struct {
	Model          string  `json:"model"`
	PromptVersion  string  `json:"prompt_version"`
	Today          string  `json:"today"`
	WarmMS         float64 `json:"warm_ms"`
	Check          string  `json:"check"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	RewriteWeight  float64 `json:"rewrite_weight"`
	NameWeight     float64 `json:"name_weight"`
}

// planJSON is a plan as the report holds it, its filters in the API's
// form: what a replay reads back.
type planJSON struct {
	Kind     string      `json:"kind"`
	Filters  filtersJSON `json:"filters"`
	Rewrites []string    `json:"rewrites"`
	Names    []string    `json:"names"`
}

type filtersJSON struct {
	ContentType     []string `json:"content_type,omitempty"`
	Host            []string `json:"host,omitempty"`
	Source          []string `json:"source,omitempty"`
	Site            []string `json:"site,omitempty"`
	Folder          []string `json:"folder,omitempty"`
	SavedAfter      string   `json:"saved_after,omitempty"`
	SavedBefore     string   `json:"saved_before,omitempty"`
	PublishedAfter  string   `json:"published_after,omitempty"`
	PublishedBefore string   `json:"published_before,omitempty"`
	IncludeUndated  bool     `json:"include_undated,omitempty"`
}

type droppedJSON struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func toPlanJSON(p understand.Plan) *planJSON {
	f := p.Filters
	return &planJSON{Kind: string(p.Kind), Rewrites: nonNil(p.Rewrites), Names: nonNil(p.Names),
		Filters: filtersJSON{ContentType: f.ContentType, Host: f.Host, Source: f.Source, Site: f.Site,
			Folder: f.Folder, SavedAfter: f.SavedAfter, SavedBefore: f.SavedBefore, PublishedAfter: f.PublishedAfter,
			PublishedBefore: f.PublishedBefore, IncludeUndated: f.IncludeUndated}}
}

func (p planJSON) plan() understand.Plan {
	f := p.Filters
	return understand.Plan{Kind: understand.Kind(p.Kind), Rewrites: p.Rewrites, Names: p.Names,
		Filters: search.FilterRequest{ContentType: f.ContentType, Host: f.Host, Source: f.Source, Site: f.Site,
			Folder: f.Folder, SavedAfter: f.SavedAfter, SavedBefore: f.SavedBefore, PublishedAfter: f.PublishedAfter,
			PublishedBefore: f.PublishedBefore, IncludeUndated: f.IncludeUndated}}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// planned reports whether v searches by plans.
func (v variant) planned() bool { return v.mask != nil }

// anyPlanned reports whether any of vs searches by plans.
func anyPlanned(vs []variant) bool {
	for _, v := range vs {
		if v.planned() {
			return true
		}
	}
	return false
}

// replay is a previous report's plans, read for -plans, and the prompt
// version they were made with.
type replay struct {
	path          string
	promptVersion string
	plans         []queryPlan
}

// loadReplay reads the planned plans of the report at path, matched with
// the query file's queries as -baseline matches them: by index for the
// same file, by id when every query has one, refused otherwise. It reads
// the first planned variant's: every planned variant of a run searches by
// the same plans.
func loadReplay(path string, qf queryFile) (*replay, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the -plans file: %w", err)
	}
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("the plans %s are not a searcheval report: %w", path, err)
	}
	if rep.Format != reportFormat {
		return nil, fmt.Errorf("the plans %s are not a searcheval report of format %d (it has format %d)",
			path, reportFormat, rep.Format)
	}
	var source *variantReport
	for i := range rep.Variants {
		if v, ok := variantNamed(rep.Variants[i].Name); ok && v.planned() {
			source = &rep.Variants[i]
			break
		}
	}
	if source == nil || len(source.Queries) == 0 {
		return nil, fmt.Errorf("the report %s ran no planned variant: there are no plans in it to replay", path)
	}
	// matchQueries reads the first variant's queries; the planned one's are
	// the same queries.
	rep.Variants = []variantReport{*source}
	byID, err := matchQueries(&rep, qf)
	if err != nil {
		return nil, fmt.Errorf("the plans %s: %w", path, err)
	}
	out := &replay{path: path, plans: make([]queryPlan, len(qf.set.Queries))}
	if rep.Planner != nil {
		out.promptVersion = rep.Planner.PromptVersion
	}
	for i, q := range qf.set.Queries {
		old, ok := matchingQuery(queryReport{Index: i, ID: q.ID}, source.Queries, byID)
		if !ok {
			return nil, fmt.Errorf("the plans %s hold no plan of query %s", path, queryLabel(q.ID, i))
		}
		qp := queryPlan{status: understand.Status(old.PlanStatus)}
		if old.Plan != nil {
			p := old.Plan.plan()
			qp.plan = &p
		}
		for _, d := range old.PlanDropped {
			qp.dropped = append(qp.dropped, understand.Dropped{Field: d.Field, Reason: d.Reason})
		}
		out.plans[i] = qp
	}
	return out, nil
}

// plannerSetup is what planning the queries needs besides them: the
// config, the day the planner is told is today, and the log.
type plannerSetup struct {
	cfg   config.Config
	today time.Time
	log   *slog.Logger
}

// planQueries plans every query once with the planner config.yaml
// configures, whatever search.understand says: it pings the model (never
// pulling one), warms it, runs the fitness check, then asks for each
// query's plan, in file order, as a search the daemon understands would,
// once the model is loaded and fit. It reports the planner and its check;
// a failed check is reported, not an error.
func planQueries(ctx context.Context, s plannerSetup, qs *eval.QuerySet) ([]queryPlan, *plannerReport, error) {
	opts := understand.OptionsFrom(s.cfg)
	opts.On, opts.Now, opts.Location, opts.Log = true, func() time.Time { return s.today }, time.Local, s.log
	gen, err := generator.New(opts.Model)
	if err != nil {
		return nil, nil, fmt.Errorf("build the planner: %w", err)
	}
	warmer, ok := gen.(generator.Warmer)
	if _, structured := gen.(generator.JSONGenerator); !structured || !ok {
		return nil, nil, fmt.Errorf("the planner's provider %s can't answer in JSON or load its model", opts.Model.Provider)
	}
	if err := pingPlanner(ctx, gen, opts.Model); err != nil {
		return nil, nil, err
	}
	rep := &plannerReport{Model: opts.Model.Model, PromptVersion: understand.PromptVersion,
		Today: s.today.Format(time.DateOnly), TimeoutSeconds: s.cfg.Search.UnderstandTimeoutSeconds,
		RewriteWeight: s.cfg.Search.RewriteWeight, NameWeight: s.cfg.Search.NameWeight}
	// The planner's background work runs on this context, and none starts:
	// Check and Ask run on the caller's.
	lifetime, stop := context.WithCancel(ctx)
	planner := understand.New(lifetime, gen, opts)
	defer func() {
		stop()
		planner.Wait()
	}()
	s.log.Info("loading the planner's model", "model", opts.Model.Model)
	start := time.Now()
	// Each plan keeps the model loaded as long again, as the daemon's do.
	if err := warmer.Warm(ctx, planner.KeepAlive()); err != nil {
		return nil, nil, fmt.Errorf("load the planner's model %s: %w", opts.Model.Model, err)
	}
	rep.WarmMS = ms(time.Since(start))

	failed, err := planner.Check(ctx)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, nil, err
	case err != nil:
		rep.Check = "error:" + err.Error()
	case failed != "":
		rep.Check = "failed:" + failed
	default:
		rep.Check = "passed"
	}
	s.log.Info("the planner's check", "model", opts.Model.Model, "check", rep.Check)

	plans := make([]queryPlan, len(qs.Queries))
	for i, q := range qs.Queries {
		out, err := planner.Ask(ctx, q.Query)
		if err != nil {
			return nil, nil, fmt.Errorf("plan query %s: %w", queryLabel(q.ID, i), err)
		}
		plans[i] = queryPlan{status: out.Status, plan: out.Plan, dropped: out.Dropped, took: out.Took}
	}
	return plans, rep, nil
}

// pingPlanner checks that Ollama has the planner's model; it never pulls
// one.
func pingPlanner(ctx context.Context, gen generator.Generator, spec config.ModelSpec) error {
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	switch err := gen.Ping(pctx); {
	case errors.Is(err, ollama.ErrModelNotLoaded):
		return fmt.Errorf("the Ollama at %s doesn't have the planner's model %s, and searcheval never pulls a "+
			"model: %w", spec.BaseURL, spec.Model, err)
	case err != nil:
		return fmt.Errorf("ask the Ollama at %s for the planner's model %s: %w", spec.BaseURL, spec.Model, err)
	}
	return nil
}

// parseToday reads -today, a day in the local zone, as its noon: the
// planner turns it into the day's date. Empty is today.
func parseToday(s string, now time.Time) (time.Time, error) {
	if s == "" {
		y, m, d := now.Date()
		return time.Date(y, m, d, 12, 0, 0, 0, time.Local), nil
	}
	day, err := time.ParseInLocation(time.DateOnly, s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("-today %q is not a day YYYY-MM-DD", s)
	}
	return day.Add(12 * time.Hour), nil
}

// planStatuses counts the queries of a planned variant by plan status.
func planStatuses(queries []queryReport) map[string]int {
	out := map[string]int{}
	for _, q := range queries {
		if q.PlanStatus != "" {
			out[q.PlanStatus]++
		}
	}
	return out
}

// agreementTotals counts a planned variant's filter verdicts over its
// queries, every dimension compared.
func agreementTotals(queries []queryReport) map[verdict]int {
	out := map[verdict]int{}
	for _, q := range queries {
		for _, v := range q.FilterAgreement {
			out[v]++
		}
	}
	return out
}

// verdictList names, for v, each query and dimension it was found for:
// "q-week (saved)".
func verdictList(queries []queryReport, v verdict) []string {
	var out []string
	for _, q := range queries {
		var dims []string
		for _, dim := range agreementDims {
			if q.FilterAgreement[dim] == v {
				dims = append(dims, dim)
			}
		}
		if len(dims) > 0 {
			out = append(out, queryLabel(q.ID, q.Index)+" ("+strings.Join(dims, ", ")+")")
		}
	}
	return out
}
