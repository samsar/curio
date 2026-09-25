package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// newTestDB is sqlitetest.NewDB for this package's own tests, which can't
// import sqlitetest without an import cycle.
func newTestDB(t testing.TB) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "curio.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}
