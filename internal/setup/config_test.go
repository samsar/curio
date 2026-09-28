package setup

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
)

// TestRenderConfig_RoundTrip: config.Load reads back exactly what curio up
// decided, the Qwen query prefix, two lines, byte for byte, and every other
// key at its default.
func TestRenderConfig_RoundTrip(t *testing.T) {
	cfg := config.Default()
	cfg.Embedding.Model, cfg.Embedding.Dim = "qwen3-embedding:0.6b", 1024
	cfg.Embedding.DocumentPrefix, cfg.Embedding.QueryPrefix = "", config.QwenQueryPrefix
	cfg.Generation.Model = "gemma4:26b"

	data, err := renderConfig(cfg)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), "# curio's settings for this home."))
	assert.NotContains(t, string(data), "listen", "the defaults' addresses aren't written")
	assert.NotContains(t, string(data), "base_url")
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	got, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, cfg, got)
	assert.Equal(t, config.QwenQueryPrefix, got.Embedding.QueryPrefix)
}

// TestRenderConfig_Addresses: an address that differs from the built-in
// default is written, so a home pointed elsewhere keeps pointing there.
func TestRenderConfig_Addresses(t *testing.T) {
	cfg := config.Default()
	cfg.Daemon.Listen = "127.0.0.1:9876"
	cfg.Embedding.BaseURL, cfg.Generation.BaseURL = "http://127.0.0.1:1234", "http://127.0.0.1:5678"
	cfg.Embedding.Model, cfg.Embedding.Dim = "nomic-embed-text:v1.5", 768
	cfg.Embedding.DocumentPrefix, cfg.Embedding.QueryPrefix = "search_document: ", "search_query: "

	data, err := renderConfig(cfg)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	got, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, cfg, got)
}

func TestWriteNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, writeNewFile(path, []byte("a: 1\n")))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	err = writeNewFile(path, []byte("a: 2\n"))
	require.ErrorIs(t, err, fs.ErrExist, "never over a file already there")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "a: 1\n", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file left behind")
}

// TestWriteNewFile_NoHardLinks: on a filesystem without hard links the
// file is created exclusively in place, 0600, with no temp file left, and
// a file already there is still never written over.
func TestWriteNewFile_NoHardLinks(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.ENOTSUP, syscall.EPERM} {
		t.Run(errno.Error(), func(t *testing.T) {
			linkFile = func(oldname, newname string) error {
				return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: errno}
			}
			t.Cleanup(func() { linkFile = os.Link })
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")

			require.NoError(t, writeNewFile(path, []byte("a: 1\n")))
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, "a: 1\n", string(data))

			err = writeNewFile(path, []byte("a: 2\n"))
			require.ErrorIs(t, err, fs.ErrExist)
			data, err = os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, "a: 1\n", string(data), "untouched")
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			assert.Len(t, entries, 1, "no temp file left behind")
		})
	}
}

// TestWriteNewFile_OtherLinkErrors: a link that fails for another reason
// fails the write, and nothing is left behind.
func TestWriteNewFile_OtherLinkErrors(t *testing.T) {
	linkFile = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EIO}
	}
	t.Cleanup(func() { linkFile = os.Link })
	dir := t.TempDir()
	err := writeNewFile(filepath.Join(dir, "config.yaml"), []byte("a: 1\n"))
	require.ErrorIs(t, err, syscall.EIO)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestListenOf(t *testing.T) {
	assert.Equal(t, "127.0.0.1:9999", listenOf("http://127.0.0.1:9999"))
	assert.Empty(t, listenOf(""))
}

func TestBackupPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".curio")
	at := time.Date(2026, 9, 27, 22, 14, 3, 0, time.Local)
	first := dir + ".bak-20260927-221403"

	got, err := backupPath(dir, at)
	require.NoError(t, err)
	assert.Equal(t, first, got)

	require.NoError(t, os.Mkdir(first, 0o700)) // empty, and still taken
	got, err = backupPath(dir, at)
	require.NoError(t, err)
	assert.Equal(t, first+"-2", got)

	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "gone"), first+"-2")) // dangling, and still taken
	got, err = backupPath(dir, at)
	require.NoError(t, err)
	assert.Equal(t, first+"-3", got)
}

func TestSetsGenerationModel(t *testing.T) {
	assert.True(t, setsGenerationModel([]byte("generation:\n  model: gemma4:12b\n")))
	assert.False(t, setsGenerationModel([]byte("generation:\n  timeout_seconds: 60\n")))
	assert.False(t, setsGenerationModel([]byte("# nothing\n")))
}
