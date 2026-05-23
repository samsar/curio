package curiohome

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_DefaultsToHomeDotCurio(t *testing.T) {
	t.Setenv("CURIO_HOME", "")
	got, err := Resolve()
	require.NoError(t, err)
	userHome, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(userHome, DefaultDirName), got)
}

func TestResolve_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CURIO_HOME", dir)
	got, err := Resolve()
	require.NoError(t, err)
	abs, _ := filepath.Abs(dir)
	assert.Equal(t, abs, got)
}

func TestResolve_EnvOverrideMakesRelativeAbsolute(t *testing.T) {
	t.Setenv("CURIO_HOME", "relative/path")
	got, err := Resolve()
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(got), "expected absolute path, got %s", got)
}

func TestInit_CreatesMarkerAndSubdirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	h, err := Init(dir, "nomic-embed-text", 768)
	require.NoError(t, err)

	assert.FileExists(t, h.MarkerPath())
	assert.DirExists(t, h.ContentDir())
	assert.DirExists(t, h.LogsDir())

	m, err := h.Meta()
	require.NoError(t, err)
	assert.Equal(t, CurrentSchemaVersion, m.SchemaVersion)
	assert.Equal(t, "nomic-embed-text", m.EmbeddingModel)
	assert.Equal(t, 768, m.EmbeddingDim)
	assert.False(t, m.CreatedAt.IsZero())
	assert.False(t, m.UpdatedAt.IsZero())
}

func TestInit_FailsIfMarkerExists(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, "m", 1)
	require.NoError(t, err)

	_, err = Init(dir, "m", 1)
	assert.ErrorIs(t, err, ErrAlreadyInitialized)
}

func TestOpen_SucceedsWhenMarkerPresent(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, "m", 1)
	require.NoError(t, err)

	h, err := Open(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, h.Path)
}

func TestOpen_FailsWhenDirMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := Open(dir)
	assert.ErrorIs(t, err, ErrNotInitialized)
}

func TestOpen_FailsWhenMarkerMissing(t *testing.T) {
	// User has a ~/.curio directory from some other tool. Refuse to touch it.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("not ours"), 0o600))

	_, err := Open(dir)
	assert.ErrorIs(t, err, ErrNotOurs)
}

func TestOpen_FailsWhenPathIsAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o600))

	_, err := Open(f)
	require.Error(t, err)
	// Not ErrNotInitialized or ErrNotOurs — distinct condition
	assert.False(t, errors.Is(err, ErrNotInitialized))
	assert.False(t, errors.Is(err, ErrNotOurs))
}

func TestWriteMeta_AtomicAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	h, err := Init(dir, "m1", 100)
	require.NoError(t, err)

	updated := Meta{
		SchemaVersion:  2,
		EmbeddingModel: "voyage-3",
		EmbeddingDim:   1024,
	}
	require.NoError(t, h.WriteMeta(updated))

	got, err := h.Meta()
	require.NoError(t, err)
	assert.Equal(t, 2, got.SchemaVersion)
	assert.Equal(t, "voyage-3", got.EmbeddingModel)
	assert.Equal(t, 1024, got.EmbeddingDim)
	assert.False(t, got.UpdatedAt.IsZero(), "WriteMeta should populate UpdatedAt if zero")

	// No leftover .tmp file
	_, err = os.Stat(h.MarkerPath() + ".tmp")
	assert.True(t, os.IsNotExist(err), "tmp file should not remain after successful rename")
}

func TestPathHelpers(t *testing.T) {
	h := &Home{Path: "/curio"}
	assert.Equal(t, "/curio/.curio-meta.json", h.MarkerPath())
	assert.Equal(t, "/curio/config.yaml", h.ConfigPath())
	assert.Equal(t, "/curio/curio.db", h.DBPath())
	assert.Equal(t, "/curio/content", h.ContentDir())
	assert.Equal(t, "/curio/logs", h.LogsDir())
	assert.Equal(t, "/curio/daemon.pid", h.PIDFile())
}
