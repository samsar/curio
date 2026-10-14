package sqlite

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// knnLib is a library of documents whose vectors the KNN tests place:
// many near the query, on one host, and a few far, on another, so the
// nearest thousand vectors hold none of the far ones.
type knnLib struct {
	t     *testing.T
	ctx   context.Context
	db    *DB
	rng   *rand.Rand
	query []float32
}

func newKNNLib(t *testing.T) *knnLib {
	return &knnLib{t: t, ctx: context.Background(), db: newTestDBWidth(t, filterWidth),
		rng: rand.New(rand.NewPCG(3, 5)), query: make([]float32, filterWidth)}
}

// vector is a random vector whose components lie in [lo, lo+0.5).
func (l *knnLib) vector(lo float32) []float32 {
	v := make([]float32, filterWidth)
	for i := range v {
		v[i] = lo + 0.5*l.rng.Float32()
	}
	return v
}

// distance is v's L2 distance from the query, as a brute-force ranking
// computes it.
func (l *knnLib) distance(v []float32) float64 {
	var sum float64
	for i, x := range v {
		d := float64(x) - float64(l.query[i])
		sum += d * d
	}
	return math.Sqrt(sum)
}

// chunked writes a document at url with n chunks whose vectors start at
// lo, and returns its chunks' IDs with their vectors.
func (l *knnLib) chunked(url string, n int, lo float32) map[string][]float32 {
	l.t.Helper()
	id := seedDocs(l.t, l.db, "local", url)[0]
	inputs := make([]store.ChunkInput, n)
	for i := range inputs {
		inputs[i] = store.ChunkInput{Text: fmt.Sprint("chunk ", i), Embedding: l.vector(lo)}
	}
	chunks := NewChunks(l.db, filterWidth)
	require.NoError(l.t, chunks.ReplaceForDocument(l.ctx, id, latestExtractionID(l.t, l.db, id), "", nil, inputs))
	stored, err := chunks.EmbeddingsForDocument(l.ctx, id)
	require.NoError(l.t, err)
	out := map[string][]float32{}
	for _, c := range stored {
		out[c.ChunkID] = c.Embedding
	}
	return out
}

// carded writes documents at urls, each with a card embedded at a
// vector starting at lo, in one transaction, and returns their IDs with
// their vectors.
func (l *knnLib) carded(lo float32, urls ...string) map[string][]float32 {
	l.t.Helper()
	tx, err := l.db.BeginTx(l.ctx, nil)
	require.NoError(l.t, err)
	defer tx.Rollback()
	out := map[string][]float32{}
	for _, url := range urls {
		id := "doc-" + url
		v := l.vector(lo)
		blob, err := sqlitevec.SerializeFloat32(v)
		require.NoError(l.t, err)
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO documents (id, tenant_id, url) VALUES (?, 'local', ?)`, []any{id, url}},
			{`INSERT INTO document_cards (document_id, text, embedded_text, embedded_at) VALUES (?, 'card', 'card', 'now')`,
				[]any{id}},
			{`INSERT INTO document_cards_vec (document_id, embedding) VALUES (?, ?)`, []any{id, blob}},
		} {
			_, err := tx.ExecContext(l.ctx, q.sql, q.args...)
			require.NoError(l.t, err)
		}
		out[id] = v
	}
	require.NoError(l.t, tx.Commit())
	return out
}

// bruteForce is ids ranked by distance from the query, ties by ID.
func (l *knnLib) bruteForce(vectors map[string][]float32) []string {
	ids := make([]string, 0, len(vectors))
	for id := range vectors {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		return cmp.Or(cmp.Compare(l.distance(vectors[a]), l.distance(vectors[b])), strings.Compare(a, b))
	})
	return ids
}

var onTarget = store.SearchFilters{Host: []string{"target.example"}}

// TestVectorSearch_NarrowFilterRanksItsChunksExactly: a filter whose
// chunks all lie beyond the nearest thousand still gets them, nearest
// first, in the order a brute-force ranking gives.
func TestVectorSearch_NarrowFilterRanksItsChunksExactly(t *testing.T) {
	l := newKNNLib(t)
	for i := range 3 {
		l.chunked(fmt.Sprintf("https://near.example/%d", i), 400, 0)
	}
	target := map[string][]float32{}
	for i, n := range []int{3, 5, 7} {
		maps.Copy(target, l.chunked(fmt.Sprintf("https://target.example/%d", i), n, 1))
	}
	seedDocs(t, l.db, "local", "https://target.example/unindexed")
	chunks := NewChunks(l.db, filterWidth)

	unfiltered, err := chunks.VectorSearch(l.ctx, "local", l.query, 100, store.SearchFilters{})
	require.NoError(t, err)
	for _, h := range unfiltered {
		assert.NotContains(t, target, h.ChunkID, "the over-fetch of a thousand reaches no target chunk")
	}

	plan, err := chunks.vectorSearchQuery(l.ctx, "local", queryVectorOf(t, l.query), 100, onTarget)
	require.NoError(t, err)
	assert.Equal(t, knnExact, plan.path)
	hits, err := chunks.VectorSearch(l.ctx, "local", l.query, 100, onTarget)
	require.NoError(t, err)
	got := make([]string, len(hits))
	for i, h := range hits {
		got[i] = h.ChunkID
	}
	assert.Equal(t, l.bruteForce(target), got)

	hits, err = chunks.VectorSearch(l.ctx, "local", l.query, 4, onTarget)
	require.NoError(t, err)
	assert.Len(t, hits, 4, "the nearest limit")
	assert.Equal(t, l.bruteForce(target)[:4], []string{hits[0].ChunkID, hits[1].ChunkID, hits[2].ChunkID,
		hits[3].ChunkID})
}

// TestSearchCards_NarrowFilterRanksItsCardsExactly: as for the chunks; a
// card never embedded and one marked unembeddable are left out, with no
// error, since the exact path lists only cards with a vector.
func TestSearchCards_NarrowFilterRanksItsCardsExactly(t *testing.T) {
	l := newKNNLib(t)
	near := make([]string, 1100)
	for i := range near {
		near[i] = fmt.Sprintf("https://near.example/%d", i)
	}
	l.carded(0, near...)
	target := l.carded(1, "https://target.example/a", "https://target.example/b", "https://target.example/c")
	_, err := l.db.Exec(`
		INSERT INTO documents (id, tenant_id, url) VALUES ('never', 'local', 'https://target.example/never'),
			('unembeddable', 'local', 'https://target.example/unembeddable');
		INSERT INTO document_cards (document_id, text, embedded_text, embedded_at) VALUES ('never', 'card', NULL, NULL),
			('unembeddable', 'card', 'card', NULL);`)
	require.NoError(t, err)
	ix := NewDocumentIndex(l.db, filterWidth)

	unfiltered, err := ix.SearchCards(l.ctx, "local", l.query, 100, store.SearchFilters{})
	require.NoError(t, err)
	for _, h := range unfiltered {
		assert.NotContains(t, target, h.DocumentID, "the over-fetch of a thousand reaches no target card")
	}
	plan, err := ix.searchCardsQuery(l.ctx, "local", queryVectorOf(t, l.query), 100, onTarget)
	require.NoError(t, err)
	assert.Equal(t, knnExact, plan.path)
	hits, err := ix.SearchCards(l.ctx, "local", l.query, 100, onTarget)
	require.NoError(t, err)
	assert.Equal(t, l.bruteForce(target), hitIDs(hits))
}

// TestKNN_OverABoundOverFetches: a set past the bound takes the
// over-fetch, as many neighbours as its share of the index calls for, and
// still finds what it can.
func TestKNN_OverABoundOverFetches(t *testing.T) {
	l := newKNNLib(t)
	target := l.chunked("https://target.example/1", 3, 1)
	l.chunked("https://near.example/1", 9, 0)
	cards := l.carded(1, "https://target.example/2", "https://target.example/3")
	near := make([]string, 6)
	for i := range near {
		near[i] = fmt.Sprintf("https://near.example/card/%d", i)
	}
	l.carded(0, near...)
	l.db.SetExactKNNMaxIDs(1)
	embedding := queryVectorOf(t, l.query)
	// A quarter of each index: four times the unfiltered over-fetch.
	const k = 4 * maxVectorOverfetch

	chunks := NewChunks(l.db, filterWidth)
	plan, err := chunks.vectorSearchQuery(l.ctx, "local", embedding, 100, onTarget)
	require.NoError(t, err)
	assert.Equal(t, knnOverFetch, plan.path)
	assert.Equal(t, k, plan.args[1], "k")
	wantSQL, _ := vectorQuery("local", embedding, k, onTarget)
	assert.Equal(t, wantSQL, plan.sql)
	hits, err := chunks.VectorSearch(l.ctx, "local", l.query, 100, onTarget)
	require.NoError(t, err)
	assert.Len(t, hits, len(target))

	ix := NewDocumentIndex(l.db, filterWidth)
	plan, err = ix.searchCardsQuery(l.ctx, "local", embedding, 100, onTarget)
	require.NoError(t, err)
	assert.Equal(t, knnOverFetch, plan.path)
	assert.Equal(t, k, plan.args[1], "k")
	cardHits, err := ix.SearchCards(l.ctx, "local", l.query, 100, onTarget)
	require.NoError(t, err)
	assert.Equal(t, l.bruteForce(cards), hitIDs(cardHits))
}

// TestOverFetchKFor: the over-fetch of a set reads the unfiltered k
// divided by the set's share of the index, rounded up, never fewer than
// the unfiltered k nor more than sqlite-vec takes.
func TestOverFetchKFor(t *testing.T) {
	for _, tc := range []struct{ unfiltered, n, total, want int }{
		{1000, 51_052, 51_052, 1000},
		{1000, 40_748, 51_052, 1253},
		{1000, 25_526, 51_052, 2000},
		{1000, 15_001, 51_052, 3404},
		{1000, 12_000, 51_052, vec0MaxK},
		{1000, 1, 51_052, vec0MaxK},
		{500, 3, 4, 667},
		{1000, 60_000, 51_052, 1000},
		{1000, 0, 51_052, vec0MaxK},
	} {
		assert.Equal(t, tc.want, overFetchKFor(tc.unfiltered, tc.n, tc.total), "%+v", tc)
	}
}

// TestKNN_ExclusionAloneIsTodaysQuery: leaving out one document (find
// related) doesn't narrow: the lists run the SQL and k they ran before
// filters narrowed, byte for byte, and count nothing.
func TestKNN_ExclusionAloneIsTodaysQuery(t *testing.T) {
	ctx := context.Background()
	embedding := queryVectorOf(t, make([]float32, filterWidth))
	exclude := store.SearchFilters{ExcludeDocumentID: "doc"}
	// A database without tables fails any count, so a plan made on one
	// counted nothing.
	db, _ := openUnmigrated(t)

	plan, err := NewChunks(db, filterWidth).vectorSearchQuery(ctx, "local", embedding, 800, exclude)
	require.NoError(t, err)
	assert.Equal(t, `
	SELECT v.chunk_id, c.document_id, v.distance
	FROM chunks_vec v
	JOIN chunks c     ON c.id = v.chunk_id
	JOIN documents d  ON d.id = c.document_id
	WHERE v.embedding MATCH ? AND k = ?
	  AND d.tenant_id = ? AND d.state NOT IN (?, ?) AND d.id != ?
	ORDER BY v.distance`, plan.sql)
	assert.Equal(t, []any{embedding, 1000, "local", store.DocStateFailed, store.DocStateDead, "doc"}, plan.args)

	plan, err = NewDocumentIndex(db, filterWidth).searchCardsQuery(ctx, "local", embedding, 50, exclude)
	require.NoError(t, err)
	assert.Equal(t, `
	SELECT v.document_id, v.distance
	FROM document_cards_vec v
	JOIN documents d ON d.id = v.document_id
	WHERE v.embedding MATCH ? AND k = ?
	  AND d.tenant_id = ? AND d.id != ?
	ORDER BY v.distance`, plan.sql)
	assert.Equal(t, []any{embedding, 500, "local", "doc"}, plan.args)
}

// TestKNN_NothingLeftRunsNoKNN: a filter that leaves no chunk, or only
// documents without chunks, runs no KNN, and finds nothing.
func TestKNN_NothingLeftRunsNoKNN(t *testing.T) {
	l := newKNNLib(t)
	l.chunked("https://near.example/1", 3, 0)
	seedDocs(t, l.db, "local", "https://target.example/unindexed")
	chunks := NewChunks(l.db, filterWidth)
	ix := NewDocumentIndex(l.db, filterWidth)
	for _, f := range []store.SearchFilters{onTarget, {Host: []string{"nowhere.example"}}} {
		plan, err := chunks.vectorSearchQuery(l.ctx, "local", queryVectorOf(t, l.query), 100, f)
		require.NoError(t, err)
		assert.Equal(t, knnNone, plan.path)
		assert.Empty(t, plan.sql)
		hits, err := chunks.VectorSearch(l.ctx, "local", l.query, 100, f)
		require.NoError(t, err)
		assert.Empty(t, hits)

		cardPlan, err := ix.searchCardsQuery(l.ctx, "local", queryVectorOf(t, l.query), 100, f)
		require.NoError(t, err)
		assert.Equal(t, knnNone, cardPlan.path)
		cards, err := ix.SearchCards(l.ctx, "local", l.query, 100, f)
		require.NoError(t, err)
		assert.Empty(t, cards)
	}
}

// TestKNN_MissingVectorFallsBack: vec0 answers an IN list naming a row it
// has no vector for with nothing at all. The lists detect it, warn once
// for each index, and read the over-fetch instead, which finds the set's
// rows that have vectors, nearest first, rather than losing them all.
func TestKNN_MissingVectorFallsBack(t *testing.T) {
	l := newKNNLib(t)
	var logged bytes.Buffer
	l.db.SetLog(slog.New(slog.NewTextHandler(&logged, nil)))
	target := l.chunked("https://target.example/1", 3, 1)
	cards := l.carded(1, "https://target.example/2", "https://target.example/3")
	for _, id := range slices.Sorted(maps.Keys(target))[:1] {
		_, err := l.db.Exec(`DELETE FROM chunks_vec WHERE chunk_id = ?`, id)
		require.NoError(t, err)
		delete(target, id)
	}
	for _, id := range slices.Sorted(maps.Keys(cards))[:1] {
		_, err := l.db.Exec(`DELETE FROM document_cards_vec WHERE document_id = ?`, id)
		require.NoError(t, err)
		delete(cards, id)
	}
	err := l.db.checkExactKNN(l.ctx, chunkSet("local", onTarget), "chunk")
	require.ErrorIs(t, err, store.ErrVectorIndexInconsistent, "the exact path finds the vector missing")
	assert.ErrorContains(t, err, "curio reindex --all")
	chunks := NewChunks(l.db, filterWidth)
	plan, err := chunks.vectorSearchQuery(l.ctx, "local", queryVectorOf(t, l.query), 100, onTarget)
	require.NoError(t, err)
	assert.Equal(t, knnExact, plan.path)
	ix := NewDocumentIndex(l.db, filterWidth)

	for range 2 {
		hits, err := chunks.VectorSearch(l.ctx, "local", l.query, 100, onTarget)
		require.NoError(t, err)
		got := make([]string, len(hits))
		for i, h := range hits {
			got[i] = h.ChunkID
		}
		assert.Equal(t, l.bruteForce(target), got, "the chunks with vectors, nearest first")
		cardHits, err := ix.SearchCards(l.ctx, "local", l.query, 100, onTarget)
		require.NoError(t, err)
		assert.Equal(t, l.bruteForce(cards), hitIDs(cardHits))
	}
	warnings := strings.Split(strings.TrimSpace(logged.String()), "\n")
	require.Len(t, warnings, 2, "one for each index, however many searches meet it")
	assert.Contains(t, warnings[0], "level=WARN")
	assert.Contains(t, warnings[0], "index=chunks_vec")
	assert.Contains(t, warnings[1], "index=document_cards_vec")
	assert.Contains(t, warnings[1], "curio reindex --all")

	hits, err := chunks.VectorSearch(l.ctx, "local", l.query, 100, store.SearchFilters{})
	require.NoError(t, err, "an unfiltered search reads past it")
	assert.Len(t, hits, 2)
}

// queryVectorOf is v in sqlite-vec's format.
func queryVectorOf(t *testing.T, v []float32) []byte {
	t.Helper()
	blob, err := sqlitevec.SerializeFloat32(v)
	require.NoError(t, err)
	return blob
}
