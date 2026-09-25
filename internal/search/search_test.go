package search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// fakeEmbedder returns canned vectors keyed on input text. Tests control
// which chunk the vector retriever ranks first by matching the query's
// embedding to the seeded chunks' embeddings.
type fakeEmbedder struct {
	byText map[string][]float32
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := f.byText[t]
		if !ok {
			// Default vector for any unknown text — far from anything.
			v = filledVec(0.999)
		}
		out[i] = v
	}
	return out, nil
}

const dim = 768

func TestEngine_QueryPrefixApplied(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, docIDs := seedCorpus(t, db)

	// The query term matches nothing in BM25, so ranking is driven purely by
	// the vector path. The embedder only returns the postgres chunk's vector
	// (0.10, the closest) for the PREFIXED query; without the prefix it'd get
	// the default far vector and postgres would rank last, not first.
	emb := &fakeEmbedder{byText: map[string][]float32{
		"search_query: zzqterm": filledVec(0.10),
	}}
	eng := New(chunks, docs, emb, Config{QueryPrefix: "search_query: "})

	res, err := eng.Search(context.Background(), Request{TenantID: "local", Query: "zzqterm", K: 3})
	require.NoError(t, err)
	require.NotEmpty(t, res.Items)
	assert.Equal(t, docIDs[0], res.Items[0].Document.ID, "prefixed query vector should rank the postgres doc first")
}

func filledVec(v float32) []float32 {
	out := make([]float32, dim)
	for i := range out {
		out[i] = v
	}
	return out
}

// seedCorpus puts three documents and one chunk per document into the DB.
// The first chunk gets embedding 0.1, second 0.2, third 0.3.
func seedCorpus(t *testing.T, db *sqlitestore.DB) (docs *sqlitestore.Documents, chunks *sqlitestore.Chunks, docIDs []string) {
	t.Helper()
	ctx := context.Background()
	docs = sqlitestore.NewDocuments(db)
	exts := sqlitestore.NewExtractions(db)
	chunks = sqlitestore.NewChunks(db, dim)

	corpus := []struct {
		url  string
		text string
		vec  float32
	}{
		{"https://example.com/postgres", "PostgreSQL uses MVCC for concurrency control between transactions.", 0.10},
		{"https://example.com/btree", "B-tree indexes power range scans across many database systems.", 0.20},
		{"https://example.com/llm", "Large language models predict the next token using attention.", 0.30},
	}

	for _, c := range corpus {
		d := &store.Document{TenantID: "local", URL: c.url, ContentType: store.ContentTypeArticle}
		require.NoError(t, docs.Create(ctx, d))
		e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK, FetchedAt: time.Now().UTC()}
		require.NoError(t, exts.Create(ctx, e))
		require.NoError(t, docs.SetCurrentExtraction(ctx, d.ID, e.ID))
		require.NoError(t, chunks.ReplaceForDocument(ctx, d.ID, e.ID, "", nil,
			[]store.ChunkInput{{Text: c.text, Embedding: filledVec(c.vec)}}))
		docIDs = append(docIDs, d.ID)
	}
	return
}

func TestEngine_HybridSearch(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)

	emb := &fakeEmbedder{byText: map[string][]float32{
		"mvcc concurrency": filledVec(0.10), // closest to postgres chunk
	}}
	engine := New(chunks, docs, emb, Config{})

	res, err := engine.Search(context.Background(), Request{
		TenantID: "local",
		Query:    "mvcc concurrency",
		K:        3,
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Items)
	assert.Contains(t, res.Items[0].Document.URL, "postgres")
}

func TestEngine_BM25OnlyMatch(t *testing.T) {
	// When the embedder is "lost" but BM25 has a strong match, the result
	// still surfaces the right document.
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)

	emb := &fakeEmbedder{byText: nil} // returns the default far-away vector
	engine := New(chunks, docs, emb, Config{})

	res, err := engine.Search(context.Background(), Request{
		TenantID: "local",
		Query:    "attention token",
		K:        3,
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Items)
	assert.Contains(t, res.Items[0].Document.URL, "llm")
}

func TestEngine_RequiresQuery(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)
	engine := New(chunks, docs, &fakeEmbedder{}, Config{})

	_, err := engine.Search(context.Background(), Request{TenantID: "local"})
	require.Error(t, err)
}

func TestEngine_PerHitScoresExposed(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)
	emb := &fakeEmbedder{byText: map[string][]float32{"mvcc": filledVec(0.10)}}
	engine := New(chunks, docs, emb, Config{})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "mvcc", K: 3})
	require.NoError(t, err)
	require.NotEmpty(t, res.Items)
	hit := res.Items[0]
	require.NotEmpty(t, hit.Chunks, "results should include chunk matches")
	// At least one of BM25 or vector should have surfaced the top chunk.
	cm := hit.Chunks[0]
	assert.True(t, cm.BM25Score != nil || cm.VectorScore != nil)
}

func TestSanitizeBM25Query(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			// Real failing query from the field: punctuation no longer
			// crashes FTS5, stopwords dropped, content terms OR'd.
			"Find me all the best articles about computer science, data structures, and algorithms",
			`"articles" OR "computer" OR "science" OR "data" OR "structures" OR "algorithms"`,
		},
		{"", ""},
		{"   ,,,   ???   ", ""},
		{"the and of for", ""}, // all stopwords → empty (caller skips BM25)
		{"don't break apostrophes", `"don't" OR "break" OR "apostrophes"`},
		{"state-of-the-art", `"state-of-the-art"`},
		{`he said "hi"`, `"said" OR "hi"`}, // "he" stopworded, inner quotes stripped
		{"AND OR NOT", ""},                 // case-insensitive stopword match
	}
	for _, c := range cases {
		got := sanitizeBM25Query(c.in)
		if got != c.want {
			t.Errorf("sanitizeBM25Query(%q):\n  got:  %q\n  want: %q", c.in, got, c.want)
		}
	}
}

func TestEngine_Related_RanksByProximity(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, docIDs := seedCorpus(t, db)

	// Related needs no embedder — it reads stored vectors. The fake is
	// only here to satisfy the constructor.
	engine := New(chunks, docs, &fakeEmbedder{}, Config{})

	res, err := engine.Related(context.Background(), RelatedRequest{
		TenantID:   "local",
		DocumentID: docIDs[0], // postgres @ 0.10
		K:          5,
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 2, "self must be excluded")

	// btree (0.20) is nearer to postgres (0.10) than llm (0.30).
	assert.Contains(t, res.Items[0].Document.URL, "btree")
	assert.Contains(t, res.Items[1].Document.URL, "llm")
	for _, it := range res.Items {
		assert.NotEqual(t, docIDs[0], it.Document.ID)
	}
}

func TestEngine_Related_UnindexedDocIsEmpty(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)

	// A document with no chunks: create one without indexing it.
	ctx := context.Background()
	d := &store.Document{TenantID: "local", URL: "https://example.com/pending", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Create(ctx, d))

	engine := New(chunks, docs, &fakeEmbedder{}, Config{})
	res, err := engine.Related(ctx, RelatedRequest{TenantID: "local", DocumentID: d.ID, K: 5})
	require.NoError(t, err)
	assert.Empty(t, res.Items)
}

func TestEngine_Related_UnknownDocIsNotFound(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)

	engine := New(chunks, docs, &fakeEmbedder{}, Config{})
	_, err := engine.Related(context.Background(), RelatedRequest{
		TenantID:   "local",
		DocumentID: "00000000-0000-0000-0000-000000000000",
		K:          5,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound, "unknown doc must surface ErrNotFound for the API's 404 mapping")
}

// embedFunc adapts a function to Embedder.
type embedFunc func(ctx context.Context, texts []string) ([][]float32, error)

func (f embedFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

var errRefused = errors.New("ollama: post: dial tcp 127.0.0.1:11434: connect: connection refused")

func failingEmbedder() Embedder {
	return embedFunc(func(context.Context, []string) ([][]float32, error) { return nil, errRefused })
}

// hangingEmbedder blocks until its context ends, like an Ollama that accepts
// the request and never answers. entered is closed on the first call.
func hangingEmbedder(entered chan struct{}) Embedder {
	var once sync.Once
	return embedFunc(func(ctx context.Context, _ []string) ([][]float32, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	})
}

func TestEngine_DegradesToKeywordResults(t *testing.T) {
	cases := []struct {
		name string
		emb  Embedder
	}{
		{"embedder error", failingEmbedder()},
		{"no vectors", embedFunc(func(context.Context, []string) ([][]float32, error) { return nil, nil })},
		{"wrong dimension", embedFunc(func(context.Context, []string) ([][]float32, error) {
			return [][]float32{{1, 2, 3}}, nil
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := sqlitetest.NewDB(t)
			docs, chunks, _ := seedCorpus(t, db)
			var logs bytes.Buffer
			engine := New(chunks, docs, tc.emb, Config{Log: slog.New(slog.NewTextHandler(&logs, nil))})

			res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "attention token", K: 3})
			require.NoError(t, err)
			require.NotEmpty(t, res.Items)
			assert.Contains(t, res.Items[0].Document.URL, "llm")
			assert.True(t, res.Degraded)
			require.Len(t, res.Warnings, 1)
			assert.Contains(t, res.Warnings[0], "semantic search unavailable")
			assert.Contains(t, res.Warnings[0], "keyword-only")
			assert.Zero(t, res.VectorHits)
			assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"), "one warning is logged")
		})
	}
}

func TestEngine_DegradedWithoutKeywordTerms(t *testing.T) {
	// Nothing survives BM25 sanitization, so the vector leg was the only
	// retriever: the result is empty, but still a success with a warning.
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)
	engine := New(chunks, docs, failingEmbedder(), Config{Log: slog.New(slog.DiscardHandler)})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "the and of", K: 3})
	require.NoError(t, err)
	assert.Empty(t, res.Items)
	assert.True(t, res.Degraded)
	assert.NotEmpty(t, res.Warnings)
}

func TestEngine_EmbedTimeoutDegrades(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)
	engine := New(chunks, docs, hangingEmbedder(make(chan struct{})), Config{
		EmbedTimeout: 50 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})

	start := time.Now()
	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "attention token", K: 3})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "bounded by the embed timeout")
	assert.True(t, res.Degraded)
	require.NotEmpty(t, res.Items)
	assert.Contains(t, res.Items[0].Document.URL, "llm")
}

func TestEngine_CallerContextEndIsAnError(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		db := sqlitetest.NewDB(t)
		docs, chunks, _ := seedCorpus(t, db)
		entered := make(chan struct{})
		engine := New(chunks, docs, hangingEmbedder(entered), Config{Log: slog.New(slog.DiscardHandler)})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-entered
			cancel()
		}()
		res, err := engine.Search(ctx, Request{TenantID: "local", Query: "attention token", K: 3})
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, res)
	})
	t.Run("deadline", func(t *testing.T) {
		db := sqlitetest.NewDB(t)
		docs, chunks, _ := seedCorpus(t, db)
		engine := New(chunks, docs, hangingEmbedder(make(chan struct{})), Config{Log: slog.New(slog.DiscardHandler)})

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := engine.Search(ctx, Request{TenantID: "local", Query: "attention token", K: 3})
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

// hookedChunks lets a test intercept BM25Search.
type hookedChunks struct {
	store.ChunkStore
	bm25 func(ctx context.Context) error
}

func (h *hookedChunks) BM25Search(ctx context.Context, tenantID, query string, limit int, filters store.SearchFilters) ([]store.ChunkHit, error) {
	if err := h.bm25(ctx); err != nil {
		return nil, err
	}
	return h.ChunkStore.BM25Search(ctx, tenantID, query, limit, filters)
}

// brokenChunkLookup fails GetByIDs, the snippet lookup for each hit.
type brokenChunkLookup struct {
	store.ChunkStore
}

func (brokenChunkLookup) GetByIDs(context.Context, []string) ([]*store.Chunk, error) {
	return nil, errors.New("database is locked")
}

func TestEngine_ChunkLookupFailureKeepsHitsAndIsLogged(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)
	var logs bytes.Buffer
	engine := New(brokenChunkLookup{chunks}, docs, &fakeEmbedder{}, Config{Log: slog.New(slog.NewTextHandler(&logs, nil))})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "attention token", K: 3})
	require.NoError(t, err)
	require.NotEmpty(t, res.Items)
	assert.Empty(t, res.Items[0].Chunks, "the hit is kept without its chunks")
	assert.Contains(t, logs.String(), "level=WARN")
	assert.Contains(t, logs.String(), res.Items[0].Document.ID)
	assert.Contains(t, logs.String(), "database is locked")
}

// vanishingDocs is a document store in which the documents in gone were
// deleted after the retrievers read their chunks.
type vanishingDocs struct {
	store.DocumentStore
	gone map[string]bool
}

func (v vanishingDocs) GetByID(ctx context.Context, id string) (*store.Document, error) {
	if v.gone[id] {
		return nil, fmt.Errorf("document %s: %w", id, store.ErrNotFound)
	}
	return v.DocumentStore.GetByID(ctx, id)
}

// TestEngine_HitDeletedMidQueryIsSkipped: a matching document deleted
// between the chunk search and hydration drops out of the results; the
// query doesn't fail, and Related doesn't report its source as missing.
func TestEngine_HitDeletedMidQueryIsSkipped(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, docIDs := seedCorpus(t, db) // postgres, btree, llm
	gone := vanishingDocs{docs, map[string]bool{docIDs[1]: true}}
	engine := New(chunks, gone, &fakeEmbedder{byText: map[string][]float32{
		"database": filledVec(0.20), // nearest the btree chunk
	}}, Config{})
	ids := func(hits []Hit) []string {
		out := make([]string, 0, len(hits))
		for _, h := range hits {
			out = append(out, h.Document.ID)
		}
		return out
	}

	t.Run("search", func(t *testing.T) {
		res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "database", K: 2})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{docIDs[0], docIDs[2]}, ids(res.Items), "the next-ranked document fills the slot")
	})
	t.Run("related", func(t *testing.T) {
		res, err := engine.Related(context.Background(), RelatedRequest{TenantID: "local", DocumentID: docIDs[0], K: 5})
		require.NoError(t, err)
		assert.Equal(t, []string{docIDs[2]}, ids(res.Items))
	})
}

// legStartBound bounds how long a test leg waits for the other leg to start.
// It only runs out when the legs don't overlap, so it is generous.
const legStartBound = 10 * time.Second

// awaitLeg blocks until the other leg has signaled started, ctx ends, or
// legStartBound runs out.
func awaitLeg(ctx context.Context, started <-chan struct{}, leg string) error {
	select {
	case <-started:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(legStartBound):
		return fmt.Errorf("the %s leg never started while this one ran", leg)
	}
}

func TestEngine_KeywordFailureIsFatalAndStopsTheVectorLeg(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)
	errDisk := errors.New("disk I/O error")

	// The embed is in flight when BM25 fails, and its own timeout is far
	// beyond the test's bound, so only BM25's failure can cancel it.
	embedStarted := make(chan struct{})
	markEmbedStarted := sync.OnceFunc(func() { close(embedStarted) })
	embedEnded := make(chan error, 1)
	emb := embedFunc(func(ctx context.Context, _ []string) ([][]float32, error) {
		markEmbedStarted()
		select {
		case <-ctx.Done():
			embedEnded <- ctx.Err()
		case <-time.After(legStartBound):
			embedEnded <- errors.New("the embed was never canceled")
		}
		return nil, errors.New("embed abandoned")
	})
	broken := &hookedChunks{ChunkStore: chunks, bm25: func(ctx context.Context) error {
		if err := awaitLeg(ctx, embedStarted, "vector"); err != nil {
			return err
		}
		return errDisk
	}}
	engine := New(broken, docs, emb, Config{EmbedTimeout: time.Hour, Log: slog.New(slog.DiscardHandler)})

	_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "attention token", K: 3})
	require.ErrorIs(t, err, errDisk)
	select {
	case embedErr := <-embedEnded:
		assert.ErrorIs(t, embedErr, context.Canceled, "BM25's failure canceled the in-flight embed")
	default:
		t.Fatal("Search returned while the vector leg was still running")
	}
}

func TestEngine_LegsRunConcurrently(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)

	// Each leg signals that it started, then waits for the other. Run one
	// after the other, in either order, the first leg would wait out the
	// bound: BM25 would then fail the search, the vector leg degrade it.
	embedStarted, bm25Started := make(chan struct{}), make(chan struct{})
	markEmbedStarted := sync.OnceFunc(func() { close(embedStarted) })
	markBM25Started := sync.OnceFunc(func() { close(bm25Started) })
	emb := embedFunc(func(ctx context.Context, _ []string) ([][]float32, error) {
		markEmbedStarted()
		if err := awaitLeg(ctx, bm25Started, "BM25"); err != nil {
			return nil, err
		}
		return [][]float32{filledVec(0.3)}, nil
	})
	rendezvous := &hookedChunks{ChunkStore: chunks, bm25: func(ctx context.Context) error {
		markBM25Started()
		return awaitLeg(ctx, embedStarted, "vector")
	}}
	engine := New(rendezvous, docs, emb, Config{EmbedTimeout: time.Hour})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "attention token", K: 3})
	require.NoError(t, err)
	assert.False(t, res.Degraded, "warnings: %v", res.Warnings)
	assert.Positive(t, res.BM25Hits)
	assert.Positive(t, res.VectorHits)
}

func TestEngine_FanoutScalesWithK(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs := sqlitestore.NewDocuments(db)
	exts := sqlitestore.NewExtractions(db)
	chunks := sqlitestore.NewChunks(db, dim)
	ctx := context.Background()
	for i := range 60 {
		d := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/zebra/%d", i),
			ContentType: store.ContentTypeArticle}
		require.NoError(t, docs.Create(ctx, d))
		e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK, FetchedAt: time.Now().UTC()}
		require.NoError(t, exts.Create(ctx, e))
		require.NoError(t, chunks.ReplaceForDocument(ctx, d.ID, e.ID, "", nil,
			[]store.ChunkInput{{Text: fmt.Sprintf("zebra sighting number %d", i), Embedding: filledVec(0.5)}}))
	}
	engine := New(chunks, docs, failingEmbedder(), Config{Log: slog.New(slog.DiscardHandler)})

	res, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: 60})
	require.NoError(t, err)
	assert.Len(t, res.Items, 60, "a K above the 50-chunk floor still gets K documents")
}

func TestEngine_KContract(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)
	engine := New(chunks, docs, &fakeEmbedder{}, Config{DefaultK: 2})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zzqterm"})
	require.NoError(t, err)
	assert.Len(t, res.Items, 2, "K 0 uses the configured default")

	for _, k := range []int{-1, MaxK + 1} {
		_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zzqterm", K: k})
		assert.Error(t, err, "k=%d", k)
	}
}
