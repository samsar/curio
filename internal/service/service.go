// Package service runs curio-daemon under the operating system's service
// manager: a per-user launchd agent on macOS, which starts the daemon at
// login and restarts it after a crash. Elsewhere there is none, and the
// clients start the daemon on demand (internal/daemonctl).
//
// The Manager interface is the seam daemonctl and `curio up` drive the
// agent through, and tests fake (servicetest). It lives here rather than in
// the setup wizard because daemonctl uses it too, and the wizard imports
// daemonctl. Which implementation runs is decided at run time, not by build
// tags, so the launchd code compiles, and its tests run, on every platform.
package service

import (
	"context"
	"errors"
	"runtime"
	"time"

	"github.com/samsar/curio/internal/curiohome"
)

var (
	// ErrUnsupported is returned by every change a Manager is asked to make
	// on a platform with no service manager curio supports.
	ErrUnsupported = errors.New("no service manager for curio-daemon on this platform")

	// ErrNoGUISession: the user has no desktop login session (logged in
	// only over ssh, say), which a launchd agent runs in. The clients start
	// the daemon themselves meanwhile.
	ErrNoGUISession = errors.New("no GUI login session to run the launchd agent in")
)

// ExitTimeout is how long launchd lets the daemon exit after SIGTERM before
// it sends SIGKILL (the plist's ExitTimeOut). launchd's own default is 5s;
// the daemon's shutdown takes up to 20s (5s for HTTP, 15s to drain the
// workers), and daemonctl waits 30s for a stopped daemon to release its
// lock, so 25s lets a draining daemon finish and still ends before the
// client gives up.
const ExitTimeout = 25 * time.Second

// Manager runs curio-daemon for one home under the service manager.
type Manager interface {
	// Status reports what the service manager knows. A service that isn't
	// installed, or isn't loaded, is a Status saying so, never an error.
	Status(ctx context.Context) (Status, error)
	// Preflight returns the error Install would fail with before changing
	// anything for spec, or nil, and changes nothing itself: a caller
	// checks it before stopping a daemon Install would replace.
	Preflight(ctx context.Context, spec Spec) error
	// Install writes the service definition for spec and loads it, which
	// starts the daemon. changed is false when the same definition was
	// already loaded and nothing was done.
	Install(ctx context.Context, spec Spec) (changed bool, err error)
	// Uninstall unloads the service, which stops the daemon, and removes
	// its definition. removed is false when there was nothing to remove.
	Uninstall(ctx context.Context) (removed bool, err error)
	// Start starts the loaded service unless it runs already, and returns
	// the PID of the process that runs, new or already running.
	Start(ctx context.Context) (pid int, err error)
	// Stop sends the running daemon SIGTERM and returns without waiting
	// for it to exit: whether it has is the daemon lock's to say
	// (daemonctl).
	Stop(ctx context.Context) error
	// Restart kills a running instance and starts a new one, returning
	// the PID it reports; that may still be the old instance's while the
	// swap is under way.
	Restart(ctx context.Context) (pid int, err error)
}

// Status is the service manager's view of the daemon's service.
type Status struct {
	// Supported is false where there is no service manager, and every
	// other field is then zero.
	Supported bool
	Label     string // the service's name
	Path      string // the service definition file
	// Installed: the definition file exists. Without it the service
	// manager isn't asked about the service at all.
	Installed bool
	// Program is the daemon executable the definition runs, as written
	// there; empty when the definition can't be read.
	Program string
	Loaded  bool   // the service manager has the service loaded
	State   string // the service manager's word for it ("running", "not running")
	PID     int    // the running daemon's; 0 when none runs
	// NoGUISession: the service is installed, but the user has no login
	// session for it to be loaded in (see ErrNoGUISession).
	NoGUISession bool
	// LastExit is how the service's last process ended, as the service
	// manager words it ("exit code 1", "signal Killed: 9"), for messages;
	// empty when it never ran or ended cleanly.
	LastExit string
}

// Running reports whether the service manager runs a daemon for the
// service.
func (s Status) Running() bool { return s.Loaded && s.PID > 0 }

// Spec is what Install installs.
type Spec struct {
	// Program is the absolute path of the curio-daemon to run, as
	// installed: never resolved through symlinks, so a package upgrade
	// that swaps the file behind a symlink needs no new definition.
	Program string
}

// ForHome returns the Manager for home's daemon on this platform: its
// launchd agent on macOS, and one that manages nothing elsewhere.
func ForHome(home *curiohome.Home) (Manager, error) {
	if runtime.GOOS != "darwin" {
		return Unsupported{}, nil
	}
	return NewLaunchd(home, LaunchdOptions{})
}
