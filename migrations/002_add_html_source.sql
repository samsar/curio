-- +goose Up
-- +goose StatementBegin

-- Add 'html' to the bookmarks.source CHECK list so the importer can
-- record HTML-export-imported bookmarks. SQLite doesn't support
-- ALTER TABLE ... DROP CONSTRAINT, so we do the standard rebuild dance:
-- new table → copy → drop old → rename new → restore indexes/triggers.

PRAGMA foreign_keys = OFF;

CREATE TABLE bookmarks_new (
    id           TEXT    PRIMARY KEY,
    tenant_id    TEXT    NOT NULL,
    document_id  TEXT    REFERENCES documents(id) ON DELETE SET NULL,
    url          TEXT    NOT NULL,
    title        TEXT,
    saved_at     TEXT    NOT NULL,
    source       TEXT    NOT NULL
                         CHECK (source IN ('chrome','safari','firefox','manual','html')),
    folder_path  TEXT,
    tags         TEXT    CHECK (tags IS NULL OR json_valid(tags)),
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, url, source)
);

INSERT INTO bookmarks_new
    SELECT id, tenant_id, document_id, url, title, saved_at, source,
           folder_path, tags, created_at, updated_at
    FROM bookmarks;

DROP TRIGGER IF EXISTS trg_bookmarks_updated_at;
DROP TABLE bookmarks;
ALTER TABLE bookmarks_new RENAME TO bookmarks;

CREATE INDEX idx_bookmarks_tenant_source ON bookmarks(tenant_id, source);
CREATE INDEX idx_bookmarks_document      ON bookmarks(document_id);
CREATE INDEX idx_bookmarks_folder        ON bookmarks(tenant_id, folder_path);

CREATE TRIGGER trg_bookmarks_updated_at
AFTER UPDATE ON bookmarks FOR EACH ROW
BEGIN
    UPDATE bookmarks SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = NEW.id;
END;

PRAGMA foreign_keys = ON;

UPDATE schema_meta SET schema_version = 2 WHERE id = 1;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reverses to v1's CHECK. Any rows with source='html' will fail the
-- new constraint; the down migration deletes them rather than aborting,
-- because the alternative is leaving the DB in a broken state.

PRAGMA foreign_keys = OFF;

DELETE FROM bookmarks WHERE source = 'html';

CREATE TABLE bookmarks_old (
    id           TEXT    PRIMARY KEY,
    tenant_id    TEXT    NOT NULL,
    document_id  TEXT    REFERENCES documents(id) ON DELETE SET NULL,
    url          TEXT    NOT NULL,
    title        TEXT,
    saved_at     TEXT    NOT NULL,
    source       TEXT    NOT NULL
                         CHECK (source IN ('chrome','safari','firefox','manual')),
    folder_path  TEXT,
    tags         TEXT    CHECK (tags IS NULL OR json_valid(tags)),
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, url, source)
);

INSERT INTO bookmarks_old
    SELECT id, tenant_id, document_id, url, title, saved_at, source,
           folder_path, tags, created_at, updated_at
    FROM bookmarks;

DROP TRIGGER IF EXISTS trg_bookmarks_updated_at;
DROP TABLE bookmarks;
ALTER TABLE bookmarks_old RENAME TO bookmarks;

CREATE INDEX idx_bookmarks_tenant_source ON bookmarks(tenant_id, source);
CREATE INDEX idx_bookmarks_document      ON bookmarks(document_id);
CREATE INDEX idx_bookmarks_folder        ON bookmarks(tenant_id, folder_path);

CREATE TRIGGER trg_bookmarks_updated_at
AFTER UPDATE ON bookmarks FOR EACH ROW
BEGIN
    UPDATE bookmarks SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = NEW.id;
END;

PRAGMA foreign_keys = ON;

UPDATE schema_meta SET schema_version = 1 WHERE id = 1;

-- +goose StatementEnd
