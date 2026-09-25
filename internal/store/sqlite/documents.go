package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

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

// documentColumns is the column list scanDocument expects, in order.
const documentColumns = `id, tenant_id, url, url_canonical, content_type, title, author,
	published_at, language, word_count, current_extraction_id, state,
	created_at, updated_at`

func (s *Documents) queryOne(ctx context.Context, where string, args ...any) (*store.Document, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+documentColumns+" FROM documents WHERE "+where, args...)
	return scanDocument(row)
}

// scanDocument scans documentColumns, then any extra columns a query
// selects after them into extra. A missing row is store.ErrNotFound.
func scanDocument(row interface{ Scan(...any) error }, extra ...any) (*store.Document, error) {
	var (
		d                                            store.Document
		urlCanonical, title, author, language, curEx sql.NullString
		publishedAt                                  sql.NullString
		wordCount                                    sql.NullInt64
		createdAt, updatedAt                         string
	)
	err := row.Scan(slices.Concat([]any{
		&d.ID, &d.TenantID, &d.URL,
		&urlCanonical, &d.ContentType,
		&title, &author,
		&publishedAt, &language,
		&wordCount, &curEx, &d.State,
		&createdAt, &updatedAt,
	}, extra)...)
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

func (s *Documents) UpdateState(ctx context.Context, id string, state store.DocState) error {
	res, err := s.db.ExecContext(ctx, `UPDATE documents SET state = ? WHERE id = ?`, state, id)
	if err != nil {
		return fmt.Errorf("update document state: %w", err)
	}
	return ensureRow(res, "document")
}

// ListWithLastError joins each document to the most recent failed job whose
// payload names it (json_extract on payload.document_id) and to its current
// extraction for the markdown path.
func (s *Documents) ListWithLastError(ctx context.Context, tenantID string, opts store.ListDocumentsOpts) ([]store.DocumentWithError, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + qualify("d", documentColumns) + `,
		COALESCE(j.last_error, '') AS last_error,
		COALESCE(e.markdown_path, '') AS markdown_path
		FROM documents d
		LEFT JOIN jobs j ON j.id = (
			SELECT id FROM jobs
			WHERE status = 'failed'
			  AND json_extract(payload, '$.document_id') = d.id
			ORDER BY updated_at DESC
			LIMIT 1
		)
		LEFT JOIN document_extractions e ON e.id = d.current_extraction_id
		WHERE d.tenant_id = ?`
	args := []any{tenantID}
	if opts.State != "" {
		q += ` AND d.state = ?`
		args = append(args, opts.State)
	}
	q += ` ORDER BY d.updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list documents with error: %w", err)
	}
	defer rows.Close()

	var out []store.DocumentWithError
	for rows.Next() {
		var item store.DocumentWithError
		if item.Document, err = scanDocument(rows, &item.LastError, &item.MarkdownPath); err != nil {
			return nil, fmt.Errorf("list documents with error: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list documents with error: %w", err)
	}
	return out, nil
}

func (s *Documents) ListIDsWithContent(ctx context.Context, tenantID string, state store.DocState) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM documents
		WHERE tenant_id = ? AND state = ? AND current_extraction_id IS NOT NULL`,
		tenantID, state)
	if err != nil {
		return nil, fmt.Errorf("list document ids with content: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list document ids with content: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list document ids with content: %w", err)
	}
	return out, nil
}

func (s *Documents) CountByState(ctx context.Context, tenantID string) (map[store.DocState]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT state, count(*) FROM documents WHERE tenant_id = ? GROUP BY state`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("count documents: %w", err)
	}
	defer rows.Close()
	out := map[store.DocState]int{}
	for rows.Next() {
		var (
			state store.DocState
			n     int
		)
		if err := rows.Scan(&state, &n); err != nil {
			return nil, fmt.Errorf("count documents: %w", err)
		}
		out[state] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count documents: %w", err)
	}
	return out, nil
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

func (s *Documents) RequeueFetch(ctx context.Context, tenantID, documentID string) (*store.Job, error) {
	job, err := newFetchJob(tenantID, documentID)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin requeue fetch: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Write first, so the transaction takes the write lock outright instead
	// of upgrading from a read lock (see decisions.md "Job queue claim via
	// atomic UPDATE ... RETURNING").
	res, err := tx.ExecContext(ctx,
		`UPDATE documents SET state = ? WHERE tenant_id = ? AND id = ?`,
		store.DocStatePending, tenantID, documentID)
	if err != nil {
		return nil, fmt.Errorf("reset document state: %w", err)
	}
	if err := ensureRow(res, "document"); err != nil {
		return nil, err
	}
	if err := insertJob(ctx, tx, job); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit requeue fetch: %w", err)
	}
	return job, nil
}

func (s *Documents) RequeueFetchByStates(ctx context.Context, tenantID string, states []store.DocState) (int, error) {
	if len(states) == 0 {
		return 0, errors.New("requeue fetch: states required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin requeue fetch: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Write first, as in RequeueFetch. The whole corpus goes in one
	// transaction: resetting and enqueueing 50k documents takes 1.5-2s,
	// inside the 5s busy_timeout other writers wait for up to roughly 130k
	// documents (docs/decisions.md "Refetch: state reset and fetch job in
	// one transaction").
	args := appendArgs([]any{store.DocStatePending, tenantID}, states)
	rows, err := tx.QueryContext(ctx, `
		UPDATE documents SET state = ?
		WHERE tenant_id = ? AND state IN (`+placeholders(len(states))+`)
		RETURNING id`, args...)
	if err != nil {
		return 0, fmt.Errorf("reset document states: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("reset document states: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("reset document states: %w", err)
	}

	for _, id := range ids {
		job, err := newFetchJob(tenantID, id)
		if err != nil {
			return 0, err
		}
		if err := insertJob(ctx, tx, job); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit requeue fetch: %w", err)
	}
	return len(ids), nil
}

// newFetchJob builds a fetch job for a document. The payload is
// jobs.FetchPayload's shape, which package store can't import.
func newFetchJob(tenantID, documentID string) (*store.Job, error) {
	payload, err := json.Marshal(struct {
		DocumentID string `json:"document_id"`
	}{documentID})
	if err != nil {
		return nil, fmt.Errorf("encode fetch payload: %w", err)
	}
	return &store.Job{TenantID: tenantID, Kind: store.JobKindFetch, Payload: payload}, nil
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
