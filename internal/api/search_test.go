package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// embedFunc adapts a function to search.Embedder.
type embedFunc func(ctx context.Context, texts []string) ([][]float32, error)

func (f embedFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

// unitVec is a unit vector as wide as the test server's home, which
// newStartingTestServer creates with the default embedding model and width.
func unitVec() []float32 {
	v := make([]float32, config.Default().Embedding.Dim)
	v[0] = 1
	return v
}

// newSearchServer serves /v1/search with emb as the query embedder, over
// three indexed documents that all mention "kafka". Each option adjusts the
// Deps after the search engine is set.
func newSearchServer(t *testing.T, emb search.Embedder, cfg search.Config, options ...func(*Deps)) *testServer {
	t.Helper()
	cfg.Log = slog.New(slog.DiscardHandler)
	s := newTestServer(t, append([]func(*Deps){func(d *Deps) {
		d.Search = search.New(d.Chunks, d.Documents, emb, cfg)
	}}, options...)...)
	s.indexKafka(t, 0, 3)
	return s
}

// indexKafka indexes untitled documents from to from+n-1, each mentioning
// "kafka" once and embedded a little further from unitVec than the one
// before it, so both retrievers rank them in the order written.
func (s *testServer) indexKafka(t *testing.T, from, n int) {
	t.Helper()
	ctx := context.Background()
	for i := from; i < from+n; i++ {
		doc := s.seedDocument(t, fmt.Sprintf("https://example.com/kafka/%03d", i), store.DocStateFetched)
		ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test",
			Status: store.ExtractionStatusOK, FetchedAt: time.Now().UTC()}
		require.NoError(t, s.deps.Extractions.Create(ctx, ext))
		require.NoError(t, s.deps.Documents.SetCurrentExtraction(ctx, doc.ID, ext.ID))
		vec := unitVec()
		vec[1] = float32(i) * 0.001
		require.NoError(t, s.deps.Chunks.ReplaceForDocument(ctx, doc.ID, ext.ID, "", nil,
			[]store.ChunkInput{{Text: fmt.Sprintf("kafka partitions part %d", i), Embedding: vec}}))
	}
}

func (s *testServer) search(t *testing.T, body string) response {
	t.Helper()
	return s.do(t, request{method: http.MethodPost, path: "/v1/search", contentType: "application/json", body: body})
}

func okEmbedder() search.Embedder {
	return embedFunc(func(context.Context, []string) ([][]float32, error) {
		return [][]float32{unitVec()}, nil
	})
}

func TestSearch_KBounds(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{DefaultK: 2})
	for _, body := range []string{`{"query":"kafka","k":-1}`, `{"query":"kafka","k":101}`} {
		t.Run(body, func(t *testing.T) {
			assertProblem(t, s.search(t, body), http.StatusBadRequest)
		})
	}
}

// TestSearch_OffsetBounds: an offset below 0, or one that takes the window
// past the ranking's 100 documents with the k the request gets, is a 400
// naming the bounds, refused before the query is embedded; checking it
// can't overflow.
func TestSearch_OffsetBounds(t *testing.T) {
	var embeds atomic.Int32
	counting := embedFunc(func(context.Context, []string) ([][]float32, error) {
		embeds.Add(1)
		return [][]float32{unitVec()}, nil
	})
	s := newSearchServer(t, counting, search.Config{DefaultK: 10})
	for body, detail := range map[string]string{
		`{"query":"kafka","offset":-1}`:                  "offset must be between 0 and 90 (offset + k at most 100), got -1",
		`{"query":"kafka","offset":91}`:                  "offset must be between 0 and 90 (offset + k at most 100), got 91",
		`{"query":"kafka","k":100,"offset":1}`:           "offset must be between 0 and 0 (offset + k at most 100), got 1",
		`{"query":"kafka","k":1,"offset":100}`:           "offset must be between 0 and 99 (offset + k at most 100), got 100",
		`{"query":"kafka","offset":9223372036854775807}`: "offset must be between 0 and 90 (offset + k at most 100), got 9223372036854775807",
	} {
		t.Run(body, func(t *testing.T) {
			p := assertProblem(t, s.search(t, body), http.StatusBadRequest)
			assert.Equal(t, detail, p.Detail)
		})
	}
	assert.Zero(t, embeds.Load(), "a refused offset embeds nothing")

	for _, body := range []string{`{"query":"kafka","offset":0}`, `{"query":"kafka","k":10,"offset":90}`,
		`{"query":"kafka","k":100,"offset":0}`, `{"query":"kafka","offset":90}`} {
		resp := s.search(t, body)
		assert.Equal(t, http.StatusOK, resp.status, "%s: %s", body, resp.body)
	}
}

// decodeSearch decodes a 200 search response.
func decodeSearch(t *testing.T, resp response) SearchResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	return got
}

func searchIDs(hits []SearchHitResponse) []string {
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.Document.ID)
	}
	return ids
}

// TestSearch_Pages: the pages of a query are windows of its one ranking,
// so pages of 10 are the k=100 answer in order; each says how many
// documents the ranking holds and whether more matched, and an offset at or
// past that count is an empty page, not an error.
func TestSearch_Pages(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{})
	whole := decodeSearch(t, s.search(t, `{"query":"kafka","k":100}`))
	assert.Equal(t, 3, whole.Total)
	assert.False(t, whole.Capped)
	assert.Contains(t, s.search(t, `{"query":"kafka"}`).body, `"capped":false`, "sent when false")
	for _, offset := range []int{3, 90} {
		resp := s.search(t, fmt.Sprintf(`{"query":"kafka","offset":%d}`, offset))
		got := decodeSearch(t, resp)
		assert.Empty(t, got.Items, "offset %d", offset)
		assert.Contains(t, resp.body, `"items":[]`, "offset %d", offset)
		assert.Equal(t, 3, got.Total, "offset %d", offset)
	}

	s.indexKafka(t, 3, 102)
	whole = decodeSearch(t, s.search(t, `{"query":"kafka","k":100}`))
	require.Len(t, whole.Items, 100)
	var paged []SearchHitResponse
	for offset := 0; offset <= 90; offset += 10 {
		page := decodeSearch(t, s.search(t, fmt.Sprintf(`{"query":"kafka","k":10,"offset":%d}`, offset)))
		assert.Equal(t, 100, page.Total, "offset %d", offset)
		assert.True(t, page.Capped, "offset %d: 105 documents matched", offset)
		paged = append(paged, page.Items...)
	}
	assert.Equal(t, searchIDs(whole.Items), searchIDs(paged))
	assert.Equal(t, 100, whole.Total)
	assert.True(t, whole.Capped)
}

// titleLookups is a document store that records each batched document
// read, the one that names untitled hits, and fails it with err when set.
type titleLookups struct {
	store.DocumentStore
	calls [][]string
	err   error
}

func (l *titleLookups) GetByIDsWithLastError(ctx context.Context, tenantID string, ids []string) ([]store.DocumentWithError, error) {
	l.calls = append(l.calls, ids)
	if l.err != nil {
		return nil, l.err
	}
	return l.DocumentStore.GetByIDsWithLastError(ctx, tenantID, ids)
}

// TestSearch_NamedByBookmark: search hits and related documents without a
// title of their own carry their bookmark's, read once per response for
// the untitled hits alone; a response with none reads nothing, and a
// failed read fails the request.
func TestSearch_NamedByBookmark(t *testing.T) {
	lookups := &titleLookups{}
	s := newSearchServer(t, okEmbedder(), search.Config{}, func(d *Deps) {
		lookups.DocumentStore = d.Documents
		d.Documents = lookups
	})
	ids := s.documentIDsByURL(t) // kafka/000, 001, 002: ranked in that order
	_, err := s.db.Exec(`UPDATE documents SET title = 'Kafka, titled' WHERE id = ?`, ids[0])
	require.NoError(t, err)
	named := "Partitions, as bookmarked"
	_, err = s.deps.Bookmarks.Ingest(context.Background(), &store.Bookmark{TenantID: "local",
		URL: "https://example.com/kafka/001", Title: &named, Source: store.SourceChrome, SavedAt: time.Now().UTC()})
	require.NoError(t, err)

	got := decodeSearch(t, s.search(t, `{"query":"kafka","k":3}`))
	require.Equal(t, ids, searchIDs(got.Items))
	require.Len(t, lookups.calls, 1, "one read for the page")
	assert.Equal(t, ids[1:], lookups.calls[0], "the untitled hits alone")
	assert.Empty(t, got.Items[0].BookmarkTitle, "a titled document keeps its title")
	assert.Equal(t, named, got.Items[1].BookmarkTitle)
	assert.Empty(t, got.Items[2].BookmarkTitle, "no bookmark names it")

	lookups.calls = nil
	got = decodeSearch(t, s.search(t, `{"query":"kafka","k":1}`))
	require.Equal(t, ids[:1], searchIDs(got.Items))
	assert.Empty(t, lookups.calls, "every hit has a title: nothing to read")

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + ids[0] + "/related"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var related RelatedResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &related))
	require.Equal(t, ids[1:], searchIDs(related.Items))
	assert.Equal(t, named, related.Items[0].BookmarkTitle)

	lookups.err = errInjected
	p := assertProblem(t, s.search(t, `{"query":"kafka","k":3}`), http.StatusInternalServerError)
	assert.Contains(t, p.Detail, errInjected.Error())
}

func TestSearch_DefaultK(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{DefaultK: 2})

	resp := s.search(t, `{"query":"kafka"}`)
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Len(t, got.Items, 2, "an omitted k uses search.default_k")
	assert.False(t, got.Degraded)
	assert.NotContains(t, resp.body, `"degraded"`, "omitted when false")

	resp = s.search(t, `{"query":"kafka","k":3}`)
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Len(t, got.Items, 3)
}

func TestSearch_DegradedResponse(t *testing.T) {
	// The embedder hangs until the engine's embed deadline, so the request
	// takes at least that long and took_ms must show it.
	hang := embedFunc(func(ctx context.Context, _ []string) ([][]float32, error) {
		<-ctx.Done()
		return nil, fmt.Errorf("ollama: %w", ctx.Err())
	})
	s := newSearchServer(t, hang, search.Config{EmbedTimeout: 20 * time.Millisecond})

	resp := s.search(t, `{"query":"kafka"}`)
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.True(t, got.Degraded)
	require.NotEmpty(t, got.Warnings)
	assert.Contains(t, got.Warnings[0], "keyword-only")
	assert.Len(t, got.Items, 3, "keyword results are returned")
	assert.GreaterOrEqual(t, got.TookMS, int64(20))
}

func TestSearch_UnsupportedFieldsAreRejected(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{})
	for _, body := range []string{
		`{"query":"kafka","weights":{"bm25":2}}`,
		`{"query":"kafka","collapse":"sum"}`,
		`{"query":"kafka","filters":{"saved_after":"2024-01-01T00:00:00Z"}}`,
		`{"query":"kafka","filters":{"folder":"/a"}}`,
		`{"query":"kafka","filters":{"tag":["t"]}}`,
	} {
		t.Run(body, func(t *testing.T) {
			assertProblem(t, s.search(t, body), http.StatusBadRequest)
		})
	}
}

// TestSearch_FiltersValidated: a content_type or source outside its set is
// a 400 naming the allowed values, refused before the query is embedded,
// not an empty answer that reads as "nothing matches"; values in the set
// filter.
func TestSearch_FiltersValidated(t *testing.T) {
	var embeds atomic.Int32
	counting := embedFunc(func(context.Context, []string) ([][]float32, error) {
		embeds.Add(1)
		return [][]float32{unitVec()}, nil
	})
	s := newSearchServer(t, counting, search.Config{})
	for body, allowed := range map[string]string{
		`{"query":"kafka","filters":{"content_type":["articles"]}}`: `"articles" must be one of: ` + contentTypeList,
		`{"query":"kafka","filters":{"source":["chrom"]}}`:          `"chrom" must be one of: ` + sourceList,
	} {
		t.Run(body, func(t *testing.T) {
			p := assertProblem(t, s.search(t, body), http.StatusBadRequest)
			assert.Contains(t, p.Detail, allowed)
		})
	}
	assert.Zero(t, embeds.Load(), "a refused filter embeds nothing")

	for _, body := range []string{
		`{"query":"kafka","filters":{"content_type":["article"]}}`,
		`{"query":"kafka","filters":{"source":["chrome"]}}`,
	} {
		resp := s.search(t, body)
		assert.Equal(t, http.StatusOK, resp.status, resp.body)
	}
}

// documentIDsByURL lists the server's document IDs in URL order.
func (s *testServer) documentIDsByURL(t *testing.T) []string {
	t.Helper()
	var ids []string
	rows, err := s.db.Query(`SELECT id FROM documents ORDER BY url`)
	require.NoError(t, err)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return ids
}

func TestRelatedDocuments(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{})
	ids := s.documentIDsByURL(t)

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + ids[0] + "/related?k=5"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got RelatedResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, ids[0], got.DocID)
	require.Len(t, got.Items, 2, "every other indexed document")
	for _, hit := range got.Items {
		assert.NotEqual(t, ids[0], hit.Document.ID)
	}

	unindexed := s.seedDocument(t, "https://example.com/unindexed", store.DocStatePending)
	resp = s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + unindexed.ID + "/related"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Empty(t, got.Items)

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/documents/no-such-document/related"}),
		http.StatusNotFound)
	assert.Equal(t, `document "no-such-document" not found`, p.Detail)
}

// vanishingDocs is a document store in which the documents in gone were
// deleted after search read their chunks.
type vanishingDocs struct {
	store.DocumentStore
	gone map[string]bool
}

func (v *vanishingDocs) GetByID(ctx context.Context, id string) (*store.Document, error) {
	if v.gone[id] {
		return nil, fmt.Errorf("document %s: %w", id, store.ErrNotFound)
	}
	return v.DocumentStore.GetByID(ctx, id)
}

// TestRelatedDocuments_HitDeletedMidQuery: a related document deleted
// while the query runs drops out; it is not reported as the source
// document being missing.
func TestRelatedDocuments_HitDeletedMidQuery(t *testing.T) {
	docs := &vanishingDocs{gone: map[string]bool{}}
	s := newSearchServer(t, okEmbedder(), search.Config{}, func(d *Deps) {
		docs.DocumentStore = d.Documents
		d.Search = search.New(d.Chunks, docs, okEmbedder(), search.Config{Log: slog.New(slog.DiscardHandler)})
	})
	ids := s.documentIDsByURL(t)
	docs.gone[ids[1]] = true

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/documents/" + ids[0] + "/related"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got RelatedResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	require.Len(t, got.Items, 1)
	assert.Equal(t, ids[2], got.Items[0].Document.ID)
}

// TestSearch_ExtractionLookupFailure: a hit whose markdown path can't be
// read fails the search rather than coming back without one.
func TestSearch_ExtractionLookupFailure(t *testing.T) {
	s := newSearchServer(t, okEmbedder(), search.Config{},
		func(d *Deps) { d.Extractions = failingExtractionLookup{d.Extractions} })
	p := assertProblem(t, s.search(t, `{"query":"kafka"}`), http.StatusInternalServerError)
	assert.Contains(t, p.Detail, errInjected.Error())
}
