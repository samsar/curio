package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
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
		t.Run(strconv.Itoa(i), func(t *testing.T) {
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

// TestPragmasOnEveryConnection holds more connections at once than
// database/sql keeps by default and checks each one: the DSN has to set
// curio's pragmas on every connection the pool opens.
func TestPragmasOnEveryConnection(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	const held = 8
	for range held {
		c, err := db.Conn(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() }) // all held until the end
		for _, p := range []struct {
			pragma string
			want   any
		}{
			{"busy_timeout", int64(5000)},
			{"foreign_keys", int64(1)},
			{"journal_mode", "wal"},
			{"synchronous", int64(1)}, // NORMAL
		} {
			var got any
			require.NoError(t, c.QueryRowContext(ctx, "PRAGMA "+p.pragma).Scan(&got))
			if b, ok := got.([]byte); ok {
				got = string(b)
			}
			assert.Equal(t, p.want, got, p.pragma)
		}
	}
	assert.GreaterOrEqual(t, db.Stats().OpenConnections, held)
}

// TestTransactions_ReadThenWrite: a transaction that reads and then writes
// while others do the same must wait for the lock, not fail. Deferred
// transactions get SQLITE_BUSY at once upgrading the read to a write,
// without consulting busy_timeout; immediate ones wait at BEGIN.
func TestTransactions_ReadThenWrite(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "curio.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE counter (id INTEGER PRIMARY KEY, n INTEGER NOT NULL);
		INSERT INTO counter (id, n) VALUES (1, 0)`)
	require.NoError(t, err)

	increment := func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT n FROM counter WHERE id = 1`).Scan(&n); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE counter SET n = ? WHERE id = 1`, n+1); err != nil {
			return err
		}
		return tx.Commit()
	}

	const workers, each = 8, 25
	errs := make(chan error, workers*each)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range each {
				errs <- increment()
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var n int
	require.NoError(t, db.QueryRow(`SELECT n FROM counter WHERE id = 1`).Scan(&n))
	assert.Equal(t, workers*each, n, "no increment lost")
}

// TestReads_DoNotWaitForWriter: in WAL mode an autocommit read never waits
// on the write lock, so the store's reads return promptly while a writer
// holds it, far inside busy_timeout.
func TestReads_DoNotWaitForWriter(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs, jobs, chunks := NewDocuments(db), NewJobs(db), NewChunks(db, vecDim)
	ids := seedDocs(t, db, "local", "https://example.com/a")
	require.NoError(t, chunks.ReplaceForDocument(ctx, ids[0], latestExtractionID(t, db, ids[0]), "", nil,
		[]store.ChunkInput{{Text: "readers keep reading", Embedding: fillVec(0.1)}}))

	writer, err := db.Conn(ctx)
	require.NoError(t, err)
	defer writer.Close()
	tx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE documents SET title = 'held' WHERE id = ?`, ids[0])
	require.NoError(t, err)

	rctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err = docs.GetByID(rctx, ids[0])
	require.NoError(t, err)
	_, err = docs.ListWithLastError(rctx, "local", store.ListDocumentsOpts{})
	require.NoError(t, err)
	_, err = jobs.CountByStatus(rctx, "local")
	require.NoError(t, err)
	hits, err := chunks.BM25Search(rctx, "local", "readers", 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.Len(t, hits, 1)
}

// TestPool_KeepsConnectionsForTheWorkers: the pool is capped above the
// daemon's 21 workers and keeps what it opens, so a burst of claims closes
// no connection only to reopen it.
func TestPool_KeepsConnectionsForTheWorkers(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	assert.Equal(t, maxOpenConns, db.Stats().MaxOpenConnections)

	q := NewJobs(db)
	for range 200 {
		require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch}))
	}
	const workers = 21
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				j, err := q.ClaimNext(ctx, []store.JobKind{store.JobKindFetch})
				if errors.Is(err, store.ErrNotFound) {
					return
				}
				if err == nil {
					err = q.MarkDone(ctx, j.ID)
				}
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Zero(t, db.Stats().MaxIdleClosed, "no connection was closed for exceeding the idle limit")
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

func TestOpen_RejectsPath(t *testing.T) {
	for _, path := range []string{"", ":memory:"} {
		_, err := Open(context.Background(), path)
		require.Error(t, err, "%q", path)
	}
}
