-- +goose Up
-- +goose StatementBegin

-- Insight layer (M4): clustering documents by embedding similarity into
-- labeled topic "interests".
--
-- Model:
--   * A `cluster_runs` row is one execution of the clustering job. Clustering
--     fully recomputes from the current corpus; each run is a snapshot. The
--     "current" interests are the clusters of the latest run with status='done'.
--     Old runs are kept (cheap) so a later milestone can do trajectory analysis
--     ("what's new this month"); a prune step can trim them if they pile up.
--   * A `clusters` row is one topic within a run: a label + summary + size +
--     cohesion (mean member similarity to the cluster medoid).
--   * `cluster_documents` links a cluster to its member documents. Documents
--     that clustering classified as noise simply have no membership row.
--
-- Conventions match migration 001: TEXT UUIDs, RFC 3339 UTC TEXT timestamps,
-- JSON columns validated with CHECK(json_valid(...)), FK cascade, tenant_id on
-- the top-level tables (runs, clusters) while the join table inherits tenant
-- scope through its parent cluster. PRAGMA foreign_keys is set per-connection
-- by the daemon's DSN, so these cascades fire.

CREATE TABLE cluster_runs (
    id             TEXT    PRIMARY KEY,
    tenant_id      TEXT    NOT NULL,
    status         TEXT    NOT NULL DEFAULT 'running'
                           CHECK (status IN ('running','done','failed')),
    algo           TEXT    NOT NULL,               -- clusterer name, e.g. 'knn-graph'
    params         TEXT    CHECK (params IS NULL OR json_valid(params)),
    num_documents  INTEGER NOT NULL DEFAULT 0,     -- docs considered (had vectors)
    num_clusters   INTEGER NOT NULL DEFAULT 0,
    num_noise      INTEGER NOT NULL DEFAULT 0,     -- docs left unclustered
    error          TEXT,                           -- set when status='failed'
    started_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    finished_at    TEXT,
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Hot query: "latest done run for a tenant" → ORDER BY started_at DESC LIMIT 1.
CREATE INDEX idx_cluster_runs_tenant_status ON cluster_runs(tenant_id, status, started_at DESC);

CREATE TABLE clusters (
    id          TEXT    PRIMARY KEY,
    tenant_id   TEXT    NOT NULL,
    run_id      TEXT    NOT NULL REFERENCES cluster_runs(id) ON DELETE CASCADE,
    label       TEXT,                              -- topic name; NULL until labeled
    summary     TEXT,                              -- one-line description; NULL if none
    size        INTEGER NOT NULL DEFAULT 0,        -- member count (denormalized)
    cohesion    REAL    NOT NULL DEFAULT 0,        -- mean member cosine to medoid, 0..1
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Listing interests reads a run's clusters largest-first.
CREATE INDEX idx_clusters_run ON clusters(run_id, size DESC);

CREATE TABLE cluster_documents (
    cluster_id  TEXT    NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    document_id TEXT    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    similarity  REAL    NOT NULL DEFAULT 0,        -- cosine to the cluster medoid, 0..1
    PRIMARY KEY (cluster_id, document_id)
);

-- Reverse lookup: "which cluster is this document in" (drill-down / cleanup).
CREATE INDEX idx_cluster_documents_document ON cluster_documents(document_id);

-- updated_at maintenance. cluster_runs is mutated (running → done/failed with
-- counts); clusters get a trigger too for consistency. cluster_documents is a
-- write-once join table and has no updated_at.
CREATE TRIGGER trg_cluster_runs_updated_at
AFTER UPDATE ON cluster_runs FOR EACH ROW
BEGIN
    UPDATE cluster_runs SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = NEW.id;
END;

CREATE TRIGGER trg_clusters_updated_at
AFTER UPDATE ON clusters FOR EACH ROW
BEGIN
    UPDATE clusters SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = NEW.id;
END;

UPDATE schema_meta SET schema_version = 4 WHERE id = 1;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS trg_clusters_updated_at;
DROP TRIGGER IF EXISTS trg_cluster_runs_updated_at;

DROP TABLE IF EXISTS cluster_documents;
DROP TABLE IF EXISTS clusters;
DROP TABLE IF EXISTS cluster_runs;

UPDATE schema_meta SET schema_version = 3 WHERE id = 1;

-- +goose StatementEnd
