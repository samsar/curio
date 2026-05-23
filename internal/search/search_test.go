package search

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store"
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
		require.NoError(t, docs.Upsert(ctx, d))
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
	db := sqlitestore.NewEphemeralDB(t)
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
	db := sqlitestore.NewEphemeralDB(t)
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
	db := sqlitestore.NewEphemeralDB(t)
	docs, chunks, _ := seedCorpus(t, db)
	engine := New(chunks, docs, &fakeEmbedder{}, Config{})

	_, err := engine.Search(context.Background(), Request{TenantID: "local"})
	require.Error(t, err)
}

func TestEngine_PerHitScoresExposed(t *testing.T) {
	db := sqlitestore.NewEphemeralDB(t)
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
