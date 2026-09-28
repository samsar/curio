//go:build darwin

package setup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSystemProbe is a smoke test of the real probe on the Mac the tests
// run on: plausible facts, and free space measured at the nearest
// directory that exists. The GPU core count isn't asserted: a CI virtual
// machine may report none.
func TestSystemProbe(t *testing.T) {
	dir := t.TempDir()
	m, err := SystemProbe{}.Machine(context.Background(), filepath.Join(dir, "home", "not-yet"), dir)
	require.NoError(t, err)
	assert.Equal(t, "darwin", m.OS)
	assert.NotEmpty(t, m.Chip)
	assert.GreaterOrEqual(t, m.Memory, uint64(4*GiB))
	assert.Positive(t, m.PerfCores)
	assert.GreaterOrEqual(t, m.EffCores, 0)
	assert.True(t, m.Home.Known())
	assert.True(t, m.Models.Known())
	assert.Positive(t, m.Home.Free)
	assert.Equal(t, dir, m.Home.Path, "measured at the nearest directory that exists")
	assert.Equal(t, m.Home.ID, m.Models.ID, "one volume")
}
