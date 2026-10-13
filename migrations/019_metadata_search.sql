-- +goose Up
-- +goose StatementBegin

-- Metadata search: what a document was saved as, and a card for each
-- document, searched beside its chunks (docs/decisions.md "Metadata
-- search: a fields index and a card per document").
--
-- document_fields is a document's searchable metadata: its title, the
-- titles and folders it was bookmarked under, its site and its author,
-- each derived in Go (internal/docmeta) and written only by the store's
-- refresh (internal/store/sqlite/fields.go), never with INSERT OR REPLACE.
-- host, declared_site and title_suffixes are the inputs of the site name
-- agreed across a host, kept per row so agreeing reads only the host's
-- rows; site_name is the name agreed; rules is the docmeta.RulesVersion
-- the row was derived by, so a change of the rules re-derives every row at
-- the next start. seq is the FTS rowid, an INTEGER PRIMARY KEY that VACUUM
-- keeps, as chunks.seq is (008).
--
-- document_cards is the short text a document is embedded by, its card;
-- embedded_text is the text its vector in document_cards_vec was made
-- from, so a card is stale exactly when the two differ.
-- document_cards_vec, at the home's width, and the trigger that deletes a
-- card's vector with it are created in Go (sqlite.EnsureVectorIndex),
-- together: a migration can't know the width, and a trigger naming a
-- table that doesn't exist yet fails when it fires.
--
-- Neither table is filled here. The daemon derives every document's row
-- at start (sqlite.EnsureDocumentFields), through the same code as every
-- later write, and the cards job embeds the cards.
--
-- Like chunks, the derived tables carry no updated_at.

CREATE TABLE document_fields (
    seq             INTEGER PRIMARY KEY,
    document_id     TEXT    NOT NULL UNIQUE REFERENCES documents(id) ON DELETE CASCADE,
    tenant_id       TEXT    NOT NULL,
    host            TEXT    NOT NULL DEFAULT '',
    declared_site   TEXT    NOT NULL DEFAULT '',
    title_suffixes  TEXT    NOT NULL DEFAULT '',
    site_name       TEXT    NOT NULL DEFAULT '',
    title           TEXT    NOT NULL DEFAULT '',
    bookmark_titles TEXT    NOT NULL DEFAULT '',
    folders         TEXT    NOT NULL DEFAULT '',
    site            TEXT    NOT NULL DEFAULT '',
    author          TEXT    NOT NULL DEFAULT '',
    rules           INTEGER NOT NULL
);

-- A host's rows, read to agree its site name on every write of one of
-- them.
CREATE INDEX idx_document_fields_host ON document_fields(tenant_id, host);

CREATE VIRTUAL TABLE document_fields_fts USING fts5 (
    title,
    bookmark_titles,
    folders,
    site,
    author,
    content        = 'document_fields',
    content_rowid  = 'seq',
    tokenize       = 'porter unicode61 remove_diacritics 2'
);

-- The external-content pattern of chunks_fts (008). The update trigger
-- names the indexed columns alone, so a write of the agreement's inputs,
-- the agreed name or the rules version never re-tokenizes a row.
CREATE TRIGGER trg_document_fields_insert AFTER INSERT ON document_fields BEGIN
    INSERT INTO document_fields_fts (rowid, title, bookmark_titles, folders, site, author)
    VALUES (new.seq, new.title, new.bookmark_titles, new.folders, new.site, new.author);
END;

CREATE TRIGGER trg_document_fields_delete AFTER DELETE ON document_fields BEGIN
    INSERT INTO document_fields_fts (document_fields_fts, rowid, title, bookmark_titles, folders, site, author)
    VALUES ('delete', old.seq, old.title, old.bookmark_titles, old.folders, old.site, old.author);
END;

CREATE TRIGGER trg_document_fields_update
AFTER UPDATE OF title, bookmark_titles, folders, site, author ON document_fields BEGIN
    INSERT INTO document_fields_fts (document_fields_fts, rowid, title, bookmark_titles, folders, site, author)
    VALUES ('delete', old.seq, old.title, old.bookmark_titles, old.folders, old.site, old.author);
    INSERT INTO document_fields_fts (rowid, title, bookmark_titles, folders, site, author)
    VALUES (new.seq, new.title, new.bookmark_titles, new.folders, new.site, new.author);
END;

CREATE TABLE document_cards (
    document_id   TEXT PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    text          TEXT NOT NULL,
    embedded_text TEXT,
    embedded_at   TEXT
);

-- The stale cards alone, which the cards job pages through by document.
CREATE INDEX idx_document_cards_stale ON document_cards(document_id) WHERE embedded_text IS NOT text;

-- jobs gains the cards kind. SQLite can't change a CHECK in place, so the
-- table is rebuilt. Nothing references jobs (pragma_foreign_key_list
-- names no table whose foreign key points at it), so dropping the old one
-- runs no ON DELETE actions even with foreign keys on, and the rebuild
-- runs inside goose's transaction, as 008's of chunks does. The old table
-- steps aside rather than the new one being renamed into place, so jobs
-- keeps the CREATE TABLE text written here; its indexes go with it and
-- come back with their 018 text. jobs has had no triggers since 006.

DROP INDEX idx_jobs_document;
DROP INDEX idx_jobs_claim;
DROP INDEX idx_jobs_tenant_updated;
DROP INDEX idx_jobs_tenant_status_updated;
ALTER TABLE jobs RENAME TO jobs_v18;

CREATE TABLE jobs (
    id          TEXT    PRIMARY KEY,
    tenant_id   TEXT    NOT NULL,
    kind        TEXT    NOT NULL
                        CHECK (kind IN ('fetch','index','import','cluster','summarize','cards')),
    payload     TEXT    NOT NULL CHECK (json_valid(payload)),
    status      TEXT    NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','running','done','failed')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    run_after   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_error  TEXT,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    started_at  TEXT,
    document_id TEXT    REFERENCES documents(id) ON DELETE SET NULL
);

INSERT INTO jobs (id, tenant_id, kind, payload, status, attempts, run_after, last_error, created_at, updated_at,
                  started_at, document_id)
    SELECT id, tenant_id, kind, payload, status, attempts, run_after, last_error, created_at, updated_at,
           started_at, document_id
    FROM jobs_v18
    ORDER BY rowid;

DROP TABLE jobs_v18;

CREATE INDEX idx_jobs_document ON jobs(document_id, status, updated_at);
CREATE INDEX idx_jobs_claim ON jobs(status, kind, run_after, created_at);
CREATE INDEX idx_jobs_tenant_updated ON jobs(tenant_id, updated_at, id);
CREATE INDEX idx_jobs_tenant_status_updated ON jobs(tenant_id, status, updated_at, id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- 018's jobs can't hold a cards job, so those go first. The table comes
-- back with the text 018 left it with, the columns 003 and 007 added
-- included, and its indexes with theirs, so the schema is exactly what it
-- was. document_cards_vec is Go's (EnsureVectorIndex), so it may not
-- exist; curio's driver loads sqlite-vec, which a vec0 table's DROP needs.
-- Dropping document_cards takes the trigger EnsureVectorIndex put on it.

DELETE FROM jobs WHERE kind = 'cards';

DROP INDEX idx_jobs_document;
DROP INDEX idx_jobs_claim;
DROP INDEX idx_jobs_tenant_updated;
DROP INDEX idx_jobs_tenant_status_updated;
ALTER TABLE jobs RENAME TO jobs_v19;

CREATE TABLE jobs (
    id          TEXT    PRIMARY KEY,
    tenant_id   TEXT    NOT NULL,
    kind        TEXT    NOT NULL
                        CHECK (kind IN ('fetch','index','import','cluster','summarize')),
    payload     TEXT    NOT NULL CHECK (json_valid(payload)),
    status      TEXT    NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','running','done','failed')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    run_after   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_error  TEXT,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
, started_at TEXT, document_id TEXT REFERENCES documents(id) ON DELETE SET NULL);

INSERT INTO jobs (id, tenant_id, kind, payload, status, attempts, run_after, last_error, created_at, updated_at,
                  started_at, document_id)
    SELECT id, tenant_id, kind, payload, status, attempts, run_after, last_error, created_at, updated_at,
           started_at, document_id
    FROM jobs_v19
    ORDER BY rowid;

DROP TABLE jobs_v19;

CREATE INDEX idx_jobs_document ON jobs(document_id, status, updated_at);
CREATE INDEX idx_jobs_claim ON jobs(status, kind, run_after, created_at);
CREATE INDEX idx_jobs_tenant_updated ON jobs(tenant_id, updated_at, id);
CREATE INDEX idx_jobs_tenant_status_updated ON jobs(tenant_id, status, updated_at, id);

DROP TABLE IF EXISTS document_cards_vec;
DROP TABLE document_cards;
DROP TABLE document_fields_fts;
DROP TABLE document_fields;

-- +goose StatementEnd
