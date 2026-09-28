-- +goose Up
-- +goose StatementBegin

-- Whether the daemon keeps the Mac from idle sleep while its workers have
-- queued work and it runs on AC power (internal/keepawake). A queue
-- setting, like the pause, rather than a config.yaml key: it is turned on
-- and off while an import runs, takes effect at once, and survives a
-- restart, launchd's included. Off unless the user turns it on.
--
-- Adding a column rebuilds nothing, so this runs inside goose's
-- transaction, and the existing row, if any, reads 0.

ALTER TABLE queue_settings ADD COLUMN keep_awake INTEGER NOT NULL DEFAULT 0 CHECK (keep_awake IN (0, 1));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE queue_settings DROP COLUMN keep_awake;

-- +goose StatementEnd
