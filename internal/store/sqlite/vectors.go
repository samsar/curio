package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/samsar/curio/internal/store"
)

// The vector index's width is the home's, recorded in its marker when the
// home is created, so it can't be a migration's: goose SQL takes no
// parameters, and an applied migration never changes. Migration 001 still
// creates chunks_vec at FLOAT[768]; EnsureVectorIndex, which the daemon runs
// after migrating, replaces that empty table on a new database and checks
// the width on every start after.
const (
	// vectorIndexDeclSQL reads the statement chunks_vec was created with,
	// the only place SQLite records its width.
	vectorIndexDeclSQL = `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'chunks_vec'`
	// vectorIndexHasRowsSQL asks whether chunks_vec holds any vector.
	vectorIndexHasRowsSQL = `SELECT EXISTS (SELECT 1 FROM chunks_vec)`
	dropVectorIndexSQL    = `DROP TABLE chunks_vec`
	// createVectorIndexPrefix and createVectorIndexSuffix frame the width
	// in chunks_vec's CREATE statement (createVectorIndexSQL).
	createVectorIndexPrefix = `CREATE VIRTUAL TABLE chunks_vec USING vec0 (
    chunk_id   TEXT PRIMARY KEY,
    embedding  FLOAT[`
	createVectorIndexSuffix = `]
)`
)

// vectorWidthRE finds the width in chunks_vec's CREATE statement.
var vectorWidthRE = regexp.MustCompile(`(?i)\bembedding\s+float\[(\d+)\]`)

// createVectorIndexSQL is chunks_vec's CREATE statement at dim, which the
// caller has checked is in [1, store.MaxEmbeddingDim]: an int is all that
// reaches the SQL.
func createVectorIndexSQL(dim int) string {
	return createVectorIndexPrefix + strconv.Itoa(dim) + createVectorIndexSuffix
}

// VectorWidthError: chunks_vec holds vectors of another width than the
// home's. EnsureVectorIndex changed nothing.
type VectorWidthError struct {
	Path string // the database
	Have int    // chunks_vec's width
	Want int    // the home's
}

func (e *VectorWidthError) Error() string {
	return fmt.Sprintf("%s: the vector index holds %d-dimensional vectors, but this home's embedding width is %d; "+
		"the database belongs to another home, or its marker was edited", e.Path, e.Have, e.Want)
}

// EnsureVectorIndex makes chunks_vec, the vector index, dim wide. It creates
// the table when it is missing and does nothing when it already has that
// width. A table of another width is dropped and created again at dim, in
// one transaction, only while it holds no vector; one that holds vectors is
// left alone and reported as a *VectorWidthError, since rebuilding it would
// discard every embedding. Idempotent.
func EnsureVectorIndex(ctx context.Context, db *DB, dim int) error {
	if dim < 1 || dim > store.MaxEmbeddingDim {
		return fmt.Errorf("vector index width must be in [1, %d], got %d", store.MaxEmbeddingDim, dim)
	}
	have, err := vectorIndexWidth(ctx, db)
	if err != nil {
		return err
	}
	if have == dim {
		return nil
	}
	return resizeVectorIndex(ctx, db, dim)
}

// resizeVectorIndex creates chunks_vec at dim, replacing an empty table of
// another width. It decides again under the write lock, which BeginTx takes
// at once, so nothing can write a vector between the check and the drop.
func resizeVectorIndex(ctx context.Context, db *DB, dim int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin vector index resize: %w", err)
	}
	defer tx.Rollback()

	have, err := vectorIndexWidth(ctx, tx)
	if err != nil {
		return err
	}
	if have == dim {
		return nil
	}
	if have != 0 {
		var hasRows bool
		if err := tx.QueryRowContext(ctx, vectorIndexHasRowsSQL).Scan(&hasRows); err != nil {
			return fmt.Errorf("check vector index for vectors: %w", err)
		}
		if hasRows {
			return &VectorWidthError{Path: db.path, Have: have, Want: dim}
		}
		if _, err := tx.ExecContext(ctx, dropVectorIndexSQL); err != nil {
			return fmt.Errorf("drop %d-dimensional vector index: %w", have, err)
		}
	}
	if _, err := tx.ExecContext(ctx, createVectorIndexSQL(dim)); err != nil {
		return fmt.Errorf("create %d-dimensional vector index: %w", dim, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit vector index resize: %w", err)
	}
	return nil
}

// VectorIndexWidth is the width chunks_vec was created with, as its CREATE
// statement declares it, or 0 when there is no chunks_vec.
func VectorIndexWidth(ctx context.Context, db *DB) (int, error) {
	return vectorIndexWidth(ctx, db)
}

func vectorIndexWidth(ctx context.Context, q rowQuerier) (int, error) {
	var decl string
	err := q.QueryRowContext(ctx, vectorIndexDeclSQL).Scan(&decl)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read vector index declaration: %w", err)
	}
	m := vectorWidthRE.FindStringSubmatch(decl)
	if m == nil {
		return 0, fmt.Errorf("vector index declaration has no embedding width: %q", decl)
	}
	width, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("vector index declaration %q: %w", decl, err)
	}
	if width < 1 {
		return 0, fmt.Errorf("vector index declaration has a width of %d: %q", width, decl)
	}
	return width, nil
}
