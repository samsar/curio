//go:build unix

package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// maxStderrBytes caps how much of a tool's stderr is kept for error
// messages. The head is what says what went wrong.
const maxStderrBytes = 64 << 10

// waitDelay bounds how long Wait keeps reading a killed process's output
// pipes. A descendant that escaped the process group can still hold them.
const waitDelay = 2 * time.Second

// runCapped runs name with args under timeout and returns the head of its
// stderr. stdout, if not nil, receives the tool's standard output.
//
// The tool runs in its own process group, and when timeout or ctx ends the
// whole group is killed. exec.CommandContext alone kills only the direct
// child: a helper it spawned (yt-dlp and web2md's node both can) kept the
// output pipes open, and Wait blocked until that helper exited on its own.
//
// A run cut short by ctx fails with an error wrapping ctx.Err(), so a
// timeout stays retryable and a shutdown lets the worker requeue the job.
// A tool that exits non-zero fails with its *exec.ExitError.
func runCapped(ctx context.Context, timeout time.Duration, stdout io.Writer, name string, args ...string) (stderr string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	errOut := &cappedBuffer{max: maxStderrBytes}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = errOut
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay

	runErr := cmd.Run()
	if runErr == nil {
		return errOut.String(), nil
	}
	tool := filepath.Base(name)
	switch ctxErr := ctx.Err(); {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return errOut.String(), fmt.Errorf("%s timed out after %s: %w", tool, timeout, ctxErr)
	case ctxErr != nil:
		return errOut.String(), fmt.Errorf("%s interrupted: %w", tool, ctxErr)
	}
	return errOut.String(), fmt.Errorf("%s: %w", tool, runErr)
}

// cappedBuffer collects a stream up to max bytes. Past that it either fails
// the write (strict: output that would be cut short is useless, and failing
// the pipe stops the tool from producing more) or keeps the head and drops
// the rest (diagnostics, whose first lines matter most).
type cappedBuffer struct {
	buf        []byte
	max        int64
	strict     bool
	overflowed bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.max - int64(len(b.buf))
	if int64(len(p)) <= room {
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	b.overflowed = true
	if b.strict {
		return 0, b.tooLarge()
	}
	b.buf = append(b.buf, p[:max(room, 0)]...)
	return len(p), nil
}

func (b *cappedBuffer) tooLarge() error {
	return fmt.Errorf("%w (limit %d bytes)", ErrTooLarge, b.max)
}

func (b *cappedBuffer) Bytes() []byte { return b.buf }

// String returns the collected text, noting when the tail was dropped.
func (b *cappedBuffer) String() string {
	if b.overflowed && !b.strict {
		return string(b.buf) + " …(truncated)"
	}
	return string(b.buf)
}

// toolError describes a failed run: the run error plus the tool's stderr,
// where web2md and yt-dlp both write their diagnostics.
func toolError(prefix string, err error, stderr string) error {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return fmt.Errorf("%s: %w: %s", prefix, err, msg)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
