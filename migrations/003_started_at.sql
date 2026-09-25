-- +goose Up
-- +goose StatementBegin

-- jobs.started_at records when ClaimNext moved a job to 'running'. Without
-- it, the only way to compute execution time is updated_at - created_at,
-- which conflates queue wait time with actual work time and produces
-- nonsense when many jobs are enqueued at once.
--
-- Nullable: jobs from before this migration don't have it, and a job sent
-- back to pending (Requeue, orphan recovery) has it cleared until its next
-- claim. The durations metric skips rows without it (started_at IS NOT
-- NULL); only the oldest-running metric falls back to updated_at. MarkDone
-- and MarkFailed leave it as ClaimNext set it.

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
