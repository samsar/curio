package indexer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// capturingEmbedder records every text it's asked to embed.
type capturingEmbedder struct {
	dim  int
	seen []string
}

func (c *capturingEmbedder) Dimensions() int { return c.dim }
func (*capturingEmbedder) Model() string     { return "fake" }
func (c *capturingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	c.seen = append(c.seen, texts...)
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, c.dim)
	}
	return out, nil
}

func TestIndexer_DocumentPrefixOnlyOnEmbedInput(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	chunks := sqlitestore.NewChunks(db, dim)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/p")

	emb := &capturingEmbedder{dim: dim}
	idx := New(chunks, emb, Options{ChunkSize: 10, ChunkOverlap: 2, DocumentPrefix: "search_document: "})

	require.NoError(t, idx.Index(context.Background(), IndexInput{
		DocumentID:   docID,
		ExtractionID: extID,
		Title:        "T",
		Markdown:     "Ada Lovelace wrote the first published algorithm.",
	}))

	// Every text sent to the embedder carries the prefix.
	require.NotEmpty(t, emb.seen)
	for _, s := range emb.seen {
		assert.True(t, strings.HasPrefix(s, "search_document: "), "embed input should be prefixed: %q", s)
	}
	// But the STORED chunk text is raw — the prefix must not pollute BM25/snippets.
	hits, err := chunks.BM25Search(context.Background(), "local", "Lovelace algorithm", 10, store.SearchFilters{})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	stored, err := chunks.GetByIDs(context.Background(), []string{hits[0].ChunkID})
	require.NoError(t, err)
	assert.NotContains(t, stored[0].Text, "search_document:")
}

// TestIndexer_DefaultConfigSendsChunksAsTheyAre: under the default
// embedding config, Qwen3-Embedding's, documents take no prefix, so the
// embedder gets each chunk's text exactly.
func TestIndexer_DefaultConfigSendsChunksAsTheyAre(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/p")
	emb := &capturingEmbedder{dim: dim}
	opts := Options{ChunkSize: 10, ChunkOverlap: 2, DocumentPrefix: config.Default().Embedding.DocumentPrefix}
	const md = "Ada Lovelace wrote the first published algorithm.\n\nShe saw that the engine could do more than arithmetic."

	require.NoError(t, New(sqlitestore.NewChunks(db, dim), emb, opts).Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: md,
	}))
	chunks := ChunkText(md, ChunkOptions{SizeTokens: opts.ChunkSize, OverlapTokens: opts.ChunkOverlap})
	want := make([]string, 0, len(chunks))
	for _, c := range chunks {
		want = append(want, c.Text)
	}
	require.Len(t, want, 2)
	assert.Equal(t, want, emb.seen)
}

// batchRecorder records every batch it is asked to embed.
type batchRecorder struct {
	dim     int
	batches [][]string
}

func (r *batchRecorder) Dimensions() int { return r.dim }
func (*batchRecorder) Model() string     { return "fake" }
func (r *batchRecorder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	r.batches = append(r.batches, slices.Clone(texts))
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, r.dim)
	}
	return out, nil
}

// TestIndexer_EmbedChunksSendsWhatIndexSent: re-embedding a document's
// stored chunk texts, in order, sends the embedder the batches indexing
// sent: the same texts, prefixed the same way, split at the same places.
// The drift check relies on it to compare like with like.
func TestIndexer_EmbedChunksSendsWhatIndexSent(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	chunks := sqlitestore.NewChunks(db, dim)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/long")
	opts := Options{ChunkSize: 1, DocumentPrefix: "search_document: ", EmbedBatchSize: 3}
	indexed := &batchRecorder{dim: dim}

	require.NoError(t, New(chunks, indexed, opts).Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: numberedMarkdown(8),
	}))
	require.Len(t, indexed.batches, 3, "several batches")

	reembedded := &batchRecorder{dim: dim}
	_, err := New(chunks, reembedded, opts).EmbedChunks(context.Background(), storedTexts(t, chunks, docID))
	require.NoError(t, err)
	assert.Equal(t, indexed.batches, reembedded.batches)
	assert.True(t, strings.HasPrefix(reembedded.batches[0][0], "search_document: w000"))
}

// storedTexts is the document's stored chunk texts in chunk order.
func storedTexts(t *testing.T, chunks store.ChunkStore, docID string) []string {
	t.Helper()
	ctx := context.Background()
	embs, err := chunks.EmbeddingsForDocument(ctx, docID)
	require.NoError(t, err)
	ids := make([]string, len(embs))
	for i, e := range embs {
		ids[i] = e.ChunkID
	}
	stored, err := chunks.GetByIDs(ctx, ids)
	require.NoError(t, err)
	slices.SortFunc(stored, func(a, b *store.Chunk) int { return a.Ord - b.Ord })
	texts := make([]string, len(stored))
	for i, c := range stored {
		texts[i] = c.Text
	}
	return texts
}

// fakeEmbedder returns a fixed-size vector for every text. The value is
// derived from the text length so different chunks get different vectors.
type fakeEmbedder struct {
	dim   int
	model string
}

func (f *fakeEmbedder) Dimensions() int { return f.dim }
func (f *fakeEmbedder) Model() string {
	if f.model == "" {
		return "fake"
	}
	return f.model
}
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		seed := float32((len(t) % 100)) / 100.0
		for j := range v {
			v[j] = seed
		}
		out[i] = v
	}
	return out, nil
}

func seedDocAndExtraction(t *testing.T, db *sqlitestore.DB, tenant, url string) (docID, extID string) {
	t.Helper()
	ctx := context.Background()
	docs := sqlitestore.NewDocuments(db)
	exts := sqlitestore.NewExtractions(db)

	d := &store.Document{TenantID: tenant, URL: url, ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Create(ctx, d))

	e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK, FetchedAt: time.Now().UTC()}
	require.NoError(t, exts.Create(ctx, e))
	require.NoError(t, docs.SetCurrentExtraction(ctx, d.ID, e.ID))
	return d.ID, e.ID
}

func TestIndexer_HappyPath(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	chunks := sqlitestore.NewChunks(db, dim)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/x")

	idx := New(chunks, &fakeEmbedder{dim: dim}, Options{ChunkSize: 10, ChunkOverlap: 2})

	md := "Postgres uses MVCC for concurrency.\n\nB-trees power range scans efficiently."
	require.NoError(t, idx.Index(context.Background(), IndexInput{
		DocumentID:   docID,
		ExtractionID: extID,
		Title:        "Database Internals",
		Tags:         []string{"db"},
		Markdown:     md,
	}))

	// Now BM25 + vector both work over those chunks.
	hits, err := chunks.BM25Search(context.Background(), "local", "MVCC", 10, store.SearchFilters{})
	require.NoError(t, err)
	require.NotEmpty(t, hits, "MVCC should match the first chunk")
}

func TestIndexer_EmptyMarkdown_ClearsChunks(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	chunks := sqlitestore.NewChunks(db, dim)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/empty")
	idx := New(chunks, &fakeEmbedder{dim: dim}, Options{})

	// First write some content.
	require.NoError(t, idx.Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: "real content here",
	}))
	before, _ := chunks.BM25Search(context.Background(), "local", "real", 10, store.SearchFilters{})
	require.NotEmpty(t, before)

	// Empty replaces away.
	require.NoError(t, idx.Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: "",
	}))
	after, _ := chunks.BM25Search(context.Background(), "local", "real", 10, store.SearchFilters{})
	assert.Empty(t, after, "empty re-index should clear previous chunks")
}

func TestIndexer_Idempotent(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	chunks := sqlitestore.NewChunks(db, dim)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/idem")
	idx := New(chunks, &fakeEmbedder{dim: dim}, Options{})

	md := "the same content twice"
	for range 2 {
		require.NoError(t, idx.Index(context.Background(), IndexInput{
			DocumentID: docID, ExtractionID: extID, Markdown: md,
		}))
	}
	// Same content twice produces the same single-chunk result.
	hits, _ := chunks.BM25Search(context.Background(), "local", "content", 10, store.SearchFilters{})
	assert.Len(t, hits, 1, "re-indexing identical content should still produce one chunk")
}

func TestIndexer_RequiresIDs(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	idx := New(sqlitestore.NewChunks(db, dim), &fakeEmbedder{dim: dim}, Options{})

	err := idx.Index(context.Background(), IndexInput{ExtractionID: "x", Markdown: "y"})
	require.Error(t, err)
	err = idx.Index(context.Background(), IndexInput{DocumentID: "x", Markdown: "y"})
	require.Error(t, err)
}

// indexedEmbedder encodes each chunk's position into its dim-wide vector:
// every text is "wNNN", and component 0 of its vector is NNN. It records
// batch sizes, fails the call numbered failOn (1-based), and runs onCall
// after each call.
type indexedEmbedder struct {
	dim     int
	batches []int
	failOn  int
	onCall  func()
}

func (e *indexedEmbedder) Dimensions() int { return e.dim }
func (*indexedEmbedder) Model() string     { return "fake" }
func (e *indexedEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.batches = append(e.batches, len(texts))
	if e.onCall != nil {
		defer e.onCall()
	}
	if len(e.batches) == e.failOn {
		return nil, errors.New("ollama: HTTP 500")
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		var n int
		if _, err := fmt.Sscanf(text, "w%d", &n); err != nil {
			return nil, err
		}
		out[i] = make([]float32, e.dim)
		out[i][0] = float32(n)
	}
	return out, nil
}

// numberedMarkdown is n one-word paragraphs "w000".."w{n-1}": with a chunk
// size of one word, each becomes its own chunk.
func numberedMarkdown(n int) string {
	paras := make([]string, n)
	for i := range paras {
		paras[i] = fmt.Sprintf("w%03d", i)
	}
	return strings.Join(paras, "\n\n")
}

var oneWordChunks = Options{ChunkSize: 1, ChunkOverlap: 0}

func TestIndexer_EmbedsInOrderedBatches(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	chunks := sqlitestore.NewChunks(db, dim)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/long")
	emb := &indexedEmbedder{dim: dim}

	require.NoError(t, New(chunks, emb, oneWordChunks).Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: numberedMarkdown(100),
	}))
	assert.Equal(t, []int{32, 32, 32, 4}, emb.batches)

	stored, err := chunks.EmbeddingsForDocument(context.Background(), docID)
	require.NoError(t, err)
	require.Len(t, stored, 100)
	for i, e := range stored {
		assert.Equal(t, float32(i), e.Embedding[0], "chunk %d got another chunk's vector", i)
	}
}

func TestIndexer_FailedBatchKeepsPreviousChunks(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	chunks := sqlitestore.NewChunks(db, dim)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/long")
	require.NoError(t, New(chunks, &fakeEmbedder{dim: dim}, Options{}).Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: "legacy content",
	}))

	err := New(chunks, &indexedEmbedder{dim: dim, failOn: 3}, oneWordChunks).Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: numberedMarkdown(100),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "embed chunks 64-95 of 100 (longest chunk 4 bytes)")

	hits, err := chunks.BM25Search(context.Background(), "local", "legacy", 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.NotEmpty(t, hits, "the document's previous chunks are still searchable")
}

// TestIndexer_InputTooLongNamesTheLongestChunk: a batch the embedder
// refuses as too long fails with the batch's range and its longest input's
// size, and stays matchable as embedder.ErrInputTooLong.
func TestIndexer_InputTooLongNamesTheLongestChunk(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/long")
	refuse := embedFunc(func(context.Context, []string) ([][]float32, error) {
		return nil, fmt.Errorf("ollama embed: %w: HTTP 400: the input length exceeds the context length",
			embedder.ErrInputTooLong)
	})
	md := "short\n\n" + strings.Repeat("x", 120) + "\n\nmiddling words"

	err := New(sqlitestore.NewChunks(db, dim), refuse, Options{ChunkSize: 1, DocumentPrefix: "doc: "}).
		Index(context.Background(), IndexInput{DocumentID: docID, ExtractionID: extID, Markdown: md})
	require.ErrorIs(t, err, embedder.ErrInputTooLong)
	assert.Contains(t, err.Error(), "embed chunks 0-3 of 4 (longest chunk 125 bytes)",
		"the size counts the prefix: it is what the embedder was sent")
}

// embedFunc adapts a function to embedder.Embedder.
type embedFunc func(ctx context.Context, texts []string) ([][]float32, error)

func (f embedFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}
func (embedFunc) Dimensions() int { return 0 }
func (embedFunc) Model() string   { return "fake" }

func TestIndexer_CanceledBetweenBatchesWritesNothing(t *testing.T) {
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	chunks := sqlitestore.NewChunks(db, dim)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/long")
	require.NoError(t, New(chunks, &fakeEmbedder{dim: dim}, Options{}).Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: "legacy content",
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emb := &indexedEmbedder{dim: dim, onCall: cancel}
	err := New(chunks, emb, oneWordChunks).Index(ctx, IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: numberedMarkdown(100),
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []int{32}, emb.batches, "no batch is sent after cancellation")

	stored, err := chunks.EmbeddingsForDocument(context.Background(), docID)
	require.NoError(t, err)
	assert.Len(t, stored, 1, "the previous single chunk is untouched")
}
