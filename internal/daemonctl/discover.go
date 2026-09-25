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
// the running executable.
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
	base := cmp.Or(daemonURL, "http://"+cfg.Daemon.Listen)
	return Env{Home: home, Config: cfg, Client: client.New(base), Controller: New(home, bin, base)}, nil
}

// openHome opens the home override names, initializing it on first use.
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

// daemonBinary is $CURIO_DAEMON_BIN, or curio-daemon next to the running
// executable, which is where every install puts it.
func daemonBinary() (string, error) {
	if bin := os.Getenv("CURIO_DAEMON_BIN"); bin != "" {
		return bin, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate curio-daemon next to this executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "curio-daemon"), nil
}
