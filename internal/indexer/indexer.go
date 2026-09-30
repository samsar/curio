package indexer

import (
	"context"
	"errors"
	"fmt"

	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/store"
)

// Indexer orchestrates the chunk → embed → write step for one extraction.
//
// It does NOT fetch, write markdown to disk, or update document state —
// those are the fetch-job handler's responsibilities. The indexer's job is
// strictly: given an extraction's markdown, replace the document's chunks
// in BM25 and vector indices. Idempotent thanks to
// ChunkStore.ReplaceForDocument.
type Indexer struct {
	chunks    store.ChunkStore
	embedder  embedder.Embedder
	opts      ChunkOptions
	docPrefix string
	batchSize int
}

// Options for constructing an Indexer.
type Options struct {
	ChunkSize    int
	ChunkOverlap int
	// DocumentPrefix is prepended to each chunk before embedding (not
	// stored), for models trained with a document instruction; the default,
	// Qwen3-Embedding, takes none. It is one half of a scheme with the
	// search engine's query prefix.
	DocumentPrefix string
	// EmbedBatchSize caps how many chunks go into one embed request.
	// Default EmbedBatchSize.
	EmbedBatchSize int
}

// EmbedBatchSize bounds one embed request to at most 32 chunks of at most
// 3500 bytes (~112 KB), so a request's work doesn't grow with the document:
// the embedder's timeout then measures Ollama's health, not document
// length, and a search's query embedding waits behind a bounded amount of
// index work in Ollama. How long a batch takes depends on the model and
// the machine; decisions.md "Indexer: embed in batches of 32" has what was
// measured, and what wasn't.
const EmbedBatchSize = 32

func New(chunks store.ChunkStore, emb embedder.Embedder, opts Options) *Indexer {
	co := ChunkOptions{
		SizeTokens:    opts.ChunkSize,
		OverlapTokens: opts.ChunkOverlap,
	}
	batch := opts.EmbedBatchSize
	if batch <= 0 {
		batch = EmbedBatchSize
	}
	return &Indexer{chunks: chunks, embedder: emb, opts: co, docPrefix: opts.DocumentPrefix, batchSize: batch}
}

// IndexInput is everything Index needs to do its work.
type IndexInput struct {
	DocumentID   string
	ExtractionID string
	Title        string   // denormalized into chunks_fts for boostable title search
	Tags         []string // denormalized into chunks_fts
	Markdown     string
}

// Index chunks, embeds, and writes. Replaces any previous chunks for the
// document atomically. Empty markdown is valid — produces zero chunks,
// which clears any prior index for the document.
func (i *Indexer) Index(ctx context.Context, in IndexInput) error {
	if in.DocumentID == "" {
		return errors.New("indexer: DocumentID required")
	}
	if in.ExtractionID == "" {
		return errors.New("indexer: ExtractionID required")
	}

	chunks := ChunkText(in.Markdown, i.opts)
	if len(chunks) == 0 {
		// Replace with empty set — clears any previous chunks for the doc.
		return i.chunks.ReplaceForDocument(ctx, in.DocumentID, in.ExtractionID, in.Title, in.Tags, nil)
	}

	texts := make([]string, len(chunks))
	for j, c := range chunks {
		texts[j] = c.Text
	}
	vectors, err := i.EmbedChunks(ctx, texts)
	if err != nil {
		return err
	}

	inputs := make([]store.ChunkInput, len(chunks))
	for j, c := range chunks {
		inputs[j] = store.ChunkInput{
			Text:       c.Text,
			TokenCount: c.TokenCount,
			Embedding:  vectors[j],
		}
	}
	return i.chunks.ReplaceForDocument(ctx, in.DocumentID, in.ExtractionID, in.Title, in.Tags, inputs)
}

// EmbedChunks embeds chunk texts as indexing does, and is the path every
// index embed request takes: each text prefixed with the document prefix
// (models with a document instruction need it; the stored text stays raw,
// so BM25 and snippets never see it), in consecutive batches of at most
// the batch size, in order. The drift check re-embeds stored chunks
// through it, so what it compares was sent exactly as the stored vectors
// were.
func (i *Indexer) EmbedChunks(ctx context.Context, texts []string) ([][]float32, error) {
	prefixed := make([]string, len(texts))
	for j, t := range texts {
		prefixed[j] = i.docPrefix + t
	}
	return i.embed(ctx, prefixed)
}

// embed embeds texts in consecutive batches of at most batchSize, preserving
// order. Nothing is written until every batch has succeeded, so a failure
// leaves the document's previous chunks searchable; the job-level retry then
// redoes the whole document. A failure names the batch's chunks and the
// size of the longest text sent, which is the one to look at when the
// embedder refuses an input as too long (embedder.ErrInputTooLong fails the
// whole batch).
func (i *Indexer) embed(ctx context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += i.batchSize {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("indexer: stopped before chunk %d of %d: %w", start, len(texts), err)
		}
		end := min(start+i.batchSize, len(texts))
		batch, err := i.embedder.Embed(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("indexer: embed chunks %d-%d of %d (longest chunk %d bytes): %w",
				start, end-1, len(texts), longest(texts[start:end]), err)
		}
		if len(batch) != end-start {
			return nil, fmt.Errorf("indexer: embedder returned %d vectors for chunks %d-%d of %d",
				len(batch), start, end-1, len(texts))
		}
		vectors = append(vectors, batch...)
	}
	return vectors, nil
}

// longest is the length in bytes of the longest of texts.
func longest(texts []string) int {
	n := 0
	for _, t := range texts {
		n = max(n, len(t))
	}
	return n
}
