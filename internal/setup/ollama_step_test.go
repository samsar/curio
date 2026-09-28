package setup_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
)

const brew = "/opt/homebrew/bin/brew"

// TestUp_OllamaStarts: with nothing answering, curio up plans the start
// or install what is installed calls for, asks once, runs it, and waits
// until Ollama answers; the step then passes.
func TestUp_OllamaStarts(t *testing.T) {
	cases := []struct {
		name     string
		detected setup.Detection
		want     [][]string
	}{
		{"formula installed", setup.Detection{Brew: brew, Formula: true},
			[][]string{{brew, "services", "start", "ollama"}}},
		{"the app", setup.Detection{Brew: brew, App: "/Applications/Ollama.app"},
			[][]string{{"/usr/bin/open", "-a", "Ollama"}}},
		{"nothing installed", setup.Detection{Brew: brew},
			[][]string{{brew, "install", "ollama"}, {brew, "services", "start", "ollama"}}},
		{"no Homebrew, in a terminal", setup.Detection{},
			[][]string{{"/usr/bin/open", "https://ollama.com/download"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.writeHome(genModel)
			h.ollama.Stop()
			h.installer.Detected = tc.detected
			var ran [][]string
			h.installer.OnRun = func(argv []string) error {
				ran = append(ran, argv)
				if len(ran) == len(tc.want) {
					h.ollama.Start() // the last command brings Ollama up
				}
				return nil
			}
			ui := setuptest.NewUI(t, setuptest.Yes().About("Ollama"), setuptest.Yes(), setuptest.Yes())
			plan, _, err := h.up(ui, setup.Options{DryRun: true})
			require.NoError(t, err)
			ollama := item(t, plan, "ollama")
			require.NotNil(t, ollama.Fix)
			assert.Equal(t, tc.want, ollama.Fix.Commands)

			r := h.runner(ui, setup.Options{})
			res := checkStep(t, r, "ollama")
			require.NotNil(t, res.Fix)
			require.NoError(t, applyOnly(t, r, "ollama", ui))
			assert.Equal(t, tc.want, h.installer.Runs())
			assert.Equal(t, setup.OK, checkStep(t, r, "ollama").Status, "Ollama answers now")
		})
	}
}

// TestUp_OllamaBlocked: what curio up won't install or start is left to
// do by hand, with what to run: --no-install, an Ollama elsewhere, a
// script with no Homebrew to install with.
func TestUp_OllamaBlocked(t *testing.T) {
	cases := []struct {
		name     string
		opts     setup.Options
		detected setup.Detection
		remote   bool
		ui       func(t *testing.T) setup.UI
		hint     string
	}{
		{"--no-install", setup.Options{NoInstall: true}, setup.Detection{Brew: brew}, false, nil,
			"run `" + brew + " install ollama` and `" + brew + " services start ollama` (--no-install runs nothing)"},
		{"a remote base_url", setup.Options{}, setup.Detection{Brew: brew}, true, nil, "start Ollama at http://192.0.2.1:11434"},
		{"no Homebrew, no terminal", setup.Options{Yes: true}, setup.Detection{}, false,
			func(t *testing.T) setup.UI { return setuptest.NewUI(t).NonInteractive() },
			"install Ollama from https://ollama.com/download"},
		{"an ollama that is neither", setup.Options{}, setup.Detection{Brew: brew, Binary: "/usr/local/bin/ollama"}, false, nil,
			"start it (ollama serve)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.ollama.Stop()
			h.installer.Detected = tc.detected
			if tc.remote {
				h.defaults.Embedding.BaseURL = "http://192.0.2.1:11434"
			}
			ui := setup.UI(setuptest.NewUI(t))
			if tc.ui != nil {
				ui = tc.ui(t)
			}
			plan, _, err := h.up(ui, tc.opts)
			var blocked *setup.BlockedError
			require.ErrorAs(t, err, &blocked)
			ollama := item(t, plan, "ollama")
			assert.True(t, ollama.Blocked())
			assert.Contains(t, ollama.Hint, tc.hint)
			assert.Empty(t, h.installer.Runs())
		})
	}
}

// TestUp_OllamaVersionGate: an Ollama older than the writing model needs
// is upgraded when Homebrew installed it, and restarted, since the old
// server runs until it is; any other install is left to upgrade by hand.
func TestUp_OllamaVersionGate(t *testing.T) {
	h := newHarness(t)
	h.writeHome(genModel)
	h.ollama.SetVersion("0.29.0")
	h.installer.Detected = setup.Detection{Brew: brew, Formula: true}
	h.installer.OnRun = func(argv []string) error {
		if argv[1] == "services" {
			h.ollama.SetVersion("0.34.4")
		}
		return nil
	}
	r := h.runner(setuptest.NewUI(t), setup.Options{})
	res := checkStep(t, r, "ollama")
	assert.Contains(t, res.Detail, "Ollama 0.29.0 at "+h.ollama.URL+" is too old: gemma4:26b needs 0.30.5 or later")
	require.NotNil(t, res.Fix)
	assert.Equal(t, [][]string{{brew, "upgrade", "ollama"}, {brew, "services", "restart", "ollama"}}, res.Fix.Commands)
	require.NoError(t, applyOnly(t, r, "ollama", setuptest.NewUI(t)))
	assert.Equal(t, setup.OK, checkStep(t, r, "ollama").Status)

	h.ollama.SetVersion("0.29.0")
	h.installer.Detected = setup.Detection{Brew: brew, App: "/Applications/Ollama.app"}
	res = checkStep(t, r, "ollama")
	assert.True(t, res.Blocked())
	assert.Contains(t, res.Hint, "install the latest from https://ollama.com/download")

	h.ollama.SetVersion("0.35.0-rc1")
	assert.Equal(t, setup.OK, checkStep(t, r, "ollama").Status)
	h.ollama.SetVersion("dev")
	res = checkStep(t, r, "ollama")
	assert.Equal(t, setup.Warn, res.Status, "a version it can't read is a warning, not a block")
	assert.Nil(t, res.Fix)
}

// TestUp_OllamaCommandFails: a command that fails fails the step, naming
// the command; nothing after it runs.
func TestUp_OllamaCommandFails(t *testing.T) {
	h := newHarness(t)
	h.writeHome(genModel)
	h.ollama.Stop()
	h.installer.Detected = setup.Detection{Brew: brew}
	h.installer.OnRun = func(argv []string) error {
		return errors.New("`" + argv[0] + " " + argv[1] + " ollama` failed: exit status 1")
	}
	ui := setuptest.NewUI(t, setuptest.Yes().About("Install Ollama"))
	_, _, err := h.up(ui, setup.Options{})
	var failed *setup.StepError
	require.ErrorAs(t, err, &failed)
	assert.Equal(t, "ollama", failed.Step)
	assert.Contains(t, err.Error(), brew+" install ollama` failed: exit status 1")
	assert.Len(t, h.installer.Runs(), 1, "the start never ran")
	assert.Zero(t, h.changes())
}
