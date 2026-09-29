package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// --- fakes ---

type fakeFetcher struct {
	name string
	res  *fetcher.Result
	err  error
}

func (f *fakeFetcher) Name() string { return f.name }
func (f *fakeFetcher) Fetch(_ context.Context, _ string) (*fetcher.Result, error) {
	return f.res, f.err
}

type fakeEmbedder struct{ dim int }

func (f *fakeEmbedder) Dimensions() int { return f.dim }
func (*fakeEmbedder) Model() string     { return "fake" }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		v := make([]float32, f.dim)
		for j := range v {
			v[j] = float32(i+1) * 0.01
		}
		out[i] = v
	}
	return out, nil
}

// --- helpers ---

func newTestDeps(t *testing.T) (Deps, *sqlitestore.DB, *fakeFetcher) {
	t.Helper()

	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	home, err := curiohome.Init(t.TempDir(), "fake", dim)
	require.NoError(t, err)

	docs := sqlitestore.NewDocuments(db)
	exts := sqlitestore.NewExtractions(db)
	bms := sqlitestore.NewBookmarks(db)
	chunks := sqlitestore.NewChunks(db, dim)
	queue := sqlitestore.NewJobs(db)

	ff := &fakeFetcher{
		name: "fakefetch",
		res: &fetcher.Result{
			Markdown:    "# Title\n\nThe body of an article about MVCC and concurrency.",
			FinalURL:    "https://example.com/article",
			ContentType: "article",
			Title:       "Article Title",
			Meta:        map[string]any{"via": "test"},
		},
	}
	dispatcher := &fetcher.Single{F: ff}

	idx := indexer.New(chunks, &fakeEmbedder{dim: dim}, indexer.Options{})

	return Deps{
		Home:        home,
		Documents:   docs,
		Extractions: exts,
		Bookmarks:   bms,
		Queue:       queue,
		Dispatcher:  dispatcher,
		Indexer:     idx,
		Log:         quietLog,
	}, db, ff
}

// docJob builds a fetch or index job for a document, as the queue holds it.
func docJob(t *testing.T, kind store.JobKind, docID string) *store.Job {
	t.Helper()
	job, err := store.NewDocumentJob("local", kind, docID)
	require.NoError(t, err)
	return job
}

// --- fetch handler ---

func TestFetchHandler_HappyPath(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()

	// Create a document in pending state.
	doc := &store.Document{TenantID: "local", URL: "https://example.com/article", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))

	job := docJob(t, store.JobKindFetch, doc.ID)

	err := fetchHandler(deps)(ctx, job)
	require.NoError(t, err)

	// Document should have title, extraction, etc.
	got, err := deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Title)
	assert.Equal(t, "Article Title", *got.Title)
	require.NotNil(t, got.CurrentExtractionID)

	// Extraction row exists with markdown_path set.
	ext, err := deps.Extractions.GetByID(ctx, *got.CurrentExtractionID)
	require.NoError(t, err)
	require.NotNil(t, ext.MarkdownPath)
	assert.Contains(t, *ext.MarkdownPath, doc.ID)

	// File written to disk.
	fullPath := deps.Home.ContentDir() + "/" + *ext.MarkdownPath
	info, err := os.Stat(fullPath)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))

	// An index job was enqueued.
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = ?`, store.JobKindIndex).Scan(&n))
	assert.Equal(t, 1, n)
}

// TestFetchHandler_RefetchClearsStaleMetadata: the document's metadata
// describes its current extraction, so what the new fetch didn't find is
// cleared rather than carried over from the previous one.
func TestFetchHandler_RefetchClearsStaleMetadata(t *testing.T) {
	deps, _, ff := newTestDeps(t)
	ctx := context.Background()
	doc := &store.Document{TenantID: "local", URL: "https://example.com/moved"}
	require.NoError(t, deps.Documents.Create(ctx, doc))
	job := docJob(t, store.JobKindFetch, doc.ID)

	ff.res.Author = "Ada"
	ff.res.FinalURL = "https://example.com/moved-here"
	require.NoError(t, fetchHandler(deps)(ctx, job))
	got, err := deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Author)
	assert.Equal(t, "Ada", *got.Author)
	require.NotNil(t, got.URLCanonical)
	assert.Equal(t, "https://example.com/moved-here", *got.URLCanonical)
	first := *got.CurrentExtractionID

	ff.res.Author = ""
	ff.res.FinalURL = ""
	require.NoError(t, fetchHandler(deps)(ctx, job))
	got, err = deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	assert.NotEqual(t, first, *got.CurrentExtractionID)
	assert.Nil(t, got.Author, "no author in the new extraction")
	assert.Nil(t, got.URLCanonical, "no redirect in the new fetch")
	require.NotNil(t, got.Title)
	assert.Equal(t, "Article Title", *got.Title)
	assert.Equal(t, store.ContentTypeArticle, got.ContentType)
}

// TestFetchHandler_ExtractionStatus: a result flagged Partial (fetched,
// but missing its primary content) is stored as a partial extraction whose
// error_message says why. The fetch still succeeds and queues indexing, so
// the document is searchable by what it has.
func TestFetchHandler_ExtractionStatus(t *testing.T) {
	const reason = "transcript not downloaded: yt-dlp: Unable to download video subtitles for 'en': HTTP Error 429: Too Many Requests"
	cases := []struct {
		name       string
		partial    bool
		reason     string
		wantStatus string
	}{
		{"ok", false, "", store.ExtractionStatusOK},
		{"partial", true, reason, store.ExtractionStatusPartial},
		{"partial without a reason", true, "", store.ExtractionStatusPartial},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, db, ff := newTestDeps(t)
			ff.res.Partial = tc.partial
			ff.res.PartialReason = tc.reason
			ctx := context.Background()
			doc := &store.Document{TenantID: "local", URL: "https://example.com/video", ContentType: store.ContentTypeVideo}
			require.NoError(t, deps.Documents.Create(ctx, doc))

			require.NoError(t, fetchHandler(deps)(ctx, docJob(t, store.JobKindFetch, doc.ID)))

			got, err := deps.Documents.GetByID(ctx, doc.ID)
			require.NoError(t, err)
			ext, err := deps.Extractions.GetByID(ctx, *got.CurrentExtractionID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, ext.Status)
			assert.Equal(t, store.NullableString(tc.reason), ext.ErrorMessage)

			var indexJobs int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = ?`, store.JobKindIndex).Scan(&indexJobs))
			assert.Equal(t, 1, indexJobs)
		})
	}
}

func TestFetchHandler_FetcherError_Retryable(t *testing.T) {
	deps, _, ff := newTestDeps(t)
	ff.res = nil
	ff.err = errors.New("network timeout")

	doc := &store.Document{TenantID: "local", URL: "https://x", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(context.Background(), doc))

	err := fetchHandler(deps)(context.Background(), docJob(t, store.JobKindFetch, doc.ID))
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrPermanent), "network failures must be retryable")
}

func TestFetchHandler_MissingDocument_Permanent(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	err := fetchHandler(deps)(context.Background(), docJob(t, store.JobKindFetch, "00000000-0000-0000-0000-000000000000"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermanent)
}

// TestFetchHandler_DeadLinkSentinelSurvivesBridge: the ErrPermanent bridge
// must preserve the fetcher's sentinel chain (double-%w) so the
// permanent-failure hook can distinguish dead links from other failures.
func TestFetchHandler_DeadLinkSentinelSurvivesBridge(t *testing.T) {
	deps, _, ff := newTestDeps(t)
	ff.res = nil
	ff.err = &fetcher.PermanentError{Err: fetcher.ErrDeadLink}

	doc := &store.Document{TenantID: "local", URL: "https://x/gone", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(context.Background(), doc))

	err := fetchHandler(deps)(context.Background(), docJob(t, store.JobKindFetch, doc.ID))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermanent)
	assert.ErrorIs(t, err, fetcher.ErrDeadLink, "sentinel must survive the ErrPermanent bridge")
}

func TestMarkDocFailed_DeadLinkGoesDead(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://x/gone", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))

	job := docJob(t, store.JobKindFetch, doc.ID)

	// A dead link → dead, for a dead link.
	require.NoError(t, markDocFailed(deps)(ctx, job, &fetcher.PermanentError{Err: fetcher.ErrDeadLink}))
	got, err := deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStateDead, got.State)
	assert.Equal(t, store.FailureCauseDeadLink, got.FailureCause)

	// Any other error → failed, for what the error says.
	require.NoError(t, markDocFailed(deps)(ctx, job, errors.New("some other permanent failure")))
	got, err = deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStateFailed, got.State)
	assert.Equal(t, store.FailureCauseOther, got.FailureCause)
}

// TestMarkDocFailed_IndexJobFailsForIndex: an index job that gave up
// failed its document for index, whatever its error says: the fetch
// worked.
func TestMarkDocFailed_IndexJobFailsForIndex(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://x/indexed", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))
	job := docJob(t, store.JobKindIndex, doc.ID)

	for _, jobErr := range []error{
		fmt.Errorf("%w: index: %w", ErrPermanent, embedder.ErrInputTooLong),
		&fetcher.PermanentError{Err: fetcher.ErrDeadLink},
		errOrphanExhausted,
	} {
		require.NoError(t, markDocFailed(deps)(ctx, job, jobErr))
		got, err := deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStateFailed, got.State, jobErr)
		assert.Equal(t, store.FailureCauseIndex, got.FailureCause, jobErr)
	}
}

// TestMarkDocFailed_NoErrorRecordsOther: a hook called without an error
// still records a cause the store takes, instead of an empty one it would
// refuse on every retry.
func TestMarkDocFailed_NoErrorRecordsOther(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://x/no-error", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))

	require.NoError(t, markDocFailed(deps)(ctx, docJob(t, store.JobKindFetch, doc.ID), nil))
	got, err := deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStateFailed, got.State)
	assert.Equal(t, store.FailureCauseOther, got.FailureCause)
}

// TestMarkDocFailed_FinalLoginWallGoesFailed: a page-level login wall that
// the fetcher made final is a failed document, not a dead one; the page may
// well exist behind the wall.
func TestMarkDocFailed_FinalLoginWallGoesFailed(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://x/thin", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))
	job := docJob(t, store.JobKindFetch, doc.ID)

	cause := &fetcher.PermanentError{Err: fmt.Errorf("native: %w (extracted text < 500 bytes)", fetcher.ErrLoginWall)}
	require.NoError(t, markDocFailed(deps)(ctx, job, cause))
	got, err := deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStateFailed, got.State)
	assert.Equal(t, store.FailureCauseLoginWall, got.FailureCause)
}

func TestFetchHandler_BadPayload_Permanent(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	err := fetchHandler(deps)(context.Background(), &store.Job{Payload: []byte("not json")})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermanent)
}

// --- index handler ---

func TestIndexHandler_HappyPath(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://example.com/idx", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))

	// Run the fetch first to set everything up.
	require.NoError(t, fetchHandler(deps)(ctx, docJob(t, store.JobKindFetch, doc.ID)))

	// Now run index.
	require.NoError(t, indexHandler(deps)(ctx, docJob(t, store.JobKindIndex, doc.ID)))

	got, _ := deps.Documents.GetByID(ctx, doc.ID)
	assert.Equal(t, store.DocStateFetched, got.State, "document should be fetched after index")

	// Searchable via BM25.
	hits, err := sqlitestore.NewChunks(db, sqlitetest.Width(t, db)).BM25Search(ctx, "local", "MVCC", 10, store.SearchFilters{})
	require.NoError(t, err)
	require.NotEmpty(t, hits, "indexed content should be searchable")
}

// TestIndexHandler_BookmarkTagsAreSearchable proves a bookmark's tags reach
// chunks_fts: a tag word that does NOT appear in the body is searchable
// after indexing.
func TestIndexHandler_BookmarkTagsAreSearchable(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://example.com/tagged", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))

	// Bookmark with a distinctive tag absent from the fetched content.
	docID := doc.ID
	require.NoError(t, deps.Bookmarks.Create(ctx, &store.Bookmark{
		TenantID: "local", DocumentID: &docID, URL: doc.URL, Source: store.SourceManual,
		SavedAt: time.Now().UTC(), Tags: []string{"zorptag"},
	}))

	require.NoError(t, fetchHandler(deps)(ctx, docJob(t, store.JobKindFetch, doc.ID)))
	require.NoError(t, indexHandler(deps)(ctx, docJob(t, store.JobKindIndex, doc.ID)))

	// Sanity: the tag is not in the body, so without denormalization this
	// would return nothing.
	hits, err := sqlitestore.NewChunks(db, sqlitetest.Width(t, db)).BM25Search(ctx, "local", "zorptag", 10, store.SearchFilters{})
	require.NoError(t, err)
	require.NotEmpty(t, hits, "bookmark tag should be searchable via chunks_fts")
	assert.Equal(t, doc.ID, hits[0].DocumentID)
}

func TestIndexHandler_MissingExtraction_Permanent(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()
	doc := &store.Document{TenantID: "local", URL: "https://x", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))
	// No extraction created.

	err := indexHandler(deps)(ctx, docJob(t, store.JobKindIndex, doc.ID))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermanent)
}

// --- pools ---

// TestNewPools: the daemon's pools each claim one kind, and only the
// per-document ones mark their document when a job gives up.
func TestNewPools(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	type shape struct {
		name         string
		kinds, hooks []store.JobKind
		size         int
	}
	pools := NewPools(deps, PoolSizes{Fetch: 16, Index: 4}, WorkerOptions{})
	got := make([]shape, 0, len(pools))
	for _, p := range pools {
		got = append(got, shape{p.Name, p.Worker.kinds(), slices.Sorted(maps.Keys(p.Worker.onPermFail)), p.Size})
		assert.Equal(t, []store.JobKind{p.Kind}, p.Worker.kinds(), "the pool's Kind is what it claims")
	}
	assert.Equal(t, []shape{
		{"fetch", []store.JobKind{store.JobKindFetch}, []store.JobKind{store.JobKindFetch}, 16},
		{"index", []store.JobKind{store.JobKindIndex}, []store.JobKind{store.JobKindIndex}, 4},
		{"cluster", []store.JobKind{store.JobKindCluster}, nil, 1},
	}, got)
}

// runPools runs every goroutine of pools until the returned stop is called.
func runPools(pools []Pool) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, p := range pools {
		for range p.Size {
			wg.Go(func() { _ = p.Worker.Run(ctx) })
		}
	}
	return func() {
		cancel()
		wg.Wait()
	}
}

// runPoolsUntil runs the daemon's pools over deps until cond holds, and
// stops them.
func runPoolsUntil(t *testing.T, deps Deps, cond func(c *assert.CollectT)) {
	t.Helper()
	stop := runPools(NewPools(deps, PoolSizes{Fetch: 1, Index: 1},
		WorkerOptions{PollInterval: 10 * time.Millisecond, Log: quietLog}))
	defer stop()
	require.EventuallyWithT(t, cond, 5*time.Second, 10*time.Millisecond)
}

// TestWorker_FullFetchIndexChain runs a document through the pools the
// daemon runs: the fetch pool fetches and enqueues the index job, which only
// the index pool claims.
func TestWorker_FullFetchIndexChain(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://example.com/e2e", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))
	require.NoError(t, deps.Queue.Enqueue(ctx, docJob(t, store.JobKindFetch, doc.ID)))

	stop := runPools(NewPools(deps, PoolSizes{Fetch: 1, Index: 1},
		WorkerOptions{PollInterval: 20 * time.Millisecond, Log: quietLog}))
	defer stop()

	// The document turns fetched before the index job is marked done, so
	// wait on the jobs themselves.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var n int
		require.NoError(c, db.QueryRow(`SELECT count(*) FROM jobs WHERE status = ?`, store.JobStatusDone).Scan(&n))
		assert.Equal(c, 2, n, "fetch + index should both be done")
	}, 5*time.Second, 20*time.Millisecond)
	stop()

	got, err := deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	require.Equal(t, store.DocStateFetched, got.State)
}

// TestWorker_RefetchClearsTheFailureCause: a document that failed, refetched
// and now fetched and indexed through the pools, ends fetched with no
// failure cause.
func TestWorker_RefetchClearsTheFailureCause(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()
	doc := &store.Document{TenantID: "local", URL: "https://example.com/back", FailureCause: store.FailureCauseAntiBot}
	require.NoError(t, deps.Documents.Create(ctx, doc))

	_, err := deps.Documents.RequeueFetch(ctx, "local", doc.ID)
	require.NoError(t, err)
	runPoolsUntil(t, deps, func(c *assert.CollectT) {
		got, err := deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(c, err)
		assert.Equal(c, store.DocStateFetched, got.State)
		assert.Empty(c, got.FailureCause)
	})
}

// TestWorker_PermanentFetchFailureSetsDocState runs the pools end to end:
// the fetcher returns a permanent failure, the job fails on attempt 1
// without burning the retry budget, and the fetch pool's permanent-failure
// hook records why the document failed, which sets its state. A dead link
// is dead; a certificate that failed verification is failed, since
// certificates get fixed.
func TestWorker_PermanentFetchFailureSetsDocState(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		want  store.FailureCause
	}{
		{"dead link", fmt.Errorf("native: dead link (HTTP 404): %w", fetcher.ErrDeadLink), store.FailureCauseDeadLink},
		{"invalid certificate", fmt.Errorf("native: fetch: %w: x509: certificate has expired", fetcher.ErrTLSCertificate),
			store.FailureCauseTLS},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, db, ff := newTestDeps(t)
			ff.res = nil
			ff.err = &fetcher.PermanentError{Err: tc.cause}
			ctx := context.Background()

			doc := &store.Document{TenantID: "local", URL: "https://example.com/x", ContentType: store.ContentTypeArticle}
			require.NoError(t, deps.Documents.Create(ctx, doc))
			require.NoError(t, deps.Queue.Enqueue(ctx, docJob(t, store.JobKindFetch, doc.ID)))

			stop := runPools(NewPools(deps, PoolSizes{Fetch: 1, Index: 1},
				WorkerOptions{PollInterval: 10 * time.Millisecond, Log: quietLog}))
			defer stop()

			require.EventuallyWithT(t, func(c *assert.CollectT) {
				got, err := deps.Documents.GetByID(ctx, doc.ID)
				require.NoError(c, err)
				assert.Equal(c, tc.want.State(), got.State)
				assert.Equal(c, tc.want, got.FailureCause)
			}, 5*time.Second, 10*time.Millisecond)
			stop()

			var (
				attempts int
				status   store.JobStatus
			)
			require.NoError(t, db.QueryRow(`SELECT attempts, status FROM jobs WHERE kind = ?`, store.JobKindFetch).Scan(&attempts, &status))
			assert.Equal(t, 1, attempts)
			assert.Equal(t, store.JobStatusFailed, status)
		})
	}
}

// refusingEmbedder fails every batch with err.
type refusingEmbedder struct{ err error }

func (refusingEmbedder) Dimensions() int { return 0 }
func (refusingEmbedder) Model() string   { return "fake" }
func (e refusingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, e.err
}

// TestWorker_DeterministicEmbedFailureIsPermanent: an index job whose
// chunk the model refuses as too long, or whose vectors come back at the
// wrong width, fails on its first attempt instead of repeating the same
// request, and its document goes failed for index, not dead. last_error
// says which chunks and why.
func TestWorker_DeterministicEmbedFailureIsPermanent(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		reason string
	}{
		{"input too long", fmt.Errorf("ollama embed: %w: %w", embedder.ErrInputTooLong,
			&ollama.StatusError{Code: http.StatusBadRequest, Body: `{"error":"the input length exceeds the context length"}`}),
			"the input length exceeds the context length"},
		{"wrong dimension", fmt.Errorf("ollama embed: %w: embedding[0] has dim 768, expected 1024",
			embedder.ErrWrongDimension), "has dim 768, expected 1024"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, db, _ := newTestDeps(t)
			deps.Indexer = indexer.New(sqlitestore.NewChunks(db, sqlitetest.Width(t, db)), refusingEmbedder{tc.err},
				indexer.Options{})
			ctx := context.Background()
			doc := &store.Document{TenantID: "local", URL: "https://example.com/long", ContentType: store.ContentTypeArticle}
			require.NoError(t, deps.Documents.Create(ctx, doc))
			require.NoError(t, deps.Queue.Enqueue(ctx, docJob(t, store.JobKindFetch, doc.ID)))

			runPoolsUntil(t, deps, func(c *assert.CollectT) {
				var status store.JobStatus
				require.NoError(c, db.QueryRow(`SELECT status FROM jobs WHERE kind = ?`, store.JobKindIndex).Scan(&status))
				assert.Equal(c, store.JobStatusFailed, status)
			})

			var (
				attempts  int
				lastError string
			)
			require.NoError(t, db.QueryRow(`SELECT attempts, last_error FROM jobs WHERE kind = ?`, store.JobKindIndex).
				Scan(&attempts, &lastError))
			assert.Equal(t, 1, attempts, "not retried")
			assert.Contains(t, lastError, "embed chunks 0-0 of 1")
			assert.Contains(t, lastError, tc.reason)
			got, err := deps.Documents.GetByID(ctx, doc.ID)
			require.NoError(t, err)
			assert.Equal(t, store.DocStateFailed, got.State)
			assert.Equal(t, store.FailureCauseIndex, got.FailureCause)
		})
	}
}

// TestWorker_RefetchRejectsJinaJunk refetches a fetched, indexed document
// through the pools with the real Native fetcher, whose origin now serves a
// thin page while Jina answers a challenge page or reports the target's 404.
// The document fails as anti-bot, or goes dead, on the first attempt. It
// keeps the extraction it had, is never marked fetched again, and leaves
// search.
func TestWorker_RefetchRejectsJinaJunk(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  store.FailureCause
	}{
		{"challenge", "Title: Just a moment...\n\nURL Source: https://example.com/article\n\n" +
			"Warning: Target URL returned error 403: Forbidden\n" +
			"Warning: This page maybe requiring CAPTCHA, please make sure you are authorized to access this page.\n\n" +
			"Markdown Content:\n## Performing security verification\n\n" +
			"This website uses a security service to protect against malicious bots. " +
			"This page is displayed while the website verifies you are not a bot.", store.FailureCauseAntiBot},
		{"target 404", "Title: Welcome to Python.org\n\nURL Source: https://example.com/article\n\n" +
			"Warning: Target URL returned error 404: Not Found\n\n" +
			"Markdown Content:\n" + strings.Repeat("The official home of the Python Programming Language. ", 60),
			store.FailureCauseDeadLink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, db, _ := newTestDeps(t)
			ctx := context.Background()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `<html><body><p>nope</p></body></html>`)
			}))
			defer origin.Close()
			var jinaHits atomic.Int32
			jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				jinaHits.Add(1)
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer jina.Close()
			chunks := sqlitestore.NewChunks(db, sqlitetest.Width(t, db))
			searched := func(docID string) (bm25, vector bool) {
				t.Helper()
				bm, err := chunks.BM25Search(ctx, "local", "MVCC", 10, store.SearchFilters{})
				require.NoError(t, err)
				query := make([]float32, sqlitetest.Width(t, db))
				for i := range query {
					query[i] = 0.01 // fakeEmbedder's first vector
				}
				vec, err := chunks.VectorSearch(ctx, "local", query, 10, store.SearchFilters{})
				require.NoError(t, err)
				has := func(hits []store.ChunkHit) bool {
					return slices.ContainsFunc(hits, func(h store.ChunkHit) bool { return h.DocumentID == docID })
				}
				return has(bm), has(vec)
			}

			// The fake fetcher fetched the document once, and it was indexed.
			doc := &store.Document{TenantID: "local", URL: origin.URL + "/article", ContentType: store.ContentTypeArticle}
			require.NoError(t, deps.Documents.Create(ctx, doc))
			require.NoError(t, deps.Queue.Enqueue(ctx, docJob(t, store.JobKindFetch, doc.ID)))
			runPoolsUntil(t, deps, func(c *assert.CollectT) {
				var n int
				require.NoError(c, db.QueryRow(`SELECT count(*) FROM jobs WHERE status = ?`, store.JobStatusDone).Scan(&n))
				assert.Equal(c, 2, n, "fetch + index should both be done")
			})
			before, err := deps.Documents.GetByID(ctx, doc.ID)
			require.NoError(t, err)
			require.Equal(t, store.DocStateFetched, before.State)
			require.NotNil(t, before.CurrentExtractionID)
			bm, vec := searched(doc.ID)
			require.True(t, bm && vec, "the fetched document is searched")

			deps.Dispatcher = &fetcher.Single{F: fetcher.NewNative(fetcher.NativeOptions{
				Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/",
				DeadLinkDetection: true, Log: quietLog,
			})}
			refetch, err := deps.Documents.RequeueFetch(ctx, "local", doc.ID)
			require.NoError(t, err)
			runPoolsUntil(t, deps, func(c *assert.CollectT) {
				got, err := deps.Documents.GetByID(ctx, doc.ID)
				require.NoError(c, err)
				assert.Equal(c, tc.want.State(), got.State)
				assert.Equal(c, tc.want, got.FailureCause)
			})

			var (
				attempts int
				status   store.JobStatus
			)
			require.NoError(t, db.QueryRow(`SELECT attempts, status FROM jobs WHERE id = ?`, refetch.ID).Scan(&attempts, &status))
			assert.Equal(t, 1, attempts)
			assert.Equal(t, store.JobStatusFailed, status)
			assert.Equal(t, int32(1), jinaHits.Load())

			after, err := deps.Documents.GetByID(ctx, doc.ID)
			require.NoError(t, err)
			assert.Equal(t, before.CurrentExtractionID, after.CurrentExtractionID)
			var extractions, indexJobs int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM document_extractions WHERE document_id = ?`, doc.ID).Scan(&extractions))
			assert.Equal(t, 1, extractions, "the junk answer is not stored")
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = ?`, store.JobKindIndex).Scan(&indexJobs))
			assert.Equal(t, 1, indexJobs, "no index job, so the document never turns fetched")

			bm, vec = searched(doc.ID)
			assert.False(t, bm, "bm25")
			assert.False(t, vec, "vector")
		})
	}
}

// TestWorker_SiteBlockIsAntiBotWhateverJinaSays: a site that answers 403
// fails even its first document for anti_bot when Jina refuses the target
// too. The origin's verdict is host-wide, so it is cached and the refusal
// stays retryable; the retry fails from the host cache, without a request,
// and the document records the cached verdict, not Jina's.
func TestWorker_SiteBlockIsAntiBotWhateverJinaSays(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()
	var originHits, jinaHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originHits.Add(1)
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer origin.Close()
	jina := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jinaHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnavailableForLegalReasons)
		_, _ = io.WriteString(w, `{"code": 451, "message": "This domain is excluded from Jina Reader at the request of its owner."}`)
	}))
	defer jina.Close()
	deps.Dispatcher = &fetcher.Single{F: fetcher.NewNative(fetcher.NativeOptions{
		Timeout: 5 * time.Second, JinaFallback: true, JinaBaseURL: jina.URL + "/", Log: quietLog,
	})}

	doc := &store.Document{TenantID: "local", URL: origin.URL + "/article", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))
	job := docJob(t, store.JobKindFetch, doc.ID)
	require.NoError(t, deps.Queue.Enqueue(ctx, job))
	jobRow := func(c require.TestingT) (status store.JobStatus, attempts int, lastError string) {
		require.NoError(c, db.QueryRow(`SELECT status, attempts, last_error FROM jobs WHERE id = ?`, job.ID).
			Scan(&status, &attempts, &lastError))
		return status, attempts, lastError
	}

	// Attempt 1 asks the origin, then Jina, and is retried.
	runPoolsUntil(t, deps, func(c *assert.CollectT) {
		status, attempts, lastError := jobRow(c)
		assert.Equal(c, store.JobStatusPending, status)
		assert.Equal(c, 1, attempts)
		assert.Contains(c, lastError, "jina: refused the target: HTTP 451")
	})
	got, err := deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	require.Equal(t, store.DocStatePending, got.State)

	// Attempt 2, once its backoff is skipped, fails from the host cache.
	_, err = db.ExecContext(ctx, `UPDATE jobs SET run_after = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`, job.ID)
	require.NoError(t, err)
	runPoolsUntil(t, deps, func(c *assert.CollectT) {
		got, err := deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(c, err)
		assert.Equal(c, store.DocStateFailed, got.State)
		assert.Equal(c, store.FailureCauseAntiBot, got.FailureCause)
	})
	status, attempts, lastError := jobRow(t)
	assert.Equal(t, store.JobStatusFailed, status)
	assert.Equal(t, 2, attempts)
	assert.Contains(t, lastError, "(cached: jina: refused the target: HTTP 451")
	assert.Equal(t, int32(1), originHits.Load())
	assert.Equal(t, int32(1), jinaHits.Load())
}

func TestWorker_PermanentFailureDoesNotRetry(t *testing.T) {
	q := sqlitestore.NewJobs(sqlitetest.NewDB(t))
	q.MaxAttempts = 5

	ctx := context.Background()
	require.NoError(t, q.Enqueue(ctx, &store.Job{TenantID: "local", Kind: store.JobKindSummarize, Payload: []byte("{}")}))

	var calls atomic.Int32
	worker := NewWorker(q, WorkerOptions{PollInterval: 10 * time.Millisecond})
	worker.Register(store.JobKindSummarize, func(_ context.Context, _ *store.Job) error {
		calls.Add(1)
		return errors.Join(ErrPermanent, errors.New("bad input"))
	})

	rctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_ = worker.Run(rctx)

	assert.Equal(t, int32(1), calls.Load(), "permanent failures should not be retried")
}
