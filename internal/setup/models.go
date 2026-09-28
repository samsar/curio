package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/ollama"
)

// pickSource is where the writing model came from.
type pickSource int

const (
	noWritingModel    pickSource = iota // interest labels use none
	fromAdvisor                         // curio picked it for this Mac, or the user chose from its tiers
	fromFlag                            // --generation-model
	fromConfig                          // config.yaml's generation.model
	fromDaemonDefault                   // config.yaml sets none, so the daemon's default
)

// wanted is a model and the Ollama that serves it.
type wanted struct {
	Model
	baseURL string
}

// picks are the models the home needs: the embedding model always, and a
// writing model when interest labels use one.
type picks struct {
	embedding  wanted
	generation *wanted // nil: no writing model is pulled
	source     pickSource
	advice     Advice // curio's pick for this Mac
	notes      []string
}

// curios reports whether curio picked the writing model, which the user
// is asked about.
func (p picks) curios() bool { return p.source == fromAdvisor }

// all is every model picked.
func (p picks) all() []wanted {
	if p.generation == nil {
		return []wanted{p.embedding}
	}
	return []wanted{p.embedding, *p.generation}
}

// String names the picks: "qwen3-embedding:0.6b for search and gemma4:26b
// for writing".
func (p picks) String() string {
	s := p.embedding.Name + " for search"
	if p.generation != nil {
		s += " and " + p.generation.Name + " for writing"
	}
	return s
}

// pick decides the models, reading the home as it is: the embedding model
// is the home's, or a new home's (--embedding-model, or the default); the
// writing model is config.yaml's, kept whatever the flags say, or the
// flag, or curio's pick for this Mac. A --generation-model config.yaml
// contradicts is an error: curio up never rewrites a value the user set.
func (w *world) pick(ctx context.Context, hs homeState) (picks, error) {
	cfg := w.baseConfig(hs)
	machine, err := w.probeMachine(ctx)
	p := picks{advice: w.advisor.Advise(machine.Memory)}
	if err != nil {
		p.notes = append(p.notes, "the Mac couldn't be probed, so curio picks for the smallest tier")
	}
	embedding := w.newEmbeddingModel()
	if hs.kind == homeOurs && !w.freshPending() && hs.metaErr == nil {
		embedding = hs.meta.EmbeddingModel
	}
	p.embedding = wanted{knownModel(embedding), cfg.Embedding.BaseURL}

	if !cfg.Insight.Enabled || cfg.Insight.Labeling != insight.LabelingLLM {
		p.notes = append(p.notes, "no writing model is pulled: interest labels don't use one "+
			"(insight.enabled false, or insight.labeling not llm)")
		return p, nil
	}
	flag := w.opts.GenerationModel
	var model string
	switch {
	case w.keepsConfig(hs) && hs.setsGeneration:
		model, p.source = cfg.Generation.Model, fromConfig
		if flag != "" && flag != model {
			return p, fmt.Errorf("--generation-model %s: %s sets generation.model: %s, and curio up never rewrites "+
				"a value you set; to switch, edit generation.model there and run curio up again",
				flag, hs.home.ConfigPath(), model)
		}
		p.notes = append(p.notes, "keeping generation.model: "+model+" from config.yaml")
	case w.keepsConfig(hs):
		model, p.source = cfg.Generation.Model, fromDaemonDefault
		if flag != "" {
			return p, fmt.Errorf("--generation-model %s: %s sets no generation.model, and curio up never edits "+
				"an existing config.yaml; set generation.model: %s there and run curio up again",
				flag, hs.home.ConfigPath(), flag)
		}
		if best := p.advice.Generation.Name; best != model {
			p.notes = append(p.notes, fmt.Sprintf("config.yaml sets no generation.model, so the daemon writes with %s; "+
				"curio would pick %s for this Mac: set generation.model: %s in %s", model, best, best, hs.home.ConfigPath()))
		}
	case flag != "":
		model, p.source = flag, fromFlag
	case w.chosen != nil:
		model, p.source = w.chosen.Name, fromAdvisor
	default:
		model, p.source = p.advice.Generation.Name, fromAdvisor
	}
	p.generation = &wanted{knownModel(model), cfg.Generation.BaseURL}
	return p, nil
}

// generationToWrite is the writing model a new config.yaml records.
// There is no config.yaml to contradict a flag, so the pick can't fail.
func (w *world) generationToWrite(ctx context.Context, hs homeState) string {
	p, err := w.pick(ctx, hs)
	if err != nil || p.generation == nil {
		return w.defaults.Generation.Model
	}
	return p.generation.Name
}

// missing lists the picks Ollama doesn't have, by the exact name Ollama
// would run (ollama.Client.Ping). An Ollama that doesn't answer is an
// error: nothing is known missing or present.
func (w *world) missing(ctx context.Context, p picks) ([]wanted, error) {
	var out []wanted
	for _, m := range p.all() {
		c, err := w.ollamaClient(m.baseURL, m.Name)
		if err != nil {
			return nil, err
		}
		switch err := c.Ping(ctx); {
		case errors.Is(err, ollama.ErrModelNotLoaded):
			out = append(out, m)
		case err != nil:
			return nil, fmt.Errorf("the Ollama at %s: %w", m.baseURL, err)
		}
	}
	return out, nil
}

// checkModels is the models' check: the picks are pulled, and the disk
// takes the ones that aren't.
func (w *world) checkModels(ctx context.Context) Result {
	hs := w.readHome()
	p, err := w.pick(ctx, hs)
	if err != nil {
		return Result{Status: Fail, Detail: err.Error(), Notes: p.notes}
	}
	res := Result{Notes: p.notes}
	if p.curios() {
		res.Hint = "to write with another model, run curio up --generation-model <tag>"
	}
	missing, err := w.missing(ctx, p)
	switch {
	case err != nil:
		res.Status, res.Detail = Fail, "not checked: "+err.Error()
		res.Fix = &Fix{Summary: "pull " + sized(p.all()) + ", whichever Ollama lacks once it answers"}
	case len(missing) > 0:
		machine, _ := w.probeMachine(ctx) // the machine check reports a failed probe; its volumes are then unknown
		if short := diskShortage(machine, models(missing), hs.kind != homeOurs || w.freshPending()); short != "" {
			return Result{Status: Fail, Detail: "not enough disk space: " + short, Notes: p.notes,
				Hint: "free up space, or pick a smaller writing model with --generation-model"}
		}
		res.Status, res.Detail = Fail, "missing "+names(missing)
		res.Fix = &Fix{Summary: "pull " + sized(missing)}
	case p.curios() && !w.pickConfirmed:
		res.Status, res.Detail = Warn, p.String()+": present; curio picked them for this Mac"
		res.Fix = &Fix{Summary: "use " + p.String()}
	default:
		res.Status, res.Detail = OK, p.String()+": present"
	}
	return res
}

// modelsStep picks the models and pulls the missing ones, with progress.
type modelsStep struct{ w *world }

func (*modelsStep) Name() string { return "models" }

func (s *modelsStep) Check(ctx context.Context) Result { return s.w.checkModels(ctx) }

// Confirm shows the machine and curio's picks, each with its size and
// why, and the smaller alternative, and asks `Use these? [Y/n/choose]`;
// choose lists every tier. Models config.yaml or a flag names are only
// confirmed for the pull.
func (s *modelsStep) Confirm(ctx context.Context, ui UI, fix Fix) (bool, error) {
	w := s.w
	p, err := w.pick(ctx, w.readHome())
	if err != nil {
		return false, err
	}
	if !p.curios() {
		return ui.Confirm(ctx, capitalize(fix.Summary)+"?", true)
	}
	machine, _ := w.probeMachine(ctx) // a failed probe was reported by the machine check
	ui.Info("This Mac: " + machine.String())
	ui.Info(fmt.Sprintf("  for search:  %s (%s): %s", p.embedding.Name, size(p.embedding.Model), p.embedding.Reason))
	ui.Info(fmt.Sprintf("  for writing: %s (%s): %s", p.generation.Name, size(p.generation.Model), p.generation.Reason))
	if alt := p.advice.Smaller; alt != nil && p.generation.Name == p.advice.Generation.Name {
		ui.Info(fmt.Sprintf("  smaller:     %s (%s), the tier below", alt.Name, size(*alt)))
	}
	answer, err := ui.Ask(ctx, "Use these?", "yes", "no", "choose")
	switch {
	case err != nil:
		return false, err
	case answer == 1:
		return false, nil
	case answer == 2:
		if err := s.choose(ctx, ui, p); err != nil {
			return false, err
		}
	}
	w.pickConfirmed = true
	return true, nil
}

// choose lets the user pick the writing model from every tier.
func (s *modelsStep) choose(ctx context.Context, ui UI, p picks) error {
	tiers := s.w.advisor.Tiers()
	options := make([]string, len(tiers))
	def := 0
	for i, t := range tiers {
		options[i] = fmt.Sprintf("%s (%s): %s", t.Model.Name, size(t.Model), t.Model.Reason)
		if t.Model.Name == p.generation.Name {
			def = i
		}
	}
	i, err := ui.Select(ctx, "Which writing model?", options, def)
	if err != nil {
		return err
	}
	chosen := tiers[i].Model
	s.w.chosen = &chosen
	return nil
}

// Apply pulls the picks Ollama lacks, one progress line each, after the
// disk check: the user may have chosen other models since the plan.
func (s *modelsStep) Apply(ctx context.Context, ui UI) error {
	w := s.w
	hs := w.readHome()
	p, err := w.pick(ctx, hs)
	if err != nil {
		return err
	}
	missing, err := w.missing(ctx, p)
	if err != nil {
		return err
	}
	machine, _ := w.probeMachine(ctx) // a failed probe was reported by the machine check
	if short := diskShortage(machine, models(missing), hs.kind != homeOurs || w.freshPending()); short != "" {
		return errors.New("not enough disk space: " + short)
	}
	for _, m := range missing {
		if err := pull(ctx, ui, m); err != nil {
			return err
		}
	}
	return nil
}

// pull pulls one model with a progress line: the layers Ollama reports,
// each counted once, summed.
func pull(ctx context.Context, ui UI, m wanted) error {
	c, err := ollama.New(m.baseURL, m.Name, 0)
	if err != nil {
		return err
	}
	type layer struct{ completed, total int64 }
	layers := map[string]layer{}
	bar := ui.Progress("pulling " + m.Name)
	err = c.Pull(ctx, func(p ollama.PullProgress) {
		if p.Digest == "" || p.Total <= 0 {
			return
		}
		layers[p.Digest] = layer{p.Completed, p.Total}
		var completed, total int64
		for _, l := range layers {
			completed, total = completed+l.completed, total+l.total
		}
		bar.Update(completed, total)
	})
	if err != nil {
		bar.Stop()
		return fmt.Errorf("%w; `ollama pull %s` pulls it by hand", err, m.Name)
	}
	bar.Done()
	return nil
}

// models are the wanted models' Models.
func models(ws []wanted) []Model {
	out := make([]Model, len(ws))
	for i, m := range ws {
		out[i] = m.Model
	}
	return out
}

// names lists models by name: "qwen3-embedding:0.6b and gemma4:26b".
func names(ws []wanted) string {
	out := make([]string, len(ws))
	for i, m := range ws {
		out[i] = m.Name
	}
	return strings.Join(out, " and ")
}

// sized lists models with their sizes: "gemma4:26b (19 GB)".
func sized(ws []wanted) string {
	out := make([]string, len(ws))
	for i, m := range ws {
		out[i] = fmt.Sprintf("%s (%s)", m.Name, size(m.Model))
	}
	return strings.Join(out, " and ")
}

// size is a model's download size, or that it is unknown.
func size(m Model) string {
	if m.Size == 0 {
		return "size unknown"
	}
	return FormatSize(m.Size)
}
