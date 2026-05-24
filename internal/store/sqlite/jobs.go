package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
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

	// args: 3 for the SET clause (status, started_at, eligibility now),
	// 2 for the SELECT predicate (status, run_after), optional kinds.
	args := []any{store.JobStatusRunning, now, store.JobStatusPending, now}
	kindSQL := ""
	if len(kinds) > 0 {
		placeholders := strings.Repeat("?,", len(kinds))
		placeholders = strings.TrimRight(placeholders, ",")
		kindSQL = " AND kind IN (" + placeholders + ")"
		for _, k := range kinds {
			args = append(args, k)
		}
	}

	const cols = `id, tenant_id, kind, payload, status, attempts, run_after, last_error, created_at, updated_at`

	q := `UPDATE jobs SET status = ?, started_at = ?, attempts = attempts + 1
	      WHERE id = (
	          SELECT id FROM jobs
	          WHERE status = ? AND run_after <= ?` + kindSQL + `
	          ORDER BY created_at LIMIT 1
	      )
	      RETURNING ` + cols

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
		`UPDATE jobs SET status = ? WHERE id = ?`,
		store.JobStatusDone, id)
	if err != nil {
		return fmt.Errorf("mark done: %w", err)
	}
	return ensureRow(res, "job")
}

func (s *Jobs) MarkFailed(ctx context.Context, id, errMsg string, retry bool) (bool, error) {
	job, err := s.GetByID(ctx, id)
	if err != nil {
		return false, err
	}

	permanent := !retry || job.Attempts >= s.MaxAttempts

	if !permanent {
		// Exponential backoff: 2^attempts seconds, capped at 1 hour.
		backoff := time.Duration(math.Pow(2, float64(job.Attempts))) * time.Second
		if backoff > time.Hour {
			backoff = time.Hour
		}
		runAfter := time.Now().UTC().Add(backoff)
		_, err = s.db.ExecContext(ctx, `
			UPDATE jobs SET status = ?, last_error = ?, run_after = ? WHERE id = ?`,
			store.JobStatusPending, errMsg, formatTime(runAfter), id)
	} else {
		_, err = s.db.ExecContext(ctx, `
			UPDATE jobs SET status = ?, last_error = ? WHERE id = ?`,
			store.JobStatusFailed, errMsg, id)
	}
	if err != nil {
		return false, fmt.Errorf("mark failed: %w", err)
	}
	return permanent, nil
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
	q += " ORDER BY j.created_at DESC LIMIT ?"
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
	const cols = `id, tenant_id, kind, payload, status, attempts, run_after, last_error, created_at, updated_at`
	q := "SELECT " + cols + " FROM jobs WHERE tenant_id = ?"
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
	const cols = `id, tenant_id, kind, payload, status, attempts, run_after, last_error, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, "SELECT "+cols+" FROM jobs WHERE id = ?", id)
	return scanJob(row)
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
