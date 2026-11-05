package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// rerankWith is -rerank-with: the reranker's settings a grid point
// changes from the daemon's, each empty or zero when it keeps them.
type rerankWith struct {
	Method   rerank.Method   `json:"method,omitempty"`
	Model    string          `json:"model,omitempty"`
	Depth    int             `json:"depth,omitempty"`
	Blend    rerank.Blend    `json:"blend,omitempty"`
	Input    rerank.Input    `json:"input,omitempty"`
	Textless rerank.Textless `json:"textless,omitempty"`
	Folders  *bool           `json:"folders,omitempty"`
	Claims   *bool           `json:"claims,omitempty"`
	Timeout  time.Duration   `json:"timeout,omitempty"`
}

// parseRerankWith reads -rerank-with, key=value pairs joined by commas,
// strictly: an unknown key, a value outside its set or a key given twice
// is an error.
func parseRerankWith(s string) (rerankWith, error) {
	var w rerankWith
	if s == "" {
		return w, nil
	}
	seen := map[string]bool{}
	for pair := range strings.SplitSeq(s, ",") {
		key, value, ok := strings.Cut(pair, "=")
		switch {
		case !ok || value == "":
			return rerankWith{}, fmt.Errorf("-rerank-with: %q is not key=value", pair)
		case seen[key]:
			return rerankWith{}, fmt.Errorf("-rerank-with: %s given twice", key)
		}
		seen[key] = true
		if err := w.set(key, value); err != nil {
			return rerankWith{}, fmt.Errorf("-rerank-with %s: %w", key, err)
		}
	}
	return w, nil
}

// set sets key to value.
func (w *rerankWith) set(key, value string) error {
	oneOf := func(v string, set ...string) error {
		if !slices.Contains(set, v) {
			return fmt.Errorf("%q is not one of %s", v, strings.Join(set, ", "))
		}
		return nil
	}
	switch key {
	case "method":
		w.Method = rerank.Method(value)
		return oneOf(value, string(rerank.MethodListwise), string(rerank.MethodPointwise))
	case "model":
		w.Model = value
	case "depth":
		n, err := strconv.Atoi(value)
		if err != nil || n < 2 || n > rerank.MaxDepth {
			return fmt.Errorf("%q is not a depth in [2, %d]", value, rerank.MaxDepth)
		}
		w.Depth = n
	case "blend":
		w.Blend = rerank.Blend(value)
		return oneOf(value, string(rerank.BlendPure), string(rerank.BlendPosition), string(rerank.BlendRRF))
	case "input":
		w.Input = rerank.Input(value)
		return oneOf(value, string(rerank.InputSummary), string(rerank.InputPassage), string(rerank.InputBoth))
	case "textless":
		w.Textless = rerank.Textless(value)
		return oneOf(value, string(rerank.TextlessRead), string(rerank.TextlessHeld))
	case "folders", "claims":
		on, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%q is not true or false", value)
		}
		if key == "folders" {
			w.Folders = &on
		} else {
			w.Claims = &on
		}
	case "timeout":
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("%q is not a positive duration", value)
		}
		w.Timeout = d
	default:
		return errors.New("unknown key (the keys are method, model, depth, blend, input, textless, folders, " +
			"claims, timeout)")
	}
	return nil
}

// rerankOptions are the reranker's options for the run: the daemon's
// (rerank.OptionsFrom) for config.yaml's reranker role, on whatever
// search.rerank says, and whatever search.understand says, since the
// tool plans every query itself, with w's changes laid over them.
func rerankOptions(cfg config.Config, w rerankWith) rerank.Options {
	if w.Model != "" {
		roles := make(map[config.Role]config.ModelSpec, len(cfg.Generation.Roles)+1)
		maps.Copy(roles, cfg.Generation.Roles)
		spec := roles[config.RoleReranker]
		spec.Model = w.Model
		roles[config.RoleReranker] = spec
		cfg.Generation.Roles = roles
	}
	o := rerank.OptionsFrom(cfg)
	o.On, o.OffReason, o.Unplanned = true, "", ""
	s := &o.Settings
	s.Force = w.Method
	if w.Depth > 0 {
		s.Depth = w.Depth
	}
	if w.Blend != "" {
		s.Blend = w.Blend
	}
	if w.Input != "" {
		s.Input = w.Input
	}
	if w.Textless != "" {
		s.Textless = w.Textless
	}
	if w.Folders != nil {
		s.Folders = *w.Folders
	}
	if w.Claims != nil {
		s.Claims = *w.Claims
	}
	if w.Timeout > 0 {
		o.Timeout = w.Timeout
	}
	return o
}

// rerankReport is the reranker a reranked variant ran: its model and
// family, the routes or the method forced, its settings, the prompts'
// version, its fitness check (passed, failed:<probe>, error:<why>, or
// replayed when no model was asked) and its calls' recorded time, and how
// long warming it took; and whether Ollama still held it, and the
// embedder, once the variant ran.
type rerankReport struct {
	Model         string            `json:"model"`
	Family        rerank.Family     `json:"family"`
	Routes        map[string]string `json:"routes"`
	Depth         int               `json:"depth"`
	Blend         rerank.Blend      `json:"blend"`
	Input         rerank.Input      `json:"input"`
	Textless      rerank.Textless   `json:"textless"`
	Folders       bool              `json:"folders"`
	Claims        bool              `json:"claims"`
	TimeoutMS     float64           `json:"timeout_ms"`
	PromptVersion string            `json:"prompt_version"`
	Check         string            `json:"check"`
	CheckMS       float64           `json:"check_ms"`
	WarmMS        float64           `json:"warm_ms"`
	With          rerankWith        `json:"with"`
	Answers       answerCounts      `json:"answers"`
	ResidentAfter []string          `json:"resident_after,omitempty"`
	Evicted       []string          `json:"evicted,omitempty"`
}

// answerCounts are how many of the model's answers the variants asked it
// for, and how many were replayed.
type answerCounts struct {
	Asked    int `json:"asked"`
	Replayed int `json:"replayed"`
}

// rerankRun is the run's reranker, over its answer book, and what it read
// of Ollama before it.
type rerankRun struct {
	reranker *rerank.Reranker
	book     *answerBook
	opts     rerank.Options
	report   rerankReport
	// residentBefore are the models Ollama held before the run asked it
	// anything; loadedBefore whether the reranker's was one.
	residentBefore []string
	loadedBefore   bool
	ps             *ollama.Client // nil when no model is asked
	readers        rerank.Readers
	stop           context.CancelFunc
}

// rerankerSetup is what building the run's reranker needs.
type rerankerSetup struct {
	cfg              config.Config
	with             rerankWith
	replay, recordTo string
	readers          rerank.Readers
	log              *slog.Logger
}

// startReranker builds the run's reranker from config.yaml's reranker
// role, whatever search.rerank says, its model overridable: with a model
// to ask (no -reranks, or a -reranks-out to record misses to) it pings it
// (never pulling), reads what Ollama holds, warms the model and runs the
// fitness check, which is reported, never a gate.
func startReranker(ctx context.Context, s rerankerSetup) (*rerankRun, error) {
	opts := rerankOptions(s.cfg, s.with)
	opts.Log = s.log
	if err := opts.Settings.Validate(); err != nil {
		return nil, fmt.Errorf("the reranker's settings: %w", err)
	}
	var gen generator.Generator
	live := s.replay == "" || s.recordTo != ""
	if live {
		g, err := generator.New(opts.Model)
		if err != nil {
			return nil, fmt.Errorf("build the reranker: %w", err)
		}
		if err := pingModel(ctx, g, opts.Model, "reranker's"); err != nil {
			return nil, err
		}
		gen = g
	}
	book, err := newAnswerBook(gen, opts.Model.Model, s.replay, s.recordTo)
	if err != nil {
		return nil, err
	}
	lifetime, stop := context.WithCancel(ctx)
	run := &rerankRun{reranker: rerank.New(lifetime, book, opts), book: book, opts: opts, readers: s.readers,
		stop: stop}
	run.report = newRerankReport(opts, s.with)
	if !live {
		run.report.Check = "replayed"
		return run, nil
	}
	if run.ps, err = ollama.New(opts.Model.BaseURL, opts.Model.Model, pingTimeout); err != nil {
		return nil, errors.Join(err, run.close(ctx))
	}
	if err := run.readResident(ctx); err != nil {
		return nil, errors.Join(err, run.close(ctx))
	}
	start := time.Now()
	// Each rerank keeps the model loaded as long again, as the daemon's do.
	if err := book.Warm(ctx, opts.KeepAlive); err != nil {
		return nil, errors.Join(fmt.Errorf("load the reranker's model %s: %w", opts.Model.Model, err), run.close(ctx))
	}
	run.report.WarmMS = ms(time.Since(start))
	switch failed, err := run.reranker.Check(ctx); {
	case err != nil && ctx.Err() != nil:
		return nil, errors.Join(err, run.close(ctx))
	case err != nil:
		run.report.Check = "error:" + err.Error()
	case failed != "":
		run.report.Check = "failed:" + failed
	default:
		run.report.Check = "passed"
	}
	// The check's calls are the run's, not the first reranked query's: the
	// book counts every call's time until it is taken.
	checked, _, _ := book.take()
	run.report.CheckMS = ms(checked)
	s.log.Info("the reranker's check", "model", opts.Model.Model, "check", run.report.Check,
		"took_ms", run.report.CheckMS)
	return run, nil
}

// newRerankReport is the report of a reranker over opts, w's changes laid
// over the daemon's.
func newRerankReport(opts rerank.Options, w rerankWith) rerankReport {
	s := opts.Settings
	routes := map[string]string{}
	for _, g := range []rerank.Group{rerank.GroupOne, rerank.GroupTopical, rerank.GroupNone, rerank.GroupKeywords} {
		routes[string(g)] = string(rerank.MethodSkip)
		if m, ok := s.Routes[g]; ok && g != rerank.GroupKeywords {
			routes[string(g)] = string(m)
		}
		if s.Force != "" {
			routes[string(g)] = string(s.Force)
		}
	}
	return rerankReport{Model: opts.Model.Model, Family: rerank.FamilyOf(opts.Model.Model), Routes: routes,
		Depth: s.Depth, Blend: s.Blend, Input: s.Input, Textless: s.Textless, Folders: s.Folders, Claims: s.Claims,
		TimeoutMS: ms(opts.Timeout), PromptVersion: rerank.PromptVersion, With: w}
}

// resident are the names of the models Ollama holds now.
func (r *rerankRun) resident(ctx context.Context) ([]string, error) {
	running, err := r.ps.Running(ctx)
	if err != nil {
		return nil, fmt.Errorf("ask Ollama what it holds: %w", err)
	}
	out := make([]string, len(running))
	for i, m := range running {
		out[i] = m.Name
	}
	return out, nil
}

// readResident reads what Ollama holds before the run asks it anything.
func (r *rerankRun) readResident(ctx context.Context) error {
	running, err := r.ps.Running(ctx)
	if err != nil {
		return fmt.Errorf("ask Ollama what it holds: %w", err)
	}
	for _, m := range running {
		r.residentBefore = append(r.residentBefore, m.Name)
		r.loadedBefore = r.loadedBefore || m.Is(r.opts.Model.Model)
	}
	return nil
}

// observe records in the report what Ollama holds after the reranked
// variants, and the models it held before that it doesn't now: evicted
// while the run asked the reranker.
func (r *rerankRun) observe(ctx context.Context, log *slog.Logger) {
	r.report.Answers = answerCounts{Asked: r.book.asked, Replayed: r.book.replayed}
	if r.ps == nil {
		return
	}
	after, err := r.resident(ctx)
	if err != nil {
		log.Warn("couldn't read what Ollama holds after the reranked variants", "err", err)
		return
	}
	r.report.ResidentAfter = after
	for _, m := range r.residentBefore {
		if !slices.Contains(after, m) {
			r.report.Evicted = append(r.report.Evicted, m)
		}
	}
}

// close stops the reranker, writes out the answers recorded, and unloads
// the reranker's model when the run loaded it.
func (r *rerankRun) close(ctx context.Context) error {
	r.stop()
	r.reranker.Wait()
	if r.ps != nil && !r.loadedBefore {
		unloadModel(ctx, r.book, r.opts.Model.Model, r.opts.Log)
	}
	return r.book.Close()
}

// rerankQuery is how a query's head was reranked: the outcome's status
// (timeout when the recorded call outlasted the reranker's timeout,
// timed_out_by_budget saying so), the order applied, the model's recorded
// time, and the pointwise candidates unscored and bounded.
type rerankQuery struct {
	Status           rerank.Status `json:"status"`
	Reason           string        `json:"reason,omitempty"`
	Method           rerank.Method `json:"method,omitempty"`
	Order            []string      `json:"order,omitempty"`
	TookMS           float64       `json:"took_ms"`
	Unscored         int           `json:"unscored,omitempty"`
	Bounded          int           `json:"bounded,omitempty"`
	TimedOutByBudget bool          `json:"timed_out_by_budget,omitempty"`
}

// rerankedRanker reranks its base ranking's head as the daemon reranks a
// search that asks: the first depth hits read as candidates
// (rerank.ReadCandidates), the reranker asked (Reranker.Ask, routed by the
// query's plan kind, or as a search without a plan), and the head
// reordered, the rest in first-stage order. Its rerank stage is the reads
// and the model's recorded time, or the timeout when that is longer, the
// first ranking kept then.
type rerankedRanker struct {
	base ranker
	run  *rerankRun
}

func (r rerankedRanker) rank(ctx context.Context, i int, query string, filters store.SearchFilters) (ranking,
	error) {
	out, err := r.base.rank(ctx, i, query, filters)
	if err != nil {
		return ranking{}, err
	}
	var kind understand.Kind
	planned := out.plan != nil && out.plan.plan != nil
	if planned {
		kind = out.plan.plan.Kind
	}
	out.base = out.docs
	depth, early := r.run.reranker.Decide(query, kind, planned)
	if depth == 0 {
		out.rerank = &rerankQuery{Status: early.Status, Reason: early.Reason}
		out.stages = append(out.stages, stage{"rerank", 0})
		return out, nil
	}
	began := time.Now()
	cands, err := rerank.ReadCandidates(ctx, r.run.readers, store.LocalTenantID, out.hits[:min(depth, len(out.hits))])
	if err != nil {
		return ranking{}, err
	}
	reads := time.Since(began)
	res, err := r.run.reranker.Ask(ctx, rerank.Request{Question: query, Kind: kind, Planned: planned,
		Candidates: cands})
	recorded, measured, missed := r.run.book.take()
	switch {
	case err != nil:
		return ranking{}, err
	case missed:
		return ranking{}, errNotRecorded
	}
	rq := &rerankQuery{Status: res.Status, Reason: res.Reason, Method: res.Method, TookMS: ms(recorded),
		Unscored: res.Unscored, Bounded: res.Bounded}
	counted := recorded
	if timeout := r.run.opts.Timeout; recorded > timeout {
		rq.Status, rq.Reason, rq.TimedOutByBudget = rerank.StatusTimeout, "", true
		counted = timeout
	}
	if rq.Status == rerank.StatusReranked {
		rq.Order = res.Order
		out.docs = reorderDocs(out.docs, res.Order)
	}
	out.rerank = rq
	out.adjust += counted - measured
	out.stages = append(out.stages, stage{"rerank", reads + counted})
	return out, nil
}

// reorderDocs is docs laid out by order (rerank.Apply).
func reorderDocs(docs []rankedDoc, order []string) []rankedDoc {
	ids := make([]string, len(docs))
	byID := make(map[string]rankedDoc, len(docs))
	for i, d := range docs {
		ids[i] = d.id
		byID[d.id] = d
	}
	applied, _ := rerank.Apply(ids, order)
	out := make([]rankedDoc, len(applied))
	for i, id := range applied {
		out[i] = byID[id]
	}
	return out
}
