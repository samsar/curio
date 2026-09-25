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
			vec, err := sqlitevec.SerializeFloat32(fillVec(float32(d*10+i) * 0.01))
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

	ch := NewChunks(db, vecDim)
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
