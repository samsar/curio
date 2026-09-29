package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/migrations"
)

// These tests drive goose's Provider directly, the way Migrate does, so they
// can run migrations from any fs.FS and stop at a chosen version.

// migration001Dim is the width migration 001 creates chunks_vec with, which
// a database stopped before the daemon's EnsureVectorIndex keeps.
const migration001Dim = 768

func openUnmigrated(t testing.TB) (*DB, string) {
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

// latestMigration is the newest version among the embedded migrations, as
// goose reads them.
func latestMigration(t *testing.T) int64 {
	t.Helper()
	db, _ := openUnmigrated(t)
	sources := newProvider(t, db, migrations.FS).ListSources()
	require.NotEmpty(t, sources)
	return sources[len(sources)-1].Version // ListSources sorts by version
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

// TestMigration007_BackfillsJobDocumentID: jobs.document_id is filled in
// from the payload when the named document exists and left NULL otherwise,
// nothing else in the rows changes, and the lists read through it. Down
// restores the previous schema.
func TestMigration007_BackfillsJobDocumentID(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 6)
	_, err := db.Exec(`
		INSERT INTO documents (id, tenant_id, url, title, state, updated_at) VALUES
			('d1', 'local', 'https://example.com/1', 'One', 'fetched', '2024-01-01T00:00:01.000Z'),
			('d2', 'local', 'https://example.com/2', 'Two', 'failed',  '2024-01-01T00:00:02.000Z');
		INSERT INTO jobs (id, tenant_id, kind, payload, status, attempts, run_after, last_error,
		                  created_at, updated_at, started_at) VALUES
			('fetch-d1-done',    'local', 'fetch',     '{"document_id":"d1"}',   'done',    1,
			 '2024-01-01T00:00:00.000Z', NULL,           '2024-01-01T00:00:00.000Z', '2024-01-01T00:01:00.000Z', '2024-01-01T00:00:30.000Z'),
			('fetch-d2-failed',  'local', 'fetch',     '{"document_id":"d2"}',   'failed',  5,
			 '2024-01-01T00:10:00.000Z', 'older error',  '2024-01-01T00:00:00.000Z', '2024-01-01T00:02:00.000Z', '2024-01-01T00:01:30.000Z'),
			('index-d2-failed',  'local', 'index',     '{"document_id":"d2"}',   'failed',  1,
			 '2024-01-01T00:00:00.000Z', 'newest error', '2024-01-01T00:00:00.000Z', '2024-01-01T00:03:00.000Z', '2024-01-01T00:02:30.000Z'),
			('index-d1-pending', 'local', 'index',     '{"document_id":"d1"}',   'pending', 0,
			 '2024-01-01T00:00:00.000Z', NULL,           '2024-01-01T00:00:00.000Z', '2024-01-01T00:04:00.000Z', NULL),
			('cluster',          'local', 'cluster',   '{}',                     'done',    1,
			 '2024-01-01T00:00:00.000Z', NULL,           '2024-01-01T00:00:00.000Z', '2024-01-01T00:05:00.000Z', '2024-01-01T00:04:30.000Z'),
			('fetch-gone',       'local', 'fetch',     '{"document_id":"gone"}', 'failed',  5,
			 '2024-01-01T00:00:00.000Z', 'gone error',   '2024-01-01T00:00:00.000Z', '2024-01-01T00:06:00.000Z', '2024-01-01T00:05:30.000Z'),
			('summarize-array',  'local', 'summarize', '[1, 2]',                 'done',    1,
			 '2024-01-01T00:00:00.000Z', NULL,           '2024-01-01T00:00:00.000Z', '2024-01-01T00:07:00.000Z', '2024-01-01T00:06:30.000Z');`)
	require.NoError(t, err)
	const oldColumns = `SELECT id, tenant_id, kind, payload, status, attempts, run_after, last_error,
		created_at, updated_at, started_at FROM jobs ORDER BY id`
	jobsBefore := dumpRows(t, db, oldColumns)
	schemaBefore := schemaDump(t, db)

	_, err = p.UpTo(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, jobsBefore, dumpRows(t, db, oldColumns), "the backfill changes nothing else")
	docIDs := map[string]any{}
	for _, row := range dumpRows(t, db, `SELECT id, document_id FROM jobs`) {
		docIDs[row[0].(string)] = row[1]
	}
	assert.Equal(t, map[string]any{
		"fetch-d1-done": "d1", "fetch-d2-failed": "d2", "index-d2-failed": "d2", "index-d1-pending": "d1",
		"cluster": nil, "fetch-gone": nil, "summarize-array": nil,
	}, docIDs)
	assert.Empty(t, dumpRows(t, db, `PRAGMA foreign_key_check`))

	// The store reads the latest schema.
	_, err = p.Up(ctx)
	require.NoError(t, err)
	docs, err := NewDocuments(db).ListWithLastError(ctx, "local", store.ListDocumentsOpts{})
	require.NoError(t, err)
	require.Len(t, docs, 2)
	assert.Equal(t, "d2", docs[0].ID)
	assert.Equal(t, "newest error", docs[0].LastError)
	assert.Equal(t, "d1", docs[1].ID)
	assert.Empty(t, docs[1].LastError)

	jobs, err := NewJobs(db).ListWithDoc(ctx, "local", store.ListJobsOpts{Status: store.JobStatusFailed})
	require.NoError(t, err)
	require.Len(t, jobs, 3)
	got := map[string]string{}
	for _, j := range jobs {
		got[j.ID] = j.URL
	}
	assert.Equal(t, map[string]string{"fetch-gone": "", "index-d2-failed": "https://example.com/2",
		"fetch-d2-failed": "https://example.com/2"}, got)

	_, err = p.DownTo(ctx, 6)
	require.NoError(t, err)
	assert.Equal(t, schemaBefore, schemaDump(t, db))
	assert.Equal(t, jobsBefore, dumpRows(t, db, oldColumns))
}

// TestMigration009_KeysetIndexes: the list indexes gain id as their last
// column and bookmarks page by created_at; the rows are untouched, and Down
// restores the schema exactly.
func TestMigration009_KeysetIndexes(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 8)
	_, err := db.Exec(`
		INSERT INTO documents (id, tenant_id, url) VALUES ('d1', 'local', 'https://example.com/1');
		INSERT INTO bookmarks (id, tenant_id, document_id, url, saved_at, source)
			VALUES ('b1', 'local', 'd1', 'https://example.com/1', '2024-01-01T00:00:00.000Z', 'chrome');
		INSERT INTO jobs (id, tenant_id, kind, payload, status) VALUES ('j1', 'local', 'fetch', '{"document_id":"d1"}', 'done');`)
	require.NoError(t, err)
	schemaBefore := schemaDump(t, db)
	rowsBefore := map[string][][]any{}
	for _, table := range []string{"documents", "bookmarks", "jobs"} {
		rowsBefore[table] = dumpRows(t, db, `SELECT * FROM `+table)
	}

	_, err = p.UpTo(ctx, 9)
	require.NoError(t, err)
	indexes := map[string]string{}
	for _, row := range dumpRows(t, db, `SELECT name, sql FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL`) {
		indexes[row[0].(string)] = row[1].(string)
	}
	for name, columns := range map[string]string{
		"idx_documents_tenant_updated":       "documents(tenant_id, updated_at, id)",
		"idx_documents_tenant_state_updated": "documents(tenant_id, state, updated_at, id)",
		"idx_jobs_tenant_updated":            "jobs(tenant_id, updated_at, id)",
		"idx_jobs_tenant_status_updated":     "jobs(tenant_id, status, updated_at, id)",
		"idx_bookmarks_tenant_created":       "bookmarks(tenant_id, created_at, id)",
	} {
		assert.Equal(t, "CREATE INDEX "+name+" ON "+columns, indexes[name])
	}
	assert.NotContains(t, indexes, "idx_bookmarks_tenant_id")
	for table, rows := range rowsBefore {
		assert.Equal(t, rows, dumpRows(t, db, `SELECT * FROM `+table), table)
	}

	_, err = p.DownTo(ctx, 8)
	require.NoError(t, err)
	assert.Equal(t, schemaBefore, schemaDump(t, db))
}

// TestMigration010_DropsUnusedBookmarkIndexes: 010 drops the two bookmark
// indexes no query reads, keeps every row, and its Down restores the
// schema exactly.
func TestMigration010_DropsUnusedBookmarkIndexes(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 9)
	_, err := db.Exec(`
		INSERT INTO documents (id, tenant_id, url) VALUES ('d1', 'local', 'https://example.com/1');
		INSERT INTO bookmarks (id, tenant_id, document_id, url, saved_at, source, folder_path)
			VALUES ('b1', 'local', 'd1', 'https://example.com/1', '2024-01-01T00:00:00.000Z', 'chrome', '/Tech');`)
	require.NoError(t, err)
	schemaBefore := schemaDump(t, db)
	rowsBefore := dumpRows(t, db, `SELECT * FROM bookmarks`)
	indexes := func() []string {
		rows := dumpRows(t, db, `SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'bookmarks'`)
		names := make([]string, 0, len(rows))
		for _, row := range rows {
			names = append(names, row[0].(string))
		}
		return names
	}
	require.Subset(t, indexes(), []string{"idx_bookmarks_tenant_source", "idx_bookmarks_folder"})

	_, err = p.UpTo(ctx, 10)
	require.NoError(t, err)
	assert.NotContains(t, indexes(), "idx_bookmarks_tenant_source")
	assert.NotContains(t, indexes(), "idx_bookmarks_folder")
	assert.Subset(t, indexes(), []string{"idx_bookmarks_tenant_created", "idx_bookmarks_document"})
	assert.Equal(t, rowsBefore, dumpRows(t, db, `SELECT * FROM bookmarks`))

	_, err = p.DownTo(ctx, 9)
	require.NoError(t, err)
	assert.Equal(t, schemaBefore, schemaDump(t, db))
}

// TestMigration011_QueueSettings: 011 creates the one-row queue_settings
// table, whose constraints refuse any row the gate couldn't read, and its
// Down restores the schema exactly.
func TestMigration011_QueueSettings(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 10)
	schemaBefore := schemaDump(t, db)

	_, err := p.UpTo(ctx, 11)
	require.NoError(t, err)
	for name, insert := range map[string]string{
		"a second row":        `INSERT INTO queue_settings (id) VALUES (2)`,
		"an unknown throttle": `INSERT INTO queue_settings (id, throttle) VALUES (1, 'fast')`,
		"a paused of 2":       `INSERT INTO queue_settings (id, paused) VALUES (1, 2)`,
		"a start alone":       `INSERT INTO queue_settings (id, schedule_start) VALUES (1, 60)`,
		"an end alone":        `INSERT INTO queue_settings (id, schedule_end) VALUES (1, 60)`,
		"equal ends":          `INSERT INTO queue_settings (id, schedule_start, schedule_end) VALUES (1, 60, 60)`,
		"an end past the day": `INSERT INTO queue_settings (id, schedule_start, schedule_end) VALUES (1, 60, 1440)`,
	} {
		_, err := db.Exec(insert)
		assert.ErrorContains(t, err, "constraint failed", name)
	}
	_, err = db.Exec(`INSERT INTO queue_settings (id, schedule_start, schedule_end) VALUES (1, 1320, 420)`)
	require.NoError(t, err)
	var paused int
	var throttle, updatedAt string
	require.NoError(t, db.QueryRow(`SELECT paused, throttle, updated_at FROM queue_settings`).Scan(&paused, &throttle, &updatedAt))
	assert.Zero(t, paused)
	assert.Equal(t, "normal", throttle)
	_, err = parseTime(updatedAt)
	require.NoError(t, err)

	_, err = p.DownTo(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, schemaBefore, schemaDump(t, db))
}

// TestMigration012_DropsSchemaMeta: 012 drops the table that claimed
// nomic-embed-text at 768 for every home. Its Down restores the table as
// 005 left it, with 001's row, so the Downs before it still run.
func TestMigration012_DropsSchemaMeta(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 11)
	schemaBefore := schemaDump(t, db)

	_, err := p.UpTo(ctx, 12)
	require.NoError(t, err)
	var tables int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'schema_meta'`).Scan(&tables))
	assert.Zero(t, tables)

	_, err = p.DownTo(ctx, 11)
	require.NoError(t, err)
	assert.Equal(t, schemaBefore, schemaDump(t, db))
	var model string
	var dim int
	require.NoError(t, db.QueryRow(`SELECT embedding_model, embedding_dim FROM schema_meta WHERE id = 1`).Scan(&model, &dim))
	assert.Equal(t, "nomic-embed-text", model)
	assert.Equal(t, 768, dim)

	_, err = p.DownTo(ctx, 0)
	require.NoError(t, err, "every older Down runs over the restored table")
}

// TestMigration013_KeepAwake: 013 adds keep_awake, off for a row stored
// before it, and refuses any value but 0 and 1; its Down leaves
// queue_settings exactly as 011 created it.
func TestMigration013_KeepAwake(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 12)
	schemaBefore := schemaDump(t, db)
	_, err := db.Exec(`INSERT INTO queue_settings (id, paused, throttle, schedule_start, schedule_end)
		VALUES (1, 1, 'gentle', 1320, 420)`)
	require.NoError(t, err)

	_, err = p.UpTo(ctx, 13)
	require.NoError(t, err)
	got, err := NewQueueSettings(db).Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, store.QueueSettings{Paused: true, Throttle: store.ThrottleGentle,
		Schedule: store.DailyWindow{Start: 1320, End: 420}}, got, "the stored settings, keep-awake off")
	_, err = db.Exec(`UPDATE queue_settings SET keep_awake = 2`)
	assert.ErrorContains(t, err, "constraint failed")
	_, err = db.Exec(`UPDATE queue_settings SET keep_awake = 1`)
	require.NoError(t, err)

	_, err = p.DownTo(ctx, 12)
	require.NoError(t, err)
	assert.Equal(t, schemaBefore, schemaDump(t, db), "011's table, byte for byte")
}

// abuseBlockError is the reason Jina gives for a domain it blocks.
const abuseBlockError = "AbuseAlleviationError: Anonymous access to domain twitter.com blocked until " +
	"Mon Sep 28 2026 10:00:00 GMT+0000 (Coordinated Universal Time) due to previous abuse found on " +
	"https://twitter.com/someone: DDoS attack suspected: Too many requests"

// TestMigration014_FailureCause: 014 gives each failed or dead document the
// cause the error of its most recent failed job reads as, and every other
// document none. Nothing else in the rows changes, the invariant holds,
// the cause index is there, Down restores 013's schema exactly, and Up runs
// again.
func TestMigration014_FailureCause(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 13)
	const (
		thin      = "native: login wall or thin content (extracted text < 500 bytes)"
		origin403 = "native: HTTP 403 Forbidden: origin blocked the request (likely anti-bot)"
	)
	type failedJob struct{ kind, lastError string }
	fetch := func(lastError string) []failedJob { return []failedJob{{"fetch", lastError}} }
	cases := []struct {
		name  string
		state store.DocState
		jobs  []failedJob // failed jobs, oldest first
		want  store.FailureCause
	}{
		{"an anti-bot cache hit, cached from a Jina rejection", store.DocStateFailed, fetch(
			"permanent failure: native: origin blocked the request (likely anti-bot) (cached: jina: answer is not " +
				"the page: " + thin[len("native: "):] + " (after " + origin403 + "))"), store.FailureCauseAntiBot},
		{"an anti-bot cache hit, cached from a Jina refusal", store.DocStateFailed, fetch(
			"permanent failure: native: origin blocked the request (likely anti-bot) (cached: jina: refused the " +
				"target: HTTP 403 Forbidden: " + abuseBlockError + " (after " + origin403 + "))"), store.FailureCauseAntiBot},
		{"an unreachable cache hit", store.DocStateFailed, fetch(
			"permanent failure: native: host unreachable (cached: native: fetch: host unreachable: " +
				`Get "https://gone.example/a": dial tcp: lookup gone.example: no such host)`), store.FailureCauseUnreachable},
		{"a login-wall cache hit", store.DocStateFailed, fetch(
			"permanent failure: native: login wall or thin content (cached: native: site-wide login wall or thin " +
				"content (redirected to a login/auth path: /login))"), store.FailureCauseLoginWall},

		{"Jina's domain block", store.DocStateFailed, fetch(
			"permanent failure: jina: refused the target: HTTP 403 Forbidden: " + abuseBlockError + " (after " + thin + ")"),
			store.FailureCauseJinaRefused},
		{"Jina's 422 about a timeout", store.DocStateFailed, fetch(
			"permanent failure: jina: refused the target: HTTP 422 Unprocessable Entity: TimeoutError: page.goto: " +
				"Timeout 30000ms exceeded (after " + thin + ")"), store.FailureCauseJinaRefused},
		{"a thin Jina answer", store.DocStateFailed, fetch(
			"permanent failure: jina: answer is not the page: login wall or thin content (extracted text < 500 bytes) " +
				"(after " + thin + ")"), store.FailureCauseLoginWall},
		{"Jina's CAPTCHA warning", store.DocStateFailed, fetch(
			"permanent failure: jina: answer is not the page: origin blocked the request (likely anti-bot) " +
				"(bot challenge: jina reports a CAPTCHA) (after " + thin + ")"), store.FailureCauseAntiBot},
		{"target 403 through Jina", store.DocStateFailed, fetch(
			"fetch failed: jina: answer is not the page: target answered HTTP 403 Forbidden: origin blocked the " +
				"request (likely anti-bot) (after " + origin403 + ")"), store.FailureCauseAntiBot},
		{"target 401 through Jina", store.DocStateFailed, fetch(
			"fetch failed: jina: answer is not the page: target answered HTTP 401 Unauthorized (after " + origin403 + ")"),
			store.FailureCauseHTTPError},
		{"target 500 through Jina", store.DocStateFailed, fetch(
			"fetch failed: jina: target failed for now: target answered HTTP 500 Internal Server Error (after " +
				origin403 + ")"), store.FailureCauseHTTPError},
		{"target 429 through Jina", store.DocStateFailed, fetch(
			"fetch failed: jina: target failed for now: target answered HTTP 429 Too Many Requests (after " + thin + ")"),
			store.FailureCauseRateLimited},
		{"Jina's own 403", store.DocStateFailed, fetch(
			"fetch failed: jina: HTTP 403 Forbidden: OperationNotAllowedError: This operation is not allowed (after " +
				origin403 + ")"), store.FailureCauseAntiBot},

		{"another site's login page", store.DocStateFailed, fetch(
			"permanent failure: native: offsite login wall or thin content (redirected onto another site's login " +
				"page: accounts.example.org/signin)"), store.FailureCauseLoginWall},
		{"an expired certificate", store.DocStateFailed, fetch(
			`permanent failure: native: fetch: invalid TLS certificate: Get "https://expired.example/": tls: failed ` +
				"to verify certificate: x509: certificate has expired or is not yet valid"), store.FailureCauseTLS},
		{"origin 401", store.DocStateFailed, fetch("permanent failure: native: HTTP 401 Unauthorized"),
			store.FailureCauseHTTPError},
		{"origin 999", store.DocStateFailed, fetch("permanent failure: native: HTTP 999"), store.FailureCauseHTTPError},
		{"origin 500", store.DocStateFailed, fetch("fetch failed: native: HTTP 500 Internal Server Error"),
			store.FailureCauseHTTPError},
		{"origin 429", store.DocStateFailed, fetch("fetch failed: native: HTTP 429 Too Many Requests"),
			store.FailureCauseRateLimited},
		{"YouTube's cooldown", store.DocStateFailed, fetch(
			"fetch failed: youtube: not run, rate-limit cooldown has 2m0s left: HTTP 429 Too Many Requests"),
			store.FailureCauseRateLimited},
		{"GitHub's cooldown", store.DocStateFailed, fetch(
			"fetch failed: github: https://api.github.com/repos/owner/repo: not sent, rate-limit cooldown has " +
				"24m30s left: rate limited: HTTP 429 Too Many Requests"), store.FailureCauseRateLimited},
		{"a channel page", store.DocStateFailed, fetch(
			"permanent failure: youtube: cannot extract video ID from https://www.youtube.com/@channel"),
			store.FailureCauseUnsupported},
		{"a GitHub profile", store.DocStateFailed, fetch(
			"permanent failure: github: not a recognized GitHub URL: https://github.com/someone"),
			store.FailureCauseUnsupported},
		{"an image", store.DocStateFailed, fetch(
			`permanent failure: native: unsupported content type "image/png" (not HTML); URL: https://example.com/a.png`),
			store.FailureCauseUnsupported},
		{"no fetcher", store.DocStateFailed, fetch(
			"permanent failure: no fetcher for https://example.com/x: fetcher: no fetcher matches url"),
			store.FailureCauseUnsupported},
		{"a client timeout", store.DocStateFailed, fetch(
			`fetch failed: native: fetch: Get "https://slow.example/": context deadline exceeded ` +
				"(Client.Timeout exceeded while awaiting headers)"), store.FailureCauseTimeout},
		{"a DNS timeout", store.DocStateFailed, fetch(
			`fetch failed: native: fetch: Get "https://slow.example/": dial tcp: lookup slow.example: i/o timeout`),
			store.FailureCauseTimeout},
		{"our network unreachable", store.DocStateFailed, fetch(
			`fetch failed: native: fetch: Get "https://example.com/": dial tcp 192.0.2.1:443: connect: network is unreachable`),
			store.FailureCauseNetwork},
		{"a TLS alert", store.DocStateFailed, fetch(
			`fetch failed: native: fetch: Get "https://example.com/": remote error: tls: handshake failure`),
			store.FailureCauseNetwork},
		{"a redirect loop", store.DocStateFailed, fetch(
			`fetch failed: native: fetch: Get "/a": stopped after 10 redirects`), store.FailureCauseNetwork},
		{"GitHub's 404", store.DocStateFailed, fetch(
			"permanent failure: github: https://api.github.com/repos/owner/gone: HTTP 404 Not Found"),
			store.FailureCauseHTTPError},
		{"a body over the cap", store.DocStateFailed, fetch(
			"permanent failure: native: https://example.com/huge: response too large (limit 33554432 bytes)"),
			store.FailureCauseTooLarge},
		{"a private video", store.DocStateFailed, fetch(
			"permanent failure: youtube: ERROR: [youtube] abc: Private video. Sign in if you've been granted access " +
				"to this video"), store.FailureCauseOther},
		{"an orphan out of attempts", store.DocStateFailed, fetch(orphanExhaustedError), store.FailureCauseOther},
		{"a handler panic", store.DocStateFailed, fetch(
			"panic: runtime error: invalid memory address or nil pointer dereference (permanent failure)"),
			store.FailureCauseOther},

		{"an index failure after a fetch failure", store.DocStateFailed,
			[]failedJob{{"fetch", "fetch failed: native: HTTP 500 Internal Server Error"},
				{"index", "index: embed: ollama unreachable"}}, store.FailureCauseIndex},
		{"a fetch failure after an index failure", store.DocStateFailed,
			[]failedJob{{"index", "index: embed: ollama unreachable"},
				{"fetch", "fetch failed: native: HTTP 429 Too Many Requests"}}, store.FailureCauseRateLimited},
		{"a failure without an error", store.DocStateFailed, fetch(""), store.FailureCauseOther},
		{"no failed job", store.DocStateFailed, nil, store.FailureCauseOther},
		{"dead, with no failed job", store.DocStateDead, nil, store.FailureCauseDeadLink},
		{"dead, whatever its error says", store.DocStateDead, fetch("fetch failed: native: HTTP 500 Internal Server Error"),
			store.FailureCauseDeadLink},
		{"fetched after a failed job", store.DocStateFetched, fetch("fetch failed: native: HTTP 500"), ""},
		{"pending after a failed job", store.DocStatePending, fetch(origin403), ""},
	}
	for i, tc := range cases {
		id := fmt.Sprintf("d%02d", i)
		_, err := db.Exec(`INSERT INTO documents (id, tenant_id, url, state, updated_at)
			VALUES (?, 'local', ?, ?, '2024-01-01T00:00:00.000Z')`, id, "https://example.com/"+id, tc.state)
		require.NoError(t, err)
		for j, job := range tc.jobs {
			_, err := db.Exec(`INSERT INTO jobs (id, tenant_id, kind, payload, status, attempts, last_error,
				updated_at, document_id) VALUES (?, 'local', ?, json_object('document_id', ?), 'failed', 5, ?, ?, ?)`,
				fmt.Sprintf("%s-j%d", id, j), job.kind, id, store.NullableString(job.lastError),
				fmt.Sprintf("2024-01-01T00:0%d:00.000Z", j+1), id)
			require.NoError(t, err)
		}
	}
	var columns string
	require.NoError(t, db.QueryRow(`SELECT group_concat(name, ', ') FROM pragma_table_info('documents')`).Scan(&columns))
	docsQuery := `SELECT ` + columns + ` FROM documents ORDER BY id`
	docsBefore := dumpRows(t, db, docsQuery)
	jobsBefore := dumpRows(t, db, `SELECT * FROM jobs ORDER BY id`)
	schemaBefore := schemaDump(t, db)

	causes := func() map[string]store.FailureCause {
		t.Helper()
		got := map[string]store.FailureCause{}
		for _, row := range dumpRows(t, db, `SELECT id, COALESCE(failure_cause, '') FROM documents`) {
			got[row[0].(string)] = store.FailureCause(row[1].(string))
		}
		return got
	}
	want := map[string]store.FailureCause{}
	for i, tc := range cases {
		want[fmt.Sprintf("d%02d", i)] = tc.want
	}

	_, err := p.UpTo(ctx, 14)
	require.NoError(t, err)
	got := causes()
	for i, tc := range cases {
		id := fmt.Sprintf("d%02d", i)
		assert.Equal(t, tc.want, got[id], "%s (%s)", tc.name, id)
	}
	assert.Equal(t, docsBefore, dumpRows(t, db, docsQuery), "updated_at and every other column untouched")
	assert.Equal(t, jobsBefore, dumpRows(t, db, `SELECT * FROM jobs ORDER BY id`))
	assertCauseInvariant(t, db, "after the backfill")
	var indexed string
	require.NoError(t, db.QueryRow(`SELECT group_concat(name, ', ') FROM
		(SELECT name FROM pragma_index_info('idx_documents_tenant_cause_updated') ORDER BY seqno)`).Scan(&indexed))
	assert.Equal(t, "tenant_id, failure_cause, updated_at, id", indexed)
	var partial bool
	require.NoError(t, db.QueryRow(`SELECT partial FROM pragma_index_list('documents')
		WHERE name = 'idx_documents_tenant_cause_updated'`).Scan(&partial))
	assert.True(t, partial)

	_, err = p.DownTo(ctx, 13)
	require.NoError(t, err)
	assert.Equal(t, schemaBefore, schemaDump(t, db), "013's schema, byte for byte")
	assert.Equal(t, docsBefore, dumpRows(t, db, docsQuery))

	_, err = p.UpTo(ctx, 14)
	require.NoError(t, err)
	assert.Equal(t, want, causes(), "Up again gives the same causes")
}

// TestMigration015_BookmarksSavedOrder: 015 adds the saved-order index and
// rebuilds idx_bookmarks_document ending in (saved_at, id), keeps every
// row, its Down restores 014's schema exactly, and Up runs again.
func TestMigration015_BookmarksSavedOrder(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 14)
	_, err := db.Exec(`
		INSERT INTO documents (id, tenant_id, url) VALUES ('d1', 'local', 'https://example.com/1');
		INSERT INTO bookmarks (id, tenant_id, document_id, url, title, saved_at, source, folder_path, created_at)
			VALUES ('b1', 'local', 'd1', 'https://example.com/1', 'One', '2024-01-01T00:00:00.000Z', 'chrome', '/Tech',
			        '2026-09-01T00:00:00.000Z'),
			       ('b2', 'local', 'd1', 'https://example.com/1', NULL, '2025-01-01T00:00:00.000Z', 'safari', NULL,
			        '2026-09-02T00:00:00.000Z'),
			       ('b3', 'local', NULL, 'https://example.com/3', 'Three', '2023-01-01T00:00:00.000Z', 'manual', NULL,
			        '2026-09-03T00:00:00.000Z');`)
	require.NoError(t, err)
	schemaBefore := schemaDump(t, db)
	rowsBefore := dumpRows(t, db, `SELECT * FROM bookmarks ORDER BY id`)
	indexes := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, row := range dumpRows(t, db, `SELECT name, sql FROM sqlite_master
			WHERE type = 'index' AND tbl_name = 'bookmarks' AND sql IS NOT NULL`) {
			out[row[0].(string)] = row[1].(string)
		}
		return out
	}

	_, err = p.UpTo(ctx, 15)
	require.NoError(t, err)
	got := indexes()
	assert.Equal(t, "CREATE INDEX idx_bookmarks_tenant_saved ON bookmarks(tenant_id, saved_at, id)",
		got["idx_bookmarks_tenant_saved"])
	assert.Equal(t, "CREATE INDEX idx_bookmarks_document ON bookmarks(document_id, saved_at, id)",
		got["idx_bookmarks_document"])
	assert.Equal(t, "CREATE INDEX idx_bookmarks_tenant_created ON bookmarks(tenant_id, created_at, id)",
		got["idx_bookmarks_tenant_created"], "the created order keeps its index")
	assert.Equal(t, rowsBefore, dumpRows(t, db, `SELECT * FROM bookmarks ORDER BY id`))

	_, err = p.DownTo(ctx, 14)
	require.NoError(t, err)
	assert.Equal(t, schemaBefore, schemaDump(t, db), "014's schema, byte for byte")
	assert.Equal(t, rowsBefore, dumpRows(t, db, `SELECT * FROM bookmarks ORDER BY id`))

	_, err = p.UpTo(ctx, 15)
	require.NoError(t, err)
	assert.Contains(t, indexes(), "idx_bookmarks_tenant_saved", "Up runs again")
}

// bm25BeforeMigration008 is BM25Search's query before migration 008, over
// the regular six-column chunks_fts.
const bm25BeforeMigration008 = `
	SELECT fts.chunk_id, fts.document_id, bm25(chunks_fts) AS bm25_score,
	       snippet(chunks_fts, 0, '<em>', '</em>', '…', 32)
	FROM chunks_fts fts
	JOIN documents d ON d.id = fts.document_id
	WHERE chunks_fts MATCH ?
	  AND d.tenant_id = ?
	ORDER BY bm25_score
	LIMIT ?`

func bm25Before008(t *testing.T, db *DB, query string) []store.ChunkHit {
	t.Helper()
	rows, err := db.Query(bm25BeforeMigration008, query, "local", 50)
	require.NoError(t, err)
	defer rows.Close()
	var hits []store.ChunkHit
	for rows.Next() {
		var h store.ChunkHit
		var bm25 float64
		require.NoError(t, rows.Scan(&h.ChunkID, &h.DocumentID, &bm25, &h.Snippet))
		h.Score = -bm25
		hits = append(hits, h)
	}
	require.NoError(t, rows.Err())
	return hits
}

// assertSameHits compares two BM25 result lists: the same chunks and
// documents in the same order with the same snippets, and scores equal to
// within floating-point noise.
func assertSameHits(t *testing.T, want, got []store.ChunkHit, query string) {
	t.Helper()
	require.Len(t, got, len(want), query)
	for i := range want {
		assert.Equal(t, want[i].ChunkID, got[i].ChunkID, "%s: hit %d", query, i)
		assert.Equal(t, want[i].DocumentID, got[i].DocumentID, "%s: hit %d", query, i)
		assert.Equal(t, want[i].Snippet, got[i].Snippet, "%s: hit %d", query, i)
		assert.InDelta(t, want[i].Score, got[i].Score, 1e-9, "%s: hit %d", query, i)
	}
}

// TestMigration008_ChunksFTSExternalContent seeds chunks the way the store
// wrote them before migration 008 (chunk row, six-column FTS row, vector)
// and checks that search is unchanged across it, in both directions.
func TestMigration008_ChunksFTSExternalContent(t *testing.T) {
	ctx := context.Background()
	db, p := migratedTo(t, 7)

	type chunk struct{ id, text string }
	docs := []struct {
		id, title, tags string // tags as the old writer indexed them: JSON, or "" for none
		chunks          []chunk
	}{
		{"doc-pg", "Postgres Internals", `["db","postgres"]`, []chunk{
			{"pg-0", "PostgreSQL uses MVCC for concurrency control, keeping old row versions around until vacuum removes them."},
			{"pg-1", "The write ahead log records every change before it reaches the heap, so a crash can replay it."},
			{"pg-2", "B-tree indexes accelerate range scans; vacuum keeps their pages tidy."},
		}},
		{"doc-notes", "", "", []chunk{
			{"notes-0", "Kafka partitions let consumer groups scale reads across many brokers at once."},
			{"notes-1", "A short note on MVCC in other databases."},
		}},
		{"doc-kafka", "Kafka Streams Guide", `["observability"]`, []chunk{
			{"kafka-0", "Stream processing joins and windows over topics, with a write ahead log of its own for local state stores."},
			{"kafka-1", "Consumer lag metrics tell you when a partition falls behind."},
		}},
	}
	for d, doc := range docs {
		_, err := db.Exec(`INSERT INTO documents (id, tenant_id, url, title, state) VALUES (?, 'local', ?, ?, 'fetched')`,
			doc.id, "https://example.com/"+doc.id, doc.title)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO document_extractions (id, document_id, fetcher, status) VALUES (?, ?, 'test', 'ok')`,
			"ext-"+doc.id, doc.id)
		require.NoError(t, err)
		for i, c := range doc.chunks {
			_, err := db.Exec(`INSERT INTO chunks (id, document_id, extraction_id, ord, text, token_count)
				VALUES (?, ?, ?, ?, ?, ?)`, c.id, doc.id, "ext-"+doc.id, i, c.text, len(strings.Fields(c.text)))
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO chunks_fts (text, title, title_search, tags, chunk_id, document_id)
				VALUES (?, ?, ?, ?, ?, ?)`, c.text, doc.title, doc.title, doc.tags, c.id, doc.id)
			require.NoError(t, err)
			vec, err := sqlitevec.SerializeFloat32(fillVecOf(migration001Dim, float32(d*10+i)*0.01))
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO chunks_vec (chunk_id, embedding) VALUES (?, ?)`, c.id, vec)
			require.NoError(t, err)
		}
	}

	queries := []string{
		`mvcc`,              // body term
		`"write ahead log"`, // phrase
		`kafka OR vacuum`,   // OR, across body and title
		`internals`,         // in a title only
		`observability`,     // in tags only
	}
	before := map[string][]store.ChunkHit{}
	for _, q := range queries {
		hits := bm25Before008(t, db, q)
		require.NotEmpty(t, hits, q)
		for i := 1; i < len(hits); i++ {
			require.NotEqual(t, hits[i-1].Score, hits[i].Score, "%s: the fixture must not tie, or the order is arbitrary", q)
		}
		before[q] = hits
	}
	const chunkColumns = `SELECT id, document_id, extraction_id, ord, text, token_count FROM chunks ORDER BY id`
	const vectors = `SELECT chunk_id, embedding FROM chunks_vec ORDER BY chunk_id`
	chunksBefore := dumpRows(t, db, chunkColumns)
	indexedBefore := dumpRows(t, db, `SELECT chunk_id, title_search, tags FROM chunks_fts ORDER BY chunk_id`)
	vectorsBefore := dumpRows(t, db, vectors)
	schemaBefore := schemaDump(t, db)

	_, err := p.UpTo(ctx, 8)
	require.NoError(t, err)

	ch := NewChunks(db, migration001Dim)
	for _, q := range queries {
		got, err := ch.BM25Search(ctx, "local", q, 50, store.SearchFilters{})
		require.NoError(t, err)
		assertSameHits(t, before[q], got, q)
	}
	_, err = db.Exec(`INSERT INTO chunks_fts (chunks_fts, rank) VALUES ('integrity-check', 1)`)
	require.NoError(t, err)
	assert.Equal(t, chunksBefore, dumpRows(t, db, chunkColumns))
	assert.Equal(t, indexedBefore, dumpRows(t, db, `SELECT id, title, tags FROM chunks ORDER BY id`),
		"each chunk keeps the title and tags it was indexed with")
	assert.Equal(t, vectorsBefore, dumpRows(t, db, vectors))
	var contentTables int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'chunks_fts_content'`).Scan(&contentTables))
	assert.Zero(t, contentTables, "the index keeps no copy of the text")

	_, err = p.DownTo(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, schemaBefore, schemaDump(t, db))
	for _, q := range queries {
		assertSameHits(t, before[q], bm25Before008(t, db, q), q)
	}
	assert.Equal(t, chunksBefore, dumpRows(t, db, chunkColumns))
	assert.Equal(t, indexedBefore, dumpRows(t, db, `SELECT chunk_id, title_search, tags FROM chunks_fts ORDER BY chunk_id`))
	assert.Equal(t, vectorsBefore, dumpRows(t, db, vectors))
}

// TestMigrate_TruncatesWAL: a migration that rewrites a table leaves a WAL
// about the table's size, which Migrate truncates. The control migrates an
// identical database with goose alone.
func TestMigrate_TruncatesWAL(t *testing.T) {
	ctx := context.Background()
	seed := func(db *DB) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO documents (id, tenant_id, url) VALUES ('d', 'local', 'https://example.com/');
			INSERT INTO document_extractions (id, document_id, fetcher, status) VALUES ('e', 'd', 'test', 'ok')`)
		require.NoError(t, err)
		text := strings.Repeat("write ahead log pages add up quickly ", 40)
		for i := range 500 {
			id := fmt.Sprint("c", i)
			_, err := db.Exec(`INSERT INTO chunks (id, document_id, extraction_id, ord, text) VALUES (?, 'd', 'e', ?, ?)`,
				id, i, text)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO chunks_fts (text, title, title_search, tags, chunk_id, document_id)
				VALUES (?, 'T', 'T', '', ?, 'd')`, text, id)
			require.NoError(t, err)
		}
		_, err = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		require.NoError(t, err)
	}
	walSize := func(path string) int64 {
		t.Helper()
		info, err := os.Stat(path + "-wal")
		require.NoError(t, err)
		return info.Size()
	}

	control, controlPath := openUnmigrated(t)
	p := newProvider(t, control, migrations.FS)
	_, err := p.UpTo(ctx, 7)
	require.NoError(t, err)
	seed(control)
	_, err = p.Up(ctx)
	require.NoError(t, err)
	require.Greater(t, walSize(controlPath), int64(1<<20), "the rewrite leaves a large WAL")

	db, path := openUnmigrated(t)
	_, err = newProvider(t, db, migrations.FS).UpTo(ctx, 7)
	require.NoError(t, err)
	seed(db)
	_, err = Migrate(ctx, db)
	require.NoError(t, err)
	assert.Zero(t, walSize(path))
}
