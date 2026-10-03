-- +goose Up
-- +goose StatementBegin

-- Interests in two levels, with identities that outlive runs. Replaces 004's
-- cluster_runs, clusters and cluster_documents, which held one flat
-- partition per run under IDs that lasted that run alone. Today's run is
-- dropped, not converted: nothing in its shape could seed a warm start or
-- be carried over, and the daemon's first rebuild after this migration
-- groups the library afresh.
--
-- Model:
--   * interest_runs: one rebuild attempt, running → done | failed. What
--     started it (trigger), whether it started from the previous grouping
--     (kind) and ran the split check, the shape it produced, the grouper and
--     its params, when it read the vectors, the mean it centered them on,
--     and its outcome: the counts it found and what it did to identities.
--     A done run never changes. The current grouping is the tenant's
--     latest done run.
--   * interests: identities, areas and interests alike. An identity
--     outlives the runs that hold it, and carries the label and summary;
--     a run that no longer holds it retires it (retired_at, retired_run_id),
--     and retired identities are kept 180 days for their lineage.
--   * interest_groups: an identity as one run found it, with its parent
--     area, size, loose fits, cohesion and centroid.
--   * interest_assignments: every document a run grouped, once: a member
--     or a loose fit of an interest, or unsorted with its nearest interest;
--     and the seeds the next run's warm start reads.
--   * interest_placements: documents placed between rebuilds. They go with
--     their run.
--   * interest_lineage: what each run did to an old identity, toward each
--     new one: kept, moved, split or merged. It outlives runs.
--   * insight_state: per tenant, what a rebuild can't derive from runs.
--
-- Run-scoped tables (groups, assignments, placements) inherit their tenant
-- through interest_runs; runs and identities carry tenant_id. Vectors (the
-- mean, centroids) are float32 little-endian BLOBs, as decodeVector reads.
--
-- Departures from the design's sketch, each with its reason:
--   * interest_assignments.area_id: the area whose community holds the
--     document, NULL in the flat shape and outside every area. Carry-over
--     matches areas by area-community membership, which interest_id and
--     the groups' parent_id can't rebuild for a document in an area but in
--     no interest.
--   * interests.retired_run_id, with idx_interests_retired on (tenant_id,
--     retired_run_id) for retired identities: the changes a run made, and
--     the 410 a retired ID answers, name the run that retired it, which
--     retired_at alone doesn't say. It has no foreign key: runs are pruned.
--   * interests.created_run_id is NOT NULL: every identity is minted by a
--     run, and a run's new identities are read through it.
--   * interests.label_source accepts 'user' now, though nothing writes it
--     until renames: four tables reference interests, so widening the CHECK
--     later would mean rebuilding it by the recipe in README.md.
--   * idx_interest_assignments_document and idx_interest_placements_document:
--     deleting a document cascades to its rows, and without them each
--     delete scans both tables, as idx_cluster_documents_document spared
--     cluster_documents.
--   * CHECKs tie fit to interest_id (unsorted exactly when it has none) and
--     keep nearest_id to unsorted rows.
--   * insight_state has no shape column: the current done run's shape is
--     the state the grouper reads, and two copies could disagree.
--
-- Nothing outside the three cluster tables references them, and each
-- DROP's implicit DELETE runs no ON DELETE action elsewhere, so this runs
-- inside goose's transaction.

DROP TABLE cluster_documents;
DROP TABLE clusters;
DROP TABLE cluster_runs;

CREATE TABLE interest_runs (
    id                  TEXT    PRIMARY KEY,
    tenant_id           TEXT    NOT NULL,
    status              TEXT    NOT NULL DEFAULT 'running'
                                CHECK (status IN ('running','done','failed')),
    trigger             TEXT    NOT NULL
                                CHECK (trigger IN ('first','auto','manual','reindex','params','shape')),
    kind                TEXT    NOT NULL CHECK (kind IN ('fresh','warm')),
    split_check         INTEGER NOT NULL CHECK (split_check IN (0,1)),
    shape               TEXT    NOT NULL CHECK (shape IN ('flat','areas')),
    grouper             TEXT    NOT NULL,              -- the grouper's name, e.g. 'louvain'
    params              TEXT    CHECK (params IS NULL OR json_valid(params)),
    vectors_read_at     TEXT,                          -- when the run read the vectors
    mean                BLOB,                          -- the centering mean, float32 LE; NULL when not centered
    num_documents       INTEGER NOT NULL DEFAULT 0,
    num_areas           INTEGER NOT NULL DEFAULT 0,
    num_interests       INTEGER NOT NULL DEFAULT 0,
    num_loose           INTEGER NOT NULL DEFAULT 0,
    num_unsorted        INTEGER NOT NULL DEFAULT 0,
    changed_documents   INTEGER NOT NULL DEFAULT 0,    -- documents added or gone since the previous run
    changes_since_split INTEGER NOT NULL DEFAULT 0,    -- changes absorbed since the last split check
    kept                INTEGER NOT NULL DEFAULT 0,    -- interest identities carried over (moved included)
    created             INTEGER NOT NULL DEFAULT 0,
    split               INTEGER NOT NULL DEFAULT 0,
    merged              INTEGER NOT NULL DEFAULT 0,
    moved               INTEGER NOT NULL DEFAULT 0,
    dissolved           INTEGER NOT NULL DEFAULT 0,
    error               TEXT,                          -- set when status='failed'
    started_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    finished_at         TEXT,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- The latest done run of a tenant, read by every read of interests.
CREATE INDEX idx_interest_runs_tenant_status ON interest_runs(tenant_id, status, started_at DESC);

CREATE TABLE interests (
    id             TEXT    PRIMARY KEY,
    tenant_id      TEXT    NOT NULL,
    level          TEXT    NOT NULL CHECK (level IN ('area','interest')),
    label          TEXT,                               -- NULL until labeled
    summary        TEXT,
    label_source   TEXT    CHECK (label_source IN ('llm','terms','user')),
    created_run_id TEXT    NOT NULL,                   -- the run that minted it; no FK: runs are pruned
    labeled_at     TEXT,
    retired_at     TEXT,                               -- set by the run that no longer holds it
    retired_run_id TEXT,                               -- that run; no FK: runs are pruned
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- The identities a run retired, and the retired ones past their retention.
CREATE INDEX idx_interests_retired ON interests(tenant_id, retired_run_id) WHERE retired_at IS NOT NULL;

CREATE TABLE interest_groups (
    run_id      TEXT    NOT NULL REFERENCES interest_runs(id) ON DELETE CASCADE,
    interest_id TEXT    NOT NULL REFERENCES interests(id),
    parent_id   TEXT    REFERENCES interests(id),      -- an interest's area; NULL for areas and flat interests
    size        INTEGER NOT NULL,                      -- members
    loose       INTEGER NOT NULL,                      -- loose fits
    cohesion    REAL    NOT NULL,
    centroid    BLOB,                                  -- members' unit mean, float32 LE; interests only
    PRIMARY KEY (run_id, interest_id)
);

-- A run's top-level groups, and an area's interests, largest first.
CREATE INDEX idx_interest_groups_list ON interest_groups(run_id, parent_id, size DESC, cohesion DESC, interest_id);

CREATE TABLE interest_assignments (
    run_id        TEXT    NOT NULL REFERENCES interest_runs(id) ON DELETE CASCADE,
    document_id   TEXT    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    interest_id   TEXT    REFERENCES interests(id),    -- NULL: unsorted
    area_id       TEXT    REFERENCES interests(id),    -- the area whose community holds it; NULL outside every area
    fit           TEXT    NOT NULL CHECK (fit IN ('member','loose','unsorted')),
    similarity    REAL    NOT NULL,                    -- to its interest; for unsorted, to the nearest
    nearest_id    TEXT    REFERENCES interests(id),    -- unsorted only; NULL when there is no interest
    area_seed     INTEGER NOT NULL,                    -- warm-start seeds, -1 for none
    interest_seed INTEGER NOT NULL,
    PRIMARY KEY (run_id, document_id),
    CHECK ((fit = 'unsorted') = (interest_id IS NULL)),
    CHECK (fit = 'unsorted' OR nearest_id IS NULL)
);

-- An interest's members, then its loose fits, and the unsorted, each most
-- similar first.
CREATE INDEX idx_interest_assignments_list
    ON interest_assignments(run_id, interest_id, fit, similarity DESC, document_id);
CREATE INDEX idx_interest_assignments_document ON interest_assignments(document_id);

CREATE TABLE interest_placements (
    run_id      TEXT    NOT NULL REFERENCES interest_runs(id) ON DELETE CASCADE,
    document_id TEXT    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    interest_id TEXT    REFERENCES interests(id),      -- NULL: unsorted
    similarity  REAL    NOT NULL,
    placed_at   TEXT    NOT NULL,
    PRIMARY KEY (run_id, document_id)
);

-- An interest's placements, or the unsorted ones, newest first.
CREATE INDEX idx_interest_placements_list ON interest_placements(run_id, interest_id, placed_at DESC);
CREATE INDEX idx_interest_placements_document ON interest_placements(document_id);

CREATE TABLE interest_lineage (
    run_id TEXT    NOT NULL,                           -- no FK: runs are pruned
    old_id TEXT    NOT NULL REFERENCES interests(id) ON DELETE CASCADE,
    new_id TEXT    NOT NULL REFERENCES interests(id) ON DELETE CASCADE,
    event  TEXT    NOT NULL CHECK (event IN ('kept','split','merged','moved')),
    shared INTEGER NOT NULL,                           -- the old identity's members the new one holds
    PRIMARY KEY (run_id, old_id, new_id)
);

CREATE TABLE insight_state (
    tenant_id       TEXT    PRIMARY KEY,
    fresh_owed      TEXT    CHECK (fresh_owed IN ('reindex','params','shape')),
    failures        INTEGER NOT NULL DEFAULT 0,
    last_failure_at TEXT,
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- 004's tables come back empty, with the text 004 created them with and
-- without the triggers 006 dropped, so the schema is exactly 015's.

DROP TABLE interest_placements;
DROP TABLE interest_assignments;
DROP TABLE interest_groups;
DROP TABLE interest_lineage;
DROP TABLE insight_state;
DROP TABLE interests;
DROP TABLE interest_runs;

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

CREATE INDEX idx_clusters_run ON clusters(run_id, size DESC);

CREATE TABLE cluster_documents (
    cluster_id  TEXT    NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    document_id TEXT    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    similarity  REAL    NOT NULL DEFAULT 0,        -- cosine to the cluster medoid, 0..1
    PRIMARY KEY (cluster_id, document_id)
);

CREATE INDEX idx_cluster_documents_document ON cluster_documents(document_id);

-- +goose StatementEnd
