package sqlite

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

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
		require.NoError(t, docs.Create(ctx, d))

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
	}
	return out
}

// fillVec returns a vector of length vecDim with the same value in every slot.
// Two such vectors have a known L2 distance (= |a-b| * sqrt(dim)).
func fillVec(v float32) []float32 { return fillVecOf(vecDim, v) }

// fillVecOf is fillVec for a vector index dim wide.
func fillVecOf(dim int, v float32) []float32 {
	out := make([]float32, dim)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestChunks_ReplaceForDocument_FullCycle(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
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
	snippets, err := ch.Snippets(ctx, "mvcc concurrency", []string{hits[0].ChunkID})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{hits[0].ChunkID: "PostgreSQL uses <em>MVCC</em> for <em>concurrency</em> control."},
		snippets)

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
	db := newTestDB(t)
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

// chunkKeys is each of a document's chunks' ID and seq, in order.
func chunkKeys(t *testing.T, db *DB, documentID string) [][]any {
	t.Helper()
	return dumpRows(t, db, `SELECT id, seq FROM chunks WHERE document_id = ? ORDER BY ord`, documentID)
}

// vecSlots is how chunks_vec stores its vectors: its vec0 chunks, the
// blocks of 1,024 slots it inserts into, and its rowids, a row per vector.
func vecSlots(t *testing.T, db *DB) []any {
	t.Helper()
	return dumpRows(t, db, `SELECT (SELECT count(*) FROM chunks_vec_chunks), (SELECT count(*) FROM chunks_vec_rowids)`)[0]
}

// ftsShadows dumps chunks_fts's own tables, which any write to the index
// changes.
func ftsShadows(t *testing.T, db *DB) [][][]any {
	t.Helper()
	return [][][]any{dumpRows(t, db, `SELECT * FROM chunks_fts_data ORDER BY id`),
		dumpRows(t, db, `SELECT * FROM chunks_fts_idx ORDER BY segid, term`),
		dumpRows(t, db, `SELECT * FROM chunks_fts_docsize ORDER BY id`)}
}

// replaceTexts re-indexes documentID as texts, each chunk's vector
// fillVec(base + its position).
func replaceTexts(t *testing.T, ch *Chunks, documentID, extractionID string, base float32, texts ...string) {
	t.Helper()
	inputs := make([]store.ChunkInput, len(texts))
	for i, text := range texts {
		inputs[i] = store.ChunkInput{Text: text, TokenCount: len(strings.Fields(text)),
			Context: "Doc — Example — " + text, ContextVersion: 1, Embedding: fillVec(base + float32(i)/1000)}
	}
	require.NoError(t, ch.ReplaceForDocument(context.Background(), documentID, extractionID, "Title", []string{"tag"},
		inputs))
}

// TestChunks_ReplaceForDocument_InPlace: re-indexing a document with as
// many chunks as it had rewrites each chunk where it is: its ID and seq
// stay, the vector index takes no new slot however often it runs, and both
// searches find the new text and vectors, never the old. sqlite-vec reuses
// a freed slot in its newest vec0 chunk alone, so the library fills one
// chunk and starts a second: deleting and inserting the first one's
// vectors again would add a third.
func TestChunks_ReplaceForDocument_InPlace(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)
	docs := seedDocs(t, db, "local", "https://example.com/big", "https://example.com/a", "https://example.com/b",
		"https://example.com/c")
	texts := func(round, doc int) []string {
		out := make([]string, 8)
		if doc == 0 {
			out = make([]string, 1024)
		}
		for i := range out {
			out[i] = fmt.Sprintf("round%d doc%d part%d", round, doc, i)
		}
		return out
	}
	index := func(round int) {
		t.Helper()
		for d, id := range docs {
			replaceTexts(t, ch, id, latestExtractionID(t, db, id), float32(round*10+d), texts(round, d)...)
		}
	}
	index(0)
	keys := make([][][]any, len(docs))
	for d, id := range docs {
		keys[d] = chunkKeys(t, db, id)
	}
	slots := vecSlots(t, db)
	require.Equal(t, []any{int64(2), int64(1048)}, slots, "the first vec0 chunk full, and a second")

	for round := 1; round <= 3; round++ {
		index(round)
	}
	assert.Equal(t, slots, vecSlots(t, db), "no vector took a new slot")
	for d, id := range docs {
		assert.Equal(t, keys[d], chunkKeys(t, db, id), "every chunk kept its ID and seq")
	}
	assertDerivedRowsMatchChunks(t, db)

	hits, err := ch.BM25Search(ctx, "local", "round0", 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.Empty(t, hits, "the old text is gone from the index")
	hits, err = ch.BM25Search(ctx, "local", "round3", 2000, store.SearchFilters{})
	require.NoError(t, err)
	assert.Len(t, hits, 1048)
	nearest, err := ch.VectorSearch(ctx, "local", fillVec(32+0.003), 1, store.SearchFilters{})
	require.NoError(t, err)
	require.Len(t, nearest, 1)
	got, err := ch.GetByIDs(ctx, []string{nearest[0].ChunkID})
	require.NoError(t, err)
	assert.Equal(t, "round3 doc2 part3", got[0].Text, "the new vectors are searched")
	// Round 0's vector of doc2 part3 is gone: its nearest is now one of
	// round 3's.
	gone, err := ch.VectorSearch(ctx, "local", fillVec(2+0.003), 1, store.SearchFilters{})
	require.NoError(t, err)
	require.Len(t, gone, 1)
	got, err = ch.GetByIDs(ctx, []string{gone[0].ChunkID})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got[0].Text, "round3 "), "no old vector is left to find: %s", got[0].Text)
}

// TestChunks_ReplaceForDocument_GrowsAndShrinks: a re-index that adds
// chunks keeps the first ones' IDs and adds the rest; one that drops chunks
// keeps the first ones' and deletes the rest, their FTS entries and vectors
// with them; a refetch's extraction moves to every kept chunk; and every
// chunk always has exactly one vector.
func TestChunks_ReplaceForDocument_GrowsAndShrinks(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)
	exts := NewExtractions(db)
	docs := seedDocs(t, db, "local", "https://example.com/a", "https://example.com/b")
	id, other := docs[0], docs[1]
	replaceTexts(t, ch, other, latestExtractionID(t, db, other), 9, "bystander one", "bystander two")
	bystander := chunkKeys(t, db, other)

	extraction := latestExtractionID(t, db, id)
	var prev [][]any
	for step, n := range []int{3, 5, 2, 0, 4, 4, 1} {
		if step%2 == 1 { // a refetch's new extraction
			e := &store.DocumentExtraction{DocumentID: id, Fetcher: "test", Status: store.ExtractionStatusOK,
				FetchedAt: time.Now().UTC()}
			require.NoError(t, exts.Create(ctx, e))
			extraction = e.ID
		}
		texts := make([]string, n)
		for i := range texts {
			texts[i] = fmt.Sprintf("step%d part%d", step, i)
		}
		replaceTexts(t, ch, id, extraction, float32(step), texts...)

		keys := chunkKeys(t, db, id)
		require.Len(t, keys, n, "step %d", step)
		for i := range min(len(prev), n) {
			assert.Equal(t, prev[i], keys[i], "step %d: the chunk kept at %d keeps its ID and seq", step, i)
		}
		assert.Equal(t, [][]any{{int64(n)}}, dumpRows(t, db,
			`SELECT count(*) FROM chunks WHERE document_id = ? AND extraction_id = ?`, id, extraction),
			"step %d: every chunk is of the extraction indexed", step)
		assert.Equal(t, [][]any{{int64(n)}}, dumpRows(t, db,
			`SELECT count(*) FROM chunks WHERE document_id = ? AND ord BETWEEN 0 AND ?`, id, n-1),
			"step %d: in order from 0", step)
		assertDerivedRowsMatchChunks(t, db)
		hits, err := ch.BM25Search(ctx, "local", fmt.Sprintf("step%d", step), 50, store.SearchFilters{})
		require.NoError(t, err)
		assert.Len(t, hits, n, "step %d", step)
		if step > 0 {
			hits, err = ch.BM25Search(ctx, "local", fmt.Sprintf("step%d", step-1), 50, store.SearchFilters{})
			require.NoError(t, err)
			assert.Empty(t, hits, "step %d: the step before's text is gone", step)
		}
		prev = keys
	}
	assert.Equal(t, bystander, chunkKeys(t, db, other), "another document's chunks are untouched")
}

// TestChunks_ReplaceForDocument_SameTextWritesNoIndex: a re-index whose
// texts, title and tags are those stored, a re-embed, writes nothing to
// chunks_fts, whatever else changes.
func TestChunks_ReplaceForDocument_SameTextWritesNoIndex(t *testing.T) {
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)
	id := seedDocs(t, db, "local", "https://example.com/a")[0]
	ext := latestExtractionID(t, db, id)
	texts := []string{"kafka streams", "consumer groups", "retention"}
	replaceTexts(t, ch, id, ext, 1, texts...)
	before := ftsShadows(t, db)

	inputs := make([]store.ChunkInput, len(texts))
	for i, text := range texts {
		inputs[i] = store.ChunkInput{Text: text, TokenCount: 7, Context: "another context", ContextVersion: 2,
			Embedding: fillVec(0.5)}
	}
	require.NoError(t, ch.ReplaceForDocument(context.Background(), id, ext, "Title", []string{"tag"}, inputs))
	assert.Equal(t, before, ftsShadows(t, db), "chunks_fts is as it was")
	assert.Equal(t, [][]any{{"another context", int64(2), int64(7)}, {"another context", int64(2), int64(7)},
		{"another context", int64(2), int64(7)}},
		dumpRows(t, db, `SELECT context, context_version, token_count FROM chunks ORDER BY ord`))
	assertDerivedRowsMatchChunks(t, db)
}

func TestChunks_BM25_FiltersByTenant(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
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
	db := newTestDB(t)
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
	ch := NewChunks(newTestDB(t), vecDim)
	err := ch.ReplaceForDocument(context.Background(), "doc", "ext", "", nil,
		[]store.ChunkInput{{Text: "short", Embedding: []float32{0.1, 0.2, 0.3}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("embedding length 3 != configured dim %d", vecDim))
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
	db := newTestDB(t)
	docsStore := NewDocuments(db)
	exts := NewExtractions(db)
	bms := NewBookmarks(db)
	ch := NewChunks(db, vecDim)

	seeds := []struct {
		url    string
		ctype  store.ContentType
		source string
		vec    float32
	}{
		{"https://example.com/k8s-guide", store.ContentTypeArticle, store.SourceChrome, 0.10},
		{"https://github.com/foo/bar", store.ContentTypeRepo, store.SourceManual, 0.20},
		{"https://example.com/k8s.pdf", store.ContentTypePDF, store.SourceChrome, 0.30},
	}
	byURL := map[string]string{}
	for _, s := range seeds {
		d := &store.Document{TenantID: "local", URL: s.url, ContentType: s.ctype}
		require.NoError(t, docsStore.Create(ctx, d))
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
		{"ctype pdf", store.SearchFilters{ContentType: []string{string(store.ContentTypePDF)}}, []string{"https://example.com/k8s.pdf"}},
		{"ctype article+repo", store.SearchFilters{ContentType: []string{string(store.ContentTypeArticle), string(store.ContentTypeRepo)}}, []string{"https://example.com/k8s-guide", "https://github.com/foo/bar"}},
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

func TestChunks_EmbeddingsForDocument_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)

	ids := seedDocs(t, db, "local", "https://example.com/roundtrip")
	docID := ids[0]
	ext := latestExtractionID(t, db, docID)

	inputs := []store.ChunkInput{
		{Text: "first chunk", Embedding: fillVec(0.10)},
		{Text: "second chunk", Embedding: fillVec(0.20)},
		{Text: "third chunk", Embedding: fillVec(0.30)},
	}
	require.NoError(t, ch.ReplaceForDocument(ctx, docID, ext, "", nil, inputs))

	embs, err := ch.EmbeddingsForDocument(ctx, docID)
	require.NoError(t, err)
	require.Len(t, embs, 3)

	// Chunk order preserved, float values round-trip exactly.
	for i, want := range []float32{0.10, 0.20, 0.30} {
		require.Len(t, embs[i].Embedding, vecDim)
		assert.Equal(t, want, embs[i].Embedding[0], "chunk %d first component", i)
		assert.Equal(t, want, embs[i].Embedding[vecDim-1], "chunk %d last component", i)
	}
}

func TestChunks_EmbeddingsForDocument_EmptyForUnindexed(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)

	ids := seedDocs(t, db, "local", "https://example.com/unindexed")
	embs, err := ch.EmbeddingsForDocument(ctx, ids[0])
	require.NoError(t, err)
	assert.Empty(t, embs)
}

func TestChunks_VectorSearch_ExcludeDocument(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)

	ids := seedDocs(t, db, "local", "https://example.com/self", "https://example.com/other")
	for i, docID := range ids {
		ext := latestExtractionID(t, db, docID)
		require.NoError(t, ch.ReplaceForDocument(ctx, docID, ext, "", nil,
			[]store.ChunkInput{{Text: "text", Embedding: fillVec(float32(i+1) * 0.1)}}))
	}

	// Query at the excluded doc's own embedding: without exclusion its
	// chunk would be the nearest hit. With exclusion, only the other
	// doc's chunk may come back — this exercises the over-fetch path,
	// since sqlite-vec applies predicates after the k cutoff.
	hits, err := ch.VectorSearch(ctx, "local", fillVec(0.1), 10,
		store.SearchFilters{ExcludeDocumentID: ids[0]})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	for _, h := range hits {
		assert.NotEqual(t, ids[0], h.DocumentID, "excluded document leaked into results")
	}
	assert.Equal(t, ids[1], hits[0].DocumentID)
}

// TestChunks_GetByIDs_MissingIDs: IDs that match no chunk are left out, and
// none matching is an empty result, not an error. A reindex can replace a
// document's chunks between retrieval and hydration.
func TestChunks_GetByIDs_MissingIDs(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)
	ids := seedDocs(t, db, "local", "https://example.com/doc")
	require.NoError(t, ch.ReplaceForDocument(ctx, ids[0], latestExtractionID(t, db, ids[0]), "", nil,
		[]store.ChunkInput{{Text: "kept", Embedding: fillVec(0.1)}}))
	hits, err := ch.BM25Search(ctx, "local", "kept", 10, store.SearchFilters{})
	require.NoError(t, err)
	require.Len(t, hits, 1)

	got, err := ch.GetByIDs(ctx, []string{"gone-1", hits[0].ChunkID, "gone-2"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "kept", got[0].Text)

	got, err = ch.GetByIDs(ctx, []string{"gone-1", "gone-2"})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestChunks_Snippets: the snippets of the chunks asked for are the ones
// the MATCH itself makes, the query's terms marked and a long text cut
// around them; a chunk the query doesn't match, or that is gone, is absent.
func TestChunks_Snippets(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)
	ids := seedDocs(t, db, "local", "https://example.com/a", "https://example.com/b")
	long := strings.Repeat("filler words before the match ", 12) + "kafka partitions" +
		strings.Repeat(" and filler words after it", 12)
	for i, texts := range [][]string{{long, "consumer groups read kafka topics"}, {"nothing to see here"}} {
		inputs := make([]store.ChunkInput, 0, len(texts))
		for _, text := range texts {
			inputs = append(inputs, store.ChunkInput{Text: text, Embedding: fillVec(0.1)})
		}
		require.NoError(t, ch.ReplaceForDocument(ctx, ids[i], latestExtractionID(t, db, ids[i]), "", nil, inputs))
	}
	const query = `"kafka" OR "partitions"`

	inQuery := snippetsOfEveryMatch(t, db, query)
	require.Len(t, inQuery, 2)

	var unmatched string
	require.NoError(t, db.QueryRow(`SELECT id FROM chunks WHERE document_id = ?`, ids[1]).Scan(&unmatched))
	asked := append(slices.Collect(maps.Keys(inQuery)), unmatched, "gone")
	got, err := ch.Snippets(ctx, query, asked)
	require.NoError(t, err)
	assert.Equal(t, inQuery, got)
	for _, snippet := range got {
		assert.Contains(t, snippet, "<em>kafka</em>")
	}
	assert.Contains(t, slices.Collect(maps.Values(got)), "consumer groups read <em>kafka</em> topics")
	for _, snippet := range got {
		if strings.HasPrefix(snippet, "…") {
			assert.Contains(t, snippet, "<em>kafka</em> <em>partitions</em>")
			assert.True(t, strings.HasSuffix(snippet, "…"), "a long text is cut on both sides of its match")
		}
	}

	got, err = ch.Snippets(ctx, query, []string{"gone"})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// snippetsOfEveryMatch are the snippets query's MATCH makes for every row
// it finds, by chunk ID, as BM25Search's own query once made them.
func snippetsOfEveryMatch(t *testing.T, db *DB, query string) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT c.id, `+snippetSQL+` FROM chunks_fts JOIN chunks c ON c.seq = chunks_fts.rowid
		WHERE chunks_fts MATCH ?`, query)
	require.NoError(t, err)
	defer rows.Close()
	snippets := map[string]string{}
	for rows.Next() {
		var id, snippet string
		require.NoError(t, rows.Scan(&id, &snippet))
		snippets[id] = snippet
	}
	require.NoError(t, rows.Err())
	return snippets
}

// TestChunks_SnippetsWithNothingToMark: no chunks, or a blank query, which
// BM25Search matches nothing for, run no query: a closed database isn't
// asked.
func TestChunks_SnippetsWithNothingToMark(t *testing.T) {
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)
	require.NoError(t, db.Close())
	for _, tc := range []struct {
		name  string
		query string
		ids   []string
	}{
		{"no chunks", `"kafka"`, nil},
		{"an empty query", "", []string{"chunk"}},
		{"a blank query", " \t\n", []string{"chunk"}},
	} {
		got, err := ch.Snippets(context.Background(), tc.query, tc.ids)
		require.NoError(t, err, tc.name)
		assert.Empty(t, got, tc.name)
	}
	_, err := ch.Snippets(context.Background(), `"kafka"`, []string{"chunk"})
	require.Error(t, err, "a chunk is asked about")
}

// TestChunks_BM25TiesInWriteOrder: chunks with the same text score the
// same, and come back in the order they were written, however many tie
// and wherever the limit cuts them: search ranks again for every page it
// shows, and a tie's order decides the fused scores.
func TestChunks_BM25TiesInWriteOrder(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)
	urls := make([]string, 0, 40)
	for i := range 40 {
		urls = append(urls, fmt.Sprintf("https://example.com/copy/%d", i))
	}
	ids := seedDocs(t, db, "local", urls...)
	for _, id := range ids {
		require.NoError(t, ch.ReplaceForDocument(ctx, id, latestExtractionID(t, db, id), "", nil,
			[]store.ChunkInput{{Text: "the same syndicated kafka post", Embedding: fillVec(0.1)}}))
	}
	written := chunkIDsWritten(t, db)
	for _, limit := range []int{7, 25, 40} {
		hits, err := ch.BM25Search(ctx, "local", `"kafka"`, limit, store.SearchFilters{})
		require.NoError(t, err)
		got := make([]string, 0, len(hits))
		for _, h := range hits {
			assert.Equal(t, hits[0].Score, h.Score, "every copy scores the same")
			got = append(got, h.ChunkID)
		}
		assert.Equal(t, written[:limit], got, "limit %d", limit)
	}
}

// chunkIDsWritten are the chunks' IDs in the order they were written.
func chunkIDsWritten(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM chunks ORDER BY seq`)
	require.NoError(t, err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// TestChunks_HostFilter: the host is matched literally, so '_' and '%' in a
// requested host are not wildcards, and case-insensitively, as DNS names
// are. Both retrievers share the clause.
func TestChunks_HostFilter(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)
	urls := []string{
		"https://myxsite.com/a", "https://my_site.com/b",
		"https://example.com/c", "http://example.com/d", "https://example.com.evil/e",
	}
	ids := seedDocs(t, db, "local", urls...)
	byURL := map[string]string{}
	for i, id := range ids {
		byURL[urls[i]] = id
		require.NoError(t, ch.ReplaceForDocument(ctx, id, latestExtractionID(t, db, id), "", nil,
			[]store.ChunkInput{{Text: "shared term", Embedding: fillVec(0.1)}}))
	}

	cases := []struct {
		host string
		want []string
	}{
		{"my_site.com", []string{"https://my_site.com/b"}},
		{"%", nil},
		{"_yxsite.com", nil},
		{"EXAMPLE.com", []string{"https://example.com/c", "http://example.com/d"}},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			var want []string
			for _, u := range tc.want {
				want = append(want, byURL[u])
			}
			filters := store.SearchFilters{Host: []string{tc.host}}

			bm, err := ch.BM25Search(ctx, "local", "shared", 50, filters)
			require.NoError(t, err)
			vec, err := ch.VectorSearch(ctx, "local", fillVec(0.1), 50, filters)
			require.NoError(t, err)
			for name, hits := range map[string][]store.ChunkHit{"bm25": bm, "vector": vec} {
				var got []string
				for _, h := range hits {
					got = append(got, h.DocumentID)
				}
				assert.ElementsMatch(t, want, got, name)
			}
		})
	}
}

func TestEscapeLike(t *testing.T) {
	cases := map[string]string{
		"plain.com":      "plain.com",
		"my_site.com":    `my\_site.com`,
		"100%":           `100\%`,
		`back\slash`:     `back\\slash`,
		`\%_`:            `\\\%\_`,
		"https://h.com/": "https://h.com/",
	}
	for in, want := range cases {
		assert.Equal(t, want, escapeLike(in), in)
	}
}

// assertDerivedRowsMatchChunks: chunks_vec holds exactly one vector per
// chunk, and the external-content FTS index agrees with the chunks table.
func assertDerivedRowsMatchChunks(t *testing.T, db *DB) {
	t.Helper()
	var chunks, vectors, orphans int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM chunks`).Scan(&chunks))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM chunks_vec`).Scan(&vectors))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM chunks_vec v
		WHERE NOT EXISTS (SELECT 1 FROM chunks c WHERE c.id = v.chunk_id)`).Scan(&orphans))
	assert.Equal(t, chunks, vectors, "one vector per chunk")
	assert.Zero(t, orphans, "no vector outlives its chunk")
	_, err := db.Exec(`INSERT INTO chunks_fts (chunks_fts, rank) VALUES ('integrity-check', 1)`)
	assert.NoError(t, err, "chunks_fts matches chunks")
}

// TestChunks_DerivedRowsFollowEveryDelete: however a chunk goes (replaced by
// a reindex, or cascaded from its extraction or document), its FTS entry
// and vector go with it.
func TestChunks_DerivedRowsFollowEveryDelete(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)
	ids := seedDocs(t, db, "local", "https://example.com/a", "https://example.com/b", "https://example.com/c")
	index := func(docID string, texts ...string) {
		t.Helper()
		inputs := make([]store.ChunkInput, len(texts))
		for i, text := range texts {
			inputs[i] = store.ChunkInput{Text: text, Embedding: fillVec(float32(i+1) * 0.1)}
		}
		require.NoError(t, ch.ReplaceForDocument(ctx, docID, latestExtractionID(t, db, docID),
			"Title", []string{"tag"}, inputs))
	}
	matches := func(term string) int {
		t.Helper()
		hits, err := ch.BM25Search(ctx, "local", term, 50, store.SearchFilters{})
		require.NoError(t, err)
		return len(hits)
	}

	for _, id := range ids {
		index(id, "original alpha", "original beta")
	}
	index(ids[0], "replacement gamma")
	assertDerivedRowsMatchChunks(t, db)
	assert.Equal(t, 4, matches("original"), "only the replaced document's chunks are gone")
	assert.Equal(t, 1, matches("replacement"))

	_, err := db.Exec(`DELETE FROM document_extractions WHERE document_id = ?`, ids[1])
	require.NoError(t, err)
	assertDerivedRowsMatchChunks(t, db)
	assert.Equal(t, 2, matches("original"), "an extraction's chunks cascade")

	_, err = db.Exec(`DELETE FROM documents WHERE id = ?`, ids[2])
	require.NoError(t, err)
	assertDerivedRowsMatchChunks(t, db)
	assert.Zero(t, matches("original"), "a document's chunks cascade")
	assert.Equal(t, 1, matches("title"), "the chunks left keep their title")
}

// TestChunks_NothingReferencesChunks: migration 008 rebuilds chunks inside
// goose's transaction, where foreign keys stay on. That is safe only while
// no table's foreign key references chunks; dropping a referenced table
// would run its ON DELETE actions (see migrations/README.md).
func TestChunks_NothingReferencesChunks(t *testing.T) {
	db := newTestDB(t)
	var referencing []string
	rows, err := db.Query(`SELECT m.name FROM sqlite_master m, pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' AND f."table" = 'chunks'`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		referencing = append(referencing, name)
	}
	require.NoError(t, rows.Err())
	assert.Empty(t, referencing)
}

// TestChunks_SearchSkipsFailedAndDeadDocuments: a failed or dead document
// keeps its chunks but leaves both retrievers, and comes back once it is
// fetched again, or while a refetch of it is pending.
func TestChunks_SearchSkipsFailedAndDeadDocuments(t *testing.T) {
	for _, gone := range []store.DocState{store.DocStateFailed, store.DocStateDead} {
		for _, back := range []store.DocState{store.DocStateFetched, store.DocStatePending} {
			t.Run(string(gone)+" then "+string(back), func(t *testing.T) {
				ctx := context.Background()
				db := newTestDB(t)
				ch := NewChunks(db, vecDim)
				docs := NewDocuments(db)
				docID := seedDocs(t, db, "local", "https://example.com/junk")[0]
				require.NoError(t, ch.ReplaceForDocument(ctx, docID, latestExtractionID(t, db, docID), "", nil,
					[]store.ChunkInput{{Text: "zebra migration", Embedding: fillVec(0.1)}}))
				searched := func() (bm25, vector bool) {
					t.Helper()
					bm, err := ch.BM25Search(ctx, "local", "zebra", 10, store.SearchFilters{})
					require.NoError(t, err)
					vec, err := ch.VectorSearch(ctx, "local", fillVec(0.1), 10, store.SearchFilters{})
					require.NoError(t, err)
					return len(bm) == 1, len(vec) == 1
				}

				require.NoError(t, docs.MarkFetched(ctx, docID, 0))
				bm, vec := searched()
				require.True(t, bm && vec, "a fetched document is searched")

				require.NoError(t, docs.MarkFailed(ctx, docID, stateCause(gone)))
				bm, vec = searched()
				assert.False(t, bm, "bm25")
				assert.False(t, vec, "vector")

				if back == store.DocStateFetched {
					require.NoError(t, docs.MarkFetched(ctx, docID, 0))
				} else {
					_, err := docs.RequeueFetch(ctx, "local", docID)
					require.NoError(t, err)
				}
				bm, vec = searched()
				assert.True(t, bm, "bm25")
				assert.True(t, vec, "vector")
			})
		}
	}
}

// TestChunks_VectorSearch_SkippedChunksDontCostHits: the chunks nearest the
// query belong to failed and dead documents. Vector search still returns
// limit hits from the others, with and without the related path's
// exclusion.
func TestChunks_VectorSearch_SkippedChunksDontCostHits(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ch := NewChunks(db, vecDim)
	docs := NewDocuments(db)
	index := func(docID string, v float32) {
		t.Helper()
		require.NoError(t, ch.ReplaceForDocument(ctx, docID, latestExtractionID(t, db, docID), "", nil,
			[]store.ChunkInput{{Text: "text", Embedding: fillVec(v)}}))
	}

	junk := seedDocs(t, db, "local", "https://example.com/j1", "https://example.com/j2",
		"https://example.com/j3", "https://example.com/j4")
	for i, docID := range junk {
		index(docID, 0.1)
		cause := store.FailureCauseAntiBot
		if i%2 == 1 {
			cause = store.FailureCauseDeadLink
		}
		require.NoError(t, docs.MarkFailed(ctx, docID, cause))
	}
	live := seedDocs(t, db, "local", "https://example.com/l1", "https://example.com/l2",
		"https://example.com/l3", "https://example.com/l4", "https://example.com/l5")
	for i, docID := range live {
		index(docID, 0.2+0.05*float32(i))
	}

	for name, filters := range map[string]store.SearchFilters{
		"search":  {},
		"related": {ExcludeDocumentID: live[0]},
	} {
		t.Run(name, func(t *testing.T) {
			hits, err := ch.VectorSearch(ctx, "local", fillVec(0.1), 3, filters)
			require.NoError(t, err)
			require.Len(t, hits, 3)
			for _, h := range hits {
				assert.Contains(t, live, h.DocumentID)
				assert.NotEqual(t, filters.ExcludeDocumentID, h.DocumentID)
			}
		})
	}
}

// sampleLibrary writes documents and their chunks for SampleChunks' tests.
type sampleLibrary struct {
	t      *testing.T
	db     *DB
	chunks *Chunks
	docs   *Documents
	n      int // documents written, for unique URLs and vectors
}

func newSampleLibrary(t *testing.T) *sampleLibrary {
	db := newTestDB(t)
	return &sampleLibrary{t: t, db: db, chunks: NewChunks(db, vecDim), docs: NewDocuments(db)}
}

// add writes a document of tenant in state whose chunks are texts, in
// order, with no context, and returns its ID.
func (l *sampleLibrary) add(tenant string, state store.DocState, texts ...string) string {
	l.t.Helper()
	inputs := make([]store.ChunkInput, len(texts))
	for i, text := range texts {
		inputs[i] = store.ChunkInput{Text: text}
	}
	return l.addChunks(tenant, state, inputs...)
}

// addChunks writes a document of tenant in state whose chunks are inputs,
// in order, each given a vector of its own, and returns its ID.
func (l *sampleLibrary) addChunks(tenant string, state store.DocState, inputs ...store.ChunkInput) string {
	l.t.Helper()
	ctx := context.Background()
	l.n++
	id := seedDocs(l.t, l.db, tenant, fmt.Sprintf("https://example.com/%d", l.n))[0]
	for i := range inputs {
		inputs[i].Embedding = sampleVector(l.n*100 + i)
	}
	require.NoError(l.t, l.chunks.ReplaceForDocument(ctx, id, latestExtractionID(l.t, l.db, id), "", nil, inputs))
	switch state {
	case store.DocStateFetched:
		require.NoError(l.t, l.docs.MarkFetched(ctx, id, 0))
	case store.DocStateFailed, store.DocStateDead:
		require.NoError(l.t, l.docs.MarkFailed(ctx, id, stateCause(state)))
	case store.DocStatePending:
	}
	return id
}

func (l *sampleLibrary) sample(n store.ChunkSample) []store.SampledChunk {
	l.t.Helper()
	got, err := l.chunks.SampleChunks(context.Background(), "local", n)
	require.NoError(l.t, err)
	return got
}

// sampleVector is a vector unique to seed whose components are hard to
// round-trip: negative, tiny and irrational values.
func sampleVector(seed int) []float32 {
	v := make([]float32, vecDim)
	for i := range v {
		v[i] = float32(math.Sin(float64(seed*vecDim+i))) * float32(math.Pow(10, float64(i%7-3)))
	}
	return v
}

// textsOf maps each sampled chunk's document to its text.
func textsOf(t *testing.T, sample []store.SampledChunk) map[string]string {
	t.Helper()
	out := make(map[string]string, len(sample))
	for _, c := range sample {
		_, dup := out[c.DocumentID]
		require.False(t, dup, "document %s sampled twice", c.DocumentID)
		out[c.DocumentID] = c.Text
	}
	return out
}

// TestSampleChunks_OnlyTheTenantsFetchedDocuments: chunks of pending,
// failed and dead documents, and of another tenant's, are never sampled,
// however many are asked for.
func TestSampleChunks_OnlyTheTenantsFetchedDocuments(t *testing.T) {
	l := newSampleLibrary(t)
	fetched := l.add("local", store.DocStateFetched, "fetched one", "fetched two")
	for _, state := range []store.DocState{store.DocStatePending, store.DocStateFailed, store.DocStateDead} {
		l.add("local", state, "a much longer chunk of a document that isn't fetched")
	}
	l.add("other", store.DocStateFetched, "a much longer chunk of another tenant's document")

	for _, n := range []store.ChunkSample{{Longest: 16, Random: 48}, {Longest: 0, Random: 48}, {Longest: 16}} {
		got := textsOf(t, l.sample(n))
		assert.Equal(t, []string{fetched}, slices.Collect(maps.Keys(got)), "%+v", n)
	}
}

// TestSampleChunks_Longest: each document is represented by its longest
// chunk in bytes, the documents with the longest representatives are
// chosen, and ties go to the chunk written first, within a document and
// between documents.
func TestSampleChunks_Longest(t *testing.T) {
	for name, tc := range map[string]struct {
		docs    [][]string // each document's chunks, in the order written
		longest int
		want    []string // the texts sampled
	}{
		"each document's longest chunk": {
			docs:    [][]string{{"short", "the longest one", "middling"}},
			longest: 1,
			want:    []string{"the longest one"},
		},
		"the documents with the longest chunks": {
			docs:    [][]string{{"ten bytes!"}, {"thirty bytes, give or take it"}, {"twenty bytes, or so"}},
			longest: 2,
			want:    []string{"thirty bytes, give or take it", "twenty bytes, or so"},
		},
		"a tie within a document goes to the chunk written first": {
			docs:    [][]string{{"x", "first", "later", "y"}},
			longest: 1,
			want:    []string{"first"},
		},
		"a tie between documents goes to the chunk written first": {
			docs:    [][]string{{"x", "first"}, {"later"}, {"third"}},
			longest: 2,
			want:    []string{"first", "later"},
		},
		"bytes, not characters, within a document": {
			docs:    [][]string{{"abcdefghi", "ééééé"}}, // 9 bytes, then 5 characters in 10 bytes
			longest: 1,
			want:    []string{"ééééé"},
		},
		"bytes, not characters, between documents": {
			docs:    [][]string{{"abcdefghi"}, {"ééééé"}},
			longest: 1,
			want:    []string{"ééééé"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := newSampleLibrary(t)
			for _, texts := range tc.docs {
				l.add("local", store.DocStateFetched, texts...)
			}
			got := textsOf(t, l.sample(store.ChunkSample{Longest: tc.longest}))
			assert.ElementsMatch(t, tc.want, slices.Collect(maps.Values(got)))
		})
	}

	t.Run("the bytes embedded: text and context", func(t *testing.T) {
		l := newSampleLibrary(t)
		l.addChunks("local", store.DocStateFetched, store.ChunkInput{Text: "a longer text, alone"},
			store.ChunkInput{Text: "short", Context: "Title — Site — a long heading path"})
		l.addChunks("local", store.DocStateFetched, store.ChunkInput{Text: "twenty-one bytes long"})
		got := textsOf(t, l.sample(store.ChunkSample{Longest: 1}))
		assert.ElementsMatch(t, []string{"short"}, slices.Collect(maps.Values(got)))
	})
}

// TestSampleChunks_Random: the random chunks come from documents the
// longest set doesn't hold, one each, and every chunk of those documents
// can be chosen.
func TestSampleChunks_Random(t *testing.T) {
	l := newSampleLibrary(t)
	longestDocs := map[string]string{
		l.add("local", store.DocStateFetched, "short", strings.Repeat("l", 60)): strings.Repeat("l", 60),
		l.add("local", store.DocStateFetched, strings.Repeat("m", 50), "tiny"):  strings.Repeat("m", 50),
	}
	others := map[string]bool{} // every chunk of the other documents
	for i := range 6 {
		texts := []string{fmt.Sprintf("doc %d, one", i), fmt.Sprintf("doc %d, two", i), fmt.Sprintf("doc %d, three", i)}
		l.add("local", store.DocStateFetched, texts...)
		for _, text := range texts {
			others[text] = false
		}
	}

	for range 200 {
		got := textsOf(t, l.sample(store.ChunkSample{Longest: 2, Random: 3}))
		require.Len(t, got, 5)
		for doc, text := range longestDocs {
			assert.Equal(t, text, got[doc], "the longest set")
			delete(got, doc)
		}
		require.Len(t, got, 3, "the random chunks come from the other documents")
		for _, text := range got {
			_, ok := others[text]
			require.True(t, ok, "%q is another document's chunk", text)
			others[text] = true
		}
	}
	for text, seen := range others {
		assert.True(t, seen, "%q was never chosen", text)
	}
}

// TestSampleChunks_FewerThanAsked: a library smaller than the sample gives
// every fetched document with chunks once; one without chunks never.
func TestSampleChunks_FewerThanAsked(t *testing.T) {
	l := newSampleLibrary(t)
	a := l.add("local", store.DocStateFetched, "a one", "a two")
	b := l.add("local", store.DocStateFetched, "b one")
	l.add("local", store.DocStateFetched) // no chunks

	for range 20 {
		got := textsOf(t, l.sample(store.ChunkSample{Longest: 1, Random: 48}))
		assert.ElementsMatch(t, []string{a, b}, slices.Collect(maps.Keys(got)))
	}
}

func TestSampleChunks_EmptyLibrary(t *testing.T) {
	l := newSampleLibrary(t)
	l.add("local", store.DocStateFetched) // no chunks

	got := l.sample(store.ChunkSample{Longest: 16, Random: 48})
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// TestSampleChunks_TextAndVectorAsStored: a sampled chunk carries its ID,
// its document, its text and context exactly as stored and its vector bit
// for bit.
func TestSampleChunks_TextAndVectorAsStored(t *testing.T) {
	l := newSampleLibrary(t)
	const (
		text  = "  Ada Lovelace — «the first algorithm»\n\nwith trailing space "
		about = "Notes — Example — Analytical Engine › Note G"
	)
	doc := l.addChunks("local", store.DocStateFetched, store.ChunkInput{Text: text, Context: about, ContextVersion: 1})
	want := sampleVector(l.n * 100)

	got := l.sample(store.ChunkSample{Longest: 1})
	require.Len(t, got, 1)
	assert.Equal(t, doc, got[0].DocumentID)
	assert.Equal(t, text, got[0].Text)
	assert.Equal(t, about, got[0].Context)
	stored, err := l.chunks.EmbeddingsForDocument(context.Background(), doc)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, stored[0].ChunkID, got[0].ChunkID)
	require.Len(t, got[0].Embedding, vecDim)
	for i := range want {
		require.Equal(t, math.Float32bits(want[i]), math.Float32bits(got[0].Embedding[i]), "component %d", i)
	}
}

func TestSampleChunks_Refusals(t *testing.T) {
	l := newSampleLibrary(t)
	l.add("local", store.DocStateFetched, "text")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.chunks.SampleChunks(ctx, "local", store.ChunkSample{Longest: 1, Random: 1})
	require.ErrorIs(t, err, context.Canceled)

	_, err = l.chunks.SampleChunks(context.Background(), "", store.ChunkSample{Longest: 1})
	require.Error(t, err, "a tenant is required")
	_, err = l.chunks.SampleChunks(context.Background(), "local", store.ChunkSample{Random: -1})
	require.Error(t, err, "SQLite would read a negative LIMIT as none")
}
