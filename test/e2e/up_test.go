//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/daemonctl"
	"github.com/samsar/curio/internal/service"
	"github.com/samsar/curio/internal/service/servicetest"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/version"
)

// upMachine is a Mac for `curio up --yes` to set up, with no launchd in
// it: an Ollama (setuptest's) that has no models, an installer that finds
// Homebrew, a 64 GiB Apple silicon Mac, and a launchd agent
// (servicetest's) that launches the curio-daemon TestMain built, with
// CURIO_HOME set and its output in daemon.log, as launchd would.
type upMachine struct {
	t      *testing.T
	home   string
	listen string
	ollama *setuptest.Ollama
	agent  *servicetest.Fake
	deps   setup.Deps
}

func newUpMachine(t *testing.T) *upMachine {
	t.Helper()
	m := &upMachine{
		t:      t,
		home:   filepath.Join(t.TempDir(), "curio"),
		listen: freeLoopbackAddr(t),
		ollama: setuptest.NewOllama(t, "0.34.4"),
		agent:  servicetest.New(t, service.BaseLabel+".e2e"),
	}
	t.Setenv("CURIO_DAEMON_BIN", daemonBin)
	m.agent.Launch = func() *exec.Cmd {
		cmd := exec.Command(daemonBin)
		cmd.Env = append(os.Environ(), "CURIO_HOME="+m.home)
		if log, err := os.OpenFile(filepath.Join(m.home, curiohome.LogsDirName, curiohome.DaemonLogFile),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			cmd.Stdout, cmd.Stderr = log, log
		}
		return cmd
	}
	defaults := config.Default()
	defaults.Daemon.Listen = m.listen
	defaults.Embedding.BaseURL, defaults.Generation.BaseURL = m.ollama.URL, m.ollama.URL
	m.deps = setup.Deps{
		// The UI of `curio up --yes` without a terminal.
		UI:        setup.NewUI(strings.NewReader(""), testLog{t}, true),
		Probe:     setuptest.NewProbe(),
		Installer: setuptest.NewInstaller(),
		Connect: func(home *curiohome.Home, cfg config.Config, daemonURL string) (daemonctl.Env, error) {
			env, err := daemonctl.Connect(home, cfg, daemonURL)
			if err == nil {
				env.Controller.Service = m.agent
			}
			return env, err
		},
		Defaults: &defaults,
		Sources:  setuptest.NoSources,
	}
	return m
}

// up runs `curio up --yes`, with --fresh when fresh.
func (m *upMachine) up(fresh bool) (setup.Outcome, error) {
	m.t.Helper()
	return m.upWith(setup.Options{Fresh: fresh})
}

// upWith runs `curio up --yes` with opts.
func (m *upMachine) upWith(opts setup.Options) (setup.Outcome, error) {
	m.t.Helper()
	opts.Home, opts.Yes = m.home, true
	r, err := setup.New(opts, m.deps)
	require.NoError(m.t, err)
	return r.Run(context.Background(), func(setup.Plan) {})
}

// changes counts the agent's calls that change something.
func (m *upMachine) changes() int {
	n := 0
	for _, method := range []string{"Install", "Uninstall", "Start", "Stop", "Restart"} {
		n += m.agent.Count(method)
	}
	return n
}

// testLog is a writer into the test's log.
type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// files reads every file under dir.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		out[rel] = string(b)
		return err
	}))
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestUp: `curio up --yes` on a Mac with only Ollama ends with the
// daemon the agent launched serving a new home at the default model's
// width; a second run has nothing to do; `--fresh` sets the home aside
// whole and makes a new one.
func TestUp(t *testing.T) {
	ctx := context.Background()
	m := newUpMachine(t)
	home := func() *curiohome.Home {
		h, err := curiohome.Open(m.home)
		require.NoError(t, err)
		return h
	}
	logs := func() string {
		b, err := os.ReadFile(filepath.Join(m.home, curiohome.LogsDirName, curiohome.DaemonLogFile))
		if err != nil {
			return "(no daemon log: " + err.Error() + ")"
		}
		return string(b)
	}

	out, err := m.up(false)
	require.NoError(t, err, logs())
	assert.True(t, out.Changed)
	c := client.New("http://" + m.listen)
	health, err := c.Healthz(ctx)
	require.NoError(t, err, logs())
	assert.Equal(t, m.agent.PID(), health.PID, "the agent's daemon serves")
	assert.Equal(t, version.String(), health.Version)
	assert.Equal(t, "gemma4:26b", health.GenerationModel)
	assert.True(t, daemonctl.SameHome(m.home, health.Home))
	cfg, err := config.Load(home().ConfigPath())
	require.NoError(t, err)
	assert.Equal(t, m.listen, cfg.Daemon.Listen)
	meta, err := home().Meta()
	require.NoError(t, err)
	assert.Equal(t, curiohome.CurrentFormat, meta.Format)
	assert.Equal(t, "qwen3-embedding:0.6b", meta.EmbeddingModel)
	assert.Equal(t, 1024, meta.EmbeddingDim)
	assert.True(t, out.Status.Daemon.Managed)

	changes, pulls := m.changes(), len(m.ollama.Pulls())
	out, err = m.up(false)
	require.NoError(t, err, logs())
	assert.False(t, out.Changed, "nothing to do")
	assert.True(t, out.Plan.Empty())
	assert.Equal(t, changes, m.changes())
	assert.Len(t, m.ollama.Pulls(), pulls)

	// Stop the daemon first, so the files set aside can be compared with
	// what the old home holds at rest.
	require.NoError(t, m.agent.Stop(ctx))
	require.Eventually(t, func() bool { return m.agent.PID() == 0 }, 30*time.Second, 50*time.Millisecond,
		"the daemon exited")
	before := files(t, m.home)
	require.Contains(t, before, curiohome.DBFile)
	uninstalls, installs := m.agent.Count("Uninstall"), m.agent.Count("Install")

	out, err = m.up(true)
	require.NoError(t, err, logs())
	assert.True(t, out.Changed)
	backups, err := filepath.Glob(m.home + ".bak-*")
	require.NoError(t, err)
	require.Len(t, backups, 1)
	after := files(t, backups[0])
	assert.ElementsMatch(t, keys(before), keys(after))
	for name, body := range before {
		assert.True(t, body == after[name], "%s is what it was", name)
	}
	assert.Equal(t, uninstalls+1, m.agent.Count("Uninstall"))
	assert.Equal(t, installs+1, m.agent.Count("Install"))
	health, err = c.Healthz(ctx)
	require.NoError(t, err, logs())
	assert.Equal(t, m.agent.PID(), health.PID)
	assert.True(t, daemonctl.SameHome(m.home, health.Home), "a new home serves")
	meta, err = home().Meta()
	require.NoError(t, err)
	assert.Equal(t, 1024, meta.EmbeddingDim)
	assert.NotEqual(t, before[curiohome.MarkerFile], files(t, m.home)[curiohome.MarkerFile], "a new marker")
}

// TestDaemon_RefusedHomeExitsZero: the built daemon, given a home it
// refuses (a legacy one), exits 0, so a launchd agent doesn't relaunch it
// every 10 seconds, and says once, at ERROR, that it stays down.
func TestDaemon_RefusedHomeExitsZero(t *testing.T) {
	home := newHome(t, freeLoopbackAddr(t), "http://127.0.0.1:1")
	require.NoError(t, os.WriteFile(home.MarkerPath(),
		[]byte(`{"schema_version":11,"embedding_model":"nomic-embed-text","embedding_dim":768}`), 0o600))
	cmd := exec.Command(daemonBin)
	cmd.Env = append(os.Environ(), "CURIO_HOME="+home.Path)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "exit status 0:\n%s", out)
	assert.Equal(t, 1, strings.Count(string(out), `"level":"ERROR"`), "%s", out)
	assert.Contains(t, string(out), "stays down until the cause is fixed")
	assert.Contains(t, string(out), "curio home from an older curio")
}

// TestUp_Import: `curio up --yes --import html:<file>` on a Mac with only
// Ollama sets curio up and imports the file into the real daemon: the
// queue opened at full speed with keep-awake left off, then the three
// pages' bookmarks, documents and fetch jobs, the bookmarklet filtered and
// the duplicate skipped. The runs after it, with --import or without, have
// nothing to do.
func TestUp_Import(t *testing.T) {
	ctx := context.Background()
	pages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, articleHTML())
	}))
	t.Cleanup(pages.Close)
	var export strings.Builder
	export.WriteString("<!DOCTYPE NETSCAPE-Bookmark-file-1>\n<DL><p>\n")
	for _, href := range []string{pages.URL + "/one", pages.URL + "/two", "javascript:alert(1)", pages.URL + "/three",
		pages.URL + "/one"} {
		fmt.Fprintf(&export, "<DT><A HREF=%q>A page</A>\n", href)
	}
	export.WriteString("</DL><p>\n")
	file := filepath.Join(t.TempDir(), "bookmarks.html")
	require.NoError(t, os.WriteFile(file, []byte(export.String()), 0o600))

	m := newUpMachine(t)
	t.Cleanup(func() { _ = m.agent.Stop(context.Background()) })
	out, err := m.upWith(setup.Options{Import: "html:" + file})
	require.NoError(t, err)
	require.NotNil(t, out.Imported)
	assert.Equal(t, 3, out.Imported.Created)
	assert.Equal(t, 3, out.Imported.JobsEnqueued)
	assert.Equal(t, 1, out.Imported.Filtered)
	assert.Equal(t, 3, out.Imported.Pages)
	assert.Equal(t, setup.PaceFull, out.Imported.Pace)
	assert.False(t, out.Imported.KeepAwake)
	assert.False(t, out.Imported.CheckBack.IsZero())

	c := client.New("http://" + m.listen)
	stats, err := c.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, stats.BookmarksTotal)
	assert.Equal(t, 3, stats.DocumentsTotal)
	q, err := c.Queue(ctx)
	require.NoError(t, err)
	assert.False(t, q.Paused)
	assert.Equal(t, client.ThrottleNormal, q.Throttle)
	assert.Empty(t, q.Schedule)
	assert.False(t, q.KeepAwake)
	jobs, err := c.ListJobs(ctx, client.JobListOpts{Kind: string(store.JobKindFetch)})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 3)

	for _, opts := range []setup.Options{{}, {Import: "html:" + file}} {
		out, err = m.upWith(opts)
		require.NoError(t, err)
		assert.True(t, out.Plan.Empty(), "%+v", out.Plan)
		assert.False(t, out.Changed)
		assert.Nil(t, out.Imported)
	}
}
