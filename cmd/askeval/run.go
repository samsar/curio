package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/devtool"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/modelgate"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/retrieve"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
	"github.com/samsar/curio/internal/urlutil"
)

// planRecord is a plan as a book records it.
type planRecord struct {
	Kind     string               `json:"kind"`
	Filters  search.FilterRequest `json:"filters"`
	Rewrites []string             `json:"rewrites,omitempty"`
	Names    []string             `json:"names,omitempty"`
}

// warmUpQuestion is asked once, uncounted, before the questions: it loads
// the models and runs their checks, so every counted ask is warm.
const warmUpQuestion = "what did I save about getting started"

// opTimeout bounds asking Ollama what it holds, and unloading a model.
const opTimeout = 30 * time.Second

// served is a model at its server, the pair Ollama's holdings are judged
// by: one name may be loaded at one server and not another.
type served struct {
	baseURL, model string
}

// servedOf is model at baseURL, the server as its URL names it ("" is
// Ollama's default) and the model as Ollama matches its name.
func servedOf(baseURL, model string) served {
	return served{baseURL: serverOf(baseURL), model: ollama.NormalizeModel(model)}
}

// serverOf is the server baseURL names: "" is Ollama's default, and a
// trailing slash names the same one.
func serverOf(baseURL string) string {
	if baseURL == "" {
		baseURL = ollama.DefaultBaseURL
	}
	return strings.TrimRight(baseURL, "/")
}

// setup is what an evaluation asks through.
type setup struct {
	cfg     config.Config
	asker   *ask.Asker
	writer  *streamBook
	answers *book
	// judge judges each answer (nil judges nothing), and judge2, a second
	// judge asked the same prompt, is compared with it (nil for none).
	judge, judge2 *judge
	planner       *understand.Planner
	rerank        *rerank.Reranker
	// models are the models the run may use, at their servers; before is
	// what each of those servers held at the start; loaded are the writing
	// models the run may load, to unload at the end where they weren't
	// held before.
	models []served
	before map[string][]ollama.Running
	loaded []served
	stop   context.CancelFunc
	// recorded is the report the recorded variant replays its answer-route
	// questions from; nil without one.
	recorded *report
	waitFor  []func()
	// traffic is every role's calls of each model, shared as the
	// daemon's roles share theirs.
	traffic modelgate.Traffic
}

// live reports whether an ask of this run may ask a model: no book
// replays it, or one records what it lacks.
func (o options) live() bool { return o.answers == "" || o.answersOut != "" }

func evaluate(ctx context.Context, opts options, stdout io.Writer, log *slog.Logger) error {
	if opts.json != "" {
		if err := devtool.CheckOutput(opts.json); err != nil {
			return err
		}
	}
	cfg := config.Default()
	if opts.config != "" {
		var err error
		if cfg, err = config.Load(opts.config); err != nil {
			return err
		}
	}
	qf, err := readQuestions(opts.questions)
	if err != nil {
		return err
	}
	var recorded *report
	if opts.recorded != "" {
		if recorded, err = readRecorded(opts, qf); err != nil {
			return err
		}
	}
	lib, err := devtool.OpenCopy(ctx, opts.db)
	if err != nil {
		return err
	}
	defer func() {
		if err := lib.Close(); err != nil {
			log.Warn("close the database", "err", err)
		}
	}()
	s, err := build(ctx, opts, cfg, lib, log)
	if err != nil {
		return err
	}
	s.recorded = recorded
	defer s.close(log)

	rep := report{GeneratedAt: time.Now().UTC(), Questions: qf.path, QuestionsSHA256: qf.sha256,
		Writer: cfg.Generation.Role(config.RoleAnswers).Model, PromptVersion: ask.PromptVersion,
		Judge: opts.judgeModel, Judge2: opts.judge2Model, JudgeVersion: judgeVersion,
		Today: opts.today.Format(time.DateOnly), Sources: cfg.Ask.Sources, MaxTokens: cfg.Ask.MaxTokens,
		Rerank: cfg.Ask.Rerank}
	if recorded != nil {
		rep.RecordedPromptVersion = recorded.PromptVersion
	}
	rep.Checks = s.checks(ctx, opts, log)
	if opts.live() {
		if _, err := s.askOne(ctx, "warmup", "ask", question{ID: "warmup", Question: warmUpQuestion,
			Facts: []string{"-"}}, log); err != nil {
			return fmt.Errorf("the warm-up ask: %w", err)
		}
	}
	for _, v := range opts.variants {
		var as []answered
		for _, q := range qf.questions {
			a, err := s.answer(ctx, v, q, log)
			if err != nil {
				return err
			}
			as = append(as, a)
			log.Info("asked", "id", q.ID, "variant", v, "status", a.status, "route", a.route,
				"first_token_ms", a.stages.FirstToken, "total_ms", a.stages.Total,
				"cited", len(a.cited)+len(a.markers), "invalid", a.invalid, "replayed", a.timingsReplayed)
		}
		rep.Variants = append(rep.Variants, variantReport(v, as, s.judge2 != nil))
	}
	rep.Evicted = s.evicted(ctx, log)
	if err := rep.writeText(stdout); err != nil {
		return fmt.Errorf("write the report: %w", err)
	}
	if opts.json != "" {
		return devtool.WriteJSON(opts.json, rep)
	}
	return nil
}

// answer is q answered as variant v and judged: the recorded variant
// takes a question RouteFor sends to the answer route from the recorded
// report, asking nothing, and asks every other one live as the ask
// variant does.
func (s *setup) answer(ctx context.Context, v string, q question, log *slog.Logger) (answered, error) {
	if v == variantRecorded && ask.RouteFor(q.Question) == ask.RouteAnswer {
		a, ok := s.recorded.answeredOf(q)
		if !ok {
			return answered{}, fmt.Errorf("question %s: the recorded report has no answer to it", q.ID)
		}
		return a, nil
	}
	a, err := s.askOne(ctx, q.ID, v, q, log)
	if err != nil {
		return answered{}, err
	}
	if a.status != string(ask.StatusNoSources) {
		if err := s.judgeOne(ctx, &a, log); err != nil {
			return answered{}, fmt.Errorf("question %s: %w", q.ID, err)
		}
	}
	return a, nil
}

// build sets up the ask path over lib as the daemon does, its models
// config.yaml's, the writer's through an answer book.
func build(ctx context.Context, opts options, cfg config.Config, lib *devtool.Copy, log *slog.Logger) (*setup,
	error) {
	if lib.Width != cfg.Embedding.Dim {
		return nil, fmt.Errorf("the copy's vectors are %d wide, and the config sets embedding.dim %d", lib.Width,
			cfg.Embedding.Dim)
	}
	// What each server holds is read before the run loads anything, the
	// embedder for the stale cards first: what it held then stays loaded.
	s := &setup{cfg: cfg, models: servers(cfg, opts)}
	var err error
	if s.before, err = holdings(ctx, s.models); err != nil {
		return nil, err
	}
	timeout := time.Duration(cfg.Embedding.TimeoutSeconds) * time.Second
	emb, err := embedder.NewOllama(embedder.OllamaOptions{BaseURL: cfg.Embedding.BaseURL, Model: cfg.Embedding.Model,
		Dim: cfg.Embedding.Dim, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	idx := indexer.New(lib.Chunks, emb, indexer.Options{DocumentPrefix: cfg.Embedding.DocumentPrefix, Log: log})
	if n, err := idx.EmbedStaleCards(ctx, lib.Index, store.LocalTenantID); err != nil {
		return nil, fmt.Errorf("embed the copy's stale cards (%d first): %w", n, err)
	}

	// The clock runs from -today's noon, so holds still pass.
	shift := time.Until(opts.today)
	now := func() time.Time { return time.Now().Add(shift) }
	lifetime, stop := context.WithCancel(ctx)
	s.stop = stop

	if s.answers, err = openBook(opts.answers, opts.answersOut); err != nil {
		return nil, err
	}
	writerModel := cfg.Generation.Role(config.RoleAnswers)
	s.writer = &streamBook{model: writerModel.Model, version: ask.PromptVersion, book: s.answers}
	var writer generator.Generator = replayWriter{model: writerModel.Model, book: s.writer}
	if opts.answersOut != "" {
		live, err := ollamaOf(writerModel)
		if err != nil {
			return nil, err
		}
		s.writer.live = live
		writer = liveWriter{Ollama: live, book: s.writer}
		s.loaded = append(s.loaded, servedOf(writerModel.BaseURL, writerModel.Model))
	}

	engine := search.New(lib.Chunks, lib.Documents, lib.Index, emb, search.ConfigFrom(cfg))
	ret := retrieve.Retriever{Engine: engine, Fields: lib.Index, Summaries: lib.Summaries,
		TenantID: store.LocalTenantID, Log: log}
	// A replay that records asks a question its book lacks live, so it
	// needs the planner and the reranker of the variant it records.
	if opts.live() {
		if err := s.livePlanner(lifetime, cfg, now, log, &ret); err != nil {
			return nil, err
		}
	}
	// The writer is measured whatever ask.enabled says, as cmd/searcheval
	// plans whatever search.understand says: off by default is what a
	// model not yet measured is.
	askOpts := ask.OptionsFrom(cfg)
	askOpts.On, askOpts.OffReason = true, ""
	askOpts.Now, askOpts.Location, askOpts.Log, askOpts.Traffic = now, time.Local, log, &s.traffic
	s.asker = ask.New(lifetime, writer, ret, ask.Stores{Library: lib.Documents, Fields: lib.Index,
		Summaries: lib.Summaries, Bookmarks: lib.Bookmarks, Chunks: lib.Chunks, Interests: lib.Insights,
		TenantID: store.LocalTenantID}, askOpts)
	s.waitFor = append(s.waitFor, s.asker.Wait)

	if opts.judgeModel != "none" {
		if s.judge, err = s.newJudge(cfg, opts.judgeModel, opts.judgments, opts.judgmentsOut,
			"-judgments-out"); err != nil {
			return nil, err
		}
	}
	if opts.judge2Model != "" {
		if s.judge2, err = s.newJudge(cfg, opts.judge2Model, opts.judgments2, opts.judgments2Out,
			"-judgments2-out"); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// servers are the models the run may use, at their servers: the
// embedding model's, each writing role's in use, and the judges', which
// ask the answers role's server.
func servers(cfg config.Config, opts options) []served {
	out := []served{servedOf(cfg.Embedding.BaseURL, cfg.Embedding.Model)}
	for _, role := range cfg.RolesInUse() {
		spec := cfg.Generation.Role(role)
		out = append(out, servedOf(spec.BaseURL, spec.Model))
	}
	answers := cfg.Generation.Role(config.RoleAnswers).BaseURL
	for _, model := range []string{opts.judgeModel, opts.judge2Model} {
		if model != "" && model != "none" {
			out = append(out, servedOf(answers, model))
		}
	}
	return out
}

// holdings are what Ollama holds at each server of models, read once a
// server.
func holdings(ctx context.Context, models []served) (map[string][]ollama.Running, error) {
	out := map[string][]ollama.Running{}
	for _, m := range models {
		if _, read := out[m.baseURL]; read {
			continue
		}
		c, err := ollama.New(m.baseURL, m.model, opTimeout)
		if err != nil {
			return nil, err
		}
		pctx, cancel := context.WithTimeout(ctx, opTimeout)
		running, err := c.Running(pctx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("ask Ollama at %s what it holds: %w", m.baseURL, err)
		}
		out[m.baseURL] = running
	}
	return out, nil
}

// newJudge is a judge of model through a book that replays replay and
// records to record (recordFlag names its flag); it asks the model, at
// the answers role's server, only while it records.
func (s *setup) newJudge(cfg config.Config, model, replay, record, recordFlag string) (*judge, error) {
	b, err := openBook(replay, record)
	if err != nil {
		return nil, err
	}
	j := &judge{model: model, book: b, recordFlag: recordFlag}
	if record != "" {
		spec := cfg.Generation.Role(config.RoleAnswers)
		spec.Model = model
		if j.gen, err = ollamaOf(spec); err != nil {
			return nil, err
		}
		s.loaded = append(s.loaded, servedOf(spec.BaseURL, spec.Model))
	}
	return j, nil
}

// livePlanner builds the planner and the reranker as the daemon does, for
// a run that asks them.
func (s *setup) livePlanner(lifetime context.Context, cfg config.Config, now func() time.Time, log *slog.Logger,
	ret *retrieve.Retriever) error {
	if slices.Contains(cfg.RolesInUse(), config.RolePlanner) {
		spec := cfg.Generation.Role(config.RolePlanner)
		gen, err := generator.New(spec)
		if err != nil {
			return err
		}
		popts := understand.OptionsFrom(cfg)
		popts.Now, popts.Location, popts.Log, popts.Traffic = now, time.Local, log, &s.traffic
		s.planner = understand.New(lifetime, gen, popts)
		ret.Planner = s.planner
		s.waitFor = append(s.waitFor, s.planner.Wait)
		s.loaded = append(s.loaded, servedOf(spec.BaseURL, spec.Model))
	}
	spec := cfg.Generation.Role(config.RoleReranker)
	gen, err := generator.New(spec)
	if err != nil {
		return err
	}
	ropts := rerank.OptionsFrom(cfg)
	ropts.Now, ropts.Log, ropts.Traffic = now, log, &s.traffic
	s.rerank = rerank.New(lifetime, gen, ropts)
	ret.Reranker = s.rerank
	s.waitFor = append(s.waitFor, s.rerank.Wait)
	s.loaded = append(s.loaded, servedOf(spec.BaseURL, spec.Model))
	return nil
}

// ollamaOf is spec's Ollama generator.
func ollamaOf(spec config.ModelSpec) (*generator.Ollama, error) {
	gen, err := generator.New(spec)
	if err != nil {
		return nil, err
	}
	o, ok := gen.(*generator.Ollama)
	if !ok {
		return nil, fmt.Errorf("askeval asks Ollama alone, not %s", spec.Provider)
	}
	return o, nil
}

// checks runs the planner's, the reranker's and the writer's fitness
// checks now, reported and never a gate, so every ask finds its model
// checked; a replay asks none.
func (s *setup) checks(ctx context.Context, opts options, log *slog.Logger) map[string]string {
	out := map[string]string{}
	verdict := func(failed string, err error) string {
		switch {
		case err != nil:
			return "unreached: " + err.Error()
		case failed != "":
			return "unfit: " + failed
		}
		return "fit"
	}
	if s.planner != nil {
		out["planner"] = verdict(s.planner.Check(ctx))
	}
	if s.rerank != nil {
		out["reranker"] = verdict(s.rerank.Check(ctx))
	}
	if opts.live() {
		s.writer.asking("check")
		out["writer"] = verdict(s.asker.Check(ctx))
	} else {
		out["writer"] = "replayed"
	}
	for role, v := range out {
		log.Info("check", "role", role, "verdict", v)
	}
	return out
}

// request is q as variant v asks it: ask leaves the rerank to ask.rerank,
// as the daemon does; norerank turns it off; literal turns off the
// understanding too; recorded asks as ask.
func request(v string, q question) ask.Request {
	req := ask.Request{Question: q.Question, Understand: v != "literal"}
	if v == "norerank" || v == "literal" {
		req.Rerank = new(false)
	}
	return req
}

// collected are an ask's events as the evaluation reads them.
type collected struct {
	retrieved ask.Retrieved
	evidence  *ask.Evidence
	table     *ask.Table
	groups    *ask.Groups
	answer    strings.Builder
	done      ask.Done
}

// citable is how many of what the answer was written from it may cite:
// its sources, the table's marks or the groups shown.
func (c *collected) citable() int {
	switch {
	case c.table != nil:
		return c.table.Shown
	case c.groups != nil:
		return c.groups.Shown
	}
	return len(c.retrieved.Sources)
}

// askOne asks q, as variant v, through the asker, and records it. An ask
// the book holds is replayed: its plan and order supplied, and its
// timings the recorded ones, since a replayed answer costs nothing.
func (s *setup) askOne(ctx context.Context, id, v string, q question, log *slog.Logger) (answered, error) {
	req := request(v, q)
	rec, replayed := s.answers.askOf(v, id)
	if replayed {
		req.Understand, req.Rerank = false, new(false)
		if rec.Plan != nil {
			req.Plan = &understand.Plan{Kind: understand.Kind(rec.Plan.Kind), Filters: rec.Plan.Filters,
				Rewrites: rec.Plan.Rewrites, Names: rec.Plan.Names}
		}
		if len(rec.Order) > 0 {
			req.RerankOrder = rec.Order
		}
	}
	s.writer.asking(id)
	var got collected
	err := s.asker.Ask(ctx, req, func(e ask.Event) error {
		switch e := e.(type) {
		case ask.Retrieved:
			got.retrieved = e
		case ask.Evidence:
			got.evidence = &e
		case ask.Table:
			got.table = &e
		case ask.Groups:
			got.groups = &e
		case ask.Delta:
			got.answer.WriteString(e.Text)
		case ask.Done:
			got.done = e
		case ask.Progress:
		}
		return nil
	})
	if err != nil {
		return answered{}, fmt.Errorf("question %s: %w", id, err)
	}
	done := got.done
	if done.Status == ask.StatusUnavailable && strings.Contains(done.Reason, errNotRecorded.Error()) {
		return answered{}, fmt.Errorf("question %s: %s", id, done.Reason)
	}
	prompt, cost, fromBook := s.writer.answered()
	a := scored(q, &got, prompt, cost)
	// An answer the book held, though this ask wasn't a replay (another
	// variant of the run wrote the same prompt), was timed by nobody.
	a.timingsReplayed = fromBook
	if replayed {
		a.stages = rec.Stages
		if a.stages.Total == 0 {
			a.stages.FirstToken, a.stages.Total = rec.FirstMS, rec.TotalMS
		}
		a.promptTokens, a.outputTokens = rec.PromptTokens, rec.OutputTokens
		a.timingsReplayed = true
		log.Debug("ask replayed", "id", id, "variant", v)
		return a, nil
	}
	rec = askRecord{Variant: v, ID: id, Route: string(done.Route), Status: string(done.Status),
		FirstMS: done.Timings.FirstToken.Milliseconds(), TotalMS: done.Timings.Total.Milliseconds(),
		Stages: a.stages, PromptTokens: a.promptTokens, OutputTokens: a.outputTokens, Answer: got.answer.String(),
		Citation: done.Citations, Markers: done.Markers}
	if u := got.retrieved.Understanding; u != nil && u.Plan != nil {
		rec.Plan = &planRecord{Kind: string(u.Plan.Kind), Filters: u.Plan.Filters, Rewrites: u.Plan.Rewrites,
			Names: u.Plan.Names}
	}
	if r := got.retrieved.Reranking; r != nil {
		rec.Order = r.Order
	}
	for _, src := range got.retrieved.Sources {
		rec.Sources = append(rec.Sources, src.DocID)
	}
	if err := s.answers.addAsk(rec); err != nil {
		return answered{}, err
	}
	log.Debug("ask recorded", "id", id, "variant", v)
	return a, nil
}

// scored is an ask as the evaluation scores it: its route, its citations
// read sentence by sentence as the ask reads them (ask.CitationsOf), the
// pages it cites (on the list route every page of each group it cites),
// what its writer read (ask.Material of its prompt) and what it cost.
func scored(q question, got *collected, prompt string, cost call) answered {
	done := got.done
	route := done.Route
	a := answered{q: q, route: string(route), status: string(done.Status), cited: done.Citations,
		markers: done.Markers, invalid: len(done.InvalidCitations), abstained: done.Abstained,
		sents: sentences(got.answer.String()), answer: got.answer.String(), material: ask.Material(prompt),
		stages: stagesOf(done.Timings), promptTokens: cost.PromptTokens, outputTokens: cost.OutputTokens}
	if got.evidence != nil {
		a.evidence = string(got.evidence.Status)
	}
	for _, src := range got.retrieved.Sources {
		a.sources = append(a.sources, src.URL)
	}
	n := got.citable()
	a.sentCited = make([]bool, len(a.sents))
	a.sentCitations = make([][]string, len(a.sents))
	for i, sent := range a.sents {
		c := ask.CitationsOf(sent, route, n)
		a.sentCited[i] = len(c.Cited)+len(c.Markers) > 0
		a.sentCitations[i] = written(c)
	}
	a.citedURLs = citedURLs(got)
	return a
}

// written are c's citations as written: the markers and the sources'
// numbers it cites, then the invalid ones; nil for none.
func written(c ask.Citations) []string {
	var out []string
	out = append(out, c.Markers...)
	for _, k := range c.Cited {
		out = append(out, strconv.Itoa(k))
	}
	return append(out, c.Invalid...)
}

// citedURLs are the normalized URLs of the pages an answer cites: its
// sources by number, or on the list route every page of each group it
// cites.
func citedURLs(got *collected) []string {
	var urls []string
	if got.groups != nil {
		for _, g := range got.groups.Groups {
			if slices.Contains(got.done.Markers, g.Marker) {
				for _, d := range g.Documents {
					urls = append(urls, d.URL)
				}
			}
		}
	}
	for _, n := range got.done.Citations {
		if n >= 1 && n <= len(got.retrieved.Sources) {
			urls = append(urls, got.retrieved.Sources[n-1].URL)
		}
	}
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if n, err := urlutil.Normalize(u); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// judgeOne has each judge judge a's answer against what its writer read,
// the material of the writer's own prompt, with the markers template for
// an answer citing table rows or groups. A judge whose verdict can't be
// used leaves the answer unjudged by it, counted and logged: a judge that
// strays on one answer says nothing of the others.
func (s *setup) judgeOne(ctx context.Context, a *answered, log *slog.Logger) error {
	if s.judge == nil {
		return nil
	}
	markers := a.route == string(ask.RouteInterests) || a.route == string(ask.RouteList)
	for _, by := range []struct {
		judge    *judge
		verdicts *judgment
		judged   *bool
	}{{s.judge, &a.verdicts, &a.judged}, {s.judge2, &a.verdicts2, &a.judged2}} {
		if by.judge == nil {
			continue
		}
		j, err := by.judge.judgeAnswer(ctx, a.material, markers, a.sents, a.q.Facts)
		switch {
		case errors.Is(err, errBadJudgment):
			log.Warn("a judge's verdict couldn't be used; the answer stays unjudged by it", "id", a.q.ID,
				"judge", by.judge.model, "err", err)
			continue
		case err != nil:
			return err
		}
		*by.verdicts, *by.judged = j, true
	}
	return nil
}

// evicted are the models a server held before the run and doesn't now,
// each named with its server.
func (s *setup) evicted(ctx context.Context, log *slog.Logger) []string {
	after, err := holdings(ctx, s.models)
	if err != nil {
		log.Warn("ask Ollama what it holds after the run", "err", err)
		return nil
	}
	urls := slices.Sorted(maps.Keys(s.before))
	var out []string
	for _, u := range urls {
		for _, m := range s.before[u] {
			if !slices.ContainsFunc(after[u], func(r ollama.Running) bool { return r.Is(m.Name) }) {
				out = append(out, m.Name+" at "+u)
			}
		}
	}
	return out
}

// close stops the background work and unloads every model the run loaded
// that its server didn't hold before it, each at its own server.
func (s *setup) close(log *slog.Logger) {
	s.stop()
	for _, wait := range s.waitFor {
		wait()
	}
	books := []*book{s.answers}
	for _, j := range []*judge{s.judge, s.judge2} {
		if j != nil {
			books = append(books, j.book)
		}
	}
	for _, b := range books {
		if err := b.close(); err != nil {
			log.Warn("close a book", "err", err)
		}
	}
	unloaded := map[served]bool{}
	for _, m := range s.loaded {
		if unloaded[m] || s.heldBefore(m) {
			continue
		}
		unloaded[m] = true
		g, err := generator.NewOllama(generator.OllamaOptions{BaseURL: m.baseURL, Model: m.model, Timeout: opTimeout})
		if err != nil {
			log.Warn("unload a model", "model", m.model, "err", err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		if err := g.Unload(ctx); err != nil {
			log.Warn("unload a model", "model", m.model, "err", err)
		}
		cancel()
	}
	if err := s.unloadEmbedder(); err != nil {
		log.Warn("unload the embedding model", "model", s.cfg.Embedding.Model, "err", err)
	}
}

// heldBefore reports whether m's server held m when the run began.
func (s *setup) heldBefore(m served) bool {
	return slices.ContainsFunc(s.before[m.baseURL], func(r ollama.Running) bool { return r.Is(m.model) })
}

// unloadEmbedder unloads the embedding model, which every search loads,
// unless its server held it when the run began: an embed of nothing kept
// for no time.
func (s *setup) unloadEmbedder() error {
	e := s.cfg.Embedding
	if s.heldBefore(servedOf(e.BaseURL, e.Model)) {
		return nil
	}
	c, err := ollama.New(e.BaseURL, e.Model, opTimeout)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	req := struct {
		Model     string   `json:"model"`
		Input     []string `json:"input"`
		KeepAlive int      `json:"keep_alive"`
	}{Model: e.Model, Input: []string{}}
	var reply struct{}
	if err := c.PostJSON(ctx, "/api/embed", req, &reply, embedReplyBytes); err != nil {
		return fmt.Errorf("embed nothing to unload it: %w", err)
	}
	return nil
}

// embedReplyBytes bounds the reply to the embed that unloads the
// embedding model, which carries no vector.
const embedReplyBytes = 64 << 10
