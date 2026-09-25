// Package sqlitetest provides migrated, throwaway SQLite databases for tests,
// the way net/http/httptest provides servers. It is test support only:
// depguard keeps production code from importing it.
package sqlitetest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samsar/curio/internal/store/sqlite"
)

// NewDB returns a fresh database under t.TempDir() with every migration
// applied, closed when the test ends. It lives on disk rather than in
// ":memory:" because database/sql pools connections, and each in-memory
// connection would see a different database.
func NewDB(t testing.TB) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "curio.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	if _, err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}
