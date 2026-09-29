package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

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

	var createdAt, updatedAt string
	err = s.db.QueryRowContext(ctx, `
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

func (s *Documents) ApplyFetch(ctx context.Context, id string, m store.FetchedMetadata) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE documents SET
			content_type = ?, url_canonical = ?, title = ?, author = ?, language = ?,
			published_at = ?, current_extraction_id = ?, state = ?, failure_cause = NULL,
			updated_at = `+sqlNow+`
		WHERE id = ?`,
		m.ContentType, strPtr(m.URLCanonical), strPtr(m.Title), strPtr(m.Author), strPtr(m.Language),
		timePtr(m.PublishedAt), m.ExtractionID, store.DocStatePending,
		id)
	if err != nil {
		return fmt.Errorf("apply fetch to document %s: %w", id, err)
	}
	return ensureRow(res, "document")
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
const (
	markFailedSQL  = `UPDATE documents SET state = ?, failure_cause = ?, updated_at = ` + sqlNow + ` WHERE id = ?`
	markFetchedSQL = `UPDATE documents SET state = ?, failure_cause = NULL, updated_at = ` + sqlNow + ` WHERE id = ?`
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

func (s *Documents) MarkFetched(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, markFetchedSQL, store.DocStateFetched, id)
	if err != nil {
		return fmt.Errorf("mark document %s fetched: %w", id, err)
	}
	return ensureRow(res, "document")
}

// ListWithLastError looks up, for each document, the error of the most
// recent failed job for it, its current extraction for the markdown path,
// and, for an untitled one, its bookmark's title, all in one query.
func (s *Documents) ListWithLastError(ctx context.Context, tenantID string, opts store.ListDocumentsOpts) ([]store.DocumentWithError, error) {
	q, args := listDocumentsQuery(tenantID, opts)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list documents with error: %w", err)
	}
	defer rows.Close()

	var out []store.DocumentWithError
	for rows.Next() {
		item, err := scanDocumentWithError(rows)
		if err != nil {
			return nil, fmt.Errorf("list documents with error: %w", err)
		}
		out = append(out, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list documents with error: %w", err)
	}
	return out, nil
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
// none or d has a title of its own. It seeks idx_bookmarks_document,
// which holds a document's bookmarks in (saved_at, id) order, and runs
// only for the untitled rows. It takes no argument.
const bookmarkTitleSQL = `COALESCE(CASE WHEN coalesce(d.title, '') = '' THEN (
		SELECT b.title FROM bookmarks b
		WHERE b.document_id = d.id AND b.tenant_id = d.tenant_id AND trim(b.title) <> ''
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

func (s *Documents) SetCurrentExtraction(ctx context.Context, docID, extractionID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE documents SET current_extraction_id = ?, updated_at = `+sqlNow+` WHERE id = ?`,
		extractionID, docID)
	if err != nil {
		return fmt.Errorf("set current extraction: %w", err)
	}
	return ensureRow(res, "document")
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
