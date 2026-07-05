package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

const vecDim = 768

// seedDocs inserts N documents owned by the given tenant and returns their
// IDs. Used by chunk-store tests that need parent rows.
func seedDocs(t *testing.T, db *DB, tenantID string, urls ...string) []string {
	t.Helper()
	ctx := context.Background()
	docs := NewDocuments(db)
	exts := NewExtractions(db)
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		d := &store.Document{
			TenantID:    tenantID,
			URL:         u,
			ContentType: store.ContentTypeArticle,
		}
		require.NoError(t, docs.Upsert(ctx, d))

		// Each test wants a current extraction to attach chunks to.
		e := &store.DocumentExtraction{
			DocumentID: d.ID,
			Fetcher:    "test",
			Status:     store.ExtractionStatusOK,
			FetchedAt:  time.Now().UTC(),
		}
		require.NoError(t, exts.Create(ctx, e))
		require.NoError(t, docs.SetCurrentExtraction(ctx, d.ID, e.ID))
		out = append(out, d.ID)
		_ = e
	}
	return out
}

// fillVec returns a vector of length vecDim with the same value in every slot.
// Two such vectors have a known L2 distance (= |a-b| * sqrt(dim)).
func fillVec(v float32) []float32 {
	out := make([]float32, vecDim)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestChunks_ReplaceForDocument_FullCycle(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	ch := NewChunks(db, vecDim)

	ids := seedDocs(t, db, "local", "https://example.com/postgres-internals")
	docID := ids[0]
	ext := latestExtractionID(t, db, docID)

	inputs := []store.ChunkInput{
		{Text: "PostgreSQL uses MVCC for concurrency control.", Embedding: fillVec(0.10), TokenCount: 8},
		{Text: "B-tree indexes accelerate range scans in databases.", Embedding: fillVec(0.20), TokenCount: 9},
	}
	require.NoError(t, ch.ReplaceForDocument(ctx, docID, ext, "Postgres Internals", []string{"db"}, inputs))

	// BM25 picks the MVCC chunk for an MVCC-flavored query.
	hits, err := ch.BM25Search(ctx, "local", "mvcc concurrency", 10, store.SearchFilters{})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	first, err := ch.GetByIDs(ctx, []string{hits[0].ChunkID})
	require.NoError(t, err)
	assert.Contains(t, first[0].Text, "MVCC")
	assert.NotEmpty(t, hits[0].Snippet)

	// Vector search: closest vector to 0.10 is the first chunk.
	vHits, err := ch.VectorSearch(ctx, "local", fillVec(0.10), 10, store.SearchFilters{})
	require.NoError(t, err)
	require.NotEmpty(t, vHits)
	closest, err := ch.GetByIDs(ctx, []string{vHits[0].ChunkID})
	require.NoError(t, err)
	assert.Contains(t, closest[0].Text, "MVCC")
}

func TestChunks_ReplaceForDocument_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	ch := NewChunks(db, vecDim)

	ids := seedDocs(t, db, "local", "https://example.com/idem")
	docID := ids[0]
	ext := latestExtractionID(t, db, docID)

	first := []store.ChunkInput{
		{Text: "alpha beta", Embedding: fillVec(0.1)},
		{Text: "gamma delta", Embedding: fillVec(0.2)},
	}
	require.NoError(t, ch.ReplaceForDocument(ctx, docID, ext, "T", nil, first))

	// Replace with fewer chunks; old ones must be gone.
	second := []store.ChunkInput{
		{Text: "replaced content only", Embedding: fillVec(0.3)},
	}
	require.NoError(t, ch.ReplaceForDocument(ctx, docID, ext, "T", nil, second))

	hits, err := ch.BM25Search(ctx, "local", "alpha", 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.Empty(t, hits, "old chunks should be gone after Replace")

	hits, err = ch.BM25Search(ctx, "local", "replaced", 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.Len(t, hits, 1)
}

func TestChunks_BM25_FiltersByTenant(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	ch := NewChunks(db, vecDim)

	idsA := seedDocs(t, db, "tenant_a", "https://a.example.com/x")
	idsB := seedDocs(t, db, "tenant_b", "https://b.example.com/x")

	require.NoError(t, ch.ReplaceForDocument(ctx, idsA[0], latestExtractionID(t, db, idsA[0]),
		"", nil, []store.ChunkInput{{Text: "rocket science", Embedding: fillVec(0.1)}}))
	require.NoError(t, ch.ReplaceForDocument(ctx, idsB[0], latestExtractionID(t, db, idsB[0]),
		"", nil, []store.ChunkInput{{Text: "rocket science", Embedding: fillVec(0.1)}}))

	hitsA, _ := ch.BM25Search(ctx, "tenant_a", "rocket", 10, store.SearchFilters{})
	hitsB, _ := ch.BM25Search(ctx, "tenant_b", "rocket", 10, store.SearchFilters{})

	require.Len(t, hitsA, 1)
	require.Len(t, hitsB, 1)
	assert.NotEqual(t, hitsA[0].ChunkID, hitsB[0].ChunkID, "cross-tenant chunks must not collide")
}

func TestChunks_Vector_FiltersByTenant(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	ch := NewChunks(db, vecDim)

	idsA := seedDocs(t, db, "tenant_a", "https://a.example.com/v")
	idsB := seedDocs(t, db, "tenant_b", "https://b.example.com/v")
	require.NoError(t, ch.ReplaceForDocument(ctx, idsA[0], latestExtractionID(t, db, idsA[0]),
		"", nil, []store.ChunkInput{{Text: "x", Embedding: fillVec(0.5)}}))
	require.NoError(t, ch.ReplaceForDocument(ctx, idsB[0], latestExtractionID(t, db, idsB[0]),
		"", nil, []store.ChunkInput{{Text: "y", Embedding: fillVec(0.5)}}))

	hitsA, err := ch.VectorSearch(ctx, "tenant_a", fillVec(0.5), 10, store.SearchFilters{})
	require.NoError(t, err)
	require.Len(t, hitsA, 1)
}

func TestChunks_DimensionMismatch(t *testing.T) {
	ch := NewChunks(NewEphemeralDB(t), vecDim)
	err := ch.ReplaceForDocument(context.Background(), "doc", "ext", "", nil,
		[]store.ChunkInput{{Text: "short", Embedding: []float32{0.1, 0.2, 0.3}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "embedding length 3 != configured dim 768")
}

// latestExtractionID returns the most recent extraction id for a document.
// Used by tests that need to attach chunks; seedDocs already creates one.
func latestExtractionID(t *testing.T, db *DB, documentID string) string {
	t.Helper()
	var id string
	err := db.QueryRow(`SELECT id FROM document_extractions WHERE document_id = ?
		ORDER BY fetched_at DESC LIMIT 1`, documentID).Scan(&id)
	require.NoError(t, err)
	return id
}

// TestChunks_SearchFilters verifies content_type / host / source scoping on
// both retrievers. Three docs all match the term "kubernetes"; filters must
// narrow which documents come back.
func TestChunks_SearchFilters(t *testing.T) {
	ctx := context.Background()
	db := NewEphemeralDB(t)
	docsStore := NewDocuments(db)
	exts := NewExtractions(db)
	bms := NewBookmarks(db)
	ch := NewChunks(db, vecDim)

	seeds := []struct {
		url, ctype, source string
		vec                float32
	}{
		{"https://example.com/k8s-guide", store.ContentTypeArticle, store.SourceChrome, 0.10},
		{"https://github.com/foo/bar", store.ContentTypeRepo, store.SourceManual, 0.20},
		{"https://example.com/k8s.pdf", store.ContentTypePDF, store.SourceChrome, 0.30},
	}
	byURL := map[string]string{}
	for _, s := range seeds {
		d := &store.Document{TenantID: "local", URL: s.url, ContentType: s.ctype}
		require.NoError(t, docsStore.Upsert(ctx, d))
		e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK, FetchedAt: time.Now().UTC()}
		require.NoError(t, exts.Create(ctx, e))
		require.NoError(t, docsStore.SetCurrentExtraction(ctx, d.ID, e.ID))
		require.NoError(t, ch.ReplaceForDocument(ctx, d.ID, e.ID, "", nil,
			[]store.ChunkInput{{Text: "kubernetes deployment guide", Embedding: fillVec(s.vec), TokenCount: 3}}))
		docID := d.ID
		require.NoError(t, bms.Create(ctx, &store.Bookmark{
			TenantID: "local", DocumentID: &docID, URL: s.url, Source: s.source, SavedAt: time.Now().UTC(),
		}))
		byURL[s.url] = d.ID
	}

	docSet := func(hits []store.ChunkHit) map[string]bool {
		m := map[string]bool{}
		for _, h := range hits {
			m[h.DocumentID] = true
		}
		return m
	}

	cases := []struct {
		name   string
		filter store.SearchFilters
		want   []string // expected URLs
	}{
		{"none", store.SearchFilters{}, []string{"https://example.com/k8s-guide", "https://github.com/foo/bar", "https://example.com/k8s.pdf"}},
		{"ctype pdf", store.SearchFilters{ContentType: []string{store.ContentTypePDF}}, []string{"https://example.com/k8s.pdf"}},
		{"ctype article+repo", store.SearchFilters{ContentType: []string{store.ContentTypeArticle, store.ContentTypeRepo}}, []string{"https://example.com/k8s-guide", "https://github.com/foo/bar"}},
		{"source manual", store.SearchFilters{Source: []string{store.SourceManual}}, []string{"https://github.com/foo/bar"}},
		{"host github", store.SearchFilters{Host: []string{"github.com"}}, []string{"https://github.com/foo/bar"}},
		{"host example", store.SearchFilters{Host: []string{"example.com"}}, []string{"https://example.com/k8s-guide", "https://example.com/k8s.pdf"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bm, err := ch.BM25Search(ctx, "local", "kubernetes", 50, tc.filter)
			require.NoError(t, err)
			gotBM := docSet(bm)
			assert.Len(t, gotBM, len(tc.want), "bm25 result count")
			for _, u := range tc.want {
				assert.True(t, gotBM[byURL[u]], "bm25 missing %s", u)
			}

			vec, err := ch.VectorSearch(ctx, "local", fillVec(0.15), 50, tc.filter)
			require.NoError(t, err)
			gotVec := docSet(vec)
			assert.Len(t, gotVec, len(tc.want), "vector result count")
			for _, u := range tc.want {
				assert.True(t, gotVec[byURL[u]], "vector missing %s", u)
			}
		})
	}
}
