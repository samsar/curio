package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
)

// Jobs implements store.JobQueue. Single-table queue backed by SQLite.
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

var _ store.JobQueue = (*Jobs)(nil)

func NewJobs(db *DB) *Jobs {
	return &Jobs{db: db, MaxAttempts: 5}
}

func (s *Jobs) Enqueue(ctx context.Context, j *store.Job) error {
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	if j.TenantID == "" {
		return fmt.Errorf("jobs: tenant_id required")
	}
	if j.Kind == "" {
		return fmt.Errorf("jobs: kind required")
	}
	if len(j.Payload) == 0 {
		j.Payload = []byte("{}")
	}
	if j.Status == "" {
		j.Status = store.JobStatusPending
	}

	runAfter := j.RunAfter
	if runAfter.IsZero() {
		runAfter = time.Now().UTC()
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs (id, tenant_id, kind, payload, status, attempts, run_after)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.TenantID, j.Kind, string(j.Payload),
		j.Status, j.Attempts, formatTime(runAfter),
	)
	if err != nil {
		return fmt.Errorf("enqueue job: %w", err)
	}
	got, err := s.GetByID(ctx, j.ID)
	if err != nil {
		return err
	}
	j.RunAfter = got.RunAfter
	j.CreatedAt = got.CreatedAt
	j.UpdatedAt = got.UpdatedAt
	return nil
}

// ClaimNext claims one runnable job atomically. Pass kinds to restrict by
// job kind ("fetch", "index", ...). Empty slice means "any kind."
//
// Implemented as a single UPDATE ... WHERE id = (SELECT ...) RETURNING
// statement. SQLite serializes this as one atomic write; no separate
// transaction is needed and concurrent workers don't deadlock on lock
// upgrades from reader to writer.
func (s *Jobs) ClaimNext(ctx context.Context, kinds []string) (*store.Job, error) {
	now := formatTime(time.Now().UTC())

	// args: 2 for the SET clause (status, started_at), 2 for the SELECT
	// predicate (status, run_after), then the optional kinds.
	args := []any{store.JobStatusRunning, now, store.JobStatusPending, now}
	kindSQL := ""
	if len(kinds) > 0 {
		kindSQL = " AND kind IN (" + placeholders(len(kinds)) + ")"
		args = appendStrings(args, kinds)
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

func (s *Jobs) RecoverOrphans(ctx context.Context, kinds []string) ([]*store.Job, int, error) {
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
	failedArgs := appendStrings([]any{store.JobStatusFailed, orphanExhaustedError,
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

	requeueArgs := appendStrings([]any{store.JobStatusPending, formatTime(time.Now().UTC()),
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
		return err
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

// JobWithDoc pairs a Job with its target document's URL, title, and
// current-extraction markdown_path (when present). Two LEFT JOINs:
// jobs → documents via json_extract(payload, '$.document_id'), and
// documents → document_extractions via current_extraction_id. All three
// fields are empty when the join misses (no doc, or doc has no extraction).
type JobWithDoc struct {
	*store.Job
	URL          string
	Title        string
	MarkdownPath string
}

// ListWithDoc is the debug-friendly variant of List: same filters, but
// each row carries the doc URL + title + markdown_path so the CLI
// doesn't have to do an N+1 round-trip.
func (s *Jobs) ListWithDoc(ctx context.Context, tenantID, status, kind string, limit int) ([]JobWithDoc, error) {
	if limit <= 0 {
		limit = 50
	}
	const jobCols = `j.id, j.tenant_id, j.kind, j.payload, j.status, j.attempts, j.run_after, j.last_error, j.created_at, j.updated_at`

	q := "SELECT " + jobCols + ", COALESCE(d.url, '') AS doc_url, COALESCE(d.title, '') AS doc_title, " +
		"COALESCE(e.markdown_path, '') AS markdown_path " +
		"FROM jobs j " +
		"LEFT JOIN documents d ON d.id = json_extract(j.payload, '$.document_id') " +
		"LEFT JOIN document_extractions e ON e.id = d.current_extraction_id " +
		"WHERE j.tenant_id = ?"
	args := []any{tenantID}
	if status != "" {
		q += " AND j.status = ?"
		args = append(args, status)
	}
	if kind != "" {
		q += " AND j.kind = ?"
		args = append(args, kind)
	}
	q += " ORDER BY j.updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs with doc: %w", err)
	}
	defer rows.Close()
	var out []JobWithDoc
	for rows.Next() {
		job, url, title, mdPath, err := scanJobWithDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, JobWithDoc{Job: job, URL: url, Title: title, MarkdownPath: mdPath})
	}
	return out, rows.Err()
}

func scanJobWithDoc(row interface{ Scan(...any) error }) (*store.Job, string, string, string, error) {
	var (
		j                              store.Job
		payload                        string
		lastErr                        sql.NullString
		runAfter, createdAt, updatedAt string
		url, title, mdPath             string
	)
	err := row.Scan(
		&j.ID, &j.TenantID, &j.Kind, &payload, &j.Status,
		&j.Attempts, &runAfter, &lastErr,
		&createdAt, &updatedAt,
		&url, &title, &mdPath,
	)
	if err != nil {
		return nil, "", "", "", fmt.Errorf("scan job with doc: %w", err)
	}
	j.Payload = []byte(payload)
	j.LastError = nullableString(lastErr)
	if j.RunAfter, err = parseTime(runAfter); err != nil {
		return nil, "", "", "", err
	}
	if j.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, "", "", "", err
	}
	if j.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, "", "", "", err
	}
	return &j, url, title, mdPath, nil
}

// List returns recent jobs for a tenant, optionally filtered by status
// and/or kind. Ordered most-recently-created first.
func (s *Jobs) List(ctx context.Context, tenantID, status, kind string, limit int) ([]*store.Job, error) {
	if limit <= 0 {
		limit = 50
	}
	q := "SELECT " + jobColumns + " FROM jobs WHERE tenant_id = ?"
	args := []any{tenantID}
	if status != "" {
		q += " AND status = ?"
		args = append(args, status)
	}
	if kind != "" {
		q += " AND kind = ?"
		args = append(args, kind)
	}
	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	return scanJobRows(rows)
}

// DeleteByStatus removes every job for the tenant in the given status.
// Returns how many rows were deleted. status="" is rejected — there's no
// safe "delete all jobs" path; callers must opt into a specific status.
func (s *Jobs) DeleteByStatus(ctx context.Context, tenantID, status string) (int64, error) {
	if status == "" {
		return 0, fmt.Errorf("delete jobs: status is required (no nuke-all path)")
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM jobs WHERE tenant_id = ? AND status = ?`,
		tenantID, status)
	if err != nil {
		return 0, fmt.Errorf("delete jobs by status: %w", err)
	}
	return res.RowsAffected()
}

// PruneOlderThan deletes every job for the tenant whose updated_at is
// before the given cutoff. Used by the retention path so the jobs table
// doesn't grow without bound — even successful runs leave a row per fetch
// + per index, so a corpus of 5k bookmarks adds 10k rows per pass.
func (s *Jobs) PruneOlderThan(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM jobs WHERE tenant_id = ? AND updated_at < ?`,
		tenantID, formatTime(before.UTC()))
	if err != nil {
		return 0, fmt.Errorf("prune jobs: %w", err)
	}
	return res.RowsAffected()
}

// KindMetrics is the aggregated performance picture for one job kind
// over a rolling window. Times are in milliseconds (computed from
// updated_at - created_at). Failed is the count of jobs in that kind
// whose terminal status was 'failed' in the window.
type KindMetrics struct {
	Kind                 string
	Count                int
	MeanMS               float64
	P50MS                float64
	P95MS                float64
	P99MS                float64
	Failed               int
	Running              int // currently in-flight (status='running'); not bounded by window
	OldestRunningSeconds int // age of the oldest running job
}

// MetricsByKind returns one KindMetrics per job kind for the given
// window. Uses window functions to compute percentiles; SQLite 3.25+ is
// fine (we're on much newer). Counts cover done+failed jobs whose
// updated_at falls in the window; Running counts ignore the window (it's
// "right now").
//
// Cost: O(N rows in window) — well-indexed via (status, run_after,
// created_at). Becomes slow only if the jobs table grows huge without
// pruning; users can `curio jobs prune` to mitigate.
func (s *Jobs) MetricsByKind(ctx context.Context, tenantID string, window time.Duration) ([]KindMetrics, error) {
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

	out := map[string]*KindMetrics{}
	for rows.Next() {
		var m KindMetrics
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
		var kind string
		var running int
		var oldest float64
		if err := iRows.Scan(&kind, &running, &oldest); err != nil {
			return nil, fmt.Errorf("scan in-flight: %w", err)
		}
		m, ok := out[kind]
		if !ok {
			m = &KindMetrics{Kind: kind}
			out[kind] = m
		}
		m.Running = running
		m.OldestRunningSeconds = int(oldest)
	}
	if err := iRows.Err(); err != nil {
		return nil, err
	}

	// Sort by kind for deterministic output.
	kinds := make([]string, 0, len(out))
	for k := range out {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	result := make([]KindMetrics, 0, len(kinds))
	for _, k := range kinds {
		result = append(result, *out[k])
	}
	return result, nil
}

// CountByStatus returns the number of jobs in each status for a tenant.
// Surfaces queue depth via /v1/stats so import progress is visible.
func (s *Jobs) CountByStatus(ctx context.Context, tenantID string) (map[string]int, error) {
	const q = `SELECT status, count(*) FROM jobs WHERE tenant_id = ? GROUP BY status`
	rows, err := s.db.QueryContext(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("count jobs: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
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

func appendStrings(args []any, vals []string) []any {
	for _, v := range vals {
		args = append(args, v)
	}
	return args
}

func scanJob(row interface{ Scan(...any) error }) (*store.Job, error) {
	var (
		j                              store.Job
		payload                        string
		lastErr                        sql.NullString
		runAfter, createdAt, updatedAt string
	)
	err := row.Scan(
		&j.ID, &j.TenantID, &j.Kind, &payload, &j.Status,
		&j.Attempts, &runAfter, &lastErr,
		&createdAt, &updatedAt,
	)
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
