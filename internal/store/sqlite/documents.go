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
	"time"

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

func (s *Documents) Create(ctx context.Context, d *store.Document) error {
	if d.TenantID == "" {
		return errors.New("documents: tenant_id required")
	}
	if d.URL == "" {
		return errors.New("documents: url required")
	}
	if d.CurrentExtractionID != nil {
		return errors.New("documents: a new document has no current extraction")
	}
	state, err := newDocumentState(d.State, d.FailureCause)
	if err != nil {
		return err
	}
	doc := *d
	if doc.ID == "" {
		doc.ID = uuid.NewString()
	}
	doc.State = state
	doc.ContentType = cmp.Or(doc.ContentType, store.ContentTypeUnknown)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin document insert: %w", err)
	}
	defer tx.Rollback()
	var createdAt, updatedAt string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO documents (
			id, tenant_id, url, url_canonical, content_type, title, author,
			published_at, language, word_count, state, failure_cause
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING created_at, updated_at`,
		doc.ID, doc.TenantID, doc.URL,
		strPtr(doc.URLCanonical),
		doc.ContentType,
		strPtr(doc.Title), strPtr(doc.Author),
		timePtr(doc.PublishedAt),
		strPtr(doc.Language),
		intPtr(doc.WordCount),
		doc.State,
		store.NullableString(string(doc.FailureCause)),
	).Scan(&createdAt, &updatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("document for (tenant, url) %s: %w", doc.URL, store.ErrConflict)
		}
		return fmt.Errorf("insert document: %w", err)
	}
	if doc.CreatedAt, err = parseTime(createdAt); err != nil {
		return err
	}
	if doc.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return err
	}
	if err := s.db.refreshFields(ctx, tx, doc.TenantID, doc.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit document insert: %w", err)
	}
	*d = doc
	return nil
}

// newDocumentState is the state Create inserts a document in, given the
// state and failure cause it was asked for: a cause must be valid and go
// with the state (store.FailureCause.State), which it fills in when empty,
// and a failed or dead document must have one.
func newDocumentState(state store.DocState, cause store.FailureCause) (store.DocState, error) {
	if cause == "" {
		state = cmp.Or(state, store.DocStatePending)
		if state == store.DocStateFailed || state == store.DocStateDead {
			return "", fmt.Errorf("documents: a %s document needs a failure cause", state)
		}
		return state, nil
	}
	if !cause.Valid() {
		return "", fmt.Errorf("documents: unknown failure cause %q", cause)
	}
	if state != "" && state != cause.State() {
		return "", fmt.Errorf("documents: a %s document can't have failure cause %s", state, cause)
	}
	return cause.State(), nil
}

// ApplyFetch writes the fetch and derives the document's search fields
// again in one transaction: its title, author and the site name its new
// extraction declares are among what they are derived from.
func (s *Documents) ApplyFetch(ctx context.Context, id string, m store.FetchedMetadata) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply fetch to document %s: begin: %w", id, err)
	}
	defer tx.Rollback()
	var tenantID string
	err = tx.QueryRowContext(ctx, `
		UPDATE documents SET
			content_type = ?, url_canonical = ?, title = ?, author = ?, language = ?,
			published_at = ?, current_extraction_id = ?, state = ?, failure_cause = NULL,
			updated_at = `+sqlNow+`
		WHERE id = ?
		RETURNING tenant_id`,
		m.ContentType, strPtr(m.URLCanonical), strPtr(m.Title), strPtr(m.Author), strPtr(m.Language),
		timePtr(m.PublishedAt), m.ExtractionID, store.DocStatePending,
		id).Scan(&tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("document %s: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("apply fetch to document %s: %w", id, err)
	}
	if err := s.db.refreshFields(ctx, tx, tenantID, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apply fetch to document %s: commit: %w", id, err)
	}
	return nil
}

func (s *Documents) GetByID(ctx context.Context, id string) (*store.Document, error) {
	return s.queryOne(ctx, "id = ?", id)
}

// getByURLWhere is GetByURL's predicate, pinned in plans_test to the
// UNIQUE (tenant_id, url) index.
const getByURLWhere = "tenant_id = ? AND url = ?"

func (s *Documents) GetByURL(ctx context.Context, tenantID, url string) (*store.Document, error) {
	return s.queryOne(ctx, getByURLWhere, tenantID, url)
}

// documentColumns is the column list scanDocument expects, in order.
const documentColumns = `id, tenant_id, url, url_canonical, content_type, title, author,
	published_at, language, word_count, current_extraction_id, state, failure_cause,
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
		publishedAt, failureCause                    sql.NullString
		wordCount                                    sql.NullInt64
		createdAt, updatedAt                         string
	)
	err := row.Scan(slices.Concat([]any{
		&d.ID, &d.TenantID, &d.URL,
		&urlCanonical, &d.ContentType,
		&title, &author,
		&publishedAt, &language,
		&wordCount, &curEx, &d.State, &failureCause,
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
	d.FailureCause = store.FailureCause(failureCause.String)
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

// markFailedSQL and markFetchedSQL write a document's state and failure
// cause together, so no write can leave the one without the other.
// markFetchedSQL and markIndexedSQL are also the writes of indexed_at and
// word_count: the count describes the text whose chunks were just indexed,
// and SQLite reads 'now' once per statement, so indexed_at equals the
// updated_at written with it. markIndexedSQL is markFetchedSQL held to the
// extraction indexed (?4).
//
// markReembeddedSQL is a re-embed's: new vectors of a document that is
// fetched and stays so, held to the extraction indexed (?3) and the
// fetched state (?4). Unlike every other UPDATE the store runs, it leaves
// updated_at alone: the document didn't change, and updated_at orders the
// library's lists and their keyset pages, which a re-embed of every
// document would otherwise reshuffle for its whole run. indexed_at, which
// the interests' change counts, their settling and the placement sweep
// read, records the new vectors.
const (
	markFailedSQL  = `UPDATE documents SET state = ?, failure_cause = ?, updated_at = ` + sqlNow + ` WHERE id = ?`
	markFetchedSQL = `UPDATE documents SET state = ?, failure_cause = NULL, word_count = ?, updated_at = ` + sqlNow +
		`, indexed_at = ` + sqlNow + ` WHERE id = ?`
	markIndexedSQL = `UPDATE documents SET state = ?1, failure_cause = NULL, word_count = ?2, updated_at = ` + sqlNow +
		`, indexed_at = ` + sqlNow + ` WHERE id = ?3 AND current_extraction_id = ?4`
	markReembeddedSQL = `UPDATE documents SET word_count = ?1, indexed_at = ` + sqlNow + `
		WHERE id = ?2 AND current_extraction_id = ?3 AND state = ?4`
	// documentExistsSQL tells a write its guard refused from one whose
	// document is gone.
	documentExistsSQL = `SELECT EXISTS (SELECT 1 FROM documents WHERE id = ?)`
)

func (s *Documents) MarkFailed(ctx context.Context, id string, cause store.FailureCause) error {
	if !cause.Valid() {
		return fmt.Errorf("mark document %s failed: unknown failure cause %q", id, cause)
	}
	res, err := s.db.ExecContext(ctx, markFailedSQL, cause.State(), cause, id)
	if err != nil {
		return fmt.Errorf("mark document %s failed: %w", id, err)
	}
	return ensureRow(res, "document")
}

// MarkFetched is MarkIndexed for no extraction in particular: the document
// becomes fetched, with no failure cause, a word count of words and its
// indexed_at now, whatever its current extraction. It isn't in
// store.DocumentStore: the index job writes through MarkIndexed, held to
// what it read, and this is how tests and fixtures seed a document as
// indexed. A negative count is an error, and nothing is written.
// ErrNotFound if there is no such document.
func (s *Documents) MarkFetched(ctx context.Context, id string, words int) error {
	if words < 0 {
		return fmt.Errorf("mark document %s fetched: a word count of %d", id, words)
	}
	res, err := s.db.ExecContext(ctx, markFetchedSQL, store.DocStateFetched, words, id)
	if err != nil {
		return fmt.Errorf("mark document %s fetched: %w", id, err)
	}
	return ensureRow(res, "document")
}

// MarkIndexed implements store.DocumentStore.
func (s *Documents) MarkIndexed(ctx context.Context, id string, guard store.IndexGuard, words int) error {
	if words < 0 {
		return fmt.Errorf("mark document %s indexed: a word count of %d", id, words)
	}
	var (
		res sql.Result
		err error
	)
	if guard.Reembed {
		res, err = s.db.ExecContext(ctx, markReembeddedSQL, words, id, guard.ExtractionID, store.DocStateFetched)
	} else {
		res, err = s.db.ExecContext(ctx, markIndexedSQL, store.DocStateFetched, words, id, guard.ExtractionID)
	}
	if err != nil {
		return fmt.Errorf("mark document %s indexed: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark document %s indexed: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, documentExistsSQL, id).Scan(&exists); err != nil {
		return fmt.Errorf("mark document %s indexed: %w", id, err)
	}
	if !exists {
		return fmt.Errorf("mark document %s indexed: %w", id, store.ErrNotFound)
	}
	return fmt.Errorf("mark document %s indexed from extraction %s: %w", id, guard.ExtractionID, store.ErrSuperseded)
}

// ListWithLastError looks up, for each document, the error of the most
// recent failed job for it, its current extraction for the markdown path,
// and, for an untitled one, its bookmark's title, all in one query.
func (s *Documents) ListWithLastError(ctx context.Context, tenantID string, opts store.ListDocumentsOpts) ([]store.DocumentWithError, error) {
	q, args := listDocumentsQuery(tenantID, opts)
	docs, err := s.queryDocumentsWithError(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list documents with error: %w", err)
	}
	return docs, nil
}

func (s *Documents) GetWithLastError(ctx context.Context, tenantID, id string) (*store.DocumentWithError, error) {
	q, args := getDocumentWithErrorQuery(tenantID, id)
	doc, err := scanDocumentWithError(s.db.QueryRowContext(ctx, q, args...))
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("document %s: %w", id, err)
	}
	if err != nil {
		return nil, fmt.Errorf("get document %s with error: %w", id, err)
	}
	return doc, nil
}

// getDocumentsWithErrorSQL is GetByIDsWithLastError's query. The IDs come
// as one JSON array, so the SQL is the same however many there are, and no
// number of them reaches SQLite's limit on bound parameters (32,766). Each
// ID seeks the documents primary key. The tenant term is written
// +d.tenant_id so that it is only checked on the rows found: SQLite, which
// has no statistics to go on, takes d.tenant_id = ? with ten IDs or more
// as a reason to walk idx_documents_tenant_state_updated through every
// document the tenant has, 3 to 4 times slower on the author's library and
// slower as the library grows. Its arguments are store.JobStatusFailed,
// the IDs and the tenant.
var getDocumentsWithErrorSQL = selectDocumentsWithError +
	" WHERE d.id IN (SELECT value FROM json_each(?)) AND +d.tenant_id = ?"

func (s *Documents) GetByIDsWithLastError(ctx context.Context, tenantID string, ids []string) ([]store.DocumentWithError, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("get documents with error: encode their IDs: %w", err)
	}
	docs, err := s.queryDocumentsWithError(ctx, getDocumentsWithErrorSQL, store.JobStatusFailed, string(list), tenantID)
	if err != nil {
		return nil, fmt.Errorf("get documents with error: %w", err)
	}
	return docs, nil
}

// queryDocumentsWithError runs q, a query of selectDocumentsWithError, and
// scans its rows.
func (s *Documents) queryDocumentsWithError(ctx context.Context, q string, args ...any) ([]store.DocumentWithError, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.DocumentWithError
	for rows.Next() {
		item, err := scanDocumentWithError(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// lastErrorSQL is the error of the most recent failed job of documents d,
// or empty when it has none, or d is NULL for a bookmark linked to no
// document: a seek on idx_jobs_document. Its one argument is
// store.JobStatusFailed.
const lastErrorSQL = `COALESCE((
		SELECT j.last_error FROM jobs j
		WHERE j.document_id = d.id AND j.status = ?
		ORDER BY j.updated_at DESC
		LIMIT 1
	), '')`

// bookmarkTitleSQL names an untitled document d: the title of its most
// recently saved bookmark whose title isn't blank, or empty when it has
// none or d has a title of its own. Blank is ASCII whitespace alone, as
// the pages, which trim what they show, see it: SQL's one-argument trim
// strips only spaces, and a tab-only title would hide an older real one.
// It seeks idx_bookmarks_document, which holds a document's bookmarks in
// (saved_at, id) order, and runs only for the untitled rows. It takes no
// argument.
const bookmarkTitleSQL = `COALESCE(CASE WHEN coalesce(d.title, '') = '' THEN (
		SELECT b.title FROM bookmarks b
		WHERE b.document_id = d.id AND b.tenant_id = d.tenant_id
			AND trim(b.title, ' ' || char(9, 10, 11, 12, 13)) <> ''
		ORDER BY b.saved_at DESC, b.id DESC
		LIMIT 1
	) END, '')`

// selectDocumentsWithError reads documents as ListWithLastError and
// GetWithLastError return them: each with the error of its most recent
// failed job, its current extraction's markdown path, and an untitled
// one's bookmark title. Its one argument is store.JobStatusFailed; the
// query that uses it adds a WHERE on documents d.
var selectDocumentsWithError = `SELECT ` + qualify("d", documentColumns) + `,
	` + lastErrorSQL + ` AS last_error,
	COALESCE(e.markdown_path, '') AS markdown_path,
	` + bookmarkTitleSQL + ` AS bookmark_title
	FROM documents d
	LEFT JOIN document_extractions e ON e.id = d.current_extraction_id`

// scanDocumentWithError scans a row of selectDocumentsWithError.
func scanDocumentWithError(row interface{ Scan(...any) error }) (*store.DocumentWithError, error) {
	var item store.DocumentWithError
	var err error
	if item.Document, err = scanDocument(row, &item.LastError, &item.MarkdownPath, &item.BookmarkTitle); err != nil {
		return nil, err
	}
	return &item, nil
}

// listDocumentsQuery builds ListWithLastError's query. It walks
// idx_documents_tenant_cause_updated when filtered by cause,
// idx_documents_tenant_state_updated when filtered by state alone, and
// idx_documents_tenant_updated otherwise, in (updated_at, id) order from
// opts.After, so it stops at the limit. The cause's partial index holds
// only the documents that failed, so a rare cause reads its own documents
// rather than the whole list. The other filters are checked on each row
// it walks, the folder's through a seek on idx_bookmarks_document, and the
// last-error and bookmark-title subqueries run only for the rows returned.
func listDocumentsQuery(tenantID string, opts store.ListDocumentsOpts) (string, []any) {
	clauses := []string{"d.tenant_id = ?"}
	args := []any{store.JobStatusFailed, tenantID}
	if opts.State != "" {
		clauses = append(clauses, "d.state = ?")
		args = append(args, opts.State)
	}
	if opts.ContentType != "" {
		clauses = append(clauses, "d.content_type = ?")
		args = append(args, opts.ContentType)
	}
	if opts.Host != "" {
		cond, condArgs := hostPredicate("d.url", opts.Host)
		clauses = append(clauses, cond)
		args = append(args, condArgs...)
	}
	if opts.Cause != "" {
		clauses = append(clauses, "d.failure_cause = ?")
		args = append(args, opts.Cause)
	}
	if cond, condArgs, ok := folderPredicate("b.folder_path", opts.Folder); ok {
		// EXISTS, not a join: a document with several bookmarks in the
		// folder is still one row.
		clauses = append(clauses, "EXISTS (SELECT 1 FROM bookmarks b"+
			" WHERE b.document_id = d.id AND b.tenant_id = d.tenant_id AND "+cond+")")
		args = append(args, condArgs...)
	}
	if !opts.After.IsZero() {
		pred, predArgs := keysetAfter("d.updated_at", "d.id", opts.After)
		clauses = append(clauses, pred)
		args = append(args, predArgs...)
	}
	q := selectDocumentsWithError + " WHERE " + strings.Join(clauses, " AND ") +
		" ORDER BY d.updated_at DESC, d.id DESC LIMIT ?"
	return q, append(args, listLimit(opts.Limit))
}

// getDocumentWithErrorQuery builds GetWithLastError's query: a point
// search of the documents primary key.
func getDocumentWithErrorQuery(tenantID, id string) (string, []any) {
	return selectDocumentsWithError + " WHERE d.id = ? AND d.tenant_id = ?",
		[]any{store.JobStatusFailed, id, tenantID}
}

// Reads of the tenant's documents by state. Both use
// idx_documents_tenant_state_updated.
const (
	listIDsWithContentSQL = `
		SELECT id FROM documents
		WHERE tenant_id = ? AND state = ? AND current_extraction_id IS NOT NULL`
	countDocumentsSQL = `SELECT state, count(*) FROM documents WHERE tenant_id = ? GROUP BY state`
)

func (s *Documents) ListIDsWithContent(ctx context.Context, tenantID string, state store.DocState) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, listIDsWithContentSQL, tenantID, state)
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

// The reads and the write of the documents to re-embed for a new chunk
// context. The chunks of an older version are one range of
// idx_chunks_context_version (covering), which once the library is
// re-embedded holds only the chunks of documents no re-embed reaches
// (failed, dead or pending, until a fetch indexes them again).
//
//   - The list and the count read that range whole, the one into a list
//     the tenant's fetched documents (idx_documents_tenant_state_updated)
//     are checked against, the other checked against a list of those
//     documents: a few milliseconds over the owner's 50,938 chunks to
//     re-embed, less as they go, cheap enough for the Status page's
//     2-second poll.
//   - The top-up counts the re-embed jobs running and pending, due and
//     not, the running and the pending index jobs (idx_jobs_claim), each
//     one's payload read by its rowid; then walks the range in order, each
//     chunk's document by its primary key and its index jobs, and its
//     re-embeds failed for good within the wait, a seek of
//     idx_jobs_document each, stopping at the documents it needs; first
//     past every document a re-embed of failed for good, then, only when
//     that finds too few, past those it failed within the wait. The
//     documents it walks past are those no re-embed reaches, those in
//     flight and those that failed: a few hundred chunks.
const (
	reembedWhereSQL = `
		WHERE d.id IN (SELECT document_id FROM chunks WHERE context_version < ?1)
		  AND d.tenant_id = ?2 AND d.state = ?3 AND d.current_extraction_id IS NOT NULL`
	documentsToReembedSQL = `SELECT d.id FROM documents d` + reembedWhereSQL
	// countToReembedSQL's document term is written +c.document_id, so that
	// it is checked against the list of fetched documents rather than
	// seeking idx_chunks_document for each of them, whose rows it would
	// then read for their version.
	countToReembedSQL = `SELECT count(DISTINCT c.document_id), count(*) FROM chunks c
		WHERE c.context_version < ?1 AND +c.document_id IN (SELECT d.id FROM documents d
			WHERE d.tenant_id = ?2 AND d.state = ?3 AND d.current_extraction_id IS NOT NULL)`
	// reembedQueuedSQL counts the re-embed index jobs (kind ?3): those
	// running (?1), those pending (?2), and those pending and due by ?4.
	reembedQueuedSQL = `SELECT
		(SELECT count(*) FROM jobs WHERE status = ?1 AND kind = ?3 AND json_extract(payload, '$.reembed')),
		count(*), coalesce(sum(run_after <= ?4), 0)
		FROM jobs WHERE status = ?2 AND kind = ?3 AND json_extract(payload, '$.reembed')`
	// nextToReembedSQL is the next ?5 of the tenant's documents to
	// re-embed for version ?1 with no index job (?6) pending (?4) or
	// running (?7), and no re-embed job failed (?8) for good after ?9, in
	// the range's order.
	nextToReembedSQL = `SELECT DISTINCT c.document_id FROM chunks c CROSS JOIN documents d ON d.id = c.document_id
		WHERE c.context_version < ?1 AND d.tenant_id = ?2 AND d.state = ?3 AND d.current_extraction_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.document_id = d.id AND j.status IN (?4, ?7) AND j.kind = ?6)
		  AND NOT EXISTS (SELECT 1 FROM jobs f WHERE f.document_id = d.id AND f.status = ?8 AND f.updated_at > ?9
			AND +f.kind = ?6 AND json_extract(f.payload, '$.reembed'))
		LIMIT ?5`
	// reembedFailuresSQL counts a document's re-embed jobs failed for
	// good, and reads when the first of them failed, a seek of
	// idx_jobs_document; its kind is written +kind, so that idx_jobs_claim
	// can't serve it as a walk of every failed index job.
	reembedFailuresSQL = `SELECT count(*), min(updated_at) FROM jobs
		WHERE document_id = ?1 AND status = ?2 AND +kind = ?3 AND json_extract(payload, '$.reembed')`
	// othersIndexedSQL asks whether another of the tenant's (?1) documents
	// than ?3 was indexed after ?2: a range of idx_documents_tenant_indexed,
	// its first row past the document itself the answer.
	othersIndexedSQL = `SELECT EXISTS (SELECT 1 FROM documents WHERE tenant_id = ?1 AND indexed_at > ?2 AND id <> ?3)`
)

// DocumentsToReembed implements store.DocumentStore.
func (s *Documents) DocumentsToReembed(ctx context.Context, tenantID string, version int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, documentsToReembedSQL, version, tenantID, store.DocStateFetched)
	if err != nil {
		return nil, fmt.Errorf("list the documents to re-embed: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list the documents to re-embed: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the documents to re-embed: %w", err)
	}
	return out, nil
}

// CountToReembed implements store.DocumentStore.
func (s *Documents) CountToReembed(ctx context.Context, tenantID string, version int) (store.ReembedCount, error) {
	var n store.ReembedCount
	if err := s.db.QueryRowContext(ctx, countToReembedSQL, version, tenantID, store.DocStateFetched).Scan(
		&n.Documents, &n.Chunks); err != nil {
		return store.ReembedCount{}, fmt.Errorf("count the documents to re-embed: %w", err)
	}
	return n, nil
}

// EnqueueReembed implements store.DocumentStore. Its transaction takes the
// write lock at BEGIN, so two top-ups never both count a place free.
func (s *Documents) EnqueueReembed(ctx context.Context, tenantID string, version int,
	limits store.ReembedLimits) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("queue the re-embed: begin: %w", err)
	}
	defer tx.Rollback()
	now := time.Now()
	var running, pending, due int
	if err := tx.QueryRowContext(ctx, reembedQueuedSQL, store.JobStatusRunning, store.JobStatusPending,
		store.JobKindIndex, formatTime(now)).Scan(&running, &pending, &due); err != nil {
		return 0, fmt.Errorf("queue the re-embed: count the jobs queued: %w", err)
	}
	n := min(limits.InFlight-running-due, limits.InFlight+limits.Waiting-running-pending)
	if n <= 0 {
		return 0, nil
	}
	ids, err := nextToQueue(ctx, tx, tenantID, version, n, now.Add(-limits.FailedWait))
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		payload, err := json.Marshal(store.DocumentJobPayload{DocumentID: id, Reembed: true})
		if err != nil {
			return 0, fmt.Errorf("queue the re-embed of document %s: %w", id, err)
		}
		job := &store.Job{TenantID: tenantID, Kind: store.JobKindIndex, Payload: payload}
		if err := insertJob(ctx, tx, job); err != nil {
			return 0, fmt.Errorf("queue the re-embed of document %s: %w", id, err)
		}
	}
	var wake []store.JobKind
	if len(ids) > 0 {
		wake = []store.JobKind{store.JobKindIndex}
	}
	if err := s.db.commitNotify(tx, wake...); err != nil {
		return 0, fmt.Errorf("queue the re-embed: commit: %w", err)
	}
	return len(ids), nil
}

// nextToQueue reads the next n documents to re-embed for version, the
// documents none of whose re-embeds failed for good first, then those whose
// last failed before failedAfter. A document that fails for its own
// reasons stays in the stale range, at its place in the walk's order,
// which is the order of its ID: queued first, it would come up first at
// every top-up and every start, and, failing again, put the brake back on
// before any document that would re-embed got a job. Both passes are the
// one pinned walk; the second runs only once the first finds fewer than n,
// and finds those again, which are left out.
func nextToQueue(ctx context.Context, tx *sql.Tx, tenantID string, version, n int, failedAfter time.Time) (
	[]string, error) {
	ids, err := nextToReembed(ctx, tx, tenantID, version, n, time.Time{})
	if err != nil || len(ids) == n {
		return ids, err
	}
	again, err := nextToReembed(ctx, tx, tenantID, version, n, failedAfter)
	if err != nil {
		return nil, err
	}
	for _, id := range again {
		if len(ids) < n && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// nextToReembed reads the next n documents to re-embed for version with no
// index job pending or running, and no re-embed job failed for good after
// failedAfter: none at all for the zero time.
func nextToReembed(ctx context.Context, tx *sql.Tx, tenantID string, version, n int,
	failedAfter time.Time) ([]string, error) {
	rows, err := tx.QueryContext(ctx, nextToReembedSQL, version, tenantID, store.DocStateFetched,
		store.JobStatusPending, n, store.JobKindIndex, store.JobStatusRunning, store.JobStatusFailed,
		formatTime(failedAfter))
	if err != nil {
		return nil, fmt.Errorf("queue the re-embed: read the next documents: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("queue the re-embed: read the next documents: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue the re-embed: read the next documents: %w", err)
	}
	return ids, nil
}

// ReembedFailures implements store.DocumentStore.
func (s *Documents) ReembedFailures(ctx context.Context, tenantID, documentID string) (store.ReembedFailures,
	error) {
	var (
		out   store.ReembedFailures
		first sql.NullString
	)
	if err := s.db.QueryRowContext(ctx, reembedFailuresSQL, documentID, store.JobStatusFailed,
		store.JobKindIndex).Scan(&out.Failed, &first); err != nil {
		return store.ReembedFailures{}, fmt.Errorf("count the failed re-embeds of document %s: %w", documentID, err)
	}
	if !first.Valid {
		return out, nil
	}
	if err := s.db.QueryRowContext(ctx, othersIndexedSQL, tenantID, first.String, documentID).Scan(
		&out.OthersIndexed); err != nil {
		return store.ReembedFailures{}, fmt.Errorf("read whether others were indexed since document %s's "+
			"re-embed failed: %w", documentID, err)
	}
	return out, nil
}

func (s *Documents) CountByState(ctx context.Context, tenantID string) (map[store.DocState]int, error) {
	rows, err := s.db.QueryContext(ctx, countDocumentsSQL, tenantID)
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

// lastIndexedSQL is the tenant's latest indexed_at, read from the end of
// its range of idx_documents_tenant_indexed.
const lastIndexedSQL = `SELECT max(indexed_at) FROM documents WHERE tenant_id = ?`

func (s *Documents) LastIndexedAt(ctx context.Context, tenantID string) (time.Time, error) {
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, lastIndexedSQL, tenantID).Scan(&last); err != nil {
		return time.Time{}, fmt.Errorf("last indexed time: %w", err)
	}
	if !last.Valid {
		return time.Time{}, nil
	}
	return parseTime(last.String)
}

// failureSummarySQL reads the cause and URL of each of the tenant's failed
// and dead documents, for FailureSummary to count. The cause's partial
// index holds exactly those documents, so it reads no others. Counting in
// Go costs about 1 ms for 3,000 rows, and saves SQL both extracting a host
// from each URL and ranking hosts within each cause.
const failureSummarySQL = `SELECT failure_cause, url FROM documents
	WHERE tenant_id = ? AND failure_cause IS NOT NULL`

func (s *Documents) FailureSummary(ctx context.Context, tenantID string, topHosts int) (store.FailureSummary, error) {
	rows, err := s.db.QueryContext(ctx, failureSummarySQL, tenantID)
	if err != nil {
		return store.FailureSummary{}, fmt.Errorf("failure summary: %w", err)
	}
	defer rows.Close()
	var total int
	counts := map[store.FailureCause]int{}
	hosts := map[store.FailureCause]map[string]int{}
	for rows.Next() {
		var (
			cause store.FailureCause
			url   string
		)
		if err := rows.Scan(&cause, &url); err != nil {
			return store.FailureSummary{}, fmt.Errorf("failure summary: %w", err)
		}
		total++
		counts[cause]++
		if host := urlAuthority(url); host != "" {
			if hosts[cause] == nil {
				hosts[cause] = map[string]int{}
			}
			hosts[cause][host]++
		}
	}
	if err := rows.Err(); err != nil {
		return store.FailureSummary{}, fmt.Errorf("failure summary: %w", err)
	}

	summary := store.FailureSummary{Total: total, Causes: make([]store.CauseCount, 0, len(counts))}
	for cause, n := range counts {
		summary.Causes = append(summary.Causes,
			store.CauseCount{Cause: cause, Count: n, Hosts: topHostCounts(hosts[cause], topHosts)})
	}
	slices.SortFunc(summary.Causes, func(a, b store.CauseCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Cause, b.Cause))
	})
	return summary, nil
}

// topHostCounts is the n hosts with the most documents in counts, the most
// first, then by host.
func topHostCounts(counts map[string]int, n int) []store.HostCount {
	out := make([]store.HostCount, 0, len(counts))
	for host, c := range counts {
		out = append(out, store.HostCount{Host: host, Count: c})
	}
	slices.SortFunc(out, func(a, b store.HostCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Host, b.Host))
	})
	return out[:min(max(n, 0), len(out))]
}

// urlAuthority is the host of URL u as the documents list's host filter
// matches it (hostPredicate): the text after "://" up to the next '/', '?'
// or '#', lowercased as DNS names compare, with any port kept. It is ""
// when u has none.
func urlAuthority(u string) string {
	_, rest, ok := strings.Cut(u, "://")
	if !ok {
		return ""
	}
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		rest = rest[:end]
	}
	return strings.ToLower(rest)
}

// SetCurrentExtraction points the document at another extraction and
// derives its search fields again, in one transaction: the site name its
// page declared is the extraction's.
func (s *Documents) SetCurrentExtraction(ctx context.Context, docID, extractionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set current extraction: begin: %w", err)
	}
	defer tx.Rollback()
	var tenantID string
	err = tx.QueryRowContext(ctx,
		`UPDATE documents SET current_extraction_id = ?, updated_at = `+sqlNow+` WHERE id = ? RETURNING tenant_id`,
		extractionID, docID).Scan(&tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("document %s: %w", docID, store.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("set current extraction: %w", err)
	}
	if err := s.db.refreshFields(ctx, tx, tenantID, docID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set current extraction: commit: %w", err)
	}
	return nil
}

func (s *Documents) RequeueFetch(ctx context.Context, tenantID, documentID string) (*store.Job, error) {
	job, err := store.NewDocumentJob(tenantID, store.JobKindFetch, documentID)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin requeue fetch: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE documents SET state = ?, failure_cause = NULL, updated_at = `+sqlNow+`
		WHERE tenant_id = ? AND id = ?`,
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
	if err := s.db.commitNotify(tx, store.JobKindFetch); err != nil {
		return nil, fmt.Errorf("commit requeue fetch: %w", err)
	}
	return job, nil
}

func (s *Documents) RequeueFetchByStates(ctx context.Context, tenantID string, states []store.DocState,
	cause store.FailureCause) (int, error) {
	if len(states) == 0 {
		return 0, errors.New("requeue fetch: states required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin requeue fetch: %w", err)
	}
	defer tx.Rollback()

	// The whole corpus goes in one transaction: resetting and enqueueing 50k
	// documents takes about 2.4s, inside the 5s busy_timeout other writers
	// wait for up to roughly 100k documents (docs/decisions.md "Refetch:
	// state reset and fetch job in one transaction").
	args := appendArgs([]any{store.DocStatePending, tenantID}, states)
	if cause != "" {
		args = append(args, cause)
	}
	rows, err := tx.QueryContext(ctx, resetStatesSQL(len(states), cause != ""), args...)
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
		job, err := store.NewDocumentJob(tenantID, store.JobKindFetch, id)
		if err != nil {
			return 0, err
		}
		if err := insertJob(ctx, tx, job); err != nil {
			return 0, err
		}
	}
	var wake []store.JobKind
	if len(ids) > 0 {
		wake = []store.JobKind{store.JobKindFetch}
	}
	if err := s.db.commitNotify(tx, wake...); err != nil {
		return 0, fmt.Errorf("commit requeue fetch: %w", err)
	}
	return len(ids), nil
}

// resetStatesSQL sets the tenant's documents in nStates states, and with
// one failure cause when withCause, to pending, clearing their causes, and
// returns their IDs. Its args are the new state, the tenant, the states,
// then the cause. A cause seeks idx_documents_tenant_cause_updated, and
// states alone idx_documents_tenant_state_updated.
func resetStatesSQL(nStates int, withCause bool) string {
	q := `
	UPDATE documents SET state = ?, failure_cause = NULL, updated_at = ` + sqlNow + `
	WHERE tenant_id = ? AND state IN (` + placeholders(nStates) + `)`
	if withCause {
		q += ` AND failure_cause = ?`
	}
	return q + `
	RETURNING id`
}

// getOrCreateDocument returns the tenant's document for url, inserting it in
// state pending when there is none; created reports whether it did. It runs
// inside the caller's transaction, for units of work that save a reference
// (a bookmark today) together with its document.
func getOrCreateDocument(ctx context.Context, tx *sql.Tx, tenantID, url string) (id string, state store.DocState, created bool, err error) {
	err = tx.QueryRowContext(ctx, `
		INSERT INTO documents (id, tenant_id, url) VALUES (?, ?, ?)
		ON CONFLICT (tenant_id, url) DO NOTHING
		RETURNING id, state`,
		uuid.NewString(), tenantID, url).Scan(&id, &state)
	switch {
	case err == nil:
		return id, state, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", "", false, fmt.Errorf("insert document: %w", err)
	}
	// DO NOTHING returned no row: the document exists.
	err = tx.QueryRowContext(ctx,
		`SELECT id, state FROM documents WHERE tenant_id = ? AND url = ?`,
		tenantID, url).Scan(&id, &state)
	if err != nil {
		return "", "", false, fmt.Errorf("look up document: %w", err)
	}
	return id, state, false, nil
}
