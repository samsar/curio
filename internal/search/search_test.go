package search

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
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
	db := sqlitestore.NewEphemeralDB(t)
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
	db := sqlitestore.NewEphemeralDB(t)
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
	db := sqlitestore.NewEphemeralDB(t)
	docs, chunks, _ := seedCorpus(t, db)

	// A document with no chunks: create one without indexing it.
	ctx := context.Background()
	d := &store.Document{TenantID: "local", URL: "https://example.com/pending", ContentType: store.ContentTypeArticle}
	require.NoError(t, docs.Upsert(ctx, d))

	engine := New(chunks, docs, &fakeEmbedder{}, Config{})
	res, err := engine.Related(ctx, RelatedRequest{TenantID: "local", DocumentID: d.ID, K: 5})
	require.NoError(t, err)
	assert.Empty(t, res.Items)
}

func TestEngine_Related_UnknownDocIsNotFound(t *testing.T) {
	db := sqlitestore.NewEphemeralDB(t)
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
