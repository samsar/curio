package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/service"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
)

// TestUp_NothingToDo: with everything up, curio up changes nothing, says
// so, and shows the status.
func TestUp_NothingToDo(t *testing.T) {
	w := upWorld(t)
	code, stdout, stderr := w.exit(t, "up")
	assert.Equal(t, 0, code, stderr)
	assert.Equal(t, "Nothing to do: curio is up.\n"+
		fmt.Sprintf("daemon:   running (pid %d), kept running by its launchd agent\n", os.Getpid())+
		"ollama:   0.34.4; qwen3-embedding:0.6b present, gemma4:26b present\n"+
		"library:  0 documents\n"+
		"queue:    open\n", stdout)
	assert.Empty(t, stderr)
	assert.Zero(t, w.agent.Count("Install"))
}

// TestUp_DaemonStarting: a daemon still starting leaves the parts of the
// status it serves unread, and the status says so for each, never taking a
// failed read for an empty library or an open queue.
func TestUp_DaemonStarting(t *testing.T) {
	w := upWorldFrom(t, apitest.StartNotReady)
	code, stdout, stderr := w.exit(t, "up")
	assert.Equal(t, 0, code, stderr)
	assert.Empty(t, stderr)
	assert.True(t, strings.HasPrefix(stdout, "Nothing to do: curio is up.\n"), stdout)
	for _, want := range []string{
		"daemon:   unavailable: starting: initializing\n",
		"library:  unavailable: curio-daemon is starting: initializing\n",
		"queue:    unavailable: curio-daemon is starting: initializing\n",
	} {
		assert.Contains(t, stdout, want)
	}
	assert.NotContains(t, stdout, "0 documents")
	assert.NotContains(t, stdout, "queue:    open")
}

// TestUp_FreshMachine: on a Mac with only Ollama, `curio up --yes` sets
// everything up, says what it did on stderr, and ends with the status and
// what to do next on stdout.
func TestUp_FreshMachine(t *testing.T) {
	w := freshWorld(t)
	code, stdout, stderr := w.exit(t, "up", "--yes")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "curio up will:\n  1. models: pull qwen3-embedding:0.6b (639 MB) and gemma4:26b (19 GB)\n")
	assert.Contains(t, stdout, "curio is up.\n")
	assert.Contains(t, stdout, "kept running by its launchd agent")
	assert.Contains(t, stdout, "Next:\n  curio import chrome")
	assert.Contains(t, stdout, "claude mcp add curio -- ")
	assert.Contains(t, stderr, "Use these? yes (--yes)")
	assert.Contains(t, stderr, "pulling gemma4:26b: 100%")
	assert.NotContains(t, stdout, "pulling", "progress is stderr's")
	assert.FileExists(t, filepath.Join(w.home, curiohome.ConfigFile))

	code, stdout, _ = w.exit(t, "up")
	assert.Equal(t, 0, code)
	assert.True(t, strings.HasPrefix(stdout, "Nothing to do: curio is up.\n"), stdout)
}

// TestUp_EmbeddingOverride: the model a flag names makes the home, at the
// width it embeds with, before anything could make a default one: the
// root's hook discovers no home for up.
func TestUp_EmbeddingOverride(t *testing.T) {
	w := freshWorld(t)
	const nomic = "nomic-embed-text:v1.5"
	w.ollama.SetWidth(nomic, 768)
	code, _, stderr := w.exit(t, "up", "--yes", "--embedding-model", nomic)
	require.Equal(t, 0, code, stderr)
	home, err := curiohome.Open(w.home)
	require.NoError(t, err)
	meta, err := home.Meta()
	require.NoError(t, err)
	assert.Equal(t, nomic, meta.EmbeddingModel)
	assert.Equal(t, 768, meta.EmbeddingDim)
	cfg, err := config.Load(home.ConfigPath())
	require.NoError(t, err)
	assert.Equal(t, nomic, cfg.Embedding.Model)
	assert.Equal(t, 768, cfg.Embedding.Dim)
}

// TestUp_ExitCodes: 0 for nothing to do, applied, or a dry run; 1 for a
// blocked plan, a declined step, a failed one, no terminal and no --yes,
// and root; 130, with nothing said, for a prompt left unanswered.
func TestUp_ExitCodes(t *testing.T) {
	t.Run("dry run", func(t *testing.T) {
		w := freshWorld(t)
		code, stdout, stderr := w.exit(t, "up", "--dry-run")
		assert.Equal(t, 0, code, stderr)
		assert.Contains(t, stdout, "curio up will:")
		assert.Contains(t, stdout, "Dry run: nothing was changed.\n")
		assert.NoDirExists(t, w.home, "a dry run makes no home")
	})
	t.Run("blocked", func(t *testing.T) {
		w := upWorld(t)
		require.NoError(t, os.WriteFile(filepath.Join(w.home, curiohome.MarkerFile),
			[]byte(`{"schema_version":11,"embedding_model":"nomic-embed-text","embedding_dim":768}`), 0o600))
		code, stdout, stderr := w.exit(t, "up")
		assert.Equal(t, 1, code)
		assert.Contains(t, stdout, "To fix by hand first:\n  ✗ home: curio home from an older curio")
		assert.Equal(t, "Error: nothing was changed: home, daemon must be fixed by hand first (see above)\n", stderr)
	})
	t.Run("no terminal and no --yes", func(t *testing.T) {
		w := freshWorld(t)
		code, stdout, stderr := w.exit(t, "up")
		assert.Equal(t, 1, code)
		assert.Contains(t, stdout, "curio up will:")
		assert.Contains(t, stderr, "run `curio up --yes` to apply the plan above")
		assert.NoDirExists(t, w.home)
	})
	t.Run("aborted", func(t *testing.T) {
		w := freshWorld(t)
		w.scripted(setuptest.NewUI(t, setuptest.Abort().About("Use these?")))
		code, _, stderr := w.exit(t, "up")
		assert.Equal(t, 130, code)
		assert.Empty(t, stderr)
	})
	t.Run("declined", func(t *testing.T) {
		w := freshWorld(t)
		w.scripted(setuptest.NewUI(t, setuptest.Pick(1).About("Use these?")))
		code, _, stderr := w.exit(t, "up")
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr, "Error: stopped at models: declined.")
		assert.Contains(t, stderr, "--generation-model")
	})
	t.Run("a step fails", func(t *testing.T) {
		w := freshWorld(t)
		w.ollama.FailPull(genModel, "pull model manifest: file does not exist")
		code, _, stderr := w.exit(t, "up", "--yes")
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr, "Error: models: ollama pull gemma4:26b: pull model manifest: file does not exist")
	})
	t.Run("root", func(t *testing.T) {
		w := freshWorld(t)
		w.deps.geteuid = func() int { return 0 }
		probe := setuptest.NewProbe()
		w.deps.probe = probe
		code, stdout, stderr := w.exit(t, "up")
		assert.Equal(t, 1, code)
		assert.Equal(t, "Error: curio up sets curio up for your own user, and never uses sudo: run it without sudo\n", stderr)
		assert.Empty(t, stdout)
		assert.Zero(t, probe.Calls(), "before any check")
	})
}

// TestUp_Help: up has exactly its own flags and the root's, each
// explained.
func TestUp_Help(t *testing.T) {
	out := runArgs(t, "up", "--help")
	var flags []string
	for line := range strings.SplitSeq(out, "\n") {
		// Flag lines are indented; the description's lines aren't.
		if f := strings.Fields(line); len(f) > 0 && strings.HasPrefix(line, " ") && strings.HasPrefix(f[0], "--") {
			flags = append(flags, f[0])
		}
	}
	assert.ElementsMatch(t, []string{"--dry-run", "--embedding-model", "--fresh", "--generation-model", "--no-install",
		"--yes", "--curio-home", "--daemon-url"}, flags)
	assert.Contains(t, out, "deleting nothing")
	assert.Contains(t, out, "never uses sudo")
}

// TestBareCurio: bare curio prints its help, creating and starting
// nothing, and points at curio up when there is no home, or its daemon
// serves an empty library.
func TestBareCurio(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "curio")
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	out, err := runCLIAt(t, missing, down.URL)
	require.NoError(t, err)
	assert.Contains(t, out, "Usage:\n  curio [flags]\n  curio [command]")
	assert.True(t, strings.HasSuffix(out, "\ncurio isn't set up here yet: run `curio up`.\n"), out)
	assert.NoDirExists(t, missing)

	w := upWorld(t)
	out, err = w.run(t)
	require.NoError(t, err)
	assert.Contains(t, out, "Your library is empty: run `curio up`")

	_, err = w.run(t, "add", "https://example.com/a")
	require.NoError(t, err)
	out, err = w.run(t)
	require.NoError(t, err)
	assert.NotContains(t, out, "curio up`", "a library with bookmarks needs no hint")

	out, err = runCLIAt(t, w.home, down.URL)
	require.NoError(t, err)
	assert.NotContains(t, out, "curio up`", "no daemon: nothing to say, and none started")
}

// TestMCPCommand: Claude Code registers curio-mcp by name when the one on
// PATH is the one next to curio, and by its path otherwise.
func TestMCPCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Tools")
	require.NoError(t, os.Mkdir(dir, 0o700))
	next := filepath.Join(dir, "curio-mcp")
	require.NoError(t, os.WriteFile(next, []byte("#!/bin/false\n"), 0o700))
	exe := filepath.Join(dir, "curio")

	t.Setenv("PATH", dir)
	assert.Equal(t, "claude mcp add curio -- curio-mcp", mcpCommandFor(exe))

	elsewhere := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(elsewhere, "curio-mcp"), []byte("#!/bin/false\n"), 0o700))
	t.Setenv("PATH", elsewhere)
	assert.Equal(t, "claude mcp add curio -- '"+next+"'", mcpCommandFor(exe), "another curio-mcp on PATH isn't this curio's")

	t.Setenv("PATH", t.TempDir())
	assert.Equal(t, "claude mcp add curio -- '"+next+"'", mcpCommandFor(exe), "none on PATH")
}

// TestDoctorAgreesWithUp: doctor's shared checks are curio up's: over the
// same worlds, each shows the status the setup check finds, and up's plan
// is empty exactly when none of them fails or has something to fix.
func TestDoctorAgreesWithUp(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	worlds := map[string]func(t *testing.T) *world{
		"a fresh machine": func(t *testing.T) *world {
			w := newWorld(t)
			w.home, w.daemonURL = filepath.Join(t.TempDir(), "curio"), down.URL
			w.ollama.Stop()
			return w
		},
		"everything up": func(t *testing.T) *world { return upWorld(t) },
		"a file home": func(t *testing.T) *world {
			w := newWorld(t, embedModel, genModel)
			w.home, w.daemonURL = filepath.Join(t.TempDir(), "curio"), down.URL
			require.NoError(t, os.WriteFile(w.home, []byte("notes"), 0o600))
			return w
		},
		"a non-curio directory": func(t *testing.T) *world {
			w := newWorld(t, embedModel, genModel)
			w.home, w.daemonURL = t.TempDir(), down.URL
			require.NoError(t, os.WriteFile(filepath.Join(w.home, "notes.txt"), []byte("mine"), 0o600))
			return w
		},
		"a legacy home": func(t *testing.T) *world {
			w := upWorld(t)
			require.NoError(t, os.WriteFile(filepath.Join(w.home, curiohome.MarkerFile),
				[]byte(`{"schema_version":11,"embedding_model":"nomic-embed-text","embedding_dim":768}`), 0o600))
			return w
		},
		"Ollama down": func(t *testing.T) *world {
			w := upWorld(t)
			w.ollama.Stop()
			return w
		},
		"a model missing": func(t *testing.T) *world {
			w := upWorld(t)
			w.ollama = setuptest.NewOllama(t, "0.34.4", embedModel)
			writeConfig(t, w.srv.Home, w.ollama.URL, genModel)
			return w
		},
		"no agent": func(t *testing.T) *world {
			w := upWorld(t)
			w.agent.Set(func(st *service.Status) { *st = service.Status{Supported: true, Label: agentLabel} })
			return w
		},
		"another build": func(t *testing.T) *world {
			w := upWorld(t)
			old := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
				rw.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(rw, `{"status":"ok","pid":%d,"home":%q,"version":"v0.9.0","generation_model":%q,"upstreams":[]}`,
					os.Getpid(), w.home, genModel)
			}))
			t.Cleanup(old.Close)
			w.daemonURL = old.URL
			return w
		},
	}
	for name, build := range worlds {
		t.Run(name, func(t *testing.T) {
			w := build(t)
			out, _ := w.run(t, "doctor")
			r, err := setup.New(setup.Options{Home: w.home, DaemonURL: w.daemonURL}, setup.Deps{
				UI: setuptest.NewUI(t).NonInteractive(), Probe: w.deps.probe, Installer: w.deps.installer,
				Connect: w.deps.connect, Defaults: w.deps.defaults,
			})
			require.NoError(t, err)
			actionable := false
			for _, c := range r.Checks() {
				res := c.Run(context.Background())
				line, _ := doctorLine(t, out, c.Name)
				assert.Equal(t, statusOf(res.Status).marker(), markerOf(line), "%s: %s", c.Name, line)
				actionable = actionable || res.Status == setup.Fail || res.Pending()
			}
			assert.Equal(t, !actionable, r.Plan(context.Background()).Empty(), out)
		})
	}
}
