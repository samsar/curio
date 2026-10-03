-- +goose Up
-- +goose StatementBegin

-- What automatic rebuilds of the interests need (docs/decisions.md,
-- "Interests: two levels, stable identities, automatic rebuilds").
--
-- documents.indexed_at is when a document's vectors were last written: the
-- index handler's MarkFetched sets it, in the statement that marks the
-- document fetched, and nothing else does. The change count since a run R
-- reads it (a fetched document indexed at or after R read its vectors is
-- added, or reindexed when R assigned it), and so does the settle rule
-- (nothing indexed for a while). A document R couldn't group, one indexed
-- without a chunk or with a vector R dropped as non-finite, was indexed
-- before R's read, so it isn't counted as added at every check after.
--
-- The backfill is exact: MarkFetched is the only write that leaves a
-- document fetched, and every write sets updated_at in its statement, so a
-- fetched document's updated_at is when it was marked fetched. Every other
-- document's stays NULL until it is fetched, and a NULL is never counted.
--
-- idx_documents_tenant_indexed serves both reads: the documents indexed
-- since a time, a range, and the latest time, a seek to the range's end.
--
-- insight_state is rebuilt in place, nothing references it (README.md,
-- "Changing a constraint"), for what a rebuild can't derive from runs:
--   * fresh_owed narrows to the reasons that have a writer: 'reindex'
--     (`curio reindex --all` re-embeds the library) and 'manual'
--     (`curio interests rebuild --fresh`). A change of the grouper's params
--     is read from the done run's params, which every run records, and a
--     change of shape is decided inside every rebuild, so neither is
--     stored. 016 wrote no row, so no 'params' or 'shape' can be dropped.
--   * fresh_owed_at, set with fresh_owed and NULL exactly when it is: a
--     fresh run consumes what was owed only when it read its vectors after
--     this time, so a rebuild owed again while one ran isn't lost.
--   * last_error, with failures and last_failure_at: the scheduler retries
--     a failed rebuild after a backoff from the last failure, and reports
--     the failure; a done rebuild clears all three.
-- The old table steps aside rather than the new one being renamed into
-- place, so insight_state keeps the CREATE TABLE text written here.
--
-- Adding a column and an index rebuilds nothing, and the rebuilt table has
-- no foreign keys either way, so this runs inside goose's transaction.

ALTER TABLE documents ADD COLUMN indexed_at TEXT;

UPDATE documents SET indexed_at = updated_at WHERE state = 'fetched';

CREATE INDEX idx_documents_tenant_indexed ON documents(tenant_id, indexed_at);

ALTER TABLE insight_state RENAME TO insight_state_016;

CREATE TABLE insight_state (
    tenant_id       TEXT    PRIMARY KEY,
    fresh_owed      TEXT    CHECK (fresh_owed IN ('reindex','manual')),
    fresh_owed_at   TEXT,                              -- when it was last owed
    failures        INTEGER NOT NULL DEFAULT 0,        -- failed rebuilds since the last done one
    last_failure_at TEXT,
    last_error      TEXT,
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    CHECK ((fresh_owed IS NULL) = (fresh_owed_at IS NULL))
);

INSERT INTO insight_state (tenant_id, fresh_owed, fresh_owed_at, failures, last_failure_at, updated_at)
    SELECT tenant_id,
           CASE WHEN fresh_owed = 'reindex' THEN 'reindex' END,
           CASE WHEN fresh_owed = 'reindex' THEN updated_at END,
           failures, last_failure_at, updated_at
    FROM insight_state_016;

DROP TABLE insight_state_016;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- 016's insight_state comes back with 016's CREATE text, so the schema is
-- 016's byte for byte; a 'manual' rebuild owed, which it can't hold, and
-- the last error are dropped.

DROP INDEX idx_documents_tenant_indexed;
ALTER TABLE documents DROP COLUMN indexed_at;

ALTER TABLE insight_state RENAME TO insight_state_017;

CREATE TABLE insight_state (
    tenant_id       TEXT    PRIMARY KEY,
    fresh_owed      TEXT    CHECK (fresh_owed IN ('reindex','params','shape')),
    failures        INTEGER NOT NULL DEFAULT 0,
    last_failure_at TEXT,
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

INSERT INTO insight_state (tenant_id, fresh_owed, failures, last_failure_at, updated_at)
    SELECT tenant_id, CASE WHEN fresh_owed = 'reindex' THEN 'reindex' END, failures, last_failure_at, updated_at
    FROM insight_state_017;

DROP TABLE insight_state_017;

-- +goose StatementEnd
