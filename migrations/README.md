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

## Schema changes that require care

- **Adding a column**: trivial; `ALTER TABLE ADD COLUMN`.
- **Changing a column type or dropping a column**: SQLite can't do this
  in-place. Use the 12-step recreate dance from the SQLite docs. Goose
  will hold the transaction; just be careful about FTS5/vec table rebuilds.
- **Changing embedding dimensions**: DROP and CREATE the `chunks_vec` table;
  enqueue index jobs for every chunk. See
  [`../docs/decisions.md#embedding-model-swap`](../docs/decisions.md#embedding-model-swap).
- **Changing the FTS5 tokenizer**: requires rebuilding `chunks_fts`. Cheap —
  no embedder round-trips, just re-tokenization from `chunks.text`.
