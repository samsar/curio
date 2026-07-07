package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
)

// Insights implements store.InsightStore over the cluster_runs / clusters /
// cluster_documents tables (migration 004). Clustering fully recomputes each
// run; the current interests are the clusters of the latest done run.
type Insights struct {
	db *DB
}

var _ store.InsightStore = (*Insights)(nil)

// NewInsights constructs the store.
func NewInsights(db *DB) *Insights { return &Insights{db: db} }

func (s *Insights) CreateRun(ctx context.Context, run *store.ClusterRun) error {
	if run.TenantID == "" {
		return fmt.Errorf("insights: tenant_id required")
	}
	if run.Algo == "" {
		return fmt.Errorf("insights: algo required")
	}
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if run.Status == "" {
		run.Status = store.ClusterRunRunning
	}
	var params any
	if len(run.Params) > 0 {
		params = string(run.Params)
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO cluster_runs
			(id, tenant_id, status, algo, params, num_documents, num_clusters, num_noise)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING started_at, created_at, updated_at`,
		run.ID, run.TenantID, run.Status, run.Algo, params,
		run.NumDocuments, run.NumClusters, run.NumNoise)

	var started, created, updated string
	if err := row.Scan(&started, &created, &updated); err != nil {
		if isUniqueViolation(err) {
			return store.ErrConflict
		}
		return fmt.Errorf("insert cluster_run: %w", err)
	}
	run.StartedAt, _ = parseTime(started)
	run.CreatedAt, _ = parseTime(created)
	run.UpdatedAt, _ = parseTime(updated)
	return nil
}

func (s *Insights) ReplaceClusters(ctx context.Context, runID string, clusters []store.ClusterWithMembers) error {
	if runID == "" {
		return fmt.Errorf("insights: run_id required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Idempotent for job retries: drop anything previously written for the
	// run (cascade removes memberships) before re-inserting.
	if _, err := tx.ExecContext(ctx, `DELETE FROM clusters WHERE run_id = ?`, runID); err != nil {
		return fmt.Errorf("delete clusters: %w", err)
	}

	for _, cw := range clusters {
		c := cw.Cluster
		if c.ID == "" {
			c.ID = uuid.NewString()
		}
		if c.TenantID == "" {
			return fmt.Errorf("insights: cluster tenant_id required")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO clusters (id, tenant_id, run_id, label, summary, size, cohesion)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.TenantID, runID, strPtr(c.Label), strPtr(c.Summary), c.Size, c.Cohesion); err != nil {
			return fmt.Errorf("insert cluster: %w", err)
		}
		for _, m := range cw.Members {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO cluster_documents (cluster_id, document_id, similarity)
				VALUES (?, ?, ?)`,
				c.ID, m.DocumentID, m.Similarity); err != nil {
				return fmt.Errorf("insert cluster_document: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit clusters: %w", err)
	}
	return nil
}

func (s *Insights) FinishRun(ctx context.Context, runID, status string, numDocuments, numClusters, numNoise int, errMsg *string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE cluster_runs
		SET status = ?, num_documents = ?, num_clusters = ?, num_noise = ?, error = ?,
		    finished_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = ?`,
		status, numDocuments, numClusters, numNoise, strPtr(errMsg), runID)
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

const clusterRunColumns = `id, tenant_id, status, algo, params, num_documents,
	num_clusters, num_noise, error, started_at, finished_at, created_at, updated_at`

func (s *Insights) LatestRun(ctx context.Context, tenantID, status string) (*store.ClusterRun, error) {
	q := `SELECT ` + clusterRunColumns + ` FROM cluster_runs WHERE tenant_id = ?`
	args := []any{tenantID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY started_at DESC LIMIT 1`
	return scanClusterRun(s.db.QueryRowContext(ctx, q, args...))
}

func (s *Insights) GetRun(ctx context.Context, id string) (*store.ClusterRun, error) {
	q := `SELECT ` + clusterRunColumns + ` FROM cluster_runs WHERE id = ?`
	return scanClusterRun(s.db.QueryRowContext(ctx, q, id))
}

const clusterColumns = `id, tenant_id, run_id, label, summary, size, cohesion, created_at, updated_at`

func (s *Insights) ListClusters(ctx context.Context, runID string, limit int) ([]*store.Cluster, error) {
	q := `SELECT ` + clusterColumns + ` FROM clusters WHERE run_id = ? ORDER BY size DESC, cohesion DESC`
	args := []any{runID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	defer rows.Close()

	var out []*store.Cluster
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Insights) GetCluster(ctx context.Context, id string) (*store.Cluster, error) {
	q := `SELECT ` + clusterColumns + ` FROM clusters WHERE id = ?`
	return scanCluster(s.db.QueryRowContext(ctx, q, id))
}

func (s *Insights) ClusterMembers(ctx context.Context, clusterID string, limit int) ([]store.ClusterMember, error) {
	q := `SELECT cluster_id, document_id, similarity
	      FROM cluster_documents WHERE cluster_id = ? ORDER BY similarity DESC`
	args := []any{clusterID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("cluster members: %w", err)
	}
	defer rows.Close()

	var out []store.ClusterMember
	for rows.Next() {
		var m store.ClusterMember
		if err := rows.Scan(&m.ClusterID, &m.DocumentID, &m.Similarity); err != nil {
			return nil, fmt.Errorf("scan cluster member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Insights) PruneRunsExcept(ctx context.Context, tenantID, keepRunID string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM cluster_runs WHERE tenant_id = ? AND id != ?`, tenantID, keepRunID); err != nil {
		return fmt.Errorf("prune runs: %w", err)
	}
	return nil
}

// scanClusterRun scans a cluster_runs row (from *sql.Row or *sql.Rows). Maps
// sql.ErrNoRows to store.ErrNotFound.
func scanClusterRun(sc interface{ Scan(...any) error }) (*store.ClusterRun, error) {
	var (
		r        store.ClusterRun
		params   sql.NullString
		errMsg   sql.NullString
		started  string
		finished sql.NullString
		created  string
		updated  string
	)
	if err := sc.Scan(&r.ID, &r.TenantID, &r.Status, &r.Algo, &params,
		&r.NumDocuments, &r.NumClusters, &r.NumNoise, &errMsg,
		&started, &finished, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan cluster_run: %w", err)
	}
	if params.Valid && params.String != "" {
		r.Params = json.RawMessage(params.String)
	}
	r.Error = nullableString(errMsg)
	r.StartedAt, _ = parseTime(started)
	if finished.Valid {
		if t, err := parseTime(finished.String); err == nil {
			r.FinishedAt = &t
		}
	}
	r.CreatedAt, _ = parseTime(created)
	r.UpdatedAt, _ = parseTime(updated)
	return &r, nil
}

// scanCluster scans a clusters row. Maps sql.ErrNoRows to store.ErrNotFound.
func scanCluster(sc interface{ Scan(...any) error }) (*store.Cluster, error) {
	var (
		c       store.Cluster
		label   sql.NullString
		summary sql.NullString
		created string
		updated string
	)
	if err := sc.Scan(&c.ID, &c.TenantID, &c.RunID, &label, &summary,
		&c.Size, &c.Cohesion, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("scan cluster: %w", err)
	}
	c.Label = nullableString(label)
	c.Summary = nullableString(summary)
	c.CreatedAt, _ = parseTime(created)
	c.UpdatedAt, _ = parseTime(updated)
	return &c, nil
}
