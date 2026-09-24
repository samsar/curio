package daemonctl

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/samsar/curio/internal/curiohome"
)

// Single-instance protocol.
//
// A running daemon holds an exclusive flock(2) on $CURIO_HOME/daemon.pid for
// its whole life and records its PID inside. Liveness is the lock, never the
// PID: the kernel drops a flock however the holder dies, so a PID left behind
// by a crash or a reboot can't pass for a running daemon, and a recycled PID
// is never mistaken for ours. flock rather than fcntl locks, because fcntl
// locks are released when the process closes *any* descriptor for the file.
//
// The file is truncated on clean exit but never removed or replaced: a new
// daemon locking a fresh inode while a prober still holds the old one would
// let two daemons believe they are alone.

// ErrAlreadyRunning is returned by AcquireLock when another daemon holds
// the home's lock.
var ErrAlreadyRunning = errors.New("curio-daemon is already running")

// errMalformedPID marks a daemon.pid whose contents aren't a PID.
var errMalformedPID = errors.New("malformed pid")

const (
	// lockWait is how long AcquireLock keeps retrying. A client probing
	// status holds a shared lock for microseconds; that mustn't make a
	// starting daemon give up.
	lockWait  = 2 * time.Second
	lockRetry = 50 * time.Millisecond
)

// Lock is a daemon's exclusive hold on its home.
type Lock struct {
	f *os.File
}

// AcquireLock takes the home's single-instance lock and records the calling
// process's PID in it. It fails with ErrAlreadyRunning, naming the holder's
// PID, if another daemon holds the lock for longer than lockWait.
func AcquireLock(home *curiohome.Home) (*Lock, error) {
	f, err := os.OpenFile(home.PIDFile(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	if err := lockWithRetry(f); err != nil {
		defer f.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("lock %s: %w", home.PIDFile(), err)
		}
		holder := "unknown pid"
		if pid, perr := readPID(f); perr == nil && pid > 0 {
			holder = "pid " + strconv.Itoa(pid)
		}
		return nil, fmt.Errorf("%w for %s (%s)", ErrAlreadyRunning, home.Path, holder)
	}
	if err := writePID(f, os.Getpid()); err != nil {
		f.Close()
		return nil, fmt.Errorf("record daemon pid: %w", err)
	}
	return &Lock{f: f}, nil
}

// Release empties the PID file and drops the lock.
func (l *Lock) Release() error {
	// Truncate while still holding the lock, so nobody reads our PID after
	// we're gone.
	truncErr := l.f.Truncate(0)
	if err := errors.Join(truncErr, l.f.Close()); err != nil {
		return fmt.Errorf("release daemon lock: %w", err)
	}
	return nil
}

func lockWithRetry(f *os.File) error {
	deadline := time.Now().Add(lockWait)
	for {
		err := flock(f, syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(lockRetry)
	}
}

// probeLock reports whether a daemon holds the lock at path. pid is the
// value in the file: while held, the holder's PID (0 in the instant between
// locking and recording it); otherwise whatever a crashed daemon or an older
// CLI left behind (0 after a clean shutdown, or if that isn't a PID at all).
func probeLock(path string) (held bool, pid int, err error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("open daemon lock: %w", err)
	}
	defer f.Close() // also drops the shared lock, if we got it

	switch err := flock(f, syscall.LOCK_SH|syscall.LOCK_NB); {
	case err == nil:
		held = false
	case errors.Is(err, syscall.EWOULDBLOCK):
		held = true
	default:
		return false, 0, fmt.Errorf("probe daemon lock: %w", err)
	}
	pid, err = readPID(f)
	if errors.Is(err, errMalformedPID) && !held {
		// Leftovers in a free lock file are informational only, and the
		// next daemon overwrites them; garbage mustn't block a start.
		return false, 0, nil
	}
	return held, pid, err
}

// lockStart takes the exclusive start lock, blocking until it is free, so
// concurrent auto-starters (the CLI and the MCP sidecar) spawn one daemon.
// A separate file from the daemon's own lock so the two never contend.
// Closing the returned file releases it.
func lockStart(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open start lock: %w", err)
	}
	if err := flock(f, syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("take start lock: %w", err)
	}
	return f, nil
}

// flock retries on EINTR, which a blocking lock returns when a signal lands.
func flock(f *os.File, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func readPID(f *os.File) (int, error) {
	b, err := io.ReadAll(io.NewSectionReader(f, 0, 32))
	if err != nil {
		return 0, fmt.Errorf("read daemon pid: %w", err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, nil
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%w %q in %s: %w", errMalformedPID, s, f.Name(), err)
	}
	return pid, nil
}

// writePID replaces the file's contents in place; see the package comment
// for why it's never swapped for a new file.
func writePID(f *os.File, pid int) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(pid)+"\n"), 0)
	return err
}
