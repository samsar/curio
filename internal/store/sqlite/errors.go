package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/samsar/curio/internal/store"
)

// isUniqueViolation reports whether err is a uniqueness violation: a UNIQUE
// constraint (SQLITE_CONSTRAINT_UNIQUE) or a TEXT PRIMARY KEY collision
// (SQLITE_CONSTRAINT_PRIMARYKEY). Both mean the row already exists; CHECK and
// foreign-key violations don't.
func isUniqueViolation(err error) bool {
	var serr sqlite3.Error
	if !errors.As(err, &serr) {
		return false
	}
	return serr.ExtendedCode == sqlite3.ErrConstraintUnique ||
		serr.ExtendedCode == sqlite3.ErrConstraintPrimaryKey
}

// ensureRow turns a write that matched no row into an error wrapping
// store.ErrNotFound for entity.
func ensureRow(res sql.Result, entity string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: rows affected: %w", entity, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", entity, store.ErrNotFound)
	}
	return nil
}
