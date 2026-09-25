package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/curiohome"
	"github.com/samsar/curio/internal/fetcher"
	"github.com/samsar/curio/internal/indexer"
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
func (f *fakeEmbedder) Model() string   { return "fake" }
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

	homeDir := t.TempDir()
	home, err := curiohome.Init(homeDir, "fake", 768)
	require.NoError(t, err)

	db := sqlitetest.NewDB(t)

	docs := sqlitestore.NewDocuments(db)
	exts := sqlitestore.NewExtractions(db)
	bms := sqlitestore.NewBookmarks(db)
	chunks := sqlitestore.NewChunks(db, 768)
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

	idx := indexer.New(chunks, &fakeEmbedder{dim: 768}, indexer.Options{})

	return Deps{
		Home:        home,
		Documents:   docs,
		Extractions: exts,
		Bookmarks:   bms,
		Chunks:      chunks,
		Queue:       queue,
		Dispatcher:  dispatcher,
		Indexer:     idx,
	}, db, ff
}

// --- fetch handler ---

func TestFetchHandler_HappyPath(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()

	// Create a document in pending state.
	doc := &store.Document{TenantID: "local", URL: "https://example.com/article", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Upsert(ctx, doc))

	payload, _ := json.Marshal(FetchPayload{DocumentID: doc.ID})
	job := &store.Job{TenantID: "local", Kind: store.JobKindFetch, Payload: payload}

	err := FetchHandler(deps)(ctx, job)
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

// TestFetchHandler_ExtractionStatus: a result flagged Partial (fetched,
// but missing its primary content) is stored as a partial extraction.
func TestFetchHandler_ExtractionStatus(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%v", partial), func(t *testing.T) {
			deps, _, ff := newTestDeps(t)
			ff.res.Partial = partial
			ctx := context.Background()
			doc := &store.Document{TenantID: "local", URL: "https://example.com/video", ContentType: store.ContentTypeVideo}
			require.NoError(t, deps.Documents.Upsert(ctx, doc))

			payload, _ := json.Marshal(FetchPayload{DocumentID: doc.ID})
			require.NoError(t, FetchHandler(deps)(ctx, &store.Job{TenantID: "local", Kind: store.JobKindFetch, Payload: payload}))

			got, err := deps.Documents.GetByID(ctx, doc.ID)
			require.NoError(t, err)
			ext, err := deps.Extractions.GetByID(ctx, *got.CurrentExtractionID)
			require.NoError(t, err)
			want := store.ExtractionStatusOK
			if partial {
				want = store.ExtractionStatusPartial
			}
			assert.Equal(t, want, ext.Status)
		})
	}
}

func TestFetchHandler_FetcherError_Retryable(t *testing.T) {
	deps, _, ff := newTestDeps(t)
	ff.res = nil
	ff.err = errors.New("network timeout")

	doc := &store.Document{TenantID: "local", URL: "https://x", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Upsert(context.Background(), doc))

	payload, _ := json.Marshal(FetchPayload{DocumentID: doc.ID})
	err := FetchHandler(deps)(context.Background(), &store.Job{Payload: payload})
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrPermanent), "network failures must be retryable")
}

func TestFetchHandler_MissingDocument_Permanent(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	payload, _ := json.Marshal(FetchPayload{DocumentID: "00000000-0000-0000-0000-000000000000"})
	err := FetchHandler(deps)(context.Background(), &store.Job{Payload: payload})
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
	require.NoError(t, deps.Documents.Upsert(context.Background(), doc))

	payload, _ := json.Marshal(FetchPayload{DocumentID: doc.ID})
	err := FetchHandler(deps)(context.Background(), &store.Job{Payload: payload})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermanent)
	assert.ErrorIs(t, err, fetcher.ErrDeadLink, "sentinel must survive the ErrPermanent bridge")
}

func TestMarkDocFailed_DeadLinkGoesDead(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://x/gone", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Upsert(ctx, doc))

	payload, _ := json.Marshal(FetchPayload{DocumentID: doc.ID})
	job := &store.Job{Payload: payload}

	// Dead-link cause → dead.
	require.NoError(t, MarkDocFailed(deps)(ctx, job, &fetcher.PermanentError{Err: fetcher.ErrDeadLink}))
	got, err := deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStateDead, got.State)

	// Any other cause → failed.
	require.NoError(t, MarkDocFailed(deps)(ctx, job, errors.New("some other permanent failure")))
	got, err = deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStateFailed, got.State)
}

// TestMarkDocFailed_FinalLoginWallGoesFailed: a page-level login wall that
// the fetcher made final is a failed document, not a dead one; the page may
// well exist behind the wall.
func TestMarkDocFailed_FinalLoginWallGoesFailed(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://x/thin", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Upsert(ctx, doc))
	payload, _ := json.Marshal(FetchPayload{DocumentID: doc.ID})

	cause := &fetcher.PermanentError{Err: fmt.Errorf("native: %w (extracted text < 500 bytes)", fetcher.ErrLoginWall)}
	require.NoError(t, MarkDocFailed(deps)(ctx, &store.Job{Payload: payload}, cause))
	got, err := deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStateFailed, got.State)
}

func TestFetchHandler_BadPayload_Permanent(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	err := FetchHandler(deps)(context.Background(), &store.Job{Payload: []byte("not json")})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermanent)
}

// --- index handler ---

func TestIndexHandler_HappyPath(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://example.com/idx", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Upsert(ctx, doc))

	// Run the fetch first to set everything up.
	payload, _ := json.Marshal(FetchPayload{DocumentID: doc.ID})
	require.NoError(t, FetchHandler(deps)(ctx, &store.Job{Payload: payload}))

	// Now run index.
	ip, _ := json.Marshal(IndexPayload{DocumentID: doc.ID})
	require.NoError(t, IndexHandler(deps)(ctx, &store.Job{Payload: ip}))

	got, _ := deps.Documents.GetByID(ctx, doc.ID)
	assert.Equal(t, store.DocStateFetched, got.State, "document should be fetched after index")

	// Searchable via BM25.
	hits, _ := deps.Chunks.BM25Search(ctx, "local", "MVCC", 10, store.SearchFilters{})
	require.NotEmpty(t, hits, "indexed content should be searchable")
}

// TestIndexHandler_BookmarkTagsAreSearchable proves a bookmark's tags reach
// chunks_fts: a tag word that does NOT appear in the body is searchable
// after indexing.
func TestIndexHandler_BookmarkTagsAreSearchable(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()

	doc := &store.Document{TenantID: "local", URL: "https://example.com/tagged", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Upsert(ctx, doc))

	// Bookmark with a distinctive tag absent from the fetched content.
	docID := doc.ID
	require.NoError(t, deps.Bookmarks.Create(ctx, &store.Bookmark{
		TenantID: "local", DocumentID: &docID, URL: doc.URL, Source: store.SourceManual,
		SavedAt: time.Now().UTC(), Tags: []string{"zorptag"},
	}))

	fp, _ := json.Marshal(FetchPayload{DocumentID: doc.ID})
	require.NoError(t, FetchHandler(deps)(ctx, &store.Job{Payload: fp}))
	ip, _ := json.Marshal(IndexPayload{DocumentID: doc.ID})
	require.NoError(t, IndexHandler(deps)(ctx, &store.Job{Payload: ip}))

	// Sanity: the tag is not in the body, so without denormalization this
	// would return nothing.
	hits, err := deps.Chunks.BM25Search(ctx, "local", "zorptag", 10, store.SearchFilters{})
	require.NoError(t, err)
	require.NotEmpty(t, hits, "bookmark tag should be searchable via chunks_fts")
	assert.Equal(t, doc.ID, hits[0].DocumentID)
}

func TestIndexHandler_MissingExtraction_Permanent(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()
	doc := &store.Document{TenantID: "local", URL: "https://x", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Upsert(ctx, doc))
	// No extraction created.

	payload, _ := json.Marshal(IndexPayload{DocumentID: doc.ID})
	err := IndexHandler(deps)(ctx, &store.Job{Payload: payload})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermanent)
}

// --- worker integration ---

func TestWorker_FullFetchIndexChain(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doc := &store.Document{TenantID: "local", URL: "https://example.com/e2e", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Upsert(ctx, doc))

	payload, _ := json.Marshal(FetchPayload{DocumentID: doc.ID})
	require.NoError(t, deps.Queue.Enqueue(ctx, &store.Job{
		TenantID: "local", Kind: store.JobKindFetch, Payload: payload,
	}))

	worker := NewWorker(deps.Queue, WorkerOptions{PollInterval: 20 * time.Millisecond})
	Register(worker, deps)

	// Run worker in background; cancel after both jobs complete. The
	// document turns fetched before the index job is marked done, so wait
	// on the jobs themselves.
	done := make(chan struct{})
	go func() { _ = worker.Run(ctx); close(done) }()

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var n int
		require.NoError(c, db.QueryRow(`SELECT count(*) FROM jobs WHERE status = ?`, store.JobStatusDone).Scan(&n))
		assert.Equal(c, 2, n, "fetch + index should both be done")
	}, 5*time.Second, 20*time.Millisecond)

	cancel()
	<-done

	got, err := deps.Documents.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	require.Equal(t, store.DocStateFetched, got.State)
}

// TestWorker_DeadLinkMarksDocDead runs the real worker loop end-to-end:
// fetcher says dead link → job permanently fails on attempt 1 → the
// permanent-failure hook flips the document to state=dead (not failed).
func TestWorker_DeadLinkMarksDocDead(t *testing.T) {
	deps, db, ff := newTestDeps(t)
	ff.res = nil
	ff.err = &fetcher.PermanentError{Err: fmt.Errorf("native: dead link (HTTP 404): %w", fetcher.ErrDeadLink)}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doc := &store.Document{TenantID: "local", URL: "https://example.com/gone", ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Upsert(ctx, doc))

	payload, _ := json.Marshal(FetchPayload{DocumentID: doc.ID})
	require.NoError(t, deps.Queue.Enqueue(ctx, &store.Job{
		TenantID: "local", Kind: store.JobKindFetch, Payload: payload,
	}))

	worker := NewWorker(deps.Queue, WorkerOptions{PollInterval: 10 * time.Millisecond})
	Register(worker, deps)

	done := make(chan struct{})
	go func() { _ = worker.Run(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, _ := deps.Documents.GetByID(ctx, doc.ID)
		if d != nil && d.State == store.DocStateDead {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	got, err := deps.Documents.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DocStateDead, got.State)

	// One attempt only — dead links must not burn the retry budget.
	var attempts int
	require.NoError(t, db.QueryRow(`SELECT attempts FROM jobs WHERE kind = ?`, store.JobKindFetch).Scan(&attempts))
	assert.Equal(t, 1, attempts)
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
