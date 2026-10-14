package sqlite

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// filterWidth is the vector width of the filter tests' libraries: small,
// since what they check is which rows a list reads, not how near.
const filterWidth = 4

// newTestDBWidth is newTestDB with vector indexes dim wide.
func newTestDBWidth(t testing.TB, dim int) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "curio.db"))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	_, err = Migrate(ctx, db)
	require.NoError(t, err)
	require.NoError(t, EnsureVectorIndex(ctx, db, dim))
	return db
}

// filterLib is a synthetic library the filter tests search, every
// document titled, carded and chunked with the same words, so each list
// finds every document a filter leaves.
type filterLib struct {
	t      *testing.T
	ctx    context.Context
	db     *DB
	docs   *Documents
	bms    *Bookmarks
	chunks *Chunks
	index  *DocumentIndex
	tenant string
	names  map[string]string // document ID → name
}

func newFilterLib(t *testing.T, db *DB, tenant string) *filterLib {
	return &filterLib{t: t, ctx: context.Background(), db: db, docs: NewDocuments(db), bms: NewBookmarks(db),
		chunks: NewChunks(db, filterWidth), index: NewDocumentIndex(db, filterWidth), tenant: tenant,
		names: map[string]string{}}
}

// saved is a bookmark of a filterLib document: from the source, by
// default the next of store.Sources, and exact, unless it says.
type saved struct {
	source, folder string
	at             time.Time
	class          store.SavedDate
}

// add writes a document named name at url, published at published (zero
// for none), with one chunk and a card at vec and bookmarks, and returns
// its ID. Each bookmark is stored with its class as given.
func (l *filterLib) add(name, url string, published time.Time, vec float32, bookmarks ...saved) string {
	l.t.Helper()
	d := &store.Document{TenantID: l.tenant, URL: url, ContentType: store.ContentTypeArticle,
		Title: store.NullableString("Kafka retention " + name)}
	if !published.IsZero() {
		d.PublishedAt = &published
	}
	require.NoError(l.t, l.docs.Create(l.ctx, d))
	e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
		FetchedAt: time.Now().UTC()}
	require.NoError(l.t, NewExtractions(l.db).Create(l.ctx, e))
	require.NoError(l.t, l.docs.SetCurrentExtraction(l.ctx, d.ID, e.ID))
	require.NoError(l.t, l.chunks.ReplaceForDocument(l.ctx, d.ID, e.ID, "", nil,
		[]store.ChunkInput{{Text: "kafka retention notes", Embedding: fillVecOf(filterWidth, vec)}}))
	for i, b := range bookmarks {
		bm := &store.Bookmark{TenantID: l.tenant, DocumentID: &d.ID, URL: url,
			Source: cmp.Or(b.source, store.Sources()[i]), FolderPath: store.NullableString(b.folder), SavedAt: b.at}
		require.NoError(l.t, l.bms.Create(l.ctx, bm))
		_, err := l.db.Exec(`UPDATE bookmarks SET saved_date = ? WHERE id = ?`,
			cmp.Or(b.class, store.SavedDateExact), bm.ID)
		require.NoError(l.t, err)
	}
	l.embedCards(vec)
	l.names[d.ID] = name
	return d.ID
}

// embedCards gives every stale card of the library a vector at vec.
func (l *filterLib) embedCards(vec float32) {
	l.t.Helper()
	stale, err := l.index.StaleCards(l.ctx, l.tenant, "", 100)
	require.NoError(l.t, err)
	put := make([]store.CardVector, len(stale))
	for i, c := range stale {
		put[i] = store.CardVector{DocumentID: c.DocumentID, Text: c.Text, Embedding: fillVecOf(filterWidth, vec)}
	}
	_, err = l.index.PutCardVectors(l.ctx, put)
	require.NoError(l.t, err)
}

// found is what every list finds under f, by list, as sorted names.
func (l *filterLib) found(f store.SearchFilters) map[string][]string {
	l.t.Helper()
	out := map[string][]string{}
	add := func(list string, ids []string) {
		names := make([]string, 0, len(ids))
		for _, id := range ids {
			names = append(names, l.names[id])
		}
		slices.Sort(names)
		out[list] = slices.Compact(names)
	}
	chunks, err := l.chunks.BM25Search(l.ctx, l.tenant, `"kafka"`, 100, f)
	require.NoError(l.t, err)
	add("bm25", chunkDocs(chunks))
	vec, err := l.chunks.VectorSearch(l.ctx, l.tenant, fillVecOf(filterWidth, 0), 100, f)
	require.NoError(l.t, err)
	add("vector", chunkDocs(vec))
	fields, err := l.index.SearchFields(l.ctx, l.tenant, `"kafka"`, 100, f)
	require.NoError(l.t, err)
	add("fields", hitIDs(fields))
	titles, err := l.index.SearchTitles(l.ctx, l.tenant, `"kafka" AND "retention"`, 100, f)
	require.NoError(l.t, err)
	add("titles", hitIDs(titles))
	cards, err := l.index.SearchCards(l.ctx, l.tenant, fillVecOf(filterWidth, 0), 100, f)
	require.NoError(l.t, err)
	add("cards", hitIDs(cards))
	return out
}

// assertFound asserts every list finds exactly want under f.
func (l *filterLib) assertFound(f store.SearchFilters, want ...string) {
	l.t.Helper()
	slices.Sort(want)
	if want == nil {
		want = []string{}
	}
	for list, got := range l.found(f) {
		assert.Equal(l.t, want, got, "%s under %+v", list, f)
	}
}

func chunkDocs(hits []store.ChunkHit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.DocumentID
	}
	return ids
}

var (
	windowStart = time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)
	windowEnd   = time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC)
	midWindow   = time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
)

// TestSearchFilters_SavedWindow: the saved window is [after, before) on
// the bookmarks of a known date, exact or approx; a document with no
// such bookmark is undated, kept only with IncludeUndated, and one whose
// known dates all fall outside is left out either way.
func TestSearchFilters_SavedWindow(t *testing.T) {
	l := newFilterLib(t, newTestDBWidth(t, filterWidth), "local")
	exact, approx, unknown := store.SavedDateExact, store.SavedDateApprox, store.SavedDateUnknown
	l.add("at-start", "https://example.com/1", time.Time{}, 0.1, saved{at: windowStart, class: exact})
	l.add("before-end", "https://example.com/2", time.Time{}, 0.2,
		saved{at: windowEnd.Add(-time.Millisecond), class: exact})
	l.add("at-end", "https://example.com/3", time.Time{}, 0.3, saved{at: windowEnd, class: exact})
	l.add("approx", "https://example.com/4", time.Time{}, 0.4, saved{at: midWindow, class: approx})
	l.add("unknown", "https://example.com/5", time.Time{}, 0.5, saved{at: midWindow, class: unknown})
	l.add("outside-and-unknown", "https://example.com/6", time.Time{}, 0.6,
		saved{at: windowStart.Add(-24 * time.Hour), class: exact}, saved{at: midWindow, class: unknown})
	l.add("no-bookmark", "https://example.com/7", time.Time{}, 0.7)

	window := store.SearchFilters{SavedAfter: windowStart, SavedBefore: windowEnd}
	l.assertFound(window, "at-start", "before-end", "approx")
	window.IncludeUndated = true
	l.assertFound(window, "at-start", "before-end", "approx", "unknown", "no-bookmark")
	l.assertFound(store.SearchFilters{SavedAfter: windowEnd}, "at-end")
	l.assertFound(store.SearchFilters{SavedBefore: windowStart}, "outside-and-unknown")
}

// TestSearchFilters_PublishedWindow: a published date in the window
// passes when it is trusted, at most a day after the document's earliest
// exact save; one of a document with no exact save is trusted. An
// untrusted or missing one is undated, kept only with IncludeUndated.
func TestSearchFilters_PublishedWindow(t *testing.T) {
	l := newFilterLib(t, newTestDBWidth(t, filterWidth), "local")
	published := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	dayBefore := published.Add(-24 * time.Hour)
	l.add("saved-a-day-before", "https://example.com/1", published, 0.1,
		saved{at: dayBefore, class: store.SavedDateExact})
	l.add("saved-earlier", "https://example.com/2", published, 0.2,
		saved{at: dayBefore.Add(-time.Millisecond), class: store.SavedDateExact})
	l.add("approx-earlier", "https://example.com/3", published, 0.3,
		saved{at: published.AddDate(0, -1, 0), class: store.SavedDateApprox})
	l.add("no-bookmark", "https://example.com/4", published, 0.4)
	l.add("no-date", "https://example.com/5", time.Time{}, 0.5, saved{at: published, class: store.SavedDateExact})
	l.add("published-later", "https://example.com/6", published.AddDate(0, 2, 0), 0.6)

	window := store.SearchFilters{PublishedAfter: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		PublishedBefore: time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)}
	l.assertFound(window, "saved-a-day-before", "approx-earlier", "no-bookmark")
	window.IncludeUndated = true
	l.assertFound(window, "saved-a-day-before", "approx-earlier", "no-bookmark", "saved-earlier", "no-date")
	l.assertFound(store.SearchFilters{PublishedBefore: published})
	l.assertFound(store.SearchFilters{PublishedAfter: published}, "saved-a-day-before", "approx-earlier",
		"no-bookmark", "published-later")
}

// TestSearchFilters_Site: a site matches its host, www. and every deeper
// subdomain, never another domain ending in its name; every character is
// literal.
func TestSearchFilters_Site(t *testing.T) {
	l := newFilterLib(t, newTestDBWidth(t, filterWidth), "local")
	l.add("bare", "https://example.com/a", time.Time{}, 0.1)
	l.add("www", "https://www.example.com/b", time.Time{}, 0.2)
	l.add("sub", "https://blog.example.com/c", time.Time{}, 0.3)
	l.add("deep", "https://a.b.example.com:8443/d", time.Time{}, 0.4)
	l.add("lookalike", "https://notexample.com/e", time.Time{}, 0.5)
	l.add("underscore", "https://ex_ample.com/f", time.Time{}, 0.6)
	l.add("any-char", "https://exxample.com/g", time.Time{}, 0.7)

	l.assertFound(store.SearchFilters{Site: []string{"example.com"}}, "bare", "www", "sub", "deep")
	l.assertFound(store.SearchFilters{Site: []string{"ex_ample.com"}}, "underscore")
	l.assertFound(store.SearchFilters{Site: []string{"ex_ample.com", "notexample.com"}}, "underscore", "lookalike")
}

// TestSearchFilters_Folder: a folder matches itself and its subfolders on
// path segments, case-sensitively, on any of a document's bookmarks.
func TestSearchFilters_Folder(t *testing.T) {
	l := newFilterLib(t, newTestDBWidth(t, filterWidth), "local")
	at := midWindow
	l.add("tech", "https://example.com/1", time.Time{}, 0.1, saved{folder: "/Tech", at: at})
	l.add("tech-ai", "https://example.com/2", time.Time{}, 0.2, saved{folder: "/Tech/AI", at: at})
	l.add("technology", "https://example.com/3", time.Time{}, 0.3, saved{folder: "/Technology", at: at})
	l.add("lower", "https://example.com/4", time.Time{}, 0.4, saved{folder: "/tech", at: at})
	l.add("second-bookmark", "https://example.com/5", time.Time{}, 0.5, saved{folder: "/Other", at: at},
		saved{folder: "/Tech/Go", at: at})
	l.add("no-folder", "https://example.com/6", time.Time{}, 0.6, saved{at: at})

	l.assertFound(store.SearchFilters{Folder: []string{"/Tech"}}, "tech", "tech-ai", "second-bookmark")
	l.assertFound(store.SearchFilters{Folder: []string{"/Tech/AI", "/Technology"}}, "tech-ai", "technology")
}

// TestSearchFilters_Combined: dimensions AND, values within one OR; each
// bookmark-level dimension is checked on the document's bookmarks on its
// own.
func TestSearchFilters_Combined(t *testing.T) {
	l := newFilterLib(t, newTestDBWidth(t, filterWidth), "local")
	exact := store.SavedDateExact
	l.add("chrome-tech-in", "https://a.example/1", time.Time{}, 0.1,
		saved{source: store.SourceChrome, folder: "/Tech", at: midWindow, class: exact})
	l.add("chrome-tech-out", "https://a.example/2", time.Time{}, 0.2,
		saved{source: store.SourceChrome, folder: "/Tech", at: windowEnd, class: exact})
	l.add("safari-tech-in", "https://b.example/3", time.Time{}, 0.3,
		saved{source: store.SourceSafari, folder: "/Tech", at: midWindow, class: exact})
	// Chrome's bookmark is old, Safari's in the window: source and window
	// pass on different bookmarks.
	l.add("split", "https://c.example/4", time.Time{}, 0.4,
		saved{source: store.SourceChrome, folder: "/Tech", at: windowStart.AddDate(-5, 0, 0), class: exact},
		saved{source: store.SourceSafari, folder: "/Other", at: midWindow, class: exact})

	l.assertFound(store.SearchFilters{Source: []string{store.SourceChrome}, SavedAfter: windowStart,
		SavedBefore: windowEnd}, "chrome-tech-in", "split")
	l.assertFound(store.SearchFilters{Site: []string{"a.example", "b.example"}, Folder: []string{"/Tech"},
		SavedAfter: windowStart, SavedBefore: windowEnd}, "chrome-tech-in", "safari-tech-in")
	l.assertFound(store.SearchFilters{Site: []string{"c.example"}, ContentType: []string{"pdf"}})
}

// TestSearchFilters_EveryListAtItsMost: every list dimension at its most
// values at once stays inside SQLite's expression depth, in every list.
func TestSearchFilters_EveryListAtItsMost(t *testing.T) {
	l := newFilterLib(t, newTestDBWidth(t, filterWidth), "local")
	l.add("one", "https://example.com/1", time.Time{}, 0.1,
		saved{source: store.SourceChrome, folder: "/F0", at: midWindow, class: store.SavedDateExact})
	n := store.MaxFilterValues
	f := store.SearchFilters{SavedAfter: windowStart, PublishedBefore: windowEnd, IncludeUndated: true}
	for i := range n {
		f.Host = append(f.Host, fmt.Sprintf("h%d.example", i))
		f.Site = append(f.Site, fmt.Sprintf("s%d.example", i))
		f.Folder = append(f.Folder, fmt.Sprintf("/F%d", i))
		f.ContentType = append(f.ContentType, string(store.ContentTypeArticle))
		f.Source = append(f.Source, store.SourceChrome)
	}
	f.Host[0], f.Site[0] = "example.com", "example.com"
	require.NoError(t, f.Validate())
	l.assertFound(f, "one")
	count, err := l.index.CountUndated(l.ctx, l.tenant, f)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "no published date")
}

// TestSearchFilters_EmptyIsTodaysSQL: with no filter, the text lists'
// queries are the SQL they were before filters grew, byte for byte, so an
// unfiltered ranking can't change.
func TestSearchFilters_EmptyIsTodaysSQL(t *testing.T) {
	bm25, bm25Args := bm25Query("local", `"kafka"`, 800, store.SearchFilters{})
	assert.Equal(t, `
	SELECT c.id, c.document_id, bm25(chunks_fts) AS bm25_score
	FROM chunks_fts
	JOIN chunks c    ON c.seq = chunks_fts.rowid
	JOIN documents d ON d.id = c.document_id
	WHERE chunks_fts MATCH ?
	  AND d.tenant_id = ? AND d.state NOT IN (?, ?)
	ORDER BY bm25_score, c.seq
	LIMIT ?`, bm25)
	assert.Equal(t, []any{`"kafka"`, "local", store.DocStateFailed, store.DocStateDead, 800}, bm25Args)

	const fieldsSQL = `
	SELECT f.document_id, %s AS score
	FROM document_fields_fts
	JOIN document_fields f ON f.seq = document_fields_fts.rowid
	JOIN documents d       ON d.id = f.document_id
	WHERE document_fields_fts MATCH ?
	  AND d.tenant_id = ?
	ORDER BY score, f.seq
	LIMIT ?`
	fields, fieldsArgs := fieldsQuery("local", `"kafka"`, 200, store.SearchFilters{})
	assert.Equal(t, fmt.Sprintf(fieldsSQL, "bm25(document_fields_fts, 1, 1, 0.5, 1, 1)"), fields)
	assert.Equal(t, []any{`"kafka"`, "local", 200}, fieldsArgs)
	titles, titlesArgs := titlesQuery("local", `"kafka" AND "retention"`, 200, store.SearchFilters{})
	assert.Equal(t, fmt.Sprintf(fieldsSQL, "bm25(document_fields_fts, 1, 1, 0, 0, 0)"), titles)
	assert.Equal(t, []any{`{title bookmark_titles} : ("kafka" AND "retention")`, "local", 200}, titlesArgs)
}

// TestSearchFilters_DateBoundsAsStored: a bound is compared as the text
// the store keeps, in UTC to the millisecond, whatever zone it came in.
func TestSearchFilters_DateBoundsAsStored(t *testing.T) {
	zone := time.FixedZone("UTC+2", 2*60*60)
	_, args := buildFilterClause(store.SearchFilters{SavedAfter: time.Date(2024, 1, 10, 2, 0, 0, 0, zone),
		SavedBefore: time.Date(2024, 1, 20, 0, 0, 0, 123_456_789, time.UTC)})
	assert.Equal(t, []any{store.SavedDateUnknown, "2024-01-10T00:00:00.000Z", "2024-01-20T00:00:00.123Z"}, args)
}

// TestCountUndated: the documents, of any state, that the date dimensions
// can't judge among those the other dimensions pass: kept with
// IncludeUndated, left out without it, the same count either way.
func TestCountUndated(t *testing.T) {
	l := newFilterLib(t, newTestDBWidth(t, filterWidth), "local")
	exact, approx, unknown := store.SavedDateExact, store.SavedDateApprox, store.SavedDateUnknown
	published := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	early := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	l.add("in-both", "https://a.example/1", published, 0.1, saved{at: midWindow, class: exact})
	l.add("out-of-both", "https://a.example/2", published.AddDate(1, 0, 0), 0.2,
		saved{at: windowEnd.AddDate(1, 0, 0), class: exact})
	l.add("undated-both", "https://a.example/3", time.Time{}, 0.3, saved{at: midWindow, class: unknown})
	l.add("untrusted", "https://a.example/4", published, 0.4, saved{at: early, class: exact},
		saved{at: midWindow, class: unknown})
	failed := l.add("bare-failed", "https://b.example/5", time.Time{}, 0.5)
	require.NoError(t, l.docs.MarkFailed(l.ctx, failed, store.FailureCauseAntiBot))
	l.add("approx-unpublished", "https://b.example/6", time.Time{}, 0.6, saved{at: midWindow, class: approx})
	other := newFilterLib(t, l.db, "other")
	other.add("their-undated", "https://a.example/1", time.Time{}, 0.1)

	savedWindow := store.SearchFilters{SavedAfter: windowStart, SavedBefore: windowEnd}
	publishedWindow := store.SearchFilters{PublishedAfter: windowStart, PublishedBefore: windowEnd}
	both := savedWindow
	both.PublishedAfter, both.PublishedBefore = windowStart, windowEnd
	onA := func(f store.SearchFilters) store.SearchFilters { f.Site = []string{"a.example"}; return f }
	for _, tc := range []struct {
		name string
		f    store.SearchFilters
		want int
	}{
		{"saved", savedWindow, 2},                // undated-both, bare-failed
		{"saved on a site", onA(savedWindow), 1}, // undated-both
		{"published", publishedWindow, 4},        // undated-both, untrusted, bare-failed, approx-unpublished
		{"published on a site", onA(publishedWindow), 2},
		{"both", both, 3}, // undated-both, bare-failed, approx-unpublished
		{"both on a site", onA(both), 1},
		{"no date", store.SearchFilters{Site: []string{"a.example"}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, include := range []bool{false, true} {
				tc.f.IncludeUndated = include && tc.f.HasDates()
				n, err := l.index.CountUndated(l.ctx, "local", tc.f)
				require.NoError(t, err)
				assert.Equal(t, tc.want, n, "include_undated %v", include)
			}
		})
	}
}
