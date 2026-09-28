package setup_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/service"
	"github.com/samsar/curio/internal/service/servicetest"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
)

// The models curio picks for the harness's Mac, AppleSilicon(64).
const (
	embedModel = "qwen3-embedding:0.6b"
	genModel   = "gemma4:26b"
)

// harness is a machine for `curio up` to set up: a home path, an Ollama
// (setuptest's, answering, with no models), an installer that finds
// Homebrew and nothing else, a 64 GiB Apple silicon Mac, and a launchd
// agent (servicetest's) that runs this test binary as curio-daemon. The
// daemon binary, curio-daemon next to curio, is this test binary too, so
// a daemon started without the agent is the same fake.
type harness struct {
	t         *testing.T
	home      string
	listen    string
	exe       string
	ollama    *setuptest.Ollama
	installer *setuptest.Installer
	probe     *setuptest.Probe
	agent     *servicetest.Fake
	defaults  config.Config
	// service is what the daemon's controller gets; the agent unless a
	// test says otherwise.
	service service.Manager
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("CURIO_DAEMON_BIN", exe)
	t.Setenv(setuptest.DaemonVar, "1") // what a spawned curio-daemon runs as
	t.Setenv(setuptest.DaemonVersionVar, "")
	h := &harness{
		t:         t,
		home:      filepath.Join(t.TempDir(), "curio"),
		listen:    freeAddr(t),
		exe:       exe,
		ollama:    setuptest.NewOllama(t, "0.34.4"),
		installer: setuptest.NewInstaller(),
		probe:     setuptest.NewProbe(),
		agent:     servicetest.New(t, service.BaseLabel+".test"),
	}
	h.defaults = config.Default()
	h.defaults.Daemon.Listen = h.listen
	h.defaults.Embedding.BaseURL, h.defaults.Generation.BaseURL = h.ollama.URL, h.ollama.URL
	h.agent.Launch = h.launch
	h.service = h.agent
	t.Cleanup(h.killDaemon)
	return h
}

// launch is the command the agent runs: this test binary as the daemon,
// with the home's CURIO_HOME and its output in the home's log, as the
// plist has launchd run it.
func (h *harness) launch() *exec.Cmd {
	cmd := exec.Command(h.exe)
	cmd.Env = append(os.Environ(), "CURIO_HOME="+h.home, setuptest.DaemonVar+"=1")
	if log, err := os.OpenFile(filepath.Join(h.home, "logs", "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = log, log
	}
	return cmd
}

// killDaemon kills a daemon still holding the home's lock when the test
// ends: one the harness's own controllers spawned.
func (h *harness) killDaemon() {
	home, err := curiohome.Open(h.home)
	if err != nil {
		return
	}
	data, err := os.ReadFile(home.PIDFile())
	if err != nil {
		return
	}
	var pid int
	if _, err := fmt.Sscan(strings.TrimSpace(string(data)), &pid); err == nil && pid > 0 && pid != os.Getpid() {
		_ = syscall.Kill(pid, syscall.SIGKILL) // it may have exited since
	}
}

// deps are setup's dependencies over the harness, with ui.
func (h *harness) deps(ui setup.UI) setup.Deps {
	return setup.Deps{
		UI:        ui,
		Probe:     h.probe,
		Installer: h.installer,
		Connect:   h.connect,
		Defaults:  &h.defaults,
		Timeouts:  setup.Timeouts{Probe: 2 * time.Second, Start: 5 * time.Second, Download: 5 * time.Second, Poll: 20 * time.Millisecond},
	}
}

// connect is daemonctl.Connect with the harness's service manager.
func (h *harness) connect(home *curiohome.Home, cfg config.Config, daemonURL string) (daemonctl.Env, error) {
	env, err := daemonctl.Connect(home, cfg, daemonURL)
	if err != nil {
		return env, err
	}
	env.Controller.Service = h.service
	return env, nil
}

// runner is a Runner for opts over the harness, answering with ui.
func (h *harness) runner(ui setup.UI, opts setup.Options) *setup.Runner {
	h.t.Helper()
	opts.Home = h.home
	r, err := setup.New(opts, h.deps(ui))
	require.NoError(h.t, err)
	return r
}

// up runs `curio up` with opts, answering with ui, and returns the plan
// it showed (nil when it showed none), its outcome and its error.
func (h *harness) up(ui setup.UI, opts setup.Options) (setup.Plan, setup.Outcome, error) {
	h.t.Helper()
	var shown setup.Plan
	out, err := h.runner(ui, opts).Run(context.Background(), func(p setup.Plan) { shown = p })
	return shown, out, err
}

// firstRun is a run of `curio up` on the harness that agrees to
// everything, and must succeed.
func (h *harness) firstRun() setup.Outcome {
	h.t.Helper()
	ui := setuptest.NewUI(h.t, setuptest.Pick(0).About("Use these?"), setuptest.Yes().About("launchd agent"))
	_, out, err := h.up(ui, setup.Options{})
	require.NoError(h.t, err, "%v", ui.Events())
	require.Zero(h.t, ui.Unanswered())
	return out
}

// openHome opens the harness's home, which must exist.
func (h *harness) openHome() *curiohome.Home {
	h.t.Helper()
	home, err := curiohome.Open(h.home)
	require.NoError(h.t, err)
	return home
}

// writeHome makes the harness's home a current one embedding with the
// default model, with config.yaml pointing at the harness and naming
// generation as its writing model.
func (h *harness) writeHome(generation string) *curiohome.Home {
	h.t.Helper()
	home, err := curiohome.Init(h.home, embedModel, 1024)
	require.NoError(h.t, err)
	cfg := fmt.Sprintf("daemon:\n  listen: %q\nembedding:\n  base_url: %q\ngeneration:\n  model: %s\n  base_url: %q\n",
		h.listen, h.ollama.URL, generation, h.ollama.URL)
	require.NoError(h.t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
	return home
}

// changes counts the agent's calls that change something.
func (h *harness) changes() int {
	n := 0
	for _, m := range []string{"Install", "Uninstall", "Start", "Stop", "Restart"} {
		n += h.agent.Count(m)
	}
	return n
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// stepsOf are the step names of plan items.
func stepsOf(items []setup.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Step)
	}
	return out
}

// item is the plan's item for step.
func item(t *testing.T, plan setup.Plan, step string) setup.Result {
	t.Helper()
	for _, it := range plan {
		if it.Step == step {
			return it.Result
		}
	}
	t.Fatalf("no %s step in the plan", step)
	return setup.Result{}
}

// step is r's step called name.
func step(t *testing.T, r *setup.Runner, name string) setup.Step {
	t.Helper()
	for _, s := range r.Steps() {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no step %s", name)
	return nil
}

// checkStep checks r's step called name.
func checkStep(t *testing.T, r *setup.Runner, name string) setup.Result {
	t.Helper()
	return step(t, r, name).Check(context.Background())
}

// applyOnly checks r's step called name and applies what it found,
// without the runner's questions.
func applyOnly(t *testing.T, r *setup.Runner, name string, ui setup.UI) error {
	t.Helper()
	s := step(t, r, name)
	s.Check(context.Background())
	return s.Apply(context.Background(), ui)
}
