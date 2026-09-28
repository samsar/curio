package keepawake

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// DefaultCaffeinate is macOS's caffeinate.
const DefaultCaffeinate = "/usr/bin/caffeinate"

// releaseGrace is how long a released hold's process has to exit on
// SIGTERM before it is killed.
const releaseGrace = 2 * time.Second

// Asserter holds the Mac out of idle sleep.
type Asserter interface {
	// Hold starts holding the Mac awake, until Release, the hold's own
	// end, or ctx's.
	Hold(ctx context.Context) (Hold, error)
}

// Hold is one hold on the Mac.
type Hold interface {
	// PID is the process that holds the Mac.
	PID() int
	// Done is closed once that process has exited, released or not.
	Done() <-chan struct{}
	// Release ends the hold and returns once its process has exited.
	Release()
	// Err is how the process ended, once Done is closed: nil for a clean
	// exit.
	Err() error
}

// Caffeinate is the Asserter that runs `caffeinate -i -w <pid>`: -i
// prevents idle sleep only (the display may sleep, and a closed lid still
// sleeps a laptop), and -w ends it when the daemon, pid, exits, however it
// exits. It runs in the daemon's process group, so a kill of the group
// (launchd's, at the end of ExitTimeOut) reaches it too.
type Caffeinate struct {
	Bin string // default DefaultCaffeinate
	PID int    // the daemon's
}

// Hold implements Asserter. ctx ending sends the process SIGTERM.
func (c Caffeinate) Hold(ctx context.Context) (Hold, error) {
	bin := c.Bin
	if bin == "" {
		bin = DefaultCaffeinate
	}
	cmd := exec.CommandContext(ctx, bin, "-i", "-w", strconv.Itoa(c.PID)) //nolint:gosec // G204: caffeinate's fixed path; the argument is a pid
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = releaseGrace
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	h := &caffeinated{cmd: cmd, done: make(chan struct{})}
	go func() {
		h.err = cmd.Wait()
		close(h.done)
	}()
	return h, nil
}

// caffeinated is a running caffeinate. Its goroutine reaps it.
type caffeinated struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // Wait's; read only after done closes
}

func (h *caffeinated) PID() int              { return h.cmd.Process.Pid }
func (h *caffeinated) Done() <-chan struct{} { return h.done }

// Release sends SIGTERM, and SIGKILL after releaseGrace.
func (h *caffeinated) Release() {
	select {
	case <-h.done:
		return
	default:
	}
	// Either signal fails only for a process that has exited meanwhile,
	// which is what it asked for; Wait reports the end.
	_ = h.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-h.done:
	case <-time.After(releaseGrace):
		_ = h.cmd.Process.Kill()
		<-h.done
	}
}

func (h *caffeinated) Err() error {
	<-h.done
	return h.err
}
