package curiohome

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	h, err := Init(dir, "qwen3-embedding:0.6b", 1024)
	require.NoError(t, err)

	assert.FileExists(t, h.MarkerPath())
	assert.DirExists(t, h.ContentDir())
	assert.DirExists(t, h.LogsDir())

	m, err := h.Meta()
	require.NoError(t, err)
	assert.Equal(t, CurrentFormat, m.Format)
	assert.Equal(t, CurrentSchemaVersion, m.SchemaVersion)
	assert.Equal(t, "qwen3-embedding:0.6b", m.EmbeddingModel)
	assert.Equal(t, 1024, m.EmbeddingDim)
	assert.Empty(t, m.EmbeddingModelDigest, "the daemon records the fingerprint, not Init")
	assert.Empty(t, m.OllamaVersion)
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

	stale := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	updated := Meta{
		Format:               CurrentFormat,
		SchemaVersion:        2,
		EmbeddingModel:       "voyage-3",
		EmbeddingDim:         1024,
		EmbeddingModelDigest: "0a109f422b47e3a30ba2b10eca18548e944e8a23073ee3f3e947efcf3c45e59f",
		OllamaVersion:        "0.34.4",
		CreatedAt:            stale,
		UpdatedAt:            stale,
	}
	before := time.Now().UTC()
	require.NoError(t, h.WriteMeta(updated))

	got, err := h.Meta()
	require.NoError(t, err)
	assert.False(t, got.UpdatedAt.Before(before), "every write stamps UpdatedAt, whatever the caller passed")
	got.UpdatedAt = stale
	assert.Equal(t, updated, got, "every other field round-trips, CreatedAt included")

	// No leftover .tmp file
	_, err = os.Stat(h.MarkerPath() + ".tmp")
	assert.ErrorIs(t, err, fs.ErrNotExist, "tmp file should not remain after successful rename")
}

// TestWriteMeta_FingerprintOmittedUntilRecorded: a marker without a
// fingerprint doesn't carry empty fingerprint keys.
func TestWriteMeta_FingerprintOmittedUntilRecorded(t *testing.T) {
	h, err := Init(t.TempDir(), "m", 1)
	require.NoError(t, err)
	raw, err := os.ReadFile(h.MarkerPath())
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"format": 2`)
	assert.NotContains(t, string(raw), "embedding_model_digest")
	assert.NotContains(t, string(raw), "ollama_version")
}

// legacyMarker is a marker as curio wrote it before home formats: no
// format key.
const legacyMarker = `{
  "schema_version": 11,
  "embedding_model": "nomic-embed-text",
  "embedding_dim": 768,
  "created_at": "2026-05-23T10:00:00Z",
  "updated_at": "2026-09-27T10:00:00Z"
}
`

// legacyHome is a home whose marker predates home formats.
func legacyHome(t *testing.T) *Home {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, MarkerFile), []byte(legacyMarker), 0o600))
	h, err := Open(dir)
	require.NoError(t, err, "a legacy home still opens")
	return h
}

func TestMeta_CheckEmbedding(t *testing.T) {
	current := Meta{Format: CurrentFormat, EmbeddingModel: "qwen3-embedding:0.6b", EmbeddingDim: 1024}
	cases := []struct {
		name         string
		meta         Meta
		model        string
		dim          int
		wantLegacy   bool
		wantMismatch bool
	}{
		{"matches", current, "qwen3-embedding:0.6b", 1024, false, false},
		{"another model", current, "mxbai-embed-large", 1024, false, true},
		{"another width", current, "qwen3-embedding:0.6b", 768, false, true},
		{"no format, matching", Meta{EmbeddingModel: "nomic-embed-text", EmbeddingDim: 768}, "nomic-embed-text", 768, true, false},
		{"older format, mismatched", Meta{Format: 1, EmbeddingModel: "nomic-embed-text", EmbeddingDim: 768},
			"qwen3-embedding:0.6b", 1024, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.meta.CheckEmbedding(tc.model, tc.dim)
			assert.Equal(t, tc.wantLegacy, errors.Is(err, ErrLegacyHome), "legacy: %v", err)
			var mismatch *EmbeddingMismatchError
			assert.Equal(t, tc.wantMismatch, errors.As(err, &mismatch), "mismatch: %v", err)
			if tc.wantMismatch {
				assert.Equal(t, Embedding{Model: tc.meta.EmbeddingModel, Dim: tc.meta.EmbeddingDim}, mismatch.Recorded)
				assert.Equal(t, Embedding{Model: tc.model, Dim: tc.dim}, mismatch.Configured)
			}
			if !tc.wantLegacy && !tc.wantMismatch {
				assert.NoError(t, err)
			}
		})
	}
}

// TestHome_CheckEmbedding_Legacy: the refusal of a legacy home names the
// home, its marker, what the marker records and the fix, deleting nothing.
func TestHome_CheckEmbedding_Legacy(t *testing.T) {
	h := legacyHome(t)
	_, err := h.CheckEmbedding("nomic-embed-text", 768)
	require.ErrorIs(t, err, ErrLegacyHome)
	for _, want := range []string{
		h.Path + " was made before home format 2", h.MarkerPath(), `"nomic-embed-text" (dim 768)`,
		"`curio up --fresh`", h.Path + ".bak-<YYYYMMDD-HHMMSS>", "deletes nothing", "move it aside yourself",
		"import your bookmarks again",
	} {
		assert.Contains(t, err.Error(), want)
	}
	_, err = os.Stat(h.MarkerPath())
	require.NoError(t, err, "the check changes nothing")
}

// TestHome_CheckEmbedding_Mismatch: the refusal names config.yaml and the
// marker, both model/dim pairs, and both fixes.
func TestHome_CheckEmbedding_Mismatch(t *testing.T) {
	h, err := Init(t.TempDir(), "qwen3-embedding:0.6b", 1024)
	require.NoError(t, err)

	meta, err := h.CheckEmbedding("qwen3-embedding:0.6b", 1024)
	require.NoError(t, err)
	assert.Equal(t, 1024, meta.EmbeddingDim)

	_, err = h.CheckEmbedding("nomic-embed-text", 768)
	var mismatch *EmbeddingMismatchError
	require.ErrorAs(t, err, &mismatch)
	for _, want := range []string{
		`configured "nomic-embed-text" (dim 768)`, `made with "qwen3-embedding:0.6b" (dim 1024)`,
		h.ConfigPath(), h.MarkerPath(), "Set embedding.model and embedding.dim back to the recorded values",
		"`curio up --fresh`", "deletes nothing",
	} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "Embedding model swap")
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
