// Package daemonctl manages the curio-daemon process lifecycle for its
// clients (the CLI and the MCP sidecar): finding a running daemon, starting
// one, stopping it.
//
// Liveness comes from the daemon's lock on $CURIO_HOME/daemon.pid (see
// lock.go); identity comes from /v1/healthz, which reports the answering
// daemon's PID and home. A PID is only ever signalled when the lock vouches
// for it.
//
// When the service manager has the home's agent loaded (a launchd agent on
// macOS, see internal/service), the daemon is started, stopped and
// restarted through it; otherwise it is a child this process spawns. The
// lock stays the arbiter either way: a daemon launchd starts next to one a
// client spawned simply loses the lock and exits.
package daemonctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/service"
)

const (
	// defaultStartTimeout is how long a starting daemon may go without
	// answering. A daemon that is alive answers at once, even while it
	// migrates, and one that dies is reported as soon as it exits; 15s
	// covers the first launch of a freshly installed binary, which the OS
	// may scan before it runs.
	defaultStartTimeout = 15 * time.Second
	// defaultReadyTimeout caps how long a daemon that keeps reporting it is
	// starting may take to become ready. Migrations are what take long, and
	// their time grows with the library: 9s for 1 GB, 37s for a 2.4 GB
	// home. 30 min is about 50 times that. The ceiling only stops a script
	// from hanging on a wedged daemon; the daemon carries on either way.
	defaultReadyTimeout = 30 * time.Minute
	// defaultStopTimeout exceeds the daemon's own shutdown budget (20s, see
	// cmd/curio-daemon) and the time launchd gives it to exit before a
	// SIGKILL (service.ExitTimeout, 25s), so a healthy daemon always
	// finishes first.
	defaultStopTimeout = 30 * time.Second

	// pollInterval paces probes while nothing answers or the daemon is
	// initializing, so a routine start isn't slowed.
	pollInterval = 100 * time.Millisecond
	// migratingPollInterval paces probes of a daemon that reports it is
	// migrating: the Retry-After it sends. Each probe is an access-log
	// line in daemon.log, which is never rotated.
	migratingPollInterval = time.Second
	logTailLines          = 20
)

var (
	// ErrStillStarting: the daemon is running and reports it is starting,
	// but didn't become ready within ReadyTimeout. It keeps starting and
	// was not signalled.
	ErrStillStarting = errors.New("curio-daemon is still starting")

	// errHolderExited: the daemon being waited for released the lock
	// without ever serving. It was shutting down, or failed to start.
	errHolderExited = errors.New("the daemon holding the lock exited before it began serving")
)

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
	// PID is the lock holder's PID when Running (0 in the moment between
	// the daemon taking the lock and recording its PID), otherwise the
	// value left in daemon.pid.
	PID int
	// Health is what answered at BaseURL if it serves the full API, and
	// Startup what answered if it is still starting. Both are nil if
	// nothing did.
	Health  *client.Health
	Startup *client.Startup
	// Service is what the service manager reports about the daemon's
	// agent, nil when the controller has no manager or couldn't read it;
	// ServiceErr says why it couldn't. Neither changes State, which is the
	// lock's to say.
	Service    *service.Status
	ServiceErr error
}

// Managed reports whether the service manager runs the daemon holding the
// lock.
func (s Status) Managed() bool {
	return s.State == Running && s.PID != 0 && s.Service != nil && s.Service.Running() && s.Service.PID == s.PID
}

// AnsweredBy is the pid and home of whatever answered at BaseURL, serving
// or starting; answered is false if nothing did.
func (s Status) AnsweredBy() (pid int, home string, answered bool) {
	a := s.answer()
	pid, home = a.identity()
	return pid, home, a.health != nil || a.startup != nil
}

// answer is what answered at BaseURL when s was taken.
func (s Status) answer() answer {
	return answer{health: s.Health, startup: s.Startup}
}

// Controller manages the daemon for one home.
//
// Its waits trust only our daemon: an answer at BaseURL for this home from
// the daemon this call spawned or the one holding the home's lock.
type Controller struct {
	Home      *curiohome.Home
	DaemonBin string // absolute path to the curio-daemon executable
	BaseURL   string // where the daemon serves, for healthz probing
	// Service is the service manager that runs the home's daemon when it
	// has the daemon's agent loaded; nil spawns the daemon always.
	Service service.Manager
	// StartTimeout is how long a starting daemon may go without an answer
	// from our daemon, counted from the start of a wait and again from each
	// answer in which it reports it is starting. A daemon this call didn't
	// start gets max(StartTimeout, StopTimeout): it may be draining after a
	// stop rather than starting.
	StartTimeout time.Duration
	// ReadyTimeout is how long our daemon may keep reporting it is starting
	// before EnsureRunning gives up with ErrStillStarting.
	ReadyTimeout time.Duration
	// StopTimeout is how long a signalled daemon has to release its lock.
	StopTimeout time.Duration
	// OnMigrating, if set, is called at most once per EnsureRunning or
	// EnsureStarted call, the first time our daemon reports it is migrating
	// its database, so the caller can say why it waits. daemonctl itself
	// never writes to stdout or stderr.
	OnMigrating func(client.Startup)
}

func New(home *curiohome.Home, daemonBin, baseURL string) *Controller {
	return &Controller{
		Home:         home,
		DaemonBin:    daemonBin,
		BaseURL:      baseURL,
		StartTimeout: defaultStartTimeout,
		ReadyTimeout: defaultReadyTimeout,
		StopTimeout:  defaultStopTimeout,
	}
}

// Status reads the lock for liveness, healthz for identity, and the service
// manager, if any, for its agent.
func (c *Controller) Status(ctx context.Context) (Status, error) {
	held, pid, err := probeLock(c.Home.PIDFile())
	if err != nil {
		return Status{}, err
	}
	a := c.probe(ctx)
	st := Status{PID: pid, Health: a.health, Startup: a.startup}
	if c.Service != nil {
		if svc, err := c.Service.Status(ctx); err != nil {
			st.ServiceErr = err
		} else {
			st.Service = &svc
		}
	}
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

// EnsureRunning returns once a daemon for this home serves the full API at
// BaseURL, starting one if none is running: through the service manager
// when it has the daemon's agent loaded, as a child of this process
// otherwise. It waits for as long as the daemon is visibly starting, a
// migration included, up to ReadyTimeout; it fails at once if the daemon
// exits, and after StartTimeout if it stops answering. Concurrent callers
// start one daemon between them.
func (c *Controller) EnsureRunning(ctx context.Context) error {
	w := &waiter{c: c, goal: untilReady}
	return w.ensure(ctx)
}

// EnsureStarted is EnsureRunning, except that it returns as soon as our
// daemon answers at all: when it serves, with a nil Startup, or when it
// verifiably reports it is starting, with what it reported. A caller that
// must answer its own client quickly (the MCP sidecar's handshake) uses it
// to fail fast on a daemon that can't start without waiting out a
// migration.
func (c *Controller) EnsureStarted(ctx context.Context) (*client.Startup, error) {
	w := &waiter{c: c, goal: untilStarted}
	err := w.ensure(ctx)
	return w.started, err
}

// Stop sends SIGTERM to the daemon holding this home's lock, through the
// service manager when it runs that daemon, and waits for it to release the
// lock. stopped is true when a daemon for this home was running when Stop
// began and is gone when it returns. A home with no daemon, only a stale
// PID file, or another home's daemon on the port is (false, nil): there was
// nothing here to stop.
func (c *Controller) Stop(ctx context.Context) (stopped bool, err error) {
	st, err := c.Status(ctx)
	if err != nil {
		return false, err
	}
	return c.stopRunning(ctx, st)
}

// stopRunning stops the daemon st found running, as Stop does.
func (c *Controller) stopRunning(ctx context.Context, st Status) (stopped bool, err error) {
	switch st.State {
	case NotRunning, Stale:
		return false, nil
	case Legacy:
		return false, c.legacyStopError(st.PID)
	case Running:
	}

	if st.PID == 0 {
		return false, errors.New("the daemon is still starting and hasn't recorded its pid; try again in a moment")
	}
	if pid, home, answered := st.AnsweredBy(); answered && (pid != st.PID || !SameHome(home, c.Home.Path)) {
		return false, fmt.Errorf("%s answers as pid %d for %s, but this home's lock is held by pid %d; not signalling either",
			c.BaseURL, pid, home, st.PID)
	}
	if err := c.signalHolder(ctx, st.PID); err != nil {
		return false, err
	}
	return true, nil
}

// signalHolder stops pid, which the lock named, and waits for the lock to be
// released. Status probed healthz after reading the lock, which can take a
// while, so the lock is read again right before the signal: if pid no longer
// holds it, pid has exited or is exiting (its number may even belong to
// another process by now), so it is not signalled, and waitReleased's rule
// decides when it is gone. ESRCH from the signal means it already is.
//
// A daemon the service manager runs is stopped through it (`launchctl kill
// SIGTERM`), so launchd knows the stop was asked for; one it doesn't run,
// started by a client or by hand next to a loaded agent, is signalled
// directly.
func (c *Controller) signalHolder(ctx context.Context, pid int) error {
	held, holder, err := probeLock(c.Home.PIDFile())
	if err != nil {
		return err
	}
	if !held || holder != pid {
		return c.waitReleased(ctx, pid)
	}
	if c.manages(ctx, pid) {
		if err := c.stopManaged(ctx, pid); err != nil {
			return err
		}
		return c.waitReleased(ctx, pid)
	}
	err = syscall.Kill(pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil // it exited since the probe, and a process that has exited holds no lock
	}
	if err != nil {
		return fmt.Errorf("signal daemon (pid %d): %w", pid, err)
	}
	return c.waitReleased(ctx, pid)
}

// manages reports whether the service manager runs pid. A manager that
// can't say doesn't: the lock vouches for pid, and a direct SIGTERM stops
// a daemon launchd runs just as well, which exits 0 and so stays stopped.
func (c *Controller) manages(ctx context.Context, pid int) bool {
	if c.Service == nil {
		return false
	}
	st, err := c.Service.Status(ctx)
	return err == nil && st.Running() && st.PID == pid
}

// stopManaged asks the service manager to stop its daemon, pid. A request
// that failed while pid stopped anyway (it was exiting already) is no
// failure.
func (c *Controller) stopManaged(ctx context.Context, pid int) error {
	stopErr := c.Service.Stop(ctx)
	if stopErr == nil {
		return nil
	}
	if st, err := c.Service.Status(ctx); err == nil && (!st.Running() || st.PID != pid) {
		return nil
	}
	return stopErr
}

// SameHome reports whether two CURIO_HOME paths name the same directory,
// resolving symlinks (on macOS /tmp is /private/tmp). A path that doesn't
// exist here (a home on another machine's filesystem) is compared as
// written.
func SameHome(a, b string) bool {
	return curiohome.CanonicalPath(a) == curiohome.CanonicalPath(b)
}

// answer is what healthz at BaseURL said: a daemon serving the full API
// (health), one still starting (startup), or, with both nil, nothing
// usable.
type answer struct {
	health  *client.Health
	startup *client.Startup
}

// identity is the pid and home the answer names; both zero for none, or a
// daemon from before healthz named itself.
func (a answer) identity() (pid int, home string) {
	switch {
	case a.health != nil:
		return a.health.PID, a.health.Home
	case a.startup != nil:
		return a.startup.PID, a.startup.Home
	}
	return 0, ""
}

// probe asks healthz at BaseURL who is there. Any failure means nothing
// usable answered, an ordinary state here. The probe is bounded by
// client.Healthz, which allows for a ready daemon's own wait on Ollama.
func (c *Controller) probe(ctx context.Context) answer {
	h, err := client.New(c.BaseURL).Healthz(ctx)
	if err != nil {
		return answer{startup: client.StartupOf(err)}
	}
	return answer{health: h}
}

// probeBy is probe, given up at deadline.
func (c *Controller) probeBy(ctx context.Context, deadline time.Time) answer {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return c.probe(ctx)
}

func hasIdentity(h *client.Health) bool {
	return h.PID != 0 && h.Home != ""
}

// goal is what an ensure call waits for.
type goal int

const (
	untilReady   goal = iota // the daemon serves the full API
	untilStarted             // the daemon serves, or verifiably reports it is starting
)

// waiter is one EnsureRunning or EnsureStarted call.
type waiter struct {
	c    *Controller
	goal goal
	// started is what our daemon reported when an untilStarted wait ended
	// on it starting.
	started *client.Startup
	// toldMigrating is set once OnMigrating has been called.
	toldMigrating bool
}

// ensure finds the daemon for this home, waits for one that holds the
// home's lock, and starts one when none does.
func (w *waiter) ensure(ctx context.Context) error {
	c := w.c
	if done, err := c.served(c.probe(ctx)); done || err != nil {
		return err
	}
	held, _, err := probeLock(c.Home.PIDFile())
	if err != nil {
		return err
	}
	if held {
		// A daemon is up but not serving yet: starting (migrating, say),
		// started by hand, or draining after a stop.
		err := w.wait(ctx, nil)
		if !errors.Is(err, errHolderExited) {
			return err
		}
		// It went away instead (a `curio daemon stop` draining, say): start
		// our own.
	}
	return w.start(ctx)
}

// served reports whether a settles the ensure without a wait. A daemon
// serving this home does. So does one from before the lock protocol: its
// home can't be checked, but a second daemon would only fail to bind next
// to it, and an upgrade shouldn't strand anyone (`curio daemon stop`
// explains how to retire it). A daemon for another home, serving or
// starting, is an error: a new daemon could only fail to bind the same
// port.
func (c *Controller) served(a answer) (bool, error) {
	if _, home := a.identity(); home != "" && !SameHome(home, c.Home.Path) {
		return true, fmt.Errorf("%s is served by the curio-daemon for %s, not %s; "+
			"stop that daemon or give this home a different daemon.listen port", c.BaseURL, home, c.Home.Path)
	}
	return a.health != nil, nil
}

// start starts a daemon and waits for it. daemon.start.lock serializes
// starting, and is held only until the new daemon holds daemon.pid: from
// then on the daemon lock itself tells every other starter to wait for it
// instead, and holding the start lock through a long migration would park
// them where they can't report anything.
func (w *waiter) start(ctx context.Context) error {
	c := w.c
	startLock, err := lockStart(ctx, c.Home.StartLockFile())
	if err != nil {
		return err
	}
	// Another starter may have spawned a daemon while we waited for the
	// start lock.
	if done, err := c.served(c.probe(ctx)); done || err != nil {
		startLock.Close()
		return err
	}
	held, _, err := probeLock(c.Home.PIDFile())
	if err != nil {
		startLock.Close()
		return err
	}
	if held {
		startLock.Close()
		err := w.wait(ctx, nil)
		if errors.Is(err, errHolderExited) {
			// That starter's daemon failed; its log says why.
			return c.startFailed(err)
		}
		return err
	}

	ch, err := c.startDaemon(ctx)
	if err != nil {
		startLock.Close()
		return err
	}
	ch.startLock = startLock
	defer ch.releaseStartLock()
	return w.wait(ctx, ch)
}

// startDaemon starts a daemon for this home: the service manager's, when
// it has the daemon's agent loaded, and a child of this process otherwise.
// Next to a loaded agent a child would only race launchd's daemon for the
// lock, so a manager that can't say whether the agent is loaded fails the
// start. An agent installed but not loaded (disabled, or with no GUI
// session to run in, over ssh) is left alone.
func (c *Controller) startDaemon(ctx context.Context) (*child, error) {
	if c.Service == nil {
		return c.startChild()
	}
	st, err := c.Service.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("check for curio-daemon's launchd agent before starting the daemon: %w", err)
	}
	if !st.Loaded {
		return c.startChild()
	}
	since := time.Now()
	pid, err := c.Service.Start(ctx)
	if err != nil {
		return nil, err
	}
	return &child{pid: pid, launched: true, since: since}, nil
}

// child is a daemon this call started: spawned as a child of this process,
// or launched by the service manager.
type child struct {
	pid int
	// exited delivers a spawned child's cmd.Wait result once; nil for a
	// launched one, whose end the service manager reports.
	exited   <-chan error
	launched bool
	since    time.Time // when the launch began
	// stale is, for a restart, the PID of the daemon being replaced: its
	// answers and its hold on the lock are never taken for this one's.
	stale int
	// locked is set once the daemon has held daemon.pid.
	locked bool
	// startLock is held until the child holds daemon.pid, exits, or the
	// wait for it ends.
	startLock *os.File
}

// holds reports whether pid, the lock's holder, is ch. A launch that
// didn't say which process it started takes the first holder that isn't
// the daemon it replaces.
func (ch *child) holds(pid int) bool {
	if pid == 0 || pid == ch.stale {
		return false
	}
	if ch.launched && ch.pid == 0 {
		ch.pid = pid
	}
	return pid == ch.pid
}

func (ch *child) releaseStartLock() {
	if ch.startLock != nil {
		ch.startLock.Close()
		ch.startLock = nil
	}
}

// startChild runs curio-daemon for this home, detached, with both its
// stdout (its log) and its stderr in daemon.log.
func (c *Controller) startChild() (*child, error) {
	if err := os.MkdirAll(c.Home.LogsDir(), 0o700); err != nil {
		return nil, fmt.Errorf("create logs dir: %w", err)
	}
	logFile, err := os.OpenFile(c.Home.DaemonLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon log: %w", err)
	}

	// Not CommandContext: the daemon must outlive ctx and this process.
	cmd := exec.Command(c.DaemonBin) //nolint:gosec,noctx // G204: DaemonBin is curio-daemon beside this binary or $CURIO_DAEMON_BIN, run without a shell; noctx: see above
	// The daemon must serve exactly the home whose lock this controller
	// watches, whatever this process's own $CURIO_HOME says. The last
	// CURIO_HOME in the list is the one the child sees.
	cmd.Env = append(os.Environ(), "CURIO_HOME="+c.Home.Path)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Setsid so the daemon survives the CLI exit.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	startErr := cmd.Start()
	// The child has its own descriptor now; ours has no further use.
	logFile.Close()
	if startErr != nil {
		return nil, fmt.Errorf("start %s: %w", c.DaemonBin, startErr)
	}

	// Wait reaps the child and tells us at once if it dies during startup.
	// If we return first, the daemon carries on, reparented to init.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return &child{pid: cmd.Process.Pid, exited: exited}, nil
}

// wait polls healthz until our daemon is ready, or, for untilStarted,
// until it answers at all. Our daemon is ch, the child this call started,
// or whichever daemon holds the lock; ch is nil when there is no child to
// watch. The wait fails when the child exits, when the lock holder
// releases the lock (errHolderExited), and when our daemon runs out of
// budget (see budget).
func (w *waiter) wait(ctx context.Context, ch *child) error {
	c := w.c
	var exited <-chan error
	silence := c.StartTimeout
	if ch != nil {
		exited = ch.exited
	} else {
		// Not our child: the holder may be starting up, or draining after
		// a stop, which lasts up to the daemon's shutdown budget.
		silence = max(c.StartTimeout, c.StopTimeout)
	}
	b := newBudget(silence, c.ReadyTimeout)
	next := time.NewTimer(0)
	defer next.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case waitErr := <-exited:
			exited = nil
			ch.releaseStartLock()
			if err := c.childExited(waitErr); err != nil {
				return err
			}
			// The child lost the lock to a daemon that is still starting
			// (one started by hand, say). Wait for that one instead.
			continue
		case <-next.C:
		}

		holder, err := w.lockHolder(ctx, ch, exited != nil)
		if err != nil {
			return err
		}
		ready, starting := w.ours(c.probeBy(ctx, b.deadline()), ch, holder)
		switch {
		case ready:
			return nil
		case starting != nil:
			w.tellMigrating(*starting)
			if w.goal == untilStarted {
				w.started = starting
				return nil
			}
			b.heard(starting)
		}
		// No child alive to report on: waiting on whoever holds the lock.
		holderOnly := ch == nil || (!ch.launched && exited == nil)
		if err := b.check(c, ch, holderOnly); err != nil {
			return err
		}
		next.Reset(b.untilNextProbe())
	}
}

// lockHolder returns the PID holding the daemon lock, 0 if none. A child
// that holds it no longer needs the start lock. Once no spawned child is
// alive to report its own exit (childAlive false), the lock is how the
// wait learns that our daemon is gone. A launched daemon that doesn't hold
// it is the service manager's to account for (launchedUnlocked); the one
// a restart replaces, still holding it on its way out, isn't ours.
func (w *waiter) lockHolder(ctx context.Context, ch *child, childAlive bool) (int, error) {
	held, holder, err := probeLock(w.c.Home.PIDFile())
	switch {
	case err != nil:
		return 0, err
	case held && ch != nil && ch.holds(holder):
		ch.locked = true
		ch.releaseStartLock()
	case held && ch != nil && holder == ch.stale:
		return 0, nil
	case held:
	case ch != nil && ch.launched:
		return 0, w.c.launchedUnlocked(ctx, ch)
	case !childAlive && ch == nil:
		return 0, errHolderExited
	case !childAlive:
		return 0, w.c.startFailed(errHolderExited)
	default:
		return 0, nil // the file names a daemon that is gone
	}
	return holder, nil
}

// launchedUnlocked accounts for a launched daemon that doesn't hold the
// lock: not yet, while the service manager runs it (or still runs the
// daemon a restart replaces, on its way out), or no longer, which before
// it served is a failed start. The manager is asked only here, so a daemon
// that holds the lock through a long migration costs no subprocess per
// probe.
func (c *Controller) launchedUnlocked(ctx context.Context, ch *child) error {
	st, err := c.Service.Status(ctx)
	if err != nil {
		return fmt.Errorf("check on the curio-daemon launchd started: %w", err)
	}
	switch {
	case ch.locked:
	case st.Running() && st.PID == ch.stale:
		return nil
	case st.Running() && (ch.pid == 0 || st.PID == ch.pid):
		ch.pid = st.PID
		return nil
	}
	cause := errors.New("exited before it began serving")
	if st.LastExit != "" {
		cause = fmt.Errorf("exited before it began serving (%s)", st.LastExit)
	}
	return c.launchFailed(cause, ch.since)
}

// budget is how long a wait may go on. Our daemon may go for silence
// without an answer, counted from the start of the wait and again from
// each answer in which it reports it is starting; and one that keeps
// reporting it is starting must be ready by readyBy.
type budget struct {
	silence  time.Duration
	lastLife time.Time // the start of the wait, or the last starting answer
	readyBy  time.Time
	last     *client.Startup // what our daemon last reported, if anything
}

func newBudget(silence, readyTimeout time.Duration) *budget {
	now := time.Now()
	return &budget{silence: silence, lastLife: now, readyBy: now.Add(readyTimeout)}
}

// heard records our daemon reporting s.
func (b *budget) heard(s *client.Startup) {
	b.lastLife, b.last = time.Now(), s
}

// deadline is when the wait gives up unless our daemon answers again.
func (b *budget) deadline() time.Time {
	if quiet := b.lastLife.Add(b.silence); quiet.Before(b.readyBy) {
		return quiet
	}
	return b.readyBy
}

// check returns the wait's error once the deadline has passed: the daemon
// still starting if it was the ready ceiling that ran out on a daemon
// reporting progress, and the daemon gone silent otherwise. ch is the
// child the wait watches, if any, and holder says whether the daemon that
// went silent is one this call didn't start.
func (b *budget) check(c *Controller, ch *child, holder bool) error {
	deadline := b.deadline()
	switch {
	case time.Now().Before(deadline):
		return nil
	case b.last != nil && deadline.Equal(b.readyBy):
		return c.stillStarting(*b.last)
	default:
		return c.silent(deadline.Sub(b.lastLife), ch, holder)
	}
}

// untilNextProbe paces the probes: a daemon reporting it is migrating
// asks for a slower pace than one that is silent or initializing. Either
// way it is asked at least twice per silence budget, so a daemon that
// answers keeps the wait alive.
func (b *budget) untilNextProbe() time.Duration {
	interval := pollInterval
	if b.last != nil && b.last.Phase == client.PhaseMigrating {
		interval = migratingPollInterval
	}
	return min(interval, b.silence/2, time.Until(b.deadline()))
}

// ours reads a as evidence about our daemon: the child ch (if any) or the
// lock holder (pid holder, 0 if none), answering for this home. ready is
// true if it serves the full API, and starting is what it reported if it
// is starting. Any other answer is no evidence either way.
func (w *waiter) ours(a answer, ch *child, holder int) (ready bool, starting *client.Startup) {
	pid, home := a.identity()
	if home == "" || !SameHome(home, w.c.Home.Path) {
		return false, nil
	}
	isChild := ch != nil && pid != 0 && pid == ch.pid
	if !isChild && (pid != holder || pid == 0) {
		return false, nil
	}
	return a.health != nil, a.startup
}

// tellMigrating calls OnMigrating the first time our daemon reports it is
// migrating.
func (w *waiter) tellMigrating(s client.Startup) {
	if s.Phase != client.PhaseMigrating || w.toldMigrating {
		return
	}
	w.toldMigrating = true
	if w.c.OnMigrating != nil {
		w.c.OnMigrating(s)
	}
}

// childExited handles the spawned daemon's exit before it began serving:
// its failure to start, unless another daemon holds the lock (the child
// lost it to one started by hand), which the wait carries on for, and
// childExited returns nil.
func (c *Controller) childExited(waitErr error) error {
	cause := childExitCause(waitErr)
	held, _, err := probeLock(c.Home.PIDFile())
	if err != nil {
		return errors.Join(c.startFailed(cause), err)
	}
	if !held {
		return c.startFailed(cause)
	}
	return nil
}

// silent is the error for our daemon going quiet for budget. holder says
// whether that daemon is one this call didn't start, whose startup isn't
// ours to report on; otherwise it is ch.
func (c *Controller) silent(budget time.Duration, ch *child, holder bool) error {
	cause := fmt.Errorf("no answer at %s within %s", c.BaseURL, budget)
	switch {
	case holder:
		return fmt.Errorf("waiting for the curio-daemon holding the lock for %s "+
			"(starting up or shutting down): %w (`curio daemon status` shows its pid)", c.Home.Path, cause)
	case ch.launched:
		return c.launchFailed(cause, ch.since)
	default:
		return c.startFailed(cause)
	}
}

// stillStarting is the error for our daemon still reporting s past
// ReadyTimeout. It isn't signalled: it is making progress, and will serve
// once it is ready.
func (c *Controller) stillStarting(s client.Startup) error {
	return fmt.Errorf("%w after %s (pid %d: %s); it keeps running and serves once it is ready: "+
		"`curio daemon status` shows its progress and `curio daemon logs -f` follows it",
		ErrStillStarting, c.ReadyTimeout, s.PID, s.Progress())
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
			return fmt.Errorf("daemon (pid %d) is still running %s after SIGTERM; see %s", pid, c.StopTimeout, c.Home.DaemonLogPath())
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
	logPath := c.Home.DaemonLogPath()
	tail, err := logTail(logPath, logTailLines)
	if err != nil {
		return fmt.Errorf("curio-daemon failed to start: %w (log %s unreadable: %w)", cause, logPath, err)
	}
	return fmt.Errorf("curio-daemon failed to start: %w\nlast lines of %s:\n%s", cause, logPath, tail)
}

// launchFailed is startFailed for a daemon the service manager launched at
// since. launchd sends the daemon's stderr to launchd.err, where a daemon
// that dies before its log is up (a panic, a fatal runtime error) explains
// itself, so its tail is quoted too when it was written since the launch.
// The launch time is taken to the second, which is all some filesystems
// keep of a modification time.
func (c *Controller) launchFailed(cause error, since time.Time) error {
	failed := c.startFailed(cause)
	path := c.Home.LaunchdErrPath()
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return failed
	case err != nil:
		return fmt.Errorf("%w\n(%s unreadable: %w)", failed, path, err)
	case info.Size() == 0 || info.ModTime().Before(since.Truncate(time.Second)):
		return failed
	}
	tail, err := logTail(path, logTailLines)
	if err != nil {
		return fmt.Errorf("%w\n(%s unreadable: %w)", failed, path, err)
	}
	return fmt.Errorf("%w\nlast lines of %s:\n%s", failed, path, tail)
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
