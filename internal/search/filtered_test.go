package search

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// filteredWidth is the filtered library's vector width: small, since the
// tests place vectors by hand.
const filteredWidth = 4

// The documents of filteredLibrary nearer the query than any sighting,
// and the chunks of each: more chunks than the 4,096 nearest that
// sqlite-vec's KNN reads at most.
const (
	lionDocuments = 11
	lionChunks    = 400
)

// filteredLibrary is a library where a narrow filter's documents lie
// beyond every chunk sqlite-vec's KNN can read, so that only an exact
// ranking of the filter's chunks reaches them: the lions' chunks, about
// something else and saved by no bookmark, sit nearer the query than any
// zebra sighting, and the sightings are on a host of their own, each saved
// at a day of its own, from 2024-01-01 on.
type filteredLibrary struct {
	db        *sqlitestore.DB
	docs      *sqlitestore.Documents
	chunks    *sqlitestore.Chunks
	index     *sqlitestore.DocumentIndex
	sightings []string
	// sightingChunks are the sightings' chunks, by ID, with their
	// documents.
	sightingChunks map[string]string
}

func newFilteredLibrary(t *testing.T, sightings int) *filteredLibrary {
	t.Helper()
	ctx := context.Background()
	db := sqlitetest.NewDBWithDim(t, filteredWidth)
	l := &filteredLibrary{db: db, docs: sqlitestore.NewDocuments(db), chunks: sqlitestore.NewChunks(db, filteredWidth),
		index: sqlitestore.NewDocumentIndex(db, filteredWidth), sightingChunks: map[string]string{}}
	bms := sqlitestore.NewBookmarks(db)
	add := func(url, text string, vecs []float32, saved time.Time) string {
		d := &store.Document{TenantID: "local", URL: url, ContentType: store.ContentTypeArticle}
		require.NoError(t, l.docs.Create(ctx, d))
		e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
			FetchedAt: time.Now().UTC()}
		require.NoError(t, sqlitestore.NewExtractions(db).Create(ctx, e))
		inputs := make([]store.ChunkInput, len(vecs))
		for i, v := range vecs {
			inputs[i] = store.ChunkInput{Text: text, Embedding: filledVec(filteredWidth, v)}
		}
		require.NoError(t, l.chunks.ReplaceForDocument(ctx, d.ID, e.ID, "", nil, inputs))
		if !saved.IsZero() {
			require.NoError(t, bms.Create(ctx, &store.Bookmark{TenantID: "local", DocumentID: &d.ID, URL: url,
				Source: store.SourceChrome, SavedAt: saved}))
		}
		return d.ID
	}
	for i := range lionDocuments {
		near := make([]float32, lionChunks)
		for j := range near {
			near[j] = float32(j) * 1e-4
		}
		add(fmt.Sprintf("https://elsewhere.example/%d", i), "lions on the plain", near, time.Time{})
	}
	for i := range sightings {
		// Two chunks, the second farther, each sighting farther than the
		// one before.
		id := add(fmt.Sprintf("https://zebras.example/%d", i), fmt.Sprintf("zebra sighting number %d", i),
			[]float32{1 + float32(i)*0.01, 1.005 + float32(i)*0.01}, time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC))
		l.sightings = append(l.sightings, id)
		embeddings, err := l.chunks.EmbeddingsForDocument(ctx, id)
		require.NoError(t, err)
		for _, c := range embeddings {
			l.sightingChunks[c.ChunkID] = id
		}
	}
	// A day apart, each save is exact once its source is classified.
	_, err := bms.ClassifySavedDates(ctx, "local", store.SourceChrome)
	require.NoError(t, err)
	return l
}

func (l *filteredLibrary) engine(cfg Config) *Engine {
	cfg.Log = slog.New(slog.DiscardHandler)
	query := embedFunc(func(context.Context, []string) ([][]float32, error) {
		return [][]float32{filledVec(filteredWidth, 0)}, nil
	})
	return New(l.chunks, l.docs, l.index, query, cfg)
}

var onZebras = store.SearchFilters{Host: []string{"zebras.example"}}

// TestEngine_NarrowFilterGetsItsNearestChunks: a host filter whose
// documents' chunks all lie past the nearest 4,096 gets them from the
// vector list, every one of the documents' chunks, nearest first, which
// only the exact ranking reaches: unfiltered, or with the filter's set
// over the bound, the vector list finds none.
func TestEngine_NarrowFilterGetsItsNearestChunks(t *testing.T) {
	l := newFilteredLibrary(t, 5)
	engine := l.engine(Config{BM25Weight: 0.001, VectorWeight: 1})
	ctx := context.Background()
	res, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: 5, Filters: onZebras})
	require.NoError(t, err)
	assert.False(t, res.Degraded, "%v", res.Warnings)
	assert.Equal(t, len(l.sightingChunks), res.VectorHits, "every chunk the filter leaves")
	assert.Equal(t, l.sightings, hitIDs(res.Items), "nearest first")
	for _, hit := range res.Items {
		for _, m := range hit.Chunks {
			assert.NotNil(t, m.VectorScore, "the vector list found %s", m.ChunkID)
		}
	}

	unfiltered, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: lionDocuments + 5})
	require.NoError(t, err)
	sighted := 0
	for _, hit := range unfiltered.Items {
		if !slices.Contains(l.sightings, hit.Document.ID) {
			continue
		}
		sighted++
		for _, m := range hit.Chunks {
			assert.Nil(t, m.VectorScore, "unfiltered, the nearest chunks are the lions'")
		}
	}
	assert.Positive(t, sighted, "BM25 finds sightings unfiltered")

	l.db.SetExactKNNMaxIDs(1)
	overFetched, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: 5, Filters: onZebras})
	require.NoError(t, err)
	assert.Zero(t, overFetched.VectorHits, "the over-fetch's nearest 4,096 are the lions'")
}

// TestEngine_FilteredPagesTileTheRanking: a filtered query's pages are
// windows of one ranking, as an unfiltered one's are, and the same
// request ranks the same way twice.
func TestEngine_FilteredPagesTileTheRanking(t *testing.T) {
	l := newFilteredLibrary(t, 110)
	engine := l.engine(Config{})
	ctx := context.Background()
	filters := store.SearchFilters{Host: []string{"zebras.example"}, SavedAfter: time.Date(2024, 1, 5, 0, 0, 0, 0,
		time.UTC)}

	whole, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: 100, Filters: filters})
	require.NoError(t, err)
	require.Len(t, whole.Items, 100)
	again, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: 100, Filters: filters})
	require.NoError(t, err)
	assert.Equal(t, hitIDs(whole.Items), hitIDs(again.Items))
	var paged []Hit
	for offset := 0; offset <= 90; offset += 10 {
		page, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", K: 10, Offset: offset,
			Filters: filters})
		require.NoError(t, err)
		assert.Equal(t, whole.Undated, page.Undated, "offset %d", offset)
		paged = append(paged, page.Items...)
	}
	assert.Equal(t, hitIDs(whole.Items), hitIDs(paged))
	for _, hit := range paged {
		assert.NotEqual(t, l.sightings[0], hit.Document.ID, "saved before the window")
	}
}

// TestEngine_MissingVectorFallsBack: a narrow filter whose chunks include
// one without a vector can't rank them exactly; the search keeps its
// semantic lists, reading the over-fetch instead, whose nearest 4,096
// here are the lions', and is not degraded.
func TestEngine_MissingVectorFallsBack(t *testing.T) {
	l := newFilteredLibrary(t, 3)
	for _, chunkID := range slices.Sorted(maps.Keys(l.sightingChunks))[:1] {
		_, err := l.db.Exec(`DELETE FROM chunks_vec WHERE chunk_id = ?`, chunkID)
		require.NoError(t, err)
	}
	l.db.SetLog(slog.New(slog.DiscardHandler))
	engine := l.engine(Config{})
	ctx := context.Background()

	res, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", Filters: onZebras})
	require.NoError(t, err)
	assert.False(t, res.Degraded, "%v", res.Warnings)
	assert.Empty(t, res.Warnings)
	assert.Zero(t, res.VectorHits, "the over-fetch's nearest are the lions'")
	assert.Len(t, res.Items, 3, "the keyword results")
}

// TestEngine_Undated: a date filter's search says how many documents it
// couldn't judge, the same on every page and for every query with the
// filters; a search without one says nothing.
func TestEngine_Undated(t *testing.T) {
	l := newFilteredLibrary(t, 4)
	engine := l.engine(Config{})
	ctx := context.Background()
	window := store.SearchFilters{SavedAfter: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)}

	for _, req := range []Request{
		{Query: "zebra", Filters: window},
		{Query: "zebra", Offset: 2, K: 1, Filters: window},
		{Query: "lions", Filters: window},
		{Query: "zebra", Filters: store.SearchFilters{SavedAfter: window.SavedAfter, IncludeUndated: true}},
	} {
		req.TenantID = "local"
		res, err := engine.Search(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, res.Undated, "%+v", req)
		assert.Equal(t, lionDocuments, *res.Undated, "the lions', with no bookmark: %+v", req)
	}
	for _, f := range []store.SearchFilters{{}, onZebras} {
		res, err := engine.Search(ctx, Request{TenantID: "local", Query: "zebra", Filters: f})
		require.NoError(t, err)
		assert.Nil(t, res.Undated)
		assert.Zero(t, res.Timings.Undated)
	}
}

// TestEngine_RefusesFiltersTheStoreWont: filters that skipped
// FilterRequest.Resolve are checked again, before anything is read.
func TestEngine_RefusesFiltersTheStoreWont(t *testing.T) {
	engine := New(nil, nil, unusedIndex{}, failingEmbedder(), Config{})
	_, err := engine.Search(context.Background(), Request{TenantID: "local", Query: "zebra",
		Filters: store.SearchFilters{Site: slices.Repeat([]string{"example.com"}, store.MaxFilterValues+1)}})
	assert.ErrorContains(t, err, "filters.site: 33 values, at most 32")
	_, err = engine.Search(context.Background(), Request{TenantID: "local", Query: "zebra",
		Filters: store.SearchFilters{IncludeUndated: true}})
	assert.ErrorContains(t, err, "filters.include_undated")
}
