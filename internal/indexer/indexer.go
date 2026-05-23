package indexer

import (
	"context"
	"errors"
	"fmt"

	"github.com/samansartipi/curio/internal/embedder"
	"github.com/samansartipi/curio/internal/store"
)

// Indexer orchestrates the chunk → embed → write step for one extraction.
//
// It does NOT fetch, write markdown to disk, or update document state —
// those are the fetch-job handler's responsibilities. The indexer's job is
// strictly: given an extraction's markdown, replace the document's chunks
// in BM25 and vector indices. Idempotent thanks to
// ChunkStore.ReplaceForDocument.
type Indexer struct {
	chunks   store.ChunkStore
	embedder embedder.Embedder
	opts     ChunkOptions
}

// Options for constructing an Indexer.
type Options struct {
	ChunkSize    int
	ChunkOverlap int
}

func New(chunks store.ChunkStore, emb embedder.Embedder, opts Options) *Indexer {
	co := ChunkOptions{
		SizeTokens:    opts.ChunkSize,
		OverlapTokens: opts.ChunkOverlap,
	}
	return &Indexer{chunks: chunks, embedder: emb, opts: co}
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

	vectors, err := i.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("indexer: embed: %w", err)
	}
	if len(vectors) != len(chunks) {
		return fmt.Errorf("indexer: embedder returned %d vectors for %d chunks", len(vectors), len(chunks))
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
