package daemonctl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/curiohome"
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
}
