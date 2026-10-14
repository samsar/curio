package sqlite

import (
	"context"
	"fmt"

	"github.com/samsar/curio/internal/store"
)

// sqlite-vec applies a KNN query's k cutoff before any other predicate,
// so a list that filters the neighbours it reads over-fetches them. That
// covers the predicates every search has (searchedDocSQL) and leaves out
// one document (find-related), but a filter that narrows the library to a
// small part of it would find few of its documents among even the
// nearest vec0MaxK. So a narrowing filter's KNN ranks the rows it leaves
// exactly, through vec0's primary-key IN constraint, which scans the
// listed rows alone, as long as they are at most the DB's
// exactKNNMaxIDs. A larger set over-fetches, as many neighbours as its
// share of the index calls for: the unfiltered over-fetch's k divided by
// that share, at most vec0MaxK. In a library the size of the one the
// bound was measured on, such a set is a large enough share of it that
// those neighbours usually hold a list's depth of its rows; in a far
// larger one they may not. See docs/decisions.md "Saved dates and search
// filters".

// vec0MaxK is the largest k sqlite-vec's KNN takes (v0.1.6: "k value in
// knn query too large").
const vec0MaxK = 4096

// maxVectorOverfetch caps how many neighbors a vector search reads before
// its document predicates apply, for a filter that doesn't narrow.
const maxVectorOverfetch = 1000

// overfetchK is how many neighbours a KNN list reads for limit hits under
// a filter that doesn't narrow.
func overfetchK(limit int) int { return min(limit*10, maxVectorOverfetch) }

// DefaultExactKNNMaxIDs is the most IDs a KNN list ranks exactly: the
// largest round count whose exact ranking, measured on the owner's
// library, costs no more than the over-fetch it replaces. vec0's cost is
// per listed ID, so the bound counts IDs (chunks or cards), never
// documents, which hold from one chunk to hundreds.
const DefaultExactKNNMaxIDs = 15_000

// The counts of every row each vector index holds, which a set's share of
// it is taken against: a chunk has its vector while it exists (the chunks
// triggers keep chunks_vec in step), and a card exactly while it is
// embedded. Each is a pass over a small index, a fraction of a millisecond
// over the owner's library, so it is counted for each search that needs
// it rather than cached.
const (
	chunkIndexCountSQL = `SELECT count(*) FROM chunks`
	cardIndexCountSQL  = `SELECT count(*) FROM document_cards WHERE embedded_at IS NOT NULL`
)

// rowSet is a subquery listing the IDs a KNN list may return under a
// narrowing filter, and its arguments.
type rowSet struct {
	sql  string
	args []any
}

// countSQL counts the set's IDs: every one, since an over-fetch is sized
// by how many there are.
func (s rowSet) countSQL() (string, []any) {
	return "SELECT count(*) FROM (" + s.sql + ")", s.args
}

// existsSQL asks whether the set lists any ID.
func (s rowSet) existsSQL() (string, []any) {
	return "SELECT EXISTS (" + s.sql + ")", s.args
}

// knnPath is how a KNN list reads a narrowing filter's neighbours.
type knnPath int

const (
	// knnNone: the filter leaves no row, and the list reads nothing.
	knnNone knnPath = iota
	// knnExact: vec0 ranks the set's rows alone (rowSet in the KNN's IN).
	knnExact
	// knnOverFetch: vec0's nearest neighbours are filtered after the
	// cutoff: overfetchK of them for a filter that doesn't narrow, and as
	// many as its share calls for (sizedOverFetchK) for a set too large to
	// rank exactly.
	knnOverFetch
)

// knnQuery is the statement a KNN list runs, the path it takes, and for
// a narrowing filter, the set it reads and how many IDs it lists.
type knnQuery struct {
	path knnPath
	sql  string
	args []any
	set  rowSet
	n    int
}

// chooseKNNPath counts set's IDs and picks the path that reads them,
// returning the count too.
func (d *DB) chooseKNNPath(ctx context.Context, set rowSet) (knnPath, int, error) {
	q, args := set.countSQL()
	var n int
	if err := d.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, 0, fmt.Errorf("count the rows the filter leaves: %w", err)
	}
	switch {
	case n == 0:
		return knnNone, 0, nil
	case int64(n) > d.exactKNNMaxIDs.Load():
		return knnOverFetch, n, nil
	}
	return knnExact, n, nil
}

// sizedOverFetchK is how many neighbours a list reads to over-fetch a
// set of n of the rows of the index indexSQL counts, where it reads
// unfiltered without a narrowing filter: unfiltered divided by the set's
// share of the index (overFetchKFor).
func (d *DB) sizedOverFetchK(ctx context.Context, indexSQL string, unfiltered, n int) (int, error) {
	var total int
	if err := d.QueryRowContext(ctx, indexSQL).Scan(&total); err != nil {
		return 0, fmt.Errorf("count the vector index's rows: %w", err)
	}
	return overFetchKFor(unfiltered, n, total), nil
}

// overFetchKFor is unfiltered divided by a set's share of an index, n of
// its total rows, rounded up: the nearest that many then hold about as
// many of the set's rows as the nearest unfiltered hold of the index's.
// It is at least unfiltered, should the counts disagree, and at most
// vec0MaxK.
func overFetchKFor(unfiltered, n, total int) int {
	if n <= 0 {
		return vec0MaxK
	}
	k := (unfiltered*total + n - 1) / n
	return min(max(k, unfiltered), vec0MaxK)
}

// checkExactKNN tells an exact KNN that found nothing from vec0's silent
// failure: an IN list naming a row vec0 holds no vector for makes the KNN
// return no rows and no error, whatever else the list holds. When set
// still lists rows, the KNN should have ranked them, so a vector is
// missing: the error wraps store.ErrVectorIndexInconsistent. When it lists
// none, the set changed since it was counted, and the empty answer stands.
// what names the rows, for the error.
func (d *DB) checkExactKNN(ctx context.Context, set rowSet, what string) error {
	var listed bool
	q, args := set.existsSQL()
	if err := d.QueryRowContext(ctx, q, args...).Scan(&listed); err != nil {
		return fmt.Errorf("check the rows the filter leaves: %w", err)
	}
	if listed {
		return fmt.Errorf("%w: a %s the filter leaves has no vector, so the vector index ranks none of them; "+
			"`curio reindex --all` embeds them again", store.ErrVectorIndexInconsistent, what)
	}
	return nil
}

// warnInconsistent is the store's one warning, once per index: it found
// the index missing a vector of a row it lists (err, from checkExactKNN),
// and its filtered searches over-fetch instead, which ranks what the
// nearest neighbours hold of the filter's rows rather than all of them.
// A search that lost its semantic lists for one missing vector would be
// worse; the warning says how to repair it.
func (d *DB) warnInconsistent(index string, err error) {
	if _, warned := d.inconsistent.LoadOrStore(index, true); warned {
		return
	}
	d.logger().Warn("vector search: the vector index is missing vectors; a narrow filter's search reads the "+
		"nearest neighbours instead of ranking its rows exactly, and may miss some of them", "index", index,
		"err", err)
}
