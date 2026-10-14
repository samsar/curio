-- +goose Up
-- +goose StatementBegin

-- Chunks embedded with their context (docs/decisions.md "Chunks embedded
-- with their context").
--
-- chunks.context is what a chunk's vector was embedded from besides its
-- text: its document's title and site and the headings it sits under,
-- exactly as the indexer sent them (indexer.Context), without the
-- configured document prefix and the blank line that join it to the
-- text. context_version is the indexer.ContextVersion whose rules built
-- it. The defaults, '' and 0, mean a chunk embedded before chunk context:
-- its vector was made from its text alone. No row is rewritten here: the
-- daemon's start finds the documents holding chunks of an older version
-- and queues their re-embed, which writes both.
--
-- idx_chunks_context_version serves those reads: the chunks of an older
-- version are one range of it, which once the library is re-embedded
-- holds only those of the documents no re-embed reaches (failed, dead or
-- pending, until a fetch indexes them again).
--
-- trg_chunks_update re-tokenized a chunk in chunks_fts on any UPDATE. It
-- now fires only when an indexed value changes (seq is the FTS rowid), as
-- 019's document_fields trigger does, so a re-index that rewrites a chunk
-- in place with the same text, title and tags, or records its context,
-- writes nothing to chunks_fts.
--
-- Two added columns, an index and a trigger rebuild nothing, so this runs
-- inside goose's transaction.

ALTER TABLE chunks ADD COLUMN context TEXT NOT NULL DEFAULT '';
ALTER TABLE chunks ADD COLUMN context_version INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_chunks_context_version ON chunks(context_version, document_id);

DROP TRIGGER trg_chunks_update;
CREATE TRIGGER trg_chunks_update
AFTER UPDATE OF seq, text, title, tags ON chunks
WHEN (old.seq, old.text, old.title, old.tags) IS NOT (new.seq, new.text, new.title, new.tags) BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, text, title, tags)
    VALUES ('delete', old.seq, old.text, old.title, old.tags);
    INSERT INTO chunks_fts (rowid, text, title, tags)
    VALUES (new.seq, new.text, new.title, new.tags);
END;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- 008's update trigger comes back with its text, and the index goes before
-- the columns, which a DROP COLUMN refuses while an index names one, so
-- the schema is 020's byte for byte.

DROP TRIGGER trg_chunks_update;
CREATE TRIGGER trg_chunks_update AFTER UPDATE ON chunks BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, text, title, tags)
    VALUES ('delete', old.seq, old.text, old.title, old.tags);
    INSERT INTO chunks_fts (rowid, text, title, tags)
    VALUES (new.seq, new.text, new.title, new.tags);
END;

DROP INDEX idx_chunks_context_version;
ALTER TABLE chunks DROP COLUMN context_version;
ALTER TABLE chunks DROP COLUMN context;

-- +goose StatementEnd
