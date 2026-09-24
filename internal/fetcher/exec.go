package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
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
// When timeout or ctx ends, the tool's whole process group is killed where
// the platform has them (see isolateProcessGroup). exec.CommandContext
// alone kills only the direct child, and a helper that child spawned
// (yt-dlp and web2md's node both can) inherits the output pipes, so Wait
// would block until the helper exits on its own. WaitDelay bounds that
// wait for a descendant that left the group.
//
// A run cut short by ctx fails with an error wrapping ctx.Err(), so a
// timeout stays retryable and a shutdown lets the worker requeue the job.
// A tool that exits non-zero fails with its *exec.ExitError. Errors don't
// name the tool; callers add that.
func runCapped(ctx context.Context, timeout time.Duration, stdout io.Writer, name string, args ...string) (stderr string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	errOut := &cappedBuffer{max: maxStderrBytes}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = errOut
	isolateProcessGroup(cmd)
	cmd.WaitDelay = waitDelay

	runErr := cmd.Run()
	if runErr == nil {
		return errOut.String(), nil
	}
	switch ctxErr := ctx.Err(); {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return errOut.String(), fmt.Errorf("timed out after %s: %w", timeout, ctxErr)
	case ctxErr != nil:
		return errOut.String(), fmt.Errorf("interrupted: %w", ctxErr)
	}
	return errOut.String(), runErr
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
