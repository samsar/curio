-- +goose Up
-- +goose StatementBegin

-- The queue gate's settings: whether the queue is paused, how hard it runs
-- (normal, or gentle: fewer fetches and one index job at a time) and the
-- daily window it may run in. The daemon reads the row once at startup and
-- writes it only when a setting changes, so a pause survives a restart.
--
-- One row (id = 1), and no tenant_id: workers claim across tenants, so the
-- gate is daemon-wide. No row means the defaults, so there is no seed.
-- The window is minutes after midnight on the daemon's local clock, from
-- schedule_start up to schedule_end, wrapping midnight when the end is the
-- smaller; both are NULL when there is no schedule.
--
-- No table references this one, so it is created inside goose's
-- transaction.

CREATE TABLE queue_settings (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    paused         INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0, 1)),
    throttle       TEXT    NOT NULL DEFAULT 'normal' CHECK (throttle IN ('normal', 'gentle')),
    schedule_start INTEGER CHECK (schedule_start BETWEEN 0 AND 1439),
    schedule_end   INTEGER CHECK (schedule_end BETWEEN 0 AND 1439),
    updated_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    CHECK ((schedule_start IS NULL) = (schedule_end IS NULL)),
    CHECK (schedule_start IS NULL OR schedule_start <> schedule_end)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE queue_settings;

-- +goose StatementEnd
