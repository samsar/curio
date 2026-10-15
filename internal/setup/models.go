package setup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/understand"
)

// pickSource is where generation.model's pick came from.
type pickSource int

const (
	noWritingModel    pickSource = iota // no role in use writes with generation.model
	fromAdvisor                         // curio picked it for this Mac, or the user chose from its tiers
	fromFlag                            // --generation-model
	fromConfig                          // config.yaml's generation.model
	fromDaemonDefault                   // config.yaml sets none, so the daemon's default
)

// wanted is a model and the Ollama that serves it. An optional one is
// wanted only by roles that don't pull alone (config.Role.PullsAlone):
// search understanding, whose searches use their words without it, so
// curio up offers it and a no leaves it out.
type wanted struct {
	Model
	baseURL  string
	optional bool
}

// picks are the models the home needs: the embedding model always, and
// the writing model of each role in use.
type picks struct {
	embedding wanted
	// generation is generation.model's pick, which a role without a model
	// of its own writes with; nil when no role in use does.
	generation *wanted
	// writing are the roles' writing models, one per model and server.
	writing []wanted
	source  pickSource
	advice  Advice // curio's pick for this Mac
	notes   []string
}

// curios reports whether curio picked the writing model, which the user
// is asked about.
func (p picks) curios() bool { return p.source == fromAdvisor }

// all is every model picked, each once.
func (p picks) all() []wanted {
	out := []wanted{p.embedding}
	for _, m := range p.writing {
		if !slices.ContainsFunc(out, m.same) {
			out = append(out, m)
		}
	}
	return out
}

// same reports whether o is the same model on the same server as m.
func (m wanted) same(o wanted) bool {
	return m.Name == o.Name && strings.TrimRight(m.baseURL, "/") == strings.TrimRight(o.baseURL, "/")
}

// String names the picks: "qwen3-embedding:0.6b for search and gemma4:26b
// for writing".
func (p picks) String() string {
	s := p.embedding.Name + " for search"
	if len(p.writing) > 0 {
		s += " and " + names(p.writing) + " for writing"
	}
	return s
}

// pick decides the models, reading the home as it is: the embedding model
// is the home's, or a new home's (--embedding-model, or the default). A
// role in use whose model config.yaml's generation.roles sets writes with
// that one; the others with generation.model, which is config.yaml's,
// kept whatever the flags say, or the flag, or curio's pick for this Mac.
// A --generation-model config.yaml contradicts is an error: curio up never
// rewrites a value the user set.
func (w *world) pick(ctx context.Context, hs homeState) (picks, error) {
	cfg := w.baseConfig(hs)
	machine, err := w.probeMachine(ctx)
	p := picks{advice: w.advisor.Advise(machine.Memory)}
	if err != nil {
		p.notes = append(p.notes, "the Mac couldn't be probed, so curio picks for the smallest tier")
	}
	embedding := w.newEmbeddingModel()
	if hs.kind == homeOurs && !w.freshPending() && hs.metaErr == nil && w.formatRefused(hs) == nil {
		embedding = hs.meta.EmbeddingModel
	}
	p.embedding = wanted{Model: knownModel(embedding), baseURL: cfg.Embedding.BaseURL}

	roles := cfg.RolesInUse()
	if len(roles) == 0 {
		p.notes = append(p.notes, "no writing model is pulled: no role writes with one "+
			"(interest labels are off with insight.enabled false or insight.labeling not llm, "+
			"search understanding with search.understand false)")
		return p, nil
	}
	if withheld := understand.OptionsFrom(cfg).Withheld; withheld != "" && slices.Contains(roles, config.RolePlanner) {
		// Its model would write nothing: no question goes to it.
		roles = slices.DeleteFunc(roles, func(r config.Role) bool { return r == config.RolePlanner })
		p.notes = append(p.notes, "search understanding's model isn't pulled: "+withheld)
		if len(roles) == 0 {
			return p, nil
		}
	}
	var inheriting []config.Role
	for _, r := range roles {
		own := cfg.Generation.Roles[r].Model
		if own == "" {
			inheriting = append(inheriting, r)
			continue
		}
		p.addWriting(wanted{knownModel(own), cfg.Generation.Role(r).BaseURL, !r.PullsAlone()})
		p.notes = append(p.notes, fmt.Sprintf("keeping generation.roles.%s.model: %s from config.yaml", r, own))
	}
	if len(inheriting) == 0 {
		if flag := w.opts.GenerationModel; flag != "" {
			return p, fmt.Errorf("--generation-model %s: %s gives every writing role its own model in generation.roles, "+
				"so generation.model writes nothing; to switch, edit generation.roles there and run curio up again",
				flag, hs.home.ConfigPath())
		}
		return p, nil
	}
	model, err := w.pickGeneration(cfg, hs, &p)
	if err != nil {
		return p, err
	}
	p.generation = &wanted{Model: knownModel(model), baseURL: cfg.Generation.BaseURL}
	for _, r := range inheriting {
		p.addWriting(wanted{p.generation.Model, cfg.Generation.Role(r).BaseURL, !r.PullsAlone()})
	}
	return p, nil
}

// addWriting adds m to the writing models unless it is there already,
// then required if either wants it so.
func (p *picks) addWriting(m wanted) {
	if i := slices.IndexFunc(p.writing, m.same); i >= 0 {
		p.writing[i].optional = p.writing[i].optional && m.optional
		return
	}
	p.writing = append(p.writing, m)
}

// pickGeneration picks generation.model, for the roles that write with
// it: config.yaml's, kept whatever the flags say, or the flag, or curio's
// pick for this Mac. It records where the pick came from, and its notes,
// in p.
func (w *world) pickGeneration(cfg config.Config, hs homeState, p *picks) (string, error) {
	flag := w.opts.GenerationModel
	var model string
	switch {
	case w.keepsConfig(hs) && hs.setsGeneration:
		model, p.source = cfg.Generation.Model, fromConfig
		if flag != "" && flag != model {
			return "", fmt.Errorf("--generation-model %s: %s sets generation.model: %s, and curio up never rewrites "+
				"a value you set; to switch, edit generation.model there and run curio up again",
				flag, hs.home.ConfigPath(), model)
		}
		p.notes = append(p.notes, "keeping generation.model: "+model+" from config.yaml")
	case w.keepsConfig(hs):
		model, p.source = cfg.Generation.Model, fromDaemonDefault
		if flag != "" {
			return "", fmt.Errorf("--generation-model %s: %s sets no generation.model, and curio up never edits "+
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
	return model, nil
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
	if err := w.formatRefused(hs); err != nil {
		return Result{Status: Warn, Detail: "not checked: " + err.Error() + " (see the curio home check)"}
	}
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
	case len(missing) > 0 && !slices.ContainsFunc(missing, required):
		return w.offerOptional(ctx, missing, res)
	case len(missing) > 0:
		machine, _ := w.probeMachine(ctx) // the machine check reports a failed probe; its volumes are then unknown
		if short := diskShortage(machine, models(missing)); short != "" {
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

// required reports whether m is a model curio needs, not one it offers.
func required(m wanted) bool { return !m.optional }

// offerOptional is the models' check when only optional models are
// missing, those only search understanding writes with: a warning, never
// a failure, since searches use their words without them, with a pull
// offered that a no skips (Offer), and nothing offered when the disk
// can't take them.
func (w *world) offerOptional(ctx context.Context, missing []wanted, res Result) Result {
	without := fmt.Sprintf("%s isn't pulled, and only search understanding writes with it: searches use their "+
		"words until it is", names(missing))
	machine, _ := w.probeMachine(ctx) // the machine check reports a failed probe; its volumes are then unknown
	if short := diskShortage(machine, models(missing)); short != "" {
		res.Status, res.Detail = Warn, without
		res.Hint = "not enough disk space to pull it: " + short + "; free up space and run curio up again"
		return res
	}
	res.Status, res.Detail = Warn, without
	res.Fix = &Fix{Summary: "pull " + sized(missing) + " for search understanding", Consent: Offer,
		Declined: fmt.Sprintf("not pulling %s: searches use their words; run curio up again to pull it, or set "+
			"search.understand: false to stop understanding questions", names(missing))}
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
	if short := diskShortage(machine, models(missing)); short != "" {
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
	bar := ui.Progress("pulling "+m.Name, Bytes)
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
