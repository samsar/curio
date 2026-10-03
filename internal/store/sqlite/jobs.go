package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
)

// Jobs implements store.JobStore: a single-table queue in SQLite.
//
// Claim semantics: ClaimNext is one autocommit UPDATE ... WHERE id = (SELECT
// ... LIMIT 1) RETURNING. SQLite runs it under its write lock, so each job
// goes to exactly one of the daemon's many worker goroutines; see ClaimNext.
type Jobs struct {
	db *DB

	// MaxAttempts before a failed job is marked permanently failed instead
	// of re-queued. Exported for tests.
	MaxAttempts int
}

var _ store.JobStore = (*Jobs)(nil)

func NewJobs(db *DB) *Jobs {
	return &Jobs{db: db, MaxAttempts: 5}
}

// AttemptLimit implements store.JobStore: MaxAttempts.
func (s *Jobs) AttemptLimit() int { return s.MaxAttempts }

func (s *Jobs) Enqueue(ctx context.Context, j *store.Job) error {
	if err := insertJob(ctx, s.db, j); err != nil {
		return err
	}
	if j.Status == store.JobStatusPending {
		s.db.enqueued.notify(j.Kind)
	}
	return nil
}

// The statements of EnqueueOnce. The pending check names the tenant as
// +tenant_id: written plainly, SQLite (curio never runs ANALYZE) seeks
// idx_jobs_tenant_status_updated and walks every pending job of the
// tenant, thousands during an import; this way it seeks idx_jobs_claim,
// whose (status, kind) prefix holds only the pending jobs of the kind.
const (
	// insertJobOnceSQL is insertJobSQL unless the tenant has a pending job
	// of the kind: then it inserts nothing and returns no row.
	insertJobOnceSQL = `
	INSERT INTO jobs (id, tenant_id, kind, payload, document_id, status, attempts, run_after)
	SELECT ?1, ?2, ?3, ?4, json_extract(?4, '$.document_id'), ?5, ?6, ?7
	WHERE NOT EXISTS (SELECT 1 FROM jobs WHERE status = ?5 AND kind = ?3 AND +tenant_id = ?2)
	RETURNING run_after, created_at, updated_at`
	// pendingJobSQL is the tenant's pending job of a kind that runs first.
	// Its args are the status, the kind and the tenant.
	pendingJobSQL = `SELECT ` + jobColumns + ` FROM jobs
	WHERE status = ? AND kind = ? AND +tenant_id = ?
	ORDER BY run_after, created_at LIMIT 1`
)

// EnqueueOnce implements store.JobQueue. The transaction begins
// IMMEDIATE (see DB), so no claim or enqueue lands between the check and
// the read of the job it found.
func (s *Jobs) EnqueueOnce(ctx context.Context, j *store.Job) (bool, error) {
	if j.Status != "" && j.Status != store.JobStatusPending {
		return false, fmt.Errorf("enqueue once: a %s job is not queued", j.Status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("enqueue once: begin: %w", err)
	}
	defer tx.Rollback()
	err = insertJobWith(ctx, tx, insertJobOnceSQL, j)
	queued := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		var pending *store.Job
		if pending, err = scanJob(tx.QueryRowContext(ctx, pendingJobSQL, store.JobStatusPending, j.Kind, j.TenantID)); err == nil {
			*j = *pending
		}
	}
	if err != nil {
		return false, fmt.Errorf("enqueue once: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("enqueue once: commit: %w", err)
	}
	if queued {
		s.db.enqueued.notify(j.Kind)
	}
	return queued, nil
}

// Enqueued implements store.JobQueue. Every enqueue path in this package
// (Jobs, Documents, Bookmarks) signals through the *DB they share.
func (s *Jobs) Enqueued(kinds []store.JobKind) <-chan struct{} {
	return s.db.enqueued.wait(kinds)
}

// rowQuerier reads through *sql.DB or *sql.Tx alike.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// insertJobSQL inserts a job. document_id is derived from the payload
// (param 4) by the rule migration 007 backfilled existing rows with, so the
// column can't disagree with the payload the API returns, and any JSON
// payload still inserts: one that names no document leaves it NULL.
const insertJobSQL = `
	INSERT INTO jobs (id, tenant_id, kind, payload, document_id, status, attempts, run_after)
	VALUES (?1, ?2, ?3, ?4, json_extract(?4, '$.document_id'), ?5, ?6, ?7)
	RETURNING run_after, created_at, updated_at`

// insertJob applies the queue's defaults to j (ID, payload, status,
// run_after), inserts it through q, and fills in the timestamps the database
// assigned. Every job insert goes through here, inside a transaction or not.
// A payload naming a document that doesn't exist is an error wrapping
// store.ErrNotFound.
func insertJob(ctx context.Context, q rowQuerier, j *store.Job) error {
	return insertJobWith(ctx, q, insertJobSQL, j)
}

// insertJobWith is insertJob with query, a statement taking insertJobSQL's
// arguments and returning its row. One that inserts nothing is an error
// wrapping sql.ErrNoRows.
func insertJobWith(ctx context.Context, q rowQuerier, query string, j *store.Job) error {
	if j.TenantID == "" {
		return errors.New("jobs: tenant_id required")
	}
	if j.Kind == "" {
		return errors.New("jobs: kind required")
	}
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	if len(j.Payload) == 0 {
		j.Payload = []byte("{}")
	}
	if j.Status == "" {
		j.Status = store.JobStatusPending
	}
	if j.RunAfter.IsZero() {
		j.RunAfter = time.Now().UTC()
	}

	var runAfter, createdAt, updatedAt string
	err := q.QueryRowContext(ctx, query,
		j.ID, j.TenantID, j.Kind, string(j.Payload),
		j.Status, j.Attempts, formatTime(j.RunAfter),
	).Scan(&runAfter, &createdAt, &updatedAt)
	if isForeignKeyViolation(err) {
		return fmt.Errorf("enqueue %s job: its payload names a document that doesn't exist: %w",
			j.Kind, store.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("enqueue job: %w", err)
	}
	if j.RunAfter, err = parseTime(runAfter); err != nil {
		return err
	}
	if j.CreatedAt, err = parseTime(createdAt); err != nil {
		return err
	}
	j.UpdatedAt, err = parseTime(updatedAt)
	return err
}

// ClaimNext claims one runnable job atomically. Pass kinds to restrict by
// job kind ("fetch", "index", ...). Empty slice means "any kind."
//
// Implemented as a single UPDATE ... WHERE id = (SELECT ...) RETURNING
// statement. SQLite serializes this as one atomic write; no separate
// transaction is needed and concurrent workers don't deadlock on lock
// upgrades from reader to writer.
func (s *Jobs) ClaimNext(ctx context.Context, kinds []store.JobKind) (*store.Job, error) {
	now := formatTime(time.Now().UTC())
	args := appendArgs([]any{store.JobStatusRunning, now, now, store.JobStatusPending, now}, kinds)
	job, err := scanJob(s.db.QueryRowContext(ctx, claimSQL(len(kinds)), args...))
	if errors.Is(err, store.ErrNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	return job, nil
}

// claimSQL is ClaimNext's statement for nKinds kinds, 0 meaning any kind.
// Its args are 3 for the SET clause (status, started_at, updated_at), 2 for
// the predicate (status, run_after), then the kinds.
//
// Jobs are claimed in the order they became runnable. For one kind, which
// is what every daemon pool claims, idx_jobs_claim (status, kind, run_after,
// created_at) serves that order directly: a seek and the first row, however
// many jobs are queued. Several kinds, or any kind, take a sort.
func claimSQL(nKinds int) string {
	kindSQL := ""
	if nKinds > 0 {
		kindSQL = " AND kind IN (" + placeholders(nKinds) + ")"
	}
	return `
	UPDATE jobs SET status = ?, started_at = ?, updated_at = ?, attempts = attempts + 1
	WHERE id = (
		SELECT id FROM jobs
		WHERE status = ? AND run_after <= ?` + kindSQL + `
		ORDER BY run_after, created_at LIMIT 1
	)
	RETURNING ` + jobColumns
}

// markDoneSQL sets a running job done. A done job has nothing left to
// explain, so the error a retry or a deferral left goes.
const markDoneSQL = `
	UPDATE jobs SET status = ?, last_error = NULL, updated_at = ` + sqlNow + `
	WHERE id = ? AND status = ?`

func (s *Jobs) MarkDone(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, markDoneSQL, store.JobStatusDone, id, store.JobStatusRunning)
	if err != nil {
		return fmt.Errorf("mark done: %w", err)
	}
	return s.ensureTransitioned(ctx, res, id)
}

// MarkFailed reads before it writes; that's safe because only the worker
// holding the claim transitions a running job, and the status guard on the
// UPDATE catches anything that slipped in between.
func (s *Jobs) MarkFailed(ctx context.Context, id, errMsg string, retry bool) (bool, error) {
	job, err := s.GetByID(ctx, id)
	if err != nil {
		return false, err
	}
	if job.Status != store.JobStatusRunning {
		return false, notRunning(job)
	}

	permanent := !retry || job.Attempts >= s.MaxAttempts

	var res sql.Result
	if !permanent {
		now := time.Now().UTC()
		res, err = s.db.ExecContext(ctx, `
			UPDATE jobs SET status = ?, last_error = ?, run_after = ?, updated_at = ?
			WHERE id = ? AND status = ?`,
			store.JobStatusPending, errMsg, formatTime(now.Add(retryBackoff(job.Attempts))), formatTime(now),
			id, store.JobStatusRunning)
	} else {
		res, err = s.db.ExecContext(ctx, `
			UPDATE jobs SET status = ?, last_error = ?, updated_at = `+sqlNow+`
			WHERE id = ? AND status = ?`,
			store.JobStatusFailed, errMsg, id, store.JobStatusRunning)
	}
	if err != nil {
		return false, fmt.Errorf("mark failed: %w", err)
	}
	if err := s.ensureTransitioned(ctx, res, id); err != nil {
		return false, err
	}
	return permanent, nil
}

// retryBackoff is how long a job that has failed attempts times waits
// before the next: 30s doubled per attempt, capped at an hour.
func retryBackoff(attempts int) time.Duration {
	const base, limit = 30 * time.Second, time.Hour
	// base<<7 is past the limit already; shifting further could overflow.
	return min(base<<min(max(attempts, 0), 7), limit)
}

func (s *Jobs) Requeue(ctx context.Context, id string) error {
	now := formatTime(time.Now().UTC())
	var kind store.JobKind
	err := s.db.QueryRowContext(ctx, `
		UPDATE jobs SET status = ?, attempts = max(attempts - 1, 0), started_at = NULL,
		                run_after = ?, updated_at = ?
		WHERE id = ? AND status = ?
		RETURNING kind`,
		store.JobStatusPending, now, now, id, store.JobStatusRunning).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return s.notTransitioned(ctx, id)
	}
	if err != nil {
		return fmt.Errorf("requeue job: %w", err)
	}
	s.db.enqueued.notify(kind)
	return nil
}

// deferJobSQL sends a running job back to pending until a set time, with
// the attempt its claim counted refunded and why it waits as its
// last_error. Its args are status, run_after, last_error, updated_at, then
// the job's ID and status.
const deferJobSQL = `
	UPDATE jobs SET status = ?, attempts = max(attempts - 1, 0), started_at = NULL,
	                run_after = ?, last_error = ?, updated_at = ?
	WHERE id = ? AND status = ?`

// Defer implements store.JobQueue. It signals nothing: the job isn't
// runnable before until.
func (s *Jobs) Defer(ctx context.Context, id string, until time.Time, reason string) error {
	res, err := s.db.ExecContext(ctx, deferJobSQL,
		store.JobStatusPending, formatTime(until), reason, formatTime(time.Now().UTC()),
		id, store.JobStatusRunning)
	if err != nil {
		return fmt.Errorf("defer job: %w", err)
	}
	return s.ensureTransitioned(ctx, res, id)
}

// orphanExhaustedError is the last_error of an orphan with no attempts left.
const orphanExhaustedError = "the daemon exited while this job was running, and it has no attempts left"

func (s *Jobs) RecoverOrphans(ctx context.Context, kinds []store.JobKind) ([]*store.Job, int, error) {
	if len(kinds) == 0 {
		return nil, 0, errors.New("recover orphaned jobs: kinds required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("begin orphan recovery: %w", err)
	}
	defer tx.Rollback()

	// Exhausted orphans first: the requeue below takes every running job
	// of these kinds that is left.
	now := formatTime(time.Now().UTC())
	failedArgs := appendArgs([]any{store.JobStatusFailed, orphanExhaustedError, now,
		store.JobStatusRunning, s.MaxAttempts}, kinds)
	rows, err := tx.QueryContext(ctx, failOrphansSQL(len(kinds)), failedArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("fail exhausted orphans: %w", err)
	}
	defer rows.Close()
	failed, err := scanJobRows(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("fail exhausted orphans: %w", err)
	}

	requeueArgs := appendArgs([]any{store.JobStatusPending, now, now,
		store.JobStatusRunning}, kinds)
	res, err := tx.ExecContext(ctx, requeueOrphansSQL(len(kinds)), requeueArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("requeue orphans: %w", err)
	}
	requeued, err := res.RowsAffected()
	if err != nil {
		return nil, 0, fmt.Errorf("requeue orphans: %w", err)
	}

	var wake []store.JobKind
	if requeued > 0 {
		wake = kinds
	}
	if err := s.db.commitNotify(tx, wake...); err != nil {
		return nil, 0, fmt.Errorf("commit orphan recovery: %w", err)
	}
	return failed, int(requeued), nil
}

// failOrphansSQL fails the running jobs of nKinds kinds that have used
// their attempts, returning them. Its args are status, last_error,
// updated_at, then status, the attempts limit and the kinds.
func failOrphansSQL(nKinds int) string {
	return `
	UPDATE jobs SET status = ?, last_error = ?, updated_at = ?
	WHERE status = ? AND attempts >= ? AND kind IN (` + placeholders(nKinds) + `)
	RETURNING ` + jobColumns
}

// requeueOrphansSQL sends the running jobs of nKinds kinds back to pending.
// Its args are status, run_after, updated_at, then status and the kinds.
func requeueOrphansSQL(nKinds int) string {
	return `
	UPDATE jobs SET status = ?, started_at = NULL, run_after = ?, updated_at = ?
	WHERE status = ? AND kind IN (` + placeholders(nKinds) + `)`
}

// ensureTransitioned turns a status-guarded UPDATE that matched no row into
// the reason: the job doesn't exist, or it isn't running.
func (s *Jobs) ensureTransitioned(ctx context.Context, res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("job %s: rows affected: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	return s.notTransitioned(ctx, id)
}

// notTransitioned explains why a status-guarded UPDATE of job id matched no
// row: the job doesn't exist, or it isn't running.
func (s *Jobs) notTransitioned(ctx context.Context, id string) error {
	job, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	return notRunning(job)
}

func notRunning(job *store.Job) error {
	return fmt.Errorf("job %s is %s: %w", job.ID, job.Status, store.ErrNotRunning)
}

// ListWithDoc joins each job to its document through jobs.document_id, and
// the document to its current extraction for the markdown path, so the CLI
// needs no round-trip per row.
func (s *Jobs) ListWithDoc(ctx context.Context, tenantID string, opts store.ListJobsOpts) ([]store.JobWithDoc, error) {
	q, args := listJobsQuery(tenantID, opts)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs with doc: %w", err)
	}
	defer rows.Close()
	var out []store.JobWithDoc
	for rows.Next() {
		item, err := scanJobWithDoc(rows)
		if err != nil {
			return nil, fmt.Errorf("list jobs with doc: %w", err)
		}
		out = append(out, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list jobs with doc: %w", err)
	}
	return out, nil
}

// GetWithDoc reads the job through the same SELECT and joins as
// ListWithDoc, so a job looks the same either way.
func (s *Jobs) GetWithDoc(ctx context.Context, tenantID, id string) (*store.JobWithDoc, error) {
	q, args := getJobWithDocQuery(tenantID, id)
	job, err := scanJobWithDoc(s.db.QueryRowContext(ctx, q, args...))
	if err != nil {
		return nil, fmt.Errorf("get job %s: %w", id, err)
	}
	return job, nil
}

// jobWithDocFrom selects jobs, each with its document's URL and title and
// its current extraction's markdown path, in the columns scanJobWithDoc
// reads. Callers add the WHERE.
var jobWithDocFrom = "SELECT " + qualify("j", jobColumns) + ", COALESCE(d.url, '') AS doc_url, " +
	"COALESCE(d.title, '') AS doc_title, COALESCE(e.markdown_path, '') AS markdown_path " +
	"FROM jobs j " +
	"LEFT JOIN documents d ON d.id = j.document_id " +
	"LEFT JOIN document_extractions e ON e.id = d.current_extraction_id"

// scanJobWithDoc scans a row of jobWithDocFrom.
func scanJobWithDoc(row interface{ Scan(...any) error }) (*store.JobWithDoc, error) {
	var item store.JobWithDoc
	var err error
	if item.Job, err = scanJob(row, &item.URL, &item.Title, &item.MarkdownPath); err != nil {
		return nil, err
	}
	return &item, nil
}

// getJobWithDocQuery builds GetWithDoc's query: a point lookup on the jobs
// primary key.
func getJobWithDocQuery(tenantID, id string) (string, []any) {
	return jobWithDocFrom + " WHERE j.tenant_id = ? AND j.id = ?", []any{tenantID, id}
}

// listJobsQuery builds ListWithDoc's query. It walks
// idx_jobs_tenant_status_updated when filtered by status, and
// idx_jobs_tenant_updated otherwise, in (updated_at, id) order from
// opts.After, so it stops at the limit instead of sorting every tenant job.
//
// A document's jobs are the exception: idx_jobs_document seeks them and
// they are sorted, a handful of rows. The tenant term is written
// +j.tenant_id then, because the unary plus keeps SQLite off the tenant
// indexes: curio never runs ANALYZE, and without statistics SQLite takes
// the index that serves the ORDER BY and walks every tenant job to find
// the document's few (3 to 16 ms at 12k jobs, against 0.03 ms).
func listJobsQuery(tenantID string, opts store.ListJobsOpts) (string, []any) {
	clauses := []string{"j.tenant_id = ?"}
	args := []any{tenantID}
	if opts.DocumentID != "" {
		clauses = []string{"+j.tenant_id = ?", "j.document_id = ?"}
		args = append(args, opts.DocumentID)
	}
	if opts.Status != "" {
		clauses = append(clauses, "j.status = ?")
		args = append(args, opts.Status)
	}
	if opts.Kind != "" {
		clauses = append(clauses, "j.kind = ?")
		args = append(args, opts.Kind)
	}
	if !opts.After.IsZero() {
		pred, predArgs := keysetAfter("j.updated_at", "j.id", opts.After)
		clauses = append(clauses, pred)
		args = append(args, predArgs...)
	}
	q := jobWithDocFrom + " WHERE " + strings.Join(clauses, " AND ") + " ORDER BY j.updated_at DESC, j.id DESC LIMIT ?"
	return q, append(args, listLimit(opts.Limit))
}

// DeleteByStatus removes every job for the tenant in the given status,
// which must be a finished one (done or failed): deleting pending or
// running work would leave its document in pending with no job to move it
// on. Returns how many rows were deleted.
func (s *Jobs) DeleteByStatus(ctx context.Context, tenantID string, status store.JobStatus) (int64, error) {
	if !status.IsFinished() {
		return 0, fmt.Errorf("delete jobs: status %q is not finished; only done or failed jobs can be deleted", status)
	}
	res, err := s.db.ExecContext(ctx, deleteJobsByStatusSQL, tenantID, status)
	if err != nil {
		return 0, fmt.Errorf("delete jobs by status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete jobs by status: %w", err)
	}
	return n, nil
}

// Retention statements. Both walk idx_jobs_tenant_status_updated.
const (
	deleteJobsByStatusSQL = `DELETE FROM jobs WHERE tenant_id = ? AND status = ?`
	pruneJobsSQL          = `DELETE FROM jobs WHERE tenant_id = ? AND status IN (?, ?) AND updated_at < ?`
)

// PruneOlderThan deletes the tenant's finished (done or failed) jobs whose
// updated_at is before the cutoff. Used by the retention path so the jobs
// table doesn't grow without bound — even successful runs leave a row per
// fetch + per index, so a corpus of 5k bookmarks adds 10k rows per pass.
// Pending and running jobs are live work and are kept however old they are.
func (s *Jobs) PruneOlderThan(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, pruneJobsSQL,
		tenantID, store.JobStatusDone, store.JobStatusFailed, formatTime(before.UTC()))
	if err != nil {
		return 0, fmt.Errorf("prune jobs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune jobs: %w", err)
	}
	return n, nil
}

// MetricsByKind returns one store.KindMetrics per job kind for the given
// window. Uses window functions to compute percentiles; SQLite 3.25+ is
// fine (we're on much newer). Counts cover done+failed jobs whose
// updated_at falls in the window; Running counts ignore the window (it's
// "right now").
//
// Cost: O(jobs finished in the window), read as a range of
// idx_jobs_tenant_status_updated however many jobs the table holds.
func (s *Jobs) MetricsByKind(ctx context.Context, tenantID string, window time.Duration) ([]store.KindMetrics, error) {
	cutoff := time.Now().UTC().Add(-window)
	rows, err := s.db.QueryContext(ctx, metricsDurationsSQL, tenantID, formatTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("metrics by kind: %w", err)
	}
	defer rows.Close()

	out := map[store.JobKind]*store.KindMetrics{}
	for rows.Next() {
		var m store.KindMetrics
		if err := rows.Scan(&m.Kind, &m.Count, &m.Failed, &m.MeanMS, &m.P50MS, &m.P95MS, &m.P99MS); err != nil {
			return nil, fmt.Errorf("scan metrics: %w", err)
		}
		out[m.Kind] = &m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Layer in-flight info on top via a second cheap query.
	iRows, err := s.db.QueryContext(ctx, metricsInflightSQL, tenantID)
	if err != nil {
		return nil, fmt.Errorf("metrics in-flight: %w", err)
	}
	defer iRows.Close()
	for iRows.Next() {
		var kind store.JobKind
		var running int
		var oldest float64
		if err := iRows.Scan(&kind, &running, &oldest); err != nil {
			return nil, fmt.Errorf("scan in-flight: %w", err)
		}
		m, ok := out[kind]
		if !ok {
			m = &store.KindMetrics{Kind: kind}
			out[kind] = m
		}
		m.Running = running
		m.OldestRunningSeconds = int(oldest)
	}
	if err := iRows.Err(); err != nil {
		return nil, err
	}

	// Sort by kind for deterministic output.
	result := make([]store.KindMetrics, 0, len(out))
	for _, k := range slices.Sorted(maps.Keys(out)) {
		result = append(result, *out[k])
	}
	return result, nil
}

// metricsDurationsSQL aggregates the tenant's jobs that finished in the
// window, with two CTEs:
//
//	durations: every terminal job in the window with its run duration
//	done_ranked: durations of successful jobs ranked within their kind
//
// Per-kind aggregation does scalar subqueries against done_ranked for
// mean / p50 / p95 / p99. We compute percentiles only over successful
// runs because failed-job durations are dominated by retry backoff and
// MaxAttempts × timeout, not actual work time.
const metricsDurationsSQL = `
WITH durations AS (
	SELECT kind, status,
	       (julianday(updated_at) - julianday(started_at)) * 86400000.0 AS ms
	FROM jobs
	WHERE tenant_id = ?
	  AND status IN ('done','failed')
	  AND updated_at > ?
	  AND started_at IS NOT NULL  -- pre-migration rows lack this
),
done_ranked AS (
	SELECT kind, ms,
	       percent_rank() OVER (PARTITION BY kind ORDER BY ms) AS pct
	FROM durations WHERE status = 'done'
)
SELECT d.kind,
       count(*) AS total,
       sum(CASE WHEN d.status='failed' THEN 1 ELSE 0 END) AS failed,
       coalesce((SELECT avg(ms) FROM done_ranked WHERE kind = d.kind), 0) AS mean_ms,
       coalesce((SELECT min(ms) FROM done_ranked WHERE kind = d.kind AND pct >= 0.50), 0) AS p50,
       coalesce((SELECT min(ms) FROM done_ranked WHERE kind = d.kind AND pct >= 0.95), 0) AS p95,
       coalesce((SELECT min(ms) FROM done_ranked WHERE kind = d.kind AND pct >= 0.99), 0) AS p99
FROM durations d
GROUP BY d.kind`

// metricsInflightSQL counts the tenant's running jobs per kind, whatever
// the window. "Oldest running" is the time since started_at, which
// ClaimNext sets, the truthful "running since" time. updated_at is the
// fallback for rows from before migration 003 added started_at.
const metricsInflightSQL = `
SELECT kind,
       count(*) AS running,
       coalesce(
         max((julianday('now') - julianday(coalesce(started_at, updated_at))) * 86400),
         0
       ) AS oldest_running_seconds
FROM jobs
WHERE tenant_id = ? AND status = 'running'
GROUP BY kind`

// countJobsSQL is a covering walk of idx_jobs_tenant_status_updated, which
// yields the rows grouped by status.
const countJobsSQL = `SELECT status, count(*) FROM jobs WHERE tenant_id = ? GROUP BY status`

func (s *Jobs) CountByStatus(ctx context.Context, tenantID string) (map[store.JobStatus]int, error) {
	rows, err := s.db.QueryContext(ctx, countJobsSQL, tenantID)
	if err != nil {
		return nil, fmt.Errorf("count jobs: %w", err)
	}
	defer rows.Close()
	out := map[store.JobStatus]int{}
	for rows.Next() {
		var (
			status store.JobStatus
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("count jobs: %w", err)
		}
		out[status] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count jobs: %w", err)
	}
	return out, nil
}

// queueCountsSQL counts the unfinished jobs per status and kind, across
// tenants, and of those the ones whose run_after is after ?3 (now), with
// the earliest such run_after. It is a covering walk of idx_jobs_claim's
// pending and running ranges, whose entries hold run_after and yield the
// rows grouped, so finished jobs cost it nothing however many pile up.
const queueCountsSQL = `
	SELECT status, kind, count(*),
	       count(CASE WHEN run_after > ?3 THEN 1 END),
	       min(CASE WHEN run_after > ?3 THEN run_after END)
	FROM jobs WHERE status IN (?1, ?2) GROUP BY status, kind`

func (s *Jobs) QueueCounts(ctx context.Context) (map[store.JobKind]store.QueueCount, error) {
	rows, err := s.db.QueryContext(ctx, queueCountsSQL, store.JobStatusPending, store.JobStatusRunning,
		formatTime(time.Now().UTC()))
	if err != nil {
		return nil, fmt.Errorf("count queued jobs: %w", err)
	}
	defer rows.Close()
	out := map[store.JobKind]store.QueueCount{}
	for rows.Next() {
		var (
			status   store.JobStatus
			kind     store.JobKind
			n, later int
			nextDue  sql.NullString
		)
		if err := rows.Scan(&status, &kind, &n, &later, &nextDue); err != nil {
			return nil, fmt.Errorf("count queued jobs: %w", err)
		}
		c := out[kind]
		if status == store.JobStatusRunning {
			c.Running = n
		} else {
			c.Pending, c.DueLater = n, later
			if nextDue.Valid {
				if c.NextDue, err = parseTime(nextDue.String); err != nil {
					return nil, fmt.Errorf("count queued jobs: %w", err)
				}
			}
		}
		out[kind] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count queued jobs: %w", err)
	}
	return out, nil
}

func (s *Jobs) GetByID(ctx context.Context, id string) (*store.Job, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ?", id)
	return scanJob(row)
}

// jobColumns is the column list scanJob expects, in order.
const jobColumns = `id, tenant_id, kind, payload, status, attempts, run_after, last_error, created_at, updated_at`

func scanJobRows(rows *sql.Rows) ([]*store.Job, error) {
	var out []*store.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// appendArgs appends vals to a bind-argument list.
func appendArgs[T ~string](args []any, vals []T) []any {
	for _, v := range vals {
		args = append(args, v)
	}
	return args
}

// scanJob scans jobColumns, then any extra columns a query selects after
// them into extra. A missing row is store.ErrNotFound.
func scanJob(row interface{ Scan(...any) error }, extra ...any) (*store.Job, error) {
	var (
		j                              store.Job
		payload                        string
		lastErr                        sql.NullString
		runAfter, createdAt, updatedAt string
	)
	err := row.Scan(slices.Concat([]any{
		&j.ID, &j.TenantID, &j.Kind, &payload, &j.Status,
		&j.Attempts, &runAfter, &lastErr,
		&createdAt, &updatedAt,
	}, extra)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan job: %w", err)
	}
	j.Payload = []byte(payload)
	j.LastError = nullableString(lastErr)
	if j.RunAfter, err = parseTime(runAfter); err != nil {
		return nil, err
	}
	if j.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if j.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &j, nil
}
