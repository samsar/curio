package setup_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
)

// TestUp_FirstRun: on a Mac with Ollama and nothing else, curio up pulls
// both models, creates the home at the width it measures with its
// config.yaml, installs the agent and has it run the daemon; the status
// says so.
func TestUp_FirstRun(t *testing.T) {
	h := newHarness(t)
	ui := setuptest.NewUI(t, setuptest.Pick(0).About("Use these?"), setuptest.Yes().About("launchd agent"))

	plan, out, err := h.up(ui, setup.Options{})
	require.NoError(t, err, "%v", ui.Events())
	assert.Equal(t, []string{"models", "home", "daemon", "import"}, stepsOf(plan.Fixes()),
		"a new home's library is empty; the fake daemon then reports one with bookmarks")
	assert.True(t, out.Changed)
	assert.Equal(t, []string{embedModel, genModel}, h.ollama.Pulls())
	require.Len(t, ui.Reports(), 2)
	for _, p := range ui.Reports() {
		done, _ := p.Finished()
		assert.True(t, done, p.Title)
		assert.NotEmpty(t, p.Updates(), p.Title)
	}
	assert.True(t, ui.Said("This Mac: Apple M4 Max, 64 GB of memory"))

	home := h.openHome()
	meta, err := home.Meta()
	require.NoError(t, err)
	assert.Equal(t, curiohome.CurrentFormat, meta.Format)
	assert.Equal(t, embedModel, meta.EmbeddingModel)
	assert.Equal(t, 1024, meta.EmbeddingDim)
	cfg, err := config.Load(home.ConfigPath())
	require.NoError(t, err)
	assert.Equal(t, genModel, cfg.Generation.Model)
	assert.Equal(t, config.QwenQueryPrefix, cfg.Embedding.QueryPrefix)
	assert.Equal(t, h.listen, cfg.Daemon.Listen)
	info, err := os.Stat(home.ConfigPath())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	assert.Equal(t, 1, h.agent.Count("Install"))
	assert.True(t, out.Status.Daemon.Managed, "%+v", out.Status.Daemon)
	assert.Equal(t, h.agent.PID(), out.Status.Daemon.PID)
	assert.Equal(t, "0.34.4", out.Status.Ollama.Version)
	assert.Equal(t, []setup.ModelSnapshot{{Name: embedModel, Present: true}, {Name: genModel, Present: true}},
		out.Status.Ollama.Models)
	assert.Equal(t, 3, out.Status.Documents)
	require.NoError(t, out.Status.QueueErr)
	assert.Equal(t, client.QueueOpen, out.Status.Queue.State)
	assert.Empty(t, h.installer.Runs(), "Ollama answered: nothing to install")
}

// TestUp_NothingToDo: a run after a complete one finds every check
// passing, shows no plan, asks nothing, and changes nothing.
func TestUp_NothingToDo(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	changes, pulls := h.changes(), len(h.ollama.Pulls())

	ui := setuptest.NewUI(t)
	plan, out, err := h.up(ui, setup.Options{})
	require.NoError(t, err)
	assert.Nil(t, plan, "no plan shown")
	assert.True(t, out.Plan.Empty())
	assert.False(t, out.Changed)
	assert.Empty(t, ui.Lines("prompt"))
	assert.Equal(t, changes, h.changes())
	assert.Len(t, h.ollama.Pulls(), pulls)
	assert.Empty(t, h.installer.Runs())
	assert.True(t, out.Status.Daemon.Managed)
}

// TestUp_Resume: a run stopped at the daemon step's question leaves what
// came before applied; the next run's plan is the daemon step alone, and
// the one after has nothing to do. Nothing records where a run stopped.
func TestUp_Resume(t *testing.T) {
	h := newHarness(t)
	ui := setuptest.NewUI(t, setuptest.Pick(0), setuptest.Abort().About("launchd agent"))
	_, _, err := h.up(ui, setup.Options{})
	require.ErrorIs(t, err, setup.ErrAborted)
	assert.FileExists(t, filepath.Join(h.home, curiohome.MarkerFile))
	assert.FileExists(t, filepath.Join(h.home, curiohome.ConfigFile))
	assert.Zero(t, h.agent.Count("Install"))

	ui = setuptest.NewUI(t, setuptest.Yes().About("launchd agent"))
	plan, out, err := h.up(ui, setup.Options{})
	require.NoError(t, err)
	assert.Equal(t, []string{"daemon"}, stepsOf(plan.Fixes()))
	assert.True(t, out.Changed)
	assert.Equal(t, 1, h.agent.Count("Install"))

	plan, out, err = h.up(setuptest.NewUI(t), setup.Options{})
	require.NoError(t, err)
	assert.Nil(t, plan)
	assert.False(t, out.Changed)
	entries, err := os.ReadDir(h.home)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotEqual(t, setup.StateFile, e.Name(), "no state recorded where a run stopped")
	}
}

// TestUp_DryRun: a dry run shows the plan and changes nothing: no home,
// no pull, no command, no agent.
func TestUp_DryRun(t *testing.T) {
	h := newHarness(t)
	ui := setuptest.NewUI(t)
	plan, out, err := h.up(ui, setup.Options{DryRun: true})
	require.NoError(t, err)
	assert.True(t, out.DryRun)
	assert.False(t, out.Changed)
	assert.Equal(t, []string{"models", "home", "daemon", "import"}, stepsOf(plan.Fixes()))
	assert.Contains(t, item(t, plan, "home").Fix.Summary, embedModel+", 1024 dimensions")
	assert.NoDirExists(t, h.home)
	assert.Empty(t, h.ollama.Pulls())
	assert.Empty(t, h.installer.Runs())
	assert.Zero(t, h.changes())
	assert.Zero(t, h.agent.Count("Preflight"), "no home: the agent isn't asked")
	assert.Empty(t, ui.Lines("prompt"))
}

// TestUp_NoTerminal: without a terminal and --yes, a plan is shown and
// nothing is applied; --yes applies it without a question.
func TestUp_NoTerminal(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.up(setuptest.NewUI(t).NonInteractive(), setup.Options{})
	require.ErrorIs(t, err, setup.ErrNeedsYes)
	assert.NoDirExists(t, h.home)

	_, out, err := h.up(yes(t), setup.Options{Yes: true})
	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.True(t, out.Status.Daemon.Managed)
}

// yes is the UI of `curio up --yes` without a terminal.
func yes(t *testing.T) setup.UI {
	return setup.NewUI(emptyReader{}, testWriter{t}, true)
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, errors.New("no terminal") }

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

// TestUp_BlockedBeforeInstalling: a blocker the plan finds stops the run
// before anything is applied: a legacy home with Ollama down runs no brew
// command, though Homebrew would install Ollama.
func TestUp_BlockedBeforeInstalling(t *testing.T) {
	h := newHarness(t)
	h.writeHome(genModel)
	require.NoError(t, os.WriteFile(filepath.Join(h.home, curiohome.MarkerFile),
		[]byte(`{"schema_version":11,"embedding_model":"nomic-embed-text","embedding_dim":768}`), 0o600))
	h.ollama.Stop()

	plan, _, err := h.up(setuptest.NewUI(t), setup.Options{})
	var blocked *setup.BlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Contains(t, stepsOf(blocked.Blockers), "home")
	home := item(t, plan, "home")
	assert.Contains(t, home.Detail, "curio home from an older curio")
	assert.Contains(t, home.Hint, "curio up --fresh")
	assert.NotNil(t, item(t, plan, "ollama").Fix, "the Ollama install was planned")
	assert.Empty(t, h.installer.Runs(), "and never run")
}

// TestUp_Blockers: what the plan can't fix blocks it, with why and what
// to do.
func TestUp_Blockers(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, h *harness)
		step   string
		detail string
		hint   string
	}{
		{"a directory that isn't a curio home", func(t *testing.T, h *harness) {
			require.NoError(t, os.MkdirAll(h.home, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(h.home, "notes.txt"), []byte("mine"), 0o600))
		}, "home", "isn't a curio home", "--curio-home"},
		{"a config.yaml that doesn't load", func(t *testing.T, h *harness) {
			home := h.writeHome(genModel)
			require.NoError(t, os.WriteFile(home.ConfigPath(), []byte("daemon:\n  fetch_workers: 0\n"), 0o600))
		}, "home", "daemon.fetch_workers must be positive", "edit "},
		{"a newer home", func(t *testing.T, h *harness) {
			home := h.writeHome(genModel)
			meta, err := home.Meta()
			require.NoError(t, err)
			meta.Format = curiohome.CurrentFormat + 1
			require.NoError(t, home.WriteMeta(meta))
		}, "home", "curio home from a newer curio", "upgrade curio first"},
		{"a mismatched config.yaml", func(t *testing.T, h *harness) {
			home := h.writeHome(genModel)
			cfg, err := os.ReadFile(home.ConfigPath())
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(home.ConfigPath(), append(cfg, []byte("  dim: 768\n")...), 0o600))
		}, "home", "", ""},
		{"no curio-daemon", func(t *testing.T, _ *harness) {
			t.Setenv("CURIO_DAEMON_BIN", filepath.Join(t.TempDir(), "curio-daemon"))
		}, "daemon", "curio-daemon is missing", "CURIO_DAEMON_BIN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(t, h)
			plan, _, err := h.up(setuptest.NewUI(t), setup.Options{})
			var blocked *setup.BlockedError
			require.ErrorAs(t, err, &blocked)
			assert.Contains(t, stepsOf(blocked.Blockers), tc.step)
			res := item(t, plan, tc.step)
			assert.Contains(t, res.Detail, tc.detail)
			assert.Contains(t, res.Hint, tc.hint)
			assert.Empty(t, h.ollama.Pulls())
			assert.Zero(t, h.changes())
		})
	}
}

// TestUp_AnotherHomesDaemon: the port another home's daemon serves
// blocks the plan: the new daemon could only fail to bind it.
func TestUp_AnotherHomesDaemon(t *testing.T) {
	h := newHarness(t)
	other := newHarness(t)
	other.listen = h.listen
	other.defaults.Daemon.Listen = h.listen
	other.firstRun()

	plan, _, err := h.up(setuptest.NewUI(t), setup.Options{})
	var blocked *setup.BlockedError
	require.ErrorAs(t, err, &blocked)
	daemon := item(t, plan, "daemon")
	assert.Contains(t, daemon.Detail, "is served by the curio-daemon for "+other.home)
	assert.Contains(t, daemon.Hint, "daemon.listen")
}

// TestUp_SilentOllama: an Ollama that takes connections and never answers
// can't hang the plan: every probe gives up.
func TestUp_SilentOllama(t *testing.T) {
	h := newHarness(t)
	h.defaults.Embedding.BaseURL = setuptest.Silent(t)
	h.defaults.Generation.BaseURL = h.defaults.Embedding.BaseURL
	deps := h.deps(setuptest.NewUI(t))
	deps.Timeouts.Probe = 200 * time.Millisecond
	r, err := setup.New(setup.Options{Home: h.home}, deps)
	require.NoError(t, err)

	start := time.Now()
	plan := r.Plan(context.Background())
	assert.Less(t, time.Since(start), 10*time.Second)
	assert.Equal(t, setup.Fail, item(t, plan, "ollama").Status)
	assert.Contains(t, item(t, plan, "ollama").Detail, "nothing answers at")
}

// TestUp_EmbeddingModelWithoutATag: an untagged --embedding-model is
// refused before anything is checked: a home's vectors are fixed for its
// life, and :latest moves.
func TestUp_EmbeddingModelWithoutATag(t *testing.T) {
	h := newHarness(t)
	_, err := setup.New(setup.Options{Home: h.home, EmbeddingModel: "qwen3-embedding"}, h.deps(setuptest.NewUI(t)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name a tag")
}

// TestUp_MachineQuestion: an Intel Mac is asked once, before anything
// runs, whether to go on; a no stops the run, changing nothing.
func TestUp_MachineQuestion(t *testing.T) {
	h := newHarness(t)
	h.probe.Facts.AppleSilicon, h.probe.Facts.Chip = false, "Intel(R) Core(TM) i9"
	ui := setuptest.NewUI(t, setuptest.No().About("Intel"))
	plan, _, err := h.up(ui, setup.Options{})
	var declined *setup.DeclinedError
	require.ErrorAs(t, err, &declined)
	assert.Equal(t, "machine", declined.Step)
	assert.True(t, strings.HasSuffix(item(t, plan, "machine").Question, "Continue?"))
	assert.NoDirExists(t, h.home)
	assert.Empty(t, h.ollama.Pulls())
}

// TestUp_Consent: a fix shows every command it runs, in full, before its
// one question; a new home is announced, not asked about; and a no ends
// the run, leaving what is left to do and the commands to run by hand.
func TestUp_Consent(t *testing.T) {
	h := newHarness(t)
	h.ollama.Stop()
	h.installer.Detected = setup.Detection{Brew: brew}
	h.installer.OnRun = func(argv []string) error {
		if argv[1] == "services" {
			h.ollama.Start()
		}
		return nil
	}
	ui := setuptest.NewUI(t, setuptest.Yes().About("Install Ollama with Homebrew"), setuptest.Pick(0).About("Use these?"),
		setuptest.Yes().About("launchd agent"))
	_, _, err := h.up(ui, setup.Options{})
	require.NoError(t, err, "%v", ui.Events())
	var before []setuptest.Event
	for _, e := range ui.Events() {
		if e.Kind == "prompt" {
			break
		}
		before = append(before, e)
	}
	assert.Equal(t, []setuptest.Event{
		{Kind: "info", Text: "  $ " + brew + " install ollama"},
		{Kind: "info", Text: "  $ " + brew + " services start ollama"},
	}, before, "every command, before the question")
	assert.True(t, ui.Said("home: create a curio home at "+h.home))
	for _, p := range ui.Lines("prompt") {
		assert.NotContains(t, p, "curio home", "a new home isn't asked about")
	}

	h = newHarness(t)
	h.ollama.Stop()
	h.installer.Detected = setup.Detection{Brew: brew}
	_, _, err = h.up(setuptest.NewUI(t, setuptest.No().About("Install Ollama")), setup.Options{})
	var declined *setup.DeclinedError
	require.ErrorAs(t, err, &declined)
	assert.Equal(t, "ollama", declined.Step)
	for _, want := range []string{"stopped at ollama: declined. Left to do", brew + " install ollama",
		brew + " services start ollama", "models: ", "home: ", "daemon: "} {
		assert.Contains(t, err.Error(), want)
	}
	assert.Empty(t, h.installer.Runs())
}

// tree lists every file under dir with its size and modification time;
// empty when dir doesn't exist.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return filepath.SkipAll
		}
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = fmt.Sprintf("%d %s", info.Size(), info.ModTime())
		return nil
	})
	require.NoError(t, err)
	return out
}

// TestChecks_ReadOnly: the checks, which `curio doctor` runs and a dry
// run's plan is made of, change nothing, whatever the world: no file
// written, no command run, no pull, no change to the agent, no daemon
// started.
func TestChecks_ReadOnly(t *testing.T) {
	worlds := map[string]func(t *testing.T, h *harness){
		"a fresh machine": func(*testing.T, *harness) {},
		"a legacy home":   func(t *testing.T, h *harness) { legacyHome(t, h) },
		"Ollama down":     func(_ *testing.T, h *harness) { h.writeHome(genModel); h.ollama.Stop() },
		"everything up":   func(_ *testing.T, h *harness) { h.firstRun() },
	}
	for name, setupWorld := range worlds {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			setupWorld(t, h)
			before, changes, pulls, runs := tree(t, h.home), h.changes(), len(h.ollama.Pulls()), len(h.installer.Runs())
			r := h.runner(setuptest.NewUI(t), setup.Options{})
			for _, c := range r.Checks() {
				c.Run(context.Background())
			}
			r.Plan(context.Background())
			assert.Equal(t, before, tree(t, h.home), "nothing written, daemon.pid included: no daemon started")
			assert.Equal(t, changes, h.changes())
			assert.Len(t, h.ollama.Pulls(), pulls)
			assert.Len(t, h.installer.Runs(), runs)
		})
	}
}
