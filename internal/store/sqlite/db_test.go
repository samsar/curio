package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAndMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "curio.db")
	db, err := Open(path)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, Migrate(db))

	// schema_meta should be populated by the initial migration.
	var version int
	var model string
	var dim int
	err = db.QueryRow(`SELECT schema_version, embedding_model, embedding_dim FROM schema_meta WHERE id=1`).
		Scan(&version, &model, &dim)
	require.NoError(t, err)
	// schema_version reflects the latest applied migration.
	assert.GreaterOrEqual(t, version, 1)
	assert.Equal(t, "nomic-embed-text", model)
	assert.Equal(t, 768, dim)
}

func TestMigrate_Idempotent(t *testing.T) {
	db := NewEphemeralDB(t)
	// Running migrations again should be a no-op.
	require.NoError(t, Migrate(db))
}

func TestPragmasApplied(t *testing.T) {
	db := NewEphemeralDB(t)

	// foreign_keys is per-connection; query it from a pooled conn.
	var fk int
	require.NoError(t, db.QueryRow("PRAGMA foreign_keys").Scan(&fk))
	assert.Equal(t, 1, fk, "foreign_keys should be ON")

	var jm string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&jm))
	assert.Equal(t, "wal", jm)
}

func TestSqliteVecLoaded(t *testing.T) {
	db := NewEphemeralDB(t)

	// vec_version() is provided by sqlite-vec. If the extension didn't
	// load, this query fails.
	var version string
	require.NoError(t, db.QueryRow(`SELECT vec_version()`).Scan(&version))
	assert.NotEmpty(t, version, "sqlite-vec should report a version string")
}

func TestChunksVecTableExists(t *testing.T) {
	db := NewEphemeralDB(t)

	// The migration creates chunks_vec; verify it's queryable.
	rows, err := db.Query(`SELECT count(*) FROM chunks_vec`)
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var n int
	require.NoError(t, rows.Scan(&n))
	assert.Equal(t, 0, n)
}

func TestOpen_EmptyPath(t *testing.T) {
	_, err := Open("")
	require.Error(t, err)
}
