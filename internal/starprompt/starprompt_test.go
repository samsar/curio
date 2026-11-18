package starprompt

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "star.json")
}

// TestSearched_AsksOnceAtTheThreshold: the fifth search asks, the ones
// before and after don't, and the file says where and when it asked.
func TestSearched_AsksOnceAtTheThreshold(t *testing.T) {
	path := newPath(t)
	p := Open(path, false, nil)
	p.now = func() time.Time { return time.Date(2026, 11, 17, 9, 30, 15, 500, time.UTC) }
	for i := 1; i < Threshold; i++ {
		assert.False(t, p.Searched(SurfaceCLI), "search %d", i)
	}
	assert.True(t, p.Searched(SurfaceDashboard), "search %d asks", Threshold)
	for range 3 {
		assert.False(t, p.Searched(SurfaceCLI), "never again")
	}

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"searches":5,"shown_at":"2026-11-17T09:30:15Z","shown_in":"dashboard"}`, string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestSearched_SurvivesARestart: the count is kept across processes, and
// a home that was asked is never asked again.
func TestSearched_SurvivesARestart(t *testing.T) {
	path := newPath(t)
	p := Open(path, false, nil)
	for range Threshold - 1 {
		require.False(t, p.Searched(SurfaceCLI))
	}

	p = Open(path, false, nil)
	assert.True(t, p.Searched(SurfaceCLI), "the count started from the file")

	p = Open(path, false, nil)
	for range Threshold * 2 {
		assert.False(t, p.Searched(SurfaceCLI), "asked already")
	}
}

// TestOpen_NeverAsks: turned off, or over a file that can't be read or
// isn't star.json, a Prompt never asks; nor does a nil one.
func TestOpen_NeverAsks(t *testing.T) {
	cases := map[string]func(t *testing.T) *Prompt{
		"off": func(t *testing.T) *Prompt { return Open(newPath(t), true, nil) },
		"not json": func(t *testing.T) *Prompt {
			path := newPath(t)
			require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
			return Open(path, false, nil)
		},
		"unreadable": func(t *testing.T) *Prompt {
			path := newPath(t)
			require.NoError(t, os.Mkdir(path, 0o700))
			return Open(path, false, nil)
		},
		"nil": func(*testing.T) *Prompt { return nil },
	}
	for name, open := range cases {
		t.Run(name, func(t *testing.T) {
			p := open(t)
			for range Threshold * 2 {
				assert.False(t, p.Searched(SurfaceCLI))
			}
		})
	}
}

// TestOpen_OffWritesNothing: a Prompt turned off leaves the home alone.
func TestOpen_OffWritesNothing(t *testing.T) {
	path := newPath(t)
	p := Open(path, true, nil)
	p.Searched(SurfaceCLI)
	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestSearched_UnwritableNeverAsks: a home whose star.json can't be
// written is never asked, rather than asked again after every restart.
func TestSearched_UnwritableNeverAsks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "star.json")
	p := Open(path, false, nil)
	for range Threshold * 2 {
		assert.False(t, p.Searched(SurfaceCLI))
	}
}

// TestSearched_Concurrent: of many searches at once past the threshold,
// exactly one asks.
func TestSearched_Concurrent(t *testing.T) {
	p := Open(newPath(t), false, nil)
	var asked atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if p.Searched(SurfaceCLI) {
				asked.Add(1)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), asked.Load())
}
