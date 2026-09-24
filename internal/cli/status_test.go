package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHomeMismatchWarning(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(home, link))

	assert.Empty(t, homeMismatchWarning(home, home))
	assert.Empty(t, homeMismatchWarning(link, home), "a symlink to the same home is the same home")
	assert.Empty(t, homeMismatchWarning("", home), "a daemon that doesn't report its home can't be checked")

	w := homeMismatchWarning(other, home)
	assert.Contains(t, w, "serves "+other+", not "+home)
	assert.Contains(t, w, "daemon.listen")
}
