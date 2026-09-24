// Package daemonctl manages the curio-daemon process lifecycle for its
// clients (the CLI and the MCP sidecar): finding a running daemon, starting
// one, stopping it.
//
// Liveness comes from the daemon's lock on $CURIO_HOME/daemon.pid (see
// lock.go); identity comes from /v1/healthz, which reports the answering
// daemon's PID and home. A PID is only ever signalled when the lock vouches
// for it.
package daemonctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/curiohome"
)

const (
	// defaultStartTimeout covers migrations on a large database; a daemon
	// that dies during startup is reported as soon as it exits.
	defaultStartTimeout = 15 * time.Second
	// defaultStopTimeout exceeds the daemon's own shutdown budget (20s, see
	// cmd/curio-daemon), so a healthy daemon always finishes first.
	defaultStopTimeout = 30 * time.Second

	pollInterval = 100 * time.Millisecond
	logTailLines = 20
)

// errHolderExited: the daemon being waited for released the lock without
// ever serving. It was shutting down, or failed to start.
var errHolderExited = errors.New("the daemon holding the lock exited before it began serving")

// State is the daemon's run state as seen from one home.
type State int

const (
	// NotRunning: no daemon holds the lock and the PID file is empty.
	NotRunning State = iota
	// Running: a daemon holds the lock.
	Running
	// Stale: no daemon holds the lock, but daemon.pid still names a PID,
	// left by a crashed daemon or an older CLI. It is never signalled: by
	// now it may belong to an unrelated process.
	Stale
	// Legacy: a daemon from before the lock protocol answers at BaseURL. It
	// holds no lock and reports no identity, so it can't be verified.
	Legacy
)

// Status is a snapshot of the daemon's state.
type Status struct {
	State State
	// PID is the lock holder's PID when Running (0 while it is still
	// starting), otherwise the value left in daemon.pid.
	PID int
	// Health is what answered at BaseURL, or nil if nothing did.
	Health *client.Health
}

// Controller manages the daemon for one home.
type Controller struct {
	Home         *curiohome.Home
	DaemonBin    string        // absolute path to the curio-daemon executable
	BaseURL      string        // where the daemon serves, for healthz probing
	StartTimeout time.Duration // how long a spawned daemon has to become healthy
	StopTimeout  time.Duration // how long a signalled daemon has to release its lock
}

func New(home *curiohome.Home, daemonBin, baseURL string) *Controller {
	return &Controller{
		Home:         home,
		DaemonBin:    daemonBin,
		BaseURL:      baseURL,
		StartTimeout: defaultStartTimeout,
		StopTimeout:  defaultStopTimeout,
	}
}

// Status reads the lock for liveness and healthz for identity.
func (c *Controller) Status(ctx context.Context) (Status, error) {
	held, pid, err := probeLock(c.Home.PIDFile())
	if err != nil {
		return Status{}, err
	}
	st := Status{PID: pid, Health: c.probeHealth(ctx)}
	switch {
	case held:
		st.State = Running
	case st.Health != nil && !hasIdentity(st.Health):
		st.State = Legacy
	case pid != 0:
		st.State = Stale
	default:
		st.State = NotRunning
	}
	return st, nil
}

// EnsureRunning returns once a daemon serving this home answers at BaseURL,
// spawning one if none is running. Concurrent callers are serialized so only
// one of them spawns.
func (c *Controller) EnsureRunning(ctx context.Context) error {
	if up, err := c.answering(ctx); up || err != nil {
		return err
	}

	startLock, err := lockStart(c.Home.StartLockFile())
	if err != nil {
		return err
	}
	defer startLock.Close()

	// Another starter may have finished while we waited for the start lock.
	if up, err := c.answering(ctx); up || err != nil {
		return err
	}
	held, _, err := probeLock(c.Home.PIDFile())
	if err != nil {
		return err
	}
	if held {
		// A daemon is up but not serving yet: migrating, or started by hand.
		err := c.waitReady(ctx, 0, nil)
		if !errors.Is(err, errHolderExited) {
			return err
		}
		// It went away instead (a `curio daemon stop` draining, say), and
		// we still hold the start lock: start our own.
	}
	return c.spawn(ctx)
}

// Stop sends SIGTERM to the daemon holding this home's lock and waits for it
// to release the lock. A daemon that isn't running is not an error.
func (c *Controller) Stop(ctx context.Context) error {
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	switch st.State {
	case NotRunning, Stale:
		return nil
	case Legacy:
		return c.legacyStopError(st.PID)
	}

	if st.PID == 0 {
		return errors.New("the daemon is still starting and hasn't recorded its pid; try again in a moment")
	}
	if h := st.Health; h != nil && (h.PID != st.PID || !SameHome(h.Home, c.Home.Path)) {
		return fmt.Errorf("%s answers as pid %d for %s, but this home's lock is held by pid %d; not signalling either",
			c.BaseURL, h.PID, h.Home, st.PID)
	}
	if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal daemon (pid %d): %w", st.PID, err)
	}
	return c.waitReleased(ctx, st.PID)
}

// SameHome reports whether two CURIO_HOME paths name the same directory,
// resolving symlinks (on macOS /tmp is /private/tmp).
func SameHome(a, b string) bool {
	return canonicalPath(a) == canonicalPath(b)
}

// canonicalPath resolves symlinks where it can. A path that doesn't exist
// here (a home on another machine's filesystem) is compared as written.
func canonicalPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// answering reports whether a daemon for this home already serves BaseURL.
// A daemon for a different home there is an error: a new daemon could only
// fail to bind the same port.
func (c *Controller) answering(ctx context.Context) (bool, error) {
	h := c.probeHealth(ctx)
	switch {
	case h == nil:
		return false, nil
	case !hasIdentity(h):
		// A pre-lock daemon: its home can't be checked, but spawning a
		// second daemon would only fail to bind. Keep using it so an
		// upgrade doesn't strand anyone; `curio daemon stop` explains
		// how to retire it.
		return true, nil
	case !SameHome(h.Home, c.Home.Path):
		return true, fmt.Errorf("%s is served by the curio-daemon for %s, not %s; "+
			"stop that daemon or give this home a different daemon.listen port", c.BaseURL, h.Home, c.Home.Path)
	default:
		return true, nil
	}
}

// probeHealth returns what answers healthz at BaseURL, or nil. An error
// means nothing usable answered, which is an ordinary state here. The probe
// is bounded by client.Healthz, which allows for the daemon's own wait on
// Ollama.
func (c *Controller) probeHealth(ctx context.Context) *client.Health {
	h, err := client.New(c.BaseURL).Healthz(ctx)
	if err != nil {
		return nil
	}
	return h
}

func hasIdentity(h *client.Health) bool {
	return h.PID != 0 && h.Home != ""
}

func (c *Controller) spawn(ctx context.Context) error {
	if err := os.MkdirAll(c.Home.LogsDir(), 0o700); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}
	logFile, err := os.OpenFile(c.logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}

	cmd := exec.Command(c.DaemonBin)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Setsid so the daemon survives the CLI exit.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	startErr := cmd.Start()
	// The child has its own descriptor now; ours has no further use.
	logFile.Close()
	if startErr != nil {
		return fmt.Errorf("start %s: %w", c.DaemonBin, startErr)
	}

	// Wait reaps the child and tells us at once if it dies during startup.
	// If we return first, the daemon carries on, reparented to init.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return c.waitReady(ctx, cmd.Process.Pid, exited)
}

// waitReady polls healthz until the daemon for this home answers: the child
// we spawned (childPID), or whichever daemon holds the lock. exited delivers
// the child's exit; nil when there is no child to watch. With no child left,
// the lock holder is what's being waited for, and its exit without serving
// ends the wait with errHolderExited.
func (c *Controller) waitReady(ctx context.Context, childPID int, exited <-chan error) error {
	deadline := time.NewTimer(c.StartTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		if c.ready(ctx, childPID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			cause := fmt.Errorf("no healthy response at %s within %s", c.BaseURL, c.StartTimeout)
			if exited == nil {
				// The daemon being waited for isn't one we spawned, so
				// its startup isn't ours to report on.
				return fmt.Errorf("waiting for the curio-daemon already starting for %s: %w "+
					"(`curio daemon status` shows its pid)", c.Home.Path, cause)
			}
			return c.startFailed(cause)
		case exitErr := <-exited:
			cause := childExitCause(exitErr)
			held, _, err := probeLock(c.Home.PIDFile())
			if err != nil {
				return errors.Join(c.startFailed(cause), err)
			}
			if !held {
				return c.startFailed(cause)
			}
			// The child lost the lock to a daemon that is still starting
			// (one started by hand, say). Wait for that one instead.
			exited = nil
		case <-tick.C:
			if exited != nil {
				continue // the child's exit is reported on its own
			}
			held, _, err := probeLock(c.Home.PIDFile())
			if err != nil {
				return err
			}
			if !held {
				if childPID == 0 {
					return errHolderExited
				}
				return c.startFailed(errHolderExited)
			}
		}
	}
}

// childExitCause describes a spawned daemon that exited before serving.
// Wait reports a clean exit as nil: the daemon was told to stop while it
// was still starting.
func childExitCause(waitErr error) error {
	if waitErr == nil {
		return errors.New("exited with status 0 before it began serving")
	}
	return waitErr
}

// ready reports whether healthz is answered by a daemon for this home that is
// either the spawned child or the current lock holder.
func (c *Controller) ready(ctx context.Context, childPID int) bool {
	h := c.probeHealth(ctx)
	if h == nil || !hasIdentity(h) || !SameHome(h.Home, c.Home.Path) {
		return false
	}
	if h.PID == childPID {
		return true
	}
	held, holder, err := probeLock(c.Home.PIDFile())
	return err == nil && held && h.PID == holder
}

// waitReleased waits for the signalled daemon (pid) to drop its lock, which
// it does only after its shutdown has finished. The lock held under another
// PID means the same thing: that daemon is gone, and another client has
// already started the next one.
func (c *Controller) waitReleased(ctx context.Context, pid int) error {
	deadline := time.Now().Add(c.StopTimeout)
	for {
		held, holder, err := probeLock(c.Home.PIDFile())
		if err != nil {
			return err
		}
		// Holder 0 is a handover in progress (the old daemon emptying
		// the file, or the new one yet to record itself): look again.
		if !held || (holder != pid && holder != 0) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon (pid %d) is still running %s after SIGTERM; see %s", pid, c.StopTimeout, c.logPath())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (c *Controller) legacyStopError(pid int) error {
	how := "find its pid with `lsof -nP -iTCP -sTCP:LISTEN`"
	if u, err := url.Parse(c.BaseURL); err == nil && u.Port() != "" {
		how = fmt.Sprintf("find its pid with `lsof -nP -iTCP:%s -sTCP:LISTEN`", u.Port())
	}
	if pid > 0 {
		how = fmt.Sprintf("its pid file says %d: confirm with `ps -p %d`, then `kill %d`", pid, pid, pid)
	}
	return fmt.Errorf("a curio-daemon from an older version is serving %s; it can't be verified, "+
		"so it won't be signalled automatically. Stop it by hand (%s); the next command starts a current daemon",
		c.BaseURL, how)
}

// startFailed builds the error for a daemon that didn't come up, with the
// tail of its log: that's where a crashing daemon explains itself.
func (c *Controller) startFailed(cause error) error {
	tail, err := logTail(c.logPath(), logTailLines)
	if err != nil {
		return fmt.Errorf("curio-daemon failed to start: %w (log %s unreadable: %w)", cause, c.logPath(), err)
	}
	return fmt.Errorf("curio-daemon failed to start: %w\nlast lines of %s:\n%s", cause, c.logPath(), tail)
}

func (c *Controller) logPath() string {
	return filepath.Join(c.Home.LogsDir(), "daemon.log")
}

// logTail returns the last n lines of the file at path, reading at most the
// final 64 KiB (the log is shared by every daemon run and never rotated).
func logTail(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	const window = 64 << 10
	offset := max(0, info.Size()-window)
	b, err := io.ReadAll(io.NewSectionReader(f, offset, info.Size()-offset))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n"), nil
}
