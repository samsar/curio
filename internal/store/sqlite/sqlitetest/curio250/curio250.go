// Package curio250 places documents into a run of the interests the way curio
// v2.5.0 does, for tests of what a later curio finds after going back to
// 2.5.x for a while. 2.5.x starts on a database migrated past it, since
// goose ignores versions it doesn't know, and knows nothing of the interest
// map: a placement it makes has no place on the map, and one it moves to
// another interest keeps the place it had. It is test support only.
package curio250

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/samsar/curio/internal/store"
)

// The statements, verbatim from tag v2.5.0's
// internal/store/sqlite/insight.go.
const (
	// placeDocumentSQL is 2.5.0's PlaceDocument. Its args are the run, the
	// document, the interest, the similarity, the time, the tenant and
	// the done status.
	placeDocumentSQL = `
	INSERT INTO interest_placements (run_id, document_id, interest_id, similarity, placed_at)
	SELECT ?1, ?2, ?3, ?4, ?5
	WHERE ?1 = (SELECT id FROM interest_runs WHERE tenant_id = ?6 AND status = ?7
	            ORDER BY started_at DESC, rowid DESC LIMIT 1)
	  AND NOT EXISTS (SELECT 1 FROM interest_assignments WHERE run_id = ?1 AND document_id = ?2)
	  AND EXISTS (SELECT 1 FROM documents WHERE id = ?2)
	ON CONFLICT (run_id, document_id) DO UPDATE SET interest_id = excluded.interest_id,
		similarity = excluded.similarity, placed_at = excluded.placed_at`
	// placeIfAbsentSQL is what 2.5.0's PlaceMany runs for each document,
	// inside a transaction that checked the run. Its args are the run,
	// the document, the interest, the similarity and the time.
	placeIfAbsentSQL = `
	INSERT INTO interest_placements (run_id, document_id, interest_id, similarity, placed_at)
	SELECT ?1, ?2, ?3, ?4, ?5
	WHERE NOT EXISTS (SELECT 1 FROM interest_assignments WHERE run_id = ?1 AND document_id = ?2)
	  AND EXISTS (SELECT 1 FROM documents WHERE id = ?2)
	ON CONFLICT (run_id, document_id) DO NOTHING`
)

// timeFormat is the store's timestamp layout, which 2.5.0 writes too.
const timeFormat = "2006-01-02T15:04:05.000Z"

// Execer runs a statement: a *sql.DB, a *sqlite.DB or a *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// PlaceDocument places p as 2.5.0's index jobs do, and reports whether it
// wrote: while p.RunID is the tenant's latest done run, a document the run
// didn't assign is placed into p.InterestID ("" for Unsorted) with no place
// on the map, or, placed already, moved there with the place it had.
func PlaceDocument(t testing.TB, db Execer, tenantID string, p store.Placement) bool {
	t.Helper()
	return exec(t, db, placeDocumentSQL, p.RunID, p.DocumentID, interest(p), p.Similarity, now(), tenantID,
		store.InterestRunDone)
}

// PlaceIfAbsent places p as 2.5.0's sweep does, and reports whether it
// wrote: a document the run didn't assign is placed into p.InterestID with
// no place on the map, unless it is placed already. Unlike the sweep, it
// doesn't check that p.RunID is the latest done run.
func PlaceIfAbsent(t testing.TB, db Execer, p store.Placement) bool {
	t.Helper()
	return exec(t, db, placeIfAbsentSQL, p.RunID, p.DocumentID, interest(p), p.Similarity, now())
}

func exec(t testing.TB, db Execer, query string, args ...any) bool {
	t.Helper()
	res, err := db.ExecContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("place a document as 2.5.0 does: %v", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("place a document as 2.5.0 does: rows affected: %v", err)
	}
	return n > 0
}

// interest is p's interest as 2.5.0 binds it: NULL for Unsorted.
func interest(p store.Placement) sql.NullString {
	return sql.NullString{String: p.InterestID, Valid: p.InterestID != ""}
}

func now() string { return time.Now().UTC().Format(timeFormat) }
