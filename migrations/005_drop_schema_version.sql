-- +goose Up
-- +goose StatementBegin

-- goose_db_version is the one record of the schema version. This column
-- was a hand-kept copy that every migration had to bump, and one that
-- forgot would have left /v1/healthz, `curio version` and `curio doctor`
-- reporting the wrong version. sqlite.Migrate returns goose's version.

ALTER TABLE schema_meta DROP COLUMN schema_version;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- The Downs of 004 through 002 still set this column, so it comes back at
-- the version they start from.
ALTER TABLE schema_meta ADD COLUMN schema_version INTEGER NOT NULL DEFAULT 4;

-- +goose StatementEnd
