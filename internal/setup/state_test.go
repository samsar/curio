package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestState_RoundTrip(t *testing.T) {
	home := t.TempDir()
	var warnings bytes.Buffer
	ui := newPlainUI(&warnings)

	s := LoadState(home, ui)
	assert.Equal(t, State{Version: 1, Declined: map[string]time.Time{}}, s, "no file: an empty state")

	declined := time.Date(2026, 9, 27, 22, 30, 0, 0, time.UTC)
	s.Declined["yt-dlp"] = declined
	require.NoError(t, SaveState(home, s))
	info, err := os.Stat(filepath.Join(home, StateFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(filepath.Join(home, StateFile))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"yt-dlp": "2026-09-27T22:30:00Z"`, "RFC 3339")

	got := LoadState(home, ui)
	assert.Equal(t, 1, got.Version)
	assert.True(t, declined.Equal(got.Declined["yt-dlp"]))
	assert.Empty(t, warnings.String())
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file left behind")
}

// TestState_Corrupt: a setup.json that can't be parsed is ignored with a
// warning, never a failed run.
func TestState_Corrupt(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, StateFile), []byte("{not json"), 0o600))
	var warnings bytes.Buffer
	s := LoadState(home, newPlainUI(&warnings))
	assert.Empty(t, s.Declined)
	assert.Contains(t, warnings.String(), "warning: ignoring "+filepath.Join(home, StateFile))
}

// TestState_NoHome: nothing writes setup.json before the home exists.
func TestState_NoHome(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "home")
	require.Error(t, SaveState(missing, State{}))
	assert.NoDirExists(t, missing)
}
