-- +goose Up
-- +goose StatementBegin

-- Keyset pages for the document, job and bookmark lists. Each list orders
-- by a timestamp and then by id, so rows that share a timestamp still have
-- one order, and a page starts strictly after the previous page's last row
-- with the row-value predicate (ts, id) < (?, ?). SQLite only turns that
-- predicate into an index range, and only serves the ORDER BY without a
-- sort, when id is the index's last column; the plans are pinned in
-- internal/store/sqlite/plans_test.go.
--
-- Dropping and creating indexes rebuilds no table, so this runs inside
-- goose's transaction.

DROP INDEX idx_documents_tenant_updated;
CREATE INDEX idx_documents_tenant_updated ON documents(tenant_id, updated_at, id);
DROP INDEX idx_documents_tenant_state_updated;
CREATE INDEX idx_documents_tenant_state_updated ON documents(tenant_id, state, updated_at, id);

DROP INDEX idx_jobs_tenant_updated;
CREATE INDEX idx_jobs_tenant_updated ON jobs(tenant_id, updated_at, id);
DROP INDEX idx_jobs_tenant_status_updated;
CREATE INDEX idx_jobs_tenant_status_updated ON jobs(tenant_id, status, updated_at, id);

-- Bookmarks page newest first by created_at, which never changes, instead
-- of by id, which for UUIDv4 is no order at all.
DROP INDEX idx_bookmarks_tenant_id;
CREATE INDEX idx_bookmarks_tenant_created ON bookmarks(tenant_id, created_at, id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- The indexes come back with the text 007 created them with, so the schema
-- is exactly what it was.

DROP INDEX idx_bookmarks_tenant_created;
CREATE INDEX idx_bookmarks_tenant_id ON bookmarks(tenant_id, id);

DROP INDEX idx_jobs_tenant_status_updated;
CREATE INDEX idx_jobs_tenant_status_updated ON jobs(tenant_id, status, updated_at);
DROP INDEX idx_jobs_tenant_updated;
CREATE INDEX idx_jobs_tenant_updated ON jobs(tenant_id, updated_at);

DROP INDEX idx_documents_tenant_state_updated;
CREATE INDEX idx_documents_tenant_state_updated ON documents(tenant_id, state, updated_at);
DROP INDEX idx_documents_tenant_updated;
CREATE INDEX idx_documents_tenant_updated ON documents(tenant_id, updated_at);

-- +goose StatementEnd
