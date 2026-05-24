package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/samansartipi/curio/internal/store"
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

	args := []any{store.JobStatusRunning, store.JobStatusPending, now}
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

	q := `UPDATE jobs SET status = ?, attempts = attempts + 1
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

func (s *Jobs) MarkFailed(ctx context.Context, id, errMsg string, retry bool) error {
	job, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if retry && job.Attempts < s.MaxAttempts {
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
		return fmt.Errorf("mark failed: %w", err)
	}
	return nil
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
