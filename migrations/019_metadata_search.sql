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
-- host, declared_site and title_suffixes are what the document contributes
-- to the site name agreed across its host: the name its page declares, and
-- the distinct title suffixes of its title and bookmark titles, a JSON
-- array; site_name is the name agreed; rules is the docmeta.RulesVersion
-- the row was derived by, so a change of the rules re-derives every row at
-- the next start. seq is the FTS rowid, an INTEGER PRIMARY KEY that VACUUM
-- keeps, as chunks.seq is (008).
--
-- site_name_votes is what agreeing a host's name reads: per tenant and
-- host, how many of its documents declare each name and how many carry
-- each title suffix, so a write reads the two leaders of its host, one
-- indexed seek each, never every row of the host. Triggers on
-- document_fields keep it equal to what the rows contribute, in the
-- statement that writes a row, whatever writes it: a cascade delete of a
-- document withdraws its votes as it removes its row. The suffixes are a
-- JSON array because a trigger can split one with json_each, and has no
-- way to split a list a line (a trigger can't hold a CTE).
--
-- document_cards is the short text a document is embedded by, its card;
-- embedded_text is the text its vector in document_cards_vec was made
-- from, so a card is stale exactly when the two differ.
-- document_cards_vec, at the home's width, and the trigger that deletes a
-- card's vector with it are created in Go (sqlite.EnsureVectorIndex),
-- together: a migration can't know the width, and a trigger naming a
-- table that doesn't exist yet fails when it fires.
--
-- No table is filled here. The daemon derives every document's row at
-- start (sqlite.EnsureDocumentFields), through the same code as every
-- later write, the triggers count the rows' votes as they are written,
-- and the cards job embeds the cards.
--
-- Like chunks, the derived tables carry no updated_at.

CREATE TABLE document_fields (
    seq             INTEGER PRIMARY KEY,
    document_id     TEXT    NOT NULL UNIQUE REFERENCES documents(id) ON DELETE CASCADE,
    tenant_id       TEXT    NOT NULL,
    host            TEXT    NOT NULL DEFAULT '',
    declared_site   TEXT    NOT NULL DEFAULT '',
    title_suffixes  TEXT    NOT NULL DEFAULT '[]'
                            CHECK (json_valid(title_suffixes) AND json_type(title_suffixes) = 'array'),
    site_name       TEXT    NOT NULL DEFAULT '',
    title           TEXT    NOT NULL DEFAULT '',
    bookmark_titles TEXT    NOT NULL DEFAULT '',
    folders         TEXT    NOT NULL DEFAULT '',
    site            TEXT    NOT NULL DEFAULT '',
    author          TEXT    NOT NULL DEFAULT '',
    rules           INTEGER NOT NULL
);

-- A host's documents by their declared name and the name they hold: those
-- without a declared name and with another name than the one their host
-- agrees on are two ranges of it, read on every write to find the
-- documents to rename, and found only when the agreement changed.
CREATE INDEX idx_document_fields_site ON document_fields(tenant_id, host, declared_site, site_name);

CREATE TABLE site_name_votes (
    tenant_id TEXT    NOT NULL,
    host      TEXT    NOT NULL,
    kind      TEXT    NOT NULL CHECK (kind IN ('declared', 'suffix')),
    name      TEXT    NOT NULL,
    votes     INTEGER NOT NULL CHECK (votes > 0),
    PRIMARY KEY (tenant_id, host, kind, name)
) WITHOUT ROWID;

-- A host's names of one kind, the most held first, ties in byte order:
-- the leader is the first row.
CREATE INDEX idx_site_name_votes_lead ON site_name_votes(tenant_id, host, kind, votes DESC, name);

-- A row's votes: its declared name, when it has one, and each of its
-- suffixes, DISTINCT so a document counts once for a suffix whatever its
-- array holds. A vote row at 1 is deleted rather than decremented, as the
-- CHECK requires, so a name nobody holds has no row.
CREATE TRIGGER trg_document_fields_votes_insert AFTER INSERT ON document_fields BEGIN
    INSERT INTO site_name_votes (tenant_id, host, kind, name, votes)
        SELECT new.tenant_id, new.host, 'declared', new.declared_site, 1 WHERE new.declared_site <> ''
        ON CONFLICT DO UPDATE SET votes = votes + 1;
    INSERT INTO site_name_votes (tenant_id, host, kind, name, votes)
        SELECT DISTINCT new.tenant_id, new.host, 'suffix', value, 1 FROM json_each(new.title_suffixes) WHERE true
        ON CONFLICT DO UPDATE SET votes = votes + 1;
END;

CREATE TRIGGER trg_document_fields_votes_delete AFTER DELETE ON document_fields BEGIN
    DELETE FROM site_name_votes
        WHERE tenant_id = old.tenant_id AND host = old.host AND kind = 'declared' AND name = old.declared_site
          AND votes = 1;
    UPDATE site_name_votes SET votes = votes - 1
        WHERE tenant_id = old.tenant_id AND host = old.host AND kind = 'declared' AND name = old.declared_site
          AND votes > 1;
    DELETE FROM site_name_votes
        WHERE tenant_id = old.tenant_id AND host = old.host AND kind = 'suffix'
          AND name IN (SELECT value FROM json_each(old.title_suffixes)) AND votes = 1;
    UPDATE site_name_votes SET votes = votes - 1
        WHERE tenant_id = old.tenant_id AND host = old.host AND kind = 'suffix'
          AND name IN (SELECT value FROM json_each(old.title_suffixes)) AND votes > 1;
END;

-- A move of the votes: the old row's withdrawn, the new row's added. It
-- fires only when what the row contributes changed, so an upsert that sets
-- every column, as the refresh's does, moves nothing for a row whose
-- contribution stayed.
CREATE TRIGGER trg_document_fields_votes_update
AFTER UPDATE OF tenant_id, host, declared_site, title_suffixes ON document_fields
WHEN (old.tenant_id, old.host, old.declared_site, old.title_suffixes)
     IS NOT (new.tenant_id, new.host, new.declared_site, new.title_suffixes) BEGIN
    DELETE FROM site_name_votes
        WHERE tenant_id = old.tenant_id AND host = old.host AND kind = 'declared' AND name = old.declared_site
          AND votes = 1;
    UPDATE site_name_votes SET votes = votes - 1
        WHERE tenant_id = old.tenant_id AND host = old.host AND kind = 'declared' AND name = old.declared_site
          AND votes > 1;
    DELETE FROM site_name_votes
        WHERE tenant_id = old.tenant_id AND host = old.host AND kind = 'suffix'
          AND name IN (SELECT value FROM json_each(old.title_suffixes)) AND votes = 1;
    UPDATE site_name_votes SET votes = votes - 1
        WHERE tenant_id = old.tenant_id AND host = old.host AND kind = 'suffix'
          AND name IN (SELECT value FROM json_each(old.title_suffixes)) AND votes > 1;
    INSERT INTO site_name_votes (tenant_id, host, kind, name, votes)
        SELECT new.tenant_id, new.host, 'declared', new.declared_site, 1 WHERE new.declared_site <> ''
        ON CONFLICT DO UPDATE SET votes = votes + 1;
    INSERT INTO site_name_votes (tenant_id, host, kind, name, votes)
        SELECT DISTINCT new.tenant_id, new.host, 'suffix', value, 1 FROM json_each(new.title_suffixes) WHERE true
        ON CONFLICT DO UPDATE SET votes = votes + 1;
END;

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
-- fires only when a searched column's value changed, not whenever an
-- UPDATE names one, so a write of the agreement's inputs, the agreed name
-- or the rules version never re-tokenizes a row, even through an upsert
-- that sets every column.
CREATE TRIGGER trg_document_fields_insert AFTER INSERT ON document_fields BEGIN
    INSERT INTO document_fields_fts (rowid, title, bookmark_titles, folders, site, author)
    VALUES (new.seq, new.title, new.bookmark_titles, new.folders, new.site, new.author);
END;

CREATE TRIGGER trg_document_fields_delete AFTER DELETE ON document_fields BEGIN
    INSERT INTO document_fields_fts (document_fields_fts, rowid, title, bookmark_titles, folders, site, author)
    VALUES ('delete', old.seq, old.title, old.bookmark_titles, old.folders, old.site, old.author);
END;

CREATE TRIGGER trg_document_fields_update
AFTER UPDATE OF title, bookmark_titles, folders, site, author ON document_fields
WHEN (old.title, old.bookmark_titles, old.folders, old.site, old.author)
     IS NOT (new.title, new.bookmark_titles, new.folders, new.site, new.author) BEGIN
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
DROP TABLE site_name_votes;

-- +goose StatementEnd
