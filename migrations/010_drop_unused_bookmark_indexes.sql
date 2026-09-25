-- +goose Up
-- +goose StatementBegin

-- Drop the two bookmark indexes no query reads. By EXPLAIN QUERY PLAN,
-- Bookmarks.List walks idx_bookmarks_tenant_created under every filter,
-- Count scans that index as a covering index, TagsForDocument and the search
-- source filter use idx_bookmarks_document, and Get, Delete and LinkDocument
-- go by the primary key. Nothing uses idx_bookmarks_tenant_source or
-- idx_bookmarks_folder, yet every bookmark insert and every source or folder
-- update maintained them. The plans are pinned in
-- internal/store/sqlite/plans_test.go.
--
-- Dropping an index rebuilds no table, so this runs inside goose's
-- transaction.

DROP INDEX idx_bookmarks_tenant_source;
DROP INDEX idx_bookmarks_folder;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- The indexes come back with the text 002 created them with, so the schema
-- is exactly what it was.

CREATE INDEX idx_bookmarks_tenant_source ON bookmarks(tenant_id, source);
CREATE INDEX idx_bookmarks_folder        ON bookmarks(tenant_id, folder_path);

-- +goose StatementEnd
