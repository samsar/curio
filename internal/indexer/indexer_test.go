package indexer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
)

// capturingEmbedder records every text it's asked to embed.
type capturingEmbedder struct {
	dim  int
	seen []string
}

func (c *capturingEmbedder) Dimensions() int { return c.dim }
func (c *capturingEmbedder) Model() string   { return "fake" }
func (c *capturingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	c.seen = append(c.seen, texts...)
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, c.dim)
	}
	return out, nil
}

func TestIndexer_DocumentPrefixOnlyOnEmbedInput(t *testing.T) {
	db := sqlitestore.NewEphemeralDB(t)
	chunks := sqlitestore.NewChunks(db, 768)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/p")

	emb := &capturingEmbedder{dim: 768}
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
	require.NoError(t, docs.Upsert(ctx, d))

	e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK, FetchedAt: time.Now().UTC()}
	require.NoError(t, exts.Create(ctx, e))
	require.NoError(t, docs.SetCurrentExtraction(ctx, d.ID, e.ID))
	return d.ID, e.ID
}

func TestIndexer_HappyPath(t *testing.T) {
	db := sqlitestore.NewEphemeralDB(t)
	chunks := sqlitestore.NewChunks(db, 768)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/x")

	idx := New(chunks, &fakeEmbedder{dim: 768}, Options{ChunkSize: 10, ChunkOverlap: 2})

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
	db := sqlitestore.NewEphemeralDB(t)
	chunks := sqlitestore.NewChunks(db, 768)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/empty")
	idx := New(chunks, &fakeEmbedder{dim: 768}, Options{})

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
	db := sqlitestore.NewEphemeralDB(t)
	chunks := sqlitestore.NewChunks(db, 768)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/idem")
	idx := New(chunks, &fakeEmbedder{dim: 768}, Options{})

	md := "the same content twice"
	for i := 0; i < 2; i++ {
		require.NoError(t, idx.Index(context.Background(), IndexInput{
			DocumentID: docID, ExtractionID: extID, Markdown: md,
		}))
	}
	// Same content twice produces the same single-chunk result.
	hits, _ := chunks.BM25Search(context.Background(), "local", "content", 10, store.SearchFilters{})
	assert.Len(t, hits, 1, "re-indexing identical content should still produce one chunk")
}

func TestIndexer_RequiresIDs(t *testing.T) {
	idx := New(sqlitestore.NewChunks(sqlitestore.NewEphemeralDB(t), 768),
		&fakeEmbedder{dim: 768}, Options{})

	err := idx.Index(context.Background(), IndexInput{ExtractionID: "x", Markdown: "y"})
	require.Error(t, err)
	err = idx.Index(context.Background(), IndexInput{DocumentID: "x", Markdown: "y"})
	require.Error(t, err)
}

// indexedEmbedder encodes each chunk's position into its vector: every text is
// "wNNN", and component 0 of its vector is NNN. It records batch sizes, fails
// the call numbered failOn (1-based), and runs onCall after each call.
type indexedEmbedder struct {
	batches []int
	failOn  int
	onCall  func()
}

func (e *indexedEmbedder) Dimensions() int { return 768 }
func (e *indexedEmbedder) Model() string   { return "fake" }
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
		out[i] = make([]float32, 768)
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
	db := sqlitestore.NewEphemeralDB(t)
	chunks := sqlitestore.NewChunks(db, 768)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/long")
	emb := &indexedEmbedder{}

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
	db := sqlitestore.NewEphemeralDB(t)
	chunks := sqlitestore.NewChunks(db, 768)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/long")
	require.NoError(t, New(chunks, &fakeEmbedder{dim: 768}, Options{}).Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: "legacy content",
	}))

	err := New(chunks, &indexedEmbedder{failOn: 3}, oneWordChunks).Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: numberedMarkdown(100),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "embed chunks 64-95 of 100")

	hits, err := chunks.BM25Search(context.Background(), "local", "legacy", 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.NotEmpty(t, hits, "the document's previous chunks are still searchable")
}

func TestIndexer_CanceledBetweenBatchesWritesNothing(t *testing.T) {
	db := sqlitestore.NewEphemeralDB(t)
	chunks := sqlitestore.NewChunks(db, 768)
	docID, extID := seedDocAndExtraction(t, db, "local", "https://example.com/long")
	require.NoError(t, New(chunks, &fakeEmbedder{dim: 768}, Options{}).Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: "legacy content",
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emb := &indexedEmbedder{onCall: cancel}
	err := New(chunks, emb, oneWordChunks).Index(ctx, IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: numberedMarkdown(100),
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []int{32}, emb.batches, "no batch is sent after cancellation")

	stored, err := chunks.EmbeddingsForDocument(context.Background(), docID)
	require.NoError(t, err)
	assert.Len(t, stored, 1, "the previous single chunk is untouched")
}
