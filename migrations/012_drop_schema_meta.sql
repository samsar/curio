-- +goose Up
-- +goose StatementBegin

-- schema_meta held the embedding model and width 001 hard-coded
-- (nomic-embed-text, 768), whatever the home was created with, and nothing
-- read it. A home's embedding model and width are its marker's
-- (.curio-meta.json), fixed when the home is created, and the vector index
-- is sized from the marker by sqlite.EnsureVectorIndex; the row would
-- contradict every home not made at nomic-embed-text's width.
--
-- No table references schema_meta, so it is dropped inside goose's
-- transaction.

DROP TABLE schema_meta;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- The table as 005 left it, with the row 001 seeded, so the Downs of 005
-- and before still run.
CREATE TABLE schema_meta (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    embedding_model TEXT    NOT NULL,
    embedding_dim   INTEGER NOT NULL,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

INSERT INTO schema_meta (id, embedding_model, embedding_dim)
VALUES (1, 'nomic-embed-text', 768);

-- +goose StatementEnd
