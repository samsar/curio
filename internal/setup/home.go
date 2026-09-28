package setup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/embedder"
)

// homeStep makes the home and its config.yaml: a new home where there is
// none (moving one aside first with --fresh), and a config.yaml where it
// has none. It never converts or edits a home or a config.yaml.
type homeStep struct{ w *world }

func (*homeStep) Name() string { return "home" }

func (s *homeStep) Check(ctx context.Context) Result {
	return combine(s.w.checkHome(ctx), s.w.checkConfig(ctx))
}

// checkHome is the check of the home itself: there, ours, and one the
// daemon would serve; and for a new home, room for it.
func (w *world) checkHome(ctx context.Context) Result {
	hs := w.readHome()
	if hs.err != nil {
		return Result{Status: Fail, Detail: hs.err.Error(), Hint: "fix the permissions, or pass another --curio-home"}
	}
	switch hs.kind {
	case homeNotDir:
		return Result{Status: Fail, Detail: hs.path + " is a file, not a curio home",
			Hint: "pass another --curio-home (or set CURIO_HOME), or move the file"}
	case homeNotOurs:
		return Result{Status: Fail,
			Detail: fmt.Sprintf("%s has files and no %s: it isn't a curio home, and curio up never writes into it or moves it",
				hs.path, curiohome.MarkerFile),
			Hint: "pass another --curio-home (or set CURIO_HOME), or move that directory yourself"}
	case homeMissing, homeEmpty:
		if res, short := w.noRoomForHome(ctx); short {
			return res
		}
		return Result{Status: Fail, Detail: "no curio home at " + hs.path,
			Fix: &Fix{Summary: "create a curio home at " + hs.path + " (" + w.newEmbedding() + ")", Consent: Announce}}
	case homeOurs:
	}
	res := w.checkOurs(hs)
	if w.freshPending() {
		if res, short := w.noRoomForHome(ctx); short {
			return res
		}
		res.Status = max(res.Status, Warn)
		res.Fix = &Fix{Summary: fmt.Sprintf("move %s aside to %s, and create a new home (%s)",
			hs.path, w.backupName(hs), w.newEmbedding()), Consent: AskNo}
		res.Hint = "nothing is deleted: the old home stays whole at its new name"
	}
	return res
}

// noRoomForHome is the blocker for a new home whose volume the probe
// measured with less than it needs; short is false when there is room, or
// the volume is unknown.
func (w *world) noRoomForHome(ctx context.Context) (res Result, short bool) {
	m, err := w.probeMachine(ctx) // a failed probe is the machine check's to report
	if err != nil {
		return Result{}, false
	}
	why := newHomeShortage(m)
	if why == "" {
		return Result{}, false
	}
	return Result{Status: Fail, Detail: "not enough disk space for a new home: " + why,
		Hint: "free up space, or pass another --curio-home"}, true
}

// checkOurs judges a curio home that stays.
func (w *world) checkOurs(hs homeState) Result {
	if hs.metaErr != nil {
		return Result{Status: Fail, Detail: fmt.Sprintf("%s: its marker is unreadable: %v", hs.path, hs.metaErr),
			Hint: "check the permissions of " + hs.home.MarkerPath() + ", or `curio up --fresh` to set the home aside"}
	}
	if w.opts.EmbeddingModel != "" && w.opts.EmbeddingModel != hs.meta.EmbeddingModel {
		return Result{Status: Fail,
			Detail: fmt.Sprintf("%s embeds with %s, for good; --embedding-model %s needs a new home",
				hs.path, hs.meta.EmbeddingModel, w.opts.EmbeddingModel),
			Hint: fmt.Sprintf("`curio up --fresh --embedding-model %s` sets this home aside and starts a new one; "+
				"then import your bookmarks again", w.opts.EmbeddingModel)}
	}
	if hs.configErr != nil {
		// The config check says why; the home can't be judged without it.
		return Result{Status: OK, Detail: hs.path + describeMeta(hs.meta)}
	}
	model, dim := w.embeddingSettings(hs)
	if _, err := hs.home.CheckEmbedding(model, dim); err != nil {
		hint := "`curio up --fresh` sets this home aside and starts a new one"
		if errors.Is(err, curiohome.ErrNewerHome) {
			hint = "upgrade curio first (`brew upgrade curio`)"
		}
		return Result{Status: Fail, Detail: err.Error(), Hint: hint}
	}
	return Result{Status: OK, Detail: hs.path + describeMeta(hs.meta)}
}

// describeMeta is a marker in a few words, after the home's path.
func describeMeta(m curiohome.Meta) string {
	return fmt.Sprintf(" (format %d, schema v%d, %s at %d dimensions)", m.Format, m.SchemaVersion, m.EmbeddingModel,
		m.EmbeddingDim)
}

// newEmbedding says what a new home will embed with: the width a known
// model has, or that it is measured after the pull.
func (w *world) newEmbedding() string {
	model := w.newEmbeddingModel()
	if model == w.defaults.Embedding.Model {
		return fmt.Sprintf("%s, %d dimensions", model, w.defaults.Embedding.Dim)
	}
	return model + ", its width measured after the pull"
}

// checkConfig is the check of config.yaml: it loads, or curio up writes
// it where there is none.
func (w *world) checkConfig(ctx context.Context) Result {
	hs := w.readHome()
	switch {
	case hs.kind != homeOurs || w.freshPending():
		return Result{Status: Warn, Detail: "none yet: curio up writes one with the new home",
			Fix: &Fix{Summary: "write config.yaml with the new home", Consent: Announce}}
	case !hs.configExists:
		gen := w.generationToWrite(ctx, hs)
		return Result{Status: Warn, Detail: "no " + hs.home.ConfigPath() + ": the daemon runs on the defaults",
			Fix: &Fix{Summary: "write " + hs.home.ConfigPath() + " (writing model " + gen + ")", Consent: Announce}}
	case hs.configErr != nil:
		return Result{Status: Fail, Detail: "doesn't load: " + hs.configErr.Error(),
			Hint: "edit " + hs.home.ConfigPath() + " (or `curio up --fresh` sets the whole home aside)"}
	}
	c := hs.config
	return Result{Status: OK, Detail: fmt.Sprintf("fetch_workers=%d, index_workers=%d, fetcher=%s, writing model %s",
		c.Daemon.FetchWorkers, c.Daemon.IndexWorkers, c.Fetcher.Default, c.Generation.Model)}
}

// Apply moves the home aside for --fresh, creates a new one where there
// is none, and writes a config.yaml where it has none, reading the home
// again before each: a home another command created meanwhile is judged
// as it is, never overwritten.
func (s *homeStep) Apply(ctx context.Context, ui UI) error {
	w := s.w
	hs := w.readHome()
	if w.freshPending() && hs.kind == homeOurs {
		// Measured before the move and created inside it, while no daemon
		// can start: an auto-starter waiting on the start lock would
		// otherwise spawn a daemon that makes a default home at the path
		// before this one does.
		model, width, err := w.measureNewHome(ctx, ui, hs)
		if err != nil {
			return err
		}
		dest, err := w.moveAside(ctx, hs, func(dir string) error {
			return initHome(ui, hs, dir, model, width)
		})
		if err != nil {
			return err
		}
		ui.Info(fmt.Sprintf("moved %s aside to %s; nothing in it was changed", hs.path, dest))
		hs = w.readHome()
	}
	w.moved = true
	if hs.kind == homeMissing || hs.kind == homeEmpty {
		if err := w.createHome(ctx, ui, hs); err != nil {
			return err
		}
		hs = w.readHome()
	}
	if hs.kind == homeOurs && hs.metaErr == nil && !hs.configExists {
		return w.writeConfig(ctx, ui, hs)
	}
	return nil
}

// createHome makes a new home at hs.path, recording the embedding model
// and the width it measures.
func (w *world) createHome(ctx context.Context, ui UI, hs homeState) error {
	model, width, err := w.measureNewHome(ctx, ui, hs)
	if err != nil {
		return err
	}
	dir, err := resolveHome(hs.path)
	if err != nil {
		return err
	}
	return initHome(ui, hs, dir, model, width)
}

// measureNewHome is the embedding model a new home records and the width
// it measures: the model decides its width, and a new home records what
// the daemon will receive.
func (w *world) measureNewHome(ctx context.Context, ui UI, hs homeState) (model string, width int, err error) {
	model = w.newEmbeddingModel()
	cfg := w.baseConfig(hs)
	width, err = embedder.MeasureWidth(ctx, embedder.OllamaOptions{
		BaseURL: cfg.Embedding.BaseURL,
		Model:   model,
		Timeout: time.Duration(cfg.Embedding.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		return "", 0, err
	}
	if model == w.defaults.Embedding.Model && width != w.defaults.Embedding.Dim {
		ui.Warn(fmt.Sprintf("%s returned %d-dimensional vectors, not the %d curio expects; the new home records %d",
			model, width, w.defaults.Embedding.Dim, width))
	}
	return model, width, nil
}

// initHome creates the home at dir, hs.path resolved, for model at width.
// A home another command created there meanwhile is left as it is.
func initHome(ui UI, hs homeState, dir, model string, width int) error {
	_, err := curiohome.Init(dir, model, width)
	switch {
	case errors.Is(err, curiohome.ErrAlreadyInitialized):
		ui.Warn(hs.path + " became a curio home meanwhile; checking it as it is")
		return nil
	case err != nil:
		return err
	}
	ui.Info(fmt.Sprintf("created a curio home at %s (%s, %d dimensions)", hs.path, model, width))
	return nil
}

// newEmbeddingModel is the model a new home embeds with: --embedding-model,
// or the default.
func (w *world) newEmbeddingModel() string {
	if w.opts.EmbeddingModel != "" {
		return w.opts.EmbeddingModel
	}
	return w.defaults.Embedding.Model
}

// combine is a step's result from its parts' checks: the first that fails
// or has a fix, then the first warning, then the first.
func combine(results ...Result) Result {
	for _, r := range results {
		if r.Status == Fail || r.Pending() {
			return r
		}
	}
	for _, r := range results {
		if r.Status == Warn {
			return r
		}
	}
	return results[0]
}
