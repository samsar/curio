package service

import (
	"context"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/curiohome"
)

// TestUnsupported: where there is no service manager, the status says so
// without running anything, and every change fails with ErrUnsupported,
// saying what the CLI does instead.
func TestUnsupported(t *testing.T) {
	ctx := context.Background()
	m := Unsupported{}
	st, err := m.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, Status{}, st)
	assert.False(t, st.Supported)

	_, installErr := m.Install(ctx, Spec{Program: "/opt/homebrew/bin/curio-daemon"})
	_, uninstallErr := m.Uninstall(ctx)
	_, startErr := m.Start(ctx)
	_, restartErr := m.Restart(ctx)
	for name, err := range map[string]error{
		"preflight": m.Preflight(ctx, Spec{Program: "/opt/homebrew/bin/curio-daemon"}),
		"install":   installErr, "uninstall": uninstallErr, "start": startErr,
		"stop": m.Stop(ctx), "restart": restartErr,
	} {
		require.ErrorIs(t, err, ErrUnsupported, name)
		assert.Contains(t, err.Error(), "launchd agents are macOS-only", name)
		assert.Contains(t, err.Error(), "the CLI starts the daemon on demand", name)
	}
}

// TestForHome: macOS gets the home's launchd agent, every other platform
// the manager that manages nothing.
func TestForHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home, err := curiohome.Init(t.TempDir(), "qwen3-embedding:0.6b", 1024)
	require.NoError(t, err)
	m, err := ForHome(home)
	require.NoError(t, err)
	if runtime.GOOS != "darwin" {
		assert.Equal(t, Unsupported{}, m)
		return
	}
	l, ok := m.(*Launchd)
	require.True(t, ok, "%T", m)
	assert.Equal(t, defaultLaunchctl, l.opts.Launchctl)
	st, err := m.Status(context.Background())
	require.NoError(t, err, "no plist: launchctl isn't run")
	assert.True(t, st.Supported)
	assert.False(t, st.Installed)
}

func TestStatus_Running(t *testing.T) {
	assert.True(t, Status{Loaded: true, PID: 42}.Running())
	assert.False(t, Status{Loaded: true}.Running())
	assert.False(t, Status{PID: 42}.Running(), "a PID launchd doesn't vouch for")
}
