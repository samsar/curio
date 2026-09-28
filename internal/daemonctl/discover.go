package daemonctl

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/service"
)

// Env is what a client of the daemon works with: a home, its config, and a
// client and controller for the daemon that serves it.
type Env struct {
	Home       *curiohome.Home
	Config     config.Config
	Client     *client.Client
	Controller *Controller
}

// Discover finds the home and the daemon that serves it. The CLI and the MCP
// sidecar both start here, so they can't drift apart, and nothing here
// touches the process environment.
//
// homeOverride names the home; empty means $CURIO_HOME, then ~/.curio. A
// home that doesn't exist yet is initialized with the default embedding
// model and dimension, which the daemon checks against config.yaml when it
// starts. daemonURL overrides the address config.yaml's daemon.listen
// gives. The daemon binary is $CURIO_DAEMON_BIN, or curio-daemon next to
// the running executable. The controller starts the daemon through the
// home's launchd agent when one is loaded (service.ForHome).
func Discover(homeOverride, daemonURL string) (Env, error) {
	home, err := openHome(homeOverride)
	if err != nil {
		return Env{}, err
	}
	cfg, err := config.Load(home.ConfigPath())
	if err != nil {
		return Env{}, err
	}
	bin, err := daemonBinary()
	if err != nil {
		return Env{}, err
	}
	svc, err := service.ForHome(home)
	if err != nil {
		return Env{}, err
	}
	base := cmp.Or(daemonURL, "http://"+cfg.Daemon.Listen)
	ctl := New(home, bin, base)
	ctl.Service = svc
	return Env{Home: home, Config: cfg, Client: client.New(base), Controller: ctl}, nil
}

// openHome opens the home override names, initializing it on first use with
// the default embedding model and width.
func openHome(override string) (*curiohome.Home, error) {
	path, err := homePath(override)
	if err != nil {
		return nil, err
	}
	home, err := curiohome.Open(path)
	if !errors.Is(err, curiohome.ErrNotInitialized) {
		return home, err
	}
	defaults := config.Default().Embedding
	home, err = curiohome.Init(path, defaults.Model, defaults.Dim)
	if err != nil {
		return nil, fmt.Errorf("initialize %s: %w", path, err)
	}
	return home, nil
}

// homePath is override made absolute, or the home curiohome.Resolve finds.
// The daemon is handed this path, so a relative one must not depend on the
// directory the daemon happens to start in.
func homePath(override string) (string, error) {
	if override == "" {
		return curiohome.Resolve()
	}
	path, err := filepath.Abs(override)
	if err != nil {
		return "", fmt.Errorf("resolve home %q: %w", override, err)
	}
	return path, nil
}

// daemonBinary is $CURIO_DAEMON_BIN, made absolute, or curio-daemon next to
// the running executable, which is where every install puts it. Neither is
// resolved through symlinks: a launchd agent installed with the path runs
// whatever it points at after an upgrade (/opt/homebrew/bin/curio-daemon,
// not a versioned Cellar directory), and on macOS os.Executable already
// reports the path this process was started by, symlink included.
func daemonBinary() (string, error) {
	if bin := os.Getenv("CURIO_DAEMON_BIN"); bin != "" {
		abs, err := filepath.Abs(bin)
		if err != nil {
			return "", fmt.Errorf("resolve CURIO_DAEMON_BIN=%q: %w", bin, err)
		}
		return abs, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate curio-daemon next to this executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "curio-daemon"), nil
}
