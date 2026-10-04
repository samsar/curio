-- +goose Up
-- +goose StatementBegin

-- The interest map (docs/decisions.md, "Interest map: two views of each
-- regrouping, drawn when it is built"): every done run draws two views of
-- its grouping, and keeps them on the rows it already has.
--
--   * interest_runs: the map's status, NULL when the run drew none (a run
--     from before this migration, or one not done), 'built' or 'failed'; for
--     a built map its kind (fresh or warm), how long it took, its params, the
--     dots' radius and Unsorted's disc in the zoom view; for a failed one its
--     error. A CHECK ties them together, so a run is never half a map.
--   * interest_groups: each area's and interest's circle in the zoom view and
--     its label's anchor on the document map, all or none; and an interest's
--     three most similar interests, a JSON list of {"id", "cosine"}.
--   * interest_assignments and interest_placements: each document's place on
--     the document map (map_x, map_y) and in the zoom view (zoom_x, zoom_y),
--     all or none.
--
-- Columns on the rows that exist per document, rather than a table of
-- their own: CommitRun writes each assignment once, PlaceDocument stays one
-- guarded upsert, the run's cascade prunes them, and a document's delete
-- seeks the indexes it already does.
--
-- Adding columns rebuilds nothing, so this runs inside goose's transaction.
-- A CHECK that names other columns sits on the last of them: SQLite drops a
-- column only when no other column's CHECK names it, so Down drops them in
-- the reverse order, which leaves sqlite_master as 017 left it.

ALTER TABLE interest_runs ADD COLUMN map_status TEXT CHECK (map_status IN ('built','failed'));
ALTER TABLE interest_runs ADD COLUMN map_kind TEXT CHECK (map_kind IN ('fresh','warm'));
ALTER TABLE interest_runs ADD COLUMN map_error TEXT;
ALTER TABLE interest_runs ADD COLUMN map_ms INTEGER;
ALTER TABLE interest_runs ADD COLUMN map_params TEXT CHECK (map_params IS NULL OR json_valid(map_params));
ALTER TABLE interest_runs ADD COLUMN map_dot_radius REAL;
ALTER TABLE interest_runs ADD COLUMN map_unsorted_x REAL;
ALTER TABLE interest_runs ADD COLUMN map_unsorted_y REAL;
ALTER TABLE interest_runs ADD COLUMN map_unsorted_r REAL CHECK (
    (map_status IS NULL AND map_kind IS NULL AND map_error IS NULL AND map_ms IS NULL AND map_params IS NULL
        AND map_dot_radius IS NULL AND map_unsorted_x IS NULL AND map_unsorted_y IS NULL AND map_unsorted_r IS NULL)
    OR (map_status = 'failed' AND map_kind IS NULL AND map_error IS NOT NULL AND map_ms IS NOT NULL
        AND map_params IS NOT NULL AND map_dot_radius IS NULL AND map_unsorted_x IS NULL AND map_unsorted_y IS NULL
        AND map_unsorted_r IS NULL)
    OR (map_status = 'built' AND map_kind IS NOT NULL AND map_error IS NULL AND map_ms IS NOT NULL
        AND map_params IS NOT NULL AND map_dot_radius > 0 AND map_unsorted_x IS NOT NULL
        AND map_unsorted_y IS NOT NULL AND map_unsorted_r > 0));

ALTER TABLE interest_groups ADD COLUMN zoom_x REAL;
ALTER TABLE interest_groups ADD COLUMN zoom_y REAL;
ALTER TABLE interest_groups ADD COLUMN zoom_r REAL;
ALTER TABLE interest_groups ADD COLUMN anchor_x REAL;
ALTER TABLE interest_groups ADD COLUMN anchor_y REAL CHECK (
    (zoom_x IS NULL AND zoom_y IS NULL AND zoom_r IS NULL AND anchor_x IS NULL AND anchor_y IS NULL)
    OR (zoom_x IS NOT NULL AND zoom_y IS NOT NULL AND zoom_r > 0 AND anchor_x IS NOT NULL AND anchor_y IS NOT NULL));
ALTER TABLE interest_groups ADD COLUMN similar TEXT CHECK (similar IS NULL OR json_valid(similar));

ALTER TABLE interest_assignments ADD COLUMN map_x REAL;
ALTER TABLE interest_assignments ADD COLUMN map_y REAL;
ALTER TABLE interest_assignments ADD COLUMN zoom_x REAL;
ALTER TABLE interest_assignments ADD COLUMN zoom_y REAL CHECK (
    (map_x IS NULL AND map_y IS NULL AND zoom_x IS NULL AND zoom_y IS NULL)
    OR (map_x IS NOT NULL AND map_y IS NOT NULL AND zoom_x IS NOT NULL AND zoom_y IS NOT NULL));

ALTER TABLE interest_placements ADD COLUMN map_x REAL;
ALTER TABLE interest_placements ADD COLUMN map_y REAL;
ALTER TABLE interest_placements ADD COLUMN zoom_x REAL;
ALTER TABLE interest_placements ADD COLUMN zoom_y REAL CHECK (
    (map_x IS NULL AND map_y IS NULL AND zoom_x IS NULL AND zoom_y IS NULL)
    OR (map_x IS NOT NULL AND map_y IS NOT NULL AND zoom_x IS NOT NULL AND zoom_y IS NOT NULL));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- The columns go in the reverse order they came, the one whose CHECK names
-- the others first, which leaves the schema exactly 017's.

ALTER TABLE interest_placements DROP COLUMN zoom_y;
ALTER TABLE interest_placements DROP COLUMN zoom_x;
ALTER TABLE interest_placements DROP COLUMN map_y;
ALTER TABLE interest_placements DROP COLUMN map_x;

ALTER TABLE interest_assignments DROP COLUMN zoom_y;
ALTER TABLE interest_assignments DROP COLUMN zoom_x;
ALTER TABLE interest_assignments DROP COLUMN map_y;
ALTER TABLE interest_assignments DROP COLUMN map_x;

ALTER TABLE interest_groups DROP COLUMN similar;
ALTER TABLE interest_groups DROP COLUMN anchor_y;
ALTER TABLE interest_groups DROP COLUMN anchor_x;
ALTER TABLE interest_groups DROP COLUMN zoom_r;
ALTER TABLE interest_groups DROP COLUMN zoom_y;
ALTER TABLE interest_groups DROP COLUMN zoom_x;

ALTER TABLE interest_runs DROP COLUMN map_unsorted_r;
ALTER TABLE interest_runs DROP COLUMN map_unsorted_y;
ALTER TABLE interest_runs DROP COLUMN map_unsorted_x;
ALTER TABLE interest_runs DROP COLUMN map_dot_radius;
ALTER TABLE interest_runs DROP COLUMN map_params;
ALTER TABLE interest_runs DROP COLUMN map_ms;
ALTER TABLE interest_runs DROP COLUMN map_error;
ALTER TABLE interest_runs DROP COLUMN map_kind;
ALTER TABLE interest_runs DROP COLUMN map_status;

-- +goose StatementEnd
