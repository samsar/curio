package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/samsar/curio/internal/store"
)

// Insights implements store.InsightStore over migration 016's tables, as
// 017 left them: interest_runs, the identities in interests, and each
// run's interest_groups, interest_assignments and interest_placements, with
// the interest_lineage that outlives runs, and each tenant's
// insight_state. The current grouping is the latest done run's.
type Insights struct {
	db *DB
}

var _ store.InsightStore = (*Insights)(nil)

// NewInsights constructs the store.
func NewInsights(db *DB) *Insights { return &Insights{db: db} }

// insertRunSQL inserts a running run; the database assigns its times.
const insertRunSQL = `
	INSERT INTO interest_runs
		(id, tenant_id, status, trigger, kind, split_check, shape, grouper, params, vectors_read_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	RETURNING started_at, created_at, updated_at`

func (s *Insights) CreateRun(ctx context.Context, run *store.InterestRun) error {
	switch {
	case run.TenantID == "":
		return errors.New("insights: tenant_id required")
	case run.Grouper == "":
		return errors.New("insights: grouper required")
	case !run.Trigger.Valid():
		return fmt.Errorf("insights: run trigger %q is not one of the RunTrigger constants", run.Trigger)
	case !run.Kind.Valid():
		return fmt.Errorf("insights: run kind %q is not one of the RunKind constants", run.Kind)
	case !run.Shape.Valid():
		return fmt.Errorf("insights: run shape %q is not one of the InterestShape constants", run.Shape)
	}
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	run.Status = store.InterestRunRunning
	var params any
	if len(run.Params) > 0 {
		params = string(run.Params)
	}
	var started, created, updated string
	err := s.db.QueryRowContext(ctx, insertRunSQL,
		run.ID, run.TenantID, run.Status, run.Trigger, run.Kind, run.SplitCheck, run.Shape, run.Grouper,
		params, timePtr(run.VectorsReadAt),
	).Scan(&started, &created, &updated)
	if isUniqueViolation(err) {
		return fmt.Errorf("interest run %s: %w", run.ID, store.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("insert interest run: %w", err)
	}
	if run.StartedAt, err = parseTime(started); err != nil {
		return err
	}
	if run.CreatedAt, err = parseTime(created); err != nil {
		return err
	}
	run.UpdatedAt, err = parseTime(updated)
	return err
}

// The statements CommitRun runs, in its order. Foreign keys are checked
// statement by statement, so the identities go in before the groups that
// name them, and the groups before the retirement that reads them.
const (
	// insertInterestSQL mints an identity.
	insertInterestSQL = `
	INSERT INTO interests (id, tenant_id, level, label, summary, label_source, created_run_id, labeled_at,
		created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	// relabelInterestSQL names a live identity of the tenant's anew.
	relabelInterestSQL = `
	UPDATE interests SET label = ?, summary = ?, label_source = ?, labeled_at = ?, updated_at = ?
	WHERE id = ? AND tenant_id = ? AND retired_at IS NULL`
	insertGroupSQL = `
	INSERT INTO interest_groups (run_id, interest_id, parent_id, size, loose, cohesion, centroid)
	VALUES (?, ?, ?, ?, ?, ?, ?)`
	insertAssignmentSQL = `
	INSERT INTO interest_assignments
		(run_id, document_id, interest_id, area_id, fit, similarity, nearest_id, area_seed, interest_seed)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertLineageSQL = `
	INSERT INTO interest_lineage (run_id, old_id, new_id, event, shared) VALUES (?, ?, ?, ?, ?)`
	// retireInterestsSQL retires every live identity of the tenant that the
	// run's groups don't hold. It scans the tenant's identities, live and
	// retired within their retention, each checked against the run's
	// groups by their primary key. Its args are the time, the run, the
	// tenant and the run again.
	retireInterestsSQL = `
	UPDATE interests SET retired_at = ?, retired_run_id = ?, updated_at = ?
	WHERE tenant_id = ? AND retired_at IS NULL
	  AND NOT EXISTS (SELECT 1 FROM interest_groups g WHERE g.run_id = ? AND g.interest_id = interests.id)`
	// commitRunSQL moves a running run of the tenant's to done with its
	// outcome.
	commitRunSQL = `
	UPDATE interest_runs SET status = ?, kind = ?, split_check = ?, shape = ?, mean = ?,
		num_documents = ?, num_areas = ?, num_interests = ?, num_loose = ?, num_unsorted = ?,
		changed_documents = ?, changes_since_split = ?,
		kept = ?, created = ?, split = ?, merged = ?, moved = ?, dissolved = ?,
		finished_at = ?, updated_at = ?
	WHERE id = ? AND tenant_id = ? AND status = ?`
)

// CommitRun implements store.InsightStore. A commit of the owner's library
// (about 5,300 assignments and 220 groups) takes about 30 ms: each row
// kind goes through a statement prepared once.
func (s *Insights) CommitRun(ctx context.Context, c store.RunCommit) error {
	if err := checkCommit(c); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("commit interest run %s: begin: %w", c.RunID, err)
	}
	defer tx.Rollback()

	var latest string
	err = tx.QueryRowContext(ctx, latestRunSQL("id", true), c.TenantID, store.InterestRunDone).Scan(&latest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("commit interest run %s: read the latest done run: %w", c.RunID, err)
	}
	if latest != c.PriorRunID {
		return fmt.Errorf("commit interest run %s: built from run %q, but the latest done run is %q: %w",
			c.RunID, c.PriorRunID, latest, store.ErrConflict)
	}

	now := formatTime(time.Now())
	w := commitWriter{ctx: ctx, tx: tx, c: c, now: now}
	for _, step := range []func() error{w.identities, w.relabels, w.groups, w.assignments, w.lineage} {
		if err := step(); err != nil {
			return fmt.Errorf("commit interest run %s: %w", c.RunID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, retireInterestsSQL, now, c.RunID, now, c.TenantID, c.RunID); err != nil {
		return fmt.Errorf("commit interest run %s: retire interests: %w", c.RunID, err)
	}
	o := c.Outcome
	res, err := tx.ExecContext(ctx, commitRunSQL,
		store.InterestRunDone, o.Kind, o.SplitCheck, o.Shape, encodeVector(o.Mean),
		o.NumDocuments, o.NumAreas, o.NumInterests, o.NumLoose, o.NumUnsorted,
		o.ChangedDocuments, o.ChangesSinceSplit,
		o.Kept, o.Created, o.Split, o.Merged, o.Moved, o.Dissolved,
		now, now, c.RunID, c.TenantID, store.InterestRunRunning)
	if err != nil {
		return fmt.Errorf("commit interest run %s: finish it: %w", c.RunID, err)
	}
	if err := runTransitioned(ctx, tx, res, c.RunID); err != nil {
		return fmt.Errorf("commit interest run: %w", err)
	}
	if err := commitState(ctx, tx, c, now); err != nil {
		return fmt.Errorf("commit interest run %s: %w", c.RunID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit interest run %s: %w", c.RunID, err)
	}
	return nil
}

// The statements a commit runs on the tenant's insight_state, both on its
// primary key.
const (
	// clearFailuresSQL resets the tenant's failures: a done rebuild is what
	// they count up to. Its args are the time and the tenant.
	clearFailuresSQL = `
	UPDATE insight_state SET failures = 0, last_failure_at = NULL, last_error = NULL, updated_at = ?
	WHERE tenant_id = ? AND failures > 0`
	// consumeFreshSQL clears the fresh rebuild the tenant owes when it was
	// owed at or before a fresh run read its vectors; one owed again since
	// stays owed, and so does a re-embedding's when the run read them
	// mid-reindex. Its args are the time, the tenant, the run, whether it
	// read mid-reindex, and FreshReindex.
	consumeFreshSQL = `
	UPDATE insight_state SET fresh_owed = NULL, fresh_owed_at = NULL, updated_at = ?
	WHERE tenant_id = ? AND fresh_owed_at <= (SELECT vectors_read_at FROM interest_runs WHERE id = ?)
		AND NOT (? AND fresh_owed = ?)`
)

// commitState is what a commit changes in the tenant's insight_state,
// inside its transaction: no failures, and for a fresh run, the fresh
// rebuild it consumed.
func commitState(ctx context.Context, tx *sql.Tx, c store.RunCommit, now string) error {
	if _, err := tx.ExecContext(ctx, clearFailuresSQL, now, c.TenantID); err != nil {
		return fmt.Errorf("clear the failures: %w", err)
	}
	if c.Outcome.Kind != store.RunKindFresh {
		return nil
	}
	if _, err := tx.ExecContext(ctx, consumeFreshSQL, now, c.TenantID, c.RunID, c.ReadMidReindex,
		store.FreshReindex); err != nil {
		return fmt.Errorf("clear the fresh rebuild owed: %w", err)
	}
	return nil
}

// checkCommit validates what the schema's CHECKs would refuse mid-commit,
// so a bad commit fails before it starts.
func checkCommit(c store.RunCommit) error {
	o := c.Outcome
	switch {
	case c.RunID == "" || c.TenantID == "":
		return errors.New("insights: a commit needs its run and tenant")
	case !o.Kind.Valid() || !o.Shape.Valid():
		return fmt.Errorf("insights: commit of run %s: kind %q or shape %q is not valid", c.RunID, o.Kind, o.Shape)
	}
	for _, in := range slices.Concat(c.NewIdentities, c.Relabels) {
		if in.ID == "" || (in.LabelSource != "" && !in.LabelSource.Valid()) {
			return fmt.Errorf("insights: commit of run %s: identity %q with label source %q", c.RunID, in.ID, in.LabelSource)
		}
	}
	for _, in := range c.NewIdentities {
		if !in.Level.Valid() {
			return fmt.Errorf("insights: commit of run %s: identity %s has level %q", c.RunID, in.ID, in.Level)
		}
	}
	for _, a := range c.Assignments {
		if !a.Fit.Valid() {
			return fmt.Errorf("insights: commit of run %s: document %s has fit %q", c.RunID, a.DocumentID, a.Fit)
		}
	}
	for _, l := range c.Lineage {
		if !l.Event.Valid() || (l.RunID != "" && l.RunID != c.RunID) {
			return fmt.Errorf("insights: commit of run %s: lineage %s → %s of run %q, event %q",
				c.RunID, l.OldID, l.NewID, l.RunID, l.Event)
		}
	}
	return nil
}

// commitWriter writes a commit's rows inside its transaction, each kind
// through a statement prepared once.
type commitWriter struct {
	ctx context.Context
	tx  *sql.Tx
	c   store.RunCommit
	now string
}

// each runs query once for every row of n, with the arguments args(i).
func (w commitWriter) each(what, query string, n int, args func(i int) []any) (err error) {
	if n == 0 {
		return nil
	}
	stmt, err := w.tx.PrepareContext(w.ctx, query)
	if err != nil {
		return fmt.Errorf("prepare %s: %w", what, err)
	}
	defer func() {
		if cerr := stmt.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close the %s statement: %w", what, cerr))
		}
	}()
	for i := range n {
		if _, err := stmt.ExecContext(w.ctx, args(i)...); err != nil {
			return fmt.Errorf("write %s %d of %d: %w", what, i+1, n, err)
		}
	}
	return nil
}

func (w commitWriter) identities() error {
	ids := w.c.NewIdentities
	return w.each("identity", insertInterestSQL, len(ids), func(i int) []any {
		in := ids[i]
		return []any{in.ID, w.c.TenantID, in.Level, nullIfEmpty(in.Label), nullIfEmpty(in.Summary),
			nullIfEmpty(string(in.LabelSource)), w.c.RunID, timePtr(in.LabeledAt), w.now, w.now}
	})
}

func (w commitWriter) relabels() error {
	for _, in := range w.c.Relabels {
		res, err := w.tx.ExecContext(w.ctx, relabelInterestSQL, nullIfEmpty(in.Label), nullIfEmpty(in.Summary),
			nullIfEmpty(string(in.LabelSource)), timePtr(in.LabeledAt), w.now, in.ID, w.c.TenantID)
		if err != nil {
			return fmt.Errorf("relabel interest %s: %w", in.ID, err)
		}
		if err := ensureRow(res, "live interest "+in.ID); err != nil {
			return fmt.Errorf("relabel: %w", err)
		}
	}
	return nil
}

func (w commitWriter) groups() error {
	gs := w.c.Groups
	return w.each("group", insertGroupSQL, len(gs), func(i int) []any {
		g := gs[i]
		return []any{w.c.RunID, g.ID, nullIfEmpty(g.ParentID), g.Size, g.Loose, g.Cohesion, encodeVector(g.Centroid)}
	})
}

func (w commitWriter) assignments() error {
	as := w.c.Assignments
	return w.each("assignment", insertAssignmentSQL, len(as), func(i int) []any {
		a := as[i]
		return []any{w.c.RunID, a.DocumentID, nullIfEmpty(a.InterestID), nullIfEmpty(a.AreaID), a.Fit, a.Similarity,
			nullIfEmpty(a.NearestID), a.AreaSeed, a.InterestSeed}
	})
}

func (w commitWriter) lineage() error {
	ls := w.c.Lineage
	return w.each("lineage", insertLineageSQL, len(ls), func(i int) []any {
		l := ls[i]
		return []any{w.c.RunID, l.OldID, l.NewID, l.Event, l.Shared}
	})
}

// failRunSQL moves a running run to failed. Its args are the status, the
// documents, the error, the time twice, the run and the running status.
const failRunSQL = `
	UPDATE interest_runs SET status = ?, num_documents = ?, error = ?, finished_at = ?, updated_at = ?
	WHERE id = ? AND status = ?`

func (s *Insights) FailRun(ctx context.Context, runID string, numDocuments int, msg string) error {
	now := formatTime(time.Now())
	res, err := s.db.ExecContext(ctx, failRunSQL,
		store.InterestRunFailed, numDocuments, msg, now, now, runID, store.InterestRunRunning)
	if err != nil {
		return fmt.Errorf("fail interest run %s: %w", runID, err)
	}
	return runTransitioned(ctx, s.db, res, runID)
}

// insightStateColumns are a tenant's insight_state, in scanInsightState's
// order.
const insightStateColumns = `fresh_owed, fresh_owed_at, failures, last_failure_at, last_error`

// The insight_state statements, each on its primary key.
const (
	insightStateSQL = `SELECT ` + insightStateColumns + ` FROM insight_state WHERE tenant_id = ?`
	// oweFreshSQL owes the tenant a fresh rebuild, for a reason, from a
	// time: always for a re-embedding, and for a manual request unless a
	// re-embedding's is owed. Its args are the tenant, the reason, the
	// time and store.FreshReindex.
	oweFreshSQL = `
	INSERT INTO insight_state (tenant_id, fresh_owed, fresh_owed_at, updated_at) VALUES (?1, ?2, ?3, ?3)
	ON CONFLICT (tenant_id) DO UPDATE SET fresh_owed = excluded.fresh_owed, fresh_owed_at = excluded.fresh_owed_at,
		updated_at = excluded.updated_at
	WHERE excluded.fresh_owed = ?4 OR insight_state.fresh_owed IS NOT ?4`
	// recordFailureSQL counts one more failed rebuild of the tenant's,
	// failing at a time with an error, and returns the row. Its args are
	// the tenant, the time and the error.
	recordFailureSQL = `
	INSERT INTO insight_state (tenant_id, failures, last_failure_at, last_error, updated_at) VALUES (?1, 1, ?2, ?3, ?2)
	ON CONFLICT (tenant_id) DO UPDATE SET failures = insight_state.failures + 1,
		last_failure_at = excluded.last_failure_at, last_error = excluded.last_error, updated_at = excluded.updated_at
	RETURNING ` + insightStateColumns
)

func (s *Insights) State(ctx context.Context, tenantID string) (store.InsightState, error) {
	st, err := scanInsightState(s.db.QueryRowContext(ctx, insightStateSQL, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return store.InsightState{}, nil
	}
	if err != nil {
		return store.InsightState{}, fmt.Errorf("read insight state: %w", err)
	}
	return st, nil
}

func (s *Insights) OweFresh(ctx context.Context, tenantID string, reason store.FreshReason) error {
	if !reason.Valid() {
		return fmt.Errorf("owe a fresh rebuild: reason %q is not one of the FreshReason constants", reason)
	}
	if _, err := s.db.ExecContext(ctx, oweFreshSQL, tenantID, reason, formatTime(time.Now()), store.FreshReindex); err != nil {
		return fmt.Errorf("owe a fresh rebuild (%s): %w", reason, err)
	}
	return nil
}

func (s *Insights) RecordFailure(ctx context.Context, tenantID, runID string, numDocuments int, msg string) (store.InsightState, error) {
	return s.withFailure(ctx, tenantID, msg, func(tx *sql.Tx, now string) error {
		if runID == "" {
			return nil
		}
		res, err := tx.ExecContext(ctx, failRunSQL,
			store.InterestRunFailed, numDocuments, msg, now, now, runID, store.InterestRunRunning)
		if err != nil {
			return fmt.Errorf("fail interest run %s: %w", runID, err)
		}
		return runTransitioned(ctx, tx, res, runID)
	})
}

// abandonRunsSQL fails every running run of a tenant's. Its args are the
// status, the error, the time twice, the tenant and the running status.
const abandonRunsSQL = `
	UPDATE interest_runs SET status = ?, error = ?, finished_at = ?, updated_at = ?
	WHERE tenant_id = ? AND status = ?`

func (s *Insights) RecordAbandoned(ctx context.Context, tenantID, msg string) (store.InsightState, error) {
	return s.withFailure(ctx, tenantID, msg, func(tx *sql.Tx, now string) error {
		_, err := tx.ExecContext(ctx, abandonRunsSQL,
			store.InterestRunFailed, msg, now, now, tenantID, store.InterestRunRunning)
		if err != nil {
			return fmt.Errorf("fail the running interest runs: %w", err)
		}
		return nil
	})
}

// withFailure runs fail, then counts one failed rebuild of the tenant's
// with msg, in one transaction at one time, and returns the tenant's state
// after.
func (s *Insights) withFailure(ctx context.Context, tenantID, msg string, fail func(tx *sql.Tx, now string) error) (store.InsightState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.InsightState{}, fmt.Errorf("record a failed rebuild: begin: %w", err)
	}
	defer tx.Rollback()
	now := formatTime(time.Now())
	if err := fail(tx, now); err != nil {
		return store.InsightState{}, fmt.Errorf("record a failed rebuild: %w", err)
	}
	st, err := scanInsightState(tx.QueryRowContext(ctx, recordFailureSQL, tenantID, now, msg))
	if err != nil {
		return store.InsightState{}, fmt.Errorf("record a failed rebuild: count it: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return store.InsightState{}, fmt.Errorf("record a failed rebuild: commit: %w", err)
	}
	return st, nil
}

// changesSQL counts what changed since a run read its vectors, in one
// statement so every count is of one snapshot: the fetched documents
// indexed since, unassigned and assigned (a range of
// idx_documents_tenant_indexed each, the run's assignment checked by its
// primary key); the documents the run assigned that are pending, failed or
// dead now; and the run's assignments left. The third is driven from the
// documents in those states on idx_documents_tenant_state_updated, a few
// thousand at most: driven from the run's assignments with d.tenant_id = ?,
// SQLite walks every document of the tenant (see "Interests page by offset
// within a run"). Its args are the tenant, the time, the fetched state,
// the run, then the pending, failed and dead states.
const changesSQL = `
	SELECT
		(SELECT count(*) FROM documents d
		 WHERE d.tenant_id = ?1 AND d.indexed_at >= ?2 AND d.state = ?3
		   AND NOT EXISTS (SELECT 1 FROM interest_assignments a WHERE a.run_id = ?4 AND a.document_id = d.id)),
		(SELECT count(*) FROM documents d
		 WHERE d.tenant_id = ?1 AND d.indexed_at >= ?2 AND d.state = ?3
		   AND EXISTS (SELECT 1 FROM interest_assignments a WHERE a.run_id = ?4 AND a.document_id = d.id)),
		(SELECT count(*) FROM documents d
		 WHERE d.tenant_id = ?1 AND d.state IN (?5, ?6, ?7)
		   AND EXISTS (SELECT 1 FROM interest_assignments a WHERE a.run_id = ?4 AND a.document_id = d.id)),
		(SELECT count(*) FROM interest_assignments WHERE run_id = ?4)`

func (s *Insights) Changes(ctx context.Context, run *store.InterestRun) (store.RunChanges, error) {
	since := run.StartedAt
	if run.VectorsReadAt != nil {
		since = *run.VectorsReadAt
	}
	var c store.RunChanges
	var assigned int
	err := s.db.QueryRowContext(ctx, changesSQL, run.TenantID, formatTime(since), store.DocStateFetched, run.ID,
		store.DocStatePending, store.DocStateFailed, store.DocStateDead).Scan(&c.Added, &c.Reindexed, &c.Left, &assigned)
	if err != nil {
		return store.RunChanges{}, fmt.Errorf("count the changes since run %s: %w", run.ID, err)
	}
	c.Deleted = run.NumDocuments - assigned
	return c, nil
}

// runStatusSQL reads a run's status by its primary key.
const runStatusSQL = `SELECT status FROM interest_runs WHERE id = ?`

// runTransitioned turns a transition of a running run that changed nothing
// into its error, reading the run through q: ErrNotFound for a run that
// doesn't exist, ErrConflict for one that isn't running.
func runTransitioned(ctx context.Context, q rowQuerier, res sql.Result, runID string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("interest run %s: rows affected: %w", runID, err)
	}
	if n > 0 {
		return nil
	}
	var status store.InterestRunStatus
	err = q.QueryRowContext(ctx, runStatusSQL, runID).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("interest run %s: %w", runID, store.ErrNotFound)
	case err != nil:
		return fmt.Errorf("interest run %s: read its status: %w", runID, err)
	}
	return fmt.Errorf("interest run %s is %s, not running: %w", runID, status, store.ErrConflict)
}

const runColumns = `id, tenant_id, status, trigger, kind, split_check, shape, grouper, params, vectors_read_at, mean,
	num_documents, num_areas, num_interests, num_loose, num_unsorted, changed_documents, changes_since_split,
	kept, created, split, merged, moved, dissolved, error, started_at, finished_at, created_at, updated_at`

// latestRunSQL is the tenant's newest run, of a status when byStatus,
// reading columns. It seeks idx_interest_runs_tenant_status. started_at is
// kept to the millisecond, so two runs can share one; the later insert is
// the newer run.
func latestRunSQL(columns string, byStatus bool) string {
	q := `SELECT ` + columns + ` FROM interest_runs WHERE tenant_id = ?`
	if byStatus {
		q += ` AND status = ?`
	}
	return q + ` ORDER BY started_at DESC, rowid DESC LIMIT 1`
}

func (s *Insights) LatestRun(ctx context.Context, tenantID string, status store.InterestRunStatus) (*store.InterestRun, error) {
	args := []any{tenantID}
	if status != "" {
		args = append(args, status)
	}
	return scanRun(s.db.QueryRowContext(ctx, latestRunSQL(runColumns, status != ""), args...))
}

// getRunSQL reads a run by its primary key.
const getRunSQL = `SELECT ` + runColumns + ` FROM interest_runs WHERE id = ?`

func (s *Insights) GetRun(ctx context.Context, id string) (*store.InterestRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, getRunSQL, id))
}

// interestColumns are an identity's, in scanInterest's order.
const interestColumns = `id, tenant_id, level, label, summary, label_source, created_run_id, labeled_at,
	retired_at, retired_run_id, created_at, updated_at`

// groupSelect reads groups with their identity and their area's label, in
// scanGroup's order, without the centroid.
var groupSelect = `SELECT g.run_id, ` + qualify("i", interestColumns) + `, g.parent_id, p.label, g.size, g.loose,
	g.cohesion FROM interest_groups g
	JOIN interests i ON i.id = g.interest_id
	LEFT JOIN interests p ON p.id = g.parent_id`

// groupOrder is the order groups are listed in, the order of
// idx_interest_groups_list after its run and parent.
const groupOrder = ` ORDER BY g.size DESC, g.cohesion DESC, g.interest_id`

// The group reads. Every one seeks idx_interest_groups_list or the primary
// key on the run, reaching identities by their primary key.
var (
	// topGroupsSQL is a page of a run's top-level groups, read in order
	// from the index. Its args are the run, the limit and the offset.
	topGroupsSQL = groupSelect + ` WHERE g.run_id = ? AND g.parent_id IS NULL` + groupOrder + ` LIMIT ? OFFSET ?`
	// childGroupsSQL is the interests of a JSON array of areas, area by
	// area, each read in order from the index.
	childGroupsSQL = groupSelect + ` WHERE g.run_id = ? AND g.parent_id IN (SELECT value FROM json_each(?))
	ORDER BY g.parent_id, g.size DESC, g.cohesion DESC, g.interest_id`
	// nestedGroupsSQL is a page of the interests of an areas-shaped run,
	// sorted: every area's are read.
	nestedGroupsSQL = groupSelect + ` WHERE g.run_id = ? AND g.parent_id IS NOT NULL` + groupOrder + ` LIMIT ? OFFSET ?`
	getGroupSQL     = groupSelect + ` WHERE g.run_id = ? AND g.interest_id = ?`
	// runGroupsSQL is every group of a run, with its centroid.
	runGroupsSQL = `SELECT g.run_id, ` + qualify("i", interestColumns) + `, g.parent_id, p.label, g.size, g.loose,
	g.cohesion, g.centroid FROM interest_groups g
	JOIN interests i ON i.id = g.interest_id
	LEFT JOIN interests p ON p.id = g.parent_id
	WHERE g.run_id = ?`
)

// pageArgs are the LIMIT and OFFSET arguments of a page of at most limit
// rows from offset; limit <= 0 is -1, which SQLite reads as no limit. A
// negative offset is an error.
func pageArgs(limit, offset int) (int, int, error) {
	if offset < 0 {
		return 0, 0, fmt.Errorf("offset %d is negative", offset)
	}
	if limit <= 0 {
		limit = -1
	}
	return limit, offset, nil
}

func (s *Insights) TopGroups(ctx context.Context, runID string, limit, offset int) ([]store.InterestGroup, error) {
	return s.groupPage(ctx, "top-level groups", topGroupsSQL, runID, limit, offset)
}

func (s *Insights) NestedGroups(ctx context.Context, runID string, limit, offset int) ([]store.InterestGroup, error) {
	return s.groupPage(ctx, "interests", nestedGroupsSQL, runID, limit, offset)
}

func (s *Insights) groupPage(ctx context.Context, what, query, runID string, limit, offset int) ([]store.InterestGroup, error) {
	limit, offset, err := pageArgs(limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", what, err)
	}
	return s.queryGroups(ctx, "list "+what, false, query, runID, limit, offset)
}

func (s *Insights) ChildGroups(ctx context.Context, runID string, areaIDs []string) ([]store.InterestGroup, error) {
	if len(areaIDs) == 0 {
		return nil, nil
	}
	ids, err := json.Marshal(areaIDs)
	if err != nil {
		return nil, fmt.Errorf("list child groups: encode area IDs: %w", err)
	}
	return s.queryGroups(ctx, "list child groups", false, childGroupsSQL, runID, string(ids))
}

func (s *Insights) GetGroup(ctx context.Context, runID, id string) (*store.InterestGroup, error) {
	gs, err := s.queryGroups(ctx, "get group", false, getGroupSQL, runID, id)
	if err != nil {
		return nil, err
	}
	if len(gs) == 0 {
		return nil, fmt.Errorf("interest %s in run %s: %w", id, runID, store.ErrNotFound)
	}
	return &gs[0], nil
}

func (s *Insights) RunGroups(ctx context.Context, runID string) ([]store.InterestGroup, error) {
	return s.queryGroups(ctx, "read a run's groups", true, runGroupsSQL, runID)
}

func (s *Insights) queryGroups(ctx context.Context, what string, centroids bool, query string, args ...any) ([]store.InterestGroup, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer rows.Close()
	var out []store.InterestGroup
	for rows.Next() {
		g, err := scanGroup(rows, centroids)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

// The assignment reads.
const (
	// membersSQL is a page of an interest's documents of one fit, read in
	// order from idx_interest_assignments_list, which covers it. Its args
	// are the run, the interest, the fit, the limit and the offset.
	membersSQL = `
	SELECT document_id, similarity FROM interest_assignments
	WHERE run_id = ? AND interest_id = ? AND fit = ?
	ORDER BY similarity DESC, document_id LIMIT ? OFFSET ?`
	// unsortedSQL is a page of a run's unsorted documents, read in order
	// from idx_interest_assignments_list, each with its nearest interest's
	// label. Its args are the run, the unsorted fit, the limit and the
	// offset.
	unsortedSQL = `
	SELECT a.document_id, a.similarity, a.nearest_id, n.label FROM interest_assignments a
	LEFT JOIN interests n ON n.id = a.nearest_id
	WHERE a.run_id = ? AND a.interest_id IS NULL AND a.fit = ?
	ORDER BY a.similarity DESC, a.document_id LIMIT ? OFFSET ?`
	// runAssignmentsSQL is every assignment of a run, by its primary key.
	runAssignmentsSQL = `
	SELECT document_id, interest_id, area_id, fit, similarity, nearest_id, area_seed, interest_seed
	FROM interest_assignments WHERE run_id = ?`
)

func (s *Insights) Members(ctx context.Context, runID, interestID string, fit store.InterestFit, limit, offset int) ([]store.InterestAssignment, error) {
	if fit != store.InterestFitMember && fit != store.InterestFitLoose {
		return nil, fmt.Errorf("interest members: fit %q is neither member nor loose", fit)
	}
	limit, offset, err := pageArgs(limit, offset)
	if err != nil {
		return nil, fmt.Errorf("interest members: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, membersSQL, runID, interestID, fit, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("interest members: %w", err)
	}
	defer rows.Close()
	var out []store.InterestAssignment
	for rows.Next() {
		a := store.InterestAssignment{InterestID: interestID, Fit: fit}
		if err := rows.Scan(&a.DocumentID, &a.Similarity); err != nil {
			return nil, fmt.Errorf("scan interest member: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("interest members: %w", err)
	}
	return out, nil
}

func (s *Insights) Unsorted(ctx context.Context, runID string, limit, offset int) ([]store.InterestAssignment, error) {
	limit, offset, err := pageArgs(limit, offset)
	if err != nil {
		return nil, fmt.Errorf("unsorted documents: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, unsortedSQL, runID, store.InterestFitUnsorted, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("unsorted documents: %w", err)
	}
	defer rows.Close()
	var out []store.InterestAssignment
	for rows.Next() {
		a := store.InterestAssignment{Fit: store.InterestFitUnsorted}
		var nearest, label sql.NullString
		if err := rows.Scan(&a.DocumentID, &a.Similarity, &nearest, &label); err != nil {
			return nil, fmt.Errorf("scan unsorted document: %w", err)
		}
		a.NearestID, a.NearestLabel = nearest.String, label.String
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unsorted documents: %w", err)
	}
	return out, nil
}

func (s *Insights) RunAssignments(ctx context.Context, runID string) ([]store.InterestAssignment, error) {
	rows, err := s.db.QueryContext(ctx, runAssignmentsSQL, runID)
	if err != nil {
		return nil, fmt.Errorf("read a run's assignments: %w", err)
	}
	defer rows.Close()
	var out []store.InterestAssignment
	for rows.Next() {
		var a store.InterestAssignment
		var interest, area, nearest sql.NullString
		if err := rows.Scan(&a.DocumentID, &interest, &area, &a.Fit, &a.Similarity, &nearest,
			&a.AreaSeed, &a.InterestSeed); err != nil {
			return nil, fmt.Errorf("scan assignment: %w", err)
		}
		a.InterestID, a.AreaID, a.NearestID = interest.String, area.String, nearest.String
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read a run's assignments: %w", err)
	}
	return out, nil
}

// The lineage reads, both on the lineage's primary key, in its order.
const (
	runLineageSQL = `SELECT run_id, old_id, new_id, event, shared FROM interest_lineage
	WHERE run_id = ? ORDER BY old_id, new_id`
	successorsSQL = `SELECT run_id, old_id, new_id, event, shared FROM interest_lineage
	WHERE run_id = ? AND old_id = ? ORDER BY new_id`
)

func (s *Insights) RunLineage(ctx context.Context, runID string) ([]store.LineageRow, error) {
	return s.queryLineage(ctx, runLineageSQL, runID)
}

func (s *Insights) Successors(ctx context.Context, runID, oldID string) ([]store.LineageRow, error) {
	return s.queryLineage(ctx, successorsSQL, runID, oldID)
}

func (s *Insights) queryLineage(ctx context.Context, query string, args ...any) ([]store.LineageRow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read lineage: %w", err)
	}
	defer rows.Close()
	var out []store.LineageRow
	for rows.Next() {
		var l store.LineageRow
		if err := rows.Scan(&l.RunID, &l.OldID, &l.NewID, &l.Event, &l.Shared); err != nil {
			return nil, fmt.Errorf("scan lineage: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read lineage: %w", err)
	}
	return out, nil
}

// The identity reads.
var (
	getInterestSQL = `SELECT ` + interestColumns + ` FROM interests WHERE id = ?`
	// getInterestsSQL reads a JSON array of IDs, each by its primary key.
	// The tenant is checked on each, +tenant_id keeping the planner on the
	// primary key (see "Interests page by offset within a run").
	getInterestsSQL = `SELECT ` + interestColumns + ` FROM interests
	WHERE id IN (SELECT value FROM json_each(?)) AND +tenant_id = ?`
	// createdBySQL reads the identities a run minted through its groups.
	createdBySQL = `SELECT ` + qualify("i", interestColumns) + ` FROM interest_groups g
	JOIN interests i ON i.id = g.interest_id
	WHERE g.run_id = ? AND i.created_run_id = g.run_id`
	// retiredBySQL seeks the partial index of retired identities.
	retiredBySQL = `SELECT ` + interestColumns + ` FROM interests
	WHERE tenant_id = ? AND retired_run_id = ? AND retired_at IS NOT NULL`
)

func (s *Insights) GetInterest(ctx context.Context, id string) (*store.Interest, error) {
	in, err := scanInterest(s.db.QueryRowContext(ctx, getInterestSQL, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("interest %s: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get interest %s: %w", id, err)
	}
	return &in, nil
}

func (s *Insights) GetInterests(ctx context.Context, tenantID string, ids []string) ([]store.Interest, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("get interests: encode IDs: %w", err)
	}
	return s.queryInterests(ctx, "get interests", getInterestsSQL, string(list), tenantID)
}

func (s *Insights) CreatedBy(ctx context.Context, runID string) ([]store.Interest, error) {
	return s.queryInterests(ctx, "interests a run created", createdBySQL, runID)
}

func (s *Insights) RetiredBy(ctx context.Context, tenantID, runID string) ([]store.Interest, error) {
	return s.queryInterests(ctx, "interests a run retired", retiredBySQL, tenantID, runID)
}

func (s *Insights) queryInterests(ctx context.Context, what, query string, args ...any) ([]store.Interest, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer rows.Close()
	var out []store.Interest
	for rows.Next() {
		in, err := scanInterest(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

// placementsSQL is up to a limit of the placements into a run's interest,
// or into its Unsorted, newest first, read in order from
// idx_interest_placements_list. Its args are the run, the interest unless
// unsorted, and the limit.
func placementsSQL(unsorted bool) string {
	interest := `interest_id = ?`
	if unsorted {
		interest = `interest_id IS NULL`
	}
	return `SELECT run_id, document_id, interest_id, similarity, placed_at FROM interest_placements
	WHERE run_id = ? AND ` + interest + ` ORDER BY placed_at DESC LIMIT ?`
}

// placementCountsSQL counts a run's placements per interest over
// idx_interest_placements_list, which covers it.
const placementCountsSQL = `SELECT interest_id, count(*) FROM interest_placements WHERE run_id = ? GROUP BY interest_id`

func (s *Insights) Placements(ctx context.Context, runID, interestID string, limit int) ([]store.Placement, error) {
	args := []any{runID}
	if interestID != "" {
		args = append(args, interestID)
	}
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.QueryContext(ctx, placementsSQL(interestID == ""), append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("placements: %w", err)
	}
	defer rows.Close()
	var out []store.Placement
	for rows.Next() {
		var p store.Placement
		var interest sql.NullString
		var placed string
		if err := rows.Scan(&p.RunID, &p.DocumentID, &interest, &p.Similarity, &placed); err != nil {
			return nil, fmt.Errorf("scan placement: %w", err)
		}
		p.InterestID = interest.String
		if p.PlacedAt, err = parseTime(placed); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("placements: %w", err)
	}
	return out, nil
}

func (s *Insights) PlacementCounts(ctx context.Context, runID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, placementCountsSQL, runID)
	if err != nil {
		return nil, fmt.Errorf("count placements: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var interest sql.NullString
		var n int
		if err := rows.Scan(&interest, &n); err != nil {
			return nil, fmt.Errorf("scan placement count: %w", err)
		}
		out[interest.String] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count placements: %w", err)
	}
	return out, nil
}

// The placement statements. A placement is guarded by its run being the
// tenant's latest done run (latestRunSQL's order, on
// idx_interest_runs_tenant_status), so one made from a run a rebuild has
// replaced writes nothing; a document the run assigned, or one deleted, is
// left out, each checked by its primary key.
const (
	// assignedSQL reports whether a run assigned a document, by the
	// assignments' primary key.
	assignedSQL = `SELECT EXISTS (SELECT 1 FROM interest_assignments WHERE run_id = ? AND document_id = ?)`
	// placeDocumentSQL places a document into a run, or places it anew.
	// Its args are the run, the document, the interest, the similarity,
	// the time, the tenant and the done status.
	placeDocumentSQL = `
	INSERT INTO interest_placements (run_id, document_id, interest_id, similarity, placed_at)
	SELECT ?1, ?2, ?3, ?4, ?5
	WHERE ?1 = (SELECT id FROM interest_runs WHERE tenant_id = ?6 AND status = ?7
	            ORDER BY started_at DESC, rowid DESC LIMIT 1)
	  AND NOT EXISTS (SELECT 1 FROM interest_assignments WHERE run_id = ?1 AND document_id = ?2)
	  AND EXISTS (SELECT 1 FROM documents WHERE id = ?2)
	ON CONFLICT (run_id, document_id) DO UPDATE SET interest_id = excluded.interest_id,
		similarity = excluded.similarity, placed_at = excluded.placed_at`
	// placeIfAbsentSQL places a document into a run unless it is placed
	// already, inside PlaceMany's transaction, which checked the run. Its
	// args are the run, the document, the interest, the similarity and
	// the time.
	placeIfAbsentSQL = `
	INSERT INTO interest_placements (run_id, document_id, interest_id, similarity, placed_at)
	SELECT ?1, ?2, ?3, ?4, ?5
	WHERE NOT EXISTS (SELECT 1 FROM interest_assignments WHERE run_id = ?1 AND document_id = ?2)
	  AND EXISTS (SELECT 1 FROM documents WHERE id = ?2)
	ON CONFLICT (run_id, document_id) DO NOTHING`
	// unplacedSQL lists the fetched documents indexed since a time that a
	// run neither assigned nor placed: a range of
	// idx_documents_tenant_indexed, each checked against both primary
	// keys. Its args are the tenant, the time, the fetched state and the
	// run.
	unplacedSQL = `
	SELECT d.id FROM documents d
	WHERE d.tenant_id = ?1 AND d.indexed_at >= ?2 AND d.state = ?3
	  AND NOT EXISTS (SELECT 1 FROM interest_assignments a WHERE a.run_id = ?4 AND a.document_id = d.id)
	  AND NOT EXISTS (SELECT 1 FROM interest_placements p WHERE p.run_id = ?4 AND p.document_id = d.id)`
)

func (s *Insights) Assigned(ctx context.Context, runID, documentID string) (bool, error) {
	var assigned bool
	if err := s.db.QueryRowContext(ctx, assignedSQL, runID, documentID).Scan(&assigned); err != nil {
		return false, fmt.Errorf("is document %s assigned in run %s: %w", documentID, runID, err)
	}
	return assigned, nil
}

// documentPlaceSQL is where a run put a document: from the run's row, its
// assignment and its placement, each interest's group in the run for its
// area, and the labels, every one a seek of a primary key. A row of NULLs
// past the run is a document the run has nowhere. Its args are the run and
// the document.
const documentPlaceSQL = `
SELECT a.fit, a.interest_id, ai.label, ag.parent_id, ap.label, a.nearest_id, an.label, a.similarity,
       pl.document_id IS NOT NULL, pl.interest_id, pi.label, pg.parent_id, pp.label, pl.similarity
FROM interest_runs r
LEFT JOIN interest_assignments a ON a.run_id = r.id AND a.document_id = ?2
LEFT JOIN interests ai ON ai.id = a.interest_id
LEFT JOIN interest_groups ag ON ag.run_id = r.id AND ag.interest_id = a.interest_id
LEFT JOIN interests ap ON ap.id = ag.parent_id
LEFT JOIN interests an ON an.id = a.nearest_id
LEFT JOIN interest_placements pl ON pl.run_id = r.id AND pl.document_id = ?2
LEFT JOIN interests pi ON pi.id = pl.interest_id
LEFT JOIN interest_groups pg ON pg.run_id = r.id AND pg.interest_id = pl.interest_id
LEFT JOIN interests pp ON pp.id = pg.parent_id
WHERE r.id = ?1`

func (s *Insights) DocumentPlace(ctx context.Context, runID, documentID string) (*store.DocumentPlace, error) {
	var fit, interest, interestLabel, area, areaLabel, nearest, nearestLabel sql.NullString
	var placedInterest, placedLabel, placedArea, placedAreaLabel sql.NullString
	var sim, placedSim sql.NullFloat64
	var placed bool
	err := s.db.QueryRowContext(ctx, documentPlaceSQL, runID, documentID).Scan(&fit, &interest, &interestLabel,
		&area, &areaLabel, &nearest, &nearestLabel, &sim, &placed, &placedInterest, &placedLabel, &placedArea,
		&placedAreaLabel, &placedSim)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("run %s: %w", runID, store.ErrNotFound)
	case err != nil:
		return nil, fmt.Errorf("where run %s put document %s: %w", runID, documentID, err)
	case fit.Valid:
		return &store.DocumentPlace{Fit: store.InterestFit(fit.String), InterestID: interest.String,
			InterestLabel: interestLabel.String, AreaID: area.String, AreaLabel: areaLabel.String,
			NearestID: nearest.String, NearestLabel: nearestLabel.String, Similarity: sim.Float64}, nil
	case placed:
		return &store.DocumentPlace{Placed: true, InterestID: placedInterest.String, InterestLabel: placedLabel.String,
			AreaID: placedArea.String, AreaLabel: placedAreaLabel.String, Similarity: placedSim.Float64}, nil
	}
	return nil, fmt.Errorf("document %s in run %s: %w", documentID, runID, store.ErrNotFound)
}

func (s *Insights) PlaceDocument(ctx context.Context, tenantID string, p store.Placement) (bool, error) {
	res, err := s.db.ExecContext(ctx, placeDocumentSQL, p.RunID, p.DocumentID, nullIfEmpty(p.InterestID), p.Similarity,
		formatTime(time.Now()), tenantID, store.InterestRunDone)
	if err != nil {
		return false, fmt.Errorf("place document %s in run %s: %w", p.DocumentID, p.RunID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("place document %s in run %s: rows affected: %w", p.DocumentID, p.RunID, err)
	}
	return n > 0, nil
}

func (s *Insights) PlaceMany(ctx context.Context, tenantID, runID string, ps []store.Placement) (int, error) {
	if len(ps) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("place documents in run %s: begin: %w", runID, err)
	}
	defer tx.Rollback()
	var latest string
	err = tx.QueryRowContext(ctx, latestRunSQL("id", true), tenantID, store.InterestRunDone).Scan(&latest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("place documents in run %s: read the latest done run: %w", runID, err)
	}
	if latest != runID {
		return 0, nil
	}
	placed, err := placeEach(ctx, tx, runID, ps, formatTime(time.Now()))
	if err != nil {
		return 0, fmt.Errorf("place documents in run %s: %w", runID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("place documents in run %s: commit: %w", runID, err)
	}
	return placed, nil
}

// placeEach writes each placement into runID unless the document is placed
// already, through one prepared statement, and returns how many it wrote.
func placeEach(ctx context.Context, tx *sql.Tx, runID string, ps []store.Placement, now string) (placed int, err error) {
	stmt, err := tx.PrepareContext(ctx, placeIfAbsentSQL)
	if err != nil {
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer func() {
		if cerr := stmt.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close the statement: %w", cerr))
		}
	}()
	for _, p := range ps {
		res, err := stmt.ExecContext(ctx, runID, p.DocumentID, nullIfEmpty(p.InterestID), p.Similarity, now)
		if err != nil {
			return 0, fmt.Errorf("place document %s: %w", p.DocumentID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("place document %s: rows affected: %w", p.DocumentID, err)
		}
		placed += int(n)
	}
	return placed, nil
}

func (s *Insights) Unplaced(ctx context.Context, tenantID, runID string, since time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, unplacedSQL, tenantID, formatTime(since), store.DocStateFetched, runID)
	if err != nil {
		return nil, fmt.Errorf("documents run %s didn't place: %w", runID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan an unplaced document: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("documents run %s didn't place: %w", runID, err)
	}
	return out, nil
}

// pruneRunsSQL deletes the tenant's runs but keep of them, cascading their
// groups, assignments and placements.
func pruneRunsSQL(keep int) string {
	return `DELETE FROM interest_runs WHERE tenant_id = ? AND id NOT IN (` + placeholders(keep) + `)`
}

// PruneRunsExcept implements store.InsightStore. Keeping none would delete
// every run, the current interests with them, so it is refused.
func (s *Insights) PruneRunsExcept(ctx context.Context, tenantID string, keepRunIDs ...string) error {
	if len(keepRunIDs) == 0 {
		return errors.New("prune interest runs: no run to keep")
	}
	if _, err := s.db.ExecContext(ctx, pruneRunsSQL(len(keepRunIDs)), appendArgs([]any{tenantID}, keepRunIDs)...); err != nil {
		return fmt.Errorf("prune interest runs: %w", err)
	}
	return nil
}

// pruneRetiredSQL deletes the tenant's identities retired before a cutoff,
// seeking the partial index of retired identities; their lineage cascades.
// Each deleted identity's foreign keys are checked against the tables that
// reference it, about 1 ms each on the owner's library, once a rebuild.
const pruneRetiredSQL = `DELETE FROM interests WHERE tenant_id = ? AND retired_at IS NOT NULL AND retired_at < ?`

func (s *Insights) PruneRetired(ctx context.Context, tenantID string, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, pruneRetiredSQL, tenantID, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("prune retired interests: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune retired interests: rows affected: %w", err)
	}
	return int(n), nil
}

// trimLineageSQL deletes the lineage rows of other runs than one whose old
// identity is live. It scans the lineage, which the trim keeps to the
// current run's rows and the retired identities'.
const trimLineageSQL = `
	DELETE FROM interest_lineage WHERE run_id <> ?
	  AND old_id IN (SELECT id FROM interests WHERE tenant_id = ? AND retired_at IS NULL)`

func (s *Insights) TrimLineage(ctx context.Context, tenantID, keepRunID string) error {
	if _, err := s.db.ExecContext(ctx, trimLineageSQL, keepRunID, tenantID); err != nil {
		return fmt.Errorf("trim interest lineage: %w", err)
	}
	return nil
}

// scanRun scans runColumns. A missing row is store.ErrNotFound.
func scanRun(sc interface{ Scan(...any) error }) (*store.InterestRun, error) {
	var (
		r                          store.InterestRun
		params, readAt, errMsg     sql.NullString
		finished                   sql.NullString
		mean                       []byte
		started, created, modified string
	)
	o := &r.RunOutcome
	err := sc.Scan(&r.ID, &r.TenantID, &r.Status, &r.Trigger, &o.Kind, &o.SplitCheck, &o.Shape, &r.Grouper,
		&params, &readAt, &mean, &o.NumDocuments, &o.NumAreas, &o.NumInterests, &o.NumLoose, &o.NumUnsorted,
		&o.ChangedDocuments, &o.ChangesSinceSplit, &o.Kept, &o.Created, &o.Split, &o.Merged, &o.Moved, &o.Dissolved,
		&errMsg, &started, &finished, &created, &modified)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan interest run: %w", err)
	}
	if params.Valid && params.String != "" {
		r.Params = json.RawMessage(params.String)
	}
	r.Error = nullableString(errMsg)
	if o.Mean, err = decodeVectorBlob(mean); err != nil {
		return nil, fmt.Errorf("interest run %s: mean: %w", r.ID, err)
	}
	if err := setNullTime(&r.VectorsReadAt, readAt); err != nil {
		return nil, err
	}
	if err := setNullTime(&r.FinishedAt, finished); err != nil {
		return nil, err
	}
	if r.StartedAt, err = parseTime(started); err != nil {
		return nil, err
	}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if r.UpdatedAt, err = parseTime(modified); err != nil {
		return nil, err
	}
	return &r, nil
}

// scanInsightState scans insightStateColumns; a missing row is
// sql.ErrNoRows.
func scanInsightState(sc interface{ Scan(...any) error }) (store.InsightState, error) {
	var (
		st                                store.InsightState
		owed, owedAt, failedAt, lastError sql.NullString
	)
	if err := sc.Scan(&owed, &owedAt, &st.Failures, &failedAt, &lastError); err != nil {
		return store.InsightState{}, err
	}
	st.FreshOwed, st.LastError = store.FreshReason(owed.String), lastError.String
	var err error
	if st.FreshOwedAt, err = parseNullTime(owedAt); err != nil {
		return store.InsightState{}, err
	}
	if st.LastFailureAt, err = parseNullTime(failedAt); err != nil {
		return store.InsightState{}, err
	}
	return st, nil
}

// parseNullTime parses a nullable timestamp: the zero time for NULL.
func parseNullTime(s sql.NullString) (time.Time, error) {
	if !s.Valid {
		return time.Time{}, nil
	}
	return parseTime(s.String)
}

// interestScan holds an identity's nullable columns while they are
// scanned.
type interestScan struct {
	label, summary, source, labeled, retired, retiredRun sql.NullString
	created, updated                                     string
}

// dest are the scan destinations of interestColumns into in.
func (s *interestScan) dest(in *store.Interest) []any {
	return []any{&in.ID, &in.TenantID, &in.Level, &s.label, &s.summary, &s.source, &in.CreatedRunID,
		&s.labeled, &s.retired, &s.retiredRun, &s.created, &s.updated}
}

// fill sets in's fields from the scanned values.
func (s *interestScan) fill(in *store.Interest) error {
	in.Label, in.Summary, in.LabelSource = s.label.String, s.summary.String, store.LabelSource(s.source.String)
	in.RetiredRunID = s.retiredRun.String
	if err := setNullTime(&in.LabeledAt, s.labeled); err != nil {
		return err
	}
	if err := setNullTime(&in.RetiredAt, s.retired); err != nil {
		return err
	}
	var err error
	if in.CreatedAt, err = parseTime(s.created); err != nil {
		return err
	}
	in.UpdatedAt, err = parseTime(s.updated)
	return err
}

// scanInterest scans interestColumns; a missing row is sql.ErrNoRows.
func scanInterest(sc interface{ Scan(...any) error }) (store.Interest, error) {
	var in store.Interest
	var s interestScan
	if err := sc.Scan(s.dest(&in)...); err != nil {
		return store.Interest{}, err
	}
	return in, s.fill(&in)
}

// scanGroup scans groupSelect's columns, and the centroid after them when
// centroid is set.
func scanGroup(sc interface{ Scan(...any) error }, centroid bool) (store.InterestGroup, error) {
	var (
		g             store.InterestGroup
		s             interestScan
		parent, label sql.NullString
		blob          []byte
	)
	dest := slices.Concat([]any{&g.RunID}, s.dest(&g.Interest), []any{&parent, &label, &g.Size, &g.Loose, &g.Cohesion})
	if centroid {
		dest = append(dest, &blob)
	}
	if err := sc.Scan(dest...); err != nil {
		return store.InterestGroup{}, fmt.Errorf("scan interest group: %w", err)
	}
	if err := s.fill(&g.Interest); err != nil {
		return store.InterestGroup{}, err
	}
	g.ParentID, g.ParentLabel = parent.String, label.String
	var err error
	if g.Centroid, err = decodeVectorBlob(blob); err != nil {
		return store.InterestGroup{}, fmt.Errorf("interest %s: centroid: %w", g.ID, err)
	}
	return g, nil
}

// setNullTime sets *dst to the nullable timestamp s: nil for NULL.
func setNullTime(dst **time.Time, s sql.NullString) error {
	*dst = nil
	if !s.Valid {
		return nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return err
	}
	*dst = &t
	return nil
}

// nullIfEmpty is the bind value of a nullable text column the store keeps
// as a Go string: NULL for "".
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
