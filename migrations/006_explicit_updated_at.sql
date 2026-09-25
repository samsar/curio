-- +goose Up
-- +goose StatementBegin

-- updated_at is written by each UPDATE statement from now on, not by these
-- triggers. Each trigger issued a second UPDATE of the row, doubling the
-- cost of every update, and RETURNING reports the row before a trigger
-- runs, so every UPDATE ... RETURNING handed back a stale updated_at.

DROP TRIGGER trg_documents_updated_at;
DROP TRIGGER trg_bookmarks_updated_at;
DROP TRIGGER trg_jobs_updated_at;
DROP TRIGGER trg_cluster_runs_updated_at;
DROP TRIGGER trg_clusters_updated_at;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

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

-- +goose StatementEnd
