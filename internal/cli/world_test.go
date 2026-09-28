package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api"
	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/service/servicetest"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
)

// world is a Mac for doctor and up to look at, in some state: a home,
// the daemon serving it (apitest's API, as this test process), an Ollama
// (a fake) and a launchd agent (a fake). run runs curio in it.
type world struct {
	srv       *apitest.Server // the daemon, in an upWorld
	home      string          // the home's path, which may not exist
	daemonURL string
	ollama    *setuptest.Ollama
	agent     *servicetest.Fake
	bin       string // this curio's curio-daemon
	deps      deps
}

// run runs curio with args in the world, and returns stdout and the error.
func (w *world) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runCLIStreams(newRootCmdWith(w.deps), w.home, w.daemonURL, &stdout, &stderr, args...)
	return stdout.String(), err
}

// exit runs curio with args in the world through Run's exit handling, and
// returns the exit code, stdout and stderr.
func (w *world) exit(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := newRootCmdWith(w.deps)
	root.SetIn(strings.NewReader(""))
	code = run(context.Background(), root, append([]string{"--curio-home", w.home, "--daemon-url", w.daemonURL}, args...),
		&out, &errOut)
	return code, out.String(), errOut.String()
}

// newWorld is a world whose deps point at a new fake Ollama with models
// and a new fake agent, not installed; curio-daemon is an executable file
// that isn't one.
func newWorld(t *testing.T, models ...string) *world {
	t.Helper()
	w := &world{ollama: setuptest.NewOllama(t, "0.34.4", models...), agent: servicetest.New(t, agentLabel),
		bin: daemonBin(t)}
	w.deps = testDeps(t)
	defaults := *w.deps.defaults
	defaults.Embedding.BaseURL, defaults.Generation.BaseURL = w.ollama.URL, w.ollama.URL
	w.deps.defaults = &defaults
	w.deps.connect = withService(w.agent)
	return w
}

// upWorld is a world where everything checks out: the daemon (apitest's,
// as this process, holding the home's lock) serves the home with
// config.yaml's writing model, Ollama has curio's models, and the launchd
// agent runs this curio's curio-daemon and manages the daemon. opts adjust
// the API's Deps.
func upWorld(t *testing.T, opts ...func(*api.Deps)) *world {
	t.Helper()
	return upWorldFrom(t, apitest.Start, opts...)
}

// upWorldFrom is upWorld with its API started by start: apitest.Start, or
// StartNotReady for a daemon still starting.
func upWorldFrom(t *testing.T, start func(testing.TB, ...func(*api.Deps)) *apitest.Server,
	opts ...func(*api.Deps)) *world {
	t.Helper()
	w := newWorld(t, embedModel, genModel)
	srv := start(t, append([]func(*api.Deps){func(d *api.Deps) {
		d.GenerationModel = genModel
		d.GitHubToken = true
	}}, opts...)...)
	w.srv, w.home, w.daemonURL = srv, srv.Home.Path, srv.URL
	writeConfig(t, srv.Home, w.ollama.URL, genModel)
	holdLockAsDaemon(t, srv.Home)
	managedByFake(w.agent, w.bin)
	return w
}

// writeConfig gives home a config.yaml whose Ollama is at ollamaURL and
// whose writing model is generation.
func writeConfig(t *testing.T, home *curiohome.Home, ollamaURL, generation string) {
	t.Helper()
	cfg := fmt.Sprintf("embedding:\n  base_url: %q\ngeneration:\n  model: %s\n  base_url: %q\n", ollamaURL, generation, ollamaURL)
	require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(cfg), 0o600))
}

// freshWorld is a Mac with Ollama and nothing else, whose launchd agent
// runs this test binary as the fake daemon, and whose curio-daemon is this
// test binary too: `curio up` can take it all the way.
func freshWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.home = filepath.Join(t.TempDir(), "curio")
	listen := freeAddr(t)
	w.daemonURL = "http://" + listen
	exe, err := os.Executable()
	require.NoError(t, err)
	w.bin = exe
	t.Setenv("CURIO_DAEMON_BIN", exe)
	t.Setenv(setuptest.DaemonVar, "1") // what a spawned curio-daemon runs as
	w.agent.Launch = func() *exec.Cmd {
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "CURIO_HOME="+w.home)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		return cmd
	}
	return w
}

// scripted makes the world's commands answer with a scripted UI.
func (w *world) scripted(ui setup.UI) {
	w.deps.newUI = func(io.Reader, io.Writer, bool) setup.UI { return ui }
}

// doctorLine is doctor's line for the check called name, and the hint
// under it, if any.
func doctorLine(t *testing.T, out, name string) (line, hint string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if len(l) > 2 && strings.HasPrefix(l[strings.Index(l, " ")+1:], fmt.Sprintf("%-22s ", name)) {
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "  → ") {
				hint = strings.TrimPrefix(lines[i+1], "  → ")
			}
			return l, hint
		}
	}
	t.Fatalf("no %q line in:\n%s", name, out)
	return "", ""
}

// markerOf is the status marker a doctor line starts with.
func markerOf(line string) string {
	marker, _, _ := strings.Cut(line, " ")
	return marker
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}
