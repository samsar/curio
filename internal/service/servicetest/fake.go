// Package servicetest is an in-process service.Manager for tests of the
// code that drives one (internal/daemonctl, internal/cli, the setup
// wizard), so none of them runs launchctl or touches ~/Library/LaunchAgents.
// It is test support only: depguard keeps production code from importing
// it.
package servicetest

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/samsar/curio/internal/service"
)

// Fake is a service.Manager whose state a test sets and inspects. It keeps
// a log of the calls made to it.
//
// With Launch set, it runs a real process as the service's daemon, the way
// launchd would: Install starts it (RunAtLoad), Start starts it unless it
// runs, Stop sends it SIGTERM, Restart and a changed Install replace it
// once it has exited, and Uninstall sends it SIGTERM. Status reports the
// process's PID while it runs and how it ended once it has. Like launchd's
// bootout, Uninstall doesn't wait for the process to exit; unlike it,
// neither do Restart and Install, which return before the replacement
// runs, so a caller's own waits are what a test exercises.
type Fake struct {
	// Launch returns the command the service runs, not yet started, each
	// time the fake starts its daemon. The fake starts it in a session of
	// its own. Without it, the fake runs nothing and Start fails.
	Launch func() *exec.Cmd
	// OnInstall and OnUninstall, when set, run as Install and Uninstall
	// begin; an error fails the call before anything changes.
	OnInstall   func(service.Spec) error
	OnUninstall func() error
	// IgnoreStop makes Stop succeed without signalling anything, as a
	// launchd that took the request and never acted on it would.
	IgnoreStop bool

	mu    sync.Mutex
	st    service.Status
	calls []string
	specs []service.Spec
	errs  map[string]error
	proc  *process   // the daemon most recently started, if any
	all   []*process // every daemon started, for the cleanup
	procs sync.WaitGroup
}

var _ service.Manager = (*Fake)(nil)

// process is a daemon the fake started. Its fields change under the
// fake's lock.
type process struct {
	cmd  *exec.Cmd
	done chan struct{} // closed once it has exited and been reaped
	exit string        // how it ended; set before done closes
	// replaced: start the next daemon as this one exits, in the same
	// step, so no Status sees the service between the two.
	replaced bool
}

func (p *process) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// New returns a Fake for a supported service named label, neither
// installed nor loaded. Any daemon it still runs when the test ends is
// killed.
func New(t testing.TB, label string) *Fake {
	t.Helper()
	f := &Fake{st: service.Status{Supported: true, Label: label, Path: "/fake/LaunchAgents/" + label + ".plist"}}
	t.Cleanup(f.kill)
	return f
}

// Set changes the status the fake reports; PID, State and LastExit are
// the running process's instead while the fake has started one.
func (f *Fake) Set(change func(*service.Status)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(&f.st)
}

// Fail makes every call of method ("Install", "Start", ...) fail with
// err; a nil err clears it.
func (f *Fake) Fail(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errs == nil {
		f.errs = map[string]error{}
	}
	f.errs[method] = err
}

// Calls returns the methods called so far, in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Count returns how many times method was called.
func (f *Fake) Count(method string) int {
	n := 0
	for _, c := range f.Calls() {
		if c == method {
			n++
		}
	}
	return n
}

// Specs returns the specs Install was called with, in order.
func (f *Fake) Specs() []service.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]service.Spec(nil), f.specs...)
}

// PID is the running daemon's PID, 0 when none runs.
func (f *Fake) PID() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.proc == nil || !f.proc.running() {
		return 0
	}
	return f.proc.cmd.Process.Pid
}

// record logs a call to method and returns the error set for it. The
// caller holds f.mu.
func (f *Fake) record(method string) error {
	f.calls = append(f.calls, method)
	return f.errs[method]
}

// Status implements service.Manager.
func (f *Fake) Status(context.Context) (service.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Status"); err != nil {
		return service.Status{}, err
	}
	st := f.st
	switch {
	case !st.Loaded:
		st.PID, st.State = 0, ""
	case f.proc == nil:
	case f.proc.running():
		st.PID, st.State = f.proc.cmd.Process.Pid, "running"
	default:
		st.PID, st.State, st.LastExit = 0, "not running", f.proc.exit
	}
	return st, nil
}

// Install implements service.Manager.
func (f *Fake) Install(_ context.Context, spec service.Spec) (bool, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	err := f.record("Install")
	hook := f.OnInstall
	f.mu.Unlock()
	if err != nil {
		return false, err
	}
	if hook != nil {
		if err := hook(spec); err != nil {
			return false, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st.Loaded && f.st.Program == spec.Program {
		return false, nil
	}
	f.st.Installed, f.st.Loaded, f.st.Program = true, true, spec.Program
	if f.Launch == nil {
		return true, nil
	}
	if _, err := f.replace(); err != nil {
		return false, err
	}
	return true, nil
}

// Uninstall implements service.Manager.
func (f *Fake) Uninstall(context.Context) (bool, error) {
	f.mu.Lock()
	err := f.record("Uninstall")
	hook := f.OnUninstall
	f.mu.Unlock()
	if err != nil {
		return false, err
	}
	if hook != nil {
		if err := hook(); err != nil {
			return false, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	removed := f.st.Installed || f.st.Loaded
	if f.st.Loaded {
		f.signal(syscall.SIGTERM)
	}
	f.st.Installed, f.st.Loaded, f.st.Program = false, false, ""
	return removed, nil
}

// Start implements service.Manager.
func (f *Fake) Start(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Start"); err != nil {
		return 0, err
	}
	if !f.st.Loaded {
		return 0, errors.New("servicetest: Start of a service that isn't loaded")
	}
	if f.proc != nil && f.proc.running() {
		return f.proc.cmd.Process.Pid, nil
	}
	return f.launch()
}

// Stop implements service.Manager.
func (f *Fake) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Stop"); err != nil {
		return err
	}
	if !f.IgnoreStop {
		f.signal(syscall.SIGTERM)
	}
	return nil
}

// Restart implements service.Manager. With a daemon running, it returns
// that daemon's PID: the replacement starts once it has exited, as a
// launchd status read mid-swap would report.
func (f *Fake) Restart(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("Restart"); err != nil {
		return 0, err
	}
	if !f.st.Loaded {
		return 0, errors.New("servicetest: Restart of a service that isn't loaded")
	}
	return f.replace()
}

// replace starts the daemon, or, while one runs, sends it SIGTERM and
// starts the next as it exits, returning the running one's PID. The caller
// holds f.mu.
func (f *Fake) replace() (int, error) {
	old := f.proc
	if old == nil || !old.running() {
		return f.launch()
	}
	old.replaced = true
	f.signal(syscall.SIGTERM)
	return old.cmd.Process.Pid, nil
}

// launch starts the daemon. The caller holds f.mu.
func (f *Fake) launch() (int, error) {
	if f.Launch == nil {
		return 0, errors.New("servicetest: no Launch to start the daemon with")
	}
	cmd := f.Launch()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("servicetest: start %s: %w", cmd.Path, err)
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	f.proc = p
	f.all = append(f.all, p)
	f.procs.Go(func() {
		exit := exitText(cmd.Wait())
		f.mu.Lock()
		defer f.mu.Unlock()
		p.exit = exit
		close(p.done)
		if p.replaced && f.st.Loaded && f.proc == p {
			// A replacement that fails to start leaves the service loaded
			// and not running, which Status reports as launchd would.
			_, _ = f.launch()
		}
	})
	return cmd.Process.Pid, nil
}

// signal sends sig to the running daemon, if any. The caller holds f.mu.
func (f *Fake) signal(sig syscall.Signal) {
	if f.proc != nil && f.proc.running() {
		_ = f.proc.cmd.Process.Signal(sig) // it may exit meanwhile, which is what sig asks
	}
}

// kill ends every daemon the fake started, and waits for them.
func (f *Fake) kill() {
	f.mu.Lock()
	f.st.Loaded = false // no replacement starts from here on
	for _, p := range f.all {
		if p.running() {
			_ = p.cmd.Process.Kill() // it may have exited meanwhile
		}
	}
	f.mu.Unlock()
	done := make(chan struct{})
	go func() {
		f.procs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// exitText words a process's end as launchd reports it: empty for a
// clean exit.
func exitText(err error) string {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &exit):
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return "signal " + ws.Signal().String()
		}
		return fmt.Sprintf("exit code %d", exit.ExitCode())
	default:
		return err.Error()
	}
}
