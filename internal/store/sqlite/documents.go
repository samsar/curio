package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
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

// DocumentWithError pairs a Document with the most recent failed-job error
// message that targeted it. Used by the debug-listing endpoint so users
// can see in one query which docs are broken and why, without cross-
// referencing the jobs table by hand.
type DocumentWithError struct {
	*store.Document
	LastError string // empty if no failed job is associated
}

// ListWithLastError returns documents for a tenant, optionally filtered by
// state, paired with the last_error of their most recent failed fetch or
// index job. Ordered most-recently-updated first.
//
// The subquery uses json_extract on jobs.payload to find jobs whose
// document_id matches; the payload format is set by the handlers in
// internal/jobs/handlers.go. If we add more job kinds that carry
// document_id later, those errors will surface here automatically.
func (s *Documents) ListWithLastError(ctx context.Context, tenantID, state string, limit int) ([]DocumentWithError, error) {
	if limit <= 0 {
		limit = 50
	}
	const cols = `d.id, d.tenant_id, d.url, d.url_canonical, d.content_type, d.title, d.author,
		d.published_at, d.language, d.word_count, d.current_extraction_id, d.state,
		d.created_at, d.updated_at`

	q := `SELECT ` + cols + `, COALESCE(j.last_error, '') AS last_error
		FROM documents d
		LEFT JOIN jobs j ON j.id = (
			SELECT id FROM jobs
			WHERE status = 'failed'
			  AND json_extract(payload, '$.document_id') = d.id
			ORDER BY updated_at DESC
			LIMIT 1
		)
		WHERE d.tenant_id = ?`
	args := []any{tenantID}
	if state != "" {
		q += ` AND d.state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY d.updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list documents with error: %w", err)
	}
	defer rows.Close()

	var out []DocumentWithError
	for rows.Next() {
		doc, lastErr, err := scanDocumentWithError(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, DocumentWithError{Document: doc, LastError: lastErr})
	}
	return out, rows.Err()
}

// scanDocumentWithError mirrors scanDocument but adds the joined last_error
// column at the end.
func scanDocumentWithError(row interface{ Scan(...any) error }) (*store.Document, string, error) {
	var (
		d                                            store.Document
		urlCanonical, title, author, language, curEx sql.NullString
		publishedAt                                  sql.NullString
		wordCount                                    sql.NullInt64
		createdAt, updatedAt                         string
		lastErr                                      string
	)
	err := row.Scan(
		&d.ID, &d.TenantID, &d.URL,
		&urlCanonical, &d.ContentType,
		&title, &author,
		&publishedAt, &language,
		&wordCount, &curEx, &d.State,
		&createdAt, &updatedAt,
		&lastErr,
	)
	if err != nil {
		return nil, "", fmt.Errorf("scan document with error: %w", err)
	}
	d.URLCanonical = nullableString(urlCanonical)
	d.Title = nullableString(title)
	d.Author = nullableString(author)
	d.Language = nullableString(language)
	d.CurrentExtractionID = nullableString(curEx)
	d.WordCount = nullableInt(wordCount)
	if publishedAt.Valid {
		pt, perr := parseTime(publishedAt.String)
		if perr != nil {
			return nil, "", perr
		}
		d.PublishedAt = &pt
	}
	if d.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, "", err
	}
	if d.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, "", err
	}
	return &d, lastErr, nil
}

// ListIDs returns all document IDs for a tenant, optionally restricted
// to a particular state. Used by the bulk refetch path.
func (s *Documents) ListIDs(ctx context.Context, tenantID, state string) ([]string, error) {
	q := `SELECT id FROM documents WHERE tenant_id = ?`
	args := []any{tenantID}
	if state != "" {
		q += ` AND state = ?`
		args = append(args, state)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list document ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CountByState returns (total, perStateMap) for one tenant.
func (s *Documents) CountByState(ctx context.Context, tenantID string) (int, map[string]int, error) {
	const q = `SELECT state, count(*) FROM documents WHERE tenant_id = ? GROUP BY state`
	rows, err := s.db.QueryContext(ctx, q, tenantID)
	if err != nil {
		return 0, nil, fmt.Errorf("count documents: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	total := 0
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return 0, nil, err
		}
		out[state] = n
		total += n
	}
	return total, out, rows.Err()
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
