-- +goose Up
-- +goose StatementBegin

-- The Library's Date saved order lists saves, newest saved first:
-- Bookmarks.List in saved order walks idx_bookmarks_tenant_saved in
-- (saved_at, id) order from the cursor and stops at its limit, checking
-- every filter on the rows it reads, as the created order walks
-- idx_bookmarks_tenant_created.
--
-- idx_bookmarks_document gains (saved_at, id) for the reads of one
-- document's bookmarks newest saved first: ListByDocument (the Document
-- page's Bookmarks panel), and the title of an untitled document's newest
-- titled bookmark in ListWithLastError and GetWithLastError. With
-- document_id alone, SQLite (curio never runs ANALYZE) prefers walking the
-- new tenant index through every bookmark in saved order over seeking the
-- document and sorting its few rows. document_id stays the first column,
-- so TagsForDocument, the folder and search source filters' EXISTS, and the
-- foreign-key action when a document is deleted keep their seek. The plans
-- are pinned in internal/store/sqlite/plans_test.go.
--
-- Creating and dropping indexes rebuilds no table, so this runs inside
-- goose's transaction.

CREATE INDEX idx_bookmarks_tenant_saved ON bookmarks(tenant_id, saved_at, id);
DROP INDEX idx_bookmarks_document;
CREATE INDEX idx_bookmarks_document ON bookmarks(document_id, saved_at, id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- idx_bookmarks_document comes back with the text 002 created it with, so
-- the schema is exactly what it was.

DROP INDEX idx_bookmarks_document;
CREATE INDEX idx_bookmarks_document      ON bookmarks(document_id);
DROP INDEX idx_bookmarks_tenant_saved;

-- +goose StatementEnd
