# Migrations

SQLite schema migrations, managed by [`pressly/goose`](https://github.com/pressly/goose).

## Naming

`NNN_short_name.sql`, zero-padded to 3 digits. Numbers are sequential, no gaps.

## Running

The daemon runs pending migrations at startup. Manual control via:

```sh
goose -dir migrations sqlite3 ~/.curio/curio.db up
goose -dir migrations sqlite3 ~/.curio/curio.db status
```

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
- **Enums**: TEXT with `CHECK (col IN (...))`.
- **Booleans**: INTEGER (0 / 1).
- **Soft delete**: not used. Cascading FKs handle cleanup. If we ever need
  retention/restore, add a dedicated trash table per entity.

## Schema versioning

`schema_meta` table holds the current schema version, embedding model, and
embedding dimension. The daemon cross-checks this against `~/.curio/.curio-meta.json`
at startup and refuses to start on mismatch (suggests `curio reindex`).

## Adding a migration

1. Create `migrations/NNN_description.sql` with `-- +goose Up` and `-- +goose Down`
   blocks.
2. Test against a fresh DB: `goose up` → apply, `goose down` → revert cleanly.
3. Never edit an applied migration. Add a new one that fixes the old one.
   The one exception is a fix that leaves the resulting schema exactly as it
   was, proven by a test comparing `sqlite_master` before and after: 002 was
   rewritten into the rebuild recipe below that way
   (`internal/store/sqlite/migrations_test.go`).

## Schema changes that require care

- **Adding a column**: trivial; `ALTER TABLE ADD COLUMN`.
- **Changing a constraint or a column type, or dropping a column**: SQLite
  can't do this in place, so the table is rebuilt. Use the recipe below,
  and be careful about FTS5/vec table rebuilds.
- **Changing embedding dimensions**: DROP and CREATE the `chunks_vec` table;
  enqueue index jobs for every chunk. See
  [`../docs/decisions.md#embedding-model-swap`](../docs/decisions.md#embedding-model-swap).
- **Changing the FTS5 tokenizer**: requires rebuilding `chunks_fts`. Cheap —
  no embedder round-trips, just re-tokenization from `chunks.text`.

## Rebuilding a table

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

UPDATE schema_meta SET schema_version = N WHERE id = 1;
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
  `bookmarks.document_id`. So the migration turns them off itself, then
  opens its own transaction.
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
