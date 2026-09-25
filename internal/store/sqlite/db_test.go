package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAndMigrate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "curio.db")
	db, err := Open(ctx, path)
	require.NoError(t, err)
	defer db.Close()
	assert.Equal(t, path, db.Path())

	require.NoError(t, Migrate(ctx, db))

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

	got, err := ReadSchemaVersion(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, version, got)
}

func TestMigrate_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	applied := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM goose_db_version`).Scan(&n))
		return n
	}
	before := applied()

	require.NoError(t, Migrate(ctx, db))
	assert.Equal(t, before, applied(), "a second run applies nothing")
}

func TestMigrate_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "curio.db"))
	require.NoError(t, err)
	defer db.Close()

	require.ErrorIs(t, Migrate(ctx, db), context.Canceled)
}

// TestMigrate_Parallel: migrating separate databases at once is safe. Each
// Migrate uses its own goose Provider; goose's package-level API shares
// global state and races here.
func TestMigrate_Parallel(t *testing.T) {
	for i := range 6 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			db := newTestDB(t)
			v, err := ReadSchemaVersion(context.Background(), db)
			require.NoError(t, err)
			assert.Positive(t, v)
		})
	}
}

func TestReadSchemaVersion_Unmigrated(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "curio.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = ReadSchemaVersion(context.Background(), db)
	require.ErrorContains(t, err, "read schema version")
}

func TestPragmasApplied(t *testing.T) {
	db := newTestDB(t)

	// foreign_keys is per-connection; query it from a pooled conn.
	var fk int
	require.NoError(t, db.QueryRow("PRAGMA foreign_keys").Scan(&fk))
	assert.Equal(t, 1, fk, "foreign_keys should be ON")

	var jm string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&jm))
	assert.Equal(t, "wal", jm)
}

func TestSqliteVecLoaded(t *testing.T) {
	db := newTestDB(t)

	// vec_version() is provided by sqlite-vec. If the extension didn't
	// load, this query fails.
	var version string
	require.NoError(t, db.QueryRow(`SELECT vec_version()`).Scan(&version))
	assert.NotEmpty(t, version, "sqlite-vec should report a version string")
}

func TestChunksVecTableExists(t *testing.T) {
	db := newTestDB(t)

	// The migration creates chunks_vec; verify it's queryable.
	rows, err := db.Query(`SELECT count(*) FROM chunks_vec`)
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var n int
	require.NoError(t, rows.Scan(&n))
	require.NoError(t, rows.Err())
	assert.Equal(t, 0, n)
}

func TestOpen_EmptyPath(t *testing.T) {
	_, err := Open(context.Background(), "")
	require.Error(t, err)
}
