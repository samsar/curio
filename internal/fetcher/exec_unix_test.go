//go:build unix

package fetcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubprocess_TimeoutKillsProcessGroup: on timeout the whole process
// group dies, helpers holding the output pipes included, and the fetch
// returns promptly with a deadline error instead of waiting for them.
func TestSubprocess_TimeoutKillsProcessGroup(t *testing.T) {
	fetchers := map[string]func(bin string) Fetcher{
		"web2md": func(bin string) Fetcher {
			f, err := NewWeb2MD(Web2MDOptions{Bin: bin, Timeout: 200 * time.Millisecond})
			require.NoError(t, err)
			return f
		},
		"youtube": func(bin string) Fetcher {
			return NewYouTube(YouTubeOptions{Bin: bin, Timeout: 200 * time.Millisecond})
		},
	}
	for name, build := range fetchers {
		t.Run(name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "helper.pid")
			t.Setenv(fakePIDFileEnv, pidFile)
			f := build(fakeTool(t, "hang-with-grandchild"))

			start := time.Now()
			_, err := f.Fetch(context.Background(), "https://www.youtube.com/watch?v=test_id")
			assert.Less(t, time.Since(start), 3*time.Second)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Contains(t, err.Error(), "timed out after 200ms")
			var pe *PermanentError
			assert.False(t, errors.As(err, &pe), "a timeout must stay retryable")

			raw, err := os.ReadFile(pidFile)
			require.NoError(t, err, "the fake never started its helper")
			pid, err := strconv.Atoi(string(raw))
			require.NoError(t, err)
			assert.Eventually(t, func() bool {
				return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
			}, 2*time.Second, 10*time.Millisecond, "helper %d outlived the timeout", pid)
		})
	}
}
