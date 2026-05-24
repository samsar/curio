package indexer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
)

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
	hits, err := chunks.BM25Search(context.Background(), "local", "MVCC", 10)
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
	before, _ := chunks.BM25Search(context.Background(), "local", "real", 10)
	require.NotEmpty(t, before)

	// Empty replaces away.
	require.NoError(t, idx.Index(context.Background(), IndexInput{
		DocumentID: docID, ExtractionID: extID, Markdown: "",
	}))
	after, _ := chunks.BM25Search(context.Background(), "local", "real", 10)
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
	hits, _ := chunks.BM25Search(context.Background(), "local", "content", 10)
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
