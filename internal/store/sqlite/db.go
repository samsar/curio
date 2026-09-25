// Package sqlite is the SQLite implementation of curio's storage interfaces.
//
// Open returns a *sql.DB configured with the pragmas curio depends on
// (foreign_keys = ON per-connection, WAL, sane busy timeout) and the
// sqlite-vec extension auto-loaded on every connection.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3" // sqlite3 driver
	"github.com/pressly/goose/v3"

	"github.com/samsar/curio/migrations"
)

// vecOnce makes sure sqlite_vec.Auto() runs exactly once per process.
// Calling it multiple times is harmless but the bindings panic if the
// extension's already registered.
var vecOnce sync.Once

// DB is a thin wrapper around *sql.DB. Lets us attach lifecycle methods
// without polluting the standard interface.
type DB struct {
	*sql.DB
	path string
}

// Open opens (or creates) a SQLite database at path. Use ":memory:" for
// ephemeral in-memory storage in tests, but pair it with SetMaxOpenConns(1)
// or use shared cache to avoid the "each connection is a different DB"
// footgun.
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
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping sqlite %q: %w", path, errors.Join(err, db.Close()))
	}
	return &DB{DB: db, path: path}, nil
}

// Migrate applies pending migrations from the embedded FS and returns the
// schema version the database is left at: the highest version in goose's
// goose_db_version table, the only record of it. Idempotent.
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
	if _, err := provider.Up(ctx); err != nil {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// Path returns the path Open was called with.
func (d *DB) Path() string { return d.path }

// buildDSN produces a connection string that sets curio's required pragmas
// on every pooled connection. Without this, foreign_keys defaults to OFF
// per connection and the schema's FK constraints silently aren't enforced.
//
// mattn/go-sqlite3 uses `_fk`, `_journal_mode`, `_synchronous`,
// `_busy_timeout` query params (NOT `_pragma=`, which is modernc.org/sqlite).
func buildDSN(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("sqlite path must not be empty")
	}

	q := url.Values{}
	q.Set("_fk", "true")
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "NORMAL")
	q.Set("_busy_timeout", "5000")

	// In-memory has its own form.
	if path == ":memory:" {
		return ":memory:?" + q.Encode(), nil
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve sqlite path %q: %w", path, err)
	}
	return "file:" + abs + "?" + q.Encode(), nil
}
