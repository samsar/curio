package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/devtool"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/summarize"
)

// summariesReport is what -summarize or -summaries did to the copy before
// any query: the writer (its model, prompt and check) or the replay file,
// how many summaries there were to write, and of them how many were
// written, replayed, failed or mismatched, what a page cost the model, the
// answers that couldn't be used by rule, the kinds written and the
// entities grounding dropped. A summary already current isn't to write:
// the pass's rule leaves it out.
type summariesReport struct {
	Mode          string `json:"mode"` // "write" or "replay"
	Model         string `json:"model,omitempty"`
	PromptVersion string `json:"prompt_version"`
	// Check is the writer's fitness check: passed, failed:<probe> or
	// error:<why it couldn't be asked>. It is reported, never a gate: the
	// tool measures the model.
	Check string `json:"check,omitempty"`
	// From is the replayed file; Documents the -documents file writing was
	// held to.
	From      string `json:"from,omitempty"`
	Documents string `json:"documents,omitempty"`

	ToWrite         int            `json:"to_write"`
	Written         int            `json:"written"`
	Replayed        int            `json:"replayed"`
	Mismatched      int            `json:"mismatched"`
	Failed          map[string]int `json:"failed"`
	Kinds           map[string]int `json:"kinds"`
	EntitiesDropped int            `json:"entities_dropped"`

	TookMS       float64     `json:"took_ms"`
	PageMS       percentiles `json:"page_ms"`
	PromptTokens intPercent  `json:"prompt_tokens"`
	OutputTokens intPercent  `json:"output_tokens"`
}

// intPercent is a count's nearest-rank p50 and p95.
type intPercent struct {
	P50 int `json:"p50"`
	P95 int `json:"p95"`
}

// summaryRecord is one page's outcome as -summaries-out writes it, a JSON
// line each, appended as the run goes so a stopped run keeps what it did:
// the summary as written (status written) or why it wasn't (failed, with
// the cause), with what the call cost. The file holds what pages say, so
// it stays out of the repository, like the query file. -summaries replays
// its written records.
type summaryRecord struct {
	DocumentID      string    `json:"document_id"`
	ExtractionID    string    `json:"extraction_id"`
	Status          string    `json:"status"`
	Cause           string    `json:"cause,omitempty"`
	Kind            string    `json:"kind,omitempty"`
	Site            string    `json:"site,omitempty"`
	Author          string    `json:"author,omitempty"`
	About           string    `json:"about,omitempty"`
	Entities        []string  `json:"entities,omitempty"`
	Claims          []string  `json:"claims,omitempty"`
	Descriptions    []string  `json:"descriptions,omitempty"`
	Model           string    `json:"model"`
	PromptVersion   string    `json:"prompt_version"`
	InputHash       string    `json:"input_hash,omitempty"`
	WrittenAt       time.Time `json:"written_at"`
	TookMS          float64   `json:"took_ms"`
	PromptTokens    int       `json:"prompt_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	EntitiesDropped int       `json:"entities_dropped"`
}

// The statuses of a summaryRecord.
const (
	recordWritten = "written"
	recordFailed  = "failed"
)

// summariesSetup is what writing or replaying summaries needs besides
// the copy: the flags, the config, the log.
type summariesSetup struct {
	opts options
	cfg  config.Config
	log  *slog.Logger
}

// summaries writes (-summarize) or else replays (-summaries) the copy's
// summaries. No card reads a summary, so none is embedded again. The
// caller asks only with one of the flags.
func summaries(ctx context.Context, s summariesSetup, lib *devtool.Copy) (*summariesReport, error) {
	if s.opts.summarize {
		return writeSummaries(ctx, s, lib)
	}
	return replaySummaries(ctx, s.opts.summaries, lib, s.log)
}

// writeSummaries writes the summaries the summary pass would write, those
// in -documents when it is set, one call at a time, with config.yaml's
// summaries role (whatever summaries.enabled says) through the daemon's
// writer and its input (jobs.SummaryInput, from the markdown under
// -content), each stored alone as soon as it is written: a run stopped
// part way keeps them, and the next writes only what is left, by the
// pass's own rule (SummaryStore.ToSummarize), which leaves out a page
// failed for good within its wait: a page's verdict is recorded as the
// pass's hook records it. With nothing to write it asks no model. It never
// pulls a model, and unloads the one it loaded. The fitness check is run
// and reported first, and stops nothing. A model that stops answering
// stops the run, with what it wrote kept; a page's own failure is counted
// by cause and the run goes on.
func writeSummaries(ctx context.Context, s summariesSetup, lib *devtool.Copy) (*summariesReport, error) {
	opts := summarize.OptionsFrom(s.cfg)
	opts.On, opts.Measure, opts.Log = true, true, s.log
	if opts.Withheld != "" {
		return nil, fmt.Errorf("-summarize: %s", opts.Withheld)
	}
	var only map[string]bool
	if s.opts.documents != "" {
		ids, err := readDocumentList(s.opts.documents)
		if err != nil {
			return nil, err
		}
		only = ids
	}
	gen, err := generator.New(opts.Model)
	if err != nil {
		return nil, fmt.Errorf("build the summaries' model: %w", err)
	}
	warmer, ok := gen.(generator.Warmer)
	if _, structured := gen.(generator.JSONGenerator); !structured || !ok {
		return nil, fmt.Errorf("the summaries' provider %s can't answer in JSON or load its model", opts.Model.Provider)
	}
	if err := pingModel(ctx, gen, opts.Model, "summaries'"); err != nil {
		return nil, err
	}
	writer := summarize.New(gen, opts)
	rep := &summariesReport{Mode: "write", Model: opts.Model.Model, PromptVersion: summarize.PromptVersion,
		Documents: s.opts.documents, Failed: map[string]int{}, Kinds: map[string]int{}}
	targets, err := lib.Summaries.ToSummarize(ctx, store.LocalTenantID, writer.Identity(), jobs.SummariesFailedWait,
		maxTargets)
	if err != nil {
		return nil, err
	}
	if only != nil {
		targets = slices.DeleteFunc(targets, func(t store.SummaryTarget) bool { return !only[t.DocumentID] })
	}
	rep.ToWrite = len(targets)
	if len(targets) == 0 {
		s.log.Info("no summary to write", "model", opts.Model.Model)
		return rep, nil
	}

	var out *recordFile
	if s.opts.summariesOut != "" {
		if out, err = openRecords(s.opts.summariesOut); err != nil {
			return nil, err
		}
		defer func() {
			if err := out.Close(); err != nil {
				s.log.Warn("close the summaries file", "path", s.opts.summariesOut, "err", err)
			}
		}()
	}

	wasLoaded, err := warmer.Loaded(ctx)
	if err != nil {
		return nil, fmt.Errorf("ask whether %s is loaded: %w", opts.Model.Model, err)
	}
	if !wasLoaded {
		defer unloadModel(ctx, warmer, opts.Model.Model, s.log)
	}
	if err := writer.Ready(ctx); err != nil {
		return nil, fmt.Errorf("load the summaries' model %s: %w", opts.Model.Model, err)
	}
	switch failed, err := writer.Check(ctx); {
	case err != nil && ctx.Err() != nil:
		return nil, err
	case err != nil:
		rep.Check = "error:" + err.Error()
	case failed != "":
		rep.Check = "failed:" + failed
	default:
		rep.Check = "passed"
	}
	s.log.Info("the summaries' check", "model", opts.Model.Model, "check", rep.Check, "to_write", rep.ToWrite)

	deps := jobs.Deps{ContentDir: s.opts.content, Extractions: lib.Extractions, DocumentIndex: lib.Index, Log: s.log}
	w := &summaryWriting{rep: rep, out: out, log: s.log, began: time.Now()}
	for _, t := range targets {
		if err := w.one(ctx, lib, deps, writer, t); err != nil {
			return nil, err
		}
	}
	w.finish()
	return rep, nil
}

// maxTargets bounds the documents writeSummaries lists: far more than any
// library holds.
const maxTargets = 1 << 20

// summaryWriting is a run of writeSummaries under way: its report, the
// record file, and each written page's cost.
type summaryWriting struct {
	rep    *summariesReport
	out    *recordFile
	log    *slog.Logger
	began  time.Time
	logged time.Time
	took   []time.Duration
	prompt []int
	output []int
}

// one writes the summary of t, records it, and counts it.
func (w *summaryWriting) one(ctx context.Context, lib *devtool.Copy, deps jobs.Deps, writer *summarize.Writer,
	t store.SummaryTarget) error {
	doc, err := lib.Documents.GetByID(ctx, t.DocumentID)
	if err != nil {
		return fmt.Errorf("load document %s: %w", t.DocumentID, err)
	}
	in, err := jobs.SummaryInput(ctx, deps, doc)
	if err != nil {
		if errors.Is(err, jobs.ErrNoMarkdown) {
			return fmt.Errorf("the markdown of document %s is missing under %s: point -content at the content "+
				"directory of the home the copy is from: %w", doc.ID, deps.ContentDir, err)
		}
		return err
	}
	written, err := writer.Write(ctx, in)
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("write summaries (%d written first): %w", w.rep.Written, ctx.Err())
	case err != nil && stopsTheRun(err):
		return fmt.Errorf("write the summary of document %s (%d written first): %w", doc.ID, w.rep.Written, err)
	case err != nil:
		cause := failureCause(err)
		w.rep.Failed[cause]++
		if verdictCause(cause) {
			// As the pass's hook records it: the rerun leaves the page, as
			// the pass does, within its wait.
			if err := lib.Summaries.RecordSummaryFailure(ctx, store.LocalTenantID, doc.ID, store.SummaryFailure{
				ExtractionID: t.ExtractionID, SummaryWriter: writer.Identity(), At: time.Now(),
				Error: err.Error()}); err != nil {
				return fmt.Errorf("record the summary failure of document %s: %w", doc.ID, err)
			}
		}
		return w.out.write(summaryRecord{DocumentID: doc.ID, ExtractionID: t.ExtractionID, Status: recordFailed,
			Cause: cause, Model: writer.Identity().Model, PromptVersion: summarize.PromptVersion, WrittenAt: time.Now()})
	}
	sum := written.Summary
	sum.DocumentID, sum.ExtractionID = doc.ID, t.ExtractionID
	if err := lib.Summaries.PutSummary(ctx, store.LocalTenantID, sum); err != nil {
		return fmt.Errorf("store the summary of document %s: %w", doc.ID, err)
	}
	w.rep.Written++
	w.rep.Kinds[string(sum.Kind)]++
	w.rep.EntitiesDropped += written.Dropped
	w.took = append(w.took, written.Took)
	w.prompt = append(w.prompt, written.Usage.PromptTokens)
	w.output = append(w.output, written.Usage.OutputTokens)
	w.progress()
	return w.out.write(summaryRecord{DocumentID: doc.ID, ExtractionID: t.ExtractionID, Status: recordWritten,
		Kind: string(sum.Kind), Site: sum.Site, Author: sum.Author, About: sum.About, Entities: sum.Entities,
		Claims: sum.Claims, Descriptions: sum.Descriptions, Model: sum.Model, PromptVersion: sum.PromptVersion,
		InputHash: sum.InputHash, WrittenAt: time.Now().UTC(), TookMS: ms(written.Took),
		PromptTokens: written.Usage.PromptTokens, OutputTokens: written.Usage.OutputTokens,
		EntitiesDropped: written.Dropped})
}

// summariesProgressEvery is how often a run says how far it got.
const summariesProgressEvery = 30 * time.Second

// progress logs how far the run got, at most every summariesProgressEvery.
func (w *summaryWriting) progress() {
	if time.Since(w.logged) < summariesProgressEvery {
		return
	}
	w.logged = time.Now()
	done := w.rep.Written + failedCount(w.rep.Failed)
	perPage := time.Since(w.began) / time.Duration(max(done, 1))
	w.log.Info("writing summaries", "done", done, "of", w.rep.ToWrite, "written", w.rep.Written,
		"eta", (perPage * time.Duration(w.rep.ToWrite-done)).Round(time.Second).String())
}

// finish fills in the run's timings.
func (w *summaryWriting) finish() {
	w.rep.TookMS = ms(time.Since(w.began))
	slices.Sort(w.took)
	slices.Sort(w.prompt)
	slices.Sort(w.output)
	w.rep.PageMS = percentiles{P50: ms(nearestRank(w.took, 50)), P95: ms(nearestRank(w.took, 95))}
	w.rep.PromptTokens = intPercent{P50: nearestCount(w.prompt, 50), P95: nearestCount(w.prompt, 95)}
	w.rep.OutputTokens = intPercent{P50: nearestCount(w.output, 50), P95: nearestCount(w.output, 95)}
	w.log.Info("summaries written", "written", w.rep.Written, "failed", failedCount(w.rep.Failed),
		"took_ms", int64(w.rep.TookMS))
}

// nearestCount is nearestRank over counts.
func nearestCount(sorted []int, pct int) int {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[max((pct*len(sorted)+99)/100-1, 0)]
}

// failedCount is how many pages failed, of every cause.
func failedCount(failed map[string]int) int {
	n := 0
	for _, c := range failed {
		n += c
	}
	return n
}

// stopsTheRun reports whether a write's failure is the model's, not the
// page's: no answer, a model that isn't there or won't serve the caller,
// or a writer that writes nothing. The rest of the pages would fail the
// same way, so the run stops, keeping what it wrote.
func stopsTheRun(err error) bool {
	return errors.Is(err, generator.ErrUnreachable) || errors.Is(err, generator.ErrModelMissing) ||
		errors.Is(err, generator.ErrUnauthorized) || errors.Is(err, summarize.ErrNotWriting)
}

// failureCause names a page's own failure for the report: the rule an
// answer that couldn't be used broke (invalid:<rule>), or the generator's
// verdict.
func failureCause(err error) string {
	if ia, ok := errors.AsType[*summarize.InvalidAnswer](err); ok {
		return "invalid:" + ia.Rule
	}
	switch {
	case errors.Is(err, generator.ErrTruncated):
		return "truncated"
	case errors.Is(err, generator.ErrNotJSON):
		return "not_json"
	case errors.Is(err, generator.ErrBadRequest):
		return "bad_request"
	case errors.Is(err, summarize.ErrSlow), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, generator.ErrUnavailable):
		return "unavailable"
	}
	return "other"
}

// verdictCause reports whether a page's failure of cause is a verdict on
// the page, which the pass fails the page for good for and the store
// records: an answer that couldn't be used, a truncated or non-JSON one,
// a request the provider refused as made. A timeout or a provider that
// couldn't serve is an attempt the pass retries.
func verdictCause(cause string) bool {
	return strings.HasPrefix(cause, "invalid:") || cause == "truncated" || cause == "not_json" ||
		cause == "bad_request"
}

// replaySummaries stores the written summaries of the file at path
// through PutSummary, asking no model: each where its document is still
// fetched at the extraction it was written from, the rest counted
// mismatched. A document written more than once (a resumed run) keeps its
// last.
func replaySummaries(ctx context.Context, path string, lib *devtool.Copy, log *slog.Logger) (*summariesReport,
	error) {
	records, err := readRecords(path)
	if err != nil {
		return nil, err
	}
	rep := &summariesReport{Mode: "replay", From: path, Failed: map[string]int{}, Kinds: map[string]int{}}
	start := time.Now()
	writers := map[store.SummaryWriter]int{}
	for _, r := range records {
		if r.Status != recordWritten {
			rep.Failed[r.Cause]++
			continue
		}
		rep.ToWrite++
		doc, err := lib.Documents.GetByID(ctx, r.DocumentID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			rep.Mismatched++
			continue
		case err != nil:
			return nil, fmt.Errorf("load document %s: %w", r.DocumentID, err)
		case doc.State != store.DocStateFetched || doc.CurrentExtractionID == nil ||
			*doc.CurrentExtractionID != r.ExtractionID:
			rep.Mismatched++
			continue
		}
		sum := r.summary()
		switch err := lib.Summaries.PutSummary(ctx, store.LocalTenantID, sum); {
		case errors.Is(err, store.ErrSuperseded):
			rep.Mismatched++
			continue
		case err != nil:
			return nil, fmt.Errorf("replay the summary of document %s: %w", r.DocumentID, err)
		}
		rep.Replayed++
		rep.Kinds[r.Kind]++
		rep.EntitiesDropped += r.EntitiesDropped
		writers[sum.SummaryWriter]++
	}
	rep.TookMS = ms(time.Since(start))
	// The replayed summaries' writer, when they share one: the grid replays
	// one full pass, whose prompt stays frozen.
	if len(writers) == 1 {
		for w := range maps.Keys(writers) {
			rep.Model, rep.PromptVersion = w.Model, w.PromptVersion
		}
	}
	log.Info("summaries replayed", "from", path, "replayed", rep.Replayed, "mismatched", rep.Mismatched,
		"writers", len(writers))
	return rep, nil
}

// summary is the record's summary as the store takes it.
func (r summaryRecord) summary() store.Summary {
	return store.Summary{DocumentID: r.DocumentID, ExtractionID: r.ExtractionID, Kind: store.SummaryKind(r.Kind),
		Site: r.Site, Author: r.Author, About: r.About, Entities: r.Entities, Claims: r.Claims,
		Descriptions: r.Descriptions, SummaryWriter: store.SummaryWriter{Model: r.Model,
			PromptVersion: r.PromptVersion}, InputHash: r.InputHash, WrittenAt: r.WrittenAt}
}

// recordFile is the -summaries-out file, appended to a line at a time;
// a nil one writes nothing.
type recordFile struct {
	f   *os.File
	enc *json.Encoder
}

// openRecords opens path for appending, creating it readable by its owner
// alone: it holds what pages say.
func openRecords(path string) (*recordFile, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("the -summaries-out file: %w", err)
	}
	return &recordFile{f: f, enc: json.NewEncoder(f)}, nil
}

// write appends r as a line.
func (o *recordFile) write(r summaryRecord) error {
	if o == nil {
		return nil
	}
	if err := o.enc.Encode(r); err != nil {
		return fmt.Errorf("write the -summaries-out file: %w", err)
	}
	return nil
}

// Close closes the file.
func (o *recordFile) Close() error { return o.f.Close() }

// readRecords reads a -summaries-out file, a record a line, refusing a
// line that isn't one.
func readRecords(path string) ([]summaryRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("the -summaries file: %w", err)
	}
	defer f.Close()
	var out []summaryRecord
	dec := json.NewDecoder(bufio.NewReader(f))
	dec.DisallowUnknownFields()
	for line := 1; ; line++ {
		var r summaryRecord
		switch err := dec.Decode(&r); {
		case errors.Is(err, io.EOF):
			return out, nil
		case err != nil:
			return nil, fmt.Errorf("the -summaries file %s, record %d: %w", path, line, err)
		case r.DocumentID == "" || r.ExtractionID == "":
			return nil, fmt.Errorf("the -summaries file %s, record %d: no document or extraction", path, line)
		}
		out = append(out, r)
	}
}

// readDocumentList reads a -documents file: a document ID a line, blank
// lines and lines starting with # left out.
func readDocumentList(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the -documents file: %w", err)
	}
	out := map[string]bool{}
	for line := range strings.Lines(string(data)) {
		if id := strings.TrimSpace(line); id != "" && !strings.HasPrefix(id, "#") {
			out[id] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the -documents file %s names no document", path)
	}
	return out, nil
}

// pingModel checks that Ollama has the model whose (e.g. "planner's") is
// named; it never pulls one.
func pingModel(ctx context.Context, gen generator.Generator, spec config.ModelSpec, whose string) error {
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	switch err := gen.Ping(pctx); {
	case errors.Is(err, ollama.ErrModelNotLoaded):
		return fmt.Errorf("the Ollama at %s doesn't have the %s model %s, and searcheval never pulls a "+
			"model: %w", spec.BaseURL, whose, spec.Model, err)
	case err != nil:
		return fmt.Errorf("ask the Ollama at %s for the %s model %s: %w", spec.BaseURL, whose, spec.Model, err)
	}
	return nil
}

// unloadBound bounds unloading a model at the end of a run, which runs
// even when the run's context ended.
const unloadBound = 30 * time.Second

// unloadModel frees the model the run loaded.
func unloadModel(ctx context.Context, warmer generator.Warmer, model string, log *slog.Logger) {
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unloadBound)
	defer cancel()
	if err := warmer.Unload(uctx); err != nil {
		log.Warn("couldn't unload the model searcheval loaded", "model", model, "err", err)
		return
	}
	log.Info("unloaded the model searcheval loaded", "model", model)
}

// text is the report's lines about the summaries.
func (r *summariesReport) text() string {
	var b strings.Builder
	switch r.Mode {
	case "write":
		fmt.Fprintf(&b, "Summaries: %d of %d written with %s (prompt %s, check %s) in %.1f s; per page p50 %.0f ms, "+
			"p95 %.0f ms; prompt tokens p50 %d, p95 %d; output tokens p50 %d, p95 %d\n", r.Written, r.ToWrite, r.Model,
			shortHash(r.PromptVersion), r.Check, r.TookMS/1000, r.PageMS.P50, r.PageMS.P95, r.PromptTokens.P50,
			r.PromptTokens.P95, r.OutputTokens.P50, r.OutputTokens.P95)
	default:
		fmt.Fprintf(&b, "Summaries: %d of %d replayed from %s (%s, prompt %s), %d mismatched, in %.1f s\n", r.Replayed,
			r.ToWrite, r.From, r.Model, shortHash(r.PromptVersion), r.Mismatched, r.TookMS/1000)
	}
	fmt.Fprintf(&b, "  kinds: %s; entities dropped by grounding: %d; failed: %s\n", counts(r.Kinds),
		r.EntitiesDropped, counts(r.Failed))
	return b.String()
}

// counts are a map's counts, by key: "article 3, home 1", or "none".
func counts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}
