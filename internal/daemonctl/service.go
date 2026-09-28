package daemonctl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/service"
)

// ErrVersionMismatch: the daemon serving this home reports another version
// than the caller's, and restarting it didn't change that.
var ErrVersionMismatch = errors.New("curio-daemon runs another version")

// errNoManager is Install's and Uninstall's error for a controller given
// no service manager (New's).
var errNoManager = fmt.Errorf("%w: this controller spawns the daemon itself", service.ErrUnsupported)

// Install installs the daemon's agent with the service manager, so the
// daemon starts at login and restarts after a crash, and returns once the
// agent's daemon serves. changed is false when the same agent was already
// loaded.
//
// It holds daemon.start.lock while it works, so no auto-starter spawns a
// daemon meanwhile, and a daemon running outside the agent is stopped
// first: it would keep the lock from the agent's. The lock is released
// before the wait for the agent's daemon, which EnsureRunning does, taking
// it itself if the daemon needs starting. It doesn't check that the daemon
// will serve the home; callers do that first (`curio daemon install` and
// `curio up` do).
func (c *Controller) Install(ctx context.Context) (changed bool, err error) {
	if c.Service == nil {
		return false, errNoManager
	}
	startLock, err := lockStart(ctx, c.Home.StartLockFile())
	if err != nil {
		return false, err
	}
	changed, err = c.install(ctx)
	startLock.Close()
	if err != nil {
		return false, err
	}
	return changed, c.EnsureRunning(ctx)
}

// install is Install's part under the start lock.
func (c *Controller) install(ctx context.Context) (bool, error) {
	st, err := c.Status(ctx)
	switch {
	case err != nil:
		return false, err
	case st.ServiceErr != nil:
		return false, st.ServiceErr
	case st.State == Legacy:
		return false, c.legacyStopError(st.PID)
	case st.State == Running && !st.Managed():
		if _, err := c.stopRunning(ctx, st); err != nil {
			return false, fmt.Errorf("stop the running curio-daemon, so its launchd agent's can take over: %w", err)
		}
	}
	return c.Service.Install(ctx, service.Spec{Program: c.DaemonBin})
}

// Uninstall removes the daemon's agent from the service manager, which
// stops the agent's daemon, and returns once that daemon has released
// daemon.pid: a caller may move the home afterwards. A daemon running
// outside the agent is left alone. removed is false when no agent was
// installed.
func (c *Controller) Uninstall(ctx context.Context) (removed bool, err error) {
	if c.Service == nil {
		return false, errNoManager
	}
	startLock, err := lockStart(ctx, c.Home.StartLockFile())
	if err != nil {
		return false, err
	}
	defer startLock.Close()
	st, err := c.Status(ctx)
	if err != nil {
		return false, err
	}
	if st.ServiceErr != nil {
		return false, st.ServiceErr
	}
	removed, err = c.Service.Uninstall(ctx)
	if err != nil {
		return false, err
	}
	if st.Managed() {
		if err := c.waitReleased(ctx, st.PID); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// Restart stops the daemon and starts a new one: through the service
// manager when it runs the daemon, and otherwise with Stop, lock-verified,
// then EnsureRunning. It returns once the new daemon serves. The daemon
// reads its config and looks for tools such as yt-dlp only at startup, so
// changing either takes a restart.
func (c *Controller) Restart(ctx context.Context) error {
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if st.Managed() {
		return c.restartManaged(ctx, st.PID)
	}
	if _, err := c.stopRunning(ctx, st); err != nil {
		return err
	}
	return c.EnsureRunning(ctx)
}

// restartManaged has the service manager replace its daemon, old, and
// waits for the new one to serve. Whatever PID the manager reports, the
// old daemon's hold on the lock and its answers are never taken for the
// new one's: it answers until it has shut down, and a manager asked
// mid-swap may still name it.
func (c *Controller) restartManaged(ctx context.Context, old int) error {
	startLock, err := lockStart(ctx, c.Home.StartLockFile())
	if err != nil {
		return err
	}
	ch := &child{launched: true, since: time.Now(), stale: old, startLock: startLock}
	defer ch.releaseStartLock()
	pid, err := c.Service.Restart(ctx)
	if err != nil {
		return err
	}
	if pid != old {
		ch.pid = pid
	}
	if err := c.waitReleased(ctx, old); err != nil {
		return err
	}
	w := &waiter{c: c, goal: untilReady}
	return w.wait(ctx, ch)
}

// EnsureVersion makes sure a daemon serving this home reports version
// want, the caller's own (version.String()): it starts one when none runs
// (EnsureRunning) and restarts, once, one that reports another version,
// such as the daemon of the curio just upgraded or rebuilt. restarted says
// whether it restarted one. A daemon still reporting another version
// afterwards fails with ErrVersionMismatch, naming what runs. A daemon
// from before the lock protocol can't be verified, so it is never
// restarted; the error says how to stop it by hand.
func (c *Controller) EnsureVersion(ctx context.Context, want string) (restarted bool, err error) {
	if err := c.EnsureRunning(ctx); err != nil {
		return false, err
	}
	h, err := client.New(c.BaseURL).Healthz(ctx)
	if err != nil {
		return false, err
	}
	if h.Version == want {
		return false, nil
	}
	if !hasIdentity(h) {
		st, err := c.Status(ctx)
		if err != nil {
			return false, err
		}
		return false, fmt.Errorf("it reports version %s, not %s: %w", h.Version, want, c.legacyStopError(st.PID))
	}
	if err := c.Restart(ctx); err != nil {
		return false, fmt.Errorf("restart curio-daemon %s to run %s: %w", h.Version, want, err)
	}
	h, err = client.New(c.BaseURL).Healthz(ctx)
	if err != nil {
		return true, err
	}
	if h.Version != want {
		return true, fmt.Errorf("%w: restarted, it reports version %s, not %s; it runs %s, which is another build than this curio",
			ErrVersionMismatch, h.Version, want, c.program(ctx))
	}
	return true, nil
}

// program is the daemon executable that runs for this home: the one the
// service manager's agent names when it runs the daemon, and DaemonBin
// otherwise.
func (c *Controller) program(ctx context.Context) string {
	st, err := c.Status(ctx)
	if err == nil && st.Managed() && st.Service.Program != "" {
		return st.Service.Program
	}
	return c.DaemonBin
}
