// Package sqlitetest provides migrated, throwaway SQLite databases for tests,
// the way net/http/httptest provides servers. It is test support only:
// depguard keeps production code from importing it.
package sqlitetest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/store/sqlite"
)

// NewDB returns a fresh database under t.TempDir() with every migration
// applied and a vector index as wide as a new home's (the default
// embedding.dim), closed when the test ends. It lives on disk rather than
// in ":memory:" because database/sql pools connections, and each in-memory
// connection would see a different database.
func NewDB(t testing.TB) *sqlite.DB {
	t.Helper()
	return NewDBWithDim(t, config.Default().Embedding.Dim)
}

// NewDBWithDim is NewDB with a vector index dim wide, sized the way the
// daemon sizes a home's (sqlite.EnsureVectorIndex).
func NewDBWithDim(t testing.TB, dim int) *sqlite.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "curio.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	if err := sqlite.EnsureVectorIndex(ctx, db, dim); err != nil {
		t.Fatalf("size test database's vector index: %v", err)
	}
	return db
}

// Width is the width of db's vector index: every vector written to it or
// searched for in it must have that many components.
func Width(t testing.TB, db *sqlite.DB) int {
	t.Helper()
	width, err := sqlite.VectorIndexWidth(context.Background(), db)
	if err != nil {
		t.Fatalf("read the test database's vector width: %v", err)
	}
	return width
}
