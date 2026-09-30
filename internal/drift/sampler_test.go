package drift

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// chunkSource is a ChunkSampler that returns sample, or err.
type chunkSource struct {
	sample []store.SampledChunk
	err    error
	asked  []store.ChunkSample
}

func (s *chunkSource) SampleChunks(ctx context.Context, tenantID string, n store.ChunkSample) ([]store.SampledChunk, error) {
	if tenantID != store.LocalTenantID {
		return nil, fmt.Errorf("tenant %q", tenantID)
	}
	s.asked = append(s.asked, n)
	if s.err != nil {
		return nil, s.err
	}
	return s.sample, ctx.Err()
}

// reembedder is a ChunkEmbedder that answers with embed, recording the
// texts it was sent.
type reembedder struct {
	embed func(ctx context.Context, texts []string) ([][]float32, error)
	sent  [][]string
}

func (e *reembedder) EmbedChunks(ctx context.Context, texts []string) ([][]float32, error) {
	e.sent = append(e.sent, slices.Clone(texts))
	return e.embed(ctx, texts)
}

// unit is the unit vector along axis of a 4-wide space.
func unit(axis int) []float32 {
	v := make([]float32, 4)
	v[axis] = 1
	return v
}

// at is a unit vector at cosine cos with unit(0).
func at(cos float64) []float32 {
	return []float32{float32(cos), float32(math.Sqrt(1 - cos*cos)), 0, 0}
}

func scaled(v []float32, by float32) []float32 {
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * by
	}
	return out
}

// storedSample is n chunks whose stored vectors are unit(0).
func storedSample(n int) []store.SampledChunk {
	out := make([]store.SampledChunk, n)
	for i := range out {
		out[i] = store.SampledChunk{
			ChunkID: fmt.Sprintf("c%d", i), DocumentID: fmt.Sprintf("d%d", i),
			Text: fmt.Sprintf("chunk %d", i), Embedding: unit(0),
		}
	}
	return out
}

// answering re-embeds each chunk as fresh(i) gives.
func answering(fresh func(i int) []float32) *reembedder {
	return &reembedder{embed: func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = fresh(i)
		}
		return out, nil
	}}
}

func newTestSampler(t *testing.T, src ChunkSampler, emb ChunkEmbedder) *Sampler {
	t.Helper()
	s, err := NewSampler(src, emb, time.Minute)
	require.NoError(t, err)
	return s
}

func TestSampler_Verify(t *testing.T) {
	nan := float32(math.NaN())
	for name, tc := range map[string]struct {
		fresh func(i int) []float32 // chunk i's vector re-embedded, of 64 stored as unit(0)
		want  Comparison
	}{
		"all identical": {
			fresh: func(int) []float32 { return unit(0) },
			want:  Comparison{Sampled: 64, Identical: 64, MinCosine: 1},
		},
		"one of 64 moved past the bound": {
			fresh: func(i int) []float32 {
				if i == 17 {
					return at(0.9998)
				}
				return unit(0)
			},
			want: Comparison{Sampled: 64, Changed: 1, Identical: 63, MinCosine: float64(float32(0.9998))},
		},
		"noise within the bound": {
			fresh: func(int) []float32 { return at(0.99995) },
			want:  Comparison{Sampled: 64, MinCosine: float64(float32(0.99995))},
		},
		"another direction altogether": {
			fresh: func(int) []float32 { return unit(1) },
			want:  Comparison{Sampled: 64, Changed: 64, MinCosine: 0},
		},
		"twice as long": {
			fresh: func(int) []float32 { return scaled(unit(0), 2) },
			want:  Comparison{Sampled: 64, Changed: 64, MinCosine: 1},
		},
		"a zero vector": {
			fresh: func(i int) []float32 {
				if i == 0 {
					return make([]float32, 4)
				}
				return unit(0)
			},
			want: Comparison{Sampled: 64, Changed: 1, Identical: 63, MinCosine: 0},
		},
		"a NaN": {
			fresh: func(i int) []float32 {
				if i == 0 {
					return []float32{nan, 0, 0, 0}
				}
				return unit(0)
			},
			want: Comparison{Sampled: 64, Changed: 1, Identical: 63, MinCosine: 0},
		},
		"another width": {
			fresh: func(int) []float32 { return []float32{1, 0, 0} },
			want:  Comparison{Sampled: 64, Changed: 64, MinCosine: 0},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := newTestSampler(t, &chunkSource{sample: storedSample(64)}, answering(tc.fresh)).
				Verify(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.want.Sampled, got.Sampled)
			assert.Equal(t, tc.want.Changed, got.Changed)
			assert.Equal(t, tc.want.Identical, got.Identical)
			assert.InDelta(t, tc.want.MinCosine, got.MinCosine, 1e-6)
			assert.Equal(t, tc.want.Changed == 0, got.Same())
		})
	}

	t.Run("bit-identical NaN vectors are identical", func(t *testing.T) {
		sample := storedSample(2)
		sample[0].Embedding = []float32{nan, 0, 1, 0}
		got, err := newTestSampler(t, &chunkSource{sample: sample},
			answering(func(i int) []float32 { return slices.Clone(sample[i].Embedding) })).Verify(context.Background())
		require.NoError(t, err)
		assert.Equal(t, Comparison{Sampled: 2, Identical: 2, MinCosine: 1}, got)
	})
}

// TestSampler_SendsTheStoredTexts: the texts re-embedded are the stored
// ones, unchanged and in order; the indexer adds the document prefix.
func TestSampler_SendsTheStoredTexts(t *testing.T) {
	sample := storedSample(5)
	src := &chunkSource{sample: sample}
	emb := answering(func(int) []float32 { return unit(0) })

	_, err := newTestSampler(t, src, emb).Verify(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []store.ChunkSample{{Longest: LongestChunks, Random: RandomChunks}}, src.asked)
	require.Len(t, emb.sent, 1)
	assert.Equal(t, []string{"chunk 0", "chunk 1", "chunk 2", "chunk 3", "chunk 4"}, emb.sent[0])
}

// TestSampler_EmptyLibraryMatches: nothing to sample is a match, without
// an embed call.
func TestSampler_EmptyLibraryMatches(t *testing.T) {
	emb := answering(func(int) []float32 { return unit(0) })
	got, err := newTestSampler(t, &chunkSource{sample: []store.SampledChunk{}}, emb).Verify(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Comparison{MinCosine: 1}, got)
	assert.Empty(t, emb.sent)
}

// TestCompareVectors: identical vectors match whatever they hold; others
// match only within the cosine bound and the length guard, and have no
// angle, so cosine 0, when either is zero, not finite or of another width.
func TestCompareVectors(t *testing.T) {
	nan, inf := float32(math.NaN()), float32(math.Inf(1))
	for name, tc := range map[string]struct {
		stored, fresh []float32
		want          vectorMatch
	}{
		"identical":            {unit(0), unit(0), vectorMatch{identical: true, cosine: 1, matches: true}},
		"identical NaN":        {[]float32{nan, 1, 0, 0}, []float32{nan, 1, 0, 0}, vectorMatch{identical: true, cosine: 1, matches: true}},
		"identical zero":       {make([]float32, 4), make([]float32, 4), vectorMatch{identical: true, cosine: 1, matches: true}},
		"within the bound":     {unit(0), at(0.99995), vectorMatch{cosine: float64(float32(0.99995)), matches: true}},
		"past the bound":       {unit(0), at(0.9998), vectorMatch{cosine: float64(float32(0.9998))}},
		"opposite":             {unit(0), scaled(unit(0), -1), vectorMatch{cosine: -1}},
		"twice as long":        {unit(0), scaled(unit(0), 2), vectorMatch{cosine: 1}},
		"1% longer":            {unit(0), scaled(unit(0), 1.01), vectorMatch{cosine: 1, matches: true}},
		"2% shorter":           {unit(0), scaled(unit(0), 0.98), vectorMatch{cosine: 1}},
		"zero re-embedded":     {unit(0), make([]float32, 4), vectorMatch{}},
		"zero stored":          {make([]float32, 4), unit(0), vectorMatch{}},
		"NaN re-embedded":      {unit(0), []float32{nan, 0, 0, 0}, vectorMatch{}},
		"NaN stored":           {[]float32{1, nan, 0, 0}, unit(0), vectorMatch{}},
		"infinite re-embedded": {unit(0), []float32{inf, 0, 0, 0}, vectorMatch{}},
		"another width":        {unit(0), []float32{1, 0, 0}, vectorMatch{}},
	} {
		t.Run(name, func(t *testing.T) {
			got := compareVectors(tc.stored, tc.fresh)
			assert.Equal(t, tc.want.identical, got.identical, "identical")
			assert.Equal(t, tc.want.matches, got.matches, "matches")
			assert.InDelta(t, tc.want.cosine, got.cosine, 1e-6, "cosine")
			assert.False(t, math.IsNaN(got.cosine) || math.IsInf(got.cosine, 0))
		})
	}
}

// TestSampler_Errors: a failure the same build repeats is ErrUnverifiable,
// its cause kept; any other is returned as it is, to be retried.
func TestSampler_Errors(t *testing.T) {
	status := func(code int) error {
		return fmt.Errorf("ollama embed: %w", &ollama.StatusError{Code: code, Body: `{"error":"no"}`})
	}
	for name, tc := range map[string]struct {
		err          error
		unverifiable bool
	}{
		"wrong width": {fmt.Errorf("ollama embed: %w: embedding[0] has dim 768, expected 1024",
			embedder.ErrWrongDimension), true},
		"too long": {fmt.Errorf("ollama embed: %w: HTTP 400: the input length exceeds the context length",
			embedder.ErrInputTooLong), true},
		"bad request":       {status(400), true},
		"unprocessable":     {status(422), true},
		"model not pulled":  {fmt.Errorf("%w: %w", ollama.ErrModelNotLoaded, status(404)), false},
		"request timeout":   {status(408), false},
		"rate limited":      {status(429), false},
		"server error":      {status(500), false},
		"unreachable":       {fmt.Errorf("%w: connection refused", ollama.ErrUnreachable), false},
		"deadline":          {fmt.Errorf("post /api/embed: %w", context.DeadlineExceeded), false},
		"cancelled":         {context.Canceled, false},
		"something unknown": {errors.New("ollama embed: requested 32 embeddings, got 31"), false},
	} {
		t.Run(name, func(t *testing.T) {
			emb := &reembedder{embed: func(context.Context, []string) ([][]float32, error) { return nil, tc.err }}
			_, err := newTestSampler(t, &chunkSource{sample: storedSample(3)}, emb).Verify(context.Background())
			require.ErrorIs(t, err, tc.err, "the cause stays in the chain")
			assert.Equal(t, tc.unverifiable, errors.Is(err, ErrUnverifiable))
		})
	}

	t.Run("a vector short", func(t *testing.T) {
		emb := &reembedder{embed: func(context.Context, []string) ([][]float32, error) {
			return [][]float32{unit(0)}, nil
		}}
		_, err := newTestSampler(t, &chunkSource{sample: storedSample(3)}, emb).Verify(context.Background())
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrUnverifiable)
	})

	t.Run("the store", func(t *testing.T) {
		broken := errors.New("database is locked")
		emb := answering(func(int) []float32 { return unit(0) })
		_, err := newTestSampler(t, &chunkSource{err: broken}, emb).Verify(context.Background())
		require.ErrorIs(t, err, broken)
		assert.NotErrorIs(t, err, ErrUnverifiable)
		assert.Empty(t, emb.sent)
	})
}

// TestSampler_BoundedByItsBatches: a hung embedder gives up within the
// sample's bound, as a deadline to retry after, not as unverifiable.
func TestSampler_BoundedByItsBatches(t *testing.T) {
	emb := &reembedder{embed: func(ctx context.Context, _ []string) ([][]float32, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	s, err := NewSampler(&chunkSource{sample: storedSample(64)}, emb, 10*time.Millisecond)
	require.NoError(t, err)

	start := time.Now()
	_, err = s.Verify(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotErrorIs(t, err, ErrUnverifiable)
	assert.Less(t, time.Since(start), 5*time.Second)
}

// TestSampleBound: each of the sample's batches may take one request's
// timeout: two batches of 32, two minutes at the default 60 seconds.
func TestSampleBound(t *testing.T) {
	assert.Equal(t, 2*time.Minute, sampleBound(time.Minute))
	_, err := NewSampler(&chunkSource{}, answering(nil), 0)
	require.Error(t, err)
}

// buildEmbedder is an embedder.Embedder whose vectors depend on the text
// and on the build it plays: the same build embeds a text bit for bit the
// same, another build differently.
type buildEmbedder struct {
	dim   int
	build string
}

func (e *buildEmbedder) Dimensions() int { return e.dim }
func (*buildEmbedder) Model() string     { return "fake" }
func (e *buildEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		sum := sha256.Sum256([]byte(e.build + "\x00" + text))
		r := rand.New(rand.NewPCG(binary.LittleEndian.Uint64(sum[:8]), binary.LittleEndian.Uint64(sum[8:16])))
		v := make([]float32, e.dim)
		for j := range v {
			v[j] = float32(r.NormFloat64())
		}
		out[i] = v
	}
	return out, nil
}

// TestSampler_OverTheIndexedLibrary: over a library the indexer wrote,
// the build that indexed it re-embeds every sampled chunk bit for bit, and
// another build changes every one.
func TestSampler_OverTheIndexedLibrary(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.NewDB(t)
	dim := sqlitetest.Width(t, db)
	chunks := sqlitestore.NewChunks(db, dim)
	docs := sqlitestore.NewDocuments(db)
	exts := sqlitestore.NewExtractions(db)
	emb := &buildEmbedder{dim: dim, build: "0.34.4"}
	idx := indexer.New(chunks, emb, indexer.Options{ChunkSize: 8, DocumentPrefix: "doc: "})
	for i := range 3 {
		d := &store.Document{TenantID: store.LocalTenantID, URL: fmt.Sprintf("https://example.com/%d", i),
			ContentType: store.ContentTypeArticle}
		require.NoError(t, docs.Create(ctx, d))
		e := &store.DocumentExtraction{DocumentID: d.ID, Fetcher: "test", Status: store.ExtractionStatusOK,
			FetchedAt: time.Now().UTC()}
		require.NoError(t, exts.Create(ctx, e))
		require.NoError(t, idx.Index(ctx, indexer.IndexInput{DocumentID: d.ID, ExtractionID: e.ID,
			Markdown: fmt.Sprintf("Document %d opens here.\n\nIt goes on for a while.\n\nAnd ends.", i)}))
		require.NoError(t, docs.MarkFetched(ctx, d.ID))
	}
	sampler, err := NewSampler(chunks, idx, time.Minute)
	require.NoError(t, err)

	same, err := sampler.Verify(ctx)
	require.NoError(t, err)
	assert.Equal(t, Comparison{Sampled: 3, Identical: 3, MinCosine: 1}, same)

	emb.build = "0.35.0"
	changed, err := sampler.Verify(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, changed.Sampled)
	assert.Equal(t, 3, changed.Changed)
	assert.Zero(t, changed.Identical)
	assert.Less(t, changed.MinCosine, MinCosine)
}
