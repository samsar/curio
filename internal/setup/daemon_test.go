package setup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/service"
	"github.com/samsar/curio/internal/setup"
	"github.com/samsar/curio/internal/setup/setuptest"
	"github.com/samsar/curio/internal/version"
)

// healthz asks the harness's daemon who it is.
func healthz(t *testing.T, h *harness) *client.Health {
	t.Helper()
	health, err := client.New("http://" + h.listen).Healthz(context.Background())
	require.NoError(t, err)
	return health
}

// TestUp_DaemonAnotherBuild: a daemon of another build is restarted once,
// through its agent, and the new one runs this curio.
func TestUp_DaemonAnotherBuild(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	// The agent's daemon is now another build's: one an older curio left.
	t.Setenv(setuptest.DaemonVersionVar, "v0.9.0 (abc, 2026-09-01)")
	_, err := h.agent.Restart(context.Background())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		health, err := client.New("http://" + h.listen).Healthz(context.Background())
		return err == nil && health.Version == "v0.9.0 (abc, 2026-09-01)"
	}, 10*time.Second, 20*time.Millisecond)
	t.Setenv(setuptest.DaemonVersionVar, "")
	restarts := h.agent.Count("Restart")

	ui := setuptest.NewUI(t, setuptest.Yes().About("Restart"))
	plan, _, err := h.up(ui, setup.Options{})
	require.NoError(t, err, "%v", ui.Events())
	assert.Equal(t, []string{"daemon"}, stepsOf(plan.Fixes()))
	assert.Contains(t, item(t, plan, "daemon").Detail, "it runs curio v0.9.0 (abc, 2026-09-01), not this curio's "+version.String())
	assert.Equal(t, restarts+1, h.agent.Count("Restart"))
	assert.Equal(t, version.String(), healthz(t, h).Version)
	assert.Equal(t, h.agent.PID(), healthz(t, h).PID)
}

// TestUp_DaemonAnotherWritingModel: after generation.model changes in
// config.yaml, curio up pulls the new model and restarts the daemon once,
// and the new daemon writes with it.
func TestUp_DaemonAnotherWritingModel(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	cfgPath := filepath.Join(h.home, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath,
		[]byte(strings.Replace(mustRead(t, cfgPath), "model: "+genModel, "model: gemma4:12b", 1)), 0o600))

	ui := setuptest.NewUI(t, setuptest.Yes().About("Pull gemma4:12b"), setuptest.Yes().About("Restart"))
	plan, _, err := h.up(ui, setup.Options{})
	require.NoError(t, err, "%v", ui.Events())
	assert.Equal(t, []string{"models", "daemon"}, stepsOf(plan.Fixes()))
	assert.Contains(t, item(t, plan, "daemon").Detail, "it writes with gemma4:26b, not config.yaml's gemma4:12b")
	assert.Equal(t, 1, h.agent.Count("Restart"))
	assert.Equal(t, "gemma4:12b", healthz(t, h).GenerationModel)
}

// TestUp_DaemonNoGUISession: over ssh the agent can't be installed; the
// daemon is started on demand instead, with a warning, and the check
// passes with one.
func TestUp_DaemonNoGUISession(t *testing.T) {
	h := newHarness(t)
	h.agent.Fail("Preflight", service.ErrNoGUISession)
	ui := setuptest.NewUI(t, setuptest.Pick(0), setuptest.Yes().About("Start curio-daemon"))
	_, out, err := h.up(ui, setup.Options{})
	require.NoError(t, err, "%v", ui.Events())
	assert.Zero(t, h.agent.Count("Install"))
	assert.True(t, ui.Said("no GUI login session"))
	assert.False(t, out.Status.Daemon.Managed)
	assert.NotZero(t, out.Status.Daemon.PID)
	assert.Equal(t, os.Getenv("CURIO_DAEMON_BIN"), h.exe)
	assert.Equal(t, h.home, healthz(t, h).Home)

	plan, _, err := h.up(setuptest.NewUI(t), setup.Options{})
	require.NoError(t, err)
	assert.Nil(t, plan, "nothing to do")
	var warned bool
	for _, w := range out.Status.Warnings {
		warned = warned || strings.Contains(w.Text, "there is no GUI login session")
	}
	assert.True(t, warned, "%v", out.Status.Warnings)
}

// TestUp_DaemonProgramMissing: without its curio-daemon, the plan is
// blocked and nothing is stopped: the running daemon carries on.
func TestUp_DaemonProgramMissing(t *testing.T) {
	h := newHarness(t)
	h.firstRun()
	pid := h.agent.PID()
	t.Setenv("CURIO_DAEMON_BIN", filepath.Join(t.TempDir(), "curio-daemon"))

	plan, _, err := h.up(setuptest.NewUI(t), setup.Options{})
	var blocked *setup.BlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Contains(t, item(t, plan, "daemon").Detail, "curio-daemon is missing")
	assert.Equal(t, pid, h.agent.PID())
	assert.Zero(t, h.agent.Count("Stop"))
	assert.Zero(t, h.agent.Count("Uninstall"))
}

// TestUp_DaemonAgentRefuses: an agent the service manager would refuse
// for another reason than a missing GUI session blocks the plan.
func TestUp_DaemonAgentRefuses(t *testing.T) {
	h := newHarness(t)
	h.writeHome(genModel)
	h.agent.Fail("Preflight", errors.New("install the launchd agent as your own user, not root"))
	plan, _, err := h.up(setuptest.NewUI(t), setup.Options{})
	var blocked *setup.BlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Contains(t, item(t, plan, "daemon").Detail, "the launchd agent can't be installed: install the launchd agent as your own user")
}

// TestUp_DaemonStartedOutsideTheAgent: a daemon a command spawned is
// replaced by the agent's.
func TestUp_DaemonStartedOutsideTheAgent(t *testing.T) {
	h := newHarness(t)
	h.service = service.Unsupported{}
	ui := setuptest.NewUI(t, setuptest.Pick(0), setuptest.Yes())
	_, out, err := h.up(ui, setup.Options{})
	require.NoError(t, err)
	require.False(t, out.Status.Daemon.Managed)
	spawned := out.Status.Daemon.PID

	h.service = h.agent
	plan, out, err := h.up(setuptest.NewUI(t, setuptest.Yes().About("launchd agent")), setup.Options{})
	require.NoError(t, err)
	assert.Contains(t, item(t, plan, "daemon").Detail, "no launchd agent keeps it running")
	assert.True(t, out.Status.Daemon.Managed)
	assert.NotEqual(t, spawned, out.Status.Daemon.PID)
}

// TestDriftWarning: the warning line says how sure the drift is and quotes
// the daemon's evidence; a daemon that predates verifying a change gets
// the line it always did.
func TestDriftWarning(t *testing.T) {
	changes := []client.DriftChange{
		{What: client.DriftModelDigest, Recorded: "sha256:0a109f42", Current: "sha256:ac6da0df"},
		{What: client.DriftOllamaVersion, Recorded: "0.30.0", Current: "0.34.4"},
	}
	const changed = "model digest sha256:0a109f42 → sha256:ac6da0df, Ollama 0.30.0 → 0.34.4"
	for name, tc := range map[string]struct {
		verification *client.DriftVerification
		want         string
	}{
		"verified": {
			&client.DriftVerification{Verified: true, Detail: "64 of 64 sampled chunks changed (worst cosine 0.9713)"},
			"warning: embeddings drifted since the library was indexed (" + changed +
				"; 64 of 64 sampled chunks changed (worst cosine 0.9713)); run `curio reindex --all`",
		},
		"unverified": {
			&client.DriftVerification{Detail: "not verified: after 3 attempts: ollama unreachable"},
			"warning: embeddings may have drifted since the library was indexed (" + changed +
				"; not verified: after 3 attempts: ollama unreachable); run `curio reindex --all`",
		},
		"an older daemon": {
			nil,
			"warning: embeddings drifted since the library was indexed (" + changed + "); run `curio reindex --all`",
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := &client.EmbeddingDrift{Changes: changes, Verification: tc.verification, Fix: "curio reindex --all"}
			assert.Equal(t, tc.want, setup.DriftWarning(d))
		})
	}
}
