package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
)

// Jobs implements store.JobStore. Single-table queue backed by SQLite.
//
// Claim semantics: an atomic UPDATE ... WHERE status='pending' AND id=(...)
// inside a transaction ensures one job is claimed by exactly one worker even
// under concurrency. Multi-worker is tested even though M0 runs only one.
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

func (s *Jobs) Enqueue(ctx context.Context, j *store.Job) error {
	return insertJob(ctx, s.db, j)
}

// rowQuerier is what insertJob needs from *sql.DB or *sql.Tx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// insertJob applies the queue's defaults to j (ID, payload, status,
// run_after), inserts it through q, and fills in the timestamps the database
// assigned. Every job insert goes through here, inside a transaction or not.
func insertJob(ctx context.Context, q rowQuerier, j *store.Job) error {
	if j.TenantID == "" {
		return fmt.Errorf("jobs: tenant_id required")
	}
	if j.Kind == "" {
		return fmt.Errorf("jobs: kind required")
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
	err := q.QueryRowContext(ctx, `
		INSERT INTO jobs (id, tenant_id, kind, payload, status, attempts, run_after)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING run_after, created_at, updated_at`,
		j.ID, j.TenantID, j.Kind, string(j.Payload),
		j.Status, j.Attempts, formatTime(j.RunAfter),
	).Scan(&runAfter, &createdAt, &updatedAt)
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

	// args: 2 for the SET clause (status, started_at), 2 for the SELECT
	// predicate (status, run_after), then the optional kinds.
	args := []any{store.JobStatusRunning, now, store.JobStatusPending, now}
	kindSQL := ""
	if len(kinds) > 0 {
		kindSQL = " AND kind IN (" + placeholders(len(kinds)) + ")"
		args = appendArgs(args, kinds)
	}

	q := `UPDATE jobs SET status = ?, started_at = ?, attempts = attempts + 1
	      WHERE id = (
	          SELECT id FROM jobs
	          WHERE status = ? AND run_after <= ?` + kindSQL + `
	          ORDER BY created_at LIMIT 1
	      )
	      RETURNING ` + jobColumns

	row := s.db.QueryRowContext(ctx, q, args...)
	job, err := scanJob(row)
	if errors.Is(err, store.ErrNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	return job, nil
}

func (s *Jobs) MarkDone(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ? WHERE id = ? AND status = ?`,
		store.JobStatusDone, id, store.JobStatusRunning)
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
		// Exponential backoff: 30 * 2^attempts seconds, capped at 1 hour.
		backoff := 30 * time.Duration(math.Pow(2, float64(job.Attempts))) * time.Second
		if backoff > time.Hour {
			backoff = time.Hour
		}
		runAfter := time.Now().UTC().Add(backoff)
		res, err = s.db.ExecContext(ctx, `
			UPDATE jobs SET status = ?, last_error = ?, run_after = ? WHERE id = ? AND status = ?`,
			store.JobStatusPending, errMsg, formatTime(runAfter), id, store.JobStatusRunning)
	} else {
		res, err = s.db.ExecContext(ctx, `
			UPDATE jobs SET status = ?, last_error = ? WHERE id = ? AND status = ?`,
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

func (s *Jobs) Requeue(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = ?, attempts = max(attempts - 1, 0), started_at = NULL, run_after = ?
		WHERE id = ? AND status = ?`,
		store.JobStatusPending, formatTime(time.Now().UTC()), id, store.JobStatusRunning)
	if err != nil {
		return fmt.Errorf("requeue job: %w", err)
	}
	return s.ensureTransitioned(ctx, res, id)
}

// orphanExhaustedError is the last_error of an orphan with no attempts left.
const orphanExhaustedError = "the daemon exited while this job was running, and it has no attempts left"

func (s *Jobs) RecoverOrphans(ctx context.Context, kinds []store.JobKind) ([]*store.Job, int, error) {
	if len(kinds) == 0 {
		return nil, 0, errors.New("recover orphaned jobs: kinds required")
	}
	kindSQL := "kind IN (" + placeholders(len(kinds)) + ")"

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("begin orphan recovery: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Exhausted orphans first. The first statement writes, so the
	// transaction takes the write lock outright instead of upgrading from a
	// read lock (see decisions.md "Job queue claim via atomic UPDATE ...
	// RETURNING").
	failedArgs := appendArgs([]any{store.JobStatusFailed, orphanExhaustedError,
		store.JobStatusRunning, s.MaxAttempts}, kinds)
	rows, err := tx.QueryContext(ctx, `
		UPDATE jobs SET status = ?, last_error = ?
		WHERE status = ? AND attempts >= ? AND `+kindSQL+`
		RETURNING `+jobColumns, failedArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("fail exhausted orphans: %w", err)
	}
	defer rows.Close()
	failed, err := scanJobRows(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("fail exhausted orphans: %w", err)
	}

	requeueArgs := appendArgs([]any{store.JobStatusPending, formatTime(time.Now().UTC()),
		store.JobStatusRunning}, kinds)
	res, err := tx.ExecContext(ctx, `
		UPDATE jobs SET status = ?, started_at = NULL, run_after = ?
		WHERE status = ? AND `+kindSQL, requeueArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("requeue orphans: %w", err)
	}
	requeued, err := res.RowsAffected()
	if err != nil {
		return nil, 0, fmt.Errorf("requeue orphans: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("commit orphan recovery: %w", err)
	}
	return failed, int(requeued), nil
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
	job, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	return notRunning(job)
}

func notRunning(job *store.Job) error {
	return fmt.Errorf("job %s is %s: %w", job.ID, job.Status, store.ErrNotRunning)
}

// ListWithDoc joins each job to its document through
// json_extract(payload, '$.document_id'), and the document to its current
// extraction for the markdown path, so the CLI needs no round-trip per row.
func (s *Jobs) ListWithDoc(ctx context.Context, tenantID string, opts store.ListJobsOpts) ([]store.JobWithDoc, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	q := "SELECT " + qualify("j", jobColumns) + ", COALESCE(d.url, '') AS doc_url, " +
		"COALESCE(d.title, '') AS doc_title, COALESCE(e.markdown_path, '') AS markdown_path " +
		"FROM jobs j " +
		"LEFT JOIN documents d ON d.id = json_extract(j.payload, '$.document_id') " +
		"LEFT JOIN document_extractions e ON e.id = d.current_extraction_id " +
		"WHERE j.tenant_id = ?"
	args := []any{tenantID}
	if opts.Status != "" {
		q += " AND j.status = ?"
		args = append(args, opts.Status)
	}
	if opts.Kind != "" {
		q += " AND j.kind = ?"
		args = append(args, opts.Kind)
	}
	q += " ORDER BY j.updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs with doc: %w", err)
	}
	defer rows.Close()
	var out []store.JobWithDoc
	for rows.Next() {
		var item store.JobWithDoc
		if item.Job, err = scanJob(rows, &item.URL, &item.Title, &item.MarkdownPath); err != nil {
			return nil, fmt.Errorf("list jobs with doc: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list jobs with doc: %w", err)
	}
	return out, nil
}

// DeleteByStatus removes every job for the tenant in the given status,
// which must be a finished one (done or failed): deleting pending or
// running work would leave its document in pending with no job to move it
// on. Returns how many rows were deleted.
func (s *Jobs) DeleteByStatus(ctx context.Context, tenantID string, status store.JobStatus) (int64, error) {
	if !status.IsFinished() {
		return 0, fmt.Errorf("delete jobs: status %q is not finished; only done or failed jobs can be deleted", status)
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM jobs WHERE tenant_id = ? AND status = ?`,
		tenantID, status)
	if err != nil {
		return 0, fmt.Errorf("delete jobs by status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete jobs by status: %w", err)
	}
	return n, nil
}

// PruneOlderThan deletes the tenant's finished (done or failed) jobs whose
// updated_at is before the cutoff. Used by the retention path so the jobs
// table doesn't grow without bound — even successful runs leave a row per
// fetch + per index, so a corpus of 5k bookmarks adds 10k rows per pass.
// Pending and running jobs are live work and are kept however old they are.
func (s *Jobs) PruneOlderThan(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM jobs WHERE tenant_id = ? AND status IN (?, ?) AND updated_at < ?`,
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
// Cost: O(N rows in window) — well-indexed via (status, run_after,
// created_at). Becomes slow only if the jobs table grows huge without
// pruning; users can `curio jobs prune` to mitigate.
func (s *Jobs) MetricsByKind(ctx context.Context, tenantID string, window time.Duration) ([]store.KindMetrics, error) {
	cutoff := time.Now().UTC().Add(-window)

	// Two CTEs:
	//   durations: every terminal job in the window with its run duration
	//   done_ranked: durations of successful jobs ranked within their kind
	// Per-kind aggregation does scalar subqueries against done_ranked for
	// mean / p50 / p95 / p99. We compute percentiles only over successful
	// runs because failed-job durations are dominated by retry backoff and
	// MaxAttempts × timeout, not actual work time.
	const q = `
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

	rows, err := s.db.QueryContext(ctx, q, tenantID, formatTime(cutoff))
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

	// Layer in-flight info on top via a second cheap query. "Running"
	// rows aren't bounded by the window — they're "right now."
	// "Oldest running" is the time since started_at, not updated_at,
	// because updated_at isn't touched once the job goes running (the
	// trigger fires on UPDATE but ClaimNext is the only writer that
	// gets it there). started_at is the truthful "running since" time.
	const inflightQ = `
	SELECT kind,
	       count(*) AS running,
	       coalesce(
	         max((julianday('now') - julianday(coalesce(started_at, updated_at))) * 86400),
	         0
	       ) AS oldest_running_seconds
	FROM jobs
	WHERE tenant_id = ? AND status = 'running'
	GROUP BY kind`

	iRows, err := s.db.QueryContext(ctx, inflightQ, tenantID)
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
	kinds := make([]store.JobKind, 0, len(out))
	for k := range out {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	result := make([]store.KindMetrics, 0, len(kinds))
	for _, k := range kinds {
		result = append(result, *out[k])
	}
	return result, nil
}

func (s *Jobs) CountByStatus(ctx context.Context, tenantID string) (map[store.JobStatus]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT status, count(*) FROM jobs WHERE tenant_id = ? GROUP BY status`, tenantID)
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
