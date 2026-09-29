package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
)

type Bookmarks struct {
	db *DB
}

var _ store.BookmarkStore = (*Bookmarks)(nil)

// tagsForDocumentSQL reads the tags of a document's bookmarks. Its args are
// the tenant and the document.
const tagsForDocumentSQL = `SELECT tags FROM bookmarks WHERE tenant_id = ? AND document_id = ?`

// TagsForDocument returns the deduplicated tags across all bookmarks that
// reference the document, scoped to the tenant. Order is first-seen.
// Malformed tag JSON on a row is skipped rather than failing the whole call.
func (s *Bookmarks) TagsForDocument(ctx context.Context, tenantID, documentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, tagsForDocumentSQL, tenantID, documentID)
	if err != nil {
		return nil, fmt.Errorf("tags for document: %w", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var tagsJSON sql.NullString
		if err := rows.Scan(&tagsJSON); err != nil {
			return nil, fmt.Errorf("scan tags: %w", err)
		}
		if !tagsJSON.Valid || tagsJSON.String == "" {
			continue
		}
		var tags []string
		if err := json.Unmarshal([]byte(tagsJSON.String), &tags); err != nil {
			continue // tolerate a single malformed row
		}
		for _, t := range tags {
			t = strings.TrimSpace(t)
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func NewBookmarks(db *DB) *Bookmarks { return &Bookmarks{db: db} }

func (s *Bookmarks) Ingest(ctx context.Context, b *store.Bookmark) (store.IngestResult, error) {
	if err := validateBookmark(b); err != nil {
		return store.IngestResult{}, err
	}
	tags, err := encodeTags(b.Tags)
	if err != nil {
		return store.IngestResult{}, err
	}
	id := b.ID
	if id == "" {
		id = uuid.NewString()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.IngestResult{}, fmt.Errorf("begin ingest: %w", err)
	}
	defer tx.Rollback()

	docID, state, created, err := getOrCreateDocument(ctx, tx, b.TenantID, b.URL)
	if err != nil {
		return store.IngestResult{}, err
	}

	var createdAt, updatedAt string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO bookmarks (id, tenant_id, document_id, url, title, saved_at, source, folder_path, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, url, source) DO NOTHING
		RETURNING created_at, updated_at`,
		id, b.TenantID, docID, b.URL,
		strPtr(b.Title), formatTime(b.SavedAt), b.Source,
		strPtr(b.FolderPath), tags,
	).Scan(&createdAt, &updatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// The deferred Rollback discards the document insert too.
		return store.IngestResult{}, fmt.Errorf("%w: bookmark for (tenant, url, source) exists", store.ErrConflict)
	case isUniqueViolation(err):
		return store.IngestResult{}, fmt.Errorf("bookmark %s: %w", id, store.ErrConflict)
	case err != nil:
		return store.IngestResult{}, fmt.Errorf("insert bookmark: %w", err)
	}
	createdTime, err := parseTime(createdAt)
	if err != nil {
		return store.IngestResult{}, err
	}
	updatedTime, err := parseTime(updatedAt)
	if err != nil {
		return store.IngestResult{}, err
	}

	res := store.IngestResult{DocumentState: state, DocumentCreated: created}
	// Only a new document needs a fetch. An existing pending document
	// already has its fetch or index job queued, and a failed or dead one is
	// left to `curio refetch`.
	var wake []store.JobKind
	if created {
		if res.FetchJob, err = store.NewDocumentJob(b.TenantID, store.JobKindFetch, docID); err != nil {
			return store.IngestResult{}, err
		}
		if err := insertJob(ctx, tx, res.FetchJob); err != nil {
			return store.IngestResult{}, err
		}
		wake = append(wake, store.JobKindFetch)
	}
	if err := s.db.commitNotify(tx, wake...); err != nil {
		return store.IngestResult{}, fmt.Errorf("commit ingest: %w", err)
	}

	b.ID = id
	b.DocumentID = &docID
	b.CreatedAt = createdTime
	b.UpdatedAt = updatedTime
	return res, nil
}

func (s *Bookmarks) Create(ctx context.Context, b *store.Bookmark) error {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	if err := validateBookmark(b); err != nil {
		return err
	}
	tagsJSON, err := encodeTags(b.Tags)
	if err != nil {
		return err
	}

	var createdAt, updatedAt string
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO bookmarks (id, tenant_id, document_id, url, title, saved_at, source, folder_path, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING created_at, updated_at`,
		b.ID, b.TenantID,
		strPtr(b.DocumentID), b.URL,
		strPtr(b.Title), formatTime(b.SavedAt), b.Source,
		strPtr(b.FolderPath), tagsJSON,
	).Scan(&createdAt, &updatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: bookmark for (tenant, url, source) exists", store.ErrConflict)
		}
		return fmt.Errorf("insert bookmark: %w", err)
	}
	if b.CreatedAt, err = parseTime(createdAt); err != nil {
		return err
	}
	b.UpdatedAt, err = parseTime(updatedAt)
	return err
}

// validateBookmark checks the fields every bookmark insert requires.
func validateBookmark(b *store.Bookmark) error {
	switch {
	case b.TenantID == "":
		return errors.New("bookmarks: tenant_id required")
	case b.URL == "":
		return errors.New("bookmarks: url required")
	case b.Source == "":
		return errors.New("bookmarks: source required")
	case b.SavedAt.IsZero():
		return errors.New("bookmarks: saved_at required")
	}
	return nil
}

func (s *Bookmarks) GetByID(ctx context.Context, id string) (*store.Bookmark, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+bookmarkColumns+" FROM bookmarks WHERE id = ?", id)
	return scanBookmark(row)
}

// List reads what each bookmark's row shows of its document through a
// join, so a page is one query however many bookmarks it holds.
func (s *Bookmarks) List(ctx context.Context, tenantID string, opts store.ListBookmarksOpts) ([]store.BookmarkWithDocument, error) {
	q, args, err := listBookmarksQuery(tenantID, opts)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	defer rows.Close()

	var out []store.BookmarkWithDocument
	for rows.Next() {
		item, err := scanBookmarkWithDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("list bookmarks: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	return out, nil
}

// scanBookmarkWithDocument scans a row of listBookmarksQuery.
func scanBookmarkWithDocument(row interface{ Scan(...any) error }) (store.BookmarkWithDocument, error) {
	var (
		item  store.BookmarkWithDocument
		title sql.NullString
		err   error
	)
	item.Bookmark, err = scanBookmark(row, &item.DocumentState, &title, &item.DocumentContentType,
		&item.DocumentFailureCause, &item.DocumentLastError)
	if err != nil {
		return store.BookmarkWithDocument{}, err
	}
	item.DocumentTitle = nullableString(title)
	return item, nil
}

// bookmarkOrderColumns are the timestamps each BookmarkOrder lists by,
// newest first: those of idx_bookmarks_tenant_created and
// idx_bookmarks_tenant_saved, each followed by id, which ends the index.
var bookmarkOrderColumns = map[store.BookmarkOrder]string{
	store.BookmarkOrderCreated: "b.created_at",
	store.BookmarkOrderSaved:   "b.saved_at",
}

// listBookmarksQuery builds List's query, or refuses an order outside the
// BookmarkOrder constants. A page walks its order's index in (timestamp,
// id) order from opts.After, checking every filter on each row it reads,
// so it reads about one page of rows unless a filter matches few. Source,
// folder and host are the bookmark's, the host on its URL, which is its
// document's (ingest keys the document by it), so a row they reject costs
// no document lookup. State, type and cause are the document's, reached by
// its primary key, and the last error, as ListWithLastError reads it, runs
// only for the rows returned.
func listBookmarksQuery(tenantID string, opts store.ListBookmarksOpts) (string, []any, error) {
	order := cmp.Or(opts.Order, store.BookmarkOrderCreated)
	ts, ok := bookmarkOrderColumns[order]
	if !ok {
		return "", nil, fmt.Errorf("unknown order %q", opts.Order)
	}
	clauses := []string{"b.tenant_id = ?"}
	args := []any{store.JobStatusFailed, tenantID}
	if opts.Source != "" {
		clauses = append(clauses, "b.source = ?")
		args = append(args, opts.Source)
	}
	if cond, condArgs, ok := folderPredicate("b.folder_path", opts.FolderPath); ok {
		clauses = append(clauses, cond)
		args = append(args, condArgs...)
	}
	if opts.Host != "" {
		cond, condArgs := hostPredicate("b.url", opts.Host)
		clauses = append(clauses, cond)
		args = append(args, condArgs...)
	}
	if opts.State != "" {
		clauses = append(clauses, "d.state = ?")
		args = append(args, opts.State)
	}
	if opts.ContentType != "" {
		clauses = append(clauses, "d.content_type = ?")
		args = append(args, opts.ContentType)
	}
	if opts.Cause != "" {
		clauses = append(clauses, "d.failure_cause = ?")
		args = append(args, opts.Cause)
	}
	if !opts.After.IsZero() {
		pred, predArgs := keysetAfter(ts, "b.id", opts.After)
		clauses = append(clauses, pred)
		args = append(args, predArgs...)
	}
	q := "SELECT " + qualify("b", bookmarkColumns) + ", COALESCE(d.state, ''), d.title," +
		" COALESCE(d.content_type, ''), COALESCE(d.failure_cause, ''), " + lastErrorSQL +
		" FROM bookmarks b LEFT JOIN documents d ON d.id = b.document_id" +
		" WHERE " + strings.Join(clauses, " AND ") +
		" ORDER BY " + ts + " DESC, b.id DESC LIMIT ?"
	return q, append(args, listLimit(opts.Limit)), nil
}

// listBookmarksByDocumentSQL reads a document's bookmarks. Its args are the
// tenant and the document. It seeks idx_bookmarks_document, which holds a
// document's bookmarks in (saved_at, id) order, so it reads them in the
// order it returns them.
const listBookmarksByDocumentSQL = "SELECT " + bookmarkColumns +
	" FROM bookmarks WHERE tenant_id = ? AND document_id = ? ORDER BY saved_at DESC, id DESC"

func (s *Bookmarks) ListByDocument(ctx context.Context, tenantID, documentID string) ([]*store.Bookmark, error) {
	rows, err := s.db.QueryContext(ctx, listBookmarksByDocumentSQL, tenantID, documentID)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks of document %s: %w", documentID, err)
	}
	defer rows.Close()
	var out []*store.Bookmark
	for rows.Next() {
		b, err := scanBookmark(rows)
		if err != nil {
			return nil, fmt.Errorf("list bookmarks of document %s: %w", documentID, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bookmarks of document %s: %w", documentID, err)
	}
	return out, nil
}

// countBookmarksSQL counts a tenant's bookmarks. Its arg is the tenant.
const countBookmarksSQL = `SELECT count(*) FROM bookmarks WHERE tenant_id = ?`

func (s *Bookmarks) Count(ctx context.Context, tenantID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, countBookmarksSQL, tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count bookmarks: %w", err)
	}
	return n, nil
}

// previewIngestSQL looks each URL of a JSON array up the way Ingest
// would: the tenant's document of it, and its bookmark from the source.
// Its args are the tenant, the tenant, the source and the array. Each
// lookup is a point search of a unique index, so a library of any size
// answers ten thousand URLs in milliseconds.
const previewIngestSQL = `SELECT j.value,
	EXISTS (SELECT 1 FROM documents d WHERE d.tenant_id = ? AND d.url = j.value),
	EXISTS (SELECT 1 FROM bookmarks b WHERE b.tenant_id = ? AND b.url = j.value AND b.source = ?)
FROM json_each(?) j`

func (s *Bookmarks) PreviewIngest(ctx context.Context, tenantID, source string, urls []string) (map[string]store.IngestPreview, error) {
	out := make(map[string]store.IngestPreview, len(urls))
	if len(urls) == 0 {
		return out, nil
	}
	list, err := json.Marshal(urls)
	if err != nil {
		return nil, fmt.Errorf("preview ingest: encode urls: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, previewIngestSQL, tenantID, tenantID, source, string(list))
	if err != nil {
		return nil, fmt.Errorf("preview ingest: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var url string
		var p store.IngestPreview
		if err := rows.Scan(&url, &p.DocumentExists, &p.BookmarkExists); err != nil {
			return nil, fmt.Errorf("preview ingest: scan: %w", err)
		}
		out[url] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("preview ingest: %w", err)
	}
	return out, nil
}

func (s *Bookmarks) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete bookmark: %w", err)
	}
	return ensureRow(res, "bookmark")
}

func (s *Bookmarks) LinkDocument(ctx context.Context, bookmarkID, documentID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE bookmarks SET document_id = ?, updated_at = `+sqlNow+` WHERE id = ?`,
		documentID, bookmarkID)
	if err != nil {
		return fmt.Errorf("link bookmark: %w", err)
	}
	return ensureRow(res, "bookmark")
}

// bookmarkColumns is the column list scanBookmark expects, in order.
const bookmarkColumns = `id, tenant_id, document_id, url, title, saved_at, source,
	folder_path, tags, created_at, updated_at`

// scanBookmark scans bookmarkColumns, then any extra columns a query
// selects after them into extra. A missing row is store.ErrNotFound.
func scanBookmark(row interface{ Scan(...any) error }, extra ...any) (*store.Bookmark, error) {
	var (
		b                              store.Bookmark
		docID, title, folderPath, tags sql.NullString
		savedAt, createdAt, updatedAt  string
	)
	err := row.Scan(slices.Concat([]any{
		&b.ID, &b.TenantID, &docID, &b.URL, &title,
		&savedAt, &b.Source, &folderPath, &tags,
		&createdAt, &updatedAt,
	}, extra)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan bookmark: %w", err)
	}
	b.DocumentID = nullableString(docID)
	b.Title = nullableString(title)
	b.FolderPath = nullableString(folderPath)
	if tags.Valid && tags.String != "" {
		if err := json.Unmarshal([]byte(tags.String), &b.Tags); err != nil {
			return nil, fmt.Errorf("decode tags: %w", err)
		}
	}
	if b.SavedAt, err = parseTime(savedAt); err != nil {
		return nil, err
	}
	if b.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if b.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &b, nil
}

// encodeTags serializes a tag list as JSON. An empty list is NULL, which the
// column's CHECK allows.
func encodeTags(tags []string) (sql.NullString, error) {
	if len(tags) == 0 {
		return sql.NullString{}, nil
	}
	out, err := json.Marshal(tags)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode tags: %w", err)
	}
	return sql.NullString{String: string(out), Valid: true}, nil
}
