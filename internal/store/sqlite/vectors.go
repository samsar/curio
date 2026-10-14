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

// The vector indexes' width is the home's, recorded in its marker when the
// home is created, so it can't be a migration's: goose SQL takes no
// parameters, and an applied migration never changes. Migration 001 still
// creates chunks_vec at FLOAT[768]; EnsureVectorIndex, which the daemon
// runs after migrating, replaces that empty table on a new database,
// creates the others, and checks the width of each on every start after.

// vectorIndex is one of the vec0 tables, each a vector per row of the
// table it indexes, every one at the home's width.
type vectorIndex struct {
	table string
	// createPrefix and createSuffix frame the width in the table's CREATE
	// statement (createSQL).
	createPrefix, createSuffix string
	// alongside are run in the transaction that creates the table: what
	// must exist, or change, exactly when it does.
	alongside []string
}

// createCardVectorTriggerSQL deletes a card's vector with its card, so
// every way a card goes, foreign-key cascades included, takes its vector:
// a vec0 point lookup by primary key. It names document_cards_vec, so it
// is created with it: a trigger naming a table that doesn't exist fails
// when it fires.
const createCardVectorTriggerSQL = `CREATE TRIGGER IF NOT EXISTS trg_document_cards_delete
AFTER DELETE ON document_cards BEGIN
    DELETE FROM document_cards_vec WHERE document_id = old.document_id;
END`

// staleEveryCardSQL makes every card stale: a new card vector table holds
// no vector, whatever the cards say they were embedded as.
const staleEveryCardSQL = `UPDATE document_cards SET embedded_text = NULL, embedded_at = NULL`

// vectorIndexes are the vector indexes EnsureVectorIndex keeps: the
// chunks', and the document cards'.
var vectorIndexes = []vectorIndex{
	{
		table: "chunks_vec",
		createPrefix: `CREATE VIRTUAL TABLE chunks_vec USING vec0 (
    chunk_id   TEXT PRIMARY KEY,
    embedding  FLOAT[`,
		createSuffix: `]
)`,
	},
	{
		table: "document_cards_vec",
		createPrefix: `CREATE VIRTUAL TABLE document_cards_vec USING vec0 (
    document_id TEXT PRIMARY KEY,
    embedding   FLOAT[`,
		createSuffix: `]
)`,
		alongside: []string{createCardVectorTriggerSQL, staleEveryCardSQL},
	},
}

// vectorIndexDeclSQL reads the statement a table was created with, the
// only place SQLite records a vector index's width.
const vectorIndexDeclSQL = `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`

// createSQL is ix's CREATE statement at dim, which the caller has checked
// is in [1, store.MaxEmbeddingDim]: an int is all that reaches the SQL.
func (ix vectorIndex) createSQL(dim int) string {
	return ix.createPrefix + strconv.Itoa(dim) + ix.createSuffix
}

// hasRowsSQL asks whether ix holds any vector, and dropSQL drops it. The
// table's name is one of vectorIndexes', never input.
func (ix vectorIndex) hasRowsSQL() string { return `SELECT EXISTS (SELECT 1 FROM ` + ix.table + `)` }

func (ix vectorIndex) dropSQL() string { return `DROP TABLE ` + ix.table }

// vectorWidthRE finds the width in a vector index's CREATE statement.
var vectorWidthRE = regexp.MustCompile(`(?i)\bembedding\s+float\[(\d+)\]`)

// VectorWidthError: a vector index holds vectors of another width than
// the home's. EnsureVectorIndex changed nothing of it.
type VectorWidthError struct {
	Path  string // the database
	Table string // the vector index
	Have  int    // its width
	Want  int    // the home's
}

func (e *VectorWidthError) Error() string {
	return fmt.Sprintf("%s: the vector index %s holds %d-dimensional vectors, but this home's embedding width is %d; "+
		"the database belongs to another home, or its marker was edited", e.Path, e.Table, e.Have, e.Want)
}

// EnsureVectorIndex makes every vector index dim wide: chunks_vec, and
// document_cards_vec with the trigger that deletes a card's vector with
// its card. It creates a table that is missing and does nothing to one
// that already has that width. A table of another width is dropped and
// created again at dim, in one transaction, only while it holds no
// vector; when one holds vectors, every table is left alone and that one
// reported as a *VectorWidthError, since rebuilding it would discard
// every embedding. Whenever it creates the card vector table, every card
// becomes stale, to be embedded into it. Idempotent.
func EnsureVectorIndex(ctx context.Context, db *DB, dim int) error {
	if dim < 1 || dim > store.MaxEmbeddingDim {
		return fmt.Errorf("vector index width must be in [1, %d], got %d", store.MaxEmbeddingDim, dim)
	}
	var resize []vectorIndex
	for _, ix := range vectorIndexes {
		have, err := vectorIndexWidth(ctx, db, ix.table)
		if err != nil {
			return err
		}
		if have == dim {
			continue
		}
		if err := ix.checkEmpty(ctx, db, db.path, have, dim); err != nil {
			return err
		}
		resize = append(resize, ix)
	}
	for _, ix := range resize {
		if err := resizeVectorIndex(ctx, db, ix, dim); err != nil {
			return err
		}
	}
	return nil
}

// checkEmpty is a *VectorWidthError when ix, have wide, holds vectors, for
// a home dim wide; nil for a missing table (have 0) or an empty one.
func (ix vectorIndex) checkEmpty(ctx context.Context, q rowQuerier, path string, have, dim int) error {
	if have == 0 {
		return nil
	}
	var hasRows bool
	if err := q.QueryRowContext(ctx, ix.hasRowsSQL()).Scan(&hasRows); err != nil {
		return fmt.Errorf("check %s for vectors: %w", ix.table, err)
	}
	if hasRows {
		return &VectorWidthError{Path: path, Table: ix.table, Have: have, Want: dim}
	}
	return nil
}

// resizeVectorIndex creates ix at dim, replacing an empty table of
// another width. It decides again under the write lock, which BeginTx
// takes at once, so nothing can write a vector between the check and the
// drop.
func resizeVectorIndex(ctx context.Context, db *DB, ix vectorIndex, dim int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin vector index resize: %w", err)
	}
	defer tx.Rollback()

	have, err := vectorIndexWidth(ctx, tx, ix.table)
	if err != nil {
		return err
	}
	if have == dim {
		return nil
	}
	if err := ix.checkEmpty(ctx, tx, db.path, have, dim); err != nil {
		return err
	}
	if have != 0 {
		if _, err := tx.ExecContext(ctx, ix.dropSQL()); err != nil {
			return fmt.Errorf("drop %d-dimensional %s: %w", have, ix.table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, ix.createSQL(dim)); err != nil {
		return fmt.Errorf("create %d-dimensional %s: %w", dim, ix.table, err)
	}
	for _, q := range ix.alongside {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("create %s: %w", ix.table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s resize: %w", ix.table, err)
	}
	return nil
}

// chunkVectorBlocksSQL counts chunks_vec's vec0 chunks: the blocks of
// slots its vectors are stored in, a row each in its shadow table.
const chunkVectorBlocksSQL = `SELECT count(*) FROM chunks_vec_chunks`

// ChunkVectorBlocks is how many vec0 chunks, blocks of 1,024 slots,
// chunks_vec stores its vectors in. sqlite-vec inserts only into its
// newest and never reuses a slot freed in an older one, so a re-index
// that deleted and inserted its chunks again would grow it; one in place
// leaves it as it was.
func ChunkVectorBlocks(ctx context.Context, db *DB) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, chunkVectorBlocksSQL).Scan(&n); err != nil {
		return 0, fmt.Errorf("count chunks_vec's blocks: %w", err)
	}
	return n, nil
}

// VectorIndexWidth is the width chunks_vec was created with, as its
// CREATE statement declares it, or 0 when there is no chunks_vec.
func VectorIndexWidth(ctx context.Context, db *DB) (int, error) {
	return vectorIndexWidth(ctx, db, vectorIndexes[0].table)
}

// vectorIndexWidth is the width table was created with, or 0 when there
// is no such table.
func vectorIndexWidth(ctx context.Context, q rowQuerier, table string) (int, error) {
	var decl string
	err := q.QueryRowContext(ctx, vectorIndexDeclSQL, table).Scan(&decl)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read %s's declaration: %w", table, err)
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
