package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/samsar/curio/internal/store"
)

// benchLibrary saves n documents through Ingest, each from one bookmark
// whose title carries its site's name, spread over hosts: the first host
// has largest of them, the others ten each. It returns the first host's
// documents, in the order they were saved.
func benchLibrary(b *testing.B, db *DB, n, largest int) []string {
	b.Helper()
	ctx := context.Background()
	bms := NewBookmarks(db)
	saved := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var ids []string
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
		if i < largest {
			ids = append(ids, *bm.DocumentID)
		}
	}
	return ids
}

// BenchmarkApplyFetch times recording a fetch that retitles a document
// already saved, on a host of 10, 1,000 and 10,000 documents, the
// library's only host. Every iteration fetches one of the host's
// documents again, so the host keeps its size, and its titles keep the
// suffix the host agrees on, so no fetch renames the host: the time is a
// write's own, and the host's agreement is two reads of its vote counts
// whatever its size.
func BenchmarkApplyFetch(b *testing.B) {
	for _, size := range []int{10, 1000, 10000} {
		b.Run(fmt.Sprintf("host of %d", size), func(b *testing.B) {
			db := newTestDB(b)
			db.SetCardsJobDelay(time.Hour)
			ctx := context.Background()
			ids := benchLibrary(b, db, size, size)
			ids = ids[:min(len(ids), 100)]
			docs, exts := NewDocuments(db), NewExtractions(db)
			extractions := make([]string, len(ids))
			for i, id := range ids {
				ext := &store.DocumentExtraction{DocumentID: id, Fetcher: "bench", Status: store.ExtractionStatusOK}
				if err := exts.Create(ctx, ext); err != nil {
					b.Fatal(err)
				}
				extractions[i] = ext.ID
			}
			i := 0
			for b.Loop() {
				title := fmt.Sprintf("Fetched %d | Site of large.example", i)
				if err := docs.ApplyFetch(ctx, ids[i%len(ids)], store.FetchedMetadata{ExtractionID: extractions[i%len(ids)],
					ContentType: store.ContentTypeArticle, Title: &title}); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	}
}

// BenchmarkRefreshFields_Rename times a refresh that changes the name a
// host of 5,000 documents agrees on: its first document's page now
// declares a name, which every sibling without one of its own takes, its
// site and its card rewritten. Each iteration's transaction is rolled
// back, so every iteration renames the same 4,999 siblings. This is the
// cost that grows with a host: its siblings' rows hold the name, so a
// change of name writes each of them.
func BenchmarkRefreshFields_Rename(b *testing.B) {
	db := newTestDB(b)
	ctx := context.Background()
	id := benchLibrary(b, db, 5000, 5000)[0]
	meta, err := json.Marshal(map[string]string{store.ExtractionMetaSiteName: "Large Example"})
	if err != nil {
		b.Fatal(err)
	}
	ext := &store.DocumentExtraction{DocumentID: id, Fetcher: "bench", Status: store.ExtractionStatusOK,
		ExtractionMeta: meta}
	if err := NewExtractions(db).Create(ctx, ext); err != nil {
		b.Fatal(err)
	}
	// The extraction becomes current behind the store's back, so the
	// stored fields still say nothing of the name, and the refresh finds it.
	if _, err := db.Exec(`UPDATE documents SET current_extraction_id = ? WHERE id = ?`, ext.ID, id); err != nil {
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
// largest host has 350: on that host, and on a host of ten. Each
// iteration adds a document to the host it saves on, so a long run
// measures hosts of thousands; the host's agreement reads its vote
// counts, so the growth doesn't change what a save costs.
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
