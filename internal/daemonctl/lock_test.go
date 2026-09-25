package daemonctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/curiohome"
)

// TestAcquireLock_Contention: a second daemon for a home is refused with
// ErrAlreadyRunning naming the holder, and gets the lock once it's released.
func TestAcquireLock_Contention(t *testing.T) {
	t.Parallel() // the refusal waits out lockWait
	home, err := curiohome.Init(t.TempDir(), "nomic-embed-text", 768)
	require.NoError(t, err)

	held, err := AcquireLock(home)
	require.NoError(t, err)
	pid, err := os.ReadFile(home.PIDFile())
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid()), strings.TrimSpace(string(pid)), "the holder records its PID")

	_, err = AcquireLock(home)
	require.ErrorIs(t, err, ErrAlreadyRunning)
	assert.Contains(t, err.Error(), home.Path)
	assert.Contains(t, err.Error(), fmt.Sprintf("pid %d", os.Getpid()))

	require.NoError(t, held.Release())
	pid, err = os.ReadFile(home.PIDFile())
	require.NoError(t, err)
	assert.Empty(t, pid, "a released lock leaves no PID behind")

	again, err := AcquireLock(home)
	require.NoError(t, err)
	require.NoError(t, again.Release())
}

// TestLockStart_HonoursContext: waiting for the start lock ends with the
// caller's context, within one retry, and the lock is taken once it is
// free.
func TestLockStart_HonoursContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.start.lock")
	held, err := lockStart(context.Background(), path)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = lockStart(ctx, path)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 300*time.Millisecond+2*pollInterval)

	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	_, err = lockStart(cancelled, path)
	require.ErrorIs(t, err, context.Canceled)

	require.NoError(t, held.Close())
	again, err := lockStart(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, again.Close())
}
