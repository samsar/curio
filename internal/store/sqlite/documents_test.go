package sqlite

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// listDocumentURLs lists the tenant's documents under opts and returns
// their URLs, in list order.
func listDocumentURLs(t *testing.T, docs *Documents, tenantID string, opts store.ListDocumentsOpts) []string {
	t.Helper()
	opts.Limit = 1000
	got, err := docs.ListWithLastError(context.Background(), tenantID, opts)
	require.NoError(t, err)
	urls := make([]string, 0, len(got))
	for _, d := range got {
		urls = append(urls, d.URL)
	}
	return urls
}

// TestDocuments_ListHostFilter: the documents list matches a host as the
// search host filter does, since it is the same predicate.
func TestDocuments_ListHostFilter(t *testing.T) {
	db := newTestDB(t)
	docs := NewDocuments(db)
	for _, u := range []string{
		"https://myxsite.com/a", "https://my_site.com/b",
		"https://example.com/c", "http://example.com/d", "https://example.com.evil/e",
	} {
		seedDoc(t, docs, u, store.DocStateFetched)
	}

	cases := []struct {
		host string
		want []string
	}{
		{"my_site.com", []string{"https://my_site.com/b"}},
		{"%", nil},
		{"_yxsite.com", nil},
		{"EXAMPLE.com", []string{"https://example.com/c", "http://example.com/d"}},
		{"example.com.evil", []string{"https://example.com.evil/e"}},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			assert.ElementsMatch(t, tc.want, listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{Host: tc.host}))
		})
	}
}

// TestDocuments_ListFolderFilter: the documents list matches a folder as
// the bookmark folder filter does, through the document's bookmarks: a
// document is listed once however many of its bookmarks match, and one
// without bookmarks never matches.
func TestDocuments_ListFolderFilter(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	bms := NewBookmarks(db)
	folders := []string{
		"/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents",
		"/Tech/AIRPLANES", "/Tech/AI_x", "/Tech/AI0", "/tech/ai/lower",
		"/100% Reading", "/100X Reading",
	}
	byFolder := map[string]string{}
	for i, f := range folders {
		folder := f
		url := fmt.Sprintf("https://example.com/%d", i)
		_, err := bms.Ingest(ctx, &store.Bookmark{TenantID: "local", URL: url, Source: store.SourceChrome,
			SavedAt: time.Now().UTC(), FolderPath: &folder})
		require.NoError(t, err)
		byFolder[f] = url
	}
	// A second bookmark of the /Tech/AI/Agents page, from another source
	// and in another matching folder.
	agents := "/Tech/AI"
	_, err := bms.Ingest(ctx, &store.Bookmark{TenantID: "local", URL: byFolder["/Tech/AI/Agents"],
		Source: store.SourceSafari, SavedAt: time.Now().UTC(), FolderPath: &agents})
	require.NoError(t, err)
	seedDoc(t, docs, "https://example.com/no-bookmark", store.DocStateFetched)

	urls := func(folders ...string) []string {
		out := make([]string, 0, len(folders))
		for _, f := range folders {
			out = append(out, byFolder[f])
		}
		return out
	}
	cases := []struct {
		filter string
		want   []string
	}{
		{"/Tech/AI", urls("/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents")},
		{"/Tech/AI/", urls("/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents")},
		{"/Tech/AI/Agents", urls("/Tech/AI/Agents")},
		{"/Tech", urls("/Tech/AI", "/Tech/AI/", "/Tech/AI/Agents", "/Tech/AIRPLANES", "/Tech/AI_x", "/Tech/AI0")},
		{"/tech/ai", urls("/tech/ai/lower")},
		{"/100% Reading", urls("/100% Reading")},
		{"/Tech/AI_", nil},
		{"/", append(urls(folders...), "https://example.com/no-bookmark")},
	}
	for _, tc := range cases {
		t.Run(tc.filter, func(t *testing.T) {
			assert.ElementsMatch(t, tc.want, listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{Folder: tc.filter}))
		})
	}
}

// TestDocuments_ListFiltersCombine: every filter given must match, and a
// bookmark of another tenant's never matches.
func TestDocuments_ListFiltersCombine(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	bms := NewBookmarks(db)
	add := func(tenant, url string, ct store.ContentType, state store.DocState, folder string) {
		t.Helper()
		_, err := bms.Ingest(ctx, &store.Bookmark{TenantID: tenant, URL: url, Source: store.SourceChrome,
			SavedAt: time.Now().UTC(), FolderPath: store.NullableString(folder)})
		require.NoError(t, err)
		_, err = db.Exec(`UPDATE documents SET content_type = ?, state = ? WHERE tenant_id = ? AND url = ?`,
			ct, state, tenant, url)
		require.NoError(t, err)
	}
	add("local", "https://a.example/pdf", store.ContentTypePDF, store.DocStateFetched, "/Papers")
	add("local", "https://a.example/article", store.ContentTypeArticle, store.DocStateFetched, "/Papers")
	add("local", "https://b.example/pdf", store.ContentTypePDF, store.DocStateFetched, "/Papers")
	add("local", "https://a.example/failed", store.ContentTypePDF, store.DocStateFailed, "/Papers")
	add("local", "https://a.example/elsewhere", store.ContentTypePDF, store.DocStateFetched, "/Other")
	add("other", "https://a.example/theirs", store.ContentTypePDF, store.DocStateFetched, "/Papers")
	// Another tenant's bookmark of a local document, in the folder asked
	// for.
	var elsewhere string
	require.NoError(t, db.QueryRow(`SELECT id FROM documents WHERE tenant_id = 'local' AND url = ?`,
		"https://a.example/elsewhere").Scan(&elsewhere))
	papers := "/Papers"
	require.NoError(t, bms.Create(ctx, &store.Bookmark{TenantID: "other", DocumentID: &elsewhere,
		URL: "https://a.example/elsewhere", Source: store.SourceSafari, SavedAt: time.Now().UTC(), FolderPath: &papers}))

	assert.ElementsMatch(t,
		[]string{"https://a.example/pdf", "https://a.example/article", "https://b.example/pdf", "https://a.example/failed"},
		listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{Folder: "/Papers"}))
	assert.ElementsMatch(t,
		[]string{"https://a.example/pdf", "https://b.example/pdf", "https://a.example/failed", "https://a.example/elsewhere"},
		listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{ContentType: store.ContentTypePDF}))
	assert.Equal(t, []string{"https://a.example/pdf"}, listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{
		State: store.DocStateFetched, ContentType: store.ContentTypePDF, Host: "a.example", Folder: "/Papers"}))
	assert.Empty(t, listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{ContentType: store.ContentTypeVideo}))
}

// TestDocuments_ListFilteredPages: a walk one row a page over a filtered
// list visits every matching document once, most recently updated first.
func TestDocuments_ListFilteredPages(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var want []string
	for i := range 7 {
		host := "a.example"
		if i%3 == 0 {
			host = "b.example"
		}
		d := seedDoc(t, docs, fmt.Sprintf("https://%s/%d", host, i), store.DocStateFetched)
		// Two documents share each timestamp, so the ID breaks the tie.
		_, err := db.Exec(`UPDATE documents SET updated_at = ? WHERE id = ?`,
			formatTime(base.Add(time.Duration(i/2)*time.Hour)), d.ID)
		require.NoError(t, err)
		if host == "a.example" {
			want = append(want, d.URL)
		}
	}

	var got []string
	var after store.PageKey
	for range 10 {
		page, err := docs.ListWithLastError(ctx, "local", store.ListDocumentsOpts{Host: "a.example", Limit: 1, After: after})
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		require.Len(t, page, 1)
		got = append(got, page[0].URL)
		after = store.PageKey{At: page[0].UpdatedAt, ID: page[0].ID}
	}
	assert.ElementsMatch(t, want, got, "every matching row once")
	all, err := docs.ListWithLastError(ctx, "local", store.ListDocumentsOpts{Host: "a.example", Limit: 100})
	require.NoError(t, err)
	inOrder := make([]string, 0, len(all))
	for _, d := range all {
		inOrder = append(inOrder, d.URL)
	}
	assert.Equal(t, inOrder, got, "in the list's order: updated_at, then ID, descending")
}

// TestDocuments_GetWithLastError: one document as the list returns it: the
// error of its most recent failed job, and its markdown path.
func TestDocuments_GetWithLastError(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	exts := NewExtractions(db)
	q := NewJobs(db)

	doc := seedDoc(t, docs, "https://example.com/a", store.DocStateFailed)
	rel := doc.ID + "/e.md"
	ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
		MarkdownPath: &rel}
	require.NoError(t, exts.Create(ctx, ext))
	require.NoError(t, docs.SetCurrentExtraction(ctx, doc.ID, ext.ID))
	for i, msg := range []string{"older failure", "newest failure", "oldest failure"} {
		job, err := store.NewDocumentJob("local", store.JobKindFetch, doc.ID)
		require.NoError(t, err)
		job.Status = store.JobStatusFailed
		require.NoError(t, q.Enqueue(ctx, job))
		at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add([]time.Duration{time.Hour, 2 * time.Hour, 0}[i])
		_, err = db.Exec(`UPDATE jobs SET last_error = ?, updated_at = ? WHERE id = ?`, msg, formatTime(at), job.ID)
		require.NoError(t, err)
	}
	// A later job that didn't fail doesn't count.
	done, err := store.NewDocumentJob("local", store.JobKindFetch, doc.ID)
	require.NoError(t, err)
	done.Status = store.JobStatusDone
	require.NoError(t, q.Enqueue(ctx, done))

	got, err := docs.GetWithLastError(ctx, "local", doc.ID)
	require.NoError(t, err)
	assert.Equal(t, doc.URL, got.URL)
	assert.Equal(t, store.DocStateFailed, got.State)
	assert.Equal(t, "newest failure", got.LastError)
	assert.Equal(t, rel, got.MarkdownPath)

	listed, err := docs.ListWithLastError(ctx, "local", store.ListDocumentsOpts{})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, listed[0], *got, "as the list has it")

	clean := seedDoc(t, docs, "https://example.com/clean", store.DocStateFetched)
	got, err = docs.GetWithLastError(ctx, "local", clean.ID)
	require.NoError(t, err)
	assert.Empty(t, got.LastError)
	assert.Empty(t, got.MarkdownPath)

	theirs := &store.Document{TenantID: "other", URL: "https://example.com/theirs"}
	require.NoError(t, docs.Create(ctx, theirs))
	for _, id := range []string{theirs.ID, "no-such-document"} {
		_, err = docs.GetWithLastError(ctx, "local", id)
		assert.ErrorIs(t, err, store.ErrNotFound, id)
	}
}

// TestDocuments_BookmarkTitle: an untitled document is named by its most
// recently saved bookmark whose title isn't blank, the tenant's own; a
// titled document, and one with no such bookmark, get none. The list and
// the single read agree.
func TestDocuments_BookmarkTitle(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	untitled := seedDoc(t, docs, "https://example.com/untitled", store.DocStateFailed)
	emptyTitle := seedDoc(t, docs, "https://example.com/empty-title", store.DocStateFetched)
	_, err := db.Exec(`UPDATE documents SET title = '' WHERE id = ?`, emptyTitle.ID)
	require.NoError(t, err)
	titled := seedDoc(t, docs, "https://example.com/titled", store.DocStateFetched)
	_, err = db.Exec(`UPDATE documents SET title = 'Its own' WHERE id = ?`, titled.ID)
	require.NoError(t, err)
	unnamed := seedDoc(t, docs, "https://example.com/unnamed", store.DocStatePending)
	bare := seedDoc(t, docs, "https://example.com/bare", store.DocStatePending)

	for i, b := range []struct {
		doc          *store.Document
		tenant       string
		title        *string
		saved        string
		source, name string
	}{
		{untitled, "local", new("Older title"), "2026-01-01T00:00:00.000Z", store.SourceChrome, "older"},
		{untitled, "local", new("Newest titled"), "2026-02-01T00:00:00.000Z", store.SourceSafari, "newest titled"},
		{untitled, "local", nil, "2026-03-01T00:00:00.000Z", store.SourceFirefox, "newer, no title"},
		{untitled, "local", new("  "), "2026-04-01T00:00:00.000Z", store.SourceManual, "newer, blank"},
		{untitled, "local", new("\t\r\n "), "2026-04-15T00:00:00.000Z", store.SourceHTML, "newer, whitespace"},
		{untitled, "other", new("Theirs"), "2026-05-01T00:00:00.000Z", store.SourceChrome, "another tenant's"},
		{emptyTitle, "local", new("Named by its bookmark"), "2026-01-01T00:00:00.000Z", store.SourceChrome, "empty"},
		{titled, "local", new("A bookmark title"), "2026-01-01T00:00:00.000Z", store.SourceChrome, "titled"},
		{unnamed, "local", nil, "2026-01-01T00:00:00.000Z", store.SourceChrome, "no title"},
		{unnamed, "local", new(""), "2026-02-01T00:00:00.000Z", store.SourceSafari, "empty title"},
	} {
		_, err := db.Exec(`INSERT INTO bookmarks (id, tenant_id, document_id, url, title, saved_at, source)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, fmt.Sprintf("b%d", i), b.tenant, b.doc.ID, b.doc.URL, strPtr(b.title),
			b.saved, b.source)
		require.NoError(t, err, b.name)
	}

	want := map[string]string{untitled.ID: "Newest titled", emptyTitle.ID: "Named by its bookmark", titled.ID: "",
		unnamed.ID: "", bare.ID: ""}
	listed, err := docs.ListWithLastError(ctx, "local", store.ListDocumentsOpts{})
	require.NoError(t, err)
	require.Len(t, listed, len(want))
	for _, d := range listed {
		assert.Equal(t, want[d.ID], d.BookmarkTitle, d.URL)
		got, err := docs.GetWithLastError(ctx, "local", d.ID)
		require.NoError(t, err)
		assert.Equal(t, d.BookmarkTitle, got.BookmarkTitle, "%s: as the list has it", d.URL)
	}
}

// TestDocuments_GetByIDsWithLastError: the tenant's documents with the
// IDs given, each as GetWithLastError returns it, its last error, markdown
// path and bookmark title included; another tenant's and unknown IDs are
// left out, and no number of IDs is too many for one read.
func TestDocuments_GetByIDsWithLastError(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	exts := NewExtractions(db)
	q := NewJobs(db)

	failed := seedDoc(t, docs, "https://example.com/failed", store.DocStateFailed)
	rel := failed.ID + "/e.md"
	ext := &store.DocumentExtraction{DocumentID: failed.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
		MarkdownPath: &rel}
	require.NoError(t, exts.Create(ctx, ext))
	require.NoError(t, docs.SetCurrentExtraction(ctx, failed.ID, ext.ID))
	job, err := store.NewDocumentJob("local", store.JobKindFetch, failed.ID)
	require.NoError(t, err)
	job.Status = store.JobStatusFailed
	require.NoError(t, q.Enqueue(ctx, job))
	_, err = db.Exec(`UPDATE jobs SET last_error = 'HTTP 503' WHERE id = ?`, job.ID)
	require.NoError(t, err)

	untitled := seedDoc(t, docs, "https://example.com/untitled", store.DocStateFetched)
	_, err = db.Exec(`INSERT INTO bookmarks (id, tenant_id, document_id, url, title, saved_at, source)
		VALUES ('b1', 'local', ?, ?, 'Saved as this', '2026-01-01T00:00:00.000Z', 'chrome')`, untitled.ID, untitled.URL)
	require.NoError(t, err)
	titled := seedDoc(t, docs, "https://example.com/titled", store.DocStateFetched)
	_, err = db.Exec(`UPDATE documents SET title = 'Its own' WHERE id = ?`, titled.ID)
	require.NoError(t, err)
	theirs := &store.Document{TenantID: "other", URL: "https://example.com/theirs"}
	require.NoError(t, docs.Create(ctx, theirs))

	got, err := docs.GetByIDsWithLastError(ctx, "local",
		[]string{titled.ID, failed.ID, theirs.ID, "no-such-document", untitled.ID, failed.ID})
	require.NoError(t, err)
	byID := map[string]store.DocumentWithError{}
	for _, d := range got {
		byID[d.ID] = d
	}
	assert.Len(t, got, 3, "each once, the tenant's own")
	assert.ElementsMatch(t, []string{failed.ID, untitled.ID, titled.ID}, slices.Collect(maps.Keys(byID)))
	for id, d := range byID {
		one, err := docs.GetWithLastError(ctx, "local", id)
		require.NoError(t, err)
		assert.Equal(t, *one, d, "%s: as GetWithLastError has it", d.URL)
	}
	assert.Equal(t, "HTTP 503", byID[failed.ID].LastError)
	assert.Equal(t, rel, byID[failed.ID].MarkdownPath)
	assert.Equal(t, "Saved as this", byID[untitled.ID].BookmarkTitle)
	assert.Empty(t, byID[titled.ID].BookmarkTitle)

	none, err := docs.GetByIDsWithLastError(ctx, "local", nil)
	require.NoError(t, err)
	assert.Empty(t, none)

	// More IDs than SQLite binds parameters in one statement (32,766).
	const missing = 40_000
	many := make([]string, 0, missing+1)
	many = append(many, untitled.ID)
	for i := range missing {
		many = append(many, fmt.Sprintf("missing-%d", i))
	}
	got, err = docs.GetByIDsWithLastError(ctx, "local", many)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, untitled.ID, got[0].ID)
}

// TestDocuments_ListCauseFilter: the list narrows to the documents that
// failed for a cause, together with the other filters, and pages through
// them in its order, every one once.
func TestDocuments_ListCauseFilter(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	fail := func(url string, cause store.FailureCause, at time.Time) *store.Document {
		t.Helper()
		d := &store.Document{TenantID: "local", URL: url, FailureCause: cause}
		require.NoError(t, docs.Create(ctx, d))
		_, err := db.Exec(`UPDATE documents SET updated_at = ? WHERE id = ?`, formatTime(at), d.ID)
		require.NoError(t, err)
		return d
	}
	var blocked, blockedOnA []string
	for i := range 7 {
		host := "a.example"
		if i%2 == 0 {
			host = "b.example"
		}
		cause := store.FailureCauseAntiBot
		if i%3 == 0 {
			cause = store.FailureCauseTimeout
		}
		// Two documents share each timestamp, so the ID breaks the tie.
		d := fail(fmt.Sprintf("https://%s/%d", host, i), cause, base.Add(time.Duration(i/2)*time.Hour))
		if cause == store.FailureCauseAntiBot {
			blocked = append(blocked, d.URL)
			if host == "a.example" {
				blockedOnA = append(blockedOnA, d.URL)
			}
		}
	}
	gone := fail("https://a.example/gone", store.FailureCauseDeadLink, base)
	seedDoc(t, docs, "https://a.example/fetched", store.DocStateFetched)
	filed := newIngestBookmark("https://a.example/filed", store.SourceChrome)
	filed.FolderPath = store.NullableString("/Blocked")
	_, err := NewBookmarks(db).Ingest(ctx, filed)
	require.NoError(t, err)
	require.NoError(t, docs.MarkFailed(ctx, *filed.DocumentID, store.FailureCauseAntiBot))
	blocked = append(blocked, filed.URL)
	blockedOnA = append(blockedOnA, filed.URL)

	for name, tc := range map[string]struct {
		opts store.ListDocumentsOpts
		want []string
	}{
		"alone":              {store.ListDocumentsOpts{Cause: store.FailureCauseAntiBot}, blocked},
		"with its state":     {store.ListDocumentsOpts{Cause: store.FailureCauseAntiBot, State: store.DocStateFailed}, blocked},
		"with another":       {store.ListDocumentsOpts{Cause: store.FailureCauseAntiBot, State: store.DocStateFetched}, nil},
		"with a host":        {store.ListDocumentsOpts{Cause: store.FailureCauseAntiBot, Host: "a.example"}, blockedOnA},
		"with a folder":      {store.ListDocumentsOpts{Cause: store.FailureCauseAntiBot, Folder: "/Blocked"}, []string{filed.URL}},
		"dead links":         {store.ListDocumentsOpts{Cause: store.FailureCauseDeadLink}, []string{gone.URL}},
		"dead, if failed":    {store.ListDocumentsOpts{Cause: store.FailureCauseDeadLink, State: store.DocStateFailed}, nil},
		"a cause no one has": {store.ListDocumentsOpts{Cause: store.FailureCauseIndex}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			assert.ElementsMatch(t, tc.want, listDocumentURLs(t, docs, "local", tc.opts))
		})
	}

	var got []string
	var after store.PageKey
	for range 20 {
		page, err := docs.ListWithLastError(ctx, "local",
			store.ListDocumentsOpts{Cause: store.FailureCauseAntiBot, Limit: 1, After: after})
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		require.Len(t, page, 1)
		assert.Equal(t, store.FailureCauseAntiBot, page[0].FailureCause)
		got = append(got, page[0].URL)
		after = store.PageKey{At: page[0].UpdatedAt, ID: page[0].ID}
	}
	assert.Equal(t, listDocumentURLs(t, docs, "local", store.ListDocumentsOpts{Cause: store.FailureCauseAntiBot}), got,
		"every one once, in the list's order")
}

// assertCauseInvariant asserts that no document's failure cause disagrees
// with its state: a cause is set exactly when the document is failed or
// dead, and a dead document's is dead_link.
func assertCauseInvariant(t *testing.T, db *DB, after string) {
	t.Helper()
	var violations int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM documents
		WHERE (state IN ('failed', 'dead')) != (failure_cause IS NOT NULL)
		   OR (state = 'dead') != (failure_cause IS 'dead_link')`).Scan(&violations))
	assert.Zero(t, violations, "after %s", after)
}

// TestDocuments_FailureCauseFollowsState: every write that sets a
// document's state keeps its failure cause in step with it.
func TestDocuments_FailureCauseFollowsState(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs, exts := NewDocuments(db), NewExtractions(db)
	n := 0
	create := func(state store.DocState, cause store.FailureCause) *store.Document {
		t.Helper()
		n++
		d := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/%d", n), State: state,
			FailureCause: cause}
		require.NoError(t, docs.Create(ctx, d))
		return d
	}
	stored := func(d *store.Document) *store.Document {
		t.Helper()
		got, err := docs.GetByID(ctx, d.ID)
		require.NoError(t, err)
		return got
	}

	create("", "")
	create(store.DocStatePending, "")
	create(store.DocStateFetched, "")
	for _, cause := range store.FailureCauses() {
		assert.Equal(t, cause.State(), stored(create("", cause)).State, "the state follows from the cause")
		assert.Equal(t, cause, stored(create(cause.State(), cause)).FailureCause)
	}
	assertCauseInvariant(t, db, "Create")

	d := create("", "")
	for _, cause := range store.FailureCauses() {
		require.NoError(t, docs.MarkFailed(ctx, d.ID, cause))
		assertCauseInvariant(t, db, "MarkFailed "+string(cause))
	}
	require.NoError(t, docs.MarkFetched(ctx, d.ID))
	assertCauseInvariant(t, db, "MarkFetched")

	// A fetch job that succeeds for a failed document, say one a refetch
	// raced with.
	failed := create("", store.FailureCauseAntiBot)
	ext := &store.DocumentExtraction{DocumentID: failed.ID, Fetcher: "test", Status: store.ExtractionStatusOK}
	require.NoError(t, exts.Create(ctx, ext))
	require.NoError(t, docs.ApplyFetch(ctx, failed.ID, store.FetchedMetadata{ExtractionID: ext.ID,
		ContentType: store.ContentTypeArticle}))
	assert.Empty(t, stored(failed).FailureCause)
	assertCauseInvariant(t, db, "ApplyFetch")

	for _, cause := range []store.FailureCause{store.FailureCauseTLS, store.FailureCauseDeadLink} {
		d := create("", cause)
		_, err := docs.RequeueFetch(ctx, "local", d.ID)
		require.NoError(t, err)
		got := stored(d)
		assert.Equal(t, store.DocStatePending, got.State)
		assert.Empty(t, got.FailureCause)
		assertCauseInvariant(t, db, "RequeueFetch of a document failed for "+string(cause))
	}

	requeued, err := docs.RequeueFetchByStates(ctx, "local", []store.DocState{store.DocStateFailed, store.DocStateDead}, "")
	require.NoError(t, err)
	assert.Equal(t, 2*len(store.FailureCauses()), requeued)
	assertCauseInvariant(t, db, "RequeueFetchByStates")
	var causes int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM documents WHERE failure_cause IS NOT NULL`).Scan(&causes))
	assert.Zero(t, causes)

	_, err = NewBookmarks(db).Ingest(ctx, newIngestBookmark("https://example.com/new", store.SourceChrome))
	require.NoError(t, err)
	assertCauseInvariant(t, db, "Ingest")
}

// TestDocuments_Create_CauseMustFitTheState: Create refuses a cause that
// doesn't go with the state, and a failed or dead document without one,
// inserting nothing.
func TestDocuments_Create_CauseMustFitTheState(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	docs := NewDocuments(db)
	for name, d := range map[string]store.Document{
		"failed without a cause":  {State: store.DocStateFailed},
		"dead without a cause":    {State: store.DocStateDead},
		"dead for another cause":  {State: store.DocStateDead, FailureCause: store.FailureCauseAntiBot},
		"failed for a dead link":  {State: store.DocStateFailed, FailureCause: store.FailureCauseDeadLink},
		"pending with a cause":    {State: store.DocStatePending, FailureCause: store.FailureCauseOther},
		"fetched with a cause":    {State: store.DocStateFetched, FailureCause: store.FailureCauseTLS},
		"an unknown cause":        {FailureCause: "bogus"},
		"an unknown failed cause": {State: store.DocStateFailed, FailureCause: "Anti_Bot"},
	} {
		d.TenantID, d.URL = "local", "https://example.com/refused"
		assert.Error(t, docs.Create(ctx, &d), name)
	}
	assert.Zero(t, countRows(t, db, "documents"))
}
