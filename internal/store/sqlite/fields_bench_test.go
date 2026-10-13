package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/samsar/curio/internal/store"
)

// benchLibrary saves n documents through Ingest, each from one bookmark
// whose title carries its site's name, spread over hosts: the first host
// has largest of them, the others ten each.
func benchLibrary(b *testing.B, db *DB, n, largest int) {
	b.Helper()
	ctx := context.Background()
	bms := NewBookmarks(db)
	saved := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		host := "large.example"
		if i >= largest {
			host = fmt.Sprintf("site%d.example", (i-largest)/10)
		}
		title := fmt.Sprintf("Post %d | Site of %s", i, host)
		folder := "/Bookmarks Bar/Reading"
		bm := &store.Bookmark{TenantID: "local", URL: fmt.Sprintf("https://%s/%d", host, i), Source: store.SourceChrome,
			SavedAt: saved.Add(time.Duration(i) * time.Second), Title: &title, FolderPath: &folder}
		if _, err := bms.Ingest(ctx, bm); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRefreshFields_LargeHost times what deriving a document's fields
// adds to a write on a host of 5,000 documents: the document's own reads
// and writes, and the read of its host's rows to agree the host's name.
// Each run's transaction is rolled back, so every run derives the same.
func BenchmarkRefreshFields_LargeHost(b *testing.B) {
	db := newTestDB(b)
	ctx := context.Background()
	benchLibrary(b, db, 5000, 5000)
	var id string
	if err := db.QueryRow(`SELECT id FROM documents ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		b.Fatal(err)
	}
	// What a new bookmark changes: the document's fields to derive again.
	if _, err := db.Exec(`UPDATE bookmarks SET title = 'Retitled | Site of large.example' WHERE document_id = ?`,
		id); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			b.Fatal(err)
		}
		if err := db.refreshFields(ctx, tx, "local", id); err != nil {
			b.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIngest times saving a bookmark of a new page, which creates its
// document and queues its fetch, in a library of 7,500 documents whose
// largest host has 350: on that host, and on a host of ten.
func BenchmarkIngest(b *testing.B) {
	db := newTestDB(b)
	ctx := context.Background()
	benchLibrary(b, db, 7500, 350)
	bms := NewBookmarks(db)
	for _, host := range []string{"large.example", "site0.example"} {
		b.Run(host, func(b *testing.B) {
			i := 0
			for b.Loop() {
				i++
				title := fmt.Sprintf("New %d | Site of %s", i, host)
				bm := &store.Bookmark{TenantID: "local", URL: fmt.Sprintf("https://%s/new/%d", host, i),
					Source: store.SourceChrome, SavedAt: time.Now().UTC(), Title: &title}
				if _, err := bms.Ingest(ctx, bm); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
