package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/samansartipi/curio/internal/store"
)

// Documents implements store.DocumentStore on top of a *DB.
type Documents struct {
	db *DB
}

// Compile-time interface assertion. If the methods drift, the compiler tells us.
var _ store.DocumentStore = (*Documents)(nil)

func NewDocuments(db *DB) *Documents { return &Documents{db: db} }

func (s *Documents) Upsert(ctx context.Context, d *store.Document) error {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	if d.TenantID == "" {
		return fmt.Errorf("documents: tenant_id required")
	}
	if d.URL == "" {
		return fmt.Errorf("documents: url required")
	}
	if d.ContentType == "" {
		d.ContentType = store.ContentTypeUnknown
	}
	if d.State == "" {
		d.State = store.DocStatePending
	}

	const q = `
	INSERT INTO documents (
		id, tenant_id, url, url_canonical, content_type, title, author,
		published_at, language, word_count, current_extraction_id, state
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (tenant_id, url) DO UPDATE SET
		url_canonical         = COALESCE(excluded.url_canonical, documents.url_canonical),
		content_type          = excluded.content_type,
		title                 = COALESCE(excluded.title, documents.title),
		author                = COALESCE(excluded.author, documents.author),
		published_at          = COALESCE(excluded.published_at, documents.published_at),
		language              = COALESCE(excluded.language, documents.language),
		word_count            = COALESCE(excluded.word_count, documents.word_count),
		current_extraction_id = COALESCE(excluded.current_extraction_id, documents.current_extraction_id),
		state                 = excluded.state
	RETURNING id, created_at, updated_at`

	row := s.db.QueryRowContext(ctx, q,
		d.ID, d.TenantID, d.URL,
		strPtr(d.URLCanonical),
		d.ContentType,
		strPtr(d.Title), strPtr(d.Author),
		timePtr(d.PublishedAt),
		strPtr(d.Language),
		intPtr(d.WordCount),
		strPtr(d.CurrentExtractionID),
		d.State,
	)
	var createdAt, updatedAt string
	if err := row.Scan(&d.ID, &createdAt, &updatedAt); err != nil {
		return fmt.Errorf("upsert document: %w", err)
	}
	var err error
	if d.CreatedAt, err = parseTime(createdAt); err != nil {
		return err
	}
	d.UpdatedAt, err = parseTime(updatedAt)
	return err
}

func (s *Documents) GetByID(ctx context.Context, id string) (*store.Document, error) {
	return s.queryOne(ctx, "id = ?", id)
}

func (s *Documents) GetByURL(ctx context.Context, tenantID, url string) (*store.Document, error) {
	return s.queryOne(ctx, "tenant_id = ? AND url = ?", tenantID, url)
}

func (s *Documents) queryOne(ctx context.Context, where string, args ...any) (*store.Document, error) {
	const cols = `id, tenant_id, url, url_canonical, content_type, title, author,
		published_at, language, word_count, current_extraction_id, state,
		created_at, updated_at`
	row := s.db.QueryRowContext(ctx, "SELECT "+cols+" FROM documents WHERE "+where, args...)
	return scanDocument(row)
}

func scanDocument(row interface{ Scan(...any) error }) (*store.Document, error) {
	var (
		d                                            store.Document
		urlCanonical, title, author, language, curEx sql.NullString
		publishedAt                                  sql.NullString
		wordCount                                    sql.NullInt64
		createdAt, updatedAt                         string
	)
	err := row.Scan(
		&d.ID, &d.TenantID, &d.URL,
		&urlCanonical, &d.ContentType,
		&title, &author,
		&publishedAt, &language,
		&wordCount, &curEx, &d.State,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan document: %w", err)
	}
	d.URLCanonical = nullableString(urlCanonical)
	d.Title = nullableString(title)
	d.Author = nullableString(author)
	d.Language = nullableString(language)
	d.CurrentExtractionID = nullableString(curEx)
	d.WordCount = nullableInt(wordCount)
	if publishedAt.Valid {
		pt, err := parseTime(publishedAt.String)
		if err != nil {
			return nil, err
		}
		d.PublishedAt = &pt
	}
	if d.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if d.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Documents) UpdateState(ctx context.Context, id, state string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE documents SET state = ? WHERE id = ?`, state, id)
	if err != nil {
		return fmt.Errorf("update document state: %w", err)
	}
	return ensureRow(res, "document")
}

func (s *Documents) SetCurrentExtraction(ctx context.Context, docID, extractionID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE documents SET current_extraction_id = ? WHERE id = ?`,
		extractionID, docID)
	if err != nil {
		return fmt.Errorf("set current extraction: %w", err)
	}
	return ensureRow(res, "document")
}

func ensureRow(res sql.Result, entity string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", entity, store.ErrNotFound)
	}
	return nil
}
