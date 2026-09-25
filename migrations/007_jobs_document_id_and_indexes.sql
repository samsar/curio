-- +goose Up
-- +goose StatementBegin

-- jobs.document_id: the document a fetch or index job works on, as a real
-- column. The payload keeps document_id too (the API returns payloads
-- verbatim); this column is a projection of it, written once at insert by
-- the same json_extract rule the backfill below uses. Reads join on it
-- instead of json_extract, which no index can serve.
--
-- ON DELETE SET NULL, not CASCADE: jobs are the audit trail (last_error,
-- the durations metrics read), so a deleted document's jobs stay listed
-- with no document, as they already did. A payload naming a document that
-- no longer exists backfills to NULL, the same state.
--
-- ADD COLUMN with a REFERENCES clause and a NULL default needs no table
-- rebuild, so this runs inside goose's transaction.
ALTER TABLE jobs ADD COLUMN document_id TEXT REFERENCES documents(id) ON DELETE SET NULL;

UPDATE jobs SET document_id = d.id
FROM documents d
WHERE d.id = json_extract(jobs.payload, '$.document_id');

-- The index set is built around the queries the store runs; the test in
-- internal/store/sqlite/plans_test.go pins which query uses which. curio
-- never runs ANALYZE, so the plans come from SQLite's heuristics.

-- The last error of a document (curio docs), and the FK action when a
-- document is deleted.
CREATE INDEX idx_jobs_document ON jobs(document_id, status, updated_at);

-- ClaimNext: one seek per kind, taking jobs in the order they became
-- runnable, with no sort. Replaces idx_jobs_dispatch, which left the
-- claim sorting every runnable job under the write lock.
DROP INDEX idx_jobs_dispatch;
CREATE INDEX idx_jobs_claim ON jobs(status, kind, run_after, created_at);

-- Job lists filtered by status, retention, metrics windows and counts.
-- Replaces idx_jobs_kind, which none of those could walk in order.
DROP INDEX idx_jobs_kind;
CREATE INDEX idx_jobs_tenant_status_updated ON jobs(tenant_id, status, updated_at);
-- Job lists with no status filter.
CREATE INDEX idx_jobs_tenant_updated ON jobs(tenant_id, updated_at);

-- Document lists filtered by state, counts, and the per-state scans.
DROP INDEX idx_documents_tenant_state;
CREATE INDEX idx_documents_tenant_state_updated ON documents(tenant_id, state, updated_at);
-- Document lists with no state filter.
CREATE INDEX idx_documents_tenant_updated ON documents(tenant_id, updated_at);

-- No query reads these; each cost a write on every document update.
DROP INDEX idx_documents_tenant_ctype;
DROP INDEX idx_documents_url_canonical;

-- Bookmark pages: WHERE tenant_id = ? AND id > ? ORDER BY id.
CREATE INDEX idx_bookmarks_tenant_id ON bookmarks(tenant_id, id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- The indexes come back with the text 001 created them with, spacing
-- included, so the schema is exactly what it was.

DROP INDEX idx_bookmarks_tenant_id;

CREATE INDEX idx_documents_url_canonical   ON documents(url_canonical);
CREATE INDEX idx_documents_tenant_ctype    ON documents(tenant_id, content_type);

DROP INDEX idx_documents_tenant_updated;
DROP INDEX idx_documents_tenant_state_updated;
CREATE INDEX idx_documents_tenant_state    ON documents(tenant_id, state);

DROP INDEX idx_jobs_tenant_updated;
DROP INDEX idx_jobs_tenant_status_updated;
CREATE INDEX idx_jobs_kind     ON jobs(tenant_id, kind, status);

DROP INDEX idx_jobs_claim;
CREATE INDEX idx_jobs_dispatch ON jobs(status, run_after, created_at);

DROP INDEX idx_jobs_document;
ALTER TABLE jobs DROP COLUMN document_id;

-- +goose StatementEnd
