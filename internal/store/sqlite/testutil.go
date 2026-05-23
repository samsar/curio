package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// NewEphemeralDB returns a fresh on-disk SQLite database in t.TempDir() with
// all migrations applied. Use this instead of ":memory:" — database/sql
// pools connections, and each in-memory connection sees a different DB,
// which silently breaks tests that span more than one query.
func NewEphemeralDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "curio.db")
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, Migrate(db))
	return db
}
