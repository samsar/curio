package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/store"
)

// fieldsEnv is a test database and its stores, for writing documents and
// bookmarks and reading the fields and cards derived from them.
type fieldsEnv struct {
	t    *testing.T
	ctx  context.Context
	db   *DB
	docs *Documents
	exts *Extractions
	bms  *Bookmarks
	// saved is when the next bookmark is saved: each a minute after the
	// one before, so their order is the order they were saved in.
	saved time.Time
}

func newFieldsEnv(t *testing.T) *fieldsEnv {
	t.Helper()
	db := newTestDB(t)
	return &fieldsEnv{t: t, ctx: context.Background(), db: db, docs: NewDocuments(db), exts: NewExtractions(db),
		bms: NewBookmarks(db), saved: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
}

// bookmark is url saved from source as title in folder, the next one
// saved.
func (e *fieldsEnv) bookmark(url, source, title, folder string) *store.Bookmark {
	e.saved = e.saved.Add(time.Minute)
	return &store.Bookmark{TenantID: "local", URL: url, Source: source, SavedAt: e.saved,
		Title: store.NullableString(title), FolderPath: store.NullableString(folder)}
}

// ingest saves url from source as title in folder, and returns the
// bookmark.
func (e *fieldsEnv) ingest(url, source, title, folder string) *store.Bookmark {
	e.t.Helper()
	b := e.bookmark(url, source, title, folder)
	_, err := e.bms.Ingest(e.ctx, b)
	require.NoError(e.t, err)
	return b
}

// fetch records a fetch of document id that read title and author from a
// page declaring site.
func (e *fieldsEnv) fetch(id, title, author, site string) {
	e.t.Helper()
	ext := &store.DocumentExtraction{DocumentID: id, Fetcher: "test", Status: store.ExtractionStatusOK}
	if site != "" {
		meta, err := json.Marshal(map[string]any{"via": "test", store.ExtractionMetaSiteName: site})
		require.NoError(e.t, err)
		ext.ExtractionMeta = meta
	}
	require.NoError(e.t, e.exts.Create(e.ctx, ext))
	require.NoError(e.t, e.docs.ApplyFetch(e.ctx, id, store.FetchedMetadata{ExtractionID: ext.ID,
		ContentType: store.ContentTypeArticle, Title: store.NullableString(title), Author: store.NullableString(author)}))
}

// fieldsRow is a document's fields as stored.
type fieldsRow struct {
	Host, DeclaredSite, TitleSuffixes, SiteName, Title, BookmarkTitles, Folders, Site, Author string
	Rules                                                                                     int
}

func (e *fieldsEnv) fields(id string) fieldsRow {
	e.t.Helper()
	var r fieldsRow
	require.NoError(e.t, e.db.QueryRow(`SELECT host, declared_site, title_suffixes, site_name, title, bookmark_titles,
		folders, site, author, rules FROM document_fields WHERE document_id = ?`, id).Scan(&r.Host, &r.DeclaredSite,
		&r.TitleSuffixes, &r.SiteName, &r.Title, &r.BookmarkTitles, &r.Folders, &r.Site, &r.Author, &r.Rules))
	return r
}

func (e *fieldsEnv) card(id string) string {
	e.t.Helper()
	var text string
	require.NoError(e.t, e.db.QueryRow(`SELECT text FROM document_cards WHERE document_id = ?`, id).Scan(&text))
	return text
}

// pendingCardsJobs counts the cards jobs waiting to run.
func (e *fieldsEnv) pendingCardsJobs() int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = ? AND status = ?`,
		store.JobKindCards, store.JobStatusPending).Scan(&n))
	return n
}

// derived is every derived row, but each fields row's seq, the order rows
// were first written in: the fields and the card of each document, by
// document.
func (e *fieldsEnv) derived() [][]any {
	e.t.Helper()
	return dumpRows(e.t, e.db, `SELECT f.document_id, f.tenant_id, f.host, f.declared_site, f.title_suffixes,
		f.site_name, f.title, f.bookmark_titles, f.folders, f.site, f.author, f.rules, c.text
		FROM document_fields f LEFT JOIN document_cards c ON c.document_id = f.document_id
		ORDER BY f.document_id`)
}

// rederived is what EnsureDocumentFields derives with every derived row
// wiped: a full derivation of the same data, which every sequence of
// writes must have left.
func (e *fieldsEnv) rederived() [][]any {
	e.t.Helper()
	_, err := e.db.Exec(`DELETE FROM document_fields; DELETE FROM document_cards`)
	require.NoError(e.t, err)
	_, err = EnsureDocumentFields(e.ctx, e.db)
	require.NoError(e.t, err)
	return e.derived()
}

func (e *fieldsEnv) integrityCheck() {
	e.t.Helper()
	_, err := e.db.Exec(`INSERT INTO document_fields_fts (document_fields_fts) VALUES ('integrity-check')`)
	require.NoError(e.t, err)
}

// TestRefreshFields_NewDocument: a document saved from a bookmark is
// searchable by the bookmark's title, its folder without the browser's
// root, and its host's words, and by the name its host agrees on.
func TestRefreshFields_NewDocument(t *testing.T) {
	e := newFieldsEnv(t)
	first := e.ingest("https://www.example.com/a", store.SourceChrome, "Queues | Example Times", "/Bookmarks Bar/Tech/Queues")
	id := *first.DocumentID
	assert.Equal(t, fieldsRow{Host: "example.com", TitleSuffixes: "Example Times",
		BookmarkTitles: "Queues | Example Times", Folders: "Tech / Queues", Site: "example",
		Rules: docmeta.RulesVersion}, e.fields(id))
	assert.Equal(t, "Queues | Example Times — example.com — Tech / Queues", e.card(id))

	second := e.ingest("https://example.com/b", store.SourceChrome, "Logs - Example Times", "")
	assert.Equal(t, "Example Times", e.fields(*second.DocumentID).SiteName, "two documents agree on a suffix")
	assert.Equal(t, "Example Times", e.fields(id).SiteName, "and the first takes the name too")
	assert.Equal(t, "Example Times example", e.fields(id).Site)
	assert.Equal(t, "Queues | Example Times — Example Times — Tech / Queues", e.card(id))

	e.ingest("https://example.com/c", store.SourceSafari, "", "")
	third, err := e.docs.GetByURL(e.ctx, "local", "https://example.com/c")
	require.NoError(t, err)
	assert.Equal(t, "Example Times", e.fields(third.ID).SiteName, "a new document of the host takes its name")
	e.integrityCheck()
}

// TestRefreshFields_Bookmarks: a second source's bookmark adds its title
// and folder to the document's fields; deleting it removes them, and
// moving a bookmark to another document moves them.
func TestRefreshFields_Bookmarks(t *testing.T) {
	e := newFieldsEnv(t)
	chrome := e.ingest("https://example.org/a", store.SourceChrome, "Caching notes", "/Bookmarks Bar/Reading")
	id := *chrome.DocumentID
	safari := e.ingest("https://example.org/a", store.SourceSafari, "Notes on caches", "/Favorites/Later")
	assert.Equal(t, "Caching notes\nNotes on caches", e.fields(id).BookmarkTitles)
	assert.Equal(t, "Reading\nLater", e.fields(id).Folders)

	require.NoError(t, e.bms.Delete(e.ctx, safari.ID))
	assert.Equal(t, "Caching notes", e.fields(id).BookmarkTitles)
	assert.Equal(t, "Reading", e.fields(id).Folders)

	other := &store.Document{TenantID: "local", URL: "https://example.org/b"}
	require.NoError(t, e.docs.Create(e.ctx, other))
	require.NoError(t, e.bms.LinkDocument(e.ctx, chrome.ID, other.ID))
	assert.Empty(t, e.fields(id).BookmarkTitles, "the bookmark took its title with it")
	assert.Equal(t, "Caching notes", e.fields(other.ID).BookmarkTitles)
	assert.Equal(t, "Reading", e.fields(other.ID).Folders)

	manual := e.bookmark("https://example.org/a", store.SourceManual, "Added by hand", "")
	manual.DocumentID = &id
	require.NoError(t, e.bms.Create(e.ctx, manual))
	assert.Equal(t, "Added by hand", e.fields(id).BookmarkTitles)
	e.integrityCheck()
}

// TestRefreshFields_Fetch: a fetch's title, author and declared name are
// the document's fields; a new declared name the host agrees on renames
// its siblings without one of their own; a generic name or one that only
// spells the host says nothing.
func TestRefreshFields_Fetch(t *testing.T) {
	e := newFieldsEnv(t)
	a := *e.ingest("https://news.example.net/a", store.SourceChrome, "", "").DocumentID
	b := *e.ingest("https://news.example.net/b", store.SourceChrome, "", "").DocumentID
	assert.Empty(t, e.fields(b).SiteName)

	e.fetch(a, "Ranking at scale", "Ada", "The Example Net")
	got := e.fields(a)
	assert.Equal(t, "Ranking at scale", got.Title)
	assert.Equal(t, "Ada", got.Author)
	assert.Equal(t, "The Example Net", got.DeclaredSite)
	assert.Equal(t, "The Example Net", got.SiteName)
	assert.Equal(t, "The Example Net example news", got.Site)
	assert.Equal(t, "Ranking at scale — The Example Net — Ada", e.card(a))
	assert.Equal(t, "The Example Net", e.fields(b).SiteName, "the sibling takes the host's declared name")
	assert.Equal(t, "The Example Net", e.card(b), "the name in place of the host")

	e.fetch(a, "Ranking at scale", "Ada", "Medium")
	assert.Empty(t, e.fields(a).DeclaredSite, "a platform's name is no site's")
	assert.Empty(t, e.fields(b).SiteName, "nothing agreed any more")
	e.fetch(a, "Ranking at scale", "Ada", "Example.net")
	assert.Empty(t, e.fields(a).DeclaredSite, "a name that only spells the host")

	// A fetch that finds the page again as it was changes nothing derived.
	before := e.derived()
	e.fetch(a, "Ranking at scale", "Ada", "Example.net")
	assert.Equal(t, before, e.derived())
	e.integrityCheck()
}

// TestRefreshFields_Agreement: suffixes agree only across two documents,
// the curated name comes last, and ties go to byte order however the
// documents were written.
func TestRefreshFields_Agreement(t *testing.T) {
	e := newFieldsEnv(t)
	lone := *e.ingest("https://paulgraham.com/one.html", store.SourceChrome, "One - Essays", "").DocumentID
	assert.Equal(t, "Paul Graham", e.fields(lone).SiteName, "one document's suffix agrees on nothing; the curated name")
	e.ingest("https://paulgraham.com/two.html", store.SourceChrome, "Two - Essays", "")
	assert.Equal(t, "Essays", e.fields(lone).SiteName, "two agree, before the curated name")

	for _, order := range [][]string{{"Beta", "Alpha"}, {"Alpha", "Beta"}} {
		e := newFieldsEnv(t)
		var ids []string
		for i, name := range append(order, order...) {
			b := e.ingest(fmt.Sprintf("https://tie.example/%d", i), store.SourceChrome, "Post | "+name, "")
			ids = append(ids, *b.DocumentID)
		}
		for _, id := range ids {
			assert.Equal(t, "Alpha", e.fields(id).SiteName, "order %v", order)
		}
	}
}

// TestRefreshFields_QueuesCards: a write that changes a card queues one
// cards job, at most one pending per tenant, in its own transaction, so
// one that rolls back leaves none; one that changes no card queues
// nothing.
func TestRefreshFields_QueuesCards(t *testing.T) {
	e := newFieldsEnv(t)
	q := NewJobs(e.db)
	woken := q.Enqueued([]store.JobKind{store.JobKindCards})
	b := e.ingest("https://example.com/a", store.SourceChrome, "A title", "")
	assert.Equal(t, 1, e.pendingCardsJobs())
	select {
	case <-woken:
	default:
		t.Fatal("the commit woke no cards worker")
	}
	var payload string
	require.NoError(t, e.db.QueryRow(`SELECT payload FROM jobs WHERE kind = ?`, store.JobKindCards).Scan(&payload))
	assert.JSONEq(t, `{}`, payload)

	e.ingest("https://example.com/b", store.SourceChrome, "Another", "")
	assert.Equal(t, 1, e.pendingCardsJobs(), "one pending is enough")

	claimed, err := q.ClaimNext(e.ctx, []store.JobKind{store.JobKindCards})
	require.NoError(t, err)
	e.ingest("https://example.com/c", store.SourceChrome, "A third", "")
	assert.Equal(t, 1, e.pendingCardsJobs(), "a running job doesn't count: the card changed behind it")
	require.NoError(t, q.MarkDone(e.ctx, claimed.ID))

	other := e.ingest("https://example.com/a", store.SourceSafari, "A title", "")
	assert.Equal(t, *b.DocumentID, *other.DocumentID)
	assert.Equal(t, 1, e.pendingCardsJobs(), "an unchanged card queues nothing")

	_, err = q.ClaimNext(e.ctx, []store.JobKind{store.JobKindCards})
	require.NoError(t, err)
	_, err = e.db.Exec(`CREATE TRIGGER t_fail BEFORE INSERT ON jobs WHEN new.kind = 'cards'
		BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	require.NoError(t, err)
	_, err = e.bms.Ingest(e.ctx, e.bookmark("https://example.com/d", store.SourceChrome, "Rolled back", ""))
	require.ErrorContains(t, err, "injected")
	_, err = e.docs.GetByURL(e.ctx, "local", "https://example.com/d")
	assert.ErrorIs(t, err, store.ErrNotFound, "the document rolled back with the job")
	assert.Equal(t, 3, countFetchJobs(t, e.db), "and its fetch job")
	assert.Equal(t, 3, countRows(t, e.db, "document_fields"))
	assert.Equal(t, 3, countRows(t, e.db, "document_cards"))
	assert.Zero(t, e.pendingCardsJobs())
}

// TestRefreshFields_NoChangeWritesNothing: a write that changes nothing a
// document's fields are derived from writes no derived row, no index
// entry and no job: a refused ingest, and a second source's bookmark
// saved under the same title and folder.
func TestRefreshFields_NoChangeWritesNothing(t *testing.T) {
	e := newFieldsEnv(t)
	// total_changes() counts the rows one connection's statements and
	// their triggers wrote, so every statement takes the one connection.
	e.db.SetMaxOpenConns(1)
	totalChanges := func() int {
		t.Helper()
		var n int
		require.NoError(t, e.db.QueryRow(`SELECT total_changes()`).Scan(&n))
		return n
	}
	e.ingest("https://example.com/a", store.SourceChrome, "Same title", "/Bookmarks Bar/Same")
	_, err := NewJobs(e.db).ClaimNext(e.ctx, []store.JobKind{store.JobKindCards})
	require.NoError(t, err, "the cards job runs: a change would queue another")
	derived := e.derived()

	before := totalChanges()
	_, err = e.bms.Ingest(e.ctx, e.bookmark("https://example.com/a", store.SourceChrome, "Other title", ""))
	require.ErrorIs(t, err, store.ErrConflict)
	assert.Equal(t, before, totalChanges(), "a refused ingest writes nothing")

	e.ingest("https://example.com/a", store.SourceFirefox, "Same title", "/Bookmarks Toolbar/Same")
	assert.Equal(t, before+1, totalChanges(), "the bookmark's row alone")
	assert.Equal(t, derived, e.derived())
	assert.Zero(t, e.pendingCardsJobs())
}

// TestRefreshFields_DeletedDocument: a document's derived rows go with it:
// its fields, their index entry, its card and its card's vector.
func TestRefreshFields_DeletedDocument(t *testing.T) {
	e := newFieldsEnv(t)
	id := *e.ingest("https://example.com/a", store.SourceChrome, "Gone soon", "/Bookmarks Bar/Tmp").DocumentID
	written, err := NewDocumentIndex(e.db, vecDim).PutCardVectors(e.ctx,
		[]store.CardVector{{DocumentID: id, Text: e.card(id), Embedding: fillVec(0.1)}})
	require.NoError(t, err)
	require.Equal(t, 1, written)

	_, err = e.db.Exec(`DELETE FROM documents WHERE id = ?`, id)
	require.NoError(t, err)
	for _, table := range []string{"document_fields", "document_cards", "document_cards_vec"} {
		assert.Zero(t, countRows(t, e.db, table), table)
	}
	e.integrityCheck()
	hits, err := NewDocumentIndex(e.db, vecDim).SearchFields(e.ctx, "local", `"gone"`, 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.Empty(t, hits)
}

// TestRefreshFields_AnyOrderEndsAsAFullDerivation: whatever writes a
// library sees, in whatever order, its derived rows end as a derivation
// of the final data from scratch would leave them.
func TestRefreshFields_AnyOrderEndsAsAFullDerivation(t *testing.T) {
	hosts := []string{"https://www.alpha.example", "https://blog.beta.example", "https://gamma.example",
		"https://paulgraham.com"}
	sources := []string{store.SourceChrome, store.SourceSafari, store.SourceFirefox, store.SourceManual}
	titles := []string{"", "Post", "Post | Alpha Weekly", "Note - Beta Blog", "Note - Beta Blog", "Essay · Gamma",
		"Essay · Gamma", "Untitled - 2026"}
	folders := []string{"", "/Bookmarks Bar/Tech", "/Favorites/Reading/Later", "/Other Bookmarks", "/Tech/Deep"}
	sites := []string{"", "", "Alpha Weekly", "Medium", "gamma.example", "Beta Blog", "Gamma Times"}
	for seed := range uint64(6) {
		t.Run(fmt.Sprint("seed ", seed), func(t *testing.T) {
			e := newFieldsEnv(t)
			r := rand.New(rand.NewPCG(seed, 42))
			pick := func(list []string) string { return list[r.IntN(len(list))] }
			url := func() string { return fmt.Sprintf("%s/%d", pick(hosts), r.IntN(12)) }
			var bookmarks []string
			for range 120 {
				switch r.IntN(4) {
				case 0:
					err := e.docs.Create(e.ctx, &store.Document{TenantID: "local", URL: url()})
					if !errors.Is(err, store.ErrConflict) {
						require.NoError(t, err)
					}
				case 1:
					b := e.bookmark(url(), pick(sources), pick(titles), pick(folders))
					if _, err := e.bms.Ingest(e.ctx, b); !errors.Is(err, store.ErrConflict) {
						require.NoError(t, err)
						bookmarks = append(bookmarks, b.ID)
					}
				case 2:
					doc, err := e.docs.GetByURL(e.ctx, "local", url())
					if errors.Is(err, store.ErrNotFound) {
						continue
					}
					require.NoError(t, err)
					e.fetch(doc.ID, pick(titles), pick([]string{"", "Ada", "Grace"}), pick(sites))
				case 3:
					if len(bookmarks) == 0 {
						continue
					}
					i := r.IntN(len(bookmarks))
					require.NoError(t, e.bms.Delete(e.ctx, bookmarks[i]))
					bookmarks = append(bookmarks[:i], bookmarks[i+1:]...)
				}
			}
			var docs int
			require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM documents`).Scan(&docs))
			require.GreaterOrEqual(t, docs, 30, "a library of at least 30 documents")
			e.integrityCheck()
			assert.Equal(t, e.derived(), e.rederived())
		})
	}
}

// TestEnsureDocumentFields: it derives the documents without fields, and
// those derived by other rules, once; a second run writes nothing; and a
// run cut short leaves what it committed for the next to finish.
func TestEnsureDocumentFields(t *testing.T) {
	e := newFieldsEnv(t)
	for i := range 6 {
		source := store.SourceChrome
		if i >= 3 {
			source = store.SourceFirefox
		}
		e.ingest(fmt.Sprintf("https://host%d.example/a", i%3), source, fmt.Sprint("Title ", i), "/Reading")
		e.ingest(fmt.Sprintf("https://host%d.example/b%d", i%3, i), store.SourceSafari, "Post - Host Name", "")
	}
	want := e.derived()
	_, err := e.db.Exec(`DELETE FROM document_fields; DELETE FROM document_cards; DELETE FROM jobs WHERE kind = 'cards'`)
	require.NoError(t, err)

	done, err := EnsureDocumentFields(e.ctx, e.db)
	require.NoError(t, err)
	assert.Equal(t, 9, done.Documents)
	assert.Equal(t, 3, done.Hosts)
	assert.Equal(t, 9, done.CardsStale)
	assert.Equal(t, want, e.derived())
	assert.Equal(t, 1, e.pendingCardsJobs())

	again, err := EnsureDocumentFields(e.ctx, e.db)
	require.NoError(t, err)
	assert.Zero(t, again.Documents, "nothing left to derive")
	assert.Equal(t, want, e.derived())
	assert.Equal(t, 1, e.pendingCardsJobs(), "still the one job")

	_, err = e.db.Exec(`UPDATE document_fields SET rules = ?`, docmeta.RulesVersion-1)
	require.NoError(t, err)
	bumped, err := EnsureDocumentFields(e.ctx, e.db)
	require.NoError(t, err)
	assert.Equal(t, 9, bumped.Documents, "rows of other rules are derived again")
	assert.Equal(t, want, e.derived())

	// A run cut short: the first batch committed, the rest left.
	_, err = e.db.Exec(`DELETE FROM document_fields; DELETE FROM document_cards`)
	require.NoError(t, err)
	batches, _, _, err := unrefreshedBatches(e.ctx, e.db)
	require.NoError(t, err)
	require.NoError(t, refreshBatch(e.ctx, e.db, fieldsBatch{tenantID: "local", ids: batches[0].ids[:4]}))
	cancelled, cancel := context.WithCancel(e.ctx)
	cancel()
	_, err = EnsureDocumentFields(cancelled, e.db)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 4, countRows(t, e.db, "document_fields"), "what was committed stays")
	resumed, err := EnsureDocumentFields(e.ctx, e.db)
	require.NoError(t, err)
	assert.Equal(t, 5, resumed.Documents)
	assert.Equal(t, want, e.derived())
	e.integrityCheck()
}

// TestEnsureDocumentFields_Batches: whole hosts share a transaction up to
// maxFieldsBatch documents, a larger host is split, and a tenant's
// documents never share one with another's.
func TestEnsureDocumentFields_Batches(t *testing.T) {
	e := newFieldsEnv(t)
	insert := func(tenant, host string, n int) {
		for i := range n {
			_, err := e.db.Exec(`INSERT INTO documents (id, tenant_id, url) VALUES (?, ?, ?)`,
				fmt.Sprintf("%s-%s-%04d", tenant, host, i), tenant, fmt.Sprintf("https://%s/%d", host, i))
			require.NoError(t, err)
		}
	}
	insert("a", "big.example", maxFieldsBatch+10)
	insert("a", "one.example", 3)
	insert("a", "two.example", 4)
	insert("b", "one.example", 2)

	batches, docs, hosts, err := unrefreshedBatches(e.ctx, e.db)
	require.NoError(t, err)
	assert.Equal(t, maxFieldsBatch+19, docs)
	assert.Equal(t, 4, hosts)
	sizes := make([]string, 0, len(batches))
	for _, b := range batches {
		sizes = append(sizes, fmt.Sprintf("%s:%d", b.tenantID, len(b.ids)))
	}
	assert.Equal(t, []string{"a:1000", "a:17", "b:2"}, sizes,
		"big.example fills one batch, its last 10 sharing the next with the small hosts")
}
