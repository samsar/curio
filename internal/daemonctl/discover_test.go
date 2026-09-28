package daemonctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/service"
	"github.com/samsar/curio/internal/service/servicetest"
)

func TestDiscover(t *testing.T) {
	t.Setenv("CURIO_DAEMON_BIN", "/opt/curio/curio-daemon")

	t.Run("initializes a new home and reads its listen address", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "home")
		env, err := Discover(path, "")
		require.NoError(t, err)
		assert.Equal(t, path, env.Home.Path)
		assert.FileExists(t, env.Home.MarkerPath())
		assert.Equal(t, "http://"+env.Config.Daemon.Listen, env.Controller.BaseURL)
		assert.Equal(t, "/opt/curio/curio-daemon", env.Controller.DaemonBin)
		assert.Same(t, env.Home, env.Controller.Home)

		meta, err := env.Home.Meta()
		require.NoError(t, err)
		assert.Equal(t, curiohome.CurrentFormat, meta.Format)
		assert.Equal(t, "qwen3-embedding:0.6b", meta.EmbeddingModel, "the default embedding model")
		assert.Equal(t, 1024, meta.EmbeddingDim)
	})

	t.Run("an explicit daemon URL wins", func(t *testing.T) {
		env, err := Discover(filepath.Join(t.TempDir(), "home"), "http://127.0.0.1:9999")
		require.NoError(t, err)
		assert.Equal(t, "http://127.0.0.1:9999", env.Controller.BaseURL)
	})

	t.Run("a relative home is made absolute", func(t *testing.T) {
		t.Chdir(t.TempDir())
		env, err := Discover("rel-home", "")
		require.NoError(t, err)
		wd, err := os.Getwd()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(wd, "rel-home"), env.Home.Path)
	})

	t.Run("no override means $CURIO_HOME", func(t *testing.T) {
		home, err := curiohome.Init(t.TempDir(), "nomic-embed-text", 768)
		require.NoError(t, err)
		t.Setenv("CURIO_HOME", home.Path)
		env, err := Discover("", "")
		require.NoError(t, err)
		assert.Equal(t, home.Path, env.Home.Path)
	})

	t.Run("a directory that isn't a curio home is refused", func(t *testing.T) {
		_, err := Discover(t.TempDir(), "")
		require.ErrorIs(t, err, curiohome.ErrNotOurs)
	})

	t.Run("the controller has the platform's service manager", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir()) // no agents of the user's own
		env, err := Discover(filepath.Join(t.TempDir(), "home"), "")
		require.NoError(t, err)
		require.NotNil(t, env.Controller.Service)
		if runtime.GOOS == "darwin" {
			assert.IsType(t, &service.Launchd{}, env.Controller.Service)
		} else {
			assert.Equal(t, service.Unsupported{}, env.Controller.Service)
		}
	})
}

// TestDiscover_DaemonBin: CURIO_DAEMON_BIN is made absolute, since a
// launchd agent has no working directory to resolve it in, and never
// resolved through a symlink, so the agent keeps running whatever the
// link points at after an upgrade.
func TestDiscover_DaemonBin(t *testing.T) {
	t.Run("relative", func(t *testing.T) {
		wd := t.TempDir()
		t.Chdir(wd)
		t.Setenv("CURIO_DAEMON_BIN", filepath.Join("bin", "curio-daemon"))
		env, err := Discover(filepath.Join(t.TempDir(), "home"), "")
		require.NoError(t, err)
		resolvedWD, err := os.Getwd()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(resolvedWD, "bin", "curio-daemon"), env.Controller.DaemonBin)
	})

	t.Run("a symlink reaches the agent as the symlink", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "curio-daemon")
		require.NoError(t, os.WriteFile(target, []byte("#!/bin/false\n"), 0o700))
		link := filepath.Join(t.TempDir(), "curio-daemon")
		require.NoError(t, os.Symlink(target, link))
		t.Setenv("CURIO_DAEMON_BIN", link)
		env, err := Discover(filepath.Join(t.TempDir(), "home"), "http://127.0.0.1:1")
		require.NoError(t, err)
		fake := servicetest.New(t, service.BaseLabel)
		fake.Fail("Install", errors.New("stop here"))
		env.Controller.Service = fake

		_, err = env.Controller.Install(context.Background())
		require.Error(t, err)
		require.Len(t, fake.Specs(), 1)
		assert.Equal(t, link, fake.Specs()[0].Program)
	})
}
