package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
func (s *Bookmarks) TagsForDocument(ctx context.Context, tenantID, documentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tags FROM bookmarks WHERE tenant_id = ? AND document_id = ?`,
		tenantID, documentID)
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

const bookmarkListLimitDefault = 50

func (s *Bookmarks) Create(ctx context.Context, b *store.Bookmark) error {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	if b.TenantID == "" {
		return fmt.Errorf("bookmarks: tenant_id required")
	}
	if b.URL == "" {
		return fmt.Errorf("bookmarks: url required")
	}
	if b.Source == "" {
		return fmt.Errorf("bookmarks: source required")
	}
	if b.SavedAt.IsZero() {
		return fmt.Errorf("bookmarks: saved_at required")
	}

	tagsJSON, err := encodeTags(b.Tags)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO bookmarks (id, tenant_id, document_id, url, title, saved_at, source, folder_path, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.TenantID,
		strPtr(b.DocumentID), b.URL,
		strPtr(b.Title), formatTime(b.SavedAt), b.Source,
		strPtr(b.FolderPath), tagsJSON,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: bookmark for (tenant, url, source) exists", store.ErrConflict)
		}
		return fmt.Errorf("insert bookmark: %w", err)
	}

	got, err := s.GetByID(ctx, b.ID)
	if err != nil {
		return err
	}
	b.CreatedAt = got.CreatedAt
	b.UpdatedAt = got.UpdatedAt
	return nil
}

func (s *Bookmarks) GetByID(ctx context.Context, id string) (*store.Bookmark, error) {
	row := s.db.QueryRowContext(ctx, bookmarkSelectCols+" FROM bookmarks WHERE id = ?", id)
	return scanBookmark(row)
}

func (s *Bookmarks) List(ctx context.Context, tenantID string, opts store.ListBookmarksOpts) ([]*store.Bookmark, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = bookmarkListLimitDefault
	}

	var (
		clauses []string
		args    []any
	)
	clauses = append(clauses, "tenant_id = ?")
	args = append(args, tenantID)

	if opts.Source != "" {
		clauses = append(clauses, "source = ?")
		args = append(args, opts.Source)
	}
	if opts.FolderPath != "" {
		clauses = append(clauses, "folder_path LIKE ?")
		args = append(args, opts.FolderPath+"%")
	}
	if opts.Cursor != "" {
		clauses = append(clauses, "id > ?")
		args = append(args, opts.Cursor)
	}

	q := bookmarkSelectCols +
		" FROM bookmarks WHERE " + strings.Join(clauses, " AND ") +
		" ORDER BY id LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	defer rows.Close()

	var out []*store.Bookmark
	for rows.Next() {
		b, err := scanBookmark(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
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
		`UPDATE bookmarks SET document_id = ? WHERE id = ?`,
		documentID, bookmarkID)
	if err != nil {
		return fmt.Errorf("link bookmark: %w", err)
	}
	return ensureRow(res, "bookmark")
}

const bookmarkSelectCols = `SELECT id, tenant_id, document_id, url, title, saved_at, source,
		folder_path, tags, created_at, updated_at`

func scanBookmark(row interface{ Scan(...any) error }) (*store.Bookmark, error) {
	var (
		b                              store.Bookmark
		docID, title, folderPath, tags sql.NullString
		savedAt, createdAt, updatedAt  string
	)
	err := row.Scan(
		&b.ID, &b.TenantID, &docID, &b.URL, &title,
		&savedAt, &b.Source, &folderPath, &tags,
		&createdAt, &updatedAt,
	)
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

// encodeTags serializes a tag list as JSON or returns nil for an empty list
// so the column lands as NULL (matches the CHECK that allows NULL).
func encodeTags(tags []string) (any, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	out, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("encode tags: %w", err)
	}
	return string(out), nil
}

// isUniqueViolation returns true if err is a SQLite UNIQUE constraint error.
// We string-match because mattn/go-sqlite3 doesn't surface a typed code that
// distinguishes uniqueness from other constraint errors cleanly across
// versions; the message is stable.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed")
}
