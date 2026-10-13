package devtool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/migrations"
)

// newDatabase writes a database at dir/name migrated up to version, and
// returns its path and the newest migration's version.
func newDatabase(t *testing.T, dir, name string, version int64) (path string, latest int64) {
	t.Helper()
	ctx := context.Background()
	path = filepath.Join(dir, name)
	db, err := sqlite.Open(ctx, path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	p, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, version)
	require.NoError(t, err)
	sources := p.ListSources()
	return path, sources[len(sources)-1].Version // ListSources sorts by version
}

// files are a directory's file names with their contents.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		out[e.Name()] = string(data)
	}
	return out
}

// TestOpenCopy_MigratesACopy: a copy at an older schema opens migrated to
// the newest, with stores over its vector index's width, until Close.
func TestOpenCopy_MigratesACopy(t *testing.T) {
	path, latest := newDatabase(t, t.TempDir(), "copy.db", 4)
	c, err := OpenCopy(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, path, c.Path)
	assert.Equal(t, latest, c.Schema)
	assert.Equal(t, 768, c.Width, "the width the first migration gives the index")
	n, err := c.Documents.CountByState(context.Background(), store.LocalTenantID)
	require.NoError(t, err)
	assert.Empty(t, n)

	require.NoError(t, c.Close())
	_, err = c.Documents.CountByState(context.Background(), store.LocalTenantID)
	assert.ErrorContains(t, err, "database is closed")
}

func TestOpenCopy_ReadsTheWidth(t *testing.T) {
	path, latest := newDatabase(t, t.TempDir(), "copy.db", goose.MaxVersion)
	db, err := sqlite.Open(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, sqlite.EnsureVectorIndex(context.Background(), db, 8))
	require.NoError(t, db.Close())

	c, err := OpenCopy(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	assert.Equal(t, latest, c.Schema)
	assert.Equal(t, 8, c.Width)
}

// TestOpenCopy_RefusesAHome: a database beside daemon.pid is refused, by
// its own path or through a link from elsewhere, before anything opens
// it: no -wal or -shm file, its time unchanged. The refusal says how to
// take a copy.
func TestOpenCopy_RefusesAHome(t *testing.T) {
	home := t.TempDir()
	db, _ := newDatabase(t, home, "curio.db", goose.MaxVersion)
	require.NoError(t, os.WriteFile(filepath.Join(home, "daemon.pid"), []byte("4242\n"), 0o600))
	link := filepath.Join(t.TempDir(), "link.db")
	require.NoError(t, os.Symlink(db, link))
	before := files(t, home)
	stat, err := os.Stat(db)
	require.NoError(t, err)

	for _, path := range []string{db, link} {
		_, err := OpenCopy(context.Background(), path)
		require.ErrorIs(t, err, ErrLiveHome)
		assert.Contains(t, err.Error(), "daemon.pid")
		assert.Contains(t, err.Error(), `sqlite3 -readonly `+path+` ".backup <copy>"`)
		assert.Contains(t, err.Error(), `sqlite3 'file:`+path+`?immutable=1' ".backup <copy>"`)
	}
	assert.Equal(t, before, files(t, home), "no -wal or -shm file beside it")
	after, err := os.Stat(db)
	require.NoError(t, err)
	assert.Equal(t, stat.ModTime(), after.ModTime())
}

func TestOpenCopy_RefusesWhatIsNoFile(t *testing.T) {
	dir := t.TempDir()
	_, err := OpenCopy(context.Background(), filepath.Join(dir, "typo.db"))
	require.ErrorContains(t, err, "no such file")
	assert.Empty(t, files(t, dir), "nothing created in its place")

	_, err = OpenCopy(context.Background(), dir)
	require.ErrorContains(t, err, "is not a file")
}

func TestCheckOutput(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "reports"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "readonly"), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "readonly"), 0o700) })

	require.NoError(t, CheckOutput(filepath.Join(dir, "report.json")))
	for _, tc := range []struct{ path, says string }{
		{filepath.Join(dir, "reports"), "is a directory"},
		{filepath.Join(dir, "missing", "report.json"), "no such file"},
		{filepath.Join(dir, "readonly", "report.json"), "permission denied"},
	} {
		assert.ErrorContains(t, CheckOutput(tc.path), tc.says, tc.path)
	}
	assert.Empty(t, files(t, dir), "the probe leaves nothing behind")
}

// TestWriteJSON: the report replaces what was at its path, 0600, and
// leaves no temporary file, whether it is written or not.
func TestWriteJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	require.NoError(t, os.WriteFile(path, []byte("an earlier report"), 0o644))

	require.NoError(t, WriteJSON(path, map[string]int{"n": 1}))
	assert.Equal(t, map[string]string{"report.json": "{\n  \"n\": 1\n}\n"}, files(t, dir))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.Error(t, WriteJSON(path, map[string]any{"bad": make(chan int)}), "unencodable")
	require.Error(t, WriteJSON(filepath.Join(dir, "missing", "report.json"), 1))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "reports"), 0o700))
	require.Error(t, WriteJSON(filepath.Join(dir, "reports"), 1), "a directory, which the rename can't replace")
	assert.Equal(t, map[string]string{"report.json": "{\n  \"n\": 1\n}\n"}, files(t, dir),
		"a failed write leaves the last report and no temporary file")
}
