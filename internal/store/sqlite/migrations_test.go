package sqlite

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/migrations"
)

// These tests drive goose's Provider directly, the way Migrate does, so they
// can run migrations from any fs.FS and stop at a chosen version.

func openUnmigrated(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "curio.db")
	db, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	return db, path
}

func newProvider(t *testing.T, db *DB, fsys fs.FS) *goose.Provider {
	t.Helper()
	p, err := goose.NewProvider(goose.DialectSQLite3, db.DB, fsys)
	require.NoError(t, err)
	return p
}

// migratedTo returns a database the real migrations have brought to
// version, and the provider that can move it on.
func migratedTo(t *testing.T, version int64) (*DB, *goose.Provider) {
	t.Helper()
	db, _ := openUnmigrated(t)
	p := newProvider(t, db, migrations.FS)
	_, err := p.UpTo(context.Background(), version)
	require.NoError(t, err)
	return db, p
}

// latestMigration is the highest numeric prefix among the migration files.
func latestMigration(t *testing.T) int64 {
	t.Helper()
	entries, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)
	var latest int64
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		require.True(t, ok, e.Name())
		n, err := strconv.ParseInt(prefix, 10, 64)
		require.NoError(t, err, e.Name())
		latest = max(latest, n)
	}
	return latest
}

func hasColumn(t *testing.T, db *DB, table, column string) bool {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`,
		table, column).Scan(&n))
	return n > 0
}

// realMigrations returns the embedded migrations, with 002 swapped for
// replace002 when it is non-nil.
func realMigrations(t *testing.T, replace002 []byte) fstest.MapFS {
	t.Helper()
	const name002 = "002_add_html_source.sql"
	out := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)
	for _, e := range entries {
		data, err := fs.ReadFile(migrations.FS, e.Name())
		require.NoError(t, err)
		out[e.Name()] = &fstest.MapFile{Data: data}
	}
	if replace002 != nil {
		out[name002] = &fstest.MapFile{Data: replace002}
	}
	return out
}

type schemaObject struct {
	Type, Name, TblName string
	SQL                 sql.NullString
}

func schemaDump(t *testing.T, db *DB) []schemaObject {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name`)
	require.NoError(t, err)
	defer rows.Close()
	var out []schemaObject
	for rows.Next() {
		var o schemaObject
		require.NoError(t, rows.Scan(&o.Type, &o.Name, &o.TblName, &o.SQL))
		out = append(out, o)
	}
	require.NoError(t, rows.Err())
	return out
}

// assertForeignKeysOnEverywhere holds several connections at once, so the
// one a migration ran on is among them, and checks each enforces FKs.
func assertForeignKeysOnEverywhere(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	const held = 4
	conns := make([]*sql.Conn, 0, held)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for range held {
		c, err := db.Conn(ctx)
		require.NoError(t, err)
		conns = append(conns, c)
		var on int
		require.NoError(t, c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&on))
		assert.Equal(t, 1, on, "foreign_keys must be back on for every pooled connection")
	}
}

// TestMigration002_FixKeepsSchema: rewriting 002 into the safe rebuild
// recipe must not change what it produces, so databases migrated with the
// original are indistinguishable from new ones.
func TestMigration002_FixKeepsSchema(t *testing.T) {
	ctx := context.Background()
	orig, err := os.ReadFile(filepath.Join("testdata", "002_add_html_source.orig.sql"))
	require.NoError(t, err)

	fixedDB, _ := openUnmigrated(t)
	_, err = newProvider(t, fixedDB, realMigrations(t, nil)).Up(ctx)
	require.NoError(t, err)

	origDB, _ := openUnmigrated(t)
	_, err = newProvider(t, origDB, realMigrations(t, orig)).Up(ctx)
	require.NoError(t, err)

	assert.Equal(t, schemaDump(t, origDB), schemaDump(t, fixedDB))
}

// Synthetic parent/child schema for exercising the rebuild recipe on a
// table other tables reference.
const recipeSchema = `-- +goose Up
-- +goose StatementBegin
CREATE TABLE parent (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE child_cascade (id INTEGER PRIMARY KEY,
    parent_id INTEGER NOT NULL REFERENCES parent(id) ON DELETE CASCADE);
CREATE TABLE child_setnull (id INTEGER PRIMARY KEY,
    parent_id INTEGER REFERENCES parent(id) ON DELETE SET NULL);
INSERT INTO parent (id, name) VALUES (1, 'a'), (2, 'b');
INSERT INTO child_cascade (id, parent_id) VALUES (10, 1), (11, 2);
INSERT INTO child_setnull (id, parent_id) VALUES (20, 1), (21, 2);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE child_setnull;
DROP TABLE child_cascade;
DROP TABLE parent;
-- +goose StatementEnd
`

// safeRebuild is the migrations/README.md recipe applied to parent.
// keepRows filters the copy, so a test can make the rebuild break a
// reference.
func safeRebuild(keepRows string) string {
	return `-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;
CREATE TABLE parent_new (id INTEGER PRIMARY KEY, name TEXT NOT NULL CHECK (name <> ''));
INSERT INTO parent_new SELECT id, name FROM parent WHERE ` + keepRows + `;
DROP TABLE parent;
ALTER TABLE parent_new RENAME TO parent;
CREATE TEMP TABLE _fk_guard (violations INTEGER NOT NULL CHECK (violations = 0));
INSERT INTO _fk_guard SELECT count(*) FROM pragma_foreign_key_check;
DROP TABLE temp._fk_guard;
COMMIT;
PRAGMA foreign_keys = ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
`
}

// unsafeRebuild is the recipe the original 002 used: the PRAGMAs run inside
// goose's transaction, where SQLite ignores them.
const unsafeRebuild = `-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys = OFF;
CREATE TABLE parent_new (id INTEGER PRIMARY KEY, name TEXT NOT NULL CHECK (name <> ''));
INSERT INTO parent_new SELECT id, name FROM parent;
DROP TABLE parent;
ALTER TABLE parent_new RENAME TO parent;
PRAGMA foreign_keys = ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
`

func recipeMigrations(rebuild string) fstest.MapFS {
	return fstest.MapFS{
		"001_schema.sql":  &fstest.MapFile{Data: []byte(recipeSchema)},
		"002_rebuild.sql": &fstest.MapFile{Data: []byte(rebuild)},
	}
}

func TestRebuildRecipe_ParentTable(t *testing.T) {
	cases := []struct {
		name         string
		rebuild      string
		wantChildren bool
	}{
		{name: "safe recipe keeps children", rebuild: safeRebuild("1"), wantChildren: true},
		// Why the recipe exists: DROP TABLE on a parent runs its ON DELETE
		// actions when foreign keys are on, which they still are here.
		{name: "transactional recipe loses children", rebuild: unsafeRebuild, wantChildren: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := openUnmigrated(t)
			_, err := newProvider(t, db, recipeMigrations(tc.rebuild)).Up(context.Background())
			require.NoError(t, err)

			var cascaded, linked int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM child_cascade`).Scan(&cascaded))
			require.NoError(t, db.QueryRow(`SELECT count(parent_id) FROM child_setnull`).Scan(&linked))
			if !tc.wantChildren {
				assert.Zero(t, cascaded, "CASCADE children deleted")
				assert.Zero(t, linked, "SET NULL references nulled")
				return
			}
			assert.Equal(t, 2, cascaded)
			assert.Equal(t, 2, linked)

			assertForeignKeysOnEverywhere(t, db)
			_, err = db.Exec(`INSERT INTO child_cascade (id, parent_id) VALUES (99, 999)`)
			assert.ErrorContains(t, err, "FOREIGN KEY constraint failed")
		})
	}
}

// TestRebuildRecipe_GuardAbortsBrokenRebuild: a rebuild that drops a
// referenced row fails the guard, and nothing it did is committed.
func TestRebuildRecipe_GuardAbortsBrokenRebuild(t *testing.T) {
	ctx := context.Background()
	poisoned, path := openUnmigrated(t)
	p := newProvider(t, poisoned, recipeMigrations(safeRebuild("id <> 1")))

	_, err := p.UpTo(ctx, 1)
	require.NoError(t, err)
	_, err = p.Up(ctx)
	require.ErrorContains(t, err, "CHECK constraint failed: violations = 0")

	// The failed rebuild leaves its connection mid-transaction with foreign
	// keys off, so the handle is unusable: inspect through a fresh one.
	fresh, err := Open(ctx, path)
	require.NoError(t, err)
	defer fresh.Close()

	var parents, version int
	require.NoError(t, fresh.QueryRow(`SELECT count(*) FROM parent`).Scan(&parents))
	assert.Equal(t, 2, parents, "the rebuild rolled back")
	var parentSQL string
	require.NoError(t, fresh.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'parent'`).Scan(&parentSQL))
	assert.NotContains(t, parentSQL, "CHECK", "the original table is still in place")
	require.NoError(t, fresh.QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&version))
	assert.Equal(t, 1, version, "the failed migration isn't recorded")

	require.NoError(t, poisoned.Close())
}

// TestMigration002_UpgradesLinkedBookmarks runs the real 002 on a v1
// database whose bookmarks reference documents, then the rest through
// Migrate, as the daemon does.
func TestMigration002_UpgradesLinkedBookmarks(t *testing.T) {
	ctx := context.Background()
	db, _ := openUnmigrated(t)
	p := newProvider(t, db, migrations.FS)
	_, err := p.UpTo(ctx, 1)
	require.NoError(t, err)

	_, err = db.Exec(`
		INSERT INTO documents (id, tenant_id, url) VALUES ('d1', 'local', 'https://example.com/1'),
		                                                  ('d2', 'local', 'https://example.com/2');
		INSERT INTO bookmarks (id, tenant_id, document_id, url, saved_at, source) VALUES
			('b1', 'local', 'd1', 'https://example.com/1', '2024-01-01T00:00:00.000Z', 'chrome'),
			('b2', 'local', 'd2', 'https://example.com/2', '2024-01-01T00:00:00.000Z', 'safari'),
			('b3', 'local', NULL, 'https://example.com/3', '2024-01-01T00:00:00.000Z', 'manual');`)
	require.NoError(t, err)

	_, err = p.UpTo(ctx, 2)
	require.NoError(t, err)
	// 002 recreates what DROP TABLE took with it. Later migrations drop the
	// trigger, so it is checked here.
	for _, name := range []string{"idx_bookmarks_tenant_source", "idx_bookmarks_document",
		"idx_bookmarks_folder", "trg_bookmarks_updated_at"} {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n))
		assert.Equal(t, 1, n, name)
	}

	_, err = Migrate(ctx, db)
	require.NoError(t, err)

	assertLinks := func(want map[string]sql.NullString) {
		t.Helper()
		rows, err := db.Query(`SELECT id, document_id FROM bookmarks WHERE source <> 'html' ORDER BY id`)
		require.NoError(t, err)
		defer rows.Close()
		got := map[string]sql.NullString{}
		for rows.Next() {
			var id string
			var docID sql.NullString
			require.NoError(t, rows.Scan(&id, &docID))
			got[id] = docID
		}
		require.NoError(t, rows.Err())
		assert.Equal(t, want, got)
	}
	links := map[string]sql.NullString{
		"b1": {String: "d1", Valid: true},
		"b2": {String: "d2", Valid: true},
		"b3": {},
	}
	assertLinks(links)

	bms := NewBookmarks(db)
	require.NoError(t, bms.Create(ctx, &store.Bookmark{TenantID: "local", URL: "https://example.com/4",
		Source: store.SourceHTML, SavedAt: time.Now().UTC()}), "source=html is accepted after 002")

	assertForeignKeysOnEverywhere(t, db)

	_, err = p.DownTo(ctx, 1)
	require.NoError(t, err)
	assertLinks(links)
	var html int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM bookmarks WHERE source = 'html'`).Scan(&html))
	assert.Zero(t, html, "down deletes the rows v1 can't hold")
	assertForeignKeysOnEverywhere(t, db)
}

// TestMigration005_DropsSchemaVersion: schema_meta loses its copy of the
// version and keeps everything else. Down brings the column back at 4, so
// the Downs of 004 through 002, which set it, still run.
func TestMigration005_DropsSchemaVersion(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 4)
	_, err := db.Exec(`UPDATE schema_meta SET created_at = '2024-01-01T00:00:00.000Z',
		updated_at = '2024-02-01T00:00:00.000Z' WHERE id = 1`)
	require.NoError(t, err)
	meta := func() []any {
		var model, created, updated string
		var dim int
		require.NoError(t, db.QueryRow(`SELECT embedding_model, embedding_dim, created_at, updated_at
			FROM schema_meta WHERE id = 1`).Scan(&model, &dim, &created, &updated))
		return []any{model, dim, created, updated}
	}
	before := meta()

	_, err = p.UpTo(ctx, 5)
	require.NoError(t, err)
	assert.False(t, hasColumn(t, db, "schema_meta", "schema_version"))
	assert.Equal(t, before, meta())

	_, err = p.DownTo(ctx, 4)
	require.NoError(t, err)
	var version int
	require.NoError(t, db.QueryRow(`SELECT schema_version FROM schema_meta WHERE id = 1`).Scan(&version))
	assert.Equal(t, 4, version)
	assert.Equal(t, before, meta())

	_, err = p.DownTo(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT schema_version FROM schema_meta WHERE id = 1`).Scan(&version))
	assert.Equal(t, 1, version)
}

// dumpRows reads every row query returns, as the driver hands the values
// back, for comparing a table before and after a migration.
func dumpRows(t *testing.T, db *DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		out = append(out, vals)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestMigration006_DropsUpdatedAtTriggers: the triggers go, the rows stay
// as they were, and an UPDATE no longer rewrites updated_at behind the
// statement's back. Down recreates the triggers exactly.
func TestMigration006_DropsUpdatedAtTriggers(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 5)
	const old = "2024-01-01T00:00:00.000Z"
	_, err := db.Exec(`
		INSERT INTO documents (id, tenant_id, url, updated_at) VALUES ('d1', 'local', 'https://example.com/1', '` + old + `');
		INSERT INTO bookmarks (id, tenant_id, document_id, url, saved_at, source, updated_at)
			VALUES ('b1', 'local', 'd1', 'https://example.com/1', '` + old + `', 'chrome', '` + old + `');
		INSERT INTO jobs (id, tenant_id, kind, payload, status, updated_at)
			VALUES ('j1', 'local', 'fetch', '{"document_id":"d1"}', 'done', '` + old + `');
		INSERT INTO cluster_runs (id, tenant_id, algo, updated_at) VALUES ('r1', 'local', 'knn-graph', '` + old + `');
		INSERT INTO clusters (id, tenant_id, run_id, label, updated_at) VALUES ('c1', 'local', 'r1', 'x', '` + old + `');`)
	require.NoError(t, err)

	tables := []string{"documents", "bookmarks", "jobs", "cluster_runs", "clusters"}
	const triggersQ = `SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'trg%updated_at' ORDER BY name`
	triggers := dumpRows(t, db, triggersQ)
	require.Len(t, triggers, len(tables))
	before := map[string][][]any{}
	for _, table := range tables {
		before[table] = dumpRows(t, db, `SELECT * FROM `+table)
	}

	_, err = p.UpTo(ctx, 6)
	require.NoError(t, err)
	assert.Empty(t, dumpRows(t, db, triggersQ))
	for _, table := range tables {
		assert.Equal(t, before[table], dumpRows(t, db, `SELECT * FROM `+table), table)
	}
	_, err = db.Exec(`UPDATE jobs SET status = 'failed' WHERE id = 'j1'`)
	require.NoError(t, err)
	var updatedAt string
	require.NoError(t, db.QueryRow(`SELECT updated_at FROM jobs WHERE id = 'j1'`).Scan(&updatedAt))
	assert.Equal(t, old, updatedAt, "only the statement writes updated_at")

	_, err = p.DownTo(ctx, 5)
	require.NoError(t, err)
	assert.Equal(t, triggers, dumpRows(t, db, triggersQ))
}
