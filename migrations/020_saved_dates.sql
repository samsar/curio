-- +goose Up
-- +goose StatementBegin

-- How far each bookmark's saved_at can be trusted (docs/decisions.md
-- "Saved dates and search filters").
--
-- dateless is the one fact only the import knows: 1 when the bookmark's
-- source gave no save date (Safari's bookmarks, an HTML anchor without
-- ADD_DATE, a browser entry without date_added), so saved_at is the time
-- the import stored it. Its default, and the Go zero value, mean dated, so
-- every insert that doesn't name it, an older daemon's after a downgrade
-- included, keeps the meaning saved_at had before this migration.
--
-- saved_date is the class the store derives from a (tenant, source)
-- group's rows (internal/docmeta SavedDates): exact, approx or unknown. A
-- row nobody has classified claims no date. The daemon classifies every
-- bookmark at start (sqlite.EnsureSavedDates) and an import's source after
-- it, through the same code; no class is computed here, which would be a
-- second implementation of the rule.
--
-- The backfill can only approximate the fact: a Safari or HTML bookmark
-- saved within 5 s of when curio stored it was stored with the import's
-- time. Like 017's, it leaves updated_at alone: the schema changed, not
-- the bookmark.
--
-- No index: the classification reads a tenant's bookmarks in saved order
-- through idx_bookmarks_tenant_saved, and the filters reach a document's
-- bookmarks through idx_bookmarks_document. Adding columns rebuilds no
-- table, so this runs inside goose's transaction.

ALTER TABLE bookmarks ADD COLUMN dateless INTEGER NOT NULL DEFAULT 0 CHECK (dateless IN (0, 1));
ALTER TABLE bookmarks ADD COLUMN saved_date TEXT NOT NULL DEFAULT 'unknown'
    CHECK (saved_date IN ('exact', 'approx', 'unknown'));

UPDATE bookmarks SET dateless = 1
WHERE source IN ('safari', 'html') AND abs(julianday(saved_at) - julianday(created_at)) * 86400 < 5;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- saved_at was never changed, so dropping the two columns gives 019's
-- bookmarks back as they were.

ALTER TABLE bookmarks DROP COLUMN saved_date;
ALTER TABLE bookmarks DROP COLUMN dateless;

-- +goose StatementEnd
