-- +goose Up
-- +goose StatementBegin

-- jobs.started_at captures when ClaimNext first transitioned a job to
-- 'running'. Without this, the only way to compute execution time is
-- updated_at - created_at, which conflates queue wait time with actual
-- work time and produces nonsense when many jobs are enqueued at once.
--
-- Nullable: old jobs from before the migration won't have it, and the
-- metrics query COALESCEs them out. ClaimNext is the only writer.
-- The trg_jobs_updated_at trigger doesn't touch this column, so it
-- stays put across subsequent UPDATEs (MarkDone, MarkFailed, etc.).

ALTER TABLE jobs ADD COLUMN started_at TEXT;

UPDATE schema_meta SET schema_version = 3 WHERE id = 1;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- SQLite can't drop columns before 3.35; we're well past that but goose
-- supports ALTER TABLE DROP COLUMN syntax via SQLite directly.
ALTER TABLE jobs DROP COLUMN started_at;

UPDATE schema_meta SET schema_version = 2 WHERE id = 1;

-- +goose StatementEnd
