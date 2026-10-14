package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/store"
)

// Indexer orchestrates the chunk → embed → write step for one extraction,
// and embeds the documents' stale cards.
//
// It does NOT fetch, write markdown to disk, or update document state —
// those are the fetch-job handler's responsibilities. The indexer's job is
// strictly: given an extraction's markdown, replace the document's chunks
// in BM25 and vector indices, idempotent thanks to
// ChunkStore.ReplaceForDocument; and given the cards the store made stale,
// store their vectors.
type Indexer struct {
	chunks    store.ChunkStore
	embedder  embedder.Embedder
	opts      ChunkOptions
	docPrefix string
	batchSize int
	log       *slog.Logger
}

// Options for constructing an Indexer.
type Options struct {
	ChunkSize    int
	ChunkOverlap int
	// DocumentPrefix is prepended to each chunk's input before embedding
	// (not stored), for models trained with a document instruction; the
	// default, Qwen3-Embedding, takes none. It is one half of a scheme with
	// the search engine's query prefix.
	DocumentPrefix string
	// EmbedBatchSize caps how many chunks go into one embed request.
	// Default EmbedBatchSize.
	EmbedBatchSize int
	// Log is where EmbedStaleCards says how far a pass got. nil logs
	// nothing.
	Log *slog.Logger
}

// EmbedBatchSize bounds one embed request to at most 8 inputs of at most
// about 4 KB (a chunk at the 3,500-byte cap and its context), so a
// request's work doesn't grow with the document: the embedder's timeout
// then measures Ollama's health, not document length. Ollama serves one
// embed request at a time and embeds a batch's inputs one after another,
// so the size sets how much index work a search's query embedding waits
// behind, not how fast the library is embedded: with 4 index workers
// re-embedding the owner's library, query embeds waited 6.0 s at the 95th
// percentile in batches of 32, past search's 10 s timeout at worst, and
// 3.35 s in batches of 8, at the same 11.2 chunks a second
// (decisions.md "Indexer: embed in batches of 32").
const EmbedBatchSize = 8

func New(chunks store.ChunkStore, emb embedder.Embedder, opts Options) *Indexer {
	co := ChunkOptions{
		SizeTokens:    opts.ChunkSize,
		OverlapTokens: opts.ChunkOverlap,
	}
	batch := opts.EmbedBatchSize
	if batch <= 0 {
		batch = EmbedBatchSize
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Indexer{chunks: chunks, embedder: emb, opts: co, docPrefix: opts.DocumentPrefix, batchSize: batch,
		log: log}
}

// IndexInput is everything Index needs to do its work.
type IndexInput struct {
	DocumentID   string
	ExtractionID string
	Tags         []string // denormalized into chunks_fts
	Markdown     string
	// Document is what each chunk's context is built from (Context). Its
	// Title, the document's own, is also denormalized into chunks_fts for
	// boostable title search.
	Document DocumentContext
	// Reembed marks a re-embed's index, written only while the document
	// is fetched (store.IndexGuard).
	Reembed bool
}

// Index chunks, embeds, and writes, and returns how many chunks it wrote.
// Each chunk is embedded with its context (Context), stored with it and
// with ContextVersion; its text, which BM25 and snippets read, stays its
// own. A chunk the embedding model can't take with its context is embedded
// without it (embedForIndex), and stored with none, so that a model with a
// small context indexes every document it indexed before chunks had one.
// Replaces any previous chunks for the document atomically, while
// ExtractionID is still its current extraction (ChunkStore.ReplaceIndexed:
// store.ErrSuperseded otherwise, nothing written). Empty markdown is
// valid — produces zero chunks, which clears any prior index for the
// document.
func (i *Indexer) Index(ctx context.Context, in IndexInput) (int, error) {
	if in.DocumentID == "" {
		return 0, errors.New("indexer: DocumentID required")
	}
	if in.ExtractionID == "" {
		return 0, errors.New("indexer: ExtractionID required")
	}

	chunks := ChunkText(in.Markdown, i.opts)
	embeds := make([]EmbedInput, len(chunks))
	for j, c := range chunks {
		embeds[j] = EmbedInput{Context: Context(in.Document, c.Headings), Text: c.Text}
	}
	vectors, bare, err := i.embedForIndex(ctx, embeds)
	if err != nil {
		return 0, err
	}
	if bare > 0 {
		i.log.Warn("index: the embedding model can't take some of a document's chunks with their context; "+
			"they are embedded without it", "document_id", in.DocumentID, "chunks", bare, "of", len(chunks))
	}

	inputs := make([]store.ChunkInput, len(chunks))
	for j, c := range chunks {
		inputs[j] = store.ChunkInput{
			Text:           c.Text,
			TokenCount:     c.TokenCount,
			Context:        embeds[j].Context,
			ContextVersion: ContextVersion,
			Embedding:      vectors[j],
		}
	}
	// No chunks replace the document's with none.
	guard := store.IndexGuard{ExtractionID: in.ExtractionID, Reembed: in.Reembed}
	if err := i.chunks.ReplaceIndexed(ctx, in.DocumentID, guard, in.Document.Title, in.Tags, inputs); err != nil {
		return 0, err
	}
	return len(inputs), nil
}

// embedForIndex embeds a document's chunks as EmbedChunks does, batch by
// batch. A batch the model refuses as too long (embedder.ErrInputTooLong,
// which fails a whole batch) is embedded again a chunk at a time, and a
// chunk refused alone with its context is embedded without it: its
// context is cleared in inputs, so that what is stored is what its vector
// was made from. bare counts those. Only a chunk refused without its
// context fails the document, as it did before chunks had one.
func (i *Indexer) embedForIndex(ctx context.Context, inputs []EmbedInput) (vectors [][]float32, bare int,
	err error) {
	vectors = make([][]float32, 0, len(inputs))
	texts := make([]string, len(inputs))
	for j, in := range inputs {
		texts[j] = i.embedText(in)
	}
	for start := 0; start < len(inputs); start += i.batchSize {
		end := min(start+i.batchSize, len(inputs))
		batch, err := i.embedBatch(ctx, texts, start, end)
		if errors.Is(err, embedder.ErrInputTooLong) {
			var n int
			batch, n, err = i.embedChunksEach(ctx, inputs, start, end)
			bare += n
		}
		if err != nil {
			return nil, 0, err
		}
		vectors = append(vectors, batch...)
	}
	return vectors, bare, nil
}

// embedChunksEach embeds inputs[start:end] one a request, after the model
// refused them together as too long: each with its context, and one
// refused that way without it, its context cleared. bare counts those.
func (i *Indexer) embedChunksEach(ctx context.Context, inputs []EmbedInput, start, end int) (vectors [][]float32,
	bare int, err error) {
	for j := start; j < end; j++ {
		v, err := i.embedChunk(ctx, inputs, j)
		if errors.Is(err, embedder.ErrInputTooLong) && inputs[j].Context != "" {
			inputs[j].Context = ""
			if v, err = i.embedChunk(ctx, inputs, j); err == nil {
				bare++
			}
		}
		if err != nil {
			return nil, 0, err
		}
		vectors = append(vectors, v)
	}
	return vectors, bare, nil
}

// embedChunk embeds inputs[j] alone, after checking ctx. A failure names
// the chunk and the size of what was sent.
func (i *Indexer) embedChunk(ctx context.Context, inputs []EmbedInput, j int) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("indexer: stopped before chunk %d of %d: %w", j, len(inputs), err)
	}
	text := i.embedText(inputs[j])
	v, err := i.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("indexer: embed chunk %d of %d (%d bytes): %w", j, len(inputs), len(text), err)
	}
	if len(v) != 1 {
		return nil, fmt.Errorf("indexer: embedder returned %d vectors for chunk %d of %d", len(v), j, len(inputs))
	}
	return v[0], nil
}

// EmbedInput is one input to embed as a chunk: its text, and the context
// it is embedded with, "" for none (a card).
type EmbedInput struct {
	Context string
	Text    string
}

// embedText is what the embedder is sent for in: the document prefix
// (models with a document instruction need it; nothing stored holds it, so
// BM25 and snippets never see it), then the context and a blank line when
// there is one, then the text. Every embed of the indexer's input is
// built here alone.
func (i *Indexer) embedText(in EmbedInput) string {
	if in.Context == "" {
		return i.docPrefix + in.Text
	}
	return i.docPrefix + in.Context + "\n\n" + in.Text
}

// EmbedChunks embeds inputs as indexing does, and is the path every index
// embed request takes: each input as embedText builds it, in consecutive
// batches of at most the batch size, in order. The drift check re-embeds
// stored chunks, their stored context included, through it, so what it
// compares was sent exactly as the stored vectors were.
func (i *Indexer) EmbedChunks(ctx context.Context, inputs []EmbedInput) ([][]float32, error) {
	texts := make([]string, len(inputs))
	for j, in := range inputs {
		texts[j] = i.embedText(in)
	}
	return i.embed(ctx, texts)
}

// EmbedStaleCards embeds the tenant's stale cards (store.DocumentIndex),
// in one pass by document: a batch of at most the batch size at a time,
// each card embedded as a chunk with no context (EmbedChunks, the document
// prefix applied) and the batch's vectors stored in one transaction
// (PutCardVectors), which keeps a vector only for a card that still holds
// the text embedded. A card that changes behind the pass is left to the
// cards job its write queued. ctx is checked before every batch; the
// batches stored before a failure stay stored. embedded counts the vectors
// stored. A long pass says how far it got (cardsProgress), against the
// stale cards counted as it began.
//
// One card the model can't take (embedder.ErrInputTooLong, which fails its
// whole batch) mustn't hold back the cards behind it, in every later pass
// too: that batch is embedded again a card at a time (embedEach), and each
// card refused alone is marked unembeddable (MarkUnembeddable), with a
// WARN, then the pass goes on. A vector of the wrong width
// (embedder.ErrWrongDimension) is the model's, not a card's, and ends the
// pass like any other error.
func (i *Indexer) EmbedStaleCards(ctx context.Context, cards store.DocumentIndex, tenantID string) (int, error) {
	of, err := cards.CountStaleCards(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("indexer: count stale cards: %w", err)
	}
	progress := cardsProgress{log: i.log, of: of, began: time.Now()}
	embedded := 0
	for after := ""; ; {
		if err := ctx.Err(); err != nil {
			return embedded, fmt.Errorf("indexer: stopped after %d cards: %w", embedded, err)
		}
		batch, err := cards.StaleCards(ctx, tenantID, after, i.batchSize)
		if err != nil {
			return embedded, fmt.Errorf("indexer: read stale cards: %w", err)
		}
		if len(batch) == 0 {
			return embedded, nil
		}
		n, err := i.embedCards(ctx, cards, batch)
		embedded += n
		if err != nil {
			return embedded, err
		}
		progress.stored(embedded)
		after = batch[len(batch)-1].DocumentID
	}
}

// embedCards embeds batch in one request and stores its vectors, or, when
// the request is refused as too long, a card at a time (embedEach). stored
// counts the vectors stored.
func (i *Indexer) embedCards(ctx context.Context, cards store.DocumentIndex, batch []store.Card) (stored int,
	err error) {
	inputs := make([]EmbedInput, len(batch))
	for j, c := range batch {
		inputs[j] = EmbedInput{Text: c.Text}
	}
	vectors, err := i.EmbedChunks(ctx, inputs)
	switch {
	case errors.Is(err, embedder.ErrInputTooLong):
		return i.embedEach(ctx, cards, batch)
	case err != nil:
		return 0, fmt.Errorf("indexer: embed cards: %w", err)
	}
	put := make([]store.CardVector, len(batch))
	for j, c := range batch {
		put[j] = store.CardVector{DocumentID: c.DocumentID, Text: c.Text, Embedding: vectors[j]}
	}
	if stored, err = cards.PutCardVectors(ctx, put); err != nil {
		return 0, fmt.Errorf("indexer: store card vectors: %w", err)
	}
	return stored, nil
}

// embedEach embeds batch's cards one a request, after the model refused
// them together as too long: it stores the vectors of those it takes, and
// marks unembeddable those it refuses alone, each with a WARN. Any other
// error ends it, storing nothing of the batch, as a batch's error does.
// stored counts the vectors stored.
func (i *Indexer) embedEach(ctx context.Context, cards store.DocumentIndex, batch []store.Card) (stored int,
	err error) {
	put := make([]store.CardVector, 0, len(batch))
	var refused []store.Card
	for _, c := range batch {
		vectors, err := i.EmbedChunks(ctx, []EmbedInput{{Text: c.Text}})
		switch {
		case errors.Is(err, embedder.ErrInputTooLong):
			i.log.Warn("cards: the embedding model can't take this card, which isn't searched by meaning until "+
				"its text changes", "document_id", c.DocumentID, "bytes", len(c.Text), "err", err)
			refused = append(refused, c)
			continue
		case err != nil:
			return 0, fmt.Errorf("indexer: embed the card of document %s: %w", c.DocumentID, err)
		}
		put = append(put, store.CardVector{DocumentID: c.DocumentID, Text: c.Text, Embedding: vectors[0]})
	}
	if stored, err = cards.PutCardVectors(ctx, put); err != nil {
		return 0, fmt.Errorf("indexer: store card vectors: %w", err)
	}
	if _, err := cards.MarkUnembeddable(ctx, refused); err != nil {
		return stored, fmt.Errorf("indexer: mark the cards the model can't take: %w", err)
	}
	return stored, nil
}

// cardsProgressEvery is how many cards stored apart a pass of
// EmbedStaleCards says how far it got: about 7 s of embedding at the
// owner's measured rate (7,557 cards in 108 s), so a fill of thousands
// logs a line every few seconds and a job of a few cards none.
const cardsProgressEvery = 500

// cardsProgress says how far a pass of EmbedStaleCards got: one INFO
// "cards embedding" each time the cards it stored pass a multiple of
// cardsProgressEvery, with the stale cards it began with (of) and how long
// it has run.
type cardsProgress struct {
	log   *slog.Logger
	of    int
	began time.Time
	// logged is the multiple of cardsProgressEvery last logged.
	logged int
}

// stored notes that the pass has stored embedded cards.
func (p *cardsProgress) stored(embedded int) {
	if embedded/cardsProgressEvery <= p.logged {
		return
	}
	p.logged = embedded / cardsProgressEvery
	p.log.Info("cards embedding", "embedded", embedded, "of", p.of, "took_ms", time.Since(p.began).Milliseconds())
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
		batch, err := i.embedBatch(ctx, texts, start, min(start+i.batchSize, len(texts)))
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, batch...)
	}
	return vectors, nil
}

// embedBatch embeds texts[start:end] in one request, after checking ctx.
func (i *Indexer) embedBatch(ctx context.Context, texts []string, start, end int) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("indexer: stopped before chunk %d of %d: %w", start, len(texts), err)
	}
	batch, err := i.embedder.Embed(ctx, texts[start:end])
	if err != nil {
		return nil, fmt.Errorf("indexer: embed chunks %d-%d of %d (longest chunk %d bytes): %w",
			start, end-1, len(texts), longest(texts[start:end]), err)
	}
	if len(batch) != end-start {
		return nil, fmt.Errorf("indexer: embedder returned %d vectors for chunks %d-%d of %d",
			len(batch), start, end-1, len(texts))
	}
	return batch, nil
}

// longest is the length in bytes of the longest of texts.
func longest(texts []string) int {
	n := 0
	for _, t := range texts {
		n = max(n, len(t))
	}
	return n
}
