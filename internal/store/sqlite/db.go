// Package sqlite is the SQLite implementation of curio's storage interfaces.
//
// Open returns a *sql.DB configured with the pragmas curio depends on
// (foreign_keys = ON per-connection, WAL, sane busy timeout), immediate
// transactions, a pool sized for the daemon's workers, and the sqlite-vec
// extension auto-loaded on every connection.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3" // sqlite3 driver
	"github.com/pressly/goose/v3"

	"github.com/samsar/curio/migrations"
)

// vecOnce runs sqlite_vec.Auto() once per process. Running it again would be
// harmless, since sqlite3_auto_extension ignores an entry point it already
// has; the Once only saves the repeated cgo call on every Open.
var vecOnce sync.Once

// Pool policy. A connection is held by every transaction and by every write
// waiting out busy_timeout, so the cap sits above the daemon's default 21
// worker goroutines (16 fetch, 4 index, 1 cluster) with room for the API;
// at the cap, reads would queue behind the waiting writers. Idle
// connections are kept up to the cap because opening one (pragmas,
// sqlite-vec) costs about 0.7 ms against a few microseconds for a query on
// a pooled one, and database/sql's default of two idle connections had the
// workers reopening them constantly. They close after five idle minutes,
// releasing their page caches while the daemon has nothing to do.
const (
	maxOpenConns    = 32
	maxIdleConns    = maxOpenConns
	connMaxIdleTime = 5 * time.Minute
)

// DB is a thin wrapper around *sql.DB. Lets us attach lifecycle methods
// without polluting the standard interface.
//
// Every transaction begins with BEGIN IMMEDIATE (the DSN's _txlock), taking
// the write lock up front: busy_timeout then covers the whole transaction,
// which can never fail upgrading a read lock to a write lock, whatever order
// its statements run in. So BeginTx is for writing only, and every caller
// writes. mattn/go-sqlite3 ignores sql.TxOptions.ReadOnly, so a read must
// never open a transaction: as autocommit statements, reads in WAL mode
// never wait on a writer.
type DB struct {
	*sql.DB
	path     string
	enqueued *enqueueSignal // wakes workers when a job becomes claimable
}

// Open opens (or creates) the SQLite database file at path. ":memory:" is
// rejected: each pooled connection would open a database of its own.
//
// Open does NOT run migrations. Call Migrate after.
func Open(ctx context.Context, path string) (*DB, error) {
	vecOnce.Do(func() { sqlitevec.Auto() })

	dsn, err := buildDSN(path)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxIdleTime(connMaxIdleTime)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping sqlite %q: %w", path, errors.Join(err, db.Close()))
	}
	return &DB{DB: db, path: path, enqueued: newEnqueueSignal()}, nil
}

// Migrate applies pending migrations from the embedded FS and returns the
// schema version the database is left at: the highest version in goose's
// goose_db_version table, the only record of it. Idempotent.
//
// After applying any migration it checkpoints the WAL and truncates it. A
// migration that rewrites a table writes all of it through the WAL, and
// SQLite's automatic checkpoints copy those pages back but never shrink the
// file, so a rewrite of the chunks table would otherwise leave a WAL about
// its size on disk, which `curio status` reports.
//
// It uses goose's Provider, which keeps its state per instance: goose's
// package-level API (SetBaseFS, SetDialect, Up) reads and writes process
// globals, a data race when two databases migrate at once.
//
// After an error, close db and don't reuse it: a table-rebuild migration
// runs outside goose's transaction (see migrations/README.md), and one that
// fails leaves its pooled connection inside an open transaction with
// foreign keys off.
func Migrate(ctx context.Context, db *DB) (int64, error) {
	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, migrations.FS)
	if err != nil {
		return 0, fmt.Errorf("load migrations: %w", err)
	}
	applied, err := provider.Up(ctx)
	if err != nil {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if len(applied) > 0 {
		// The result row's busy flag is set only when a reader holds the WAL
		// open; nothing else uses the database while it migrates, and if
		// something did, automatic checkpoints would still catch up.
		if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return 0, fmt.Errorf("checkpoint after migrating: %w", err)
		}
	}
	return version, nil
}

// Path returns the path Open was called with.
func (d *DB) Path() string { return d.path }

// buildDSN produces a connection string that sets curio's required pragmas
// on every pooled connection. Without this, foreign_keys defaults to OFF
// per connection and the schema's FK constraints silently aren't enforced.
// It also makes every transaction BEGIN IMMEDIATE (see DB).
//
// mattn/go-sqlite3 uses `_fk`, `_journal_mode`, `_synchronous`,
// `_busy_timeout`, `_txlock` query params (NOT `_pragma=`, which is
// modernc.org/sqlite).
func buildDSN(path string) (string, error) {
	switch path {
	case "":
		return "", errors.New("sqlite path must not be empty")
	case ":memory:":
		return "", errors.New("sqlite: in-memory databases are not supported: " +
			"each pooled connection would open a database of its own")
	}

	q := url.Values{}
	q.Set("_fk", "true")
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "NORMAL")
	q.Set("_busy_timeout", "5000")
	q.Set("_txlock", "immediate")

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve sqlite path %q: %w", path, err)
	}
	return "file:" + abs + "?" + q.Encode(), nil
}
