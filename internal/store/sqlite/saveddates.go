package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/store"
)

// A bookmark's saved-date class is derived from the bookmarks of its
// tenant and source alone (docmeta.SavedDates), so the store derives a
// group's classes whole, whenever it likes: after each import of the
// source (Bookmarks.ClassifySavedDates) and for every group at start
// (EnsureSavedDates). Reading the group and writing what changed share one
// BEGIN IMMEDIATE transaction, so they see one state of it.

// The statements of the classification.
const (
	// savedDateRowsSQL reads what the rule reads of a tenant's bookmarks,
	// with each one's class, in saved order through
	// idx_bookmarks_tenant_saved. savedDateSourceRowsSQL reads one source's
	// the same way, the source checked on each row: a source's bookmarks
	// are most of a tenant's.
	savedDateRowsSQL = `SELECT id, source, coalesce(folder_path, ''), saved_at, created_at, dateless, saved_date
	FROM bookmarks WHERE tenant_id = ? ORDER BY saved_at, id`
	savedDateSourceRowsSQL = `SELECT id, source, coalesce(folder_path, ''), saved_at, created_at, dateless, saved_date
	FROM bookmarks WHERE tenant_id = ? AND source = ? ORDER BY saved_at, id`
	// setSavedDateSQL writes one bookmark's class, by primary key.
	setSavedDateSQL = `UPDATE bookmarks SET saved_date = ?, updated_at = ` + sqlNow + ` WHERE id = ?`
	// markDatelessSQL marks the tenant's bookmarks from a source that
	// never gives a date (its argument, Safari's) dateless where they
	// aren't, which a daemon that predates the column leaves them.
	markDatelessSQL = `UPDATE bookmarks SET dateless = 1, updated_at = ` + sqlNow + `
	WHERE tenant_id = ? AND source = ? AND dateless = 0`
	// bookmarkTenantsSQL lists the tenants with bookmarks, in order.
	bookmarkTenantsSQL = `SELECT DISTINCT tenant_id FROM bookmarks ORDER BY tenant_id`
)

// savedDateClasses converts the rule's classes to the store's; a test
// holds the two sets equal.
var savedDateClasses = map[docmeta.SavedDate]store.SavedDate{
	docmeta.SavedExact:   store.SavedDateExact,
	docmeta.SavedApprox:  store.SavedDateApprox,
	docmeta.SavedUnknown: store.SavedDateUnknown,
}

// savedDateCounts are how many bookmarks a classification left in each
// class, how many it changed, the bookmarks it skipped (store.
// SavedDatesClassified), and how many it marked dateless.
type savedDateCounts struct {
	changed int
	byClass map[store.SavedDate]int
	skipped []string
	marked  int
}

// ClassifySavedDates implements store.BookmarkStore.
func (s *Bookmarks) ClassifySavedDates(ctx context.Context, tenantID, source string) (store.SavedDatesClassified,
	error) {
	counts, err := classifyTenant(ctx, s.db, tenantID, source, false)
	if err != nil {
		return store.SavedDatesClassified{}, err
	}
	return store.SavedDatesClassified{Changed: counts.changed, Skipped: counts.skipped}, nil
}

// classifyTenant classifies the tenant's bookmarks from source, every
// source when it is "", in a transaction of its own. With markSafari it
// first marks the tenant's Safari bookmarks dateless: Safari's bookmarks
// file holds no dates, so each of them is, and a daemon that predates
// the column wrote its default.
func classifyTenant(ctx context.Context, db *DB, tenantID, source string, markSafari bool) (savedDateCounts,
	error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return savedDateCounts{}, fmt.Errorf("classify saved dates: begin: %w", err)
	}
	defer tx.Rollback()
	var marked int64
	if markSafari {
		res, err := tx.ExecContext(ctx, markDatelessSQL, tenantID, store.SourceSafari)
		if err != nil {
			return savedDateCounts{}, fmt.Errorf("mark tenant %s's Safari bookmarks dateless: %w", tenantID, err)
		}
		if marked, err = res.RowsAffected(); err != nil {
			return savedDateCounts{}, fmt.Errorf("mark tenant %s's Safari bookmarks dateless: %w", tenantID, err)
		}
	}
	counts, err := classifySavedDates(ctx, tx, tenantID, source)
	if err != nil {
		return savedDateCounts{}, fmt.Errorf("classify saved dates of tenant %s: %w", tenantID, err)
	}
	counts.marked = int(marked)
	if err := tx.Commit(); err != nil {
		return savedDateCounts{}, fmt.Errorf("classify saved dates: commit: %w", err)
	}
	return counts, nil
}

// classifySavedDates derives the classes of the tenant's bookmarks from
// source, every source when it is "", inside tx, and writes those that
// changed, setting their updated_at. A bookmark whose times don't parse is
// unknown, and the rule classifies the others without it.
func classifySavedDates(ctx context.Context, tx *sql.Tx, tenantID, source string) (savedDateCounts, error) {
	read, err := readSavedDateRows(ctx, tx, tenantID, source)
	if err != nil {
		return savedDateCounts{}, err
	}
	counts := savedDateCounts{byClass: map[store.SavedDate]int{}}
	var changed []classWrite
	set := func(id string, stored, class store.SavedDate) {
		counts.byClass[class]++
		if class != stored {
			changed = append(changed, classWrite{id: id, class: class})
		}
	}
	for i, class := range docmeta.SavedDates(read.rows) {
		set(read.rows[i].ID, read.stored[i], savedDateClasses[class])
	}
	for _, sk := range read.skipped {
		set(sk.id, sk.class, store.SavedDateUnknown)
		counts.skipped = append(counts.skipped, sk.id)
	}
	if err := writeSavedDates(ctx, tx, changed); err != nil {
		return savedDateCounts{}, err
	}
	counts.changed = len(changed)
	return counts, nil
}

// classWrite is a bookmark's class to write.
type classWrite struct {
	id    string
	class store.SavedDate
}

// savedDateRead is what readSavedDateRows read: the rule's input and each
// row's stored class, parallel, and the bookmarks whose times don't parse
// with their stored classes.
type savedDateRead struct {
	rows    []docmeta.SavedRow
	stored  []store.SavedDate
	skipped []classWrite
}

// readSavedDateRows reads the rule's input of the tenant's bookmarks from
// source, every source when it is "", and each one's stored class. A
// bookmark whose saved_at or created_at doesn't parse is set aside, by
// ID: one damaged row would otherwise stop every classification of its
// tenant, at every start and import.
func readSavedDateRows(ctx context.Context, tx *sql.Tx, tenantID, source string) (savedDateRead, error) {
	q, args := savedDateRowsSQL, []any{tenantID}
	if source != "" {
		q, args = savedDateSourceRowsSQL, []any{tenantID, source}
	}
	res, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return savedDateRead{}, fmt.Errorf("read the bookmarks: %w", err)
	}
	defer res.Close()
	var out savedDateRead
	for res.Next() {
		var (
			r                  docmeta.SavedRow
			savedAt, createdAt string
			class              store.SavedDate
		)
		if err := res.Scan(&r.ID, &r.Source, &r.Folder, &savedAt, &createdAt, &r.Dateless, &class); err != nil {
			return savedDateRead{}, fmt.Errorf("read the bookmarks: %w", err)
		}
		var savedErr, createdErr error
		r.SavedAt, savedErr = parseTime(savedAt)
		r.CreatedAt, createdErr = parseTime(createdAt)
		if savedErr != nil || createdErr != nil {
			out.skipped = append(out.skipped, classWrite{id: r.ID, class: class})
			continue
		}
		out.rows = append(out.rows, r)
		out.stored = append(out.stored, class)
	}
	if err := res.Err(); err != nil {
		return savedDateRead{}, fmt.Errorf("read the bookmarks: %w", err)
	}
	return out, nil
}

// writeSavedDates writes each of changed, through one prepared statement.
func writeSavedDates(ctx context.Context, tx *sql.Tx, changed []classWrite) (err error) {
	if len(changed) == 0 {
		return nil
	}
	set, err := tx.PrepareContext(ctx, setSavedDateSQL)
	if err != nil {
		return fmt.Errorf("prepare the class write: %w", err)
	}
	defer func() { err = errors.Join(err, set.Close()) }()
	for _, w := range changed {
		if _, err := set.ExecContext(ctx, w.class, w.id); err != nil {
			return fmt.Errorf("write bookmark %s's class: %w", w.id, err)
		}
	}
	return nil
}

// SavedDatesRefresh is what EnsureSavedDates did.
type SavedDatesRefresh struct {
	// Bookmarks is how many bookmarks it classified, Changed how many of
	// them changed class, and Exact, Approx and Unknown how many it left
	// in each.
	Bookmarks, Changed, Exact, Approx, Unknown int
	// MarkedDateless counts the Safari bookmarks it marked dateless.
	MarkedDateless int
	// Skipped are the IDs of the bookmarks whose times don't parse, which
	// it classified unknown (store.SavedDatesClassified).
	Skipped []string
	Took    time.Duration
}

// add counts a tenant's classification.
func (r *SavedDatesRefresh) add(c savedDateCounts) {
	r.Changed += c.changed
	r.Exact += c.byClass[store.SavedDateExact]
	r.Approx += c.byClass[store.SavedDateApprox]
	r.Unknown += c.byClass[store.SavedDateUnknown]
	r.Bookmarks = r.Exact + r.Approx + r.Unknown
	r.MarkedDateless += c.marked
	r.Skipped = append(r.Skipped, c.skipped...)
}

// EnsureSavedDates classifies every bookmark of every tenant and source,
// a tenant a transaction, tenants in order, writing the classes that
// changed, after marking dateless the tenant's Safari bookmarks a daemon
// that predates the column wrote dated. The daemon runs it at start: what
// applies a change of the rule, finishes the classes an interrupted or
// failed classification left, derives again the runs a deleted bookmark
// split, and classifies the rows an older daemon wrote. A second run
// writes nothing. On an error it reports what the tenants before it did,
// which keep their classes.
func EnsureSavedDates(ctx context.Context, db *DB) (done SavedDatesRefresh, err error) {
	began := time.Now()
	defer func() { done.Took = time.Since(began) }()
	tenants, err := bookmarkTenants(ctx, db)
	if err != nil {
		return done, err
	}
	for _, tenantID := range tenants {
		counts, err := classifyTenant(ctx, db, tenantID, "", true)
		if err != nil {
			return done, err
		}
		done.add(counts)
	}
	return done, nil
}

// bookmarkTenants lists the tenants with bookmarks.
func bookmarkTenants(ctx context.Context, db *DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, bookmarkTenantsSQL)
	if err != nil {
		return nil, fmt.Errorf("list the tenants with bookmarks: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, fmt.Errorf("list the tenants with bookmarks: %w", err)
		}
		out = append(out, tenantID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the tenants with bookmarks: %w", err)
	}
	return out, nil
}
