package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/importer"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/version"
)

// Options are `curio up`'s flags.
type Options struct {
	// Home is --curio-home; empty means $CURIO_HOME, then ~/.curio.
	Home string
	// DaemonURL is --daemon-url; empty means config.yaml's daemon.listen.
	DaemonURL string
	// Yes answers every question with a yes or its default.
	Yes bool
	// NoInstall runs no Installer command: what would be installed or
	// started is left to do by hand.
	NoInstall bool
	// DryRun shows the plan and changes nothing.
	DryRun bool
	// Fresh moves an existing home aside and starts a new one.
	Fresh bool
	// EmbeddingModel and GenerationModel override the models curio would
	// pick.
	EmbeddingModel, GenerationModel string
	// Import is --import, the source to import from without a menu: chrome
	// (the Default profile), chrome:<profile>, safari, firefox or
	// html:<path>.
	Import string
}

// Deps are what the steps work through. The CLI builds the real ones;
// tests pass fakes (internal/setup/setuptest, servicetest).
type Deps struct {
	UI        UI
	Probe     Probe
	Installer Installer
	// Connect builds the daemon's environment for a home that exists
	// (daemonctl.Connect, or a test's with a fake service manager).
	Connect daemonctl.ConnectFunc
	// DaemonBin locates curio-daemon; nil means daemonctl.DaemonBinary.
	DaemonBin func() (string, error)
	// Defaults is the config a new home's config.yaml starts from, and the
	// one a home without config.yaml runs with; nil means
	// config.Default(). Tests point its addresses at fakes.
	Defaults *config.Config
	// Version is this curio's, which the daemon must run; empty means
	// version.String().
	Version string
	// Sources finds the bookmarks the import step offers; nil means
	// importer.Discover. Tests pass their own, so no test reads the
	// browsers of the machine it runs on.
	Sources func() []importer.Source
	// Now is the clock the --fresh backup's name, the import's estimates
	// and their measurement are taken from; nil means time.Now.
	Now func() time.Time
	// Timeouts bound the probes and waits; zero fields take the defaults.
	Timeouts Timeouts
}

// Timeouts bound what setup waits on.
type Timeouts struct {
	// Probe bounds one read: Ollama's version or models, healthz, a status
	// read. Default 2s.
	Probe time.Duration
	// Start is how long Ollama may take to answer after curio started or
	// installed it. Default 60s.
	Start time.Duration
	// Download is how long curio waits for Ollama to answer after it
	// opened the download page, for the user to install and open it.
	// Default 10 minutes.
	Download time.Duration
	// Poll paces the checks while waiting for Ollama. Default 2s.
	Poll time.Duration
}

func (t Timeouts) withDefaults() Timeouts {
	if t.Probe == 0 {
		t.Probe = 2 * time.Second
	}
	if t.Start == 0 {
		t.Start = time.Minute
	}
	if t.Download == 0 {
		t.Download = 10 * time.Minute
	}
	if t.Poll == 0 {
		t.Poll = 2 * time.Second
	}
	return t
}

// world is what the steps check and change, and what they decided along
// the way. The runner owns it; its checks read the filesystem and the
// services again each time, so they see what earlier steps did.
type world struct {
	opts     Options
	deps     Deps
	defaults config.Config
	version  string
	times    Timeouts
	advisor  ModelAdvisor
	// homePath is where the home is: --curio-home made absolute, or
	// $CURIO_HOME, or ~/.curio.
	homePath string

	machineOnce sync.Once
	machine     Machine
	machineErr  error

	// moved: --fresh's move is done, or there was nothing to move.
	moved bool
	// freshAt is the time the --fresh backup is named after, fixed at
	// its first use so the name the confirmation shows is the one used.
	freshAt time.Time
	// chosen is the writing model the user chose over curio's pick.
	chosen *Model
	// pickConfirmed: the user agreed to the models curio picked.
	pickConfirmed bool
	// imp is what the import step found and decided.
	imp importState
}

func newWorld(opts Options, deps Deps) (*world, error) {
	path, err := daemonctl.HomePath(opts.Home)
	if err != nil {
		return nil, err
	}
	spec, err := parseImportSpec(opts.Import)
	if err != nil {
		return nil, err
	}
	w := &world{opts: opts, deps: deps, homePath: path, times: deps.Timeouts.withDefaults(),
		defaults: config.Default(), version: deps.Version}
	if deps.Defaults != nil {
		w.defaults = *deps.Defaults
	}
	if w.version == "" {
		w.version = version.String()
	}
	if w.deps.DaemonBin == nil {
		w.deps.DaemonBin = daemonctl.DaemonBinary
	}
	if w.deps.Now == nil {
		w.deps.Now = time.Now
	}
	if w.deps.Sources == nil {
		w.deps.Sources = importer.Discover
	}
	w.imp = newImportState(spec)
	return w, nil
}

// freshPending reports whether --fresh has a home still to move aside.
func (w *world) freshPending() bool { return w.opts.Fresh && !w.moved }

// probeMachine probes the machine once per run.
func (w *world) probeMachine(ctx context.Context) (Machine, error) {
	w.machineOnce.Do(func() {
		w.machine, w.machineErr = w.deps.Probe.Machine(ctx, w.homePath, ModelsDir())
	})
	return w.machine, w.machineErr
}

// homeKind is what is at the home's path.
type homeKind int

const (
	homeMissing homeKind = iota // nothing
	homeEmpty                   // an empty directory, which becomes the home
	homeNotDir                  // a file
	homeNotOurs                 // a directory with something in it and no marker
	homeOurs                    // a curio home
)

// homeState is the home as it is on disk now.
type homeState struct {
	path string
	kind homeKind
	// err is a failure to look, which makes the kind unknowable.
	err  error
	home *curiohome.Home // for homeOurs
	meta curiohome.Meta
	// metaErr is an unreadable marker.
	metaErr error
	// configExists: config.yaml is there; config is it loaded, or
	// configErr why it doesn't load.
	configExists bool
	config       config.Config
	configErr    error
	// setsGeneration: config.yaml sets generation.model itself.
	setsGeneration bool
}

// readHome looks at the home, changing nothing.
func (w *world) readHome() homeState {
	hs := homeState{path: w.homePath}
	info, err := os.Stat(hs.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		hs.kind = homeMissing
		return hs
	case err != nil:
		hs.err = fmt.Errorf("look at %s: %w", hs.path, err)
		return hs
	case !info.IsDir():
		hs.kind = homeNotDir
		return hs
	}
	home, err := curiohome.Open(hs.path)
	switch {
	case errors.Is(err, curiohome.ErrNotOurs):
		entries, rerr := os.ReadDir(hs.path)
		switch {
		case rerr != nil:
			hs.err = fmt.Errorf("look in %s: %w", hs.path, rerr)
		case len(entries) == 0:
			hs.kind = homeEmpty
		default:
			hs.kind = homeNotOurs
		}
		return hs
	case err != nil:
		hs.err = err
		return hs
	}
	hs.kind, hs.home = homeOurs, home
	hs.meta, hs.metaErr = home.Meta()
	data, err := os.ReadFile(home.ConfigPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return hs
	case err != nil:
		hs.configExists, hs.configErr = true, fmt.Errorf("read config %q: %w", home.ConfigPath(), err)
		return hs
	}
	hs.configExists = true
	hs.config, hs.configErr = config.Load(home.ConfigPath())
	hs.setsGeneration = setsGenerationModel(data)
	return hs
}

// keepsConfig reports whether the home stays and has a config.yaml that
// loads: what it says is what the daemon runs with, and curio up never
// edits it.
func (w *world) keepsConfig(hs homeState) bool {
	return hs.kind == homeOurs && !w.freshPending() && hs.configExists && hs.configErr == nil
}

// baseConfig is the config the home runs with: its config.yaml when it
// keeps one, and otherwise the defaults a new config.yaml starts from,
// daemon.listen following --daemon-url.
func (w *world) baseConfig(hs homeState) config.Config {
	if w.keepsConfig(hs) {
		return hs.config
	}
	cfg := w.defaults
	if listen := listenOf(w.opts.DaemonURL); listen != "" {
		cfg.Daemon.Listen = listen
	}
	return cfg
}

// embeddingSettings are the model and width the home is held to: its
// config.yaml's, or the defaults' when it has none.
func (w *world) embeddingSettings(hs homeState) (string, int) {
	if hs.configExists && hs.configErr == nil {
		return hs.config.Embedding.Model, hs.config.Embedding.Dim
	}
	return w.defaults.Embedding.Model, w.defaults.Embedding.Dim
}

// refusal is why the daemon would refuse a home as it stands, and the
// remedy that fits the reason.
type refusal struct {
	// why is empty when the daemon would serve the home.
	why  string
	hint string
}

// freshRemedy is the remedy for a home only a new one replaces.
const freshRemedy = "`curio up --fresh` sets the home aside and starts a new one"

// refusal says why the daemon would refuse the home as it stands, and what
// to do: nothing when it wouldn't, or when --fresh will replace it. A home
// without a config.yaml isn't refused: curio up writes one.
func (w *world) refusal(hs homeState) refusal {
	if hs.kind != homeOurs || w.freshPending() {
		return refusal{}
	}
	switch {
	case hs.metaErr != nil:
		return refusal{why: "its marker is unreadable: " + hs.metaErr.Error(),
			hint: "check the permissions of " + hs.home.MarkerPath() + ", or " + freshRemedy}
	case hs.configErr != nil:
		return refusal{why: "its config.yaml doesn't load: " + hs.configErr.Error(),
			hint: "edit " + hs.home.ConfigPath() + ", or " + freshRemedy}
	case w.opts.EmbeddingModel != "" && w.opts.EmbeddingModel != hs.meta.EmbeddingModel:
		return refusal{
			why: fmt.Sprintf("it embeds with %s, and --embedding-model asks for %s", hs.meta.EmbeddingModel,
				w.opts.EmbeddingModel),
			hint: fmt.Sprintf("`curio up --fresh --embedding-model %s` sets the home aside and starts a new one",
				w.opts.EmbeddingModel)}
	}
	model, dim := w.embeddingSettings(hs)
	_, err := hs.home.CheckEmbedding(model, dim)
	var mismatch *curiohome.EmbeddingMismatchError
	switch {
	case err == nil:
		return refusal{}
	case errors.Is(err, curiohome.ErrNewerHome):
		return refusal{why: err.Error(), hint: "upgrade curio (`brew upgrade curio`)"}
	case errors.As(err, &mismatch):
		return refusal{why: err.Error(), hint: "set config.yaml back (embedding.model and embedding.dim as the marker " +
			"records them), or " + freshRemedy}
	default:
		return refusal{why: err.Error(), hint: freshRemedy}
	}
}

// formatRefused is ErrLegacyHome or ErrNewerHome for a home that stays
// whose marker records a format the daemon refuses, whatever config.yaml
// says; nil otherwise. Nothing about such a home, its embedding model
// included, is this curio's to act on.
func (w *world) formatRefused(hs homeState) error {
	// An unreadable marker records no format; the home check reports it.
	stays := hs.kind == homeOurs && !w.freshPending() && hs.metaErr == nil
	switch {
	case !stays:
		return nil
	case hs.meta.Format < curiohome.CurrentFormat:
		return curiohome.ErrLegacyHome
	case hs.meta.Format > curiohome.CurrentFormat:
		return curiohome.ErrNewerHome
	}
	return nil
}

// unusable says why the home's path can hold no home curio up makes, for
// the checks that can't be made without one; "" when it can.
func (hs homeState) unusable() string {
	switch {
	case hs.err != nil:
		return "the home can't be looked at"
	case hs.kind == homeNotDir:
		return hs.path + " is a file"
	case hs.kind == homeNotOurs:
		return hs.path + " isn't a curio home"
	}
	return ""
}

// homeReady reports whether the home is there, stays, and the daemon
// would serve it: with its config.yaml, or on the defaults without one.
func (w *world) homeReady(hs homeState) bool {
	return hs.kind == homeOurs && !w.freshPending() && w.refusal(hs).why == ""
}

// connect builds the daemon's environment for an existing home, with its
// config, or the defaults when it has none that loads.
func (w *world) connect(hs homeState) (daemonctl.Env, error) {
	cfg := w.defaults
	if hs.configExists && hs.configErr == nil {
		cfg = hs.config
	}
	return w.deps.Connect(hs.home, cfg, w.opts.DaemonURL)
}

// ollamaClient is a client for model at baseURL whose requests give up
// after the probe timeout.
func (w *world) ollamaClient(baseURL, model string) (*ollama.Client, error) {
	return ollama.New(baseURL, model, w.times.Probe)
}

// bounded is read(ctx), given up after timeout: every read a check makes
// is bounded, so a daemon or a service that hangs can't hang the plan.
func bounded[T any](ctx context.Context, timeout time.Duration, read func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return read(ctx)
}

// setsGenerationModel reports whether a config.yaml sets
// generation.model itself, rather than leaving it to the default.
func setsGenerationModel(data []byte) bool {
	var set struct {
		Generation struct {
			Model *string `yaml:"model"`
		} `yaml:"generation"`
	}
	// Load already accepted this document; a failure here reads as unset.
	if yaml.Unmarshal(data, &set) != nil {
		return false
	}
	return set.Generation.Model != nil
}

// resolveHome is the directory path names, symlinks followed, whether or
// not it exists: a symlinked home is moved aside, and made again, at its
// target, so the link keeps working.
func resolveHome(path string) (string, error) {
	const maxLinks = 40
	for range maxLinks {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return path, nil
		}
		if err != nil {
			return "", fmt.Errorf("look at %s: %w", path, err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return path, nil
		}
		link, err := os.Readlink(path)
		if err != nil {
			return "", fmt.Errorf("read the symlink %s: %w", path, err)
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(path), link)
		}
		path = link
	}
	return "", fmt.Errorf("%s: more than %d symlinks", path, maxLinks)
}
