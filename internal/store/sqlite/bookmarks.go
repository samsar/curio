package sqlite

import (
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

// TagsForDocument returns the deduplicated tags across all bookmarks that
// reference the document, scoped to the tenant. Order is first-seen.
// Malformed tag JSON on a row is skipped rather than failing the whole call.
// tagsForDocumentSQL reads the tags of a document's bookmarks. Its args are
// the tenant and the document.
const tagsForDocumentSQL = `SELECT tags FROM bookmarks WHERE tenant_id = ? AND document_id = ?`

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

// List reads each bookmark's document state through a join, so a page is
// one query however many bookmarks it holds.
func (s *Bookmarks) List(ctx context.Context, tenantID string, opts store.ListBookmarksOpts) ([]store.BookmarkWithState, error) {
	q, args := listBookmarksQuery(tenantID, opts)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	defer rows.Close()

	var out []store.BookmarkWithState
	for rows.Next() {
		var item store.BookmarkWithState
		if item.Bookmark, err = scanBookmark(rows, &item.DocumentState); err != nil {
			return nil, fmt.Errorf("list bookmarks: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	return out, nil
}

// listBookmarksQuery builds List's query. A page walks
// idx_bookmarks_tenant_created in (created_at, id) order from opts.After,
// checking the source and folder filters per row, so it reads about one
// page of rows, not every bookmark the tenant has.
func listBookmarksQuery(tenantID string, opts store.ListBookmarksOpts) (string, []any) {
	clauses := []string{"b.tenant_id = ?"}
	args := []any{tenantID}
	if opts.Source != "" {
		clauses = append(clauses, "b.source = ?")
		args = append(args, opts.Source)
	}
	if folder := strings.TrimRight(opts.FolderPath, "/"); folder != "" {
		// The folder itself or anything under it, compared byte-wise: '0'
		// is the byte after '/', so [folder+"/", folder+"0") holds exactly
		// the paths that start with folder+"/". Unlike LIKE there is nothing
		// to escape, and it is case-sensitive like the equality.
		clauses = append(clauses, "(b.folder_path = ? OR (b.folder_path >= ? AND b.folder_path < ?))")
		args = append(args, folder, folder+"/", folder+"0")
	}
	if !opts.After.IsZero() {
		pred, predArgs := keysetAfter("b.created_at", "b.id", opts.After)
		clauses = append(clauses, pred)
		args = append(args, predArgs...)
	}
	q := "SELECT " + qualify("b", bookmarkColumns) + ", COALESCE(d.state, '')" +
		" FROM bookmarks b LEFT JOIN documents d ON d.id = b.document_id" +
		" WHERE " + strings.Join(clauses, " AND ") +
		" ORDER BY b.created_at DESC, b.id DESC LIMIT ?"
	return q, append(args, listLimit(opts.Limit))
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
