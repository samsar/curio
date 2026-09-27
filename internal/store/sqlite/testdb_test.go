package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// vecDim is the width of this package's test databases' vector index: a
// new home's, so newTestDB resizes the table migration 001 creates, as the
// daemon does for a new home.
const vecDim = 1024

// newTestDB is sqlitetest.NewDB for this package's own tests, which can't
// import sqlitetest without an import cycle. Its vector index is vecDim
// wide.
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
	if _, err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	if err := EnsureVectorIndex(context.Background(), db, vecDim); err != nil {
		t.Fatalf("size test database's vector index: %v", err)
	}
	return db
}
