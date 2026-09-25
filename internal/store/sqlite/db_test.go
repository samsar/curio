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

	version, err := Migrate(ctx, db)
	require.NoError(t, err)

	var model string
	var dim int
	err = db.QueryRow(`SELECT embedding_model, embedding_dim FROM schema_meta WHERE id=1`).Scan(&model, &dim)
	require.NoError(t, err)
	assert.Equal(t, "nomic-embed-text", model)
	assert.Equal(t, 768, dim)

	// goose_db_version is the only record of the version.
	assert.Equal(t, latestMigration(t), version, "the newest migration file")
	var recorded int64
	require.NoError(t, db.QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&recorded))
	assert.Equal(t, recorded, version)
	var copies int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('schema_meta') WHERE name = 'schema_version'`).Scan(&copies))
	assert.Zero(t, copies, "schema_meta keeps no copy of the version")
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

	version, err := Migrate(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, before, applied(), "a second run applies nothing")
	assert.Equal(t, latestMigration(t), version, "and still reports the version")
}

func TestMigrate_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "curio.db"))
	require.NoError(t, err)
	defer db.Close()

	_, err = Migrate(ctx, db)
	require.ErrorIs(t, err, context.Canceled)
}

// TestMigrate_Parallel: migrating separate databases at once is safe. Each
// Migrate uses its own goose Provider; goose's package-level API shares
// global state and races here.
func TestMigrate_Parallel(t *testing.T) {
	for i := range 6 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			db, err := Open(context.Background(), filepath.Join(t.TempDir(), "curio.db"))
			require.NoError(t, err)
			defer db.Close()
			v, err := Migrate(context.Background(), db)
			require.NoError(t, err)
			assert.Equal(t, latestMigration(t), v)
		})
	}
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
