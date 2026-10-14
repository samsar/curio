# Migrations

SQLite schema migrations, managed by [`pressly/goose`](https://github.com/pressly/goose).

## Naming

`NNN_short_name.sql`, zero-padded to 3 digits. Numbers are sequential, no gaps.

## Running

The daemon runs pending migrations at startup, through the driver that
loads sqlite-vec; it is the supported migrator. The goose CLI has no
sqlite-vec, so it can show where a database stands:

```sh
goose -dir migrations sqlite3 ~/.curio/curio.db status   # make migrate-status
```

but it can't create a new database (001 creates `chunks_vec`, a vec0
table), apply 019 to a schema-18 database, or run 019's Down: 019 renames
`jobs` out of the way, which makes SQLite parse every trigger again, and
`chunks_vec`'s triggers name a vec0 table (019's Down also drops
`document_cards_vec`, which needs the module). Each fails with `no such
module: vec0` and rolls back, leaving the version where it was (checked
with goose v3.27.0). It can apply 021 to a schema-20 database, but not
run 021's Down: its `DROP COLUMN`s make SQLite parse `chunks`' triggers
again, and `trg_chunks_delete` names `chunks_vec` (checked with goose
v3.27.0: `no such module: vec0`, rolled back at version 21). `make
migrate-up` runs the CLI's `up`, so it works only for migrations that need
no sqlite-vec.

## Required pragmas

Every connection the daemon opens must set:

```sql
PRAGMA foreign_keys = ON;     -- SQLite default is OFF (!)
PRAGMA journal_mode = WAL;    -- allows concurrent reads during writes
PRAGMA busy_timeout = 5000;   -- wait 5s for write locks before erroring
PRAGMA synchronous = NORMAL;  -- safe with WAL, faster than FULL
```

`foreign_keys` is *per-connection* and SQLite defaults it OFF. Forgetting this
silently disables FK enforcement — every connection must turn it on.

## Schema conventions

- **IDs**: UUID v4 as 36-char TEXT.
- **Timestamps**: RFC 3339 UTC as TEXT (`YYYY-MM-DDTHH:MM:SS.fffZ`).
  Lexicographic sort = chronological sort.
- **JSON columns**: TEXT validated with `CHECK (col IS NULL OR json_valid(col))`.
- **Enums**: TEXT with `CHECK (col IN (...))`. The one exception is
  `documents.failure_cause` (014), validated in Go (`store.FailureCause`):
  causes will grow, and changing a CHECK on `documents` means rebuilding
  the table most others reference.
- **Booleans**: INTEGER (0 / 1).
- **Soft delete**: not used. Cascading FKs handle cleanup. If we ever need
  retention/restore, add a dedicated trash table per entity.

## Schema versioning

goose's `goose_db_version` table is the only record of the schema version:
the version is the highest migration applied. `sqlite.Migrate` returns it,
and the daemon copies it into `~/.curio/.curio-meta.json` after migrating,
as a cache for `/v1/healthz`, `curio version` and `curio doctor`. A
migration never records the version itself.

The embedding model and the vector width are the home's, recorded in its
marker, `.curio-meta.json`, when the home is created; no table records
them. 012 dropped `schema_meta`, which held the model and width 001
hard-coded (`nomic-embed-text`, 768) and which nothing read. The daemon
sizes `chunks_vec`, and creates `document_cards_vec`, from the marker
after migrating (`sqlite.EnsureVectorIndex`), so the FLOAT[768] in 001
only ever describes an empty table.

## Adding a migration

1. Create `migrations/NNN_description.sql` with `-- +goose Up` and `-- +goose Down`
   blocks.
2. Test it in `internal/store/sqlite/migrations_test.go`, through the
   daemon's driver: `migratedTo` brings a database to the version before
   it, the test seeds rows and records `schemaDump`, applies the new
   version, checks what it did, then `DownTo` the version before and
   compares `schemaDump` byte for byte, and applies it again. The goose
   CLI can't stand in for this once a migration touches a database with
   vec0 tables (see Running).
3. Never edit an applied migration. Add a new one that fixes the old one.
   The one exception is a fix that leaves the resulting schema exactly as it
   was, proven by a test comparing `sqlite_master` before and after: 002 was
   rewritten into the rebuild recipe below that way
   (`internal/store/sqlite/migrations_test.go`).

## Schema changes that require care

- **Adding a column**: trivial; `ALTER TABLE ADD COLUMN`.
- **Changing a constraint or a column type, or dropping a column**: SQLite
  can't do this in place, so the table is rebuilt. When another table's
  foreign key references the table, use the recipe below. When none does
  (check `pragma_foreign_key_list`), rebuild it inside goose's transaction
  like an ordinary migration, as 008 does for `chunks`: dropping a table
  nothing references runs no ON DELETE actions, and the version bump then
  commits with the rebuild.
- **Changing embedding dimensions**: not a migration. The width is each
  home's, fixed when it is created: `sqlite.EnsureVectorIndex` creates
  `chunks_vec` at the marker's width and refuses to rebuild one that holds
  vectors, and another width means a new home. See
  [`../docs/decisions.md#embedding-model-and-per-home-width`](../docs/decisions.md#embedding-model-and-per-home-width).
- **Reshaping `chunks_vec` or `document_cards_vec`** (a column, a
  partition key, a distance metric): not a SQL migration, which can't know
  the home's width. Their definitions live in
  `internal/store/sqlite/vectors.go` (`vectorIndexes`, each table's CREATE
  framed around the width), and `sqlite.EnsureVectorIndex` creates them
  from there after the migrations run: it recreates the empty `chunks_vec`
  001 left at FLOAT[768], and creates `document_cards_vec` with the
  trigger that deletes a card's vector with its card
  (`trg_document_cards_delete`), which no migration creates, since a
  trigger naming a table that doesn't exist yet fails when it fires. A
  reshape made only in a migration would be undone on every new home.
  Change it there, and bring existing tables along in that Go step.
- **Changing the FTS5 tokenizer**: recreate `chunks_fts` with the new
  tokenizer and repopulate it with FTS5's rebuild command,
  `INSERT INTO chunks_fts (chunks_fts) VALUES ('rebuild')`. It is an
  external-content index over `chunks`, so that re-tokenizes `chunks.text`,
  `title` and `tags`; no embedder round-trips.
- **Writing `chunks`**: never `INSERT OR REPLACE`. The triggers that keep
  `chunks_fts` and `chunks_vec` in step don't fire for rows REPLACE
  deletes.

## Rebuilding a table other tables reference

```sql
-- +goose NO TRANSACTION

-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

CREATE TABLE things_new (...);
INSERT INTO things_new SELECT ... FROM things;
DROP TABLE things;
ALTER TABLE things_new RENAME TO things;
-- recreate things' own indexes and triggers here

CREATE TEMP TABLE _fk_guard (violations INTEGER NOT NULL CHECK (violations = 0));
INSERT INTO _fk_guard SELECT count(*) FROM pragma_foreign_key_check;
DROP TABLE temp._fk_guard;

COMMIT;
PRAGMA foreign_keys = ON;
-- +goose StatementEnd
```

Every part of it is there for a reason:

- **The migration opts out of goose's transaction.** `PRAGMA foreign_keys`
  does nothing inside a transaction. With foreign keys still on,
  `DROP TABLE` on a table other tables reference runs their `ON DELETE`
  actions first. Rebuilding `documents` that way would delete every
  extraction, chunk and cluster membership, and null every
  `bookmarks.document_id` and, since 007, every `jobs.document_id`. So the
  migration turns them off itself, then opens its own transaction.
- **The guard must enforce.** Only an error can abort a goose SQL
  migration. A bare `PRAGMA foreign_key_check;` returns rows that goose
  discards, so it checks nothing. Counting `pragma_foreign_key_check` into
  a `CHECK`-constrained table fails the statement, and so the migration,
  before `COMMIT` when a reference dangles. The check covers the whole
  database, so a dangling reference that predates the migration aborts it
  too; repair the data first rather than narrowing the guard.
- **It is one `StatementBegin` block.** In NO TRANSACTION mode the goose
  CLI (`make migrate-up`) runs each statement on whichever pooled
  connection it gets; only the daemon's `goose.Provider` pins one. The
  pragma, the `BEGIN`, the rebuild and the `COMMIT` must all land on the
  same connection either way.
- **Recreate the table's own indexes and triggers.** `DROP TABLE` takes
  them with it. Also re-check any view or trigger on *another* table that
  references this one.
- **It must be safe to run twice.** Without goose's transaction the
  version is recorded after `COMMIT`. If the process dies between the two,
  the block runs again on the already-rebuilt table.
- **A failure poisons the connection.** When a statement inside the block
  fails, the rest of the block never runs, so its connection is left in an
  open transaction with foreign keys off. Close the database handle after
  a failed `Migrate`; the daemon exits on it.
