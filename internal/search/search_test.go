package search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// fakeEmbedder returns canned vectors keyed on input text, and a dim-wide
// vector far from every seeded chunk for any other text. Tests control
// which chunk the vector retriever ranks first by matching the query's
// embedding to the seeded chunks' embeddings.
type fakeEmbedder struct {
	dim    int
	byText map[string][]float32
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := f.byText[t]
		if !ok {
			// Default vector for any unknown text — far from anything.
			v = filledVec(f.dim, 0.999)
		}
		out[i] = v
	}
	return out, nil
}

func TestEngine_QueryPrefixApplied(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docs, chunks, docIDs := seedCorpus(t, db)

	// The query term matches nothing in BM25, so ranking is driven purely by
	// the vector path. The embedder only returns the postgres chunk's vector
	// (0.10, the closest) for the PREFIXED query; without the prefix it'd get
	// the default far vector and postgres would rank last, not first.
	emb := &fakeEmbedder{dim: dim, byText: map[string][]float32{
		"search_query: zzqterm": filledVec(dim, 0.10),
	}}
	eng := New(chunks, docs, emb, Config{QueryPrefix: "search_query: "})

	res, err := eng.Search(context.Background(), Request{TenantID: "local", Query: "zzqterm", K: 3})
	require.NoError(t, err)
	require.NotEmpty(t, res.Items)
	assert.Equal(t, docIDs[0], res.Items[0].Document.ID, "prefixed query vector should rank the postgres doc first")
}

// TestEngine_DefaultQueryPrefix: under the default embedding config a query
// is embedded as Qwen3-Embedding's instruction immediately followed by the
// query, with no separator.
func TestEngine_DefaultQueryPrefix(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)
	var seen []string
	emb := embedFunc(func(_ context.Context, texts []string) ([][]float32, error) {
		seen = append(seen, texts...)
		return [][]float32{filledVec(dim, 0.10)}, nil
	})
	eng := New(chunks, docs, emb, Config{QueryPrefix: config.Default().Embedding.QueryPrefix})

	_, err := eng.Search(context.Background(), Request{TenantID: "local", Query: "mvcc concurrency", K: 3})
	require.NoError(t, err)
	assert.Equal(t, []string{"Instruct: Given a web search query, retrieve relevant passages that answer the query" +
		"\nQuery:mvcc concurrency"}, seen)
}

// filledVec is a dim-wide vector with v in every component.
func filledVec(dim int, v float32) []float32 {
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
	dim := sqlitetest.Width(t, db)
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
			[]store.ChunkInput{{Text: c.text, Embedding: filledVec(dim, c.vec)}}))
		docIDs = append(docIDs, d.ID)
	}
	return
}

func TestEngine_HybridSearch(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)

	emb := &fakeEmbedder{dim: dim, byText: map[string][]float32{
		"mvcc concurrency": filledVec(dim, 0.10), // closest to postgres chunk
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
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)

	emb := &fakeEmbedder{dim: dim, byText: nil} // returns the default far-away vector
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
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)
	engine := New(chunks, docs, &fakeEmbedder{dim: dim}, Config{})

	_, err := engine.Search(context.Background(), Request{TenantID: "local"})
	require.Error(t, err)
}

func TestEngine_PerHitScoresExposed(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)
	emb := &fakeEmbedder{dim: dim, byText: map[string][]float32{"mvcc": filledVec(dim, 0.10)}}
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
	dim := sqlitetest.Width(t, db)
	docs, chunks, docIDs := seedCorpus(t, db)

	// Related needs no embedder — it reads stored vectors. The fake is
	// only here to satisfy the constructor.
	engine := New(chunks, docs, &fakeEmbedder{dim: dim}, Config{})

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
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)

	// A document with no chunks: create one without indexing it.
	ctx := context.Background()
	d := &store.Document{TenantID: "local", URL: "https://example.com/pending", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Create(ctx, d))

	engine := New(chunks, docs, &fakeEmbedder{dim: dim}, Config{})
	res, err := engine.Related(ctx, RelatedRequest{TenantID: "local", DocumentID: d.ID, K: 5})
	require.NoError(t, err)
	assert.Empty(t, res.Items)
}

func TestEngine_Related_UnknownDocIsNotFound(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)

	engine := New(chunks, docs, &fakeEmbedder{dim: dim}, Config{})
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

// TestEngine_QueryTooLongDegrades: a query longer than the embedding
// model's context can't be embedded, so the search returns its keyword
// results marked degraded, with a warning that says why.
func TestEngine_QueryTooLongDegrades(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)
	tooLong := embedFunc(func(context.Context, []string) ([][]float32, error) {
		return nil, fmt.Errorf("ollama embed: %w: HTTP 400: {\"error\":\"the input length exceeds the context length\"}",
			embedder.ErrInputTooLong)
	})
	engine := New(chunks, docs, tooLong, Config{Log: slog.New(slog.DiscardHandler)})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "attention token", K: 3})
	require.NoError(t, err)
	assert.True(t, res.Degraded)
	require.NotEmpty(t, res.Items)
	assert.Contains(t, res.Items[0].Document.URL, "llm")
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "input longer than the embedding model's context")
	assert.Contains(t, res.Warnings[0], "keyword-only")
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
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)
	var logs bytes.Buffer
	engine := New(brokenChunkLookup{chunks}, docs, &fakeEmbedder{dim: dim}, Config{Log: slog.New(slog.NewTextHandler(&logs, nil))})

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
	dim := sqlitetest.Width(t, db)
	docs, chunks, docIDs := seedCorpus(t, db) // postgres, btree, llm
	gone := vanishingDocs{docs, map[string]bool{docIDs[1]: true}}
	engine := New(chunks, gone, &fakeEmbedder{dim: dim, byText: map[string][]float32{
		"database": filledVec(dim, 0.20), // nearest the btree chunk
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
	dim := sqlitetest.Width(t, db)
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
		return [][]float32{filledVec(dim, 0.3)}, nil
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

// seedMatching puts n documents into the DB, document i with one chunk of
// text(i) embedded as filledVec(vec(i)), and returns their IDs in the order
// written.
func seedMatching(t *testing.T, db *sqlitestore.DB, n int, text func(int) string, vec func(int) float32) (
	docs *sqlitestore.Documents, chunks *sqlitestore.Chunks, ids []string) {
	t.Helper()
	ctx := context.Background()
	dim := sqlitetest.Width(t, db)
	docs = sqlitestore.NewDocuments(db)
	exts := sqlitestore.NewExtractions(db)
	chunks = sqlitestore.NewChunks(db, dim)
	for i := range n {
		d := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/zebra/%d", i),
			ContentType: store.ContentTypeArticle}
		require.NoError(t, docs.Create(ctx, d))
		e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK, FetchedAt: time.Now().UTC()}
		require.NoError(t, exts.Create(ctx, e))
		require.NoError(t, chunks.ReplaceForDocument(ctx, d.ID, e.ID, "", nil,
			[]store.ChunkInput{{Text: text(i), Embedding: filledVec(dim, vec(i))}}))
		ids = append(ids, d.ID)
	}
	return docs, chunks, ids
}

// sightings are n documents about zebras, each a sighting of its own
// number, their vectors spread so the vector leg ranks them in a strict
// order of its own.
func sightings(t *testing.T, db *sqlitestore.DB, n int) (*sqlitestore.Documents, *sqlitestore.Chunks, []string) {
	t.Helper()
	return seedMatching(t, db, n, func(i int) string { return fmt.Sprintf("zebra sighting number %d", i) },
		func(i int) float32 { return 0.1 + float32(i)*0.005 })
}

// zebraEmbedder embeds every query as the vector the sightings are spread
// towards.
func zebraEmbedder(dim int) Embedder {
	return embedFunc(func(context.Context, []string) ([][]float32, error) {
		return [][]float32{filledVec(dim, 0.6)}, nil
	})
}

func hitIDs(hits []Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Document.ID)
	}
	return out
}

// TestEngine_PagesTileTheRanking: every window of a query is a slice of
// one ranking, so its pages of 10 are the k=100 ranking's documents, in
// its order, each once; each page says how many documents the ranking
// holds and that more matched.
func TestEngine_PagesTileTheRanking(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := sightings(t, db, 110)
	engine := New(chunks, docs, zebraEmbedder(sqlitetest.Width(t, db)), Config{})
	ctx := context.Background()

	whole, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: 100})
	require.NoError(t, err)
	require.Len(t, whole.Items, 100)
	var paged []Hit
	for offset := 0; offset <= 90; offset += 10 {
		page, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: 10, Offset: offset})
		require.NoError(t, err)
		assert.Len(t, page.Items, 10, "offset %d", offset)
		assert.Equal(t, 100, page.Total, "offset %d", offset)
		assert.True(t, page.Capped, "offset %d: 110 documents matched", offset)
		paged = append(paged, page.Items...)
	}
	assert.Equal(t, hitIDs(whole.Items), hitIDs(paged))
	for i := range paged {
		assert.Equal(t, whole.Items[i].Score, paged[i].Score, "rank %d", i)
	}
}

// TestEngine_KOnlyIsTheFirstPage: a request without an offset ranks the
// same pool as a page does, so its results are the ranking's first K,
// whatever K: the CLI, MCP and the dashboard's first page agree.
func TestEngine_KOnlyIsTheFirstPage(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := sightings(t, db, 110)
	engine := New(chunks, docs, zebraEmbedder(sqlitetest.Width(t, db)), Config{})
	ctx := context.Background()

	ten, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: 10})
	require.NoError(t, err)
	hundred, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: 100})
	require.NoError(t, err)
	assert.Equal(t, hundred.Items[:10], ten.Items)
}

// TestEngine_SmallPool: a pool of fewer documents than the cap is ranked
// whole: its total is exact and not capped, a window that runs past its end
// is cut short, and one that starts past it is empty, not an error.
func TestEngine_SmallPool(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := sightings(t, db, 37)
	engine := New(chunks, docs, zebraEmbedder(sqlitetest.Width(t, db)), Config{})
	for _, tc := range []struct{ offset, items int }{{0, 10}, {30, 7}, {37, 0}, {40, 0}} {
		res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zebra", Offset: tc.offset})
		require.NoError(t, err, "offset %d", tc.offset)
		assert.Len(t, res.Items, tc.items, "offset %d", tc.offset)
		assert.Equal(t, 37, res.Total, "offset %d", tc.offset)
		assert.False(t, res.Capped, "offset %d", tc.offset)
	}

	degraded := New(chunks, docs, failingEmbedder(), Config{Log: slog.New(slog.DiscardHandler)})
	res, err := degraded.Search(context.Background(), Request{TenantID: "local", Query: "zebra", Offset: 30})
	require.NoError(t, err)
	assert.True(t, res.Degraded)
	assert.Len(t, res.Items, 7)
	assert.Equal(t, 37, res.Total, "keyword results are counted too")
	assert.False(t, res.Capped)
}

// countingChunks counts the chunk store's reads and records the limit each
// retriever was asked for. Snippets calls onSnippets and fails with
// snippetsErr, each when set.
type countingChunks struct {
	store.ChunkStore
	mu                  sync.Mutex
	bm25Limit, vecLimit int
	getByIDs, snippets  int
	snippetIDs          []string
	snippetsErr         error
	onSnippets          func()
}

func (c *countingChunks) BM25Search(ctx context.Context, tenantID, query string, limit int, filters store.SearchFilters) ([]store.ChunkHit, error) {
	c.mu.Lock()
	c.bm25Limit = limit
	c.mu.Unlock()
	return c.ChunkStore.BM25Search(ctx, tenantID, query, limit, filters)
}

func (c *countingChunks) VectorSearch(ctx context.Context, tenantID string, embedding []float32, limit int, filters store.SearchFilters) ([]store.ChunkHit, error) {
	c.mu.Lock()
	c.vecLimit = limit
	c.mu.Unlock()
	return c.ChunkStore.VectorSearch(ctx, tenantID, embedding, limit, filters)
}

func (c *countingChunks) GetByIDs(ctx context.Context, ids []string) ([]*store.Chunk, error) {
	c.mu.Lock()
	c.getByIDs++
	c.mu.Unlock()
	return c.ChunkStore.GetByIDs(ctx, ids)
}

func (c *countingChunks) Snippets(ctx context.Context, query string, chunkIDs []string) (map[string]string, error) {
	c.mu.Lock()
	c.snippets++
	c.snippetIDs = append(c.snippetIDs, chunkIDs...)
	c.mu.Unlock()
	if c.onSnippets != nil {
		c.onSnippets()
	}
	if c.snippetsErr != nil {
		return nil, c.snippetsErr
	}
	return c.ChunkStore.Snippets(ctx, query, chunkIDs)
}

// countingDocs counts the document reads.
type countingDocs struct {
	store.DocumentStore
	getByID atomic.Int32
}

func (c *countingDocs) GetByID(ctx context.Context, id string) (*store.Document, error) {
	c.getByID.Add(1)
	return c.DocumentStore.GetByID(ctx, id)
}

// TestEngine_HydratesOnlyTheWindow: a page deep in the ranking reads its
// own documents and chunks, not those of the pages before it.
func TestEngine_HydratesOnlyTheWindow(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := sightings(t, db, 110)
	cd, cc := &countingDocs{DocumentStore: docs}, &countingChunks{ChunkStore: chunks}
	engine := New(cc, cd, zebraEmbedder(sqlitetest.Width(t, db)), Config{})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zebra", K: 10, Offset: 60})
	require.NoError(t, err)
	require.Len(t, res.Items, 10)
	assert.EqualValues(t, 10, cd.getByID.Load(), "one read per document of the window")
	assert.Equal(t, 10, cc.getByIDs, "one chunk read per document of the window")
	assert.Equal(t, 1, cc.snippets, "one snippet read for the window")
}

// TestEngine_FixedFanout: each retriever reads the pool for the cap,
// whatever the window asked for; a window as large as the cap still gets
// every document it asks for.
func TestEngine_FixedFanout(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := sightings(t, db, 60)
	cc := &countingChunks{ChunkStore: chunks}
	engine := New(cc, docs, zebraEmbedder(sqlitetest.Width(t, db)), Config{})
	for _, req := range []Request{{K: 1}, {K: 10}, {K: 100}, {K: 1, Offset: 50}, {K: 10, Offset: 50}} {
		req.TenantID, req.Query = "local", "zebra"
		_, err := engine.Search(context.Background(), req)
		require.NoError(t, err, "k %d, offset %d", req.K, req.Offset)
		assert.Equal(t, 8*store.MaxSearchK, cc.bm25Limit, "k %d, offset %d", req.K, req.Offset)
		assert.Equal(t, 8*store.MaxSearchK, cc.vecLimit, "k %d, offset %d", req.K, req.Offset)
	}

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zebra", K: 60})
	require.NoError(t, err)
	assert.Len(t, res.Items, 60)
}

// TestEngine_TiesRankByID: documents whose fused scores are equal, from
// the same text ranked in opposite orders by the two retrievers, are
// ordered by ID, on every call, so each shows on exactly one page.
func TestEngine_TiesRankByID(t *testing.T) {
	db := sqlitetest.NewDB(t)
	const n = 30
	// BM25 ties every copy and ranks them as written; the vector leg ranks
	// them the other way, so copies i and n-1-i fuse to the same score.
	docs, chunks, ids := seedMatching(t, db, n, func(int) string { return "the same syndicated zebra post" },
		func(i int) float32 { return 0.1 + float32(i)*0.01 })
	engine := New(chunks, docs, zebraEmbedder(sqlitetest.Width(t, db)), Config{})
	page := func(offset int) []Hit {
		res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zebra", K: 10, Offset: offset})
		require.NoError(t, err)
		return res.Items
	}

	var first []Hit
	for offset := 0; offset < n; offset += 10 {
		first = append(first, page(offset)...)
	}
	assert.ElementsMatch(t, ids, hitIDs(first), "every document on exactly one page")
	ties := 0
	for i := 1; i < len(first); i++ {
		a, b := first[i-1], first[i]
		require.GreaterOrEqual(t, a.Score, b.Score, "rank %d", i)
		if a.Score == b.Score {
			ties++
			assert.Less(t, a.Document.ID, b.Document.ID, "a tie is ordered by ID, rank %d", i)
		}
	}
	assert.Equal(t, n/2, ties, "the copies tie in pairs")

	var again []Hit
	for offset := 0; offset < n; offset += 10 {
		again = append(again, page(offset)...)
	}
	assert.Equal(t, hitIDs(first), hitIDs(again), "the same ranking on every call")
}

// TestEngine_OffsetContract: an offset is checked against the K the
// request gets, its default included, without overflowing.
func TestEngine_OffsetContract(t *testing.T) {
	db := sqlitetest.NewDB(t)
	docs, chunks, _ := seedCorpus(t, db)
	engine := New(chunks, docs, &fakeEmbedder{dim: sqlitetest.Width(t, db)}, Config{DefaultK: 10})
	for _, tc := range []struct{ k, offset int }{{10, -1}, {10, 91}, {0, 91}, {100, 1}, {10, math.MaxInt}, {0, math.MaxInt}} {
		_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "database", K: tc.k, Offset: tc.offset})
		assert.Error(t, err, "k %d, offset %d", tc.k, tc.offset)
	}
	for _, tc := range []struct{ k, offset int }{{10, 90}, {0, 90}, {100, 0}, {1, 99}} {
		_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "database", K: tc.k, Offset: tc.offset})
		assert.NoError(t, err, "k %d, offset %d", tc.k, tc.offset)
	}
	assert.Equal(t, 10, engine.DefaultK())
	assert.Equal(t, 10, New(chunks, docs, &fakeEmbedder{}, Config{}).DefaultK(), "the default's default")
}

// TestEngine_SnippetsForTheWindow: one read makes the snippets of the
// window's chunks that BM25 returned, their terms marked; a chunk only the
// vector leg returned has none, and isn't asked about.
func TestEngine_SnippetsForTheWindow(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docs, chunks, ids := seedCorpus(t, db) // postgres, btree, llm
	cc := &countingChunks{ChunkStore: chunks}
	// "mvcc" matches the postgres chunk's words; the vector leg returns all
	// three chunks.
	engine := New(cc, docs, &fakeEmbedder{dim: dim, byText: map[string][]float32{"mvcc": filledVec(dim, 0.10)}}, Config{})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "mvcc", K: 3})
	require.NoError(t, err)
	require.Len(t, res.Items, 3)
	assert.Equal(t, 1, cc.snippets)
	byDoc := map[string]ChunkMatch{}
	for _, h := range res.Items {
		require.Len(t, h.Chunks, 1)
		byDoc[h.Document.ID] = h.Chunks[0]
	}
	pg := byDoc[ids[0]]
	require.NotNil(t, pg.BM25Score)
	assert.Equal(t, "PostgreSQL uses <em>MVCC</em> for concurrency control between transactions.", pg.Snippet)
	assert.Equal(t, []string{pg.ChunkID}, cc.snippetIDs, "only BM25's chunks are asked about")
	for _, id := range ids[1:] {
		m := byDoc[id]
		assert.Nil(t, m.BM25Score)
		assert.NotNil(t, m.VectorScore)
		assert.Empty(t, m.Snippet, "a vector-only match has no snippet")
	}

	cc.snippets = 0
	res, err = engine.Search(context.Background(), Request{TenantID: "local", Query: "zzqterm", K: 3})
	require.NoError(t, err)
	require.NotEmpty(t, res.Items)
	assert.Zero(t, cc.snippets, "a window BM25 found nothing in reads no snippets")
}

// TestEngine_SnippetFailureKeepsHitsAndIsLogged: the snippets only
// decorate the matches, so a failed read leaves them without, logged once;
// the caller's context ending is an error.
func TestEngine_SnippetFailureKeepsHitsAndIsLogged(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)
	var logs bytes.Buffer
	cc := &countingChunks{ChunkStore: chunks, snippetsErr: errors.New("database is locked")}
	engine := New(cc, docs, &fakeEmbedder{dim: dim}, Config{Log: slog.New(slog.NewTextHandler(&logs, nil))})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "attention token", K: 3})
	require.NoError(t, err)
	require.NotEmpty(t, res.Items)
	require.NotEmpty(t, res.Items[0].Chunks, "the hit keeps its matches")
	assert.NotNil(t, res.Items[0].Chunks[0].BM25Score)
	assert.Empty(t, res.Items[0].Chunks[0].Snippet)
	assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"))
	assert.Contains(t, logs.String(), "database is locked")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cc.onSnippets = cancel
	cc.snippetsErr = context.Canceled
	res, err = engine.Search(ctx, Request{TenantID: "local", Query: "attention token", K: 3})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, res)
}

func TestEngine_KContract(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docs, chunks, _ := seedCorpus(t, db)
	engine := New(chunks, docs, &fakeEmbedder{dim: dim}, Config{DefaultK: 2})

	res, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zzqterm"})
	require.NoError(t, err)
	assert.Len(t, res.Items, 2, "K 0 uses the configured default")

	for _, k := range []int{-1, store.MaxSearchK + 1} {
		_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zzqterm", K: k})
		assert.Error(t, err, "k=%d", k)
	}
}
