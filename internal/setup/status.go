package setup

import (
	"context"
	"errors"
	"fmt"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/ollama"
)

// Snapshot is curio's state after a run, or when there was nothing to do:
// what `curio up` shows last. Each part is read on its own, bounded by
// the probe timeout, and one that can't be read carries its error instead
// of failing the run. Nothing is started to read it.
type Snapshot struct {
	Daemon DaemonSnapshot
	Ollama OllamaSnapshot
	// Documents counts the library's documents; DocumentsErr is why it
	// couldn't.
	Documents    int
	DocumentsErr error
	Queue        *client.Queue
	QueueErr     error
	// Drift is what the daemon reports changed in the build that makes
	// the embeddings; nil when nothing did.
	Drift *client.EmbeddingDrift
	// Warnings are the checks' warnings without a fix.
	Warnings []Warning
}

// Warning is a check's warning: "step: detail", and the notes under it.
type Warning struct {
	Text  string
	Notes []string
}

// DaemonSnapshot is the daemon serving the home.
type DaemonSnapshot struct {
	PID     int
	Version string
	// Managed: its launchd agent runs it (daemonctl.Status.Managed).
	Managed bool
	Err     error
}

// OllamaSnapshot is the Ollama at embedding.base_url and curio's models in
// it.
type OllamaSnapshot struct {
	Version string
	Models  []ModelSnapshot
	Err     error
}

// ModelSnapshot is one of curio's models in Ollama.
type ModelSnapshot struct {
	Name    string
	Present bool
	Err     error
}

// errNoHome is a snapshot of a run that ended without a home to read.
var errNoHome = errors.New("there is no curio home")

// snapshot reads curio's state, with plan's warnings.
func (w *world) snapshot(ctx context.Context, plan Plan) Snapshot {
	var s Snapshot
	for _, it := range plan.Warnings() {
		s.Warnings = append(s.Warnings, Warning{Text: it.Step + ": " + it.Result.Detail, Notes: it.Result.Notes})
	}
	hs := w.readHome()
	s.Ollama = w.ollamaSnapshot(ctx, hs)
	if hs.kind != homeOurs {
		s.Daemon.Err, s.DocumentsErr, s.QueueErr = errNoHome, errNoHome, errNoHome
		return s
	}
	env, err := w.connect(hs)
	if err != nil {
		s.Daemon.Err, s.DocumentsErr, s.QueueErr = err, err, err
		return s
	}
	health := w.daemonSnapshot(ctx, env, &s.Daemon)
	if health != nil {
		s.Drift = health.EmbeddingDrift
	}
	rctx, cancel := context.WithTimeout(ctx, w.times.Probe)
	defer cancel()
	if stats, err := env.Client.Stats(rctx); err != nil {
		s.DocumentsErr = err
	} else {
		s.Documents = stats.DocumentsTotal
	}
	qctx, cancel := context.WithTimeout(ctx, w.times.Probe)
	defer cancel()
	s.Queue, s.QueueErr = env.Client.Queue(qctx)
	return s
}

// daemonSnapshot fills d from the daemon's status, and returns what its
// healthz said, if it serves.
func (w *world) daemonSnapshot(ctx context.Context, env daemonctl.Env, d *DaemonSnapshot) *client.Health {
	ctx, cancel := context.WithTimeout(ctx, w.times.Probe)
	defer cancel()
	st, err := env.Controller.Status(ctx)
	switch {
	case err != nil:
		d.Err = err
		return nil
	case st.Health != nil:
		d.PID, d.Version, d.Managed = st.PID, st.Health.Version, st.Managed()
		return st.Health
	case st.Startup != nil:
		d.PID, d.Version, d.Managed = st.PID, st.Startup.Version, st.Managed()
		d.Err = fmt.Errorf("starting: %s", st.Startup.Progress())
	default:
		d.Err = errors.New("not running")
	}
	return nil
}

// ollamaSnapshot reads Ollama's version and whether curio's models are
// in it.
func (w *world) ollamaSnapshot(ctx context.Context, hs homeState) OllamaSnapshot {
	var o OllamaSnapshot
	base := w.baseConfig(hs).Embedding.BaseURL
	c, err := w.ollamaClient(base, w.newEmbeddingModel())
	if err != nil {
		o.Err = err
		return o
	}
	if o.Version, err = c.Version(ctx); err != nil {
		o.Err = err
		return o
	}
	p, err := w.pick(ctx, hs)
	if err != nil {
		o.Err = err
		return o
	}
	for _, m := range p.all() {
		ms := ModelSnapshot{Name: m.Name}
		c, err := w.ollamaClient(m.baseURL, m.Name)
		if err == nil {
			err = c.Ping(ctx)
		}
		switch {
		case err == nil:
			ms.Present = true
		case !errors.Is(err, ollama.ErrModelNotLoaded):
			ms.Err = err
		}
		o.Models = append(o.Models, ms)
	}
	return o
}
