-- +goose Up
-- +goose StatementBegin

-- Summaries: one structured call of a writing model per fetched document
-- (docs/decisions.md "Summaries: one structured call per document,
-- stored for the reranker").
--
-- document_summaries holds a document's summary, text a writing model
-- wrote, not a derived row: no field or card reads it, and the store
-- writes it with an upsert (internal/store/sqlite/summaries.go), never
-- with INSERT OR REPLACE. extraction_id is the extraction the
-- summary read, NULL for a row that holds only a failure; model and
-- prompt_version are who wrote it, input_hash the SHA-256 of that
-- prompt's version and the exact input, so a refetch whose input is the
-- same is pointed at its new extraction without asking the model again.
-- kind has no CHECK: the kinds grow with the prompts, and the store
-- validates them in Go, as 014 does the failure causes. The lists
-- (entities, claims, descriptions) hold one item a line, as
-- document_fields' do, so the tokenizer reads words, not JSON escapes.
-- owed marks a summary to write again (a model or prompt changed and the
-- user asked); the failed_* columns are the last failure for good, under
-- which extraction, model and prompt, cleared by the next write. seq is
-- the FTS rowid, an INTEGER PRIMARY KEY that VACUUM keeps, as
-- document_fields.seq is (019). Like the derived tables, it carries no
-- updated_at.
--
-- document_summaries_fts is the summary list's index: its own, never
-- columns of document_fields_fts. FTS5's bm25() normalizes a match by the
-- length of the whole row, so summary columns there would move every
-- fields and title match, and a weight bm25() isn't given is 1, so an
-- older daemon's five weights would count them at full weight. The update
-- trigger fires only when a searched value changed (019's pattern), so a
-- write that points a summary at a new extraction, owes it, or records a
-- failure re-tokenizes nothing. A cascade delete of a document fires the
-- delete trigger with its row.
--
-- Nothing references the new table and nothing is rebuilt, so this runs
-- inside goose's transaction.

CREATE TABLE document_summaries (
    seq                   INTEGER PRIMARY KEY,
    document_id           TEXT    NOT NULL UNIQUE REFERENCES documents(id) ON DELETE CASCADE,
    extraction_id         TEXT,
    kind                  TEXT    NOT NULL DEFAULT '',
    site                  TEXT    NOT NULL DEFAULT '',
    author                TEXT    NOT NULL DEFAULT '',
    about                 TEXT    NOT NULL DEFAULT '',
    entities              TEXT    NOT NULL DEFAULT '',
    claims                TEXT    NOT NULL DEFAULT '',
    descriptions          TEXT    NOT NULL DEFAULT '',
    model                 TEXT    NOT NULL DEFAULT '',
    prompt_version        TEXT    NOT NULL DEFAULT '',
    input_hash            TEXT    NOT NULL DEFAULT '',
    written_at            TEXT,
    owed                  INTEGER NOT NULL DEFAULT 0 CHECK (owed IN (0, 1)),
    failed_extraction_id  TEXT,
    failed_model          TEXT,
    failed_prompt_version TEXT,
    failed_at             TEXT,
    failure               TEXT
);

CREATE VIRTUAL TABLE document_summaries_fts USING fts5 (
    site,
    author,
    entities,
    about,
    descriptions,
    claims,
    content        = 'document_summaries',
    content_rowid  = 'seq',
    tokenize       = 'porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER trg_document_summaries_insert AFTER INSERT ON document_summaries BEGIN
    INSERT INTO document_summaries_fts (rowid, site, author, entities, about, descriptions, claims)
    VALUES (new.seq, new.site, new.author, new.entities, new.about, new.descriptions, new.claims);
END;

CREATE TRIGGER trg_document_summaries_delete AFTER DELETE ON document_summaries BEGIN
    INSERT INTO document_summaries_fts (document_summaries_fts, rowid, site, author, entities, about, descriptions,
        claims)
    VALUES ('delete', old.seq, old.site, old.author, old.entities, old.about, old.descriptions, old.claims);
END;

CREATE TRIGGER trg_document_summaries_update
AFTER UPDATE OF site, author, entities, about, descriptions, claims ON document_summaries
WHEN (old.site, old.author, old.entities, old.about, old.descriptions, old.claims)
     IS NOT (new.site, new.author, new.entities, new.about, new.descriptions, new.claims) BEGIN
    INSERT INTO document_summaries_fts (document_summaries_fts, rowid, site, author, entities, about, descriptions,
        claims)
    VALUES ('delete', old.seq, old.site, old.author, old.entities, old.about, old.descriptions, old.claims);
    INSERT INTO document_summaries_fts (rowid, site, author, entities, about, descriptions, claims)
    VALUES (new.seq, new.site, new.author, new.entities, new.about, new.descriptions, new.claims);
END;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER trg_document_summaries_update;
DROP TRIGGER trg_document_summaries_delete;
DROP TRIGGER trg_document_summaries_insert;
DROP TABLE document_summaries_fts;
DROP TABLE document_summaries;

-- +goose StatementEnd
