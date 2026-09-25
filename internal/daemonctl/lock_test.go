package daemonctl

import (
	"fmt"
	"os"
	"strings"
	"testing"

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
	assert.Equal(t, fmt.Sprint(os.Getpid()), strings.TrimSpace(string(pid)), "the holder records its PID")

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
