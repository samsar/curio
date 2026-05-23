-- +goose Up
-- +goose StatementBegin

-- Curio v1 schema.
--
-- See docs/data-model.md for the conceptual model. Notes specific to SQLite:
--   * Foreign key enforcement requires `PRAGMA foreign_keys = ON` at every
--     connection. The daemon sets this on each opened connection.
--   * UUIDs are stored as TEXT (36-char canonical form). SQLite has no native
--     UUID type and TEXT is fine at this scale; do not store as BLOB.
--   * Timestamps are TEXT in RFC 3339 UTC (`YYYY-MM-DDTHH:MM:SS.SSSZ`).
--     SQLite has no timestamp type; this is the standard convention and
--     sorts correctly lexicographically.
--   * JSON columns are TEXT validated with `CHECK(json_valid(col))`.
--
-- NOTE: PRAGMA foreign_keys and journal_mode are set per-connection by the
-- daemon (via DSN params), not here. PRAGMA journal_mode = WAL cannot run
-- inside goose's migration transaction; setting it via DSN is also more
-- correct because connections in the pool need consistent state.

-- ============================================================================
-- documents: universal content table, deduplicated by (tenant_id, url)
-- ============================================================================

CREATE TABLE documents (
    id                    TEXT    PRIMARY KEY,
    tenant_id             TEXT    NOT NULL,
    url                   TEXT    NOT NULL,
    url_canonical         TEXT,
    content_type          TEXT    NOT NULL DEFAULT 'unknown'
                                  CHECK (content_type IN
                                    ('article','repo','video','pdf','thread','unknown')),
    title                 TEXT,
    author                TEXT,
    published_at          TEXT,
    language              TEXT,
    word_count            INTEGER,
    current_extraction_id TEXT,
    state                 TEXT    NOT NULL DEFAULT 'pending'
                                  CHECK (state IN ('pending','fetched','failed','dead')),
    created_at            TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at            TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, url)
    -- current_extraction_id FK is added later (circular dep with extractions)
);

CREATE INDEX idx_documents_tenant_state    ON documents(tenant_id, state);
CREATE INDEX idx_documents_tenant_ctype    ON documents(tenant_id, content_type);
CREATE INDEX idx_documents_url_canonical   ON documents(url_canonical);

-- ============================================================================
-- document_extractions: each fetch attempt is a row; preserves history
-- ============================================================================

CREATE TABLE document_extractions (
    id              TEXT    PRIMARY KEY,
    document_id     TEXT    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    fetched_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    fetcher         TEXT    NOT NULL,
    status          TEXT    NOT NULL
                            CHECK (status IN ('ok','partial','paywalled','error')),
    markdown_path   TEXT,   -- relative to $CURIO_HOME/content/
    raw_path        TEXT,
    extraction_meta TEXT    CHECK (extraction_meta IS NULL OR json_valid(extraction_meta)),
    error_message   TEXT
);

CREATE INDEX idx_extractions_document ON document_extractions(document_id, fetched_at DESC);

-- Now we can add the FK on documents.current_extraction_id without circular issues
-- (SQLite doesn't support ALTER TABLE ADD CONSTRAINT, so we enforce via trigger
-- and rely on the daemon to keep it consistent.)
CREATE TRIGGER documents_current_extraction_fk
BEFORE UPDATE OF current_extraction_id ON documents
WHEN NEW.current_extraction_id IS NOT NULL
     AND NOT EXISTS (SELECT 1 FROM document_extractions WHERE id = NEW.current_extraction_id)
BEGIN
    SELECT RAISE(ABORT, 'current_extraction_id references missing document_extractions row');
END;

-- ============================================================================
-- chunks: text segments produced from an extraction; indexed by FTS5 + vec
-- ============================================================================

CREATE TABLE chunks (
    id            TEXT    PRIMARY KEY,
    document_id   TEXT    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    extraction_id TEXT    NOT NULL REFERENCES document_extractions(id) ON DELETE CASCADE,
    ord           INTEGER NOT NULL,
    text          TEXT    NOT NULL,
    token_count   INTEGER,
    UNIQUE (extraction_id, ord)
);

CREATE INDEX idx_chunks_document ON chunks(document_id);

-- FTS5 virtual table for BM25.
-- Tokenizer: porter unicode61 (Porter stemming over Unicode-aware tokenization).
-- We index chunk text and a few denormalized boostable fields populated by
-- the indexer at write time. Keeping title/tags here means we can boost without
-- a JOIN at query time.
CREATE VIRTUAL TABLE chunks_fts USING fts5 (
    text,
    title          UNINDEXED,  -- displayed in results, not searched here
    title_search,              -- copy of title that IS searched, can be boosted
    tags,
    chunk_id       UNINDEXED,
    document_id    UNINDEXED,
    tokenize       = 'porter unicode61 remove_diacritics 2'
);

-- Vector index for ANN search.
-- Dimension MUST match the configured embedder.Dimensions(). v1 locks to 768
-- (nomic-embed-text). Changing dimensions requires DROP + CREATE of this table
-- and re-embedding all chunks; see decisions.md#embedding-model-swap.
CREATE VIRTUAL TABLE chunks_vec USING vec0 (
    chunk_id   TEXT PRIMARY KEY,
    embedding  FLOAT[768]
);

-- ============================================================================
-- bookmarks: the v1 reference table
-- ============================================================================

CREATE TABLE bookmarks (
    id           TEXT    PRIMARY KEY,
    tenant_id    TEXT    NOT NULL,
    document_id  TEXT    REFERENCES documents(id) ON DELETE SET NULL,
    url          TEXT    NOT NULL,
    title        TEXT,
    saved_at     TEXT    NOT NULL,
    source       TEXT    NOT NULL
                         CHECK (source IN ('chrome','safari','firefox','manual')),
    folder_path  TEXT,
    tags         TEXT    CHECK (tags IS NULL OR json_valid(tags)),  -- JSON string array
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, url, source)
);

CREATE INDEX idx_bookmarks_tenant_source ON bookmarks(tenant_id, source);
CREATE INDEX idx_bookmarks_document      ON bookmarks(document_id);
CREATE INDEX idx_bookmarks_folder        ON bookmarks(tenant_id, folder_path);

-- ============================================================================
-- jobs: SQLite-backed work queue
-- ============================================================================

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
);

-- The worker pool's hot query is:
--   SELECT * FROM jobs WHERE status='pending' AND run_after <= ? ORDER BY created_at LIMIT N
-- Composite index makes this an index-only scan.
CREATE INDEX idx_jobs_dispatch ON jobs(status, run_after, created_at);
CREATE INDEX idx_jobs_kind     ON jobs(tenant_id, kind, status);

-- ============================================================================
-- schema_meta: single-row table mirroring .curio-meta.json for sanity checks
-- ============================================================================

CREATE TABLE schema_meta (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    schema_version  INTEGER NOT NULL,
    embedding_model TEXT    NOT NULL,
    embedding_dim   INTEGER NOT NULL,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

INSERT INTO schema_meta (id, schema_version, embedding_model, embedding_dim)
VALUES (1, 1, 'nomic-embed-text', 768);

-- ============================================================================
-- updated_at maintenance triggers
-- ============================================================================

CREATE TRIGGER trg_documents_updated_at
AFTER UPDATE ON documents FOR EACH ROW
BEGIN
    UPDATE documents SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = NEW.id;
END;

CREATE TRIGGER trg_bookmarks_updated_at
AFTER UPDATE ON bookmarks FOR EACH ROW
BEGIN
    UPDATE bookmarks SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = NEW.id;
END;

CREATE TRIGGER trg_jobs_updated_at
AFTER UPDATE ON jobs FOR EACH ROW
BEGIN
    UPDATE jobs SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = NEW.id;
END;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS trg_jobs_updated_at;
DROP TRIGGER IF EXISTS trg_bookmarks_updated_at;
DROP TRIGGER IF EXISTS trg_documents_updated_at;
DROP TRIGGER IF EXISTS documents_current_extraction_fk;

DROP TABLE IF EXISTS schema_meta;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS bookmarks;
DROP TABLE IF EXISTS chunks_vec;
DROP TABLE IF EXISTS chunks_fts;
DROP TABLE IF EXISTS chunks;
DROP TABLE IF EXISTS document_extractions;
DROP TABLE IF EXISTS documents;

-- +goose StatementEnd
