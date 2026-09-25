-- +goose Up
-- +goose StatementBegin

-- chunks_fts becomes an external-content FTS5 index over chunks, kept in
-- step by triggers. It stored a second copy of every chunk's text, plus a
-- title nothing read and the chunk and document IDs, and deleting a
-- document's rows from it (or from chunks_vec) scanned the whole table.
--
-- chunks gets:
--   * seq INTEGER PRIMARY KEY, the FTS rowid. SQLite keeps an INTEGER
--     PRIMARY KEY across VACUUM and table rebuilds; an implicit rowid may be
--     renumbered, which would silently detach the index from its rows. id
--     stays the chunk's public ID.
--   * title and tags: exactly the strings indexed for the chunk. Deleting
--     from an external-content index must supply the indexed values, and
--     the document's title may have changed since.
--
-- This runs in goose's transaction: no foreign key references chunks, so
-- dropping the old table runs no ON DELETE actions even with foreign keys
-- on. chunks_vec is not touched.
--
-- The rebuild re-tokenizes every chunk; measured in docs/decisions.md
-- ("Chunks: external-content FTS").

-- Save each chunk's indexed title and tags in one pass, so the old index
-- and its copy of the text can go first.
CREATE TEMP TABLE chunk_fts_values (
    chunk_id TEXT PRIMARY KEY,
    title    TEXT,
    tags     TEXT
);
INSERT OR IGNORE INTO temp.chunk_fts_values (chunk_id, title, tags)
    SELECT chunk_id, title_search, tags FROM chunks_fts;

DROP TABLE chunks_fts;

-- The old table steps aside rather than the new one being renamed into
-- place, so chunks keeps the CREATE TABLE text written here.
DROP INDEX idx_chunks_document;
ALTER TABLE chunks RENAME TO chunks_v7;

CREATE TABLE chunks (
    seq           INTEGER PRIMARY KEY,
    id            TEXT    NOT NULL UNIQUE,
    document_id   TEXT    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    extraction_id TEXT    NOT NULL REFERENCES document_extractions(id) ON DELETE CASCADE,
    ord           INTEGER NOT NULL,
    text          TEXT    NOT NULL,
    token_count   INTEGER,
    title         TEXT    NOT NULL DEFAULT '',  -- as indexed in chunks_fts
    tags          TEXT    NOT NULL DEFAULT '',  -- as indexed in chunks_fts
    UNIQUE (extraction_id, ord)
);

INSERT INTO chunks (id, document_id, extraction_id, ord, text, token_count, title, tags)
    SELECT c.id, c.document_id, c.extraction_id, c.ord, c.text, c.token_count,
           COALESCE(f.title, ''), COALESCE(f.tags, '')
    FROM chunks_v7 c
    LEFT JOIN temp.chunk_fts_values f ON f.chunk_id = c.id
    ORDER BY c.rowid;

DROP TABLE chunks_v7;
CREATE INDEX idx_chunks_document ON chunks(document_id);

CREATE VIRTUAL TABLE chunks_fts USING fts5 (
    text,
    title,
    tags,
    content        = 'chunks',
    content_rowid  = 'seq',
    tokenize       = 'porter unicode61 remove_diacritics 2'
);
INSERT INTO chunks_fts (chunks_fts) VALUES ('rebuild');

-- The FTS5 external-content pattern: every change to chunks is mirrored
-- into the index, the delete supplying the values that were indexed. The
-- delete trigger also removes the chunk's vector, so every way a chunk
-- goes, foreign-key cascades included, takes its derived rows with it.
-- Never write chunks with INSERT OR REPLACE: REPLACE deletes the old row
-- without firing delete triggers (recursive_triggers is off).
CREATE TRIGGER trg_chunks_insert AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts (rowid, text, title, tags)
    VALUES (new.seq, new.text, new.title, new.tags);
END;

CREATE TRIGGER trg_chunks_delete AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, text, title, tags)
    VALUES ('delete', old.seq, old.text, old.title, old.tags);
    DELETE FROM chunks_vec WHERE chunk_id = old.id;
END;

CREATE TRIGGER trg_chunks_update AFTER UPDATE ON chunks BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, text, title, tags)
    VALUES ('delete', old.seq, old.text, old.title, old.tags);
    INSERT INTO chunks_fts (rowid, text, title, tags)
    VALUES (new.seq, new.text, new.title, new.tags);
END;

DROP TABLE temp.chunk_fts_values;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Back to 001's chunks and its regular six-column FTS table, repopulated
-- from the chunks' own title and tags. The triggers go first: the delete
-- trigger would otherwise take chunks_vec rows with it.
DROP TRIGGER trg_chunks_update;
DROP TRIGGER trg_chunks_delete;
DROP TRIGGER trg_chunks_insert;
DROP TABLE chunks_fts;

DROP INDEX idx_chunks_document;
ALTER TABLE chunks RENAME TO chunks_v8;

CREATE TABLE chunks (
    id            TEXT    PRIMARY KEY,
    document_id   TEXT    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    extraction_id TEXT    NOT NULL REFERENCES document_extractions(id) ON DELETE CASCADE,
    ord           INTEGER NOT NULL,
    text          TEXT    NOT NULL,
    token_count   INTEGER,
    UNIQUE (extraction_id, ord)
);
INSERT INTO chunks (id, document_id, extraction_id, ord, text, token_count)
    SELECT id, document_id, extraction_id, ord, text, token_count FROM chunks_v8 ORDER BY seq;

CREATE VIRTUAL TABLE chunks_fts USING fts5 (
    text,
    title          UNINDEXED,  -- displayed in results, not searched here
    title_search,              -- copy of title that IS searched, can be boosted
    tags,
    chunk_id       UNINDEXED,
    document_id    UNINDEXED,
    tokenize       = 'porter unicode61 remove_diacritics 2'
);
INSERT INTO chunks_fts (text, title, title_search, tags, chunk_id, document_id)
    SELECT text, title, title, tags, id, document_id FROM chunks_v8 ORDER BY seq;

DROP TABLE chunks_v8;
CREATE INDEX idx_chunks_document ON chunks(document_id);

-- +goose StatementEnd
