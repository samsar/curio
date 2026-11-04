package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samsar/curio/internal/store"
)

// Summaries implements store.SummaryStore over document_summaries
// (migration 022). A summary is written text of its own, searched only by
// the summary list: no derived row reads it, so writing one changes no
// card and queues no cards job.
type Summaries struct {
	db *DB
}

var _ store.SummaryStore = (*Summaries)(nil)

// NewSummaries constructs the summary store.
func NewSummaries(db *DB) *Summaries { return &Summaries{db: db} }

// The statements of the summary store.
const (
	// summaryGuardSQL is a summary write's guard: the tenant's document is
	// fetched and its current extraction is ?3, read by primary key in the
	// write's transaction.
	summaryGuardSQL = `SELECT EXISTS (SELECT 1 FROM documents
	WHERE id = ?1 AND tenant_id = ?2 AND state = ?4 AND current_extraction_id = ?3)`
	// putSummarySQL writes a summary over the document's row, clearing
	// owed and the failure, and writes nothing when the row already holds
	// exactly that: no row change, no trigger fired.
	putSummarySQL = `
	INSERT INTO document_summaries (document_id, extraction_id, kind, site, author, about, entities, claims,
		descriptions, model, prompt_version, input_hash, written_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (document_id) DO UPDATE SET
		extraction_id = excluded.extraction_id, kind = excluded.kind, site = excluded.site,
		author = excluded.author, about = excluded.about, entities = excluded.entities, claims = excluded.claims,
		descriptions = excluded.descriptions, model = excluded.model, prompt_version = excluded.prompt_version,
		input_hash = excluded.input_hash, written_at = excluded.written_at, owed = 0, failed_extraction_id = NULL,
		failed_model = NULL, failed_prompt_version = NULL, failed_at = NULL, failure = NULL
	WHERE (extraction_id, kind, site, author, about, entities, claims, descriptions, model, prompt_version,
		input_hash, written_at, owed, failed_extraction_id, failed_model, failed_prompt_version, failed_at, failure)
		IS NOT (excluded.extraction_id, excluded.kind, excluded.site, excluded.author, excluded.about,
		excluded.entities, excluded.claims, excluded.descriptions, excluded.model, excluded.prompt_version,
		excluded.input_hash, excluded.written_at, 0, NULL, NULL, NULL, NULL, NULL)`
	// recordSummaryFailureSQL records a failure for good on the
	// document's row, a row of its own when it has none, while the tenant
	// has the document; the summary written before stays. Its args are
	// the document, the failure's extraction, model, prompt, time and
	// error, and the tenant.
	recordSummaryFailureSQL = `
	INSERT INTO document_summaries (document_id, failed_extraction_id, failed_model, failed_prompt_version,
		failed_at, failure)
	SELECT ?1, ?2, ?3, ?4, ?5, ?6 WHERE EXISTS (SELECT 1 FROM documents WHERE id = ?1 AND tenant_id = ?7)
	ON CONFLICT (document_id) DO UPDATE SET failed_extraction_id = excluded.failed_extraction_id,
		failed_model = excluded.failed_model, failed_prompt_version = excluded.failed_prompt_version,
		failed_at = excluded.failed_at, failure = excluded.failure`
	// getSummarySQL reads the tenant's document's row by its unique key.
	getSummarySQL = `SELECT s.extraction_id, s.kind, s.site, s.author, s.about, s.entities, s.claims,
		s.descriptions, s.model, s.prompt_version, s.input_hash, s.written_at, s.owed, s.failed_extraction_id,
		s.failed_model, s.failed_prompt_version, s.failed_at, s.failure
	FROM document_summaries s JOIN documents d ON d.id = s.document_id
	WHERE s.document_id = ?1 AND d.tenant_id = ?2`
	// summaryClassSQL sorts a document d, with its row s, into the class
	// SummaryCounts names it by, under the writer ?3 (model) and ?4
	// (prompt): fetched with a current extraction is the caller's
	// predicate.
	summaryClassSQL = `CASE
		WHEN s.extraction_id IS d.current_extraction_id AND NOT s.owed
			AND s.model = ?3 AND s.prompt_version = ?4 AND s.kind = '` + string(store.SummaryKindNotThePage) + `'
			THEN 'not_the_page'
		WHEN s.extraction_id IS d.current_extraction_id AND NOT s.owed AND s.model = ?3 AND s.prompt_version = ?4
			THEN 'written'
		WHEN s.extraction_id IS d.current_extraction_id AND NOT s.owed THEN 'outdated'
		WHEN s.failed_extraction_id IS d.current_extraction_id AND s.failed_model = ?3
			AND s.failed_prompt_version = ?4 THEN 'failed'
		ELSE 'to_write' END`
	// countSummariesSQL counts the tenant's (?1) fetched (?2) documents
	// with a current extraction by class: a range of
	// idx_documents_tenant_state_updated, each document's row by its
	// unique key.
	countSummariesSQL = `SELECT
		coalesce(sum(class = 'written'), 0), coalesce(sum(class = 'to_write'), 0),
		coalesce(sum(class = 'outdated'), 0), coalesce(sum(class = 'failed'), 0),
		coalesce(sum(class = 'not_the_page'), 0)
	FROM (SELECT ` + summaryClassSQL + ` AS class FROM documents d
		LEFT JOIN document_summaries s ON s.document_id = d.id
		WHERE d.tenant_id = ?1 AND d.state = ?2 AND d.current_extraction_id IS NOT NULL)`
	// oweOutdatedSQL marks owed the summaries another writer than ?3
	// (model) and ?4 (prompt) wrote, of the tenant's (?1) fetched (?2)
	// documents with a current extraction, the documents the pass
	// writes: a user's request, which lists them once.
	oweOutdatedSQL = `UPDATE document_summaries SET owed = 1
	WHERE owed = 0 AND extraction_id IS NOT NULL AND (model <> ?3 OR prompt_version <> ?4)
	  AND document_id IN (SELECT id FROM documents
		WHERE tenant_id = ?1 AND state = ?2 AND current_extraction_id IS NOT NULL)`
	// oweAllSQL marks owed every summary of the tenant's (?1) fetched (?2)
	// documents with a current extraction and clears their failures for
	// good, so a page whose summary failed is tried again too; as
	// oweOutdatedSQL, a user's request.
	oweAllSQL = `UPDATE document_summaries SET owed = 1, failed_extraction_id = NULL, failed_model = NULL,
		failed_prompt_version = NULL, failed_at = NULL, failure = NULL
	WHERE (owed = 0 OR failed_at IS NOT NULL)
	  AND document_id IN (SELECT id FROM documents
		WHERE tenant_id = ?1 AND state = ?2 AND current_extraction_id IS NOT NULL)`
	// nextToSummarizeSQL is the next ?9 of the tenant's (?1) fetched (?2)
	// documents to write a summary of under the writer ?3 (model) and ?4
	// (prompt): those indexed most recently first, walking
	// idx_documents_tenant_indexed backwards, each document's row by its
	// unique key and its summarize jobs (?8) pending (?6) or running (?7)
	// by idx_jobs_document; one failed for good under its current
	// extraction and the writer after ?5 is left out. The state is checked
	// on each row (+d.state), so the walk stays the index's, with no sort.
	nextToSummarizeSQL = `SELECT d.id, d.current_extraction_id FROM documents d
	LEFT JOIN document_summaries s ON s.document_id = d.id
	WHERE d.tenant_id = ?1 AND +d.state = ?2 AND d.current_extraction_id IS NOT NULL
	  AND (s.extraction_id IS NOT d.current_extraction_id OR s.owed)
	  AND NOT (s.failed_extraction_id IS d.current_extraction_id AND s.failed_model IS ?3
		AND s.failed_prompt_version IS ?4 AND s.failed_at > ?5)
	  AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.document_id = d.id AND j.status IN (?6, ?7) AND j.kind = ?8)
	ORDER BY d.indexed_at DESC
	LIMIT ?9`
	// summarizeQueuedSQL counts the summarize jobs (?3): those running
	// (?1), those pending (?2), and those pending and due by ?4.
	summarizeQueuedSQL = `SELECT
		(SELECT count(*) FROM jobs WHERE status = ?1 AND kind = ?3),
		count(*), coalesce(sum(run_after <= ?4), 0)
		FROM jobs WHERE status = ?2 AND kind = ?3`
	// insertSummarizeJobSQL is insertJobSQL unless the payload's document
	// has a summarize job pending or running, or is gone (deleted since
	// the walk read it, outside the lock): then it inserts nothing and
	// returns no row. The checks seek idx_jobs_document and the
	// documents' primary key.
	insertSummarizeJobSQL = `
	INSERT INTO jobs (id, tenant_id, kind, payload, document_id, status, attempts, run_after)
	SELECT ?1, ?2, ?3, ?4, json_extract(?4, '$.document_id'), ?5, ?6, ?7
	WHERE EXISTS (SELECT 1 FROM documents WHERE id = json_extract(?4, '$.document_id'))
	  AND NOT EXISTS (SELECT 1 FROM jobs WHERE document_id = json_extract(?4, '$.document_id')
		AND status IN (?5, '` + string(store.JobStatusRunning) + `') AND kind = ?3)
	RETURNING run_after, created_at, updated_at`
)

// PutSummary implements store.SummaryStore.
func (x *Summaries) PutSummary(ctx context.Context, tenantID string, s store.Summary) error {
	if !s.Kind.Valid() {
		return fmt.Errorf("put the summary of document %s: kind %q isn't one of the summary kinds",
			s.DocumentID, s.Kind)
	}
	entities, claims, descriptions, err := summaryLists(s)
	if err != nil {
		return fmt.Errorf("put the summary of document %s: %w", s.DocumentID, err)
	}
	written := s.WrittenAt
	if written.IsZero() {
		written = time.Now()
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("put the summary of document %s: begin: %w", s.DocumentID, err)
	}
	defer tx.Rollback()
	var current bool
	if err := tx.QueryRowContext(ctx, summaryGuardSQL, s.DocumentID, tenantID, s.ExtractionID,
		store.DocStateFetched).Scan(&current); err != nil {
		return fmt.Errorf("put the summary of document %s: read the document: %w", s.DocumentID, err)
	}
	if !current {
		return fmt.Errorf("put the summary of document %s: %w", s.DocumentID, store.ErrSuperseded)
	}
	if _, err := tx.ExecContext(ctx, putSummarySQL, s.DocumentID, s.ExtractionID, string(s.Kind), s.Site, s.Author,
		s.About, entities, claims, descriptions, s.Model, s.PromptVersion, s.InputHash, formatTime(written)); err != nil {
		return fmt.Errorf("put the summary of document %s: %w", s.DocumentID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("put the summary of document %s: commit: %w", s.DocumentID, err)
	}
	return nil
}

// summaryLists are s's lists as document_summaries keeps them, one item a
// line. An item holding a line break would split in two when read back,
// so it is an error; the writer's decoding refuses control characters.
func summaryLists(s store.Summary) (entities, claims, descriptions string, err error) {
	join := func(field string, items []string) (string, error) {
		for _, it := range items {
			if strings.ContainsAny(it, "\r\n") {
				return "", fmt.Errorf("an item of its %s holds a line break", field)
			}
		}
		return strings.Join(items, "\n"), nil
	}
	if entities, err = join("entities", s.Entities); err != nil {
		return "", "", "", err
	}
	if claims, err = join("claims", s.Claims); err != nil {
		return "", "", "", err
	}
	if descriptions, err = join("descriptions", s.Descriptions); err != nil {
		return "", "", "", err
	}
	return entities, claims, descriptions, nil
}

// RecordSummaryFailure implements store.SummaryStore.
func (x *Summaries) RecordSummaryFailure(ctx context.Context, tenantID, documentID string,
	f store.SummaryFailure) error {
	at := f.At
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := x.db.ExecContext(ctx, recordSummaryFailureSQL, documentID, f.ExtractionID, f.Model,
		f.PromptVersion, formatTime(at), f.Error, tenantID); err != nil {
		return fmt.Errorf("record the failed summary of document %s: %w", documentID, err)
	}
	return nil
}

// GetSummary implements store.SummaryStore.
func (x *Summaries) GetSummary(ctx context.Context, tenantID, documentID string) (*store.StoredSummary, error) {
	var (
		extraction, writtenAt                                   sql.NullString
		failedExt, failedModel, failedPrompt, failedAt, failure sql.NullString
		entities, claims, descriptions                          string
		owed                                                    bool
		s                                                       = store.Summary{DocumentID: documentID}
	)
	err := x.db.QueryRowContext(ctx, getSummarySQL, documentID, tenantID).Scan(&extraction, &s.Kind, &s.Site,
		&s.Author, &s.About, &entities, &claims, &descriptions, &s.Model, &s.PromptVersion, &s.InputHash, &writtenAt,
		&owed, &failedExt, &failedModel, &failedPrompt, &failedAt, &failure)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("summary of document %s: %w", documentID, store.ErrNotFound)
	case err != nil:
		return nil, fmt.Errorf("summary of document %s: %w", documentID, err)
	}
	out := &store.StoredSummary{Owed: owed}
	if extraction.Valid {
		s.ExtractionID = extraction.String
		s.Entities, s.Claims, s.Descriptions = splitLines(entities), splitLines(claims), splitLines(descriptions)
		if s.WrittenAt, err = parseNullTime(writtenAt); err != nil {
			return nil, fmt.Errorf("summary of document %s: written_at: %w", documentID, err)
		}
		out.Summary = &s
	}
	if failedAt.Valid {
		at, err := parseTime(failedAt.String)
		if err != nil {
			return nil, fmt.Errorf("summary of document %s: failed_at: %w", documentID, err)
		}
		out.Failure = &store.SummaryFailure{ExtractionID: failedExt.String, At: at, Error: failure.String,
			SummaryWriter: store.SummaryWriter{Model: failedModel.String, PromptVersion: failedPrompt.String}}
	}
	return out, nil
}

// splitLines are the items of a one-a-line list, none for an empty one.
func splitLines(list string) []string {
	if list == "" {
		return nil
	}
	return strings.Split(list, "\n")
}

// CountSummaries implements store.SummaryStore.
func (x *Summaries) CountSummaries(ctx context.Context, tenantID string, w store.SummaryWriter) (
	store.SummaryCounts, error) {
	var n store.SummaryCounts
	if err := x.db.QueryRowContext(ctx, countSummariesSQL, tenantID, store.DocStateFetched, w.Model,
		w.PromptVersion).Scan(&n.Written, &n.ToWrite, &n.Outdated, &n.Failed, &n.NotThePage); err != nil {
		return store.SummaryCounts{}, fmt.Errorf("count the summaries: %w", err)
	}
	return n, nil
}

// OweSummaries implements store.SummaryStore.
func (x *Summaries) OweSummaries(ctx context.Context, tenantID string, scope store.SummaryScope,
	w store.SummaryWriter) (int, error) {
	if !scope.Valid() {
		return 0, fmt.Errorf("owe summaries: scope %q must be %s or %s", scope, store.SummaryScopeOutdated,
			store.SummaryScopeAll)
	}
	query, args := oweOutdatedSQL, []any{tenantID, store.DocStateFetched, w.Model, w.PromptVersion}
	if scope == store.SummaryScopeAll {
		query, args = oweAllSQL, args[:2]
	}
	res, err := x.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("owe summaries: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("owe summaries: %w", err)
	}
	return int(n), nil
}

// summaryToQueue is a document to queue a summarize job for, and the
// extraction it is to read.
type summaryToQueue struct {
	documentID, extractionID string
}

// EnqueueSummarize implements store.SummaryStore. The documents are read
// first, outside the write lock, as many as the limits could queue; the
// transaction, which takes the lock at BEGIN, counts the jobs queued
// again and inserts a job only for a document that still has none, so a
// read that went stale queues nothing twice.
func (x *Summaries) EnqueueSummarize(ctx context.Context, tenantID string, w store.SummaryWriter,
	limits store.SummarizeLimits) (int, error) {
	next, err := x.nextToSummarize(ctx, tenantID, w, limits.FailedWait, limits.InFlight+limits.Waiting)
	if err != nil || len(next) == 0 {
		return 0, err
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("queue summaries: begin: %w", err)
	}
	defer tx.Rollback()
	var running, pending, due int
	if err := tx.QueryRowContext(ctx, summarizeQueuedSQL, store.JobStatusRunning, store.JobStatusPending,
		store.JobKindSummarize, formatTime(time.Now())).Scan(&running, &pending, &due); err != nil {
		return 0, fmt.Errorf("queue summaries: count the jobs queued: %w", err)
	}
	n := min(limits.InFlight-running-due, limits.InFlight+limits.Waiting-running-pending)
	runAfter := runNoSooner(limits.NotBefore, time.Now())
	queued := 0
	for _, d := range next {
		if queued >= n {
			break
		}
		payload, err := json.Marshal(store.DocumentJobPayload{DocumentID: d.documentID, ExtractionID: d.extractionID})
		if err != nil {
			return 0, fmt.Errorf("queue the summary of document %s: %w", d.documentID, err)
		}
		job := &store.Job{TenantID: tenantID, Kind: store.JobKindSummarize, Payload: payload, RunAfter: runAfter}
		switch err := insertJobWith(ctx, tx, insertSummarizeJobSQL, job); {
		case errors.Is(err, sql.ErrNoRows):
			continue // queued, or deleted, since it was read
		case err != nil:
			return 0, fmt.Errorf("queue the summary of document %s: %w", d.documentID, err)
		}
		queued++
	}
	var wake []store.JobKind
	if queued > 0 && runAfter.IsZero() {
		wake = []store.JobKind{store.JobKindSummarize}
	}
	if err := x.db.commitNotify(tx, wake...); err != nil {
		return 0, fmt.Errorf("queue summaries: commit: %w", err)
	}
	return queued, nil
}

// runNoSooner is the run_after of a job to run no sooner than t: zero (now)
// when t isn't after now, else t rounded up to the millisecond the jobs
// table keeps, since a time cut down to it could be claimed a moment
// before t. Both are read as wall time, the table's clock, whatever
// monotonic readings they carry.
func runNoSooner(t, now time.Time) time.Time {
	t, now = t.Round(0), now.Round(0)
	if !t.After(now) {
		return time.Time{}
	}
	if cut := t.Truncate(time.Millisecond); cut.Before(t) {
		return cut.Add(time.Millisecond)
	}
	return t
}

// ToSummarize implements store.SummaryStore.
func (x *Summaries) ToSummarize(ctx context.Context, tenantID string, w store.SummaryWriter, failedWait time.Duration,
	limit int) ([]store.SummaryTarget, error) {
	next, err := x.nextToSummarize(ctx, tenantID, w, failedWait, limit)
	if err != nil {
		return nil, err
	}
	out := make([]store.SummaryTarget, len(next))
	for i, d := range next {
		out[i] = store.SummaryTarget{DocumentID: d.documentID, ExtractionID: d.extractionID}
	}
	return out, nil
}

// nextToSummarize reads the next documents to queue a summary of, at most
// most of them, skipping those whose summary failed for good within
// failedWait.
func (x *Summaries) nextToSummarize(ctx context.Context, tenantID string, w store.SummaryWriter,
	failedWait time.Duration, most int) ([]summaryToQueue, error) {
	if most <= 0 {
		return nil, nil
	}
	rows, err := x.db.QueryContext(ctx, nextToSummarizeSQL, tenantID, store.DocStateFetched, w.Model,
		w.PromptVersion, formatTime(time.Now().Add(-failedWait)), store.JobStatusPending,
		store.JobStatusRunning, store.JobKindSummarize, most)
	if err != nil {
		return nil, fmt.Errorf("queue summaries: read the next documents: %w", err)
	}
	defer rows.Close()
	var out []summaryToQueue
	for rows.Next() {
		var d summaryToQueue
		if err := rows.Scan(&d.documentID, &d.extractionID); err != nil {
			return nil, fmt.Errorf("queue summaries: read the next documents: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue summaries: read the next documents: %w", err)
	}
	return out, nil
}
