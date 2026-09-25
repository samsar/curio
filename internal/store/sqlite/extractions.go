package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
)

type Extractions struct {
	db *DB
}

var _ store.ExtractionStore = (*Extractions)(nil)

func NewExtractions(db *DB) *Extractions { return &Extractions{db: db} }

func (s *Extractions) Create(ctx context.Context, e *store.DocumentExtraction) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.DocumentID == "" {
		return fmt.Errorf("extractions: document_id required")
	}
	if e.Fetcher == "" {
		return fmt.Errorf("extractions: fetcher required")
	}
	if e.Status == "" {
		return fmt.Errorf("extractions: status required")
	}

	// A zero FetchedAt takes the column's default, now.
	var fetchedAt any
	if !e.FetchedAt.IsZero() {
		fetchedAt = formatTime(e.FetchedAt)
	}
	var stored string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO document_extractions (`+extractionColumns+`)
		VALUES (?, ?, COALESCE(?, `+sqlNow+`), ?, ?, ?, ?, ?, ?)
		RETURNING fetched_at`,
		e.ID, e.DocumentID, fetchedAt, e.Fetcher, e.Status,
		strPtr(e.MarkdownPath), strPtr(e.RawPath),
		rawJSON(e.ExtractionMeta),
		strPtr(e.ErrorMessage),
	).Scan(&stored)
	if err != nil {
		return fmt.Errorf("insert extraction: %w", err)
	}
	e.FetchedAt, err = parseTime(stored)
	return err
}

// extractionColumns is the column list scanExtraction expects, in order.
const extractionColumns = `id, document_id, fetched_at, fetcher, status,
	markdown_path, raw_path, extraction_meta, error_message`

func (s *Extractions) GetByID(ctx context.Context, id string) (*store.DocumentExtraction, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+extractionColumns+` FROM document_extractions WHERE id = ?`, id)
	return scanExtraction(row)
}

func (s *Extractions) ListByDocument(ctx context.Context, documentID string) ([]*store.DocumentExtraction, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+extractionColumns+` FROM document_extractions WHERE document_id = ?
		ORDER BY fetched_at DESC`, documentID)
	if err != nil {
		return nil, fmt.Errorf("list extractions: %w", err)
	}
	defer rows.Close()

	var out []*store.DocumentExtraction
	for rows.Next() {
		e, err := scanExtraction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanExtraction(row interface{ Scan(...any) error }) (*store.DocumentExtraction, error) {
	var (
		e                             store.DocumentExtraction
		fetchedAt                     string
		markdownPath, rawPath, errMsg sql.NullString
		extractionMeta                sql.NullString
	)
	err := row.Scan(
		&e.ID, &e.DocumentID, &fetchedAt, &e.Fetcher, &e.Status,
		&markdownPath, &rawPath, &extractionMeta, &errMsg,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan extraction: %w", err)
	}
	if e.FetchedAt, err = parseTime(fetchedAt); err != nil {
		return nil, err
	}
	e.MarkdownPath = nullableString(markdownPath)
	e.RawPath = nullableString(rawPath)
	e.ErrorMessage = nullableString(errMsg)
	if extractionMeta.Valid {
		e.ExtractionMeta = []byte(extractionMeta.String)
	}
	return &e, nil
}

// rawJSON returns the raw bytes if non-empty, else nil so the column lands as NULL.
func rawJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
