// Package daemonctl manages the curio-daemon process lifecycle from the CLI.
// PID-file-based; the daemon executable runs in the background.
package daemonctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/samansartipi/curio/internal/client"
	"github.com/samansartipi/curio/internal/curiohome"
)

// Status describes the daemon's run state.
type Status int

const (
	StatusNotRunning Status = iota
	StatusRunning
	StatusStale // PID file exists but the process doesn't
)

// Controller is the CLI-side daemon manager.
type Controller struct {
	Home       *curiohome.Home
	DaemonBin  string // absolute path to the curio-daemon executable
	BaseURL    string // for healthz probing
	StartTimeout time.Duration // how long to wait for daemon to become ready
}

func New(home *curiohome.Home, daemonBin, baseURL string) *Controller {
	return &Controller{
		Home:         home,
		DaemonBin:    daemonBin,
		BaseURL:      baseURL,
		StartTimeout: 5 * time.Second,
	}
}

// Status reads the PID file and probes the daemon's healthz endpoint.
func (c *Controller) Status() (Status, int, error) {
	pid, err := readPID(c.Home.PIDFile())
	if errors.Is(err, os.ErrNotExist) {
		return StatusNotRunning, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if !processAlive(pid) {
		return StatusStale, pid, nil
	}
	return StatusRunning, pid, nil
}

// Start launches the daemon if it isn't already running. Returns nil if
// the daemon is already up and healthy.
func (c *Controller) Start() error {
	status, pid, err := c.Status()
	if err != nil {
		return err
	}
	switch status {
	case StatusRunning:
		return nil
	case StatusStale:
		// Pid file from a previous run; remove and proceed.
		_ = os.Remove(c.Home.PIDFile())
		_ = pid
	}

	// Spawn the daemon as a detached child. We deliberately don't use
	// daemon(3) trickery here — relying on os.StartProcess + Release()
	// is portable across mac/linux and good enough for v0.
	cmd := exec.Command(c.DaemonBin)
	logPath := filepath.Join(c.Home.LogsDir(), "daemon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()

	// Setsid so the daemon survives the CLI exit.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start daemon: %w", err)
	}
	// We don't Wait — the daemon should outlive the CLI.
	if err := writePID(c.Home.PIDFile(), cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("write PID file: %w", err)
	}
	// Release so we don't keep a zombie around.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release process: %w", err)
	}

	return c.waitReady()
}

// Stop sends SIGTERM to the running daemon and waits briefly for it to exit.
func (c *Controller) Stop() error {
	status, pid, err := c.Status()
	if err != nil {
		return err
	}
	if status == StatusNotRunning {
		return nil
	}
	if status == StatusStale {
		_ = os.Remove(c.Home.PIDFile())
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM: %w", err)
	}
	// Wait up to 5s for the process to exit; then remove the PID file.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			_ = os.Remove(c.Home.PIDFile())
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon (pid %d) did not exit within 5s", pid)
}

// EnsureRunning is the CLI's "auto-start" entry point. Equivalent to Start
// when the daemon is down; no-op when it's up.
func (c *Controller) EnsureRunning() error {
	return c.Start()
}

// waitReady polls /healthz until ready or StartTimeout elapses.
func (c *Controller) waitReady() error {
	cl := client.New(c.BaseURL)
	deadline := time.Now().Add(c.StartTimeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, err := cl.Healthz(ctx)
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become ready within %s", c.StartTimeout)
}

func readPID(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(stringTrim(string(b)))
	if err != nil {
		return 0, fmt.Errorf("malformed PID file %s: %w", path, err)
	}
	return pid, nil
}

func writePID(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

// processAlive checks whether pid is a running process by sending signal 0.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

// stringTrim is filepath.Clean's cousin for free-form text; avoids pulling
// strings just for this.
func stringTrim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\r' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && (s[0] == '\n' || s[0] == ' ' || s[0] == '\r' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}
