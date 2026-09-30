package drift

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/store"
)

// The sample a verification re-embeds: one chunk from each of 64
// documents. The random chunks catch a change that touches some inputs
// (one that touches 1 chunk in 10 slips past all 48 with probability
// 0.9^48, 0.6%) and any change that touches every input; the longest
// chunks cover the long-sequence path, where a change to attention or
// batching shows first. decisions.md "Embedding drift: verified by
// re-embedding a sample" has the measurements.
const (
	LongestChunks = 16
	RandomChunks  = 48
)

// MinCosine is how close a re-embedded chunk's vector must stay to its
// stored one to count as the same. Between unit vectors it bounds how far
// a score can move to sqrt(2(1-MinCosine)), 0.014 of the distance search
// ranks by; the same build re-embeds bit for bit.
const MinCosine = 0.9999

// maxLengthShift is how far a re-embedded vector's length may stray from
// its stored one's, as a fraction of it: the score shift MinCosine allows.
// Search ranks by L2 distance, which orders like cosine only between unit
// vectors, and a cosine can't see a change of length.
var maxLengthShift = math.Sqrt(2 * (1 - MinCosine))

// ErrUnverifiable is a verification that failed in a way the same build
// repeats on every attempt: the sample can't be re-embedded at all (a
// reply of the wrong width, an input the model refuses as too long, a
// request Ollama rejects), so there is nothing to compare.
var ErrUnverifiable = errors.New("the build serving now can't re-embed the sample")

// Verifier re-embeds a sample of the library and compares it with the
// stored vectors.
type Verifier interface {
	Verify(ctx context.Context) (Comparison, error)
}

// Comparison is what re-embedding a sample found. MinCosine is the worst
// chunk's cosine with its stored vector, 1 when every chunk is identical
// or there were none, and never NaN or Inf.
type Comparison struct {
	Sampled   int // chunks re-embedded
	Changed   int // chunks that don't match their stored vector
	Identical int // chunks whose vector came back bit for bit
	MinCosine float64
}

// Same reports whether every sampled chunk matched its stored vector.
func (c Comparison) Same() bool { return c.Changed == 0 }

// ChunkSampler reads a sample of the library's chunks with their stored
// vectors; store.ChunkStore is one.
type ChunkSampler interface {
	SampleChunks(ctx context.Context, tenantID string, n store.ChunkSample) ([]store.SampledChunk, error)
}

// ChunkEmbedder embeds stored chunk texts exactly as indexing did;
// *indexer.Indexer is one.
type ChunkEmbedder interface {
	EmbedChunks(ctx context.Context, texts []string) ([][]float32, error)
}

// Sampler is the daemon's Verifier: it re-embeds a sample of the library's
// chunks through the indexer's own embed path.
type Sampler struct {
	chunks ChunkSampler
	embed  ChunkEmbedder
	bound  time.Duration
}

// NewSampler returns a Sampler over chunks and embed. requestTimeout is
// what one embed request may take (embedding.timeout_seconds); the whole
// verification may take that for each batch the sample needs.
func NewSampler(chunks ChunkSampler, embed ChunkEmbedder, requestTimeout time.Duration) (*Sampler, error) {
	if requestTimeout <= 0 {
		return nil, fmt.Errorf("drift sampler: request timeout must be positive, got %s", requestTimeout)
	}
	return &Sampler{chunks: chunks, embed: embed, bound: sampleBound(requestTimeout)}, nil
}

// sampleBound is the time a verification may take: each of the sample's
// batches as long as one embed request may. Each request already waits in
// Ollama behind the index workers' batches within that timeout, so a
// shorter bound would fail samples a slow machine's config allows.
func sampleBound(requestTimeout time.Duration) time.Duration {
	const size = LongestChunks + RandomChunks
	batches := (size + indexer.EmbedBatchSize - 1) / indexer.EmbedBatchSize
	return time.Duration(batches) * requestTimeout
}

// Verify re-embeds a sample of the library's chunks and compares each
// vector with its stored one. An empty library is an empty sample, which
// matches.
func (s *Sampler) Verify(ctx context.Context) (Comparison, error) {
	ctx, cancel := context.WithTimeout(ctx, s.bound)
	defer cancel()
	sample, err := s.chunks.SampleChunks(ctx, store.LocalTenantID,
		store.ChunkSample{Longest: LongestChunks, Random: RandomChunks})
	if err != nil {
		return Comparison{}, fmt.Errorf("sample the library's chunks: %w", err)
	}
	if len(sample) == 0 {
		return Comparison{MinCosine: 1}, nil
	}
	texts := make([]string, len(sample))
	for i, c := range sample {
		texts[i] = c.Text
	}
	vectors, err := s.embed.EmbedChunks(ctx, texts)
	if err != nil {
		err = fmt.Errorf("re-embed %d sampled chunks: %w", len(sample), err)
		if unverifiable(err) {
			return Comparison{}, fmt.Errorf("%w: %w", ErrUnverifiable, err)
		}
		return Comparison{}, err
	}
	if len(vectors) != len(sample) {
		return Comparison{}, fmt.Errorf("re-embed %d sampled chunks: got %d vectors", len(sample), len(vectors))
	}
	cmp := Comparison{Sampled: len(sample), MinCosine: 1}
	for i, c := range sample {
		v := compareVectors(c.Embedding, vectors[i])
		if v.identical {
			cmp.Identical++
		}
		if !v.matches {
			cmp.Changed++
		}
		cmp.MinCosine = min(cmp.MinCosine, v.cosine)
	}
	return cmp, nil
}

// unverifiable reports whether err is a failure the same build repeats on
// every attempt: a reply of the wrong width, an input too long for the
// model, or a request Ollama refuses (a 4xx other than a model not pulled,
// a timeout or a rate limit, which pass).
func unverifiable(err error) bool {
	if errors.Is(err, embedder.ErrWrongDimension) || errors.Is(err, embedder.ErrInputTooLong) {
		return true
	}
	var status *ollama.StatusError
	if !errors.As(err, &status) {
		return false
	}
	switch status.Code {
	case http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return status.Code >= 400 && status.Code < 500
}

// vectorMatch is how a re-embedded chunk's vector compares with its
// stored one.
type vectorMatch struct {
	identical bool    // bit for bit
	cosine    float64 // 1 when identical; 0 when there is no angle to measure
	matches   bool
}

// compareVectors holds fresh, a chunk's vector re-embedded now, to stored.
// Identical vectors match, whatever they hold. Others match when their
// cosine is at least MinCosine and their lengths differ by at most
// maxLengthShift of the stored one's. Vectors of different widths, a zero
// vector and a component that isn't finite have no angle: cosine 0, no
// match.
func compareVectors(stored, fresh []float32) vectorMatch {
	if identical(stored, fresh) {
		return vectorMatch{identical: true, cosine: 1, matches: true}
	}
	if len(stored) != len(fresh) {
		return vectorMatch{}
	}
	var dot, ss, ff float64
	for i := range stored {
		s, f := float64(stored[i]), float64(fresh[i])
		dot += s * f
		ss += s * s
		ff += f * f
	}
	// Squares of float32 components can't overflow a float64 sum, so a
	// sum that isn't finite has a component that isn't.
	if ss == 0 || ff == 0 || !finite(dot) || !finite(ss) || !finite(ff) {
		return vectorMatch{}
	}
	storedLen, freshLen := math.Sqrt(ss), math.Sqrt(ff)
	cosine := max(-1, min(1, dot/(storedLen*freshLen)))
	sameLength := math.Abs(freshLen-storedLen) <= maxLengthShift*storedLen
	return vectorMatch{cosine: cosine, matches: cosine >= MinCosine && sameLength}
}

// identical reports whether a and b hold the same float32s bit for bit.
func identical(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Float32bits(a[i]) != math.Float32bits(b[i]) {
			return false
		}
	}
	return true
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
